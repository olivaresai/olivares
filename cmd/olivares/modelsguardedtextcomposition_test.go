// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	claudeapi "github.com/olivaresai/olivares/connectors/claude-api"
	"github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/auth"
	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/secret"
	"github.com/olivaresai/olivares/core/secure"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/inferenceproxy"
	"github.com/olivaresai/olivares/modules/models"
)

// modelsguardedtextcomposition_test.go closes the causal R3 of the independent review
// returned D01-C2B for: the qualification had two useful halves and no whole.
//
// One group drove the CONCRETE executor to a real TLS server but constructed a
// ScopedPrincipal in a fixture and called ExecuteChat directly, so it never crossed the
// route's authentication, permission or gate chain. The other group DID authenticate the
// official handler but handed it a stubChatExecutor, so nothing was prepared, dispatched or
// recorded. Between them sat exactly the seam that R1 and R2 broke — the serialization the
// caller receives and the outcome the ledger keeps — and neither half could see it.
//
// This file runs the whole chain ONCE, over bounded local fixtures:
//
//	POST /v1/m/models/routing-policies/{id}/execute  (real api.Server, real Authenticator,
//	  real session bearer, real models:routing:admin, real routing decision + gates)
//	    → the module's profiled branch and its one-shot budget closure
//	      → modelsChatExecutor built by the PRODUCTION constructor and bound by the
//	        PRODUCTION dependency struct (wire.go / boot.go's two seams)
//	        → real C0 preparation → real C1 transport → local TLS gateway, trusted test CA
//	          → the HTTP response the caller actually receives
//	            → the canonical outcome the tamper-evident ledger actually keeps
//
// No provider, no network beyond loopback, no global configuration, no boot() process. The
// two seams this file uses are the ones boot() itself uses, named at their call sites.
//
// The R1/R2 assertions are made HERE, on the real wire body and the real canonical record,
// rather than only against the module stub and the executor fixture — that is the point of
// the return: the two defects lived precisely where the two halves stopped.

// chatComposition is one complete Chat-only composition: an API server serving exactly one
// module, over one store, in front of one synthetic gateway.
type chatComposition struct {
	t             *testing.T
	srv           *api.Server
	store         store.Store
	tenant        model.TenantID
	admin         string
	policyID      string
	profile       models.ExecutionProfile
	exec          *modelsChatExecutor
	upstream      *chatUpstream
	transport     *countingTransport
	signer        *audit.Signer
	env           map[string]string
	secretLookups *int
}

type chatCompositionOptions struct {
	policy    *inferenceproxy.ProxyPolicy
	inspector contentInspector
}

// newChatComposition assembles the composition. The ORDER is forced by the product: the
// immutable profile registry is keyed by tenant, and the tenant only exists once the store
// has provisioned an org — so the store comes first, the registry second, and the module
// (which needs the registry) third.
func newChatComposition(t *testing.T, opts chatCompositionOptions) *chatComposition {
	t.Helper()
	ctx := context.Background()

	upstream := &chatUpstream{body: chatCompletionBody(chatFixtureModel, "hello from the fixture")}
	server := httptest.NewTLSServer(upstream)
	t.Cleanup(server.Close)
	// The client trusts the SERVER'S OWN test CA and nothing else, so a dispatch that
	// reached any other origin would fail verification rather than quietly succeed.
	transport := &countingTransport{base: server.Client().Transport}

	endpoint := server.URL + "/v1/chat/completions"
	origin, err := profileEndpointOrigin(endpoint, false)
	if err != nil {
		t.Fatalf("composition endpoint origin: %v", err)
	}

	// --- the store, and the tenant the registry will be keyed by ------------------------
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate audit key: %v", err)
	}
	signer, err := audit.NewSigner(priv)
	if err != nil {
		t.Fatalf("audit signer: %v", err)
	}
	dir := t.TempDir()
	// The schema is the models module's own; a throwaway instance registers it because the
	// real one cannot be built before the tenant exists.
	st, err := coreengine.Open(ctx, store.Config{
		Engine: store.EngineSQLite, DSN: filepath.Join(dir, "composition.db"), SignEvent: signer.SignEvent,
	}, models.New().RegisterSchema)
	if err != nil {
		t.Fatalf("open composition store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	var tenant model.TenantID
	if err := st.System(ctx, func(sys store.SystemScope) error {
		if _, err := sys.EnsureSystemTenant(ctx); err != nil {
			return err
		}
		org, err := sys.CreateOrg(ctx, model.Org{Name: "chat-composition", Slug: "chat-composition", Status: model.StatusActive})
		if err == nil {
			tenant = org.TenantID
		}
		return err
	}); err != nil {
		t.Fatalf("provision composition tenant: %v", err)
	}

	// --- the immutable registry, through the real strict loader -------------------------
	env := map[string]string{"CHAT_FIXTURE_BEARER": "composition-token-bbbbbbbbbbbb"}
	registry := chatFixtureRegistry(t, tenant, endpoint, origin, "bearer", "env:CHAT_FIXTURE_BEARER", false)
	profile, err := registry.ResolveExecutionProfile(ctx, tenant, "chat-primary", chatFixtureRevision(t, registry, tenant))
	if err != nil {
		t.Fatalf("composition profile: %v", err)
	}

	// --- the executor, through the two PRODUCTION seams ---------------------------------
	// (1) wire.go:735 — construction under the explicit development activation only.
	exec := newModelsChatExecutor(modelGatewayChatDevelopmentPrecheck, registry, func(string) string { return "" }, discardLog())
	if exec == nil {
		t.Fatal("the production constructor built no Chat executor under the development activation")
	}
	if opts.inspector != nil {
		// Staged explicitly. In this AGPL build the configured constructor above yields no
		// inspector (wire_noenterprise.go), so a test that needs a non-nil one supplies it
		// and says so; the constructor half is asserted separately, against the production
		// constructor's own return value.
		exec.inspector = opts.inspector
	}
	policy := &stubProxyPolicy{policy: chatDefaultPolicy()}
	if opts.policy != nil {
		policy.policy = *opts.policy
	}
	lookups := 0
	resolver := secret.NewResolver(map[string]secret.Handler{
		secret.SchemeEnv: secret.EnvHandler{Lookup: func(k string) (string, bool) {
			lookups++
			v, ok := env[k]
			return v, ok && v != ""
		}},
	})
	// (2) boot.go:1949 — ONE late-bind of every dependency, before HTTP serving.
	if !exec.bind(chatExecutorDeps{
		Store: st, Policy: policy, ContextPolicy: &stubContextPolicy{},
		Secrets: resolver, HTTPClient: &http.Client{Transport: transport},
	}) {
		t.Fatal("the composition's Chat executor did not become ready")
	}

	// --- the module and the official API server ------------------------------------------
	m := models.New(
		models.WithExecutionProfileResolver(registry),
		models.WithChatExecutor(exec),
	)
	m.UseData(api.NewModuleData(st))
	setupToken := secure.NewSetupToken(filepath.Join(dir, "setup.token"))
	plaintext, _, err := setupToken.Ensure()
	if err != nil {
		t.Fatalf("setup token: %v", err)
	}
	srv, err := api.New(api.Options{
		Store: st, Authenticator: auth.NewAuthenticator(st, nil), Authorizer: auth.NewAuthorizer(nil),
		Signer: signer, SetupToken: setupToken, Version: "composition", Modules: []api.Module{m},
	})
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}

	c := &chatComposition{
		t: t, srv: srv, store: st, tenant: tenant, profile: profile, exec: exec,
		upstream: upstream, transport: transport, signer: signer, env: env, secretLookups: &lookups,
	}

	// --- a real operator, through the real setup and login ---------------------------------
	if r := c.do("POST", "/v1/setup", "", map[string]any{
		"token": plaintext, "email": "root@composition.test", "password": "supersecret1",
	}); r.code != http.StatusCreated {
		t.Fatalf("setup = %d %s", r.code, r.raw)
	}
	login := c.do("POST", "/v1/auth/login", "", map[string]any{
		"email": "root@composition.test", "password": "supersecret1",
	})
	if login.code != http.StatusOK {
		t.Fatalf("login = %d %s", login.code, login.raw)
	}
	token, _ := login.body["token"].(string)
	if token == "" {
		t.Fatalf("login returned no session token: %s", login.raw)
	}
	c.admin = token

	// The governed estate the routing decision resolves over.
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		p, err := sc.Providers().Create(ctx, model.Provider{Name: profile.ProviderRef, Kind: profile.ProviderRef, Status: model.StatusActive})
		if err != nil {
			return err
		}
		_, err = sc.Models().Create(ctx, model.Model{Name: profile.ModelRef, ProviderID: p.ID, Status: model.StatusActive})
		return err
	}); err != nil {
		t.Fatalf("seed governed model: %v", err)
	}

	created := c.do("POST", "/v1/m/models/routing-policies", c.admin, map[string]any{
		"name": "composition", "enabled": true, "strategy": "pinned",
		"pinned_model": profile.ModelRef, "execution_profile_ref": profile.Ref,
		"execution_profile_revision": profile.Revision,
	})
	if created.code != http.StatusCreated {
		t.Fatalf("create profiled policy = %d %s", created.code, created.raw)
	}
	c.policyID, _ = created.body["id"].(string)
	if c.policyID == "" {
		t.Fatalf("policy creation returned no id: %s", created.raw)
	}
	// The bootstrap must not have touched the gateway or the credential.
	if c.transport.count() != 0 || *c.secretLookups != 0 {
		t.Fatalf("bootstrap dispatched %d times and resolved %d secrets", c.transport.count(), *c.secretLookups)
	}
	return c
}

// chatCompositionResp is one response from the real handler: its status, its decoded body
// and its RAW bytes, because several assertions here are about the bytes.
type chatCompositionResp struct {
	code int
	body map[string]any
	raw  string
}

// errorCode reads the closed error code out of a failure body.
func (r chatCompositionResp) errorCode() string {
	errorObject, _ := r.body["error"].(map[string]any)
	code, _ := errorObject["code"].(string)
	return code
}

// do issues one request against the REAL handler, exactly as an operator's client does.
func (c *chatComposition) do(method, path, token string, body any) chatCompositionResp {
	c.t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			c.t.Fatalf("marshal body: %v", err)
		}
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.RemoteAddr = "10.0.0.1:1234"
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if token != "" || body != nil {
		req.Header.Set("X-Olivares-Tenant", c.tenant.String())
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	c.srv.Handler().ServeHTTP(rec, req)
	out := chatCompositionResp{code: rec.Code, raw: rec.Body.String()}
	_ = json.Unmarshal(rec.Body.Bytes(), &out.body)
	return out
}

// execute drives the official authenticated route.
func (c *chatComposition) execute(body map[string]any) chatCompositionResp {
	c.t.Helper()
	return c.do("POST", "/v1/m/models/routing-policies/"+c.policyID+"/execute", c.admin, body)
}

// records walks the CANONICAL ledger for one action. Reading through CanonicalWalker is
// deliberate: Walk nils Meta, so an assertion over the in-memory map would be vacuous.
func (c *chatComposition) records(action string) []chatLedgerRecord {
	c.t.Helper()
	var out []chatLedgerRecord
	if err := c.store.View(context.Background(), c.tenant, func(sc store.Scope) error {
		walker, ok := sc.Audit().(store.CanonicalWalker)
		if !ok {
			c.t.Fatal("this store does not expose the canonical audit walk; the metadata assertions would be vacuous")
		}
		return walker.WalkCanonical(context.Background(), 0, func(ev model.AuditEvent, metaCanonical string, _ []byte) error {
			if ev.Action != action {
				return nil
			}
			record := chatLedgerRecord{event: ev, raw: metaCanonical}
			if metaCanonical != "" {
				if err := json.Unmarshal([]byte(metaCanonical), &record.meta); err != nil {
					return err
				}
			}
			out = append(out, record)
			return nil
		})
	}); err != nil {
		c.t.Fatalf("walk composition ledger: %v", err)
	}
	return out
}

func (c *chatComposition) outcome() chatLedgerRecord {
	c.t.Helper()
	records := c.records(chatOutcomeAction)
	if len(records) != 1 {
		c.t.Fatalf("the ledger holds %d outcome records, want exactly 1", len(records))
	}
	return records[0]
}

// decodeMembers returns the response's top-level members as raw JSON, so a test can assert
// that a key EXISTS and is null — which a map lookup cannot distinguish from absence.
func decodeMembers(t *testing.T, r chatCompositionResp) map[string]json.RawMessage {
	t.Helper()
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal([]byte(r.raw), &decoded); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return decoded
}

func compositionExecution(t *testing.T, r chatCompositionResp) map[string]any {
	t.Helper()
	execution, _ := r.body["execution"].(map[string]any)
	if execution == nil {
		t.Fatalf("the response carries no execution block: %s", r.raw)
	}
	return execution
}

// --- the whole chain, once ---------------------------------------------------------------

// TestChatCompositionServesTheOfficialHandlerThroughTheConcreteExecutor is the positive
// causal the contract's §8.1 asks for and the review found missing: ONE authenticated
// request, ONE dispatch to the pinned endpoint over TLS with the resolved bearer and the
// exact prepared bytes, ONE canonical outcome, and the caller's own response body.
func TestChatCompositionServesTheOfficialHandlerThroughTheConcreteExecutor(t *testing.T) {
	c := newChatComposition(t, chatCompositionOptions{})
	// A distinctive answer, so "no content reached the ledger" is a real check, and a usage
	// object with a populated prompt-details object and an EMPTY completion-details one —
	// the pair R2 said the record could not tell apart.
	c.upstream.body = `{"object":"chat.completion","model":"` + chatFixtureModel +
		`","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"the composed answer"}}],` +
		`"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18,` +
		`"prompt_tokens_details":{"cached_tokens":0},"completion_tokens_details":{}}}`

	r := c.execute(map[string]any{"input": "the governed prompt", "session_ref": "attribution-only"})
	if r.code != http.StatusOK {
		t.Fatalf("execute = %d %s", r.code, r.raw)
	}

	// --- the dispatch: exactly one, to the configured path, with the resolved bearer -----
	calls := c.transport.calls()
	if len(calls) != 1 {
		t.Fatalf("the authenticated route dispatched %d times, want exactly 1", len(calls))
	}
	got := calls[0]
	want, _ := url.Parse(c.profile.Endpoint)
	if got.method != http.MethodPost || !strings.HasSuffix(got.url, want.Path) {
		t.Fatalf("dispatch = %s %s, want POST ...%s", got.method, got.url, want.Path)
	}
	if got.authorization != "Bearer "+c.env["CHAT_FIXTURE_BEARER"] {
		t.Fatal("the dispatch did not carry the credential resolved from the profile's reference")
	}
	if *c.secretLookups != 1 {
		t.Fatalf("the credential reference was resolved %d times, want exactly 1", *c.secretLookups)
	}
	prepared, perr := modelprovider.PrepareChatTextRequest(modelprovider.ChatTextRequest{
		Model: c.profile.ModelRef, Input: "the governed prompt", MaxCompletionTokens: 1024,
	})
	if perr != nil {
		t.Fatalf("recompute prepared: %v", perr)
	}
	if string(got.body) != string(prepared.Bytes()) {
		t.Fatalf("the bytes on the wire are not C0's prepared bytes\n got: %s\nwant: %s", got.body, prepared.Bytes())
	}
	if sha256.Sum256(got.body) != prepared.Digest() {
		t.Fatal("the dispatched body's digest is not the prepared digest the evidence bound")
	}

	// --- the response the caller receives ------------------------------------------------
	if r.body["output"] != "the composed answer" {
		t.Fatalf("output = %v", r.body["output"])
	}
	if r.body["fallback_used"] != false {
		t.Fatalf("fallback_used = %v; a Chat profile pins one target", r.body["fallback_used"])
	}
	if r.body["input_tokens"] != float64(11) || r.body["output_tokens"] != float64(7) {
		t.Fatalf("counters = %v/%v", r.body["input_tokens"], r.body["output_tokens"])
	}
	execution := compositionExecution(t, r)
	for key, expected := range map[string]any{
		"dispatch_state": models.ChatDispatchAttempted, "completion_state": models.ChatCompletionStop,
		"usage_status": models.ChatUsageReported, "intent_disposition": models.ChatIntentAnchored,
		"outcome_disposition": models.ChatOutcomeAnchored,
		"budget_assurance":    models.ChatBudgetAssuranceDevelopmentPrecheck,
		"profile_ref":         c.profile.Ref, "profile_revision": c.profile.Revision,
	} {
		if execution[key] != expected {
			t.Fatalf("execution.%s = %v, want %v", key, execution[key], expected)
		}
	}

	// --- the evidence the ledger keeps ----------------------------------------------------
	intents := c.records(chatIntentAction)
	if len(intents) != 1 {
		t.Fatalf("the ledger holds %d intent records, want exactly 1", len(intents))
	}
	record := c.outcome()
	if record.event.TargetID != model.ID(execution["attempt_ref"].(string)) {
		t.Fatalf("the outcome targets %s, not the attempt the caller was told about", record.event.TargetID)
	}
	// R2, on the record the composition actually wrote: the complete nullable usage,
	// including the empty completion-details object that the previous record could not
	// distinguish from an absent one.
	if v, ok := chatMetaCounter(t, record.meta, "prompt_cached_tokens"); !ok || v != 0 {
		t.Fatalf("prompt_cached_tokens = %d/%v, want an explicit 0", v, ok)
	}
	if !chatMetaBool(t, record.meta, "completion_tokens_details_present") {
		t.Fatal("a present-but-empty completion details object was recorded as absent")
	}
	if _, ok := chatMetaCounter(t, record.meta, "completion_reasoning_tokens"); ok {
		t.Fatal("a counter was invented for an empty details object")
	}
	if record.meta["observed_model"] != chatFixtureModel || chatMetaBool(t, record.meta, "model_mismatch") {
		t.Fatalf("observed model = %v / mismatch = %v", record.meta["observed_model"], record.meta["model_mismatch"])
	}
	// No prompt and no output text anywhere in the canonical metadata.
	if strings.Contains(record.raw, "the governed prompt") || strings.Contains(record.raw, "the composed answer") {
		t.Fatalf("content reached the canonical metadata: %s", record.raw)
	}
}

// TestChatCompositionErrorsKeepTheKnownResultOnTheWire is R1 through the official handler:
// the two failures that carry a real observation must publish it.
//
// The stub-executor test in modules/models proves the DTO; this proves the whole path fills
// it — that the counters the gateway reported survive the concrete executor, the module
// branch and the serializer, and reach the caller.
func TestChatCompositionErrorsKeepTheKnownResultOnTheWire(t *testing.T) {
	t.Run("output_withheld_keeps_partial_and_zero_counters", func(t *testing.T) {
		// A response-side DLP deny in buffer mode: the text is withheld, the counters are
		// not. output_tokens is an explicit ZERO, which must not read as "absent".
		pol := inferenceproxy.PolicyWithDLPRules(chatDefaultPolicy(),
			map[string]string{"secret.credential": "deny", "*": "allow", "unscanned": "allow"})
		pol.ResponseDLPMode = inferenceproxy.ResponseDLPBuffer
		c := newChatComposition(t, chatCompositionOptions{policy: &pol})
		c.upstream.body = `{"object":"chat.completion","model":"` + chatFixtureModel +
			`","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"AKIAIOSFODNN7EXAMPLE"}}],` +
			`"usage":{"prompt_tokens":11,"completion_tokens":0}}`

		r := c.execute(map[string]any{"input": "the governed prompt"})
		if r.code != http.StatusForbidden || r.errorCode() != models.ChatErrOutputWithheld {
			t.Fatalf("withheld = %d %s", r.code, r.raw)
		}
		if strings.Contains(r.raw, "AKIAIOSFODNN7EXAMPLE") {
			t.Fatalf("the withheld text reached the caller: %s", r.raw)
		}
		decoded := decodeMembers(t, r)
		for _, key := range []string{"error", "expected_target", "fallback_used", "output", "input_tokens", "output_tokens", "refusal", "execution"} {
			if _, ok := decoded[key]; !ok {
				t.Fatalf("the 403 body has no %q: %s", key, r.raw)
			}
		}
		if string(decoded["output"]) != "null" {
			t.Fatalf("output = %s, want an explicit null", decoded["output"])
		}
		if string(decoded["input_tokens"]) != "11" || string(decoded["output_tokens"]) != "0" {
			t.Fatalf("counters = %s/%s; a withheld response does not forget what was consumed",
				decoded["input_tokens"], decoded["output_tokens"])
		}
		if string(decoded["fallback_used"]) != "false" {
			t.Fatalf("fallback_used = %s", decoded["fallback_used"])
		}
		var expected map[string]any
		if err := json.Unmarshal(decoded["expected_target"], &expected); err != nil {
			t.Fatalf("decode expected_target: %v", err)
		}
		if expected["provider_ref"] != c.profile.ProviderRef || expected["model_ref"] != c.profile.ModelRef {
			t.Fatalf("expected_target = %v", expected)
		}
		if _, present := decoded["served"]; present {
			t.Fatalf("a withheld response claimed a served target: %s", r.raw)
		}
		execution := compositionExecution(t, r)
		if execution["completion_state"] != models.ChatCompletionOutputWithheld ||
			execution["dispatch_state"] != models.ChatDispatchAttempted ||
			execution["usage_status"] != models.ChatUsageReported {
			t.Fatalf("execution = %v", execution)
		}
		// And the same observation is in the ledger, once.
		record := c.outcome()
		if v, ok := chatMetaCounter(t, record.meta, "output_tokens"); !ok || v != 0 {
			t.Fatalf("the recorded output_tokens = %d/%v, want an explicit 0", v, ok)
		}
		if record.meta["completion_state"] != models.ChatCompletionOutputWithheld {
			t.Fatalf("recorded completion_state = %v", record.meta["completion_state"])
		}
	})

	t.Run("model_mismatch_keeps_its_observation", func(t *testing.T) {
		c := newChatComposition(t, chatCompositionOptions{})
		c.upstream.body = chatCompletionBody("a-different-model", "text from the wrong model")

		r := c.execute(map[string]any{"input": "the governed prompt"})
		if r.code != http.StatusBadGateway || r.errorCode() != models.ChatErrProtocolError {
			t.Fatalf("mismatch = %d %s", r.code, r.raw)
		}
		if strings.Contains(r.raw, "text from the wrong model") {
			t.Fatalf("a mismatched model's text reached the caller: %s", r.raw)
		}
		decoded := decodeMembers(t, r)
		if string(decoded["output"]) != "null" {
			t.Fatalf("output = %s", decoded["output"])
		}
		if string(decoded["input_tokens"]) != "11" || string(decoded["output_tokens"]) != "7" {
			t.Fatalf("counters = %s/%s; a mismatch does not erase what the gateway reported",
				decoded["input_tokens"], decoded["output_tokens"])
		}
		// The wire says nothing about WHICH model answered — that is an internal
		// observation. The ledger keeps it, which is where a reconciliation reads it.
		if strings.Contains(r.raw, "a-different-model") {
			t.Fatalf("the observed model was published to the caller: %s", r.raw)
		}
		record := c.outcome()
		if record.meta["observed_model"] != "a-different-model" || !chatMetaBool(t, record.meta, "model_mismatch") {
			t.Fatalf("the record cannot be reconciled: observed=%v mismatch=%v",
				record.meta["observed_model"], record.meta["model_mismatch"])
		}
	})
}

// TestChatCompositionPreDispatchDenyReachesNoSecretAndNoGateway is the negative the review
// asked to pair with the positive: a gate this composition owns denies BEFORE the credential
// and before the gateway, and both absences are measured, not assumed.
//
// It also fixes the reading of a 4xx that HAS an attempt ref: refs are minted by C0
// preparation, which precedes the content gates, so the body reports an attempt that was
// never dispatched — not_attempted / not_sent / unknown, with null counters. A caller must
// be able to tell that from the withholding above, where the same shape carries 11/0.
func TestChatCompositionPreDispatchDenyReachesNoSecretAndNoGateway(t *testing.T) {
	pol := inferenceproxy.PolicyWithDLPRules(chatDefaultPolicy(),
		map[string]string{"secret.credential": "deny", "*": "allow", "unscanned": "allow"})
	c := newChatComposition(t, chatCompositionOptions{policy: &pol})

	r := c.execute(map[string]any{"input": "please use AKIAIOSFODNN7EXAMPLE for this"})
	if r.code != http.StatusForbidden || r.errorCode() != models.ChatErrContentDenied {
		t.Fatalf("input deny = %d %s", r.code, r.raw)
	}
	if c.transport.count() != 0 {
		t.Fatalf("a denied request POSTed to the gateway %d times", c.transport.count())
	}
	if *c.secretLookups != 0 {
		t.Fatalf("a denied request resolved the credential %d times", *c.secretLookups)
	}
	if c.upstream.hits != 0 {
		t.Fatalf("the gateway was reached %d times", c.upstream.hits)
	}
	// Denied before the intent anchor, so there is no evidence of an authorized effect.
	if got := len(c.records(chatIntentAction)) + len(c.records(chatOutcomeAction)); got != 0 {
		t.Fatalf("a pre-intent denial wrote %d effect records", got)
	}
	decoded := decodeMembers(t, r)
	if string(decoded["input_tokens"]) != "null" || string(decoded["output_tokens"]) != "null" {
		t.Fatalf("counters = %s/%s; nothing was dispatched, so nothing is known",
			decoded["input_tokens"], decoded["output_tokens"])
	}
	if string(decoded["output"]) != "null" {
		t.Fatalf("output = %s", decoded["output"])
	}
	execution := compositionExecution(t, r)
	if execution["dispatch_state"] != models.ChatDispatchNotAttempted ||
		execution["completion_state"] != models.ChatCompletionNotSent ||
		execution["usage_status"] != models.ChatUsageUnknown {
		t.Fatalf("a request that never left the process reported %v", execution)
	}
	if execution["intent_disposition"] != models.ChatIntentRefused {
		t.Fatalf("intent_disposition = %v; no intent was anchored", execution["intent_disposition"])
	}
	// The caller learns WHICH gate refused only through the closed code and its fixed
	// message: no detector, no matched value, no endpoint.
	message, _ := r.body["error"].(map[string]any)["message"].(string)
	if message != "request blocked by data-loss-prevention policy" {
		t.Fatalf("message = %q", message)
	}
}

// TestChatCompositionRefusesWithoutTheExplicitActivation: the same composition, built with
// the default mode, serves the C2A refusal — the activation is what wires dispatch, and the
// official route is where that has to be true.
func TestChatCompositionRefusesWithoutTheExplicitActivation(t *testing.T) {
	c := newChatComposition(t, chatCompositionOptions{})
	// Rebuild the module set exactly as wire.go would with the DISABLED default: the
	// constructor returns nil, so no option is appended and the module keeps its
	// deny-closed port.
	if x := newModelsChatExecutor(modelGatewayChatDisabled, c.exec.registry, func(string) string { return "" }, discardLog()); x != nil {
		t.Fatal("the disabled default built an executor")
	}
	m := models.New(models.WithExecutionProfileResolver(c.exec.registry))
	m.UseData(api.NewModuleData(c.store))
	srv, err := api.New(api.Options{
		Store: c.store, Authenticator: auth.NewAuthenticator(c.store, nil), Authorizer: auth.NewAuthorizer(nil),
		Signer: c.signer, SetupToken: secure.NewSetupToken(filepath.Join(t.TempDir(), "setup.token")),
		Version: "composition-disabled", Modules: []api.Module{m},
	})
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	previous := c.srv
	c.srv = srv
	t.Cleanup(func() { c.srv = previous })

	r := c.execute(map[string]any{"input": "hello"})
	if r.code != http.StatusServiceUnavailable || r.errorCode() != models.ChatErrExecutionUnavailable {
		t.Fatalf("disabled execute = %d %s", r.code, r.raw)
	}
	if c.transport.count() != 0 || *c.secretLookups != 0 {
		t.Fatalf("the refusal dispatched %d times and resolved %d secrets", c.transport.count(), *c.secretLookups)
	}
}

// --- the Chat-only inspector, built the way production builds it ----------------------------

// TestChatBuildsItsInspectorThroughTheConfiguredConstructor is the construction half of R3's
// second finding. The old test claimed a "Chat-only boot" while injecting a stub into a
// fixture; this asserts the actual wiring instead.
//
// ⛔ WHAT IT PROVES, EXACTLY: the executor's inspector IS the value the production
// constructor returns for the same environment — not a test double, not a borrowed Messages
// inspector, not nothing-because-nobody-looked. In THIS build (default AGPL) that value is
// nil: newContentInspector is build-tag gated and the commercial firewall links only under
// `-tags enterprise` (wire_noenterprise.go). So a NON-NIL configured inspector cannot be
// qualified from this build at all, and the report says so rather than staging a stub and
// calling it a boot. The USE half — that whatever sits in that field is consulted in both
// directions with the exact prepared input — is
// TestChatInspectsBothDirectionsThroughItsOwnInspector, and the two together are the chain.
func TestChatBuildsItsInspectorThroughTheConfiguredConstructor(t *testing.T) {
	getenv := func(string) string { return "" }
	log := discardLog()
	registry := &modelGatewayProfileRegistry{profiles: map[modelGatewayProfileKey]modelGatewayProfileConfig{}}

	x := newModelsChatExecutor(modelGatewayChatDevelopmentPrecheck, registry, getenv, log)
	if x == nil {
		t.Fatal("the development activation built no executor")
	}
	configured := newContentInspector(getenv, log)
	if x.inspector != configured {
		t.Fatalf("the executor's inspector (%T) is not what the configured constructor returns (%T)", x.inspector, configured)
	}
	// The build's honest state, asserted rather than assumed, so this test starts failing
	// the day the default build gains a configured inspector and the limitation the report
	// records stops being true.
	if configured != nil {
		t.Fatalf("this build's configured constructor returned %T; the report's limitation needs re-stating", configured)
	}
}

// TestChatOnlyCompositionInspectsWithNoMessagesSurface: the executor built and bound by the
// production seams runs BOTH content directions through its own inspection service, in a
// composition that serves no Messages surface at all.
//
// The inspector is staged (see the constructor test above for why that is unavoidable in
// this build) and it is the executor's OWN field — the same field the configured constructor
// fills — reached through the official authenticated route rather than a direct call.
func TestChatOnlyCompositionInspectsWithNoMessagesSurface(t *testing.T) {
	inspector := &stubInspector{
		request:  claudeapi.ContentInspectionDecision{Forward: true, Meter: claudeapi.ContentInspectionMeter{Inspections: 1, Channels: 1}},
		response: claudeapi.ContentInspectionDecision{Forward: true, Meter: claudeapi.ContentInspectionMeter{Inspections: 1, Channels: 1}},
	}
	c := newChatComposition(t, chatCompositionOptions{inspector: inspector})

	// This composition mounts the models module and nothing else: there is no inference
	// proxy module, and the Messages PEP is a SEPARATE listener this process never built.
	for _, path := range []string{"/v1/messages", "/v1/m/inference-proxy/config"} {
		if r := c.do("GET", path, c.admin, nil); r.code != http.StatusNotFound {
			t.Fatalf("%s = %d; this composition must serve no Messages surface", path, r.code)
		}
	}

	r := c.execute(map[string]any{"input": "the governed prompt"})
	if r.code != http.StatusOK {
		t.Fatalf("execute = %d %s", r.code, r.raw)
	}
	if len(inspector.seen) != 2 {
		t.Fatalf("the configured inspector saw %d directions, want request and response", len(inspector.seen))
	}
	if inspector.seen[0].Direction != claudeapi.InspectDirectionRequest ||
		inspector.seen[1].Direction != claudeapi.InspectDirectionResponse {
		t.Fatalf("directions = %s, %s", inspector.seen[0].Direction, inspector.seen[1].Direction)
	}
	if inspector.seen[0].Model != c.profile.ModelRef {
		t.Fatalf("the inspector was told model %q", inspector.seen[0].Model)
	}
	if len(inspector.seen[0].Channels) == 0 || inspector.seen[0].Channels[0].Text != "the governed prompt" {
		t.Fatal("the inspector saw content other than the exact input C0 prepared")
	}
}
