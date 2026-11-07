// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package governance

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/olivaresai/olivares/core/api/oas"
	"github.com/olivaresai/olivares/core/model"
)

// PolicySpecValidator validates and canonicalizes a composition's policy spec.
// It receives the request's context and tenant so a spec can be checked against
// the tenant's own data, and must return a bounded, typed document, or a
// client-facing refusal sentence.
type PolicySpecValidator func(context.Context, model.TenantID, json.RawMessage) (map[string]any, string)

// PolicyKindAuthoring describes the authoring surface of a linked policy kind.
// AuditFields must return only non-sensitive attribution, never the raw spec.
type PolicyKindAuthoring struct {
	Validate    PolicySpecValidator
	Schema      map[string]any
	AuditFields func(map[string]any) map[string]any
}

// RegisterPolicyKind enables an additional, linked policy kind on the existing
// policy API. Call only at boot, before serving requests. The kind must already
// have a principal declaration in model.PolicyKinds; built-in kinds cannot be
// replaced. Registration supplies authoring, never policy enforcement.
func (m *Module) RegisterPolicyKind(kind string, authoring PolicyKindAuthoring) error {
	if kind == "" || strings.TrimSpace(kind) != kind || strings.ToLower(kind) != kind || authoring.Validate == nil || authoring.Schema == nil {
		return fmt.Errorf("governance: policy kind needs a canonical name and validator")
	}
	if m.ownsPolicyKind(kind) {
		return fmt.Errorf("governance: policy kind %q already registered", kind)
	}
	if _, ok := model.PolicyKinds.Variant(kind); !ok {
		return fmt.Errorf("governance: policy kind %q lacks a principal declaration", kind)
	}
	if m.policyKinds == nil {
		m.policyKinds = map[string]PolicyKindAuthoring{}
	}
	m.policyKinds[kind] = authoring
	return nil
}

func (m *Module) ownsPolicyKind(kind string) bool {
	return governancePolicyKind(kind) || m.policyKinds[kind].Validate != nil
}

func (m *Module) canonicalizePolicy(ctx context.Context, tenant model.TenantID, in *policyRequest) (map[string]any, string) {
	// Preserve the existing validation and error messages for built-in and unknown
	// kinds. Only a registered kind opts into its composition's validator.
	kind := strings.ToLower(strings.TrimSpace(in.Kind))
	if validate := m.policyKinds[kind].Validate; validate != nil {
		in.Kind, in.Name = kind, strings.TrimSpace(in.Name)
		if in.Name == "" {
			return nil, "name is required"
		}
		return validate(ctx, tenant, in.Spec)
	}
	if len(m.policyKinds) > 0 && !governancePolicyKind(kind) && strings.TrimSpace(in.Name) != "" {
		return nil, "kind must be one of " + strings.Join(m.acceptedPolicyKinds(), ", ")
	}
	return in.validateAndCanonicalize()
}

func (m *Module) policyAuditMeta(p model.Policy, meta map[string]any) map[string]any {
	if fields := m.policyKinds[p.Kind].AuditFields; fields != nil {
		for k, v := range fields(p.Spec) {
			if k != "kind" && k != "enabled" {
				meta[k] = v
			}
		}
	}
	return meta
}

// acceptedPolicyKinds lists the built-in kinds, then each registered kind in name order.
func (m *Module) acceptedPolicyKinds() []string {
	extra := make([]string, 0, len(m.policyKinds))
	for kind := range m.policyKinds {
		extra = append(extra, kind)
	}
	slices.Sort(extra)
	return append(slices.Clone(builtinPolicyKinds), extra...)
}

func (m *Module) policySchema() map[string]any {
	schema := governancePolicySchema()
	if len(m.policyKinds) == 0 {
		return schema
	}
	kinds := m.acceptedPolicyKinds()
	branches := schema["oneOf"].([]any)
	for _, kind := range kinds[len(builtinPolicyKinds):] {
		branches = append(branches, oas.Obj("properties", oas.Obj("kind", oas.Obj("const", kind), "spec", m.policyKinds[kind].Schema), "required", []string{"kind", "spec"}))
	}
	schema["properties"].(map[string]any)["kind"].(map[string]any)["enum"] = oas.Enum(kinds...)
	schema["oneOf"] = branches
	return schema
}
