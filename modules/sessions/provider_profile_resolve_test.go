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
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Root, 2026-10-01 (acceptance, first-hour check/026/028): "which profile does a new session use"
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
	m.ToolLogin = stub.status
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
		"opencode": "OpenCode is not signed in and Providers has nothing it can use. Sign it in under AI tools, or add a key or a local model (Ollama) in Providers.",
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
	if _, err := m.ResolveProfile(context.Background(), tenant, "nonesuch"); statusOf(err) != http.StatusBadRequest {
		t.Errorf("unknown driver: %v, want 400", err)
	}
	t.Run("incompatible_key_is_not_a_missing_provider", func(t *testing.T) {
		m, tenant, _ := resolveHarness(t)
		addRecord(t, m, tenant, ProviderKindOpenAICompatible, "Gateway", "https://gateway.example.test/v1", "fixture-compatible-provider-key")
		want := "OpenCode runs only on an Anthropic, OpenAI or xAI key at the provider's own address, or on a local model (Ollama, or an OpenAI-compatible server at a local address)."
		for _, resolve := range []func(context.Context, model.TenantID, string) (ResolvedProfile, error){m.PreviewProfile, m.ResolveProfile} {
			_, err := resolve(context.Background(), tenant, "opencode")
			if statusOf(err) != http.StatusConflict || err.Error() != want {
				t.Errorf("incompatible key: %v, want 409 %q", err, want)
			}
		}
		profiles, _, err := m.ListProfiles(context.Background(), tenant, ProfileActive, model.Query{Limit: 50})
		if err != nil || len(profiles) != 0 {
			t.Fatalf("refusal wrote a profile: profiles=%d, err=%v", len(profiles), err)
		}
	})
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

// An openai_compatible record at a local address could not serve OpenCode before, so a tenant
// that has one (an LM Studio box used from Codex) and an Anthropic key added later got the key.
// Binding OpenCode to such a record must not move that default: the local server comes after
// the keys, is the answer when nothing else can run the tool, and a profile can still name it.
func TestResolve_ALocalOpenAICompatibleServerNeverTakesOverFromAKeyForOpenCode(t *testing.T) {
	m, tenant, _ := resolveHarness(t)
	local := addRecord(t, m, tenant, ProviderKindOpenAICompatible, "LM Studio", "http://192.168.1.5:1234/v1", "lm-studio-fixture-0123456789")
	got, err := m.ResolveProfile(context.Background(), tenant, "opencode")
	if err != nil || got.Profile.ProviderRecordRef != local.Ref {
		t.Fatalf("with only the local server, opencode resolve = %+v %v, want it", got, err)
	}
	key := addRecord(t, m, tenant, ProviderKindAnthropic, "Team key", "", "sk-ant-fixture-0123456789")
	got, err = m.ResolveProfile(context.Background(), tenant, "opencode")
	if err != nil || got.Provider == nil || got.Provider.Ref != key.Ref {
		t.Fatalf("with an older local server and a newer key, opencode resolve = %+v %v, want the key", got, err)
	}
	// Codex has bound such records all along and keeps its order: its own vendor first, then oldest.
	codex, err := m.ResolveProfile(context.Background(), tenant, "codex")
	if err != nil || codex.Provider == nil || codex.Provider.Ref != local.Ref {
		t.Fatalf("codex resolve = %+v %v, want the older openai_compatible record as before", codex, err)
	}
}

// writeCounter counts the read-write transactions a module opens.
type writeCounter struct {
	api.ModuleData
	mutates atomic.Int32
}

func (w *writeCounter) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	w.mutates.Add(1)
	return w.ModuleData.Mutate(ctx, tenant, fn)
}

// A launch with no named profile resolves one every time. The profile that already
// exists is found in a read transaction: the account-home write lock is taken only
// to create (B4.10b). The answer is the same for both sources of the rule: a signed-in
// tool keeps its own-login profile, a signed-out tool its key profile.
func TestResolve_AnExistingProfileIsReusedWithoutAWriteTransaction(t *testing.T) {
	for _, tc := range []struct {
		name     string
		signedIn bool
		reason   string
		source   string
	}{
		{"signed in", true, ResolveOwnLogin, AuthSourceAccountHome},
		{"signed out", false, ResolveAPIKey, AuthSourceManagedInjection},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, tenant, stub := resolveHarness(t)
			stub.signedIn["claude"] = tc.signedIn
			key := addRecord(t, m, tenant, ProviderKindAnthropic, "Team key", "", "sk-ant-fixture-0123456789")
			first, err := m.ResolveProfile(context.Background(), tenant, "claude")
			if err != nil || !first.Created {
				t.Fatalf("first resolve = %+v %v, want a created profile", first, err)
			}
			writes := &writeCounter{ModuleData: m.Data}
			m.UseData(writes)
			again, err := m.ResolveProfile(context.Background(), tenant, "claude")
			if err != nil {
				t.Fatalf("second resolve: %v", err)
			}
			if again.Reason != tc.reason || again.Profile.AuthSource != tc.source {
				t.Fatalf("second resolve = %+v, want the first profile reused (%s, %s)", again, tc.reason, tc.source)
			}
			if want := (ResolvedProfile{Profile: first.Profile, Reason: first.Reason, Provider: first.Provider}); !reflect.DeepEqual(again, want) {
				t.Fatalf("second resolve = %+v, want the first answer without Created: %+v", again, want)
			}
			if !tc.signedIn && (again.Provider == nil || again.Provider.Ref != key.Ref) {
				t.Fatalf("signed-out resolve names provider %+v, want the key %s", again.Provider, key.Ref)
			}
			if n := writes.mutates.Load(); n != 0 {
				t.Fatalf("a reused profile opened %d write transactions, want 0", n)
			}
		})
	}
}

// raceOnMutate runs competing before the first write transaction opens: the profile
// another resolve makes between this one's read and its lock.
type raceOnMutate struct {
	api.ModuleData
	once      sync.Once
	competing func()
}

func (r *raceOnMutate) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	r.once.Do(r.competing)
	return r.ModuleData.Mutate(ctx, tenant, fn)
}

// The read finds nothing, a competing resolve then makes the profile, and the one
// under the lock finds it: still one profile, and this resolve made none.
func TestResolve_AProfileMadeBetweenTheReadAndTheLockIsReusedNotDuplicated(t *testing.T) {
	for _, signedIn := range []bool{true, false} {
		t.Run(fmt.Sprintf("signed_in=%v", signedIn), func(t *testing.T) {
			m, tenant, stub := resolveHarness(t)
			stub.signedIn["claude"] = signedIn
			addRecord(t, m, tenant, ProviderKindAnthropic, "Team key", "", "sk-ant-fixture-0123456789")
			inner := m.Data
			var competitor ResolvedProfile
			m.UseData(&raceOnMutate{ModuleData: inner, competing: func() {
				m.UseData(inner) // the competing resolve opens its own transactions
				var err error
				if competitor, err = m.ResolveProfile(context.Background(), tenant, "claude"); err != nil || !competitor.Created {
					t.Errorf("competing resolve = %+v %v, want a created profile", competitor, err)
				}
			}})
			got, err := m.ResolveProfile(context.Background(), tenant, "claude")
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			if got.Created || got.Profile.Ref != competitor.Profile.Ref {
				t.Fatalf("resolve = %+v, want the competing profile %s reused", got, competitor.Profile.Ref)
			}
			all, _, err := m.ListProfiles(context.Background(), tenant, ProfileActive, model.Query{Limit: 50})
			if err != nil || len(all) != 1 {
				t.Fatalf("active profiles = %d (%v), want 1", len(all), err)
			}
		})
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

// The shape CLX's CLI reads (first-hour check): the profile object, the reason, the provider
// for a key, created; a refusal is 409 with the module's error body.
func TestResolve_HTTPShape(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := New(WithSessionWorkspaceRoot(t.TempDir()), WithProviderSecretVault(newFakeVault()))
	m.UseExecutionEnvironmentRef(testEnvRef)
	m.UseProfileHomesRoot(t.TempDir())
	m.UseToolLoginsRoot(t.TempDir())
	stub := &loginStub{installed: map[string]bool{"claude": true}, signedIn: map[string]bool{"claude": true}}
	m.ToolLogin = stub.status
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
		r.body["reason"] != "own_login" || r.body["created"] != nil {
		t.Fatalf("preview = %d %s, want the reason and no created", r.code, r.raw)
	} else if got, _ := r.body["profile"].(map[string]any); got["profile_ref"] != prof["profile_ref"] {
		t.Fatalf("preview profile = %v, want the one the resolve made (%v)", r.body["profile"], prof["profile_ref"])
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

func TestResolve_NeverLaunchedKeyPreviewsModelChoiceWithoutWritingOrProbing(t *testing.T) {
	probe := &fakeProbe{}
	m := New(WithProviderSecretVault(newFakeVault()), WithProviderProbe(probe), WithProviderDriver(NewCodexDriver()))
	m.UseExecutionEnvironmentRef(testEnvRef)
	m.UseProfileHomesRoot(t.TempDir())
	m.UseToolLoginsRoot(t.TempDir())
	stub := &loginStub{installed: map[string]bool{"codex": true}, signedIn: map[string]bool{}}
	m.ToolLogin = stub.status
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "first-start")
	rec := mustCreateRecord(t, m, tenant, CreateProviderRecordInput{Kind: ProviderKindOpenAI,
		DisplayName: "Never launched", APIKey: testProviderKey})
	for _, saved := range []string{"", "coding-model"} {
		if saved != "" {
			if _, err := m.PatchProviderRecord(t.Context(), tenant, rec.Ref, ProviderRecordPatch{DefaultModel: &saved}); err != nil {
				t.Fatal(err)
			}
		}
		r := h.do("GET", "/v1/m/sessions/provider-profiles/resolve?driver=codex", admin, tenantHdr(tenant))
		provider, _ := r.body["provider"].(map[string]any)
		if r.code != http.StatusOK || r.body["model_required"] != true || provider["provider_ref"] != rec.Ref || provider["default_model"] != saved {
			t.Fatalf("never-launched key model preview = %d %s", r.code, r.raw)
		}
		profiles, _, err := m.ListProfiles(t.Context(), tenant, ProfileActive, model.Query{Limit: 50})
		if err != nil || len(profiles) != 0 || probe.calls != 0 {
			t.Fatalf("preview wrote profiles or probed: profiles=%d probes=%d err=%v", len(profiles), probe.calls, err)
		}
	}
	stub.signedIn["codex"] = true
	r := h.do("GET", "/v1/m/sessions/provider-profiles/resolve?driver=codex", admin, tenantHdr(tenant))
	if r.code != http.StatusOK || r.body["model_required"] != nil || r.body["provider"] != nil {
		t.Fatalf("own-login preview acquired bound model rules: %d %s", r.code, r.raw)
	}
}

// Root on first-hour check: the own login is the tenant's, in the home the product made for it
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

// Root on first-hour check: a profile made before (its configuration home is the engine user's
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

// Cursor also stores its credential below HOME, so a profile whose UserHome is the
// engine user's HOME is a server-user login even when CURSOR_CONFIG_DIR points elsewhere.
func TestCursorProfileOnTheServerUsersHomeIsShownAndNotUsed(t *testing.T) {
	for _, tc := range []struct {
		name  string
		alias bool
	}{{"direct", false}, {"symlink alias", true}} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			if tc.alias {
				alias := filepath.Join(t.TempDir(), "engine-home")
				if err := os.Symlink(home, alias); err != nil {
					t.Fatal(err)
				}
				home = alias
			}
			t.Setenv("HOME", home)
			m, _, tenant, _ := newRuntimeHarness(t, WithProviderSecretVault(newFakeVault()), WithProviderDriver(NewCursorDriver()))
			m.UseExecutionEnvironmentRef(testEnvRef)
			old, err := m.CreateProfile(context.Background(), tenant, CreateProfileInput{
				Driver: "cursor", AuthSource: AuthSourceAccountHome, ConfigHome: t.TempDir(), UserHome: home,
			})
			if err != nil {
				t.Fatalf("the earlier profile: %v", err)
			}
			if !m.toProfileDTO(old).ServerUserLogin {
				t.Fatal("the Cursor profile on the server user's HOME is not marked")
			}
			_, err = m.snapshotForLaunch(tenant, old, m.ExecutionEnvironmentRef())
			if statusOf(err) != http.StatusConflict || err.Error() != serverUserLoginSentence {
				t.Fatalf("launch on it = %v, want 409 %q", err, serverUserLoginSentence)
			}
		})
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
	m.ToolLogin = stub.status
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

// Both preview and resolve identify unreadable sign-in separately from a store refusal.
func TestResolve_UnreadableSignInCarriesAStableCode(t *testing.T) {
	for _, missing := range []bool{true, false} {
		t.Run(fmt.Sprintf("reader_missing=%v", missing), func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			m := New(WithSessionWorkspaceRoot(t.TempDir()), WithProviderSecretVault(newFakeVault()))
			if !missing {
				m.ToolLogin = func(context.Context, model.TenantID, string) (bool, bool, error) {
					return false, false, fmt.Errorf("fixture login reader failure")
				}
			}
			h := newHarness(t, m)
			admin := h.adminLogin()
			tenant := h.createOrg(admin, "acme")
			for _, r := range []resp{
				h.do("GET", "/v1/m/sessions/provider-profiles/resolve?driver=claude", admin, tenantHdr(tenant)),
				h.doJSON("POST", "/v1/m/sessions/provider-profiles/resolve", admin, map[string]any{"driver": "claude"}, tenantHdr(tenant)),
			} {
				e, _ := r.body["error"].(map[string]any)
				if r.code != http.StatusServiceUnavailable || e["code"] != "tool_signin_unreadable" || !strings.Contains(fmt.Sprint(e["message"]), "this node") {
					t.Fatalf("refusal = %d %s, want 503 tool_signin_unreadable and the sentence", r.code, r.raw)
				}
			}
		})
	}
}

// AI tools > Installed here lists what a new session receives: the preview names the
// profile a launch would reuse, with the programs and registries it declares, and
// still writes nothing. Before a profile exists it names none: the launch creates one
// that declares neither.
func TestResolve_PreviewNamesTheProfileALaunchReuses(t *testing.T) {
	m, tenant, stub := resolveHarness(t)
	ctx := context.Background()
	stub.signedIn["claude"] = true
	if got, err := m.PreviewProfile(ctx, tenant, "claude"); err != nil || got.Profile.Ref != "" {
		t.Fatalf("preview before any profile = %+v %v, want none", got, err)
	}
	// A read: not even the own-login folder the launch makes is created.
	if _, cfg, _ := m.OwnToolLoginHomes(tenant, "claude"); cfg == "" {
		t.Fatal("no own-login folder for claude")
	} else if _, err := os.Stat(cfg); !os.IsNotExist(err) {
		t.Fatalf("the preview created %s (%v)", cfg, err)
	}
	resolved, err := m.ResolveProfile(ctx, tenant, "claude")
	if err != nil {
		t.Fatal(err)
	}
	programs := []string{"jq"}
	regs := []SessionRegistry{{Kind: "npm", URL: "https://npm.acme.example/", Scope: "@acme"}}
	if _, err := m.PatchProfile(ctx, tenant, resolved.Profile.Ref, ProfilePatch{
		SessionPrograms: &programs, SessionRegistries: &regs, MayUseSecretEnv: true,
	}); err != nil {
		t.Fatal(err)
	}
	got, err := m.PreviewProfile(ctx, tenant, "claude")
	if err != nil || got.Profile.Ref != resolved.Profile.Ref || got.Created ||
		!slices.Equal(got.Profile.SessionPrograms, programs) || !slices.Equal(got.Profile.SessionRegistries, regs) {
		t.Fatalf("preview = %+v %v, want profile %s with its programs and registries", got.Profile, err, resolved.Profile.Ref)
	}
	if all, _, err := m.ListProfiles(ctx, tenant, ProfileActive, model.Query{Limit: 50}); err != nil || len(all) != 1 {
		t.Fatalf("profiles after the previews = %d (%v), want 1", len(all), err)
	}
	// A node with no execution environment has no profile to reuse: the preview still
	// answers (the launch refuses with its own error), and readiness does not read it.
	m.UseExecutionEnvironmentRef("")
	if got, err := m.PreviewProfile(ctx, tenant, "claude"); err != nil || got.Reason != ResolveOwnLogin || got.Profile.Ref != "" {
		t.Fatalf("preview without an environment = %+v %v, want the reason and no profile", got, err)
	}
	ready, err := m.ToolsReadiness(ctx, tenant)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range ready {
		if tool.Driver == "claude" && (!tool.Ready || tool.Reason != ResolveOwnLogin) {
			t.Fatalf("claude readiness without an environment = %+v, want ready on its own login", tool)
		}
	}
}

// Cursor keeps its login under HOME (<HOME>/.config/cursor), not under CURSOR_CONFIG_DIR, so a
// session on the tenant's own login must run with the login's HOME: with any other it finds no
// login and the CLI answers that it must sign in (found running a Cursor session on a login made
// through Olivares). OpenCode, whose login also lives under HOME, has the same rule.
func TestResolve_CursorOwnLoginRunsInTheHomeTheSignInWrote(t *testing.T) {
	m, tenant, stub := resolveHarness(t)
	stub.installed["cursor"], stub.signedIn["cursor"] = true, true
	got, err := m.ResolveProfile(context.Background(), tenant, "cursor")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	home, configDir, ok := ToolLoginHome(m.toolLoginsRoot, tenant, "cursor")
	if !ok {
		t.Fatal("cursor has no login home")
	}
	if got.Reason != ResolveOwnLogin || got.Profile.UserHome != home || got.Profile.ConfigHome != configDir {
		t.Fatalf("profile homes = user %q config %q (reason %s), want the sign-in's HOME %q and %q", got.Profile.UserHome, got.Profile.ConfigHome, got.Reason, home, configDir)
	}
	// A tool whose login lives in its configuration directory keeps the profile's own HOME.
	stub.signedIn["claude"] = true
	claude, err := m.ResolveProfile(context.Background(), tenant, "claude")
	if err != nil {
		t.Fatalf("resolve claude: %v", err)
	}
	if claudeHome, _, _ := ToolLoginHome(m.toolLoginsRoot, tenant, "claude"); claude.Profile.UserHome == claudeHome {
		t.Fatalf("claude's profile HOME became the login home %q", claudeHome)
	}
}

func TestDefaultToolLoginHome_CursorUsesTheExistingProfilesUserHome(t *testing.T) {
	m, _, tenant, _ := newRuntimeHarness(t, WithProviderSecretVault(newFakeVault()), WithProviderDriver(NewCursorDriver()))
	m.UseExecutionEnvironmentRef(testEnvRef)
	m.UseProfileHomesRoot(t.TempDir())
	m.UseToolLoginsRoot(t.TempDir())
	defaultHome, configDir, ok := ToolLoginHome(m.toolLoginsRoot, tenant, "cursor")
	if !ok {
		t.Fatal("cursor has no login home")
	}
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	profileHome := t.TempDir()
	profile, err := m.CreateProfile(context.Background(), tenant, CreateProfileInput{
		Driver: "cursor", AuthSource: AuthSourceAccountHome, ConfigHome: configDir, UserHome: profileHome,
	})
	if err != nil {
		t.Fatal(err)
	}
	home, gotConfig, err := m.DefaultToolLoginHome(context.Background(), tenant, "cursor")
	if err != nil {
		t.Fatal(err)
	}
	if home != profileHome || gotConfig != profile.ConfigHome || home == defaultHome {
		t.Fatalf("default login homes = user %q config %q, want the existing profile's %q and %q (not the fresh %q)", home, gotConfig, profileHome, profile.ConfigHome, defaultHome)
	}
}

func TestResolve_CursorSignedOutNamesTheOfficialSignIn(t *testing.T) {
	m, tenant, stub := resolveHarness(t)
	stub.installed["cursor"] = true
	_, err := m.ResolveProfile(context.Background(), tenant, "cursor")
	coded, ok := err.(*codedRunErr)
	if !ok || coded.code != resolveCodeNothingToRunOn {
		t.Fatalf("signed-out resolve error = %T %v, want nothing_to_run_on", err, err)
	}
	if !strings.Contains(coded.msg, "Sign it in under AI tools") || strings.Contains(coded.msg, "--profile") {
		t.Fatalf("signed-out message = %q, want Cursor's official sign-in, not a manual profile workaround", coded.msg)
	}
}
