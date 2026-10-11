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
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sessions"
)

// G1-B — the capability endpoint measured over a REAL HTTP transport.
//
// ⛔ WHY THIS FILE EXISTS AND WHY THE EXISTING BATTERY COULD NOT REPLACE IT. Every other
// capability case reaches the router through communicationHTTPTestRequest, and that
// helper wraps each request in a two-minute context deadline before calling
// Handler().ServeHTTP. NewHTTPServer adds nothing of the sort: net/http derives a
// request context with context.WithCancel, and ReadTimeout/WriteTimeout are connection
// limits that never become ctx.Deadline(). The typed evidence producers require a finite
// horizon before they will read the transaction clock and the authorization epoch, so on
// the real transport every question answered unknown/evidence_unavailable while the real
// administration route served 200 for the same caller. The helper was creating the
// precondition it was supposed to be validating.
//
// So the fixtures below run the production router on a real loopback listener and, for
// the primary case, supply NO deadline of their own — the server is the only thing that
// may install one. What they add instead is an observation of what the product received,
// so "the transport supplied nothing" is measured rather than assumed.

// capabilityLiveResponse is one answer read off the wire.
type capabilityLiveResponse struct {
	status  int
	header  http.Header
	raw     []byte
	elapsed time.Duration
}

// capabilityCallerContext shapes the context the PRODUCT receives, standing in for an
// earlier caller deadline or a disconnect. A nil shaper is the production case: the
// request arrives exactly as net/http built it.
type capabilityCallerContext func(context.Context) (context.Context, context.CancelFunc)

// capabilityLiveTransport is a real loopback HTTP server over the production router.
type capabilityLiveTransport struct {
	server *httptest.Server
	client *http.Client
	tenant model.TenantID
	// served counts capability requests the product handler received; deadlines
	// counts how many of them arrived carrying a context deadline from the
	// transport. The second counter is what makes the "no helper deadline" claim
	// falsifiable instead of a comment.
	served    atomic.Int64
	deadlines atomic.Int64
}

func newCapabilityLiveTransport(
	t *testing.T, eng *engine, tenant model.TenantID, shape capabilityCallerContext,
) *capabilityLiveTransport {
	t.Helper()
	live := &capabilityLiveTransport{tenant: tenant}
	router := eng.api.Handler()
	live.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/auth/capabilities" {
			live.served.Add(1)
			// Read BEFORE any shaping: this is what the transport itself delivered.
			if _, carried := r.Context().Deadline(); carried {
				live.deadlines.Add(1)
			}
		}
		if shape != nil {
			ctx, cancel := shape(r.Context())
			defer cancel()
			r = r.WithContext(ctx)
		}
		router.ServeHTTP(w, r)
	}))
	t.Cleanup(live.server.Close)
	client := live.server.Client()
	// A finite, owned client bound so a hung seam fails as a test rather than as a
	// stuck run. It is a CLIENT timeout: it is not transmitted and never becomes the
	// server's ctx.Deadline().
	client.Timeout = 60 * time.Second
	live.client = client
	return live
}

func (live *capabilityLiveTransport) do(
	t *testing.T, method, path, token string, body any,
) capabilityLiveResponse {
	t.Helper()
	var payload []byte
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal %s %s: %v", method, path, err)
		}
		payload = encoded
	}
	// http.NewRequest and NOT NewRequestWithContext: a context here would be exactly
	// the masking this file exists to remove.
	request, err := http.NewRequest(method, live.server.URL+path, bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("build %s %s: %v", method, path, err)
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if !live.tenant.IsZero() {
		request.Header.Set("X-Olivares-Tenant", live.tenant.String())
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	started := time.Now()
	response, err := live.client.Do(request)
	if err != nil {
		t.Fatalf("live %s %s: %v", method, path, err)
	}
	defer response.Body.Close() //nolint:errcheck
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read live %s %s body: %v", method, path, err)
	}
	return capabilityLiveResponse{
		status: response.StatusCode, header: response.Header.Clone(),
		raw: raw, elapsed: time.Since(started),
	}
}

// askRaw posts one batch and returns the transport answer untouched, so a case can
// assert a refusal envelope instead of results.
func (live *capabilityLiveTransport) askRaw(
	t *testing.T, token string, questions ...map[string]any,
) capabilityLiveResponse {
	t.Helper()
	return live.do(t, http.MethodPost, "/v1/auth/capabilities", token,
		map[string]any{"schema_version": 2, "questions": questions})
}

// ask drives one batch over the wire and returns the answers by id, asserting the two
// transport promises the endpoint owes every well-formed batch.
func (live *capabilityLiveTransport) ask(
	t *testing.T, token string, questions ...map[string]any,
) map[string]capabilityAnswer {
	t.Helper()
	response := live.askRaw(t, token, questions...)
	if response.status != http.StatusOK {
		t.Fatalf("live capabilities = %d: %s", response.status, response.raw)
	}
	if got := response.header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("live capabilities Cache-Control = %q, want no-store", got)
	}
	var decoded capabilityResults
	if err := json.Unmarshal(response.raw, &decoded); err != nil {
		t.Fatalf("decode live capabilities: %v (%s)", err, response.raw)
	}
	if decoded.SchemaVersion != 2 || len(decoded.Results) != len(questions) {
		t.Fatalf("live capabilities envelope = schema %d with %d results, want schema 2 and %d",
			decoded.SchemaVersion, len(decoded.Results), len(questions))
	}
	answers := make(map[string]capabilityAnswer, len(decoded.Results))
	for index, answer := range decoded.Results {
		if want, _ := questions[index]["id"].(string); answer.ID != want {
			t.Fatalf("live result %d has id %q, want %q: the batch must keep input order",
				index, answer.ID, want)
		}
		if _, duplicate := answers[answer.ID]; duplicate {
			t.Fatalf("live capabilities returned two results for id %q", answer.ID)
		}
		answers[answer.ID] = answer
	}
	return answers
}

// capabilityBudgetDelayResolver is the owned fixture that CONSUMES the endpoint's
// horizon. It wraps the production closure resolver and delegates untouched while its
// delay is zero, so the estate it is installed on behaves normally.
//
// ⛔ IT IS INSTALLED ONCE, BEFORE THE LISTENER STARTS, AND STEERED WITH AN ATOMIC. The
// module's late-binding setter writes a plain field; swapping it while a live server
// goroutine may read it is a data race the -race build would rightly report. Every
// existing case swaps it from the same goroutine that calls ServeHTTP, which this file
// deliberately no longer does.
type capabilityBudgetDelayResolver struct {
	sessions.ChannelGrantSubjectClosureResolver
	delayNanos atomic.Int64
	calls      atomic.Int64
	canceled   atomic.Int64
}

func (d *capabilityBudgetDelayResolver) ResolveChannelGrantSubjects(
	ctx context.Context,
	scope sessions.DirectoryScopeRef,
	principal sessions.CommunicationPrincipal,
) (sessions.ChannelGrantSubjectClosure, error) {
	if delay := time.Duration(d.delayNanos.Load()); delay > 0 {
		d.calls.Add(1)
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			// The horizon closed while this stage was working. Reporting the
			// context error is what an outage reports; it is never a denial.
			d.canceled.Add(1)
			return sessions.ChannelGrantSubjectClosure{}, ctx.Err()
		}
	}
	return d.ChannelGrantSubjectClosureResolver.ResolveChannelGrantSubjects(ctx, scope, principal)
}

// capabilityLiveRefusal asserts one common error envelope: a single status and code,
// never a 200 batch of per-question results.
func capabilityLiveRefusal(
	t *testing.T, name string, response capabilityLiveResponse, wantStatus int, wantCode string,
) {
	t.Helper()
	if response.status != wantStatus {
		t.Fatalf("%s = %d, want %d: %s", name, response.status, wantStatus, response.raw)
	}
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.raw, &envelope); err != nil {
		t.Fatalf("%s body: %v (%s)", name, err, response.raw)
	}
	if envelope.Error.Code != wantCode {
		t.Fatalf("%s code = %q, want %q (%s)", name, envelope.Error.Code, wantCode, response.raw)
	}
}

// capabilityLiveTargetFree asserts a common refusal names none of the asked-about ids.
func capabilityLiveTargetFree(t *testing.T, name string, raw []byte, forbidden ...string) {
	t.Helper()
	body := string(raw)
	for _, target := range forbidden {
		if strings.Contains(body, target) {
			t.Fatalf("%s body names %q: a common refusal must be target-free (%s)",
				name, target, body)
		}
	}
}

// capabilityPositiveIDs lists, in batch order, the ids a run answered with a positive.
func capabilityPositiveIDs(order []string, answers map[string]capabilityAnswer) []string {
	positives := make([]string, 0, len(order))
	for _, id := range order {
		switch answers[id].State {
		case "allowed", "reachable":
			positives = append(positives, id)
		}
	}
	return positives
}

// capabilityReconstructionOutage is a test-owned producer that fails reconstruction
// after authenticate has already succeeded. It does not wrap or replace the product
// producer implementation; it is installed only on a rebuilt API server.
type capabilityReconstructionOutage struct{ calls atomic.Int64 }

func (p *capabilityReconstructionOutage) ResolvePrincipalScope(
	context.Context, auth.PrincipalRef, model.TenantID,
) (auth.Principal, error) {
	p.calls.Add(1)
	return auth.Principal{}, errors.New("reconstruction outage sentinel")
}

// capabilityChannelScanStore counts channel row reads so a common refusal can prove
// it did not evaluate targets.
type capabilityChannelScanStore struct {
	store.Store
	reads atomic.Int64
}

func (s *capabilityChannelScanStore) View(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	return s.Store.View(ctx, tenant, func(sc store.Scope) error {
		return fn(capabilityChannelScanScope{Scope: sc, spy: s})
	})
}

type capabilityChannelScanScope struct {
	store.Scope
	spy *capabilityChannelScanStore
}

func (s capabilityChannelScanScope) Ext(kind model.Kind) (store.GenericRepo, error) {
	repo, err := s.Scope.Ext(kind)
	if err != nil || kind != "sessions.channel" {
		return repo, err
	}
	return capabilityChannelScanRepo{GenericRepo: repo, spy: s.spy}, nil
}

type capabilityChannelScanRepo struct {
	store.GenericRepo
	spy *capabilityChannelScanStore
}

func (r capabilityChannelScanRepo) Get(ctx context.Context, id model.ID) (model.Record, error) {
	r.spy.reads.Add(1)
	return r.GenericRepo.Get(ctx, id)
}

func TestCapabilityReconstructionHTTP(t *testing.T) {
	estate := bootChannelAdministrationHTTPEstate(t, communicationHTTPTestSQLiteStore(t))
	eng, tenant, workspace := estate.eng, estate.tenant, estate.workspace
	ws := workspace.String()
	ownerAll := channelAdministrationGrant(channelAdministrationSubject("user", estate.owner.id), true, true, true)
	stewardAdminOnly := channelAdministrationGrant(channelAdministrationSubject("user", estate.steward.id), false, false, true)
	held := estate.createChannel(t, workspace, "budget-reconstruction",
		[]map[string]any{ownerAll, stewardAdminOnly})

	questions := []map[string]any{
		capabilitySurfaceQuestion("surface", capSurfaceAdministration, ws),
		capabilityChannelQuestion("sheet", capOperationGrantSheet, ws, held.Channel.ID),
	}

	production := newCapabilityLiveTransport(t, eng, tenant, nil)
	healthy := production.ask(t, estate.steward.token, questions...)
	capabilityWant(t, healthy, "surface", "reachable", "admitted")
	capabilityWant(t, healthy, "sheet", "allowed", "authorized")

	producer := &capabilityReconstructionOutage{}
	spy := &capabilityChannelScanStore{Store: eng.store}
	server, err := api.New(api.Options{
		Store: spy, Authenticator: eng.authr, Authorizer: eng.authz, Signer: eng.signer,
		PrincipalEvidenceProducer: producer, SetupToken: eng.setupTok,
		Logger: eng.log, Modules: []api.Module{eng.sessionsMod}, Version: "cap-reconstruction",
	})
	if err != nil {
		t.Fatalf("rebuild API with reconstruction outage producer: %v", err)
	}
	broken := *eng
	broken.api = server
	outage := newCapabilityLiveTransport(t, &broken, tenant, nil)
	response := outage.askRaw(t, estate.steward.token, questions...)

	capabilityLiveRefusal(t, "reconstruction outage", response,
		http.StatusServiceUnavailable, "route_decision_unavailable")
	if got := response.header.Get("Retry-After"); got != "5" {
		t.Fatalf("reconstruction outage Retry-After = %q, want 5", got)
	}
	if got := response.header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("reconstruction outage Cache-Control = %q, want no-store", got)
	}
	capabilityLiveTargetFree(t, "reconstruction outage", response.raw,
		"surface", "sheet", held.Channel.ID.String(), ws, "results",
		"reconstruction outage sentinel")
	if producer.calls.Load() != 1 {
		t.Fatalf("ResolvePrincipalScope calls = %d, want 1: reconstruction must run once",
			producer.calls.Load())
	}
	if got := spy.reads.Load(); got != 0 {
		t.Fatalf("channel Get count = %d, want 0: reconstruction 503 must not evaluate targets", got)
	}
	t.Logf("CAP_LIVE_RECONSTRUCTION|status=%d|code=%s|retry_after=%s|cache=%s|producer_calls=%d|channel_gets=%d",
		response.status, "route_decision_unavailable", response.header.Get("Retry-After"),
		response.header.Get("Cache-Control"), producer.calls.Load(), spy.reads.Load())
}
