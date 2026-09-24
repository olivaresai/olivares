// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	claudeapi "github.com/olivaresai/olivares/connectors/claude-api"
	"github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/connectors/modelrouter"
	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/auth"
	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/residency"
	"github.com/olivaresai/olivares/core/secret"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/inferenceproxy"
	"github.com/olivaresai/olivares/modules/knowledge"
	"github.com/olivaresai/olivares/modules/models"
	"github.com/olivaresai/olivares/sdk"
)

// modelsguardedtext_test.go qualifies the governed Chat composition against BOUNDED LOCAL
// FIXTURES: a real TLS server with a trusted test CA, the real C0 codec, the real C1
// transport, the real immutable registry, the real secret resolver and a real store. No
// provider is contacted; every "upstream" here is a loopback synthetic.
//
// The causal claims these tests make are about ORDER and ABSENCE — which stage ran, which
// did not, and how many times the model endpoint was POSTed — because those are the
// properties a struct-shaped assertion cannot establish.

// --- fixtures --------------------------------------------------------------------------

// countingTransport counts the requests that actually reach the network and records the
// exact bytes and headers of each, so a test can assert "at most one dispatch" and "these
// exact prepared bytes" rather than inferring both from a response.
type countingTransport struct {
	mu       sync.Mutex
	base     http.RoundTripper
	requests []recordedRequest
}

type recordedRequest struct {
	method, url, authorization string
	body                       []byte
}

func (c *countingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	record := recordedRequest{method: r.Method, url: r.URL.String(), authorization: r.Header.Get("Authorization")}
	if r.Body != nil {
		body, _ := io.ReadAll(r.Body)
		record.body = body
		r.Body = io.NopCloser(strings.NewReader(string(body)))
		r.ContentLength = int64(len(body))
	}
	c.mu.Lock()
	c.requests = append(c.requests, record)
	c.mu.Unlock()
	return c.base.RoundTrip(r)
}

func (c *countingTransport) calls() []recordedRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]recordedRequest(nil), c.requests...)
}

func (c *countingTransport) count() int { return len(c.calls()) }

// stubProxyPolicy is the tenant governance posture under test. It is a fake because the
// posture is the INPUT to these causals, not the thing under test; the real loader has its
// own suite and the real digest has its own.
type stubProxyPolicy struct {
	policy inferenceproxy.ProxyPolicy
	err    error
	calls  int
}

func (s *stubProxyPolicy) Policy(context.Context, model.TenantID) (inferenceproxy.ProxyPolicy, error) {
	s.calls++
	if s.err != nil {
		return inferenceproxy.ProxyPolicy{}, s.err
	}
	return s.policy, nil
}

type stubContextPolicy struct {
	effective knowledge.EffectivePolicy
	err       error
	calls     int
}

func (s *stubContextPolicy) Apply(context.Context, model.TenantID, knowledge.ContextPolicyQuery) (knowledge.EffectivePolicy, error) {
	s.calls++
	return s.effective, s.err
}

// stubInspector stands in for the enterprise content firewall. The community build injects
// nil, so a fixture that wants to prove "a configured inspector is never silently absent"
// has to supply one.
type stubInspector struct {
	request, response claudeapi.ContentInspectionDecision
	seen              []claudeapi.ContentInspectionInput
}

func (s *stubInspector) Inspect(_ context.Context, in claudeapi.ContentInspectionInput) claudeapi.ContentInspectionDecision {
	s.seen = append(s.seen, in)
	if in.Direction == claudeapi.InspectDirectionRequest {
		return s.request
	}
	return s.response
}

// chatFixture wires one complete governed Chat path over local fixtures.
type chatFixture struct {
	t         *testing.T
	tenant    model.TenantID
	store     store.Store
	exec      *modelsChatExecutor
	profile   models.ExecutionProfile
	request   models.ChatExecutionRequest
	server    *httptest.Server
	transport *countingTransport
	policy    *stubProxyPolicy
	context   *stubContextPolicy
	env       map[string]string
	budget    *countingBudget
	handler   *chatUpstream
}

// chatUpstream is the synthetic loopback "gateway". It records what it was asked and
// answers with whatever the test staged.
type chatUpstream struct {
	mu      sync.Mutex
	path    string
	status  int
	body    string
	headers map[string]string
	delay   time.Duration
	hits    int
}

func (u *chatUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	u.mu.Lock()
	u.hits++
	u.path = r.URL.Path
	status, body, headers, delay := u.status, u.body, u.headers, u.delay
	u.mu.Unlock()
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return
		}
	}
	for k, v := range headers {
		w.Header().Set(k, v)
	}
	w.Header().Set("Content-Type", "application/json")
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

// countingBudget is the handler's precheck closure under test conditions: it records every
// call so "exactly once" is a measurement rather than a hope.
type countingBudget struct {
	status int
	denied bool
	calls  int
	// onCall runs inside the precheck, which is the ONLY place a test can change the world
	// after entry validation has accepted a snapshot and before the intent is anchored.
	// That window is exactly where a configuration reload would land in production.
	onCall func()
}

func (b *countingBudget) check(context.Context) (int, bool) {
	b.calls++
	if b.onCall != nil {
		b.onCall()
	}
	return b.status, b.denied
}

const chatFixtureModel = "gw-model-1"

func chatCompletionBody(modelRef, content string) string {
	return `{"object":"chat.completion","model":"` + modelRef + `","choices":[{"index":0,"finish_reason":"stop",` +
		`"message":{"role":"assistant","content":"` + content + `"}}],"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18}}`
}

type chatFixtureOptions struct {
	authScheme string
	allowHTTP  bool
	plainHTTP  bool
	dsn        string
	spoolBytes int64
	spoolMode  store.AuditSpoolMode
	policy     *inferenceproxy.ProxyPolicy
	inspector  contentInspector
	residency  *residency.Registry
}

func newChatFixture(t *testing.T, opts chatFixtureOptions) *chatFixture {
	t.Helper()
	ctx := context.Background()
	if opts.authScheme == "" {
		opts.authScheme = "bearer"
	}

	upstream := &chatUpstream{body: chatCompletionBody(chatFixtureModel, "hello from the fixture")}
	var server *httptest.Server
	if opts.plainHTTP {
		server = httptest.NewServer(upstream)
	} else {
		server = httptest.NewTLSServer(upstream)
	}
	t.Cleanup(server.Close)

	// The client trusts the SERVER'S OWN test CA and nothing else. A dispatch that reached
	// a different origin would fail verification rather than silently succeed.
	transport := &countingTransport{base: server.Client().Transport}

	endpoint := server.URL + "/v1/chat/completions"
	origin, err := profileEndpointOrigin(endpoint, opts.allowHTTP || opts.plainHTTP)
	if err != nil {
		t.Fatalf("fixture endpoint origin: %v", err)
	}

	st, tenant, _ := newChatStore(t, opts)

	env := map[string]string{}
	credentialRef := ""
	if opts.authScheme == "bearer" {
		// A DEDICATED synthetic bearer: it exists only for this fixture and is resolved
		// through the real resolver from a real reference, not injected as a literal.
		env["CHAT_FIXTURE_BEARER"] = "fixture-token-aaaaaaaaaaaa"
		credentialRef = "env:CHAT_FIXTURE_BEARER"
	}
	registry := chatFixtureRegistry(t, tenant, endpoint, origin, opts.authScheme, credentialRef, opts.allowHTTP || opts.plainHTTP)
	profile, err := registry.ResolveExecutionProfile(ctx, tenant, "chat-primary", chatFixtureRevision(t, registry, tenant))
	if err != nil {
		t.Fatalf("fixture profile: %v", err)
	}

	policy := &stubProxyPolicy{policy: chatDefaultPolicy()}
	if opts.policy != nil {
		policy.policy = *opts.policy
	}
	contextPolicy := &stubContextPolicy{}

	exec := &modelsChatExecutor{
		registry: registry, inspector: opts.inspector, clock: time.Now, log: discardLog(),
	}
	if !exec.bind(chatExecutorDeps{
		Store: st, Policy: policy, ContextPolicy: contextPolicy, Residency: opts.residency,
		Secrets: chatFixtureResolver(env), HTTPClient: &http.Client{Transport: transport},
	}) {
		t.Fatal("fixture chat executor did not become ready")
	}

	principal := auth.ScopedPrincipal(model.NewID(), "chat-fixture-agent", tenant, auth.RoleEditor)
	spec := [32]byte{}
	copy(spec[:], sha256.New().Sum([]byte("routing-spec")))
	return &chatFixture{
		t: t, tenant: tenant, store: st, exec: exec, profile: profile, server: server,
		transport: transport, policy: policy, context: contextPolicy, env: env,
		budget:  &countingBudget{},
		handler: upstream,
		request: models.ChatExecutionRequest{
			Tenant: tenant, Principal: principal, PolicyID: model.NewID(), PolicyVersion: 3,
			PolicySpecDigest: spec, Profile: profile,
			Target: modelrouter.Target{ProviderRef: profile.ProviderRef, ModelRef: profile.ModelRef},
			Input:  "the governed prompt", MaxTokens: 64, SessionRef: "attribution-only",
		},
	}
}

// newChatStore opens the evidence store. With a spool budget it uses the two-phase
// file-backed technique the F9 causals already use: provision with no budget, reopen with a
// 1-byte one, so every governed append after that is over budget.
func newChatStore(t *testing.T, opts chatFixtureOptions) (store.Store, model.TenantID, *audit.Signer) {
	t.Helper()
	ctx := context.Background()
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate audit key: %v", err)
	}
	signer, err := audit.NewSigner(priv)
	if err != nil {
		t.Fatalf("audit signer: %v", err)
	}
	dsn := opts.dsn
	if dsn == "" {
		dsn = filepath.Join(t.TempDir(), "chat.db")
	}
	seed, err := coreengine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: dsn, SignEvent: signer.SignEvent}, nil)
	if err != nil {
		t.Fatalf("open seed store: %v", err)
	}
	var tenant model.TenantID
	if err := seed.System(ctx, func(sys store.SystemScope) error {
		if _, err := sys.EnsureSystemTenant(ctx); err != nil {
			return err
		}
		org, err := sys.CreateOrg(ctx, model.Org{Name: "chat-fixture", Slug: "chat-fixture", Status: model.StatusActive})
		if err == nil {
			tenant = org.TenantID
		}
		return err
	}); err != nil {
		t.Fatalf("provision fixture tenant: %v", err)
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("close seed store: %v", err)
	}
	cfg := store.Config{Engine: store.EngineSQLite, DSN: dsn, SignEvent: signer.SignEvent}
	if opts.spoolBytes > 0 {
		cfg.AuditSpoolMaxBytes, cfg.AuditSpoolOnFull = opts.spoolBytes, opts.spoolMode
	}
	st, err := coreengine.Open(ctx, cfg, nil)
	if err != nil {
		t.Fatalf("open fixture store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st, tenant, signer
}

func chatDefaultPolicy() inferenceproxy.ProxyPolicy {
	// The stock safe posture, plus a permissive "*" so the deterministic classifier is not
	// the thing under test in the happy path.
	return inferenceproxy.PolicyWithDLPRules(inferenceproxy.ProxyPolicy{
		Configured: true, ResponseDLPMode: inferenceproxy.ResponseDLPBuffer,
		RecordMandatory: true, RecordMandatoryChosen: true,
		GateModelAccess: true, GateBudget: true, GateResidency: true,
		GateContextWindow: true, GateDLPRequest: true, GateDLPResponse: true,
	}, map[string]string{"*": "allow", "unscanned": "allow"})
}

// chatFixtureRegistry builds the real immutable registry from a real JSON document, so the
// profile under test went through the same strict loader production uses.
func chatFixtureRegistry(t *testing.T, tenant model.TenantID, endpoint, origin, authScheme, credentialRef string, allowHTTP bool) *modelGatewayProfileRegistry {
	t.Helper()
	entry := modelGatewayProfileConfig{
		Ref: "chat-primary", TenantRef: tenant.String(),
		Action: models.ExecutionActionTextGenerate, Protocol: models.ExecutionProtocolChatTextV1,
		AdapterID: models.ExecutionAdapterModelProviderChat, AdapterVersion: models.ExecutionAdapterVersion1,
		ProviderRef: "fixture-gateway", ModelRef: chatFixtureModel, Endpoint: endpoint,
		Surface: "direct", InferenceGeo: "us", CredentialAudience: origin,
		AuthScheme: authScheme, CredentialRef: credentialRef, AllowHTTP: allowHTTP,
		MaxRequestBytes: 1 << 20, MaxResponseBytes: 1 << 20, TimeoutMS: 5000,
	}
	revision, err := calculateModelGatewayProfileRevision(entry)
	if err != nil {
		t.Fatalf("fixture revision: %v", err)
	}
	entry.Revision = revision
	doc, err := json.Marshal(map[string]any{
		"schema_version": modelGatewayProfilesV1,
		"profiles":       []modelGatewayProfileConfig{entry},
	})
	if err != nil {
		t.Fatalf("marshal fixture document: %v", err)
	}
	decoded, err := decodeModelGatewayProfiles(doc)
	if err != nil {
		t.Fatalf("decode fixture document: %v", err)
	}
	registry, err := newModelGatewayProfileRegistry(decoded)
	if err != nil {
		t.Fatalf("build fixture registry: %v", err)
	}
	return registry
}

func chatFixtureRevision(t *testing.T, r *modelGatewayProfileRegistry, tenant model.TenantID) string {
	t.Helper()
	for key := range r.profiles {
		if key.tenant == tenant {
			return key.revision
		}
	}
	t.Fatal("fixture registry holds no profile for the tenant")
	return ""
}

// chatFixtureResolver is the REAL resolver over the env handler, so a reference genuinely
// resolves and a missing one genuinely fails closed.
func chatFixtureResolver(env map[string]string) *secret.Resolver {
	return secret.NewResolver(map[string]secret.Handler{
		secret.SchemeEnv: secret.EnvHandler{Lookup: func(k string) (string, bool) {
			v, ok := env[k]
			return v, ok && v != ""
		}},
	})
}

func (f *chatFixture) run() (models.ChatExecutionResult, error) {
	f.t.Helper()
	return f.exec.ExecuteChat(context.Background(), f.request, f.budget.check)
}

func (f *chatFixture) runCtx(ctx context.Context) (models.ChatExecutionResult, error) {
	f.t.Helper()
	return f.exec.ExecuteChat(ctx, f.request, f.budget.check)
}

// chatLedgerRecord is one committed event plus the CANONICAL metadata string the chain
// hash actually commits to.
//
// It reads through the CanonicalWalker capability rather than Walk on purpose: Walk
// deliberately returns a nil Meta ("the canonical string is authoritative; callers
// re-parse if needed"), so a test that asserted over ev.Meta would be asserting over an
// always-empty map — it would pass whatever the code recorded, including a prompt.
type chatLedgerRecord struct {
	event model.AuditEvent
	meta  map[string]any
	raw   string
}

func (f *chatFixture) auditRecords() []chatLedgerRecord {
	f.t.Helper()
	var records []chatLedgerRecord
	if err := f.store.View(context.Background(), f.tenant, func(sc store.Scope) error {
		walker, ok := sc.Audit().(store.CanonicalWalker)
		if !ok {
			f.t.Fatal("this store does not expose the canonical audit walk; the metadata assertions would be vacuous")
		}
		return walker.WalkCanonical(context.Background(), 0, func(ev model.AuditEvent, metaCanonical string, _ []byte) error {
			record := chatLedgerRecord{event: ev, raw: metaCanonical}
			if metaCanonical != "" {
				if err := json.Unmarshal([]byte(metaCanonical), &record.meta); err != nil {
					return err
				}
			}
			records = append(records, record)
			return nil
		})
	}); err != nil {
		f.t.Fatalf("walk fixture ledger: %v", err)
	}
	return records
}

func (f *chatFixture) auditEvents() []model.AuditEvent {
	f.t.Helper()
	var events []model.AuditEvent
	for _, record := range f.auditRecords() {
		events = append(events, record.event)
	}
	return events
}

func (f *chatFixture) recordsWithAction(action string) []chatLedgerRecord {
	var out []chatLedgerRecord
	for _, record := range f.auditRecords() {
		if record.event.Action == action {
			out = append(out, record)
		}
	}
	return out
}

func (f *chatFixture) eventsWithAction(action string) []model.AuditEvent {
	var out []model.AuditEvent
	for _, record := range f.recordsWithAction(action) {
		out = append(out, record.event)
	}
	return out
}

func chatErrorCode(t *testing.T, err error) string {
	t.Helper()
	var e *models.ChatExecutionError
	if !errors.As(err, &e) {
		t.Fatalf("error is not a *models.ChatExecutionError: %v", err)
	}
	return e.Code
}

// --- group 1: the real path end to end ---------------------------------------------------

// TestChatDispatchesExactlyOnceToTheConfiguredEndpointWithTheResolvedBearer is the positive
// causal: ONE POST, at the exact configured path, over TLS the fixture's own CA anchors,
// carrying the dedicated synthetic bearer and the EXACT bytes C0 prepared.
func TestChatDispatchesExactlyOnceToTheConfiguredEndpointWithTheResolvedBearer(t *testing.T) {
	f := newChatFixture(t, chatFixtureOptions{})
	res, err := f.run()
	if err != nil {
		t.Fatalf("ExecuteChat: %v", err)
	}
	calls := f.transport.calls()
	if len(calls) != 1 {
		t.Fatalf("dispatched %d times, want exactly 1", len(calls))
	}
	got := calls[0]
	want, _ := url.Parse(f.profile.Endpoint)
	if got.method != http.MethodPost || !strings.HasSuffix(got.url, want.Path) {
		t.Fatalf("dispatch = %s %s, want POST ...%s", got.method, got.url, want.Path)
	}
	if got.authorization != "Bearer "+f.env["CHAT_FIXTURE_BEARER"] {
		t.Fatalf("authorization header did not carry the resolved reference value")
	}
	// The bytes on the wire ARE the prepared bytes: C0 marshalled once, nothing re-marshalled.
	prepared, perr := modelprovider.PrepareChatTextRequest(modelprovider.ChatTextRequest{
		Model: f.profile.ModelRef, Input: f.request.Input, MaxCompletionTokens: f.request.MaxTokens,
	})
	if perr != nil {
		t.Fatalf("recompute prepared: %v", perr)
	}
	if string(got.body) != string(prepared.Bytes()) {
		t.Fatalf("dispatched body != C0 prepared bytes\n got: %s\nwant: %s", got.body, prepared.Bytes())
	}
	if sha256.Sum256(got.body) != prepared.Digest() {
		t.Fatal("dispatched body digest != the prepared digest the evidence bound")
	}
	if res.DispatchState != models.ChatDispatchAttempted || res.CompletionState != models.ChatCompletionStop {
		t.Fatalf("states = %s/%s", res.DispatchState, res.CompletionState)
	}
	if res.Output == nil || *res.Output != "hello from the fixture" {
		t.Fatalf("output = %v", res.Output)
	}
	if res.UsageStatus != models.ChatUsageReported || res.Usage == nil ||
		res.Usage.PromptTokens == nil || *res.Usage.PromptTokens != 11 {
		t.Fatalf("usage = %s %+v", res.UsageStatus, res.Usage)
	}
	if res.IntentDisposition != models.ChatIntentAnchored || res.OutcomeDisposition != models.ChatOutcomeAnchored {
		t.Fatalf("dispositions = %s/%s", res.IntentDisposition, res.OutcomeDisposition)
	}
	if res.BudgetAssurance != models.ChatBudgetAssuranceDevelopmentPrecheck {
		t.Fatalf("budget assurance = %q; this slice can claim nothing stronger", res.BudgetAssurance)
	}
	if f.budget.calls != 1 {
		t.Fatalf("budget precheck ran %d times, want exactly 1", f.budget.calls)
	}
}

// TestChatAuthNoneOverOptedInLoopbackHTTPResolvesNoSecret pairs the bearer path: an
// explicitly credential-less profile over an explicitly opted-in HTTP loopback must reach
// the endpoint with NO Authorization header and must not consult the resolver at all.
func TestChatAuthNoneOverOptedInLoopbackHTTPResolvesNoSecret(t *testing.T) {
	f := newChatFixture(t, chatFixtureOptions{authScheme: "none", plainHTTP: true})
	// A resolver with NO handlers: any resolution attempt fails closed, so a passing test
	// proves none was made rather than that one happened to succeed.
	f.exec.secrets = secret.NewResolver(map[string]secret.Handler{})
	res, err := f.run()
	if err != nil {
		t.Fatalf("ExecuteChat: %v", err)
	}
	calls := f.transport.calls()
	if len(calls) != 1 || calls[0].authorization != "" {
		t.Fatalf("auth-none dispatch carried an Authorization header: %+v", calls)
	}
	if res.CompletionState != models.ChatCompletionStop {
		t.Fatalf("completion = %s", res.CompletionState)
	}
}

// TestChatDoesNotFollowARedirectToADifferentServer: C1 disables redirects in its private
// client copy, so a 302 to another origin must surface as an upstream failure and the
// second server must never be touched.
func TestChatDoesNotFollowARedirectToADifferentServer(t *testing.T) {
	elsewhere := &chatUpstream{body: chatCompletionBody(chatFixtureModel, "from the wrong server")}
	other := httptest.NewTLSServer(elsewhere)
	t.Cleanup(other.Close)

	f := newChatFixture(t, chatFixtureOptions{})
	f.handler.mu.Lock()
	f.handler.status = http.StatusFound
	f.handler.headers = map[string]string{"Location": other.URL + "/v1/chat/completions"}
	f.handler.mu.Unlock()

	_, err := f.run()
	if code := chatErrorCode(t, err); code != models.ChatErrUpstreamUnavailable {
		t.Fatalf("redirect = %s, want %s", code, models.ChatErrUpstreamUnavailable)
	}
	if elsewhere.hits != 0 {
		t.Fatalf("the redirect target was reached %d times", elsewhere.hits)
	}
	if f.transport.count() != 1 {
		t.Fatalf("dispatched %d times", f.transport.count())
	}
}

// --- group 2: ordering, and every gate's zero-dispatch deny ---------------------------------

// TestChatGateDeniesReachNoSecretAndNoModelPost walks each local gate this composition owns
// and proves the same two absences for every one of them: nothing was POSTed to the model
// endpoint and no credential was resolved. Each case is paired with the happy path above,
// which reaches both.
func TestChatGateDeniesReachNoSecretAndNoModelPost(t *testing.T) {
	tests := []struct {
		name    string
		arrange func(*chatFixture)
		code    string
		// budgetCalls is the ordering claim: a gate BEFORE admission leaves the precheck
		// unrun, and a gate after it does not.
		budgetCalls int
	}{
		{
			name: "governance_plane_unreadable_is_deny_closed",
			arrange: func(f *chatFixture) {
				f.policy.err = errors.New("decision plane down")
			},
			code: models.ChatErrGovernanceUnavailable,
		},
		{
			name: "residency_incompatible_geo",
			arrange: func(f *chatFixture) {
				f.exec.residency = chatResidencyRegistry(t, "eu")
				chatPinTenantRegion(t, f, "eu") // the profile declares "us"
			},
			code: models.ChatErrResidencyDenied,
		},
		{
			name: "context_policy_denies",
			arrange: func(f *chatFixture) {
				f.context.effective = knowledge.EffectivePolicy{Deny: true, DenyReason: "subject forbidden"}
			},
			code: models.ChatErrContextPolicyDenied,
		},
		{
			name: "hard_context_limit_cannot_be_verified",
			arrange: func(f *chatFixture) {
				f.context.effective = knowledge.EffectivePolicy{MaxContextTokens: 1000}
			},
			code: models.ChatErrContextLimitUnverifiable,
		},
		{
			name: "required_redaction_is_incompatible",
			arrange: func(f *chatFixture) {
				f.context.effective = knowledge.EffectivePolicy{RedactionRequired: true}
			},
			code: models.ChatErrRedactionUnsupported,
		},
		{
			name: "enforced_max_tokens_ceiling_denies_instead_of_clamping",
			arrange: func(f *chatFixture) {
				pol := f.policy.policy
				pol.Ceilings = inferenceproxy.RequestCeilings{Enforce: true, MaxTokens: 8}
				f.policy.policy = pol
			},
			code: models.ChatErrRequestCeilingExceeded,
		},
		{
			name: "core_dlp_denies_the_prompt",
			arrange: func(f *chatFixture) {
				f.policy.policy = inferenceproxy.PolicyWithDLPRules(f.policy.policy, map[string]string{"*": "deny"})
				f.request.Input = "AKIAIOSFODNN7EXAMPLE is the key"
			},
			code: models.ChatErrContentDenied,
		},
		{
			name: "inspector_denial_stops_the_request",
			arrange: func(f *chatFixture) {
				f.exec.inspector = &stubInspector{request: claudeapi.ContentInspectionDecision{
					Forward: false, Status: http.StatusForbidden, Reason: "prompt injection",
				}}
			},
			code: models.ChatErrInspectionDenied,
		},
		{
			name: "inspector_no_decision_is_deny_closed",
			arrange: func(f *chatFixture) {
				// The connector's zero value means "no decision". For a security gate an
				// absent decision is not a clean pass.
				f.exec.inspector = &stubInspector{}
			},
			code: models.ChatErrInspectionDenied,
		},
		{
			name: "budget_block",
			arrange: func(f *chatFixture) {
				f.budget.status, f.budget.denied = http.StatusPaymentRequired, true
			},
			code: models.ChatErrBudgetDenied, budgetCalls: 1,
		},
		{
			name: "budget_throttle",
			arrange: func(f *chatFixture) {
				f.budget.status, f.budget.denied = http.StatusTooManyRequests, true
			},
			code: models.ChatErrBudgetThrottled, budgetCalls: 1,
		},
		{
			name: "budget_denial_without_a_spend_status_is_an_internal_deny",
			arrange: func(f *chatFixture) {
				f.budget.status, f.budget.denied = 0, true
			},
			code: models.ChatErrInternal, budgetCalls: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newChatFixture(t, chatFixtureOptions{})
			// A resolver with no handlers: ANY credential resolution fails closed, so a
			// gate that leaked past this point could not silently succeed.
			f.exec.secrets = chatFixtureResolver(map[string]string{})
			tt.arrange(f)
			_, err := f.run()
			if code := chatErrorCode(t, err); code != tt.code {
				t.Fatalf("code = %s, want %s", code, tt.code)
			}
			if f.transport.count() != 0 {
				t.Fatalf("a denied request POSTed to the model endpoint %d times", f.transport.count())
			}
			if f.handler.hits != 0 {
				t.Fatalf("the synthetic gateway was reached %d times", f.handler.hits)
			}
			if f.budget.calls != tt.budgetCalls {
				t.Fatalf("budget precheck ran %d times, want %d", f.budget.calls, tt.budgetCalls)
			}
			if len(f.eventsWithAction(chatIntentAction)) != 0 {
				t.Fatal("a pre-intent denial anchored an intent")
			}
		})
	}
}

// TestChatDLPDenialDoesNotReachTheInspector is the one ORDERING claim inside step 5: content
// the tenant's own policy already refused is not handed to the optional add-on.
func TestChatDLPDenialDoesNotReachTheInspector(t *testing.T) {
	inspector := &stubInspector{request: claudeapi.ContentInspectionDecision{Forward: true}}
	f := newChatFixture(t, chatFixtureOptions{inspector: inspector})
	f.policy.policy = inferenceproxy.PolicyWithDLPRules(f.policy.policy, map[string]string{"*": "deny"})
	f.request.Input = "AKIAIOSFODNN7EXAMPLE is the key"
	if _, err := f.run(); chatErrorCode(t, err) != models.ChatErrContentDenied {
		t.Fatalf("want a DLP denial, got %v", err)
	}
	if len(inspector.seen) != 0 {
		t.Fatalf("the inspector saw %d inputs after a core DLP denial", len(inspector.seen))
	}
}

// TestChatPolicyIsReadExactlyOnce pins step 3's "once": a second read could observe a
// different posture than the one bound into the effect digest.
func TestChatPolicyIsReadExactlyOnce(t *testing.T) {
	f := newChatFixture(t, chatFixtureOptions{})
	if _, err := f.run(); err != nil {
		t.Fatalf("ExecuteChat: %v", err)
	}
	if f.policy.calls != 1 {
		t.Fatalf("proxy policy read %d times, want exactly 1", f.policy.calls)
	}
	if f.context.calls != 1 {
		t.Fatalf("context policy read %d times, want exactly 1", f.context.calls)
	}
}

// --- group 3: revalidation, credentials and cancellation -------------------------------------

// TestChatPostIntentFailuresRecordNotSentAndNeverDispatch covers the exits that happen AFTER
// the intent is anchored and BEFORE C1's Do. Each must leave a recorded outcome whose
// dispatch state is not_attempted — an evidence trail that says nothing was sent, rather
// than no trail at all.
func TestChatPostIntentFailuresRecordNotSentAndNeverDispatch(t *testing.T) {
	tests := []struct {
		name    string
		arrange func(*chatFixture)
		code    string
	}{
		{
			name: "the_registry_entry_changed_between_intent_and_dispatch",
			arrange: func(f *chatFixture) {
				// Land the change INSIDE the request, after entry validation accepted the
				// snapshot and before the intent is anchored — the window a configuration
				// reload actually occupies. Step 8 re-reads and compares the WHOLE snapshot,
				// so an entry that changed under the same ref/revision key is caught there
				// rather than dispatched against.
				f.budget.onCall = func() {
					for key, entry := range f.exec.registry.profiles {
						entry.ModelRef = "swapped-model"
						f.exec.registry.profiles[key] = entry
					}
				}
			},
			code: models.ChatErrProfileUnavailable,
		},
		{
			name: "the_credential_reference_no_longer_resolves",
			arrange: func(f *chatFixture) {
				delete(f.env, "CHAT_FIXTURE_BEARER") // revoked between authoring and use
			},
			code: models.ChatErrCredentialUnavailable,
		},
		{
			name: "the_resolved_bearer_is_not_a_valid_credential",
			arrange: func(f *chatFixture) {
				f.env["CHAT_FIXTURE_BEARER"] = "not a bearer token" // spaces are refused by C1
			},
			code: models.ChatErrCredentialUnavailable,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newChatFixture(t, chatFixtureOptions{})
			tt.arrange(f)
			res, err := f.run()
			if code := chatErrorCode(t, err); code != tt.code {
				t.Fatalf("code = %s, want %s", code, tt.code)
			}
			if f.transport.count() != 0 {
				t.Fatalf("POSTed %d times after a pre-dispatch failure", f.transport.count())
			}
			if res.DispatchState != models.ChatDispatchNotAttempted || res.CompletionState != models.ChatCompletionNotSent {
				t.Fatalf("states = %s/%s, want not_attempted/not_sent", res.DispatchState, res.CompletionState)
			}
			outcomes := f.recordsWithAction(chatOutcomeAction)
			if len(outcomes) != 1 {
				t.Fatalf("recorded %d outcomes, want exactly 1", len(outcomes))
			}
			if outcomes[0].meta["dispatch_state"] != models.ChatDispatchNotAttempted {
				t.Fatalf("recorded dispatch_state = %v", outcomes[0].meta["dispatch_state"])
			}
			if res.OutcomeDisposition != models.ChatOutcomeAnchored {
				t.Fatalf("outcome disposition = %s", res.OutcomeDisposition)
			}
		})
	}
}

// TestChatCancellationBeforeDispatchStopsWithoutAnyEffect: a caller that goes away before
// the operation begins costs nothing — no callback, no policy read, no anchor, no POST.
func TestChatCancellationBeforeDispatchStopsWithoutAnyEffect(t *testing.T) {
	f := newChatFixture(t, chatFixtureOptions{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := f.runCtx(ctx)
	if code := chatErrorCode(t, err); code != models.ChatErrRequestCancelled {
		t.Fatalf("code = %s", code)
	}
	if f.policy.calls != 0 || f.budget.calls != 0 || f.transport.count() != 0 {
		t.Fatalf("a cancelled entry did work: policy=%d budget=%d posts=%d", f.policy.calls, f.budget.calls, f.transport.count())
	}
	if res.RequestRef != "" || res.AttemptRef != "" {
		t.Fatal("a cancelled entry minted refs")
	}
	if len(f.eventsWithAction(chatIntentAction)) != 0 {
		t.Fatal("a cancelled entry anchored an intent")
	}
}

// TestChatMissingBudgetCallbackIsAnInternalDeny: the port cannot be driven without the
// handler's precheck closure, and its absence is never read as "nothing to check".
func TestChatMissingBudgetCallbackIsAnInternalDeny(t *testing.T) {
	f := newChatFixture(t, chatFixtureOptions{})
	_, err := f.exec.ExecuteChat(context.Background(), f.request, nil)
	if code := chatErrorCode(t, err); code != models.ChatErrInternal {
		t.Fatalf("code = %s", code)
	}
	if f.transport.count() != 0 {
		t.Fatalf("POSTed %d times without a budget callback", f.transport.count())
	}
}

// TestChatCredentialRotationResolvesTheNewValueOnTheNextAttempt: nothing caches a token,
// so a rotation between two attempts is visible on the wire.
//
// It deliberately claims NOTHING about issuer-side revocation being instantaneous: the
// assertion is about what THIS process resolves and sends, which is all it can observe.
func TestChatCredentialRotationResolvesTheNewValueOnTheNextAttempt(t *testing.T) {
	f := newChatFixture(t, chatFixtureOptions{})
	if _, err := f.run(); err != nil {
		t.Fatalf("first attempt: %v", err)
	}
	f.env["CHAT_FIXTURE_BEARER"] = "fixture-token-bbbbbbbbbbbb"
	if _, err := f.run(); err != nil {
		t.Fatalf("second attempt: %v", err)
	}
	calls := f.transport.calls()
	if len(calls) != 2 {
		t.Fatalf("dispatched %d times", len(calls))
	}
	if calls[0].authorization == calls[1].authorization {
		t.Fatal("the second attempt reused the first attempt's token: something cached it")
	}
	if !strings.HasSuffix(calls[1].authorization, "bbbbbbbbbbbb") {
		t.Fatalf("the second attempt did not carry the rotated value: %q", calls[1].authorization)
	}
}

// --- group 4: evidence ------------------------------------------------------------------------

// TestChatReceiptBindsThisExactEffectAndRefusesAnother is the confused-deputy causal at the
// evidence layer: the receipt this attempt anchored is accepted for ITS binding and refused
// for any other, including one that differs only in the effect digest.
func TestChatReceiptBindsThisExactEffectAndRefusesAnother(t *testing.T) {
	f := newChatFixture(t, chatFixtureOptions{})
	res, err := f.run()
	if err != nil {
		t.Fatalf("ExecuteChat: %v", err)
	}
	intents := f.recordsWithAction(chatIntentAction)
	if len(intents) != 1 {
		t.Fatalf("anchored %d intents, want 1", len(intents))
	}
	effect, _ := intents[0].meta["effect_digest"].(string)
	if effect == "" {
		t.Fatal("the intent anchor did not record its effect digest")
	}
	// The PAYLOAD HASH is the same effect digest: the chain commits to it independently of
	// the metadata, so an edited metadata map cannot move the binding the chain sealed.
	if hex.EncodeToString(intents[0].event.PayloadHash) != effect {
		t.Fatalf("the intent's payload hash %x does not equal its recorded effect digest %s",
			intents[0].event.PayloadHash, effect)
	}
	binding := chatBindingFor(res.AttemptRef, effect)
	receipt := chatReceiptFor(binding, hex.EncodeToString(intents[0].event.Hash))
	if !receipt.AnchoredFor(binding) {
		t.Fatal("the receipt is not anchored for its own binding")
	}
	for _, other := range []struct {
		name    string
		binding sdk.EvidenceBinding
	}{
		{"a different attempt", chatBindingFor("some-other-attempt", effect)},
		{"a different effect", chatBindingFor(res.AttemptRef, strings.Repeat("0", 64))},
		{"an empty binding", chatBindingFor("", "")},
	} {
		if receipt.AnchoredFor(other.binding) {
			t.Fatalf("the receipt authorized %s", other.name)
		}
	}
}

// TestChatIntentAndOutcomeMetadataCarryNoContent is the minimal-data causal: the ledger
// records refs, closed decisions and digests, and never the prompt, the output, the refusal
// text, the credential or the endpoint path.
func TestChatIntentAndOutcomeMetadataCarryNoContent(t *testing.T) {
	f := newChatFixture(t, chatFixtureOptions{})
	f.request.Input = "a distinctive prompt string"
	f.handler.body = chatCompletionBody(chatFixtureModel, "a distinctive output string")
	res, err := f.run()
	if err != nil {
		t.Fatalf("ExecuteChat: %v", err)
	}
	forbidden := []string{
		"a distinctive prompt string", "a distinctive output string",
		f.env["CHAT_FIXTURE_BEARER"], "/v1/chat/completions", "CHAT_FIXTURE_BEARER",
	}
	for _, action := range []string{chatIntentAction, chatOutcomeAction} {
		records := f.recordsWithAction(action)
		if len(records) != 1 {
			t.Fatalf("%s recorded %d times", action, len(records))
		}
		if records[0].raw == "" || records[0].raw == "{}" {
			t.Fatalf("%s committed no metadata at all; this assertion would be vacuous", action)
		}
		for _, needle := range forbidden {
			if needle != "" && strings.Contains(records[0].raw, needle) {
				t.Fatalf("%s metadata leaked %q: %s", action, needle, records[0].raw)
			}
		}
		if records[0].event.TargetKind != chatExecutionKind {
			t.Fatalf("%s target kind = %s", action, records[0].event.TargetKind)
		}
		if records[0].event.TargetID != model.ID(res.AttemptRef) {
			t.Fatalf("%s target id = %s, want the server attempt ref", action, records[0].event.TargetID)
		}
	}
	// The PUBLIC authority metadata that IS carried: the audience, never the endpoint path.
	intent := f.recordsWithAction(chatIntentAction)[0]
	if intent.meta["credential_audience"] != f.profile.CredentialAudience {
		t.Fatalf("the intent lost the public credential audience: %s", intent.raw)
	}
}

// TestChatDegradeSpoolCommitsLossAccountingAndRefusesWhenMandatoryIsChosen is the real-store
// transaction causal F9 exists for: under a DEGRADE spool the drop must be durably
// counted (never rolled back), and a tenant that EXPLICITLY chose evidence-or-refuse must be
// refused before any dispatch.
func TestChatDegradeSpoolCommitsLossAccountingAndRefusesWhenMandatoryIsChosen(t *testing.T) {
	pol := chatDefaultPolicy() // RecordMandatory + RecordMandatoryChosen
	f := newChatFixture(t, chatFixtureOptions{
		spoolBytes: 1, spoolMode: store.AuditSpoolDegrade, policy: &pol,
	})
	before := chatPendingDrops(t, f.store)
	res, err := f.run()
	if code := chatErrorCode(t, err); code != models.ChatErrRecordingUnavailable {
		t.Fatalf("code = %s, want %s", code, models.ChatErrRecordingUnavailable)
	}
	if res.IntentDisposition != models.ChatIntentRefused {
		t.Fatalf("intent disposition = %s", res.IntentDisposition)
	}
	if f.transport.count() != 0 {
		t.Fatalf("a refused intent still POSTed %d times", f.transport.count())
	}
	if got := chatPendingDrops(t, f.store) - before; got != 1 {
		t.Fatalf("durable degrade drops advanced by %d, want exactly 1; the loss accounting was rolled back", got)
	}
}

// TestChatDegradeSpoolYieldsToTheOperatorWhenNobodyChoseARecordingPosture is the other half
// of the same rule: a DEFAULT must not override an explicit operator choice. A tenant that
// never configured a recording posture proceeds with a NAMED gap under the operator's
// declared degrade — and the gap is reported, not hidden.
func TestChatDegradeSpoolYieldsToTheOperatorWhenNobodyChoseARecordingPosture(t *testing.T) {
	pol := chatDefaultPolicy()
	pol.RecordMandatory, pol.RecordMandatoryChosen = true, false // the build default, nobody chose
	f := newChatFixture(t, chatFixtureOptions{
		spoolBytes: 1, spoolMode: store.AuditSpoolDegrade, policy: &pol,
	})
	before := chatPendingDrops(t, f.store)
	res, err := f.run()
	if err != nil {
		t.Fatalf("ExecuteChat: %v", err)
	}
	if res.IntentDisposition != models.ChatIntentOperatorDegradeGap {
		t.Fatalf("intent disposition = %s, want %s", res.IntentDisposition, models.ChatIntentOperatorDegradeGap)
	}
	if res.OutcomeDisposition != models.ChatOutcomeGap {
		t.Fatalf("outcome disposition = %s; a dropped outcome is a gap, not an anchor", res.OutcomeDisposition)
	}
	if f.transport.count() != 1 {
		t.Fatalf("dispatched %d times", f.transport.count())
	}
	// Two drops: the intent leg and the outcome leg, both durably accounted.
	if got := chatPendingDrops(t, f.store) - before; got != 2 {
		t.Fatalf("durable degrade drops advanced by %d, want exactly 2", got)
	}
}

// TestChatBlockSpoolRefusesAndWritesNothing: a BLOCK-mode spool full is nobody's declared
// choice, so it refuses regardless of who configured what — and it rolls back, so the chain
// does not grow.
func TestChatBlockSpoolRefusesAndWritesNothing(t *testing.T) {
	pol := chatDefaultPolicy()
	pol.RecordMandatoryChosen = false
	f := newChatFixture(t, chatFixtureOptions{
		spoolBytes: 1, spoolMode: store.AuditSpoolBlock, policy: &pol,
	})
	before := len(f.auditEvents())
	_, err := f.run()
	if code := chatErrorCode(t, err); code != models.ChatErrRecordingUnavailable {
		t.Fatalf("code = %s", code)
	}
	if f.transport.count() != 0 {
		t.Fatalf("a block-mode refusal still POSTed %d times", f.transport.count())
	}
	if got := len(f.auditEvents()) - before; got != 0 {
		t.Fatalf("a rolled-back block-mode append grew the chain by %d events", got)
	}
}

// TestChatBestEffortTenantProceedsWithANamedGap: RecordMandatory=false is the tenant's
// elected posture. The attempt proceeds and the disposition SAYS the evidence is a gap
// rather than reporting an anchor it does not have.
func TestChatBestEffortTenantProceedsWithANamedGap(t *testing.T) {
	pol := chatDefaultPolicy()
	pol.RecordMandatory = false
	f := newChatFixture(t, chatFixtureOptions{
		spoolBytes: 1, spoolMode: store.AuditSpoolDegrade, policy: &pol,
	})
	res, err := f.run()
	if err != nil {
		t.Fatalf("ExecuteChat: %v", err)
	}
	if res.IntentDisposition != models.ChatIntentExplicitBestEffort {
		t.Fatalf("intent disposition = %s", res.IntentDisposition)
	}
	if f.transport.count() != 1 {
		t.Fatalf("dispatched %d times", f.transport.count())
	}
}

// cancelAfterDecodeInspector is the executor's own inspector, used as the cancellation
// boundary. The executor consults it in the response direction only from outputWithheld,
// which runs after C1 returned a fully read and decoded response and before the deferred
// outcome record. Canceling the caller there is late by construction, not by a clock.
type cancelAfterDecodeInspector struct {
	cancel context.CancelFunc
	// fired counts response-direction calls; boundaryErr is the error of the context the
	// executor passed, read right after the cancel; other keeps any direction this test does
	// not expect.
	fired       int
	boundaryErr error
	other       []string
}

func (c *cancelAfterDecodeInspector) Inspect(ctx context.Context, in claudeapi.ContentInspectionInput) claudeapi.ContentInspectionDecision {
	switch in.Direction {
	case claudeapi.InspectDirectionRequest:
		return claudeapi.ContentInspectionDecision{Forward: true}
	case claudeapi.InspectDirectionResponse:
		c.fired++
		if c.fired == 1 {
			c.cancel()
			c.boundaryErr = ctx.Err()
		}
		// Forward without Block: the verdict must not be what withholds the output.
		return claudeapi.ContentInspectionDecision{Forward: true}
	default:
		c.other = append(c.other, in.Direction)
		return claudeapi.ContentInspectionDecision{}
	}
}

// TestChatOutcomeIsRecordedEvenWhenTheCallerCancels: the caller hanging up must not cancel
// the record of an effect that already happened. The outcome context is derived without
// cancellation and bounded independently.
func TestChatOutcomeIsRecordedEvenWhenTheCallerCancels(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// The upstream answers and C1 keeps a fully read and decoded response; only then does the
	// response inspector cancel the caller's context, and the outcome leg must still commit.
	// No clock orders the cancel: a run that never reaches that boundary fails below instead
	// of passing without a cancellation.
	boundary := &cancelAfterDecodeInspector{cancel: cancel}
	f := newChatFixture(t, chatFixtureOptions{inspector: boundary})
	res, err := f.runCtx(ctx)
	// ExecuteChat has returned, so its one dispatch is complete; the handler's counter is
	// still read under the handler's own lock.
	f.handler.mu.Lock()
	hits := f.handler.hits
	f.handler.mu.Unlock()
	t.Logf("CHAT_CANCEL_SEAM|fired=%d|boundary_ctx_err=%v|caller_ctx_err=%v|gateway_hits=%d|err=%v",
		boundary.fired, boundary.boundaryErr, ctx.Err(), hits, err)
	if boundary.fired != 1 || len(boundary.other) != 0 {
		t.Fatalf("the response inspector fired %d times (unexpected directions %v), want exactly once", boundary.fired, boundary.other)
	}
	if !errors.Is(boundary.boundaryErr, context.Canceled) || !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("context at the boundary = %v, caller = %v; want both cancelled", boundary.boundaryErr, ctx.Err())
	}
	if hits != 1 {
		t.Fatalf("the gateway was reached %d times, want exactly 1", hits)
	}
	if err != nil {
		t.Fatalf("ExecuteChat: %v", err)
	}
	if res.DispatchState != models.ChatDispatchAttempted || res.CompletionState != models.ChatCompletionStop {
		t.Fatalf("states = %s/%s, want attempted/stop", res.DispatchState, res.CompletionState)
	}
	if res.Output == nil || *res.Output != "hello from the fixture" {
		t.Fatalf("output = %v; the decoded response was not kept", res.Output)
	}
	if got := len(f.eventsWithAction(chatOutcomeAction)); got != 1 {
		t.Fatalf("recorded %d outcomes, want exactly 1", got)
	}
	record := chatOutcomeRecord(t, f)
	if record.meta["dispatch_state"] != models.ChatDispatchAttempted || record.meta["completion_state"] != models.ChatCompletionStop {
		t.Fatalf("recorded states = %v/%v, want attempted/stop", record.meta["dispatch_state"], record.meta["completion_state"])
	}
	if res.OutcomeDisposition != models.ChatOutcomeAnchored {
		t.Fatalf("outcome disposition = %s", res.OutcomeDisposition)
	}
}

// --- group 5: transport outcomes ------------------------------------------------------------

// TestChatUpstreamFailuresDispatchOnceAndNeverFallBack: every upstream failure mode maps to
// a fixed closed code, records an ATTEMPTED dispatch with an UNKNOWN effect, and POSTs
// exactly once — no retry, no second target, no legacy chain.
func TestChatUpstreamFailuresDispatchOnceAndNeverFallBack(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		body       string
		headers    map[string]string
		delay      time.Duration
		code       string
		dispatch   string
		completion string
		retryAfter *int64
	}{
		{name: "rate_limited", status: http.StatusTooManyRequests, headers: map[string]string{"Retry-After": "7"},
			code: models.ChatErrUpstreamRateLimited, dispatch: models.ChatDispatchAttempted,
			completion: models.ChatCompletionEffectUnknown, retryAfter: chatInt64(7)},
		{name: "upstream_credential_refused", status: http.StatusUnauthorized,
			code: models.ChatErrUpstreamUnavailable, dispatch: models.ChatDispatchAttempted,
			completion: models.ChatCompletionEffectUnknown},
		{name: "upstream_server_error", status: http.StatusBadGateway,
			code: models.ChatErrUpstreamUnavailable, dispatch: models.ChatDispatchAttempted,
			completion: models.ChatCompletionEffectUnknown},
		{name: "unsupported_tool_payload", body: `{"object":"chat.completion","model":"` + chatFixtureModel +
			`","choices":[{"index":0,"finish_reason":"tool_calls","message":{"role":"assistant","content":"x"}}]}`,
			code: models.ChatErrProtocolError, dispatch: models.ChatDispatchAttempted,
			completion: models.ChatCompletionProtocolError},
		{name: "truncated_body", body: `{"object":"chat.completion","model":"`,
			code: models.ChatErrProtocolError, dispatch: models.ChatDispatchAttempted,
			completion: models.ChatCompletionProtocolError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newChatFixture(t, chatFixtureOptions{})
			f.handler.status, f.handler.headers, f.handler.delay = tt.status, tt.headers, tt.delay
			if tt.body != "" {
				f.handler.body = tt.body
			}
			res, err := f.run()
			var chatErr *models.ChatExecutionError
			if !errors.As(err, &chatErr) || chatErr.Code != tt.code {
				t.Fatalf("code = %v, want %s", err, tt.code)
			}
			if f.transport.count() != 1 {
				t.Fatalf("POSTed %d times, want exactly 1 (no retry, no fallback)", f.transport.count())
			}
			if res.DispatchState != tt.dispatch || res.CompletionState != tt.completion {
				t.Fatalf("states = %s/%s, want %s/%s", res.DispatchState, res.CompletionState, tt.dispatch, tt.completion)
			}
			if res.UsageStatus != models.ChatUsageUnknown {
				t.Fatalf("usage status = %s; an uncertain effect has UNKNOWN usage, never zero", res.UsageStatus)
			}
			if res.Usage != nil {
				t.Fatal("an uncertain effect fabricated a usage object")
			}
			switch {
			case tt.retryAfter == nil && chatErr.RetryAfterSeconds != nil:
				t.Fatalf("invented a Retry-After hint: %d", *chatErr.RetryAfterSeconds)
			case tt.retryAfter != nil && (chatErr.RetryAfterSeconds == nil || *chatErr.RetryAfterSeconds != *tt.retryAfter):
				t.Fatalf("retry-after = %v, want %d", chatErr.RetryAfterSeconds, *tt.retryAfter)
			}
			// The effect happened as far as anyone here can tell, so it is RECORDED.
			if got := len(f.eventsWithAction(chatOutcomeAction)); got != 1 {
				t.Fatalf("recorded %d outcomes", got)
			}
		})
	}
}

// TestChatResponseTimeoutIsBoundedByTheProfile: the profile's timeout bounds the whole
// cooperative operation, and a gateway that never answers produces a timeout, one dispatch,
// and a recorded uncertain effect.
func TestChatResponseTimeoutIsBoundedByTheProfile(t *testing.T) {
	f := newChatFixture(t, chatFixtureOptions{})
	f.exec.registry = chatRegistryWithTimeout(t, f, 150*time.Millisecond)
	f.request.Profile.Timeout = 150 * time.Millisecond
	f.handler.delay = 3 * time.Second

	start := time.Now()
	res, err := f.run()
	elapsed := time.Since(start)
	if code := chatErrorCode(t, err); code != models.ChatErrUpstreamTimeout {
		t.Fatalf("code = %s", code)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("the operation ran %s; the profile bound was 150ms", elapsed)
	}
	if f.transport.count() != 1 {
		t.Fatalf("POSTed %d times", f.transport.count())
	}
	if res.DispatchState != models.ChatDispatchAttempted || res.CompletionState != models.ChatCompletionEffectUnknown {
		t.Fatalf("states = %s/%s", res.DispatchState, res.CompletionState)
	}
	// The independent five-second outcome bound is what makes this recordable at all: the
	// operation context is already expired by now.
	if got := len(f.eventsWithAction(chatOutcomeAction)); got != 1 {
		t.Fatalf("recorded %d outcomes after the operation deadline expired", got)
	}
}

// TestChatModelMismatchWithholdsOutputAndKeepsUsageAsAnObservation: a complete answer from a
// model the profile did not pin is a protocol failure. The reported usage is RETAINED — it
// says something happened — but it is not trusted as attribution to the pinned model, and no
// text is released.
func TestChatModelMismatchWithholdsOutputAndKeepsUsageAsAnObservation(t *testing.T) {
	f := newChatFixture(t, chatFixtureOptions{})
	f.handler.body = chatCompletionBody("a-different-model", "text from the wrong model")
	res, err := f.run()
	if code := chatErrorCode(t, err); code != models.ChatErrProtocolError {
		t.Fatalf("code = %s", code)
	}
	if res.Output != nil {
		t.Fatal("a mismatched model released its text")
	}
	if res.CompletionState != models.ChatCompletionProtocolError {
		t.Fatalf("completion = %s", res.CompletionState)
	}
	if res.UsageStatus != models.ChatUsageReported || res.Usage == nil || res.Usage.PromptTokens == nil {
		t.Fatalf("a valid usage object was discarded: %s %+v", res.UsageStatus, res.Usage)
	}
}

// --- group 6: output gates -------------------------------------------------------------------

// TestChatOutputGatesHonourTheDeclaredModeAndPreserveUsage walks the response-side matrix.
// In every withholding case the KNOWN usage survives: withholding text is not a reason to
// forget that tokens were consumed.
func TestChatOutputGatesHonourTheDeclaredModeAndPreserveUsage(t *testing.T) {
	secretOutput := chatCompletionBody(chatFixtureModel, "AKIAIOSFODNN7EXAMPLE")
	tests := []struct {
		name      string
		mode      string
		inspector contentInspector
		withheld  bool
	}{
		{name: "buffer_withholds", mode: inferenceproxy.ResponseDLPBuffer, withheld: true},
		{name: "flag_records_and_delivers", mode: inferenceproxy.ResponseDLPFlag, withheld: false},
		{name: "off_omits_core_response_dlp", mode: inferenceproxy.ResponseDLPOff, withheld: false},
		{
			name: "an_installed_inspector_blocks_regardless_of_the_mode", mode: inferenceproxy.ResponseDLPOff,
			inspector: &stubInspector{
				request:  claudeapi.ContentInspectionDecision{Forward: true},
				response: claudeapi.ContentInspectionDecision{Forward: true, Block: true, Status: http.StatusForbidden},
			},
			withheld: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pol := chatDefaultPolicy()
			pol.ResponseDLPMode = tt.mode
			pol = inferenceproxy.PolicyWithDLPRules(pol, map[string]string{"secret.credential": "deny", "*": "allow", "unscanned": "allow"})
			f := newChatFixture(t, chatFixtureOptions{policy: &pol, inspector: tt.inspector})
			f.handler.body = secretOutput
			res, err := f.run()
			if tt.withheld {
				if code := chatErrorCode(t, err); code != models.ChatErrOutputWithheld {
					t.Fatalf("code = %s, want %s", code, models.ChatErrOutputWithheld)
				}
				if res.Output != nil {
					t.Fatal("a withheld response released its text")
				}
				if res.CompletionState != models.ChatCompletionOutputWithheld {
					t.Fatalf("completion = %s", res.CompletionState)
				}
			} else {
				if err != nil {
					t.Fatalf("ExecuteChat: %v", err)
				}
				if res.Output == nil {
					t.Fatal("a delivering mode withheld the text")
				}
			}
			if res.UsageStatus != models.ChatUsageReported || res.Usage == nil ||
				res.Usage.CompletionTokens == nil || *res.Usage.CompletionTokens != 7 {
				t.Fatalf("known usage did not survive: %s %+v", res.UsageStatus, res.Usage)
			}
		})
	}
}

// TestChatRefusalIsReportedWithoutItsText: the caller learns THAT the model refused; the raw
// refusal string is never exposed.
func TestChatRefusalIsReportedWithoutItsText(t *testing.T) {
	f := newChatFixture(t, chatFixtureOptions{})
	f.handler.body = `{"object":"chat.completion","model":"` + chatFixtureModel +
		`","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":null,` +
		`"refusal":"I will not do that, and this sentence is the raw refusal"}}]}`
	res, err := f.run()
	if err != nil {
		t.Fatalf("ExecuteChat: %v", err)
	}
	if !res.Refusal || res.CompletionState != models.ChatCompletionRefusal {
		t.Fatalf("refusal not reported: %v / %s", res.Refusal, res.CompletionState)
	}
	if res.Output != nil {
		t.Fatalf("a refusal released text: %q", *res.Output)
	}
	if res.UsageStatus != models.ChatUsageNotReported || res.Usage != nil {
		t.Fatalf("a response with no usage object reported %s %+v; absent is not zero", res.UsageStatus, res.Usage)
	}
	for _, record := range f.auditRecords() {
		if strings.Contains(record.raw, "raw refusal") {
			t.Fatalf("the ledger recorded the refusal text: %s", record.raw)
		}
	}
}

// TestChatExplicitZeroUsageIsZeroAndMissingUsageIsNull is the arithmetic honesty causal: the
// two states a bare int64 cannot tell apart stay apart.
func TestChatExplicitZeroUsageIsZeroAndMissingUsageIsNull(t *testing.T) {
	t.Run("explicit_zero", func(t *testing.T) {
		f := newChatFixture(t, chatFixtureOptions{})
		f.handler.body = `{"object":"chat.completion","model":"` + chatFixtureModel +
			`","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"ok"}}],` +
			`"usage":{"prompt_tokens":0,"completion_tokens":0}}`
		res, err := f.run()
		if err != nil {
			t.Fatalf("ExecuteChat: %v", err)
		}
		if res.UsageStatus != models.ChatUsageReported || res.Usage == nil ||
			res.Usage.PromptTokens == nil || *res.Usage.PromptTokens != 0 {
			t.Fatalf("an explicit zero was lost: %s %+v", res.UsageStatus, res.Usage)
		}
		if res.Usage.TotalTokens != nil {
			t.Fatal("an absent total_tokens was invented")
		}
	})
	t.Run("absent_usage", func(t *testing.T) {
		f := newChatFixture(t, chatFixtureOptions{})
		f.handler.body = `{"object":"chat.completion","model":"` + chatFixtureModel +
			`","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"ok"}}]}`
		res, err := f.run()
		if err != nil {
			t.Fatalf("ExecuteChat: %v", err)
		}
		if res.UsageStatus != models.ChatUsageNotReported || res.Usage != nil {
			t.Fatalf("absent usage became %s %+v", res.UsageStatus, res.Usage)
		}
	})
}

// TestChatInspectsBothDirectionsThroughItsOwnInspector shows that the executor consults the
// inspector IT HOLDS, in both directions, with the exact input C0 prepared.
//
// ⛔ IT IS NOT A BOOT CAUSAL, AND ITS PREVIOUS NAME SAID IT WAS. Until this correction it
// was called TestChatConfiguredInspectorRunsInAChatOnlyBoot, which claimed an activation
// path it never ran: the inspector here is injected straight into the fixture, no
// configured constructor is called and no boot happens. That overstatement is R3 of the
// independent review. What the injection legitimately proves is the field's USE — that the
// executor's own inspector reaches both directions — and the construction half is proven
// separately, against the production constructor, in
// TestChatBuildsItsInspectorThroughTheConfiguredConstructor
// (modelsguardedtextcomposition_test.go).
func TestChatInspectsBothDirectionsThroughItsOwnInspector(t *testing.T) {
	inspector := &stubInspector{
		request:  claudeapi.ContentInspectionDecision{Forward: true, Meter: claudeapi.ContentInspectionMeter{Inspections: 1, Channels: 1}},
		response: claudeapi.ContentInspectionDecision{Forward: true, Meter: claudeapi.ContentInspectionMeter{Inspections: 1, Channels: 1}},
	}
	f := newChatFixture(t, chatFixtureOptions{inspector: inspector})
	if _, err := f.run(); err != nil {
		t.Fatalf("ExecuteChat: %v", err)
	}
	if len(inspector.seen) != 2 {
		t.Fatalf("the inspector saw %d directions, want request and response", len(inspector.seen))
	}
	if inspector.seen[0].Direction != claudeapi.InspectDirectionRequest ||
		inspector.seen[1].Direction != claudeapi.InspectDirectionResponse {
		t.Fatalf("directions = %s, %s", inspector.seen[0].Direction, inspector.seen[1].Direction)
	}
	if inspector.seen[0].Model != f.profile.ModelRef {
		t.Fatalf("the inspector was told model %q", inspector.seen[0].Model)
	}
	if inspector.seen[0].Channels[0].Text != f.request.Input {
		t.Fatal("the inspector saw content other than the exact input C0 prepared")
	}
}

// --- R2: the persisted outcome keeps the whole observation ---------------------------------------

// chatOutcomeRecord returns the single canonical outcome record, failing if there is not
// exactly one. Reading through the CANONICAL walker is the point: the in-memory Meta map is
// nil by contract, so an assertion over it would pass against anything.
func chatOutcomeRecord(t *testing.T, f *chatFixture) chatLedgerRecord {
	t.Helper()
	records := f.recordsWithAction(chatOutcomeAction)
	if len(records) != 1 {
		t.Fatalf("the ledger holds %d outcome records, want exactly 1", len(records))
	}
	return records[0]
}

// chatMetaCounter reads one nullable counter out of canonical metadata: present with a
// value, or absent. It distinguishes the two, because that is the distinction under test.
func chatMetaCounter(t *testing.T, meta map[string]any, key string) (int64, bool) {
	t.Helper()
	raw, present := meta[key]
	if !present {
		return 0, false
	}
	value, ok := raw.(float64)
	if !ok {
		t.Fatalf("%s = %T(%v), want a JSON number", key, raw, raw)
	}
	return int64(value), true
}

// chatUsageProjection is the USAGE-DESCRIBING part of a canonical outcome record, rendered
// deterministically so two records can be compared for the only property under test.
//
// Comparing the whole canonical string would be worthless: request_ref, attempt_ref and the
// digests are minted per attempt, so two records ALWAYS differ and an assertion over them
// passes against any metadata whatsoever. (That is not hypothetical — it is the first
// version of this control, which the mutation run below caught passing against the
// pre-correction source.) This projection contains only what the usage observation says.
func chatUsageProjection(t *testing.T, meta map[string]any) string {
	t.Helper()
	keys := []string{
		"usage_status", "input_tokens", "output_tokens", "total_tokens",
		"prompt_tokens_details_present", "prompt_cached_tokens", "prompt_audio_tokens",
		"completion_tokens_details_present", "completion_reasoning_tokens", "completion_audio_tokens",
		"completion_accepted_prediction_tokens", "completion_rejected_prediction_tokens",
	}
	projection := map[string]any{}
	for _, key := range keys {
		if v, ok := meta[key]; ok {
			projection[key] = v
		}
	}
	// json.Marshal sorts map keys, so this is a property of the projection.
	body, err := json.Marshal(projection)
	if err != nil {
		t.Fatalf("render usage projection: %v", err)
	}
	return string(body)
}

func chatMetaBool(t *testing.T, meta map[string]any, key string) bool {
	t.Helper()
	raw, present := meta[key]
	if !present {
		t.Fatalf("the canonical outcome metadata has no %q", key)
	}
	value, ok := raw.(bool)
	if !ok {
		t.Fatalf("%s = %T(%v), want a boolean", key, raw, raw)
	}
	return value
}

// chatUsageBody stages one upstream answer with an arbitrary usage object.
func chatUsageBody(modelRef, usage string) string {
	return `{"object":"chat.completion","model":"` + modelRef + `","choices":[{"index":0,"finish_reason":"stop",` +
		`"message":{"role":"assistant","content":"ok"}}],"usage":` + usage + `}`
}

// TestChatOutcomePersistsTheCompleteNullableUsage is R2's causal. The record must preserve
// all four distinctions the C0 type can express — object absent, object present and EMPTY,
// counter absent, counter explicitly zero — and it must preserve them AS DATA.
//
// The middle two are what the previous record lost. It kept three counters and a digest, so
// "no details object" and "an empty details object" produced byte-identical metadata: the
// digest moved, but a digest confirms a value someone already has, it does not answer "how
// many cached prompt tokens did this attempt observe?". The last subtest compares the two
// canonical strings directly, which is the only assertion that can fail for that reason.
func TestChatOutcomePersistsTheCompleteNullableUsage(t *testing.T) {
	usage := map[string]string{}

	t.Run("details_absent", func(t *testing.T) {
		f := newChatFixture(t, chatFixtureOptions{})
		f.handler.body = chatUsageBody(chatFixtureModel, `{"prompt_tokens":11,"completion_tokens":0}`)
		if _, err := f.run(); err != nil {
			t.Fatalf("ExecuteChat: %v", err)
		}
		record := chatOutcomeRecord(t, f)
		usage["details_absent"] = chatUsageProjection(t, record.meta)

		if v, ok := chatMetaCounter(t, record.meta, "input_tokens"); !ok || v != 11 {
			t.Fatalf("input_tokens = %d/%v", v, ok)
		}
		// An EXPLICIT ZERO is persisted as zero, not dropped as if it were missing.
		if v, ok := chatMetaCounter(t, record.meta, "output_tokens"); !ok || v != 0 {
			t.Fatalf("output_tokens = %d/%v; an explicit zero must survive", v, ok)
		}
		if _, ok := chatMetaCounter(t, record.meta, "total_tokens"); ok {
			t.Fatal("an absent total_tokens was invented in the record")
		}
		if chatMetaBool(t, record.meta, "prompt_tokens_details_present") ||
			chatMetaBool(t, record.meta, "completion_tokens_details_present") {
			t.Fatal("an absent details object was recorded as present")
		}
		for _, key := range []string{"prompt_cached_tokens", "prompt_audio_tokens", "completion_reasoning_tokens"} {
			if _, ok := chatMetaCounter(t, record.meta, key); ok {
				t.Fatalf("%s exists with no details object", key)
			}
		}
	})

	t.Run("details_present_but_empty", func(t *testing.T) {
		f := newChatFixture(t, chatFixtureOptions{})
		f.handler.body = chatUsageBody(chatFixtureModel,
			`{"prompt_tokens":11,"completion_tokens":0,"prompt_tokens_details":{},"completion_tokens_details":{}}`)
		if _, err := f.run(); err != nil {
			t.Fatalf("ExecuteChat: %v", err)
		}
		record := chatOutcomeRecord(t, f)
		usage["details_present_but_empty"] = chatUsageProjection(t, record.meta)

		if !chatMetaBool(t, record.meta, "prompt_tokens_details_present") ||
			!chatMetaBool(t, record.meta, "completion_tokens_details_present") {
			t.Fatal("a present-but-empty details object was recorded as absent")
		}
		// Present and empty: the object exists, and NOT ONE of its counters does.
		for _, key := range []string{
			"prompt_cached_tokens", "prompt_audio_tokens", "completion_reasoning_tokens",
			"completion_audio_tokens", "completion_accepted_prediction_tokens", "completion_rejected_prediction_tokens",
		} {
			if _, ok := chatMetaCounter(t, record.meta, key); ok {
				t.Fatalf("%s was invented for an empty details object", key)
			}
		}
	})

	t.Run("partial_details_with_an_explicit_zero", func(t *testing.T) {
		f := newChatFixture(t, chatFixtureOptions{})
		f.handler.body = chatUsageBody(chatFixtureModel,
			`{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18,`+
				`"prompt_tokens_details":{"cached_tokens":0},`+
				`"completion_tokens_details":{"reasoning_tokens":5,"rejected_prediction_tokens":0}}`)
		if _, err := f.run(); err != nil {
			t.Fatalf("ExecuteChat: %v", err)
		}
		record := chatOutcomeRecord(t, f)
		for _, want := range []struct {
			key   string
			value int64
		}{
			{"input_tokens", 11}, {"output_tokens", 7}, {"total_tokens", 18},
			{"prompt_cached_tokens", 0}, {"completion_reasoning_tokens", 5},
			{"completion_rejected_prediction_tokens", 0},
		} {
			if v, ok := chatMetaCounter(t, record.meta, want.key); !ok || v != want.value {
				t.Fatalf("%s = %d/%v, want %d", want.key, v, ok, want.value)
			}
		}
		// The counters the gateway did NOT report stay absent — not zero.
		for _, key := range []string{"prompt_audio_tokens", "completion_audio_tokens", "completion_accepted_prediction_tokens"} {
			if _, ok := chatMetaCounter(t, record.meta, key); ok {
				t.Fatalf("%s was invented", key)
			}
		}
		if !chatMetaBool(t, record.meta, "prompt_tokens_details_present") {
			t.Fatal("a populated prompt details object was recorded as absent")
		}
	})

	t.Run("absent_usage_records_no_counter_at_all", func(t *testing.T) {
		f := newChatFixture(t, chatFixtureOptions{})
		f.handler.body = `{"object":"chat.completion","model":"` + chatFixtureModel +
			`","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"ok"}}]}`
		if _, err := f.run(); err != nil {
			t.Fatalf("ExecuteChat: %v", err)
		}
		record := chatOutcomeRecord(t, f)
		if record.meta["usage_status"] != models.ChatUsageNotReported {
			t.Fatalf("usage_status = %v", record.meta["usage_status"])
		}
		for _, key := range []string{
			"input_tokens", "output_tokens", "total_tokens", "prompt_cached_tokens",
			"prompt_tokens_details_present", "completion_tokens_details_present",
		} {
			if _, present := record.meta[key]; present {
				t.Fatalf("%s exists for an answer that reported no usage at all", key)
			}
		}
	})

	// The claim R2 actually made: these two observations are DIFFERENT and the record must
	// say so IN ITS DATA. Two gateways, one reporting no details object and one reporting an
	// empty details object, described the same way is the loss the review named — the
	// observation digest moved, but the record a reader reconciles did not.
	t.Run("an_absent_details_object_and_an_empty_one_are_distinguishable", func(t *testing.T) {
		absent, empty := usage["details_absent"], usage["details_present_but_empty"]
		if absent == "" || empty == "" {
			t.Fatal("the two subtests above did not both record a usage projection")
		}
		if absent == empty {
			t.Fatalf("an absent details object and an empty one are recorded identically: %s", absent)
		}
	})
}

// TestChatOutcomeRecordsTheObservedModelAndItsMismatch: the model the gateway NAMED is kept
// as a labelled observation, and the mismatch is recorded explicitly rather than being left
// to a generic protocol_error that no reader can reconcile.
func TestChatOutcomeRecordsTheObservedModelAndItsMismatch(t *testing.T) {
	t.Run("exact_match_records_the_absence_of_a_mismatch", func(t *testing.T) {
		f := newChatFixture(t, chatFixtureOptions{})
		if _, err := f.run(); err != nil {
			t.Fatalf("ExecuteChat: %v", err)
		}
		record := chatOutcomeRecord(t, f)
		if record.meta["observed_model"] != chatFixtureModel {
			t.Fatalf("observed_model = %v, want %q", record.meta["observed_model"], chatFixtureModel)
		}
		if chatMetaBool(t, record.meta, "model_mismatch") {
			t.Fatal("an exact match was recorded as a mismatch")
		}
		if chatMetaBool(t, record.meta, "observed_model_truncated") {
			t.Fatal("a short model name was recorded as truncated")
		}
	})

	t.Run("mismatch_is_named_and_keeps_its_usage", func(t *testing.T) {
		f := newChatFixture(t, chatFixtureOptions{})
		f.handler.body = chatCompletionBody("a-different-model", "text from the wrong model")
		res, err := f.run()
		if code := chatErrorCode(t, err); code != models.ChatErrProtocolError {
			t.Fatalf("code = %s", code)
		}
		if res.Output != nil {
			t.Fatal("a mismatched model released its text")
		}
		record := chatOutcomeRecord(t, f)
		if record.meta["observed_model"] != "a-different-model" {
			t.Fatalf("observed_model = %v; the record cannot be reconciled without it", record.meta["observed_model"])
		}
		if !chatMetaBool(t, record.meta, "model_mismatch") {
			t.Fatal("a model mismatch was not recorded as one")
		}
		if record.meta["completion_state"] != models.ChatCompletionProtocolError {
			t.Fatalf("completion_state = %v", record.meta["completion_state"])
		}
		// The observation survives the withholding: 11/7 were reported by the gateway and
		// stay reported, without being attributed to the pinned model.
		if v, ok := chatMetaCounter(t, record.meta, "input_tokens"); !ok || v != 11 {
			t.Fatalf("input_tokens = %d/%v", v, ok)
		}
		if v, ok := chatMetaCounter(t, record.meta, "output_tokens"); !ok || v != 7 {
			t.Fatalf("output_tokens = %d/%v", v, ok)
		}
		if record.meta["usage_status"] != models.ChatUsageReported {
			t.Fatalf("usage_status = %v", record.meta["usage_status"])
		}
		// No prompt, no output text, no refusal string reaches the ledger.
		if strings.Contains(record.raw, "text from the wrong model") || strings.Contains(record.raw, f.request.Input) {
			t.Fatalf("content reached the canonical metadata: %s", record.raw)
		}
	})

	t.Run("a_gateway_named_model_is_bounded_before_it_is_recorded", func(t *testing.T) {
		// The model string is chosen by the remote side and arrives inside a body bounded
		// only by MaxResponseBytes, so the record publishes a bounded prefix and SAYS it
		// did. The complete string is still committed by the observation digest.
		long := strings.Repeat("m", 400)
		f := newChatFixture(t, chatFixtureOptions{})
		f.handler.body = chatCompletionBody(long, "ok")
		if _, err := f.run(); chatErrorCode(t, err) != models.ChatErrProtocolError {
			t.Fatalf("an unpinned model was accepted: %v", err)
		}
		record := chatOutcomeRecord(t, f)
		observed, _ := record.meta["observed_model"].(string)
		if len(observed) != 128 || observed != strings.Repeat("m", 128) {
			t.Fatalf("observed_model has %d bytes; the record copies a bounded prefix", len(observed))
		}
		if !chatMetaBool(t, record.meta, "observed_model_truncated") {
			t.Fatal("a truncated model name was not marked as truncated")
		}
		if !chatMetaBool(t, record.meta, "model_mismatch") {
			t.Fatal("an unpinned model was not recorded as a mismatch")
		}
	})
}

// --- group 8: digests ---------------------------------------------------------------------------

// TestChatEffectDigestMovesOnEveryGoverningDimension varies one dimension at a time and
// requires the binding to change. A dimension this table omits is one the evidence would
// silently stop committing to.
func TestChatEffectDigestMovesOnEveryGoverningDimension(t *testing.T) {
	base := func() *chatPlan {
		prepared, err := modelprovider.PrepareChatTextRequest(modelprovider.ChatTextRequest{
			Model: "m", Input: "hello", MaxCompletionTokens: 16,
		})
		if err != nil {
			t.Fatalf("prepare: %v", err)
		}
		return &chatPlan{
			in: models.ChatExecutionRequest{
				Tenant:    model.TenantID("11111111-1111-1111-1111-111111111111"),
				Principal: auth.ScopedPrincipal(model.ID("22222222-2222-2222-2222-222222222222"), "a", "11111111-1111-1111-1111-111111111111", auth.RoleEditor),
				PolicyID:  model.ID("33333333-3333-3333-3333-333333333333"), PolicyVersion: 2,
				PolicySpecDigest: [32]byte{9},
				Profile: models.ExecutionProfile{
					Tenant: "11111111-1111-1111-1111-111111111111", Ref: "r", Revision: "sha256:" + strings.Repeat("a", 64),
					Action: models.ExecutionActionTextGenerate, Protocol: models.ExecutionProtocolChatTextV1,
					AdapterID: models.ExecutionAdapterModelProviderChat, AdapterVersion: "1",
					ProviderRef: "p", ModelRef: "m", Endpoint: "https://x.invalid/v1", Surface: "direct",
					InferenceGeo: "us", CredentialAudience: "https://x.invalid", AuthScheme: "bearer",
					TransportKey: "k", MaxRequestBytes: 1024, MaxResponseBytes: 1024, Timeout: time.Second,
				},
				Target:    modelrouter.Target{ProviderRef: "p", ModelRef: "m"},
				Input:     "hello",
				MaxTokens: 16, SessionRef: "attribution",
			},
			prepared: prepared, requestRef: "req", attemptRef: "att",
			pol: chatDefaultPolicy(),
		}
	}
	baseline := chatEffectDigest(base())
	tests := []struct {
		name   string
		mutate func(*chatPlan)
	}{
		{"request_ref", func(p *chatPlan) { p.requestRef = "req2" }},
		{"attempt_ref", func(p *chatPlan) { p.attemptRef = "att2" }},
		{"tenant", func(p *chatPlan) { p.in.Tenant = "44444444-4444-4444-4444-444444444444" }},
		{"principal", func(p *chatPlan) {
			p.in.Principal = auth.ScopedPrincipal(model.ID("55555555-5555-5555-5555-555555555555"), "b", p.in.Tenant, auth.RoleEditor)
		}},
		{"policy_id", func(p *chatPlan) { p.in.PolicyID = model.ID("66666666-6666-6666-6666-666666666666") }},
		{"policy_version", func(p *chatPlan) { p.in.PolicyVersion = 3 }},
		{"routing_spec_digest", func(p *chatPlan) { p.in.PolicySpecDigest = [32]byte{10} }},
		{"profile_revision", func(p *chatPlan) { p.in.Profile.Revision = "sha256:" + strings.Repeat("b", 64) }},
		{"endpoint", func(p *chatPlan) { p.in.Profile.Endpoint = "https://y.invalid/v1" }},
		{"model_ref", func(p *chatPlan) { p.in.Profile.ModelRef = "m2" }},
		{"surface", func(p *chatPlan) { p.in.Profile.Surface = "bedrock" }},
		{"inference_geo", func(p *chatPlan) { p.in.Profile.InferenceGeo = "eu" }},
		{"credential_audience", func(p *chatPlan) { p.in.Profile.CredentialAudience = "https://y.invalid" }},
		{"auth_scheme", func(p *chatPlan) { p.in.Profile.AuthScheme = "none" }},
		{"transport_key", func(p *chatPlan) { p.in.Profile.TransportKey = "k2" }},
		{"allow_http", func(p *chatPlan) { p.in.Profile.AllowHTTP = true }},
		{"max_response_bytes", func(p *chatPlan) { p.in.Profile.MaxResponseBytes = 2048 }},
		{"timeout", func(p *chatPlan) { p.in.Profile.Timeout = 2 * time.Second }},
		{"expected_target", func(p *chatPlan) { p.in.Target.ViaGateway = true }},
		{"prepared_bytes", func(p *chatPlan) {
			prepared, _ := modelprovider.PrepareChatTextRequest(modelprovider.ChatTextRequest{
				Model: "m", Input: "a different prompt", MaxCompletionTokens: 16,
			})
			p.prepared = prepared
		}},
		// ⛔ THIS CASE EXISTS BECAUSE THE ONE ABOVE IS NOT ENOUGH, and a control positive
		// proved it: deleting the prepared DIGEST from the preimage and keeping only its
		// byte LENGTH left the table green, because "hello" and "a different prompt" have
		// different lengths. A same-length substitution is the only input that separates
		// "the digest is bound" from "the size is bound".
		{"prepared_digest_at_the_same_length", func(p *chatPlan) {
			prepared, _ := modelprovider.PrepareChatTextRequest(modelprovider.ChatTextRequest{
				Model: "m", Input: "hellp", MaxCompletionTokens: 16,
			})
			if len(prepared.Bytes()) != len(p.prepared.Bytes()) {
				t.Fatalf("the substitution changed the length (%d vs %d); it would not isolate the digest",
					len(prepared.Bytes()), len(p.prepared.Bytes()))
			}
			p.prepared = prepared
		}},
		{"max_tokens", func(p *chatPlan) { p.in.MaxTokens = 17 }},
		{"attribution_session_ref", func(p *chatPlan) { p.in.SessionRef = "other" }},
		{"proxy_policy_digest", func(p *chatPlan) { p.polDigest = [32]byte{7} }},
		{"residency_pin", func(p *chatPlan) { p.pin = "eu" }},
		{"residency_gap", func(p *chatPlan) { p.residencyGap = true }},
		{"context_gap", func(p *chatPlan) { p.contextGap = true }},
		{"context_max_tokens", func(p *chatPlan) { p.ctxPol.MaxContextTokens = 5 }},
		{"context_excluded_sources", func(p *chatPlan) { p.ctxPol.ExcludedSources = []string{"kb-1"} }},
		{"input_inspection_verdict", func(p *chatPlan) { p.inputVerdict.Forward = true }},
		{"input_inspection_meter", func(p *chatPlan) { p.inputVerdict.Meter.Inspections = 1 }},
		{"budget_status", func(p *chatPlan) { p.budgetStatus = 402 }},
		{"budget_denied", func(p *chatPlan) { p.budgetDenied = true }},
		{"record_mandatory", func(p *chatPlan) { p.pol.RecordMandatory = false }},
		{"witness_presence", func(p *chatPlan) {
			p.in.Authorization.EvidenceDigest = [32]byte{1}
		}},
	}
	seen := map[[32]byte]string{baseline: "baseline"}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := base()
			tt.mutate(plan)
			got := chatEffectDigest(plan)
			if got == baseline {
				t.Fatalf("%s does not move the effect digest", tt.name)
			}
			if prior, clash := seen[got]; clash {
				t.Fatalf("%s collides with %s", tt.name, prior)
			}
			seen[got] = tt.name
		})
	}
	// Stability: the same plan hashes the same way every time (the excluded-source set is
	// sorted, so a store's return order cannot perturb it).
	stable := base()
	stable.ctxPol.ExcludedSources = []string{"b", "a", "c"}
	first := chatEffectDigest(stable)
	stable.ctxPol.ExcludedSources = []string{"c", "a", "b"}
	if chatEffectDigest(stable) != first {
		t.Fatal("the effect digest depends on the presentation order of a set")
	}
}

// TestChatObservationDigestSeparatesNullFromPresent proves the decoded-observation digest
// distinguishes every nullable state, which is the whole reason it exists as a separate
// domain from the effect.
func TestChatObservationDigestSeparatesNullFromPresent(t *testing.T) {
	text, empty := "answer", ""
	zero, seven := int64(0), int64(7)
	base := modelprovider.ChatTextResponse{Model: "m", State: modelprovider.ChatTextStopped, FinishReason: "stop", Text: &text}
	tests := []struct {
		name   string
		mutate func(*modelprovider.ChatTextResponse)
	}{
		{"absent_text", func(r *modelprovider.ChatTextResponse) { r.Text = nil }},
		{"empty_text", func(r *modelprovider.ChatTextResponse) { r.Text = &empty }},
		{"refusal_present", func(r *modelprovider.ChatTextResponse) { r.Refusal = &empty }},
		{"model", func(r *modelprovider.ChatTextResponse) { r.Model = "m2" }},
		{"state", func(r *modelprovider.ChatTextResponse) { r.State = modelprovider.ChatTextLengthLimited }},
		{"finish_reason", func(r *modelprovider.ChatTextResponse) { r.FinishReason = "length" }},
		{"usage_object_present_but_empty", func(r *modelprovider.ChatTextResponse) { r.Usage = &modelprovider.ChatTextUsage{} }},
		{"usage_explicit_zero", func(r *modelprovider.ChatTextResponse) {
			r.Usage = &modelprovider.ChatTextUsage{PromptTokens: &zero}
		}},
		{"usage_nonzero", func(r *modelprovider.ChatTextResponse) {
			r.Usage = &modelprovider.ChatTextUsage{PromptTokens: &seven}
		}},
		{"usage_details", func(r *modelprovider.ChatTextResponse) {
			r.Usage = &modelprovider.ChatTextUsage{CompletionTokensDetails: &modelprovider.ChatTextCompletionTokensDetails{ReasoningTokens: &zero}}
		}},
	}
	baseline := chatObservationDigest(base)
	seen := map[[32]byte]string{baseline: "baseline"}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := base
			tt.mutate(&r)
			got := chatObservationDigest(r)
			if prior, clash := seen[got]; clash {
				t.Fatalf("%s collides with %s", tt.name, prior)
			}
			seen[got] = tt.name
		})
	}
}

// TestChatOutcomeRecordsADecodedObservationDigestAndNoWireChecksum: the metadata names the
// digest for what it is, and no key claims to be a checksum of upstream bytes this process
// never held.
func TestChatOutcomeRecordsADecodedObservationDigestAndNoWireChecksum(t *testing.T) {
	f := newChatFixture(t, chatFixtureOptions{})
	if _, err := f.run(); err != nil {
		t.Fatalf("ExecuteChat: %v", err)
	}
	outcomes := f.recordsWithAction(chatOutcomeAction)
	if len(outcomes) != 1 {
		t.Fatalf("recorded %d outcomes", len(outcomes))
	}
	meta := outcomes[0].meta
	if _, ok := meta["decoded_observation_digest"].(string); !ok {
		t.Fatalf("the outcome did not record a decoded-observation digest: %+v", meta)
	}
	for _, forbidden := range []string{"response_sha", "response_digest", "upstream_sha", "resp_sha", "wire_digest"} {
		if _, present := meta[forbidden]; present {
			t.Fatalf("the outcome published %q, a checksum of bytes C1 never returns", forbidden)
		}
	}
}

// --- activation ------------------------------------------------------------------------------------

// TestModelGatewayChatModeIsDisabledByDefaultAndFailsClosedOnAnUnknownValue: profiles alone
// never enable dispatch, and a value this build does not have is a startup error rather than
// a silent disable.
func TestModelGatewayChatModeIsDisabledByDefaultAndFailsClosedOnAnUnknownValue(t *testing.T) {
	tests := []struct {
		raw  string
		want string
		err  bool
	}{
		{raw: "", want: modelGatewayChatDisabled},
		{raw: "disabled", want: modelGatewayChatDisabled},
		{raw: "  development_precheck  ", want: modelGatewayChatDevelopmentPrecheck},
		{raw: "hard_budget", err: true},
		{raw: "enabled", err: true},
		{raw: "DISABLED", err: true},
		{raw: "1", err: true},
	}
	for _, tt := range tests {
		t.Run(strconv.Quote(tt.raw), func(t *testing.T) {
			got, err := loadModelGatewayChatMode(func(string) string { return tt.raw })
			if tt.err {
				if err == nil {
					t.Fatalf("%q was accepted as %q", tt.raw, got)
				}
				if !strings.Contains(err.Error(), "hard-budget") {
					t.Fatalf("the refusal does not say why there is no hard-budget mode: %v", err)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("%q = %q, %v; want %q", tt.raw, got, err, tt.want)
			}
		})
	}
}

// TestChatExecutorIsBuiltOnlyUnderTheExplicitActivation: a registry alone is not enough, and
// the disabled default builds nothing at all.
func TestChatExecutorIsBuiltOnlyUnderTheExplicitActivation(t *testing.T) {
	registry := &modelGatewayProfileRegistry{profiles: map[modelGatewayProfileKey]modelGatewayProfileConfig{}}
	if x := newModelsChatExecutor(modelGatewayChatDisabled, registry, func(string) string { return "" }, discardLog()); x != nil {
		t.Fatal("the disabled mode built a Chat executor")
	}
	if x := newModelsChatExecutor(modelGatewayChatDevelopmentPrecheck, nil, func(string) string { return "" }, discardLog()); x != nil {
		t.Fatal("the development mode built a Chat executor with no profile registry")
	}
	x := newModelsChatExecutor(modelGatewayChatDevelopmentPrecheck, registry, func(string) string { return "" }, discardLog())
	if x == nil {
		t.Fatal("the development mode built nothing")
	}
	if x.ready() {
		t.Fatal("an unbound adapter reported itself ready; a half-bound port must refuse")
	}
	if _, err := x.ExecuteChat(context.Background(), models.ChatExecutionRequest{}, func(context.Context) (int, bool) { return 0, false }); chatErrorCode(t, err) != models.ChatErrExecutionNotReady {
		t.Fatalf("an unbound adapter did not refuse with chat_execution_not_ready: %v", err)
	}
}

// TestChatBindIsAtomicAndRefusesAPartialSet: binding is one call, and a set missing a
// dependency the operation cannot run without leaves the port refusing.
func TestChatBindIsAtomicAndRefusesAPartialSet(t *testing.T) {
	registry := &modelGatewayProfileRegistry{profiles: map[modelGatewayProfileKey]modelGatewayProfileConfig{}}
	x := newModelsChatExecutor(modelGatewayChatDevelopmentPrecheck, registry, func(string) string { return "" }, discardLog())
	st, _, _ := newChatStore(t, chatFixtureOptions{})
	if x.bind(chatExecutorDeps{Store: st, Policy: &stubProxyPolicy{}}) {
		t.Fatal("a partial dependency set reported ready")
	}
	if !x.bind(chatExecutorDeps{
		Store: st, Policy: &stubProxyPolicy{}, ContextPolicy: &stubContextPolicy{},
		Secrets: chatFixtureResolver(nil),
	}) {
		t.Fatal("a complete dependency set did not report ready")
	}
}

// --- helpers used by the tests above ------------------------------------------------------------------

func chatInt64(v int64) *int64 { return &v }

func chatResidencyRegistry(t *testing.T, home string) *residency.Registry {
	t.Helper()
	reg, err := residency.NewRegistry(home, []string{"us", "eu"})
	if err != nil {
		t.Fatalf("residency registry: %v", err)
	}
	return reg
}

func chatPinTenantRegion(t *testing.T, f *chatFixture, region string) {
	t.Helper()
	if err := f.store.System(context.Background(), func(sys store.SystemScope) error {
		_, err := sys.SetOrgRegion(context.Background(), f.tenant, region)
		return err
	}); err != nil {
		t.Fatalf("pin tenant region: %v", err)
	}
}

func chatPendingDrops(t *testing.T, st store.Store) int64 {
	t.Helper()
	//nolint:misspell // agent noun of store.AuditSpoolStatuser, not "stature".
	statuser, ok := st.(store.AuditSpoolStatuser)
	if !ok {
		t.Fatal("store does not expose AuditSpoolStatuser")
	}
	//nolint:misspell // same local as above.
	status, configured, err := statuser.AuditSpoolStatus(context.Background())
	if err != nil {
		t.Fatalf("audit spool status: %v", err)
	}
	if !configured {
		t.Fatal("the audit spool budget is not configured on this store")
	}
	return status.PendingDrops
}

// chatRegistryWithTimeout rebuilds the fixture registry with a different timeout, keeping
// every other field identical, so a timeout causal does not have to hand-edit an immutable
// record's revision.
func chatRegistryWithTimeout(t *testing.T, f *chatFixture, timeout time.Duration) *modelGatewayProfileRegistry {
	t.Helper()
	registry := &modelGatewayProfileRegistry{profiles: map[modelGatewayProfileKey]modelGatewayProfileConfig{}}
	for key, entry := range f.exec.registry.profiles {
		entry.TimeoutMS = int64(timeout / time.Millisecond)
		revision, err := calculateModelGatewayProfileRevision(entry)
		if err != nil {
			t.Fatalf("recompute revision: %v", err)
		}
		entry.Revision = revision
		registry.profiles[modelGatewayProfileKey{tenant: key.tenant, ref: key.ref, revision: revision}] = entry
	}
	profile, err := registry.ResolveExecutionProfile(context.Background(), f.tenant, "chat-primary", chatFixtureRevision(t, registry, f.tenant))
	if err != nil {
		t.Fatalf("resolve rebuilt profile: %v", err)
	}
	f.request.Profile = profile
	f.profile = profile
	return registry
}

func chatBindingFor(attemptRef, effect string) sdk.EvidenceBinding {
	return sdk.EvidenceBinding{OperationID: sdk.OperationID(attemptRef), EffectDigest: sdk.EffectDigest(effect)}
}

func chatReceiptFor(binding sdk.EvidenceBinding, ref string) sdk.EvidenceReceipt {
	return sdk.ClassifyAnchor(binding, ref, false, sdk.EvidenceFaultNone)
}

// --- group 9: G1 format identity and the derived v2 codecs ---------------------------------------------

// The hex digests below were computed by the assessment-owned encoder
// (an internal design note (not shipped)) from the written specification
// in docs/design/route-evidence-digest-v2.md, never by asking this package for its answer. The
// three inner values are core/auth vectors for the generic witness digest and are consumed
// here as opaque 32-byte inputs, which is exactly how the effect codec consumes them.
const (
	chatVectorInnerMixed      = "e4557d5f26b171282aadd6104726f283073bfa45cdfc0bd68c2355a677f15f26"
	chatVectorInnerLeaseOnly  = "91b046671cc0464e6adc49976e9a477627bc5fe238b3c972274bb9e51c2e72b4"
	chatVectorInnerRawSubject = "b42d374a622b7a4026bb6fede6ce5cb7de47d1068d4830bbed4405a037a26182"
	chatVectorEffectPresent   = "70a270193e3bd800a5687d2b2b102c1b8fb70f7da9227c6642f4d987f9265886"
	chatVectorEffectAbsent    = "cce4b105904565e4320a5427ab98de33f86b0080796b99f575a8efc08262964c"
	chatVectorOutcomeNoObs    = "57c7ae13088f4872bbcee810ebd3e026ee9316894928a05c06886405ab4b6a93"
	chatVectorOutcomeObsUsage = "5f1bd5315d626853911dde71dc34c45e747f10c039ae610fa659f905e823a8de"
)

func chatVectorDigest(t *testing.T, want string) [32]byte {
	t.Helper()
	raw, err := hex.DecodeString(want)
	if err != nil || len(raw) != 32 {
		t.Fatalf("vector %q is not a 32-byte hex digest", want)
	}
	var out [32]byte
	copy(out[:], raw)
	return out
}

// chatVectorBytes is the 32-byte run first, first+1, ..., first+31 the encoder uses for
// opaque digests.
func chatVectorBytes(first byte) [32]byte {
	var out [32]byte
	for i := range out {
		out[i] = first + byte(i)
	}
	return out
}

// chatFixedVectorWitness is the encoder's route witness: every field a literal, and the
// evidence digest one of the core/auth vectors.
func chatFixedVectorWitness(t *testing.T, inner string) auth.RouteAuthorizationWitness {
	t.Helper()
	return auth.RouteAuthorizationWitness{
		CedarAction: "session:stop", ScopedEffect: auth.EffectGrant,
		Decision:       auth.AuthorizationEvidence{Outcome: auth.EvidenceAllow},
		ResourceDigest: chatVectorBytes(1), EvidenceDigest: chatVectorDigest(t, inner),
		QuestionDigest: chatVectorBytes(101), PolicyVersion: 7,
	}
}

// chatFixedVectorPlan is the encoder's plan: the ZERO principal, the zero prepared request,
// the zero governance snapshot and a literal profile, so every dimension of the effect
// preimage is a value written in both places.
func chatFixedVectorPlan(witness auth.RouteAuthorizationWitness) *chatPlan {
	return &chatPlan{
		in: models.ChatExecutionRequest{
			Tenant:        model.TenantID("11111111-1111-7111-8111-111111111111"),
			Authorization: witness,
			PolicyID:      model.ID("33333333-3333-7333-8333-333333333333"), PolicyVersion: 2,
			PolicySpecDigest: [32]byte{9},
			Profile: models.ExecutionProfile{
				Ref: "r", Revision: "sha256:" + strings.Repeat("a", 64),
				Action: models.ExecutionActionTextGenerate, Protocol: models.ExecutionProtocolChatTextV1,
				AdapterID: models.ExecutionAdapterModelProviderChat, AdapterVersion: models.ExecutionAdapterVersion1,
				ProviderRef: "p", ModelRef: "m", Endpoint: "https://x.invalid/v1", Surface: "direct",
				InferenceGeo: "us", CredentialAudience: "https://x.invalid", AuthScheme: "bearer",
				TransportKey: "k", MaxRequestBytes: 1024, MaxResponseBytes: 1024, Timeout: time.Second,
			},
			Target:    modelrouter.Target{ProviderRef: "p", ModelRef: "m"},
			MaxTokens: 16, SessionRef: "attribution",
		},
		requestRef: "req", attemptRef: "att",
	}
}

// TestChatCodecsNameTheirV2DomainsAndKeepObservationV1 pins the format identities G1 fixed:
// the effect and outcome codecs are v2, the decoded-observation codec stays v1 because its
// structure and semantics did not change, and the labels are the software-owned constants.
func TestChatCodecsNameTheirV2DomainsAndKeepObservationV1(t *testing.T) {
	for name, c := range map[string]struct{ got, want string }{
		"effect domain":      {chatEffectDomain, "olivares.model-gateway-chat.effect.v2"},
		"outcome domain":     {chatOutcomeDomain, "olivares.model-gateway-chat.outcome.v2"},
		"observation domain": {chatObservationDomain, "olivares.model-gateway-chat.observation.v1"},
		"algorithm label":    {chatDigestAlgorithm, "sha256"},
		"absent label":       {chatRouteEvidenceAbsent, "none"},
		"route format":       {auth.RouteEvidenceDigestFormat, "olivares.auth.route-evidence.v2"},
	} {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", name, c.got, c.want)
		}
	}
}

// TestChatEffectDigestMatchesTheIndependentFixedVectors compares the v2 effect codec with the
// encoder for a present and an absent witness, requires the shared presence predicate and the
// metadata label to agree with the codec, and shows the effect is lease-dependent: two inner
// digests that differ only in the lease subject bytes yield two effects.
func TestChatEffectDigestMatchesTheIndependentFixedVectors(t *testing.T) {
	present := chatFixedVectorPlan(chatFixedVectorWitness(t, chatVectorInnerMixed))
	absent := chatFixedVectorPlan(auth.RouteAuthorizationWitness{})
	for name, c := range map[string]struct {
		plan  *chatPlan
		want  string
		bound bool
		label string
	}{
		"witness present": {present, chatVectorEffectPresent, true, auth.RouteEvidenceDigestFormat},
		"witness absent":  {absent, chatVectorEffectAbsent, false, chatRouteEvidenceAbsent},
	} {
		t.Run(name, func(t *testing.T) {
			got := chatEffectDigest(c.plan)
			if hex.EncodeToString(got[:]) != c.want {
				t.Fatalf("effect digest = %x, want the independent vector %s", got, c.want)
			}
			if chatEffectDigest(c.plan) != got {
				t.Fatal("the effect digest is not stable across calls")
			}
			if chatRouteWitnessPresent(c.plan.in.Authorization) != c.bound {
				t.Fatalf("presence predicate = %v, want %v", !c.bound, c.bound)
			}
			if chatRouteEvidenceFormat(c.plan.in.Authorization) != c.label {
				t.Fatalf("route format label = %q, want %q", chatRouteEvidenceFormat(c.plan.in.Authorization), c.label)
			}
		})
	}
	leaseOnly := chatEffectDigest(chatFixedVectorPlan(chatFixedVectorWitness(t, chatVectorInnerLeaseOnly)))
	rawSubject := chatEffectDigest(chatFixedVectorPlan(chatFixedVectorWitness(t, chatVectorInnerRawSubject)))
	if leaseOnly == rawSubject {
		t.Fatal("two witnesses whose leased fact differs only in subject bytes bind the same effect")
	}
}

// TestChatOutcomeHashMatchesTheIndependentFixedVectors compares the v2 outcome codec with the
// encoder without an observation and with an observation plus three usage counters, the
// second over the effect the present-witness vector produced.
func TestChatOutcomeHashMatchesTheIndependentFixedVectors(t *testing.T) {
	res := models.ChatExecutionResult{
		DispatchState: models.ChatDispatchAttempted, CompletionState: models.ChatCompletionStop,
		UsageStatus: models.ChatUsageReported, IntentDisposition: models.ChatIntentAnchored,
	}
	noObservation := &chatPlan{attemptRef: "att", effect: chatVectorBytes(41)}
	if got := hex.EncodeToString(chatOutcomeHash(noObservation, &res, nil)); got != chatVectorOutcomeNoObs {
		t.Fatalf("outcome hash (no observation) = %s, want %s", got, chatVectorOutcomeNoObs)
	}
	eleven, seven, eighteen := int64(11), int64(7), int64(18)
	withUsage := res
	withUsage.Usage = &modelprovider.ChatTextUsage{PromptTokens: &eleven, CompletionTokens: &seven, TotalTokens: &eighteen}
	observed := &chatPlan{attemptRef: "att", effect: chatVectorDigest(t, chatVectorEffectPresent)}
	observation := &chatObservation{digest: chatVectorBytes(61), observedModel: "gw-model-1"}
	if got := hex.EncodeToString(chatOutcomeHash(observed, &withUsage, observation)); got != chatVectorOutcomeObsUsage {
		t.Fatalf("outcome hash (observation and usage) = %s, want %s", got, chatVectorOutcomeObsUsage)
	}
}

// chatEvidenceFormatLabels is the exact software-owned label set G1 appends: three on both
// legs, two more on the outcome.
func chatEvidenceFormatLabels(action, routeFormat string) map[string]string {
	labels := map[string]string{
		"effect_digest_format":         chatEffectDomain,
		"effect_digest_algorithm":      chatDigestAlgorithm,
		"route_evidence_digest_format": routeFormat,
	}
	if action == chatOutcomeAction {
		labels["outcome_digest_format"] = chatOutcomeDomain
		labels["outcome_digest_algorithm"] = chatDigestAlgorithm
	}
	return labels
}

// TestChatEvidenceMetadataLabelsItsDigestFormats is the G1 append-side control over the REAL
// store: both records carry exactly the software-owned labels, the route format label is
// "none" for the ordinary door and the auth format when a witness was bound — the same
// presence decision the codec uses — and no label carries content. Before G1 no record
// carried any of these keys, so a reader could not tell which codec produced a stored digest.
func TestChatEvidenceMetadataLabelsItsDigestFormats(t *testing.T) {
	for name, c := range map[string]struct {
		witness auth.RouteAuthorizationWitness
		format  string
	}{
		"ordinary door, no witness": {auth.RouteAuthorizationWitness{}, chatRouteEvidenceAbsent},
		"witness bound":             {chatFixedVectorWitness(t, chatVectorInnerMixed), auth.RouteEvidenceDigestFormat},
	} {
		t.Run(name, func(t *testing.T) {
			f := newChatFixture(t, chatFixtureOptions{})
			f.request.Authorization = c.witness
			f.request.Input = "a distinctive prompt string"
			if _, err := f.run(); err != nil {
				t.Fatalf("ExecuteChat: %v", err)
			}
			forbidden := []string{"a distinctive prompt string", f.env["CHAT_FIXTURE_BEARER"], "/v1/chat/completions"}
			for _, action := range []string{chatIntentAction, chatOutcomeAction} {
				records := f.recordsWithAction(action)
				if len(records) != 1 {
					t.Fatalf("%s recorded %d times", action, len(records))
				}
				for key, want := range chatEvidenceFormatLabels(action, c.format) {
					got, present := records[0].meta[key].(string)
					if !present || got != want {
						t.Errorf("%s metadata %s = %v, want %q: %s", action, key, records[0].meta[key], want, records[0].raw)
					}
					for _, needle := range forbidden {
						if needle != "" && strings.Contains(got, needle) {
							t.Errorf("%s label %s carries content %q", action, key, needle)
						}
					}
				}
				// The payload hash and the recorded effect digest are still the same commitment.
				effect, _ := records[0].meta["effect_digest"].(string)
				if action == chatIntentAction && hex.EncodeToString(records[0].event.PayloadHash) != effect {
					t.Fatalf("the intent payload hash %x does not equal its recorded effect digest %s", records[0].event.PayloadHash, effect)
				}
			}
		})
	}
}

// legacyShapedIntentMeta is FIXED historical-format metadata: the pre-G1 intent key set, as
// recorded on the unchanged production source before this change, with fixed values and no
// format label of any kind. It is a stored compatibility fixture and nothing more — not a
// prior-release binary, not a recovered preimage, and not a claim that the opaque payload it
// names can be reinterpreted under any codec.
func legacyShapedIntentMeta(effectDigest string) map[string]any {
	return map[string]any{
		"request_ref": "legacy-request", "attempt_ref": "legacy-attempt", "decision": "allow",
		"action": models.ExecutionActionTextGenerate, "protocol": models.ExecutionProtocolChatTextV1,
		"profile_ref": "chat-primary", "profile_revision": "sha256:" + strings.Repeat("0", 64),
		"provider_ref": "fixture-gateway", "model_ref": chatFixtureModel,
		"surface": "direct", "inference_geo": "us",
		"credential_audience": "https://legacy.invalid", "auth_scheme": "bearer",
		"policy_id": "01a086f5-a3be-7c35-bec4-934b07aad35c", "policy_version": int64(3),
		"routing_spec_digest": strings.Repeat("0", 64),
		"proxy_policy_digest": strings.Repeat("1", 64),
		"prepared_digest":     strings.Repeat("2", 64),
		"prepared_bytes":      int64(145),
		"effect_digest":       effectDigest,
		"residency_pin":       "", "residency_gap": false,
		"context_policy_gap": false,
		"budget_assurance":   models.ChatBudgetAssuranceDevelopmentPrecheck,
		"budget_precheck":    "not_denied",
		"record_mandatory":   true,
	}
}

// TestChatLedgerKeepsUnlabeledLegacyRecordsOpaqueBesideV2Records is the real-store
// compatibility fixture. A legacy-shaped, unlabeled record with an opaque payload hash is
// appended through the actual evidence writer and store; v2 intent and outcome records are
// then appended by a real attempt; the chain verifies end to end; and the legacy record's
// stored canonical metadata and hash are byte-identical before and after. The legacy record
// stays unlabeled: no codec here reinterprets its payload, and the test derives nothing from
// it beyond equality with what the store returned.
func TestChatLedgerKeepsUnlabeledLegacyRecordsOpaqueBesideV2Records(t *testing.T) {
	f := newChatFixture(t, chatFixtureOptions{})
	ctx := context.Background()
	opaque := sha256.Sum256([]byte("legacy opaque payload: its preimage was never stored and is not recoverable"))
	legacyMeta := legacyShapedIntentMeta(hex.EncodeToString(opaque[:]))
	binding := chatBindingFor("legacy-attempt", hex.EncodeToString(opaque[:]))
	receipt, err := inferenceEvidenceWriter{store: f.store}.Append(ctx, f.tenant, binding, model.AuditDraft{
		Actor: "user:legacy", ActorKind: model.ActorUser, Action: chatIntentAction,
		TargetKind: chatExecutionKind, TargetID: model.ID("legacy-attempt"),
		PayloadHash: opaque[:], Meta: legacyMeta,
	})
	if err != nil || !receipt.AnchoredFor(binding) {
		t.Fatalf("the legacy-shaped record did not anchor through the real store: %v (%+v)", err, receipt)
	}
	legacy := func(records []chatLedgerRecord) chatLedgerRecord {
		t.Helper()
		for _, record := range records {
			if record.event.TargetID == model.ID("legacy-attempt") {
				return record
			}
		}
		t.Fatal("the legacy-shaped record is not in the ledger")
		return chatLedgerRecord{}
	}
	before := legacy(f.auditRecords())
	expectedCanonical, err := json.Marshal(legacyMeta)
	if err != nil {
		t.Fatal(err)
	}
	if before.raw != string(expectedCanonical) {
		t.Fatalf("the stored canonical metadata is not the fixed legacy shape:\n got %s\nwant %s", before.raw, expectedCanonical)
	}
	if hex.EncodeToString(before.event.PayloadHash) != hex.EncodeToString(opaque[:]) {
		t.Fatal("the legacy payload hash was not stored opaquely")
	}
	for _, key := range []string{"effect_digest_format", "effect_digest_algorithm", "route_evidence_digest_format", "outcome_digest_format", "outcome_digest_algorithm"} {
		if _, present := before.meta[key]; present {
			t.Fatalf("the legacy-shaped record carries the label %q it never had", key)
		}
	}

	// New-format evidence through the real path.
	if _, err := f.run(); err != nil {
		t.Fatalf("ExecuteChat: %v", err)
	}
	after := legacy(f.auditRecords())
	if after.raw != before.raw || hex.EncodeToString(after.event.Hash) != hex.EncodeToString(before.event.Hash) ||
		hex.EncodeToString(after.event.PayloadHash) != hex.EncodeToString(before.event.PayloadHash) ||
		hex.EncodeToString(after.event.MetaCommitment) != hex.EncodeToString(before.event.MetaCommitment) ||
		after.event.Seq != before.event.Seq {
		t.Fatal("appending v2 records changed the legacy record's stored commitment")
	}
	if _, present := after.meta["effect_digest_format"]; present {
		t.Fatal("the legacy record was relabeled")
	}
	v2 := 0
	for _, record := range f.auditRecords() {
		if record.event.TargetID == model.ID("legacy-attempt") || (record.event.Action != chatIntentAction && record.event.Action != chatOutcomeAction) {
			continue
		}
		if record.event.Seq <= before.event.Seq {
			t.Fatalf("a v2 record was sequenced before the legacy record: %d <= %d", record.event.Seq, before.event.Seq)
		}
		if format, _ := record.meta["effect_digest_format"].(string); format != chatEffectDomain {
			t.Fatalf("a new %s record is not labeled v2: %s", record.event.Action, record.raw)
		}
		v2++
	}
	if v2 != 2 {
		t.Fatalf("expected one v2 intent and one v2 outcome beside the legacy record, found %d", v2)
	}
	if err := f.store.View(ctx, f.tenant, func(sc store.Scope) error {
		report, err := sc.Audit().Verify(ctx, 0)
		if err != nil {
			return err
		}
		if !report.OK || report.Checked < after.event.Seq+2 {
			return errors.New("chain verification failed or checked fewer events than were appended: " + report.Reason)
		}
		return nil
	}); err != nil {
		t.Fatalf("the interleaved chain does not verify: %v", err)
	}
}
