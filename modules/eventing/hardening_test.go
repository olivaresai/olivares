// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package eventing

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/olivaresai/olivares/connectors/webhook"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// hardening_test.go pins the behaviors the adversarial review surfaced:
// the disabled-park of a STALE claim, the late-writer ownership guard, the
// full capture→delivery flow of every cataloged type, rotation semantics for
// queued deliveries, narrowing semantics, and the deliveries-list RBAC filter.

// forceStaleDelivering rewinds the single delivery row into a stale
// "delivering" claim (the crashed-node state TestStaleClaimRescue uses).
func (h *harness) forceStaleDelivering(tenant model.TenantID) {
	h.t.Helper()
	h.forceDelivering(tenant, time.Hour)
}

// forceFreshDelivering puts the single delivery row into a lease taken JUST NOW — the
// state of a worker that is still inside its attempt, the one scanDue must refuse to touch.
func (h *harness) forceFreshDelivering(tenant model.TenantID) {
	h.t.Helper()
	h.forceDelivering(tenant, 0)
}

// forceDelivering stamps the single delivery row as claimed, with the lease taken age ago.
// Age is the whole difference between a lease worth rescuing and one still being honored:
// the stale-claim scan is bounded by m.staleClaim, and nothing else distinguishes them.
func (h *harness) forceDelivering(tenant model.TenantID, age time.Duration) {
	h.t.Helper()
	if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(deliveryKind)
		if err != nil {
			return err
		}
		recs, _, err := repo.List(context.Background(), model.Query{Limit: 1})
		if err != nil {
			return err
		}
		rec := recs[0]
		rec[colDelStatus] = statusDelivering
		rec[colDelLastAt] = model.NewTimestamp(h.clk.Now().Time().Add(-age)).String()
		_, err = repo.Update(context.Background(), rec)
		return err
	}); err != nil {
		h.t.Fatal(err)
	}
}

// A dispatch pass must leave a FRESH claim alone. Two independent layers say so, and
// this test asserts the PROPERTY rather than either implementation of it:
//
//   - scanDue never offers one up — it lists queued rows that are due, plus
//     "delivering" rows whose claim has gone stale, and nothing else.
//   - claim refuses it again inside the transaction, which is where the decision
//     actually binds under concurrency.
//
// Blinding either layer alone changes nothing observable (measured: the other one
// still holds). Blinding both makes this test fail on all three of its assertions.
// That is the point of asserting the property instead of the predicate.
//
// In production what it protects is lease integrity: a pass that took a lease another
// worker still holds would send the same event a second time and spend a rung of the
// retry ladder on a failure that had not been resolved yet.
//
// In the tests it is why every ladder-driving test waits for the OUTCOME of an attempt
// and not for the attempt to start. A pass issued over a fresh lease does nothing, so the
// clock advance that carried it is spent for nothing, and the ladder can finish short of
// dead. TestS287_DeadLetterList_FiltersStatusDead used to gate on "delivering" and went
// intermittent under CPU contention for precisely this.
//
// Note what this test does NOT guard: it is about the dispatcher, so it stays green if a
// test's own gate goes back to accepting "delivering". The predicate itself is pinned
// separately and deterministically by TestFirstAttemptCommittedRejectsALeaseInFlight.
func TestFreshClaimIsNotDispatched(t *testing.T) {
	h := newHarness(t)
	rc := newReceiver(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	rc.setStatus(http.StatusInternalServerError)
	h.createSubscription(editor, tenant, map[string]any{
		"name": "findings", "event_types": []string{"finding.reported"},
		"endpoint": rc.srv.URL,
	})
	h.publishFinding(tenant, "module:security", "guardrail", "t")
	h.waitAttempts(editor, tenant, 1) // first attempt failed and requeued

	sent := rc.count()
	h.forceFreshDelivering(tenant)

	// Move the clock past the retry backoff so "not due" cannot be the reason nothing
	// happens — but stay well inside defaultStaleClaim, because a claim older than that
	// is a CRASHED node and scanDue is right to rescue it (TestStaleClaimRescue). The
	// only thing that may hold this row back is that the claim is live.
	const advance = time.Second
	if advance >= defaultStaleClaim {
		t.Fatalf("premise broken: advancing %s ages the claim past defaultStaleClaim (%s)", advance, defaultStaleClaim)
	}
	h.clk.advance(advance)
	h.dispatch(tenant)

	if got := rc.count(); got != sent {
		t.Errorf("receiver saw %d requests after the pass, want %d — the pass stole a live claim", got, sent)
	}
	ds := h.deliveries(editor, tenant, "")
	if len(ds) != 1 {
		t.Fatalf("delivery rows = %d, want 1", len(ds))
	}
	if got := ds[0]["attempts"].(float64); got != 1 {
		t.Errorf("attempts = %v after the pass, want 1 — a rung was spent on a claim in flight", got)
	}
	if got := ds[0]["status"]; got != statusDelivering {
		t.Errorf("status = %v, want %q — the pass rewrote a row it does not own", got, statusDelivering)
	}
}

// A stale "delivering" claim whose subscription is DISABLED is parked back to
// queued (governed by the due predicate) — it must not re-match the stale
// rescue scan on every pass and churn writes forever.
func TestDisabledStaleClaimParksAsQueued(t *testing.T) {
	h := newHarness(t)
	rc := newReceiver(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	id, _ := h.createSubscription(editor, tenant, map[string]any{
		"name": "findings", "event_types": []string{"finding.reported"},
		"endpoint": rc.srv.URL,
	})
	h.publishFinding(tenant, "module:security", "guardrail", "t")
	h.waitAttempts(editor, tenant, 1)

	if r := h.do("PUT", "/v1/m/eventing/subscriptions/"+id, editor, map[string]any{
		"name": "findings", "event_types": []string{"finding.reported"},
		"endpoint": rc.srv.URL, "enabled": false,
	}, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("disable = %d %s", r.code, r.raw)
	}
	h.forceStaleDelivering(tenant)

	attempts := rc.count()
	h.dispatch(tenant)
	d := h.deliveries(editor, tenant, "")[0]
	if d["status"] != "queued" || d["last_status"] != "subscription_disabled" {
		t.Fatalf("stale+disabled claim = %v/%v, want queued/subscription_disabled", d["status"], d["last_status"])
	}
	if rc.count() != attempts {
		t.Errorf("a disabled subscription must never be attempted")
	}
	// The very next pass must NOT touch the row again (it is parked on the due
	// predicate, not perpetually re-matched by the stale scan).
	before := d["next_attempt_at"]
	h.dispatch(tenant)
	after := h.deliveries(editor, tenant, "")[0]
	if after["next_attempt_at"] != before || after["status"] != "queued" {
		t.Errorf("an immediately following pass must not rewrite the parked row: %v -> %v", before, after["next_attempt_at"])
	}
}

// A writer that outlived its claim (a rescuer or an admin redeliver bumped the
// row version) must NOT clobber the new owner's state.
func TestLateOutcomeWriterBacksOff(t *testing.T) {
	h := newHarness(t)
	rc := newReceiver(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	h.createSubscription(editor, tenant, map[string]any{
		"name": "findings", "event_types": []string{"finding.reported"},
		"endpoint": rc.srv.URL,
	})
	h.publishFinding(tenant, "module:security", "guardrail", "t")
	h.waitAttempts(editor, tenant, 1)
	d := h.deliveries(editor, tenant, "status=delivered")[0]
	id := model.ID(d["id"].(string))

	// The admin requeues the delivered row (a new owner; version bumps).
	if r := h.do("POST", "/v1/m/eventing/deliveries/"+d["id"].(string)+"/redeliver", admin, nil, tenantHdr(tenant)); r.code != http.StatusAccepted {
		t.Fatalf("redeliver = %d %s", r.code, r.raw)
	}
	// A writer holding a STALE claim version tries to record its outcome: the
	// ownership guard must refuse (status is queued and the version moved on).
	h.mod.finishOwned(context.Background(), tenant, attempt{
		deliveryID: id, attempts: 99, claimVersion: 1, // long-superseded claim
	}, statusDead, "late_writer")
	got := h.deliveries(editor, tenant, "")[0]
	if got["status"] == "dead" || got["last_status"] == "late_writer" {
		t.Fatalf("a late writer clobbered the requeued row: %v", got)
	}
}

// Every cataloged type flows capture→signed delivery end to end (the
// guardrail/approval/policy/cost paths are not just catalog entries).
func TestAllCatalogTypesFlow(t *testing.T) {
	h := newHarness(t)
	rc := newReceiver(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	_, secret := h.createSubscription(editor, tenant, map[string]any{
		"name": "everything", "role": "editor",
		"event_types": []string{
			"edge.observed", "cost.sampled", "finding.reported",
			"guardrail.observed", "approval.requested", "approval.resolved", "policy.changed",
		},
		"endpoint": rc.srv.URL,
	})

	when := time.Date(2026, 6, 11, 11, 0, 0, 0, time.UTC)
	publish := func(e event.Event) {
		h.t.Helper()
		if err := h.bus.Publish(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}
	h.publishEdge(tenant, "connector:pg")
	publish(event.FromObservation(tenant.String(), "connector:anthropic", sdkmodel.CostSample{
		ProviderRef: "anthropic", ModelRef: "claude-fable-5",
		InputTokens: 100, OutputTokens: 50, CostMicroUSD: 1234, OccurredAt: when,
	}))
	h.publishFinding(tenant, "module:security", "guardrail", "f")
	publish(event.GuardrailObserved(tenant.String(), "module:liveingest", event.ObservedText{
		Surface: "tool_args", Text: "[REDACTED] excerpt",
	}))
	publish(event.ApprovalRequested(tenant.String(), "module:governance", when, event.ApprovalRequest{
		ApprovalID: "ap-1", Action: "deploy", SubjectKind: "deployment",
		RiskTier: "critical", RequiredApprovals: 2,
	}))
	publish(event.ApprovalResolved(tenant.String(), "module:governance", when, event.ApprovalResolution{
		ApprovalID: "ap-1", Action: "deploy", SubjectKind: "deployment",
		RiskTier: "critical", Outcome: "approved", RequiredApprovals: 2,
		ApproveCount: 2,
	}))
	publish(event.PolicyChanged(tenant.String(), "module:governance", when, event.PolicyChange{
		PolicyID: "pol-1", Kind: "abac", Op: event.PolicyOpUpdated, Enabled: true,
	}))

	waitFor(t, "all seven bus types to deliver", func() bool { return rc.count() == 7 })
	seen := map[string]capturedReq{}
	for _, req := range rc.all() {
		seen[req.header.Get(headerEventType)] = req
	}
	for _, typ := range []string{"edge.observed", "cost.sampled", "finding.reported", "guardrail.observed", "approval.requested", "approval.resolved", "policy.changed"} {
		req, ok := seen[typ]
		if !ok {
			t.Errorf("type %s never delivered", typ)
			continue
		}
		if !webhook.Verify(secret, req.header.Get(headerTimestamp), req.header.Get(headerSignature), req.body) {
			t.Errorf("type %s delivery signature does not verify", typ)
		}
	}
	// The governance payloads round-trip their typed fields; the zero
	// ExpiresAt/EscalateAt are ABSENT on the wire (omitzero), never year-1.
	var apEnv struct{ Payload map[string]any }
	if err := json.Unmarshal(seen["approval.requested"].body, &apEnv); err != nil {
		t.Fatal(err)
	}
	if apEnv.Payload["ApprovalID"] != "ap-1" || apEnv.Payload["RiskTier"] != "critical" {
		t.Errorf("approval payload = %v", apEnv.Payload)
	}
	if _, present := apEnv.Payload["ExpiresAt"]; present {
		t.Error("zero ExpiresAt must be absent on the wire, not a year-1 sentinel")
	}
	var arEnv struct{ Payload map[string]any }
	if err := json.Unmarshal(seen["approval.resolved"].body, &arEnv); err != nil {
		t.Fatal(err)
	}
	if arEnv.Payload["ApprovalID"] != "ap-1" || arEnv.Payload["Outcome"] != "approved" || arEnv.Payload["ApproveCount"] != float64(2) {
		t.Errorf("approval resolution payload = %v", arEnv.Payload)
	}
	if _, present := arEnv.Payload["DecidedAt"]; present {
		t.Error("zero DecidedAt must be absent on the wire, not a year-1 sentinel")
	}
	var pcEnv struct{ Payload map[string]any }
	if err := json.Unmarshal(seen["policy.changed"].body, &pcEnv); err != nil {
		t.Fatal(err)
	}
	if pcEnv.Payload["Op"] != "updated" || pcEnv.Payload["Enabled"] != true {
		t.Errorf("policy payload = %v", pcEnv.Payload)
	}
}

// Rotating the secret mid-queue: deliveries attempted AFTER the rotation sign
// with the NEW secret (the platform holds exactly one signing secret per
// subscription — consumers update their verifier first, then rotate).
func TestRotateSecretSignsQueuedRetriesWithNewSecret(t *testing.T) {
	h := newHarness(t)
	rc := newReceiver(t)
	rc.setStatus(http.StatusServiceUnavailable)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	id, oldSecret := h.createSubscription(editor, tenant, map[string]any{
		"name": "findings", "event_types": []string{"finding.reported"},
		"endpoint": rc.srv.URL,
	})
	h.publishFinding(tenant, "module:security", "guardrail", "t")
	h.waitAttempts(editor, tenant, 1) // first attempt failed, retry queued

	r := h.do("POST", "/v1/m/eventing/subscriptions/"+id+"/rotate-secret", editor, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("rotate = %d %s", r.code, r.raw)
	}
	newSecret := r.body["secret"].(string)

	rc.setStatus(http.StatusOK)
	h.clk.advance(time.Second)
	h.dispatch(tenant)
	waitFor(t, "retry to land", func() bool {
		return len(h.deliveries(editor, tenant, "status=delivered")) == 1
	})
	reqs := rc.all()
	last := reqs[len(reqs)-1]
	if !webhook.Verify(newSecret, last.header.Get(headerTimestamp), last.header.Get(headerSignature), last.body) {
		t.Error("a post-rotation attempt must sign with the NEW secret")
	}
	if webhook.Verify(oldSecret, last.header.Get(headerTimestamp), last.header.Get(headerSignature), last.body) {
		t.Error("the old secret must stop signing immediately after rotation")
	}
}

// Narrowing a subscription's event_types does NOT recall already-queued
// deliveries of the removed type: what was captured for the subscription is
// delivered (the per-event RBAC filter still applies). The filter change
// governs CAPTURE from that point on.
func TestNarrowingTypesKeepsQueuedDeliveries(t *testing.T) {
	h := newHarness(t)
	rc := newReceiver(t)
	rc.setStatus(http.StatusServiceUnavailable)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	id, _ := h.createSubscription(editor, tenant, map[string]any{
		"name": "both", "role": "editor",
		"event_types": []string{"finding.reported", "edge.observed"},
		"endpoint":    rc.srv.URL,
	})
	h.publishEdge(tenant, "connector:pg")
	h.waitAttempts(editor, tenant, 1) // edge delivery queued for retry

	// Narrow to findings only; the queued edge delivery stays live.
	if r := h.do("PUT", "/v1/m/eventing/subscriptions/"+id, editor, map[string]any{
		"name": "both", "role": "editor", "event_types": []string{"finding.reported"},
		"endpoint": rc.srv.URL,
	}, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("narrow = %d %s", r.code, r.raw)
	}
	rc.setStatus(http.StatusOK)
	h.clk.advance(time.Second)
	h.dispatch(tenant)
	waitFor(t, "queued edge delivery to land", func() bool {
		return len(h.deliveries(editor, tenant, "status=delivered")) == 1
	})
	// New edge events are no longer captured for it. The barrier is what makes this a
	// measurement rather than a coincidence: Publish does not wait for handlers
	// (core/eventbus/bus.go), so without it the count below can be 1 because the capture
	// handler has not run yet — the assertion would hold for a reason that has nothing to do
	// with narrowing, and would keep holding if narrowing broke.
	h.publishEdge(tenant, "connector:pg")
	h.busBarrier()
	h.dispatch(tenant)
	if got := len(h.deliveries(editor, tenant, "")); got != 1 {
		t.Errorf("a narrowed subscription must not capture the removed type: %d rows", got)
	}
}

// The deliveries list applies the caller's per-type RBAC exactly like the
// event log: a viewer does not learn that privileged-type deliveries exist,
// and asking for the forbidden type explicitly is a 403.
func TestDeliveriesListRBACFiltered(t *testing.T) {
	h := newHarness(t)
	rc := newReceiver(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")
	viewer := h.roleToken(admin, tenant, "v@x.io", "viewer")

	h.createSubscription(editor, tenant, map[string]any{
		"name": "both", "role": "editor",
		"event_types": []string{"finding.reported", "edge.observed"},
		"endpoint":    rc.srv.URL,
	})
	h.publishFinding(tenant, "module:security", "guardrail", "f")
	h.publishEdge(tenant, "connector:pg")
	waitFor(t, "both delivered", func() bool { return rc.count() == 2 })

	if got := len(h.deliveries(editor, tenant, "")); got != 2 {
		t.Fatalf("editor sees %d deliveries, want 2", got)
	}
	vds := h.deliveries(viewer, tenant, "")
	if len(vds) != 1 || vds[0]["event_type"] != "finding.reported" {
		t.Errorf("viewer must see only finding.reported deliveries: %v", vds)
	}
	if r := h.do("GET", "/v1/m/eventing/deliveries?event_type=edge.observed", viewer, nil, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Errorf("explicit forbidden event_type = %d, want 403", r.code)
	}
}

// TestDuplicateEventIDCapturedOnce pins the exactly-once-capture
// property: the SAME bus event (same event id) delivered twice — the shape of
// the NATS bridge handing one event to two nodes inside the leader-failover
// overlap, or any future at-least-once transport — captures ONE event row and
// ONE delivery set. The second capture sees the existing (tenant_id, event_id)
// row and returns as already-done.
func TestDuplicateEventIDCapturedOnce(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")
	h.createSubscription(editor, tenant, map[string]any{
		"name": "findings", "event_types": []string{"finding.reported"},
		"endpoint": "https://example.invalid/hook",
	})

	mk := func(id, title string) event.Event {
		f := sdkmodel.FindingReport{
			Kind: "guardrail", Severity: sdkmodel.SeverityHigh, SubjectKind: "agent",
			SubjectRef: "agent-1", Title: title, OccurredAt: time.Date(2026, 6, 11, 11, 0, 0, 0, time.UTC),
		}
		e := event.FromObservation(tenant.String(), "module:security", f)
		e.ID = id // the stable bus id every duplicate delivery carries
		return e
	}

	dup := mk("11111111-2222-3333-4444-555555555555", "dup")
	if err := h.bus.Publish(context.Background(), dup); err != nil {
		t.Fatalf("publish 1: %v", err)
	}
	waitFor(t, "first capture", func() bool {
		return len(h.deliveries(editor, tenant, "")) == 1
	})
	if err := h.bus.Publish(context.Background(), dup); err != nil {
		t.Fatalf("publish duplicate: %v", err)
	}
	// A later sentinel event proves the capture pipeline drained PAST the
	// duplicate (per-subscriber FIFO), with no sleeps.
	if err := h.bus.Publish(context.Background(), mk("66666666-7777-8888-9999-000000000000", "sentinel")); err != nil {
		t.Fatalf("publish sentinel: %v", err)
	}
	waitFor(t, "sentinel capture", func() bool {
		return len(h.deliveries(editor, tenant, "")) >= 2
	})
	if got := len(h.deliveries(editor, tenant, "")); got != 2 {
		t.Fatalf("duplicate event id must capture once: want 2 delivery rows (original + sentinel), got %d", got)
	}
}

// TestFileMigrationApplies pins that the module's file-migration seam actually
// EXECUTES: the loader reads per-engine dirs at the FS root and silently
// no-ops when they are absent, so an embed.FS handed over with its
// "migrations/" prefix un-stripped would swallow the eventing_event_id_uniq
// index on every UPGRADED estate (fresh installs mask it via the descriptor
// index). The tracking table existing with version 1 proves migrate.Apply ran.
func TestFileMigrationApplies(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "mig.db")
	mod := New()
	st, err := engine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: dsn}, mod.RegisterSchema)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("reopen raw: %v", err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM schema_migrations_mod_eventing WHERE version = 1`).Scan(&n); err != nil {
		t.Fatalf("the module migration tracking table must exist (the migration never ran?): %v", err)
	}
	if n != 1 {
		t.Fatalf("migration 0001 must be tracked as applied, got %d rows", n)
	}
	// And its effect: the unique index is present on the event table.
	var idx int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name = 'eventing_event_id_uniq'`).Scan(&idx); err != nil {
		t.Fatalf("index lookup: %v", err)
	}
	if idx != 1 {
		t.Fatal("eventing_event_id_uniq must exist after Open")
	}
}

// The predicate the ladder-driving tests gate on, pinned as a table so a regression is one
// deterministic failure rather than an intermittent one.
//
// This is deliberately separate from TestFreshClaimIsNotDispatched, which asserts what the
// DISPATCHER does. That test stays green if someone puts "delivering" back into the gate —
// the production behavior is unchanged by a test's predicate — so it is not a guard for
// this. This is: restoring "delivering" makes the first row below fail, with no load, no
// scheduling and no wall clock involved.
func TestFirstAttemptCommittedRejectsALeaseInFlight(t *testing.T) {
	row := func(status string, attempts float64) map[string]any {
		return map[string]any{"status": status, "attempts": attempts}
	}
	for _, tc := range []struct {
		name string
		ds   []map[string]any
		want bool
	}{
		// THE regression case: the tenant of TestS287_DeadLetterList_FiltersStatusDead at the
		// instant the old gate released. An earlier row succeeded; the row under test is still
		// leased. Nothing has committed, and saying otherwise wastes the next clock advance.
		{"an earlier delivery plus a lease in flight", []map[string]any{
			row("delivered", 1), row("delivering", 1)}, false},
		{"an earlier delivery plus a requeued row", []map[string]any{
			row("delivered", 1), row("queued", 1)}, true},
		{"an earlier delivery plus a row not yet attempted", []map[string]any{
			row("delivered", 1), row("queued", 0)}, false},
		{"a lease in flight, alone", []map[string]any{row("delivering", 1)}, false},
		{"a terminal row ends the wait", []map[string]any{row("dead", 3)}, true},
		{"a delivered row is not the row being driven", []map[string]any{row("delivered", 1)}, false},
		{"no rows at all", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := firstAttemptCommitted(tc.ds); got != tc.want {
				t.Errorf("firstAttemptCommitted = %v, want %v", got, tc.want)
			}
		})
	}
}
