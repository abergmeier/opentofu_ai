// Copyright (c) The OpenTofu Authors
// SPDX-License-Identifier: MPL-2.0
// Copyright (c) 2023 HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package tofu

import (
	"sync"

	"github.com/zclconf/go-cty/cty"

	"github.com/opentofu/opentofu/internal/addrs"
	"github.com/opentofu/opentofu/internal/providers"
)

// ListResultStore records the value of each "list" block as it is queried
// during a graph walk. It is safe for concurrent use.
//
// The results are intentionally kept out of the state, because they describe
// remote objects that may or may not be managed by OpenTofu.
type ListResultStore struct {
	mu      sync.RWMutex
	results map[string]cty.Value
}

func NewListResultStore() *ListResultStore {
	return &ListResultStore{results: make(map[string]cty.Value)}
}

func (s *ListResultStore) Set(addr addrs.AbsResource, val cty.Value) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.results[addr.String()] = val
}

func (s *ListResultStore) Get(addr addrs.AbsResource) (cty.Value, bool) {
	if s == nil {
		return cty.NilVal, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	val, ok := s.results[addr.String()]
	return val, ok
}

// listResultElementType returns the type of each element of the "data"
// attribute of a list block's value, for the given managed resource schema.
func listResultElementType(resSchema providers.Schema) cty.Type {
	return cty.Object(map[string]cty.Type{
		"display_name": cty.String,
		"identity":     listResultIdentityType(resSchema),
		"state":        resSchema.Block.ImpliedType(),
	})
}

// listResultIdentityType is dynamic when the resource type has no identity schema,
// in which case the identity attribute is always null.
func listResultIdentityType(resSchema providers.Schema) cty.Type {
	if resSchema.IdentitySchema == nil {
		return cty.DynamicPseudoType
	}
	return resSchema.IdentitySchema.ImpliedType()
}

// listResultValueType returns the type of the value that "list.TYPE.NAME"
// evaluates to.
func listResultValueType(resSchema providers.Schema) cty.Type {
	return cty.Object(map[string]cty.Type{
		"data": cty.List(listResultElementType(resSchema)),
	})
}

// buildListResultValue converts the results of a list request into the value
// that "list.TYPE.NAME" evaluates to.
func buildListResultValue(resSchema providers.Schema, results []providers.ListResourceResult) cty.Value {
	elemTy := listResultElementType(resSchema)
	identityTy := listResultIdentityType(resSchema)
	stateTy := resSchema.Block.ImpliedType()

	var elems []cty.Value
	var valMarks []cty.PathValueMarks
	for i, r := range results {
		identity := cty.NullVal(identityTy)
		if r.Identity != cty.NilVal {
			identity = r.Identity
		}

		state := cty.NullVal(stateTy)
		if r.ResourceObject != cty.NilVal && !r.ResourceObject.IsNull() {
			state = r.ResourceObject
			statePath := cty.Path{cty.GetAttrStep{Name: "data"}, cty.IndexStep{Key: cty.NumberIntVal(int64(i))}, cty.GetAttrStep{Name: "state"}}
			valMarks = append(valMarks, resSchema.Block.ValueMarks(state, statePath, nil)...)
		}

		elems = append(elems, cty.ObjectVal(map[string]cty.Value{
			"display_name": cty.StringVal(r.DisplayName),
			"identity":     identity,
			"state":        state,
		}))
	}

	data := cty.ListValEmpty(elemTy)
	if len(elems) > 0 {
		data = cty.ListVal(elems)
	}
	return cty.ObjectVal(map[string]cty.Value{"data": data}).MarkWithPaths(valMarks)
}
