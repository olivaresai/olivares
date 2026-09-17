// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const joinedApplyPostcommitProbeBound = 2 * time.Second

type joinedApplyPostcommitProbeSink struct {
	st       store.Store
	tenant   model.TenantID
	inner    WorkEventSink
	timeout  time.Duration
	entered  atomic.Int32
	nestedNS atomic.Int64
	nestedOK atomic.Bool
}

func (s *joinedApplyPostcommitProbeSink) IngestDurable(ctx context.Context, e WorkEventEnvelope) error {
	s.entered.Add(1)
	timeout := s.timeout
	if timeout == 0 {
		timeout = joinedApplyPostcommitProbeBound
	}
	nestedCtx, cancel := context.WithTimeout(context.Background(), timeout)
	start := time.Now()
	err := s.st.Mutate(nestedCtx, s.tenant, func(store.Scope) error { return nil })
	s.nestedNS.Store(time.Since(start).Nanoseconds())
	cancel()
	if err == nil {
		s.nestedOK.Store(true)
	}
	if s.inner != nil {
		return s.inner.IngestDurable(ctx, e)
	}
	return err
}

func joinedApplyPostcommitClaim(workspace model.ID, kind ProtocolReplayKind, replayID string) ProtocolReplayClaim {
	return ProtocolReplayClaim{
		WorkspaceID: workspace, Protocol: BindingProtocolA2A,
		PeerAuthority: "https://peer.joined-apply-postcommit.test",
		Kind:          kind, ReplayID: replayID,
		ExpiresAt: time.Now().UTC().Add(time.Hour),
	}
}

func setJoinedApplyPostcommitOutboxAttempts(t *testing.T, f workFixture, eventID model.ID, attempts int64) {
	t.Helper()
	row := outboxRowForTest(t, f, eventID)
	row[colOutboxAttempts] = attempts
	if err := f.st.Mutate(context.Background(), f.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(workOutboxKind)
		if err != nil {
			return err
		}
		_, err = repo.Update(context.Background(), row)
		return err
	}); err != nil {
		t.Fatalf("set outbox attempts: %v", err)
	}
}

func TestJoinedApplyPostcommitDrainCollector(t *testing.T) {
	t.Parallel()

	t.Run("commit_before_sink_and_parent_survives_probe_mutate", func(t *testing.T) {
		t.Parallel()
		f := newWorkFixture(t, filepath.Join(t.TempDir(), "japc-commit.db"), nil)
		defer f.st.Close()
		probe := &joinedApplyPostcommitProbeSink{st: f.st, tenant: f.tenant}
		f.m.UseWorkEventSink(probe)
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		start := time.Now()
		var created CommandResult
		result, err := f.m.ApplyProtocolReplay(ctx, f.tenant,
			joinedApplyPostcommitClaim(f.workspace, ProtocolReplayJTI, "japc-commit-"+model.NewID().String()),
			func(joinedCtx context.Context) (ProtocolReplaySettlement, error) {
				var applyErr error
				created, applyErr = f.m.Apply(joinedCtx, f.tenant, f.principal, baseCreateCommand(f, "japc commit"))
				return ProtocolReplaySettlement{}, applyErr
			})
		elapsed := time.Since(start)
		if err != nil || result.Replayed || created.EventID.IsZero() {
			t.Fatalf("owning apply = %#v created=%#v err=%v", result, created, err)
		}
		if elapsed >= joinedApplyPostcommitProbeBound {
			t.Fatalf("parent took %s; sink still ran inside the owning Mutate", elapsed)
		}
		if probe.entered.Load() < 1 {
			t.Fatal("post-commit drain never entered the sink")
		}
		if !probe.nestedOK.Load() {
			t.Fatalf("sink nested Mutate failed after %s; parent was still open",
				time.Duration(probe.nestedNS.Load()))
		}
		if row := outboxRowForTest(t, f, created.EventID); row.String(colOutboxState) != "published" {
			t.Fatalf("committed outbox = %v", row)
		}
	})

	t.Run("rollback_does_not_capture", func(t *testing.T) {
		t.Parallel()
		f := newWorkFixture(t, filepath.Join(t.TempDir(), "japc-rollback.db"), nil)
		defer f.st.Close()
		probe := &joinedApplyPostcommitProbeSink{st: f.st, tenant: f.tenant}
		f.m.UseWorkEventSink(probe)
		injected := errors.New("japc callback refusal")
		_, err := f.m.ApplyProtocolReplay(context.Background(), f.tenant,
			joinedApplyPostcommitClaim(f.workspace, ProtocolReplayJTI, "japc-rollback-"+model.NewID().String()),
			func(joinedCtx context.Context) (ProtocolReplaySettlement, error) {
				created, applyErr := f.m.Apply(joinedCtx, f.tenant, f.principal, baseCreateCommand(f, "japc rollback"))
				if applyErr != nil || created.EventID.IsZero() {
					return ProtocolReplaySettlement{}, applyErr
				}
				return ProtocolReplaySettlement{}, injected
			})
		if !errors.Is(err, injected) {
			t.Fatalf("parent err = %v, want callback refusal", err)
		}
		if probe.entered.Load() != 0 {
			t.Fatalf("rollback entered the sink %d times", probe.entered.Load())
		}
		if n := workCount(t, f, workItemKind); n != 0 {
			t.Fatalf("rollback left %d work items", n)
		}
	})

	t.Run("nested_owner_only_flush", func(t *testing.T) {
		t.Parallel()
		f := newWorkFixture(t, filepath.Join(t.TempDir(), "japc-nested.db"), nil)
		defer f.st.Close()
		seedSink := &recordingWorkSink{}
		f.m.UseWorkEventSink(seedSink)
		created := applyCreate(t, f, "japc nested seed")
		resetRecordingWorkSink(seedSink, errors.New("offline while seeding extra"))
		pending := insertOutboxEventForTest(t, f, workItemKind, created.ResultID, 2, "work.item.updated")
		probe := &joinedApplyPostcommitProbeSink{st: f.st, tenant: f.tenant}
		f.m.UseWorkEventSink(probe)
		var sinkAtInner int32
		_, err := f.m.ApplyProtocolReplay(context.Background(), f.tenant,
			joinedApplyPostcommitClaim(f.workspace, ProtocolReplayJTI, "japc-nested-outer-"+model.NewID().String()),
			func(outerCtx context.Context) (ProtocolReplaySettlement, error) {
				inner, innerErr := f.m.ApplyProtocolReplay(outerCtx, f.tenant,
					joinedApplyPostcommitClaim(f.workspace, ProtocolReplayMessageID, "japc-nested-inner-"+model.NewID().String()),
					func(innerCtx context.Context) (ProtocolReplaySettlement, error) {
						if err := f.m.drainWorkOutboxWithDataAndPolicy(
							innerCtx, f.m.workData(f.tenant), f.tenant, 10, false, nil,
						); err != nil {
							return ProtocolReplaySettlement{}, err
						}
						sinkAtInner = probe.entered.Load()
						return ProtocolReplaySettlement{}, nil
					})
				if innerErr != nil || inner.Replayed {
					return ProtocolReplaySettlement{}, innerErr
				}
				if probe.entered.Load() != 0 {
					return ProtocolReplaySettlement{}, errors.New("nested frame flushed before owner commit")
				}
				return ProtocolReplaySettlement{}, nil
			})
		if err != nil {
			t.Fatalf("nested owner replay: %v", err)
		}
		if sinkAtInner != 0 {
			t.Fatalf("inner Mutate return entered the sink %d times", sinkAtInner)
		}
		if probe.entered.Load() < 1 || !probe.nestedOK.Load() {
			t.Fatalf("owner did not flush after commit: entered=%d nestedOK=%t",
				probe.entered.Load(), probe.nestedOK.Load())
		}
		if row := outboxRowForTest(t, f, pending); row.String(colOutboxState) != "published" {
			t.Fatalf("owner flush left pending %v", row)
		}
	})

	t.Run("failed_attempt_isolation", func(t *testing.T) {
		t.Parallel()
		f := newWorkFixture(t, filepath.Join(t.TempDir(), "japc-retry.db"), nil)
		defer f.st.Close()
		sink := &recordingWorkSink{}
		f.m.UseWorkEventSink(sink)
		seed := applyCreate(t, f, "japc retry seed")
		resetRecordingWorkSink(sink, errors.New("offline while seeding extra"))
		pending := insertOutboxEventForTest(t, f, workItemKind, seed.ResultID, 2, "work.item.updated")
		resetRecordingWorkSink(sink, nil)
		attempts := 0
		result, err := f.m.ApplyProtocolReplay(context.Background(), f.tenant,
			joinedApplyPostcommitClaim(f.workspace, ProtocolReplayJTI, "japc-retry-"+model.NewID().String()),
			func(joinedCtx context.Context) (ProtocolReplaySettlement, error) {
				attempts++
				if attempts == 1 {
					if err := f.m.drainWorkOutboxWithDataAndPolicy(
						joinedCtx, f.m.workData(f.tenant), f.tenant, 10, false, nil,
					); err != nil {
						return ProtocolReplaySettlement{}, err
					}
					return ProtocolReplaySettlement{}, store.ErrConflict
				}
				return ProtocolReplaySettlement{}, nil
			})
		if err != nil || result.Replayed || attempts != 2 {
			t.Fatalf("retry = %#v attempts=%d err=%v", result, attempts, err)
		}
		if len(recordedSinkEvents(sink)) != 0 {
			t.Fatalf("discarded attempt flushed backlog: %+v", recordedSinkEvents(sink))
		}
		if row := outboxRowForTest(t, f, pending); row.String(colOutboxState) != "pending" {
			t.Fatalf("isolated retry published backlog: %v", row)
		}
	})

	t.Run("exact_protocol_replay_skips_unrelated_backlog", func(t *testing.T) {
		t.Parallel()
		f := newWorkFixture(t, filepath.Join(t.TempDir(), "japc-protocol-replay.db"), nil)
		defer f.st.Close()
		sink := &recordingWorkSink{}
		f.m.UseWorkEventSink(sink)
		claim := joinedApplyPostcommitClaim(f.workspace, ProtocolReplayJTI, "japc-protocol-"+model.NewID().String())
		first, err := f.m.ApplyProtocolReplay(context.Background(), f.tenant, claim,
			func(context.Context) (ProtocolReplaySettlement, error) {
				return ProtocolReplaySettlement{}, nil
			})
		if err != nil || first.Replayed {
			t.Fatalf("first protocol claim = %#v err=%v", first, err)
		}
		seed := applyCreate(t, f, "japc protocol backlog seed")
		resetRecordingWorkSink(sink, errors.New("offline while seeding extra"))
		pending := insertOutboxEventForTest(t, f, workItemKind, seed.ResultID, 2, "work.item.updated")
		resetRecordingWorkSink(sink, nil)
		mutations := 0
		replayed, err := f.m.ApplyProtocolReplay(context.Background(), f.tenant, claim,
			func(context.Context) (ProtocolReplaySettlement, error) {
				mutations++
				return ProtocolReplaySettlement{}, errors.New("exact protocol replay must not run")
			})
		if err != nil || !replayed.Replayed || mutations != 0 {
			t.Fatalf("exact protocol replay = %#v mutations=%d err=%v", replayed, mutations, err)
		}
		if len(recordedSinkEvents(sink)) != 0 {
			t.Fatalf("exact protocol replay drained backlog: %+v", recordedSinkEvents(sink))
		}
		if row := outboxRowForTest(t, f, pending); row.String(colOutboxState) != "pending" {
			t.Fatalf("exact protocol replay published backlog: %v", row)
		}
	})

	t.Run("exact_command_replay_skips_unrelated_backlog", func(t *testing.T) {
		t.Parallel()
		f := newWorkFixture(t, filepath.Join(t.TempDir(), "japc-command-replay.db"), nil)
		defer f.st.Close()
		sink := &recordingWorkSink{}
		f.m.UseWorkEventSink(sink)
		cmd := baseCreateCommand(f, "japc command replay")
		first, err := f.m.Apply(context.Background(), f.tenant, f.principal, cmd)
		if err != nil || first.EventID.IsZero() {
			t.Fatalf("first command = %#v err=%v", first, err)
		}
		other := applyCreate(t, f, "japc command backlog seed")
		resetRecordingWorkSink(sink, errors.New("offline while seeding extra"))
		pending := insertOutboxEventForTest(t, f, workItemKind, other.ResultID, 2, "work.item.updated")
		resetRecordingWorkSink(sink, nil)
		var replayed CommandResult
		result, err := f.m.ApplyProtocolReplay(context.Background(), f.tenant,
			joinedApplyPostcommitClaim(f.workspace, ProtocolReplayJTI, "japc-command-"+model.NewID().String()),
			func(joinedCtx context.Context) (ProtocolReplaySettlement, error) {
				var applyErr error
				replayed, applyErr = f.m.Apply(joinedCtx, f.tenant, f.principal, cmd)
				return ProtocolReplaySettlement{}, applyErr
			})
		if err != nil || result.Replayed || !replayed.Replayed || replayed.CommandID != first.CommandID {
			t.Fatalf("joined exact command = %#v replayed=%#v err=%v", result, replayed, err)
		}
		if len(recordedSinkEvents(sink)) != 0 {
			t.Fatalf("exact command replay drained backlog: %+v", recordedSinkEvents(sink))
		}
		if row := outboxRowForTest(t, f, pending); row.String(colOutboxState) != "pending" {
			t.Fatalf("exact command replay published backlog: %v", row)
		}
	})

	t.Run("preserves_caller_restriction_and_not_public_drain", func(t *testing.T) {
		t.Parallel()
		f := newWorkFixture(t, filepath.Join(t.TempDir(), "japc-policy.db"), nil)
		defer f.st.Close()
		sink := &recordingWorkSink{}
		f.m.UseWorkEventSink(sink)
		workItem := applyCreate(t, f, "japc policy work")
		heldItem := applyCreate(t, f, "japc policy held")
		resetRecordingWorkSink(sink, errors.New("offline while seeding extra"))
		allowed := insertOutboxEventForTest(t, f, workItemKind, workItem.ResultID, 2, "work.item.updated")
		held := insertOutboxEventForTest(t, f, workItemKind, heldItem.ResultID, 2, "work.handoff.offered")
		resetRecordingWorkSink(sink, nil)
		allowWork := WorkOutboxClaimPolicyFunc(func(_ context.Context, c WorkOutboxCandidate) (bool, error) {
			return c.Family == WorkEventFamilyWork, nil
		})
		denyAll := WorkOutboxClaimPolicyFunc(func(context.Context, WorkOutboxCandidate) (bool, error) {
			return false, nil
		})
		_, err := f.m.ApplyProtocolReplay(context.Background(), f.tenant,
			joinedApplyPostcommitClaim(f.workspace, ProtocolReplayJTI, "japc-policy-"+model.NewID().String()),
			func(joinedCtx context.Context) (ProtocolReplaySettlement, error) {
				if err := f.m.drainWorkOutboxWithDataAndPolicy(
					joinedCtx, f.m.workData(f.tenant), f.tenant, 10, false, allowWork,
				); err != nil {
					return ProtocolReplaySettlement{}, err
				}
				if err := f.m.drainWorkOutboxWithDataAndPolicy(
					joinedCtx, f.m.workData(f.tenant), f.tenant, 10, false, denyAll,
				); err != nil {
					return ProtocolReplaySettlement{}, err
				}
				return ProtocolReplaySettlement{}, nil
			})
		if err != nil {
			t.Fatalf("policy replay: %v", err)
		}
		if row := outboxRowForTest(t, f, allowed); row.String(colOutboxState) != "published" {
			t.Fatalf("allowed caller row = %v", row)
		}
		if row := outboxRowForTest(t, f, held); row.String(colOutboxState) != "pending" {
			t.Fatalf("denied caller row was published: %v", row)
		}
	})

	t.Run("apply_attempts9_exclusion", func(t *testing.T) {
		t.Parallel()
		f := newWorkFixture(t, filepath.Join(t.TempDir(), "japc-attempts.db"), nil)
		defer f.st.Close()
		sink := &recordingWorkSink{}
		f.m.UseWorkEventSink(sink)
		seed := applyCreate(t, f, "japc attempts seed")
		resetRecordingWorkSink(sink, errors.New("offline while seeding extra"))
		pending := insertOutboxEventForTest(t, f, workItemKind, seed.ResultID, 2, "work.item.updated")
		setJoinedApplyPostcommitOutboxAttempts(t, f, pending, 9)
		resetRecordingWorkSink(sink, nil)
		_, err := f.m.ApplyProtocolReplay(context.Background(), f.tenant,
			joinedApplyPostcommitClaim(f.workspace, ProtocolReplayJTI, "japc-attempts-"+model.NewID().String()),
			func(joinedCtx context.Context) (ProtocolReplaySettlement, error) {
				return ProtocolReplaySettlement{}, f.m.drainWorkOutboxWithDataAndPolicy(
					joinedCtx, f.m.workData(f.tenant), f.tenant, 10, false, nil,
				)
			})
		if err != nil {
			t.Fatalf("attempts9 replay: %v", err)
		}
		row := outboxRowForTest(t, f, pending)
		if row.String(colOutboxState) != "pending" || row.Int(colOutboxAttempts) != 9 {
			t.Fatalf("Apply dead-letter exclusion lost: %v", row)
		}
		if err := f.m.DrainWorkOutbox(context.Background(), f.tenant, 10); err != nil {
			t.Fatalf("public drain: %v", err)
		}
		if row := outboxRowForTest(t, f, pending); row.String(colOutboxState) != "published" {
			t.Fatalf("public drain with dead-letter did not claim attempts=9: %v", row)
		}
	})

	t.Run("limit_budget_leaves_older_backlog", func(t *testing.T) {
		t.Parallel()
		f := newWorkFixture(t, filepath.Join(t.TempDir(), "japc-limit.db"), nil)
		defer f.st.Close()
		sink := &recordingWorkSink{}
		f.m.UseWorkEventSink(sink)
		first := applyCreate(t, f, "japc limit a")
		second := applyCreate(t, f, "japc limit b")
		resetRecordingWorkSink(sink, errors.New("offline while seeding extra"))
		a := insertOutboxEventForTest(t, f, workItemKind, first.ResultID, 2, "work.item.updated")
		b := insertOutboxEventForTest(t, f, workItemKind, second.ResultID, 2, "work.item.updated")
		resetRecordingWorkSink(sink, nil)
		_, err := f.m.ApplyProtocolReplay(context.Background(), f.tenant,
			joinedApplyPostcommitClaim(f.workspace, ProtocolReplayJTI, "japc-limit-"+model.NewID().String()),
			func(joinedCtx context.Context) (ProtocolReplaySettlement, error) {
				return ProtocolReplaySettlement{}, f.m.drainWorkOutboxWithDataAndPolicy(
					joinedCtx, f.m.workData(f.tenant), f.tenant, 1, false, nil,
				)
			})
		if err != nil {
			t.Fatalf("limit replay: %v", err)
		}
		published, pending := 0, 0
		for _, id := range []model.ID{a, b} {
			switch outboxRowForTest(t, f, id).String(colOutboxState) {
			case "published":
				published++
			case "pending":
				pending++
			}
		}
		if published != 1 || pending != 1 {
			t.Fatalf("limit 1 with two eligible rows: published=%d pending=%d", published, pending)
		}
	})

	t.Run("confined_data_handle_is_preserved", func(t *testing.T) {
		t.Parallel()
		f := newWorkFixture(t, filepath.Join(t.TempDir(), "japc-confined.db"), nil)
		defer f.st.Close()
		sink := &recordingWorkSink{}
		f.m.UseWorkEventSink(sink)
		var other model.ID
		if err := f.st.Mutate(context.Background(), f.tenant, func(sc store.Scope) error {
			ws, err := sc.Workspaces().Create(context.Background(), model.Workspace{
				Name: "japc other", Slug: "japc-other", Status: model.StatusActive,
			})
			other = ws.ID
			return err
		}); err != nil {
			t.Fatalf("create other workspace: %v", err)
		}
		home := applyCreate(t, f, "japc confined home")
		otherCmd := baseCreateCommand(f, "japc confined other")
		otherCmd.WorkspaceID = other
		otherItem, err := f.m.Apply(context.Background(), f.tenant, f.principal, otherCmd)
		if err != nil {
			t.Fatalf("create other item: %v", err)
		}
		resetRecordingWorkSink(sink, errors.New("offline while seeding extra"))
		homePending := insertOutboxEventForTest(t, f, workItemKind, home.ResultID, 2, "work.item.updated")
		otherPending := model.NewID()
		now := model.NewTimestamp(time.Now().UTC())
		due := model.NewTimestamp(time.Now().UTC().Add(-time.Second))
		payload := []byte(`{"event_type":"work.item.updated","schema_version":1}`)
		if err := f.st.Mutate(context.Background(), f.tenant, func(sc store.Scope) error {
			events, err := sc.Ext(workEventKind)
			if err != nil {
				return err
			}
			if _, err := events.Create(context.Background(), model.Record{
				colWorkWorkspaceID: other.String(), colEventID: otherPending.String(),
				colEventAggregateKind: string(workItemKind), colEventAggregateID: otherItem.ResultID.String(),
				colEventSeq: 2, colEventType: "work.item.updated",
				colEventActorKind: "user", colEventActorRef: model.NewID().String(),
				colEventOccurredAt: now.String(), colEventPayload: string(payload),
				colEventPayloadHash: hashBytes(payload), colEventCommandID: model.NewID().String(),
				colEventAuditSeq: int64(1), colEventAuditHash: hashBytes([]byte("audit")),
			}); err != nil {
				return err
			}
			outbox, err := sc.Ext(workOutboxKind)
			if err != nil {
				return err
			}
			_, err = outbox.Create(context.Background(), model.Record{
				colWorkWorkspaceID: other.String(), colOutboxEventID: otherPending.String(),
				colOutboxState: "pending", colOutboxAttempts: int64(0),
				colOutboxNextAttemptAt: due.String(),
			})
			return err
		}); err != nil {
			t.Fatalf("insert other workspace outbox: %v", err)
		}
		resetRecordingWorkSink(sink, nil)
		confined := confinedWorkData{inner: f.m.workData(f.tenant), workspace: f.workspace}
		_, err = f.m.ApplyProtocolReplay(context.Background(), f.tenant,
			joinedApplyPostcommitClaim(f.workspace, ProtocolReplayJTI, "japc-confined-"+model.NewID().String()),
			func(joinedCtx context.Context) (ProtocolReplaySettlement, error) {
				return ProtocolReplaySettlement{}, f.m.drainWorkOutboxWithDataAndPolicy(
					joinedCtx, confined, f.tenant, 10, false, nil,
				)
			})
		if err != nil {
			t.Fatalf("confined replay: %v", err)
		}
		if row := outboxRowForTest(t, f, homePending); row.String(colOutboxState) != "published" {
			t.Fatalf("confined home row = %v", row)
		}
		if row := outboxRowForTest(t, f, otherPending); row.String(colOutboxState) != "pending" {
			t.Fatalf("confined drain published the other workspace: %v", row)
		}
	})

	t.Run("sink_failure_leaves_committed_pending_row", func(t *testing.T) {
		t.Parallel()
		f := newWorkFixture(t, filepath.Join(t.TempDir(), "japc-sinkfail.db"), nil)
		defer f.st.Close()
		sink := &recordingWorkSink{err: errors.New("eventing unavailable")}
		f.m.UseWorkEventSink(sink)
		var created CommandResult
		result, err := f.m.ApplyProtocolReplay(context.Background(), f.tenant,
			joinedApplyPostcommitClaim(f.workspace, ProtocolReplayJTI, "japc-sinkfail-"+model.NewID().String()),
			func(joinedCtx context.Context) (ProtocolReplaySettlement, error) {
				var applyErr error
				created, applyErr = f.m.Apply(joinedCtx, f.tenant, f.principal, baseCreateCommand(f, "japc sink fail"))
				return ProtocolReplaySettlement{}, applyErr
			})
		if err != nil || result.Replayed || created.ResultID.IsZero() {
			t.Fatalf("sink-fail apply = %#v created=%#v err=%v", result, created, err)
		}
		if _, err := f.m.Get(context.Background(), f.tenant, f.principal, created.ResultID); err != nil {
			t.Fatalf("committed item missing after sink failure: %v", err)
		}
		if row := outboxRowForTest(t, f, created.EventID); row.String(colOutboxState) == "published" {
			t.Fatalf("sink failure published the row: %v", row)
		}
	})

	t.Run("postcommit_cancellation_does_not_undo_command", func(t *testing.T) {
		t.Parallel()
		f := newWorkFixture(t, filepath.Join(t.TempDir(), "japc-cancel.db"), nil)
		defer f.st.Close()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		sink := &joinedApplyPostcommitCancelSink{cancel: cancel}
		f.m.UseWorkEventSink(sink)
		var created CommandResult
		result, err := f.m.ApplyProtocolReplay(ctx, f.tenant,
			joinedApplyPostcommitClaim(f.workspace, ProtocolReplayJTI, "japc-cancel-"+model.NewID().String()),
			func(joinedCtx context.Context) (ProtocolReplaySettlement, error) {
				var applyErr error
				created, applyErr = f.m.Apply(joinedCtx, f.tenant, f.principal, baseCreateCommand(f, "japc cancel"))
				return ProtocolReplaySettlement{}, applyErr
			})
		if err != nil || created.ResultID.IsZero() {
			t.Fatalf("cancelled flush apply = %#v created=%#v err=%v", result, created, err)
		}
		if _, err := f.m.Get(context.Background(), f.tenant, f.principal, created.ResultID); err != nil {
			t.Fatalf("committed item missing after cancelled flush: %v", err)
		}
		if row := outboxRowForTest(t, f, created.EventID); row.String(colOutboxState) == "published" {
			t.Fatalf("cancelled flush published the row: %v", row)
		}
	})

	t.Run("mask_drops_only_replay_value", func(t *testing.T) {
		t.Parallel()
		f := newWorkFixture(t, filepath.Join(t.TempDir(), "japc-mask.db"), nil)
		defer f.st.Close()
		f.m.UseWorkEventSink(&recordingWorkSink{})
		type workspaceKey struct{}
		parent, cancel := context.WithTimeout(context.WithValue(context.Background(), workspaceKey{}, "kept"), time.Minute)
		defer cancel()
		_, err := f.m.ApplyProtocolReplay(parent, f.tenant,
			joinedApplyPostcommitClaim(f.workspace, ProtocolReplayJTI, "japc-mask-"+model.NewID().String()),
			func(joinedCtx context.Context) (ProtocolReplaySettlement, error) {
				if _, ok := protocolReplayScopeFromContext(joinedCtx, f.tenant); !ok {
					return ProtocolReplaySettlement{}, errors.New("joined context lost the replay scope")
				}
				masked := maskProtocolReplayTransaction(joinedCtx)
				if _, ok := protocolReplayScopeFromContext(masked, f.tenant); ok {
					return ProtocolReplaySettlement{}, errors.New("masked context still joins")
				}
				if masked.Value(workspaceKey{}) != "kept" {
					return ProtocolReplaySettlement{}, errors.New("mask dropped a non-replay value")
				}
				if _, ok := masked.Deadline(); !ok {
					return ProtocolReplaySettlement{}, errors.New("mask dropped the deadline")
				}
				return ProtocolReplaySettlement{}, nil
			})
		if err != nil {
			t.Fatalf("mask replay: %v", err)
		}
	})
}

type joinedApplyPostcommitCancelSink struct {
	cancel  context.CancelFunc
	entered atomic.Int32
}

func (s *joinedApplyPostcommitCancelSink) IngestDurable(ctx context.Context, _ WorkEventEnvelope) error {
	s.entered.Add(1)
	s.cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	return context.Canceled
}

func newJoinedApplyPostcommitPeerModule(t *testing.T, f workFixture) *Module {
	t.Helper()
	peer := New(WithWorkIdentityResolver(allowWorkIdentity{}), WithWorkContentGuard(allowWorkContent{}))
	peer.UseData(api.NewModuleData(f.st))
	return peer
}

func TestJoinedApplyPostcommitDistinctModuleDrain(t *testing.T) {
	t.Parallel()

	t.Run("allowed_delivery_uses_peer_authority_and_sink", func(t *testing.T) {
		t.Parallel()
		f := newWorkFixture(t, filepath.Join(t.TempDir(), "japc-peer-allow.db"), nil)
		defer f.st.Close()
		ownerSink := &recordingWorkSink{}
		peerSink := &recordingWorkSink{}
		f.m.UseWorkEventSink(ownerSink)
		peer := newJoinedApplyPostcommitPeerModule(t, f)
		peer.UseWorkEventSink(peerSink)
		seed := applyCreate(t, f, "japc peer allow seed")
		resetRecordingWorkSink(ownerSink, errors.New("offline while seeding extra"))
		pending := insertOutboxEventForTest(t, f, workItemKind, seed.ResultID, 2, "work.handoff.offered")
		resetRecordingWorkSink(ownerSink, nil)
		resetRecordingWorkSink(peerSink, nil)
		ownerAuth := &recordingOutboxAuthority{allowClaim: false, allowEffect: true}
		peerAuth := &recordingOutboxAuthority{allowClaim: true, allowEffect: true}
		f.m.UseWorkOutboxClaimAuthority(ownerAuth)
		peer.UseWorkOutboxClaimAuthority(peerAuth)

		_, err := f.m.ApplyProtocolReplay(context.Background(), f.tenant,
			joinedApplyPostcommitClaim(f.workspace, ProtocolReplayJTI, "japc-peer-allow-"+model.NewID().String()),
			func(joinedCtx context.Context) (ProtocolReplaySettlement, error) {
				return ProtocolReplaySettlement{}, peer.drainWorkOutboxWithDataAndPolicy(
					joinedCtx, peer.workData(f.tenant), f.tenant, 10, false, nil,
				)
			})
		if err != nil {
			t.Fatalf("distinct-module allowed replay: %v", err)
		}
		if row := outboxRowForTest(t, f, pending); row.String(colOutboxState) != "published" {
			t.Fatalf("peer-allowed row stayed %v; suppression would also pass a refusal-only test", row)
		}
		if n := sinkAttemptCount(peerSink, pending); n != 1 {
			t.Fatalf("peer sink attempts=%d, want 1", n)
		}
		if n := sinkAttemptCount(ownerSink, pending); n != 0 {
			t.Fatalf("owner sink received the peer request %d times", n)
		}
		if claims, effects := peerAuth.seen(pending); claims < 1 || effects < 1 {
			t.Fatalf("peer authority claims=%d effects=%d, want both used", claims, effects)
		}
		if claims, effects := ownerAuth.seen(pending); claims != 0 || effects != 0 {
			t.Fatalf("owner authority was used for the peer request claims=%d effects=%d", claims, effects)
		}
	})

	t.Run("refusal_keeps_owner_untouched", func(t *testing.T) {
		t.Parallel()
		f := newWorkFixture(t, filepath.Join(t.TempDir(), "japc-peer-refuse.db"), nil)
		defer f.st.Close()
		ownerSink := &recordingWorkSink{}
		peerSink := &recordingWorkSink{}
		f.m.UseWorkEventSink(ownerSink)
		peer := newJoinedApplyPostcommitPeerModule(t, f)
		peer.UseWorkEventSink(peerSink)
		seed := applyCreate(t, f, "japc peer refuse seed")
		resetRecordingWorkSink(ownerSink, errors.New("offline while seeding extra"))
		pending := insertOutboxEventForTest(t, f, workItemKind, seed.ResultID, 2, "work.handoff.offered")
		resetRecordingWorkSink(ownerSink, nil)
		resetRecordingWorkSink(peerSink, nil)
		ownerAuth := &recordingOutboxAuthority{allowClaim: true, allowEffect: true}
		peerAuth := &recordingOutboxAuthority{allowClaim: false, allowEffect: true}
		f.m.UseWorkOutboxClaimAuthority(ownerAuth)
		peer.UseWorkOutboxClaimAuthority(peerAuth)

		_, err := f.m.ApplyProtocolReplay(context.Background(), f.tenant,
			joinedApplyPostcommitClaim(f.workspace, ProtocolReplayJTI, "japc-peer-refuse-"+model.NewID().String()),
			func(joinedCtx context.Context) (ProtocolReplaySettlement, error) {
				return ProtocolReplaySettlement{}, peer.drainWorkOutboxWithDataAndPolicy(
					joinedCtx, peer.workData(f.tenant), f.tenant, 10, false, nil,
				)
			})
		if err != nil {
			t.Fatalf("distinct-module refusal replay: %v", err)
		}
		if row := outboxRowForTest(t, f, pending); row.String(colOutboxState) != "pending" || row.Int(colOutboxAttempts) != 0 {
			t.Fatalf("peer-refused row was claimed through the owner: %v", row)
		}
		if n := sinkAttemptCount(peerSink, pending); n != 0 {
			t.Fatalf("peer sink saw a refused request %d times", n)
		}
		if n := sinkAttemptCount(ownerSink, pending); n != 0 {
			t.Fatalf("owner sink published the refused peer request %d times", n)
		}
		if claims, effects := peerAuth.seen(pending); claims < 1 || effects != 0 {
			t.Fatalf("peer authority claims=%d effects=%d, want a claim refusal and no effect", claims, effects)
		}
		if claims, effects := ownerAuth.seen(pending); claims != 0 || effects != 0 {
			t.Fatalf("owner authority was used for the refused peer request claims=%d effects=%d", claims, effects)
		}
	})
}

func TestJoinedApplyPostcommitCollectorCloseRace(t *testing.T) {
	t.Parallel()
	c := &deferredWorkOutboxCollector{}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 64; j++ {
				c.record(deferredWorkOutboxDrain{limit: 1, tenant: "t"})
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = c.closeAndTake()
	}()
	wg.Wait()
	c.record(deferredWorkOutboxDrain{limit: 2, tenant: "late"})
	if got := c.closeAndTake(); len(got) != 0 {
		t.Fatalf("append after close was retained: %+v", got)
	}
}
