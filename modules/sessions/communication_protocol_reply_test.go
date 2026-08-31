// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/engine"
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

func protocolReplyDigestForTest(label string) string {
	digest := sha256.Sum256([]byte(label))
	return strings.ToLower(fmt.Sprintf("%x", digest[:]))
}

func TestProtocolInboundMessageRollbackRetryAndRestart(t *testing.T) {
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
	beforeWork := workflowCommunicationRecord(t, fixture, workItemKind, fixture.workID)
	beforeMessages := len(communicationRowsForTest(t, fixture.directNoticeFixture, messageKind))

	_, err := fixture.m.ApplyProtocolReplay(
		context.Background(), fixture.tenant, claim,
		func(joined context.Context) (ProtocolReplaySettlement, error) {
			if _, projectErr := fixture.m.ProjectProtocolReply(joined, fixture.tenant, command); projectErr != nil {
				return ProtocolReplaySettlement{}, projectErr
			}
			return ProtocolReplaySettlement{}, errors.New("force inbound rollback")
		},
	)
	if err == nil || len(communicationRowsForTest(t, fixture.directNoticeFixture, messageKind)) != beforeMessages ||
		len(communicationRowsForTest(t, fixture.directNoticeFixture, protocolReplayGuardKind)) != 0 {
		t.Fatalf("inbound rollback leaked Message/guard: err=%v", err)
	}

	var created ProtocolReplyResult
	guarded, err := fixture.m.ApplyProtocolReplay(
		context.Background(), fixture.tenant, claim,
		func(joined context.Context) (ProtocolReplaySettlement, error) {
			var projectErr error
			created, projectErr = fixture.m.ProjectProtocolReply(joined, fixture.tenant, command)
			return ProtocolReplaySettlement{BindingID: binding.ID}, projectErr
		},
	)
	if err != nil || guarded.Replayed || created.Replayed || created.BindingID != binding.ID ||
		created.WorkItemID != fixture.workID || created.MessageID.IsZero() ||
		created.DeliveryID.IsZero() || created.ThreadID != created.MessageID ||
		!created.ReplyToID.IsZero() || created.State != MessagePublished ||
		len(communicationRowsForTest(t, fixture.directNoticeFixture, messageKind)) != beforeMessages+1 {
		t.Fatalf("created inbound protocol Message = %+v; guard=%+v; err=%v", created, guarded, err)
	}
	message, err := messageFromRecord(
		workflowCommunicationRecord(t, fixture, messageKind, created.MessageID), 0,
	)
	if err != nil || message.Kind != MessageNotice || message.AckPolicy != AckPolicyNone ||
		message.WorkItemID != fixture.workID || message.ThreadID != message.ID ||
		message.Sender != (CommunicationActorRef{Kind: ActorUser, Ref: route.SenderUserID.String()}) {
		t.Fatalf("stored inbound protocol Message = %+v, err=%v", message, err)
	}
	var content MessageContent
	if err := json.Unmarshal(message.Payload.PlainJSON, &content); err != nil ||
		content.Subject != "Remote agent message" || len(content.Blocks) != 4 ||
		content.Blocks[1].Reference == nil || content.Blocks[1].Reference.Hash != command.SourceDigest ||
		content.Blocks[2].Text != command.Parts[0].Text || content.Blocks[3].Reference == nil ||
		content.Blocks[3].Reference.Ref != command.Parts[1].Reference {
		t.Fatalf("inbound protocol Message content = %+v, err=%v", content, err)
	}
	if len(communicationRowsForTest(t, fixture.directNoticeFixture, messageAckKind)) != 0 ||
		len(communicationRowsForTest(t, fixture.directNoticeFixture, decisionRequestKind)) != 0 ||
		len(communicationRowsForTest(t, fixture.directNoticeFixture, decisionResponseKind)) != 0 {
		t.Fatal("inbound protocol Message created Ack or Decision authority")
	}
	afterWork := workflowCommunicationRecord(t, fixture, workItemKind, fixture.workID)
	for _, column := range []string{colWorkStatus, colWorkOwnerKind, colWorkOwnerRef, colWorkTerminalCode} {
		if beforeWork[column] != afterWork[column] {
			t.Fatalf("inbound protocol Message changed WorkItem %s: %v -> %v",
				column, beforeWork[column], afterWork[column])
		}
	}

	mutationCalled := false
	replayed, err := fixture.m.ApplyProtocolReplay(
		context.Background(), fixture.tenant, claim,
		func(context.Context) (ProtocolReplaySettlement, error) {
			mutationCalled = true
			return ProtocolReplaySettlement{}, errors.New("must not run")
		},
	)
	reloaded, reloadErr := fixture.m.GetProtocolReply(context.Background(), fixture.tenant, command.Ref())
	if err != nil || !replayed.Replayed || mutationCalled || reloadErr != nil ||
		!reloaded.Replayed || reloaded.MessageID != created.MessageID ||
		reloaded.DeliveryID != created.DeliveryID {
		t.Fatalf("inbound exact retry = guard:%+v reply:%+v called=%v err=%v/%v",
			replayed, reloaded, mutationCalled, err, reloadErr)
	}

	if err := fixture.st.Close(); err != nil {
		t.Fatalf("close inbound store before restart: %v", err)
	}
	restarted := New()
	reopened, err := engine.Open(context.Background(), store.Config{
		Engine: store.EngineSQLite, DSN: dbPath, Debug: true,
		Clock: &testClock{now: fixture.now},
	}, restarted.RegisterSchema)
	if err != nil {
		t.Fatalf("restart inbound store: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	restarted.UseData(api.NewModuleData(reopened))
	restartMutationCalled := false
	restartedGuard, err := restarted.ApplyProtocolReplay(
		context.Background(), fixture.tenant, claim,
		func(context.Context) (ProtocolReplaySettlement, error) {
			restartMutationCalled = true
			return ProtocolReplaySettlement{}, errors.New("restart replay must not mutate")
		},
	)
	restartedReply, restartReplyErr := restarted.GetProtocolReply(
		context.Background(), fixture.tenant, command.Ref(),
	)
	if err != nil || !restartedGuard.Replayed || restartMutationCalled || restartReplyErr != nil ||
		restartedReply.MessageID != created.MessageID || restartedReply.DeliveryID != created.DeliveryID ||
		!restartedReply.Replayed {
		t.Fatalf("inbound restart = guard:%+v reply:%+v called=%v err=%v/%v",
			restartedGuard, restartedReply, restartMutationCalled, err, restartReplyErr)
	}
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
	beforeWork := workflowCommunicationRecord(t, fixture, workItemKind, fixture.workID)
	var created ProtocolReplyResult
	guarded, err := fixture.m.ApplyProtocolReplay(
		context.Background(), fixture.tenant, claim,
		func(joined context.Context) (ProtocolReplaySettlement, error) {
			var projectErr error
			created, projectErr = fixture.m.ProjectProtocolReply(joined, fixture.tenant, command)
			return ProtocolReplaySettlement{BindingID: binding.ID}, projectErr
		},
	)
	if err != nil {
		t.Fatalf("apply protocol reply replay guard: %v", err)
	}
	if guarded.Replayed || created.Replayed || created.MessageID.IsZero() ||
		created.ThreadID != created.MessageID || !created.ReplyToID.IsZero() ||
		created.State != MessagePublished {
		t.Fatalf("created protocol reply = %+v; guard = %+v", created, guarded)
	}
	messageRecord := workflowCommunicationRecord(t, fixture, messageKind, created.MessageID)
	message, err := messageFromRecord(messageRecord, 0)
	if err != nil {
		t.Fatalf("decode protocol reply Message: %v", err)
	}
	if message.Kind != MessageNotice || message.AckPolicy != AckPolicyNone ||
		message.WorkItemID != fixture.workID || message.ThreadID != message.ID ||
		message.Sender != (CommunicationActorRef{Kind: ActorUser, Ref: route.SenderUserID.String()}) {
		t.Fatalf("stored protocol reply Message = %+v", message)
	}
	var content MessageContent
	if err := json.Unmarshal(message.Payload.PlainJSON, &content); err != nil {
		t.Fatalf("decode protocol reply content: %v", err)
	}
	if len(content.Blocks) != 4 || content.Blocks[0].Reference == nil ||
		content.Blocks[0].Reference.Kind != "protocol_binding" ||
		content.Blocks[1].Reference == nil || content.Blocks[1].Reference.Hash != command.SourceDigest ||
		content.Blocks[2].Format != TextPlain || content.Blocks[2].Text != "Remote work completed." ||
		content.Blocks[3].Reference == nil || content.Blocks[3].Reference.Ref != command.Parts[1].Reference {
		t.Fatalf("projected protocol reply content = %+v", content)
	}
	if len(communicationRowsForTest(t, fixture.directNoticeFixture, messageAckKind)) != 0 ||
		len(communicationRowsForTest(t, fixture.directNoticeFixture, decisionRequestKind)) != 0 ||
		len(communicationRowsForTest(t, fixture.directNoticeFixture, decisionResponseKind)) != 0 {
		t.Fatal("protocol reply created Ack or Decision authority")
	}
	afterWork := workflowCommunicationRecord(t, fixture, workItemKind, fixture.workID)
	for _, column := range []string{colWorkStatus, colWorkOwnerKind, colWorkOwnerRef, colWorkTerminalCode} {
		if beforeWork[column] != afterWork[column] {
			t.Fatalf("protocol reply changed WorkItem %s: %v -> %v", column, beforeWork[column], afterWork[column])
		}
	}

	mutationCalled := false
	replayedGuard, err := fixture.m.ApplyProtocolReplay(
		context.Background(), fixture.tenant, claim,
		func(context.Context) (ProtocolReplaySettlement, error) {
			mutationCalled = true
			return ProtocolReplaySettlement{}, errors.New("must not run")
		},
	)
	if err != nil || !replayedGuard.Replayed || mutationCalled {
		t.Fatalf("exact protocol replay = %+v, called=%v, err=%v", replayedGuard, mutationCalled, err)
	}
	reloaded, err := fixture.m.GetProtocolReply(context.Background(), fixture.tenant, command.Ref())
	if err != nil || !reloaded.Replayed || reloaded.MessageID != created.MessageID ||
		reloaded.DeliveryID != created.DeliveryID || reloaded.ThreadID != created.ThreadID {
		t.Fatalf("reloaded durable protocol reply = %+v, %v", reloaded, err)
	}

	artifact := ProtocolReplyCommand{
		BindingID: binding.ID, Generation: binding.Generation, Route: route,
		PeerAuthority: binding.PeerAuthority, Kind: ProtocolReplyArtifact,
		TaskID: binding.ExternalID, ContextID: binding.ContextID, ArtifactID: "artifact-1",
		SourceDigest: protocolReplyDigestForTest("artifact-1"),
		Parts: []ProtocolReplyPart{{
			Kind: ProtocolReplyPartFile, Reference: "artifact:result-1",
			Digest: protocolReplyDigestForTest("artifact-file"),
		}},
	}
	var child ProtocolReplyResult
	_, err = fixture.m.ApplyProtocolReplay(
		context.Background(), fixture.tenant, ProtocolReplayClaim{
			WorkspaceID: fixture.workspace, Protocol: BindingProtocolA2A,
			PeerAuthority: binding.PeerAuthority, Kind: ProtocolReplayMessageID,
			ReplayID: "artifact-1", ExpiresAt: fixture.now.Add(5 * time.Minute),
			ExpectedBindingID: binding.ID,
		}, func(joined context.Context) (ProtocolReplaySettlement, error) {
			var projectErr error
			child, projectErr = fixture.m.ProjectProtocolReply(joined, fixture.tenant, artifact)
			return ProtocolReplaySettlement{BindingID: binding.ID}, projectErr
		},
	)
	if err != nil || child.ThreadID != created.ThreadID || child.ReplyToID != created.MessageID ||
		child.MessageID == created.MessageID || len(communicationRowsForTest(
		t, fixture.directNoticeFixture, messageKind,
	)) != 2 {
		t.Fatalf("threaded artifact reply = %+v, %v", child, err)
	}

	if err := fixture.st.Close(); err != nil {
		t.Fatalf("close reply store before restart: %v", err)
	}
	restarted := New()
	reopened, err := engine.Open(context.Background(), store.Config{
		Engine: store.EngineSQLite, DSN: dbPath, Debug: true,
		Clock: &testClock{now: fixture.now},
	}, restarted.RegisterSchema)
	if err != nil {
		t.Fatalf("restart reply store: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	restarted.UseData(api.NewModuleData(reopened))
	restartMutationCalled := false
	restartedGuard, err := restarted.ApplyProtocolReplay(
		context.Background(), fixture.tenant, claim,
		func(context.Context) (ProtocolReplaySettlement, error) {
			restartMutationCalled = true
			return ProtocolReplaySettlement{}, errors.New("restart replay must not mutate")
		},
	)
	if err != nil || !restartedGuard.Replayed || restartMutationCalled {
		t.Fatalf("reply restart guard = %+v, called=%v, err=%v",
			restartedGuard, restartMutationCalled, err)
	}
	restartedReply, err := restarted.GetProtocolReply(
		context.Background(), fixture.tenant, command.Ref(),
	)
	if err != nil || restartedReply.MessageID != created.MessageID ||
		restartedReply.DeliveryID != created.DeliveryID || restartedReply.ThreadID != created.ThreadID ||
		!restartedReply.Replayed {
		t.Fatalf("reply after restart = %+v, err=%v", restartedReply, err)
	}
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
	beforeMessages := len(communicationRowsForTest(t, fixture.directNoticeFixture, messageKind))
	_, err := fixture.m.ApplyProtocolReplay(
		context.Background(), fixture.tenant, claim,
		func(joined context.Context) (ProtocolReplaySettlement, error) {
			if _, projectErr := fixture.m.ProjectProtocolReply(joined, fixture.tenant, command); projectErr != nil {
				return ProtocolReplaySettlement{}, projectErr
			}
			return ProtocolReplaySettlement{}, errors.New("force rollback")
		},
	)
	if err == nil || len(communicationRowsForTest(t, fixture.directNoticeFixture, messageKind)) != beforeMessages ||
		len(communicationRowsForTest(t, fixture.directNoticeFixture, protocolReplayGuardKind)) != 0 {
		t.Fatalf("rollback leaked Message/guard: err=%v", err)
	}
	var retried ProtocolReplyResult
	_, err = fixture.m.ApplyProtocolReplay(
		context.Background(), fixture.tenant, claim,
		func(joined context.Context) (ProtocolReplaySettlement, error) {
			var projectErr error
			retried, projectErr = fixture.m.ProjectProtocolReply(joined, fixture.tenant, command)
			return ProtocolReplaySettlement{BindingID: binding.ID}, projectErr
		},
	)
	if err != nil || retried.MessageID.IsZero() || retried.Replayed ||
		len(communicationRowsForTest(t, fixture.directNoticeFixture, messageKind)) != beforeMessages+1 {
		t.Fatalf("retry after rollback = %+v, %v", retried, err)
	}
}

func TestProtocolReplyUsesExactOutboundCarrierThread(t *testing.T) {
	t.Parallel()

	fixture := newWorkflowCommunicationFixture(t, false)
	makeProtocolInterruptRecipientWriter(t, fixture)
	parent, err := fixture.m.SendWorkflowWorkTask(context.Background(), fixture.tenant, WorkflowWorkTaskCommand{
		Actor: fixture.actor, WorkItemID: fixture.workID, ChannelID: fixture.channel.ID,
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
	var reply ProtocolReplyResult
	_, err = fixture.m.ApplyProtocolReplay(
		context.Background(), fixture.tenant, ProtocolReplayClaim{
			WorkspaceID: fixture.workspace, Protocol: BindingProtocolA2A,
			PeerAuthority: binding.PeerAuthority, Kind: ProtocolReplayMessageID,
			ReplayID: command.MessageID, ExpiresAt: fixture.now.Add(5 * time.Minute),
			ExpectedBindingID: binding.ID,
		}, func(joined context.Context) (ProtocolReplaySettlement, error) {
			var projectErr error
			reply, projectErr = fixture.m.ProjectProtocolReply(joined, fixture.tenant, command)
			return ProtocolReplaySettlement{BindingID: binding.ID}, projectErr
		},
	)
	if err != nil {
		t.Fatalf("project carrier reply: %v", err)
	}
	if reply.ThreadID != parent.ThreadID || reply.ReplyToID != parent.MessageID ||
		reply.MessageID == parent.MessageID {
		t.Fatalf("carrier reply = %+v, parent = %+v", reply, parent)
	}
	message, err := messageFromRecord(
		workflowCommunicationRecord(t, fixture, messageKind, reply.MessageID), 0,
	)
	if err != nil || message.ThreadID != parent.ThreadID || message.ReplyToID != parent.MessageID ||
		message.Kind != MessageNotice || message.AckPolicy != AckPolicyNone {
		t.Fatalf("stored carrier reply = %+v, err=%v", message, err)
	}
}
