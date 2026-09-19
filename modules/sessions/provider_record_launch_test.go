// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

// Launch acceptance: a profile BOUND to a provider record resolves its
// credential from that record, and every way that resolution can fail DENIES the
// launch instead of quietly running it on the host-wide credential.

// boundHarness builds a module with a vault, a probe, a capturing runner and a
// host-wide credential source, plus one anthropic record and one claude profile
// bound to it under managed injection.
func boundHarness(t *testing.T) (*Module, model.TenantID, *fakeRunner, *countingCredentialSource, *fakeVault, ProviderRecord, ProviderProfile) {
	t.Helper()
	runner := &fakeRunner{}
	host := &countingCredentialSource{}
	vault := newFakeVault()
	probe := &fakeProbe{}
	m, _, tenant, _ := newRuntimeHarness(t,
		WithRunner(runner), WithCredentialSource(host),
		WithProviderSecretVault(vault), WithProviderProbe(probe))
	m.UseExecutionEnvironmentRef(testEnvRef)
	rec := mustCreateRecord(t, m, tenant, anthropicInput("Anthropic (prod)"))
	configHome, userHome, _, _ := twoHomes(t)
	prof := mustCreateProfile(t, m, tenant, CreateProfileInput{
		Driver: "claude", ConfigHome: configHome, UserHome: userHome, DisplayName: "bound",
		AuthSource: AuthSourceManagedInjection, ProviderRecordRef: rec.Ref,
	})
	return m, tenant, runner, host, vault, rec, prof
}

func launchBound(m *Module, tenant model.TenantID, prof ProviderProfile) (runDTO, error) {
	return m.createRun(context.Background(), tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: "user:d19", ActorKind: model.ActorUser, ProviderProfileRef: prof.Ref,
	})
}

// The whole point of provider records, in one test: the operator registered a credential,
// bound it to a profile, and the child receives exactly that credential — with no
// host environment variable involved.
func TestBoundRecord_LaunchInjectsTheRegisteredCredential(t *testing.T) {
	t.Parallel()
	m, tenant, runner, host, _, rec, prof := boundHarness(t)

	dto, err := launchBound(m, tenant, prof)
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	spec := runner.lastSpec()
	if v, ok := envValue(spec, "ANTHROPIC_API_KEY"); !ok || v != testProviderKey {
		t.Fatalf("ANTHROPIC_API_KEY = %q,%v — the bound record's credential must reach the child", v, ok)
	}
	// The HOST-wide source was never consulted: a bound record is the answer, not a
	// preference applied on top of one.
	host.mu.Lock()
	calls := host.calls
	host.mu.Unlock()
	if calls != 0 {
		t.Fatalf("the host-wide credential source was minted %d times for a launch with a bound record", calls)
	}
	if _, ok := envValue(spec, "ANTHROPIC_AUTH_TOKEN"); ok {
		t.Fatal("a record-backed launch must not also carry the host bearer")
	}
	// The run DTO names the record and carries no credential.
	raw, _ := json.Marshal(dto)
	if bytes.Contains(raw, []byte(testProviderKey)) {
		t.Fatalf("the run DTO leaks the credential: %s", raw)
	}
	// The snapshot persisted with the run names the record, which is how "which
	// credential authorised this session" stays answerable afterwards.
	stored, err := m.loadRun(context.Background(), tenant, dto.RunRef)
	if err != nil {
		t.Fatal(err)
	}
	if got := stored.String(colRunProviderRecordRef); got != rec.Ref {
		t.Fatalf("the run row records provider %q, want %q", got, rec.Ref)
	}
}

// ⛔ THE NO-FALLBACK TEST. A vault that cannot open the bound credential DENIES the
// launch. It does not reach for the host-wide credential, which would run the
// session on an account the operator did not select for it.
func TestBoundRecord_VaultFailureDeniesAndNeverFallsBack(t *testing.T) {
	t.Parallel()
	m, tenant, runner, host, vault, _, prof := boundHarness(t)
	vault.openErr = errors.New("sealer offline")

	_, err := launchBound(m, tenant, prof)
	var re *runErr
	if !errors.As(err, &re) || re.status != http.StatusServiceUnavailable {
		t.Fatalf("launch with an unopenable bound credential = %v, want 503", err)
	}
	if !strings.Contains(re.msg, "does not fall back") {
		t.Fatalf("the refusal must say it is not falling back, got %q", re.msg)
	}
	if launchCount(runner) != 0 {
		t.Fatal("a denied launch started a child")
	}
	host.mu.Lock()
	defer host.mu.Unlock()
	if host.calls != 0 {
		t.Fatal("the launch fell back to the host-wide credential")
	}
}

// A revoked record refuses the launch BY NAME. The binding is deliberately not
// rewritten by the revocation, so this is the answer an operator gets — and it is
// actionable, unlike a profile that silently stopped naming anything.
func TestBoundRecord_RevokedRefusesTheLaunch(t *testing.T) {
	t.Parallel()
	m, tenant, runner, _, _, rec, prof := boundHarness(t)
	if _, err := m.RevokeProviderRecord(context.Background(), testActor(), tenant, rec.Ref); err != nil {
		t.Fatal(err)
	}
	_, err := launchBound(m, tenant, prof)
	var re *runErr
	if !errors.As(err, &re) || re.status != http.StatusConflict {
		t.Fatalf("launch under a revoked provider = %v, want 409", err)
	}
	if !strings.Contains(re.msg, "revoked") {
		t.Fatalf("the refusal must name the cause, got %q", re.msg)
	}
	if launchCount(runner) != 0 {
		t.Fatal("a denied launch started a child")
	}
}

// Two endpoints for one launch is a conflict, not an ordering. Choosing silently
// would route a session through a gateway the operator thought they had bypassed,
// or the reverse, and neither is discoverable from the outside.
func TestBoundRecord_TwoEndpointsAreRefusedRatherThanOrdered(t *testing.T) {
	t.Parallel()
	runner := &fakeRunner{}
	vault := newFakeVault()
	m, _, tenant, _ := newRuntimeHarness(t,
		WithRunner(runner), WithCredentialSource(&countingCredentialSource{}),
		WithProviderSecretVault(vault), WithInferenceBaseURL("https://gateway.example.com"))
	m.UseExecutionEnvironmentRef(testEnvRef)
	rec := mustCreateRecord(t, m, tenant, CreateProviderRecordInput{
		Kind: ProviderKindAnthropic, DisplayName: "Anthropic", APIKey: testProviderKey,
		BaseURL: "https://api.anthropic.example",
	})
	configHome, userHome, _, _ := twoHomes(t)
	prof := mustCreateProfile(t, m, tenant, CreateProfileInput{
		Driver: "claude", ConfigHome: configHome, UserHome: userHome, DisplayName: "bound",
		AuthSource: AuthSourceManagedInjection, ProviderRecordRef: rec.Ref,
	})
	_, err := launchBound(m, tenant, prof)
	var re *runErr
	if !errors.As(err, &re) || re.status != http.StatusConflict {
		t.Fatalf("launch with two endpoints = %v, want 409", err)
	}
	if launchCount(runner) != 0 {
		t.Fatal("a denied launch started a child")
	}
}

// A profile whose auth source is the account HOME is not affected by a bound
// record: nothing is injected, because the authorized home IS the credential.
// Binding a record and then authorizing the home is contradictory, and the
// authorization wins — it is the narrower statement.
func TestBoundRecord_AccountHomeStillInjectsNothing(t *testing.T) {
	t.Parallel()
	m, tenant, runner, _, _, rec, _ := boundHarness(t)
	configHome, userHome, _, _ := twoHomes(t)
	prof := mustCreateProfile(t, m, tenant, CreateProfileInput{
		Driver: "claude", ConfigHome: configHome, UserHome: userHome, DisplayName: "home-auth",
		AuthSource: AuthSourceAccountHome, ProviderRecordRef: rec.Ref,
	})
	if _, err := launchBound(m, tenant, prof); err != nil {
		t.Fatalf("createRun: %v", err)
	}
	spec := runner.lastSpec()
	if _, ok := envValue(spec, "ANTHROPIC_API_KEY"); ok {
		t.Fatal("an account-home launch must receive no injected credential")
	}
}

// The binding is validated when it is WRITTEN: an unknown record, a revoked one
// and a kind this driver cannot read are all refused before the column changes.
func TestRecordBinding_ValidatedOnWrite(t *testing.T) {
	t.Parallel()
	m, _, tenant, _, _ := providerHarness(t)
	ctx := context.Background()
	openai := mustCreateRecord(t, m, tenant, CreateProviderRecordInput{
		Kind: ProviderKindOpenAI, DisplayName: "OpenAI", APIKey: testProviderKey,
	})
	revoked := mustCreateRecord(t, m, tenant, anthropicInput("Retired"))
	if _, err := m.RevokeProviderRecord(ctx, testActor(), tenant, revoked.Ref); err != nil {
		t.Fatal(err)
	}
	configHome, userHome, otherConfig, otherUser := twoHomes(t)
	prof := mustCreateProfile(t, m, tenant, CreateProfileInput{
		Driver: "claude", ConfigHome: configHome, UserHome: userHome, DisplayName: "p",
	})

	bind := func(ref string) error {
		_, err := m.PatchProfile(ctx, tenant, prof.Ref, ProfilePatch{ProviderRecordRef: &ref})
		return err
	}
	if err := bind("prv_notareal"); !errors.Is(err, ErrProviderRecordNotFound) {
		t.Fatalf("binding an unknown record = %v", err)
	}
	if err := bind(revoked.Ref); !errors.Is(err, ErrProviderRecordRevoked) {
		t.Fatalf("binding a revoked record = %v", err)
	}
	err := bind(openai.Ref)
	var re *runErr
	if !errors.As(err, &re) || re.status != http.StatusUnprocessableEntity {
		t.Fatalf("binding an openai credential to a claude profile = %v, want 422", err)
	}
	if !strings.Contains(re.msg, ProviderKindOpenAI) || !strings.Contains(re.msg, "claude") {
		t.Fatalf("the refusal must name both the kind and the driver, got %q", re.msg)
	}
	// None of the refusals changed the row.
	after, err := m.GetProfile(ctx, tenant, prof.Ref)
	if err != nil || after.ProviderRecordRef != "" {
		t.Fatalf("a refused binding changed the profile: %+v %v", after, err)
	}

	// The same checks run at CREATE, so a profile cannot be born with a binding the
	// patch route would refuse.
	_, err = m.CreateProfile(ctx, tenant, CreateProfileInput{
		Driver: "claude", ConfigHome: otherConfig, UserHome: otherUser, DisplayName: "q",
		ProviderRecordRef: openai.Ref,
	})
	if !errors.As(err, &re) || re.status != http.StatusUnprocessableEntity {
		t.Fatalf("creating a profile with an incompatible binding = %v, want 422", err)
	}
}

// Binding and unbinding keep the profile id and its homes: it is an authorization,
// not identity.
func TestRecordBinding_IsAuthorizationNotIdentity(t *testing.T) {
	t.Parallel()
	m, _, tenant, _, _ := providerHarness(t)
	ctx := context.Background()
	rec := mustCreateRecord(t, m, tenant, anthropicInput("A"))
	configHome, userHome, _, _ := twoHomes(t)
	prof := mustCreateProfile(t, m, tenant, CreateProfileInput{
		Driver: "claude", ConfigHome: configHome, UserHome: userHome, DisplayName: "p",
	})
	bind := rec.Ref
	bound, err := m.PatchProfile(ctx, tenant, prof.Ref, ProfilePatch{ProviderRecordRef: &bind})
	if err != nil || bound.Ref != prof.Ref || bound.ConfigHome != prof.ConfigHome {
		t.Fatalf("binding changed the identity: %+v %v", bound, err)
	}
	if bound.ProviderRecordRef != rec.Ref {
		t.Fatalf("provider_record_ref = %q, want %q", bound.ProviderRecordRef, rec.Ref)
	}
	none := ""
	unbound, err := m.PatchProfile(ctx, tenant, prof.Ref, ProfilePatch{ProviderRecordRef: &none})
	if err != nil || unbound.Ref != prof.Ref || unbound.ProviderRecordRef != "" {
		t.Fatalf("unbinding = %+v %v", unbound, err)
	}
}

// remote-control relays the session through the operator's own subscription login,
// which this module does not issue. A profile bound to a registered credential
// cannot launch under it — refused with both halves named, rather than injecting a
// key that would change the session's identity or skipping quietly and leaving the
// operator with a binding that did nothing.
func TestBoundRecord_RemoteControlIsRefusedRatherThanInjectedOrSkipped(t *testing.T) {
	t.Parallel()
	m, tenant, runner, _, _, _, prof := boundHarness(t)
	_, err := m.createRun(context.Background(), tenant, CreateRunParams{
		Transport: TransportRemoteControl, Isolation: IsolationNative,
		Actor: "user:d19", ActorKind: model.ActorUser, ProviderProfileRef: prof.Ref,
	})
	var re *runErr
	if !errors.As(err, &re) || re.status != http.StatusUnprocessableEntity {
		t.Fatalf("remote-control under a bound provider = %v, want 422", err)
	}
	if !strings.Contains(re.msg, "remote-control") || !strings.Contains(re.msg, "unbind") {
		t.Fatalf("the refusal must name the transport and a remedy, got %q", re.msg)
	}
	if launchCount(runner) != 0 {
		t.Fatal("a denied launch started a child")
	}
}

// The two-endpoint conflict is scoped to the driver the deployment gateway actually
// reaches. OLIVARES_SESSION_RUNTIME_BASE_URL is injected as ANTHROPIC_BASE_URL by the
// frame-driven bridge and reaches no other driver, so refusing a codex launch for a
// collision that cannot occur would be a rule that over-blocks.
func TestBoundRecord_TwoEndpointConflictDoesNotOverBlockAnotherDriver(t *testing.T) {
	t.Parallel()
	vault := newFakeVault()
	m, _, tenant, _ := newRuntimeHarness(t,
		WithRunner(&fakeRunner{}), WithCredentialSource(&countingCredentialSource{}),
		WithProviderSecretVault(vault), WithInferenceBaseURL("https://gateway.example.com"))
	m.UseExecutionEnvironmentRef(testEnvRef)
	rec := mustCreateRecord(t, m, tenant, CreateProviderRecordInput{
		Kind: ProviderKindOpenAI, DisplayName: "OpenAI", APIKey: testProviderKey,
		BaseURL: "https://api.openai.example",
	})
	// The mint is exercised directly: launching a codex profile end to end needs a
	// registered codex driver, and what is under test here is the refusal's SCOPE,
	// not the driver registry.
	_, env, err := m.mintFromProviderRecord(context.Background(), tenant, providerDriverCodex, rec.Ref)
	if err != nil {
		t.Fatalf("a codex launch must not be refused for an Anthropic gateway: %v", err)
	}
	if v, ok := envByName(env, "OPENAI_BASE_URL"); !ok || v != "https://api.openai.example" {
		t.Fatalf("OPENAI_BASE_URL = %q,%v", v, ok)
	}
	// And the Claude driver still refuses, so the scoping did not delete the rule.
	claudeRec := mustCreateRecord(t, m, tenant, CreateProviderRecordInput{
		Kind: ProviderKindAnthropic, DisplayName: "Anthropic", APIKey: testProviderKey,
		BaseURL: "https://api.anthropic.example",
	})
	_, _, err = m.mintFromProviderRecord(context.Background(), tenant, providerDriverClaude, claudeRec.Ref)
	var re *runErr
	if !errors.As(err, &re) || re.status != http.StatusConflict {
		t.Fatalf("claude with two endpoints = %v, want 409", err)
	}
}

// envByName is the launch-spec lookup for a raw []EnvVar (envValue takes a LaunchSpec).
func envByName(env []EnvVar, name string) (string, bool) {
	for _, item := range env {
		if item.Name == name {
			return item.Value, true
		}
	}
	return "", false
}
