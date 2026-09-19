// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Provider-record acceptance. These tests drive the module directly, with a fake vault and a
// fake probe, because the properties under test are the module's own: what a row
// may hold, what a reader may see, and what a launch does when a named credential
// cannot be produced.

// testProviderKey is long enough to earn a hint and recognisable enough that a
// search for it in a row, a DTO or a log finds it if it leaked.
const testProviderKey = "sk-ant-api03-LEAKCANARY-0123456789-ABCD"

// fakeVault is an in-memory ProviderSecretVault. It records the actor of every
// sealed write so a test can assert the attribution the real store requires.
type fakeVault struct {
	mu      sync.Mutex
	values  map[string]string
	actors  []string
	sealErr error
	openErr error
}

func newFakeVault() *fakeVault { return &fakeVault{values: map[string]string{}} }

func (v *fakeVault) Seal(_ context.Context, actor auth.Principal, tenant model.TenantID, name string, value []byte) (string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.sealErr != nil {
		return "", v.sealErr
	}
	v.actors = append(v.actors, actor.Actor())
	v.values[tenant.String()+"|"+name] = string(value)
	return name, nil
}

func (v *fakeVault) Open(_ context.Context, tenant model.TenantID, locator string) ([]byte, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.openErr != nil {
		return nil, v.openErr
	}
	val, ok := v.values[tenant.String()+"|"+locator]
	if !ok {
		return nil, errors.New("fake vault: no such secret")
	}
	return []byte(val), nil
}

func (v *fakeVault) Revoke(_ context.Context, _ auth.Principal, tenant model.TenantID, locator string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	delete(v.values, tenant.String()+"|"+locator)
	return nil
}

func (v *fakeVault) count() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return len(v.values)
}

// fakeProbe answers whatever the test set, and records the keys it was handed so a
// test can prove the value travelled to the probe and nowhere else.
type fakeProbe struct {
	mu     sync.Mutex
	result ProviderProbeResult
	err    error
	calls  int
	keys   []string
}

func (p *fakeProbe) Probe(_ context.Context, req ProviderProbeRequest) (ProviderProbeResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	p.keys = append(p.keys, req.APIKey)
	return p.result, p.err
}

// testActor is an attributable principal. The real secret store refuses an
// unattributable one, so a test that used the zero value would be testing a path
// production cannot take.
func testActor() auth.Principal {
	return auth.Principal{Kind: auth.KindUser, UserID: model.NewID(), CredID: model.NewID(), DisplayName: "d19-operator"}
}

// providerHarness is a module with a vault and a probe wired.
func providerHarness(t *testing.T, opts ...Option) (*Module, store.Store, model.TenantID, *fakeVault, *fakeProbe) {
	t.Helper()
	vault := newFakeVault()
	probe := &fakeProbe{result: ProviderProbeResult{Models: []string{"claude-opus-5"}, Detail: "1 models listed"}}
	all := append([]Option{WithProviderSecretVault(vault), WithProviderProbe(probe)}, opts...)
	m, st, tenant, _ := newRuntimeHarness(t, all...)
	m.UseExecutionEnvironmentRef(testEnvRef)
	return m, st, tenant, vault, probe
}

func mustCreateRecord(t *testing.T, m *Module, tenant model.TenantID, in CreateProviderRecordInput) ProviderRecord {
	t.Helper()
	if in.Actor.CredID.IsZero() {
		in.Actor = testActor()
	}
	rec, err := m.CreateProviderRecord(context.Background(), tenant, in)
	if err != nil {
		t.Fatalf("create provider record: %v", err)
	}
	return rec
}

func anthropicInput(name string) CreateProviderRecordInput {
	return CreateProviderRecordInput{Kind: ProviderKindAnthropic, DisplayName: name, APIKey: testProviderKey}
}

// The lifecycle an operator performs: register, read it back, list it, rename it,
// rotate it, revoke it. Every step keeps the id and none of them returns a value.
func TestProviderRecord_Lifecycle(t *testing.T) {
	t.Parallel()
	m, _, tenant, vault, _ := providerHarness(t)
	ctx := context.Background()

	rec := mustCreateRecord(t, m, tenant, anthropicInput("Anthropic (prod)"))
	if rec.State != ProviderRecordActive || rec.Kind != ProviderKindAnthropic {
		t.Fatalf("created record = %+v", rec)
	}
	if rec.KeyHint != "…ABCD" {
		t.Fatalf("hint = %q, want the last four characters", rec.KeyHint)
	}
	if rec.ProbeState != ProbeNever {
		t.Fatalf("a record nobody tested reports %q, want the never-tested state", rec.ProbeState)
	}
	if vault.count() != 1 {
		t.Fatalf("sealed values = %d, want 1", vault.count())
	}

	got, err := m.GetProviderRecord(ctx, tenant, rec.Ref)
	if err != nil || got.Ref != rec.Ref {
		t.Fatalf("get = %+v, %v", got, err)
	}
	list, _, err := m.ListProviderRecords(ctx, tenant, "", "", model.Query{Limit: 10})
	if err != nil || len(list) != 1 {
		t.Fatalf("list = %d records, %v", len(list), err)
	}

	name := "Anthropic (production)"
	renamed, err := m.PatchProviderRecord(ctx, tenant, rec.Ref, ProviderRecordPatch{DisplayName: &name, Actor: testActor()})
	if err != nil || renamed.Ref != rec.Ref || renamed.DisplayName != name {
		t.Fatalf("rename = %+v, %v", renamed, err)
	}

	rotated := "sk-ant-api03-SECOND-9876543210-WXYZ"
	after, err := m.PatchProviderRecord(ctx, tenant, rec.Ref, ProviderRecordPatch{APIKey: &rotated, Actor: testActor()})
	if err != nil || after.KeyHint != "…WXYZ" {
		t.Fatalf("rotate = %+v, %v", after, err)
	}
	opened, err := vault.Open(ctx, tenant, providerVaultName(rec.Ref))
	if err != nil || string(opened) != rotated {
		t.Fatalf("after rotation the vault holds %q, %v", string(opened), err)
	}
	if vault.count() != 1 {
		t.Fatalf("rotation left %d sealed values, want 1 (it reseals in place)", vault.count())
	}

	revoked, err := m.RevokeProviderRecord(ctx, testActor(), tenant, rec.Ref)
	if err != nil || revoked.State != ProviderRecordRevoked || revoked.RevokedAt == "" {
		t.Fatalf("revoke = %+v, %v", revoked, err)
	}
	if vault.count() != 0 {
		t.Fatalf("revoke left the sealed value behind (%d)", vault.count())
	}
	// The row survives its own revocation: the sessions it authorized still name it.
	if again, gerr := m.GetProviderRecord(ctx, tenant, rec.Ref); gerr != nil || again.State != ProviderRecordRevoked {
		t.Fatalf("a revoked record must stay readable: %+v, %v", again, gerr)
	}
}

// Every sealed write is attributed. The engine's own secret store refuses an
// unattributable principal, so a plane that dropped the actor would fail in
// production and nowhere else.
func TestProviderRecord_SealedWritesCarryTheActor(t *testing.T) {
	t.Parallel()
	m, _, tenant, vault, _ := providerHarness(t)
	actor := testActor()
	rec := mustCreateRecord(t, m, tenant, CreateProviderRecordInput{
		Kind: ProviderKindAnthropic, DisplayName: "A", APIKey: testProviderKey, Actor: actor,
	})
	rotated := "sk-ant-api03-ROTATED-000000-QRST"
	if _, err := m.PatchProviderRecord(context.Background(), tenant, rec.Ref,
		ProviderRecordPatch{APIKey: &rotated, Actor: actor}); err != nil {
		t.Fatal(err)
	}
	vault.mu.Lock()
	defer vault.mu.Unlock()
	if len(vault.actors) != 2 {
		t.Fatalf("sealed writes = %d, want 2", len(vault.actors))
	}
	for _, got := range vault.actors {
		if got != actor.Actor() || got == "" {
			t.Fatalf("sealed write attributed to %q, want %q", got, actor.Actor())
		}
	}
}

// Two active records cannot share a name within a kind, and the DATABASE says so.
// Revoking one frees the name.
func TestProviderRecord_NameIsUniquePerKindAndFreedByRevoke(t *testing.T) {
	t.Parallel()
	m, _, tenant, _, _ := providerHarness(t)
	ctx := context.Background()
	first := mustCreateRecord(t, m, tenant, anthropicInput("Shared"))

	_, err := m.CreateProviderRecord(ctx, tenant, anthropicInput("shared"))
	if !errors.Is(err, ErrProviderNameTaken) {
		t.Fatalf("a second active record with the same case-folded name = %v, want the taken conflict", err)
	}
	// Another KIND may use the name: the slot is (kind, name).
	mustCreateRecord(t, m, tenant, CreateProviderRecordInput{
		Kind: ProviderKindOpenAI, DisplayName: "Shared", APIKey: testProviderKey,
	})
	if _, err := m.RevokeProviderRecord(ctx, testActor(), tenant, first.Ref); err != nil {
		t.Fatal(err)
	}
	if _, err := m.CreateProviderRecord(ctx, tenant, anthropicInput("Shared")); err != nil {
		t.Fatalf("after revoking the holder the name must be free: %v", err)
	}
}

// The validation table. Each row states the input and the status it must earn.
func TestProviderRecord_Validation(t *testing.T) {
	t.Parallel()
	m, _, tenant, _, _ := providerHarness(t)
	cases := []struct {
		name   string
		in     CreateProviderRecordInput
		status int
	}{
		{"no kind", CreateProviderRecordInput{DisplayName: "x", APIKey: testProviderKey}, http.StatusBadRequest},
		{"invented kind", CreateProviderRecordInput{Kind: "acme", DisplayName: "x", APIKey: testProviderKey}, http.StatusBadRequest},
		{"no name", CreateProviderRecordInput{Kind: ProviderKindAnthropic, APIKey: testProviderKey}, http.StatusBadRequest},
		{"no key", CreateProviderRecordInput{Kind: ProviderKindAnthropic, DisplayName: "x"}, http.StatusBadRequest},
		{"key too short", CreateProviderRecordInput{Kind: ProviderKindAnthropic, DisplayName: "x", APIKey: "abc"}, http.StatusBadRequest},
		{"http endpoint", CreateProviderRecordInput{
			Kind: ProviderKindAnthropic, DisplayName: "x", APIKey: testProviderKey, BaseURL: "http://api.example.com",
		}, http.StatusBadRequest},
		{"endpoint carrying a credential", CreateProviderRecordInput{
			Kind: ProviderKindAnthropic, DisplayName: "x", APIKey: testProviderKey, BaseURL: "https://user:secret@api.example.com",
		}, http.StatusBadRequest},
		{"openai_compatible with no endpoint", CreateProviderRecordInput{
			Kind: ProviderKindOpenAICompatible, DisplayName: "x", APIKey: testProviderKey,
		}, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := m.CreateProviderRecord(context.Background(), tenant, tc.in)
			var re *runErr
			if !errors.As(err, &re) || re.status != tc.status {
				t.Fatalf("create = %v, want status %d", err, tc.status)
			}
		})
	}
}

// With no vault wired the engine refuses to register a provider and names the
// wiring. It does NOT store the credential in the clear, which is the only other
// thing it could have done.
func TestProviderRecord_NoVaultRefusesAndStoresNothing(t *testing.T) {
	t.Parallel()
	m, st, tenant, _ := newRuntimeHarness(t)
	m.UseExecutionEnvironmentRef(testEnvRef)
	_, err := m.CreateProviderRecord(context.Background(), tenant, anthropicInput("A"))
	var re *runErr
	if !errors.As(err, &re) || re.status != http.StatusServiceUnavailable {
		t.Fatalf("create with no vault = %v, want 503", err)
	}
	if !strings.Contains(re.msg, "vault") {
		t.Fatalf("the refusal must name the missing wiring, got %q", re.msg)
	}
	if n := countProviderRecordRows(t, st, tenant); n != 0 {
		t.Fatalf("a refused registration left %d rows", n)
	}
}

// A vault that cannot seal leaves NOTHING: no row, and no record of a credential
// the engine did not store.
func TestProviderRecord_SealFailureLeavesNoRow(t *testing.T) {
	t.Parallel()
	vault := newFakeVault()
	vault.sealErr = errors.New("sealer offline")
	m, st, tenant, _ := newRuntimeHarness(t, WithProviderSecretVault(vault))
	m.UseExecutionEnvironmentRef(testEnvRef)
	_, err := m.CreateProviderRecord(context.Background(), tenant, anthropicInput("A"))
	var re *runErr
	if !errors.As(err, &re) || re.status != http.StatusServiceUnavailable {
		t.Fatalf("create with a failing sealer = %v, want 503", err)
	}
	if n := countProviderRecordRows(t, st, tenant); n != 0 {
		t.Fatalf("a failed seal left %d rows", n)
	}
}

// THE SECRECY TEST, and it searches the artefacts rather than reading the code: the
// credential must not appear in any column of the row, nor in anything the module
// returns.
func TestProviderRecord_ValueNeverLandsInARowOrAReader(t *testing.T) {
	t.Parallel()
	m, st, tenant, _, probe := providerHarness(t)
	ctx := context.Background()
	rec := mustCreateRecord(t, m, tenant, anthropicInput("Anthropic"))
	if _, err := m.TestProviderRecord(ctx, tenant, rec.Ref); err != nil {
		t.Fatal(err)
	}

	err := st.View(ctx, tenant, func(sc store.Scope) error {
		repo, rerr := sc.Ext(providerRecordKind)
		if rerr != nil {
			return rerr
		}
		rows, _, rerr := repo.List(ctx, model.Query{Limit: 10})
		if rerr != nil {
			return rerr
		}
		if len(rows) != 1 {
			t.Fatalf("rows = %d", len(rows))
		}
		for col, raw := range rows[0] {
			if value, ok := raw.(string); ok && strings.Contains(value, testProviderKey) {
				t.Fatalf("column %q holds the credential", col)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	read, err := m.GetProviderRecord(ctx, tenant, rec.Ref)
	if err != nil {
		t.Fatal(err)
	}
	dto := toProviderRecordDTO(read)
	if strings.Contains(dto.KeyHint, testProviderKey) || strings.Contains(dto.ProbeDetail, testProviderKey) {
		t.Fatal("the read shape carries the credential")
	}
	// Counted in RUNES: the marker is one character and three bytes, and a byte
	// count here would pass a hint of six characters.
	if n := len([]rune(dto.KeyHint)); n > 5 {
		t.Fatalf("hint %q is %d characters, want at most the marker plus four", dto.KeyHint, n)
	}
	// It DID reach the probe, which is the only place it is supposed to go.
	probe.mu.Lock()
	defer probe.mu.Unlock()
	if len(probe.keys) != 1 || probe.keys[0] != testProviderKey {
		t.Fatalf("the probe received %v, want the registered credential exactly once", probe.keys)
	}
}

// Rotation clears the previous verdict. Keeping the green would report a
// connection that was measured on a credential that no longer exists.
func TestProviderRecord_RotationClearsThePreviousVerdict(t *testing.T) {
	t.Parallel()
	m, _, tenant, _, _ := providerHarness(t)
	ctx := context.Background()
	rec := mustCreateRecord(t, m, tenant, anthropicInput("A"))
	tested, err := m.TestProviderRecord(ctx, tenant, rec.Ref)
	if err != nil || tested.ProbeState != ProbeOK || len(tested.Models) != 1 {
		t.Fatalf("test = %+v, %v", tested, err)
	}
	rotated := "sk-ant-api03-ROTATED-111111-MNOP"
	after, err := m.PatchProviderRecord(ctx, tenant, rec.Ref, ProviderRecordPatch{APIKey: &rotated, Actor: testActor()})
	if err != nil {
		t.Fatal(err)
	}
	if after.ProbeState != ProbeNever || len(after.Models) != 0 {
		t.Fatalf("after rotation the record reports %q with %d models, want the never-tested state",
			after.ProbeState, len(after.Models))
	}
}

// The three probe outcomes. A refusal by the provider and an endpoint that could
// not be reached are different answers with different remedies, and the row says
// which one happened.
func TestProviderRecord_ProbeOutcomesAreThreeValued(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		res   ProviderProbeResult
		err   error
		state string
	}{
		{"reachable and accepted", ProviderProbeResult{Models: []string{"m1", "m2"}, Detail: "2 models listed"}, nil, ProbeOK},
		{"reachable and refused", ProviderProbeResult{}, ErrProviderRefused, ProbeRefused},
		{"not reachable", ProviderProbeResult{}, errors.New("dial tcp: no route to host"), ProbeUnreachable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, _, tenant, _, probe := providerHarness(t)
			probe.result, probe.err = tc.res, tc.err
			rec := mustCreateRecord(t, m, tenant, anthropicInput("A"))
			got, err := m.TestProviderRecord(context.Background(), tenant, rec.Ref)
			if err != nil {
				t.Fatalf("a probe that answered must not fail the call: %v", err)
			}
			if got.ProbeState != tc.state {
				t.Fatalf("probe state = %q, want %q", got.ProbeState, tc.state)
			}
			if got.ProbedAt == "" {
				t.Fatal("every probe stamps when it ran")
			}
			if tc.state != ProbeOK && len(got.Models) != 0 {
				t.Fatal("a failed probe must not keep a model list")
			}
		})
	}
}

// With no probe wired the test is refused and says that LAUNCHING is unaffected —
// an operator who reads "unavailable" must not conclude the deployment is broken.
func TestProviderRecord_NoProbeRefusesAndSaysLaunchingIsUnaffected(t *testing.T) {
	t.Parallel()
	vault := newFakeVault()
	m, _, tenant, _ := newRuntimeHarness(t, WithProviderSecretVault(vault))
	m.UseExecutionEnvironmentRef(testEnvRef)
	rec := mustCreateRecord(t, m, tenant, anthropicInput("A"))
	_, err := m.TestProviderRecord(context.Background(), tenant, rec.Ref)
	var re *runErr
	if !errors.As(err, &re) || re.status != http.StatusServiceUnavailable {
		t.Fatalf("test with no probe = %v, want 503", err)
	}
	if !strings.Contains(re.msg, "launching is unaffected") {
		t.Fatalf("the refusal must say what is unaffected, got %q", re.msg)
	}
}

// A revoked record cannot be tested, rotated or renamed: the id is kept so history
// reads truthfully, not so it can be brought back.
func TestProviderRecord_RevokedAcceptsNothing(t *testing.T) {
	t.Parallel()
	m, _, tenant, _, _ := providerHarness(t)
	ctx := context.Background()
	rec := mustCreateRecord(t, m, tenant, anthropicInput("A"))
	if _, err := m.RevokeProviderRecord(ctx, testActor(), tenant, rec.Ref); err != nil {
		t.Fatal(err)
	}
	name := "renamed"
	if _, err := m.PatchProviderRecord(ctx, tenant, rec.Ref, ProviderRecordPatch{DisplayName: &name, Actor: testActor()}); !errors.Is(err, ErrProviderRecordRevoked) {
		t.Fatalf("rename of a revoked record = %v", err)
	}
	if _, err := m.TestProviderRecord(ctx, tenant, rec.Ref); !errors.Is(err, ErrProviderRecordRevoked) {
		t.Fatalf("test of a revoked record = %v", err)
	}
	if _, err := m.RevokeProviderRecord(ctx, testActor(), tenant, rec.Ref); !errors.Is(err, ErrProviderRecordRevoked) {
		t.Fatalf("second revoke = %v", err)
	}
}

// The kind↔driver compatibility table, stated as a table so a future driver has to
// be added here deliberately rather than by accident.
func TestRecordServesDriver(t *testing.T) {
	t.Parallel()
	cases := []struct {
		kind, driver string
		want         bool
	}{
		{ProviderKindAnthropic, providerDriverClaude, true},
		{ProviderKindOpenAI, providerDriverClaude, false},
		{ProviderKindXAI, providerDriverClaude, false},
		{ProviderKindOpenAI, providerDriverCodex, true},
		{ProviderKindAnthropic, providerDriverCodex, false},
		{ProviderKindXAI, providerDriverGrok, true},
		{ProviderKindOpenAI, providerDriverGrok, false},
		{ProviderKindAnthropic, providerDriverOpenCode, true},
		{ProviderKindOpenAI, providerDriverOpenCode, true},
		{ProviderKindXAI, providerDriverOpenCode, true},
		// openai_compatible names what it injects, so it serves anything.
		{ProviderKindOpenAICompatible, providerDriverClaude, true},
		{ProviderKindOpenAICompatible, "some-future-driver", true},
		// An unknown driver gets NOTHING else. A derivation would have said yes.
		{ProviderKindAnthropic, "some-future-driver", false},
		{ProviderKindOpenAI, "some-future-driver", false},
	}
	for _, tc := range cases {
		if got := recordServesDriver(tc.kind, tc.driver); got != tc.want {
			t.Fatalf("recordServesDriver(%q, %q) = %v, want %v", tc.kind, tc.driver, got, tc.want)
		}
	}
}

// The environment each kind produces, and the closed set it is validated against.
func TestProviderRecordEnv(t *testing.T) {
	t.Parallel()
	cases := []struct {
		kind, base string
		want       []string
	}{
		{ProviderKindAnthropic, "", []string{"ANTHROPIC_API_KEY"}},
		{ProviderKindAnthropic, "https://gw.example.com", []string{"ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL"}},
		{ProviderKindOpenAI, "", []string{"OPENAI_API_KEY"}},
		{ProviderKindXAI, "", []string{"XAI_API_KEY"}},
		{ProviderKindOpenAICompatible, "https://llm.example.com", []string{"OPENAI_API_KEY", "OPENAI_BASE_URL"}},
	}
	for _, tc := range cases {
		env := providerRecordEnv(tc.kind, tc.base, testProviderKey)
		if len(env) != len(tc.want) {
			t.Fatalf("%s produced %d variables, want %d", tc.kind, len(env), len(tc.want))
		}
		for i, name := range tc.want {
			if env[i].Name != name {
				t.Fatalf("%s variable %d = %q, want %q", tc.kind, i, env[i].Name, name)
			}
		}
		if err := validateRecordCredentialEnv(tc.kind, env); err != nil {
			t.Fatalf("a kind's own environment must pass its own validator: %v", err)
		}
	}
	// The validator is a CLOSED set, and it stays closed across kinds: an anthropic
	// record may not set OpenAI's variable even though another kind may.
	err := validateRecordCredentialEnv(ProviderKindAnthropic, []EnvVar{{Name: "OPENAI_API_KEY", Value: "x"}})
	if err == nil {
		t.Fatal("a kind may not set another kind's variable")
	}
}

// countProviderRecordRows counts the record rows a tenant holds, for the tests that
// assert a refusal left nothing behind.
func countProviderRecordRows(t *testing.T, st store.Store, tenant model.TenantID) int {
	t.Helper()
	n := 0
	err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(providerRecordKind)
		if err != nil {
			return err
		}
		rows, _, err := repo.List(context.Background(), model.Query{Limit: 100})
		if err != nil {
			return err
		}
		n = len(rows)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}
