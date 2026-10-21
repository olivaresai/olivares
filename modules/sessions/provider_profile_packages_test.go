// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sessions/confine"
	"github.com/olivaresai/olivares/modules/sessions/egress"
)

// dev#1517: a provider profile gives its sessions programs and package
// registries, through the launch's own files and the egress policy, never
// through env_allow and never with a token in the environment or argv.

const registryCanary = "npm_LEAKCANARY_registry_token_0123456789"

var acmeRegistry = SessionRegistry{Kind: "npm", URL: "https://npm.acme.example/api/npm/", Scope: "@acme", Secret: "env/npm-acme"}

func registriesHarness(t *testing.T, opts ...Option) (*Module, model.TenantID, *fakeRunner, string) {
	t.Helper()
	vault := newFakeVault()
	fr := &fakeRunner{}
	dataDir := t.TempDir()
	base := []Option{WithRunner(fr), WithCredentialSource(staticCred()), WithProviderSecretVault(vault), WithClaudeHookPEP(dataDir, "/bin/true")}
	m, _, tenant, _ := newRuntimeHarness(t, append(base, opts...)...)
	stopModuleAtCleanup(t, m)
	vault.values[tenant.String()+"|env/npm-acme"] = registryCanary
	return m, tenant, fr, dataDir
}

func declareRegistries(t *testing.T, m *Module, tenant model.TenantID, regs []SessionRegistry, programs []string) string {
	t.Helper()
	ref := ensureRuntimeTestProfileRef(t, m, tenant)
	if _, err := m.PatchProfile(context.Background(), tenant, ref, ProfilePatch{
		SessionRegistries: &regs, SessionPrograms: &programs, MayUseSecretEnv: true,
	}); err != nil {
		t.Fatalf("declare the profile's registries: %v", err)
	}
	return ref
}

func TestProfileRegistries_TheTokenReachesOnlyTheLaunchFile(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	m, tenant, fr, dataDir := registriesHarness(t)
	declareRegistries(t, m, tenant, []SessionRegistry{acmeRegistry, {Kind: "npm", URL: "https://registry.npmjs.org"}}, nil)

	dto, err := createProfiledTestRun(t, m, ctx, tenant, secretEnvParams(nil, false))
	if err != nil {
		t.Fatalf("launch on a profile with registries: %v", err)
	}
	spec := fr.lastSpec()
	path, ok := specEnvValue(spec, envNPMGlobalConfig)
	if !ok || !strings.HasPrefix(path, filepath.Join(dataDir, "run", dto.RunRef)+string(os.PathSeparator)) {
		t.Fatalf("npm_config_globalconfig = %q, want a file under the run's directory", path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "@acme:registry=https://npm.acme.example/api/npm/\n" +
		"//npm.acme.example/api/npm/:_authToken=\"" + registryCanary + "\"\n" +
		"registry=https://registry.npmjs.org/\n"
	if string(raw) != want {
		t.Fatalf("npm configuration =\n%s\nwant\n%s", raw, want)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Fatalf("npm configuration mode = %o, want 600", info.Mode().Perm())
	}
	for _, e := range spec.Env {
		if strings.Contains(e.Value, registryCanary) {
			t.Fatalf("the token reached the environment as %s", e.Name)
		}
	}
	for _, where := range append([]string{spec.Program, spec.Dir}, spec.Args...) {
		if strings.Contains(where, registryCanary) {
			t.Fatalf("the token reached the program, folder or argv: %q", where)
		}
	}
	row, _ := json.Marshal(dto)
	details := strings.Join(runEventDetails(t, m, tenant, dto.RunRef), "\n")
	if strings.Contains(string(row)+details, registryCanary) {
		t.Fatal("the run or its ledger carries the token")
	}
	if !strings.Contains(details, "registries @acme https://npm.acme.example/api/npm/,https://registry.npmjs.org/") {
		t.Fatalf("the ledger does not record the session's registries:\n%s", details)
	}

	// The output the plane stores has the token replaced by the secret's name.
	fr.lastProc().out <- OutputFrame{Stream: streamStderr, Data: []byte("npm verb token " + registryCanary)}
	lr, _ := m.rt.getLive(tenant, dto.RunRef)
	waitFor(t, "the frame in the ring", func() bool { return len(lr.ring.readFrom(0).frames) >= 1 })
	out := string(lr.ring.readFrom(0).frames[0].Data)
	if strings.Contains(out, registryCanary) || !strings.Contains(out, "[secret env/npm-acme]") {
		t.Fatalf("output = %q, want the token withheld by name", out)
	}
}

// A confined session's network gains exactly the registry hosts; an unconfined
// one keeps its network. The launch file goes when the launch ends.
func TestProfileRegistries_TheEgressPolicyGrantsTheRegistryAndTheFileGoes(t *testing.T) {
	t.Parallel()
	m, tenant, _, dataDir := registriesHarness(t)
	runCtx, cancel := context.WithCancel(context.Background())
	p := CreateRunParams{Isolation: IsolationNative, profileRegistries: []SessionRegistry{acmeRegistry}}
	spec := LaunchSpec{NetworkPolicy: &egress.Policy{Providers: []string{"https://api.anthropic.com/"}}, Confinement: &confine.Policy{}}
	if err := m.configureProfilePackages(context.Background(), runCtx, tenant, "run_1", &p, &spec); err != nil {
		t.Fatal(err)
	}
	if path, _ := specEnvValue(spec, envNPMGlobalConfig); !slices.Contains(spec.Confinement.ReadOnly, path) {
		t.Fatalf("a confined child cannot read its npm configuration: %+v", spec.Confinement)
	}
	if want := []string{"https://api.anthropic.com/", acmeRegistry.URL}; !slices.Equal(spec.NetworkPolicy.Providers, want) {
		t.Fatalf("confined providers = %v, want %v", spec.NetworkPolicy.Providers, want)
	}
	path, _ := specEnvValue(spec, envNPMGlobalConfig)
	cancel()
	waitFor(t, "the launch file removed", func() bool { _, err := os.Stat(path); return errors.Is(err, os.ErrNotExist) })

	other := LaunchSpec{}
	q := CreateRunParams{Isolation: IsolationNative}
	if err := m.configureProfilePackages(context.Background(), context.Background(), tenant, "run_2", &q, &other); err != nil {
		t.Fatal(err)
	}
	if _, ok := specEnvValue(other, envNPMGlobalConfig); ok || other.NetworkPolicy != nil {
		t.Fatalf("a profile without registries got a registry: %+v", other)
	}
	if entries, _ := os.ReadDir(filepath.Join(dataDir, "run", "run_2")); len(entries) != 0 {
		t.Fatalf("a profile without registries left files: %v", entries)
	}
}

func TestProfileRegistries_EnvAllowCannotForwardNPMSettings(t *testing.T) {
	t.Parallel()
	m, tenant, fr, _ := registriesHarness(t)
	declareRegistries(t, m, tenant, []SessionRegistry{acmeRegistry}, nil)
	for _, name := range []string{"npm_config_registry", "NPM_CONFIG_USERCONFIG"} {
		p := secretEnvParams(nil, false)
		p.EnvAllow = []string{name}
		_, err := createProfiledTestRun(t, m, context.Background(), tenant, p)
		var re *runErr
		if !errors.As(err, &re) || re.status != http.StatusBadRequest || !strings.Contains(re.msg, "env_allow names "+name) {
			t.Fatalf("env_allow %s beside registries: %v, want 400", name, err)
		}
	}
	fr.mu.Lock()
	launched := len(fr.specs)
	fr.mu.Unlock()
	if launched != 0 {
		t.Fatal("a refused launch reached the runner")
	}
}

// Programs and registries decide what every session under the profile runs and
// installs, widen its network and hand it a vault secret: declaring or
// withdrawing them needs the tenant administration secret_env needs.
func TestProfileRegistries_DeclaringNeedsTenantAdministration(t *testing.T) {
	t.Parallel()
	m, tenant, _, _ := registriesHarness(t)
	ref := ensureRuntimeTestProfileRef(t, m, tenant)
	public := []SessionRegistry{{Kind: "npm", URL: "https://registry.npmjs.org/"}}
	programs := []string{"rea"}
	for name, patch := range map[string]ProfilePatch{
		"a registry token":  {SessionRegistries: &[]SessionRegistry{acmeRegistry}},
		"a public registry": {SessionRegistries: &public},
		"a program":         {SessionPrograms: &programs},
		"a withdrawal":      {SessionRegistries: &[]SessionRegistry{}},
	} {
		if _, err := m.PatchProfile(context.Background(), tenant, ref, patch); statusOf(err) != http.StatusForbidden {
			t.Errorf("%s by a non-administrator: %v, want 403", name, err)
		}
	}
	name := "renamed"
	if _, err := m.PatchProfile(context.Background(), tenant, ref, ProfilePatch{DisplayName: &name}); err != nil {
		t.Fatalf("a patch that declares neither needs no administration: %v", err)
	}
}

func TestProfileRegistries_Validation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		in   []SessionRegistry
		want string
	}{
		{"kind", []SessionRegistry{{Kind: "pip", URL: "https://pypi.example/"}}, "kind"},
		{"cleartext token", []SessionRegistry{{Kind: "npm", URL: "http://npm.example/"}}, "https"},
		{"userinfo", []SessionRegistry{{Kind: "npm", URL: "https://u:p@npm.example/"}}, "not a registry URL"},
		{"query", []SessionRegistry{{Kind: "npm", URL: "https://npm.example/?a=1"}}, "not a registry URL"},
		{"scope", []SessionRegistry{{Kind: "npm", URL: "https://npm.example/", Scope: "acme"}}, "npm scope"},
		{"two defaults", []SessionRegistry{{Kind: "npm", URL: "https://a.example/"}, {Kind: "npm", URL: "https://b.example/"}}, "only one registry"},
		{"secret namespace", []SessionRegistry{{Kind: "npm", URL: "https://npm.example/", Secret: "mcp/token"}}, "session secret"},
		{"empty query", []SessionRegistry{{Kind: "npm", URL: "https://npm.example/?"}}, "not a registry URL"},
		{"ini comment", []SessionRegistry{{Kind: "npm", URL: "https://npm.example/a;b/"}}, "not a registry URL"},
		{"link-local", []SessionRegistry{{Kind: "npm", URL: "https://169.254.169.254/"}}, "not a registry address"},
	} {
		if _, _, err := normalizeSessionRegistries(tc.in); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want an error naming %q", tc.name, err, tc.want)
		}
	}
	stored, secrets, err := normalizeSessionRegistries([]SessionRegistry{{Kind: "npm", URL: "http://127.0.0.1:4873", Scope: "@acme"}})
	if err != nil || secrets || stored != `[{"kind":"npm","url":"http://127.0.0.1:4873/","scope":"@acme"}]` {
		t.Fatalf("a loopback registry = %s %v %v", stored, secrets, err)
	}
}

// A declared program's install directory goes first on PATH; one that is not
// installed refuses the launch by name.
func TestProfilePrograms_PutTheInstallFirstOnPATH(t *testing.T) {
	t.Parallel()
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "rea"), []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	resolve := func(driver string) string {
		if driver == "declared-rea" {
			return filepath.Join(bin, "rea")
		}
		return ""
	}
	m, tenant, fr, _ := registriesHarness(t, WithProgramResolver(resolve))
	ref := declareRegistries(t, m, tenant, nil, []string{"rea"})
	if _, err := createProfiledTestRun(t, m, context.Background(), tenant, secretEnvParams(nil, false)); err != nil {
		t.Fatal(err)
	}
	path, _ := specEnvValue(fr.lastSpec(), "PATH")
	if want := bin + string(os.PathListSeparator) + os.Getenv("PATH"); path != want {
		t.Fatalf("PATH = %q, want %q", path, want)
	}
	confined := LaunchSpec{Confinement: &confine.Policy{}}
	if err := m.applyProfilePrograms([]string{"rea"}, &confined); err != nil || !slices.Contains(confined.Confinement.ReadOnly, bin) {
		t.Fatalf("a confined child cannot run the program's install: %v %+v", err, confined.Confinement)
	}

	missing := []string{"rea", "jq"}
	if _, err := m.PatchProfile(context.Background(), tenant, ref, ProfilePatch{SessionPrograms: &missing, MayUseSecretEnv: true}); err != nil {
		t.Fatal(err)
	}
	_, err := createProfiledTestRun(t, m, context.Background(), tenant, secretEnvParams(nil, false))
	var re *runErr
	if !errors.As(err, &re) || re.status != http.StatusConflict || !strings.Contains(re.msg, "program jq, which is not installed") {
		t.Fatalf("a profile program that is not installed: %v, want 409 naming it", err)
	}
}

// The registry is the profile's: a session on another profile of the same
// tenant gets no npm configuration and opens no registry secret.
func TestProfileRegistries_AnotherProfileGetsNone(t *testing.T) {
	t.Parallel()
	m, tenant, fr, _ := registriesHarness(t)
	declareRegistries(t, m, tenant, []SessionRegistry{acmeRegistry}, nil)
	other := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: "claude", ConfigHome: t.TempDir(), UserHome: t.TempDir(), DisplayName: "other"})
	p := secretEnvParams(nil, false)
	p.ProviderProfileRef = other.Ref
	if _, err := createProfiledTestRun(t, m, context.Background(), tenant, p); err != nil {
		t.Fatal(err)
	}
	spec := fr.lastSpec()
	if _, ok := specEnvValue(spec, envNPMGlobalConfig); ok {
		t.Fatal("a session on another profile got the registry's configuration")
	}
	for _, e := range spec.Env {
		if strings.Contains(e.Value, registryCanary) {
			t.Fatalf("a session on another profile got the registry token as %s", e.Name)
		}
	}
}

// A resume re-reads the CURRENT declaration: a new launch file with today's
// registries, the stopped launch's file gone, the ledger naming them again, and
// a withdrawn declaration giving none.
func TestProfileRegistries_ResumeUsesTheCurrentDeclaration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	m, tenant, fr, _ := registriesHarness(t)
	ref := declareRegistries(t, m, tenant, []SessionRegistry{acmeRegistry}, nil)
	dto, err := createProfiledTestRun(t, m, ctx, tenant, secretEnvParams(nil, false))
	if err != nil {
		t.Fatal(err)
	}
	first, _ := specEnvValue(fr.lastSpec(), envNPMGlobalConfig)
	if _, err := m.stopRun(ctx, tenant, dto.RunRef, actorU, actorKindU); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the stopped launch's file removed", func() bool { _, err := os.Stat(first); return errors.Is(err, os.ErrNotExist) })

	moved := acmeRegistry
	moved.URL = "https://npm2.acme.example/"
	declareRegistries(t, m, tenant, []SessionRegistry{moved}, nil)
	if _, err := m.resumeRunAs(ctx, tenant, dto.RunRef, actorU, actorKindU, "", false); err != nil {
		t.Fatalf("resume: %v", err)
	}
	second, _ := specEnvValue(fr.lastSpec(), envNPMGlobalConfig)
	raw, err := os.ReadFile(second)
	if err != nil || !strings.Contains(string(raw), "@acme:registry=https://npm2.acme.example/\n") {
		t.Fatalf("resumed npm configuration = %q, %v", raw, err)
	}
	details := strings.Join(runEventDetails(t, m, tenant, dto.RunRef), "\n")
	if !strings.Contains(details, "resumed: ") || !strings.Contains(details, "registries @acme https://npm2.acme.example/") {
		t.Fatalf("the ledger does not record the resumed registries:\n%s", details)
	}
	fr.lastProc().out <- OutputFrame{Stream: streamStderr, Data: []byte("token " + registryCanary)}
	lr, _ := m.rt.getLive(tenant, dto.RunRef)
	waitFor(t, "the frame in the ring", func() bool { return len(lr.ring.readFrom(0).frames) >= 1 })
	if out := string(lr.ring.readFrom(0).frames[len(lr.ring.readFrom(0).frames)-1].Data); strings.Contains(out, registryCanary) {
		t.Fatalf("a resumed session's output carries the token: %q", out)
	}

	if _, err := m.stopRun(ctx, tenant, dto.RunRef, actorU, actorKindU); err != nil {
		t.Fatal(err)
	}
	none := []SessionRegistry{}
	if _, err := m.PatchProfile(ctx, tenant, ref, ProfilePatch{SessionRegistries: &none, MayUseSecretEnv: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.resumeRunAs(ctx, tenant, dto.RunRef, actorU, actorKindU, "", false); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if _, ok := specEnvValue(fr.lastSpec(), envNPMGlobalConfig); ok {
		t.Fatal("a withdrawn declaration still reached the resumed session")
	}
}

// A corrupt stored declaration refuses the launch instead of reading as none.
func TestProfileRegistries_AnUnreadableDeclarationRefusesTheLaunch(t *testing.T) {
	t.Parallel()
	_, _, _, ok := decodeProfilePackages(model.Record{colPPSessionRegistries: `[{"kind":"npm","scope":7}]`})
	if ok {
		t.Fatal("a corrupt registry declaration read as valid")
	}
	if _, _, _, ok := decodeProfilePackages(model.Record{}); !ok {
		t.Fatal("no declaration read as corrupt")
	}
	p := CreateRunParams{}
	if err := applySessionPolicy(&p, providerDriverClaude, sessionPolicy{unreadable: true}); statusOf(err) != http.StatusConflict {
		t.Fatalf("an unreadable declaration = %v, want 409", err)
	}
}

// The HTTP handlers ask the authenticated caller's tenant administration: with no
// authorizer to answer it, the same administrator is refused a registry token.
func TestProfileRegistries_TheAPIAsksForTenantAdministration(t *testing.T) {
	m := New(WithProviderSecretVault(newFakeVault()))
	m.UseExecutionEnvironmentRef(testEnvRef)
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "registries")
	body := map[string]any{"driver": "claude", "config_home": t.TempDir(), "user_home": t.TempDir(),
		"session_registries": []map[string]string{{"kind": "npm", "url": acmeRegistry.URL, "scope": "@acme", "secret": "env/npm-acme"}},
		"session_programs":   []string{"rea"}}
	created := h.doJSON(http.MethodPost, "/v1/m/sessions/provider-profiles", admin, body, tenantHdr(tenant))
	if created.code != http.StatusCreated || !strings.Contains(created.raw, `"secret":"env/npm-acme"`) || !strings.Contains(created.raw, `"session_programs":["rea"]`) {
		t.Fatalf("create by a tenant administrator = %d %s", created.code, created.raw)
	}
	ref := created.body["profile_ref"].(string)
	m.WorkAuthorizer = nil
	patched := h.doJSON(http.MethodPatch, "/v1/m/sessions/provider-profiles/"+ref, admin,
		map[string]any{"session_registries": body["session_registries"]}, tenantHdr(tenant))
	if patched.code != http.StatusForbidden {
		t.Fatalf("patch without tenant administration = %d %s, want 403", patched.code, patched.raw)
	}
}

type beforeProfilePackagesRegistry struct{ store.ExtensionRegistry }

func (r beforeProfilePackagesRegistry) Register(d model.EntityDescriptor) error {
	if d.Kind == providerProfileKind {
		d.Fields = slices.DeleteFunc(slices.Clone(d.Fields), func(f model.FieldSpec) bool {
			return f.Name == colPPSessionPrograms || f.Name == colPPSessionRegistries
		})
	}
	return r.ExtensionRegistry.Register(d)
}

// A store from before the two columns upgrades in place: an existing profile
// declares nothing and launches as before, and a declaration stores and reloads.
func TestProfilePackagesNullableUpgradeBothBackends(t *testing.T) {
	for _, backend := range profileBackends(t) {
		t.Run(backend.name, func(t *testing.T) {
			ctx := context.Background()
			oldModule := New()
			m, st := openProfileModule(t, backend, func(reg store.ExtensionRegistry) error {
				return oldModule.RegisterSchema(beforeProfilePackagesRegistry{reg})
			})
			tenant := ensureTenant(t, st, "profile-packages-upgrade")
			profile := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: "claude", ConfigHome: t.TempDir(), UserHome: t.TempDir()})
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			m, st = openProfileModule(t, backend, nil)
			defer st.Close()
			upgraded, err := m.GetProfile(ctx, tenant, profile.Ref)
			if err != nil {
				t.Fatal(err)
			}
			if upgraded.SessionPrograms != nil || upgraded.SessionRegistries != nil || upgraded.packagesUnreadable || upgraded.ID != profile.ID {
				t.Fatalf("the upgrade changed the existing profile: %+v", upgraded)
			}
			regs := []SessionRegistry{acmeRegistry}
			if _, err := m.PatchProfile(ctx, tenant, profile.Ref, ProfilePatch{SessionRegistries: &regs, MayUseSecretEnv: true}); err != nil {
				t.Fatal(err)
			}
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			m, st = openProfileModule(t, backend, nil)
			defer st.Close()
			persisted, err := m.GetProfile(ctx, tenant, profile.Ref)
			if err != nil || !slices.Equal(persisted.SessionRegistries, regs) {
				t.Fatalf("the declaration did not survive reopen: %+v %v", persisted.SessionRegistries, err)
			}
		})
	}
}
