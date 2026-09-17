// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

func TestCapabilityTransportBudgetHTTP(t *testing.T) {
	estate := bootChannelAdministrationHTTPEstate(t, communicationHTTPTestSQLiteStore(t))
	eng, tenant, workspace := estate.eng, estate.tenant, estate.workspace
	owner, steward, viewer := estate.owner, estate.steward, estate.viewer
	ws := workspace.String()
	wsQuery := "workspace_id=" + ws

	ownerAll := channelAdministrationGrant(channelAdministrationSubject("user", owner.id), true, true, true)
	stewardAdminOnly := channelAdministrationGrant(channelAdministrationSubject("user", steward.id), false, false, true)
	viewerAdminOnly := channelAdministrationGrant(channelAdministrationSubject("user", viewer.id), false, false, true)

	adminNoRead := estate.createChannel(t, workspace, "budget-admin-no-read",
		[]map[string]any{ownerAll, stewardAdminOnly})
	ownerOnly := estate.createChannel(t, workspace, "budget-owner-only", []map[string]any{ownerAll})
	scopedChannel := estate.createChannel(t, workspace, "budget-scoped-admin",
		[]map[string]any{ownerAll, viewerAdminOnly})

	if eng.communicationComposition == nil || eng.communicationComposition.closure == nil {
		t.Fatal("production communication composition is unavailable for the budget controls")
	}
	delayer := &capabilityBudgetDelayResolver{
		ChannelGrantSubjectClosureResolver: sessions.ChannelGrantSubjectClosureResolver(
			eng.communicationComposition.closure),
	}
	eng.sessionsMod.UseCommunicationChannelGrantSubjectClosureResolver(delayer)

	production := newCapabilityLiveTransport(t, eng, tenant, nil)

	t.Run("the production transport supplies no deadline and the positive still holds", func(t *testing.T) {
		before := communicationHTTPTestEffects(t, eng, tenant)
		answers := production.ask(t, steward.token,
			capabilitySurfaceQuestion("surface", capSurfaceAdministration, ws),
			capabilityChannelQuestion("sheet", capOperationGrantSheet, ws, adminNoRead.Channel.ID),
			capabilityBodyQuestion("patch", capOperationPatch, ws, adminNoRead.Channel.ID),
			capabilityChannelQuestion("no-bit", capOperationGrantSheet, ws, ownerOnly.Channel.ID),
		)
		if production.served.Load() == 0 {
			t.Fatal("the live listener recorded no capability request: the fixture did not measure the transport")
		}
		if got := production.deadlines.Load(); got != 0 {
			t.Fatalf("the transport delivered %d request(s) already carrying a deadline: this fixture "+
				"must supply none, or it recreates the masking it exists to remove", got)
		}
		surface := capabilityWant(t, answers, "surface", "reachable", "admitted")
		sheet := capabilityWant(t, answers, "sheet", "allowed", "authorized")
		patch := capabilityWant(t, answers, "patch", "allowed", "authorized")
		// A concealing operation the steward holds no local ADMIN bit on keeps its one
		// public shape, on this transport exactly as on any other.
		capabilityWant(t, answers, "no-bit", "undisclosed", "not_disclosed")

		// ⛔ THE BUDGET IS THE ENDPOINT'S OWN HORIZON, NOT A HELPER'S. Under the old
		// two-minute helper these were clipped by the reconstruction window; with no
		// deadline at all they did not exist. Either way they can never exceed the
		// server-owned batch budget.
		for _, answer := range []capabilityAnswer{surface, sheet, patch} {
			if *answer.RefreshAfterMS > 5000 {
				t.Fatalf("%s refresh_after_ms = %d ms, longer than the whole server-owned "+
					"batch budget", answer.ID, *answer.RefreshAfterMS)
			}
		}

		// The projection is measured against the REAL routes on the SAME live server,
		// which is the whole claim: the endpoint agrees with the door it projects.
		administration := production.do(t, http.MethodGet,
			"/v1/m/sessions/channels/administration?"+wsQuery, steward.token, nil)
		if administration.status != http.StatusOK {
			t.Fatalf("live administration route = %d: %s", administration.status, administration.raw)
		}
		held := production.do(t, http.MethodGet,
			"/v1/m/sessions/channels/"+adminNoRead.Channel.ID.String()+"/grants?"+wsQuery,
			steward.token, nil)
		if held.status != http.StatusOK {
			t.Fatalf("live grant sheet on the held Channel = %d: %s", held.status, held.raw)
		}
		unheld := production.do(t, http.MethodGet,
			"/v1/m/sessions/channels/"+ownerOnly.Channel.ID.String()+"/grants?"+wsQuery,
			steward.token, nil)
		if unheld.status != http.StatusNotFound {
			t.Fatalf("live grant sheet on the unheld Channel = %d, want the concealed 404: %s",
				unheld.status, unheld.raw)
		}
		assertCommunicationHTTPTestNoEffects(t, eng, tenant, before, "the live capability projection")
		t.Logf("CAP_LIVE_TRANSPORT|transport_deadlines=%d|surface=%s/%s|sheet=%s/%s|"+
			"surface_ms=%d|sheet_ms=%d|route=%d",
			production.deadlines.Load(), surface.State, surface.Code, sheet.State, sheet.Code,
			*surface.RefreshAfterMS, *sheet.RefreshAfterMS, administration.status)
	})

	t.Run("a workspace-scoped positive is reachable over the same transport", func(t *testing.T) {
		// The viewer holds NEITHER permission by role: only a Cedar grant scoped to one
		// workspace plus a local ADMIN bit. It is the scoped half of the acceptance, and
		// it is published over the same live transport so no in-process helper touches
		// any part of this case.
		published := production.do(t, http.MethodPost, "/v1/m/governance/pdp/publish", owner.token,
			map[string]any{"engine": "cedar", "source": fmt.Sprintf(
				`permit(principal in User::%q, action == Action::"sessions:channel:admin", resource)`+
					` when { resource in Workspace::"k3-admin-ws" };`, viewer.id.String())})
		if published.status != http.StatusOK {
			t.Fatalf("live policy publish = %d: %s", published.status, published.raw)
		}
		answers := production.ask(t, viewer.token,
			capabilitySurfaceQuestion("surface", capSurfaceAdministration, ws),
			capabilityChannelQuestion("sheet", capOperationGrantSheet, ws, scopedChannel.Channel.ID),
			capabilitySurfaceQuestion("side", capSurfaceAdministration, estate.sideWorkspace.String()),
		)
		capabilityWant(t, answers, "surface", "reachable", "admitted")
		capabilityWant(t, answers, "sheet", "allowed", "authorized")
		// The grant names ONE workspace, and a budget installed by the endpoint does not
		// widen it into the sibling collection.
		capabilityWant(t, answers, "side", "not_reachable", "not_permitted")
		served := production.do(t, http.MethodGet,
			"/v1/m/sessions/channels/administration?"+wsQuery, viewer.token, nil)
		if served.status != http.StatusOK {
			t.Fatalf("live scoped administration route = %d: %s", served.status, served.raw)
		}
		t.Logf("CAP_LIVE_SCOPED|surface=reachable|sheet=allowed|route=%d", served.status)
	})

	t.Run("an earlier caller deadline narrows the horizon and is not replaced", func(t *testing.T) {
		const caller = 2 * time.Second
		short := newCapabilityLiveTransport(t, eng, tenant,
			func(ctx context.Context) (context.Context, context.CancelFunc) {
				return context.WithTimeout(ctx, caller)
			})
		wide := production.ask(t, steward.token,
			capabilitySurfaceQuestion("surface", capSurfaceAdministration, ws),
			capabilityChannelQuestion("sheet", capOperationGrantSheet, ws, adminNoRead.Channel.ID),
		)
		narrow := short.ask(t, steward.token,
			capabilitySurfaceQuestion("surface", capSurfaceAdministration, ws),
			capabilityChannelQuestion("sheet", capOperationGrantSheet, ws, adminNoRead.Channel.ID),
		)
		for _, id := range []string{"surface", "sheet"} {
			state, code := "reachable", "admitted"
			if id == "sheet" {
				state, code = "allowed", "authorized"
			}
			wideAnswer := capabilityWant(t, wide, id, state, code)
			narrowAnswer := capabilityWant(t, narrow, id, state, code)
			if *narrowAnswer.RefreshAfterMS > caller.Milliseconds() {
				t.Fatalf("%s under a %s caller deadline = %d ms: the endpoint's own budget "+
					"replaced the earlier horizon instead of narrowing under it",
					id, caller, *narrowAnswer.RefreshAfterMS)
			}
			if *narrowAnswer.RefreshAfterMS >= *wideAnswer.RefreshAfterMS {
				t.Fatalf("%s = %d ms with a %s caller deadline and %d ms without one: the earlier "+
					"deadline did not bind", id, *narrowAnswer.RefreshAfterMS, caller,
					*wideAnswer.RefreshAfterMS)
			}
			t.Logf("CAP_LIVE_CALLER_DEADLINE|%s|no_caller_ms=%d|caller_2s_ms=%d",
				id, *wideAnswer.RefreshAfterMS, *narrowAnswer.RefreshAfterMS)
		}
	})

	t.Run("a canceled or expired caller context keeps the target-free refusal", func(t *testing.T) {
		// A caller context that is already dead when the request arrives is refused by
		// authenticate (401 unauthenticated) before reconstruction or the batch budget
		// run. The budget must not invent a 200 batch of per-question results for that
		// request. Reconstruction 503 is a different common refusal, measured by
		// TestCapabilityReconstructionHTTP.
		expired := func(ctx context.Context) (context.Context, context.CancelFunc) {
			return context.WithDeadline(ctx, time.Now().Add(-time.Second))
		}
		canceled := func(ctx context.Context) (context.Context, context.CancelFunc) {
			dead, stop := context.WithCancel(ctx)
			stop()
			return dead, func() {}
		}
		for _, arm := range []struct {
			name  string
			shape capabilityCallerContext
		}{{"expired", expired}, {"canceled", canceled}} {
			live := newCapabilityLiveTransport(t, eng, tenant, arm.shape)
			response := live.askRaw(t, steward.token,
				capabilitySurfaceQuestion("surface", capSurfaceAdministration, ws),
				capabilityChannelQuestion("sheet", capOperationGrantSheet, ws, adminNoRead.Channel.ID),
			)
			capabilityLiveRefusal(t, arm.name+" caller context", response,
				http.StatusUnauthorized, "unauthenticated")
			capabilityLiveTargetFree(t, arm.name+" caller context", response.raw,
				"surface", "sheet", adminNoRead.Channel.ID.String(), ws, "results")
			t.Logf("CAP_LIVE_DEAD_CONTEXT|%s|status=%d|code=%s",
				arm.name, response.status, "unauthenticated")
		}
	})

	t.Run("one budget bounds the whole batch and no question renews it", func(t *testing.T) {
		if testing.Short() {
			t.Skip("the exhaustion arm spends the whole server-owned budget in real time")
		}
		// The maximum ratified batch, mixing both kinds AND both disclosure policies:
		// the administrative sheet conceals its non-positives, PATCH /channels does
		// not, and a collection has a vocabulary of its own. Each id therefore carries
		// the shape its OWN route owes when the horizon has closed, so a budget that
		// flattened them into one answer would fail here.
		order := make([]string, 0, 32)
		batch := make([]map[string]any, 0, 32)
		// unestablished is the (state, code) each id must show once nothing can be
		// established for it any more.
		unestablished := make(map[string][2]string, 32)
		concealed := [2]string{"undisclosed", "not_disclosed"}
		unavailable := [2]string{"unknown", "evidence_unavailable"}
		for index := 0; index < 32; index++ {
			id := fmt.Sprintf("q%02d", index)
			order = append(order, id)
			switch index % 4 {
			case 0:
				batch = append(batch, capabilityChannelQuestion(id, capOperationGrantSheet, ws, adminNoRead.Channel.ID))
				unestablished[id] = concealed
			case 1:
				batch = append(batch, capabilitySurfaceQuestion(id, capSurfaceAdministration, ws))
				unestablished[id] = unavailable
			case 2:
				batch = append(batch, capabilityBodyQuestion(id, capOperationPatch, ws, adminNoRead.Channel.ID))
				unestablished[id] = unavailable
			case 3:
				batch = append(batch, capabilityChannelQuestion(id, capOperationGrantSheet, ws, ownerOnly.Channel.ID))
				unestablished[id] = concealed
			}
		}

		healthy := production.askRaw(t, steward.token, batch...)
		if healthy.status != http.StatusOK {
			t.Fatalf("the healthy 32-question batch = %d: %s", healthy.status, healthy.raw)
		}
		control := production.ask(t, steward.token, batch...)
		expected := capabilityPositiveIDs(order, control)
		if len(expected) < 20 {
			t.Fatalf("the healthy batch produced only %d positives: the exhaustion arm would "+
				"prove nothing", len(expected))
		}

		// ⛔ THE DISCRIMINATOR. Each closure resolution now costs 700 ms, so the batch
		// needs far more than five seconds of work. Under ONE shared budget the horizon
		// closes part-way through and every later question is non-positive. Under a
		// per-question budget each of the 32 would start a fresh five seconds, 700 ms
		// would fit inside every one of them, and the whole batch would stay positive.
		const perCall = 700 * time.Millisecond
		delayer.delayNanos.Store(int64(perCall))
		exhausted := production.askRaw(t, steward.token, batch...)
		delayer.delayNanos.Store(0)
		if exhausted.status != http.StatusOK {
			t.Fatalf("the exhausted batch = %d, want 200 with per-question results: %s",
				exhausted.status, exhausted.raw)
		}
		var decoded capabilityResults
		if err := json.Unmarshal(exhausted.raw, &decoded); err != nil {
			t.Fatalf("decode the exhausted batch: %v (%s)", err, exhausted.raw)
		}
		if len(decoded.Results) != len(batch) {
			t.Fatalf("the exhausted batch returned %d results, want all %d: the ceiling and the "+
				"per-id independence do not depend on the budget", len(decoded.Results), len(batch))
		}
		answers := make(map[string]capabilityAnswer, len(decoded.Results))
		for index, answer := range decoded.Results {
			if answer.ID != order[index] {
				t.Fatalf("exhausted result %d has id %q, want %q", index, answer.ID, order[index])
			}
			answers[answer.ID] = answer
		}

		spent := ""
		for _, id := range expected {
			answer := answers[id]
			switch answer.State {
			case "allowed", "reachable":
				if spent != "" {
					t.Fatalf("%s is %s after %s had already exhausted the horizon: a later "+
						"question renewed the budget", id, answer.State, spent)
				}
				if answer.RefreshAfterMS == nil {
					t.Fatalf("%s is %s with no budget", id, answer.State)
				}
			default:
				if spent == "" {
					spent = id
				}
				if answer.RefreshAfterMS != nil {
					t.Fatalf("%s is %s and still carries refresh_after_ms=%d", id, answer.State,
						*answer.RefreshAfterMS)
				}
			}
		}
		if spent == "" {
			t.Fatalf("all %d positives survived %s of injected work per closure resolution: the "+
				"batch is not bounded by one horizon", len(expected), perCall)
		}
		if spent == expected[0] {
			t.Fatalf("the horizon was already gone at %s, before any question could spend it: "+
				"the arm measures a broken estate rather than a shared budget", spent)
		}
		// Each non-positive keeps the vocabulary its OWN route owes: an unestablished
		// collection is unknown/evidence_unavailable, a concealing operation keeps its
		// single public non-verdict, and the non-concealing PATCH keeps the honest
		// unknown rather than borrowing the concealment of its neighbors. None of them
		// carries a budget.
		for _, id := range order {
			answer := answers[id]
			if answer.State == "allowed" || answer.State == "reachable" {
				continue
			}
			want := unestablished[id]
			if answer.State != want[0] || answer.Code != want[1] {
				t.Fatalf("%s = %s/%s once the horizon closed, want %s/%s: the budget must not "+
					"change what a route discloses", id, answer.State, answer.Code, want[0], want[1])
			}
			if answer.RefreshAfterMS != nil {
				t.Fatalf("%s is %s and carries refresh_after_ms=%d", id, answer.State,
					*answer.RefreshAfterMS)
			}
		}
		// ⛔ AND THE AGGREGATE IS BOUNDED, WHICH IS THE OTHER HALF OF THE SAME CLAIM.
		// A per-question budget would have let every positive question pay its own
		// injected cost inside its own fresh horizon; the batch would have run for at
		// least one delay per positive instead of stopping at one shared horizon. This
		// is measured on the wire, not inferred from the answers.
		renewalFloor := time.Duration(len(expected)) * perCall
		if exhausted.elapsed >= renewalFloor/2 {
			t.Fatalf("the exhausted batch took %s: %d positives paying %s each would have needed "+
				"about %s under a per-question budget, so this is not one shared horizon",
				exhausted.elapsed, len(expected), perCall, renewalFloor)
		}
		if exhausted.elapsed < 3*time.Second {
			t.Fatalf("the exhausted batch took only %s: the horizon closed before the injected "+
				"cost could be what closed it", exhausted.elapsed)
		}
		// The estate is intact: the same batch answers as before once the injected cost
		// is gone, so the arm above measured the horizon and not a broken fixture.
		restored := production.ask(t, steward.token, batch...)
		if got := capabilityPositiveIDs(order, restored); len(got) != len(expected) {
			t.Fatalf("after the exhaustion arm the healthy batch yields %d positives, want %d",
				len(got), len(expected))
		}
		t.Logf("CAP_LIVE_SHARED_BUDGET|questions=%d|healthy_positives=%d|per_call=%s|"+
			"first_non_positive=%s|resolver_calls=%d|resolver_canceled=%d|batch_ms=%d",
			len(batch), len(expected), perCall, spent, delayer.calls.Load(),
			delayer.canceled.Load(), exhausted.elapsed.Milliseconds())
	})
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
