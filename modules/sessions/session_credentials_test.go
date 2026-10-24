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
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sessions/confine"
)

// A launch that names managed credentials gets, from the Business broker, the location
// of the helpers that use them and nothing else: never the secret, in the environment,
// the run row, the API, the ledger or the stored output. The helpers are released when
// the run ends or the launch fails.

const credentialCanary = "CANARY-db-password-0123456789"

// credentialKeyCanary is a private key as the broker holds it: several lines, the last of them
// shorter than anything the redactor can strip without blanking ordinary words.
const credentialKeyCanary = "-----BEGIN KEY-----\nKEYLINE-one-0123456789abcdef\nKEYLINE-two-0123456789abcdef\nAB=\n-----END KEY-----"

// fakeCredentialBroker is a broker that holds one canary secret and answers with the
// locations a real one would. It records what it was asked and what it released.
type fakeCredentialBroker struct {
	mu       sync.Mutex
	st       store.Store
	dir      string
	reqs     []CredentialBrokerRequest
	err      error
	env      []EnvVar
	released chan string
	bound    chan int
	// misbehaviors a real broker must never show the core: a release handed back with an
	// error, a success that names no variable, and files the child may read.
	releaseWithErr bool
	noEnv          bool
	noBind         bool
	read           []string
}

func newFakeCredentialBroker(t *testing.T) *fakeCredentialBroker {
	t.Helper()
	return &fakeCredentialBroker{dir: t.TempDir(), released: make(chan string, 8), bound: make(chan int, 8)}
}

func (f *fakeCredentialBroker) BrokerSessionCredentials(_ context.Context, req CredentialBrokerRequest) (BrokeredCredentials, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, req)
	release := func() { f.released <- req.RunRef }
	if f.err != nil {
		if f.releaseWithErr {
			return BrokeredCredentials{Release: release}, f.err
		}
		return BrokeredCredentials{}, f.err
	}
	service := filepath.Join(f.dir, req.RunRef+"-pg_service.conf")
	if err := os.WriteFile(service, []byte("[prod-db]\nhost=/run\n"), 0o600); err != nil {
		return BrokeredCredentials{}, err
	}
	env := f.env
	if env == nil {
		env = []EnvVar{{Name: "PGSERVICEFILE", Value: service}, {Name: "SSH_AUTH_SOCK", Value: filepath.Join(f.dir, "agent.sock")}}
	}
	if f.noEnv {
		env = nil
	}
	read := f.read
	if read == nil {
		read = []string{service}
	}
	bind := func(pid int) { f.bound <- pid }
	if f.noBind {
		bind = nil
	}
	return BrokeredCredentials{
		Env: env, Read: read, Withhold: []string{credentialCanary, "short", credentialKeyCanary},
		Detail:  "credentials " + strings.Join(req.Names, ", "),
		Bind:    bind,
		Release: release,
	}, nil
}

func (f *fakeCredentialBroker) requests() []CredentialBrokerRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]CredentialBrokerRequest(nil), f.reqs...)
}

func (f *fakeCredentialBroker) awaitRelease(t *testing.T) string {
	t.Helper()
	select {
	case run := <-f.released:
		return run
	case <-time.After(10 * time.Second):
		t.Fatal("the helpers were not released when the session ended")
		return ""
	}
}

func credentialHarness(t *testing.T) (*Module, model.TenantID, *fakeRunner, *fakeCredentialBroker) {
	t.Helper()
	fr := &fakeRunner{}
	gate := &spyGate{inner: LaunchDecision{Allowed: true}}
	m, st, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()), WithLaunchGate(gate))
	stopModuleAtCleanup(t, m)
	broker := newFakeCredentialBroker(t)
	broker.st = st
	m.CredentialBroker = broker
	return m, tenant, fr, broker
}

func credentialParams(mayUse bool, names ...string) CreateRunParams {
	if len(names) == 0 {
		names = []string{"prod-db"}
	}
	return CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative, PermissionMode: "default",
		Actor: actorU, ActorKind: actorKindU, Credentials: names, MayUseSecretEnv: mayUse,
	}
}

func TestCredentials_TheChildGetsTheHelperLocationsAndNeverTheSecret(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	m, tenant, fr, broker := credentialHarness(t)

	dto, err := createProfiledTestRun(t, m, ctx, tenant, credentialParams(true, "prod-db", "build-host"))
	if err != nil {
		t.Fatalf("create with credentials: %v", err)
	}
	spec := fr.lastSpec()
	if reqs := broker.requests(); len(reqs) != 1 || reqs[0].RunRef != dto.RunRef || reqs[0].Tenant != tenant ||
		strings.Join(reqs[0].Names, ",") != "prod-db,build-host" || reqs[0].Confined {
		t.Fatalf("broker requests = %+v, want one for this run, both names, unconfined", reqs)
	}
	if got, ok := specEnvValue(spec, "SSH_AUTH_SOCK"); !ok || !strings.HasSuffix(got, "agent.sock") {
		t.Fatalf("the child is not told where its agent is: %q %v", got, ok)
	}
	service, ok := specEnvValue(spec, "PGSERVICEFILE")
	if !ok {
		t.Fatal("the child is not told where its service file is")
	}
	if spec.Confinement != nil && !strings.Contains(strings.Join(spec.Confinement.ReadOnly, "\n"), service) {
		t.Fatalf("the confined child is not granted the service file: %v", spec.Confinement.ReadOnly)
	}
	for _, e := range spec.Env {
		if strings.Contains(e.Value, credentialCanary) {
			t.Fatalf("the secret is in the child's environment: %s", e.Name)
		}
	}
	raw, _ := json.Marshal(dto)
	if strings.Contains(string(raw), credentialCanary) || !strings.Contains(string(raw), `"credentials":["prod-db","build-host"]`) {
		t.Fatalf("run DTO = %s, want the names and never the secret", raw)
	}
	rec, err := m.loadRun(ctx, tenant, dto.RunRef)
	if err != nil {
		t.Fatal(err)
	}
	if rec.String(colRunCredentials) != `["prod-db","build-host"]` {
		t.Fatalf("run row credential_names = %q", rec.String(colRunCredentials))
	}
	details := strings.Join(runEventDetails(t, m, tenant, dto.RunRef), "\n")
	if !strings.Contains(details, "credentials prod-db, build-host") || strings.Contains(details, credentialCanary) {
		t.Fatalf("the ledger = %s, want the names and never the secret", details)
	}

	// The helpers go when the session does.
	if _, err := m.stopRun(ctx, tenant, dto.RunRef, actorU, actorKindU); err != nil {
		t.Fatal(err)
	}
	if got := broker.awaitRelease(t); got != dto.RunRef {
		t.Fatalf("released %q, want %q", got, dto.RunRef)
	}
}

func TestCredentials_AConfinedLaunchIsToldItsNetworkIsConfined(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	m, tenant, _, broker := credentialHarness(t)
	m.rt.Creds = CredentialSourceFunc(func(context.Context, CredentialRequest) (Credential, error) {
		return Credential{ID: "cred-1", Token: "tok-secret", Scheme: "mock", NotAfter: farFuture,
			bound: BoundProvider{Kind: ProviderKindAnthropic, Endpoint: "https://api.anthropic.com"}}, nil
	})
	if _, err := createProfiledTestRun(t, m, ctx, tenant, credentialParams(true)); err != nil {
		t.Fatal(err)
	}
	if reqs := broker.requests(); len(reqs) != 1 || !reqs[0].Confined {
		t.Fatalf("broker requests = %+v, want one with Confined set for a provider-bound launch", reqs)
	}
}

func TestCredentials_RefusalsStartNothing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cases := map[string]struct {
		params func(CreateRunParams) CreateRunParams
		err    error
		unwire bool
		status int
		asked  bool
	}{
		"not a tenant administrator": {params: func(p CreateRunParams) CreateRunParams { p.MayUseSecretEnv = false; return p }, status: http.StatusForbidden},
		"an invalid name":            {params: func(p CreateRunParams) CreateRunParams { p.Credentials = []string{"Prod DB"}; return p }, status: http.StatusBadRequest},
		"a path as a name":           {params: func(p CreateRunParams) CreateRunParams { p.Credentials = []string{"../env/x"}; return p }, status: http.StatusBadRequest},
		"a duplicate name":           {params: func(p CreateRunParams) CreateRunParams { p.Credentials = []string{"a", "a"}; return p }, status: http.StatusBadRequest},
		"too many names": {params: func(p CreateRunParams) CreateRunParams {
			p.Credentials = []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"}
			return p
		}, status: http.StatusBadRequest},
		"no broker in this edition": {unwire: true, status: http.StatusNotImplemented},
		"the broker declines":       {err: &CredentialRefusal{Reason: "no credential named prod-db"}, status: http.StatusUnprocessableEntity, asked: true},
		"the broker is down":        {err: errors.New("dial vault: connection refused"), status: http.StatusServiceUnavailable, asked: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			m, tenant, fr, broker := credentialHarness(t)
			broker.err = tc.err
			if tc.unwire {
				m.CredentialBroker = nil
			}
			p := credentialParams(true)
			if tc.params != nil {
				p = tc.params(p)
			}
			_, err := createProfiledTestRun(t, m, ctx, tenant, p)
			if statusOf(err) != tc.status {
				t.Fatalf("create = %v (status %d), want %d", err, statusOf(err), tc.status)
			}
			if err != nil && strings.Contains(err.Error(), "connection refused") {
				t.Fatalf("a refusal repeats the broker's own error to the caller: %v", err)
			}
			if got := len(broker.requests()) > 0; got != tc.asked {
				t.Fatalf("broker asked = %v, want %v", got, tc.asked)
			}
			fr.mu.Lock()
			specs := len(fr.specs)
			fr.mu.Unlock()
			if specs != 0 {
				t.Fatalf("a refused launch reached the runner %d times", specs)
			}
		})
	}
}

// A broker is a closed door: it may locate its helpers and nothing else. Anything that
// would re-point a home, impersonate a provider, change what the child loads or trusts, or
// hand it an engine secret is refused, and what the broker started is released.
func TestCredentials_ABrokerMayNameOnlyItsHelperVariables(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, name := range []string{
		"HOME", "PATH", "LD_PRELOAD", "NODE_OPTIONS", "GIT_SSH_COMMAND", "HTTPS_PROXY", "SSL_CERT_FILE",
		"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "CODEX_HOME", "CLAUDE_CONFIG_DIR", "OLIVARES_WORK_TOKEN",
		"PGPASSWORD", "PGHOST", "DATABASE_URL", "AWS_SECRET_ACCESS_KEY", "SSH_ASKPASS", "BASH_ENV",
		"OLIVARES_CREDENTIAL_MINTER_CONFIG", // the engine's own input shares the broker's prefix
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			m, tenant, fr, broker := credentialHarness(t)
			broker.env = []EnvVar{{Name: name, Value: "/tmp/x"}}
			if _, err := createProfiledTestRun(t, m, ctx, tenant, credentialParams(true)); statusOf(err) != http.StatusServiceUnavailable {
				t.Fatalf("a broker that sets %s: create = %v, want 503", name, err)
			}
			broker.awaitRelease(t)
			fr.mu.Lock()
			specs := len(fr.specs)
			fr.mu.Unlock()
			if specs != 0 {
				t.Fatalf("a launch whose broker set %s reached the runner", name)
			}
		})
	}
	// The sanctioned names pass, including the broker's own prefix.
	m, tenant, fr, broker := credentialHarness(t)
	broker.env = []EnvVar{{Name: "SSH_AUTH_SOCK", Value: "/a"}, {Name: "PGSERVICEFILE", Value: "/b"}, {Name: "OLIVARES_CREDENTIAL_SSH_CONFIG", Value: "/c"}}
	if _, err := createProfiledTestRun(t, m, ctx, tenant, credentialParams(true)); err != nil {
		t.Fatalf("sanctioned variables refused: %v", err)
	}
	if _, ok := specEnvValue(fr.lastSpec(), "OLIVARES_CREDENTIAL_SSH_CONFIG"); !ok {
		t.Fatal("the broker's own locator did not reach the child")
	}
}

// A variable the launch already carries (a secret_env of the same name) is not silently
// replaced or shadowed: the launch is refused.
func TestCredentials_ABrokerCannotShadowAVariableTheLaunchSet(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	m, tenant, _, broker := credentialHarness(t)
	vault := newFakeVault()
	vault.values[tenant.String()+"|env/agent"] = "/tmp/attacker.sock"
	m.ProviderVault = vault
	p := credentialParams(true)
	p.SecretEnv = []SecretEnvRef{{Env: "SSH_AUTH_SOCK", Secret: "env/agent"}}
	if _, err := createProfiledTestRun(t, m, ctx, tenant, p); statusOf(err) != http.StatusServiceUnavailable {
		t.Fatalf("create = %v, want 503: the broker's SSH_AUTH_SOCK shadows the launch's own", err)
	}
	broker.awaitRelease(t)
}

func TestCredentials_ALaunchThatFailsAfterTheBrokerReleases(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	m, tenant, fr, broker := credentialHarness(t)
	fr.launchErr = errors.New("synthetic spawn failure")
	if _, err := createProfiledTestRun(t, m, ctx, tenant, credentialParams(true)); err == nil {
		t.Fatal("the launch did not fail")
	}
	broker.awaitRelease(t)
}

func TestCredentials_OutputNeverCarriesAWithheldValue(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	m, tenant, fr, _ := credentialHarness(t)
	dto, err := createProfiledTestRun(t, m, ctx, tenant, credentialParams(true))
	if err != nil {
		t.Fatal(err)
	}
	fr.lastProc().out <- OutputFrame{Stream: streamStderr, Data: []byte(`psql: error: FATAL: password "` + credentialCanary + `" rejected`)}
	fr.lastProc().out <- OutputFrame{Stream: streamStdout, Data: []byte("a short word stays: short, and so does AB= and the key's END line")}
	// A key printed whole, as stream-json carries it (one JSON string, the line breaks escaped).
	fr.lastProc().out <- OutputFrame{Stream: streamStdout, Data: []byte(`{"text":"KEYLINE-one-0123456789abcdef\nKEYLINE-two-0123456789abcdef\nAB="}`)}
	lr, ok := m.rt.getLive(tenant, dto.RunRef)
	if !ok {
		t.Fatal("expected a live run")
	}
	waitFor(t, "all frames in the ring", func() bool { return len(lr.ring.readFrom(0).frames) >= 3 })
	var all strings.Builder
	for _, f := range lr.ring.readFrom(0).frames {
		all.Write(f.Data)
		all.WriteByte('\n')
	}
	out := all.String()
	if strings.Contains(out, credentialCanary) || !strings.Contains(out, "[secret credential]") {
		t.Fatalf("session output = %s, want the value withheld as [secret credential]", out)
	}
	if !strings.Contains(out, "a short word stays: short, and so does AB= and the key's END line") {
		t.Fatalf("a value below the redaction floor blanked ordinary output: %s", out)
	}
	if strings.Contains(out, "KEYLINE") || strings.Count(out, "[secret credential]") < 3 {
		t.Fatalf("a line of the private key reaches the output: %s", out)
	}
}

func TestCredentials_ResumeBrokersAgainAndAsksAgain(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	m, tenant, fr, broker := credentialHarness(t)
	dto, err := createProfiledTestRun(t, m, ctx, tenant, credentialParams(true, "prod-db", "build-host"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.stopRun(ctx, tenant, dto.RunRef, actorU, actorKindU); err != nil {
		t.Fatal(err)
	}
	broker.awaitRelease(t)

	if _, err := m.resumeRunAs(ctx, tenant, dto.RunRef, actorU, actorKindU, "", false); statusOf(err) != http.StatusForbidden {
		t.Fatalf("resume by someone who may not give it = %v, want 403", err)
	}
	if got := len(broker.requests()); got != 1 {
		t.Fatalf("a refused resume reached the broker: %d requests", got)
	}
	if _, err := m.resumeRunAs(ctx, tenant, dto.RunRef, actorU, actorKindU, "", true); err != nil {
		t.Fatalf("resume: %v", err)
	}
	reqs := broker.requests()
	if len(reqs) != 2 || strings.Join(reqs[1].Names, ",") != "prod-db,build-host" {
		t.Fatalf("broker requests = %+v, want the stored names asked again", reqs)
	}
	if _, ok := specEnvValue(fr.lastSpec(), "PGSERVICEFILE"); !ok {
		t.Fatal("the resumed child has no helper locations")
	}
	if details := strings.Join(runEventDetails(t, m, tenant, dto.RunRef), "\n"); strings.Count(details, "credentials prod-db, build-host") != 2 {
		t.Fatalf("the ledger does not record both launches:\n%s", details)
	}
}

// A session given a managed credential is not carried to another account or tool: what it
// printed cannot be proven free of a value no current secret can redact.
func TestCredentials_AGivenSessionCountsAsHavingBeenGivenSecrets(t *testing.T) {
	t.Parallel()
	if !givenSecrets(model.Record{colRunCredentials: `["prod-db"]`}) {
		t.Fatal("a run given a managed credential is not counted as given secrets")
	}
	if givenSecrets(model.Record{}) {
		t.Fatal("a run given nothing counts as given secrets")
	}
	err := refuseGivenSecrets(model.Record{colRunCredentials: `["prod-db"]`}, "moved")
	if statusOf(err) != http.StatusConflict || !strings.Contains(err.Error(), "managed credential") {
		t.Fatalf("moving a session given a credential = %v, want 409 naming a managed credential", err)
	}
}

// What a refusal before anything durable leaves behind is nothing: no run row.
func TestCredentials_AnEarlyRefusalLeavesNoRun(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cases := map[string]struct {
		params func(CreateRunParams) CreateRunParams
		unwire bool
		status int
	}{
		"not a tenant administrator": {params: func(p CreateRunParams) CreateRunParams { p.MayUseSecretEnv = false; return p }, status: http.StatusForbidden},
		"an invalid name":            {params: func(p CreateRunParams) CreateRunParams { p.Credentials = []string{"Prod DB"}; return p }, status: http.StatusBadRequest},
		"no broker in this edition":  {unwire: true, status: http.StatusNotImplemented},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			m, tenant, _, broker := credentialHarness(t)
			if tc.unwire {
				m.CredentialBroker = nil
			}
			p := credentialParams(true)
			if tc.params != nil {
				p = tc.params(p)
			}
			if _, err := createProfiledTestRun(t, m, ctx, tenant, p); statusOf(err) != tc.status {
				t.Fatalf("create = %v, want %d", err, tc.status)
			}
			if n := countRuns(t, broker.st, tenant); n != 0 {
				t.Fatalf("a refused launch left %d run rows", n)
			}
		})
	}
}

// A confined child is granted the files the broker made for it, and the brokered values are
// withheld line by line (the short last line of a key stays out of the redactor).
func TestCredentials_AConfinedChildIsGrantedTheBrokersFiles(t *testing.T) {
	t.Parallel()
	m, tenant, _, broker := credentialHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	p := credentialParams(true)
	spec := LaunchSpec{Confinement: &confine.Policy{}}
	if err := m.configureCredentials(ctx, ctx, tenant, "run-confined", &p, &spec); err != nil {
		t.Fatal(err)
	}
	service, _ := specEnvValue(spec, "PGSERVICEFILE")
	if service == "" || !slices.Contains(spec.Confinement.ReadOnly, service) {
		t.Fatalf("service file %q is not granted to the confined child: %v", service, spec.Confinement.ReadOnly)
	}
	for _, line := range []string{credentialCanary, "KEYLINE-one-0123456789abcdef", "KEYLINE-two-0123456789abcdef", "-----BEGIN KEY-----"} {
		if !slices.Contains(p.credentialWithhold, line) {
			t.Errorf("%q is not withheld", line)
		}
	}
	for _, line := range p.credentialWithhold {
		if len(line) < minSecretValue {
			t.Errorf("withheld %q is shorter than the redaction floor", line)
		}
	}
	cancel()
	broker.awaitRelease(t)
}

// A container child cannot reach the engine's sockets, so the launch is refused before the broker starts anything.
func TestCredentials_AContainerSessionIsRefusedBeforeTheBrokerStartsAnything(t *testing.T) {
	t.Parallel()
	m, tenant, _, broker := credentialHarness(t)
	for _, isolation := range []Isolation{IsolationContainer, IsolationSandbox} {
		p := credentialParams(true)
		spec := LaunchSpec{Isolation: isolation}
		err := m.configureCredentials(context.Background(), context.Background(), tenant, "run-container", &p, &spec)
		if statusOf(err) != http.StatusUnprocessableEntity || !strings.Contains(err.Error(), "native isolation") {
			t.Errorf("%s: %v, want 422 naming native isolation", isolation, err)
		}
	}
	if got := len(broker.requests()); got != 0 {
		t.Fatalf("the broker was asked %d times for a session that cannot use its helpers", got)
	}
}

// The helpers go when the child exits by itself, not only when someone stops the run.
func TestCredentials_AreReleasedWhenTheSessionEndsByItself(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	m, tenant, fr, broker := credentialHarness(t)
	if _, err := createProfiledTestRun(t, m, ctx, tenant, credentialParams(true)); err != nil {
		t.Fatal(err)
	}
	fr.lastProc().finish(0)
	broker.awaitRelease(t)
}

// A resume is judged on today's edition and today's broker, not on the stored row.
func TestCredentials_ResumeRefusesWhenTheBrokerIsGoneRefusesOrFails(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for name, tc := range map[string]struct {
		prepare   func(*Module, *fakeCredentialBroker)
		status    int
		untouched bool // refused before the run changes; the others fail the launch, as any launch failure does
	}{
		"the edition has no broker now": {func(m *Module, _ *fakeCredentialBroker) { m.CredentialBroker = nil }, http.StatusNotImplemented, true},
		"the broker declines now": {func(_ *Module, b *fakeCredentialBroker) {
			b.err = &CredentialRefusal{Reason: "no credential named prod-db"}
		}, http.StatusUnprocessableEntity, false},
		"the broker fails now": {func(_ *Module, b *fakeCredentialBroker) { b.err = errors.New("vault down") }, http.StatusServiceUnavailable, false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			m, tenant, fr, broker := credentialHarness(t)
			dto, err := createProfiledTestRun(t, m, ctx, tenant, credentialParams(true))
			if err != nil {
				t.Fatal(err)
			}
			stopped, err := m.stopRun(ctx, tenant, dto.RunRef, actorU, actorKindU)
			if err != nil {
				t.Fatal(err)
			}
			broker.awaitRelease(t)
			tc.prepare(m, broker)
			if _, err := m.resumeRunAs(ctx, tenant, dto.RunRef, actorU, actorKindU, "", true); statusOf(err) != tc.status {
				t.Fatalf("resume = %v (status %d), want %d", err, statusOf(err), tc.status)
			}
			if got := launchCount(fr); got != 1 {
				t.Fatalf("the runner was launched %d times, want only the first launch", got)
			}
			if !tc.untouched {
				return
			}
			// The early refusal comes before the run changes: it is still the stopped session it
			// was, with no new generation recorded.
			after, err := m.getRun(ctx, tenant, dto.RunRef)
			if err != nil {
				t.Fatal(err)
			}
			if after.State != stopped.State || after.LastEventSeq != stopped.LastEventSeq {
				t.Fatalf("a refused resume changed the run: state %q seq %d, was %q seq %d",
					after.State, after.LastEventSeq, stopped.State, stopped.LastEventSeq)
			}
		})
	}
}

// A broker that names one variable twice would let the later value win unseen.
func TestCredentials_ABrokerThatNamesAVariableTwiceIsRefused(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	m, tenant, fr, broker := credentialHarness(t)
	broker.env = []EnvVar{{Name: "SSH_AUTH_SOCK", Value: "/a"}, {Name: "SSH_AUTH_SOCK", Value: "/b"}}
	if _, err := createProfiledTestRun(t, m, ctx, tenant, credentialParams(true)); statusOf(err) != http.StatusServiceUnavailable {
		t.Fatalf("create = %v, want 503", err)
	}
	broker.awaitRelease(t)
	if launchCount(fr) != 0 {
		t.Fatal("a launch whose broker named a variable twice reached the runner")
	}
}

func TestValidateCredentialNames(t *testing.T) {
	t.Parallel()
	ok := [][]string{{"a"}, {"prod-db", "build.host", "api_1"}, {"a", "b", "c", "d", "e", "f", "g", "h"}, {strings.Repeat("a", 64)}}
	bad := [][]string{{""}, {"Prod"}, {"-lead"}, {".lead"}, {"a b"}, {"a/b"}, {"a\n"}, {strings.Repeat("a", 65)}, {"a", "a"}, {"a", "b", "c", "d", "e", "f", "g", "h", "i"}}
	for _, names := range ok {
		if err := validateCredentialNames(names); err != nil {
			t.Errorf("%q refused: %v", names, err)
		}
	}
	for _, names := range bad {
		if err := validateCredentialNames(names); statusOf(err) != http.StatusBadRequest {
			t.Errorf("%q accepted or wrong status: %v", names, err)
		}
	}
}
