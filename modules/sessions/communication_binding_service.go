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
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const maxProtocolBindingPage = 200

// ReserveProtocolBinding commits the complete local dispatch identity before a
// caller is allowed to transmit. A replay is returned with Replayed=true and
// must never cause another transmission.
func (m *Module) ReserveProtocolBinding(
	ctx context.Context,
	tenant model.TenantID,
	reservation ProtocolBindingReservation,
) (ProtocolBinding, error) {
	normalized, requestHash, err := normalizeProtocolBindingReservation(reservation)
	if err != nil {
		return ProtocolBinding{}, err
	}
	var result ProtocolBinding
	for attempt := 0; attempt < 2; attempt++ {
		err = m.workData(tenant).Mutate(ctx, func(sc store.Scope) error {
			repo, err := sc.Ext(protocolBindingKind)
			if err != nil {
				return err
			}
			dispatchHash := hashBytes([]byte(normalized.DispatchKey))
			if replay, err := findProtocolBindingByDispatch(
				ctx, repo, normalized.WorkspaceID, dispatchHash,
			); err != nil {
				return err
			} else if replay != nil {
				if !bytesEqual(replay.reservationHash, requestHash) {
					return protocolBindingConflict("dispatch_key_reused")
				}
				result = replay.ProtocolBinding
				result.Replayed = true
				return nil
			}

			if _, err := sc.Workspaces().Get(ctx, normalized.WorkspaceID); err != nil {
				return err
			}
			spec, err := loadActiveProtocolBindingSpec(ctx, sc, normalized)
			if err != nil {
				return err
			}
			if normalized.ExpectedExternalID != "" {
				if err := requireNoCurrentProtocolExternal(ctx, repo, spec, normalized); err != nil {
					return err
				}
			}
			if err := validateProtocolBindingReferences(ctx, sc, normalized); err != nil {
				return err
			}
			now, err := transactionNow(ctx, sc)
			if err != nil {
				return err
			}
			attemptID := normalized.AttemptID
			if attemptID.IsZero() {
				attemptID = model.NewID()
			}
			bindingID := model.NewID()
			sid := newSID()
			if err := createProtocolSyntheticIdentity(
				ctx, sc, normalized.WorkspaceID, spec.Protocol, sid, attemptID,
				normalized.Generation, now,
			); err != nil {
				return err
			}
			leaseFence, item, err := acquireProtocolWorkLease(
				ctx, sc, tenant, normalized, sid, attemptID, now,
			)
			if err != nil {
				return err
			}
			value := storedProtocolBinding{
				ProtocolBinding: ProtocolBinding{
					MutableCommunicationEntity: MutableCommunicationEntity{CommunicationEntity: CommunicationEntity{
						ID: bindingID, WorkspaceID: normalized.WorkspaceID,
					}},
					BindingSpecID: spec.ID, BindingSpecGeneration: spec.Generation,
					PinnedSpecHash:    cloneCommunicationBytes(spec.SpecHash),
					PinnedMappingHash: cloneCommunicationBytes(spec.MappingHash),
					PinnedLossesHash:  cloneCommunicationBytes(spec.LossesHash),
					WorkItemID:        normalized.WorkItemID, MessageID: normalized.MessageID,
					DeliveryID: normalized.DeliveryID, Protocol: spec.Protocol,
					ProtocolVersion: spec.ProtocolVersion, Direction: spec.Direction,
					PeerAuthority: spec.PeerAuthority, RemoteResourceRef: spec.RemoteResourceRef,
					AttemptID:  attemptID,
					Generation: normalized.Generation, SyntheticSID: sid,
					OwnerKind: normalized.OwnerKind, OwnerRef: normalized.OwnerRef,
					OwnerDigest: cloneCommunicationBytes(normalized.OwnerDigest),
					OwnerEpoch:  normalized.OwnerEpoch, LeaseFence: leaseFence,
					ExternalKind: normalized.ExpectedExternalKind,
					ExternalID:   normalized.ExpectedExternalID,
					LocalState:   "reserved", RemoteState: "unobserved",
					ObservationVerdict:   ProtocolObservationUnknown,
					ObservationCode:      "reserved_before_transmit",
					MCPTask:              cloneProtocolMCPTask(normalized.MCPTask),
					ProtocolMetadataJSON: append(json.RawMessage(nil), normalized.ProtocolMetadataJSON...),
				},
				dispatchKeyHash: dispatchHash, reservationHash: requestHash,
			}
			if normalized.MCPTask != nil {
				value.MCPTaskHash = hashBytes(value.ProtocolMetadataJSON)
				value.CurrentTTLMs = cloneProtocolInt(normalized.MCPTask.TTLMs)
				value.CurrentPollIntervalMs = cloneProtocolInt(normalized.MCPTask.PollIntervalMs)
			}
			if normalized.ExpectedExternalID != "" {
				if err := bindProtocolExternalAlias(
					ctx, sc, value, normalized.ExpectedExternalID, now,
				); err != nil {
					return err
				}
			}
			commandID, eventID, eventSeq, err := persistProtocolBindingWorkEvent(
				ctx, sc, tenant, value, item, "work.binding.reserved", now,
			)
			if err != nil {
				return err
			}
			value.LastCommandID, value.LastEventID, value.LastEventSeq = commandID, eventID, eventSeq
			created, err := repo.CreateWithID(ctx, bindingID, encodeProtocolBinding(value))
			if err != nil {
				return err
			}
			stored, err := decodeProtocolBinding(created)
			if err != nil {
				return err
			}
			result = stored.ProtocolBinding
			return nil
		})
		if err == nil || !errors.Is(err, store.ErrConflict) {
			break
		}
	}
	if err != nil {
		return ProtocolBinding{}, classifyProtocolBindingStoreError(err)
	}
	return result, nil
}

// SettleProtocolBinding records the response union or a post-transmit UNKNOWN.
// Any external identity is bound to the synthetic SID in the same transaction.
func (m *Module) SettleProtocolBinding(
	ctx context.Context,
	tenant model.TenantID,
	settlement ProtocolBindingSettlement,
) (ProtocolBinding, error) {
	normalized, updateHash, err := normalizeProtocolBindingSettlement(settlement)
	if err != nil {
		return ProtocolBinding{}, err
	}
	var result ProtocolBinding
	err = m.workData(tenant).Mutate(ctx, func(sc store.Scope) error {
		repo, err := sc.Ext(protocolBindingKind)
		if err != nil {
			return err
		}
		stored, err := loadProtocolBindingByID(ctx, repo, normalized.BindingID)
		if err != nil {
			return err
		}
		if bytesEqual(stored.lastUpdateHash, updateHash) {
			result = stored.ProtocolBinding
			result.Replayed = true
			return nil
		}
		if err := requireProtocolBindingCAS(stored, normalized.Generation, normalized.ExpectedVersion); err != nil {
			return err
		}
		if !bytesEqual(stored.dispatchKeyHash, hashBytes([]byte(normalized.DispatchKey))) {
			return protocolBindingConflict("dispatch_key_mismatch")
		}
		if stored.ExternalKind != string(normalized.ResultKind) &&
			stored.ExternalKind != string(ProtocolBindingResultTaskOrMessage) {
			return protocolBindingConflict("result_kind_mismatch")
		}
		now, err := transactionNow(ctx, sc)
		if err != nil {
			return err
		}
		primaryExternal := normalized.ExternalID
		if normalized.ResultKind == ProtocolBindingResultMessage && primaryExternal == "" {
			primaryExternal = normalized.ExternalMessageID
		}
		if err := requireStableProtocolExternal(stored.ExternalID, primaryExternal, "external_id"); err != nil {
			return err
		}
		stored.ExternalKind = string(normalized.ResultKind)
		if primaryExternal != "" {
			if err := bindProtocolExternalAlias(ctx, sc, stored, primaryExternal, now); err != nil {
				return err
			}
		}
		stored.ExternalID = primaryExternal
		stored.ContextID = normalized.ContextID
		stored.ExternalMessageID = normalized.ExternalMessageID
		stored.LocalState = normalized.LocalState
		stored.RemoteState = normalized.RemoteState
		stored.RemoteRevision = normalized.RemoteRevision
		stored.ObservationVerdict = normalized.Verdict
		stored.ObservationCode = normalized.Code
		observedAt := now.Time()
		stored.LastObservedAt = &observedAt
		stored.DetailHash = cloneCommunicationBytes(normalized.DetailHash)
		stored.CurrentTTLMs = cloneProtocolInt(normalized.TTLMs)
		stored.CurrentPollIntervalMs = cloneProtocolInt(normalized.PollIntervalMs)
		stored.Terminal = normalized.Terminal
		stored.lastUpdateHash = updateHash
		commandID, eventID, eventSeq, err := reconcileProtocolBindingWork(
			ctx, sc, tenant, stored, protocolBindingObservationEventType(stored.ObservationVerdict), now,
		)
		if err != nil {
			return err
		}
		stored.LastCommandID, stored.LastEventID, stored.LastEventSeq = commandID, eventID, eventSeq
		updated, err := repo.Update(ctx, encodeProtocolBinding(stored))
		if err != nil {
			return err
		}
		stored, err = decodeProtocolBinding(updated)
		if err != nil {
			return err
		}
		result = stored.ProtocolBinding
		return nil
	})
	if err != nil {
		return ProtocolBinding{}, classifyProtocolBindingStoreError(err)
	}
	return result, nil
}

// ObserveProtocolBinding is exact-generation reconciliation. It never changes
// a previously bound external identity and UNKNOWN remains a first-class fact.
func (m *Module) ObserveProtocolBinding(
	ctx context.Context,
	tenant model.TenantID,
	observation ProtocolBindingObservation,
) (ProtocolBinding, error) {
	normalized, updateHash, err := normalizeProtocolBindingObservation(observation)
	if err != nil {
		return ProtocolBinding{}, err
	}
	var result ProtocolBinding
	err = m.workData(tenant).Mutate(ctx, func(sc store.Scope) error {
		repo, err := sc.Ext(protocolBindingKind)
		if err != nil {
			return err
		}
		stored, err := loadProtocolBindingByID(ctx, repo, normalized.BindingID)
		if err != nil {
			return err
		}
		if bytesEqual(stored.lastUpdateHash, updateHash) {
			result = stored.ProtocolBinding
			result.Replayed = true
			return nil
		}
		if err := requireProtocolBindingCAS(stored, normalized.Generation, normalized.ExpectedVersion); err != nil {
			return err
		}
		authority, err := normalizeProtocolAuthority(normalized.PeerAuthority)
		if err != nil || authority != stored.PeerAuthority {
			return protocolBindingConflict("peer_authority_mismatch")
		}
		primaryExternal := normalized.ExternalID
		if stored.ExternalKind == string(ProtocolBindingResultMessage) && primaryExternal == "" {
			primaryExternal = normalized.ExternalMessageID
		}
		if err := requireStableProtocolExternal(stored.ExternalID, primaryExternal, "external_id"); err != nil {
			return err
		}
		if err := requireStableProtocolExternal(stored.ContextID, normalized.ContextID, "context_id"); err != nil {
			return err
		}
		if err := requireStableProtocolExternal(stored.ExternalMessageID, normalized.ExternalMessageID, "external_message_id"); err != nil {
			return err
		}
		now, err := transactionNow(ctx, sc)
		if err != nil {
			return err
		}
		if stored.ExternalID == "" && primaryExternal != "" {
			if err := bindProtocolExternalAlias(ctx, sc, stored, primaryExternal, now); err != nil {
				return err
			}
			stored.ExternalID = primaryExternal
		}
		if stored.ContextID == "" {
			stored.ContextID = normalized.ContextID
		}
		if stored.ExternalMessageID == "" {
			stored.ExternalMessageID = normalized.ExternalMessageID
		}
		stored.LocalState = normalized.LocalState
		stored.RemoteState = normalized.RemoteState
		stored.RemoteRevision = normalized.RemoteRevision
		stored.ObservationVerdict = normalized.Verdict
		stored.ObservationCode = normalized.Code
		observedAt := now.Time()
		stored.LastObservedAt = &observedAt
		stored.DetailHash = cloneCommunicationBytes(normalized.DetailHash)
		stored.CurrentTTLMs = cloneProtocolInt(normalized.TTLMs)
		stored.CurrentPollIntervalMs = cloneProtocolInt(normalized.PollIntervalMs)
		stored.Terminal = normalized.Terminal
		stored.lastUpdateHash = updateHash
		commandID, eventID, eventSeq, err := reconcileProtocolBindingWork(
			ctx, sc, tenant, stored, protocolBindingObservationEventType(stored.ObservationVerdict), now,
		)
		if err != nil {
			return err
		}
		stored.LastCommandID, stored.LastEventID, stored.LastEventSeq = commandID, eventID, eventSeq
		updated, err := repo.Update(ctx, encodeProtocolBinding(stored))
		if err != nil {
			return err
		}
		stored, err = decodeProtocolBinding(updated)
		if err != nil {
			return err
		}
		result = stored.ProtocolBinding
		return nil
	})
	if err != nil {
		return ProtocolBinding{}, classifyProtocolBindingStoreError(err)
	}
	return result, nil
}

// RequestProtocolBindingCancel durably claims the one cancellation side effect.
// Exact retries return Replayed=true so the caller does not repeat the RPC.
func (m *Module) RequestProtocolBindingCancel(
	ctx context.Context,
	tenant model.TenantID,
	intent ProtocolBindingCancelIntent,
) (ProtocolBinding, error) {
	normalized, cancelHash, err := normalizeProtocolBindingCancel(intent)
	if err != nil {
		return ProtocolBinding{}, err
	}
	var result ProtocolBinding
	err = m.workData(tenant).Mutate(ctx, func(sc store.Scope) error {
		repo, err := sc.Ext(protocolBindingKind)
		if err != nil {
			return err
		}
		stored, err := loadProtocolBindingByID(ctx, repo, normalized.BindingID)
		if err != nil {
			return err
		}
		if bytesEqual(stored.cancelKeyHash, cancelHash) {
			result = stored.ProtocolBinding
			result.Replayed = true
			return nil
		}
		if stored.CancelRequested {
			return protocolBindingConflict("cancel_already_requested")
		}
		if err := requireProtocolBindingCAS(stored, normalized.Generation, normalized.ExpectedVersion); err != nil {
			return err
		}
		if stored.Terminal {
			return protocolBindingConflict("binding_terminal")
		}
		now, err := transactionNow(ctx, sc)
		if err != nil {
			return err
		}
		requestedAt := now.Time()
		stored.CancelRequested = true
		stored.CancelRequestedAt = &requestedAt
		stored.CancelReasonCode = normalized.ReasonCode
		stored.LocalState = "cancel_requested"
		stored.ObservationVerdict = ProtocolObservationUnknown
		stored.ObservationCode = "cancel_requested_unobserved"
		stored.LastObservedAt = &requestedAt
		stored.cancelKeyHash = cancelHash
		commandID, eventID, eventSeq, err := reconcileProtocolBindingWork(
			ctx, sc, tenant, stored, "work.binding.cancel_requested", now,
		)
		if err != nil {
			return err
		}
		stored.LastCommandID, stored.LastEventID, stored.LastEventSeq = commandID, eventID, eventSeq
		updated, err := repo.Update(ctx, encodeProtocolBinding(stored))
		if err != nil {
			return err
		}
		stored, err = decodeProtocolBinding(updated)
		if err != nil {
			return err
		}
		result = stored.ProtocolBinding
		return nil
	})
	if err != nil {
		return ProtocolBinding{}, classifyProtocolBindingStoreError(err)
	}
	return result, nil
}

func (m *Module) GetProtocolBinding(
	ctx context.Context,
	tenant model.TenantID,
	ref ProtocolBindingRef,
) (ProtocolBinding, error) {
	if err := validateProtocolBindingRef(ref); err != nil {
		return ProtocolBinding{}, err
	}
	var result ProtocolBinding
	err := m.workData(tenant).View(ctx, func(sc store.Scope) error {
		repo, err := sc.Ext(protocolBindingKind)
		if err != nil {
			return err
		}
		if !ref.ID.IsZero() {
			stored, err := loadProtocolBindingByID(ctx, repo, ref.ID)
			if err != nil {
				return err
			}
			if !ref.WorkspaceID.IsZero() && stored.WorkspaceID != ref.WorkspaceID {
				return protocolBindingNotFound("binding_not_found")
			}
			result = stored.ProtocolBinding
			return nil
		}
		authority, err := normalizeProtocolAuthority(ref.PeerAuthority)
		if err != nil {
			return err
		}
		filters := []model.Filter{
			{Column: colWorkWorkspaceID, Op: model.OpEq, Value: ref.WorkspaceID.String()},
			{Column: colBindingProtocol, Op: model.OpEq, Value: string(ref.Protocol)},
			{Column: colBindingPeerAuthority, Op: model.OpEq, Value: authority},
			{Column: colBindingExternalKind, Op: model.OpEq, Value: ref.ExternalKind},
			{Column: colBindingExternalID, Op: model.OpEq, Value: ref.ExternalID},
		}
		if ref.Generation == 0 {
			filters = append(filters,
				model.Filter{Column: colBindingExternalActiveSlot, Op: model.OpEq, Value: ref.ExternalID},
			)
		} else {
			filters = append(filters,
				model.Filter{Column: colCommGeneration, Op: model.OpEq, Value: ref.Generation},
			)
		}
		rows, err := listAll(ctx, repo, filters...)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return protocolBindingNotFound("binding_not_found")
		}
		if len(rows) != 1 {
			return protocolBindingUnknown("duplicate_external_binding", nil)
		}
		stored, err := decodeProtocolBinding(rows[0])
		if err != nil {
			return err
		}
		result = stored.ProtocolBinding
		return nil
	})
	if err != nil {
		return ProtocolBinding{}, classifyProtocolBindingStoreError(err)
	}
	return result, nil
}

func (m *Module) ListProtocolBindings(
	ctx context.Context,
	tenant model.TenantID,
	query ProtocolBindingQuery,
) (ProtocolBindingPage, error) {
	normalized, err := normalizeProtocolBindingQuery(query)
	if err != nil {
		return ProtocolBindingPage{}, err
	}
	var result ProtocolBindingPage
	err = m.workData(tenant).View(ctx, func(sc store.Scope) error {
		if _, err := sc.Workspaces().Get(ctx, normalized.WorkspaceID); err != nil {
			return err
		}
		repo, err := sc.Ext(protocolBindingKind)
		if err != nil {
			return err
		}
		filters := []model.Filter{{Column: colWorkWorkspaceID, Op: model.OpEq, Value: normalized.WorkspaceID.String()}}
		if !normalized.BindingSpecID.IsZero() {
			filters = append(filters, model.Filter{Column: colBindingSpecID, Op: model.OpEq, Value: normalized.BindingSpecID.String()})
		}
		if !normalized.WorkItemID.IsZero() {
			filters = append(filters, model.Filter{Column: colWorkItemID, Op: model.OpEq, Value: normalized.WorkItemID.String()})
		}
		if normalized.Protocol != "" {
			filters = append(filters, model.Filter{Column: colBindingProtocol, Op: model.OpEq, Value: string(normalized.Protocol)})
		}
		for column, value := range map[string]string{
			colBindingPeerAuthority: normalized.PeerAuthority,
			colCommOwnerKind:        normalized.OwnerKind,
			colCommOwnerRef:         normalized.OwnerRef,
			colBindingExternalKind:  normalized.ExternalKind,
			colBindingExternalID:    normalized.ExternalID,
		} {
			if value != "" {
				filters = append(filters, model.Filter{Column: column, Op: model.OpEq, Value: value})
			}
		}
		if normalized.Verdict != "" {
			filters = append(filters, model.Filter{Column: colBindingObservationVerdict, Op: model.OpEq, Value: string(normalized.Verdict)})
		}
		if normalized.Terminal != nil {
			filters = append(filters, model.Filter{Column: colBindingTerminal, Op: model.OpEq, Value: *normalized.Terminal})
		}
		rows, page, err := repo.List(ctx, model.Query{Filters: filters, Limit: normalized.Limit, Cursor: normalized.Cursor})
		if err != nil {
			return err
		}
		result.Items = make([]ProtocolBinding, 0, len(rows))
		for _, row := range rows {
			stored, err := decodeProtocolBinding(row)
			if err != nil {
				return err
			}
			result.Items = append(result.Items, stored.ProtocolBinding)
		}
		result.NextCursor, result.HasMore = page.Cursor, page.HasMore
		return nil
	})
	if err != nil {
		return ProtocolBindingPage{}, classifyProtocolBindingStoreError(err)
	}
	return result, nil
}

func normalizeProtocolBindingReservation(
	value ProtocolBindingReservation,
) (ProtocolBindingReservation, []byte, error) {
	value.DispatchKey = strings.TrimSpace(value.DispatchKey)
	value.ExpectedExternalKind = strings.ToLower(strings.TrimSpace(value.ExpectedExternalKind))
	value.ExpectedExternalID = strings.TrimSpace(value.ExpectedExternalID)
	value.OwnerKind = strings.ToLower(strings.TrimSpace(value.OwnerKind))
	value.OwnerRef = strings.TrimSpace(value.OwnerRef)
	if !validCanonicalCommunicationID(value.WorkspaceID) ||
		!validCanonicalCommunicationID(value.BindingSpecID) || value.BindingSpecGeneration < 1 ||
		!value.ExpectedDirection.valid() ||
		!validCanonicalCommunicationID(value.WorkItemID) ||
		(!value.MessageID.IsZero() && !validCanonicalCommunicationID(value.MessageID)) ||
		(!value.DeliveryID.IsZero() && !validCanonicalCommunicationID(value.DeliveryID)) ||
		(!value.AttemptID.IsZero() && !validCanonicalCommunicationID(value.AttemptID)) ||
		!validateOpaqueRef(value.DispatchKey) || value.Generation < 1 ||
		!ProtocolBindingResultKind(value.ExpectedExternalKind).validReservation() ||
		(value.ExpectedExternalID != "" && !validateOpaqueRef(value.ExpectedExternalID)) ||
		(value.ExpectedExternalID != "" &&
			value.ExpectedExternalKind == string(ProtocolBindingResultTaskOrMessage)) ||
		!validateOpaqueRef(value.OwnerKind) || !validateOpaqueRef(value.OwnerRef) ||
		value.OwnerEpoch < 1 || value.LeaseFence < 0 ||
		(len(value.OwnerDigest) != 0 && len(value.OwnerDigest) != sha256.Size) {
		return value, nil, protocolBindingInvalid("invalid_reservation")
	}
	projection, metadata, err := normalizeProtocolMCPMetadata(value.MCPTask, value.ProtocolMetadataJSON)
	if err != nil {
		return value, nil, err
	}
	value.MCPTask, value.ProtocolMetadataJSON = projection, metadata
	requestHash, err := protocolBindingHash(value)
	if err != nil {
		return value, nil, err
	}
	return value, requestHash, nil
}

func normalizeProtocolMCPMetadata(
	projection *ProtocolMCPTaskProjection,
	raw json.RawMessage,
) (*ProtocolMCPTaskProjection, json.RawMessage, error) {
	if len(raw) != 0 {
		canonical, err := canonicalProtocolMetadata(raw)
		if err != nil {
			return nil, nil, err
		}
		var decoded ProtocolMCPTaskProjection
		if err := json.Unmarshal(canonical, &decoded); err != nil {
			return nil, nil, protocolBindingInvalid("invalid_protocol_metadata")
		}
		if projection != nil {
			left, _ := canonicalJSON(projection)
			right, _ := canonicalJSON(decoded)
			if string(left) != string(right) {
				return nil, nil, protocolBindingConflict("protocol_metadata_mismatch")
			}
		}
		projection = &decoded
	}
	if projection == nil {
		return nil, nil, nil
	}
	copy := *projection
	copy.Owner.Subject = strings.TrimSpace(copy.Owner.Subject)
	copy.Owner.ActAs = strings.TrimSpace(copy.Owner.ActAs)
	copy.Owner.Issuer = strings.TrimSpace(copy.Owner.Issuer)
	copy.Owner.ClientID = strings.TrimSpace(copy.Owner.ClientID)
	copy.Tool = strings.TrimSpace(copy.Tool)
	copy.RequiredScope = strings.TrimSpace(copy.RequiredScope)
	copy.InitialStatus = strings.ToLower(strings.TrimSpace(copy.InitialStatus))
	copy.InitialStatusReason = strings.TrimSpace(copy.InitialStatusReason)
	copy.UpstreamDescriptor = strings.TrimSpace(copy.UpstreamDescriptor)
	copy.ProtocolRevision = strings.TrimSpace(copy.ProtocolRevision)
	copy.OriginOperationID = strings.TrimSpace(copy.OriginOperationID)
	copy.OriginEffectDigest = strings.TrimSpace(copy.OriginEffectDigest)
	copy.CreatedAt = copy.CreatedAt.UTC()
	if len(copy.InitialInputRequests) != 0 {
		requests, err := normalizeProtocolInterruptRequests(copy.InitialInputRequests)
		if err != nil || copy.InitialStatus != "input_required" {
			return nil, nil, protocolBindingInvalid("invalid_protocol_metadata")
		}
		copy.InitialInputRequests = requests
	} else {
		copy.InitialInputRequests = nil
	}
	if !validateOpaqueRef(copy.Owner.Subject) || !validateOpaqueRef(copy.Owner.Issuer) ||
		(copy.Owner.ClientID != "" && !validateOpaqueRef(copy.Owner.ClientID)) ||
		(copy.Owner.IsDelegated != (copy.Owner.ActAs != "")) ||
		(copy.Owner.ActAs != "" && !validateOpaqueRef(copy.Owner.ActAs)) ||
		!validateOpaqueRef(copy.Tool) ||
		(copy.RequiredScope != "" && !validateOpaqueRef(copy.RequiredScope)) ||
		copy.CreatedAt.IsZero() || !boundedToken(copy.InitialStatus, 128) ||
		(copy.InitialStatusReason != "" && !validateOpaqueRef(copy.InitialStatusReason)) ||
		!validateOpaqueRef(copy.UpstreamDescriptor) || !validateOpaqueRef(copy.ProtocolRevision) ||
		!validateOpaqueRef(copy.OriginOperationID) || !validateOpaqueRef(copy.OriginEffectDigest) ||
		!validProtocolDuration(copy.TTLMs) || !validProtocolDuration(copy.PollIntervalMs) {
		return nil, nil, protocolBindingInvalid("invalid_protocol_metadata")
	}
	canonical, err := canonicalJSON(copy)
	if err != nil || len(canonical) > maxProtocolSelectorBytes {
		return nil, nil, protocolBindingInvalid("invalid_protocol_metadata")
	}
	return &copy, json.RawMessage(canonical), nil
}

func canonicalProtocolMetadata(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 || len(raw) > maxProtocolSelectorBytes {
		return nil, protocolBindingInvalid("invalid_protocol_metadata")
	}
	canonical, err := canonicalJSON(raw)
	if err != nil || len(canonical) == 0 || canonical[0] != '{' {
		return nil, protocolBindingInvalid("invalid_protocol_metadata")
	}
	return json.RawMessage(canonical), nil
}

func validProtocolDuration(value *int64) bool {
	return value == nil || (*value > 0 && *value <= int64((30*24*time.Hour)/time.Millisecond))
}

func normalizeProtocolBindingSettlement(
	value ProtocolBindingSettlement,
) (ProtocolBindingSettlement, []byte, error) {
	value.DispatchKey = strings.TrimSpace(value.DispatchKey)
	value.ExternalID = strings.TrimSpace(value.ExternalID)
	value.ContextID = strings.TrimSpace(value.ContextID)
	value.ExternalMessageID = strings.TrimSpace(value.ExternalMessageID)
	value.LocalState = strings.ToLower(strings.TrimSpace(value.LocalState))
	value.RemoteState = strings.ToLower(strings.TrimSpace(value.RemoteState))
	value.RemoteRevision = strings.TrimSpace(value.RemoteRevision)
	value.Code = strings.ToLower(strings.TrimSpace(value.Code))
	if !validCanonicalCommunicationID(value.BindingID) || value.Generation < 1 ||
		value.ExpectedVersion < 1 || !validateOpaqueRef(value.DispatchKey) ||
		!value.ResultKind.valid() || !validProtocolObservation(
		value.Verdict, value.Code, value.Observed, value.Terminal,
	) || !validateProtocolExternalFields(
		value.ExternalID, value.ContextID, value.ExternalMessageID,
	) || canonicalProtocolWorkState(value.LocalState) == "" ||
		!validateOpaqueRef(value.LocalState) || !validateOpaqueRef(value.RemoteState) ||
		(value.RemoteRevision != "" && !validateOpaqueRef(value.RemoteRevision)) ||
		(len(value.DetailHash) != 0 && len(value.DetailHash) != sha256.Size) ||
		!validProtocolDuration(value.TTLMs) || !validProtocolDuration(value.PollIntervalMs) {
		return value, nil, protocolBindingInvalid("invalid_settlement")
	}
	if value.Verdict == ProtocolObservationClean {
		if value.ResultKind == ProtocolBindingResultTask && value.ExternalID == "" {
			return value, nil, protocolBindingInvalid("missing_external_task_id")
		}
		if value.ResultKind == ProtocolBindingResultMessage && value.ExternalMessageID == "" {
			return value, nil, protocolBindingInvalid("missing_external_message_id")
		}
	}
	hash, err := protocolBindingHash(value)
	return value, hash, err
}

func normalizeProtocolBindingObservation(
	value ProtocolBindingObservation,
) (ProtocolBindingObservation, []byte, error) {
	value.SemanticKey = strings.TrimSpace(value.SemanticKey)
	value.PeerAuthority = strings.TrimSpace(value.PeerAuthority)
	value.ExternalID = strings.TrimSpace(value.ExternalID)
	value.ContextID = strings.TrimSpace(value.ContextID)
	value.ExternalMessageID = strings.TrimSpace(value.ExternalMessageID)
	value.LocalState = strings.ToLower(strings.TrimSpace(value.LocalState))
	value.RemoteState = strings.ToLower(strings.TrimSpace(value.RemoteState))
	value.RemoteRevision = strings.TrimSpace(value.RemoteRevision)
	value.Code = strings.ToLower(strings.TrimSpace(value.Code))
	if !validCanonicalCommunicationID(value.BindingID) || value.Generation < 1 ||
		value.ExpectedVersion < 1 || !validateOpaqueRef(value.SemanticKey) ||
		!validateOpaqueRef(value.PeerAuthority) || !validProtocolObservation(
		value.Verdict, value.Code, value.Observed, value.Terminal,
	) || !validateProtocolExternalFields(
		value.ExternalID, value.ContextID, value.ExternalMessageID,
	) || canonicalProtocolWorkState(value.LocalState) == "" ||
		!validateOpaqueRef(value.LocalState) || !validateOpaqueRef(value.RemoteState) ||
		(value.RemoteRevision != "" && !validateOpaqueRef(value.RemoteRevision)) ||
		(len(value.DetailHash) != 0 && len(value.DetailHash) != sha256.Size) ||
		!validProtocolDuration(value.TTLMs) || !validProtocolDuration(value.PollIntervalMs) {
		return value, nil, protocolBindingInvalid("invalid_observation")
	}
	hash, err := protocolBindingHash(value)
	return value, hash, err
}

func validProtocolObservation(
	verdict ProtocolObservationVerdict,
	code string,
	observed bool,
	terminal bool,
) bool {
	if !verdict.valid() || !boundedToken(code, 128) || (!observed && verdict != ProtocolObservationUnknown) {
		return false
	}
	return !terminal || (observed && verdict != ProtocolObservationUnknown)
}

func validateProtocolExternalFields(externalID, contextID, messageID string) bool {
	return (externalID == "" || validateOpaqueRef(externalID)) &&
		(contextID == "" || validateOpaqueRef(contextID)) &&
		(messageID == "" || validateOpaqueRef(messageID))
}

func normalizeProtocolBindingCancel(
	value ProtocolBindingCancelIntent,
) (ProtocolBindingCancelIntent, []byte, error) {
	value.SemanticKey = strings.TrimSpace(value.SemanticKey)
	value.ReasonCode = strings.ToLower(strings.TrimSpace(value.ReasonCode))
	if !validCanonicalCommunicationID(value.BindingID) || value.Generation < 1 ||
		value.ExpectedVersion < 1 || !validateOpaqueRef(value.SemanticKey) ||
		!boundedToken(value.ReasonCode, 128) {
		return value, nil, protocolBindingInvalid("invalid_cancel_intent")
	}
	hash, err := protocolBindingHash(value)
	return value, hash, err
}

func validateProtocolBindingRef(ref ProtocolBindingRef) error {
	if !ref.ID.IsZero() {
		if !validCanonicalCommunicationID(ref.ID) ||
			(!ref.WorkspaceID.IsZero() && !validCanonicalCommunicationID(ref.WorkspaceID)) ||
			ref.Protocol != "" || ref.PeerAuthority != "" || ref.ExternalKind != "" ||
			ref.ExternalID != "" || ref.Generation != 0 {
			return protocolBindingInvalid("invalid_binding_ref")
		}
		return nil
	}
	if !validCanonicalCommunicationID(ref.WorkspaceID) || !ref.Protocol.valid() ||
		!validateOpaqueRef(strings.TrimSpace(ref.PeerAuthority)) ||
		!validateOpaqueRef(strings.TrimSpace(ref.ExternalKind)) ||
		!validateOpaqueRef(strings.TrimSpace(ref.ExternalID)) || ref.Generation < 0 {
		return protocolBindingInvalid("invalid_binding_ref")
	}
	return nil
}

func normalizeProtocolBindingQuery(value ProtocolBindingQuery) (ProtocolBindingQuery, error) {
	if !validCanonicalCommunicationID(value.WorkspaceID) ||
		(!value.BindingSpecID.IsZero() && !validCanonicalCommunicationID(value.BindingSpecID)) ||
		(!value.WorkItemID.IsZero() && !validCanonicalCommunicationID(value.WorkItemID)) ||
		(value.Protocol != "" && !value.Protocol.valid()) ||
		(value.Verdict != "" && !value.Verdict.valid()) || value.Limit < 0 ||
		value.Limit > maxProtocolBindingPage {
		return value, protocolBindingInvalid("invalid_binding_query")
	}
	value.PeerAuthority = strings.TrimSpace(value.PeerAuthority)
	if value.PeerAuthority != "" {
		var err error
		value.PeerAuthority, err = normalizeProtocolAuthority(value.PeerAuthority)
		if err != nil {
			return value, err
		}
	}
	value.OwnerKind = strings.ToLower(strings.TrimSpace(value.OwnerKind))
	value.OwnerRef = strings.TrimSpace(value.OwnerRef)
	value.ExternalKind = strings.ToLower(strings.TrimSpace(value.ExternalKind))
	value.ExternalID = strings.TrimSpace(value.ExternalID)
	for _, optional := range []string{value.OwnerKind, value.OwnerRef, value.ExternalKind, value.ExternalID} {
		if optional != "" && !validateOpaqueRef(optional) {
			return value, protocolBindingInvalid("invalid_binding_query")
		}
	}
	if (value.OwnerKind == "") != (value.OwnerRef == "") {
		return value, protocolBindingInvalid("invalid_binding_query")
	}
	if value.Limit == 0 {
		value.Limit = 100
	}
	return value, nil
}

func loadActiveProtocolBindingSpec(
	ctx context.Context,
	sc store.Scope,
	reservation ProtocolBindingReservation,
) (ProtocolBindingSpec, error) {
	repo, err := sc.Ext(protocolBindingSpecKind)
	if err != nil {
		return ProtocolBindingSpec{}, err
	}
	record, err := repo.Get(ctx, reservation.BindingSpecID)
	if err != nil {
		return ProtocolBindingSpec{}, err
	}
	stored, err := decodeProtocolBindingSpec(record)
	if err != nil {
		return ProtocolBindingSpec{}, err
	}
	if stored.WorkspaceID != reservation.WorkspaceID ||
		stored.Generation != reservation.BindingSpecGeneration {
		return ProtocolBindingSpec{}, protocolBindingNotFound("active_spec_not_found")
	}
	if stored.State != ProtocolBindingSpecActive {
		return ProtocolBindingSpec{}, protocolBindingConflict("binding_spec_not_active")
	}
	if stored.LocalKind != BindingLocalWorkItem {
		return ProtocolBindingSpec{}, protocolBindingConflict("binding_spec_local_kind_mismatch")
	}
	if stored.Direction != reservation.ExpectedDirection && stored.Direction != BindingBidirectional {
		return ProtocolBindingSpec{}, protocolBindingConflict("binding_spec_direction_mismatch")
	}
	if stored.Protocol != BindingProtocolMCP && reservation.MCPTask != nil {
		return ProtocolBindingSpec{}, protocolBindingInvalid("metadata_protocol_mismatch")
	}
	if stored.Protocol == BindingProtocolMCP && reservation.ExpectedExternalKind == string(ProtocolBindingResultTask) && reservation.MCPTask == nil {
		return ProtocolBindingSpec{}, protocolBindingInvalid("missing_protocol_metadata")
	}
	return stored.ProtocolBindingSpec, nil
}

func requireNoCurrentProtocolExternal(
	ctx context.Context,
	repo store.GenericRepo,
	spec ProtocolBindingSpec,
	reservation ProtocolBindingReservation,
) error {
	rows, err := listAll(ctx, repo,
		model.Filter{Column: colWorkWorkspaceID, Op: model.OpEq, Value: reservation.WorkspaceID.String()},
		model.Filter{Column: colBindingProtocol, Op: model.OpEq, Value: string(spec.Protocol)},
		model.Filter{Column: colBindingPeerAuthority, Op: model.OpEq, Value: spec.PeerAuthority},
		model.Filter{Column: colBindingExternalKind, Op: model.OpEq, Value: reservation.ExpectedExternalKind},
		model.Filter{Column: colBindingExternalActiveSlot, Op: model.OpEq, Value: reservation.ExpectedExternalID},
	)
	if err != nil {
		return err
	}
	if len(rows) != 0 {
		return protocolBindingConflict("external_binding_current")
	}
	return nil
}

func validateProtocolBindingReferences(
	ctx context.Context,
	sc store.Scope,
	reservation ProtocolBindingReservation,
) error {
	for _, ref := range []struct {
		kind model.Kind
		id   model.ID
	}{
		{messageKind, reservation.MessageID},
		{messageDeliveryKind, reservation.DeliveryID},
	} {
		if ref.id.IsZero() {
			continue
		}
		repo, err := sc.Ext(ref.kind)
		if err != nil {
			return err
		}
		record, err := repo.Get(ctx, ref.id)
		if err != nil {
			return err
		}
		if record.String(colWorkWorkspaceID) != reservation.WorkspaceID.String() {
			return protocolBindingNotFound("referenced_entity_not_found")
		}
	}
	return nil
}

func createProtocolSyntheticIdentity(
	ctx context.Context,
	sc store.Scope,
	workspace model.ID,
	protocol BindingProtocol,
	sid string,
	attemptID model.ID,
	generation int64,
	now model.Timestamp,
) error {
	repo, err := sc.Ext(identityKind)
	if err != nil {
		return err
	}
	origin := OriginA2A
	if protocol == BindingProtocolMCP {
		origin = OriginMCP
	}
	if _, err := repo.Create(ctx, model.Record{
		colSID: sid, colOrigin: origin, colIDFirstSeen: now.String(),
		colIDLastSeen: now.String(), colDeclaredAt: nil, colMergedInto: nil,
		colIDWorkspaceID: workspace.String(),
	}); err != nil {
		return err
	}
	return bindAlias(ctx, sc, sid, SessionBinding{
		Provider: string(protocol), ExternalID: protocolAttemptAlias(attemptID, generation),
		Origin: origin, At: now.Time(), WorkspaceID: workspace,
	})
}

func protocolAttemptAlias(attemptID model.ID, generation int64) string {
	digest := hashBytes([]byte(fmt.Sprintf("attempt\x00%s\x00%d", attemptID, generation)))
	return "attempt_" + protocolBindingHex(digest)
}

func acquireProtocolWorkLease(
	ctx context.Context,
	sc store.Scope,
	tenant model.TenantID,
	reservation ProtocolBindingReservation,
	sid string,
	attemptID model.ID,
	now model.Timestamp,
) (int64, model.Record, error) {
	if err := advanceLeaseClock(ctx, sc, tenant, WorkCommand{WorkspaceID: reservation.WorkspaceID}, now); err != nil {
		return 0, nil, protocolBindingUnknown("lease_clock_unavailable", err)
	}
	if err := lockWorkLeaseItem(ctx, sc, tenant, reservation.WorkspaceID, reservation.WorkItemID); err != nil {
		return 0, nil, protocolBindingUnknown("lease_coordination_unavailable", err)
	}
	items, err := sc.Ext(workItemKind)
	if err != nil {
		return 0, nil, err
	}
	item, err := items.Get(ctx, reservation.WorkItemID)
	if err != nil {
		return 0, nil, err
	}
	if item.String(colWorkWorkspaceID) != reservation.WorkspaceID.String() ||
		item.String(colWorkOwnerKind) != reservation.OwnerKind ||
		item.String(colWorkOwnerRef) != reservation.OwnerRef ||
		item.Int(colWorkOwnerEpoch) != reservation.OwnerEpoch {
		return 0, nil, protocolBindingConflict("work_authority_changed")
	}
	if status := item.String(colWorkStatus); status != "ready" && status != "active" {
		return 0, nil, protocolBindingConflict("work_not_dispatchable")
	}
	lease, found, err := findWorkLease(ctx, sc, reservation.WorkItemID)
	if err != nil {
		return 0, nil, err
	}
	if !found || lease.String(colWorkWorkspaceID) != reservation.WorkspaceID.String() {
		return 0, nil, protocolBindingUnknown("work_lease_unavailable", nil)
	}
	state, err := workLeaseFenceState(lease)
	if err != nil {
		return 0, nil, err
	}
	if state.Fence != reservation.LeaseFence {
		return 0, nil, protocolBindingConflict("stale_lease_fence")
	}
	next, err := nextFence(state.Fence)
	if err != nil {
		return 0, nil, protocolBindingConflict("lease_fence_exhausted")
	}
	runRef := "protocol:" + attemptID.String()
	agentRef := ""
	if reservation.OwnerKind == "agent" {
		agentRef = reservation.OwnerRef
	}
	state = fenceState{
		Holder: workLeaseHolderKey(sid, runRef, agentRef), Fence: next,
		Lifecycle: fenceActive, AcquiredAt: now.Time(),
		ExpiresAt: now.Time().Add(defaultWorkLeaseTTL),
	}
	applyWorkLeaseFenceState(lease, state, sid, runRef, agentRef)
	leases, err := sc.Ext(workLeaseKind)
	if err != nil {
		return 0, nil, err
	}
	updated, err := leases.Update(ctx, lease)
	if err != nil {
		return 0, nil, err
	}
	return updated.Int(colLeaseFence), item, nil
}

func bindProtocolExternalAlias(
	ctx context.Context,
	sc store.Scope,
	binding storedProtocolBinding,
	externalID string,
	now model.Timestamp,
) error {
	aliasID, err := protocolExternalAlias(binding, externalID)
	if err != nil {
		return err
	}
	provider := string(binding.Protocol)
	if existing, found, err := findAlias(ctx, sc, provider, aliasID); err != nil {
		return err
	} else if found {
		if existing.String(colAliasSID) != binding.SyntheticSID {
			return protocolBindingConflict("external_alias_conflict")
		}
		return nil
	}
	return bindAlias(ctx, sc, binding.SyntheticSID, SessionBinding{
		Provider: provider, ExternalID: aliasID, Origin: string(binding.Protocol),
		At: now.Time(), WorkspaceID: binding.WorkspaceID,
	})
}

func protocolExternalAlias(binding storedProtocolBinding, externalID string) (string, error) {
	digest, err := protocolBindingHash(struct {
		Protocol      BindingProtocol `json:"protocol"`
		PeerAuthority string          `json:"peer_authority"`
		ExternalKind  string          `json:"external_kind"`
		ExternalID    string          `json:"external_id"`
		Generation    int64           `json:"generation"`
	}{binding.Protocol, binding.PeerAuthority, binding.ExternalKind, externalID, binding.Generation})
	if err != nil {
		return "", err
	}
	return "external_" + protocolBindingHex(digest), nil
}

func findProtocolBindingByDispatch(
	ctx context.Context,
	repo store.GenericRepo,
	workspace model.ID,
	dispatchHash []byte,
) (*storedProtocolBinding, error) {
	rows, err := listAll(ctx, repo,
		model.Filter{Column: colWorkWorkspaceID, Op: model.OpEq, Value: workspace.String()},
		model.Filter{Column: colBindingDispatchKeyHash, Op: model.OpEq, Value: dispatchHash},
	)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	if len(rows) != 1 {
		return nil, protocolBindingUnknown("duplicate_dispatch_binding", nil)
	}
	stored, err := decodeProtocolBinding(rows[0])
	if err != nil {
		return nil, err
	}
	return &stored, nil
}

func loadProtocolBindingByID(
	ctx context.Context,
	repo store.GenericRepo,
	id model.ID,
) (storedProtocolBinding, error) {
	record, err := repo.Get(ctx, id)
	if err != nil {
		return storedProtocolBinding{}, err
	}
	return decodeProtocolBinding(record)
}

func requireProtocolBindingCAS(binding storedProtocolBinding, generation, version int64) error {
	if binding.Generation != generation {
		return protocolBindingConflict("binding_generation_conflict")
	}
	if binding.Version != version {
		return protocolBindingConflict("binding_version_conflict")
	}
	return nil
}

func requireStableProtocolExternal(current, observed, name string) error {
	if current != "" && observed != "" && current != observed {
		return protocolBindingConflict(name + "_conflict")
	}
	return nil
}

func cloneProtocolInt(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func protocolBindingObservationEventType(verdict ProtocolObservationVerdict) string {
	if verdict == ProtocolObservationUnknown {
		return "work.binding.ambiguous"
	}
	return "work.binding.observed"
}

func reconcileProtocolBindingWork(
	ctx context.Context,
	sc store.Scope,
	tenant model.TenantID,
	binding storedProtocolBinding,
	eventType string,
	now model.Timestamp,
) (model.ID, model.ID, int64, error) {
	if err := advanceLeaseClock(
		ctx, sc, tenant, WorkCommand{WorkspaceID: binding.WorkspaceID}, now,
	); err != nil {
		return "", "", 0, protocolBindingUnknown("lease_clock_unavailable", err)
	}
	if err := lockWorkLeaseItem(
		ctx, sc, tenant, binding.WorkspaceID, binding.WorkItemID,
	); err != nil {
		return "", "", 0, protocolBindingUnknown("lease_coordination_unavailable", err)
	}
	items, err := sc.Ext(workItemKind)
	if err != nil {
		return "", "", 0, err
	}
	item, err := items.Get(ctx, binding.WorkItemID)
	if err != nil {
		return "", "", 0, err
	}
	if item.String(colWorkWorkspaceID) != binding.WorkspaceID.String() ||
		item.String(colWorkOwnerKind) != binding.OwnerKind ||
		item.String(colWorkOwnerRef) != binding.OwnerRef ||
		item.Int(colWorkOwnerEpoch) != binding.OwnerEpoch {
		return "", "", 0, protocolBindingConflict("work_authority_changed")
	}
	if eventType == "work.binding.cancel_requested" {
		item, err = applyProtocolBindingCancelLifecycle(ctx, sc, binding, item, now)
		if err != nil {
			return "", "", 0, err
		}
	} else {
		item, err = applyProtocolBindingWorkLifecycle(ctx, sc, binding, item, now)
		if err != nil {
			return "", "", 0, err
		}
	}
	return persistProtocolBindingWorkEvent(ctx, sc, tenant, binding, item, eventType, now)
}

func applyProtocolBindingWorkLifecycle(
	ctx context.Context,
	sc store.Scope,
	binding storedProtocolBinding,
	item model.Record,
	now model.Timestamp,
) (model.Record, error) {
	target := canonicalProtocolWorkState(binding.LocalState)
	if target == "" {
		return nil, protocolBindingInvalid("invalid_local_work_state")
	}
	if binding.Terminal && target == "active" {
		return nil, protocolBindingConflict("terminal_binding_keeps_active_work")
	}
	current := item.String(colWorkStatus)
	switch target {
	case "active":
		if current != "ready" && current != "active" &&
			!(current == "blocked" && protocolWorkInterruptCode(item.String(colWorkBlockedCode)) != "") {
			return nil, protocolBindingConflict("illegal_work_transition")
		}
		if err := renewProtocolBindingLease(ctx, sc, binding, now); err != nil {
			return nil, err
		}
		item[colWorkStatus], item[colWorkBlockedCode], item[colWorkBlockedReason] = "active", nil, nil
		if item.IsNull(colWorkStartedAt) {
			item[colWorkStartedAt] = now.String()
		}
	case "review":
		if current != "active" && current != "blocked" && current != "review" {
			return nil, protocolBindingConflict("illegal_work_transition")
		}
		ended, err := endProtocolBindingLeaseIfHeld(
			ctx, sc, binding, now, fenceReleased, "protocol_submitted_for_review",
		)
		if err != nil {
			return nil, err
		}
		if current == "active" && !ended {
			return nil, protocolBindingConflict("stale_lease_fence")
		}
		item[colWorkStatus], item[colWorkReviewAt] = "review", now.String()
		item[colWorkBlockedCode], item[colWorkBlockedReason] = nil, nil
	case "blocked":
		if current != "ready" && current != "active" && current != "review" && current != "blocked" {
			return nil, protocolBindingConflict("illegal_work_transition")
		}
		interruptCode := protocolBindingInterruptCode(binding)
		if binding.CancelRequested && !binding.Terminal {
			renewed, err := renewProtocolBindingLeaseIfHeld(ctx, sc, binding, now)
			if err != nil {
				return nil, err
			}
			if (current == "active" || item.String(colWorkBlockedCode) == "cancel_pending") && !renewed {
				return nil, protocolBindingConflict("stale_lease_fence")
			}
			item[colWorkBlockedCode] = "cancel_pending"
			item[colWorkBlockedReason] = "Protocol cancellation is pending remote confirmation."
		} else if interruptCode != "" {
			renewed, err := renewProtocolBindingLeaseIfHeld(ctx, sc, binding, now)
			if err != nil {
				return nil, err
			}
			if (current == "active" || protocolWorkInterruptCode(item.String(colWorkBlockedCode)) != "") && !renewed {
				return nil, protocolBindingConflict("stale_lease_fence")
			}
			item[colWorkBlockedCode] = interruptCode
			item[colWorkBlockedReason] = "Protocol peer requires local input or authorization."
		} else {
			ended, err := endProtocolBindingLeaseIfHeld(
				ctx, sc, binding, now, fenceRevoked, "protocol_binding_blocked",
			)
			if err != nil {
				return nil, err
			}
			if current == "active" && !ended {
				return nil, protocolBindingConflict("stale_lease_fence")
			}
			item[colWorkBlockedCode] = protocolWorkOutcomeCode(binding.ObservationVerdict)
			item[colWorkBlockedReason] = "Protocol binding observation: " + binding.ObservationCode + "."
		}
		item[colWorkStatus] = "blocked"
	case "canceled":
		if terminalWorkStatuses[current] && current != "canceled" {
			return nil, protocolBindingConflict("illegal_work_transition")
		}
		ended, err := endProtocolBindingLeaseIfHeld(
			ctx, sc, binding, now, fenceRevoked, "protocol_binding_canceled",
		)
		if err != nil {
			return nil, err
		}
		if current == "active" && !ended {
			return nil, protocolBindingConflict("stale_lease_fence")
		}
		item[colWorkStatus], item[colWorkTerminalAt] = "canceled", now.String()
		item[colWorkTerminalCode] = "protocol_canceled"
		item[colWorkTerminalReason] = "Protocol cancellation was observed."
		item[colWorkBlockedCode], item[colWorkBlockedReason] = nil, nil
	}
	return item, nil
}

func applyProtocolBindingCancelLifecycle(
	ctx context.Context,
	sc store.Scope,
	binding storedProtocolBinding,
	item model.Record,
	now model.Timestamp,
) (model.Record, error) {
	current := item.String(colWorkStatus)
	if current == "canceled" {
		return item, nil
	}
	if current != "ready" && current != "active" && current != "blocked" && current != "review" {
		return nil, protocolBindingConflict("illegal_work_transition")
	}
	renewed, err := renewProtocolBindingLeaseIfHeld(ctx, sc, binding, now)
	if err != nil {
		return nil, err
	}
	if (current == "active" || protocolWorkInterruptCode(item.String(colWorkBlockedCode)) != "") && !renewed {
		return nil, protocolBindingConflict("stale_lease_fence")
	}
	item[colWorkStatus] = "blocked"
	item[colWorkBlockedCode] = "cancel_pending"
	item[colWorkBlockedReason] = "Protocol cancellation is pending remote confirmation."
	return item, nil
}

func protocolBindingInterruptCode(binding storedProtocolBinding) string {
	if binding.ObservationVerdict != ProtocolObservationClean {
		return ""
	}
	for _, value := range []string{binding.RemoteState, binding.ObservationCode} {
		switch value {
		case "input_required", "remote_input_required":
			return "input_required"
		case "auth_required", "authorization_required", "remote_auth_required":
			return "auth_required"
		}
	}
	return ""
}

func protocolWorkInterruptCode(code string) string {
	switch code {
	case "input_required", "auth_required":
		return code
	default:
		return ""
	}
}

func canonicalProtocolWorkState(value string) string {
	switch value {
	case "ready", "reserved", "delegated", "registered", "working", "active":
		return "active"
	case "review", "blocked", "canceled":
		return value
	default:
		return ""
	}
}

func protocolWorkOutcomeCode(verdict ProtocolObservationVerdict) string {
	switch verdict {
	case ProtocolObservationBroken:
		return "protocol_broken"
	case ProtocolObservationUnknown:
		return "protocol_unknown"
	default:
		return "protocol_blocked"
	}
}

func protocolBindingLeaseHolder(binding storedProtocolBinding) (string, string, string) {
	runRef := "protocol:" + binding.AttemptID.String()
	agentRef := ""
	if binding.OwnerKind == "agent" {
		agentRef = binding.OwnerRef
	}
	return workLeaseHolderKey(binding.SyntheticSID, runRef, agentRef), runRef, agentRef
}

func renewProtocolBindingLease(
	ctx context.Context,
	sc store.Scope,
	binding storedProtocolBinding,
	now model.Timestamp,
) error {
	renewed, err := renewProtocolBindingLeaseIfHeld(ctx, sc, binding, now)
	if err != nil {
		return err
	}
	if !renewed {
		return protocolBindingConflict("stale_lease_fence")
	}
	return nil
}

func renewProtocolBindingLeaseIfHeld(
	ctx context.Context,
	sc store.Scope,
	binding storedProtocolBinding,
	now model.Timestamp,
) (bool, error) {
	lease, found, err := findWorkLease(ctx, sc, binding.WorkItemID)
	if err != nil {
		return false, err
	}
	if !found {
		return false, protocolBindingUnknown("work_lease_unavailable", nil)
	}
	state, err := workLeaseFenceState(lease)
	if err != nil {
		return false, err
	}
	holder, runRef, agentRef := protocolBindingLeaseHolder(binding)
	if state.Lifecycle != fenceActive {
		return false, nil
	}
	if state.Holder != holder || state.Fence != binding.LeaseFence {
		return false, protocolBindingConflict("stale_lease_fence")
	}
	state, err = fenceRenew(
		state, fenceToken{Holder: holder, Fence: binding.LeaseFence},
		now.Time(), defaultWorkLeaseTTL, workLeaseTTLPolicy,
	)
	if err != nil {
		return false, protocolBindingConflict("stale_lease_fence")
	}
	applyWorkLeaseFenceState(lease, state, binding.SyntheticSID, runRef, agentRef)
	repo, err := sc.Ext(workLeaseKind)
	if err != nil {
		return false, err
	}
	if _, err = repo.Update(ctx, lease); err != nil {
		return false, err
	}
	return true, nil
}

func endProtocolBindingLease(
	ctx context.Context,
	sc store.Scope,
	binding storedProtocolBinding,
	now model.Timestamp,
	lifecycle fenceLifecycle,
	reason string,
) error {
	ended, err := endProtocolBindingLeaseIfHeld(ctx, sc, binding, now, lifecycle, reason)
	if err != nil {
		return err
	}
	if !ended {
		return protocolBindingConflict("stale_lease_fence")
	}
	return nil
}

func endProtocolBindingLeaseIfHeld(
	ctx context.Context,
	sc store.Scope,
	binding storedProtocolBinding,
	now model.Timestamp,
	lifecycle fenceLifecycle,
	reason string,
) (bool, error) {
	lease, found, err := findWorkLease(ctx, sc, binding.WorkItemID)
	if err != nil {
		return false, err
	}
	if !found {
		return false, protocolBindingUnknown("work_lease_unavailable", nil)
	}
	state, err := workLeaseFenceState(lease)
	if err != nil {
		return false, err
	}
	holder, runRef, agentRef := protocolBindingLeaseHolder(binding)
	if state.Lifecycle != fenceActive {
		return false, nil
	}
	if state.Holder != holder || state.Fence != binding.LeaseFence {
		return false, protocolBindingConflict("stale_lease_fence")
	}
	state, err = fenceRelease(
		state, fenceToken{Holder: holder, Fence: binding.LeaseFence}, now.Time(), reason,
		fenceEndPolicy{Lifecycle: lifecycle, Bump: true, RequireLive: false},
	)
	if err != nil {
		return false, protocolBindingConflict("stale_lease_fence")
	}
	applyWorkLeaseFenceState(lease, state, binding.SyntheticSID, runRef, agentRef)
	repo, err := sc.Ext(workLeaseKind)
	if err != nil {
		return false, err
	}
	if _, err = repo.Update(ctx, lease); err != nil {
		return false, err
	}
	return true, nil
}

func cloneProtocolMCPTask(value *ProtocolMCPTaskProjection) *ProtocolMCPTaskProjection {
	if value == nil {
		return nil
	}
	copy := *value
	copy.TTLMs = cloneProtocolInt(value.TTLMs)
	copy.PollIntervalMs = cloneProtocolInt(value.PollIntervalMs)
	copy.InitialInputRequests = append(
		[]ProtocolInterruptRequestRef(nil), value.InitialInputRequests...,
	)
	return &copy
}

// persistProtocolBindingWorkEvent anchors a binding mutation in the WorkItem
// aggregate and its ordinary durable outbox. The binding row is the command
// receipt; WorkEvent is the immutable event receipt referenced by LastEventID.
func persistProtocolBindingWorkEvent(
	ctx context.Context,
	sc store.Scope,
	tenant model.TenantID,
	binding storedProtocolBinding,
	item model.Record,
	eventType string,
	now model.Timestamp,
) (model.ID, model.ID, int64, error) {
	if recordID(item) != binding.WorkItemID ||
		item.String(colWorkWorkspaceID) != binding.WorkspaceID.String() {
		return "", "", 0, protocolBindingConflict("work_binding_mismatch")
	}
	if eventType == "work.binding.reserved" {
		if item.String(colWorkStatus) != "ready" && item.String(colWorkStatus) != "active" {
			return "", "", 0, protocolBindingConflict("work_not_dispatchable")
		}
		item[colWorkStatus], item[colWorkBlockedCode], item[colWorkBlockedReason] = "active", nil, nil
		if item.IsNull(colWorkStartedAt) {
			item[colWorkStartedAt] = now.String()
		}
	}
	sequence := item.Int(colWorkLastEventSeq) + 1
	commandID, eventID := model.NewID(), model.NewID()
	payloadDoc := map[string]any{
		"binding_id": binding.ID.String(), "binding_spec_id": binding.BindingSpecID.String(),
		"binding_spec_generation": binding.BindingSpecGeneration,
		"binding_generation":      binding.Generation,
		"protocol":                string(binding.Protocol),
		"work_item_id":            binding.WorkItemID.String(),
		"workspace_id":            binding.WorkspaceID.String(),
		"work_status":             item.String(colWorkStatus),
		"lease_fence":             binding.LeaseFence,
		"verdict":                 string(binding.ObservationVerdict),
		"code":                    binding.ObservationCode,
		"terminal":                binding.Terminal,
		"event_seq":               sequence,
	}
	if binding.ExternalID != "" {
		payloadDoc["external_id_hash"] = protocolBindingHex(hashBytes([]byte(binding.ExternalID)))
	}
	payload, err := canonicalJSON(payloadDoc)
	if err != nil || len(payload) > 16*1024 {
		return "", "", 0, protocolBindingUnknown("binding_event_encoding_failed", err)
	}
	auditEvent, err := sc.Audit().Append(ctx, model.AuditDraft{
		Actor: "system:protocol-binding", ActorKind: model.ActorSystem,
		Action:     "sessions." + eventType,
		TargetKind: protocolBindingKind, TargetID: binding.ID,
		PayloadHash: hashBytes(payload),
		Meta: map[string]any{
			"workspace_id": binding.WorkspaceID.String(),
			"work_item_id": binding.WorkItemID.String(),
			"protocol":     string(binding.Protocol),
		},
	})
	if err != nil {
		return "", "", 0, err
	}
	if auditEvent.Seq == 0 {
		return "", "", 0, protocolBindingUnknown("audit_receipt_unavailable", nil)
	}
	item[colWorkLastEventSeq] = sequence
	items, err := sc.Ext(workItemKind)
	if err != nil {
		return "", "", 0, err
	}
	if _, err := items.Update(ctx, item); err != nil {
		return "", "", 0, err
	}
	events, err := sc.Ext(workEventKind)
	if err != nil {
		return "", "", 0, err
	}
	if _, err := events.Create(ctx, model.Record{
		colWorkWorkspaceID: binding.WorkspaceID.String(), colEventID: eventID.String(),
		colEventAggregateKind: string(workItemKind), colEventAggregateID: binding.WorkItemID.String(),
		colEventSeq: sequence, colEventType: eventType,
		colEventActorKind: string(model.ActorSystem), colEventActorRef: "protocol-binding",
		colEventOccurredAt: now.String(), colEventPayload: string(payload),
		colEventPayloadHash: hashBytes(payload), colEventCommandID: commandID.String(),
		colEventAuditSeq: auditEvent.Seq, colEventAuditHash: auditEvent.Hash,
	}); err != nil {
		return "", "", 0, err
	}
	outbox, err := sc.Ext(workOutboxKind)
	if err != nil {
		return "", "", 0, err
	}
	if _, err := outbox.Create(ctx, model.Record{
		colWorkWorkspaceID: binding.WorkspaceID.String(), colOutboxEventID: eventID.String(),
		colOutboxState: "pending", colOutboxAttempts: int64(0),
		colOutboxNextAttemptAt: now.String(), colOutboxClaimOwner: nil,
		colOutboxClaimUntil: nil, colOutboxPublishedAt: nil, colOutboxLastOutcome: nil,
	}); err != nil {
		return "", "", 0, err
	}
	return commandID, eventID, sequence, nil
}
