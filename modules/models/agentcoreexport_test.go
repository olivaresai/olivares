// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package models_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/olivaresai/olivares/connectors/agentcore"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/models"
)

func TestAgentCoreExportItemsFromModelAccess(t *testing.T) {
	m := models.New()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		_, err := sc.Workspaces().Create(context.Background(), model.Workspace{Name: "Payments", Slug: "payments", Status: model.StatusActive})
		return err
	}); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	if r := h.do("POST", "/v1/m/models/model-access", admin, map[string]any{
		"subject_kind":  "role",
		"subject_ref":   "viewer",
		"target_kind":   "model",
		"target_ref":    "claude-sonnet-4-6",
		"workspace_ref": "payments",
		"surfaces":      []string{"direct"},
		"effect":        "allow",
	}, tenantHdr(tenant)); r.code != 201 {
		t.Fatalf("create allow = %d %s", r.code, r.raw)
	}
	if r := h.do("POST", "/v1/m/models/model-groups", admin, map[string]any{
		"name": "frontier", "member_refs": []string{"claude-opus-4-8"},
	}, tenantHdr(tenant)); r.code != 201 {
		t.Fatalf("create model group = %d %s", r.code, r.raw)
	}
	if r := h.do("POST", "/v1/m/models/model-access", admin, map[string]any{
		"subject_kind": "agent_group",
		"subject_ref":  "bots",
		"target_kind":  "model_group",
		"target_ref":   "frontier",
		"effect":       "forbid",
	}, tenantHdr(tenant)); r.code != 201 {
		t.Fatalf("create forbid = %d %s", r.code, r.raw)
	}
	got, err := m.AgentCoreExportItems(context.Background(), tenant)
	if err != nil {
		t.Fatalf("AgentCoreExportItems: %v", err)
	}
	want := []agentcore.ExportItem{
		{
			Kind:        "model_access",
			Tenant:      tenant.String(),
			SubjectKind: "role",
			SubjectRef:  "viewer",
			ScopeKind:   "workspace",
			Workspace:   "payments",
			Effect:      "permit",
			Models:      []string{"claude-sonnet-4-6"},
			Surfaces:    []string{"direct"},
		},
		{
			Kind:        "model_access",
			Tenant:      tenant.String(),
			SubjectKind: "agent_group",
			SubjectRef:  "bots",
			ScopeKind:   "workspace",
			Effect:      "forbid",
			Models:      []string{"frontier"},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("items mismatch\n got: %#v\nwant: %#v", got, want)
	}
}
