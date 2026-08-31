// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	a2a "github.com/olivaresai/olivares/connectors/a2a"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions"
)

type a2aPushBindingStore interface {
	sessions.ProtocolReplayStore
	sessions.ProtocolReplyCommunication
	ListProtocolBindings(context.Context, model.TenantID, sessions.ProtocolBindingQuery) (sessions.ProtocolBindingPage, error)
	GetProtocolBinding(context.Context, model.TenantID, sessions.ProtocolBindingRef) (sessions.ProtocolBinding, error)
	ObserveProtocolBinding(context.Context, model.TenantID, sessions.ProtocolBindingObservation) (sessions.ProtocolBinding, error)
	sessions.ProtocolInterruptCommunication
}

// RecordReply is called only after the connector has authenticated the push
// envelope and reduced its Message/artifactUpdate to bounded Parts. Projection
// and the provider replay guard commit in one sessions transaction.
func (s *a2aPushSettlement) RecordReply(ctx context.Context, reply a2a.ReplyEvent) error {
	if strings.TrimSpace(reply.ReplayID) == "" || reply.ReplayExpiresAt.IsZero() {
		return fmt.Errorf("a2a push: verified reply replay identity is missing")
	}
	replayed, err := s.recordReply(
		ctx, reply, sessions.ProtocolReplayJTI, reply.ReplayID, reply.ReplayExpiresAt,
	)
	if err != nil {
		return err
	}
	if replayed {
		return a2a.ErrReplay
	}
	return nil
}

// RecordStreamReply is the SSE counterpart of RecordReply. A stream has no
// authenticated notification-token JTI, so it uses the canonical provider
// Message identity, or the complete artifact-update semantic identity. Exact
// resubscribe replay is a successful no-op after the durable reply is reloaded.
func (s *a2aPushSettlement) RecordStreamReply(
	ctx context.Context,
	peerAuthority string,
	reply a2a.ReplyEvent,
) error {
	peerAuthority = strings.TrimSpace(peerAuthority)
	if peerAuthority == "" || (reply.Sender != "" && reply.Sender != peerAuthority) {
		return fmt.Errorf("a2a stream: reply peer identity is invalid")
	}
	reply.Sender = peerAuthority
	kind := sessions.ProtocolReplayMessageID
	replayID := reply.MessageID
	if reply.Kind == a2a.ReplyEventArtifact {
		kind = sessions.ProtocolReplayRequestID
		replayID = "artifact:" + reply.TaskID + ":" + reply.ArtifactID + ":" + reply.Digest
	}
	if reply.Kind != a2a.ReplyEventMessage && reply.Kind != a2a.ReplyEventArtifact {
		return fmt.Errorf("a2a stream: reply kind is invalid")
	}
	_, err := s.recordReply(
		ctx, reply, kind, replayID, time.Now().UTC().Add(24*time.Hour),
	)
	return err
}

func (s *a2aPushSettlement) recordReply(
	ctx context.Context,
	reply a2a.ReplyEvent,
	replayKind sessions.ProtocolReplayKind,
	replayID string,
	replayExpiresAt time.Time,
) (bool, error) {
	route, ok := s.routes[reply.Sender]
	if !ok {
		return false, fmt.Errorf("a2a reply: verified peer has no local route")
	}
	binding, err := s.replyBinding(ctx, route, reply)
	if err != nil {
		return false, err
	}
	command := projectA2APushReply(binding, route, reply)
	replay, err := s.store.ApplyProtocolReplay(ctx, route.tenant, sessions.ProtocolReplayClaim{
		WorkspaceID: route.workspace, Protocol: sessions.BindingProtocolA2A,
		PeerAuthority: reply.Sender, Kind: replayKind,
		ReplayID: replayID, ExpiresAt: replayExpiresAt,
		ExpectedBindingID: binding.ID,
	}, func(joinedCtx context.Context) (sessions.ProtocolReplaySettlement, error) {
		_, projectErr := s.store.ProjectProtocolReply(joinedCtx, route.tenant, command)
		return sessions.ProtocolReplaySettlement{BindingID: binding.ID}, projectErr
	})
	if err != nil {
		return false, err
	}
	if replay.Replayed {
		current, reloadErr := s.store.GetProtocolBinding(
			ctx, route.tenant, sessions.ProtocolBindingRef{ID: binding.ID},
		)
		if reloadErr == nil && current.ID == binding.ID {
			_, reloadErr = s.store.GetProtocolReply(ctx, route.tenant, command.Ref())
		}
		if reloadErr != nil {
			return false, reloadErr
		}
		return true, nil
	}
	return false, nil
}

func (s *a2aPushSettlement) replyBinding(
	ctx context.Context,
	route parsedA2APushRoute,
	reply a2a.ReplyEvent,
) (sessions.ProtocolBinding, error) {
	query := sessions.ProtocolBindingQuery{
		WorkspaceID: route.workspace, Protocol: sessions.BindingProtocolA2A,
		PeerAuthority: reply.Sender, Limit: 200,
	}
	if reply.Kind == a2a.ReplyEventArtifact {
		query.ExternalKind, query.ExternalID = "task", reply.TaskID
	} else if reply.Kind == a2a.ReplyEventMessage && reply.TaskID != "" {
		query.ExternalKind, query.ExternalID = "task", reply.TaskID
	}
	page, err := s.store.ListProtocolBindings(ctx, route.tenant, query)
	if err != nil {
		return sessions.ProtocolBinding{}, err
	}
	if page.HasMore {
		return sessions.ProtocolBinding{}, sessions.ErrProtocolBindingUnknown
	}
	var directMatches []sessions.ProtocolBinding
	var taskMatches []sessions.ProtocolBinding
	for _, binding := range page.Items {
		if binding.Protocol != sessions.BindingProtocolA2A ||
			binding.PeerAuthority != reply.Sender || binding.WorkspaceID != route.workspace ||
			binding.ContextID != reply.ContextID {
			continue
		}
		switch reply.Kind {
		case a2a.ReplyEventArtifact:
			if binding.ExternalKind == "task" && binding.ExternalID == reply.TaskID {
				taskMatches = append(taskMatches, binding)
			}
		case a2a.ReplyEventMessage:
			if reply.TaskID != "" && binding.ExternalKind == "task" && binding.ExternalID == reply.TaskID {
				taskMatches = append(taskMatches, binding)
			} else if reply.TaskID == "" && binding.ExternalKind == "message" && binding.ExternalMessageID == reply.MessageID {
				directMatches = append(directMatches, binding)
			} else if reply.TaskID == "" && binding.ExternalKind == "task" && binding.ExternalID != "" {
				taskMatches = append(taskMatches, binding)
			}
		}
	}
	matches := taskMatches
	if len(directMatches) > 0 {
		// An exact direct-Message carrier is stronger evidence than a Task in
		// the same A2A context. It cannot be combined with a Task heuristic.
		matches = directMatches
	}
	if len(matches) != 1 {
		if len(matches) == 0 {
			return sessions.ProtocolBinding{}, sessions.ErrProtocolBindingNotFound
		}
		return sessions.ProtocolBinding{}, sessions.ErrProtocolBindingConflict
	}
	return matches[0], nil
}

func projectA2APushReply(
	binding sessions.ProtocolBinding,
	route parsedA2APushRoute,
	reply a2a.ReplyEvent,
) sessions.ProtocolReplyCommand {
	kind := sessions.ProtocolReplyMessage
	if reply.Kind == a2a.ReplyEventArtifact {
		kind = sessions.ProtocolReplyArtifact
	}
	taskID := reply.TaskID
	if taskID == "" && binding.ExternalKind == "task" {
		taskID = binding.ExternalID
	}
	parts := make([]sessions.ProtocolReplyPart, 0, len(reply.Parts))
	for _, part := range reply.Parts {
		parts = append(parts, sessions.ProtocolReplyPart{
			Kind: sessions.ProtocolReplyPartKind(part.Kind), Text: part.Text,
			Reference: part.Reference, Digest: part.Digest,
		})
	}
	return sessions.ProtocolReplyCommand{
		BindingID: binding.ID, Generation: binding.Generation, Route: route.interrupt,
		PeerAuthority: binding.PeerAuthority, Kind: kind, TaskID: taskID,
		ContextID: reply.ContextID, MessageID: reply.MessageID,
		ArtifactID: reply.ArtifactID, Parts: parts, SourceDigest: reply.Digest,
	}
}

type parsedA2APushRoute struct {
	tenant    model.TenantID
	workspace model.ID
	interrupt sessions.ProtocolInterruptRoute
}

type a2aPushSettlement struct {
	store  a2aPushBindingStore
	routes map[string]parsedA2APushRoute
}

func newA2APushSettlement(
	store a2aPushBindingStore,
	configs []a2aPushRouteConfig,
	allowedIssuers []string,
) (*a2aPushSettlement, error) {
	if store == nil || len(configs) == 0 {
		return nil, fmt.Errorf("a2a push: durable routes require a binding store")
	}
	allowed := make(map[string]struct{}, len(allowedIssuers))
	for _, raw := range allowedIssuers {
		if issuer := strings.TrimSpace(raw); issuer != "" {
			allowed[issuer] = struct{}{}
		}
	}
	routes := make(map[string]parsedA2APushRoute, len(configs))
	for i, raw := range configs {
		authority := strings.TrimSpace(raw.PeerAuthority)
		if _, ok := allowed[authority]; authority == "" || !ok {
			return nil, fmt.Errorf("a2a push: route %d peer_authority is not allowlisted", i)
		}
		if _, duplicate := routes[authority]; duplicate {
			return nil, fmt.Errorf("a2a push: duplicate peer_authority %q", authority)
		}
		tenant, _, err := parseBusinessTenant("a2a push route tenant", raw.Tenant)
		if err != nil || tenant.IsZero() {
			return nil, fmt.Errorf("a2a push: route %d has an invalid tenant", i)
		}
		workspace, err := model.ParseID(strings.TrimSpace(raw.WorkspaceID))
		if err != nil || workspace.IsZero() {
			return nil, fmt.Errorf("a2a push: route %d has an invalid workspace_id", i)
		}
		interrupt, err := parseProtocolInterruptRoute(
			fmt.Sprintf("a2a push route %d", i), raw.InterruptChannelID,
			raw.InterruptSenderUserID, raw.InterruptRecipientUserID,
		)
		if err != nil {
			return nil, err
		}
		routes[authority] = parsedA2APushRoute{
			tenant: tenant, workspace: workspace, interrupt: interrupt,
		}
	}
	return &a2aPushSettlement{store: store, routes: routes}, nil
}

// Record is called only after connector-side JWT/issuer verification. It finds
// the current generation under the operator route and commits the observation
// before PushReceiver acknowledges the webhook.
func (s *a2aPushSettlement) Record(ctx context.Context, update a2a.TaskUpdate) error {
	route, ok := s.routes[update.Sender]
	if !ok {
		return fmt.Errorf("a2a push: verified peer has no local route")
	}
	if strings.TrimSpace(update.ReplayID) == "" || update.ReplayExpiresAt.IsZero() {
		return fmt.Errorf("a2a push: verified replay identity is missing")
	}
	replay, err := s.store.ApplyProtocolReplay(ctx, route.tenant, sessions.ProtocolReplayClaim{
		WorkspaceID: route.workspace, Protocol: sessions.BindingProtocolA2A,
		PeerAuthority: update.Sender, Kind: sessions.ProtocolReplayJTI,
		ReplayID: update.ReplayID, ExpiresAt: update.ReplayExpiresAt,
	}, func(joinedCtx context.Context) (sessions.ProtocolReplaySettlement, error) {
		bindingID, err := s.recordUpdate(joinedCtx, route, update)
		return sessions.ProtocolReplaySettlement{BindingID: bindingID}, err
	})
	if err != nil {
		return err
	}
	if replay.Replayed {
		return a2a.ErrReplay
	}
	return nil
}

func (s *a2aPushSettlement) recordUpdate(
	ctx context.Context,
	route parsedA2APushRoute,
	update a2a.TaskUpdate,
) (model.ID, error) {
	page, err := s.store.ListProtocolBindings(ctx, route.tenant, sessions.ProtocolBindingQuery{
		WorkspaceID: route.workspace, Protocol: sessions.BindingProtocolA2A,
		PeerAuthority: update.Sender, ExternalKind: "task", ExternalID: update.TaskID,
		Limit: 200,
	})
	if err != nil {
		return "", err
	}
	if page.HasMore || len(page.Items) == 0 {
		return "", sessions.ErrProtocolBindingNotFound
	}
	binding := page.Items[0]
	for _, candidate := range page.Items[1:] {
		if candidate.Generation > binding.Generation {
			binding = candidate
		}
	}
	verdict, code := a2aPushVerdict(binding, update.State)
	localState := a2aPushLocalState(binding, update.State)
	detail := sha256.Sum256([]byte(update.Sender + "\x00" + update.TaskID + "\x00" +
		update.ContextID + "\x00" + string(update.State)))
	semantic := sha256.Sum256([]byte("olivares.a2a.push.v1\x00" + update.Sender + "\x00" +
		update.TaskID + "\x00" + string(update.State) + "\x00" + update.ContextID))
	updated, err := s.store.ObserveProtocolBinding(ctx, route.tenant, sessions.ProtocolBindingObservation{
		BindingID: binding.ID, Generation: binding.Generation, ExpectedVersion: binding.Version,
		SemanticKey: hex.EncodeToString(semantic[:]), PeerAuthority: update.Sender,
		ExternalID: update.TaskID, ContextID: update.ContextID,
		LocalState: localState, RemoteState: string(update.State),
		RemoteRevision: a2a.ProtocolVersion, Verdict: verdict, Code: code,
		Observed: true, DetailHash: detail[:], Terminal: update.Terminal,
	})
	if err != nil {
		return "", err
	}
	remoteState := a2aPushRemoteState(update.State)
	if remoteState != "input_required" && remoteState != "auth_required" {
		return updated.ID, nil
	}
	request := protocolA2AInterruptRequest(
		updated.ID, updated.Generation, update.TaskID, update.ContextID, remoteState,
	)
	result, err := s.store.RecordProtocolInterrupt(ctx, route.tenant, sessions.ProtocolInterruptCommand{
		BindingID: updated.ID, Generation: updated.Generation, Route: route.interrupt,
		RemoteState: remoteState, Requests: []sessions.ProtocolInterruptRequestRef{request},
	})
	if err != nil {
		return "", err
	}
	if result.BindingID != updated.ID || result.Generation != updated.Generation ||
		len(result.Messages) != 1 || result.Messages[0].KeyDigest != request.KeyDigest ||
		result.Messages[0].MessageID.IsZero() || result.Messages[0].DeliveryID.IsZero() {
		return "", fmt.Errorf("a2a push: interrupt communication returned mismatched durable evidence")
	}
	return updated.ID, nil
}

func a2aPushRemoteState(state a2a.TaskState) string {
	switch state {
	case a2a.TaskStateInputReq:
		return "input_required"
	case a2a.TaskStateAuthRequired:
		return "auth_required"
	default:
		return string(state)
	}
}

func a2aPushLocalState(binding sessions.ProtocolBinding, state a2a.TaskState) string {
	switch state {
	case a2a.TaskStateSubmitted, a2a.TaskStateWorking:
		return "active"
	case a2a.TaskStateInputReq, a2a.TaskStateAuthRequired,
		a2a.TaskStateFailed, a2a.TaskStateRejected:
		return "blocked"
	case a2a.TaskStateCompleted:
		return "review"
	case a2a.TaskStateCanceled:
		if binding.CancelRequested {
			return "canceled"
		}
		return "blocked"
	default:
		// An invalid/unknown remote state is recorded as BROKEN evidence but
		// cannot drive a local WorkItem transition.
		return binding.LocalState
	}
}

func a2aPushVerdict(binding sessions.ProtocolBinding, state a2a.TaskState) (sessions.ProtocolObservationVerdict, string) {
	switch state {
	case a2a.TaskStateSubmitted, a2a.TaskStateWorking:
		return sessions.ProtocolObservationClean, "remote_active"
	case a2a.TaskStateInputReq:
		return sessions.ProtocolObservationClean, "input_required"
	case a2a.TaskStateAuthRequired:
		return sessions.ProtocolObservationClean, "auth_required"
	case a2a.TaskStateCompleted:
		return sessions.ProtocolObservationClean, "remote_completed"
	case a2a.TaskStateCanceled:
		if binding.CancelRequested {
			return sessions.ProtocolObservationClean, "cancel_confirmed"
		}
		return sessions.ProtocolObservationBroken, "unexpected_remote_cancel"
	case a2a.TaskStateFailed:
		return sessions.ProtocolObservationBroken, "remote_failed"
	case a2a.TaskStateRejected:
		return sessions.ProtocolObservationBroken, "remote_rejected"
	default:
		return sessions.ProtocolObservationBroken, "invalid_remote_state"
	}
}
