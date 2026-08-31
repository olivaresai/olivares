// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	claudewif "github.com/olivaresai/olivares/connectors/claude-wif"
	"github.com/olivaresai/olivares/core/model"
	executor "github.com/olivaresai/olivares/core/runtime/executor"
	"github.com/olivaresai/olivares/modules/sessions"
)

// --- test doubles ----------------------------------------------------------------

const (
	testAssertion = "eyJ.test.svid"
	testOrgUUID   = "11111111-1111-1111-1111-111111111111"
	oatBody       = `{"access_token":"sk-ant-oat01-secret","token_type":"Bearer","expires_in":3600,"scope":"workspace:inference"}`
)

type fakeResolver struct {
	byTenant map[model.TenantID][]claudewif.ExchangeParams
}

func (f fakeResolver) FederationExchangeParams(t model.TenantID) ([]claudewif.ExchangeParams, bool) {
	p, ok := f.byTenant[t]
	return p, ok
}

type fakeAssert struct {
	mu    sync.Mutex
	tok   string
	err   error
	calls int
}

func (f *fakeAssert) mint(_ context.Context, _ string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return "", f.err
	}
	return f.tok, nil
}

func (f *fakeAssert) count() int { f.mu.Lock(); defer f.mu.Unlock(); return f.calls }

// countingDoer is the Exchanger transport: it counts calls, captures request bodies and
// returns a scripted response (mirrors connectors/claude-wif's exchangeDoer).
type countingDoer struct {
	mu     sync.Mutex
	status int
	body   string
	header http.Header
	calls  int
	bodies [][]byte
}

func (d *countingDoer) Do(req *http.Request) (*http.Response, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls++
	var b []byte
	if req.Body != nil {
		b, _ = io.ReadAll(req.Body)
	}
	d.bodies = append(d.bodies, b)
	h := d.header
	if h == nil {
		h = make(http.Header)
	}
	return &http.Response{StatusCode: d.status, Body: io.NopCloser(strings.NewReader(d.body)), Header: h}, nil
}

func (d *countingDoer) count() int { d.mu.Lock(); defer d.mu.Unlock(); return d.calls }

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time          { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *fakeClock) advance(d time.Duration) { c.mu.Lock(); defer c.mu.Unlock(); c.now = c.now.Add(d) }

func wifTenant(t *testing.T, s string) model.TenantID {
	t.Helper()
	tid, err := model.ParseTenantID(s)
	if err != nil {
		t.Fatalf("parse tenant %q: %v", s, err)
	}
	return tid
}

func paramsFor(rule, sa, ws string) claudewif.ExchangeParams {
	return claudewif.ExchangeParams{FederationRuleID: rule, OrganizationID: testOrgUUID, ServiceAccountID: sa, WorkspaceID: ws}
}

func newTestBroker(resolve federationResolver, assert assertionMinter, doer *countingDoer, now func() time.Time, log *slog.Logger) *wifCredentialBroker {
	return &wifCredentialBroker{
		exch:    claudewif.NewExchanger("https://api.test", doer),
		assert:  assert,
		resolve: resolve,
		log:     log,
		now:     now,
		slack:   defaultWIFRefreshSlack,
		entries: map[wifCacheKey]*wifCacheEntry{},
	}
}

// capturedReq decodes the exchange request body (claudewif's exchangeRequest is unexported).
type capturedReq struct {
	GrantType        string `json:"grant_type"`
	Assertion        string `json:"assertion"`
	FederationRuleID string `json:"federation_rule_id"`
	OrganizationID   string `json:"organization_id"`
	ServiceAccountID string `json:"service_account_id"`
	WorkspaceID      string `json:"workspace_id"`
}

// --- tests -----------------------------------------------------------------------

func TestBrokerMintSuccessAndCacheHit(t *testing.T) {
	tenantA := wifTenant(t, testOrgUUID)
	clock := &fakeClock{now: time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)}
	doer := &countingDoer{status: 200, body: oatBody}
	assert := &fakeAssert{tok: testAssertion}
	resolve := fakeResolver{byTenant: map[model.TenantID][]claudewif.ExchangeParams{tenantA: {paramsFor("fdrl_a", "svac_a", "wrkspc_a")}}}
	b := newTestBroker(resolve, assert, doer, clock.Now, nil)

	c1, err := b.mint(context.Background(), tenantA, "")
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if c1.token != "sk-ant-oat01-secret" || c1.scheme != wifScheme {
		t.Errorf("cred = %+v", c1)
	}
	if want := clock.now.Add(time.Hour); !c1.notAfter.Equal(want) {
		t.Errorf("notAfter = %v, want %v (broker clock + expires_in)", c1.notAfter, want)
	}
	if c1.id == "" || strings.Contains(c1.id, c1.token) {
		t.Errorf("credential id must be non-secret and not contain the token: %q", c1.id)
	}

	// The exchange request carried the resolved per-tenant ids + the assertion.
	var req capturedReq
	if err := json.Unmarshal(doer.bodies[0], &req); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	if req.Assertion != testAssertion || req.FederationRuleID != "fdrl_a" ||
		req.ServiceAccountID != "svac_a" || req.WorkspaceID != "wrkspc_a" || req.OrganizationID != testOrgUUID {
		t.Errorf("request = %+v", req)
	}

	// A second mint within the freshness window returns the cached token — no new exchange.
	c2, err := b.mint(context.Background(), tenantA, "")
	if err != nil {
		t.Fatalf("mint2: %v", err)
	}
	if c2.token != c1.token {
		t.Errorf("expected cached token, got a different one")
	}
	if doer.count() != 1 {
		t.Errorf("exchange calls = %d, want 1 (cache hit)", doer.count())
	}
	if assert.count() != 1 {
		t.Errorf("assertion fetches = %d, want 1 (cache hit)", assert.count())
	}
}

func TestBrokerReexchangeBeforeExpiry(t *testing.T) {
	tenantA := wifTenant(t, testOrgUUID)
	clock := &fakeClock{now: time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)}
	doer := &countingDoer{status: 200, body: oatBody} // 3600s lifetime, default slack 60s
	b := newTestBroker(fakeResolver{byTenant: map[model.TenantID][]claudewif.ExchangeParams{tenantA: {paramsFor("fdrl_a", "svac_a", "")}}},
		&fakeAssert{tok: testAssertion}, doer, clock.Now, nil)

	if _, err := b.mint(context.Background(), tenantA, ""); err != nil {
		t.Fatalf("initial mint: %v", err)
	}
	// Still comfortably before the slack window: cached.
	clock.advance(time.Hour - 2*time.Minute) // 120s remaining > 60s slack
	if _, err := b.mint(context.Background(), tenantA, ""); err != nil {
		t.Fatalf("mint within freshness: %v", err)
	}
	if doer.count() != 1 {
		t.Fatalf("expected cache hit, exchange calls = %d", doer.count())
	}
	// Inside the slack window: re-exchange.
	clock.advance(90 * time.Second) // now 30s remaining < 60s slack
	if _, err := b.mint(context.Background(), tenantA, ""); err != nil {
		t.Fatalf("re-exchange mint: %v", err)
	}
	if doer.count() != 2 {
		t.Errorf("expected re-exchange before NotAfter, exchange calls = %d, want 2", doer.count())
	}
}

func TestBrokerDenyOnMissingAssertion(t *testing.T) {
	tenantA := wifTenant(t, testOrgUUID)
	doer := &countingDoer{status: 200, body: oatBody}
	b := newTestBroker(fakeResolver{byTenant: map[model.TenantID][]claudewif.ExchangeParams{tenantA: {paramsFor("fdrl_a", "svac_a", "")}}},
		&fakeAssert{err: errors.New("no SPIRE socket")}, doer, time.Now, nil)

	if _, err := b.mint(context.Background(), tenantA, ""); err == nil {
		t.Fatal("expected deny-closed when the assertion is unavailable")
	}
	if doer.count() != 0 {
		t.Errorf("must not exchange without an assertion; calls = %d", doer.count())
	}
}

func TestBrokerDenyOnExchange4xxNotCached(t *testing.T) {
	tenantA := wifTenant(t, testOrgUUID)
	h := make(http.Header)
	h.Set("request-id", "req_xyz")
	doer := &countingDoer{status: 400, body: `{"error":"invalid_grant"}`, header: h}
	b := newTestBroker(fakeResolver{byTenant: map[model.TenantID][]claudewif.ExchangeParams{tenantA: {paramsFor("fdrl_a", "svac_a", "")}}},
		&fakeAssert{tok: testAssertion}, doer, time.Now, nil)

	_, err := b.mint(context.Background(), tenantA, "")
	if err == nil {
		t.Fatal("expected deny-closed on a 4xx exchange")
	}
	// A failed exchange must NOT be cached — the next mint retries.
	if _, err2 := b.mint(context.Background(), tenantA, ""); err2 == nil {
		t.Fatal("expected deny again")
	}
	if doer.count() != 2 {
		t.Errorf("a failed exchange must not be cached; exchange calls = %d, want 2", doer.count())
	}
}

func TestBrokerDenyOnNonPositiveLifetime(t *testing.T) {
	tenantA := wifTenant(t, testOrgUUID)
	doer := &countingDoer{status: 200, body: `{"access_token":"sk-ant-oat01-x","token_type":"Bearer","expires_in":0}`}
	b := newTestBroker(fakeResolver{byTenant: map[model.TenantID][]claudewif.ExchangeParams{tenantA: {paramsFor("fdrl_a", "svac_a", "")}}},
		&fakeAssert{tok: testAssertion}, doer, time.Now, nil)

	if _, err := b.mint(context.Background(), tenantA, ""); err == nil {
		t.Fatal("expected deny-closed on a non-positive token lifetime")
	}
}

func TestBrokerPerTenantAndRuleSelection(t *testing.T) {
	tenantA := wifTenant(t, testOrgUUID)
	tenantB := wifTenant(t, "22222222-2222-2222-2222-222222222222")
	tenantMulti := wifTenant(t, "33333333-3333-3333-3333-333333333333")
	tenantNone := wifTenant(t, "44444444-4444-4444-4444-444444444444")

	resolve := fakeResolver{byTenant: map[model.TenantID][]claudewif.ExchangeParams{
		tenantA:     {paramsFor("fdrl_a", "svac_a", "")},
		tenantB:     {paramsFor("fdrl_b", "svac_b", "")},
		tenantMulti: {paramsFor("fdrl_m1", "svac_m1", ""), paramsFor("fdrl_m2", "svac_m2", "")},
		// tenantNone: deliberately absent from the map.
	}}

	newB := func() (*wifCredentialBroker, *countingDoer) {
		doer := &countingDoer{status: 200, body: oatBody}
		return newTestBroker(resolve, &fakeAssert{tok: testAssertion}, doer, time.Now, nil), doer
	}

	// Tenant A and B each resolve their own rule.
	b, doer := newB()
	if _, err := b.mint(context.Background(), tenantB, ""); err != nil {
		t.Fatalf("tenant B mint: %v", err)
	}
	var req capturedReq
	_ = json.Unmarshal(doer.bodies[0], &req)
	if req.FederationRuleID != "fdrl_b" || req.ServiceAccountID != "svac_b" {
		t.Errorf("tenant B exchanged with %+v, want fdrl_b/svac_b", req)
	}

	// Unknown tenant => deny-closed, no exchange.
	b, doer = newB()
	if _, err := b.mint(context.Background(), tenantNone, ""); err == nil {
		t.Error("tenant with no federation rule must deny-closed")
	}
	if doer.count() != 0 {
		t.Errorf("no exchange for an unresolved tenant; calls = %d", doer.count())
	}

	// Ambiguous (>1 rule) without a hint => deny.
	b, _ = newB()
	if _, err := b.mint(context.Background(), tenantMulti, ""); err == nil {
		t.Error("ambiguous multi-rule tenant without a hint must deny-closed")
	}
	// With a matching hint => the named rule is used.
	b, doer = newB()
	if _, err := b.mint(context.Background(), tenantMulti, "fdrl_m2"); err != nil {
		t.Fatalf("multi-rule with hint: %v", err)
	}
	_ = json.Unmarshal(doer.bodies[0], &req)
	if req.FederationRuleID != "fdrl_m2" {
		t.Errorf("hinted rule = %q, want fdrl_m2", req.FederationRuleID)
	}
	// With a non-matching hint => deny.
	b, _ = newB()
	if _, err := b.mint(context.Background(), tenantMulti, "fdrl_nope"); err == nil {
		t.Error("a hint not declared for the tenant must deny-closed")
	}
}

func TestBrokerNeverLogsTheSecretToken(t *testing.T) {
	tenantA := wifTenant(t, testOrgUUID)
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	doer := &countingDoer{status: 200, body: oatBody}
	b := newTestBroker(fakeResolver{byTenant: map[model.TenantID][]claudewif.ExchangeParams{tenantA: {paramsFor("fdrl_a", "svac_a", "wrkspc_a")}}},
		&fakeAssert{tok: testAssertion}, doer, time.Now, logger)

	if _, err := b.mint(context.Background(), tenantA, ""); err != nil {
		t.Fatalf("mint: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, "sk-ant-oat01-secret") {
		t.Errorf("the minted token must NEVER be logged; log = %q", out)
	}
	if strings.Contains(out, testAssertion) {
		t.Errorf("the assertion must NEVER be logged; log = %q", out)
	}
	// The non-secret audit provenance IS logged (operability).
	if !strings.Contains(out, "fdrl_a") || !strings.Contains(out, "svac_a") {
		t.Errorf("expected non-secret audit fields in the log; log = %q", out)
	}
}

func TestBrokerConcurrentMintIsSingleFlight(t *testing.T) {
	tenantA := wifTenant(t, testOrgUUID)
	doer := &countingDoer{status: 200, body: oatBody}
	b := newTestBroker(fakeResolver{byTenant: map[model.TenantID][]claudewif.ExchangeParams{tenantA: {paramsFor("fdrl_a", "svac_a", "")}}},
		&fakeAssert{tok: testAssertion}, doer, time.Now, nil)

	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := b.mint(context.Background(), tenantA, ""); err != nil {
				t.Errorf("concurrent mint: %v", err)
			}
		}()
	}
	wg.Wait()
	if doer.count() != 1 {
		t.Errorf("per-key single-flight: exchange calls = %d, want 1", doer.count())
	}
}

func TestBrokerAdaptersMapAndPropagateDeny(t *testing.T) {
	tenantA := wifTenant(t, testOrgUUID)
	resolve := fakeResolver{byTenant: map[model.TenantID][]claudewif.ExchangeParams{tenantA: {paramsFor("fdrl_a", "svac_a", "")}}}

	// Session adapter mints under the request's tenant and maps fields.
	doer := &countingDoer{status: 200, body: oatBody}
	b := newTestBroker(resolve, &fakeAssert{tok: testAssertion}, doer, time.Now, nil)
	sc, err := b.sessionSource("").Mint(context.Background(), sessions.CredentialRequest{Tenant: tenantA, RunRef: "run-1"})
	if err != nil {
		t.Fatalf("session mint: %v", err)
	}
	if sc.Token != "sk-ant-oat01-secret" || sc.Scheme != wifScheme || sc.NotAfter.IsZero() {
		t.Errorf("session credential = %+v", sc)
	}

	// Executor adapter mints under the configured tenant.
	doer2 := &countingDoer{status: 200, body: oatBody}
	b2 := newTestBroker(resolve, &fakeAssert{tok: testAssertion}, doer2, time.Now, nil)
	ec, err := b2.executorSource(tenantA, "").Mint(context.Background(), executor.MintRequest{Environment: "deploy", Runtime: "tofu", Mode: executor.ModeWrite})
	if err != nil {
		t.Fatalf("executor mint: %v", err)
	}
	if ec.Token != "sk-ant-oat01-secret" || ec.Scheme != wifScheme {
		t.Errorf("executor credential = %+v", ec)
	}

	// Deny propagates through both adapters (unknown tenant).
	tenantNone := wifTenant(t, "55555555-5555-5555-5555-555555555555")
	if _, err := b.sessionSource("").Mint(context.Background(), sessions.CredentialRequest{Tenant: tenantNone}); err == nil {
		t.Error("session adapter must propagate deny-closed")
	}
	if _, err := b2.executorSource(tenantNone, "").Mint(context.Background(), executor.MintRequest{}); err == nil {
		t.Error("executor adapter must propagate deny-closed")
	}
}

func TestSessionWIFEnabled(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{{"1", true}, {"true", true}, {"TRUE", true}, {" 1 ", true}, {"", false}, {"0", false}, {"no", false}} {
		if got := sessionWIFEnabled(tc.in); got != tc.want {
			t.Errorf("sessionWIFEnabled(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestExecutorCredentialSourceWith(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	doer := &countingDoer{status: 200, body: oatBody}
	tenantA := wifTenant(t, testOrgUUID)
	broker := newTestBroker(fakeResolver{byTenant: map[model.TenantID][]claudewif.ExchangeParams{tenantA: {paramsFor("fdrl_a", "svac_a", "")}}},
		&fakeAssert{tok: testAssertion}, doer, time.Now, nil)

	isDeny := func(s executor.CredentialSource) bool { _, ok := s.(executor.DenyCredentialSource); return ok }

	// kind=wif with no broker => deny-closed.
	if !isDeny(credentialCfgJSON{Kind: "wif", Tenant: testOrgUUID}.sourceWith(nil, log)) {
		t.Error("kind=wif with nil broker must be deny-closed")
	}
	// kind=wif with an invalid/empty tenant => deny-closed.
	if !isDeny(credentialCfgJSON{Kind: "wif", Tenant: "not-a-uuid"}.sourceWith(broker, log)) {
		t.Error("kind=wif with an invalid tenant must be deny-closed")
	}
	if !isDeny(credentialCfgJSON{Kind: "wif"}.sourceWith(broker, log)) {
		t.Error("kind=wif with no tenant must be deny-closed")
	}
	// kind=wif with a valid tenant => the broker source (not deny).
	if isDeny(credentialCfgJSON{Kind: "wif", Tenant: testOrgUUID}.sourceWith(broker, log)) {
		t.Error("kind=wif with a valid tenant must wire the broker source")
	}
	// kind=file with no path => deny-closed (DenyCredentialSource).
	if !isDeny(credentialCfgJSON{Kind: "file"}.sourceWith(broker, log)) {
		t.Error("kind=file with no path must be deny-closed")
	}
}
