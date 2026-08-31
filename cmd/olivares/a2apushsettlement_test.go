// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"strings"
	"testing"
	"time"

	a2a "github.com/olivaresai/olivares/connectors/a2a"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions"
)

type recordingA2APushStore struct {
	binding     sessions.ProtocolBinding
	query       sessions.ProtocolBindingQuery
	observation sessions.ProtocolBindingObservation
	tenant      model.TenantID
	interrupt   sessions.ProtocolInterruptCommand
	replayClaim sessions.ProtocolReplayClaim
	reply       sessions.ProtocolReplyCommand
	replyResult sessions.ProtocolReplyResult
	replayed    bool
	replyCalls  int
	reloadCalls int
}

func (s *recordingA2APushStore) ApplyProtocolReplay(
	ctx context.Context,
	_ model.TenantID,
	claim sessions.ProtocolReplayClaim,
	mutation sessions.ProtocolReplayMutation,
) (sessions.ProtocolReplayResult, error) {
	s.replayClaim = claim
	if s.replayed {
		return sessions.ProtocolReplayResult{
			Guard: sessions.ProtocolReplayGuard{BindingID: s.binding.ID}, Replayed: true,
		}, nil
	}
	settlement, err := mutation(ctx)
	if err != nil {
		return sessions.ProtocolReplayResult{}, err
	}
	return sessions.ProtocolReplayResult{Guard: sessions.ProtocolReplayGuard{
		Protocol: claim.Protocol, PeerAuthority: claim.PeerAuthority,
		ReplayKind: claim.Kind, ExpiresAt: claim.ExpiresAt, BindingID: settlement.BindingID,
	}}, nil
}

func (s *recordingA2APushStore) ListProtocolBindings(
	_ context.Context,
	tenant model.TenantID,
	query sessions.ProtocolBindingQuery,
) (sessions.ProtocolBindingPage, error) {
	s.tenant, s.query = tenant, query
	return sessions.ProtocolBindingPage{Items: []sessions.ProtocolBinding{s.binding}}, nil
}

func (s *recordingA2APushStore) GetProtocolBinding(
	_ context.Context,
	_ model.TenantID,
	ref sessions.ProtocolBindingRef,
) (sessions.ProtocolBinding, error) {
	if ref.ID != s.binding.ID {
		return sessions.ProtocolBinding{}, sessions.ErrProtocolBindingNotFound
	}
	return s.binding, nil
}

func (s *recordingA2APushStore) ProjectProtocolReply(
	_ context.Context,
	_ model.TenantID,
	command sessions.ProtocolReplyCommand,
) (sessions.ProtocolReplyResult, error) {
	s.reply = command
	s.replyCalls++
	if s.replyResult.MessageID.IsZero() {
		s.replyResult = sessions.ProtocolReplyResult{
			BindingID: command.BindingID, Generation: command.Generation,
			MessageID: model.NewID(), DeliveryID: model.NewID(), ThreadID: model.NewID(),
		}
	}
	return s.replyResult, nil
}

func (s *recordingA2APushStore) GetProtocolReply(
	_ context.Context,
	_ model.TenantID,
	_ sessions.ProtocolReplyRef,
) (sessions.ProtocolReplyResult, error) {
	s.reloadCalls++
	return s.replyResult, nil
}

func (s *recordingA2APushStore) ObserveProtocolBinding(
	_ context.Context,
	tenant model.TenantID,
	observation sessions.ProtocolBindingObservation,
) (sessions.ProtocolBinding, error) {
	s.tenant, s.observation = tenant, observation
	s.binding.Version++
	return s.binding, nil
}

func (s *recordingA2APushStore) RecordProtocolInterrupt(
	_ context.Context,
	_ model.TenantID,
	command sessions.ProtocolInterruptCommand,
) (sessions.ProtocolInterruptResult, error) {
	s.interrupt = command
	items := make([]sessions.ProtocolInterruptMessage, 0, len(command.Requests))
	for _, request := range command.Requests {
		items = append(items, sessions.ProtocolInterruptMessage{
			KeyDigest: request.KeyDigest, MessageID: model.NewID(), DeliveryID: model.NewID(),
		})
	}
	return sessions.ProtocolInterruptResult{
		BindingID: command.BindingID, Generation: command.Generation, Messages: items,
	}, nil
}

func (*recordingA2APushStore) PrepareProtocolInputResponses(
	context.Context,
	model.TenantID,
	sessions.ProtocolInputResponseCommand,
) (sessions.ProtocolInputResponseResult, error) {
	return sessions.ProtocolInputResponseResult{}, nil
}

func TestA2APushSettlementPersistsVerifiedCurrentGeneration(t *testing.T) {
	tenant := model.NewTenantID()
	workspace := model.NewID()
	interruptChannel, interruptSender, interruptRecipient := model.NewID(), model.NewID(), model.NewID()
	bindingID := model.NewID()
	peer := "https://peer.example"
	store := &recordingA2APushStore{binding: sessions.ProtocolBinding{
		MutableCommunicationEntity: sessions.MutableCommunicationEntity{
			CommunicationEntity: sessions.CommunicationEntity{
				ID: bindingID, TenantID: tenant, WorkspaceID: workspace, Version: 7,
			},
		},
		Protocol: sessions.BindingProtocolA2A, PeerAuthority: peer,
		Generation: 3, ExternalKind: "task", ExternalID: "remote-task-1",
		LocalState: "active",
	}}
	settler, err := newA2APushSettlement(store, []a2aPushRouteConfig{{
		PeerAuthority: peer, Tenant: tenant.String(), WorkspaceID: workspace.String(),
		InterruptChannelID: interruptChannel.String(), InterruptSenderUserID: interruptSender.String(),
		InterruptRecipientUserID: interruptRecipient.String(),
	}}, []string{peer})
	if err != nil {
		t.Fatalf("new settlement: %v", err)
	}
	update := a2a.TaskUpdate{
		TaskID: "remote-task-1", ContextID: "context-1", State: a2a.TaskStateCompleted,
		Terminal: true, Sender: peer,
		ReplayID: "push-jti-1", ReplayExpiresAt: time.Now().UTC().Add(time.Hour),
	}
	if err := settler.Record(context.Background(), update); err != nil {
		t.Fatalf("record: %v", err)
	}
	if store.tenant != tenant || store.query.WorkspaceID != workspace ||
		store.query.Protocol != sessions.BindingProtocolA2A || store.query.PeerAuthority != peer ||
		store.query.ExternalID != update.TaskID || store.query.Limit != 200 {
		t.Fatalf("binding selector = %+v", store.query)
	}
	if store.replayClaim.Kind != sessions.ProtocolReplayJTI ||
		store.replayClaim.ReplayID != update.ReplayID ||
		store.replayClaim.WorkspaceID != workspace {
		t.Fatalf("replay claim = %+v", store.replayClaim)
	}
	got := store.observation
	if got.BindingID != bindingID || got.Generation != 3 || got.ExpectedVersion != 7 ||
		got.PeerAuthority != peer || got.ExternalID != update.TaskID ||
		got.RemoteState != string(a2a.TaskStateCompleted) || got.LocalState != "review" ||
		got.Verdict != sessions.ProtocolObservationClean || got.Code != "remote_completed" ||
		!got.Observed || !got.Terminal || len(got.DetailHash) != 32 || got.SemanticKey == "" {
		t.Fatalf("observation = %+v", got)
	}
}

func TestA2APushSettlementProjectsVerifiedReplyUnderReplayGuard(t *testing.T) {
	tenant, workspace, bindingID := model.NewTenantID(), model.NewID(), model.NewID()
	channelID, senderID, recipientID := model.NewID(), model.NewID(), model.NewID()
	peer := "https://peer.example"
	store := &recordingA2APushStore{binding: sessions.ProtocolBinding{
		MutableCommunicationEntity: sessions.MutableCommunicationEntity{
			CommunicationEntity: sessions.CommunicationEntity{
				ID: bindingID, TenantID: tenant, WorkspaceID: workspace, Version: 7,
			},
		},
		Protocol: sessions.BindingProtocolA2A, PeerAuthority: peer,
		Generation: 3, ExternalKind: "task", ExternalID: "remote-task-1",
		ContextID: "context-1", WorkItemID: model.NewID(), Terminal: true,
	}}
	settler, err := newA2APushSettlement(store, []a2aPushRouteConfig{{
		PeerAuthority: peer, Tenant: tenant.String(), WorkspaceID: workspace.String(),
		InterruptChannelID: channelID.String(), InterruptSenderUserID: senderID.String(),
		InterruptRecipientUserID: recipientID.String(),
	}}, []string{peer})
	if err != nil {
		t.Fatal(err)
	}
	reply := a2a.ReplyEvent{
		Kind: a2a.ReplyEventMessage, MessageID: "reply-message-1", ContextID: "context-1",
		Parts: []a2a.MessageResultPart{{
			Kind: "text", Text: "bounded reply", Digest: strings.Repeat("a", 64),
		}},
		Digest: strings.Repeat("b", 64), Sender: peer, ReplayID: "push-jti-reply-1",
		ReplayExpiresAt: time.Now().UTC().Add(time.Hour),
	}
	if err := settler.RecordReply(context.Background(), reply); err != nil {
		t.Fatalf("record reply: %v", err)
	}
	if store.replayClaim.Kind != sessions.ProtocolReplayJTI ||
		store.replayClaim.ReplayID != reply.ReplayID ||
		store.replayClaim.ExpectedBindingID != bindingID {
		t.Fatalf("replay claim = %+v", store.replayClaim)
	}
	command := store.reply
	if command.BindingID != bindingID || command.Generation != 3 ||
		command.Kind != sessions.ProtocolReplyMessage || command.TaskID != "remote-task-1" ||
		command.ContextID != reply.ContextID || command.MessageID != reply.MessageID ||
		command.SourceDigest != reply.Digest || command.Route.ChannelID != channelID ||
		command.Route.SenderUserID != senderID || command.Route.RecipientUserID != recipientID ||
		len(command.Parts) != 1 || command.Parts[0].Text != "bounded reply" {
		t.Fatalf("reply command = %#v", command)
	}
	if !store.observation.BindingID.IsZero() || !store.interrupt.BindingID.IsZero() {
		t.Fatalf("reply performed a lifecycle effect: observation=%+v interrupt=%+v",
			store.observation, store.interrupt)
	}
}

func TestA2AStreamSettlementProjectsMessageAndArtifactExactlyOnce(t *testing.T) {
	for _, test := range []struct {
		name       string
		reply      a2a.ReplyEvent
		replayKind sessions.ProtocolReplayKind
		kind       sessions.ProtocolReplyKind
	}{
		{
			name: "Message",
			reply: a2a.ReplyEvent{
				Kind: a2a.ReplyEventMessage, TaskID: "stream-task-1", MessageID: "stream-message-1",
				ContextID: "stream-context-1", Digest: strings.Repeat("c", 64),
				Parts: []a2a.MessageResultPart{{
					Kind: "text", Text: "stream reply", Digest: strings.Repeat("d", 64),
				}},
			},
			replayKind: sessions.ProtocolReplayMessageID,
			kind:       sessions.ProtocolReplyMessage,
		},
		{
			name: "Artifact",
			reply: a2a.ReplyEvent{
				Kind: a2a.ReplyEventArtifact, TaskID: "stream-task-1",
				ContextID: "stream-context-1", ArtifactID: "stream-artifact-1",
				Digest: strings.Repeat("e", 64), Parts: []a2a.MessageResultPart{{
					Kind: "file", Reference: "artifact:stream-result-1",
					Digest: strings.Repeat("f", 64),
				}},
			},
			replayKind: sessions.ProtocolReplayRequestID,
			kind:       sessions.ProtocolReplyArtifact,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			tenant, workspace, bindingID := model.NewTenantID(), model.NewID(), model.NewID()
			channelID, senderID, recipientID := model.NewID(), model.NewID(), model.NewID()
			peer := "https://peer.example"
			store := &recordingA2APushStore{binding: sessions.ProtocolBinding{
				MutableCommunicationEntity: sessions.MutableCommunicationEntity{
					CommunicationEntity: sessions.CommunicationEntity{
						ID: bindingID, TenantID: tenant, WorkspaceID: workspace, Version: 4,
					},
				},
				Protocol: sessions.BindingProtocolA2A, PeerAuthority: peer,
				Generation: 2, ExternalKind: "task", ExternalID: "stream-task-1",
				ContextID: "stream-context-1", WorkItemID: model.NewID(), Terminal: true,
			}}
			settler, err := newA2APushSettlement(store, []a2aPushRouteConfig{{
				PeerAuthority: peer, Tenant: tenant.String(), WorkspaceID: workspace.String(),
				InterruptChannelID: channelID.String(), InterruptSenderUserID: senderID.String(),
				InterruptRecipientUserID: recipientID.String(),
			}}, []string{peer})
			if err != nil {
				t.Fatal(err)
			}
			if err := settler.RecordStreamReply(context.Background(), peer, test.reply); err != nil {
				t.Fatalf("record stream reply: %v", err)
			}
			if store.replayClaim.Kind != test.replayKind ||
				store.replayClaim.ExpectedBindingID != bindingID ||
				store.reply.Kind != test.kind || store.replyCalls != 1 {
				t.Fatalf("stream settlement: claim=%+v command=%+v calls=%d",
					store.replayClaim, store.reply, store.replyCalls)
			}
			store.replayed = true
			if err := settler.RecordStreamReply(context.Background(), peer, test.reply); err != nil {
				t.Fatalf("exact stream replay: %v", err)
			}
			if store.replyCalls != 1 || store.reloadCalls != 1 {
				t.Fatalf("exact replay emitted again: project=%d reload=%d",
					store.replyCalls, store.reloadCalls)
			}
		})
	}
}

func TestA2APushSettlementDistinguishesExpectedCancel(t *testing.T) {
	expected := sessions.ProtocolBinding{CancelRequested: true, LocalState: "active"}
	unexpected := sessions.ProtocolBinding{LocalState: "active"}
	clean, cleanCode := a2aPushVerdict(expected, a2a.TaskStateCanceled)
	broken, brokenCode := a2aPushVerdict(unexpected, a2a.TaskStateCanceled)
	if clean != sessions.ProtocolObservationClean || cleanCode != "cancel_confirmed" {
		t.Fatalf("expected cancel = %s/%s", clean, cleanCode)
	}
	if broken != sessions.ProtocolObservationBroken || brokenCode != "unexpected_remote_cancel" {
		t.Fatalf("unexpected cancel = %s/%s", broken, brokenCode)
	}
	if got := a2aPushLocalState(expected, a2a.TaskStateCanceled); got != "canceled" {
		t.Fatalf("expected cancel local state = %q", got)
	}
	if got := a2aPushLocalState(unexpected, a2a.TaskStateCanceled); got != "blocked" {
		t.Fatalf("unexpected cancel local state = %q", got)
	}
}

func TestA2APushSettlementMaterializesVerifiedInterrupt(t *testing.T) {
	tenant, workspace, bindingID := model.NewTenantID(), model.NewID(), model.NewID()
	channelID, senderID, recipientID := model.NewID(), model.NewID(), model.NewID()
	peer := "https://peer.example"
	store := &recordingA2APushStore{binding: sessions.ProtocolBinding{
		MutableCommunicationEntity: sessions.MutableCommunicationEntity{
			CommunicationEntity: sessions.CommunicationEntity{
				ID: bindingID, TenantID: tenant, WorkspaceID: workspace, Version: 7,
			},
		},
		Protocol: sessions.BindingProtocolA2A, PeerAuthority: peer,
		Generation: 3, ExternalKind: "task", ExternalID: "remote-task-1",
		WorkItemID: model.NewID(), LocalState: "active",
	}}
	settler, err := newA2APushSettlement(store, []a2aPushRouteConfig{{
		PeerAuthority: peer, Tenant: tenant.String(), WorkspaceID: workspace.String(),
		InterruptChannelID: channelID.String(), InterruptSenderUserID: senderID.String(),
		InterruptRecipientUserID: recipientID.String(),
	}}, []string{peer})
	if err != nil {
		t.Fatal(err)
	}
	update := a2a.TaskUpdate{
		TaskID: "remote-task-1", ContextID: "context-1", State: a2a.TaskStateAuthRequired,
		Interrupt: true, Sender: peer,
		ReplayID: "push-jti-2", ReplayExpiresAt: time.Now().UTC().Add(time.Hour),
	}
	if err := settler.Record(context.Background(), update); err != nil {
		t.Fatalf("record interrupt: %v", err)
	}
	command := store.interrupt
	if command.BindingID != bindingID || command.Generation != 3 ||
		command.RemoteState != "auth_required" || command.Route.ChannelID != channelID ||
		command.Route.SenderUserID != senderID || command.Route.RecipientUserID != recipientID ||
		len(command.Requests) != 1 || len(command.Requests[0].KeyDigest) != 64 ||
		len(command.Requests[0].ContentDigest) != 64 {
		t.Fatalf("interrupt command = %#v", command)
	}
}

func TestA2APushSettlementRejectsUnallowlistedRoute(t *testing.T) {
	_, err := newA2APushSettlement(&recordingA2APushStore{}, []a2aPushRouteConfig{{
		PeerAuthority: "https://peer.example", Tenant: model.NewTenantID().String(),
		WorkspaceID:        model.NewID().String(),
		InterruptChannelID: model.NewID().String(), InterruptSenderUserID: model.NewID().String(),
		InterruptRecipientUserID: model.NewID().String(),
	}}, []string{"https://other.example"})
	if err == nil {
		t.Fatal("unallowlisted push route was accepted")
	}
}
