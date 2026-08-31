// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package notify

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// flakyDispatcher fails its first failFirst Deliver calls (a transient connector
// outage) then succeeds, recording the idempotency key seen on every attempt so a
// test can prove it is STABLE across retries.
type flakyDispatcher struct {
	mu        sync.Mutex
	dest      string
	failFirst int
	calls     int
	keys      []string
	err       error // when non-nil AND permanent, always returned (never succeeds)
	permanent bool
}

func (d *flakyDispatcher) Destinations() []string { return []string{d.dest} }

func (d *flakyDispatcher) DestinationsFor(model.TenantID) []string { return []string{d.dest} }

func (d *flakyDispatcher) Deliver(_ context.Context, _ model.TenantID, dest string, n sdk.Notification) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls++
	d.keys = append(d.keys, n.Fields[sdk.IdempotencyKeyField])
	if dest != d.dest {
		return ErrUnknownDestination
	}
	if d.permanent {
		return d.err
	}
	if d.calls <= d.failFirst {
		return errors.New("transient connector outage")
	}
	return nil
}

func (d *flakyDispatcher) ConnectorFingerprint(dest string) (string, bool) {
	return "conn:" + dest, true
}

func (d *flakyDispatcher) callCount() int { d.mu.Lock(); defer d.mu.Unlock(); return d.calls }
func (d *flakyDispatcher) recover()       { d.mu.Lock(); d.permanent = false; d.mu.Unlock() }
func (d *flakyDispatcher) seenKeys() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.keys...)
}

// outboxRows lists all notify_outbox rows for a tenant (white-box state inspection).
func (h *harness) outboxRows(tenant model.TenantID) []model.Record {
	h.t.Helper()
	var rows []model.Record
	err := h.st.View(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(outboxKind)
		if err != nil {
			return err
		}
		rows, _, err = repo.List(context.Background(), model.Query{})
		return err
	})
	if err != nil {
		h.t.Fatalf("list outbox: %v", err)
	}
	return rows
}

// tinyRetryHarness builds a harness whose outbox backoff is milliseconds and whose
// stale-claim window is short, so the deterministic clock drives the ladders fast.
func tinyRetryHarness(t *testing.T, disp Dispatcher) *harness {
	return newHarness(t,
		WithDispatcher(disp),
		WithOutboxTuning([]time.Duration{20 * time.Millisecond, 20 * time.Millisecond}, time.Minute, 0),
	)
}

func routeAndFinding(h *harness) (model.TenantID, string) {
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")
	h.mustCreateRoute(editor, tenant, map[string]any{"name": "sec", "destination": "d1", "match_kinds": []string{"security_*"}})
	return tenant, editor
}

func TestOutbox_RetryThenDeliverWithStableKey(t *testing.T) {
	disp := &flakyDispatcher{dest: "d1", failFirst: 1}
	h := tinyRetryHarness(t, disp)
	tenant, editor := routeAndFinding(h)

	// publishFinding enqueues AND auto-pumps once: attempt 1 fails (transient) → queued.
	h.publishFinding(tenant, securitySource, finding("security_guardrail", sdkmodel.SeverityHigh, "agent", "a1", "t"))
	if got := disp.callCount(); got != 1 {
		t.Fatalf("after first pump: want 1 delivery attempt, got %d", got)
	}
	if term := h.terminalDeliveries(editor, tenant, ""); len(term) != 0 {
		t.Fatalf("a failed transient attempt must not be terminal yet, got %v", term)
	}
	rows := h.outboxRows(tenant)
	if len(rows) != 1 || rows[0].String(colObStatus) != obStatusQueued {
		t.Fatalf("outbox should be queued for retry, got %v", rows)
	}

	// Advance past the backoff and pump again: attempt 2 succeeds.
	h.clk.advance(100 * time.Millisecond)
	h.pumpOutbox(tenant)
	if got := disp.callCount(); got != 2 {
		t.Fatalf("after retry: want 2 attempts, got %d", got)
	}
	term := h.terminalDeliveries(editor, tenant, "")
	if len(term) != 1 || term[0]["status"] != statusDelivered {
		t.Fatalf("want 1 terminal delivered row, got %v", term)
	}
	if rows := h.outboxRows(tenant); rows[0].String(colObStatus) != obStatusDelivered {
		t.Errorf("outbox status = %q, want delivered", rows[0].String(colObStatus))
	}
	// The idempotency key must be identical across both attempts (and be the row id).
	keys := disp.seenKeys()
	if len(keys) != 2 || keys[0] == "" || keys[0] != keys[1] {
		t.Fatalf("idempotency key must be stable + non-empty across retries, got %v", keys)
	}
	if keys[0] != h.outboxRows(tenant)[0].String(model.ColID) {
		t.Errorf("idempotency key %q != outbox row id", keys[0])
	}
}

func TestOutbox_DeadLetterAfterExhaustion(t *testing.T) {
	disp := &flakyDispatcher{dest: "d1", permanent: true, err: errors.New("always down")}
	h := tinyRetryHarness(t, disp) // 2 retries → 3 attempts total, then dead
	tenant, editor := routeAndFinding(h)

	h.publishFinding(tenant, securitySource, finding("security_guardrail", sdkmodel.SeverityHigh, "agent", "a1", "t"))
	// Drive the ladder to exhaustion.
	for i := 0; i < 5; i++ {
		h.clk.advance(100 * time.Millisecond)
		h.pumpOutbox(tenant)
	}
	rows := h.outboxRows(tenant)
	if len(rows) != 1 || rows[0].String(colObStatus) != obStatusDead {
		t.Fatalf("outbox must be dead-lettered after exhaustion, got %v", rows)
	}
	if got := rows[0].Int(colObAttempts); got != 3 {
		t.Errorf("attempts = %d, want 3 (initial + 2 retries)", got)
	}
	term := h.terminalDeliveries(editor, tenant, "")
	if len(term) != 1 || term[0]["status"] != statusFailed {
		t.Fatalf("dead-letter must append a terminal failed ledger row, got %v", term)
	}
}

func TestOutbox_UnknownDestinationDeadNotRetried(t *testing.T) {
	disp := newFakeDispatcher("d1")
	disp.failOn["d1"] = ErrUnknownDestination
	h := tinyRetryHarness(t, disp)
	tenant, editor := routeAndFinding(h)

	h.publishFinding(tenant, securitySource, finding("security_guardrail", sdkmodel.SeverityHigh, "agent", "a1", "t"))
	rows := h.outboxRows(tenant)
	if len(rows) != 1 || rows[0].String(colObStatus) != obStatusDead {
		t.Fatalf("unknown destination must dead-letter immediately, got %v", rows)
	}
	if got := rows[0].Int(colObAttempts); got != 1 {
		t.Errorf("attempts = %d, want 1 (a deterministic reject is never retried)", got)
	}
	term := h.terminalDeliveries(editor, tenant, "")
	if len(term) != 1 || term[0]["status"] != statusUnknownDest {
		t.Fatalf("ledger must record unknown_destination, got %v", term)
	}
	// Pumping again must NOT produce another attempt (it is terminal).
	h.clk.advance(time.Hour)
	h.pumpOutbox(tenant)
	if rows := h.outboxRows(tenant); rows[0].Int(colObAttempts) != 1 {
		t.Errorf("a dead row must never be re-attempted, attempts=%d", rows[0].Int(colObAttempts))
	}
}

// outboxList GETs the durable outbox (optionally filtered) via the operator API.
func (h *harness) outboxList(token string, tenant model.TenantID, query string) []map[string]any {
	h.t.Helper()
	path := "/v1/m/notify/outbox"
	if query != "" {
		path += "?" + query
	}
	r := h.do("GET", path, token, nil, tenantHdr(tenant))
	if r.code != 200 {
		h.t.Fatalf("list outbox = %d %s", r.code, r.raw)
	}
	items, _ := r.body["items"].([]any)
	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		if m, ok := it.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func TestOutbox_DLQListAndRedeliver(t *testing.T) {
	disp := &flakyDispatcher{dest: "d1", permanent: true, err: errors.New("down")}
	h := tinyRetryHarness(t, disp)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	adminTok := h.roleToken(admin, tenant, "a@x.io", "admin") // redeliver is admin-tier
	h.mustCreateRoute(adminTok, tenant, map[string]any{"name": "sec", "destination": "d1", "match_kinds": []string{"security_*"}})

	// Drive one delivery to the dead-letter queue.
	h.publishFinding(tenant, securitySource, finding("security_guardrail", sdkmodel.SeverityHigh, "agent", "a1", "t"))
	for i := 0; i < 5; i++ {
		h.clk.advance(100 * time.Millisecond)
		h.pumpOutbox(tenant)
	}
	dead := h.outboxList(adminTok, tenant, "status=dead")
	if len(dead) != 1 {
		t.Fatalf("DLQ view (?status=dead) should show 1 row, got %d: %v", len(dead), dead)
	}
	obID, _ := dead[0]["id"].(string)
	if obID == "" {
		t.Fatal("dead-letter row has no id")
	}

	// The destination recovers; redeliver from the DLQ, then a pump delivers it.
	disp.recover()
	r := h.do("POST", "/v1/m/notify/outbox/"+obID+"/redeliver", adminTok, nil, tenantHdr(tenant))
	if r.code != 200 {
		t.Fatalf("redeliver = %d %s", r.code, r.raw)
	}
	before := disp.callCount()
	h.pumpOutbox(tenant)
	if disp.callCount() != before+1 {
		t.Fatalf("redeliver must produce exactly one more attempt; before=%d after=%d", before, disp.callCount())
	}
	if rows := h.outboxRows(tenant); rows[0].String(colObStatus) != obStatusDelivered {
		t.Errorf("after redeliver+pump, outbox status = %q, want delivered", rows[0].String(colObStatus))
	}
	// The redeliver was an audited admin action.
	if acts := h.auditActions(tenant); !containsAction(acts, "notify.outbox.redeliver") {
		t.Errorf("redeliver must be audited; audit actions = %v", acts)
	}
}

func containsAction(acts []string, want string) bool {
	for _, a := range acts {
		if a == want {
			return true
		}
	}
	return false
}

// waitFor polls cond until true or the deadline (for asserting on async work).
func waitFor(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met within the deadline")
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// TestOutbox_NudgeDelivers proves the in-handler nudge delivers a freshly enqueued
// notification WITHOUT the periodic pump (the #4 fix: first-attempt delivery must work
// on the per-tenant handle, not depend on the cross-tenant pump enumeration). The
// harness here enables the nudge; delivery is driven only by publishing the event.
func TestOutbox_NudgeDelivers(t *testing.T) {
	disp := newFakeDispatcher("d1")
	h := newHarnessNudged(t, WithDispatcher(disp))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")
	h.mustCreateRoute(editor, tenant, map[string]any{"name": "sec", "destination": "d1", "match_kinds": []string{"security_*"}})

	// Fire via the bus WITHOUT an explicit pump — only the nudge can deliver it.
	f := finding("security_guardrail", sdkmodel.SeverityHigh, "agent", "a1", "t")
	f.OccurredAt = h.clk.now()
	if err := h.bus.Publish(context.Background(), event.FromObservation(tenant.String(), securitySource, f)); err != nil {
		t.Fatalf("publish: %v", err)
	}
	h.waitDelivery() // onEvent (route → enqueue → nudge queued) has completed
	waitFor(t, 3*time.Second, func() bool { return disp.count() == 1 })
}

func TestOutbox_StaleClaimRescue(t *testing.T) {
	disp := &flakyDispatcher{dest: "d1", failFirst: 0} // always succeeds when actually called
	h := tinyRetryHarness(t, disp)
	tenant, editor := routeAndFinding(h)

	// Enqueue WITHOUT delivering (processFinding routes+enqueues; no auto-pump), then
	// simulate a crashed claimant: flip the row to delivering with a stale last_attempt.
	f := finding("security_guardrail", sdkmodel.SeverityHigh, "agent", "a1", "t")
	e := event.Event{Type: event.TypeFindingReported, Tenant: tenant.String(), Source: securitySource, Payload: f}
	if err := h.mod.processFinding(context.Background(), tenant, e, f); err != nil {
		t.Fatalf("processFinding: %v", err)
	}
	if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(outboxKind)
		if err != nil {
			return err
		}
		rows, _, err := repo.List(context.Background(), model.Query{})
		if err != nil {
			return err
		}
		for _, r := range rows {
			r[colObStatus] = obStatusDelivering
			r[colObLastAt] = model.NewTimestamp(h.clk.now().Add(-10 * time.Minute)).String()
			if _, err := repo.Update(context.Background(), r); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("stage stale claim: %v", err)
	}

	// A pump must RESCUE the stale delivering row (crash recovery) and deliver it.
	h.pumpOutbox(tenant)
	if got := disp.callCount(); got != 1 {
		t.Fatalf("stale claim must be rescued and delivered, attempts=%d", got)
	}
	term := h.terminalDeliveries(editor, tenant, "")
	if len(term) != 1 || term[0]["status"] != statusDelivered {
		t.Fatalf("rescued delivery must record delivered, got %v", term)
	}
}
