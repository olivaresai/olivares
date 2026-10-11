// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

// A session started the default way runs on the organization's own login of the
// tool, the one the console signed in under AI tools. The run names that login as
// the AI tools instance it is (GET /v1/m/agenttools/providers: "claude/olivares"),
// so the session can show that instance's plan window (#427). A profile on homes of
// its own is not that login, and names none.
func TestARunNamesTheToolLoginInstanceItRunsOn(t *testing.T) {
	h, admin, tenant := toolLoginInstanceHarness(t, t.TempDir())

	r := h.doJSON("POST", "/v1/m/sessions/runs", admin, map[string]any{}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("default launch = %d %s", r.code, r.raw)
	}
	if r.body["provider_instance"] != "claude/olivares" {
		t.Fatalf("provider_instance = %v, want the organization's own login claude/olivares", r.body["provider_instance"])
	}

	own := h.doJSON("POST", "/v1/m/sessions/provider-profiles", admin, map[string]any{
		"driver": "claude", "display_name": "own homes", "auth_source": AuthSourceAccountHome,
		"config_home": t.TempDir(), "user_home": t.TempDir(),
	}, tenantHdr(tenant))
	if own.code != http.StatusCreated {
		t.Fatalf("create profile = %d %s", own.code, own.raw)
	}
	r = h.doJSON("POST", "/v1/m/sessions/runs", admin, map[string]any{"provider_profile_ref": own.body["profile_ref"]}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("launch on own homes = %d %s", r.code, r.raw)
	}
	if v, present := r.body["provider_instance"]; present {
		t.Fatalf("a run on homes of its own names instance %v", v)
	}
}

// The tool-logins root reached through a link (a data directory on another disk)
// is still the organization's own login.
func TestARunNamesTheToolLoginInstanceUnderALinkedRoot(t *testing.T) {
	link := filepath.Join(t.TempDir(), "tool-logins")
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Fatalf("link the tool-logins root: %v", err)
	}
	h, admin, tenant := toolLoginInstanceHarness(t, link)
	r := h.doJSON("POST", "/v1/m/sessions/runs", admin, map[string]any{}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("default launch = %d %s", r.code, r.raw)
	}
	if r.body["provider_instance"] != "claude/olivares" {
		t.Fatalf("provider_instance = %v, want claude/olivares under a linked root", r.body["provider_instance"])
	}
}

func toolLoginInstanceHarness(t *testing.T, toolLogins string) (*harness, string, model.TenantID) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	fr := &fakeRunner{initSID: "sess-instance"}
	m := New(WithSessionWorkspaceRoot(t.TempDir()), WithRunner(fr), WithCredentialSource(staticCred()), WithProviderSecretVault(newFakeVault()))
	m.UseExecutionEnvironmentRef(testEnvRef)
	m.UseProfileHomesRoot(t.TempDir())
	m.UseToolLoginsRoot(toolLogins)
	stub := &loginStub{installed: map[string]bool{"claude": true}, signedIn: map[string]bool{"claude": true}}
	m.ToolLogin = stub.status
	m.EnableProfiledLaunches()
	h := newHarness(t, m)
	admin := h.adminLogin()
	return h, admin, h.createOrg(admin, "acme")
}
