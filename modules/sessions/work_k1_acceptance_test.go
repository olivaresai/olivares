// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

type fixedWorkContent struct {
	decision ContentDecision
	err      error
}

func (g fixedWorkContent) Inspect(context.Context, model.TenantID, model.ID, string, []byte) (ContentDecision, error) {
	return g.decision, g.err
}

type fixedWorkIdentity struct {
	participant Participant
	err         error
}

func (r fixedWorkIdentity) ResolveParticipant(context.Context, model.TenantID, model.ID, string, string) (Participant, error) {
	return r.participant, r.err
}

func (fixedWorkIdentity) SessionActsForAgent(context.Context, model.TenantID, string, string) (bool, error) {
	return false, nil
}

type withoutTransactionClock struct{ inner workData }

func (d withoutTransactionClock) View(ctx context.Context, fn func(store.Scope) error) error {
	return d.inner.View(ctx, func(sc store.Scope) error {
		return fn(struct{ store.Scope }{Scope: sc})
	})
}

func (d withoutTransactionClock) Mutate(ctx context.Context, fn func(store.Scope) error) error {
	return d.inner.Mutate(ctx, func(sc store.Scope) error {
		return fn(struct{ store.Scope }{Scope: sc})
	})
}

func TestWorkPlanHashBindsSemanticCommandButNotObservationTime(t *testing.T) {
	t.Parallel()

	f := newWorkFixture(t, ":memory:", nil)
	defer f.st.Close()
	cmd := baseCreateCommand(f, "stable plan")

	first, err := f.m.Plan(context.Background(), f.tenant, f.principal, cmd)
	if err != nil || first.Verdict != VerdictClean {
		t.Fatalf("first plan = %#v, %v", first, err)
	}
	time.Sleep(2 * time.Millisecond)
	second, err := f.m.Plan(context.Background(), f.tenant, f.principal, cmd)
	if err != nil || second.PlanHash != first.PlanHash || second.ObservedAt == first.ObservedAt {
		t.Fatalf("same semantic plan changed: first=%#v second=%#v err=%v", first, second, err)
	}

	changed := cmd
	changed.Title = "different semantic command"
	third, err := f.m.Plan(context.Background(), f.tenant, f.principal, changed)
	if err != nil || third.Verdict != VerdictClean || third.PlanHash == first.PlanHash {
		t.Fatalf("different command did not change plan hash: %#v, %v", third, err)
	}
	if got := workCount(t, f, workItemKind); got != 0 {
		t.Fatalf("plan wrote %d work items", got)
	}
}

func TestWorkPlanDetectsCycleAndKeepsIndependentEdgeClean(t *testing.T) {
	t.Parallel()

	f := newWorkFixture(t, ":memory:", nil)
	defer f.st.Close()
	a, b := applyCreate(t, f, "plan-a"), applyCreate(t, f, "plan-b")
	c, d := applyCreate(t, f, "plan-c"), applyCreate(t, f, "plan-d")

	if _, err := f.m.Apply(context.Background(), f.tenant, f.principal, WorkCommand{
		Command: "dependency.add", WorkItemID: a.ResultID, DependsOnID: b.ResultID,
		ExpectedVersion: a.Version, IdempotencyKey: model.NewID().String(), HTTPMethod: http.MethodPost,
	}); err != nil {
		t.Fatal(err)
	}
	before := workCount(t, f, workDependencyKind)
	t.Run("cycle is broken", func(t *testing.T) {
		cycle, err := f.m.Plan(context.Background(), f.tenant, f.principal, WorkCommand{
			Command: "dependency.add", WorkItemID: b.ResultID, DependsOnID: a.ResultID,
		})
		if err != nil || cycle.Verdict != VerdictBroken || cycle.Code != "dependency_cycle" {
			t.Fatalf("cycle plan = %#v, %v", cycle, err)
		}
	})
	t.Run("independent edge is clean", func(t *testing.T) {
		independent, err := f.m.Plan(context.Background(), f.tenant, f.principal, WorkCommand{
			Command: "dependency.add", WorkItemID: c.ResultID, DependsOnID: d.ResultID,
		})
		if err != nil || independent.Verdict != VerdictClean {
			t.Fatalf("independent plan = %#v, %v", independent, err)
		}
	})
	if got := workCount(t, f, workDependencyKind); got != before {
		t.Fatalf("plans changed dependency rows: before=%d after=%d", before, got)
	}
}

func TestWorkApplyFailsClosedWithoutTransactionClock(t *testing.T) {
	t.Parallel()

	f := newWorkFixture(t, ":memory:", nil)
	defer f.st.Close()
	cmd := baseCreateCommand(f, "clock required")

	_, err := f.m.applyWithData(context.Background(), withoutTransactionClock{inner: f.m.workData(f.tenant)}, f.tenant, f.principal, cmd)
	if we := asWorkError(err); we == nil || we.verdict != VerdictUnknown || we.code != "clock_unavailable" {
		t.Fatalf("apply without transaction clock = %v", err)
	}
	if got := workCount(t, f, workItemKind); got != 0 {
		t.Fatalf("clock failure wrote %d work items", got)
	}
	if result, err := f.m.Apply(context.Background(), f.tenant, f.principal, cmd); err != nil || result.Code != "applied" {
		t.Fatalf("neighbor with transaction clock = %#v, %v", result, err)
	}
}

func TestWorkContentAndIdentityPreflightsHaveThreeOutcomes(t *testing.T) {
	t.Parallel()

	f := newWorkFixture(t, ":memory:", nil)
	defer f.st.Close()
	cmd := baseCreateCommand(f, "preflight outcomes")

	f.m.UseWorkContentGuard(fixedWorkContent{decision: ContentDecision{Allowed: false, Code: "secret_rejected"}})
	if assessment, err := f.m.Validate(context.Background(), f.tenant, f.principal, cmd); err != nil ||
		assessment.Verdict != VerdictBroken || assessment.Code != "secret_rejected" {
		t.Fatalf("content rejection = %#v, %v", assessment, err)
	}
	if got := workCount(t, f, workItemKind); got != 0 {
		t.Fatalf("content rejection wrote %d work items", got)
	}

	f.m.UseWorkContentGuard(fixedWorkContent{err: errors.New("scanner offline")})
	if assessment, err := f.m.Validate(context.Background(), f.tenant, f.principal, cmd); err != nil ||
		assessment.Verdict != VerdictUnknown || assessment.Code != "policy_unavailable" {
		t.Fatalf("content outage = %#v, %v", assessment, err)
	}

	f.m.UseWorkContentGuard(allowWorkContent{})
	f.m.UseWorkIdentityResolver(fixedWorkIdentity{participant: Participant{
		Kind: cmd.OwnerKind, CanonicalRef: cmd.OwnerRef, Active: false, WorkspaceEligible: true,
	}})
	if assessment, err := f.m.Validate(context.Background(), f.tenant, f.principal, cmd); err != nil ||
		assessment.Verdict != VerdictBroken || assessment.Code != "owner_ineligible" {
		t.Fatalf("inactive owner = %#v, %v", assessment, err)
	}

	f.m.UseWorkIdentityResolver(fixedWorkIdentity{err: errors.New("identity store offline")})
	if assessment, err := f.m.Validate(context.Background(), f.tenant, f.principal, cmd); err != nil ||
		assessment.Verdict != VerdictUnknown || assessment.Code != "evidence_unavailable" {
		t.Fatalf("identity outage = %#v, %v", assessment, err)
	}

	f.m.UseWorkIdentityResolver(allowWorkIdentity{})
	if result, err := f.m.Apply(context.Background(), f.tenant, f.principal, cmd); err != nil || result.Code != "applied" {
		t.Fatalf("clean neighboring preflights = %#v, %v", result, err)
	}
}

func TestWorkExactReplaySurvivesObserverOutage(t *testing.T) {
	t.Parallel()

	f := newWorkFixture(t, ":memory:", nil)
	defer f.st.Close()
	cmd := baseCreateCommand(f, "durable replay")

	first, err := f.m.Apply(context.Background(), f.tenant, f.principal, cmd)
	if err != nil {
		t.Fatal(err)
	}
	f.m.UseWorkContentGuard(fixedWorkContent{err: errors.New("scanner offline")})
	f.m.UseWorkIdentityResolver(fixedWorkIdentity{err: errors.New("identity store offline")})
	replay, err := f.m.Apply(context.Background(), f.tenant, f.principal, cmd)
	if err != nil || !replay.Replayed || replay.CommandID != first.CommandID || replay.ResultID != first.ResultID {
		t.Fatalf("exact replay during observer outage = %#v, %v; original=%#v", replay, err, first)
	}

	newDelivery := cmd
	newDelivery.IdempotencyKey = model.NewID().String()
	if _, err := f.m.Apply(context.Background(), f.tenant, f.principal, newDelivery); err == nil {
		t.Fatal("new delivery passed while content policy was unavailable")
	} else if we := asWorkError(err); we == nil || we.verdict != VerdictUnknown || we.code != "policy_unavailable" {
		t.Fatalf("new delivery with content outage = %v", err)
	}
	f.m.UseWorkContentGuard(allowWorkContent{})
	newDelivery.IdempotencyKey = model.NewID().String()
	if _, err := f.m.Apply(context.Background(), f.tenant, f.principal, newDelivery); err == nil {
		t.Fatal("new delivery passed while identity evidence was unavailable")
	} else if we := asWorkError(err); we == nil || we.verdict != VerdictUnknown || we.code != "evidence_unavailable" {
		t.Fatalf("new delivery with identity outage = %v", err)
	}
	if got := workCount(t, f, workItemKind); got != 1 {
		t.Fatalf("observer outage paths left %d work items, want original only", got)
	}
}

func TestWorkAuditGapCommitsNoDomainRows(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "work-audit-gap.db")
	f := newWorkFixture(t, dsn, nil)
	if err := f.st.Close(); err != nil {
		t.Fatalf("close provisioned store: %v", err)
	}

	f.m = New(WithWorkIdentityResolver(allowWorkIdentity{}), WithWorkContentGuard(allowWorkContent{}))
	degradedStore, err := engine.Open(ctx, store.Config{
		Engine: store.EngineSQLite, DSN: dsn, Debug: true,
		AuditSpoolMaxBytes: 1, AuditSpoolOnFull: store.AuditSpoolDegrade,
	}, f.m.RegisterSchema)
	if err != nil {
		t.Fatalf("reopen store with degraded audit spool: %v", err)
	}
	f.st = degradedStore
	f.m.UseData(api.NewModuleData(f.st))
	defer f.st.Close()
	statusStore, ok := f.st.(store.AuditSpoolStatuser)
	if !ok {
		t.Fatal("store does not expose audit spool status")
	}
	before, configured, err := statusStore.AuditSpoolStatus(ctx)
	if err != nil || !configured {
		t.Fatalf("audit status before = %#v, configured=%v err=%v", before, configured, err)
	}

	_, err = f.m.Apply(ctx, f.tenant, f.principal, baseCreateCommand(f, "audit gap"))
	if we := asWorkError(err); we == nil || we.verdict != VerdictUnknown || we.code != "evidence_unavailable" {
		t.Fatalf("degraded audit apply = %v", err)
	}
	for _, kind := range []model.Kind{workItemKind, workCommandKind, workEventKind, workOutboxKind} {
		if got := workCount(t, f, kind); got != 0 {
			t.Fatalf("audit gap wrote %d rows of %s", got, kind)
		}
	}
	after, _, err := statusStore.AuditSpoolStatus(ctx)
	if err != nil || after.PendingDrops <= before.PendingDrops {
		t.Fatalf("audit gap was not accounted: before=%#v after=%#v err=%v", before, after, err)
	}
}

func TestWorkSemanticAuditAnchorsReceiptAndEvent(t *testing.T) {
	t.Parallel()

	f := newWorkFixture(t, ":memory:", nil)
	defer f.st.Close()
	cmd := baseCreateCommand(f, "semantic audit")
	plan, err := f.m.Plan(context.Background(), f.tenant, f.principal, cmd)
	if err != nil {
		t.Fatal(err)
	}
	cmd.ExpectedPlanHash = plan.PlanHash
	result, err := f.m.Apply(context.Background(), f.tenant, f.principal, cmd)
	if err != nil {
		t.Fatal(err)
	}
	wantPlanHash, err := decodeHash(plan.PlanHash, true)
	if err != nil {
		t.Fatal(err)
	}

	var semantic model.AuditEvent
	if err := f.st.View(context.Background(), f.tenant, func(sc store.Scope) error {
		if err := sc.Audit().Walk(context.Background(), 1, func(ev model.AuditEvent) error {
			if ev.Action == "sessions.work.item.create" && ev.TargetID == result.CommandID {
				semantic = ev
			}
			return nil
		}); err != nil {
			return err
		}
		events, err := sc.Ext(workEventKind)
		if err != nil {
			return err
		}
		eventRows, err := listAll(context.Background(), events, model.Filter{
			Column: colEventID, Op: model.OpEq, Value: result.EventID.String(),
		})
		if err != nil {
			return err
		}
		receipts, err := sc.Ext(workCommandKind)
		if err != nil {
			return err
		}
		receiptRows, err := listAll(context.Background(), receipts, model.Filter{
			Column: colCommandID, Op: model.OpEq, Value: result.CommandID.String(),
		})
		if err != nil {
			return err
		}
		if len(eventRows) != 1 || len(receiptRows) != 1 ||
			eventRows[0].Int(colEventAuditSeq) != semantic.Seq ||
			receiptRows[0].Int(colCommandAuditSeq) != semantic.Seq ||
			!bytesEqual(eventRows[0].Bytes(colEventAuditHash), semantic.Hash) ||
			!bytesEqual(receiptRows[0].Bytes(colCommandAuditHash), semantic.Hash) {
			t.Fatalf("semantic anchors: audit=%#v event=%#v receipt=%#v", semantic, eventRows, receiptRows)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if semantic.Seq < 1 || semantic.TargetKind != workCommandKind ||
		!bytesEqual(semantic.PayloadHash, wantPlanHash) {
		t.Fatalf("semantic audit = %#v, want command target and plan commitment", semantic)
	}
}

func TestWorkIdempotencyIsActorScopedAndExactReplayStaysLocal(t *testing.T) {
	t.Parallel()

	t.Run("different actors have distinct receipt scope", func(t *testing.T) {
		f := newWorkFixture(t, ":memory:", nil)
		defer f.st.Close()
		cmd := baseCreateCommand(f, "actor-scoped receipt")
		first, err := f.m.Apply(context.Background(), f.tenant, f.principal, cmd)
		if err != nil {
			t.Fatal(err)
		}
		other := f.principal
		other.ActorRef = model.NewID().String()
		other.Actor = "user:" + other.ActorRef
		second, err := f.m.Apply(context.Background(), f.tenant, other, cmd)
		if err != nil || second.ResultID == first.ResultID || second.Replayed {
			t.Fatalf("second actor with same key = %#v, %v; first=%#v", second, err, first)
		}
		if got := workCount(t, f, workItemKind); got != 2 {
			t.Fatalf("actor-scoped receipts created %d work items, want 2", got)
		}
	})

	t.Run("same actor replays its exact receipt", func(t *testing.T) {
		f := newWorkFixture(t, ":memory:", nil)
		defer f.st.Close()
		cmd := baseCreateCommand(f, "same-actor replay")
		first, err := f.m.Apply(context.Background(), f.tenant, f.principal, cmd)
		if err != nil {
			t.Fatal(err)
		}
		replay, err := f.m.Apply(context.Background(), f.tenant, f.principal, cmd)
		if err != nil || !replay.Replayed || replay.CommandID != first.CommandID || replay.ResultID != first.ResultID {
			t.Fatalf("same actor exact replay = %#v, %v; original=%#v", replay, err, first)
		}
	})
}

func TestWorkRequestHashBindsBodyAndAllowsExactReplay(t *testing.T) {
	t.Parallel()

	t.Run("changed body conflicts", func(t *testing.T) {
		f := newWorkFixture(t, ":memory:", nil)
		defer f.st.Close()
		cmd := baseCreateCommand(f, "request hash body")
		if _, err := f.m.Apply(context.Background(), f.tenant, f.principal, cmd); err != nil {
			t.Fatal(err)
		}
		changed := cmd
		changed.Title = "same key with a different title"
		if _, err := f.m.Apply(context.Background(), f.tenant, f.principal, changed); err == nil {
			t.Fatal("same key with a changed body replayed")
		} else if we := asWorkError(err); we == nil || we.code != "idempotency_key_reused" {
			t.Fatalf("changed body = %v, want idempotency_key_reused", err)
		}
	})

	t.Run("exact body replays", func(t *testing.T) {
		f := newWorkFixture(t, ":memory:", nil)
		defer f.st.Close()
		cmd := baseCreateCommand(f, "request hash exact replay")
		first, err := f.m.Apply(context.Background(), f.tenant, f.principal, cmd)
		if err != nil {
			t.Fatal(err)
		}
		replay, err := f.m.Apply(context.Background(), f.tenant, f.principal, cmd)
		if err != nil || !replay.Replayed || replay.CommandID != first.CommandID {
			t.Fatalf("exact body replay = %#v, %v; first=%#v", replay, err, first)
		}
	})
}

func TestWorkRequestHashBindsExpectedVersionAndAllowsExactReplay(t *testing.T) {
	t.Parallel()

	command := func(item CommandResult) WorkCommand {
		return WorkCommand{
			Command: "item.update", WorkItemID: item.ResultID, Title: "etag-bound update",
			ExpectedVersion: item.Version, IdempotencyKey: model.NewID().String(), HTTPMethod: http.MethodPatch,
		}
	}
	t.Run("changed expected version conflicts", func(t *testing.T) {
		f := newWorkFixture(t, ":memory:", nil)
		defer f.st.Close()
		cmd := command(applyCreate(t, f, "etag request hash"))
		if _, err := f.m.Apply(context.Background(), f.tenant, f.principal, cmd); err != nil {
			t.Fatal(err)
		}
		changed := cmd
		changed.ExpectedVersion++
		if _, err := f.m.Apply(context.Background(), f.tenant, f.principal, changed); err == nil {
			t.Fatal("same key with a changed expected version replayed")
		} else if we := asWorkError(err); we == nil || we.code != "idempotency_key_reused" {
			t.Fatalf("changed expected version = %v, want idempotency_key_reused", err)
		}
	})

	t.Run("exact expected version replays", func(t *testing.T) {
		f := newWorkFixture(t, ":memory:", nil)
		defer f.st.Close()
		cmd := command(applyCreate(t, f, "etag exact replay"))
		first, err := f.m.Apply(context.Background(), f.tenant, f.principal, cmd)
		if err != nil {
			t.Fatal(err)
		}
		replay, err := f.m.Apply(context.Background(), f.tenant, f.principal, cmd)
		if err != nil || !replay.Replayed || replay.CommandID != first.CommandID {
			t.Fatalf("exact expected version replay = %#v, %v; first=%#v", replay, err, first)
		}
	})
}

func TestWorkGovernedReadDetectsBriefTamperAndReturnsIntactNeighbor(t *testing.T) {
	t.Parallel()

	f := newWorkFixture(t, ":memory:", nil)
	defer f.st.Close()
	intact := applyCreate(t, f, "intact brief")
	tampered := applyCreate(t, f, "tampered brief")

	t.Run("intact brief is returned", func(t *testing.T) {
		if snapshot, err := f.m.Get(context.Background(), f.tenant, f.principal, intact.ResultID); err != nil ||
			snapshot.Item.BriefHash != hexHash(hashBytes([]byte(snapshot.Item.BriefMD))) {
			t.Fatalf("intact governed read = %#v, %v", snapshot, err)
		}
	})
	if err := f.st.Mutate(context.Background(), f.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(workItemKind)
		if err != nil {
			return err
		}
		row, err := repo.Get(context.Background(), tampered.ResultID)
		if err != nil {
			return err
		}
		row[colWorkBrief] = "content changed without replacing the committed brief hash"
		_, err = repo.Update(context.Background(), row)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	t.Run("tampered brief is indeterminate", func(t *testing.T) {
		if _, err := f.m.Get(context.Background(), f.tenant, f.principal, tampered.ResultID); err == nil {
			t.Fatal("tampered brief was returned as governed evidence")
		} else if we := asWorkError(err); we == nil || we.verdict != VerdictUnknown || we.code != "evidence_unavailable" {
			t.Fatalf("tampered brief read = %v, want evidence_unavailable", err)
		}
	})
}

func TestWorkReceiptReplayRejectsCorruptResponseEvidence(t *testing.T) {
	t.Parallel()

	f := newWorkFixture(t, ":memory:", nil)
	defer f.st.Close()
	anchor := applyCreate(t, f, "receipt anchor")
	cmd := baseCreateCommand(f, "receipt corruption probe")
	plan, err := f.m.Plan(context.Background(), f.tenant, f.principal, cmd)
	if err != nil {
		t.Fatal(err)
	}
	actorFP, requestHash, idemHash, scope, err := commandHashes(f.principal, cmd)
	if err != nil {
		t.Fatal(err)
	}
	commandID := model.NewID()
	fake := CommandResult{
		Verdict: VerdictClean, Code: "applied", CommandID: commandID,
		ResultKind: string(workItemKind), ResultID: anchor.ResultID, Version: anchor.Version,
		Status: "draft", EventID: anchor.EventID, PlanHash: plan.PlanHash, AuditSeq: anchor.AuditSeq,
	}
	response, err := canonicalJSON(fake)
	if err != nil {
		t.Fatal(err)
	}
	planHash, err := decodeHash(plan.PlanHash, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.st.Mutate(context.Background(), f.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(workCommandKind)
		if err != nil {
			return err
		}
		_, err = repo.Create(context.Background(), model.Record{
			colWorkWorkspaceID: f.workspace.String(), colCommandID: commandID.String(),
			colCommandActorFP: actorFP, colCommandScope: scope, colCommandIdempotency: idemHash,
			colCommandRequestHash: requestHash, colCommandPlanHash: planHash,
			colCommandResultKind: string(workItemKind), colCommandResultID: anchor.ResultID.String(),
			colCommandHTTPStatus: int64(http.StatusOK), colCommandResponse: string(response),
			colCommandResponseHash: make([]byte, 32), colCommandAuditSeq: anchor.AuditSeq,
			colCommandAuditHash: make([]byte, 32), colCommandCompletedAt: model.SystemClock{}.Now().String(),
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.m.Apply(context.Background(), f.tenant, f.principal, cmd); err == nil {
		t.Fatal("corrupt receipt was replayed as clean")
	} else if we := asWorkError(err); we == nil || we.verdict != VerdictUnknown || we.code != "evidence_unavailable" {
		t.Fatalf("corrupt receipt replay = %v", err)
	}
	if got := workCount(t, f, workItemKind); got != 1 {
		t.Fatalf("corrupt replay created %d work items, want anchor only", got)
	}

	legitimate := baseCreateCommand(f, "intact receipt neighbor")
	first, err := f.m.Apply(context.Background(), f.tenant, f.principal, legitimate)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := f.m.Apply(context.Background(), f.tenant, f.principal, legitimate)
	if err != nil || !replay.Replayed || replay.CommandID != first.CommandID {
		t.Fatalf("intact receipt neighbor = %#v, %v", replay, err)
	}
}

func TestWorkOwnerAuthorityComesFromPrincipalNotCommandBody(t *testing.T) {
	t.Parallel()

	t.Run("body cannot forge owner authority", func(t *testing.T) {
		f := newWorkFixture(t, ":memory:", nil)
		defer f.st.Close()
		created := applyCreate(t, f, "forged owner authority")
		active := forceWorkActive(t, f, created)
		attacker := WorkPrincipal{
			ActorKind: model.ActorUser, ActorRef: model.NewID().String(), Admin: false,
		}
		attacker.Actor = "user:" + attacker.ActorRef
		forged := WorkCommand{
			Command: "item.fail", WorkItemID: created.ResultID, Code: "test_failed",
			Reason:    "The attacker supplied the current owner in the command body.",
			OwnerKind: f.principal.ActorKind, OwnerRef: f.principal.ActorRef,
			ExpectedVersion: active.Int(model.ColVersion), IdempotencyKey: model.NewID().String(),
			HTTPMethod: http.MethodPost,
		}
		_, forged = withWorkExecutionLease(t, f, f.principal, forged)
		if _, err := f.m.Apply(context.Background(), f.tenant, attacker, forged); err == nil {
			t.Fatal("caller operated as the owner supplied in the command body")
		} else if we := asWorkError(err); we == nil || we.code != "forbidden" {
			t.Fatalf("forged owner command = %v, want forbidden", err)
		}
	})

	t.Run("actual owner may act", func(t *testing.T) {
		f := newWorkFixture(t, ":memory:", nil)
		defer f.st.Close()
		created := applyCreate(t, f, "actual owner authority")
		active := forceWorkActive(t, f, created)
		owner := f.principal
		owner.Admin = false
		owner, fail := withWorkExecutionLease(t, f, owner, WorkCommand{
			Command: "item.fail", WorkItemID: created.ResultID, Code: "test_failed",
			Reason:          "The canonical owner reports a failed implementation.",
			ExpectedVersion: active.Int(model.ColVersion), IdempotencyKey: model.NewID().String(),
			HTTPMethod: http.MethodPost,
		})
		result, err := f.m.Apply(context.Background(), f.tenant, owner, fail)
		if err != nil || result.Status != "failed" {
			t.Fatalf("actual owner neighbor = %#v, %v", result, err)
		}
	})
}

func workFixtureForBackend(t *testing.T, m *Module, tenant model.TenantID) workFixture {
	t.Helper()
	m.UseWorkIdentityResolver(allowWorkIdentity{})
	m.UseWorkContentGuard(allowWorkContent{})
	var workspace model.ID
	if err := m.data.View(context.Background(), tenant, func(sc store.Scope) error {
		ws, err := sc.DefaultWorkspace(context.Background())
		if err == nil {
			workspace = ws.ID
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	actorRef := model.NewID().String()
	return workFixture{
		m: m, tenant: tenant, workspace: workspace,
		principal: WorkPrincipal{
			ActorKind: model.ActorUser, ActorRef: actorRef, Actor: "user:" + actorRef, Admin: true,
		},
	}
}

func TestWorkDependencyGuardSerializesOpposingEdges(t *testing.T) {
	t.Parallel()

	for _, be := range backends(t) {
		be := be
		t.Run(be.name, func(t *testing.T) {
			m, tenant, _ := be.open(t)
			f := workFixtureForBackend(t, m, tenant)
			a, b := applyCreate(t, f, "race-a"), applyCreate(t, f, "race-b")
			c, d := applyCreate(t, f, "race-c"), applyCreate(t, f, "race-d")
			e, g := applyCreate(t, f, "race-e"), applyCreate(t, f, "race-g")

			runPair := func(left, right WorkCommand) [2]error {
				var errs [2]error
				start := make(chan struct{})
				var wg sync.WaitGroup
				wg.Add(2)
				for i, command := range []WorkCommand{left, right} {
					i, command := i, command
					go func() {
						defer wg.Done()
						<-start
						_, errs[i] = m.Apply(context.Background(), tenant, f.principal, command)
					}()
				}
				close(start)
				wg.Wait()
				return errs
			}
			command := func(from CommandResult, to model.ID) WorkCommand {
				return WorkCommand{
					Command: "dependency.add", WorkItemID: from.ResultID, DependsOnID: to,
					ExpectedVersion: from.Version, IdempotencyKey: model.NewID().String(), HTTPMethod: http.MethodPost,
				}
			}
			t.Run("opposing edges", func(t *testing.T) {
				opposed := runPair(command(a, b.ResultID), command(b, a.ResultID))
				wins, cycles := 0, 0
				for _, err := range opposed {
					if err == nil {
						wins++
					} else if we := asWorkError(err); we != nil && we.code == "dependency_cycle" {
						cycles++
					}
				}
				if wins != 1 || cycles != 1 {
					t.Fatalf("opposing edges errors=%v, want one winner and one dependency_cycle", opposed)
				}
				assertWorkDependencyGuardCommitted(t, m, tenant, f.workspace)
			})

			t.Run("independent edges", func(t *testing.T) {
				independent := runPair(command(c, d.ResultID), command(e, g.ResultID))
				if independent[0] != nil || independent[1] != nil {
					t.Fatalf("independent edges = %v, want two winners", independent)
				}
			})
		})
	}
}

func assertWorkDependencyGuardCommitted(
	t *testing.T,
	m *Module,
	tenant model.TenantID,
	workspace model.ID,
) {
	t.Helper()
	if err := m.data.View(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(workGuardKind)
		if err != nil {
			return err
		}
		rows, err := listAll(context.Background(), repo,
			model.Filter{Column: colWorkWorkspaceID, Op: model.OpEq, Value: workspace.String()},
			model.Filter{Column: colGuardKind, Op: model.OpEq, Value: "dependency_graph"},
		)
		if err != nil {
			return err
		}
		if len(rows) != 1 || rows[0].Int(colGuardEpoch) < 1 {
			return fmt.Errorf("dependency guard rows = %#v, want one committed epoch", rows)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func workCriterion(t *testing.T, f workFixture, itemID model.ID, key string) model.Record {
	t.Helper()
	var out model.Record
	if err := f.st.View(context.Background(), f.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(workAcceptanceKind)
		if err != nil {
			return err
		}
		rows, err := listAll(context.Background(), repo,
			model.Filter{Column: colWorkItemID, Op: model.OpEq, Value: itemID.String()},
			model.Filter{Column: colAccKey, Op: model.OpEq, Value: key},
		)
		if err != nil {
			return err
		}
		if len(rows) != 1 {
			t.Fatalf("criterion %s rows = %d", key, len(rows))
		}
		out = rows[0]
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

func forceWorkActive(t *testing.T, f workFixture, created CommandResult) model.Record {
	t.Helper()
	ctx := context.Background()
	var sid string
	ready, err := f.m.Apply(ctx, f.tenant, f.principal, WorkCommand{
		Command: "item.ready", WorkItemID: created.ResultID, ExpectedVersion: created.Version,
		IdempotencyKey: model.NewID().String(), HTTPMethod: http.MethodPost,
	})
	if err != nil {
		t.Fatalf("ready work: %v", err)
	}
	var active model.Record
	if err := f.st.Mutate(ctx, f.tenant, func(sc store.Scope) error {
		if err := lockWorkLeaseItem(ctx, sc, f.tenant, f.workspace, created.ResultID); err != nil {
			return err
		}
		now, err := transactionNow(ctx, sc)
		if err != nil {
			return err
		}
		leaseRepo, err := sc.Ext(workLeaseKind)
		if err != nil {
			return err
		}
		lease, found, err := findWorkLease(ctx, sc, created.ResultID)
		if err != nil {
			return err
		}
		if !found {
			return errors.New("test work lease is missing")
		}
		state, err := workLeaseFenceState(lease)
		if err != nil {
			return err
		}
		sid = sidPrefix + model.NewID().String()
		state, err = fenceAcquire(
			state,
			workLeaseHolderKey(sid, "", ""),
			now.Time(),
			defaultWorkLeaseTTL,
			workLeaseTTLPolicy,
		)
		if err != nil {
			return err
		}
		applyWorkLeaseFenceState(lease, state, sid, "", "")
		if _, err := leaseRepo.Update(ctx, lease); err != nil {
			return err
		}
		repo, err := sc.Ext(workItemKind)
		if err != nil {
			return err
		}
		item, err := repo.Get(ctx, created.ResultID)
		if err != nil {
			return err
		}
		if item.Int(model.ColVersion) != ready.Version {
			t.Fatalf("ready version = %d, row = %d", ready.Version, item.Int(model.ColVersion))
		}
		item[colWorkStatus], item[colWorkStartedAt] = "active", now.String()
		active, err = repo.Update(ctx, item)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.m.Claim(ctx, f.tenant, sid, f.principal.Actor, 0); err != nil {
		t.Fatalf("claim forced active WorkLease holder: %v", err)
	}
	return active
}

func forceWorkReview(t *testing.T, f workFixture, itemID model.ID) model.Record {
	t.Helper()
	ctx := context.Background()
	var review model.Record
	if err := f.st.Mutate(ctx, f.tenant, func(sc store.Scope) error {
		if err := lockWorkLeaseItem(ctx, sc, f.tenant, f.workspace, itemID); err != nil {
			return err
		}
		now, err := transactionNow(ctx, sc)
		if err != nil {
			return err
		}
		lease, found, err := findWorkLease(ctx, sc, itemID)
		if err != nil {
			return err
		}
		if !found {
			return errors.New("test work lease is missing")
		}
		state, err := workLeaseFenceState(lease)
		if err != nil {
			return err
		}
		if state.Lifecycle == fenceActive {
			state, err = fenceRelease(
				state,
				fenceToken{Holder: state.Holder, Fence: state.Fence},
				now.Time(),
				"test_review_transition",
				fenceEndPolicy{Lifecycle: fenceReleased, Bump: true},
			)
			if err != nil {
				return err
			}
			applyWorkLeaseFenceState(
				lease,
				state,
				lease.String(colLeaseHolderSID),
				lease.String(colLeaseHolderRunRef),
				lease.String(colLeaseHolderAgentRef),
			)
			leaseRepo, err := sc.Ext(workLeaseKind)
			if err != nil {
				return err
			}
			if _, err := leaseRepo.Update(ctx, lease); err != nil {
				return err
			}
		}
		repo, err := sc.Ext(workItemKind)
		if err != nil {
			return err
		}
		item, err := repo.Get(ctx, itemID)
		if err != nil {
			return err
		}
		item[colWorkStatus], item[colWorkReviewAt] = "review", now.String()
		review, err = repo.Update(ctx, item)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return review
}

func withWorkExecutionLease(
	t *testing.T,
	f workFixture,
	principal WorkPrincipal,
	cmd WorkCommand,
) (WorkPrincipal, WorkCommand) {
	t.Helper()
	ctx := context.Background()
	var lease model.Record
	if err := f.st.View(ctx, f.tenant, func(sc store.Scope) error {
		var found bool
		var err error
		lease, found, err = findWorkLease(ctx, sc, cmd.WorkItemID)
		if err != nil {
			return err
		}
		if !found {
			return errors.New("test work lease is missing")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if lease.String(colLeaseState) != workLeaseActive ||
		!validCanonicalSID(lease.String(colLeaseHolderSID)) || lease.Int(colLeaseFence) < 1 {
		t.Fatalf("test work lease is not active authority: %#v", lease)
	}
	principal.SessionID = lease.String(colLeaseHolderSID)
	cmd.HolderSID = lease.String(colLeaseHolderSID)
	cmd.HolderRunRef = lease.String(colLeaseHolderRunRef)
	cmd.HolderAgentRef = lease.String(colLeaseHolderAgentRef)
	cmd.Fence = lease.Int(colLeaseFence)
	return principal, cmd
}

func completeRequiredWork(t *testing.T, f workFixture, created CommandResult) CommandResult {
	t.Helper()
	active := forceWorkActive(t, f, created)
	criterion := workCriterion(t, f, created.ResultID, "tests")
	principal, evaluate := withWorkExecutionLease(t, f, f.principal, WorkCommand{
		Command: "acceptance.evaluate", WorkItemID: created.ResultID,
		CriterionID: recordID(criterion), Acceptance: []AcceptanceInput{{
			State: "passed", EvidenceRef: "job:required-green",
			EvidenceHash: hexHash(hashBytes([]byte("green"))),
		}},
		ExpectedVersion: active.Int(model.ColVersion),
		IdempotencyKey:  model.NewID().String(), HTTPMethod: http.MethodPatch,
	})
	passed, err := f.m.Apply(context.Background(), f.tenant, principal, evaluate)
	if err != nil {
		t.Fatalf("pass required criterion: %v", err)
	}
	review := forceWorkReview(t, f, created.ResultID)
	if review.Int(model.ColVersion) != passed.Version+1 {
		t.Fatalf("review version = %d, want %d", review.Int(model.ColVersion), passed.Version+1)
	}
	completed, err := f.m.Apply(context.Background(), f.tenant, f.principal, WorkCommand{
		Command: "item.complete", WorkItemID: created.ResultID,
		ExpectedVersion: review.Int(model.ColVersion),
		IdempotencyKey:  model.NewID().String(), HTTPMethod: http.MethodPost,
	})
	if err != nil || completed.Status != "completed" {
		t.Fatalf("complete required work = %#v, %v", completed, err)
	}
	return completed
}

func TestWorkTerminalCannotReopenButSupersedingItemCanStart(t *testing.T) {
	t.Parallel()

	t.Run("terminal cannot reopen", func(t *testing.T) {
		f := newWorkFixture(t, ":memory:", nil)
		defer f.st.Close()
		completed := completeRequiredWork(t, f, applyCreate(t, f, "terminal item"))
		_, err := f.m.Apply(context.Background(), f.tenant, f.principal, WorkCommand{
			Command: "item.cancel", WorkItemID: completed.ResultID,
			Code: "reopen", Reason: "A terminal item must not be rewritten in place.",
			ExpectedVersion: completed.Version,
			IdempotencyKey:  model.NewID().String(), HTTPMethod: http.MethodPost,
		})
		if we := asWorkError(err); we == nil || we.code != "illegal_transition" {
			t.Fatalf("terminal reopen = %v, want illegal_transition", err)
		}
	})

	t.Run("superseding item can start", func(t *testing.T) {
		f := newWorkFixture(t, ":memory:", nil)
		defer f.st.Close()
		completed := completeRequiredWork(t, f, applyCreate(t, f, "superseded terminal item"))
		successor := baseCreateCommand(f, "explicit successor")
		successor.SupersedesID = completed.ResultID
		result, err := f.m.Apply(context.Background(), f.tenant, f.principal, successor)
		if err != nil {
			t.Fatalf("create superseding work item: %v", err)
		}
		snapshot, err := f.m.Get(context.Background(), f.tenant, f.principal, result.ResultID)
		if err != nil || snapshot.Item.SupersedesID != completed.ResultID || snapshot.Item.Status != "draft" {
			t.Fatalf("superseding item = %#v, %v", snapshot.Item, err)
		}
	})
}

func TestWorkReadyRequiresCompletedPredecessorNotMerelyTerminal(t *testing.T) {
	t.Parallel()

	t.Run("failed predecessor still blocks", func(t *testing.T) {
		f := newWorkFixture(t, ":memory:", nil)
		defer f.st.Close()
		failed := applyCreate(t, f, "failed predecessor")
		failedActive := forceWorkActive(t, f, failed)
		principal, fail := withWorkExecutionLease(t, f, f.principal, WorkCommand{
			Command: "item.fail", WorkItemID: failed.ResultID,
			Code: "test_failed", Reason: "The predecessor failed its governed work.",
			ExpectedVersion: failedActive.Int(model.ColVersion),
			IdempotencyKey:  model.NewID().String(), HTTPMethod: http.MethodPost,
		})
		failedResult, err := f.m.Apply(context.Background(), f.tenant, principal, fail)
		if err != nil || failedResult.Status != "failed" {
			t.Fatalf("fail predecessor = %#v, %v", failedResult, err)
		}
		blocked := applyCreate(t, f, "blocked successor")
		blockedDep, err := f.m.Apply(context.Background(), f.tenant, f.principal, WorkCommand{
			Command: "dependency.add", WorkItemID: blocked.ResultID, DependsOnID: failed.ResultID,
			ExpectedVersion: blocked.Version,
			IdempotencyKey:  model.NewID().String(), HTTPMethod: http.MethodPost,
		})
		if err != nil {
			t.Fatalf("add failed predecessor: %v", err)
		}
		_, err = f.m.Apply(context.Background(), f.tenant, f.principal, WorkCommand{
			Command: "item.ready", WorkItemID: blocked.ResultID,
			ExpectedVersion: blockedDep.Version,
			IdempotencyKey:  model.NewID().String(), HTTPMethod: http.MethodPost,
		})
		if we := asWorkError(err); we == nil || we.code != "dependency_incomplete" {
			t.Fatalf("ready behind failed predecessor = %v, want dependency_incomplete", err)
		}
	})

	t.Run("completed predecessor permits ready", func(t *testing.T) {
		f := newWorkFixture(t, ":memory:", nil)
		defer f.st.Close()
		completed := completeRequiredWork(t, f, applyCreate(t, f, "completed predecessor"))
		readyCandidate := applyCreate(t, f, "ready successor")
		completedDep, err := f.m.Apply(context.Background(), f.tenant, f.principal, WorkCommand{
			Command: "dependency.add", WorkItemID: readyCandidate.ResultID, DependsOnID: completed.ResultID,
			ExpectedVersion: readyCandidate.Version,
			IdempotencyKey:  model.NewID().String(), HTTPMethod: http.MethodPost,
		})
		if err != nil {
			t.Fatalf("add completed predecessor: %v", err)
		}
		ready, err := f.m.Apply(context.Background(), f.tenant, f.principal, WorkCommand{
			Command: "item.ready", WorkItemID: readyCandidate.ResultID,
			ExpectedVersion: completedDep.Version,
			IdempotencyKey:  model.NewID().String(), HTTPMethod: http.MethodPost,
		})
		if err != nil || ready.Status != "ready" {
			t.Fatalf("ready behind completed predecessor = %#v, %v", ready, err)
		}
	})
}

func TestWorkAcceptanceDefinitionFreezesAtExecution(t *testing.T) {
	t.Parallel()

	t.Run("active is rejected", func(t *testing.T) {
		f := newWorkFixture(t, ":memory:", nil)
		defer f.st.Close()
		created := applyCreate(t, f, "active acceptance")
		active := forceWorkActive(t, f, created)
		criterion := workCriterion(t, f, created.ResultID, "tests")
		_, err := f.m.Apply(context.Background(), f.tenant, f.principal, WorkCommand{
			Command: "acceptance.update", WorkItemID: created.ResultID,
			CriterionID: recordID(criterion), Acceptance: []AcceptanceInput{{
				Ordinal: 2, Statement: "A changed definition must not enter active work.", Required: true,
			}},
			ExpectedVersion: active.Int(model.ColVersion),
			IdempotencyKey:  model.NewID().String(), HTTPMethod: http.MethodPatch,
		})
		if we := asWorkError(err); we == nil || we.code != "illegal_transition" {
			t.Fatalf("active definition edit = %v, want illegal_transition", err)
		}
		if got := workCriterion(t, f, created.ResultID, "tests"); got.String(colAccStatement) != criterion.String(colAccStatement) {
			t.Fatalf("rejected edit changed criterion: %#v", got)
		}
	})

	t.Run("draft is editable", func(t *testing.T) {
		f := newWorkFixture(t, ":memory:", nil)
		defer f.st.Close()
		created := applyCreate(t, f, "draft acceptance")
		criterion := workCriterion(t, f, created.ResultID, "tests")
		result, err := f.m.Apply(context.Background(), f.tenant, f.principal, WorkCommand{
			Command: "acceptance.update", WorkItemID: created.ResultID,
			CriterionID: recordID(criterion), Acceptance: []AcceptanceInput{{
				Ordinal: 2, Statement: "The draft definition remains editable.", Required: true,
			}},
			ExpectedVersion: created.Version,
			IdempotencyKey:  model.NewID().String(), HTTPMethod: http.MethodPatch,
		})
		if err != nil || result.Version != created.Version+1 {
			t.Fatalf("draft definition edit = %#v, %v", result, err)
		}
		if got := workCriterion(t, f, created.ResultID, "tests"); got.String(colAccStatement) != "The draft definition remains editable." {
			t.Fatalf("draft edit not persisted: %#v", got)
		}
	})
}

func TestWorkRequiredAcceptanceBlocksButOptionalPendingDoesNot(t *testing.T) {
	t.Parallel()

	f := newWorkFixture(t, ":memory:", nil)
	defer f.st.Close()
	cmd := baseCreateCommand(f, "acceptance directions")
	cmd.Acceptance = append(cmd.Acceptance, AcceptanceInput{
		Key: "optional-note", Ordinal: 1, Statement: "An optional observation may remain pending.", Required: false,
	})
	created, err := f.m.Apply(context.Background(), f.tenant, f.principal, cmd)
	if err != nil {
		t.Fatal(err)
	}
	active := forceWorkActive(t, f, created)
	required := workCriterion(t, f, created.ResultID, "tests")
	principal, evaluate := withWorkExecutionLease(t, f, f.principal, WorkCommand{
		Command: "acceptance.evaluate", WorkItemID: created.ResultID,
		CriterionID: recordID(required), Acceptance: []AcceptanceInput{{
			State: "passed", EvidenceRef: "job:required-green", EvidenceHash: hexHash(hashBytes([]byte("green"))),
		}},
		ExpectedVersion: active.Int(model.ColVersion), IdempotencyKey: model.NewID().String(), HTTPMethod: http.MethodPatch,
	})
	passed, err := f.m.Apply(context.Background(), f.tenant, principal, evaluate)
	if err != nil {
		t.Fatal(err)
	}
	review := forceWorkReview(t, f, created.ResultID)
	if review.Int(model.ColVersion) != passed.Version+1 {
		t.Fatalf("review version = %d, want %d", review.Int(model.ColVersion), passed.Version+1)
	}
	completed, err := f.m.Apply(context.Background(), f.tenant, f.principal, WorkCommand{
		Command: "item.complete", WorkItemID: created.ResultID, ExpectedVersion: review.Int(model.ColVersion),
		IdempotencyKey: model.NewID().String(), HTTPMethod: http.MethodPost,
	})
	if err != nil || completed.Status != "completed" {
		t.Fatalf("optional pending blocked completion: %#v, %v", completed, err)
	}
	if optional := workCriterion(t, f, created.ResultID, "optional-note"); optional.String(colAccState) != "pending" {
		t.Fatalf("optional criterion unexpectedly changed: %#v", optional)
	}

	pending := applyCreate(t, f, "required pending")
	forceWorkActive(t, f, pending)
	pendingReview := forceWorkReview(t, f, pending.ResultID)
	_, err = f.m.Apply(context.Background(), f.tenant, f.principal, WorkCommand{
		Command: "item.complete", WorkItemID: pending.ResultID,
		ExpectedVersion: pendingReview.Int(model.ColVersion), IdempotencyKey: model.NewID().String(), HTTPMethod: http.MethodPost,
	})
	if we := asWorkError(err); we == nil || we.code != "acceptance_incomplete" {
		t.Fatalf("pending required completion = %v", err)
	}
}

func decisionCommand(itemID model.ID, command, key string) WorkCommand {
	return WorkCommand{
		Command: command, WorkItemID: itemID, DecisionKey: key,
		SubjectKind: "work.scope", SubjectRef: itemID.String(),
		StatementMD:  "Use the bounded K1 implementation.",
		RationaleMD:  "The mutation witness and contract support this decision.",
		AuthorityRef: "approval:test-k1", IdempotencyKey: model.NewID().String(), HTTPMethod: http.MethodPost,
	}
}

func workDecisions(t *testing.T, f workFixture, itemID model.ID, key string) ([]model.Record, model.Record) {
	t.Helper()
	var decisions []model.Record
	var head model.Record
	if err := f.m.data.View(context.Background(), f.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(workDecisionKind)
		if err != nil {
			return err
		}
		decisions, err = listAll(context.Background(), repo,
			model.Filter{Column: colWorkItemID, Op: model.OpEq, Value: itemID.String()},
			model.Filter{Column: colDecisionKey, Op: model.OpEq, Value: key},
		)
		if err != nil {
			return err
		}
		heads, err := sc.Ext(workDecisionHeadKind)
		if err != nil {
			return err
		}
		rows, err := listAll(context.Background(), heads,
			model.Filter{Column: colWorkItemID, Op: model.OpEq, Value: itemID.String()},
			model.Filter{Column: colDecisionKey, Op: model.OpEq, Value: key},
		)
		if err != nil {
			return err
		}
		if len(rows) == 1 {
			head = rows[0]
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return decisions, head
}

func TestWorkDecisionHistoryHeadAndStaleRevoke(t *testing.T) {
	t.Parallel()

	f := newWorkFixture(t, ":memory:", nil)
	defer f.st.Close()
	created := applyCreate(t, f, "decision history")

	set := decisionCommand(created.ResultID, "decision.set", "scope")
	set.ExpectedVersion = created.Version
	first, err := f.m.Apply(context.Background(), f.tenant, f.principal, set)
	if err != nil {
		t.Fatal(err)
	}
	supersede := decisionCommand(created.ResultID, "decision.supersede", "scope")
	supersede.StatementMD = "Use the hardened bounded K1 implementation."
	supersede.ExpectedVersion = first.Version
	second, err := f.m.Apply(context.Background(), f.tenant, f.principal, supersede)
	if err != nil {
		t.Fatal(err)
	}
	rows, head := workDecisions(t, f, created.ResultID, "scope")
	if len(rows) != 2 || rows[0].Int(colDecisionSeq) != 1 || rows[1].Int(colDecisionSeq) != 2 ||
		rows[0].String(colDecisionStatement) != set.StatementMD || head.String(colDecisionCurrentID) != second.ResultID.String() {
		t.Fatalf("decision history/head = rows %#v head %#v", rows, head)
	}

	stale := decisionCommand(created.ResultID, "decision.revoke", "scope")
	stale.DecisionID, stale.ExpectedVersion = first.ResultID, second.Version
	staleAssessment, err := f.m.Validate(context.Background(), f.tenant, f.principal, stale)
	if err != nil || staleAssessment.Verdict != VerdictBroken || staleAssessment.Code != "stale_decision" {
		t.Fatalf("stale revoke validation = %#v, %v", staleAssessment, err)
	}
	if _, err := f.m.Apply(context.Background(), f.tenant, f.principal, stale); asWorkError(err) == nil || asWorkError(err).code != "stale_decision" {
		t.Fatalf("stale revoke = %v", err)
	}
	valid := decisionCommand(created.ResultID, "decision.revoke", "scope")
	valid.DecisionID, valid.ExpectedVersion = second.ResultID, second.Version
	validAssessment, err := f.m.Validate(context.Background(), f.tenant, f.principal, valid)
	if err != nil || validAssessment.Verdict != VerdictClean {
		t.Fatalf("current-head revoke validation = %#v, %v", validAssessment, err)
	}
	revoked, err := f.m.Apply(context.Background(), f.tenant, f.principal, valid)
	if err != nil {
		t.Fatal(err)
	}
	rows, head = workDecisions(t, f, created.ResultID, "scope")
	if len(rows) != 3 || rows[2].Int(colDecisionSeq) != 3 || rows[2].String(colDecisionOperation) != "revoke" ||
		head.String(colDecisionHeadState) != "revoked" || head.String(colDecisionCurrentID) != revoked.ResultID.String() {
		t.Fatalf("revoked history/head = rows %#v head %#v", rows, head)
	}

	missingAuthority := decisionCommand(created.ResultID, "decision.set", "another-key")
	missingAuthority.AuthorityRef = ""
	assessment, err := f.m.Validate(context.Background(), f.tenant, f.principal, missingAuthority)
	if err != nil || assessment.Verdict != VerdictBroken || assessment.Code != "invalid_command" {
		t.Fatalf("missing decision authority = %#v, %v", assessment, err)
	}
	if after, _ := workDecisions(t, f, created.ResultID, "scope"); len(after) != 3 {
		t.Fatalf("invalid decision changed history: %#v", after)
	}
}

func TestWorkDecisionHistoryRowsRejectMutationAcrossBackends(t *testing.T) {
	t.Parallel()

	for _, be := range backends(t) {
		be := be
		t.Run(be.name, func(t *testing.T) {
			m, tenant, _ := be.open(t)
			f := workFixtureForBackend(t, m, tenant)
			created := applyCreate(t, f, "append-only decision history")
			set := decisionCommand(created.ResultID, "decision.set", "retention")
			set.ExpectedVersion = created.Version
			first, err := m.Apply(context.Background(), tenant, f.principal, set)
			if err != nil {
				t.Fatal(err)
			}

			updateErr := m.data.Mutate(context.Background(), tenant, func(sc store.Scope) error {
				repo, err := sc.Ext(workDecisionKind)
				if err != nil {
					return err
				}
				row, err := repo.Get(context.Background(), first.ResultID)
				if err != nil {
					return err
				}
				_, err = repo.Update(context.Background(), row)
				return err
			})
			if updateErr == nil {
				t.Fatal("append-only decision accepted update")
			}
			deleteErr := m.data.Mutate(context.Background(), tenant, func(sc store.Scope) error {
				repo, err := sc.Ext(workDecisionKind)
				if err != nil {
					return err
				}
				return repo.Delete(context.Background(), first.ResultID)
			})
			if deleteErr == nil {
				t.Fatal("append-only decision accepted delete")
			}

			supersede := decisionCommand(created.ResultID, "decision.supersede", "retention")
			supersede.StatementMD = "A new append-only row carries the revised decision."
			supersede.ExpectedVersion = first.Version
			second, err := m.Apply(context.Background(), tenant, f.principal, supersede)
			if err != nil {
				t.Fatalf("supersede neighbor: %v", err)
			}
			rows, head := workDecisions(t, f, created.ResultID, "retention")
			if len(rows) != 2 || rows[0].String(colDecisionStatement) != set.StatementMD ||
				rows[1].String(colDecisionStatement) != supersede.StatementMD ||
				head.String(colDecisionCurrentID) != second.ResultID.String() {
				t.Fatalf("append-only history after supersede = rows %#v head %#v", rows, head)
			}
		})
	}
}

func TestWorkWaiverRequiresEffectiveDecisionAndRevocationResetsIt(t *testing.T) {
	t.Parallel()

	f := newWorkFixture(t, ":memory:", nil)
	defer f.st.Close()
	created := applyCreate(t, f, "waiver lifecycle")
	active := forceWorkActive(t, f, created)
	criterion := workCriterion(t, f, created.ResultID, "tests")

	orphan := WorkCommand{
		Command: "acceptance.evaluate", WorkItemID: created.ResultID, CriterionID: recordID(criterion),
		Acceptance:      []AcceptanceInput{{State: "waived", WaiverDecisionID: model.NewID()}},
		ExpectedVersion: active.Int(model.ColVersion), IdempotencyKey: model.NewID().String(), HTTPMethod: http.MethodPatch,
	}
	executionPrincipal, orphan := withWorkExecutionLease(t, f, f.principal, orphan)
	if _, err := f.m.Apply(context.Background(), f.tenant, executionPrincipal, orphan); asWorkError(err) == nil || asWorkError(err).code != "acceptance_incomplete" {
		t.Fatalf("orphan waiver = %v", err)
	}

	set := decisionCommand(created.ResultID, "decision.set", "waive-tests")
	set.ExpectedVersion = active.Int(model.ColVersion)
	decision, err := f.m.Apply(context.Background(), f.tenant, f.principal, set)
	if err != nil {
		t.Fatal(err)
	}
	waive := orphan
	waive.Acceptance[0].WaiverDecisionID = decision.ResultID
	waive.ExpectedVersion = decision.Version
	waive.IdempotencyKey = model.NewID().String()
	waived, err := f.m.Apply(context.Background(), f.tenant, executionPrincipal, waive)
	if err != nil {
		t.Fatal(err)
	}
	if got := workCriterion(t, f, created.ResultID, "tests"); got.String(colAccState) != "waived" {
		t.Fatalf("effective waiver not stored: %#v", got)
	}

	revoke := decisionCommand(created.ResultID, "decision.revoke", "waive-tests")
	revoke.DecisionID, revoke.ExpectedVersion = decision.ResultID, waived.Version
	if _, err := f.m.Apply(context.Background(), f.tenant, f.principal, revoke); err != nil {
		t.Fatal(err)
	}
	if got := workCriterion(t, f, created.ResultID, "tests"); got.String(colAccState) != "pending" || !got.IsNull(colAccWaiverDecisionID) {
		t.Fatalf("revoked waiver was not reset: %#v", got)
	}

	positive := applyCreate(t, f, "effective waiver completes")
	positiveActive := forceWorkActive(t, f, positive)
	positiveDecision := decisionCommand(positive.ResultID, "decision.set", "waive-tests")
	positiveDecision.ExpectedVersion = positiveActive.Int(model.ColVersion)
	approved, err := f.m.Apply(context.Background(), f.tenant, f.principal, positiveDecision)
	if err != nil {
		t.Fatal(err)
	}
	positiveCriterion := workCriterion(t, f, positive.ResultID, "tests")
	positivePrincipal, evaluate := withWorkExecutionLease(t, f, f.principal, WorkCommand{
		Command: "acceptance.evaluate", WorkItemID: positive.ResultID, CriterionID: recordID(positiveCriterion),
		Acceptance:      []AcceptanceInput{{State: "waived", WaiverDecisionID: approved.ResultID}},
		ExpectedVersion: approved.Version, IdempotencyKey: model.NewID().String(), HTTPMethod: http.MethodPatch,
	})
	waivedResult, err := f.m.Apply(context.Background(), f.tenant, positivePrincipal, evaluate)
	if err != nil {
		t.Fatal(err)
	}
	positiveReview := forceWorkReview(t, f, positive.ResultID)
	if positiveReview.Int(model.ColVersion) != waivedResult.Version+1 {
		t.Fatalf("positive review version = %d, want %d", positiveReview.Int(model.ColVersion), waivedResult.Version+1)
	}
	completed, err := f.m.Apply(context.Background(), f.tenant, f.principal, WorkCommand{
		Command: "item.complete", WorkItemID: positive.ResultID,
		ExpectedVersion: positiveReview.Int(model.ColVersion), IdempotencyKey: model.NewID().String(), HTTPMethod: http.MethodPost,
	})
	if err != nil || completed.Status != "completed" {
		t.Fatalf("effective waiver completion = %#v, %v", completed, err)
	}
}

func TestWorkEffectiveDecisionMayWaiveAcceptance(t *testing.T) {
	t.Parallel()

	f := newWorkFixture(t, ":memory:", nil)
	defer f.st.Close()
	created := applyCreate(t, f, "effective waiver neighbor")
	active := forceWorkActive(t, f, created)
	decision := decisionCommand(created.ResultID, "decision.set", "waive-tests")
	decision.ExpectedVersion = active.Int(model.ColVersion)
	approved, err := f.m.Apply(context.Background(), f.tenant, f.principal, decision)
	if err != nil {
		t.Fatal(err)
	}
	criterion := workCriterion(t, f, created.ResultID, "tests")
	principal, evaluate := withWorkExecutionLease(t, f, f.principal, WorkCommand{
		Command: "acceptance.evaluate", WorkItemID: created.ResultID,
		CriterionID: recordID(criterion), Acceptance: []AcceptanceInput{{
			State: "waived", WaiverDecisionID: approved.ResultID,
		}},
		ExpectedVersion: approved.Version,
		IdempotencyKey:  model.NewID().String(), HTTPMethod: http.MethodPatch,
	})
	waived, err := f.m.Apply(context.Background(), f.tenant, principal, evaluate)
	if err != nil || waived.Version != approved.Version+1 {
		t.Fatalf("effective waiver = %#v, %v", waived, err)
	}
	if got := workCriterion(t, f, created.ResultID, "tests"); got.String(colAccState) != "waived" || got.String(colAccWaiverDecisionID) != approved.ResultID.String() {
		t.Fatalf("effective waiver row = %#v", got)
	}
}
