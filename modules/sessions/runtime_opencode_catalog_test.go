// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package sessions

import (
	"net/http"
	"testing"
)

// The sign-in status optimization must not turn off the session's model catalog.
func TestOpenCodeSessionChildKeepsModelCatalogFetch(t *testing.T) {
	m := New(openCodeRuntimeOptions(WithSessionWorkspaceRoot(t.TempDir()))...)
	m.UseExecutionEnvironmentRef(testEnvRef)
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "opencode-catalog")
	prof := newOpenCodeProfile(t, m, tenant, "opencode-catalog", AuthSourceAccountHome)
	record := setOpenCodeFixture(t, prof, openCodeFixture{SessionID: "ses-catalog"})
	created := h.doJSON("POST", "/v1/m/sessions/runs", admin, map[string]any{
		"transport": TransportStreamJSON, "isolation": IsolationNative, "provider_profile_ref": prof.Ref,
	}, tenantHdr(tenant))
	if created.code != http.StatusCreated {
		t.Fatalf("session launch = %d %s", created.code, created.raw)
	}
	ref := created.body["run_ref"].(string)
	t.Cleanup(func() {
		if stopped := h.doJSON("POST", "/v1/m/sessions/runs/"+ref+"/stop", admin, nil, tenantHdr(tenant)); stopped.code != http.StatusOK {
			t.Errorf("session stop = %d %s", stopped.code, stopped.raw)
		}
	})
	peer := readOpenCodeFixtureRecord(t, record)
	if flag, present := peer.Env[envOpenCodeDisableModelsFetch]; present {
		t.Fatalf("session child received a sign-in-status flag: %q", flag)
	}
}
