// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/engine/enginetest"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// A reply published on an item whose Message-kind binding is terminal at the
// item's sequence commits and leaves that binding at its version, on each
// engine. That is the state a synchronous A2A Message result leaves: its
// settlement makes the binding terminal with the item's next event, and its
// reply is projected in the same replay (cmd/olivares orchremote.go). The
// store refuses any update of a terminal binding, so the reply's event must
// advance the item alone.
func TestAReplyLeavesATerminalMessageBindingAtItsVersion(t *testing.T) {
	t.Parallel()

	t.Run("sqlite", func(t *testing.T) {
		wantReplyLeavesTerminalMessageBinding(t, newWorkflowCommunicationFixture(t, false))
	})
	t.Run("postgres", func(t *testing.T) {
		if !enginetest.PostgresAvailable(t) {
			if raceDetectorEnabled {
				t.Fatalf("%s names no PostgreSQL server: the terminal binding guard is decided there too",
					enginetest.EnvSuperuserDSN)
			}
			t.Skipf("%s unset: the PostgreSQL terminal binding guard is NOT exercised", enginetest.EnvSuperuserDSN)
		}
		pg := enginetest.IsolatedPostgres(t)
		direct := newDirectNoticeFixtureForBackend(t, communicationSchemaBackend{
			name: "postgres-terminal-message-binding", engineName: store.EnginePostgres, dsn: pg.App,
		}, AckPolicyNone, 0, true, true, true)
		wantReplyLeavesTerminalMessageBinding(t, newWorkflowCommunicationFixtureFromDirect(t, false, direct))
	})
}

// wantReplyLeavesTerminalMessageBinding is not a test helper, so a failure
// names its own assertion's line.
func wantReplyLeavesTerminalMessageBinding(t *testing.T, fixture workflowCommunicationFixture) {
	ctx := context.Background()
	binding := terminalMessageBindingForTest(t, fixture)
	item := workflowCommunicationRecord(t, fixture, workItemKind, fixture.workID)
	if !binding.Terminal || binding.LastEventSeq != item.Int(colWorkLastEventSeq) {
		t.Fatalf("setup: binding terminal=%t at event %d, item at event %d; want a terminal binding at the item's event",
			binding.Terminal, binding.LastEventSeq, item.Int(colWorkLastEventSeq))
	}
	command := ProtocolReplyCommand{
		BindingID: binding.ID, Generation: binding.Generation,
		Route: protocolReplyRouteForTest(t, fixture), PeerAuthority: binding.PeerAuthority,
		Kind: ProtocolReplyMessage, ContextID: binding.ContextID, MessageID: binding.ExternalMessageID,
		SourceDigest: protocolReplyDigestForTest("terminal-message"),
		Parts: []ProtocolReplyPart{{
			Kind: ProtocolReplyPartText, Text: "The remote agent's final answer.",
			Digest: protocolReplyDigestForTest("terminal-message-text"),
		}},
	}
	var reply ProtocolReplyResult
	_, err := fixture.m.ApplyPreparedProtocolReplay(ctx, fixture.tenant, ProtocolReplayClaim{
		WorkspaceID: fixture.workspace, Protocol: BindingProtocolA2A,
		PeerAuthority: binding.PeerAuthority, Kind: ProtocolReplayMessageID,
		ReplayID: command.MessageID, ExpiresAt: fixture.now.Add(5 * time.Minute),
		ExpectedBindingID: binding.ID,
	}, protocolReplyPlanForTest(command), func(joined context.Context) (ProtocolReplaySettlement, error) {
		var projectErr error
		reply, projectErr = fixture.m.ProjectProtocolReply(joined, fixture.tenant, command)
		return ProtocolReplaySettlement{BindingID: binding.ID}, projectErr
	})
	// CM-10: the reply is an A2A path and is refused while its publish is
	// prepared, so the terminal binding and the item both stay where they were.
	requireA2ARefused(t, err, "reply on an item whose Message binding is terminal")
	if !reply.MessageID.IsZero() {
		t.Fatalf("a refused reply returned %+v", reply)
	}
	after, err := fixture.m.GetProtocolBinding(ctx, fixture.tenant, ProtocolBindingRef{ID: binding.ID})
	if err != nil {
		t.Fatalf("read the terminal binding after the refusal: %v", err)
	}
	if !after.Terminal || after.Version != binding.Version || after.LastEventSeq != binding.LastEventSeq ||
		after.LastEventID != binding.LastEventID || after.LastCommandID != binding.LastCommandID {
		t.Fatalf("terminal binding after the refusal = version %d, event %d; want version %d, event %d, unchanged",
			after.Version, after.LastEventSeq, binding.Version, binding.LastEventSeq)
	}
	item = workflowCommunicationRecord(t, fixture, workItemKind, fixture.workID)
	if item.Int(colWorkLastEventSeq) != binding.LastEventSeq {
		t.Fatalf("item at event %d after the refusal, want it unchanged at %d",
			item.Int(colWorkLastEventSeq), binding.LastEventSeq)
	}
}

// terminalMessageBindingForTest leaves fixture's WorkItem with an outbound
// Message-kind binding that is terminal at the item's event sequence. The
// store refuses to create a binding terminal, so the binding is created in
// step with the item and then settled with the item's next event in one
// transaction, as SettleProtocolBinding appends one. The rows are written
// directly because the fixture's item is a draft, which a settlement cannot
// move to review.
func terminalMessageBindingForTest(t *testing.T, fixture workflowCommunicationFixture) ProtocolBinding {
	t.Helper()
	ctx := context.Background()
	created := protocolReplyBindingForTest(
		t, fixture, BindingOutbound, ProtocolBindingResultTaskOrMessage,
		"", "context-terminal-message", "", WorkflowWorkTaskResult{},
	)
	var settled ProtocolBinding
	err := communicationMutateFenced(ctx, fixture.m, fixture.st, fixture.tenant, []model.ID{fixture.sender},
		func(sc store.Scope) error {
			items, err := sc.Ext(workItemKind)
			if err != nil {
				return err
			}
			item, err := items.Get(ctx, fixture.workID)
			if err != nil {
				return err
			}
			seq := item.Int(colWorkLastEventSeq) + 1
			commandID := model.NewID()
			event := workSchemaEvent(fixture.workspace, fixture.workID.String(), commandID.String(), seq, "terminal-message")
			event[colEventType] = "work.binding.observed"
			event[colEventActorRef] = fixture.sender.String()
			eventID, err := model.ParseID(event.String(colEventID))
			if err != nil {
				return err
			}
			item[colWorkLastEventSeq] = seq
			if _, err := items.Update(ctx, item); err != nil {
				return err
			}
			events, err := sc.Ext(workEventKind)
			if err != nil {
				return err
			}
			if _, err := events.CreateWithID(ctx, model.NewID(), event); err != nil {
				return err
			}
			bindings, err := sc.Ext(protocolBindingKind)
			if err != nil {
				return err
			}
			record, err := bindings.Get(ctx, created.ID)
			if err != nil {
				return err
			}
			stored, err := decodeProtocolBinding(record)
			if err != nil {
				return err
			}
			stored.ExternalKind = string(ProtocolBindingResultMessage)
			stored.ExternalMessageID = "remote-terminal-message"
			stored.LocalState, stored.RemoteState = "review", "completed"
			stored.ObservationCode = "remote_message"
			stored.Terminal = true
			stored.LastCommandID, stored.LastEventID, stored.LastEventSeq = commandID, eventID, seq
			updated, err := bindings.Update(ctx, encodeProtocolBinding(stored))
			if err != nil {
				return err
			}
			stored, err = decodeProtocolBinding(updated)
			settled = stored.ProtocolBinding
			return err
		})
	if err != nil {
		t.Fatalf("settle the Message binding terminal with the item's next event: %v", err)
	}
	return settled
}
