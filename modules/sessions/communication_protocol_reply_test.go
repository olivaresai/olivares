// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func protocolReplyBindingForTest(
	t *testing.T,
	fixture workflowCommunicationFixture,
	direction BindingDirection,
	externalKind ProtocolBindingResultKind,
	externalID, contextID, externalMessageID string,
	carrier WorkflowWorkTaskResult,
) ProtocolBinding {
	t.Helper()
	digest := func(label string) []byte {
		sum := sha256.Sum256([]byte(label))
		return append([]byte(nil), sum[:]...)
	}
	bindingID := model.NewID()
	observedAt := fixture.now
	terminal := externalKind == ProtocolBindingResultMessage
	stored := storedProtocolBinding{
		ProtocolBinding: ProtocolBinding{
			MutableCommunicationEntity: MutableCommunicationEntity{
				CommunicationEntity: CommunicationEntity{
					ID: bindingID, TenantID: fixture.tenant, WorkspaceID: fixture.workspace,
					Version: 1, CreatedAt: fixture.now,
				},
				UpdatedAt: fixture.now,
			},
			BindingSpecID: model.NewID(), BindingSpecGeneration: 1,
			PinnedSpecHash:    digest("protocol-reply-spec"),
			PinnedMappingHash: digest("protocol-reply-mapping"),
			PinnedLossesHash:  digest("protocol-reply-losses"),
			WorkItemID:        fixture.workID, MessageID: carrier.MessageID, DeliveryID: carrier.DeliveryID,
			Protocol: BindingProtocolA2A, ProtocolVersion: "1.0.1", Direction: direction,
			PeerAuthority: "https://reply.example", RemoteResourceRef: "agent:reply",
			AttemptID: model.NewID(), Generation: 1, SyntheticSID: newSID(),
			OwnerKind: "agent", OwnerRef: "agent:reply", OwnerEpoch: 1,
			ExternalKind: string(externalKind), ExternalID: externalID,
			ContextID: contextID, ExternalMessageID: externalMessageID,
			LocalState: "running", RemoteState: "working",
			ObservationVerdict: ProtocolObservationClean, ObservationCode: "reply_observed",
			LastObservedAt: &observedAt, Terminal: terminal,
			LastCommandID: model.NewID(), LastEventID: model.NewID(), LastEventSeq: 1,
		},
		dispatchKeyHash: digest("protocol-reply-dispatch"),
		reservationHash: digest("protocol-reply-reservation"),
	}
	if terminal {
		stored.LocalState, stored.RemoteState = "completed", "completed"
	}
	if _, err := communicationCreateWithID(
		context.Background(), fixture.m, fixture.tenant,
		protocolBindingKind, bindingID, encodeProtocolBinding(stored),
	); err != nil {
		t.Fatalf("create protocol reply binding: %v", err)
	}
	return stored.ProtocolBinding
}

func protocolReplyRouteForTest(t *testing.T, fixture workflowCommunicationFixture) ProtocolInterruptRoute {
	t.Helper()
	recipient, err := model.ParseID(fixture.target.Ref)
	if err != nil {
		t.Fatalf("parse protocol reply recipient: %v", err)
	}
	return ProtocolInterruptRoute{
		ChannelID: fixture.channel.ID, SenderUserID: fixture.sender, RecipientUserID: recipient,
	}
}

// protocolReplyPlanForTest declares the reply command's publish, whose
// directory evidence a prepared replay reads before its transaction opens.
func protocolReplyPlanForTest(command ProtocolReplyCommand) ProtocolReplayPlan {
	return ProtocolReplayPlan{Publishes: []ProtocolReplayPublish{ProtocolReplyPublish(command.Route, command.Flow)}}
}

func protocolReplyDigestForTest(label string) string {
	digest := sha256.Sum256([]byte(label))
	return strings.ToLower(fmt.Sprintf("%x", digest[:]))
}

func TestProtocolInboundMessageIsRefusedWithOrWithoutRollback(t *testing.T) {
	t.Parallel()

	dbPath := filepath.Join(t.TempDir(), "protocol-inbound-restart.db")
	direct := newDirectNoticeFixtureForBackend(t, communicationSchemaBackend{
		name: "sqlite-protocol-inbound", engineName: store.EngineSQLite, dsn: dbPath,
	}, AckPolicyNone, 0, true, true, true)
	fixture := newWorkflowCommunicationFixtureFromDirect(t, false, direct)
	binding := protocolReplyBindingForTest(
		t, fixture, BindingInbound, ProtocolBindingResultTask,
		"task-inbound-1", "context-inbound-1", "", WorkflowWorkTaskResult{},
	)
	route := protocolReplyRouteForTest(t, fixture)
	command := ProtocolReplyCommand{
		Flow:      ProtocolReplyFlowInbound,
		BindingID: binding.ID, Generation: binding.Generation, Route: route,
		PeerAuthority: binding.PeerAuthority, Kind: ProtocolReplyMessage,
		TaskID: binding.ExternalID, ContextID: binding.ContextID, MessageID: "message-inbound-1",
		SourceDigest: protocolReplyDigestForTest("message-inbound-1"),
		Parts: []ProtocolReplyPart{
			{Kind: ProtocolReplyPartText, Text: "Authenticated inbound request.", Digest: protocolReplyDigestForTest("inbound-text")},
			{Kind: ProtocolReplyPartData, Reference: "a2a-part:" + protocolReplyDigestForTest("inbound-data"), Digest: protocolReplyDigestForTest("inbound-data")},
		},
	}
	claim := ProtocolReplayClaim{
		WorkspaceID: fixture.workspace, Protocol: BindingProtocolA2A,
		PeerAuthority: binding.PeerAuthority, Kind: ProtocolReplayMessageID,
		ReplayID: command.MessageID, ExpiresAt: fixture.now.Add(5 * time.Minute),
		ExpectedBindingID: binding.ID,
	}
	// CM-10: an inbound A2A message is an A2A path. Rolled back or not, it is
	// refused while its publish is prepared, before its transaction, and writes
	// nothing: there is no Message to retry, restart or replay.
	before := a2aRowCounts(t, fixture.directNoticeFixture)
	for _, settle := range []error{errors.New("force inbound rollback"), nil} {
		_, err := fixture.m.ApplyPreparedProtocolReplay(
			context.Background(), fixture.tenant, claim, protocolReplyPlanForTest(command),
			func(joined context.Context) (ProtocolReplaySettlement, error) {
				if _, projectErr := fixture.m.ProjectProtocolReply(joined, fixture.tenant, command); projectErr != nil {
					return ProtocolReplaySettlement{}, projectErr
				}
				return ProtocolReplaySettlement{BindingID: binding.ID}, settle
			},
		)
		requireA2ARefused(t, err, "inbound protocol Message")
	}
	requireA2ANoRows(t, fixture.directNoticeFixture, before, "a refused inbound protocol Message")
	requireA2ABindingUnchanged(t, fixture, binding)
}

func TestProtocolReplyReplayRestartAndThreading(t *testing.T) {
	t.Parallel()

	dbPath := filepath.Join(t.TempDir(), "protocol-reply-restart.db")
	direct := newDirectNoticeFixtureForBackend(t, communicationSchemaBackend{
		name: "sqlite-protocol-reply", engineName: store.EngineSQLite, dsn: dbPath,
	}, AckPolicyNone, 0, true, true, true)
	fixture := newWorkflowCommunicationFixtureFromDirect(t, false, direct)
	binding := protocolReplyBindingForTest(
		t, fixture, BindingOutbound, ProtocolBindingResultTask,
		"task-1", "context-1", "", WorkflowWorkTaskResult{},
	)
	route := protocolReplyRouteForTest(t, fixture)
	command := ProtocolReplyCommand{
		BindingID: binding.ID, Generation: binding.Generation, Route: route,
		PeerAuthority: binding.PeerAuthority, Kind: ProtocolReplyMessage,
		TaskID: binding.ExternalID, ContextID: binding.ContextID, MessageID: "message-1",
		SourceDigest: protocolReplyDigestForTest("message-1"),
		Parts: []ProtocolReplyPart{
			{Kind: ProtocolReplyPartText, Text: "Remote work completed.", Digest: protocolReplyDigestForTest("text")},
			{Kind: ProtocolReplyPartData, Reference: "a2a-part:" + protocolReplyDigestForTest("data"), Digest: protocolReplyDigestForTest("data")},
		},
	}
	claim := ProtocolReplayClaim{
		WorkspaceID: fixture.workspace, Protocol: BindingProtocolA2A,
		PeerAuthority: binding.PeerAuthority, Kind: ProtocolReplayMessageID,
		ReplayID: command.MessageID, ExpiresAt: fixture.now.Add(5 * time.Minute),
		ExpectedBindingID: binding.ID,
	}
	// CM-10: the outbound reply is an A2A path. It is refused while its publish
	// is prepared, before its transaction, and writes nothing: there is no reply
	// to replay, restart or thread.
	before := a2aRowCounts(t, fixture.directNoticeFixture)
	settle := error(nil)
	_, err := fixture.m.ApplyPreparedProtocolReplay(
		context.Background(), fixture.tenant, claim, protocolReplyPlanForTest(command),
		func(joined context.Context) (ProtocolReplaySettlement, error) {
			if _, projectErr := fixture.m.ProjectProtocolReply(joined, fixture.tenant, command); projectErr != nil {
				return ProtocolReplaySettlement{}, projectErr
			}
			return ProtocolReplaySettlement{BindingID: binding.ID}, settle
		},
	)
	requireA2ARefused(t, err, "outbound protocol reply")
	requireA2ANoRows(t, fixture.directNoticeFixture, before, "a refused outbound protocol reply")
	requireA2ABindingUnchanged(t, fixture, binding)
}

func TestProtocolReplyRollbackLeavesGuardAndMessageAbsent(t *testing.T) {
	t.Parallel()

	fixture := newWorkflowCommunicationFixture(t, false)
	binding := protocolReplyBindingForTest(
		t, fixture, BindingOutbound, ProtocolBindingResultTask,
		"task-rb", "context-rb", "", WorkflowWorkTaskResult{},
	)
	command := ProtocolReplyCommand{
		BindingID: binding.ID, Generation: binding.Generation,
		Route: protocolReplyRouteForTest(t, fixture), PeerAuthority: binding.PeerAuthority,
		Kind: ProtocolReplyMessage, TaskID: binding.ExternalID,
		ContextID: binding.ContextID, MessageID: "message-rb",
		SourceDigest: protocolReplyDigestForTest("message-rb"),
		Parts: []ProtocolReplyPart{{
			Kind: ProtocolReplyPartText, Text: "Retry after rollback.",
			Digest: protocolReplyDigestForTest("rollback-text"),
		}},
	}
	claim := ProtocolReplayClaim{
		WorkspaceID: fixture.workspace, Protocol: BindingProtocolA2A,
		PeerAuthority: binding.PeerAuthority, Kind: ProtocolReplayMessageID,
		ReplayID: command.MessageID, ExpiresAt: fixture.now.Add(5 * time.Minute),
		ExpectedBindingID: binding.ID,
	}
	// CM-10: the reply is an A2A path. Rolled back or retried, it is refused
	// while its publish is prepared and leaves no guard and no Message.
	before := a2aRowCounts(t, fixture.directNoticeFixture)
	for _, settle := range []error{errors.New("force rollback"), nil} {
		_, err := fixture.m.ApplyPreparedProtocolReplay(
			context.Background(), fixture.tenant, claim, protocolReplyPlanForTest(command),
			func(joined context.Context) (ProtocolReplaySettlement, error) {
				if _, projectErr := fixture.m.ProjectProtocolReply(joined, fixture.tenant, command); projectErr != nil {
					return ProtocolReplaySettlement{}, projectErr
				}
				return ProtocolReplaySettlement{BindingID: binding.ID}, settle
			},
		)
		requireA2ARefused(t, err, "protocol reply")
	}
	requireA2ANoRows(t, fixture.directNoticeFixture, before, "a refused protocol reply")
	if rows := communicationRowsForTest(t, fixture.directNoticeFixture, protocolReplayGuardKind); len(rows) != 0 {
		t.Fatalf("protocol replay guards after the refusal = %d, want 0", len(rows))
	}
}

func TestProtocolReplyUsesExactOutboundCarrierThread(t *testing.T) {
	t.Parallel()

	// The outbound carrier is a bound run's WorkTask. The protocol reply on it
	// is an A2A path and stays refused (CM-10).
	bound := newWorkflowBindingFixture(t, workflowSQLiteBackend(t, "carrier-thread"), false)
	fixture := bound.workflowCommunicationFixture
	makeProtocolInterruptRecipientWriter(t, fixture)
	parent, err := bound.send(context.Background(), WorkflowWorkTaskCommand{
		Actor: bound.bound, WorkItemID: fixture.workID, ChannelID: fixture.channel.ID,
		Recipient: fixture.target,
		Content: MessageContent{Subject: "Remote task", Blocks: []MessageContentBlock{{
			Type: ContentBlockText, Format: TextPlain, Text: "Perform the governed remote task.",
		}}},
		IdempotencyKey: "protocol-reply-carrier:" + model.NewID().String(),
	})
	if err != nil {
		t.Fatalf("create outbound carrier: %v", err)
	}
	binding := protocolReplyBindingForTest(
		t, fixture, BindingOutbound, ProtocolBindingResultTask,
		"task-carrier", "context-carrier", "", parent,
	)
	targetID, err := model.ParseID(fixture.target.Ref)
	if err != nil {
		t.Fatal(err)
	}
	route := ProtocolInterruptRoute{
		ChannelID: fixture.channel.ID, SenderUserID: targetID, RecipientUserID: fixture.sender,
	}
	command := ProtocolReplyCommand{
		BindingID: binding.ID, Generation: binding.Generation, Route: route,
		PeerAuthority: binding.PeerAuthority, Kind: ProtocolReplyMessage,
		TaskID: binding.ExternalID, ContextID: binding.ContextID, MessageID: "message-carrier",
		SourceDigest: protocolReplyDigestForTest("message-carrier"),
		Parts: []ProtocolReplyPart{{
			Kind: ProtocolReplyPartText, Text: "Carrier-linked reply.",
			Digest: protocolReplyDigestForTest("carrier-text"),
		}},
	}
	before := a2aRowCounts(t, fixture.directNoticeFixture)
	var reply ProtocolReplyResult
	_, err = fixture.m.ApplyPreparedProtocolReplay(
		context.Background(), fixture.tenant, ProtocolReplayClaim{
			WorkspaceID: fixture.workspace, Protocol: BindingProtocolA2A,
			PeerAuthority: binding.PeerAuthority, Kind: ProtocolReplayMessageID,
			ReplayID: command.MessageID, ExpiresAt: fixture.now.Add(5 * time.Minute),
			ExpectedBindingID: binding.ID,
		}, protocolReplyPlanForTest(command), func(joined context.Context) (ProtocolReplaySettlement, error) {
			var projectErr error
			reply, projectErr = fixture.m.ProjectProtocolReply(joined, fixture.tenant, command)
			return ProtocolReplaySettlement{BindingID: binding.ID}, projectErr
		},
	)
	requireA2ARefused(t, err, "carrier-linked protocol reply")
	requireA2ANoRows(t, fixture.directNoticeFixture, before, "a refused carrier-linked reply")
	requireA2ABindingUnchanged(t, fixture, binding)
	if !reply.MessageID.IsZero() {
		t.Fatalf("a refused carrier reply returned %+v", reply)
	}
}
