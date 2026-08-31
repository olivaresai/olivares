// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sourcescope_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/olivaresai/olivares/connectors/agentcore"
)

func TestAgentCoreExportItemsFromAssignments(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.createWorkspace(tenant, "engineering")
	h.createWorkspace(tenant, "marketing")
	h.createWorkspace(tenant, "paused")

	if r := h.do("POST", "/v1/m/sourcescope/assignments", admin, map[string]any{
		"connector_name": "github", "workspace_ref": "engineering", "mode": "r", "enabled": true,
	}, tenantHdr(tenant)); r.code != 201 {
		t.Fatalf("create read assignment = %d %s", r.code, r.raw)
	}
	if r := h.do("POST", "/v1/m/sourcescope/assignments", admin, map[string]any{
		"connector_name": "jira", "workspace_ref": "marketing", "enabled": true,
	}, tenantHdr(tenant)); r.code != 201 {
		t.Fatalf("create rw assignment = %d %s", r.code, r.raw)
	}
	if r := h.do("POST", "/v1/m/sourcescope/assignments", admin, map[string]any{
		"connector_name": "slack", "workspace_ref": "paused", "enabled": false,
	}, tenantHdr(tenant)); r.code != 201 {
		t.Fatalf("create disabled assignment = %d %s", r.code, r.raw)
	}

	got, err := h.ss.AgentCoreExportItems(context.Background(), tenant)
	if err != nil {
		t.Fatalf("AgentCoreExportItems: %v", err)
	}
	want := []agentcore.ExportItem{
		{
			Kind:        "source_scope",
			Tenant:      tenant.String(),
			SubjectKind: "workspace",
			SubjectRef:  "engineering",
			ScopeKind:   "workspace",
			Workspace:   "engineering",
			Effect:      "permit",
			Sources:     []string{"github"},
			Access:      "r",
		},
		{
			Kind:        "source_scope",
			Tenant:      tenant.String(),
			SubjectKind: "workspace",
			SubjectRef:  "marketing",
			ScopeKind:   "workspace",
			Workspace:   "marketing",
			Effect:      "permit",
			Sources:     []string{"jira"},
			Access:      "rw",
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("items mismatch\n got: %#v\nwant: %#v", got, want)
	}
}
