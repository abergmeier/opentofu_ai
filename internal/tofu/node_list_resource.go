// Copyright (c) The OpenTofu Authors
// SPDX-License-Identifier: MPL-2.0
// Copyright (c) 2023 HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package tofu

import (
	"context"
	"fmt"
	"log"

	"github.com/hashicorp/hcl/v2"
	"github.com/zclconf/go-cty/cty"

	"github.com/opentofu/opentofu/internal/addrs"
	"github.com/opentofu/opentofu/internal/lang"
	"github.com/opentofu/opentofu/internal/providers"
	"github.com/opentofu/opentofu/internal/states"
	"github.com/opentofu/opentofu/internal/tfdiags"
)

// defaultListLimit is how many results a list block asks the provider for when
// it has no "limit" argument.
const defaultListLimit = 100

// nodeExpandListResource represents a "list" block in a module that has not yet
// been expanded across the instances of that module.
type nodeExpandListResource struct {
	*NodeAbstractResource
}

var (
	_ GraphNodeModulePath           = (*nodeExpandListResource)(nil)
	_ GraphNodeReferenceable        = (*nodeExpandListResource)(nil)
	_ GraphNodeReferencer           = (*nodeExpandListResource)(nil)
	_ GraphNodeConfigResource       = (*nodeExpandListResource)(nil)
	_ GraphNodeAttachResourceConfig = (*nodeExpandListResource)(nil)
	_ GraphNodeAttachResourceSchema = (*nodeExpandListResource)(nil)
	_ GraphNodeProviderConsumer     = (*nodeExpandListResource)(nil)
	_ GraphNodeDynamicExpandable    = (*nodeExpandListResource)(nil)
	_ graphNodeTemporaryValue       = (*nodeExpandListResource)(nil)
)

// graphNodeTemporaryValue lets the pruning step drop list blocks that nothing refers to
// when applying, so they aren't queried a second time for no reason.
func (n *nodeExpandListResource) temporaryValue(_ walkOperation) bool {
	return true
}

func (n *nodeExpandListResource) Name() string {
	return n.Addr.String() + " (expand)"
}

// GraphNodeReferencer
func (n *nodeExpandListResource) References() []*addrs.Reference {
	refs := n.NodeAbstractResource.References()
	if c := n.Config; c != nil {
		limitRefs, _ := lang.ReferencesInExpr(addrs.ParseRef, c.ListLimit)
		refs = append(refs, limitRefs...)
		includeRefs, _ := lang.ReferencesInExpr(addrs.ParseRef, c.ListIncludeResource)
		refs = append(refs, includeRefs...)
	}
	return refs
}

// GraphNodeDynamicExpandable
func (n *nodeExpandListResource) DynamicExpand(evalCtx EvalContext) (*Graph, error) {
	var g Graph
	expander := evalCtx.InstanceExpander()
	for _, module := range expander.ExpandModule(n.Addr.Module) {
		expander.SetResourceSingle(module, n.Addr.Resource)

		inst := NewNodeAbstractResourceInstance(n.Addr.Resource.Absolute(module).Instance(addrs.NoKey))
		inst.Config = n.Config
		inst.ResolvedProvider = n.ResolvedProvider
		inst.Schema = n.Schema
		inst.SchemaVersion = n.SchemaVersion

		log.Printf("[TRACE] Expanding list resource: adding %s", inst.Addr)
		g.Add(&nodeListResourceInstance{NodeAbstractResourceInstance: inst})
	}
	addRootNodeToGraph(&g)
	return &g, nil
}

// nodeListResourceInstance queries a provider for the objects described by a
// "list" block in one module instance.
type nodeListResourceInstance struct {
	*NodeAbstractResourceInstance
}

var _ GraphNodeExecutable = (*nodeListResourceInstance)(nil)

func (n *nodeListResourceInstance) Execute(ctx context.Context, evalCtx EvalContext, op walkOperation) (diags tfdiags.Diagnostics) {
	if n.Config == nil {
		return diags
	}
	switch op {
	case walkValidate, walkPlan, walkApply:
	default:
		return diags
	}

	var provider providers.Interface
	var providerSchema providers.ProviderSchema
	if op == walkValidate {
		// Provider instance keys are ignored during validate, as for other resources.
		var err error
		provider, err = n.ResolvedProvider.Instance(addrs.NoKey)
		diags = diags.Append(err)
		if diags.HasErrors() {
			return diags
		}
		var schemaDiags tfdiags.Diagnostics
		providerSchema, schemaDiags = evalCtx.Providers().GetProviderSchema(ctx, n.ResolvedProvider.ProviderConfig.Provider)
		diags = diags.Append(schemaDiags)
		if diags.HasErrors() {
			return diags
		}
	} else {
		diags = diags.Append(n.resolveProvider(ctx, evalCtx, true, states.NotDeposed))
		if diags.HasErrors() {
			return diags
		}
		var err error
		provider, providerSchema, err = n.getProvider(ctx, evalCtx)
		if err != nil {
			return diags.Append(err)
		}
	}

	listSchema, ok := providerSchema.ListResources[n.Config.Type]
	if !ok {
		return diags.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Invalid list resource type",
			Detail:   fmt.Sprintf("The provider %s does not support list resource type %q.", n.Config.Provider.ForDisplay(), n.Config.Type),
			Subject:  &n.Config.TypeRange,
		})
	}
	// Results are objects of the managed resource type with the same name.
	resSchema, ok := providerSchema.ResourceTypes[n.Config.Type]
	if !ok {
		return diags.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Invalid list resource type",
			Detail:   fmt.Sprintf("The provider %s has no managed resource type %q to list.", n.Config.Provider.ForDisplay(), n.Config.Type),
			Subject:  &n.Config.TypeRange,
		})
	}

	configVal, _, evalDiags := evalCtx.EvaluateBlock(ctx, n.Config.Config, listSchema.Block, nil, EvalDataForNoInstanceKey)
	diags = diags.Append(evalDiags.InConfigBody(n.Config.Config, n.Addr.String()))
	if evalDiags.HasErrors() {
		return diags
	}

	includeVal := cty.False
	if n.Config.ListIncludeResource != nil {
		var includeDiags tfdiags.Diagnostics
		includeVal, includeDiags = evalCtx.EvaluateExpr(ctx, n.Config.ListIncludeResource, cty.Bool, nil)
		diags = diags.Append(includeDiags)
	}
	limitVal := cty.NumberIntVal(defaultListLimit)
	if n.Config.ListLimit != nil {
		var limitDiags tfdiags.Diagnostics
		limitVal, limitDiags = evalCtx.EvaluateExpr(ctx, n.Config.ListLimit, cty.Number, nil)
		diags = diags.Append(limitDiags)
	}
	if diags.HasErrors() {
		return diags
	}
	if includeVal.IsNull() || limitVal.IsNull() {
		return diags.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Invalid list block argument",
			Detail:   `The "limit" and "include_resource" arguments must not be null.`,
			Subject:  &n.Config.DeclRange,
		})
	}

	// Marks on the configuration carry over to the results so that, for
	// example, a sensitive filter doesn't leak through the returned objects.
	unmarkedConfig, configMarks := configVal.UnmarkDeep()
	unmarkedInclude, includeMarks := includeVal.UnmarkDeep()
	unmarkedLimit, limitMarks := limitVal.UnmarkDeep()

	validateResp := provider.ValidateListResourceConfig(ctx, providers.ValidateListResourceConfigRequest{
		TypeName:              n.Config.Type,
		Config:                unmarkedConfig,
		IncludeResourceObject: unmarkedInclude,
		Limit:                 unmarkedLimit,
	})
	diags = diags.Append(validateResp.Diagnostics.InConfigBody(n.Config.Config, n.Addr.String()))
	if diags.HasErrors() || op == walkValidate {
		return diags
	}

	absResource := n.Addr.ContainingResource()
	valueTy := listResultValueType(resSchema)

	if !unmarkedConfig.IsWhollyKnown() || !unmarkedInclude.IsKnown() || !unmarkedLimit.IsKnown() {
		if op == walkApply {
			return diags.Append(&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Unknown list block configuration",
				Detail:   fmt.Sprintf("The configuration of %s is not fully known, so the provider cannot be queried.", absResource),
				Subject:  &n.Config.DeclRange,
			})
		}
		// Not queryable yet; anything depending on the results is unknown too.
		evalCtx.ListResults().Set(absResource, cty.UnknownVal(valueTy))
		return diags
	}

	limit, _ := unmarkedLimit.AsBigFloat().Int64()
	resp := provider.ListResource(ctx, providers.ListResourceRequest{
		TypeName:              n.Config.Type,
		Config:                unmarkedConfig,
		IncludeResourceObject: unmarkedInclude.True(),
		Limit:                 limit,
	})
	diags = diags.Append(resp.Diagnostics.InConfigBody(n.Config.Config, n.Addr.String()))
	if resp.Diagnostics.HasErrors() {
		return diags
	}

	result := buildListResultValue(resSchema, resp.Results)
	result = result.WithMarks(configMarks, includeMarks, limitMarks)
	evalCtx.ListResults().Set(absResource, result)

	return diags
}
