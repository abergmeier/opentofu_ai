// Copyright (c) The OpenTofu Authors
// SPDX-License-Identifier: MPL-2.0
// Copyright (c) 2023 HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package tofu

import (
	"testing"

	"github.com/zclconf/go-cty/cty"

	"github.com/opentofu/opentofu/internal/addrs"
	"github.com/opentofu/opentofu/internal/configs/configschema"
	"github.com/opentofu/opentofu/internal/plugins"
	"github.com/opentofu/opentofu/internal/providers"
	"github.com/opentofu/opentofu/internal/states"
)

func TestContext2Plan_listResourceImport(t *testing.T) {
	const config = `
list "test_instance" "found" {
  provider = test

  config {
    filter = "env"
  }
}

import {
  identity = list.test_instance.found.data[0].identity
  to       = test_instance.foo

  lifecycle {
    enabled = length(list.test_instance.found.data) != 0
  }
}

resource "test_instance" "foo" {
  name = "env"
}
`
	identityBody := &configschema.Object{
		Attributes: map[string]*configschema.Attribute{
			"name": {Type: cty.String, Required: true},
		},
		Nesting: configschema.NestingSingle,
	}

	cases := map[string]struct {
		results    []providers.ListResourceResult
		wantImport bool
	}{
		"found": {
			results: []providers.ListResourceResult{{
				Identity:    cty.ObjectVal(map[string]cty.Value{"name": cty.StringVal("env")}),
				DisplayName: "env",
			}},
			wantImport: true,
		},
		"empty": {},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			SkipExperimental(t, ExperimentalFeatureImport)

			p := testProvider("test")
			p.GetProviderSchemaResponse = &providers.GetProviderSchemaResponse{
				ResourceTypes: map[string]providers.Schema{
					"test_instance": {
						Block: &configschema.Block{
							Attributes: map[string]*configschema.Attribute{
								"id":   {Type: cty.String, Computed: true},
								"name": {Type: cty.String, Optional: true},
							},
						},
						IdentitySchema:        identityBody,
						IdentitySchemaVersion: 1,
					},
				},
				ListResources: map[string]providers.Schema{
					"test_instance": {
						Block: &configschema.Block{
							Attributes: map[string]*configschema.Attribute{
								"filter": {Type: cty.String, Optional: true},
							},
						},
					},
				},
			}
			p.ListResourceResponse = &providers.ListResourceResponse{Results: tc.results}
			p.ImportResourceStateFn = func(req providers.ImportResourceStateRequest) providers.ImportResourceStateResponse {
				return providers.ImportResourceStateResponse{
					ImportedResources: []providers.ImportedResource{{
						TypeName: "test_instance",
						State: cty.ObjectVal(map[string]cty.Value{
							"id":   cty.StringVal("imported"),
							"name": cty.StringVal("env"),
						}),
					}},
				}
			}

			m := testModuleInline(t, map[string]string{"main.tf": config})
			ctx := testContext2(t, &ContextOpts{
				Plugins: plugins.NewLibrary(map[addrs.Provider]providers.Factory{
					addrs.NewDefaultProvider("test"): testProviderFuncFixed(p),
				}, nil),
			})

			if diags := ctx.Validate(t.Context(), m); diags.HasErrors() {
				t.Fatalf("unexpected validate errors: %s", diags.Err())
			}

			plan, diags := ctx.Plan(t.Context(), m, states.NewState(), &PlanOpts{})
			if diags.HasErrors() {
				t.Fatalf("unexpected plan errors: %s", diags.Err())
			}

			if !p.ListResourceCalled {
				t.Fatal("ListResource should have been called")
			}
			if got := p.ImportResourceStateCalled; got != tc.wantImport {
				t.Fatalf("ImportResourceState called = %t, want %t", got, tc.wantImport)
			}
			if len(plan.Changes.Resources) != 1 {
				t.Fatalf("expected one resource change, got %d", len(plan.Changes.Resources))
			}
			if imported := plan.Changes.Resources[0].Importing != nil; imported != tc.wantImport {
				t.Fatalf("importing = %t, want %t", imported, tc.wantImport)
			}
		})
	}
}
