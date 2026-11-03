// Copyright (c) The OpenTofu Authors
// SPDX-License-Identifier: MPL-2.0
// Copyright (c) 2023 HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package configs

import (
	"fmt"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"

	"github.com/opentofu/opentofu/internal/addrs"
)

// decodeListBlock decodes a "list" block into a [Resource] with mode
// [addrs.ListResourceMode]. The returned resource's Config is the body of the
// nested "config" block, which is decoded against the provider's list schema.
func decodeListBlock(block *hcl.Block) (*Resource, hcl.Diagnostics) {
	var diags hcl.Diagnostics
	r := &Resource{
		Mode:      addrs.ListResourceMode,
		Type:      block.Labels[0],
		Name:      block.Labels[1],
		DeclRange: block.DefRange,
		TypeRange: block.LabelRanges[0],
		Config:    hcl.EmptyBody(),
	}

	if !hclsyntax.ValidIdentifier(r.Type) {
		diags = append(diags, &hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Invalid list resource type name",
			Detail:   badIdentifierDetail,
			Subject:  &block.LabelRanges[0],
		})
	}
	if !hclsyntax.ValidIdentifier(r.Name) {
		diags = append(diags, &hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Invalid list resource name",
			Detail:   badIdentifierDetail,
			Subject:  &block.LabelRanges[1],
		})
	}

	content, contentDiags := block.Body.Content(listBlockSchema)
	diags = append(diags, contentDiags...)

	if attr, exists := content.Attributes["provider"]; exists {
		var providerDiags hcl.Diagnostics
		r.ProviderConfigRef, providerDiags = decodeProviderConfigRef(attr.Expr, "provider")
		diags = append(diags, providerDiags...)
	}

	if attr, exists := content.Attributes["depends_on"]; exists {
		deps, depsDiags := decodeDependsOn(attr)
		diags = append(diags, depsDiags...)
		r.DependsOn = append(r.DependsOn, deps...)
	}

	if attr, exists := content.Attributes["limit"]; exists {
		r.ListLimit = attr.Expr
	}

	if attr, exists := content.Attributes["include_resource"]; exists {
		r.ListIncludeResource = attr.Expr
	}

	var seenConfig *hcl.Block
	for _, b := range content.Blocks {
		if seenConfig != nil {
			diags = append(diags, &hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Duplicate config block",
				Detail:   fmt.Sprintf("This list block already has a config block at %s.", seenConfig.DefRange),
				Subject:  b.DefRange.Ptr(),
			})
			continue
		}
		seenConfig = b
		r.Config = b.Body
	}

	return r, diags
}

var listBlockSchema = &hcl.BodySchema{
	Attributes: []hcl.AttributeSchema{
		{Name: "provider"},
		{Name: "depends_on"},
		{Name: "limit"},
		{Name: "include_resource"},
	},
	Blocks: []hcl.BlockHeaderSchema{
		{Type: "config"},
	},
}
