// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMCPProbePolicyReadsOnlyNamedRegisteredTenantFolders(t *testing.T) {
	protected, own, foreign, scratch, tool := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	m := New(WithConfinement([]string{protected}, false))
	h := newHarness(t, m)
	admin := h.adminLogin()
	a, b := h.createOrg(admin, "mcp-own"), h.createOrg(admin, "mcp-other")
	for tenant, root := range map[string]string{a.String(): own, b.String(): foreign} {
		response := h.doJSON("POST", "/v1/m/sessions/workspaces", admin, map[string]any{"root_path": root, "dlp_mode": "off"}, map[string]string{"X-Olivares-Tenant": tenant})
		if response.code != 201 {
			t.Fatalf("register folder: %d %s", response.code, response.raw)
		}
	}
	link := filepath.Join(own, "foreign-link")
	if err := os.Symlink(foreign, link); err != nil {
		t.Skip("symlinks unavailable")
	}
	policy, err := m.MCPProbePolicy(t.Context(), a, scratch, tool, []string{own, foreign, link})
	if err != nil {
		t.Fatal(err)
	}
	if policy == nil || len(policy.ReadWrite) != 1 || policy.ReadWrite[0] != scratch || len(policy.Protect) != 1 || policy.Protect[0] != protected {
		t.Fatal("probe lost private scratch or protected-path policy")
	}
	if len(policy.ReadOnly) != 2 || policy.ReadOnly[0] != tool || policy.ReadOnly[1] != own {
		t.Fatalf("probe escaped named tenant folder: %v", policy.ReadOnly)
	}
}
