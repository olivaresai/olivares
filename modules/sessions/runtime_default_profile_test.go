// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// 2026-10-02 ("launch needs model and fields by hand") and NEXT-COMMON "the default
// works": POST /runs with no provider_profile_ref answered 400 "select a provider
// profile before launching a session". It now runs under the profile the engine
// resolves for Claude Code, the one `session start` and the console use.
func TestALaunchNamingNoProfileRunsOnTheEnginesChoice(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fr := &fakeRunner{initSID: "sess-default"}
	m := New(WithSessionWorkspaceRoot(t.TempDir()), WithRunner(fr), WithCredentialSource(staticCred()), WithProviderSecretVault(newFakeVault()))
	m.UseExecutionEnvironmentRef(testEnvRef)
	m.UseProfileHomesRoot(t.TempDir())
	m.UseToolLoginsRoot(t.TempDir())
	stub := &loginStub{installed: map[string]bool{"claude": true}, signedIn: map[string]bool{"claude": true}}
	m.ToolLogin = stub.status
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	r := h.doJSON("POST", "/v1/m/sessions/runs", admin, map[string]any{}, tenantHdr(tenant))
	if r.code != http.StatusCreated || r.body["provider_driver"] != "claude" {
		t.Fatalf("launch with no field = %d %s, want 201 on Claude Code", r.code, r.raw)
	}
	resolved := h.doJSON("POST", "/v1/m/sessions/provider-profiles/resolve", admin, map[string]any{"driver": "claude"}, tenantHdr(tenant))
	prof, _ := resolved.body["profile"].(map[string]any)
	if resolved.body["created"] != false || prof["profile_ref"] != r.body["provider_profile_ref"] {
		t.Fatalf("the launch ran on %v, the engine's choice is %s", r.body["provider_profile_ref"], resolved.raw)
	}

	// Nothing to run on: the resolve rule's own sentence, and nothing spawned.
	stub.mu.Lock()
	stub.signedIn["claude"] = false
	stub.mu.Unlock()
	before := launchCount(fr)
	r = h.doJSON("POST", "/v1/m/sessions/runs", admin, map[string]any{}, tenantHdr(tenant))
	msg, _ := r.body["error"].(map[string]any)
	if r.code != http.StatusConflict || !strings.Contains(fmt.Sprint(msg["message"]), "add an Anthropic key in Providers") {
		t.Fatalf("launch with nothing to run on = %d %s, want the resolve refusal", r.code, r.raw)
	}
	if launchCount(fr) != before {
		t.Fatal("a refused launch spawned")
	}
}
