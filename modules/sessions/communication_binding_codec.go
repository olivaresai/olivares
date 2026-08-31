// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

const (
	maxProtocolMappingRules  = 128
	maxProtocolBindingLosses = 128
	maxProtocolRuleRefs      = 64
	maxProtocolSelectorBytes = 64 << 10
)

type storedProtocolBindingSpec struct {
	ProtocolBindingSpec
	commandKeyHash []byte
	requestHash    []byte
}

type storedProtocolBinding struct {
	ProtocolBinding
	dispatchKeyHash []byte
	reservationHash []byte
	lastUpdateHash  []byte
	cancelKeyHash   []byte
}

func (v BindingProtocol) valid() bool {
	return v == BindingProtocolA2A || v == BindingProtocolMCP
}

func (v BindingDirection) valid() bool {
	return v == BindingInbound || v == BindingOutbound || v == BindingBidirectional
}

func (v BindingLocalKind) valid() bool {
	return v == BindingLocalWorkItem || v == BindingLocalAgent ||
		v == BindingLocalModel || v == BindingLocalChannel
}

func (v ProtocolBindingSpecState) valid() bool {
	return v == ProtocolBindingSpecDraft || v == ProtocolBindingSpecActive ||
		v == ProtocolBindingSpecDisabled || v == ProtocolBindingSpecSuperseded
}

func (v ProtocolBindingSpecOperation) valid() bool {
	return v == ProtocolBindingSpecCreateDraft || v == ProtocolBindingSpecActivate ||
		v == ProtocolBindingSpecDisable
}

func (v ProtocolObservationVerdict) valid() bool {
	return v == ProtocolObservationClean || v == ProtocolObservationBroken ||
		v == ProtocolObservationUnknown
}

func (v ProtocolBindingResultKind) valid() bool {
	return v == ProtocolBindingResultTask || v == ProtocolBindingResultMessage
}

func (v ProtocolBindingResultKind) validReservation() bool {
	return v.valid() || v == ProtocolBindingResultTaskOrMessage
}

func (v ProtocolMappingCardinality) valid() bool {
	return v == ProtocolMappingOneToOne || v == ProtocolMappingOneToMany ||
		v == ProtocolMappingManyToOne
}

func (v ProtocolMappingTransform) valid() bool {
	return v == ProtocolTransformIdentity || v == ProtocolTransformText ||
		v == ProtocolTransformReference || v == ProtocolTransformMetadata ||
		v == ProtocolTransformStatus
}

func protocolBindingInvalid(code string) error {
	return fmt.Errorf("%w: %s", ErrInvalidProtocolBinding, code)
}

func protocolBindingConflict(code string) error {
	return fmt.Errorf("%w: %s", ErrProtocolBindingConflict, code)
}

func protocolBindingNotFound(code string) error {
	return fmt.Errorf("%w: %s", ErrProtocolBindingNotFound, code)
}

func protocolBindingUnknown(code string, cause error) error {
	if cause == nil {
		return fmt.Errorf("%w: %s", ErrProtocolBindingUnknown, code)
	}
	return fmt.Errorf("%w: %s: %v", ErrProtocolBindingUnknown, code, cause)
}

func normalizeProtocolAuthority(value string) (string, error) {
	value = strings.TrimSpace(value)
	if !validateOpaqueRef(value) {
		return "", protocolBindingInvalid("invalid_peer_authority")
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" {
		return value, nil
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Host == "" {
		return "", protocolBindingInvalid("invalid_peer_authority")
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	if parsed.Path == "/" {
		parsed.Path = ""
	} else {
		parsed.Path = strings.TrimSuffix(parsed.Path, "/")
	}
	return parsed.String(), nil
}

func canonicalProtocolSelector(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 || len(raw) > maxProtocolSelectorBytes {
		return nil, protocolBindingInvalid("invalid_local_selector")
	}
	canonical, err := canonicalJSON(raw)
	if err != nil || len(canonical) == 0 || canonical[0] != '{' {
		return nil, protocolBindingInvalid("invalid_local_selector")
	}
	return json.RawMessage(canonical), nil
}

func normalizeProtocolSpecInput(input ProtocolBindingSpecInput) (ProtocolBindingSpecInput, error) {
	if !validCanonicalCommunicationID(input.WorkspaceID) || input.Generation < 1 {
		return input, protocolBindingInvalid("invalid_spec_generation")
	}
	input.BindingKey = strings.ToLower(strings.TrimSpace(input.BindingKey))
	input.Protocol = BindingProtocol(strings.ToLower(strings.TrimSpace(string(input.Protocol))))
	input.Direction = BindingDirection(strings.ToLower(strings.TrimSpace(string(input.Direction))))
	input.LocalKind = BindingLocalKind(strings.ToLower(strings.TrimSpace(string(input.LocalKind))))
	input.ProtocolVersion = strings.TrimSpace(input.ProtocolVersion)
	input.RemoteResourceKind = strings.ToLower(strings.TrimSpace(input.RemoteResourceKind))
	input.RemoteResourceRef = strings.TrimSpace(input.RemoteResourceRef)
	input.MappingSchema = strings.TrimSpace(input.MappingSchema)
	input.PermissionProfileRef = strings.TrimSpace(input.PermissionProfileRef)
	input.CurrencyPolicy = BindingCurrencyPolicy(strings.ToLower(strings.TrimSpace(string(input.CurrencyPolicy))))
	input.Validation.Code = strings.TrimSpace(input.Validation.Code)
	if input.Validation.ObservedAt.IsZero() {
		input.Validation.ObservedAt = time.Time{}
	} else {
		input.Validation.ObservedAt = input.Validation.ObservedAt.UTC()
	}
	var err error
	input.PeerAuthority, err = normalizeProtocolAuthority(input.PeerAuthority)
	if err != nil {
		return input, err
	}
	input.LocalSelector, err = canonicalProtocolSelector(input.LocalSelector)
	if err != nil {
		return input, err
	}
	if !boundedToken(input.BindingKey, 128) || !input.Protocol.valid() ||
		!input.Direction.valid() || !input.LocalKind.valid() ||
		!validateOpaqueRef(input.ProtocolVersion) ||
		strings.EqualFold(input.ProtocolVersion, "latest") ||
		strings.EqualFold(input.ProtocolVersion, "current") || input.ProtocolVersion == "*" ||
		!boundedToken(input.RemoteResourceKind, 128) ||
		!validateOpaqueRef(input.RemoteResourceRef) || !validateOpaqueRef(input.MappingSchema) ||
		len(input.MappingSchema) > 128 ||
		!validateOpaqueRef(input.PermissionProfileRef) || input.CurrencyPolicy != BindingCurrencyPinned ||
		!input.Validation.Verdict.valid() || !boundedToken(input.Validation.Code, 128) {
		return input, protocolBindingInvalid("invalid_spec")
	}
	if len(input.Mapping) == 0 || len(input.Mapping) > maxProtocolMappingRules {
		return input, protocolBindingInvalid("invalid_mapping")
	}
	input.Mapping = append([]ProtocolMappingRule(nil), input.Mapping...)
	for i := range input.Mapping {
		rule := &input.Mapping[i]
		rule.Source = strings.TrimSpace(rule.Source)
		rule.Target = strings.TrimSpace(rule.Target)
		rule.Cardinality = ProtocolMappingCardinality(strings.ToLower(strings.TrimSpace(string(rule.Cardinality))))
		rule.Transform = ProtocolMappingTransform(strings.ToLower(strings.TrimSpace(string(rule.Transform))))
		if !validateOpaqueRef(rule.Source) || !validateOpaqueRef(rule.Target) ||
			!rule.Cardinality.valid() || !rule.Transform.valid() {
			return input, protocolBindingInvalid("invalid_mapping")
		}
	}
	sort.Slice(input.Mapping, func(i, j int) bool {
		if input.Mapping[i].Source != input.Mapping[j].Source {
			return input.Mapping[i].Source < input.Mapping[j].Source
		}
		if input.Mapping[i].Target != input.Mapping[j].Target {
			return input.Mapping[i].Target < input.Mapping[j].Target
		}
		if input.Mapping[i].Cardinality != input.Mapping[j].Cardinality {
			return input.Mapping[i].Cardinality < input.Mapping[j].Cardinality
		}
		return input.Mapping[i].Transform < input.Mapping[j].Transform
	})
	for i := 1; i < len(input.Mapping); i++ {
		if input.Mapping[i] == input.Mapping[i-1] {
			return input, protocolBindingInvalid("duplicate_mapping")
		}
	}
	if len(input.KnownLosses) > maxProtocolBindingLosses {
		return input, protocolBindingInvalid("invalid_known_losses")
	}
	input.KnownLosses = append([]ProtocolBindingLoss(nil), input.KnownLosses...)
	for i := range input.KnownLosses {
		loss := &input.KnownLosses[i]
		loss.Field = strings.TrimSpace(loss.Field)
		loss.ReasonCode = strings.ToLower(strings.TrimSpace(loss.ReasonCode))
		loss.AcceptanceRef = strings.TrimSpace(loss.AcceptanceRef)
		if !validateOpaqueRef(loss.Field) || !boundedToken(loss.ReasonCode, 128) ||
			(loss.Accepted && !validateOpaqueRef(loss.AcceptanceRef)) ||
			(!loss.Accepted && loss.AcceptanceRef != "") {
			return input, protocolBindingInvalid("invalid_known_losses")
		}
	}
	sort.Slice(input.KnownLosses, func(i, j int) bool {
		if input.KnownLosses[i].Field != input.KnownLosses[j].Field {
			return input.KnownLosses[i].Field < input.KnownLosses[j].Field
		}
		return input.KnownLosses[i].ReasonCode < input.KnownLosses[j].ReasonCode
	})
	for i := 1; i < len(input.KnownLosses); i++ {
		if input.KnownLosses[i].Field == input.KnownLosses[i-1].Field &&
			input.KnownLosses[i].ReasonCode == input.KnownLosses[i-1].ReasonCode {
			return input, protocolBindingInvalid("duplicate_known_loss")
		}
	}
	if len(input.RuleRefs) > maxProtocolRuleRefs {
		return input, protocolBindingInvalid("invalid_rule_refs")
	}
	input.RuleRefs = append([]string(nil), input.RuleRefs...)
	for i := range input.RuleRefs {
		input.RuleRefs[i] = strings.TrimSpace(input.RuleRefs[i])
		if !validateOpaqueRef(input.RuleRefs[i]) {
			return input, protocolBindingInvalid("invalid_rule_refs")
		}
	}
	sort.Strings(input.RuleRefs)
	for i := 1; i < len(input.RuleRefs); i++ {
		if input.RuleRefs[i] == input.RuleRefs[i-1] {
			return input, protocolBindingInvalid("duplicate_rule_ref")
		}
	}
	if !input.SupersedesID.IsZero() && !validCanonicalCommunicationID(input.SupersedesID) {
		return input, protocolBindingInvalid("invalid_supersedes_id")
	}
	if _, err := protocolLocalResourceID(input); err != nil {
		return input, err
	}
	if err := validateProtocolMappingContract(input); err != nil {
		return input, err
	}
	return input, nil
}

func protocolBindingHash(value any) ([]byte, error) {
	canonical, err := canonicalJSON(value)
	if err != nil {
		return nil, protocolBindingInvalid("canonicalization_failed")
	}
	digest := sha256.Sum256(canonical)
	return digest[:], nil
}

func protocolBindingSpecHashes(input ProtocolBindingSpecInput) (mapping, losses, spec []byte, err error) {
	mapping, err = protocolBindingHash(input.Mapping)
	if err != nil {
		return nil, nil, nil, err
	}
	losses, err = protocolBindingHash(input.KnownLosses)
	if err != nil {
		return nil, nil, nil, err
	}
	spec, err = protocolBindingHash(struct {
		BindingKey           string                `json:"binding_key"`
		Generation           int64                 `json:"generation"`
		Protocol             BindingProtocol       `json:"protocol"`
		ProtocolVersion      string                `json:"protocol_version"`
		Direction            BindingDirection      `json:"direction"`
		LocalKind            BindingLocalKind      `json:"local_kind"`
		LocalSelector        json.RawMessage       `json:"local_selector"`
		PeerAuthority        string                `json:"peer_authority"`
		RemoteResourceKind   string                `json:"remote_resource_kind"`
		RemoteResourceRef    string                `json:"remote_resource_ref"`
		MappingSchema        string                `json:"mapping_schema"`
		MappingHash          []byte                `json:"mapping_hash"`
		LossesHash           []byte                `json:"losses_hash"`
		RuleRefs             []string              `json:"rule_refs"`
		PermissionProfileRef string                `json:"permission_profile_ref"`
		CurrencyPolicy       BindingCurrencyPolicy `json:"currency_policy"`
		SupersedesID         model.ID              `json:"supersedes_id,omitempty"`
	}{
		BindingKey: input.BindingKey, Generation: input.Generation, Protocol: input.Protocol,
		ProtocolVersion: input.ProtocolVersion, Direction: input.Direction, LocalKind: input.LocalKind,
		LocalSelector: input.LocalSelector, PeerAuthority: input.PeerAuthority,
		RemoteResourceKind: input.RemoteResourceKind, RemoteResourceRef: input.RemoteResourceRef,
		MappingSchema: input.MappingSchema, MappingHash: mapping, LossesHash: losses,
		RuleRefs: input.RuleRefs, PermissionProfileRef: input.PermissionProfileRef,
		CurrencyPolicy: input.CurrencyPolicy, SupersedesID: input.SupersedesID,
	})
	return mapping, losses, spec, err
}

func encodeProtocolBindingSpec(value storedProtocolBindingSpec) (model.Record, error) {
	rec := mutableCommunicationRecord(value.MutableCommunicationEntity)
	rec[colBindingKey] = value.BindingKey
	rec[colCommGeneration] = value.Generation
	rec[colBindingProtocol] = string(value.Protocol)
	rec[colBindingProtocolVersion] = value.ProtocolVersion
	rec[colBindingDirection] = string(value.Direction)
	rec[colBindingLocalKind] = string(value.LocalKind)
	rec[colBindingLocalSelectorJSON] = string(value.LocalSelector)
	rec[colBindingPeerAuthority] = value.PeerAuthority
	rec[colBindingRemoteResourceKind] = value.RemoteResourceKind
	rec[colBindingRemoteResourceRef] = value.RemoteResourceRef
	rec[colBindingMappingSchema] = value.MappingSchema
	if err := setCanonicalCommunicationJSON(rec, colBindingMappingJSON, value.Mapping); err != nil {
		return nil, err
	}
	rec[colBindingMappingHash] = cloneCommunicationBytes(value.MappingHash)
	if err := setCanonicalCommunicationJSON(rec, colBindingKnownLossesJSON, value.KnownLosses); err != nil {
		return nil, err
	}
	rec[colBindingLossesHash] = cloneCommunicationBytes(value.LossesHash)
	if err := setCanonicalCommunicationJSON(rec, colBindingRuleRefsJSON, value.RuleRefs); err != nil {
		return nil, err
	}
	rec[colBindingPermissionProfileRef] = value.PermissionProfileRef
	rec[colBindingCurrencyPolicy] = string(value.CurrencyPolicy)
	rec[colBindingValidationVerdict] = string(value.Validation.Verdict)
	rec[colBindingValidationCode] = value.Validation.Code
	if value.Validation.ObservedAt.IsZero() {
		rec[colBindingValidatedAt] = nil
	} else {
		rec[colBindingValidatedAt] = model.NewTimestamp(value.Validation.ObservedAt).String()
	}
	rec[colBindingState] = string(value.State)
	rec[colCommSupersedesID] = optionalCommunicationID(value.SupersedesID)
	if value.State == ProtocolBindingSpecActive {
		rec[colBindingActiveSlot] = value.BindingKey
	} else {
		rec[colBindingActiveSlot] = nil
	}
	rec[colBindingSpecHash] = cloneCommunicationBytes(value.SpecHash)
	rec[colBindingPlanHash] = cloneCommunicationBytes(value.PlanHash)
	rec[colBindingCommandKeyHash] = cloneCommunicationBytes(value.commandKeyHash)
	rec[colBindingRequestHash] = cloneCommunicationBytes(value.requestHash)
	return rec, nil
}

func decodeProtocolBindingSpec(rec model.Record) (storedProtocolBindingSpec, error) {
	r := newCommunicationRecordReader(protocolBindingSpecKind, rec)
	value := storedProtocolBindingSpec{ProtocolBindingSpec: ProtocolBindingSpec{
		MutableCommunicationEntity: r.mutableEntity(), BindingKey: r.text(colBindingKey),
		Generation: r.integer(colCommGeneration), Protocol: BindingProtocol(r.text(colBindingProtocol)),
		ProtocolVersion: r.text(colBindingProtocolVersion), Direction: BindingDirection(r.text(colBindingDirection)),
		LocalKind: BindingLocalKind(r.text(colBindingLocalKind)), LocalSelector: r.canonicalJSON(colBindingLocalSelectorJSON),
		PeerAuthority: r.text(colBindingPeerAuthority), RemoteResourceKind: r.text(colBindingRemoteResourceKind),
		RemoteResourceRef: r.text(colBindingRemoteResourceRef), MappingSchema: r.text(colBindingMappingSchema),
		MappingHash: r.bytes(colBindingMappingHash), LossesHash: r.bytes(colBindingLossesHash),
		PermissionProfileRef: r.text(colBindingPermissionProfileRef),
		CurrencyPolicy:       BindingCurrencyPolicy(r.text(colBindingCurrencyPolicy)),
		Validation:           ProtocolBindingValidation{Verdict: ProtocolObservationVerdict(r.text(colBindingValidationVerdict)), Code: r.text(colBindingValidationCode)},
		State:                ProtocolBindingSpecState(r.text(colBindingState)), SupersedesID: r.optionalID(colCommSupersedesID),
		SpecHash: r.bytes(colBindingSpecHash), PlanHash: r.bytes(colBindingPlanHash),
	}, commandKeyHash: r.bytes(colBindingCommandKeyHash), requestHash: r.bytes(colBindingRequestHash)}
	mappingRaw := r.canonicalJSON(colBindingMappingJSON)
	lossesRaw := r.canonicalJSON(colBindingKnownLossesJSON)
	rulesRaw := r.canonicalJSON(colBindingRuleRefsJSON)
	r.decodeCanonicalJSON(colBindingMappingJSON, mappingRaw, &value.Mapping)
	r.decodeCanonicalJSON(colBindingKnownLossesJSON, lossesRaw, &value.KnownLosses)
	r.decodeCanonicalJSON(colBindingRuleRefsJSON, rulesRaw, &value.RuleRefs)
	if validated := r.optionalTimestamp(colBindingValidatedAt); validated != nil {
		value.Validation.ObservedAt = *validated
	}
	activeSlot := r.optionalText(colBindingActiveSlot)
	if r.err != nil {
		return storedProtocolBindingSpec{}, r.err
	}
	input, err := normalizeProtocolSpecInput(protocolBindingSpecInput(value.ProtocolBindingSpec))
	if err != nil {
		return storedProtocolBindingSpec{}, err
	}
	mappingHash, lossesHash, specHash, err := protocolBindingSpecHashes(input)
	if err != nil || !bytesEqual(mappingHash, value.MappingHash) || !bytesEqual(lossesHash, value.LossesHash) ||
		!bytesEqual(specHash, value.SpecHash) || len(value.PlanHash) != sha256.Size ||
		len(value.commandKeyHash) != sha256.Size || len(value.requestHash) != sha256.Size ||
		!value.State.valid() || (value.State == ProtocolBindingSpecActive) != (activeSlot == value.BindingKey) ||
		(value.State != ProtocolBindingSpecActive && activeSlot != "") {
		return storedProtocolBindingSpec{}, protocolBindingUnknown("corrupt_binding_spec", err)
	}
	return value, nil
}

func protocolBindingSpecInput(value ProtocolBindingSpec) ProtocolBindingSpecInput {
	return ProtocolBindingSpecInput{
		WorkspaceID: value.WorkspaceID, BindingKey: value.BindingKey, Generation: value.Generation,
		Protocol: value.Protocol, ProtocolVersion: value.ProtocolVersion, Direction: value.Direction,
		LocalKind: value.LocalKind, LocalSelector: value.LocalSelector, PeerAuthority: value.PeerAuthority,
		RemoteResourceKind: value.RemoteResourceKind, RemoteResourceRef: value.RemoteResourceRef,
		MappingSchema: value.MappingSchema, Mapping: value.Mapping, KnownLosses: value.KnownLosses,
		RuleRefs: value.RuleRefs, PermissionProfileRef: value.PermissionProfileRef,
		CurrencyPolicy: value.CurrencyPolicy, Validation: value.Validation, SupersedesID: value.SupersedesID,
	}
}

func protocolExternalActiveSlot(value ProtocolBinding) any {
	if value.Terminal || value.ExternalID == "" {
		return nil
	}
	return value.ExternalID
}

func encodeProtocolBinding(value storedProtocolBinding) model.Record {
	rec := mutableCommunicationRecord(value.MutableCommunicationEntity)
	rec[colBindingSpecID] = value.BindingSpecID.String()
	rec[colBindingSpecGeneration] = value.BindingSpecGeneration
	rec[colBindingPinnedSpecHash] = cloneCommunicationBytes(value.PinnedSpecHash)
	rec[colBindingPinnedMappingHash] = cloneCommunicationBytes(value.PinnedMappingHash)
	rec[colBindingPinnedLossesHash] = cloneCommunicationBytes(value.PinnedLossesHash)
	rec[colWorkItemID] = optionalCommunicationID(value.WorkItemID)
	rec[colCommMessageID] = optionalCommunicationID(value.MessageID)
	rec[colCommDeliveryID] = optionalCommunicationID(value.DeliveryID)
	rec[colBindingProtocol] = string(value.Protocol)
	rec[colBindingProtocolVersion] = value.ProtocolVersion
	rec[colBindingDirection] = string(value.Direction)
	rec[colBindingPeerAuthority] = value.PeerAuthority
	rec[colBindingRemoteResourceRef] = value.RemoteResourceRef
	rec[colBindingAttemptID] = value.AttemptID.String()
	rec[colBindingDispatchKeyHash] = cloneCommunicationBytes(value.dispatchKeyHash)
	rec[colBindingReservationHash] = cloneCommunicationBytes(value.reservationHash)
	rec[colCommGeneration] = value.Generation
	rec[colBindingSyntheticSID] = value.SyntheticSID
	rec[colCommOwnerKind] = optionalCommunicationText(value.OwnerKind)
	rec[colCommOwnerRef] = optionalCommunicationText(value.OwnerRef)
	rec[colBindingOwnerDigest] = optionalCommunicationBytes(value.OwnerDigest)
	rec[colBindingOwnerEpoch] = value.OwnerEpoch
	rec[colBindingLeaseFence] = value.LeaseFence
	rec[colBindingExternalKind] = value.ExternalKind
	rec[colBindingExternalID] = optionalCommunicationText(value.ExternalID)
	rec[colBindingContextID] = optionalCommunicationText(value.ContextID)
	rec[colBindingExternalMessageID] = optionalCommunicationText(value.ExternalMessageID)
	rec[colBindingLocalState] = value.LocalState
	rec[colBindingRemoteState] = value.RemoteState
	rec[colBindingRemoteRevision] = optionalCommunicationText(value.RemoteRevision)
	rec[colBindingObservationVerdict] = string(value.ObservationVerdict)
	rec[colBindingObservationCode] = value.ObservationCode
	rec[colBindingLastObservedAt] = optionalCommunicationTimestamp(value.LastObservedAt)
	rec[colBindingDetailHash] = optionalCommunicationBytes(value.DetailHash)
	rec[colBindingCurrentTTLMs] = optionalProtocolPositiveInt(value.CurrentTTLMs)
	rec[colBindingCurrentPollMs] = optionalProtocolPositiveInt(value.CurrentPollIntervalMs)
	rec[colBindingTerminal] = value.Terminal
	rec[colBindingExternalActiveSlot] = protocolExternalActiveSlot(value.ProtocolBinding)
	rec[colBindingLastUpdateHash] = optionalCommunicationBytes(value.lastUpdateHash)
	rec[colBindingCancelRequested] = value.CancelRequested
	rec[colBindingCancelRequestedAt] = optionalCommunicationTimestamp(value.CancelRequestedAt)
	rec[colBindingCancelReasonCode] = optionalCommunicationText(value.CancelReasonCode)
	rec[colBindingCancelKeyHash] = optionalCommunicationBytes(value.cancelKeyHash)
	if value.MCPTask == nil {
		rec[colBindingMCPTaskJSON] = nil
		rec[colBindingMCPTaskHash] = nil
	} else {
		_ = setCanonicalCommunicationJSON(rec, colBindingMCPTaskJSON, value.MCPTask)
		rec[colBindingMCPTaskHash] = cloneCommunicationBytes(value.MCPTaskHash)
	}
	rec[colBindingLastCommandID] = value.LastCommandID.String()
	rec[colBindingLastEventID] = value.LastEventID.String()
	rec[colBindingLastEventSeq] = value.LastEventSeq
	return rec
}

func decodeProtocolBinding(rec model.Record) (storedProtocolBinding, error) {
	r := newCommunicationRecordReader(protocolBindingKind, rec)
	value := storedProtocolBinding{ProtocolBinding: ProtocolBinding{
		MutableCommunicationEntity: r.mutableEntity(), BindingSpecID: r.id(colBindingSpecID),
		BindingSpecGeneration: r.integer(colBindingSpecGeneration), PinnedSpecHash: r.bytes(colBindingPinnedSpecHash),
		PinnedMappingHash: r.bytes(colBindingPinnedMappingHash), PinnedLossesHash: r.bytes(colBindingPinnedLossesHash),
		WorkItemID: r.optionalID(colWorkItemID), MessageID: r.optionalID(colCommMessageID),
		DeliveryID: r.optionalID(colCommDeliveryID), Protocol: BindingProtocol(r.text(colBindingProtocol)),
		ProtocolVersion: r.text(colBindingProtocolVersion), Direction: BindingDirection(r.text(colBindingDirection)),
		PeerAuthority: r.text(colBindingPeerAuthority), RemoteResourceRef: r.text(colBindingRemoteResourceRef),
		AttemptID:  r.id(colBindingAttemptID),
		Generation: r.integer(colCommGeneration), SyntheticSID: r.text(colBindingSyntheticSID),
		OwnerKind: r.optionalText(colCommOwnerKind), OwnerRef: r.optionalText(colCommOwnerRef),
		OwnerDigest: r.optionalBytes(colBindingOwnerDigest), OwnerEpoch: r.integer(colBindingOwnerEpoch),
		LeaseFence: r.integer(colBindingLeaseFence), ExternalKind: r.text(colBindingExternalKind),
		ExternalID: r.optionalText(colBindingExternalID), ContextID: r.optionalText(colBindingContextID),
		ExternalMessageID: r.optionalText(colBindingExternalMessageID), LocalState: r.text(colBindingLocalState),
		RemoteState: r.text(colBindingRemoteState), RemoteRevision: r.optionalText(colBindingRemoteRevision),
		ObservationVerdict: ProtocolObservationVerdict(r.text(colBindingObservationVerdict)),
		ObservationCode:    r.text(colBindingObservationCode), DetailHash: r.optionalBytes(colBindingDetailHash),
		CurrentTTLMs:          optionalProtocolIntPointer(r.optionalPositiveInteger(colBindingCurrentTTLMs)),
		CurrentPollIntervalMs: optionalProtocolIntPointer(r.optionalPositiveInteger(colBindingCurrentPollMs)),
		Terminal:              r.boolean(colBindingTerminal), CancelRequested: r.boolean(colBindingCancelRequested),
		CancelReasonCode: r.optionalText(colBindingCancelReasonCode), MCPTaskHash: r.optionalBytes(colBindingMCPTaskHash),
		LastCommandID: r.id(colBindingLastCommandID), LastEventID: r.id(colBindingLastEventID),
		LastEventSeq: r.integer(colBindingLastEventSeq),
	}, dispatchKeyHash: r.bytes(colBindingDispatchKeyHash), reservationHash: r.bytes(colBindingReservationHash),
		lastUpdateHash: r.optionalBytes(colBindingLastUpdateHash), cancelKeyHash: r.optionalBytes(colBindingCancelKeyHash)}
	value.LastObservedAt = r.optionalTimestamp(colBindingLastObservedAt)
	value.CancelRequestedAt = r.optionalTimestamp(colBindingCancelRequestedAt)
	mcpTaskRaw := r.optionalCanonicalJSON(colBindingMCPTaskJSON)
	if len(mcpTaskRaw) != 0 {
		var projection ProtocolMCPTaskProjection
		r.decodeCanonicalJSON(colBindingMCPTaskJSON, mcpTaskRaw, &projection)
		value.MCPTask = &projection
		value.ProtocolMetadataJSON = append(json.RawMessage(nil), mcpTaskRaw...)
	}
	activeSlot := r.optionalText(colBindingExternalActiveSlot)
	if r.err != nil {
		return storedProtocolBinding{}, r.err
	}
	if value.MCPTask != nil {
		projection, canonical, err := normalizeProtocolMCPMetadata(value.MCPTask, mcpTaskRaw)
		if err != nil || !bytesEqual(hashBytes(canonical), value.MCPTaskHash) {
			return storedProtocolBinding{}, protocolBindingUnknown("corrupt_protocol_metadata", err)
		}
		value.MCPTask = projection
		value.ProtocolMetadataJSON = canonical
	}
	if !value.Protocol.valid() || !value.Direction.valid() || value.BindingSpecGeneration < 1 ||
		len(value.PinnedSpecHash) != sha256.Size || len(value.PinnedMappingHash) != sha256.Size ||
		len(value.PinnedLossesHash) != sha256.Size || !validCanonicalCommunicationID(value.AttemptID) ||
		len(value.dispatchKeyHash) != sha256.Size || len(value.reservationHash) != sha256.Size ||
		value.Generation < 1 || !validCanonicalSID(value.SyntheticSID) ||
		!validateOpaqueRef(value.ExternalKind) || !validateOpaqueRef(value.LocalState) ||
		!validateOpaqueRef(value.RemoteResourceRef) ||
		!validateOpaqueRef(value.RemoteState) || !value.ObservationVerdict.valid() ||
		!boundedToken(value.ObservationCode, 128) ||
		(value.ExternalID != "" && !validateOpaqueRef(value.ExternalID)) ||
		(value.ContextID != "" && !validateOpaqueRef(value.ContextID)) ||
		(value.ExternalMessageID != "" && !validateOpaqueRef(value.ExternalMessageID)) ||
		(value.RemoteRevision != "" && !validateOpaqueRef(value.RemoteRevision)) ||
		(len(value.OwnerDigest) != 0 && len(value.OwnerDigest) != sha256.Size) ||
		(len(value.DetailHash) != 0 && len(value.DetailHash) != sha256.Size) ||
		(len(value.lastUpdateHash) != 0 && len(value.lastUpdateHash) != sha256.Size) ||
		(len(value.cancelKeyHash) != 0 && len(value.cancelKeyHash) != sha256.Size) ||
		((value.MCPTask == nil) != (len(value.MCPTaskHash) == 0)) ||
		(value.MCPTask != nil && !bytesEqual(hashBytes(value.ProtocolMetadataJSON), value.MCPTaskHash)) ||
		!validCanonicalCommunicationID(value.LastCommandID) ||
		!validCanonicalCommunicationID(value.LastEventID) || value.LastEventSeq < 1 ||
		(value.CancelRequested != (value.CancelRequestedAt != nil)) ||
		(value.CancelRequested != (value.CancelReasonCode != "")) ||
		(activeSlot != "" && activeSlot != value.ExternalID) ||
		(value.Terminal && activeSlot != "") || (!value.Terminal && value.ExternalID != "" && activeSlot == "") {
		return storedProtocolBinding{}, protocolBindingUnknown("corrupt_protocol_binding", nil)
	}
	return value, nil
}

func protocolBindingHex(value []byte) string { return hex.EncodeToString(value) }

func optionalProtocolPositiveInt(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func optionalProtocolIntPointer(value int64) *int64 {
	if value == 0 {
		return nil
	}
	copy := value
	return &copy
}
