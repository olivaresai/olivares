// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const (
	protocolInterruptPending   = "pending"
	protocolInterruptPreparing = "preparing"
	protocolInterruptResponded = "responded"
	maxProtocolInterruptRefs   = 64
)

// ProtocolInterruptCommunication is the private K5 bridge from authenticated
// A2A/MCP observations into K3 Message/Ack. The route is operator-owned and is
// sealed into every link; no peer/request field may select a local User.
type ProtocolInterruptCommunication interface {
	RecordProtocolInterrupt(context.Context, model.TenantID, ProtocolInterruptCommand) (ProtocolInterruptResult, error)
	PrepareProtocolInputResponses(context.Context, model.TenantID, ProtocolInputResponseCommand) (ProtocolInputResponseResult, error)
}

var _ ProtocolInterruptCommunication = (*Module)(nil)

type ProtocolInterruptRoute struct {
	ChannelID       model.ID `json:"channel_id"`
	SenderUserID    model.ID `json:"sender_user_id"`
	RecipientUserID model.ID `json:"recipient_user_id"`
}

// ProtocolInterruptRequestRef carries only connector-computed SHA-256
// commitments. KeyDigest commits to the canonical external request identifier;
// ContentDigest commits to its canonical request value. Neither raw value is
// accepted by this boundary or persisted by sessions.
type ProtocolInterruptRequestRef struct {
	KeyDigest     string `json:"key_digest"`
	ContentDigest string `json:"content_digest"`
}

type ProtocolInterruptCommand struct {
	BindingID   model.ID                      `json:"binding_id"`
	Generation  int64                         `json:"generation"`
	Route       ProtocolInterruptRoute        `json:"route"`
	RemoteState string                        `json:"remote_state"`
	Requests    []ProtocolInterruptRequestRef `json:"requests"`
}

type ProtocolInterruptMessage struct {
	KeyDigest  string   `json:"key_digest"`
	MessageID  model.ID `json:"message_id"`
	DeliveryID model.ID `json:"delivery_id"`
	Replayed   bool     `json:"replayed"`
}

type ProtocolInterruptResult struct {
	BindingID  model.ID                   `json:"binding_id"`
	Generation int64                      `json:"generation"`
	Messages   []ProtocolInterruptMessage `json:"messages"`
}

type ProtocolInputResponseRef struct {
	KeyDigest      string `json:"key_digest"`
	ResponseDigest string `json:"response_digest"`
}

type ProtocolInputResponseCommand struct {
	BindingID    model.ID                   `json:"binding_id"`
	Generation   int64                      `json:"generation"`
	Route        ProtocolInterruptRoute     `json:"route"`
	OperationID  string                     `json:"operation_id"`
	EffectDigest string                     `json:"effect_digest"`
	Responses    []ProtocolInputResponseRef `json:"responses"`
}

type ProtocolInputResponseEvidence struct {
	KeyDigest         string   `json:"key_digest"`
	AckID             model.ID `json:"ack_id"`
	ResponseMessageID model.ID `json:"response_message_id"`
	Replayed          bool     `json:"replayed"`
}

type ProtocolInputResponseResult struct {
	BindingID  model.ID                        `json:"binding_id"`
	Generation int64                           `json:"generation"`
	Responses  []ProtocolInputResponseEvidence `json:"responses"`
}

type protocolInterruptLink struct {
	ID                 model.ID
	Version            int64
	WorkspaceID        model.ID
	BindingID          model.ID
	BindingGeneration  int64
	WorkItemID         model.ID
	Protocol           BindingProtocol
	RemoteState        string
	KeyHash            []byte
	ContentHash        []byte
	RouteHash          []byte
	Route              ProtocolInterruptRoute
	MessageID          model.ID
	DeliveryID         model.ID
	State              string
	ResponseHash       []byte
	OperationHash      []byte
	EffectHash         []byte
	AckID              model.ID
	ResponseMessageID  model.ID
	ResponseDeliveryID model.ID
}

func (r ProtocolInterruptRoute) normalize() (ProtocolInterruptRoute, []byte, error) {
	for _, id := range []model.ID{r.ChannelID, r.SenderUserID, r.RecipientUserID} {
		parsed, err := model.ParseID(id.String())
		if err != nil || parsed.IsZero() || parsed != id {
			return ProtocolInterruptRoute{}, nil, communicationError(
				ErrInvalidCommunicationModel, "protocol interrupt route is incomplete",
			)
		}
	}
	if r.SenderUserID == r.RecipientUserID {
		return ProtocolInterruptRoute{}, nil, communicationError(
			ErrInvalidCommunicationModel, "protocol interrupt route requires distinct sender and recipient Users",
		)
	}
	raw, err := canonicalJSON(r)
	if err != nil {
		return ProtocolInterruptRoute{}, nil, err
	}
	digest := sha256.Sum256(raw)
	return r, digest[:], nil
}

func parseProtocolInterruptDigest(value, name string) ([]byte, error) {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return nil, communicationError(ErrInvalidCommunicationModel, "%s is not canonical SHA-256", name)
	}
	raw, err := hex.DecodeString(value)
	if err != nil || len(raw) != sha256.Size || hex.EncodeToString(raw) != value {
		return nil, communicationError(ErrInvalidCommunicationModel, "%s is not canonical SHA-256", name)
	}
	return raw, nil
}

func protocolInterruptOpaqueHash(domain, value string) []byte {
	digest := sha256.Sum256([]byte(domain + "\x00" + value))
	return digest[:]
}

func normalizeProtocolInterruptRequests(
	requests []ProtocolInterruptRequestRef,
) ([]ProtocolInterruptRequestRef, error) {
	if len(requests) < 1 || len(requests) > maxProtocolInterruptRefs {
		return nil, communicationError(
			ErrInvalidCommunicationModel, "protocol interrupt request set is empty or exceeds its bound",
		)
	}
	result := append([]ProtocolInterruptRequestRef(nil), requests...)
	sort.Slice(result, func(i, j int) bool { return result[i].KeyDigest < result[j].KeyDigest })
	for i := range result {
		if _, err := parseProtocolInterruptDigest(result[i].KeyDigest, "request key digest"); err != nil {
			return nil, err
		}
		if _, err := parseProtocolInterruptDigest(result[i].ContentDigest, "request content digest"); err != nil {
			return nil, err
		}
		if i > 0 && result[i-1].KeyDigest == result[i].KeyDigest {
			return nil, communicationError(ErrInvalidCommunicationModel, "duplicate protocol interrupt request key")
		}
	}
	return result, nil
}

func normalizeProtocolInputResponses(
	responses []ProtocolInputResponseRef,
) ([]ProtocolInputResponseRef, error) {
	if len(responses) < 1 || len(responses) > maxProtocolInterruptRefs {
		return nil, communicationError(
			ErrInvalidCommunicationModel, "protocol input response set is empty or exceeds its bound",
		)
	}
	result := append([]ProtocolInputResponseRef(nil), responses...)
	sort.Slice(result, func(i, j int) bool { return result[i].KeyDigest < result[j].KeyDigest })
	for i := range result {
		if _, err := parseProtocolInterruptDigest(result[i].KeyDigest, "response key digest"); err != nil {
			return nil, err
		}
		if _, err := parseProtocolInterruptDigest(result[i].ResponseDigest, "response content digest"); err != nil {
			return nil, err
		}
		if i > 0 && result[i-1].KeyDigest == result[i].KeyDigest {
			return nil, communicationError(ErrInvalidCommunicationModel, "duplicate protocol input response key")
		}
	}
	return result, nil
}

func (m *Module) protocolInterruptBinding(
	ctx context.Context,
	tenant model.TenantID,
	bindingID model.ID,
	generation int64,
) (ProtocolBinding, error) {
	if tenant.IsZero() || tenant.IsSystem() || !validCanonicalCommunicationID(bindingID) || generation < 1 {
		return ProtocolBinding{}, communicationError(
			ErrInvalidCommunicationModel, "protocol interrupt binding selector is invalid",
		)
	}
	binding, err := m.GetProtocolBinding(ctx, tenant, ProtocolBindingRef{ID: bindingID})
	if err != nil {
		return ProtocolBinding{}, err
	}
	if binding.ID != bindingID || binding.Generation != generation || binding.WorkItemID.IsZero() ||
		binding.WorkspaceID.IsZero() || binding.Terminal ||
		(binding.Protocol != BindingProtocolA2A && binding.Protocol != BindingProtocolMCP) {
		return ProtocolBinding{}, communicationError(
			ErrInvalidCommunicationTransition, "protocol interrupt binding is not current and actionable",
		)
	}
	return binding, nil
}

// RecordProtocolInterrupt materializes one durable actionable Message per
// request commitment. Message publication happens first and is idempotent; if a
// process stops before the compact link row is written, the exact retry replays
// the Message receipt and completes the link without creating a duplicate.
func (m *Module) RecordProtocolInterrupt(
	ctx context.Context,
	tenant model.TenantID,
	cmd ProtocolInterruptCommand,
) (ProtocolInterruptResult, error) {
	ctx, cancel := workflowCommunicationContext(ctx)
	defer cancel()
	route, routeHash, err := cmd.Route.normalize()
	if err != nil {
		return ProtocolInterruptResult{}, err
	}
	cmd.RemoteState = strings.TrimSpace(cmd.RemoteState)
	if cmd.RemoteState != "input_required" && cmd.RemoteState != "auth_required" {
		return ProtocolInterruptResult{}, communicationError(
			ErrInvalidCommunicationModel, "protocol interrupt state is unsupported",
		)
	}
	requests, err := normalizeProtocolInterruptRequests(cmd.Requests)
	if err != nil {
		return ProtocolInterruptResult{}, err
	}
	binding, err := m.protocolInterruptBinding(ctx, tenant, cmd.BindingID, cmd.Generation)
	if err != nil {
		return ProtocolInterruptResult{}, err
	}
	if cmd.RemoteState == "auth_required" && binding.Protocol != BindingProtocolA2A {
		return ProtocolInterruptResult{}, communicationError(
			ErrInvalidCommunicationTransition, "auth_required is not an MCP Task state",
		)
	}
	actor := workflowProtocolUserActor(route.SenderUserID)
	recipient := RecipientRef{Kind: RecipientUser, Ref: route.RecipientUserID.String()}
	result := ProtocolInterruptResult{BindingID: binding.ID, Generation: binding.Generation}
	for _, request := range requests {
		content := protocolInterruptMessageContent(binding, cmd.RemoteState, request)
		semantic := protocolInterruptSemantic(binding.ID, binding.Generation, request.KeyDigest)
		published, publishErr := m.SendWorkflowWorkTask(ctx, tenant, WorkflowWorkTaskCommand{
			Actor: actor, WorkItemID: binding.WorkItemID, ChannelID: route.ChannelID,
			Recipient: recipient, Content: content, Urgency: UrgencyHigh,
			IdempotencyKey: semantic,
		})
		if publishErr != nil {
			return ProtocolInterruptResult{}, workflowCommunicationError("publish protocol interrupt", publishErr)
		}
		link, replayed, persistErr := m.persistProtocolInterruptLink(
			ctx, tenant, binding, cmd.RemoteState, route, routeHash, request, published,
		)
		if persistErr != nil {
			return ProtocolInterruptResult{}, persistErr
		}
		result.Messages = append(result.Messages, ProtocolInterruptMessage{
			KeyDigest: request.KeyDigest, MessageID: link.MessageID,
			DeliveryID: link.DeliveryID, Replayed: published.Replayed || replayed,
		})
	}
	return result, nil
}

func workflowProtocolUserActor(userID model.ID) WorkflowCommunicationActor {
	return WorkflowCommunicationActor{AuditKind: model.ActorUser, AuditRef: "user:" + userID.String()}
}

func protocolInterruptSemantic(bindingID model.ID, generation int64, keyDigest string) string {
	return fmt.Sprintf("protocol-interrupt:%s:%d:%s", bindingID, generation, keyDigest)
}

func protocolInterruptMessageContent(
	binding ProtocolBinding,
	remoteState string,
	request ProtocolInterruptRequestRef,
) MessageContent {
	subject, code := "Remote task input required", "protocol_input_required"
	if remoteState == "auth_required" {
		subject, code = "Remote task authorization required", "protocol_auth_required"
	}
	return MessageContent{Subject: subject, Blocks: []MessageContentBlock{
		{Type: ContentBlockStatus, Code: code},
		{Type: ContentBlockReference, Reference: &ContentReference{
			Kind: "protocol_binding", Ref: binding.ID.String(),
		}},
		{Type: ContentBlockReference, Reference: &ContentReference{
			Kind: "protocol_request", Ref: request.KeyDigest, Hash: request.ContentDigest,
		}},
	}}
}

func (m *Module) persistProtocolInterruptLink(
	ctx context.Context,
	tenant model.TenantID,
	binding ProtocolBinding,
	remoteState string,
	route ProtocolInterruptRoute,
	routeHash []byte,
	request ProtocolInterruptRequestRef,
	published WorkflowWorkTaskResult,
) (protocolInterruptLink, bool, error) {
	id, err := workflowCommunicationStableID(
		protocolInterruptSemantic(binding.ID, binding.Generation, request.KeyDigest),
		"protocol.interrupt.link",
	)
	if err != nil {
		return protocolInterruptLink{}, false, err
	}
	keyHash, _ := parseProtocolInterruptDigest(request.KeyDigest, "request key digest")
	contentHash, _ := parseProtocolInterruptDigest(request.ContentDigest, "request content digest")
	want := protocolInterruptLink{
		ID: id, Version: 1, WorkspaceID: binding.WorkspaceID,
		BindingID: binding.ID, BindingGeneration: binding.Generation,
		WorkItemID: binding.WorkItemID, Protocol: binding.Protocol, RemoteState: remoteState,
		KeyHash: keyHash, ContentHash: contentHash, RouteHash: append([]byte(nil), routeHash...),
		Route: route, MessageID: published.MessageID, DeliveryID: published.DeliveryID,
		State: protocolInterruptPending,
	}
	var result protocolInterruptLink
	replayed := false
	err = m.communicationData(tenant).Mutate(ctx, func(sc store.Scope) error {
		repo, err := sc.Ext(protocolInterruptKind)
		if err != nil {
			return err
		}
		row, getErr := repo.Get(ctx, id)
		if getErr == nil {
			current, decodeErr := protocolInterruptLinkFromRecord(row)
			if decodeErr != nil || !sameProtocolInterruptIdentity(current, want) {
				return communicationError(
					ErrCommunicationEvidenceUnknown, "protocol interrupt replay link changed identity",
				)
			}
			result, replayed = current, true
			return nil
		}
		if !errors.Is(getErr, store.ErrNotFound) {
			return getErr
		}
		created, err := repo.CreateWithID(ctx, id, protocolInterruptLinkRecord(want))
		if err != nil {
			return err
		}
		result, err = protocolInterruptLinkFromRecord(created)
		return err
	})
	if errors.Is(err, store.ErrConflict) {
		current, reloadErr := m.readProtocolInterruptLink(ctx, tenant, id)
		if reloadErr != nil {
			return protocolInterruptLink{}, false, reloadErr
		}
		if !sameProtocolInterruptIdentity(current, want) {
			return protocolInterruptLink{}, false, communicationError(
				ErrCommunicationEvidenceUnknown, "protocol interrupt concurrent link changed identity",
			)
		}
		return current, true, nil
	}
	return result, replayed, err
}

// PrepareProtocolInputResponses records the exact recipient Ack and a reverse
// local response Message for every previously registered request key. Both are
// idempotent K3 commands. The method returns only after every pair and compact
// link update are durable, so its caller can place it immediately before the
// remote forward. A stop after either K3 command is recovered by an exact retry.
func (m *Module) PrepareProtocolInputResponses(
	ctx context.Context,
	tenant model.TenantID,
	cmd ProtocolInputResponseCommand,
) (ProtocolInputResponseResult, error) {
	ctx, cancel := workflowCommunicationContext(ctx)
	defer cancel()
	route, routeHash, err := cmd.Route.normalize()
	if err != nil {
		return ProtocolInputResponseResult{}, err
	}
	cmd.OperationID, cmd.EffectDigest = strings.TrimSpace(cmd.OperationID), strings.TrimSpace(cmd.EffectDigest)
	if !validateOpaqueRef(cmd.OperationID) || !validateOpaqueRef(cmd.EffectDigest) {
		return ProtocolInputResponseResult{}, communicationError(
			ErrInvalidCommunicationModel, "protocol input response operation binding is invalid",
		)
	}
	responses, err := normalizeProtocolInputResponses(cmd.Responses)
	if err != nil {
		return ProtocolInputResponseResult{}, err
	}
	binding, err := m.protocolInterruptBinding(ctx, tenant, cmd.BindingID, cmd.Generation)
	if err != nil {
		return ProtocolInputResponseResult{}, err
	}
	if binding.Protocol != BindingProtocolMCP {
		return ProtocolInputResponseResult{}, communicationError(
			ErrInvalidCommunicationTransition, "protocol input responses require an MCP binding",
		)
	}
	result := ProtocolInputResponseResult{BindingID: binding.ID, Generation: binding.Generation}
	operationHash := protocolInterruptOpaqueHash("protocol-input-response-operation-v1", cmd.OperationID)
	effectHash := protocolInterruptOpaqueHash("protocol-input-response-effect-v1", cmd.EffectDigest)
	for _, response := range responses {
		link, loadErr := m.loadProtocolInterruptLink(
			ctx, tenant, binding, route, routeHash, response.KeyDigest,
		)
		if loadErr != nil {
			return ProtocolInputResponseResult{}, loadErr
		}
		responseHash, _ := parseProtocolInterruptDigest(response.ResponseDigest, "response content digest")
		if link.State == protocolInterruptResponded {
			if !sameProtocolInputResponseBinding(link, responseHash, operationHash, effectHash) ||
				link.AckID.IsZero() ||
				link.ResponseMessageID.IsZero() || link.ResponseDeliveryID.IsZero() {
				return ProtocolInputResponseResult{}, communicationError(
					ErrInvalidCommunicationTransition, "protocol input request already has another response",
				)
			}
			result.Responses = append(result.Responses, ProtocolInputResponseEvidence{
				KeyDigest: response.KeyDigest, AckID: link.AckID,
				ResponseMessageID: link.ResponseMessageID, Replayed: true,
			})
			continue
		}
		link, claimErr := m.claimProtocolInputResponse(
			ctx, tenant, link, routeHash, responseHash, operationHash, effectHash,
		)
		if claimErr != nil {
			return ProtocolInputResponseResult{}, claimErr
		}
		semantic := protocolInputResponseSemantic(
			binding.ID, binding.Generation, link.KeyHash, responseHash, operationHash, effectHash,
		)
		ack, ackErr := m.AcknowledgeWorkflowMessage(ctx, tenant, WorkflowMessageAckCommand{
			Actor:      workflowProtocolUserActor(route.RecipientUserID),
			WorkItemID: binding.WorkItemID, ChannelID: route.ChannelID,
			DeliveryID: link.DeliveryID, ExpectedVersion: 1,
			IdempotencyKey: semantic + ":ack",
		})
		if ackErr != nil {
			return ProtocolInputResponseResult{}, workflowCommunicationError("ack protocol input request", ackErr)
		}
		published, publishErr := m.SendWorkflowWorkTask(ctx, tenant, WorkflowWorkTaskCommand{
			Actor:      workflowProtocolUserActor(route.RecipientUserID),
			WorkItemID: binding.WorkItemID, ChannelID: route.ChannelID,
			Recipient: RecipientRef{Kind: RecipientUser, Ref: route.SenderUserID.String()},
			Content: protocolInputResponseMessageContent(
				binding, response, operationHash, effectHash,
			),
			Urgency:        UrgencyNormal,
			IdempotencyKey: semantic + ":message",
		})
		if publishErr != nil {
			return ProtocolInputResponseResult{}, workflowCommunicationError("publish protocol input response", publishErr)
		}
		updated, updateErr := m.completeProtocolInterruptLink(
			ctx, tenant, link, routeHash, responseHash, operationHash, effectHash, ack, published,
		)
		if updateErr != nil {
			return ProtocolInputResponseResult{}, updateErr
		}
		result.Responses = append(result.Responses, ProtocolInputResponseEvidence{
			KeyDigest: response.KeyDigest, AckID: updated.AckID,
			ResponseMessageID: updated.ResponseMessageID,
			Replayed:          ack.Replayed || published.Replayed,
		})
	}
	return result, nil
}

func protocolInputResponseMessageContent(
	binding ProtocolBinding,
	response ProtocolInputResponseRef,
	operationHash []byte,
	effectHash []byte,
) MessageContent {
	return MessageContent{Subject: "Remote task input response prepared", Blocks: []MessageContentBlock{
		{Type: ContentBlockStatus, Code: "protocol_input_response"},
		{Type: ContentBlockReference, Reference: &ContentReference{
			Kind: "protocol_binding", Ref: binding.ID.String(),
		}},
		{Type: ContentBlockReference, Reference: &ContentReference{
			Kind: "protocol_response", Ref: response.KeyDigest, Hash: response.ResponseDigest,
		}},
		{Type: ContentBlockReference, Reference: &ContentReference{
			Kind: "protocol_operation", Ref: hex.EncodeToString(operationHash), Hash: hex.EncodeToString(effectHash),
		}},
	}}
}

func protocolInputResponseSemantic(
	bindingID model.ID,
	generation int64,
	keyHash, responseHash, operationHash, effectHash []byte,
) string {
	material := strings.Join([]string{
		"protocol-input-response-v1", bindingID.String(), fmt.Sprintf("%d", generation),
		hex.EncodeToString(keyHash), hex.EncodeToString(responseHash),
		hex.EncodeToString(operationHash), hex.EncodeToString(effectHash),
	}, "\x00")
	digest := sha256.Sum256([]byte(material))
	return "protocol-input-response:" + hex.EncodeToString(digest[:])
}

func sameProtocolInputResponseBinding(
	link protocolInterruptLink,
	responseHash, operationHash, effectHash []byte,
) bool {
	return bytes.Equal(link.ResponseHash, responseHash) &&
		bytes.Equal(link.OperationHash, operationHash) &&
		bytes.Equal(link.EffectHash, effectHash)
}

// claimProtocolInputResponse atomically seals the first authorized response
// operation into the request-key row before either K3 effect is emitted. A
// process stop after this claim is harmless: only the exact operation/effect/
// response tuple can resume and its Ack and Message commands are deterministic.
func (m *Module) claimProtocolInputResponse(
	ctx context.Context,
	tenant model.TenantID,
	before protocolInterruptLink,
	routeHash, responseHash, operationHash, effectHash []byte,
) (protocolInterruptLink, error) {
	var result protocolInterruptLink
	err := m.communicationData(tenant).Mutate(ctx, func(sc store.Scope) error {
		repo, err := sc.Ext(protocolInterruptKind)
		if err != nil {
			return err
		}
		row, err := repo.Get(ctx, before.ID)
		if err != nil {
			return err
		}
		current, err := protocolInterruptLinkFromRecord(row)
		if err != nil || !sameProtocolInterruptIdentity(current, before) ||
			!bytes.Equal(current.RouteHash, routeHash) {
			return communicationError(
				ErrCommunicationEvidenceUnknown, "protocol interrupt changed before response claim",
			)
		}
		switch current.State {
		case protocolInterruptPending:
			row[colInterruptState] = protocolInterruptPreparing
			row[colInterruptResponseHash] = append([]byte(nil), responseHash...)
			row[colInterruptOperationHash] = append([]byte(nil), operationHash...)
			row[colInterruptEffectHash] = append([]byte(nil), effectHash...)
			updated, updateErr := repo.Update(ctx, row)
			if updateErr != nil {
				return updateErr
			}
			result, err = protocolInterruptLinkFromRecord(updated)
			return err
		case protocolInterruptPreparing, protocolInterruptResponded:
			if !sameProtocolInputResponseBinding(current, responseHash, operationHash, effectHash) {
				return communicationError(
					ErrInvalidCommunicationTransition,
					"protocol input request is claimed by another response operation",
				)
			}
			result = current
			return nil
		default:
			return communicationError(
				ErrCommunicationEvidenceUnknown, "protocol interrupt response state is malformed",
			)
		}
	})
	if errors.Is(err, store.ErrConflict) {
		current, reloadErr := m.readProtocolInterruptLink(ctx, tenant, before.ID)
		if reloadErr != nil {
			return protocolInterruptLink{}, reloadErr
		}
		if !sameProtocolInterruptIdentity(current, before) ||
			!bytes.Equal(current.RouteHash, routeHash) {
			return protocolInterruptLink{}, communicationError(
				ErrCommunicationEvidenceUnknown, "protocol interrupt changed during response claim",
			)
		}
		if (current.State == protocolInterruptPreparing || current.State == protocolInterruptResponded) &&
			sameProtocolInputResponseBinding(current, responseHash, operationHash, effectHash) {
			return current, nil
		}
		if current.State == protocolInterruptPreparing || current.State == protocolInterruptResponded {
			return protocolInterruptLink{}, communicationError(
				ErrInvalidCommunicationTransition,
				"protocol input request is claimed by another response operation",
			)
		}
		return protocolInterruptLink{}, communicationError(
			ErrCommunicationEvidenceUnknown, "protocol interrupt response claim did not settle",
		)
	}
	return result, err
}

func (m *Module) readProtocolInterruptLink(
	ctx context.Context,
	tenant model.TenantID,
	id model.ID,
) (protocolInterruptLink, error) {
	var link protocolInterruptLink
	err := m.communicationData(tenant).View(ctx, func(sc store.Scope) error {
		repo, err := sc.Ext(protocolInterruptKind)
		if err != nil {
			return err
		}
		row, err := repo.Get(ctx, id)
		if err != nil {
			return err
		}
		link, err = protocolInterruptLinkFromRecord(row)
		return err
	})
	return link, err
}

func (m *Module) loadProtocolInterruptLink(
	ctx context.Context,
	tenant model.TenantID,
	binding ProtocolBinding,
	route ProtocolInterruptRoute,
	routeHash []byte,
	keyDigest string,
) (protocolInterruptLink, error) {
	id, err := workflowCommunicationStableID(
		protocolInterruptSemantic(binding.ID, binding.Generation, keyDigest),
		"protocol.interrupt.link",
	)
	if err != nil {
		return protocolInterruptLink{}, err
	}
	keyHash, _ := parseProtocolInterruptDigest(keyDigest, "response key digest")
	link, err := m.readProtocolInterruptLink(ctx, tenant, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return protocolInterruptLink{}, communicationError(
				ErrInvalidCommunicationTransition, "protocol input response has no registered request key",
			)
		}
		return protocolInterruptLink{}, err
	}
	if link.ID != id || link.BindingID != binding.ID || link.BindingGeneration != binding.Generation ||
		link.WorkItemID != binding.WorkItemID || link.WorkspaceID != binding.WorkspaceID ||
		link.Protocol != BindingProtocolMCP || link.RemoteState != "input_required" ||
		!bytes.Equal(link.KeyHash, keyHash) || !bytes.Equal(link.RouteHash, routeHash) ||
		link.Route != route || link.MessageID.IsZero() || link.DeliveryID.IsZero() {
		return protocolInterruptLink{}, communicationError(
			ErrCommunicationEvidenceUnknown, "protocol input response route or request linkage changed",
		)
	}
	return link, nil
}

func (m *Module) completeProtocolInterruptLink(
	ctx context.Context,
	tenant model.TenantID,
	before protocolInterruptLink,
	routeHash, responseHash, operationHash, effectHash []byte,
	ack WorkflowMessageAckResult,
	published WorkflowWorkTaskResult,
) (protocolInterruptLink, error) {
	var result protocolInterruptLink
	err := m.communicationData(tenant).Mutate(ctx, func(sc store.Scope) error {
		repo, err := sc.Ext(protocolInterruptKind)
		if err != nil {
			return err
		}
		row, err := repo.Get(ctx, before.ID)
		if err != nil {
			return err
		}
		current, err := protocolInterruptLinkFromRecord(row)
		if err != nil || !sameProtocolInterruptIdentity(current, before) ||
			!bytes.Equal(current.RouteHash, routeHash) {
			return communicationError(
				ErrCommunicationEvidenceUnknown, "protocol interrupt changed before response completion",
			)
		}
		if current.State == protocolInterruptResponded {
			if !sameProtocolInputResponseBinding(current, responseHash, operationHash, effectHash) ||
				current.AckID != ack.AckID ||
				current.ResponseMessageID != published.MessageID ||
				current.ResponseDeliveryID != published.DeliveryID {
				return communicationError(
					ErrInvalidCommunicationTransition, "protocol interrupt already has another response",
				)
			}
			result = current
			return nil
		}
		if current.State != protocolInterruptPreparing ||
			!sameProtocolInputResponseBinding(current, responseHash, operationHash, effectHash) ||
			ack.MessageID != current.MessageID ||
			ack.DeliveryID != current.DeliveryID || ack.WorkItemID != current.WorkItemID ||
			published.WorkItemID != current.WorkItemID {
			return communicationError(
				ErrCommunicationEvidenceUnknown, "protocol interrupt response evidence crossed linkage",
			)
		}
		row[colInterruptState] = protocolInterruptResponded
		row[colInterruptResponseHash] = append([]byte(nil), responseHash...)
		row[colInterruptAckID] = ack.AckID.String()
		row[colInterruptResponseMessageID] = published.MessageID.String()
		row[colInterruptResponseDeliveryID] = published.DeliveryID.String()
		updated, err := repo.Update(ctx, row)
		if err != nil {
			return err
		}
		result, err = protocolInterruptLinkFromRecord(updated)
		return err
	})
	if errors.Is(err, store.ErrConflict) {
		current, reloadErr := m.readProtocolInterruptLink(ctx, tenant, before.ID)
		if reloadErr != nil {
			return protocolInterruptLink{}, reloadErr
		}
		if !sameProtocolInterruptIdentity(current, before) ||
			!bytes.Equal(current.RouteHash, routeHash) {
			return protocolInterruptLink{}, communicationError(
				ErrCommunicationEvidenceUnknown, "protocol interrupt changed during response completion",
			)
		}
		if current.State == protocolInterruptResponded &&
			sameProtocolInputResponseBinding(current, responseHash, operationHash, effectHash) &&
			current.AckID == ack.AckID && current.ResponseMessageID == published.MessageID &&
			current.ResponseDeliveryID == published.DeliveryID {
			return current, nil
		}
		return protocolInterruptLink{}, communicationError(
			ErrCommunicationEvidenceUnknown, "protocol interrupt response completion did not settle",
		)
	}
	return result, err
}

func protocolInterruptLinkRecord(link protocolInterruptLink) model.Record {
	return model.Record{
		colWorkWorkspaceID:             link.WorkspaceID.String(),
		colInterruptBindingID:          link.BindingID.String(),
		colInterruptBindingGeneration:  link.BindingGeneration,
		colWorkItemID:                  link.WorkItemID.String(),
		colInterruptProtocol:           string(link.Protocol),
		colInterruptRemoteState:        link.RemoteState,
		colInterruptKeyHash:            append([]byte(nil), link.KeyHash...),
		colInterruptContentHash:        append([]byte(nil), link.ContentHash...),
		colInterruptRouteHash:          append([]byte(nil), link.RouteHash...),
		colCommChannelID:               link.Route.ChannelID.String(),
		colInterruptSenderUserID:       link.Route.SenderUserID.String(),
		colInterruptRecipientUserID:    link.Route.RecipientUserID.String(),
		colInterruptMessageID:          link.MessageID.String(),
		colInterruptDeliveryID:         link.DeliveryID.String(),
		colInterruptState:              link.State,
		colInterruptResponseHash:       nil,
		colInterruptOperationHash:      nil,
		colInterruptEffectHash:         nil,
		colInterruptAckID:              nil,
		colInterruptResponseMessageID:  nil,
		colInterruptResponseDeliveryID: nil,
	}
}

func protocolInterruptLinkFromRecord(row model.Record) (protocolInterruptLink, error) {
	parseRequired := func(column string) (model.ID, error) {
		id, err := model.ParseID(row.String(column))
		if err != nil || id.IsZero() {
			return "", communicationError(ErrCommunicationEvidenceUnknown, "protocol interrupt ID is malformed")
		}
		return id, nil
	}
	parseOptional := func(column string) (model.ID, error) {
		if row.String(column) == "" {
			return "", nil
		}
		return parseRequired(column)
	}
	id, err := parseRequired(model.ColID)
	if err != nil {
		return protocolInterruptLink{}, err
	}
	workspaceID, err := parseRequired(colWorkWorkspaceID)
	if err != nil {
		return protocolInterruptLink{}, err
	}
	bindingID, err := parseRequired(colInterruptBindingID)
	if err != nil {
		return protocolInterruptLink{}, err
	}
	workItemID, err := parseRequired(colWorkItemID)
	if err != nil {
		return protocolInterruptLink{}, err
	}
	channelID, err := parseRequired(colCommChannelID)
	if err != nil {
		return protocolInterruptLink{}, err
	}
	senderID, err := parseRequired(colInterruptSenderUserID)
	if err != nil {
		return protocolInterruptLink{}, err
	}
	recipientID, err := parseRequired(colInterruptRecipientUserID)
	if err != nil {
		return protocolInterruptLink{}, err
	}
	messageID, err := parseRequired(colInterruptMessageID)
	if err != nil {
		return protocolInterruptLink{}, err
	}
	deliveryID, err := parseRequired(colInterruptDeliveryID)
	if err != nil {
		return protocolInterruptLink{}, err
	}
	ackID, err := parseOptional(colInterruptAckID)
	if err != nil {
		return protocolInterruptLink{}, err
	}
	responseMessageID, err := parseOptional(colInterruptResponseMessageID)
	if err != nil {
		return protocolInterruptLink{}, err
	}
	responseDeliveryID, err := parseOptional(colInterruptResponseDeliveryID)
	if err != nil {
		return protocolInterruptLink{}, err
	}
	link := protocolInterruptLink{
		ID: id, Version: row.Int(model.ColVersion), WorkspaceID: workspaceID,
		BindingID: bindingID, BindingGeneration: row.Int(colInterruptBindingGeneration),
		WorkItemID: workItemID, Protocol: BindingProtocol(row.String(colInterruptProtocol)),
		RemoteState: row.String(colInterruptRemoteState), KeyHash: row.Bytes(colInterruptKeyHash),
		ContentHash: row.Bytes(colInterruptContentHash), RouteHash: row.Bytes(colInterruptRouteHash),
		Route:     ProtocolInterruptRoute{ChannelID: channelID, SenderUserID: senderID, RecipientUserID: recipientID},
		MessageID: messageID, DeliveryID: deliveryID, State: row.String(colInterruptState),
		ResponseHash: row.Bytes(colInterruptResponseHash), AckID: ackID,
		OperationHash:     row.Bytes(colInterruptOperationHash),
		EffectHash:        row.Bytes(colInterruptEffectHash),
		ResponseMessageID: responseMessageID, ResponseDeliveryID: responseDeliveryID,
	}
	if link.Version < 1 || link.BindingGeneration < 1 ||
		(link.Protocol != BindingProtocolA2A && link.Protocol != BindingProtocolMCP) ||
		(link.RemoteState != "input_required" && link.RemoteState != "auth_required") ||
		len(link.KeyHash) != sha256.Size || len(link.ContentHash) != sha256.Size ||
		len(link.RouteHash) != sha256.Size ||
		(link.State != protocolInterruptPending && link.State != protocolInterruptPreparing &&
			link.State != protocolInterruptResponded) {
		return protocolInterruptLink{}, communicationError(
			ErrCommunicationEvidenceUnknown, "protocol interrupt row is malformed",
		)
	}
	if link.State == protocolInterruptPending {
		if len(link.ResponseHash) != 0 || len(link.OperationHash) != 0 || len(link.EffectHash) != 0 ||
			!link.AckID.IsZero() ||
			!link.ResponseMessageID.IsZero() || !link.ResponseDeliveryID.IsZero() {
			return protocolInterruptLink{}, communicationError(
				ErrCommunicationEvidenceUnknown, "pending protocol interrupt carries response evidence",
			)
		}
	} else if len(link.ResponseHash) != sha256.Size || len(link.OperationHash) != sha256.Size ||
		len(link.EffectHash) != sha256.Size ||
		(link.State == protocolInterruptPreparing && (!link.AckID.IsZero() ||
			!link.ResponseMessageID.IsZero() || !link.ResponseDeliveryID.IsZero())) ||
		(link.State == protocolInterruptResponded && (link.AckID.IsZero() ||
			link.ResponseMessageID.IsZero() || link.ResponseDeliveryID.IsZero())) {
		return protocolInterruptLink{}, communicationError(
			ErrCommunicationEvidenceUnknown, "responded protocol interrupt lacks response evidence",
		)
	}
	return link, nil
}

func sameProtocolInterruptIdentity(left, right protocolInterruptLink) bool {
	return left.ID == right.ID && left.WorkspaceID == right.WorkspaceID &&
		left.BindingID == right.BindingID && left.BindingGeneration == right.BindingGeneration &&
		left.WorkItemID == right.WorkItemID && left.Protocol == right.Protocol &&
		left.RemoteState == right.RemoteState && left.Route == right.Route &&
		left.MessageID == right.MessageID && left.DeliveryID == right.DeliveryID &&
		bytes.Equal(left.KeyHash, right.KeyHash) && bytes.Equal(left.ContentHash, right.ContentHash) &&
		bytes.Equal(left.RouteHash, right.RouteHash)
}
