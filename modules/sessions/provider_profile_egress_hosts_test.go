// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"crypto/sha256"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// dev#2154: an API-key session's network admits only its provider's endpoint; a
// tenant administrator can declare the hosts it may also reach. The hosts take
// the registries' route into the launch's egress policy, none is added by
// default, and each is admitted only at public addresses.

var githubHosts = []string{"github.com", "api.github.com"}

func declareEgressHosts(t *testing.T, m *Module, tenant model.TenantID, ref string, hosts []string) {
	t.Helper()
	if _, err := m.PatchProfile(context.Background(), tenant, ref, ProfilePatch{SessionEgressHosts: &hosts, MayUseSecretEnv: true}); err != nil {
		t.Fatalf("declare the profile's egress hosts: %v", err)
	}
}

// The declared hosts reach the confined session's policy beside its provider; a
// launch on a profile with none gets exactly the policy it had.
func TestProfileEgressHosts_TheLaunchGrantsTheDeclaredHostsOnly(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	m, tenant, _, _ := registriesHarness(t)
	ref := ensureRuntimeTestProfileRef(t, m, tenant)
	declareEgressHosts(t, m, tenant, ref, []string{"API.GitHub.com", "github.com", "github.com"})

	_, policy, err := m.resolveLaunchProfile(ctx, tenant, ref)
	if err != nil {
		t.Fatal(err)
	}
	p := CreateRunParams{Isolation: IsolationNative}
	if err := applySessionPolicy(&p, providerDriverClaude, policy); err != nil {
		t.Fatal(err)
	}
	// The policy a launch on a GLM key gets from the launch's own constructor.
	const key = "glm-key-for-the-pin"
	bound := BoundProvider{Kind: ProviderKindAnthropic, Endpoint: "https://api.z.ai/api/anthropic"}
	spec := LaunchSpec{NetworkPolicy: sessionNetworkPolicy(providerDriverClaude, bound, nil,
		[]EnvVar{{Name: providerRecordKeyEnv(ProviderKindAnthropic), Value: key}})}
	if spec.NetworkPolicy == nil {
		t.Fatal("a Claude Code session on an API-key provider has no network boundary")
	}
	if err := m.configureProfilePackages(ctx, ctx, tenant, "run_1", &p, &spec); err != nil {
		t.Fatal(err)
	}
	if want := []string{"https://api.github.com/", "https://github.com/"}; !slices.Equal(spec.NetworkPolicy.PublicProviders, want) {
		t.Fatalf("public providers = %v, want %v", spec.NetworkPolicy.PublicProviders, want)
	}
	if want := []string{bound.Endpoint}; !slices.Equal(spec.NetworkPolicy.Providers, want) {
		t.Fatalf("the bound provider list changed: %v, want %v", spec.NetworkPolicy.Providers, want)
	}
	if spec.NetworkPolicy.CredentialSHA256 != sha256.Sum256([]byte(key)) {
		t.Fatal("the session's key pin was lost when hosts were declared")
	}
	if !strings.Contains(p.egressDetail, "egress hosts api.github.com,github.com") {
		t.Fatalf("the ledger does not name the hosts: %q", p.egressDetail)
	}

	// No declaration: the policy is the provider's alone, as before.
	none := CreateRunParams{Isolation: IsolationNative}
	plain := LaunchSpec{NetworkPolicy: sessionNetworkPolicy(providerDriverClaude, bound, nil, nil)}
	if err := m.configureProfilePackages(ctx, ctx, tenant, "run_2", &none, &plain); err != nil {
		t.Fatal(err)
	}
	if len(plain.NetworkPolicy.PublicProviders) != 0 || none.egressDetail != "" {
		t.Fatalf("a profile without hosts got some: %+v %q", plain.NetworkPolicy, none.egressDetail)
	}

	// An unconfined session keeps the host network: no boundary is invented.
	open := LaunchSpec{}
	q := CreateRunParams{Isolation: IsolationNative, profileEgressHosts: githubHosts}
	if err := m.configureProfilePackages(ctx, ctx, tenant, "run_3", &q, &open); err != nil {
		t.Fatal(err)
	}
	if open.NetworkPolicy != nil {
		t.Fatalf("declared hosts confined an unconfined session: %+v", open.NetworkPolicy)
	}
}

// A resume reads the CURRENT declaration, like a new launch: the profile is
// resolved again each time, so a withdrawn host is gone and a new one is there.
func TestProfileEgressHosts_EachLaunchReadsTheCurrentDeclaration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	m, tenant, _, _ := registriesHarness(t)
	ref := ensureRuntimeTestProfileRef(t, m, tenant)
	hostsAt := func() []string {
		t.Helper()
		_, policy, err := m.resolveLaunchProfile(ctx, tenant, ref)
		if err != nil {
			t.Fatal(err)
		}
		p := CreateRunParams{Isolation: IsolationNative}
		if err := applySessionPolicy(&p, providerDriverClaude, policy); err != nil {
			t.Fatal(err)
		}
		return p.profileEgressHosts
	}
	if got := hostsAt(); len(got) != 0 {
		t.Fatalf("a profile that declared nothing launches with %v", got)
	}
	declareEgressHosts(t, m, tenant, ref, []string{"github.com"})
	if got := hostsAt(); !slices.Equal(got, []string{"github.com"}) {
		t.Fatalf("the declared hosts = %v", got)
	}
	declareEgressHosts(t, m, tenant, ref, []string{"proxy.golang.org"})
	if got := hostsAt(); !slices.Equal(got, []string{"proxy.golang.org"}) {
		t.Fatalf("after a change the next launch reads %v", got)
	}
	declareEgressHosts(t, m, tenant, ref, []string{})
	if got := hostsAt(); len(got) != 0 {
		t.Fatalf("a withdrawn declaration still reaches the next launch: %v", got)
	}
}

// The account view carries the declaration, from the profile it is.
func TestProfileEgressHosts_TheAccountDTOCarriesThem(t *testing.T) {
	t.Parallel()
	dto := toProviderAccountDTO(ProviderAccount{ProviderProfile: ProviderProfile{SessionEgressHosts: githubHosts}})
	if !slices.Equal(dto.SessionEgressHosts, githubHosts) {
		t.Fatalf("account DTO hosts = %v, want %v", dto.SessionEgressHosts, githubHosts)
	}
}

// A container launch cannot carry the native session's policy: refused by name.
func TestProfileEgressHosts_ANonNativeLaunchIsRefused(t *testing.T) {
	t.Parallel()
	m, tenant, _, _ := registriesHarness(t)
	p := CreateRunParams{Isolation: IsolationContainer, profileEgressHosts: githubHosts}
	err := m.configureProfilePackages(context.Background(), context.Background(), tenant, "run_1", &p, &LaunchSpec{})
	if statusOf(err) != http.StatusUnprocessableEntity || !strings.Contains(err.Error(), "egress hosts") {
		t.Fatalf("container launch with egress hosts = %v, want 422 naming them", err)
	}
}

// Declaring or withdrawing hosts widens a session's network: it needs the tenant
// administration secret_env needs, as registries do.
func TestProfileEgressHosts_DeclaringNeedsTenantAdministration(t *testing.T) {
	t.Parallel()
	m, tenant, _, _ := registriesHarness(t)
	ref := ensureRuntimeTestProfileRef(t, m, tenant)
	hosts := githubHosts
	for name, patch := range map[string]ProfilePatch{
		"a declaration": {SessionEgressHosts: &hosts},
		"a withdrawal":  {SessionEgressHosts: &[]string{}},
	} {
		if _, err := m.PatchProfile(context.Background(), tenant, ref, patch); statusOf(err) != http.StatusForbidden {
			t.Errorf("%s by a non-administrator: %v, want 403", name, err)
		}
	}
	if _, err := m.CreateProfile(context.Background(), tenant, CreateProfileInput{
		Driver: "claude", ConfigHome: t.TempDir(), UserHome: t.TempDir(), DisplayName: "hosts", SessionEgressHosts: hosts,
	}); statusOf(err) != http.StatusForbidden {
		t.Errorf("a create with hosts by a non-administrator: %v, want 403", err)
	}
	got, err := m.GetProfile(context.Background(), tenant, ref)
	if err != nil || len(got.SessionEgressHosts) != 0 {
		t.Fatalf("a refused declaration was stored: %v %v", got.SessionEgressHosts, err)
	}
}

func TestProfileEgressHosts_Validation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, in string }{
		{"scheme", "https://github.com"},
		{"port", "github.com:443"},
		{"path", "github.com/olivaresai"},
		{"wildcard", "*.github.com"},
		{"one label", "localhost"},
		{"address", "10.0.0.5"},
		{"metadata address", "169.254.169.254"},
		{"v6 address", "[::1]"},
		{"numeric last label", "github.com.1"},
		{"userinfo", "user@github.com"},
		{"leading hyphen", "-github.com"},
		{"empty label", "github..com"},
		{"space inside", "git hub.com"},
		{"control character", "github.com\r\nx.example"},
		{"empty", ""},
	} {
		if _, err := normalizeSessionEgressHosts([]string{tc.in}); err == nil || statusOf(err) != http.StatusBadRequest {
			t.Errorf("%s (%q): %v, want 400", tc.name, tc.in, err)
		}
	}
	if stored, err := normalizeSessionEgressHosts([]string{"a.example", "b.example", "c.example", "d.example", "a.example"}); err != nil ||
		stored != `["a.example","b.example","c.example","d.example"]` {
		t.Errorf("four distinct hosts and a repeat = %s %v, want them accepted", stored, err)
	}
	if _, err := normalizeSessionEgressHosts([]string{"a.example", "b.example", "c.example", "d.example", "e.example"}); err == nil ||
		!strings.Contains(err.Error(), "at most 4") {
		t.Errorf("five hosts: %v, want the limit named", err)
	}
	stored, err := normalizeSessionEgressHosts([]string{" Registry.Example.COM ", "bücher.example", "github.com", "github.com.", "github.com"})
	if err != nil || stored != `["github.com","registry.example.com","xn--bcher-kva.example"]` {
		t.Fatalf("normalized = %s %v", stored, err)
	}
	if stored, err := normalizeSessionEgressHosts(nil); err != nil || stored != "" {
		t.Fatalf("none = %q %v", stored, err)
	}
}

// Every change is in the ledger, with the actor and the resulting hosts, in the
// transaction that stored them; a withdrawal is recorded too.
func TestProfileEgressHosts_ChangesAreAudited(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	m, tenant, _, _ := registriesHarness(t)
	operator, err := auth.NewSystemOperator("test:egress-hosts-operator", "declare a profile's egress hosts")
	if err != nil {
		t.Fatal(err)
	}
	ref := ensureRuntimeTestProfileRef(t, m, tenant)
	hosts := githubHosts
	if _, err := m.PatchProfile(ctx, tenant, ref, ProfilePatch{SessionEgressHosts: &hosts, MayUseSecretEnv: true, WorkGrantActor: operator}); err != nil {
		t.Fatal(err)
	}
	declareEgressHosts(t, m, tenant, ref, []string{})
	events := acctAuditsOf(t, m, tenant, "sessions.provider_profile.egress_hosts")
	if len(events) != 2 {
		t.Fatalf("audit events = %d, want a declaration and a withdrawal", len(events))
	}
	if events[0].actor != operator.Actor() || events[0].actor == "" {
		t.Fatalf("the declaration's actor = %q, want the caller %q", events[0].actor, operator.Actor())
	}
	// A profile created with hosts is in the ledger from its first moment.
	created, err := m.CreateProfile(ctx, tenant, CreateProfileInput{
		Driver: "claude", ConfigHome: t.TempDir(), UserHome: t.TempDir(), DisplayName: "born with hosts",
		SessionEgressHosts: []string{"github.com"}, MayUseSecretEnv: true, WorkGrantActor: operator,
	})
	if err != nil {
		t.Fatal(err)
	}
	events = acctAuditsOf(t, m, tenant, "sessions.provider_profile.egress_hosts")
	if len(events) != 3 || events[2].meta["profile_ref"] != created.Ref || events[2].actor != operator.Actor() {
		t.Fatalf("a created profile's declaration is not in the ledger: %+v", events)
	}
	events = events[:2]
	declared, _ := events[0].meta["hosts"].([]any)
	withdrawn, ok := events[1].meta["hosts"].([]any)
	if len(declared) != 2 || declared[0] != "api.github.com" || !ok || len(withdrawn) != 0 || events[0].meta["profile_ref"] != ref {
		t.Fatalf("recorded hosts = %v then %v (%v)", events[0].meta, events[1].meta, ok)
	}
	// A change that names no hosts leaves the ledger alone.
	name := "renamed"
	if _, err := m.PatchProfile(context.Background(), tenant, ref, ProfilePatch{DisplayName: &name}); err != nil {
		t.Fatal(err)
	}
	if got := len(acctAuditsOf(t, m, tenant, "sessions.provider_profile.egress_hosts")); got != 3 {
		t.Fatalf("a rename wrote an egress-host event: %d", got)
	}
}

// A corrupt stored list refuses the launch rather than reading as none.
func TestProfileEgressHosts_AnUnreadableDeclarationRefusesTheLaunch(t *testing.T) {
	t.Parallel()
	if _, _, _, ok := decodeProfilePackages(model.Record{colPPSessionEgressHosts: `{"github.com":1}`}); ok {
		t.Fatal("a corrupt egress host declaration read as valid")
	}
	_, _, hosts, ok := decodeProfilePackages(model.Record{colPPSessionEgressHosts: `["github.com"]`})
	if !ok || !slices.Equal(hosts, []string{"github.com"}) {
		t.Fatalf("a stored declaration = %v %v", hosts, ok)
	}
	// A row written outside the API is read as a declaration is checked.
	for _, stored := range []string{`["GitHub.com"]`, `["github.com:8443"]`, `["10.0.0.5"]`, `["a/b.example"]`, `["localhost"]`} {
		if _, _, _, ok := decodeProfilePackages(model.Record{colPPSessionEgressHosts: stored}); ok {
			t.Errorf("stored %s read as a valid declaration", stored)
		}
	}
	p := CreateRunParams{}
	if err := applySessionPolicy(&p, providerDriverClaude, sessionPolicy{unreadable: true}); statusOf(err) != http.StatusConflict {
		t.Fatalf("an unreadable declaration = %v, want 409", err)
	}
}

// The HTTP surface: an administrator declares and reads them back; without the
// tenant administration the same call is 403 and nothing is stored.
func TestProfileEgressHosts_TheAPIAsksForTenantAdministration(t *testing.T) {
	m := New()
	m.UseExecutionEnvironmentRef(testEnvRef)
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "egress-hosts")
	created := h.doJSON(http.MethodPost, "/v1/m/sessions/provider-profiles", admin, map[string]any{
		"driver": "claude", "config_home": t.TempDir(), "user_home": t.TempDir(),
		"session_egress_hosts": []string{"github.com", "api.github.com"}}, tenantHdr(tenant))
	if created.code != http.StatusCreated || !strings.Contains(created.raw, `"session_egress_hosts":["api.github.com","github.com"]`) {
		t.Fatalf("create by a tenant administrator = %d %s", created.code, created.raw)
	}
	ref := created.body["profile_ref"].(string)
	bad := h.doJSON(http.MethodPatch, "/v1/m/sessions/provider-profiles/"+ref, admin,
		map[string]any{"session_egress_hosts": []string{"https://github.com"}}, tenantHdr(tenant))
	if bad.code != http.StatusBadRequest {
		t.Fatalf("a URL as a host = %d %s, want 400", bad.code, bad.raw)
	}
	m.WorkAuthorizer = nil
	patched := h.doJSON(http.MethodPatch, "/v1/m/sessions/provider-profiles/"+ref, admin,
		map[string]any{"session_egress_hosts": []string{"example.com"}}, tenantHdr(tenant))
	if patched.code != http.StatusForbidden {
		t.Fatalf("patch without tenant administration = %d %s, want 403", patched.code, patched.raw)
	}
	got := h.doJSON(http.MethodGet, "/v1/m/sessions/provider-profiles/"+ref, admin, nil, tenantHdr(tenant))
	if got.code != http.StatusOK || !strings.Contains(got.raw, `"session_egress_hosts":["api.github.com","github.com"]`) {
		t.Fatalf("the refused patch changed the profile: %d %s", got.code, got.raw)
	}
}

// The column is nullable: a profile stored before it declares none, and a
// declaration survives a reopen on both backends.
func TestProfileEgressHostsNullableUpgradeBothBackends(t *testing.T) {
	for _, backend := range profileBackends(t) {
		t.Run(backend.name, func(t *testing.T) {
			ctx := context.Background()
			oldModule := New()
			m, st := openProfileModule(t, backend, func(reg store.ExtensionRegistry) error {
				return oldModule.RegisterSchema(beforeProfileEgressHostsRegistry{reg})
			})
			tenant := ensureTenant(t, st, "profile-egress-upgrade")
			profile := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: "claude", ConfigHome: t.TempDir(), UserHome: t.TempDir()})
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			m, st = openProfileModule(t, backend, nil)
			defer st.Close()
			upgraded, err := m.GetProfile(ctx, tenant, profile.Ref)
			if err != nil || upgraded.SessionEgressHosts != nil || upgraded.packagesUnreadable {
				t.Fatalf("the upgrade changed the existing profile: %+v %v", upgraded, err)
			}
			hosts := githubHosts
			if _, err := m.PatchProfile(ctx, tenant, profile.Ref, ProfilePatch{SessionEgressHosts: &hosts, MayUseSecretEnv: true}); err != nil {
				t.Fatal(err)
			}
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			m, st = openProfileModule(t, backend, nil)
			defer st.Close()
			persisted, err := m.GetProfile(ctx, tenant, profile.Ref)
			if err != nil || !slices.Equal(persisted.SessionEgressHosts, []string{"api.github.com", "github.com"}) {
				t.Fatalf("the declaration did not survive reopen: %+v %v", persisted.SessionEgressHosts, err)
			}
		})
	}
}

type beforeProfileEgressHostsRegistry struct{ store.ExtensionRegistry }

func (r beforeProfileEgressHostsRegistry) Register(d model.EntityDescriptor) error {
	if d.Kind == providerProfileKind {
		d.Fields = slices.DeleteFunc(slices.Clone(d.Fields), func(f model.FieldSpec) bool { return f.Name == colPPSessionEgressHosts })
	}
	return r.ExtensionRegistry.Register(d)
}
