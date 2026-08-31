// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/olivaresai/olivares/core/model"
)

// ProtocolBindingMappingSchemaV1 is the first closed, executable-free mapping
// vocabulary. A schema name outside this catalog is not treated as an opaque
// extension: it is refused until the binary contains a reader and evaluator for
// it.
const ProtocolBindingMappingSchemaV1 = "olivares.protocol-binding/v1"

type protocolMappingValueKind uint8

const (
	protocolMappingText protocolMappingValueKind = iota + 1
	protocolMappingReference
	protocolMappingMetadata
	protocolMappingStatus
)

type protocolMappingField struct {
	kind     protocolMappingValueKind
	required bool
}

type protocolMappingRoute struct {
	sources map[string]protocolMappingField
	targets map[string]protocolMappingField
}

// ProtocolBindingRuntimeExpectation is the operator-owned tuple an adapter
// expects before it evaluates a mapping or performs an effect. RuleRefs and
// PermissionProfileRef are compared exactly, so they are live runtime inputs
// rather than decorative values covered only by SpecHash.
type ProtocolBindingRuntimeExpectation struct {
	TenantID             model.TenantID
	WorkspaceID          model.ID
	SpecID               model.ID
	Generation           int64
	Protocol             BindingProtocol
	ProtocolVersion      string
	Direction            BindingDirection
	LocalKind            BindingLocalKind
	PeerAuthority        string
	RemoteResourceKind   string
	RemoteResourceRef    string
	RuleRefs             []string
	PermissionProfileRef string
}

// ProtocolMappingEvaluation is the bounded result of the allowlisted mapping
// evaluator. Values contains only catalog target fields. EvidenceHash commits
// to the exact active generation, mapping, policy refs and resulting values.
type ProtocolMappingEvaluation struct {
	Values               map[string]any `json:"values"`
	SpecHash             string         `json:"spec_hash"`
	MappingHash          string         `json:"mapping_hash"`
	RuleRefs             []string       `json:"rule_refs"`
	PermissionProfileRef string         `json:"permission_profile_ref"`
	EvidenceHash         string         `json:"evidence_hash"`
}

// ProtocolBindingSpecDigests are the canonical desired-state commitments used
// by persistence and by composition contract fixtures.
type ProtocolBindingSpecDigests struct {
	MappingHash []byte
	LossesHash  []byte
	SpecHash    []byte
}

// ComputeProtocolBindingSpecDigests validates and canonicalizes desired state
// before returning the same commitments persistence will store.
func ComputeProtocolBindingSpecDigests(input ProtocolBindingSpecInput) (ProtocolBindingSpecDigests, error) {
	normalized, err := normalizeProtocolSpecInput(input)
	if err != nil {
		return ProtocolBindingSpecDigests{}, err
	}
	mapping, losses, spec, err := protocolBindingSpecHashes(normalized)
	if err != nil {
		return ProtocolBindingSpecDigests{}, err
	}
	return ProtocolBindingSpecDigests{
		MappingHash: append([]byte(nil), mapping...),
		LossesHash:  append([]byte(nil), losses...),
		SpecHash:    append([]byte(nil), spec...),
	}, nil
}

// EvaluateProtocolBindingMapping verifies one exact active generation and
// evaluates only its declarative, allowlisted rules against source.
func EvaluateProtocolBindingMapping(
	spec ProtocolBindingSpec,
	expect ProtocolBindingRuntimeExpectation,
	source map[string]any,
) (ProtocolMappingEvaluation, error) {
	if err := validateProtocolBindingRuntimeSpec(spec, expect); err != nil {
		return ProtocolMappingEvaluation{}, err
	}
	values, err := evaluateProtocolMappingRules(
		protocolBindingSpecInput(spec), expect.Direction, source,
	)
	if err != nil {
		return ProtocolMappingEvaluation{}, err
	}
	evidence, err := protocolBindingHash(struct {
		SpecID               model.ID `json:"spec_id"`
		Generation           int64    `json:"generation"`
		SpecHash             []byte   `json:"spec_hash"`
		MappingHash          []byte   `json:"mapping_hash"`
		RuleRefs             []string `json:"rule_refs"`
		PermissionProfileRef string   `json:"permission_profile_ref"`
		Values               any      `json:"values"`
	}{
		spec.ID, spec.Generation, spec.SpecHash, spec.MappingHash,
		append([]string(nil), spec.RuleRefs...), spec.PermissionProfileRef, values,
	})
	if err != nil {
		return ProtocolMappingEvaluation{}, err
	}
	return ProtocolMappingEvaluation{
		Values: values, SpecHash: hex.EncodeToString(spec.SpecHash),
		MappingHash:          hex.EncodeToString(spec.MappingHash),
		RuleRefs:             append([]string(nil), spec.RuleRefs...),
		PermissionProfileRef: spec.PermissionProfileRef,
		EvidenceHash:         hex.EncodeToString(evidence),
	}, nil
}

// PreviewProtocolBindingMapping runs the same evaluator for desired state. It
// is used by the protocol composer after a server-owned local-resource resolver
// has supplied the authoritative source projection; it does not assert that a
// draft is active.
func PreviewProtocolBindingMapping(
	input ProtocolBindingSpecInput,
	runtimeDirection BindingDirection,
	source map[string]any,
) (map[string]any, error) {
	normalized, err := normalizeProtocolSpecInput(input)
	if err != nil {
		return nil, err
	}
	return evaluateProtocolMappingRules(normalized, runtimeDirection, source)
}

func validateProtocolBindingRuntimeSpec(
	spec ProtocolBindingSpec,
	expect ProtocolBindingRuntimeExpectation,
) error {
	if spec.State != ProtocolBindingSpecActive || spec.ID.IsZero() || spec.ID != expect.SpecID ||
		spec.TenantID != expect.TenantID || expect.TenantID.IsZero() || expect.TenantID.IsSystem() ||
		spec.WorkspaceID != expect.WorkspaceID || spec.Generation != expect.Generation ||
		spec.Protocol != expect.Protocol || spec.ProtocolVersion != strings.TrimSpace(expect.ProtocolVersion) ||
		spec.LocalKind != expect.LocalKind ||
		spec.PeerAuthority != strings.TrimSpace(expect.PeerAuthority) ||
		spec.RemoteResourceKind != strings.ToLower(strings.TrimSpace(expect.RemoteResourceKind)) ||
		spec.RemoteResourceRef != strings.TrimSpace(expect.RemoteResourceRef) ||
		(expect.Direction != BindingInbound && expect.Direction != BindingOutbound) ||
		(spec.Direction != expect.Direction && spec.Direction != BindingBidirectional) {
		return protocolBindingConflict("runtime_spec_pin_mismatch")
	}
	wantRules := canonicalProtocolPolicyRefs(expect.RuleRefs)
	if wantRules == nil || !reflect.DeepEqual(spec.RuleRefs, wantRules) ||
		spec.PermissionProfileRef != strings.TrimSpace(expect.PermissionProfileRef) {
		return protocolBindingConflict("runtime_policy_pin_mismatch")
	}
	input, err := normalizeProtocolSpecInput(protocolBindingSpecInput(spec))
	if err != nil {
		return protocolBindingUnknown("runtime_spec_corrupt", err)
	}
	mappingHash, lossesHash, specHash, err := protocolBindingSpecHashes(input)
	if err != nil || len(spec.MappingHash) != 32 || len(spec.LossesHash) != 32 || len(spec.SpecHash) != 32 ||
		!bytes.Equal(mappingHash, spec.MappingHash) || !bytes.Equal(lossesHash, spec.LossesHash) ||
		!bytes.Equal(specHash, spec.SpecHash) {
		return protocolBindingUnknown("runtime_spec_hash_mismatch", err)
	}
	return nil
}

func canonicalProtocolPolicyRefs(values []string) []string {
	out := append([]string(nil), values...)
	for i := range out {
		out[i] = strings.TrimSpace(out[i])
		if !validateOpaqueRef(out[i]) {
			return nil
		}
	}
	sort.Strings(out)
	for i := 1; i < len(out); i++ {
		if out[i] == out[i-1] {
			return nil
		}
	}
	if out == nil {
		out = []string{}
	}
	return out
}

func validateProtocolMappingContract(input ProtocolBindingSpecInput) error {
	if input.MappingSchema != ProtocolBindingMappingSchemaV1 {
		return protocolBindingInvalid("unsupported_mapping_schema")
	}
	routes, err := protocolMappingRoutes(input.Protocol, input.Direction, input.LocalKind)
	if err != nil {
		return err
	}
	coveredSources := make([]map[string]bool, len(routes))
	coveredTargets := make([]map[string]bool, len(routes))
	for i := range routes {
		coveredSources[i], coveredTargets[i] = map[string]bool{}, map[string]bool{}
	}
	seenTargets := make(map[string]struct{}, len(input.Mapping))
	for _, rule := range input.Mapping {
		matched := false
		for routeIndex, route := range routes {
			source, sourceOK := route.sources[rule.Source]
			target, targetOK := route.targets[rule.Target]
			if !sourceOK || !targetOK {
				continue
			}
			if !protocolMappingTransformAllowed(source.kind, target.kind, rule) {
				return protocolBindingInvalid("mapping_transform_not_allowed")
			}
			coveredSources[routeIndex][rule.Source] = true
			coveredTargets[routeIndex][rule.Target] = true
			matched = true
		}
		if !matched {
			return protocolBindingInvalid("mapping_field_not_in_schema")
		}
		if _, duplicate := seenTargets[rule.Target]; duplicate {
			return protocolBindingInvalid("mapping_target_duplicated")
		}
		seenTargets[rule.Target] = struct{}{}
	}
	acceptedLosses := make(map[string]bool, len(input.KnownLosses))
	for _, loss := range input.KnownLosses {
		known := false
		for _, route := range routes {
			_, sourceKnown := route.sources[loss.Field]
			_, targetKnown := route.targets[loss.Field]
			known = known || sourceKnown || targetKnown
		}
		if !known {
			return protocolBindingInvalid("loss_field_not_in_schema")
		}
		if loss.Accepted && validateOpaqueRef(loss.AcceptanceRef) {
			acceptedLosses[loss.Field] = true
		}
	}
	for routeIndex, route := range routes {
		for field, definition := range route.sources {
			if definition.required && !coveredSources[routeIndex][field] && !acceptedLosses[field] {
				return protocolBindingInvalid("required_mapping_or_loss_missing")
			}
		}
		for field, definition := range route.targets {
			if definition.required && !coveredTargets[routeIndex][field] && !acceptedLosses[field] {
				return protocolBindingInvalid("required_mapping_or_loss_missing")
			}
		}
	}
	return nil
}

func protocolMappingTransformAllowed(
	source, target protocolMappingValueKind,
	rule ProtocolMappingRule,
) bool {
	switch rule.Transform {
	case ProtocolTransformIdentity:
		return source == target && rule.Cardinality == ProtocolMappingOneToOne
	case ProtocolTransformText:
		return target == protocolMappingText
	case ProtocolTransformReference:
		return target == protocolMappingReference &&
			(source == protocolMappingText || source == protocolMappingReference) &&
			rule.Cardinality != ProtocolMappingManyToOne
	case ProtocolTransformMetadata:
		return source == protocolMappingMetadata && target == protocolMappingMetadata &&
			rule.Cardinality != ProtocolMappingOneToMany
	case ProtocolTransformStatus:
		return source == protocolMappingStatus && target == protocolMappingStatus &&
			rule.Cardinality == ProtocolMappingOneToOne
	default:
		return false
	}
}

func protocolMappingRoutes(
	protocol BindingProtocol,
	direction BindingDirection,
	localKind BindingLocalKind,
) ([]protocolMappingRoute, error) {
	directions := []BindingDirection{direction}
	if direction == BindingBidirectional {
		directions = []BindingDirection{BindingOutbound, BindingInbound}
	}
	routes := make([]protocolMappingRoute, 0, len(directions))
	for _, runtimeDirection := range directions {
		route, err := protocolMappingRouteFor(protocol, runtimeDirection, localKind)
		if err != nil {
			return nil, err
		}
		routes = append(routes, route)
	}
	return routes, nil
}

func protocolMappingRouteFor(
	protocol BindingProtocol,
	direction BindingDirection,
	localKind BindingLocalKind,
) (protocolMappingRoute, error) {
	local, primary, sink, err := protocolLocalMappingFields(localKind)
	if err != nil {
		return protocolMappingRoute{}, err
	}
	markRequired := func(fields map[string]protocolMappingField, name string) {
		field := fields[name]
		field.required = true
		fields[name] = field
	}
	switch protocol {
	case BindingProtocolA2A:
		message := protocolA2AMappingFields()
		if direction == BindingOutbound {
			markRequired(local, primary)
			markRequired(message, "message.text")
			return protocolMappingRoute{sources: local, targets: message}, nil
		}
		if direction == BindingInbound {
			markRequired(message, "message.text")
			markRequired(local, sink)
			return protocolMappingRoute{sources: message, targets: local}, nil
		}
	case BindingProtocolMCP:
		if direction == BindingOutbound {
			task := protocolMCPMappingFields()
			markRequired(task, "task.summary")
			markRequired(local, sink)
			return protocolMappingRoute{sources: task, targets: local}, nil
		}
	}
	return protocolMappingRoute{}, protocolBindingInvalid("unsupported_mapping_route")
}

func protocolLocalMappingFields(
	kind BindingLocalKind,
) (map[string]protocolMappingField, string, string, error) {
	text, ref, metadata, status := protocolMappingField{kind: protocolMappingText},
		protocolMappingField{kind: protocolMappingReference},
		protocolMappingField{kind: protocolMappingMetadata},
		protocolMappingField{kind: protocolMappingStatus}
	switch kind {
	case BindingLocalWorkItem:
		return map[string]protocolMappingField{
			"work.id": ref, "work.workspace_id": ref, "work.title": text,
			"work.brief": text, "work.status": status, "work.kind": text,
			"work.owner_ref": ref, "work.priority": text, "work.context_refs": metadata,
		}, "work.title", "work.brief", nil
	case BindingLocalAgent:
		return map[string]protocolMappingField{
			"agent.id": ref, "agent.workspace_id": ref, "agent.name": text,
			"agent.kind": text, "agent.status": status, "agent.external_id": ref,
			"agent.identity_id": ref, "agent.labels": metadata, "agent.metadata": metadata,
		}, "agent.name", "agent.metadata", nil
	case BindingLocalModel:
		return map[string]protocolMappingField{
			"model.id": ref, "model.name": text, "model.family": text,
			"model.status": status, "model.provider_id": ref, "model.modality": text,
			"model.context_window": text, "model.metadata": metadata,
		}, "model.name", "model.metadata", nil
	case BindingLocalChannel:
		return map[string]protocolMappingField{
			"channel.id": ref, "channel.workspace_id": ref, "channel.name": text,
			"channel.description": text, "channel.kind": text, "channel.status": status,
			"channel.sensitivity": text, "channel.retention_policy_ref": ref,
			"channel.metadata": metadata,
		}, "channel.name", "channel.metadata", nil
	default:
		return nil, "", "", protocolBindingInvalid("unsupported_local_mapping_kind")
	}
}

func protocolA2AMappingFields() map[string]protocolMappingField {
	return map[string]protocolMappingField{
		"message.text":       {kind: protocolMappingText},
		"message.parts":      {kind: protocolMappingText},
		"message.id":         {kind: protocolMappingReference},
		"message.context_id": {kind: protocolMappingReference},
		"message.reference":  {kind: protocolMappingReference},
		"message.metadata":   {kind: protocolMappingMetadata},
		"message.status":     {kind: protocolMappingStatus},
		"peer.subject":       {kind: protocolMappingReference},
	}
}

func protocolMCPMappingFields() map[string]protocolMappingField {
	return map[string]protocolMappingField{
		"task.id":             {kind: protocolMappingReference},
		"task.tool":           {kind: protocolMappingText},
		"task.summary":        {kind: protocolMappingText},
		"task.status":         {kind: protocolMappingStatus},
		"task.owner":          {kind: protocolMappingMetadata},
		"task.required_scope": {kind: protocolMappingReference},
		"task.origin_ref":     {kind: protocolMappingReference},
		"task.metadata":       {kind: protocolMappingMetadata},
	}
}

func evaluateProtocolMappingRules(
	input ProtocolBindingSpecInput,
	runtimeDirection BindingDirection,
	source map[string]any,
) (map[string]any, error) {
	if runtimeDirection != BindingInbound && runtimeDirection != BindingOutbound {
		return nil, protocolBindingInvalid("invalid_runtime_mapping_direction")
	}
	if input.Direction != runtimeDirection && input.Direction != BindingBidirectional {
		return nil, protocolBindingConflict("runtime_mapping_direction_mismatch")
	}
	route, err := protocolMappingRouteFor(input.Protocol, runtimeDirection, input.LocalKind)
	if err != nil {
		return nil, err
	}
	values := make(map[string]any)
	for _, rule := range input.Mapping {
		sourceField, sourceInRoute := route.sources[rule.Source]
		if !sourceInRoute {
			continue
		}
		targetField, targetInRoute := route.targets[rule.Target]
		if !targetInRoute {
			continue
		}
		raw, ok := source[rule.Source]
		if !ok {
			return nil, protocolBindingInvalid("mapping_source_unavailable")
		}
		mapped, err := evaluateProtocolMappingValue(
			input.Protocol, runtimeDirection, rule, sourceField.kind, targetField.kind, raw,
		)
		if err != nil {
			return nil, err
		}
		if _, duplicate := values[rule.Target]; duplicate {
			return nil, protocolBindingInvalid("mapping_target_duplicated")
		}
		values[rule.Target] = mapped
	}
	return values, nil
}

func evaluateProtocolMappingValue(
	protocol BindingProtocol,
	direction BindingDirection,
	rule ProtocolMappingRule,
	sourceKind protocolMappingValueKind,
	targetKind protocolMappingValueKind,
	raw any,
) (any, error) {
	if rule.Cardinality == ProtocolMappingManyToOne {
		items, ok := protocolMappingSlice(raw)
		if !ok || len(items) == 0 {
			return nil, protocolBindingInvalid("mapping_cardinality_mismatch")
		}
		switch rule.Transform {
		case ProtocolTransformText:
			parts := make([]string, 0, len(items))
			for _, item := range items {
				value, err := protocolMappingTextValue(item)
				if err != nil {
					return nil, err
				}
				parts = append(parts, value)
			}
			return strings.Join(parts, "\n\n"), nil
		case ProtocolTransformMetadata:
			merged := map[string]any{}
			for _, item := range items {
				object, err := protocolMappingMetadataValue(item)
				if err != nil {
					return nil, err
				}
				for key, value := range object {
					if _, duplicate := merged[key]; duplicate {
						return nil, protocolBindingInvalid("mapping_metadata_key_conflict")
					}
					merged[key] = value
				}
			}
			return merged, nil
		default:
			return nil, protocolBindingInvalid("mapping_cardinality_not_allowed")
		}
	}
	if _, isSlice := protocolMappingSlice(raw); isSlice {
		return nil, protocolBindingInvalid("mapping_cardinality_mismatch")
	}
	value, err := evaluateProtocolMappingScalar(
		protocol, direction, rule.Transform, sourceKind, targetKind, raw,
	)
	if err != nil {
		return nil, err
	}
	if rule.Cardinality == ProtocolMappingOneToMany {
		return []any{value}, nil
	}
	return value, nil
}

func evaluateProtocolMappingScalar(
	protocol BindingProtocol,
	direction BindingDirection,
	transform ProtocolMappingTransform,
	sourceKind protocolMappingValueKind,
	targetKind protocolMappingValueKind,
	raw any,
) (any, error) {
	switch transform {
	case ProtocolTransformIdentity:
		if sourceKind != targetKind {
			return nil, protocolBindingInvalid("mapping_identity_kind_mismatch")
		}
		return protocolMappingIdentityValue(targetKind, raw)
	case ProtocolTransformText:
		return protocolMappingTextValue(raw)
	case ProtocolTransformReference:
		value, err := protocolMappingTextValue(raw)
		if err != nil || !validateOpaqueRef(value) {
			return nil, protocolBindingInvalid("mapping_reference_invalid")
		}
		return value, nil
	case ProtocolTransformMetadata:
		return protocolMappingMetadataValue(raw)
	case ProtocolTransformStatus:
		value, err := protocolMappingTextValue(raw)
		if err != nil {
			return nil, err
		}
		return protocolMappingStatusValue(protocol, direction, value)
	default:
		return nil, protocolBindingInvalid("mapping_transform_not_allowed")
	}
}

func protocolMappingTextValue(value any) (string, error) {
	var result string
	switch typed := value.(type) {
	case string:
		result = typed
	case fmt.Stringer:
		result = typed.String()
	case json.Number:
		result = typed.String()
	case int:
		result = strconv.Itoa(typed)
	case int64:
		result = strconv.FormatInt(typed, 10)
	case float64:
		result = strconv.FormatFloat(typed, 'g', -1, 64)
	case bool:
		result = strconv.FormatBool(typed)
	default:
		return "", protocolBindingInvalid("mapping_text_invalid")
	}
	if result == "" {
		return "", protocolBindingInvalid("mapping_text_empty")
	}
	return result, nil
}

func protocolMappingIdentityValue(kind protocolMappingValueKind, value any) (any, error) {
	switch kind {
	case protocolMappingText, protocolMappingStatus:
		text, ok := value.(string)
		if !ok || text == "" {
			return nil, protocolBindingInvalid("mapping_identity_invalid")
		}
		return text, nil
	case protocolMappingReference:
		text, ok := value.(string)
		if !ok || !validateOpaqueRef(text) {
			return nil, protocolBindingInvalid("mapping_identity_invalid")
		}
		return text, nil
	case protocolMappingMetadata:
		return protocolMappingMetadataValue(value)
	default:
		return nil, protocolBindingInvalid("mapping_identity_invalid")
	}
}

func protocolMappingMetadataValue(value any) (map[string]any, error) {
	canonical, err := canonicalJSON(value)
	if err != nil || len(canonical) == 0 || canonical[0] != '{' {
		return nil, protocolBindingInvalid("mapping_metadata_invalid")
	}
	var result map[string]any
	decoder := json.NewDecoder(bytes.NewReader(canonical))
	decoder.UseNumber()
	if err := decoder.Decode(&result); err != nil || result == nil {
		return nil, protocolBindingInvalid("mapping_metadata_invalid")
	}
	return result, nil
}

func protocolMappingStatusValue(
	protocol BindingProtocol,
	direction BindingDirection,
	value string,
) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	var statuses map[string]string
	// A2A outbound maps local Work state to the peer. MCP durable task
	// registration is modeled as an outbound Resource Server capability, but
	// its mapping source is the remote task and its target is local Work, so its
	// status projection follows the inbound table.
	if direction == BindingOutbound && protocol != BindingProtocolMCP {
		statuses = map[string]string{
			"draft": "submitted", "ready": "submitted", "submitted": "submitted",
			"active": "working", "working": "working", "review": "working",
			"blocked": "input_required", "input_required": "input_required",
			"auth_required": "auth_required", "completed": "completed",
			"failed": "failed", "canceled": "canceled", "rejected": "rejected",
		}
	} else {
		statuses = map[string]string{
			"submitted": "ready", "working": "active", "input_required": "blocked",
			"auth_required": "blocked", "completed": "review", "failed": "failed",
			"canceled": "canceled", "rejected": "canceled",
		}
	}
	if protocol == BindingProtocolMCP {
		delete(statuses, "auth_required")
		delete(statuses, "rejected")
	}
	mapped, ok := statuses[value]
	if !ok {
		return "", protocolBindingInvalid("mapping_status_unknown")
	}
	return mapped, nil
}

func protocolMappingSlice(value any) ([]any, bool) {
	rv := reflect.ValueOf(value)
	if !rv.IsValid() || (rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array) {
		return nil, false
	}
	if bytesValue, ok := value.([]byte); ok {
		_ = bytesValue
		return nil, false
	}
	out := make([]any, rv.Len())
	for i := range out {
		out[i] = rv.Index(i).Interface()
	}
	return out, true
}
