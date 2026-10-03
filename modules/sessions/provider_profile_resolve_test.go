// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Root, 2026-10-01 (HU 030, FH 025/026/028): "which profile does a new session use"
// was web code in the console and a different rule in the CLI, so the CLI said
// "Not logged in" where the console worked. It is ONE engine rule now:
// POST /v1/m/sessions/provider-profiles/resolve.

type loginStub struct {
	mu        sync.Mutex
	installed map[string]bool
	signedIn  map[string]bool
}

func (s *loginStub) status(_ context.Context, _ model.TenantID, driver string) (bool, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.installed[driver], s.signedIn[driver], nil
}

func resolveHarness(t *testing.T) (*Module, model.TenantID, *loginStub) {
	t.Helper()
	m, _, tenant, stub := resolveHarnessWithStore(t)
	return m, tenant, stub
}

func resolveHarnessWithStore(t *testing.T) (*Module, store.Store, model.TenantID, *loginStub) {
	t.Helper()
	t.Setenv("HOME", t.TempDir()) // the engine user's own home: never the product's login
	m, st, tenant, _ := newRuntimeHarness(t, WithProviderSecretVault(newFakeVault()))
	m.UseExecutionEnvironmentRef(testEnvRef)
	m.UseProfileHomesRoot(t.TempDir())
	m.UseToolLoginsRoot(t.TempDir())
	stub := &loginStub{
		installed: map[string]bool{"claude": true, "codex": true, "grok": true, "opencode": true},
		signedIn:  map[string]bool{},
	}
	m.UseToolLoginStatus(stub.status)
	return m, st, tenant, stub
}

func addRecord(t *testing.T, m *Module, tenant model.TenantID, kind, name, baseURL, key string) ProviderRecord {
	t.Helper()
	rec, err := m.CreateProviderRecord(context.Background(), tenant, CreateProviderRecordInput{
		Kind: kind, DisplayName: name, BaseURL: baseURL, APIKey: key,
		Actor: auth.Principal{Kind: auth.KindUser, UserID: model.NewID()},
	})
	if err != nil {
		t.Fatalf("add %s record: %v", kind, err)
	}
	return rec
}

func TestResolve_ASignedInToolUsesItsOwnLogin(t *testing.T) {
	m, tenant, stub := resolveHarness(t)
	stub.signedIn["claude"] = true
	addRecord(t, m, tenant, ProviderKindAnthropic, "Team key", "", "sk-ant-fixture-0123456789")
	got, err := m.ResolveProfile(context.Background(), tenant, "claude")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.Reason != ResolveOwnLogin || got.Profile.AuthSource != AuthSourceAccountHome || got.Profile.ProviderRecordRef != "" {
		t.Fatalf("resolve = %+v, want the tool's own login", got)
	}
	if got.Profile.DisplayName != "Claude Code" || !got.Created {
		t.Fatalf("display %q created %v", got.Profile.DisplayName, got.Created)
	}
	again, err := m.ResolveProfile(context.Background(), tenant, "claude")
	if err != nil || again.Created || again.Profile.Ref != got.Profile.Ref {
		t.Fatalf("second resolve = %+v %v, want the same profile reused", again, err)
	}
}

func TestResolve_NotSignedInUsesTheVendorsKeyBoundAsAManagedCredential(t *testing.T) {
	m, tenant, _ := resolveHarness(t)
	addRecord(t, m, tenant, ProviderKindOpenAICompatible, "Gateway", "https://gw.example.test/v1", "sk-compat-0123456789")
	key := addRecord(t, m, tenant, ProviderKindAnthropic, "Team key", "", "sk-ant-fixture-0123456789")
	got, err := m.ResolveProfile(context.Background(), tenant, "claude")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.Reason != ResolveAPIKey || got.Profile.AuthSource != AuthSourceManagedInjection ||
		got.Profile.ProviderRecordRef != key.Ref || got.Provider == nil || got.Provider.Ref != key.Ref {
		t.Fatalf("resolve = %+v, want managed_injection bound to the Anthropic key (own vendor first)", got)
	}
	if got.Profile.DisplayName != "Claude Code (Team key)" {
		t.Fatalf("display name %q", got.Profile.DisplayName)
	}
}

func TestResolve_RefusalSentenceNamesTheToolAndItsKey(t *testing.T) {
	m, tenant, stub := resolveHarness(t)
	for driver, want := range map[string]string{
		"claude":   "Claude Code is not signed in and Providers has nothing it can use. Sign it in under AI tools, or add an Anthropic key in Providers.",
		"codex":    "Codex is not signed in and Providers has nothing it can use. Sign it in under AI tools, or add an OpenAI key in Providers.",
		"grok":     "Grok Build is not signed in and Providers has nothing it can use. Sign it in under AI tools, or add an xAI key in Providers.",
		"opencode": "OpenCode has nothing to run on yet. Add a key or a local model (Ollama) in Providers.",
	} {
		_, err := m.ResolveProfile(context.Background(), tenant, driver)
		if statusOf(err) != http.StatusConflict || err.Error() != want {
			t.Errorf("%s: %v, want 409 %q", driver, err, want)
		}
	}
	stub.installed["codex"] = false
	if _, err := m.ResolveProfile(context.Background(), tenant, "codex"); statusOf(err) != http.StatusConflict ||
		err.Error() != "Install Codex first, under AI tools." {
		t.Errorf("not installed: %v", err)
	}
	if _, err := m.ResolveProfile(context.Background(), tenant, "cursor"); statusOf(err) != http.StatusBadRequest {
		t.Errorf("unknown driver: %v, want 400", err)
	}
}

func TestResolve_OpenCodeWithAKeyAndALocalModelUsesTheLocalModel(t *testing.T) {
	m, tenant, _ := resolveHarness(t)
	addRecord(t, m, tenant, ProviderKindAnthropic, "Team key", "", "sk-ant-fixture-0123456789")
	local := addRecord(t, m, tenant, ProviderKindOllama, "Ollama on this server", "http://127.0.0.1:11434", "")
	got, err := m.ResolveProfile(context.Background(), tenant, "opencode")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.Profile.ProviderRecordRef != local.Ref || got.Profile.AuthSource != AuthSourceManagedInjection {
		t.Fatalf("resolve = %+v, want the local model", got)
	}
	// Codex keeps its own vendor first: an OpenAI key wins over the local model.
	openai := addRecord(t, m, tenant, ProviderKindOpenAI, "OpenAI key", "", "sk-proj-fixture-0123456789")
	codex, err := m.ResolveProfile(context.Background(), tenant, "codex")
	if err != nil || codex.Profile.ProviderRecordRef != openai.Ref {
		t.Fatalf("codex resolve = %+v %v, want the OpenAI key", codex, err)
	}
}

func TestResolve_ConcurrentResolvesLeaveOneProfile(t *testing.T) {
	m, tenant, _ := resolveHarness(t)
	addRecord(t, m, tenant, ProviderKindAnthropic, "Team key", "", "sk-ant-fixture-0123456789")
	const n = 8
	refs := make([]string, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			got, err := m.ResolveProfile(context.Background(), tenant, "claude")
			if err != nil {
				t.Errorf("resolve %d: %v", i, err)
				return
			}
			refs[i] = got.Profile.Ref
		}(i)
	}
	wg.Wait()
	for _, ref := range refs[1:] {
		if ref != refs[0] {
			t.Fatalf("concurrent resolves made more than one profile: %v", refs)
		}
	}
	all, _, err := m.ListProfiles(context.Background(), tenant, ProfileActive, model.Query{Limit: 50})
	if err != nil || len(all) != 1 {
		t.Fatalf("active profiles = %d (%v), want 1", len(all), err)
	}
}

// The shape CLX's CLI reads (FH 026): the profile object, the reason, the provider
// for a key, created; a refusal is 409 with the module's error body.
func TestResolve_HTTPShape(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := New(WithSessionWorkspaceRoot(t.TempDir()), WithProviderSecretVault(newFakeVault()))
	m.UseExecutionEnvironmentRef(testEnvRef)
	m.UseProfileHomesRoot(t.TempDir())
	m.UseToolLoginsRoot(t.TempDir())
	stub := &loginStub{installed: map[string]bool{"claude": true}, signedIn: map[string]bool{"claude": true}}
	m.UseToolLoginStatus(stub.status)
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	r := h.doJSON("POST", "/v1/m/sessions/provider-profiles/resolve", admin, map[string]any{"driver": "claude"}, tenantHdr(tenant))
	if r.code != http.StatusOK || r.body["reason"] != "own_login" || r.body["created"] != true {
		t.Fatalf("resolve = %d %s", r.code, r.raw)
	}
	prof, _ := r.body["profile"].(map[string]any)
	if prof["auth_source"] != AuthSourceAccountHome || prof["driver"] != "claude" || prof["profile_ref"] == "" {
		t.Fatalf("profile = %v", prof)
	}
	if _, has := r.body["provider"]; has {
		t.Fatalf("own login carries a provider: %s", r.raw)
	}
	if r := h.do("GET", "/v1/m/sessions/provider-profiles/resolve?driver=claude", admin, tenantHdr(tenant)); r.code != http.StatusOK ||
		r.body["reason"] != "own_login" || r.body["profile"] != nil || r.body["created"] != nil {
		t.Fatalf("preview = %d %s, want the reason only", r.code, r.raw)
	}
	stub.mu.Lock()
	stub.signedIn["claude"] = false
	stub.mu.Unlock()
	r = h.doJSON("POST", "/v1/m/sessions/provider-profiles/resolve", admin, map[string]any{"driver": "claude"}, tenantHdr(tenant))
	msg, _ := r.body["error"].(map[string]any)
	if r.code != http.StatusConflict || !strings.Contains(fmt.Sprint(msg["message"]), "add an Anthropic key in Providers") {
		t.Fatalf("refusal = %d %s", r.code, r.raw)
	}
	if r := h.doJSON("POST", "/v1/m/sessions/provider-profiles/resolve", admin, map[string]any{"driver": "claude", "extra": 1}, tenantHdr(tenant)); r.code != http.StatusBadRequest {
		t.Fatalf("unknown field = %d, want 400", r.code)
	}
}

// The preview is the same rule with no write: a page can show what a new session
// would run on (Now, the New session dialog) without making a profile by being
// viewed, and its answer is the one the POST then acts on.
func TestResolve_PreviewAnswersTheSameAndWritesNothing(t *testing.T) {
	m, tenant, stub := resolveHarness(t)
	key := addRecord(t, m, tenant, ProviderKindAnthropic, "Team key", "", "sk-ant-fixture-0123456789")
	got, err := m.PreviewProfile(context.Background(), tenant, "claude")
	if err != nil || got.Reason != ResolveAPIKey || got.Provider == nil || got.Provider.Ref != key.Ref || got.Profile.Ref != "" || got.Created {
		t.Fatalf("preview = %+v %v, want the Anthropic key and no profile", got, err)
	}
	all, _, err := m.ListProfiles(context.Background(), tenant, ProfileActive, model.Query{Limit: 50})
	if err != nil || len(all) != 0 {
		t.Fatalf("profiles after a preview = %d (%v), want 0", len(all), err)
	}
	resolved, err := m.ResolveProfile(context.Background(), tenant, "claude")
	if err != nil || resolved.Profile.ProviderRecordRef != got.Provider.Ref {
		t.Fatalf("resolve = %+v %v, want the record the preview named", resolved, err)
	}
	stub.signedIn["claude"] = true
	if got, err := m.PreviewProfile(context.Background(), tenant, "claude"); err != nil || got.Reason != ResolveOwnLogin || got.Provider != nil {
		t.Fatalf("signed-in preview = %+v %v, want own_login", got, err)
	}
	if _, err := m.PreviewProfile(context.Background(), tenant, "grok"); statusOf(err) != http.StatusConflict ||
		!strings.Contains(err.Error(), "add an xAI key in Providers") {
		t.Fatalf("grok preview = %v, want the refusal sentence", err)
	}
}

// Root on FH 036: the own login is the tenant's, in the home the product made for it
// (<tool-logins>/<tenant>/<driver>), never the engine user's ~/.claude. Two tenants
// get two homes.
func TestResolve_TheOwnLoginIsTheTenantsOwnHome(t *testing.T) {
	m, st, tenant, stub := resolveHarnessWithStore(t)
	stub.signedIn["claude"] = true
	got, err := m.ResolveProfile(context.Background(), tenant, "claude")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	logins, _ := filepath.EvalSymlinks(m.toolLoginsRoot)
	if want := filepath.Join(logins, tenant.String(), "claude", ".claude"); got.Profile.ConfigHome != want {
		t.Fatalf("config home = %q, want the tenant's own %q", got.Profile.ConfigHome, want)
	}
	if strings.HasPrefix(got.Profile.ConfigHome, os.Getenv("HOME")) {
		t.Fatalf("config home %q is under the engine user's HOME", got.Profile.ConfigHome)
	}
	other := ensureTenant(t, st, "second-tenant")
	second, err := m.ResolveProfile(context.Background(), other, "claude")
	if err != nil {
		t.Fatalf("resolve for the second tenant: %v", err)
	}
	if want := filepath.Join(logins, other.String(), "claude", ".claude"); second.Profile.ConfigHome != want || second.Profile.ConfigHome == got.Profile.ConfigHome {
		t.Fatalf("second tenant config home = %q, want its own %q", second.Profile.ConfigHome, want)
	}
}

// Root on FH 036: a profile made before (its configuration home is the engine user's
// own ~/.claude) is never the resolve's answer, is shown as such, and is not launched.
func TestAProfileOnTheServerUserLoginIsShownAndNotUsed(t *testing.T) {
	m, tenant, stub := resolveHarness(t)
	stub.signedIn["claude"] = true
	userClaude := filepath.Join(os.Getenv("HOME"), ".claude")
	if err := os.MkdirAll(userClaude, 0o700); err != nil {
		t.Fatal(err)
	}
	old, err := m.CreateProfile(context.Background(), tenant, CreateProfileInput{
		Driver: "claude", AuthSource: AuthSourceAccountHome, ConfigHome: userClaude, UserHome: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("the earlier profile: %v", err)
	}
	if !m.toProfileDTO(old).ServerUserLogin {
		t.Fatal("the profile on the server's user login is not marked")
	}
	got, err := m.ResolveProfile(context.Background(), tenant, "claude")
	if err != nil || got.Profile.Ref == old.Ref || !got.Created {
		t.Fatalf("resolve = %+v %v, want a new profile on the product login, not %s", got, err, old.Ref)
	}
	p := CreateRunParams{ProviderProfileRef: old.Ref, Transport: TransportStreamJSON, Isolation: IsolationNative}
	if err := m.resolveLaunchProfileInto(context.Background(), tenant, &p); statusOf(err) != http.StatusConflict ||
		err.Error() != serverUserLoginSentence {
		t.Fatalf("launch on it = %v, want 409 %q", err, serverUserLoginSentence)
	}
}

// SR2 on 65d841a1: a tool login belongs to the organization that signed it in. A
// profile of another organization naming that home is refused when it is registered.
func TestAnotherOrganizationsToolLoginCannotBeBorrowed(t *testing.T) {
	m, st, tenantA, _ := resolveHarnessWithStore(t)
	tenantB := ensureTenant(t, st, "borrowing-tenant")
	own, err := m.CreateProfile(context.Background(), tenantA, CreateProfileInput{Driver: "claude", AuthSource: AuthSourceAccountHome})
	if err != nil {
		t.Fatalf("own-login profile in A: %v", err)
	}
	_, err = m.CreateProfile(context.Background(), tenantB, CreateProfileInput{
		Driver: "claude", AuthSource: AuthSourceAccountHome, ConfigHome: own.ConfigHome, UserHome: t.TempDir(),
	})
	if statusOf(err) != http.StatusForbidden || !strings.Contains(err.Error(), "another organization") {
		t.Fatalf("B registering A's login = %v, want 403 naming another organization", err)
	}
	// Nor may another tool of the same organization run on it.
	_, err = m.CreateProfile(context.Background(), tenantA, CreateProfileInput{
		Driver: "codex", AuthSource: AuthSourceAccountHome, ConfigHome: own.ConfigHome, UserHome: t.TempDir(),
	})
	if statusOf(err) != http.StatusForbidden || !strings.Contains(err.Error(), "another tool") {
		t.Fatalf("Codex registering Claude Code's login = %v, want 403 naming another tool", err)
	}
	// The owner's own profile still launches its snapshot.
	if _, _, err := m.resolveLaunchProfile(context.Background(), tenantA, own.Ref); err != nil {
		t.Fatalf("A's own profile: %v", err)
	}
}

// Root 2026-10-02 (09b): the resolve refusals carry a stable error.code beside the
// sentence, for GET and POST alike, so the console and `session start -o json` tell
// the known "nothing to run on yet" state from any other conflict.
func TestResolve_RefusalsCarryAStableCode(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := New(WithSessionWorkspaceRoot(t.TempDir()), WithProviderSecretVault(newFakeVault()))
	m.UseExecutionEnvironmentRef(testEnvRef)
	m.UseProfileHomesRoot(t.TempDir())
	m.UseToolLoginsRoot(t.TempDir())
	stub := &loginStub{installed: map[string]bool{"claude": true}, signedIn: map[string]bool{}}
	m.UseToolLoginStatus(stub.status)
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	for _, tc := range []struct{ driver, code, sentence string }{
		{"claude", resolveCodeNothingToRunOn, "add an Anthropic key in Providers"},
		{"codex", resolveCodeToolNotInstalled, "Install Codex first"},
	} {
		for _, r := range []resp{
			h.do("GET", "/v1/m/sessions/provider-profiles/resolve?driver="+tc.driver, admin, tenantHdr(tenant)),
			h.doJSON("POST", "/v1/m/sessions/provider-profiles/resolve", admin, map[string]any{"driver": tc.driver}, tenantHdr(tenant)),
		} {
			e, _ := r.body["error"].(map[string]any)
			if r.code != http.StatusConflict || e["code"] != tc.code || !strings.Contains(fmt.Sprint(e["message"]), tc.sentence) {
				t.Fatalf("%s refusal = %d %s, want 409 code %s and the sentence", tc.driver, r.code, r.raw, tc.code)
			}
		}
	}
}
