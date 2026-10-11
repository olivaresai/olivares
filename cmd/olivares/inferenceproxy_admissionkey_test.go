// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/internal/inferencepep"
	claudeapi "github.com/olivaresai/olivares/connectors/claude-api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// The proxy's admission: one key per call, one hold taken at the budget step, after every
// gate that can refuse the request on its content, and that hold settled exactly once.

// TestProxyAdmissionKeyIsPerCallNotPerPayload is the caller half of the replay definition.
// The proxy is handed no idempotency key: the client sends a request, not a claim about
// which earlier request it repeats. A key derived from the bytes would say "this is a
// retry" about two calls that are merely identical, a prompt sent twice or a scheduled job
// that sends the same thing each run, and the second would be answered with the first
// one's hold instead of by the ledger.
func TestProxyAdmissionKeyIsPerCallNotPerPayload(t *testing.T) {
	a, mg, bg, kg, pol := allowAll()
	d := newTestDecider(a, mg, bg, kg, pol)
	req := userReq("the same prompt, sent twice", false)

	for i := 0; i < 2; i++ {
		if dec := d.Authorize(context.Background(), req, "bearer"); !dec.Allow {
			t.Fatalf("call %d was denied: status=%d reason=%q", i, dec.Status, dec.Reason)
		}
	}

	if len(bg.keys) != 2 {
		t.Fatalf("the budget gate was asked %d time(s) for two calls: %v", len(bg.keys), bg.keys)
	}
	if bg.keys[0] == bg.keys[1] {
		t.Fatalf("two separate calls carrying the same bytes claimed one idempotency key %q: "+
			"the second one is answered with the first one's hold", bg.keys[0])
	}
	for i, k := range bg.keys {
		if !strings.HasPrefix(k, "model_gateway/") {
			t.Errorf("key %d = %q, want the model_gateway scope prefix", i, k)
		}
	}
	if len(bg.held) != 2 || bg.held[0] == bg.held[1] {
		t.Fatalf("holds = %v, want one hold of its own per call", bg.held)
	}
}

// TestProxyNoHoldWhenMCPGateDenies: a request the MCP egress gate refuses never reaches
// admission, so it leaves no admission row and no ledger row. The gate runs in the local
// phase, before the sizing pre-flight and before the budget step, for a single message and
// for every entry of a batch before any entry is admitted. The control is the same request
// with its origin granted: it reaches the budget step and takes its hold there.
func TestProxyNoHoldWhenMCPGateDenies(t *testing.T) {
	mcpReq := func() claudeapi.MessageRequest {
		r := userReq("summarize the public changelog", false)
		r.Tools = []any{mcpToolsetMap(mcpCanaryName)}
		r.MCPServers = []any{mcpServerMap(mcpCanaryName, mcpTestURL)}
		return r
	}
	for _, tc := range []struct {
		name      string
		granted   bool
		batch     bool
		wantHolds int
	}{
		{name: "message refused", granted: false, batch: false, wantHolds: 0},
		{name: "message granted", granted: true, batch: false, wantHolds: 1},
		{name: "batch refused", granted: false, batch: true, wantHolds: 0},
		{name: "batch granted", granted: true, batch: true, wantHolds: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, mg, bg, kg, pol := allowAll()
			d := newTestDecider(a, mg, bg, kg, pol)
			d.Egress = &originGate{granted: map[string]bool{mcpTestOrigin: tc.granted}}
			var allow bool
			var status int
			if tc.batch {
				dec := d.AuthorizeBatch(context.Background(), []claudeapi.BatchRequest{
					{CustomID: "c0", Params: userReq("hi", false)},
					{CustomID: "c1", Params: mcpReq()},
				}, "bearer")
				allow, status = dec.Allow, dec.Status
			} else {
				dec := d.Authorize(context.Background(), mcpReq(), "bearer")
				allow, status = dec.Allow, dec.Status
			}
			if allow != tc.granted {
				t.Fatalf("allow = %v (status %d) with the origin granted=%v", allow, status, tc.granted)
			}
			if !tc.granted && status != http.StatusForbidden {
				t.Fatalf("an ungranted MCP origin must be refused 403 %s; got %d", claudeapi.MCPDenyOriginNotGranted, status)
			}
			if len(bg.held) != tc.wantHolds || len(bg.keys) != tc.wantHolds {
				t.Fatalf("admission asked %d time(s) and took holds %v, want %d: a refused request must not reach "+
					"admission, and an admitted one holds at the budget step", len(bg.keys), bg.held, tc.wantHolds)
			}
		})
	}
}

// TestProxyReleasesHoldWhenRecordingIntentFails: the hold is taken at the budget step, and
// the recording reservation of a recording-mandating tenant comes after it. When that
// refuses the call, the call never runs, so its hold goes back at once instead of
// withholding headroom until it expires. A batch gives back the hold of every entry.
func TestProxyReleasesHoldWhenRecordingIntentFails(t *testing.T) {
	mandatory := func() fakeProxyPolicy {
		pol := allGatesOnExceptDLPAndCtx()
		pol.RecordMandatory = true
		return fakeProxyPolicy{pol: pol}
	}

	t.Run("message", func(t *testing.T) {
		a, mg, bg, kg, _ := allowAll()
		d := newTestDecider(a, mg, bg, kg, mandatory()) // no ledger store: the intent cannot be anchored
		dec := d.Authorize(context.Background(), userReq("hi", false), "bearer")
		if dec.Allow || dec.Status != http.StatusServiceUnavailable {
			t.Fatalf("a mandating tenant without a ledger must be refused 503; got allow=%v status=%d", dec.Allow, dec.Status)
		}
		if len(bg.held) != 1 {
			t.Fatalf("holds = %v, want the one the budget step took", bg.held)
		}
		if !reflect.DeepEqual(bg.released, bg.held) || len(bg.committed) != 0 {
			t.Fatalf("released %v and committed %v, want exactly the call's hold %v released", bg.released, bg.committed, bg.held)
		}
	})

	t.Run("batch", func(t *testing.T) {
		a, mg, bg, kg, _ := allowAll()
		d := newTestDecider(a, mg, bg, kg, mandatory())
		dec := d.AuthorizeBatch(context.Background(), batchReqs("claude-opus-4-8", "claude-opus-4-8"), "bearer")
		if dec.Allow || dec.Status != http.StatusServiceUnavailable {
			t.Fatalf("a mandating tenant's batch without a ledger must be refused 503; got allow=%v status=%d", dec.Allow, dec.Status)
		}
		if len(bg.held) != 2 {
			t.Fatalf("holds = %v, want one per entry", bg.held)
		}
		if !reflect.DeepEqual(bg.released, bg.held) || len(bg.committed) != 0 {
			t.Fatalf("released %v and committed %v, want every entry's hold %v released", bg.released, bg.committed, bg.held)
		}
	})
}

// TestProxyFinalizeSettlesTheCallsHold: Finalize settles the call's hold exactly once. A
// call that ran is committed at its measured cost, the sum of the cost samples Finalize
// published; a call whose upstream failed published no cost, and its hold is released.
func TestProxyFinalizeSettlesTheCallsHold(t *testing.T) {
	t.Run("committed at the measured cost", func(t *testing.T) {
		a, mg, bg, kg, pol := allowAll()
		d := newTestDecider(a, mg, bg, kg, pol)
		d.Inference = claudeapi.NewInference(claudeapi.InferenceConfig{APIKey: "test", DefaultModel: "claude-opus-4-8", Gateway: sdkmodel.GatewayDirect})
		bus := &fakeObservationBus{}
		d.Bus = bus
		dec := d.Authorize(context.Background(), userReq("hi", false), "bearer")
		if !dec.Allow || len(bg.held) != 1 {
			t.Fatalf("admission: allow=%v holds=%v", dec.Allow, bg.held)
		}
		d.Finalize(context.Background(), dec.Session, proxyRanResult())
		actual := publishedCost(t, bus)
		if want := []proxySettlement{{handle: bg.held[0], actual: actual}}; !reflect.DeepEqual(bg.committed, want) {
			t.Fatalf("commits = %+v, want %+v", bg.committed, want)
		}
		if len(bg.released) != 0 {
			t.Fatalf("a call that ran was also released: %v", bg.released)
		}
	})

	t.Run("released when the upstream failed", func(t *testing.T) {
		a, mg, bg, kg, pol := allowAll()
		d := newTestDecider(a, mg, bg, kg, pol)
		dec := d.Authorize(context.Background(), userReq("hi", false), "bearer")
		if !dec.Allow || len(bg.held) != 1 {
			t.Fatalf("admission: allow=%v holds=%v", dec.Allow, bg.held)
		}
		d.Finalize(context.Background(), dec.Session, claudeapi.ProxyForwardResult{UpstreamErr: true, UpstreamStatus: http.StatusBadGateway})
		if !reflect.DeepEqual(bg.released, bg.held) || len(bg.committed) != 0 {
			t.Fatalf("released %v and committed %v, want the call's hold %v released", bg.released, bg.committed, bg.held)
		}
	})
}

// TestProxyFinalizeSettlesAfterTheClientLeft: Finalize runs on the request's context, which
// is canceled when the client goes away, and the call's hold is settled all the same: the
// call ran, or failed upstream, whatever the client did next. A settlement that inherited
// the cancellation would fail, and the hold would withhold until it expires.
func TestProxyFinalizeSettlesAfterTheClientLeft(t *testing.T) {
	for _, tc := range []struct {
		name        string
		upstreamErr bool
	}{
		{name: "committed", upstreamErr: false},
		{name: "released", upstreamErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, mg, bg, kg, pol := allowAll()
			d := newTestDecider(a, mg, bg, kg, pol)
			dec := d.Authorize(context.Background(), userReq("hi", false), "bearer")
			if !dec.Allow || len(bg.held) != 1 {
				t.Fatalf("admission: allow=%v holds=%v", dec.Allow, bg.held)
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			d.Finalize(ctx, dec.Session, claudeapi.ProxyForwardResult{UpstreamErr: tc.upstreamErr})
			if n := len(bg.committed) + len(bg.released); n != 1 {
				t.Fatalf("settlements = %d (committed %+v, released %v), want one", n, bg.committed, bg.released)
			}
			if len(bg.settleCtxErrs) != 1 || bg.settleCtxErrs[0] != nil {
				t.Fatalf("the settlement ran under a done context (%v): it would fail, and the hold would withhold until it expires",
					bg.settleCtxErrs)
			}
		})
	}
}

// TestProxyBatchSettlesEveryEntry: every entry of an admitted batch took a hold, and
// FinalizeBatch settles each of them, not only the first entry's. An accepted submission
// has no cost yet, since its results are billed when they are ingested, so each hold is
// committed at zero; a submission the upstream refused ran nothing, so each is released.
func TestProxyBatchSettlesEveryEntry(t *testing.T) {
	for _, tc := range []struct {
		name        string
		upstreamErr bool
	}{
		{name: "accepted", upstreamErr: false},
		{name: "upstream refused", upstreamErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, mg, bg, kg, pol := allowAll()
			d := newTestDecider(a, mg, bg, kg, pol)
			dec := d.AuthorizeBatch(context.Background(), batchReqs("claude-opus-4-8", "claude-opus-4-8"), "bearer")
			if !dec.Allow || len(bg.held) != 2 {
				t.Fatalf("admission: allow=%v holds=%v", dec.Allow, bg.held)
			}
			d.FinalizeBatch(context.Background(), dec.Session, claudeapi.ProxyBatchForwardResult{Entries: 2, UpstreamErr: tc.upstreamErr})
			if tc.upstreamErr {
				if !reflect.DeepEqual(bg.released, bg.held) || len(bg.committed) != 0 {
					t.Fatalf("released %v and committed %v, want every entry's hold %v released", bg.released, bg.committed, bg.held)
				}
				return
			}
			want := []proxySettlement{{handle: bg.held[0]}, {handle: bg.held[1]}}
			if !reflect.DeepEqual(bg.committed, want) || len(bg.released) != 0 {
				t.Fatalf("committed %+v and released %v, want every entry's hold committed at zero: %+v", bg.committed, bg.released, want)
			}
		})
	}
}

// TestProxyCommitsBothComponents drives the proxy over the real ledger with a global budget
// and a spend limit on the caller's seat. The call's one hold names a budget row and a
// spend-limit row, and Finalize commits both at the measured cost, and the admission row
// that published the hold with them.
func TestProxyCommitsBothComponents(t *testing.T) {
	forEachFinOpsEngine(t, func(t *testing.T, cfg store.Config) {
		d, st, tenant, actor, bus := proxyOverLedger(t, cfg)
		createBudgetPolicy(t, st, tenant, "gateway-cap", globalBudget(20_000_000))
		createSeatSpendLimit(t, st, tenant, actor, 10_000_000)

		dec := d.Authorize(context.Background(), userReq("hi", false), "bearer")
		if !dec.Allow {
			t.Fatalf("a call under both caps was refused: status=%d reason=%q", dec.Status, dec.Reason)
		}
		h, rows := theOneHold(t, st, tenant)
		requireComponents(t, rows, "budget", "spend_limit")
		for _, r := range rows {
			if r.String("state") != "active" || r.Int("amount_micro_usd") <= 0 || r.Int("amount_micro_usd") != rows[0].Int("amount_micro_usd") {
				t.Fatalf("before Finalize the hold's %s row = state %q amount %d, want both active with one estimate",
					r.String("policy_kind"), r.String("state"), r.Int("amount_micro_usd"))
			}
		}

		d.Finalize(context.Background(), dec.Session, proxyRanResult())
		requireSettled(t, st, tenant, h, "committed", publishedCost(t, bus), "budget", "spend_limit")
	})
}

// TestProxySettlesSpendOnlyHold: with a spend limit on the caller's seat and no budget, the
// call's hold has only a spend-limit row. It is still the call's one hold, and Finalize
// commits it like any other; nothing is left withholding the seat until it expires.
func TestProxySettlesSpendOnlyHold(t *testing.T) {
	forEachFinOpsEngine(t, func(t *testing.T, cfg store.Config) {
		d, st, tenant, actor, bus := proxyOverLedger(t, cfg)
		createSeatSpendLimit(t, st, tenant, actor, 10_000_000)

		dec := d.Authorize(context.Background(), userReq("hi", false), "bearer")
		if !dec.Allow {
			t.Fatalf("a call under its seat cap was refused: status=%d reason=%q", dec.Status, dec.Reason)
		}
		h, rows := theOneHold(t, st, tenant)
		requireComponents(t, rows, "spend_limit")

		d.Finalize(context.Background(), dec.Session, proxyRanResult())
		requireSettled(t, st, tenant, h, "committed", publishedCost(t, bus), "spend_limit")
	})
}

// TestProxyBatchDenyReleasesEarlier: the entries of a batch are admitted one by one, and an
// entry the budget refuses refuses the whole submission. The entries admitted before it
// took their holds; none of them runs, so each hold is released with an actual of zero and
// its admission row with it. The refused entry holds nothing.
func TestProxyBatchDenyReleasesEarlier(t *testing.T) {
	forEachFinOpsEngine(t, func(t *testing.T, cfg store.Config) {
		d, st, tenant, actor, _ := proxyOverLedger(t, cfg)
		createBudgetPolicy(t, st, tenant, "gateway-cap", globalBudget(20_000_000))
		createSeatSpendLimit(t, st, tenant, actor, 10_000_000)
		// Any amount crosses this cap, and it scopes only the second entry's model.
		createBudgetPolicy(t, st, tenant, "haiku-cap", map[string]any{
			"dimension": "model", "key": "claude-haiku-4-5", "period": "monthly",
			"limit_micro_usd": int64(1), "action": "block",
		})

		dec := d.AuthorizeBatch(context.Background(), batchReqs("claude-opus-4-8", "claude-haiku-4-5"), "bearer")
		if dec.Allow || dec.Status != http.StatusPaymentRequired {
			t.Fatalf("a batch whose second entry is over its cap must be refused 402; got allow=%v status=%d reason=%q",
				dec.Allow, dec.Status, dec.Reason)
		}
		h, rows := theOneHold(t, st, tenant)
		requireComponents(t, rows, "budget", "spend_limit")
		requireSettled(t, st, tenant, h, "released", 0, "budget", "spend_limit")
	})
}

// proxyOverLedger is the real decider over the real admission ledger: the budget seam is
// the FinOps module on an open store, every other seam an allow-all fake. The caller is a
// member of the ledger's tenant, and the bus records what Finalize publishes.
func proxyOverLedger(t *testing.T, cfg store.Config) (*inferencepep.Decider, store.Store, model.TenantID, string, *fakeObservationBus) {
	t.Helper()
	fin, st, tenant := openFinOpsEngineOn(t, cfg)
	a, mg, bg, kg, pol := allowAll()
	a.p = auth.ScopedPrincipal(model.ID("u1"), "user one", tenant, "editor")
	d := newTestDecider(a, mg, bg, kg, pol)
	d.Budget = fin
	d.Inference = claudeapi.NewInference(claudeapi.InferenceConfig{APIKey: "test", DefaultModel: "claude-opus-4-8", Gateway: sdkmodel.GatewayDirect})
	bus := &fakeObservationBus{}
	d.Bus = bus
	return d, st, tenant, a.p.Actor(), bus
}

// globalBudget is a monthly blocking budget over all spend.
func globalBudget(limit int64) map[string]any {
	return map[string]any{"dimension": "global", "period": "monthly", "limit_micro_usd": limit, "action": "block"}
}

// createSeatSpendLimit writes a daily spend limit on one seat, as the spend-limit API
// stores it.
func createSeatSpendLimit(t *testing.T, st store.Store, tenant model.TenantID, actor string, limit int64) {
	t.Helper()
	if err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		_, err := sc.Policies().Create(context.Background(), model.Policy{
			Name: "seat-cap", Kind: "spend_limit", Enabled: true,
			Spec: map[string]any{
				"scope_type": "user", "scope_key": actor, "amount_micro_usd": limit,
				"unlimited": false, "period": "daily",
			},
		})
		return err
	}); err != nil {
		t.Fatalf("create spend limit: %v", err)
	}
}

// proxyRanResult is the forward result of a call that ran: a response with usage to price.
func proxyRanResult() claudeapi.ProxyForwardResult {
	return claudeapi.ProxyForwardResult{Response: claudeapi.MessageResponse{
		ID: "msg-1", Model: "claude-opus-4-8", StopReason: "end_turn",
		Usage: claudeapi.MessageUsage{InputTokens: 100, OutputTokens: 20},
	}}
}

// publishedCost is the sum of the cost samples on the bus: the call's measured cost.
func publishedCost(t *testing.T, bus *fakeObservationBus) int64 {
	t.Helper()
	var sum int64
	for _, c := range bus.costs() {
		sum += c.CostMicroUSD
	}
	if sum <= 0 {
		t.Fatalf("the call published a cost of %d µUSD; the fixture must price the call", sum)
	}
	return sum
}

// theOneHold returns the one hold the tenant's ledger rows name, and those rows. Every row
// must carry it: one call, one hold.
func theOneHold(t *testing.T, st store.Store, tenant model.TenantID) (string, []model.Record) {
	t.Helper()
	rows := listFinOpsRows(t, st, tenant, finopsReservationKind)
	if len(rows) == 0 {
		t.Fatal("the ledger holds no reservation row: the call took no hold")
	}
	h := rows[0].String("handle")
	for _, r := range rows {
		if r.String("handle") != h || h == "" {
			t.Fatalf("the ledger rows name holds %q and %q, want the one hold of the call", h, r.String("handle"))
		}
	}
	return h, rows
}

// requireComponents asserts the rows are exactly one per policy kind named, in any order.
func requireComponents(t *testing.T, rows []model.Record, kinds ...string) {
	t.Helper()
	got := map[string]int{}
	for _, r := range rows {
		got[r.String("policy_kind")]++
	}
	want := map[string]int{}
	for _, k := range kinds {
		want[k]++
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("the hold's rows by kind = %v, want %v", got, want)
	}
}

// requireSettled asserts every ledger row of the tenant is under h, in state, with actual,
// one per kind named, and that the one admission row naming h is in state too.
func requireSettled(t *testing.T, st store.Store, tenant model.TenantID, h, state string, actual int64, kinds ...string) {
	t.Helper()
	rows := listFinOpsRows(t, st, tenant, finopsReservationKind)
	requireComponents(t, rows, kinds...)
	for _, r := range rows {
		if r.String("handle") != h || r.String("state") != state || r.Int("actual_micro_usd") != actual {
			t.Fatalf("the hold's %s row = handle %q state %q actual %d, want %q %s %d",
				r.String("policy_kind"), r.String("handle"), r.String("state"), r.Int("actual_micro_usd"), h, state, actual)
		}
	}
	var naming []model.Record
	for _, r := range listFinOpsRows(t, st, tenant, finopsAdmissionKind) {
		if r.String("handle") == h {
			naming = append(naming, r)
		}
	}
	if len(naming) != 1 {
		t.Fatalf("%d admission row(s) name the hold, want exactly one", len(naming))
	}
	if r := naming[0]; r.String("state") != state || !strings.HasPrefix(r.String("idempotency_key"), "model_gateway/") {
		t.Fatalf("the admission row naming the hold = key %q state %q, want a model_gateway key, %s",
			r.String("idempotency_key"), r.String("state"), state)
	}
}
