// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

// ARCH.C3: "can this tool start a session, and if not, why" is one engine answer per
// tool. The first-hour screen used to decide it in the browser from up to ~29 reads.
func TestToolsReadiness_OneAnswerPerToolState(t *testing.T) {
	ctx := context.Background()
	none := func(*testing.T, *Module, model.TenantID, *loginStub) string { return "" }
	for _, tc := range []struct {
		name     string
		setup    func(t *testing.T, m *Module, tenant model.TenantID, stub *loginStub) string // the provider ref expected, if any
		driver   string
		ready    bool
		reason   string
		code     string
		message  string
		unreadOK bool // the tool's sign-in cannot be read
	}{
		{
			name: "own login",
			setup: func(_ *testing.T, _ *Module, _ model.TenantID, stub *loginStub) string {
				stub.signedIn["claude"] = true
				return ""
			},
			driver: "claude", ready: true, reason: ResolveOwnLogin,
		},
		{
			name: "not installed",
			setup: func(_ *testing.T, _ *Module, _ model.TenantID, stub *loginStub) string {
				stub.installed["codex"] = false
				return ""
			},
			driver: "codex", code: resolveCodeToolNotInstalled, message: "Install Codex first, under AI tools.",
		},
		{
			name: "nothing to run on", setup: none, driver: "codex", code: resolveCodeNothingToRunOn,
			message: "Codex is not signed in and Providers has nothing it can use. Sign it in under AI tools, or add an OpenAI key in Providers.",
		},
		{
			name: "a tested key",
			setup: func(t *testing.T, m *Module, tenant model.TenantID, _ *loginStub) string {
				return testedKey(t, m, tenant, "OpenAI main", nil).Ref
			},
			driver: "codex", ready: true, reason: ResolveAPIKey,
		},
		{
			name: "a refused key",
			setup: func(t *testing.T, m *Module, tenant model.TenantID, _ *loginStub) string {
				return testedKey(t, m, tenant, "Old OpenAI", ErrProviderRefused).Ref
			},
			driver: "codex", reason: ResolveAPIKey, code: resolveCodeKeyRefused,
			message: "Codex would run on the API key Old OpenAI, which its provider refused at the last test. Replace it in Providers, or add another key.",
		},
		{
			// The key's verdict counts only when the rule picked the key.
			name: "own login over a refused key",
			setup: func(t *testing.T, m *Module, tenant model.TenantID, stub *loginStub) string {
				testedKey(t, m, tenant, "Old OpenAI", ErrProviderRefused)
				stub.signedIn["codex"] = true
				return ""
			},
			driver: "codex", ready: true, reason: ResolveOwnLogin,
		},
		{
			name: "unreadable sign-in", setup: none, driver: "grok", code: resolveCodeSignInUnreadable, unreadOK: true,
			message: "the sign-in status of Grok Build could not be read on this node",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, tenant, stub := resolveHarness(t)
			wantRef := tc.setup(t, m, tenant, stub)
			m.ToolLogin = func(ctx context.Context, tenant model.TenantID, driver string) (bool, bool, error) {
				if tc.unreadOK && driver == tc.driver {
					return false, false, errors.New("status command failed")
				}
				return stub.status(ctx, tenant, driver)
			}
			all, err := m.ToolsReadiness(ctx, tenant)
			if err != nil {
				t.Fatal(err)
			}
			// The console's and the CLI's order (SESSION_TOOLS, sessionToolOrder), then Gemini CLI.
			drivers := []string{"claude", "codex", "grok", "opencode", "gemini-cli"}
			if len(all) != len(drivers) {
				t.Fatalf("answers = %d, want one per session tool (%v)", len(all), drivers)
			}
			var got ToolReadiness
			for i, a := range all {
				if a.Driver != drivers[i] {
					t.Fatalf("answer %d is %q, want %q (the tools' own order)", i, a.Driver, drivers[i])
				}
				if a.Driver == tc.driver {
					got = a
				}
			}
			if got.Ready != tc.ready || got.Reason != tc.reason || got.Code != tc.code || got.Message != tc.message {
				t.Fatalf("%s = %+v, want ready=%v reason=%q code=%q message=%q", tc.driver, got, tc.ready, tc.reason, tc.code, tc.message)
			}
			gotRef := ""
			if got.Provider != nil {
				gotRef = got.Provider.Ref
			}
			if gotRef != wantRef {
				t.Fatalf("provider = %q, want %q", gotRef, wantRef)
			}
		})
	}
}

// testedKey adds an OpenAI key and runs its connection test with the given verdict.
func testedKey(t *testing.T, m *Module, tenant model.TenantID, name string, verdict error) ProviderRecord {
	t.Helper()
	WithProviderProbe(&fakeProbe{err: verdict, result: ProviderProbeResult{Models: []string{"gpt-5", "gpt-5-mini"}}})(m)
	rec := addRecord(t, m, tenant, ProviderKindOpenAI, name, "", "sk-proj-fixture-0123456789")
	if _, err := m.TestProviderRecord(context.Background(), tenant, rec.Ref); err != nil {
		t.Fatal(err)
	}
	return rec
}

// The route: one read for every tool, the models a session may pick travel with the
// key, and a refusal is data in a 200.
func TestToolsReadiness_HTTPShape(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := New(WithSessionWorkspaceRoot(t.TempDir()), WithProviderSecretVault(newFakeVault()), WithProviderDriver(NewCodexDriver()))
	m.UseExecutionEnvironmentRef(testEnvRef)
	m.UseProfileHomesRoot(t.TempDir())
	m.UseToolLoginsRoot(t.TempDir())
	stub := &loginStub{installed: map[string]bool{"claude": true, "codex": true}, signedIn: map[string]bool{"claude": true}}
	m.ToolLogin = stub.status
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	WithProviderProbe(&fakeProbe{result: ProviderProbeResult{Models: []string{"gpt-5", "gpt-5-mini"}}})(m)
	rec := mustCreateRecord(t, m, tenant, CreateProviderRecordInput{Kind: ProviderKindOpenAI, DisplayName: "OpenAI main", APIKey: testProviderKey})
	if _, err := m.TestProviderRecord(t.Context(), tenant, rec.Ref); err != nil {
		t.Fatal(err)
	}
	r := h.do("GET", "/v1/m/sessions/provider-profiles/readiness", admin, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("readiness = %d %s", r.code, r.raw)
	}
	tools, _ := r.body["tools"].([]any)
	byDriver := map[string]map[string]any{}
	for _, raw := range tools {
		tool, _ := raw.(map[string]any)
		driver, _ := tool["driver"].(string)
		byDriver[driver] = tool
	}
	if c := byDriver["claude"]; c["ready"] != true || c["reason"] != "own_login" || c["provider"] != nil || c["code"] != nil {
		t.Fatalf("claude = %v", c)
	}
	codex := byDriver["codex"]
	provider, _ := codex["provider"].(map[string]any)
	models, _ := provider["models"].([]any)
	if codex["ready"] != true || codex["reason"] != "api_key" || codex["model_required"] != true ||
		provider["provider_ref"] != rec.Ref || len(models) != 2 || models[0] != "gpt-5" {
		t.Fatalf("codex = %v", codex)
	}
	if g := byDriver["grok"]; g["ready"] != false || g["code"] != resolveCodeToolNotInstalled || g["message"] != "Install Grok Build first, under AI tools." {
		t.Fatalf("grok = %v", g)
	}
	if r := h.do("GET", "/v1/m/sessions/provider-profiles/readiness", "", tenantHdr(tenant)); r.code != http.StatusUnauthorized {
		t.Fatalf("anonymous readiness = %d, want 401", r.code)
	}
	// Every role of an organization reads profiles (sessions:profile:read), so the
	// signed-in principal without it is a member of another organization: refused as
	// by the preview it replaces.
	outsider := h.viewerToken(admin, h.createOrg(admin, "globex"), "v@globex.com")
	for _, path := range []string{"/readiness", "/resolve?driver=codex"} {
		if r := h.do("GET", "/v1/m/sessions/provider-profiles"+path, outsider, tenantHdr(tenant)); r.code != http.StatusForbidden {
			t.Fatalf("another organization's member: GET %s = %d %s, want 403", path, r.code, r.raw)
		}
	}
}
