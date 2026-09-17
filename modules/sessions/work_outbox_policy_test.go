// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestClassifyWorkEventFamily(t *testing.T) {
	t.Parallel()
	cases := map[string]WorkEventFamily{
		"work.item.created":              WorkEventFamilyWork,
		"work.lease.ended":               WorkEventFamilyWork,
		"work.owner.changed":             WorkEventFamilyWork,
		"work.decision.recorded":         WorkEventFamilyWork,
		"work.binding.observed":          WorkEventFamilyWork,
		"work.message.available":         WorkEventFamilyCommunication,
		"work.message.acknowledged":      WorkEventFamilyCommunication,
		"work.handoff.offered":           WorkEventFamilyCommunication,
		"work.handoff.carrier.available": WorkEventFamilyCommunication,
		"work.decision.request.expired":  WorkEventFamilyCommunication,
		"work.protocol.reply.available":  WorkEventFamilyProtocol,
		"work.something.new":             WorkEventFamilyUnknown,
		"":                               WorkEventFamilyUnknown,
	}
	for eventType, want := range cases {
		if got := ClassifyWorkEventFamily(eventType); got != want {
			t.Errorf("ClassifyWorkEventFamily(%q) = %q, want %q", eventType, got, want)
		}
	}
}

// insertOutboxEventForTest writes one durable event and its pending outbox row
// exactly as an apply path does, with a type of the caller's choosing.
func insertOutboxEventForTest(
	t *testing.T, f workFixture, aggregateKind model.Kind, aggregateID model.ID, seq int64, eventType string,
) model.ID {
	t.Helper()
	eventID := model.NewID()
	payload := []byte(`{"event_type":"` + eventType + `","schema_version":1}`)
	now := model.NewTimestamp(time.Now().UTC())
	// Due one second ago: the SQLite transaction clock has millisecond
	// precision, so a row due "now" with nanoseconds is not yet claimable by a
	// drain that runs within the same millisecond.
	due := model.NewTimestamp(time.Now().UTC().Add(-time.Second))
	if err := f.st.Mutate(context.Background(), f.tenant, func(sc store.Scope) error {
		events, err := sc.Ext(workEventKind)
		if err != nil {
			return err
		}
		if _, err := events.Create(context.Background(), model.Record{
			colWorkWorkspaceID: f.workspace.String(), colEventID: eventID.String(),
			colEventAggregateKind: string(aggregateKind), colEventAggregateID: aggregateID.String(),
			colEventSeq: seq, colEventType: eventType, colEventActorKind: "user", colEventActorRef: model.NewID().String(),
			colEventOccurredAt: now.String(), colEventPayload: string(payload), colEventPayloadHash: hashBytes(payload),
			colEventCommandID: model.NewID().String(), colEventAuditSeq: int64(1), colEventAuditHash: hashBytes([]byte("audit")),
		}); err != nil {
			return err
		}
		outbox, err := sc.Ext(workOutboxKind)
		if err != nil {
			return err
		}
		_, err = outbox.Create(context.Background(), model.Record{
			colWorkWorkspaceID: f.workspace.String(), colOutboxEventID: eventID.String(),
			colOutboxState: "pending", colOutboxAttempts: int64(0), colOutboxNextAttemptAt: due.String(),
			colOutboxClaimOwner: nil, colOutboxClaimUntil: nil, colOutboxPublishedAt: nil, colOutboxLastOutcome: nil,
		})
		return err
	}); err != nil {
		t.Fatalf("insert %s outbox event: %v", eventType, err)
	}
	return eventID
}

func outboxRowForTest(t *testing.T, f workFixture, eventID model.ID) model.Record {
	t.Helper()
	var found model.Record
	if err := f.st.View(context.Background(), f.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(workOutboxKind)
		if err != nil {
			return err
		}
		rows, _, err := repo.List(context.Background(), model.Query{Filters: []model.Filter{{
			Column: colOutboxEventID, Op: model.OpEq, Value: eventID.String(),
		}}, Limit: 2})
		if err != nil {
			return err
		}
		if len(rows) != 1 {
			return errors.New("outbox row count != 1")
		}
		found = rows[0]
		return nil
	}); err != nil {
		t.Fatalf("read outbox row: %v", err)
	}
	return found
}

func TestDrainWorkOutboxWithPolicyHoldsGatedFamiliesWithoutTouchingRows(t *testing.T) {
	t.Parallel()
	f := newWorkFixture(t, ":memory:", nil)
	defer f.st.Close()
	sink := &recordingWorkSink{}
	f.m.UseWorkEventSink(sink)
	// One aggregate per seeded lane: the outbox delivers an aggregate in
	// sequence, so a held event would otherwise hide the ones behind it. These
	// three creates publish their seq-1 events through the online sink.
	handoffItem := applyCreate(t, f, "handoff lane")
	messageItem := applyCreate(t, f, "message lane")
	unknownItem := applyCreate(t, f, "unknown lane")
	resetRecordingWorkSink(sink, errors.New("offline while seeding"))
	created := applyCreate(t, f, "policy lanes")
	// Reset the failed first delivery so the K1 row is immediately due again.
	// The row is read OUTSIDE the mutation: an in-memory SQLite store has one
	// connection, and a View opened inside a Mutate waits for it forever.
	setWorkOutboxClaimForTest(t, f, created.EventID, time.Unix(0, 0).UTC())
	row := outboxRowForTest(t, f, created.EventID)
	row[colOutboxState], row[colOutboxNextAttemptAt] = "pending", model.NewTimestamp(time.Unix(0, 0).UTC()).String()
	row[colOutboxClaimOwner], row[colOutboxClaimUntil] = nil, nil
	if err := f.st.Mutate(context.Background(), f.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(workOutboxKind)
		if err != nil {
			return err
		}
		_, err = repo.Update(context.Background(), row)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// Three seq-2 events on WORK ITEM aggregates (the insert guard requires the
	// aggregate to resolve; a message aggregate would need a standalone message
	// row): two K3-family types and one nobody classified. The aggregate kind
	// alone would call all of them work; the family says otherwise.
	handoff := insertOutboxEventForTest(t, f, workItemKind, handoffItem.ResultID, 2, "work.handoff.offered")
	message := insertOutboxEventForTest(t, f, workItemKind, messageItem.ResultID, 2, "work.message.available")
	unknown := insertOutboxEventForTest(t, f, workItemKind, unknownItem.ResultID, 2, "work.something.new")
	resetRecordingWorkSink(sink, nil)

	var seen []WorkOutboxCandidate
	deny := WorkOutboxClaimPolicyFunc(func(_ context.Context, c WorkOutboxCandidate) (bool, error) {
		seen = append(seen, c)
		return c.Family == WorkEventFamilyWork, nil
	})
	if err := f.m.DrainWorkOutboxWithPolicy(context.Background(), f.tenant, 100, deny); err != nil {
		t.Fatalf("gated drain: %v", err)
	}
	sink.mu.Lock()
	published := append([]WorkEventEnvelope(nil), sink.events...)
	sink.mu.Unlock()
	if len(published) != 1 || published[0].EventID != created.EventID {
		t.Fatalf("gated drain published %+v, want only the K1 item event", published)
	}
	for _, id := range []model.ID{handoff, message, unknown} {
		row := outboxRowForTest(t, f, id)
		if row.String(colOutboxState) != "pending" || row.Int(colOutboxAttempts) != 0 || row.String(colOutboxClaimOwner) != "" {
			t.Fatalf("held row %s was touched: %v", id, row)
		}
	}
	families := map[model.ID]WorkEventFamily{}
	for _, c := range seen {
		families[c.EventID] = c.Family
	}
	if families[handoff] != WorkEventFamilyCommunication || families[message] != WorkEventFamilyCommunication ||
		families[unknown] != WorkEventFamilyUnknown || families[created.EventID] != WorkEventFamilyWork {
		t.Fatalf("policy saw families %+v", families)
	}

	// A policy that cannot decide aborts the drain as UNKNOWN and claims nothing.
	failing := WorkOutboxClaimPolicyFunc(func(context.Context, WorkOutboxCandidate) (bool, error) {
		return false, errors.New("readiness witness unreachable")
	})
	err := f.m.DrainWorkOutboxWithPolicy(context.Background(), f.tenant, 100, failing)
	if workErr := asWorkError(err); workErr == nil || workErr.code != "claim_policy_unavailable" {
		t.Fatalf("failing policy = %v, want claim_policy_unavailable", err)
	}
	if row := outboxRowForTest(t, f, handoff); row.String(colOutboxState) != "pending" {
		t.Fatalf("failing policy claimed %v", row)
	}

	// Allowing the communication family releases exactly those rows; the
	// unclassified type stays held until somebody classifies it.
	allow := WorkOutboxClaimPolicyFunc(func(_ context.Context, c WorkOutboxCandidate) (bool, error) {
		return c.Family != WorkEventFamilyUnknown, nil
	})
	if err := f.m.DrainWorkOutboxWithPolicy(context.Background(), f.tenant, 100, allow); err != nil {
		t.Fatalf("allowing drain: %v", err)
	}
	for _, id := range []model.ID{handoff, message} {
		if row := outboxRowForTest(t, f, id); row.String(colOutboxState) != "published" {
			t.Fatalf("released row %s = %v", id, row)
		}
	}
	if row := outboxRowForTest(t, f, unknown); row.String(colOutboxState) != "pending" {
		t.Fatalf("unknown family row was published: %v", row)
	}
	// A STANDALONE module (no K3 composition, no authority) keeps the
	// historical unconditional drain on DrainWorkOutbox. A composed module does
	// not: see TestWorkOutboxComposedModuleWithoutAuthorityHoldsK3AndUnknown.
	if err := f.m.DrainWorkOutbox(context.Background(), f.tenant, 100); err != nil {
		t.Fatalf("unconditional drain: %v", err)
	}
	if row := outboxRowForTest(t, f, unknown); row.String(colOutboxState) != "published" {
		t.Fatalf("unconditional drain held a row: %v", row)
	}
}

// recordingOutboxAuthority is a test authority with switchable answers at
// both boundaries. It refuses unknown families like the production one.
type recordingOutboxAuthority struct {
	mu          sync.Mutex
	allowClaim  bool
	allowEffect bool
	claims      []WorkOutboxCandidate
	effects     []WorkOutboxCandidate
}

func (a *recordingOutboxAuthority) AllowWorkOutboxClaim(_ context.Context, c WorkOutboxCandidate) (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.claims = append(a.claims, c)
	return a.allowClaim && c.Family != WorkEventFamilyUnknown, nil
}

func (a *recordingOutboxAuthority) AllowWorkOutboxEffect(_ context.Context, c WorkOutboxCandidate) (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.effects = append(a.effects, c)
	return a.allowEffect && c.Family != WorkEventFamilyUnknown, nil
}

func (a *recordingOutboxAuthority) set(claim, effect bool) {
	a.mu.Lock()
	a.allowClaim, a.allowEffect = claim, effect
	a.mu.Unlock()
}

func (a *recordingOutboxAuthority) seen(eventID model.ID) (claims, effects int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, c := range a.claims {
		if c.EventID == eventID {
			claims++
		}
	}
	for _, c := range a.effects {
		if c.EventID == eventID {
			effects++
		}
	}
	return claims, effects
}

func recordedSinkEvents(sink *recordingWorkSink) []WorkEventEnvelope {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	return append([]WorkEventEnvelope(nil), sink.events...)
}

func sinkAttemptCount(sink *recordingWorkSink, eventID model.ID) int {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	n := 0
	for _, e := range sink.attempts {
		if e.EventID == eventID {
			n++
		}
	}
	return n
}

// makeWorkOutboxDueForTest rewinds a pending row's next attempt so a drain
// sees it now. The row is read before the mutation opens (single connection).
func makeWorkOutboxDueForTest(t *testing.T, f workFixture, eventID model.ID) {
	t.Helper()
	row := outboxRowForTest(t, f, eventID)
	row[colOutboxNextAttemptAt] = model.NewTimestamp(time.Unix(0, 0).UTC()).String()
	if err := f.st.Mutate(context.Background(), f.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(workOutboxKind)
		if err != nil {
			return err
		}
		_, err = repo.Update(context.Background(), row)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func assertHeldRow(t *testing.T, f workFixture, eventID model.ID, what string) {
	t.Helper()
	row := outboxRowForTest(t, f, eventID)
	if row.String(colOutboxState) != "pending" || row.Int(colOutboxAttempts) != 0 || row.String(colOutboxClaimOwner) != "" {
		t.Fatalf("%s: held row was touched: %v", what, row)
	}
}

// TestWorkOutboxAuthorityGovernsEveryDrainEntryPoint is the module half of
// review finding R1: the bound authority is consulted for the communication
// and unknown families on the Apply post-commit nudge, on DrainWorkOutbox and
// on DrainWorkOutboxWithPolicy, a caller policy can only narrow it, and K1
// work facts keep flowing on every path without asking it.
func TestWorkOutboxAuthorityGovernsEveryDrainEntryPoint(t *testing.T) {
	t.Parallel()
	f := newWorkFixture(t, ":memory:", nil)
	defer f.st.Close()
	sink := &recordingWorkSink{}
	f.m.UseWorkEventSink(sink)
	authority := &recordingOutboxAuthority{allowClaim: false, allowEffect: true}
	f.m.UseWorkOutboxClaimAuthority(authority)
	if !f.m.WorkOutboxClaimAuthorityBound() {
		t.Fatal("authority not bound")
	}

	// K1 creates publish through their own nudge (work family, no authority ask).
	heldItem := applyCreate(t, f, "held lane")
	unknownItem := applyCreate(t, f, "unknown lane")
	for _, id := range []model.ID{heldItem.EventID, unknownItem.EventID} {
		if row := outboxRowForTest(t, f, id); row.String(colOutboxState) != "published" {
			t.Fatalf("K1 create not published by its nudge: %v", row)
		}
	}
	if claims, _ := authority.seen(heldItem.EventID); claims != 0 {
		t.Fatalf("authority was asked about a K1 fact %d times", claims)
	}
	k3 := insertOutboxEventForTest(t, f, workItemKind, heldItem.ResultID, 2, "work.handoff.offered")
	unknown := insertOutboxEventForTest(t, f, workItemKind, unknownItem.ResultID, 2, "work.something.new")

	// (a) The Apply nudge of an independent K1 command.
	independent := applyCreate(t, f, "independent K1")
	if row := outboxRowForTest(t, f, independent.EventID); row.String(colOutboxState) != "published" {
		t.Fatalf("independent K1 create held: %v", row)
	}
	assertHeldRow(t, f, k3, "after Apply nudge")
	assertHeldRow(t, f, unknown, "after Apply nudge")
	// (b) The public drain.
	if err := f.m.DrainWorkOutbox(context.Background(), f.tenant, 100); err != nil {
		t.Fatalf("DrainWorkOutbox: %v", err)
	}
	assertHeldRow(t, f, k3, "after DrainWorkOutbox")
	assertHeldRow(t, f, unknown, "after DrainWorkOutbox")
	// (c) A caller policy that allows everything cannot weaken the authority.
	allowAll := WorkOutboxClaimPolicyFunc(func(context.Context, WorkOutboxCandidate) (bool, error) { return true, nil })
	if err := f.m.DrainWorkOutboxWithPolicy(context.Background(), f.tenant, 100, allowAll); err != nil {
		t.Fatalf("DrainWorkOutboxWithPolicy(allow all): %v", err)
	}
	assertHeldRow(t, f, k3, "after DrainWorkOutboxWithPolicy(allow all)")
	assertHeldRow(t, f, unknown, "after DrainWorkOutboxWithPolicy(allow all)")
	if claims, effects := authority.seen(k3); claims < 3 || effects != 0 {
		t.Fatalf("authority saw the K3 candidate claims=%d effects=%d, want >=3 claim asks on three paths and no effect", claims, effects)
	}
	if claims, _ := authority.seen(unknown); claims < 3 {
		t.Fatalf("unknown family was not routed to the authority on every path: %d", claims)
	}
	if n := sinkAttemptCount(sink, k3); n != 0 {
		t.Fatalf("sink saw the held K3 event %d times", n)
	}

	// The authority allowing releases exactly the communication row; the
	// unknown row stays held because the authority refuses unknown families.
	authority.set(true, true)
	if err := f.m.DrainWorkOutbox(context.Background(), f.tenant, 100); err != nil {
		t.Fatalf("authorized drain: %v", err)
	}
	if row := outboxRowForTest(t, f, k3); row.String(colOutboxState) != "published" {
		t.Fatalf("authorized K3 row = %v", row)
	}
	if _, effects := authority.seen(k3); effects != 1 {
		t.Fatalf("effect boundary asked %d times for the delivered K3 event, want 1", effects)
	}
	assertHeldRow(t, f, unknown, "after authorized drain")

	// A caller restriction narrows an allowed family; the next unrestricted
	// drain releases it.
	k3Later := insertOutboxEventForTest(t, f, workItemKind, heldItem.ResultID, 3, "work.handoff.withdrawn")
	denyCommunication := WorkOutboxClaimPolicyFunc(func(_ context.Context, c WorkOutboxCandidate) (bool, error) {
		return c.Family != WorkEventFamilyCommunication, nil
	})
	if err := f.m.DrainWorkOutboxWithPolicy(context.Background(), f.tenant, 100, denyCommunication); err != nil {
		t.Fatalf("restricted drain: %v", err)
	}
	assertHeldRow(t, f, k3Later, "after caller restriction")
	if err := f.m.DrainWorkOutbox(context.Background(), f.tenant, 100); err != nil {
		t.Fatalf("unrestricted drain: %v", err)
	}
	if row := outboxRowForTest(t, f, k3Later); row.String(colOutboxState) != "published" {
		t.Fatalf("K3 row after caller restriction lifted = %v", row)
	}
	published := recordedSinkEvents(sink)
	for _, e := range published {
		if e.EventID == unknown {
			t.Fatal("unknown family reached the sink")
		}
	}
}

// TestWorkOutboxComposedModuleWithoutAuthorityHoldsK3AndUnknown: a module with
// any K3 readiness witness bound is a composed deployment; without an
// authority it holds the communication and unknown families deny-closed on
// every entry point while K1 keeps flowing. Unbinding with a typed nil is the
// same OFF state.
func TestWorkOutboxComposedModuleWithoutAuthorityHoldsK3AndUnknown(t *testing.T) {
	t.Parallel()
	f := newWorkFixture(t, ":memory:", nil)
	defer f.st.Close()
	sink := &recordingWorkSink{}
	f.m.UseWorkEventSink(sink)
	f.m.UseCommunicationStoreReadinessWitness(&communicationReadinessStub{storeReady: true})
	if f.m.WorkOutboxClaimAuthorityBound() {
		t.Fatal("authority bound on a module that never received one")
	}
	item := applyCreate(t, f, "composed without authority")
	if row := outboxRowForTest(t, f, item.EventID); row.String(colOutboxState) != "published" {
		t.Fatalf("K1 create held on a composed module: %v", row)
	}
	unknownItem := applyCreate(t, f, "unknown lane")
	k3 := insertOutboxEventForTest(t, f, workItemKind, item.ResultID, 2, "work.message.available")
	unknown := insertOutboxEventForTest(t, f, workItemKind, unknownItem.ResultID, 2, "work.something.new")
	if err := f.m.DrainWorkOutbox(context.Background(), f.tenant, 100); err != nil {
		t.Fatalf("DrainWorkOutbox: %v", err)
	}
	assertHeldRow(t, f, k3, "composed module, no authority, DrainWorkOutbox")
	assertHeldRow(t, f, unknown, "composed module, no authority, DrainWorkOutbox")
	later := applyCreate(t, f, "independent K1")
	if row := outboxRowForTest(t, f, later.EventID); row.String(colOutboxState) != "published" {
		t.Fatalf("independent K1 create held on a composed module: %v", row)
	}
	assertHeldRow(t, f, k3, "composed module, no authority, Apply nudge")
	var typedNil *recordingOutboxAuthority
	f.m.UseWorkOutboxClaimAuthority(typedNil)
	if f.m.WorkOutboxClaimAuthorityBound() {
		t.Fatal("typed nil authority counted as bound")
	}
	if err := f.m.DrainWorkOutbox(context.Background(), f.tenant, 100); err != nil {
		t.Fatalf("DrainWorkOutbox after typed-nil unbind: %v", err)
	}
	assertHeldRow(t, f, k3, "composed module, typed-nil authority")
	if n := sinkAttemptCount(sink, k3); n != 0 {
		t.Fatalf("sink saw the held K3 event %d times", n)
	}
}

// TestWorkOutboxEffectBoundaryReturnsClaimWithoutDelivery is the module half
// of review finding R2: an authority withdrawn between the claim transaction
// and the sink call holds the effect. The claimed row returns to pending
// through the ordinary settlement with its own cause, no byte reaches the
// sink, the drain stops instead of spinning, the row is never dead-lettered
// for that cause, and it drains exactly once when the authority is back.
func TestWorkOutboxEffectBoundaryReturnsClaimWithoutDelivery(t *testing.T) {
	t.Parallel()
	f := newWorkFixture(t, ":memory:", nil)
	defer f.st.Close()
	sink := &recordingWorkSink{}
	f.m.UseWorkEventSink(sink)
	authority := &recordingOutboxAuthority{allowClaim: true, allowEffect: false}
	f.m.UseWorkOutboxClaimAuthority(authority)
	item := applyCreate(t, f, "effect boundary")
	k3 := insertOutboxEventForTest(t, f, workItemKind, item.ResultID, 2, "work.handoff.offered")

	err := f.m.DrainWorkOutbox(context.Background(), f.tenant, 100)
	if !errors.Is(err, ErrWorkOutboxAuthorityWithdrawn) {
		t.Fatalf("drain with the effect refused = %v, want ErrWorkOutboxAuthorityWithdrawn", err)
	}
	row := outboxRowForTest(t, f, k3)
	if row.String(colOutboxState) != "pending" || row.Int(colOutboxAttempts) != 1 ||
		row.String(colOutboxClaimOwner) != "" || row.String(colOutboxClaimUntil) != "" ||
		row.String(colOutboxLastOutcome) != "authority_withdrawn" {
		t.Fatalf("row after effect refusal = %v", row)
	}
	next, err := model.ParseTimestamp(row.String(colOutboxNextAttemptAt))
	if err != nil || !next.Time().After(time.Now().Add(time.Second)) {
		t.Fatalf("effect refusal did not schedule a backoff: %q %v", row.String(colOutboxNextAttemptAt), err)
	}
	if n := sinkAttemptCount(sink, k3); n != 0 {
		t.Fatalf("sink received %d attempts for a withheld effect", n)
	}
	if claims, effects := authority.seen(k3); claims != 1 || effects != 1 {
		t.Fatalf("authority asks claims=%d effects=%d, want one each (no spin)", claims, effects)
	}

	// Never a dead letter for this cause: a row already at the exhaustion
	// threshold that is refused at the effect boundary stays pending.
	exhausted := insertOutboxEventForTest(t, f, workItemKind, item.ResultID, 3, "work.handoff.withdrawn")
	makeWorkOutboxDueForTest(t, f, k3)
	if err := f.m.DrainWorkOutbox(context.Background(), f.tenant, 100); !errors.Is(err, ErrWorkOutboxAuthorityWithdrawn) {
		t.Fatalf("second refused drain = %v", err)
	}
	if row := outboxRowForTest(t, f, k3); row.String(colOutboxState) != "pending" || row.Int(colOutboxAttempts) != 2 {
		t.Fatalf("row after second effect refusal = %v", row)
	}
	assertHeldRow(t, f, exhausted, "row behind the refused one (ordering rule)")
	{
		row := outboxRowForTest(t, f, k3)
		row[colOutboxAttempts] = int64(10)
		row[colOutboxNextAttemptAt] = model.NewTimestamp(time.Unix(0, 0).UTC()).String()
		if err := f.st.Mutate(context.Background(), f.tenant, func(sc store.Scope) error {
			repo, err := sc.Ext(workOutboxKind)
			if err != nil {
				return err
			}
			_, err = repo.Update(context.Background(), row)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.m.DrainWorkOutbox(context.Background(), f.tenant, 100); !errors.Is(err, ErrWorkOutboxAuthorityWithdrawn) {
		t.Fatalf("refused drain at the exhaustion threshold = %v", err)
	}
	if row := outboxRowForTest(t, f, k3); row.String(colOutboxState) != "pending" || row.Int(colOutboxAttempts) != 11 ||
		row.String(colOutboxLastOutcome) != "authority_withdrawn" {
		t.Fatalf("row refused at the threshold = %v, want pending, never dead_letter", row)
	}

	// Authority back: the same row drains exactly once and the row behind it
	// follows in order.
	authority.set(true, true)
	makeWorkOutboxDueForTest(t, f, k3)
	if err := f.m.DrainWorkOutbox(context.Background(), f.tenant, 100); err != nil {
		t.Fatalf("authorized drain: %v", err)
	}
	for _, id := range []model.ID{k3, exhausted} {
		if row := outboxRowForTest(t, f, id); row.String(colOutboxState) != "published" || row.String(colOutboxLastOutcome) != "published" {
			t.Fatalf("row %s after authority returned = %v", id, row)
		}
		if n := sinkAttemptCount(sink, id); n != 1 {
			t.Fatalf("sink attempts for %s = %d, want exactly 1", id, n)
		}
	}
}
