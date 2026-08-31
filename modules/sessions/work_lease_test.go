// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestWorkLeaseEventHashesOperatorReasonWithoutPublishingIt(t *testing.T) {
	t.Parallel()

	f := newWorkLeaseDomainFixture(t, "lease reason projection")
	acquired := applyWorkLeaseCommand(t, f, f.holder, f.command("lease.acquire", f.ready.Version, 0))
	canary := "secret-canary-lease-reason-" + model.NewID().String()
	release := f.command("lease.release", acquired.Version, 1)
	release.Reason = canary
	applyWorkLeaseCommand(t, f, f.holder, release)

	var payload []byte
	if err := f.st.View(context.Background(), f.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(workEventKind)
		if err != nil {
			return err
		}
		rows, _, err := repo.List(context.Background(), model.Query{
			Filters: []model.Filter{
				eq(colEventAggregateID, f.ready.ResultID.String()),
				eq(colEventType, "work.lease.ended"),
			},
			Sort:  []model.Sort{{Column: colEventSeq, Desc: true}},
			Limit: 1,
		})
		if err != nil {
			return err
		}
		if len(rows) != 1 {
			t.Fatalf("ended WorkEvents = %d, want 1", len(rows))
		}
		payload = []byte(rows[0].String(colEventPayload))
		return nil
	}); err != nil {
		t.Fatalf("read ended WorkEvent: %v", err)
	}
	if len(payload) == 0 || bytes.Contains(payload, []byte(canary)) {
		t.Fatalf("WorkEvent disclosed operator reason: %s", payload)
	}
	var fact map[string]any
	if err := json.Unmarshal(payload, &fact); err != nil {
		t.Fatalf("decode WorkEvent: %v", err)
	}
	if fact["end_reason_code"] != "holder_released" ||
		fact["end_reason_hash"] != hexHash(hashBytes([]byte(canary))) {
		t.Fatalf("bounded end reason projection = %#v", fact)
	}
	if _, exists := fact["end_reason"]; exists {
		t.Fatalf("legacy raw end_reason field survived: %#v", fact)
	}
}

func TestWorkLeaseEventProjectsEmptyReleaseReason(t *testing.T) {
	t.Parallel()

	f := newWorkLeaseDomainFixture(t, "empty lease release reason")
	acquired := applyWorkLeaseCommand(t, f, f.holder, f.command("lease.acquire", f.ready.Version, 0))
	applyWorkLeaseCommand(t, f, f.holder, f.command("lease.release", acquired.Version, 1))

	fact := latestWorkLeaseEventPayload(t, f, "work.lease.ended")
	if fact["end_reason_code"] != "holder_released" ||
		fact["end_reason_hash"] != hexHash(hashBytes(nil)) {
		t.Fatalf("empty release reason projection = %#v", fact)
	}
	if _, exists := fact["end_reason"]; exists {
		t.Fatalf("empty release published raw reason: %#v", fact)
	}
}

func TestWorkLeaseExpiredTakeoverPublishesEndBeforeAcquire(t *testing.T) {
	t.Parallel()

	f := newWorkLeaseDomainFixture(t, "implicit expiry takeover evidence")
	acquiredResult := applyWorkLeaseCommand(
		t, f, f.holder, f.command("lease.acquire", f.ready.Version, 0),
	)
	oldLease := getWorkLease(t, f)

	// NO-FIRE: a normal acquire is one acquired fact, not a fabricated end.
	if got := countWorkLeaseEvents(t, f, "work.lease.ended"); got != 0 {
		t.Fatalf("ordinary acquire published %d ended events", got)
	}
	sink := &recordingWorkSink{}
	f.m.UseWorkEventSink(sink)
	if err := f.m.DrainWorkOutbox(context.Background(), f.tenant, 100); err != nil {
		t.Fatalf("publish ordinary backlog: %v", err)
	}
	resetRecordingWorkSink(sink, nil)
	expireWorkLeaseWindow(t, f)

	secondSID, err := f.m.ResolveSession(context.Background(), f.tenant, SessionBinding{
		Provider: "work-lease-expiry-evidence", ExternalID: model.NewID().String(), At: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("resolve successor SID: %v", err)
	}
	claimWorkLeaseTestSID(t, f.m, f.tenant, secondSID, f.holder.Actor)
	admin := f.holder
	admin.Admin, admin.SessionID = true, secondSID
	takeover := f.command("lease.takeover", acquiredResult.Version, oldLease.Fence)
	takeover.HolderSID = secondSID
	result := applyWorkLeaseCommand(t, f, admin, takeover)

	var rows []model.Record
	if err := f.st.View(context.Background(), f.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(workEventKind)
		if err != nil {
			return err
		}
		rows, _, err = repo.List(context.Background(), model.Query{
			Filters: []model.Filter{eq(colEventAggregateID, f.ready.ResultID.String())},
			Sort:    []model.Sort{{Column: colEventSeq}}, Limit: 100,
		})
		return err
	}); err != nil {
		t.Fatalf("list WorkEvents: %v", err)
	}
	if len(rows) < 3 {
		t.Fatalf("WorkEvents = %d, want acquire + implicit end + takeover", len(rows))
	}
	ended, successor := rows[len(rows)-2], rows[len(rows)-1]
	if ended.String(colEventType) != "work.lease.ended" ||
		successor.String(colEventType) != "work.lease.acquired" ||
		ended.Int(colEventSeq)+1 != successor.Int(colEventSeq) {
		t.Fatalf("implicit expiry ordering = (%s,%d) then (%s,%d)",
			ended.String(colEventType), ended.Int(colEventSeq),
			successor.String(colEventType), successor.Int(colEventSeq))
	}
	var endFact, acquiredFact map[string]any
	if err := json.Unmarshal([]byte(ended.String(colEventPayload)), &endFact); err != nil {
		t.Fatalf("decode ended event: %v", err)
	}
	if err := json.Unmarshal([]byte(successor.String(colEventPayload)), &acquiredFact); err != nil {
		t.Fatalf("decode acquired event: %v", err)
	}
	if endFact["command"] != "lease.expire" || endFact["lease_state"] != workLeaseExpired ||
		endFact["holder_sid"] != oldLease.HolderSID || endFact["end_reason_code"] != "lease_expired" ||
		endFact["end_reason_hash"] != hexHash(hashBytes([]byte("lease_expired"))) {
		t.Fatalf("implicit expiry fact = %#v", endFact)
	}
	if acquiredFact["holder_sid"] != secondSID || acquiredFact["lease_state"] != workLeaseActive {
		t.Fatalf("successor acquisition fact = %#v", acquiredFact)
	}
	if result.EventID.String() != successor.String(colEventID) {
		t.Fatalf("command result event = %s, want successor %s", result.EventID, successor.String(colEventID))
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.events) != 2 || sink.events[0].EventID != model.ID(ended.String(colEventID)) ||
		sink.events[1].EventID != result.EventID || sink.events[0].Type != "work.lease.ended" ||
		sink.events[1].Type != "work.lease.acquired" {
		t.Fatalf("successful apply nudge order = %#v", sink.events)
	}
}

func TestWorkLeasePlanDeclaresAndBindsMaterializedExpiryEffects(t *testing.T) {
	t.Parallel()

	f := newWorkLeaseDomainFixture(t, "planned implicit expiry")
	ordinary := f.command("lease.acquire", f.ready.Version, 0)
	ordinaryPlan, err := f.m.Plan(context.Background(), f.tenant, f.holder, ordinary)
	if err != nil {
		t.Fatalf("plan ordinary acquire: %v", err)
	}
	if ordinaryPlan.EventType != "work.lease.acquired" || len(ordinaryPlan.EventTypes) != 0 ||
		countString(ordinaryPlan.RowEffects, "sessions.work_event:append") != 1 ||
		countString(ordinaryPlan.RowEffects, "sessions.work_outbox:insert") != 1 {
		t.Fatalf("ordinary acquire invented an expiry effect: %#v", ordinaryPlan)
	}

	acquired := applyWorkLeaseCommand(t, f, f.holder, ordinary)
	oldLease := getWorkLease(t, f)
	expireWorkLeaseWindow(t, f)
	secondSID, err := f.m.ResolveSession(context.Background(), f.tenant, SessionBinding{
		Provider: "work-plan-expiry", ExternalID: model.NewID().String(), At: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("resolve successor SID: %v", err)
	}
	claimWorkLeaseTestSID(t, f.m, f.tenant, secondSID, f.holder.Actor)
	admin := f.holder
	admin.Admin, admin.SessionID = true, secondSID
	takeover := f.command("lease.takeover", acquired.Version, oldLease.Fence)
	takeover.HolderSID = secondSID

	plan, err := f.m.Plan(context.Background(), f.tenant, admin, takeover)
	if err != nil {
		t.Fatalf("plan expired takeover: %v", err)
	}
	wantTypes := []string{"work.lease.ended", "work.lease.acquired"}
	if plan.EventType != "work.lease.acquired" || !slices.Equal(plan.EventTypes, wantTypes) ||
		countString(plan.RowEffects, "sessions.work_event:append") != 2 ||
		countString(plan.RowEffects, "sessions.work_outbox:insert") != 2 {
		t.Fatalf("expired takeover plan = %#v, want two ordered facts", plan)
	}

	prepared := takeover
	prepared.WorkspaceID = f.workspace
	if got := workPlanDigestForTest(t, plan, prepared); got != plan.PlanHash {
		t.Fatalf("plan digest = %s, recomputed %s", plan.PlanHash, got)
	}
	mutant := plan
	mutant.EventTypes = nil
	removedAppend, removedOutbox := false, false
	mutant.RowEffects = slices.DeleteFunc(slices.Clone(plan.RowEffects), func(effect string) bool {
		switch {
		case effect == "sessions.work_event:append" && !removedAppend:
			removedAppend = true
			return true
		case effect == "sessions.work_outbox:insert" && !removedOutbox:
			removedOutbox = true
			return true
		default:
			return false
		}
	})
	if got := workPlanDigestForTest(t, mutant, prepared); got == plan.PlanHash {
		t.Fatalf("plan hash did not bind the second append/outbox effect: %#v", mutant)
	}
}

func TestWorkLeaseExpiredTakeoverNudgeAndRestartKeepAggregateOrder(t *testing.T) {
	t.Parallel()

	f := newWorkLeaseDomainFixture(t, "ordered expiry outbox")
	acquired := applyWorkLeaseCommand(
		t, f, f.holder, f.command("lease.acquire", f.ready.Version, 0),
	)
	oldLease := getWorkLease(t, f)
	sink := &recordingWorkSink{}
	f.m.UseWorkEventSink(sink)
	if err := f.m.DrainWorkOutbox(context.Background(), f.tenant, 100); err != nil {
		t.Fatalf("publish fixture backlog: %v", err)
	}
	resetRecordingWorkSink(sink, errors.New("sink offline during expiry"))
	expireWorkLeaseWindow(t, f)

	secondSID, err := f.m.ResolveSession(context.Background(), f.tenant, SessionBinding{
		Provider: "work-outbox-expiry", ExternalID: model.NewID().String(), At: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("resolve successor SID: %v", err)
	}
	claimWorkLeaseTestSID(t, f.m, f.tenant, secondSID, f.holder.Actor)
	admin := f.holder
	admin.Admin, admin.SessionID = true, secondSID
	takeover := f.command("lease.takeover", acquired.Version, oldLease.Fence)
	takeover.HolderSID = secondSID
	applyWorkLeaseCommand(t, f, admin, takeover)

	sink.mu.Lock()
	if len(sink.attempts) != 1 || sink.attempts[0].Type != "work.lease.ended" {
		t.Fatalf("failed nudge attempts = %#v, want only ended", sink.attempts)
	}
	endedID := sink.attempts[0].EventID
	sink.mu.Unlock()
	setWorkOutboxClaimForTest(t, f.workFixture, endedID, time.Unix(0, 0).UTC())
	resetRecordingWorkSink(sink, nil)

	// A restart recovers the expired claim first even when limit=1 and the
	// successor row is independently due.
	if err := f.m.DrainWorkOutbox(context.Background(), f.tenant, 1); err != nil {
		t.Fatalf("recover ended delivery: %v", err)
	}
	if err := f.m.DrainWorkOutbox(context.Background(), f.tenant, 1); err != nil {
		t.Fatalf("deliver successor acquisition: %v", err)
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.events) != 2 || sink.events[0].Type != "work.lease.ended" ||
		sink.events[1].Type != "work.lease.acquired" ||
		sink.events[0].AggregateID != sink.events[1].AggregateID ||
		sink.events[0].Sequence+1 != sink.events[1].Sequence {
		t.Fatalf("restart delivery order = %#v", sink.events)
	}
}

func TestWorkOutboxBlockedSuccessorDoesNotStopIndependentAggregate(t *testing.T) {
	t.Parallel()

	f := newWorkLeaseDomainFixture(t, "independent aggregate delivery")
	acquired := applyWorkLeaseCommand(
		t, f, f.holder, f.command("lease.acquire", f.ready.Version, 0),
	)
	oldLease := getWorkLease(t, f)
	sink := &recordingWorkSink{}
	f.m.UseWorkEventSink(sink)
	if err := f.m.DrainWorkOutbox(context.Background(), f.tenant, 100); err != nil {
		t.Fatalf("publish fixture backlog: %v", err)
	}
	resetRecordingWorkSink(sink, errors.New("sink offline during expiry"))
	expireWorkLeaseWindow(t, f)

	secondSID, err := f.m.ResolveSession(context.Background(), f.tenant, SessionBinding{
		Provider: "work-outbox-independent", ExternalID: model.NewID().String(), At: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("resolve successor SID: %v", err)
	}
	claimWorkLeaseTestSID(t, f.m, f.tenant, secondSID, f.holder.Actor)
	admin := f.holder
	admin.Admin, admin.SessionID = true, secondSID
	takeover := f.command("lease.takeover", acquired.Version, oldLease.Fence)
	takeover.HolderSID = secondSID
	applyWorkLeaseCommand(t, f, admin, takeover)
	sink.mu.Lock()
	endedID := sink.attempts[0].EventID
	sink.mu.Unlock()
	setWorkOutboxClaimForTest(t, f.workFixture, endedID, time.Now().UTC().Add(time.Hour))
	resetRecordingWorkSink(sink, nil)

	independent := applyCreate(t, f.workFixture, "independent while predecessor is claimed")
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.events) != 1 || sink.events[0].EventID != independent.EventID ||
		sink.events[0].AggregateID != independent.ResultID {
		t.Fatalf("independent aggregate was stopped by blocked successor: %#v", sink.events)
	}
}

func TestWorkOutboxAdminReplayUnwedgesOrderedSuccessorWithSameEventID(t *testing.T) {
	t.Parallel()

	f := newWorkLeaseDomainFixture(t, "dead-letter predecessor replay")
	acquired := applyWorkLeaseCommand(
		t, f, f.holder, f.command("lease.acquire", f.ready.Version, 0),
	)
	oldLease := getWorkLease(t, f)
	sink := &recordingWorkSink{}
	f.m.UseWorkEventSink(sink)
	if err := f.m.DrainWorkOutbox(context.Background(), f.tenant, 100); err != nil {
		t.Fatalf("publish fixture backlog: %v", err)
	}
	resetRecordingWorkSink(sink, errors.New("sink offline during expiry"))
	expireWorkLeaseWindow(t, f)

	secondSID, err := f.m.ResolveSession(context.Background(), f.tenant, SessionBinding{
		Provider: "work-outbox-replay", ExternalID: model.NewID().String(), At: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("resolve successor SID: %v", err)
	}
	claimWorkLeaseTestSID(t, f.m, f.tenant, secondSID, f.holder.Actor)
	admin := f.holder
	admin.Admin, admin.SessionID = true, secondSID
	takeover := f.command("lease.takeover", acquired.Version, oldLease.Fence)
	takeover.HolderSID = secondSID
	applyWorkLeaseCommand(t, f, admin, takeover)
	sink.mu.Lock()
	if len(sink.attempts) != 1 || sink.attempts[0].Type != "work.lease.ended" {
		t.Fatalf("initial predecessor attempt = %#v", sink.attempts)
	}
	endedID := sink.attempts[0].EventID
	sink.mu.Unlock()
	deadLetterWorkOutboxForTest(t, f.workFixture, endedID)
	resetRecordingWorkSink(sink, nil)

	// The successor cannot overtake a dead predecessor.
	if err := f.m.DrainWorkOutbox(context.Background(), f.tenant, 1); err != nil {
		t.Fatalf("drain blocked successor: %v", err)
	}
	if len(sink.events) != 0 {
		t.Fatalf("successor overtook dead predecessor: %#v", sink.events)
	}

	cmd := WorkOutboxReplayCommand{Command: "outbox.replay", EventID: endedID}
	before := workOutboxSnapshotForTest(t, f.workFixture, endedID)
	beforeReceipts := workCount(t, f.workFixture, workCommandKind)
	beforeAudit := workAuditSeqForTest(t, f.workFixture)
	assessment, err := f.m.ValidateWorkOutboxReplay(
		context.Background(), f.tenant, admin, cmd,
	)
	if err != nil || assessment.Verdict != VerdictClean || assessment.PlanHash != "" {
		t.Fatalf("validate replay = %#v, %v", assessment, err)
	}
	plan, err := f.m.PlanWorkOutboxReplay(context.Background(), f.tenant, admin, cmd)
	if err != nil {
		t.Fatalf("plan replay: %v", err)
	}
	wantEffects := []string{
		"core.audit:append", "sessions.work_outbox:cas", "sessions.work_command:append",
	}
	if plan.Command != "outbox.replay" || plan.EventType != "" || len(plan.EventTypes) != 0 ||
		plan.ExpectedETag != fmt.Sprintf("\"v%d\"", before.version) ||
		!slices.Equal(plan.RowEffects, wantEffects) || plan.AuditAction != "sessions.work.outbox.replay" ||
		plan.Permission != string(permWorkAdmin) || plan.PlanHash == "" {
		t.Fatalf("replay plan = %#v", plan)
	}
	if after := workOutboxSnapshotForTest(t, f.workFixture, endedID); after != before ||
		workCount(t, f.workFixture, workCommandKind) != beforeReceipts ||
		workAuditSeqForTest(t, f.workFixture) != beforeAudit {
		t.Fatalf("validate/plan wrote state: before=%#v after=%#v", before, after)
	}

	cmd.ExpectedVersion = before.version
	cmd.ExpectedPlanHash = plan.PlanHash
	cmd.IdempotencyKey = model.NewID().String()
	replayed, err := f.m.ReplayWorkOutbox(context.Background(), f.tenant, admin, cmd)
	if err != nil || replayed.EventID != endedID || replayed.State != "pending" ||
		replayed.AggregateKind != string(workItemKind) || replayed.AggregateID != replayed.WorkItemID ||
		replayed.PriorState != "dead_letter" || replayed.PriorVersion != before.version ||
		replayed.Version != before.version+1 || replayed.Attempts != before.attempts ||
		replayed.AuditSeq < 1 || replayed.Replayed {
		t.Fatalf("admin replay = %#v, %v", replayed, err)
	}
	semantic := workAuditEventForTest(t, f.workFixture, replayed.AuditSeq)
	if semantic.Action != "sessions.work.outbox.replay" ||
		semantic.TargetKind != workCommandKind || semantic.TargetID != replayed.CommandID ||
		semantic.TargetID == replayed.EventID {
		t.Fatalf("replay semantic audit = %#v, want command target distinct from event", semantic)
	}
	afterApply := workOutboxSnapshotForTest(t, f.workFixture, endedID)
	auditAfterApply := workAuditSeqForTest(t, f.workFixture)
	receiptsAfterApply := workCount(t, f.workFixture, workCommandKind)
	exact, err := f.m.ReplayWorkOutbox(context.Background(), f.tenant, admin, cmd)
	exactBody := exact
	exactBody.Replayed = false
	if err != nil || !exact.Replayed || exactBody != replayed {
		t.Fatalf("exact replay = %#v, %v; first %#v", exact, err, replayed)
	}
	if afterExact := workOutboxSnapshotForTest(t, f.workFixture, endedID); afterExact != afterApply ||
		workAuditSeqForTest(t, f.workFixture) != auditAfterApply ||
		workCount(t, f.workFixture, workCommandKind) != receiptsAfterApply {
		t.Fatalf("exact replay repeated a write: apply=%#v exact=%#v", afterApply, afterExact)
	}
	if err := f.m.DrainWorkOutbox(context.Background(), f.tenant, 1); err != nil {
		t.Fatalf("publish replayed predecessor: %v", err)
	}
	if err := f.m.DrainWorkOutbox(context.Background(), f.tenant, 1); err != nil {
		t.Fatalf("publish released successor: %v", err)
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.events) != 2 || sink.events[0].EventID != endedID ||
		sink.events[0].Type != "work.lease.ended" || sink.events[1].Type != "work.lease.acquired" ||
		sink.events[0].Sequence+1 != sink.events[1].Sequence {
		t.Fatalf("replay delivery order = %#v", sink.events)
	}
}

func TestWorkOutboxReplayRejectsNonAdminWithoutWriting(t *testing.T) {
	t.Parallel()

	f := newWorkFixture(t, filepath.Join(t.TempDir(), "work-outbox-replay-nonadmin.db"), nil)
	t.Cleanup(func() { _ = f.st.Close() })
	created := applyCreate(t, f, "replay admin boundary")
	deadLetterWorkOutboxForTest(t, f, created.EventID)
	admin := f.principal
	cmd := WorkOutboxReplayCommand{Command: "outbox.replay", EventID: created.EventID}
	plan, err := f.m.PlanWorkOutboxReplay(context.Background(), f.tenant, admin, cmd)
	if err != nil {
		t.Fatalf("plan replay: %v", err)
	}
	before := workOutboxSnapshotForTest(t, f, created.EventID)
	beforeReceipts, beforeAudit := workCount(t, f, workCommandKind), workAuditSeqForTest(t, f)
	cmd.ExpectedVersion, cmd.ExpectedPlanHash = before.version, plan.PlanHash
	cmd.IdempotencyKey = model.NewID().String()
	nonAdmin := admin
	nonAdmin.Admin = false
	if _, err := f.m.ReplayWorkOutbox(context.Background(), f.tenant, nonAdmin, cmd); err == nil ||
		asWorkError(err) == nil || asWorkError(err).code != "forbidden" {
		t.Fatalf("non-admin replay = %v, want forbidden", err)
	}
	if after := workOutboxSnapshotForTest(t, f, created.EventID); after != before ||
		workCount(t, f, workCommandKind) != beforeReceipts || workAuditSeqForTest(t, f) != beforeAudit {
		t.Fatalf("refused replay wrote state: before=%#v after=%#v", before, after)
	}
}

func TestWorkOutboxReplayRollsBackAuditAndCASWhenReceiptCannotAppend(t *testing.T) {
	t.Parallel()

	f := newWorkFixture(t, filepath.Join(t.TempDir(), "work-outbox-replay-atomic.db"), nil)
	t.Cleanup(func() { _ = f.st.Close() })
	created := applyCreate(t, f, "replay atomic receipt")
	deadLetterWorkOutboxForTest(t, f, created.EventID)
	cmd := WorkOutboxReplayCommand{Command: "outbox.replay", EventID: created.EventID}
	plan, err := f.m.PlanWorkOutboxReplay(context.Background(), f.tenant, f.principal, cmd)
	if err != nil {
		t.Fatalf("plan replay: %v", err)
	}
	before := workOutboxSnapshotForTest(t, f, created.EventID)
	beforeReceipts, beforeAudit := workCount(t, f, workCommandKind), workAuditSeqForTest(t, f)
	cmd.ExpectedVersion, cmd.ExpectedPlanHash = before.version, plan.PlanHash
	cmd.IdempotencyKey = model.NewID().String()
	if _, err := f.m.replayWorkOutboxWithData(
		context.Background(), failReceiptData{inner: f.m.workData(f.tenant)},
		f.tenant, f.principal, cmd,
	); !errors.Is(err, errTestReceiptCreate) {
		t.Fatalf("failed receipt replay = %v, want forced error", err)
	}
	if after := workOutboxSnapshotForTest(t, f, created.EventID); after != before ||
		workCount(t, f, workCommandKind) != beforeReceipts || workAuditSeqForTest(t, f) != beforeAudit {
		t.Fatalf("failed receipt left partial write: before=%#v after=%#v", before, after)
	}
}

func TestWorkOutboxReplayRejectsReceiptBoundToAnotherEvent(t *testing.T) {
	t.Parallel()

	f := newWorkFixture(t, filepath.Join(t.TempDir(), "work-outbox-replay-receipt.db"), nil)
	t.Cleanup(func() { _ = f.st.Close() })
	created := applyCreate(t, f, "replay receipt event binding")
	deadLetterWorkOutboxForTest(t, f, created.EventID)
	cmd := WorkOutboxReplayCommand{Command: "outbox.replay", EventID: created.EventID}
	plan, err := f.m.PlanWorkOutboxReplay(context.Background(), f.tenant, f.principal, cmd)
	if err != nil {
		t.Fatalf("plan replay: %v", err)
	}
	before := workOutboxSnapshotForTest(t, f, created.EventID)
	cmd.ExpectedVersion, cmd.ExpectedPlanHash = before.version, plan.PlanHash
	cmd.IdempotencyKey = model.NewID().String()
	cmd = normalizeWorkOutboxReplayCommand(cmd)
	actorFP, requestHash, idemHash, scope, err := workOutboxReplayHashes(f.principal, cmd)
	if err != nil {
		t.Fatalf("replay hashes: %v", err)
	}
	planHash, err := decodeHash(plan.PlanHash, true)
	if err != nil {
		t.Fatalf("decode replay plan hash: %v", err)
	}
	commandID := model.NewID()
	forged := WorkOutboxReplay{
		Verdict: VerdictClean, Code: "requeued", CommandID: commandID,
		OutboxID: before.id, EventID: model.NewID(),
		AggregateKind: string(workItemKind), AggregateID: created.ResultID,
		State: "pending", Version: before.version + 1, Attempts: before.attempts,
		PriorState: "dead_letter", PriorVersion: before.version,
		PlanHash: plan.PlanHash, AuditSeq: created.AuditSeq,
	}
	response, err := canonicalJSON(forged)
	if err != nil {
		t.Fatalf("encode forged replay receipt: %v", err)
	}
	if err := f.st.Mutate(context.Background(), f.tenant, func(sc store.Scope) error {
		receipts, err := sc.Ext(workCommandKind)
		if err != nil {
			return err
		}
		_, err = receipts.Create(context.Background(), model.Record{
			colWorkWorkspaceID: f.workspace.String(), colCommandID: commandID.String(),
			colCommandActorFP: actorFP, colCommandScope: scope, colCommandIdempotency: idemHash,
			colCommandRequestHash: requestHash, colCommandPlanHash: planHash,
			colCommandResultKind: string(workOutboxKind), colCommandResultID: before.id.String(),
			colCommandHTTPStatus: int64(http.StatusAccepted), colCommandResponse: string(response),
			colCommandResponseHash: hashBytes(response), colCommandAuditSeq: created.AuditSeq,
			colCommandAuditHash:   make([]byte, 32),
			colCommandCompletedAt: model.SystemClock{}.Now().String(),
		})
		return err
	}); err != nil {
		t.Fatalf("insert forged replay receipt: %v", err)
	}
	if _, err := f.m.ReplayWorkOutbox(context.Background(), f.tenant, f.principal, cmd); err == nil ||
		asWorkError(err) == nil || asWorkError(err).verdict != VerdictUnknown ||
		asWorkError(err).code != "evidence_unavailable" {
		t.Fatalf("forged replay receipt = %v, want evidence_unavailable", err)
	}
	if after := workOutboxSnapshotForTest(t, f, created.EventID); after != before {
		t.Fatalf("forged receipt changed outbox: before=%#v after=%#v", before, after)
	}
}

func TestWorkOutboxReplayIdempotencyKeyBindsEventAndObservedGeneration(t *testing.T) {
	t.Parallel()

	f := newWorkFixture(t, filepath.Join(t.TempDir(), "work-outbox-replay-binding.db"), nil)
	t.Cleanup(func() { _ = f.st.Close() })
	first := applyCreate(t, f, "first replay target")
	second := applyCreate(t, f, "second replay target")
	deadLetterWorkOutboxForTest(t, f, first.EventID)
	deadLetterWorkOutboxForTest(t, f, second.EventID)
	firstCmd := WorkOutboxReplayCommand{Command: "outbox.replay", EventID: first.EventID}
	firstPlan, err := f.m.PlanWorkOutboxReplay(context.Background(), f.tenant, f.principal, firstCmd)
	if err != nil {
		t.Fatalf("plan first replay: %v", err)
	}
	firstBefore := workOutboxSnapshotForTest(t, f, first.EventID)
	sharedKey := model.NewID().String()
	firstCmd.ExpectedVersion, firstCmd.ExpectedPlanHash = firstBefore.version, firstPlan.PlanHash
	firstCmd.IdempotencyKey = sharedKey
	if _, err := f.m.ReplayWorkOutbox(context.Background(), f.tenant, f.principal, firstCmd); err != nil {
		t.Fatalf("apply first replay: %v", err)
	}

	secondCmd := WorkOutboxReplayCommand{Command: "outbox.replay", EventID: second.EventID}
	secondPlan, err := f.m.PlanWorkOutboxReplay(context.Background(), f.tenant, f.principal, secondCmd)
	if err != nil {
		t.Fatalf("plan second replay: %v", err)
	}
	secondBefore := workOutboxSnapshotForTest(t, f, second.EventID)
	secondCmd.ExpectedVersion, secondCmd.ExpectedPlanHash = secondBefore.version, secondPlan.PlanHash
	secondCmd.IdempotencyKey = sharedKey
	beforeReceipts, beforeAudit := workCount(t, f, workCommandKind), workAuditSeqForTest(t, f)
	if _, err := f.m.ReplayWorkOutbox(context.Background(), f.tenant, f.principal, secondCmd); err == nil ||
		asWorkError(err) == nil || asWorkError(err).code != "idempotency_key_reused" {
		t.Fatalf("same key with another event = %v, want idempotency_key_reused", err)
	}
	if secondAfter := workOutboxSnapshotForTest(t, f, second.EventID); secondAfter != secondBefore ||
		workCount(t, f, workCommandKind) != beforeReceipts || workAuditSeqForTest(t, f) != beforeAudit {
		t.Fatalf("key reuse wrote second event: before=%#v after=%#v", secondBefore, secondAfter)
	}

	// Replaying and dead-lettering the first target again changes its generation;
	// the new plan must bind that state and a stale If-Match must not apply.
	deadLetterWorkOutboxForTest(t, f, first.EventID)
	newPlan, err := f.m.PlanWorkOutboxReplay(
		context.Background(), f.tenant, f.principal,
		WorkOutboxReplayCommand{Command: "outbox.replay", EventID: first.EventID},
	)
	if err != nil {
		t.Fatalf("plan replayed generation: %v", err)
	}
	if newPlan.PlanHash == firstPlan.PlanHash || newPlan.ExpectedETag == firstPlan.ExpectedETag {
		t.Fatalf("plan did not bind replay generation: old=%#v new=%#v", firstPlan, newPlan)
	}
	stale := firstCmd
	stale.IdempotencyKey = model.NewID().String()
	if _, err := f.m.ReplayWorkOutbox(context.Background(), f.tenant, f.principal, stale); err == nil ||
		asWorkError(err) == nil || asWorkError(err).code != "version_mismatch" {
		t.Fatalf("stale outbox If-Match = %v, want version_mismatch", err)
	}
}

func TestWorkOutboxOrderingTreatsMissingOrInconsistentEvidenceAsUnknown(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	workspace, aggregate := model.NewID(), model.NewID()
	current := model.Record{
		colWorkWorkspaceID: workspace.String(), colEventID: model.NewID().String(),
		colEventAggregateKind: string(workItemKind), colEventAggregateID: aggregate.String(),
		colEventSeq: int64(2),
	}
	predecessor := model.Record{
		colWorkWorkspaceID: workspace.String(), colEventID: model.NewID().String(),
		colEventAggregateKind: string(workItemKind), colEventAggregateID: aggregate.String(),
		colEventSeq: int64(1),
	}
	assertUnknown := func(t *testing.T, err error) {
		t.Helper()
		we := asWorkError(err)
		if we == nil || we.verdict != VerdictUnknown {
			t.Fatalf("error = %v, want NO_HE_PODIDO_MIRAR", err)
		}
	}

	missingEvents := workListRepoForTest{list: func(model.Query) ([]model.Record, error) {
		return []model.Record{}, nil
	}}
	if _, err := workOutboxEvent(ctx, missingEvents, model.Record{
		colOutboxEventID: model.NewID().String(), colWorkWorkspaceID: workspace.String(),
	}); err == nil {
		t.Fatal("missing current WorkEvent was accepted")
	} else {
		assertUnknown(t, err)
	}
	inconsistentEvent := workListRepoForTest{list: func(model.Query) ([]model.Record, error) {
		return []model.Record{{
			colWorkWorkspaceID: model.NewID().String(), colEventAggregateKind: string(workItemKind),
			colEventAggregateID: aggregate.String(), colEventSeq: int64(2),
		}}, nil
	}}
	if _, err := workOutboxEvent(ctx, inconsistentEvent, model.Record{
		colOutboxEventID: model.NewID().String(), colWorkWorkspaceID: workspace.String(),
	}); err == nil {
		t.Fatal("inconsistent current WorkEvent was accepted")
	} else {
		assertUnknown(t, err)
	}
	if ready, err := workOutboxPredecessorPublished(ctx, missingEvents, workListRepoForTest{}, current); err == nil || ready {
		t.Fatalf("missing predecessor = ready %v, err %v", ready, err)
	} else {
		assertUnknown(t, err)
	}

	predecessorRepo := workListRepoForTest{list: func(model.Query) ([]model.Record, error) {
		return []model.Record{predecessor}, nil
	}}
	missingOutbox := workListRepoForTest{list: func(model.Query) ([]model.Record, error) {
		return []model.Record{}, nil
	}}
	if ready, err := workOutboxPredecessorPublished(ctx, predecessorRepo, missingOutbox, current); err == nil || ready {
		t.Fatalf("missing predecessor outbox = ready %v, err %v", ready, err)
	} else {
		assertUnknown(t, err)
	}

	for _, tc := range []struct {
		state string
		ready bool
		known bool
	}{
		{state: "published", ready: true, known: true},
		{state: "pending", ready: false, known: true},
		{state: "delivering", ready: false, known: true},
		{state: "dead_letter", ready: false, known: true},
		{state: "invented", ready: false, known: false},
	} {
		t.Run(tc.state, func(t *testing.T) {
			row := model.Record{
				model.ColID: model.NewID().String(), model.ColVersion: int64(1),
				colWorkWorkspaceID: workspace.String(), colOutboxEventID: predecessor.String(colEventID),
				colOutboxState: tc.state, colOutboxAttempts: int64(0),
				colOutboxNextAttemptAt: model.SystemClock{}.Now().String(),
			}
			switch tc.state {
			case "delivering":
				row[colOutboxAttempts], row[colOutboxClaimOwner] = int64(1), "sessions.work-pump"
				row[colOutboxClaimUntil] = model.SystemClock{}.Now().String()
			case "published":
				row[colOutboxAttempts], row[colOutboxPublishedAt] = int64(1), model.SystemClock{}.Now().String()
				row[colOutboxLastOutcome] = "published"
			case "dead_letter":
				row[colOutboxAttempts], row[colOutboxLastOutcome] = int64(10), "retry_exhausted"
			}
			outbox := workListRepoForTest{list: func(model.Query) ([]model.Record, error) {
				return []model.Record{row}, nil
			}}
			ready, err := workOutboxPredecessorPublished(ctx, predecessorRepo, outbox, current)
			if tc.known {
				if err != nil || ready != tc.ready {
					t.Fatalf("state %s = ready %v, err %v", tc.state, ready, err)
				}
				return
			}
			if err == nil || ready {
				t.Fatalf("inconsistent state = ready %v, err %v", ready, err)
			}
			assertUnknown(t, err)
		})
	}
}

type workListRepoForTest struct {
	store.GenericRepo
	list func(model.Query) ([]model.Record, error)
}

func (r workListRepoForTest) List(_ context.Context, query model.Query) ([]model.Record, model.Page, error) {
	if r.list == nil {
		return nil, model.Page{}, errors.New("unexpected list")
	}
	rows, err := r.list(query)
	return rows, model.Page{}, err
}

func workPlanDigestForTest(t *testing.T, plan Plan, cmd WorkCommand) string {
	t.Helper()
	preimage := plan
	preimage.PlanHash = ""
	preimage.ObservedAt = ""
	cmd.PlanHash = ""
	// This focused fixture uses allowWorkIdentity, whose observation seam binds
	// the same stable digest in Plan and Apply.
	cmd.agentAuthority.Digest = "test-authority"
	b, err := canonicalJSON(struct {
		Plan            Plan        `json:"plan"`
		Command         WorkCommand `json:"command"`
		AuthorityDigest string      `json:"authority_digest,omitempty"`
	}{Plan: preimage, Command: cmd, AuthorityDigest: cmd.agentAuthority.Digest})
	if err != nil {
		t.Fatalf("canonical plan preimage: %v", err)
	}
	return hexHash(hashBytes(b))
}

func countString(values []string, want string) int {
	count := 0
	for _, value := range values {
		if value == want {
			count++
		}
	}
	return count
}

func resetRecordingWorkSink(sink *recordingWorkSink, err error) {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	sink.events, sink.attempts, sink.err = nil, nil, err
}

func setWorkOutboxClaimForTest(t *testing.T, f workFixture, eventID model.ID, until time.Time) {
	t.Helper()
	if err := f.st.Mutate(context.Background(), f.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(workOutboxKind)
		if err != nil {
			return err
		}
		rows, _, err := repo.List(context.Background(), model.Query{Filters: []model.Filter{
			{Column: colOutboxEventID, Op: model.OpEq, Value: eventID.String()},
		}, Limit: 1})
		if err != nil {
			return err
		}
		if len(rows) != 1 {
			return fmt.Errorf("outbox event %s rows = %d", eventID, len(rows))
		}
		row := rows[0]
		row[colOutboxState] = "delivering"
		row[colOutboxAttempts] = max(int64(1), row.Int(colOutboxAttempts))
		row[colOutboxClaimOwner] = "node-before-restart"
		row[colOutboxClaimUntil] = model.NewTimestamp(until).String()
		row[colOutboxLastOutcome] = nil
		_, err = repo.Update(context.Background(), row)
		return err
	}); err != nil {
		t.Fatalf("set outbox claim for %s: %v", eventID, err)
	}
}

func deadLetterWorkOutboxForTest(t *testing.T, f workFixture, eventID model.ID) {
	t.Helper()
	if err := f.st.Mutate(context.Background(), f.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(workOutboxKind)
		if err != nil {
			return err
		}
		rows, _, err := repo.List(context.Background(), model.Query{Filters: []model.Filter{{
			Column: colOutboxEventID, Op: model.OpEq, Value: eventID.String(),
		}}, Limit: 1})
		if err != nil || len(rows) != 1 {
			return errors.Join(err, fmt.Errorf("outbox event %s rows = %d", eventID, len(rows)))
		}
		row := rows[0]
		row[colOutboxState], row[colOutboxAttempts] = "delivering", int64(10)
		row[colOutboxClaimOwner] = "sessions.work-pump"
		row[colOutboxClaimUntil] = model.NewTimestamp(time.Now().UTC().Add(time.Minute)).String()
		row[colOutboxLastOutcome] = nil
		if row, err = repo.Update(context.Background(), row); err != nil {
			return err
		}
		row[colOutboxState], row[colOutboxClaimOwner], row[colOutboxClaimUntil] = "dead_letter", nil, nil
		row[colOutboxLastOutcome] = "retry_exhausted"
		_, err = repo.Update(context.Background(), row)
		return err
	}); err != nil {
		t.Fatalf("dead-letter outbox event %s: %v", eventID, err)
	}
}

func workOutboxStateForTest(t *testing.T, f workFixture, eventID model.ID) string {
	t.Helper()
	var state string
	if err := f.st.View(context.Background(), f.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(workOutboxKind)
		if err != nil {
			return err
		}
		rows, _, err := repo.List(context.Background(), model.Query{Filters: []model.Filter{{
			Column: colOutboxEventID, Op: model.OpEq, Value: eventID.String(),
		}}, Limit: 1})
		if err != nil || len(rows) != 1 {
			return errors.Join(err, fmt.Errorf("outbox event %s rows = %d", eventID, len(rows)))
		}
		state = rows[0].String(colOutboxState)
		return nil
	}); err != nil {
		t.Fatalf("read outbox event %s: %v", eventID, err)
	}
	return state
}

type workOutboxTestSnapshot struct {
	id            model.ID
	state         string
	version       int64
	attempts      int64
	nextAttemptAt string
	claimOwner    string
	claimUntil    string
	publishedAt   string
	lastOutcome   string
}

func workOutboxSnapshotForTest(
	t *testing.T,
	f workFixture,
	eventID model.ID,
) workOutboxTestSnapshot {
	t.Helper()
	var snapshot workOutboxTestSnapshot
	if err := f.st.View(context.Background(), f.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(workOutboxKind)
		if err != nil {
			return err
		}
		rows, _, err := repo.List(context.Background(), model.Query{Filters: []model.Filter{{
			Column: colOutboxEventID, Op: model.OpEq, Value: eventID.String(),
		}}, Limit: 2})
		if err != nil || len(rows) != 1 {
			return errors.Join(err, fmt.Errorf("outbox event %s rows = %d", eventID, len(rows)))
		}
		row := rows[0]
		snapshot = workOutboxTestSnapshot{
			id: recordID(row), state: row.String(colOutboxState), version: row.Int(model.ColVersion),
			attempts: row.Int(colOutboxAttempts), nextAttemptAt: row.String(colOutboxNextAttemptAt),
			claimOwner: row.String(colOutboxClaimOwner), claimUntil: row.String(colOutboxClaimUntil),
			publishedAt: row.String(colOutboxPublishedAt), lastOutcome: row.String(colOutboxLastOutcome),
		}
		return nil
	}); err != nil {
		t.Fatalf("read outbox event %s: %v", eventID, err)
	}
	return snapshot
}

func workAuditSeqForTest(t *testing.T, f workFixture) int64 {
	t.Helper()
	var seq int64
	if err := f.st.View(context.Background(), f.tenant, func(sc store.Scope) error {
		head, ok, err := sc.Audit().Head(context.Background())
		if err == nil && ok {
			seq = head.Seq
		}
		return err
	}); err != nil {
		t.Fatalf("read work audit head: %v", err)
	}
	return seq
}

func workAuditEventForTest(t *testing.T, f workFixture, seq int64) model.AuditEvent {
	t.Helper()
	stop := errors.New("sessions: stop work audit lookup")
	var event model.AuditEvent
	if err := f.st.View(context.Background(), f.tenant, func(sc store.Scope) error {
		err := sc.Audit().Walk(context.Background(), seq, func(candidate model.AuditEvent) error {
			event = candidate
			return stop
		})
		if errors.Is(err, stop) {
			return nil
		}
		return err
	}); err != nil {
		t.Fatalf("read work audit event %d: %v", seq, err)
	}
	if event.Seq != seq {
		t.Fatalf("work audit event %d = %#v", seq, event)
	}
	return event
}

func latestWorkLeaseEventPayload(
	t *testing.T,
	f workLeaseDomainFixture,
	eventType string,
) map[string]any {
	t.Helper()
	var payload string
	if err := f.st.View(context.Background(), f.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(workEventKind)
		if err != nil {
			return err
		}
		rows, _, err := repo.List(context.Background(), model.Query{
			Filters: []model.Filter{
				eq(colEventAggregateID, f.ready.ResultID.String()), eq(colEventType, eventType),
			},
			Sort: []model.Sort{{Column: colEventSeq, Desc: true}}, Limit: 1,
		})
		if err != nil {
			return err
		}
		if len(rows) != 1 {
			return fmt.Errorf("%s WorkEvents = %d, want 1", eventType, len(rows))
		}
		payload = rows[0].String(colEventPayload)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var fact map[string]any
	if err := json.Unmarshal([]byte(payload), &fact); err != nil {
		t.Fatalf("decode %s payload: %v", eventType, err)
	}
	return fact
}

func countWorkLeaseEvents(t *testing.T, f workLeaseDomainFixture, eventType string) int {
	t.Helper()
	count := 0
	if err := f.st.View(context.Background(), f.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(workEventKind)
		if err != nil {
			return err
		}
		rows, _, err := repo.List(context.Background(), model.Query{
			Filters: []model.Filter{
				eq(colEventAggregateID, f.ready.ResultID.String()), eq(colEventType, eventType),
			},
			Limit: 100,
		})
		count = len(rows)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestWorkItemTransitionPublishesSanitizedImplicitLeaseEnd(t *testing.T) {
	t.Parallel()

	f := newWorkLeaseDomainFixture(t, "implicit lease transition")
	acquired := applyWorkLeaseCommand(
		t, f, f.holder, f.command("lease.acquire", f.ready.Version, 0),
	)
	renewed := applyWorkLeaseCommand(
		t, f, f.holder, f.command("lease.renew", acquired.Version, 1),
	)

	readLatestPayload := func(eventType string) map[string]any {
		t.Helper()
		var payload string
		if err := f.st.View(context.Background(), f.tenant, func(sc store.Scope) error {
			repo, err := sc.Ext(workEventKind)
			if err != nil {
				return err
			}
			rows, _, err := repo.List(context.Background(), model.Query{
				Filters: []model.Filter{
					eq(colEventAggregateID, f.ready.ResultID.String()),
					eq(colEventType, eventType),
				},
				Sort:  []model.Sort{{Column: colEventSeq, Desc: true}},
				Limit: 1,
			})
			if err != nil {
				return err
			}
			if len(rows) != 1 {
				t.Fatalf("%s WorkEvents = %d, want 1", eventType, len(rows))
			}
			payload = rows[0].String(colEventPayload)
			return nil
		}); err != nil {
			t.Fatalf("read %s WorkEvent: %v", eventType, err)
		}
		var fact map[string]any
		if err := json.Unmarshal([]byte(payload), &fact); err != nil {
			t.Fatalf("decode %s WorkEvent: %v", eventType, err)
		}
		return fact
	}

	// Non-trigger direction: renewing live authority is not an implicit end.
	if fact := readLatestPayload("work.lease.acquired"); fact["lease_transition"] != nil {
		t.Fatalf("renewal invented an implicit lease transition: %#v", fact)
	}

	criterion := workCriterion(t, f.workFixture, f.ready.ResultID, "tests")
	evaluate := f.command("acceptance.evaluate", renewed.Version, 1)
	evaluate.CriterionID = recordID(criterion)
	evaluate.Acceptance = []AcceptanceInput{{
		State: "passed", EvidenceRef: "job:implicit-lease-green",
		EvidenceHash: hexHash(hashBytes([]byte("implicit-lease-green"))),
	}}
	evaluated := applyWorkLeaseCommand(t, f, f.holder, evaluate)

	submit := f.command("item.submit", evaluated.Version, 1)
	applyWorkLeaseCommand(t, f, f.holder, submit)
	fact := readLatestPayload("work.item.transitioned")
	transition, ok := fact["lease_transition"].(map[string]any)
	if !ok || transition["state"] != workLeaseReleased ||
		transition["code"] != "submitted_for_review" {
		t.Fatalf("implicit lease transition = %#v", fact["lease_transition"])
	}
	for _, forbidden := range []string{
		"holder_sid", "holder_run_ref", "holder_agent_ref", "fence", "end_reason",
	} {
		if _, exists := transition[forbidden]; exists {
			t.Fatalf("implicit lease transition disclosed %s: %#v", forbidden, transition)
		}
	}
}

type workLeaseDomainFixture struct {
	workFixture
	agentRef string
	sid      string
	holder   WorkPrincipal
	ready    CommandResult
}

func claimWorkLeaseTestSID(
	t *testing.T,
	m *Module,
	tenant model.TenantID,
	sid string,
	holder string,
) {
	t.Helper()
	if _, err := m.Claim(context.Background(), tenant, sid, holder, 0); err != nil {
		t.Fatalf("claim WorkLease test SID %s: %v", sid, err)
	}
}

func newWorkLeaseDomainFixture(t *testing.T, title string) workLeaseDomainFixture {
	t.Helper()
	f := newWorkFixture(t, filepath.Join(t.TempDir(), "work-lease.db"), nil)
	t.Cleanup(func() { _ = f.st.Close() })
	return addWorkLeaseDomainItem(t, f, title)
}

func addWorkLeaseDomainItem(
	t *testing.T,
	f workFixture,
	title string,
) workLeaseDomainFixture {
	t.Helper()
	agentRef := "agent:" + model.NewID().String()
	holder := WorkPrincipal{
		ActorKind: model.ActorAgent,
		ActorRef:  agentRef,
		Actor:     agentRef,
	}
	create := baseCreateCommand(f, title)
	create.OwnerKind, create.OwnerRef = "agent", agentRef
	created, err := f.m.Apply(context.Background(), f.tenant, holder, create)
	if err != nil {
		t.Fatalf("create agent-owned WorkItem: %v", err)
	}
	ready, err := f.m.Apply(context.Background(), f.tenant, holder, WorkCommand{
		Command: "item.ready", WorkItemID: created.ResultID,
		ExpectedVersion: created.Version, IdempotencyKey: model.NewID().String(),
		HTTPMethod: http.MethodPost,
	})
	if err != nil || ready.Status != "ready" {
		t.Fatalf("make agent-owned WorkItem ready: %#v, %v", ready, err)
	}
	sid, err := f.m.ResolveSession(context.Background(), f.tenant, SessionBinding{
		Provider: "work-lease-test", ExternalID: model.NewID().String(), At: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("resolve holder SID: %v", err)
	}
	holder.SessionID = sid
	claimWorkLeaseTestSID(t, f.m, f.tenant, sid, holder.Actor)
	return workLeaseDomainFixture{
		workFixture: f, agentRef: agentRef, sid: sid, holder: holder, ready: ready,
	}
}

func (f workLeaseDomainFixture) command(command string, version, fence int64) WorkCommand {
	return WorkCommand{
		Command: command, WorkItemID: f.ready.ResultID,
		HolderSID: f.sid, HolderAgentRef: f.agentRef, Fence: fence,
		TTLSeconds: 60, ExpectedVersion: version,
		IdempotencyKey: model.NewID().String(), HTTPMethod: http.MethodPost,
	}
}

func applyWorkLeaseCommand(
	t *testing.T,
	f workLeaseDomainFixture,
	principal WorkPrincipal,
	cmd WorkCommand,
) CommandResult {
	t.Helper()
	result, err := f.m.Apply(context.Background(), f.tenant, principal, cmd)
	if err != nil {
		t.Fatalf("apply %s: %v", cmd.Command, err)
	}
	return result
}

func getWorkLease(t *testing.T, f workLeaseDomainFixture) WorkLease {
	t.Helper()
	lease, err := f.m.GetLease(
		context.Background(), f.tenant, f.holder, f.ready.ResultID,
	)
	if err != nil {
		t.Fatalf("get WorkLease: %v", err)
	}
	return lease
}

func getWorkSnapshot(t *testing.T, f workLeaseDomainFixture) WorkSnapshot {
	t.Helper()
	snapshot, err := f.m.Get(
		context.Background(), f.tenant, f.holder, f.ready.ResultID,
	)
	if err != nil {
		t.Fatalf("get WorkItem: %v", err)
	}
	return snapshot
}

// expireWorkLeaseWindow preserves every UPDATE-trigger transition invariant:
// it is an active->active renewal by the same holder, with a stable fence and
// renewal_count+1. Its already-past lifetime lets the production reaper, not
// the fixture, materialize the terminal state.
func expireWorkLeaseWindow(t *testing.T, f workLeaseDomainFixture) {
	t.Helper()
	if err := f.m.data.Mutate(context.Background(), f.tenant, func(sc store.Scope) error {
		lease, found, err := findWorkLease(context.Background(), sc, f.ready.ResultID)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("work lease is absent")
		}
		past := time.Now().UTC().Add(-2 * time.Minute)
		lease[colLeaseAcquiredAt] = model.NewTimestamp(past).String()
		lease[colLeaseRenewedAt] = model.NewTimestamp(past.Add(30 * time.Second)).String()
		lease[colLeaseExpiresAt] = model.NewTimestamp(past.Add(time.Minute)).String()
		lease[colLeaseRenewalCount] = lease.Int(colLeaseRenewalCount) + 1
		repo, err := sc.Ext(workLeaseKind)
		if err != nil {
			return err
		}
		_, err = repo.Update(context.Background(), lease)
		return err
	}); err != nil {
		t.Fatalf("seed an expired but unmaterialized WorkLease: %v", err)
	}
}

func assertWorkVerdict(t *testing.T, err error, verdict AssessmentVerdict, code string) {
	t.Helper()
	we := asWorkError(err)
	if we == nil || we.verdict != verdict || we.code != code {
		t.Fatalf("work result = %v, want %s/%s", err, verdict, code)
	}
}

func TestWorkLeaseDomainAcquireRenewReleaseExpireTakeover(t *testing.T) {
	t.Parallel()

	f := newWorkLeaseDomainFixture(t, "lease lifecycle")

	acquiredResult := applyWorkLeaseCommand(
		t, f, f.holder, f.command("lease.acquire", f.ready.Version, 0),
	)
	acquired := getWorkLease(t, f)
	if acquired.State != workLeaseActive || acquired.Fence != 1 || !acquired.Live ||
		acquired.HolderSID != f.sid || acquired.HolderAgentRef != f.agentRef {
		t.Fatalf("acquired lease = %#v", acquired)
	}

	renewedResult := applyWorkLeaseCommand(
		t, f, f.holder, f.command("lease.renew", acquiredResult.Version, acquired.Fence),
	)
	renewed := getWorkLease(t, f)
	if renewed.Fence != acquired.Fence || renewed.RenewalCount != acquired.RenewalCount+1 ||
		renewed.RenewedAt == "" {
		t.Fatalf("renewed lease = %#v; acquired = %#v", renewed, acquired)
	}

	release := f.command("lease.release", renewedResult.Version, renewed.Fence)
	release.Reason = "worker yielded"
	releasedResult := applyWorkLeaseCommand(t, f, f.holder, release)
	released := getWorkLease(t, f)
	if released.State != workLeaseReleased || released.Fence != renewed.Fence+1 || released.Live {
		t.Fatalf("released lease = %#v; renewed = %#v", released, renewed)
	}

	// Trigger direction: the token that was live before release is stale even
	// when paired with the WorkItem's current version.
	staleAfterRelease := f.command("lease.renew", releasedResult.Version, renewed.Fence)
	_, err := f.m.Apply(context.Background(), f.tenant, f.holder, staleAfterRelease)
	assertWorkVerdict(t, err, VerdictBroken, "stale_fence")
	if got := getWorkLease(t, f); got.Fence != released.Fence || got.State != workLeaseReleased {
		t.Fatalf("stale renewal changed released lease: %#v", got)
	}

	// Non-trigger direction: the same owner can reacquire a blocked item through
	// the explicit unblock path, and exactly one new fence is minted.
	reacquire := f.command("lease.acquire", releasedResult.Version, 0)
	reacquire.Unblock = true
	applyWorkLeaseCommand(t, f, f.holder, reacquire)
	reacquired := getWorkLease(t, f)
	if reacquired.State != workLeaseActive || reacquired.Fence != released.Fence+1 {
		t.Fatalf("reacquired lease = %#v; released = %#v", reacquired, released)
	}

	expireWorkLeaseWindow(t, f)
	reaped, err := f.m.ReapWorkLeases(context.Background(), f.tenant, 10)
	if err != nil || reaped != 1 {
		t.Fatalf("reap expired lease = %d, %v", reaped, err)
	}
	expired := getWorkLease(t, f)
	if expired.State != workLeaseExpired || expired.Fence != reacquired.Fence+1 || expired.Live {
		t.Fatalf("expired lease = %#v; reacquired = %#v", expired, reacquired)
	}
	expiredItem := getWorkSnapshot(t, f).Item
	if expiredItem.Status != "blocked" || expiredItem.BlockedCode != "lease_expired" {
		t.Fatalf("reaped WorkItem = %#v", expiredItem)
	}

	secondSID, err := f.m.ResolveSession(context.Background(), f.tenant, SessionBinding{
		Provider: "work-lease-test", ExternalID: model.NewID().String(), At: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("resolve takeover SID: %v", err)
	}
	claimWorkLeaseTestSID(t, f.m, f.tenant, secondSID, f.holder.Actor)
	admin := f.holder
	admin.Admin, admin.SessionID = true, secondSID
	takeover := f.command("lease.takeover", expiredItem.Version, expired.Fence)
	takeover.HolderSID, takeover.Unblock = secondSID, true
	takenResult := applyWorkLeaseCommand(t, f, admin, takeover)
	taken := getWorkLease(t, f)
	if taken.State != workLeaseActive || taken.Fence != expired.Fence+1 ||
		taken.HolderSID != secondSID {
		t.Fatalf("taken lease = %#v; expired = %#v", taken, expired)
	}

	// Trigger direction: the original writer loses with its old holder+fence,
	// despite presenting the current WorkItem version after takeover.
	staleWriter := f.command("lease.renew", takenResult.Version, reacquired.Fence)
	_, err = f.m.Apply(context.Background(), f.tenant, f.holder, staleWriter)
	assertWorkVerdict(t, err, VerdictBroken, "stale_fence")
	if got := getWorkLease(t, f); got.Fence != taken.Fence || got.HolderSID != secondSID {
		t.Fatalf("stale writer changed takeover authority: %#v", got)
	}

	// Non-trigger direction: the exact new token renews and keeps its fence.
	currentWriter := takeover
	currentWriter.Command = "lease.renew"
	currentWriter.Fence = taken.Fence
	currentWriter.ExpectedVersion = takenResult.Version
	currentWriter.IdempotencyKey = model.NewID().String()
	applyWorkLeaseCommand(t, f, admin, currentWriter)
	if got := getWorkLease(t, f); got.Fence != taken.Fence || got.RenewalCount != 1 {
		t.Fatalf("current takeover holder renewal = %#v", got)
	}
}

func TestWorkLeaseDomainLiveForceTakeoverRequiresAndRecordsAuthority(t *testing.T) {
	t.Parallel()

	f := newWorkLeaseDomainFixture(t, "live force takeover")
	acquiredResult := applyWorkLeaseCommand(
		t, f, f.holder, f.command("lease.acquire", f.ready.Version, 0),
	)
	oldLease := getWorkLease(t, f)

	readLatestLeaseEvent := func() map[string]any {
		t.Helper()
		var payload string
		if err := f.st.View(context.Background(), f.tenant, func(sc store.Scope) error {
			repo, err := sc.Ext(workEventKind)
			if err != nil {
				return err
			}
			rows, _, err := repo.List(context.Background(), model.Query{
				Filters: []model.Filter{
					eq(colEventAggregateID, f.ready.ResultID.String()),
					eq(colEventType, "work.lease.acquired"),
				},
				Sort:  []model.Sort{{Column: colEventSeq, Desc: true}},
				Limit: 1,
			})
			if err != nil {
				return err
			}
			if len(rows) != 1 {
				t.Fatalf("lease acquired WorkEvents = %d, want 1", len(rows))
			}
			payload = rows[0].String(colEventPayload)
			return nil
		}); err != nil {
			t.Fatalf("read WorkLease event: %v", err)
		}
		var fact map[string]any
		if err := json.Unmarshal([]byte(payload), &fact); err != nil {
			t.Fatalf("decode WorkLease event: %v", err)
		}
		return fact
	}

	// NO-FIRE: an ordinary acquisition is not labeled as a high-severity
	// override. This prevents a classifier that marks every lease event high
	// from satisfying the force-takeover assertion below.
	if fact := readLatestLeaseEvent(); fact["severity"] != nil || fact["forced"] != nil {
		t.Fatalf("ordinary acquire was classified as forced: %#v", fact)
	}

	newSID, err := f.m.ResolveSession(context.Background(), f.tenant, SessionBinding{
		Provider: "work-lease-force-takeover", ExternalID: model.NewID().String(), At: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("resolve force-takeover SID: %v", err)
	}
	claimWorkLeaseTestSID(t, f.m, f.tenant, newSID, f.holder.Actor)
	admin := f.holder
	admin.Admin, admin.SessionID = true, newSID

	validPlanDigest := hexHash(hashBytes([]byte("force-takeover-plan-placeholder")))
	missingReason := f.command("lease.takeover", acquiredResult.Version, oldLease.Fence)
	missingReason.HolderSID, missingReason.Force = newSID, true
	missingReason.DecisionID, missingReason.ExpectedPlanHash = model.NewID(), validPlanDigest
	_, err = f.m.Apply(context.Background(), f.tenant, admin, missingReason)
	assertWorkVerdict(t, err, VerdictBroken, "invalid_command")

	missingDecision := f.command("lease.takeover", acquiredResult.Version, oldLease.Fence)
	missingDecision.HolderSID, missingDecision.Force = newSID, true
	missingDecision.Reason, missingDecision.ExpectedPlanHash = "operator-authorized recovery", validPlanDigest
	_, err = f.m.Apply(context.Background(), f.tenant, admin, missingDecision)
	assertWorkVerdict(t, err, VerdictBroken, "invalid_command")

	decision := decisionCommand(f.ready.ResultID, "decision.set", "lease-force-takeover")
	decision.ExpectedVersion = acquiredResult.Version
	decided := applyWorkLeaseCommand(t, f, f.holder, decision)

	takeover := f.command("lease.takeover", decided.Version, oldLease.Fence)
	takeover.HolderSID, takeover.Force = newSID, true
	takeover.Reason = "operator-authorized recovery after a stuck live holder"
	takeover.DecisionID = decided.ResultID
	plan, err := f.m.Plan(context.Background(), f.tenant, admin, takeover)
	if err != nil || plan.Verdict != VerdictClean || len(plan.PlanHash) != 64 {
		t.Fatalf("plan live force-takeover = %#v, %v", plan, err)
	}

	// FIRE: even a fully authorized command cannot actuate until it presents the
	// exact plan it just observed.
	missingPlan := takeover
	missingPlan.IdempotencyKey = model.NewID().String()
	_, err = f.m.Apply(context.Background(), f.tenant, admin, missingPlan)
	assertWorkVerdict(t, err, VerdictBroken, "plan_changed")
	if got := getWorkLease(t, f); got.Fence != oldLease.Fence || got.HolderSID != oldLease.HolderSID {
		t.Fatalf("missing plan changed live authority: %#v", got)
	}

	// FIRE: possession of the plan, reason and Decision does not replace the
	// administrative permission.
	nonAdmin := takeover
	nonAdmin.IdempotencyKey, nonAdmin.ExpectedPlanHash = model.NewID().String(), plan.PlanHash
	_, err = f.m.Apply(context.Background(), f.tenant, f.holder, nonAdmin)
	assertWorkVerdict(t, err, VerdictBroken, "forbidden")
	if got := getWorkLease(t, f); got.Fence != oldLease.Fence || got.HolderSID != oldLease.HolderSID {
		t.Fatalf("non-admin changed live authority: %#v", got)
	}

	// Positive direction: the complete override must succeed, so a deny-all
	// implementation cannot pass only the rejection assertions above.
	takeover.IdempotencyKey, takeover.ExpectedPlanHash = model.NewID().String(), plan.PlanHash
	takenResult := applyWorkLeaseCommand(t, f, admin, takeover)
	taken := getWorkLease(t, f)
	if taken.State != workLeaseActive || !taken.Live || taken.Fence != oldLease.Fence+1 ||
		taken.HolderSID != newSID {
		t.Fatalf("live force-takeover lease = %#v; old = %#v", taken, oldLease)
	}

	fact := readLatestLeaseEvent()
	wantReasonHash := hexHash(hashBytes([]byte(takeover.Reason)))
	if fact["forced"] != true || fact["severity"] != "high" ||
		fact["decision_id"] != decided.ResultID.String() ||
		fact["takeover_reason_hash"] != wantReasonHash {
		t.Fatalf("force-takeover event projection = %#v", fact)
	}
	encoded, err := json.Marshal(fact)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte(takeover.Reason)) {
		t.Fatalf("force-takeover event disclosed operator reason: %s", encoded)
	}

	// The swap minted one fence atomically: the previous token is stale while
	// the exact new holder can still renew without moving the fence.
	stale := f.command("lease.renew", takenResult.Version, oldLease.Fence)
	_, err = f.m.Apply(context.Background(), f.tenant, f.holder, stale)
	assertWorkVerdict(t, err, VerdictBroken, "stale_fence")
	current := f.command("lease.renew", takenResult.Version, taken.Fence)
	current.HolderSID = newSID
	applyWorkLeaseCommand(t, f, admin, current)
	if got := getWorkLease(t, f); got.Fence != taken.Fence || got.RenewalCount != 1 ||
		got.HolderSID != newSID {
		t.Fatalf("new force-takeover holder could not renew: %#v", got)
	}
}

func TestWorkLeaseDomainConcurrentAcquireHasOneWinner(t *testing.T) {
	t.Parallel()

	f := newWorkLeaseDomainFixture(t, "concurrent acquisition")
	secondSID, err := f.m.ResolveSession(context.Background(), f.tenant, SessionBinding{
		Provider: "work-lease-test", ExternalID: model.NewID().String(), At: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("resolve second SID: %v", err)
	}
	claimWorkLeaseTestSID(t, f.m, f.tenant, secondSID, f.holder.Actor)
	principals := []WorkPrincipal{f.holder, f.holder}
	principals[1].SessionID = secondSID
	commands := []WorkCommand{
		f.command("lease.acquire", f.ready.Version, 0),
		f.command("lease.acquire", f.ready.Version, 0),
	}
	commands[1].HolderSID = secondSID

	start := make(chan struct{})
	errs := make([]error, 2)
	results := make([]CommandResult, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	for i := range commands {
		go func(i int) {
			defer wg.Done()
			<-start
			results[i], errs[i] = f.m.Apply(
				context.Background(), f.tenant, principals[i], commands[i],
			)
		}(i)
	}
	close(start)
	wg.Wait()

	winner := -1
	for i, err := range errs {
		if err == nil {
			if winner != -1 {
				t.Fatalf("both acquisitions won: %#v", results)
			}
			winner = i
			continue
		}
		we := asWorkError(err)
		if we == nil || we.verdict != VerdictBroken ||
			(we.code != "version_mismatch" && we.code != "lease_held") {
			t.Fatalf("losing acquisition %d = %v", i, err)
		}
	}
	if winner == -1 {
		t.Fatalf("no acquisition won: %v", errs)
	}
	lease := getWorkLease(t, f)
	if lease.Fence != 1 || lease.State != workLeaseActive ||
		lease.HolderSID != commands[winner].HolderSID {
		t.Fatalf("concurrent acquisition lease = %#v; winner %d", lease, winner)
	}

	// Non-trigger direction: replaying the winner's exact command is a clean
	// receipt replay, not a second acquisition or another fence.
	replayed, err := f.m.Apply(
		context.Background(), f.tenant, principals[winner], commands[winner],
	)
	if err != nil || !replayed.Replayed || replayed.CommandID != results[winner].CommandID {
		t.Fatalf("winner replay = %#v, %v", replayed, err)
	}
	if got := getWorkLease(t, f); got.Fence != 1 || got.HolderSID != lease.HolderSID {
		t.Fatalf("winner replay changed lease: %#v", got)
	}
}

func TestWorkLeaseDomainConcurrentTakeoverHasOneWinner(t *testing.T) {
	t.Parallel()

	for _, be := range backends(t) {
		be := be
		t.Run(be.name, func(t *testing.T) {
			m, tenant, _ := be.open(t)
			f := addWorkLeaseDomainItem(
				t, workFixtureForBackend(t, m, tenant), "concurrent takeover",
			)
			testWorkLeaseDomainConcurrentTakeoverHasOneWinner(t, f)
		})
	}
}

func testWorkLeaseDomainConcurrentTakeoverHasOneWinner(
	t *testing.T,
	f workLeaseDomainFixture,
) {
	t.Helper()
	applyWorkLeaseCommand(t, f, f.holder, f.command("lease.acquire", f.ready.Version, 0))
	expireWorkLeaseWindow(t, f)
	if reaped, err := f.m.ReapWorkLeases(context.Background(), f.tenant, 10); err != nil || reaped != 1 {
		t.Fatalf("prepare expired lease = %d, %v", reaped, err)
	}
	expired := getWorkLease(t, f)
	item := getWorkSnapshot(t, f).Item

	principals := make([]WorkPrincipal, 2)
	commands := make([]WorkCommand, 2)
	for i := range principals {
		sid, err := f.m.ResolveSession(context.Background(), f.tenant, SessionBinding{
			Provider: "work-lease-takeover", ExternalID: model.NewID().String(), At: time.Now().UTC(),
		})
		if err != nil {
			t.Fatalf("resolve takeover SID %d: %v", i, err)
		}
		claimWorkLeaseTestSID(t, f.m, f.tenant, sid, f.holder.Actor)
		principals[i] = f.holder
		principals[i].Admin, principals[i].SessionID = true, sid
		commands[i] = f.command("lease.takeover", item.Version, expired.Fence)
		commands[i].HolderSID, commands[i].Unblock = sid, true
	}

	start := make(chan struct{})
	errs := make([]error, 2)
	results := make([]CommandResult, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	for i := range commands {
		go func(i int) {
			defer wg.Done()
			<-start
			results[i], errs[i] = f.m.Apply(
				context.Background(), f.tenant, principals[i], commands[i],
			)
		}(i)
	}
	close(start)
	wg.Wait()

	winner := -1
	for i, err := range errs {
		if err == nil {
			if winner != -1 {
				t.Fatalf("both takeovers won: %#v", results)
			}
			winner = i
			continue
		}
		we := asWorkError(err)
		if we == nil || we.status != http.StatusConflict || we.code != "stale_fence" {
			t.Fatalf("losing takeover %d = %v, want 409/stale_fence", i, err)
		}
	}
	if winner == -1 {
		t.Fatalf("no takeover won: %v", errs)
	}
	taken := getWorkLease(t, f)
	if taken.State != workLeaseActive || taken.Fence != expired.Fence+1 ||
		taken.HolderSID != commands[winner].HolderSID {
		t.Fatalf("concurrent takeover lease = %#v; winner %d", taken, winner)
	}

	// Non-trigger direction: after the winner releases, a later observed-fence
	// takeover is admitted instead of globally denying recovery.
	release := f.command("lease.release", results[winner].Version, taken.Fence)
	release.HolderSID = commands[winner].HolderSID
	release.Reason = "handoff after race"
	releasedResult := applyWorkLeaseCommand(t, f, principals[winner], release)
	released := getWorkLease(t, f)
	nextSID, err := f.m.ResolveSession(context.Background(), f.tenant, SessionBinding{
		Provider: "work-lease-takeover", ExternalID: model.NewID().String(), At: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("resolve recovery SID: %v", err)
	}
	claimWorkLeaseTestSID(t, f.m, f.tenant, nextSID, f.holder.Actor)
	next := f.holder
	next.Admin, next.SessionID = true, nextSID
	recover := f.command("lease.takeover", releasedResult.Version, released.Fence)
	recover.HolderSID, recover.Unblock = nextSID, true
	applyWorkLeaseCommand(t, f, next, recover)
	if got := getWorkLease(t, f); got.State != workLeaseActive ||
		got.Fence != released.Fence+1 || got.HolderSID != nextSID {
		t.Fatalf("takeover after release = %#v", got)
	}
}

func TestWorkLeaseDomainConcreteSessionCannotUseSiblingAgentLease(t *testing.T) {
	t.Parallel()

	f := newWorkLeaseDomainFixture(t, "session-bound agent lease")
	acquiredResult := applyWorkLeaseCommand(
		t, f, f.holder, f.command("lease.acquire", f.ready.Version, 0),
	)
	lease := getWorkLease(t, f)
	siblingSID, err := f.m.ResolveSession(context.Background(), f.tenant, SessionBinding{
		Provider: "work-lease-test", ExternalID: model.NewID().String(), At: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("resolve sibling SID: %v", err)
	}
	claimWorkLeaseTestSID(t, f.m, f.tenant, siblingSID, f.holder.Actor)
	sibling := f.holder
	sibling.SessionID = siblingSID
	renew := f.command("lease.renew", acquiredResult.Version, lease.Fence)
	_, err = f.m.Apply(context.Background(), f.tenant, sibling, renew)
	assertWorkVerdict(t, err, VerdictBroken, "forbidden")
	if got := getWorkLease(t, f); got.RenewalCount != 0 || got.Fence != lease.Fence {
		t.Fatalf("sibling SID changed WorkLease: %#v", got)
	}

	// Non-trigger direction: the exact session and unchanged fence still renew.
	renew.IdempotencyKey = model.NewID().String()
	applyWorkLeaseCommand(t, f, f.holder, renew)
	if got := getWorkLease(t, f); got.RenewalCount != 1 || got.Fence != lease.Fence {
		t.Fatalf("exact holder renewal = %#v", got)
	}
}

func TestWorkLeaseDomainTTLPolicyBoundaries(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		seconds int64
		want    time.Duration
	}{
		{name: "default", seconds: 0, want: defaultWorkLeaseTTL},
		{name: "minimum", seconds: 30, want: minWorkLeaseTTL},
		{name: "maximum", seconds: 1800, want: maxWorkLeaseTTL},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newWorkLeaseDomainFixture(t, "TTL "+tc.name)
			cmd := f.command("lease.acquire", f.ready.Version, 0)
			cmd.TTLSeconds = tc.seconds
			applyWorkLeaseCommand(t, f, f.holder, cmd)
			lease := getWorkLease(t, f)
			acquired, err := model.ParseTimestamp(lease.AcquiredAt)
			if err != nil {
				t.Fatalf("parse acquired_at: %v", err)
			}
			expires, err := model.ParseTimestamp(lease.ExpiresAt)
			if err != nil {
				t.Fatalf("parse expires_at: %v", err)
			}
			if got := expires.Time().Sub(acquired.Time()); got != tc.want {
				t.Fatalf("TTL delta = %s, want %s", got, tc.want)
			}
		})
	}

	for _, seconds := range []int64{29, 1801} {
		t.Run(fmt.Sprintf("reject-%d", seconds), func(t *testing.T) {
			f := newWorkLeaseDomainFixture(t, fmt.Sprintf("invalid TTL %d", seconds))
			cmd := f.command("lease.acquire", f.ready.Version, 0)
			cmd.TTLSeconds = seconds
			_, err := f.m.Apply(context.Background(), f.tenant, f.holder, cmd)
			assertWorkVerdict(t, err, VerdictBroken, "invalid_command")
			if got := getWorkLease(t, f); got.State != workLeaseVacant || got.Fence != 0 {
				t.Fatalf("invalid TTL changed WorkLease: %#v", got)
			}

			// Non-trigger direction: the nearest admitted boundary uses the same
			// command shape and acquires normally.
			if seconds < 30 {
				cmd.TTLSeconds = 30
			} else {
				cmd.TTLSeconds = 1800
			}
			cmd.IdempotencyKey = model.NewID().String()
			applyWorkLeaseCommand(t, f, f.holder, cmd)
			if got := getWorkLease(t, f); got.State != workLeaseActive || got.Fence != 1 {
				t.Fatalf("valid boundary lease = %#v", got)
			}
		})
	}
}

func TestWorkLeaseDomainReaperBlocksOnlyExpiredActive(t *testing.T) {
	t.Parallel()

	f := newWorkLeaseDomainFixture(t, "reaper")
	acquiredResult := applyWorkLeaseCommand(
		t, f, f.holder, f.command("lease.acquire", f.ready.Version, 0),
	)
	acquired := getWorkLease(t, f)

	// Non-trigger direction: a live active lease is invisible to the reaper and
	// its WorkItem remains active.
	reaped, err := f.m.ReapWorkLeases(context.Background(), f.tenant, 10)
	if err != nil || reaped != 0 {
		t.Fatalf("reap live lease = %d, %v", reaped, err)
	}
	if got := getWorkSnapshot(t, f).Item; got.Status != "active" || got.Version != acquiredResult.Version {
		t.Fatalf("live lease reaper changed WorkItem: %#v", got)
	}

	expireWorkLeaseWindow(t, f)
	reaped, err = f.m.ReapWorkLeases(context.Background(), f.tenant, 10)
	if err != nil || reaped != 1 {
		t.Fatalf("reap expired active lease = %d, %v", reaped, err)
	}
	lease := getWorkLease(t, f)
	item := getWorkSnapshot(t, f).Item
	if lease.State != workLeaseExpired || lease.Fence != acquired.Fence+1 ||
		item.Status != "blocked" || item.BlockedCode != "lease_expired" {
		t.Fatalf("reaper result: lease=%#v item=%#v", lease, item)
	}

	// Reaping the already-materialized terminal fact is a no-op.
	reaped, err = f.m.ReapWorkLeases(context.Background(), f.tenant, 10)
	if err != nil || reaped != 0 {
		t.Fatalf("second reap = %d, %v", reaped, err)
	}
}

type workLeaseFixedClockScope struct {
	store.Scope
	locker store.TransactionLocker
	now    model.Timestamp
}

func (s workLeaseFixedClockScope) TransactionNow(context.Context) (model.Timestamp, error) {
	return s.now, nil
}

func (s workLeaseFixedClockScope) LockTransaction(ctx context.Context, key string) error {
	if s.locker == nil {
		return store.ErrStoreUnavailable
	}
	return s.locker.LockTransaction(ctx, key)
}

type workLeaseFixedClockData struct {
	inner workData
	now   model.Timestamp
}

func (d workLeaseFixedClockData) wrap(sc store.Scope) store.Scope {
	locker, _ := sc.(store.TransactionLocker)
	return workLeaseFixedClockScope{Scope: sc, locker: locker, now: d.now}
}

func (d workLeaseFixedClockData) View(ctx context.Context, fn func(store.Scope) error) error {
	return d.inner.View(ctx, func(sc store.Scope) error { return fn(d.wrap(sc)) })
}

func (d workLeaseFixedClockData) Mutate(ctx context.Context, fn func(store.Scope) error) error {
	return d.inner.Mutate(ctx, func(sc store.Scope) error { return fn(d.wrap(sc)) })
}

func setWorkLeaseExpiryForTest(
	t *testing.T,
	f workLeaseDomainFixture,
	expires time.Time,
) {
	t.Helper()
	if err := f.st.Mutate(context.Background(), f.tenant, func(sc store.Scope) error {
		lease, found, err := findWorkLease(context.Background(), sc, f.ready.ResultID)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("work lease is absent")
		}
		lease[colLeaseRenewedAt] = model.NewTimestamp(expires.Add(-time.Minute)).String()
		lease[colLeaseExpiresAt] = model.NewTimestamp(expires).String()
		lease[colLeaseRenewalCount] = lease.Int(colLeaseRenewalCount) + 1
		repo, err := sc.Ext(workLeaseKind)
		if err != nil {
			return err
		}
		_, err = repo.Update(context.Background(), lease)
		return err
	}); err != nil {
		t.Fatalf("set WorkLease expiry: %v", err)
	}
}

func TestWorkLeaseDomainReaperIncludesExactExpiryBoundary(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name       string
		offset     time.Duration
		wantReaped int
		wantState  string
	}{
		{name: "equal is expired", offset: 0, wantReaped: 1, wantState: workLeaseExpired},
		{name: "future is live", offset: time.Millisecond, wantReaped: 0, wantState: workLeaseActive},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newWorkLeaseDomainFixture(t, "expiry boundary "+tc.name)
			applyWorkLeaseCommand(t, f, f.holder, f.command("lease.acquire", f.ready.Version, 0))
			fixed := time.Now().UTC().Add(2 * time.Minute).Truncate(time.Millisecond)
			setWorkLeaseExpiryForTest(t, f, fixed.Add(tc.offset))
			data := workLeaseFixedClockData{inner: f.m.workData(f.tenant), now: model.NewTimestamp(fixed)}

			reaped, err := f.m.reapWorkLeasesWithData(
				context.Background(), data, f.tenant, 10,
			)
			if err != nil || reaped != tc.wantReaped {
				t.Fatalf("boundary reaper = %d, %v; want %d", reaped, err, tc.wantReaped)
			}
			lease := getWorkLease(t, f)
			if lease.State != tc.wantState {
				t.Fatalf("boundary lease = %#v; want state %s", lease, tc.wantState)
			}
			item := getWorkSnapshot(t, f).Item
			if tc.wantReaped == 1 && (item.Status != "blocked" || item.BlockedCode != "lease_expired") {
				t.Fatalf("exact expiry WorkItem = %#v", item)
			}
			if tc.wantReaped == 0 && item.Status != "active" {
				t.Fatalf("future expiry WorkItem = %#v", item)
			}
		})
	}
}

func TestWorkLeaseDomainReaperIsolatesWorkspaceClockRollback(t *testing.T) {
	t.Parallel()

	base := newWorkFixture(t, filepath.Join(t.TempDir(), "work-lease-reaper-isolation.db"), nil)
	t.Cleanup(func() { _ = base.st.Close() })
	badClock := addWorkLeaseDomainItem(t, base, "rolled-back workspace")
	_, healthyWorkspace := workSchemaWorkspaces(t, context.Background(), base.m, base.tenant)
	healthyBase := base
	healthyBase.workspace = healthyWorkspace
	healthy := addWorkLeaseDomainItem(t, healthyBase, "healthy workspace")

	applyWorkLeaseCommand(
		t, badClock, badClock.holder,
		badClock.command("lease.acquire", badClock.ready.Version, 0),
	)
	applyWorkLeaseCommand(
		t, healthy, healthy.holder,
		healthy.command("lease.acquire", healthy.ready.Version, 0),
	)
	fixed := time.Now().UTC().Add(2 * time.Minute).Truncate(time.Millisecond)
	setWorkLeaseExpiryForTest(t, badClock, fixed.Add(-time.Millisecond))
	setWorkLeaseExpiryForTest(t, healthy, fixed.Add(-time.Millisecond))

	if err := base.st.Mutate(context.Background(), base.tenant, func(sc store.Scope) error {
		guard, found, err := leaseClockGuard(context.Background(), sc, badClock.workspace)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("rolled-back workspace has no clock guard")
		}
		guard[colGuardEpoch] = guard.Int(colGuardEpoch) + 1
		guard[colGuardLastDBTime] = model.NewTimestamp(fixed.Add(time.Hour)).String()
		repo, err := sc.Ext(workGuardKind)
		if err != nil {
			return err
		}
		_, err = repo.Update(context.Background(), guard)
		return err
	}); err != nil {
		t.Fatalf("seed workspace clock rollback: %v", err)
	}

	data := workLeaseFixedClockData{inner: base.m.workData(base.tenant), now: model.NewTimestamp(fixed)}
	reaped, err := base.m.reapWorkLeasesWithData(
		context.Background(), data, base.tenant, 10,
	)
	if reaped != 1 {
		t.Fatalf("workspace-isolated reaper count = %d, %v; want 1", reaped, err)
	}
	assertWorkVerdict(t, err, VerdictUnknown, "clock_rollback")
	if lease := getWorkLease(t, badClock); lease.State != workLeaseActive || lease.Fence != 1 {
		t.Fatalf("rolled-back workspace lease was asserted expired: %#v", lease)
	}
	if lease := getWorkLease(t, healthy); lease.State != workLeaseExpired || lease.Fence != 2 {
		t.Fatalf("healthy workspace did not recover: %#v", lease)
	}
	if item := getWorkSnapshot(t, healthy).Item; item.Status != "blocked" || item.BlockedCode != "lease_expired" {
		t.Fatalf("healthy workspace WorkItem = %#v", item)
	}
}

func TestWorkLeaseDomainDerivedClaimableLeasedOrphaned(t *testing.T) {
	t.Parallel()

	f := newWorkLeaseDomainFixture(t, "derived lease state")
	ready := getWorkSnapshot(t, f).Item
	if !ready.Claimable || ready.Leased || ready.Orphaned || ready.Lease == nil {
		t.Fatalf("ready vacant derivation = %#v", ready)
	}

	acquiredResult := applyWorkLeaseCommand(
		t, f, f.holder, f.command("lease.acquire", f.ready.Version, 0),
	)
	active := getWorkSnapshot(t, f).Item
	if active.Claimable || !active.Leased || active.Orphaned || active.Lease == nil ||
		!active.Lease.Live {
		t.Fatalf("active live derivation = %#v", active)
	}

	release := f.command("lease.release", acquiredResult.Version, active.Lease.Fence)
	release.Reason = "owner yielded"
	applyWorkLeaseCommand(t, f, f.holder, release)
	orphaned := getWorkSnapshot(t, f).Item
	if orphaned.Claimable || orphaned.Leased || !orphaned.Orphaned ||
		orphaned.Status != "blocked" {
		t.Fatalf("blocked unleased derivation = %#v", orphaned)
	}

	// Non-trigger direction: ready alone does not imply claimable. A user-owned
	// WorkItem has no machine/session execution authority to lease.
	userFixture := newWorkFixture(t, filepath.Join(t.TempDir(), "user-work.db"), nil)
	t.Cleanup(func() { _ = userFixture.st.Close() })
	created := applyCreate(t, userFixture, "ready user-owned item")
	readyUser, err := userFixture.m.Apply(
		context.Background(), userFixture.tenant, userFixture.principal, WorkCommand{
			Command: "item.ready", WorkItemID: created.ResultID,
			ExpectedVersion: created.Version, IdempotencyKey: model.NewID().String(),
			HTTPMethod: http.MethodPost,
		},
	)
	if err != nil {
		t.Fatalf("make user-owned WorkItem ready: %v", err)
	}
	snapshot, err := userFixture.m.Get(
		context.Background(), userFixture.tenant, userFixture.principal, readyUser.ResultID,
	)
	if err != nil || snapshot.Item.Claimable || snapshot.Item.Leased || snapshot.Item.Orphaned {
		t.Fatalf("user-owned ready derivation = %#v, %v", snapshot.Item, err)
	}
}

type workLeaseLockerOnlyScope struct {
	store.Scope
	locker store.TransactionLocker
}

func (s workLeaseLockerOnlyScope) LockTransaction(ctx context.Context, key string) error {
	return s.locker.LockTransaction(ctx, key)
}

type workLeaseClockOnlyScope struct {
	store.Scope
	clock store.TransactionClock
}

func (s workLeaseClockOnlyScope) TransactionNow(ctx context.Context) (model.Timestamp, error) {
	return s.clock.TransactionNow(ctx)
}

type workLeaseMissingCapabilityData struct {
	inner        workData
	missingClock bool
}

func (d workLeaseMissingCapabilityData) View(
	ctx context.Context,
	fn func(store.Scope) error,
) error {
	return d.inner.View(ctx, fn)
}

func (d workLeaseMissingCapabilityData) Mutate(
	ctx context.Context,
	fn func(store.Scope) error,
) error {
	return d.inner.Mutate(ctx, func(sc store.Scope) error {
		if d.missingClock {
			locker, ok := sc.(store.TransactionLocker)
			if !ok {
				return fmt.Errorf("fixture scope has no TransactionLocker")
			}
			return fn(workLeaseLockerOnlyScope{Scope: sc, locker: locker})
		}
		clock, ok := sc.(store.TransactionClock)
		if !ok {
			return fmt.Errorf("fixture scope has no TransactionClock")
		}
		return fn(workLeaseClockOnlyScope{Scope: sc, clock: clock})
	})
}

func TestWorkLeaseDomainMissingTransactionCapabilitiesAreUnknown(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name         string
		missingClock bool
		code         string
	}{
		{name: "clock", missingClock: true, code: "clock_unavailable"},
		{name: "locker", missingClock: false, code: "coordination_unavailable"},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			f := newWorkLeaseDomainFixture(t, "missing "+tc.name)
			cmd := f.command("lease.acquire", f.ready.Version, 0)
			data := workLeaseMissingCapabilityData{
				inner: f.m.workData(f.tenant), missingClock: tc.missingClock,
			}
			_, err := f.m.applyWithData(
				context.Background(), data, f.tenant, f.holder, cmd,
			)
			assertWorkVerdict(t, err, VerdictUnknown, tc.code)
			before := getWorkSnapshot(t, f).Item
			if before.Status != "ready" || !before.Claimable || before.Lease == nil ||
				before.Lease.State != workLeaseVacant || before.Lease.Fence != 0 {
				t.Fatalf("missing %s mutated state: %#v", tc.name, before)
			}

			// Non-trigger direction: the unchanged command succeeds on the real
			// scope, proving the failure came from the missing capability.
			applied, err := f.m.Apply(context.Background(), f.tenant, f.holder, cmd)
			if err != nil || applied.Verdict != VerdictClean {
				t.Fatalf("real scope after missing %s = %#v, %v", tc.name, applied, err)
			}
			if got := getWorkLease(t, f); got.State != workLeaseActive || got.Fence != 1 {
				t.Fatalf("real scope acquisition after missing %s = %#v", tc.name, got)
			}
		})
	}
}

func TestWorkLeaseDomainRenewAcceptsIdenticalTransactionClockAcrossBackends(t *testing.T) {
	t.Parallel()

	for _, be := range backends(t) {
		be := be
		t.Run(be.name, func(t *testing.T) {
			m, tenant, _ := be.open(t)
			f := workFixtureForBackend(t, m, tenant)
			agentRef := "agent:" + model.NewID().String()
			holder := WorkPrincipal{
				ActorKind: model.ActorAgent,
				ActorRef:  agentRef,
				Actor:     agentRef,
			}
			create := baseCreateCommand(f, "fixed transaction clock lease")
			create.OwnerKind, create.OwnerRef = "agent", agentRef
			created, err := m.Apply(context.Background(), tenant, holder, create)
			if err != nil {
				t.Fatalf("create WorkItem: %v", err)
			}
			ready, err := m.Apply(context.Background(), tenant, holder, WorkCommand{
				Command: "item.ready", WorkItemID: created.ResultID,
				ExpectedVersion: created.Version, IdempotencyKey: model.NewID().String(),
				HTTPMethod: http.MethodPost,
			})
			if err != nil {
				t.Fatalf("make WorkItem ready: %v", err)
			}
			sid, err := m.ResolveSession(context.Background(), tenant, SessionBinding{
				Provider: "fixed-work-lease-clock", ExternalID: model.NewID().String(),
				At: time.Now().UTC(),
			})
			if err != nil {
				t.Fatalf("resolve holder SID: %v", err)
			}
			claimWorkLeaseTestSID(t, m, tenant, sid, holder.Actor)
			holder.SessionID = sid

			var fixed model.Timestamp
			if err := m.data.View(context.Background(), tenant, func(sc store.Scope) error {
				clock, ok := sc.(store.TransactionClock)
				if !ok {
					return fmt.Errorf("fixture scope has no TransactionClock")
				}
				fixed, err = clock.TransactionNow(context.Background())
				return err
			}); err != nil {
				t.Fatalf("capture transaction clock: %v", err)
			}
			data := workLeaseFixedClockData{inner: m.workData(tenant), now: fixed}
			command := func(name string, version, fence int64) WorkCommand {
				return WorkCommand{
					Command: name, WorkItemID: ready.ResultID,
					HolderSID: sid, HolderAgentRef: agentRef, Fence: fence,
					TTLSeconds: 60, ExpectedVersion: version,
					IdempotencyKey: model.NewID().String(), HTTPMethod: http.MethodPost,
				}
			}
			apply := func(cmd WorkCommand) CommandResult {
				t.Helper()
				result, applyErr := m.applyWithData(
					context.Background(), data, tenant, holder, cmd,
				)
				if applyErr != nil {
					t.Fatalf("apply %s: %v", cmd.Command, applyErr)
				}
				return result
			}
			get := func() WorkLease {
				t.Helper()
				lease, getErr := m.getLeaseWithData(
					context.Background(), data, ready.ResultID,
				)
				if getErr != nil {
					t.Fatalf("get WorkLease: %v", getErr)
				}
				return lease
			}

			acquiredResult := apply(command("lease.acquire", ready.Version, 0))
			acquired := get()
			firstResult := apply(command("lease.renew", acquiredResult.Version, acquired.Fence))
			first := get()
			secondResult := apply(command("lease.renew", firstResult.Version, first.Fence))
			second := get()
			wantExpiry := model.NewTimestamp(fixed.Time().Add(time.Minute)).String()
			if secondResult.Verdict != VerdictClean || second.Fence != acquired.Fence ||
				second.RenewalCount != 2 || second.RenewedAt != fixed.String() ||
				second.ExpiresAt != wantExpiry {
				t.Fatalf("two equal-clock renewals = result %#v, lease %#v", secondResult, second)
			}

			assertGuardRejects := func(name string, mutate func(model.Record)) {
				t.Helper()
				before := workSchemaGetLease(t, m, tenant, ready.ResultID)
				candidate := workSchemaClone(before)
				candidate[colLeaseRenewedAt] = fixed.String()
				candidate[colLeaseExpiresAt] = wantExpiry
				candidate[colLeaseRenewalCount] = before.Int(colLeaseRenewalCount) + 1
				mutate(candidate)
				if _, updateErr := workSchemaUpdateLease(t, m, tenant, candidate); updateErr == nil {
					t.Fatalf("%s WorkLease update succeeded", name)
				}
				after := workSchemaGetLease(t, m, tenant, ready.ResultID)
				if after.Int(colLeaseFence) != before.Int(colLeaseFence) ||
					after.Int(colLeaseRenewalCount) != before.Int(colLeaseRenewalCount) ||
					after.String(colLeaseHolderSID) != before.String(colLeaseHolderSID) ||
					after.String(colLeaseRenewedAt) != before.String(colLeaseRenewedAt) ||
					after.String(colLeaseExpiresAt) != before.String(colLeaseExpiresAt) {
					t.Fatalf("rejected %s update changed WorkLease: before %#v after %#v", name, before, after)
				}
			}
			assertGuardRejects("changed holder", func(candidate model.Record) {
				candidate[colLeaseHolderSID] = "osn_" + model.NewID().String()
			})
			assertGuardRejects("changed fence", func(candidate model.Record) {
				candidate[colLeaseFence] = candidate.Int(colLeaseFence) + 1
			})
		})
	}
}

func TestWorkLeaseDomainClockRollbackRequiresAuditedRebase(t *testing.T) {
	t.Parallel()

	f := newWorkLeaseDomainFixture(t, "clock rollback")
	decision := decisionCommand(f.ready.ResultID, "decision.set", "lease-clock-rebase")
	decision.ExpectedVersion = f.ready.Version
	decided := applyWorkLeaseCommand(t, f, f.holder, decision)

	var future model.Timestamp
	if err := f.st.Mutate(context.Background(), f.tenant, func(sc store.Scope) error {
		guard, found, err := leaseClockGuard(context.Background(), sc, f.workspace)
		if err != nil {
			return err
		}
		future = model.NewTimestamp(time.Now().UTC().Add(time.Hour))
		repo, err := sc.Ext(workGuardKind)
		if err != nil {
			return err
		}
		if !found {
			_, err = repo.Create(context.Background(), model.Record{
				colWorkWorkspaceID: f.workspace.String(), colGuardKind: "lease_clock",
				colGuardEpoch: int64(1), colGuardLastDBTime: future.String(),
			})
			return err
		}
		guard[colGuardLastDBTime] = future.String()
		_, err = repo.Update(context.Background(), guard)
		return err
	}); err != nil {
		t.Fatalf("seed future lease clock observation: %v", err)
	}

	acquire := f.command("lease.acquire", decided.Version, 0)
	_, err := f.m.Apply(context.Background(), f.tenant, f.holder, acquire)
	assertWorkVerdict(t, err, VerdictUnknown, "clock_rollback")
	if got := getWorkLease(t, f); got.State != workLeaseVacant || got.Fence != 0 {
		t.Fatalf("clock rollback acquired lease: %#v", got)
	}

	admin := f.holder
	admin.Admin = true
	rebase := WorkCommand{
		Command: "lease.clock_rebase", WorkItemID: f.ready.ResultID,
		DecisionID: decided.ResultID, EvidenceRef: "incident:clock-source-restored",
		ExpectedVersion: decided.Version, IdempotencyKey: model.NewID().String(),
		HTTPMethod: http.MethodPost,
	}
	rebased := applyWorkLeaseCommand(t, f, admin, rebase)
	if err := f.st.View(context.Background(), f.tenant, func(sc store.Scope) error {
		guard, found, err := leaseClockGuard(context.Background(), sc, f.workspace)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("rebased lease clock guard is absent")
		}
		at, err := model.ParseTimestamp(guard.String(colGuardLastDBTime))
		if err != nil {
			return err
		}
		if !at.Before(future) || guard.String(colGuardRebaseDecision) != decided.ResultID.String() ||
			guard.String(colGuardRebaseEvidence) != rebase.EvidenceRef {
			t.Fatalf("rebased guard = %#v; seeded future = %s", guard, future.String())
		}
		return nil
	}); err != nil {
		t.Fatalf("read rebased guard: %v", err)
	}

	// Trigger direction: without a fresh rollback, an admin cannot launder the
	// clock evidence through another rebase.
	rebase.ExpectedVersion = rebased.Version
	rebase.IdempotencyKey = model.NewID().String()
	_, err = f.m.Apply(context.Background(), f.tenant, admin, rebase)
	assertWorkVerdict(t, err, VerdictBroken, "clock_not_rolled_back")

	// Non-trigger direction: once the guarded rollback is rebased, the unchanged
	// acquisition is admitted and mints the first fence.
	acquire.ExpectedVersion = rebased.Version
	acquire.IdempotencyKey = model.NewID().String()
	acquired := applyWorkLeaseCommand(t, f, f.holder, acquire)
	if lease := getWorkLease(t, f); acquired.Status != "active" || lease.Fence != 1 || !lease.Live {
		t.Fatalf("acquire after clock rebase = %#v, lease %#v", acquired, lease)
	}
}

func TestWorkLeaseDomainExpiryAtPenultimateFenceCommitsRefusal(t *testing.T) {
	t.Parallel()

	f := newWorkFixture(t, filepath.Join(t.TempDir(), "work-lease-exhaustion.db"), nil)
	t.Cleanup(func() { _ = f.st.Close() })
	ctx := context.Background()

	seed := func(title string, fence int64) (model.ID, WorkPrincipal, WorkCommand) {
		t.Helper()
		agentRef := "agent:" + model.NewID().String()
		itemInput := workSchemaItem(f.workspace, title)
		at := model.NewTimestamp(time.Now().UTC().Add(-time.Hour)).String()
		itemInput[colWorkStatus], itemInput[colWorkOwnerKind], itemInput[colWorkOwnerRef] =
			"active", "agent", agentRef
		itemInput[colWorkReadyAt], itemInput[colWorkStartedAt] = at, at
		item := workSchemaMustCreate(t, ctx, f.m, f.tenant, workItemKind, itemInput)
		itemID := recordID(item)
		sid, err := f.m.ResolveSession(ctx, f.tenant, SessionBinding{
			Provider: "work-lease-exhaustion", ExternalID: model.NewID().String(), At: time.Now().UTC(),
		})
		if err != nil {
			t.Fatalf("resolve exhaustion SID: %v", err)
		}
		claimWorkLeaseTestSID(t, f.m, f.tenant, sid, agentRef)
		past := time.Now().UTC().Add(-2 * time.Minute)
		workSchemaMustCreate(t, ctx, f.m, f.tenant, workLeaseKind, model.Record{
			colWorkWorkspaceID: f.workspace.String(), colWorkItemID: itemID.String(),
			colLeaseHolderSID: sid, colLeaseHolderAgentRef: agentRef,
			colLeaseFence: fence, colLeaseState: workLeaseActive,
			colLeaseAcquiredAt:   model.NewTimestamp(past).String(),
			colLeaseExpiresAt:    model.NewTimestamp(past.Add(time.Minute)).String(),
			colLeaseRenewalCount: int64(0),
		})
		principal := WorkPrincipal{
			ActorKind: model.ActorAgent, ActorRef: agentRef, Actor: agentRef, SessionID: sid,
		}
		cmd := WorkCommand{
			Command: "lease.acquire", WorkItemID: itemID,
			HolderSID: sid, HolderAgentRef: agentRef, TTLSeconds: 60,
			ExpectedVersion: item.Int(model.ColVersion), IdempotencyKey: model.NewID().String(),
			HTTPMethod: http.MethodPost,
		}
		return itemID, principal, cmd
	}

	exhaustedID, exhaustedPrincipal, exhaustedCmd := seed("penultimate fence", math.MaxInt64-1)
	exhaustedPlan, err := f.m.Plan(ctx, f.tenant, exhaustedPrincipal, exhaustedCmd)
	if err != nil || exhaustedPlan.EventType != "work.lease.ended" ||
		len(exhaustedPlan.EventTypes) != 0 ||
		countString(exhaustedPlan.RowEffects, "sessions.work_event:append") != 1 ||
		countString(exhaustedPlan.RowEffects, "sessions.work_outbox:insert") != 1 {
		t.Fatalf("exhausted plan = %#v, %v; want one ended fact", exhaustedPlan, err)
	}
	result, err := f.m.Apply(ctx, f.tenant, exhaustedPrincipal, exhaustedCmd)
	assertWorkVerdict(t, err, VerdictBroken, "fence_exhausted")
	if result.Verdict != VerdictBroken || result.Code != "fence_exhausted" || result.EventID.IsZero() {
		t.Fatalf("post-commit refusal result = %#v, %v", result, err)
	}
	exhausted, err := f.m.GetLease(ctx, f.tenant, exhaustedPrincipal, exhaustedID)
	if err != nil || exhausted.State != workLeaseExpired || exhausted.Fence != math.MaxInt64 {
		t.Fatalf("durable exhausted expiry = %#v, %v", exhausted, err)
	}
	snapshot, err := f.m.Get(ctx, f.tenant, exhaustedPrincipal, exhaustedID)
	if err != nil || snapshot.Item.Status != "blocked" || snapshot.Item.BlockedCode != "lease_expired" {
		t.Fatalf("durable exhausted WorkItem = %#v, %v", snapshot.Item, err)
	}
	replay, err := f.m.Apply(ctx, f.tenant, exhaustedPrincipal, exhaustedCmd)
	assertWorkVerdict(t, err, VerdictBroken, "fence_exhausted")
	if !replay.Replayed || replay.CommandID != result.CommandID || replay.EventID != result.EventID {
		t.Fatalf("exhaustion replay = %#v, %v; first %#v", replay, err, result)
	}

	// Non-trigger direction: with two monotonic values remaining, the expiry and
	// replacement acquisition both commit and the new holder owns MaxInt64.
	controlID, controlPrincipal, controlCmd := seed("two fences remain", math.MaxInt64-2)
	controlPlan, err := f.m.Plan(ctx, f.tenant, controlPrincipal, controlCmd)
	if err != nil || controlPlan.EventType != "work.lease.acquired" ||
		!slices.Equal(controlPlan.EventTypes, []string{"work.lease.ended", "work.lease.acquired"}) ||
		countString(controlPlan.RowEffects, "sessions.work_event:append") != 2 ||
		countString(controlPlan.RowEffects, "sessions.work_outbox:insert") != 2 {
		t.Fatalf("two-fence plan = %#v, %v; want ordered expiry and acquisition", controlPlan, err)
	}
	control, err := f.m.Apply(ctx, f.tenant, controlPrincipal, controlCmd)
	if err != nil || control.Verdict != VerdictClean {
		t.Fatalf("control acquisition = %#v, %v", control, err)
	}
	lease, err := f.m.GetLease(ctx, f.tenant, controlPrincipal, controlID)
	if err != nil || lease.State != workLeaseActive || lease.Fence != math.MaxInt64 || !lease.Live {
		t.Fatalf("control lease = %#v, %v", lease, err)
	}
}
