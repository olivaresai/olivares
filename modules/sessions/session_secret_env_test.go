// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// 2026-10-01 (design FH 016, approved by Root): a session can use secrets
// from the Olivares vault as environment variables. The person picks secrets by
// NAME; the value reaches the child's environment and nothing else — not argv,
// not the folder, not the run row, not the API, not the ledger, not the output
// the console streams. Only someone who may manage the tenant's secrets may give
// them to a session, and a resume cannot widen what the run was given.

const (
	secretCanary = "ghp_LEAKCANARY_session_secret_0123456789"
	// quotedCanary needs JSON escaping, so a stream-json line carries it escaped.
	quotedCanary = `pa"ss\word-LEAKCANARY-0123456789`
)

var githubSecretEnv = []SecretEnvRef{{Env: "GITHUB_TOKEN", Secret: "env/github"}}

func secretEnvHarness(t *testing.T) (*Module, model.TenantID, *fakeRunner, *fakeVault, *spyGate) {
	t.Helper()
	vault := newFakeVault()
	fr := &fakeRunner{}
	gate := &spyGate{inner: LaunchDecision{Allowed: true}}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()),
		WithLaunchGate(gate), WithProviderSecretVault(vault))
	stopModuleAtCleanup(t, m)
	vault.values[tenant.String()+"|env/github"] = secretCanary
	vault.values[tenant.String()+"|env/quoted"] = quotedCanary
	return m, tenant, fr, vault, gate
}

func secretEnvParams(refs []SecretEnvRef, mayUse bool) CreateRunParams {
	return CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative, PermissionMode: "default",
		Actor: actorU, ActorKind: actorKindU,
		SecretEnv: append([]SecretEnvRef(nil), refs...), MayUseSecretEnv: mayUse,
	}
}

func specEnvValue(spec LaunchSpec, name string) (string, bool) {
	for _, e := range spec.Env {
		if e.Name == name {
			return e.Value, true
		}
	}
	return "", false
}

func runEventDetails(t *testing.T, m *Module, tenant model.TenantID, runRef string) []string {
	t.Helper()
	var details []string
	if err := m.Data.View(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(runEventKind)
		if err != nil {
			return err
		}
		records, _, err := repo.List(context.Background(), model.Query{
			Filters: []model.Filter{eq(colEvRunRef, runRef)}, Limit: 100,
		})
		for _, ev := range records {
			details = append(details, ev.String(colEvEvent)+": "+ev.String(colEvDetail))
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return details
}

func TestSecretEnv_TheValueReachesOnlyTheChildEnvironment(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	m, tenant, fr, _, gate := secretEnvHarness(t)

	dto, err := createProfiledTestRun(t, m, ctx, tenant, secretEnvParams(githubSecretEnv, true))
	if err != nil {
		t.Fatalf("create with a vault secret: %v", err)
	}
	spec := fr.lastSpec()
	if got, _ := specEnvValue(spec, "GITHUB_TOKEN"); got != secretCanary {
		t.Fatalf("GITHUB_TOKEN in the child env = %q, want the vault value", got)
	}
	for _, where := range append([]string{spec.Program, spec.Dir}, spec.Args...) {
		if strings.Contains(where, secretCanary) {
			t.Fatalf("the value reached the program, folder or argv: %q", where)
		}
	}
	// The gate, and so any approval it opens, is asked about the NAMES.
	if got := gate.last(t).SecretEnv; !reflect.DeepEqual(got, githubSecretEnv) {
		t.Fatalf("launch intent secret_env = %+v, want %+v", got, githubSecretEnv)
	}
	// The API answers with names only.
	raw, _ := json.Marshal(dto)
	if strings.Contains(string(raw), secretCanary) {
		t.Fatalf("the run DTO carries the value: %s", raw)
	}
	if !strings.Contains(string(raw), `"secret_env":[{"env":"GITHUB_TOKEN","secret":"env/github"}]`) {
		t.Fatalf("the run DTO does not name the secret: %s", raw)
	}
	// The row keeps the names, the ledger records them, neither holds the value.
	rec, err := m.loadRun(ctx, tenant, dto.RunRef)
	if err != nil {
		t.Fatal(err)
	}
	row, _ := json.Marshal(rec)
	if strings.Contains(string(row), secretCanary) || !strings.Contains(string(row), "env/github") {
		t.Fatalf("run row = %s, want the names and never the value", row)
	}
	details := strings.Join(runEventDetails(t, m, tenant, dto.RunRef), "\n")
	if strings.Contains(details, secretCanary) {
		t.Fatalf("the ledger carries the value: %s", details)
	}
	if !strings.Contains(details, "GITHUB_TOKEN<-env/github") {
		t.Fatalf("the ledger does not record which secret the session received:\n%s", details)
	}
}

func TestSecretEnv_OutputNeverCarriesTheValue(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	m, tenant, fr, _, _ := secretEnvHarness(t)
	refs := []SecretEnvRef{{Env: "GITHUB_TOKEN", Secret: "env/github"}, {Env: "DB_PASSWORD", Secret: "env/quoted"}}
	dto, err := createProfiledTestRun(t, m, ctx, tenant, secretEnvParams(refs, true))
	if err != nil {
		t.Fatal(err)
	}
	escaped, _ := json.Marshal(quotedCanary)
	p := fr.lastProc()
	p.out <- OutputFrame{Stream: streamStdout, Data: []byte(
		`{"type":"user","message":{"content":[{"type":"tool_result","content":"token=` + secretCanary + `"}]}}`)}
	p.out <- OutputFrame{Stream: streamStdout, Data: []byte(
		`{"type":"assistant","message":{"content":[{"type":"text","text":` + string(escaped) + `}]}}`)}
	p.out <- OutputFrame{Stream: streamStderr, Data: []byte("debug: password " + quotedCanary)}
	lr, ok := m.rt.getLive(tenant, dto.RunRef)
	if !ok {
		t.Fatal("expected a live run")
	}
	waitFor(t, "three frames in the ring", func() bool { return len(lr.ring.readFrom(0).frames) >= 3 })
	var all strings.Builder
	for _, f := range lr.ring.readFrom(0).frames {
		all.Write(f.Data)
		all.WriteByte('\n')
	}
	out := all.String()
	for _, leaked := range []string{secretCanary, quotedCanary, string(escaped[1 : len(escaped)-1])} {
		if strings.Contains(out, leaked) {
			t.Fatalf("the session output carries a secret value:\n%s", out)
		}
	}
	for _, name := range []string{"[secret env/github]", "[secret env/quoted]"} {
		if !strings.Contains(out, name) {
			t.Fatalf("the output does not say %s was withheld:\n%s", name, out)
		}
	}
	// A stream-json line stays valid JSON after the value is withheld.
	for _, f := range lr.ring.readFrom(0).frames {
		if f.Stream == streamStdout && !json.Valid(f.Data) {
			t.Fatalf("redaction broke a stream-json line: %s", f.Data)
		}
	}
}

func TestSecretEnv_OnlyATenantAdministratorMayGiveSecrets(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	m, tenant, fr, _, gate := secretEnvHarness(t)
	_, err := createProfiledTestRun(t, m, ctx, tenant, secretEnvParams(githubSecretEnv, false))
	if statusOf(err) != http.StatusForbidden {
		t.Fatalf("secret_env without tenant administration = %v, want 403", err)
	}
	fr.mu.Lock()
	launched := len(fr.specs)
	fr.mu.Unlock()
	if launched != 0 || len(gate.seen) != 0 {
		t.Fatal("a refused secret launch reached the gate or the runner")
	}
	// A launch that names no secret is unaffected.
	if _, err := createProfiledTestRun(t, m, ctx, tenant, secretEnvParams(nil, false)); err != nil {
		t.Fatalf("a launch without secrets: %v", err)
	}
}

func TestSecretEnv_RefusesReservedAndMalformedNames(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	m, tenant, fr, _, _ := secretEnvHarness(t)
	for _, refs := range [][]SecretEnvRef{
		{{Env: "PATH", Secret: "env/github"}},
		{{Env: "HOME", Secret: "env/github"}},
		{{Env: "ANTHROPIC_API_KEY", Secret: "env/github"}},
		{{Env: "OPENAI_API_KEY", Secret: "env/github"}},
		{{Env: "OLIVARES_WORK_TOKEN", Secret: "env/github"}},
		{{Env: "CLAUDE_CONFIG_DIR", Secret: "env/github"}},
		{{Env: "CODEX_HOME", Secret: "env/github"}},
		{{Env: "LD_PRELOAD", Secret: "env/github"}},
		{{Env: "bad-name", Secret: "env/github"}},
		{{Env: "", Secret: "env/github"}},
		{{Env: "GITHUB_TOKEN", Secret: "mcp/github"}},
		{{Env: "GITHUB_TOKEN", Secret: "sessions/provider/x"}},
		{{Env: "GITHUB_TOKEN", Secret: "env/"}},
		{{Env: "GITHUB_TOKEN", Secret: "env/github"}, {Env: "GITHUB_TOKEN", Secret: "env/quoted"}},
	} {
		_, err := createProfiledTestRun(t, m, ctx, tenant, secretEnvParams(refs, true))
		if statusOf(err) != http.StatusBadRequest {
			t.Errorf("secret_env %+v = %v, want 400", refs, err)
		}
	}
	p := secretEnvParams(githubSecretEnv, true)
	p.EnvAllow = []string{"GITHUB_TOKEN"}
	if _, err := createProfiledTestRun(t, m, ctx, tenant, p); statusOf(err) != http.StatusBadRequest {
		t.Errorf("a name in both env_allow and secret_env = %v, want 400", err)
	}
	fr.mu.Lock()
	defer fr.mu.Unlock()
	if len(fr.specs) != 0 {
		t.Fatal("a refused secret launch reached the runner")
	}
}

func TestSecretEnv_AMissingSecretRefusesTheLaunchByName(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	m, tenant, fr, vault, _ := secretEnvHarness(t)
	_, err := createProfiledTestRun(t, m, ctx, tenant, secretEnvParams([]SecretEnvRef{{Env: "AWS_SECRET_ACCESS_KEY", Secret: "env/aws"}}, true))
	if statusOf(err) != http.StatusConflict || !strings.Contains(err.Error(), "env/aws") ||
		!strings.Contains(err.Error(), secretEnvPlace) {
		t.Fatalf("a missing secret = %v, want 409 naming env/aws and %s", err, secretEnvPlace)
	}
	vault.mu.Lock()
	vault.values[tenant.String()+"|env/pin"] = "1234"
	vault.mu.Unlock()
	_, err = createProfiledTestRun(t, m, ctx, tenant, secretEnvParams([]SecretEnvRef{{Env: "PIN", Secret: "env/pin"}}, true))
	if statusOf(err) != http.StatusConflict || !strings.Contains(err.Error(), "env/pin") {
		t.Fatalf("a value too short to withhold = %v, want 409 naming env/pin", err)
	}
	vault.openErr = fmt.Errorf("sealer offline")
	_, err = createProfiledTestRun(t, m, ctx, tenant, secretEnvParams(githubSecretEnv, true))
	if statusOf(err) != http.StatusServiceUnavailable {
		t.Fatalf("an unreadable vault = %v, want 503", err)
	}
	fr.mu.Lock()
	defer fr.mu.Unlock()
	if len(fr.specs) != 0 {
		t.Fatal("a launch whose secret could not be read reached the runner")
	}
}

// Resume reads the names from the run row: it re-reads the CURRENT value (a
// rotated secret is picked up), asks again whether the resumer may use secrets,
// and cannot add a name the run was not given.
func TestSecretEnv_ResumeUsesTheStoredNamesAndTheCurrentValue(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	m, tenant, fr, vault, gate := secretEnvHarness(t)
	dto, err := createProfiledTestRun(t, m, ctx, tenant, secretEnvParams(githubSecretEnv, true))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.stopRun(ctx, tenant, dto.RunRef, actorU, actorKindU); err != nil {
		t.Fatal(err)
	}
	rotated := "ghp_LEAKCANARY_rotated_9876543210"
	vault.mu.Lock()
	vault.values[tenant.String()+"|env/github"] = rotated
	vault.mu.Unlock()

	if _, err := m.resumeRunAs(ctx, tenant, dto.RunRef, actorU, actorKindU, "", false); statusOf(err) != http.StatusForbidden {
		t.Fatalf("resume by someone who may not use secrets = %v, want 403", err)
	}
	if _, err := m.resumeRunAs(ctx, tenant, dto.RunRef, actorU, actorKindU, "", true); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if got, _ := specEnvValue(fr.lastSpec(), "GITHUB_TOKEN"); got != rotated {
		t.Fatalf("resumed GITHUB_TOKEN = %q, want the current vault value", got)
	}
	if got := gate.last(t).SecretEnv; !reflect.DeepEqual(got, githubSecretEnv) {
		t.Fatalf("resume intent secret_env = %+v, want the stored %+v", got, githubSecretEnv)
	}
}

// secretEnvPlace is where the console keeps session secrets (web/src/features/
// first-hour: the start form's More options, then Manage session secrets).
const secretEnvPlace = "New session > More options > Manage session secrets"

// A stopped session's secrets cannot be edited, and session secrets live in the
// tenant's store behind the start form's "Manage session secrets", not the
// deployment-wide Secrets page (#511). Every refusal of a stored secret on resume
// names that place and the other way out, a new session, and none offers to
// remove the secret from this session.
func TestSecretEnv_AResumeRefusalNamesAWayOutThatExists(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	m, tenant, fr, vault, _ := secretEnvHarness(t)
	dto, err := createProfiledTestRun(t, m, ctx, tenant, secretEnvParams(githubSecretEnv, true))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.stopRun(ctx, tenant, dto.RunRef, actorU, actorKindU); err != nil {
		t.Fatal(err)
	}
	fr.mu.Lock()
	before := len(fr.specs)
	fr.mu.Unlock()
	key := tenant.String() + "|env/github"
	for _, tc := range []struct {
		name  string
		value string // "" deletes the secret
		want  string // the refusal this value reaches
	}{
		{"deleted", "", "no session secret named env/github"},
		{"too short", "1234", "shorter than"},
		{"a short line", "long enough line\nq7", "a line of the value"},
		{"shares the marker", "x[secret env/github]x", "the mark that replaces"},
		{"shares the cut note", "abcdefgh" + diagnosticTruncatedMark, "the note that ends"},
	} {
		vault.mu.Lock()
		if tc.value == "" {
			delete(vault.values, key)
		} else {
			vault.values[key] = tc.value
		}
		vault.mu.Unlock()
		_, err := m.resumeRunAs(ctx, tenant, dto.RunRef, actorU, actorKindU, "", true)
		if statusOf(err) != http.StatusConflict || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: resume = %v, want 409 saying %q", tc.name, err, tc.want)
			continue
		}
		msg := err.Error()
		if !strings.Contains(msg, secretEnvPlace) || !strings.Contains(msg, "start a new session") ||
			strings.Contains(msg, "remove it from this session") {
			t.Errorf("%s: resume refusal %q, want it to name %s and a new session, and no removal", tc.name, msg, secretEnvPlace)
		}
		for _, part := range append(secretLines(tc.value), tc.value) {
			if part != "" && strings.Contains(msg, part) {
				t.Errorf("%s: resume refusal carries the value", tc.name)
			}
		}
	}
	fr.mu.Lock()
	defer fr.mu.Unlock()
	if len(fr.specs) != before {
		t.Fatal("a refused resume reached the runner")
	}
}

// A template names secrets the same way; the launcher still has to be allowed to
// use them, and the template's name for a variable wins over the request's.
func TestSecretEnv_ATemplateNamesSecretsForTheLaunchItGoverns(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	m, tenant, fr, _, _ := secretEnvHarness(t)
	tplID := seedTemplate(t, m, tenant, "with-github", tplBody{Settings: &tplSettings{
		SecretEnv: []SecretEnvRef{{Env: "GITHUB_TOKEN", Secret: "env/github"}},
	}})

	p := secretEnvParams(nil, false)
	p.TemplateID = tplID
	if _, err := createProfiledTestRun(t, m, ctx, tenant, p); statusOf(err) != http.StatusForbidden {
		t.Fatalf("a template's secrets for a launcher who may not use them = %v, want 403", err)
	}

	p = secretEnvParams([]SecretEnvRef{{Env: "GITHUB_TOKEN", Secret: "env/quoted"}}, true)
	p.TemplateID = tplID
	if _, err := createProfiledTestRun(t, m, ctx, tenant, p); err != nil {
		t.Fatalf("launch from the template: %v", err)
	}
	if got, _ := specEnvValue(fr.lastSpec(), "GITHUB_TOKEN"); got != secretCanary {
		t.Fatalf("GITHUB_TOKEN = %q, want the template's secret env/github", got)
	}
}

// PEP on 834b0c8c: the hook path and the approval reviewer redacted by PATTERN only,
// so a secret value inside a tool input could reach an approval request. The run's
// own exact-value redactor is exposed to them (RedactSessionSecrets).
func TestSecretEnv_TheHookPathCanWithholdTheRunsValues(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	m, tenant, _, _, _ := secretEnvHarness(t)
	refs := append([]SecretEnvRef{{Env: "QUOTED", Secret: "env/quoted"}}, githubSecretEnv...)
	dto, err := createProfiledTestRun(t, m, ctx, tenant, secretEnvParams(refs, true))
	if err != nil {
		t.Fatalf("create with vault secrets: %v", err)
	}
	escaped, _ := json.Marshal(quotedCanary)
	input := []byte(`{"tool_name":"Bash","tool_input":{"command":"curl -H 'Authorization: ` + secretCanary +
		`' && echo ` + string(escaped) + `"}}`)
	got, ok := m.RedactSessionSecrets(tenant, dto.RunRef, input)
	if !ok {
		t.Fatal("a supervised run could not vouch for its own tool input")
	}
	if strings.Contains(string(got), secretCanary) || strings.Contains(string(got), "LEAKCANARY") {
		t.Fatalf("a value survived: %s", got)
	}
	if !strings.Contains(string(got), "[secret env/github]") || !strings.Contains(string(got), "[secret env/quoted]") {
		t.Fatalf("the values are not named by their secret: %s", got)
	}
	// A run this node does not supervise cannot vouch: the caller withholds instead.
	if same, ok := m.RedactSessionSecrets(tenant, "run_not_here", input); ok || string(same) != string(input) {
		t.Fatalf("an unknown run = (%q, %v), want the input back and ok false", same, ok)
	}
	// A run with no secrets passes its data through, vouched.
	plain, err := createProfiledTestRun(t, m, ctx, tenant, secretEnvParams(nil, false))
	if err != nil {
		t.Fatalf("create without secrets: %v", err)
	}
	if same, ok := m.RedactSessionSecrets(tenant, plain.RunRef, input); !ok || string(same) != string(input) {
		t.Fatalf("a run with no secrets = (%q, %v), want its data unchanged and ok true", same, ok)
	}
}

// SR2 on 834b0c8c: a queued launch's secret permission is the launcher's CURRENT tenant
// administration when the approval arrives (SR2's oracle covers the demoted launcher).
// The positive half: a launcher who is still an administrator gets the secret.
func TestSecretEnv_AQueuedLaunchKeepsSecretsWhileItsLauncherIsStillAnAdministrator(t *testing.T) {
	gate := &controlledLaunchApproval{}
	runner := &fakeRunner{initSID: "queued-secret-admin"}
	vault := newFakeVault()
	m := New(WithSessionWorkspaceRoot(t.TempDir()), WithRunner(runner), WithCredentialSource(staticCred()), WithLaunchGate(gate), WithProviderSecretVault(vault))
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "queued-secret-admin")
	vault.values[tenant.String()+"|env/github"] = secretCanary
	if r := h.doJSON("POST", "/v1/users", admin, map[string]any{"email": "queued-admin@fh.invalid", "password": "synthetic-password1", "tenant": tenant.String(), "role": auth.RoleAdmin}, nil); r.code != http.StatusCreated {
		t.Fatalf("tenant admin = %d %s", r.code, r.raw)
	}
	login := h.doJSON("POST", "/v1/auth/login", "", map[string]any{"email": "queued-admin@fh.invalid", "password": "synthetic-password1"}, nil)
	token, _ := login.body["token"].(string)
	issuer := auth.NewAuthenticator(h.st, nil)
	m.QueuedCredentialCapture = issuer.BindQueuedCredential
	m.QueuedLaunchAuthorization = func(ctx context.Context, tenant model.TenantID, credential auth.QueuedCredential, runID string, workspace model.ID) (auth.Principal, error) {
		return issuer.RevalidateQueuedCredential(ctx, credential)
	}
	queued := h.doJSON("POST", "/v1/m/sessions/runs", token, map[string]any{"provider_profile_ref": ensureRuntimeTestProfileRef(t, h.m, tenant), "name": "queued secret launch", "secret_env": githubSecretEnv}, tenantHdr(tenant))
	if queued.code != http.StatusAccepted || launchCount(runner) != 0 {
		t.Fatalf("queued = %d %s", queued.code, queued.raw)
	}
	gate.approved.Store(true)
	waitFor(t, "the approved launch", func() bool { return launchCount(runner) > 0 })
	if got, _ := specEnvValue(runner.lastSpec(), "GITHUB_TOKEN"); got != secretCanary {
		t.Fatalf("GITHUB_TOKEN = %q, want the vault value for a launcher who is still an administrator", got)
	}
}

// PEP/N1 on the proof line: a later pattern clean (connectors/redact.CleanMasked) split
// "[secret <name>]" because it could not tell the markers this redactor wrote. The
// redactor now returns each marker's span, recorded while it writes it.
func TestSecretEnv_SpansAreTheMarkersTheRedactorWrote(t *testing.T) {
	t.Parallel()
	escaped, _ := json.Marshal(quotedCanary)
	quotedInner := string(escaped[1 : len(escaped)-1])
	r := newSecretRedactor(
		[]SecretEnvRef{{Env: "GITHUB_TOKEN", Secret: "env/github"}, {Env: "QUOTED", Secret: "env/quoted"}},
		[]EnvVar{{Name: "GITHUB_TOKEN", Value: secretCanary}, {Name: "QUOTED", Value: quotedCanary}},
	)
	// A second binding whose other secret's value is the text inside the first's marker.
	markerish := newSecretRedactor(
		[]SecretEnvRef{{Env: "GITHUB_TOKEN", Secret: "env/github"}, {Env: "MARKERISH", Secret: "env/markerish"}},
		[]EnvVar{{Name: "GITHUB_TOKEN", Value: secretCanary}, {Name: "MARKERISH", Value: "secret env/github"}},
	)
	const github, quoted = "[secret env/github]", "[secret env/quoted]"
	check := func(name string, r *secretRedactor, input string, want []string) {
		t.Helper()
		out, spans := r.applyWithSpans([]byte(input))
		if len(spans) != len(want) {
			t.Fatalf("%s: %d spans in %q, want %d", name, len(spans), out, len(want))
		}
		prev := 0
		for i, sp := range spans {
			if sp.Start < prev || sp.End <= sp.Start || sp.End > len(out) {
				t.Fatalf("%s: span %d %+v is out of order or out of range (prev end %d)", name, i, sp, prev)
			}
			if got := string(out[sp.Start:sp.End]); got != want[i] {
				t.Fatalf("%s: span %d covers %q, want %q", name, i, got, want[i])
			}
			prev = sp.End
		}
		if strings.Contains(string(out), "LEAKCANARY") {
			t.Fatalf("%s: a value survived: %q", name, out)
		}
		if plain := r.apply([]byte(input)); string(plain) != string(out) {
			t.Fatalf("%s: apply and applyWithSpans disagree: %q vs %q", name, plain, out)
		}
	}
	check("one value", r, "token="+secretCanary+" done", []string{github})
	check("adjacent values", r, secretCanary+quotedInner, []string{github, quoted})
	check("repeated value", r, "a "+secretCanary+" b "+secretCanary, []string{github, github})
	check("raw and JSON-escaped forms", r, `{"a":"`+quotedInner+`","b":"`+quotedCanary+`"}`, []string{quoted, quoted})
	// Text that already says "[secret env/github]" is not a marker this redactor wrote.
	check("a marker already in the input", r, "said "+github+" then "+secretCanary, []string{github})
	out, spans := r.applyWithSpans([]byte("said " + github + " then " + secretCanary))
	if spans[0].Start != len("said "+github+" then ") {
		t.Fatalf("the span points at the literal text, not the generated marker: %+v in %q", spans, out)
	}
	// A binding where a marker could complete a value is refused at launch; a redactor
	// built from one anyway withholds the whole frame (SR2 report159).
	if out, spans := markerish.applyWithSpans([]byte("token=" + secretCanary)); len(out) != 0 || spans != nil {
		t.Fatalf("a binding a marker could complete = (%q, %v), want nothing", out, spans)
	}
	// Nothing to replace: the input comes back as it is, with no spans.
	if out, spans := r.applyWithSpans([]byte("nothing here")); string(out) != "nothing here" || spans != nil {
		t.Fatalf("no secret = (%q, %v)", out, spans)
	}
	var none *secretRedactor
	if out, spans := none.applyWithSpans([]byte("x")); string(out) != "x" || spans != nil {
		t.Fatalf("a session with no secrets = (%q, %v)", out, spans)
	}
}

// The port the hook path calls returns the same spans for a supervised run.
func TestSecretEnv_TheHookPortReturnsTheMarkerSpans(t *testing.T) {
	t.Parallel()
	m, tenant, _, _, _ := secretEnvHarness(t)
	dto, err := createProfiledTestRun(t, m, context.Background(), tenant, secretEnvParams(githubSecretEnv, true))
	if err != nil {
		t.Fatalf("create with a vault secret: %v", err)
	}
	input := []byte(`{"tool_input":{"command":"PASSWORD=` + secretCanary + ` curl --token=` + secretCanary + `"}}`)
	out, spans, ok := m.RedactSessionSecretsWithSpans(tenant, dto.RunRef, input)
	if !ok || len(spans) != 2 {
		t.Fatalf("port = (%q, %v, %v), want two spans", out, spans, ok)
	}
	for _, sp := range spans {
		if string(out[sp.Start:sp.End]) != "[secret env/github]" {
			t.Fatalf("span %+v covers %q", sp, out[sp.Start:sp.End])
		}
	}
	if same, spans, ok := m.RedactSessionSecretsWithSpans(tenant, "run_not_here", input); ok || spans != nil || string(same) != string(input) {
		t.Fatalf("an unknown run = (%q, %v, %v), want the input back, no spans, ok false", same, spans, ok)
	}
}

// SR2 report159/160, Root's choice: a value (raw or JSON-escaped) that shares text
// with a marker the run's redactor can write ("[secret <name>]" for every bound name)
// would reach the output whole, because one pass never scans a marker it wrote. The
// launch, and a resume that reads a rotated value, refuse before any child starts,
// naming the variable and neither the value nor a marker; safe bindings still launch
// and the hook port returns their spans.
func TestSecretEnv_AValueAMarkerCouldCompleteRefusesTheLaunchBeforeSpawn(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	refs := []SecretEnvRef{{Env: "GITHUB_TOKEN", Secret: "env/github"}, {Env: "SECOND", Secret: "env/second"}}
	const github, second = "[secret env/github]", "[secret env/second]"
	for _, c := range []struct{ name, value string }{
		{"inside the other secret's marker", "secret env/github"},
		{"inside its own marker", "secret env/second"},
		{"containing a marker", "pre-[secret env/github]-post"},
		{"ending where a marker begins", "ghp_0123456789[secret"},
		{"beginning where a marker ends", "github]0123456789"},
		{"needing JSON escaping", `pa"ss-0123[secret env/`},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			m, tenant, fr, vault, _ := secretEnvHarness(t)
			vault.mu.Lock()
			vault.values[tenant.String()+"|env/second"] = c.value
			vault.mu.Unlock()
			_, err := createProfiledTestRun(t, m, ctx, tenant, secretEnvParams(refs, true))
			if statusOf(err) != http.StatusConflict || !strings.Contains(err.Error(), "SECOND") {
				t.Fatalf("launch = %v, want 409 naming the variable SECOND", err)
			}
			for _, leak := range []string{c.value, secretCanary, github, second} {
				if strings.Contains(err.Error(), leak) {
					t.Fatalf("the refusal carries %q: %v", leak, err)
				}
			}
			fr.mu.Lock()
			spawned := len(fr.specs)
			fr.mu.Unlock()
			if spawned != 0 {
				t.Fatal("a refused binding reached the runner")
			}
			if runs := tenantRunCount(t, m, tenant); runs != 0 {
				t.Fatalf("a refused launch left %d run rows", runs)
			}
		})
	}

	// A safe pair launches; a rotation that makes one collide refuses the resume
	// before spawn; the safe run's spans are the markers written.
	m, tenant, fr, vault, _ := secretEnvHarness(t)
	vault.mu.Lock()
	vault.values[tenant.String()+"|env/second"] = "a[b]c-0123456789"
	vault.mu.Unlock()
	dto, err := createProfiledTestRun(t, m, ctx, tenant, secretEnvParams(refs, true))
	if err != nil {
		t.Fatalf("a safe binding (brackets away from the edges) = %v, want a launch", err)
	}
	input := []byte(`{"command":"` + secretCanary + ` a[b]c-0123456789"}`)
	out, spans, ok := m.RedactSessionSecretsWithSpans(tenant, dto.RunRef, input)
	if !ok || len(spans) != 2 || string(out[spans[0].Start:spans[0].End]) != github ||
		string(out[spans[1].Start:spans[1].End]) != second {
		t.Fatalf("safe spans = (%q, %v, %v), want the two markers", out, spans, ok)
	}
	if _, err := m.stopRun(ctx, tenant, dto.RunRef, actorU, actorKindU); err != nil {
		t.Fatal(err)
	}
	fr.mu.Lock()
	before := len(fr.specs)
	fr.mu.Unlock()
	vault.mu.Lock()
	vault.values[tenant.String()+"|env/second"] = "secret env/github"
	vault.mu.Unlock()
	_, err = m.resumeRunAs(ctx, tenant, dto.RunRef, actorU, actorKindU, "", true)
	if statusOf(err) != http.StatusConflict || !strings.Contains(err.Error(), "SECOND") ||
		strings.Contains(err.Error(), "secret env/github") {
		t.Fatalf("resume on a colliding rotation = %v, want 409 naming SECOND and not the value", err)
	}
	fr.mu.Lock()
	after := len(fr.specs)
	fr.mu.Unlock()
	if after != before {
		t.Fatal("a resume refused for a colliding value reached the runner")
	}
	details := strings.Join(runEventDetails(t, m, tenant, dto.RunRef), "\n")
	for _, leak := range []string{"secret env/github", secretCanary, "a[b]c-0123456789"} {
		if strings.Contains(details, leak) {
			t.Fatalf("the ledger carries %q:\n%s", leak, details)
		}
	}
}

// The forms the launch refuses, and the ones it keeps.
func TestSecretEnv_MarkerCollisionForms(t *testing.T) {
	t.Parallel()
	refs := []SecretEnvRef{{Env: "A", Secret: "env/github"}, {Env: "B", Secret: "env/b"}}
	for _, c := range []struct {
		value   string
		refused bool
	}{
		{secretCanary, false},
		{quotedCanary, false},
		{"a[b]c-0123456789", false},
		{"secret env/github", true},
		{"[secret env/b]-tail", true},
		{"0123456789[secret e", true},
		{"/b]0123456789", true},
		{"]0123456789", true},
		{"0123456789[", true},
	} {
		env, got := markerCollision(refs, []EnvVar{{Name: "A", Value: secretCanary}, {Name: "B", Value: c.value}})
		if got != c.refused || (got && env != "B") {
			t.Fatalf("value %q: collision = (%q, %v), want refused=%v naming B", c.value, env, got, c.refused)
		}
	}
}

// Claude Code (JSON.stringify), Codex (serde_json) and OpenCode write stream-json
// without Go's HTML escaping: a value with a quote or a backslash and also <, > or &
// reaches the output in a form json.Marshal never produces. The redactor withholds
// that form too.
func TestSecretEnv_AValueEscapedTheWayTheToolsWriteJSONIsWithheld(t *testing.T) {
	t.Parallel()
	for _, value := range []string{`p&ss\word<0123>`, `a"b>c&d-0123456789`, "tab\there&0123456"} {
		r := newSecretRedactor([]SecretEnvRef{{Env: "DB_PASSWORD", Secret: "env/db"}},
			[]EnvVar{{Name: "DB_PASSWORD", Value: value}})
		var line bytes.Buffer
		enc := json.NewEncoder(&line)
		enc.SetEscapeHTML(false) // as the tools write it
		if err := enc.Encode(map[string]string{"text": "x " + value + " y"}); err != nil {
			t.Fatal(err)
		}
		out, spans := r.applyWithSpans(line.Bytes())
		var got map[string]string
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("%q: the redacted line is not JSON: %v (%q)", value, err, out)
		}
		if strings.Contains(got["text"], value) || got["text"] != "x [secret env/db] y" || len(spans) != 1 {
			t.Fatalf("%q: tool-escaped line redacted to %q (spans %v), want the marker", value, got["text"], spans)
		}
	}
	// JSON.stringify and serde_json leave U+2028 and <, > and & as they are.
	if got, want := toolJSONForm("a\u2028<&\x1b\"\\"), "a\u2028<&"+`\u001b\"\\`; got != want {
		t.Fatalf("toolJSONForm = %q, want %q", got, want)
	}
}

func tenantRunCount(t *testing.T, m *Module, tenant model.TenantID) int {
	t.Helper()
	var n int
	if err := m.Data.View(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		records, _, err := repo.List(context.Background(), model.Query{Limit: 100})
		n = len(records)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return n
}

// SR2 46ff P1: a resume delivers exactly the binding the run row stores. A template
// edited since the launch to point the same variable at another secret neither
// changes what the resumed child receives nor what the gate is asked about; the
// value at the stored name is still read fresh (rotation keeps working).
func TestSecretEnv_ResumeKeepsTheStoredBindingWhenTheTemplateRebindsItsVariable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	m, tenant, fr, vault, gate := secretEnvHarness(t)
	tpl := seedTemplate(t, m, tenant, "fh-secret-rebind", tplBody{Settings: &tplSettings{SecretEnv: githubSecretEnv}})
	params := secretEnvParams(nil, true)
	params.TemplateID = tpl
	dto, err := createProfiledTestRun(t, m, ctx, tenant, params)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.stopRun(ctx, tenant, dto.RunRef, actorU, actorKindU); err != nil {
		t.Fatal(err)
	}
	retermTemplate(t, m, tenant, tpl, tplBody{Settings: &tplSettings{SecretEnv: []SecretEnvRef{{Env: "GITHUB_TOKEN", Secret: "env/quoted"}}}})
	rotated := "ghp_LEAKCANARY_rotated_after_rebind_0123"
	vault.mu.Lock()
	vault.values[tenant.String()+"|env/github"] = rotated
	vault.mu.Unlock()
	if _, err := m.resumeRunAs(ctx, tenant, dto.RunRef, actorU, actorKindU, "", true); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if got, _ := specEnvValue(fr.lastSpec(), "GITHUB_TOKEN"); got != rotated {
		t.Fatalf("resumed GITHUB_TOKEN = %q, want the stored secret's current value", got)
	}
	if got := gate.last(t).SecretEnv; !reflect.DeepEqual(got, githubSecretEnv) {
		t.Fatalf("resume intent secret_env = %+v, want the stored %+v", got, githubSecretEnv)
	}
	rec, err := m.loadRun(ctx, tenant, dto.RunRef)
	if err != nil {
		t.Fatal(err)
	}
	if stored := storedSecretEnvNames(rec); !reflect.DeepEqual(stored, githubSecretEnv) {
		t.Fatalf("stored secret_env = %+v, want %+v", stored, githubSecretEnv)
	}
}
