// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"unicode"

	a2a "github.com/olivaresai/olivares/connectors/a2a"
	mcpc "github.com/olivaresai/olivares/connectors/mcp"
	"github.com/olivaresai/olivares/modules/sessions"
)

func protocolConfiguredSpecLineage(
	current sessions.ProtocolBindingSpec,
	input sessions.ProtocolBindingSpecInput,
	configuredGeneration int64,
) bool {
	if current.Generation != configuredGeneration {
		return false
	}
	if input.Generation == configuredGeneration && input.SupersedesID.IsZero() {
		digests, err := sessions.ComputeProtocolBindingSpecDigests(input)
		return err == nil && current.State == sessions.ProtocolBindingSpecDraft &&
			bytes.Equal(current.SpecHash, digests.SpecHash) &&
			bytes.Equal(current.MappingHash, digests.MappingHash) &&
			bytes.Equal(current.LossesHash, digests.LossesHash)
	}
	return current.State == sessions.ProtocolBindingSpecActive &&
		input.SupersedesID == current.ID && input.Generation > current.Generation
}

type protocolRuntimePolicy struct {
	ruleRefs             []string
	permissionProfileRef string
}

func protocolRuntimePolicyMatches(
	ruleRefs []string,
	permissionProfileRef string,
	want protocolRuntimePolicy,
) bool {
	got, err := resolveProtocolRuntimePolicy(ruleRefs, permissionProfileRef, protocolRuntimePolicy{})
	return err == nil && slices.Equal(got.ruleRefs, want.ruleRefs) &&
		got.permissionProfileRef == want.permissionProfileRef
}

var (
	a2aOutboundRuntimePolicy = protocolRuntimePolicy{
		ruleRefs: []string{"rule:remote-work"}, permissionProfileRef: "permission:remote-work",
	}
	a2aInboundRuntimePolicy = protocolRuntimePolicy{
		ruleRefs: []string{"rule:a2a-inbound"}, permissionProfileRef: "permission:a2a-inbound",
	}
	mcpTaskRuntimePolicy = protocolRuntimePolicy{
		ruleRefs: []string{"rule:mcp-task"}, permissionProfileRef: "permission:mcp-task",
	}
)

func resolveProtocolRuntimePolicy(
	ruleRefs []string,
	permissionProfileRef string,
	fallback protocolRuntimePolicy,
) (protocolRuntimePolicy, error) {
	if len(ruleRefs) == 0 {
		ruleRefs = fallback.ruleRefs
	}
	if strings.TrimSpace(permissionProfileRef) == "" {
		permissionProfileRef = fallback.permissionProfileRef
	}
	result := protocolRuntimePolicy{
		ruleRefs:             append([]string(nil), ruleRefs...),
		permissionProfileRef: strings.TrimSpace(permissionProfileRef),
	}
	for i := range result.ruleRefs {
		result.ruleRefs[i] = strings.TrimSpace(result.ruleRefs[i])
		if !validProtocolRuntimeRef(result.ruleRefs[i]) {
			return protocolRuntimePolicy{}, fmt.Errorf("invalid protocol rule ref")
		}
	}
	if !validProtocolRuntimeRef(result.permissionProfileRef) {
		return protocolRuntimePolicy{}, fmt.Errorf("invalid protocol permission profile ref")
	}
	sort.Strings(result.ruleRefs)
	for i := 1; i < len(result.ruleRefs); i++ {
		if result.ruleRefs[i] == result.ruleRefs[i-1] {
			return protocolRuntimePolicy{}, fmt.Errorf("duplicate protocol rule ref")
		}
	}
	return result, nil
}

func validProtocolRuntimeRef(value string) bool {
	if value == "" || len(value) > 512 {
		return false
	}
	for _, r := range value {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func protocolWorkMappingSource(item sessions.WorkItem) map[string]any {
	contextMetadata := map[string]any{"items": []any{}}
	if len(item.ContextRefs) != 0 {
		var refs any
		if json.Unmarshal(item.ContextRefs, &refs) == nil {
			contextMetadata["items"] = refs
		}
	}
	return map[string]any{
		"work.id": item.ID.String(), "work.workspace_id": item.WorkspaceID.String(),
		"work.title": item.Title, "work.brief": item.BriefMD, "work.status": item.Status,
		"work.kind": item.WorkKind, "work.owner_ref": item.OwnerRef,
		"work.priority": item.Priority, "work.context_refs": contextMetadata,
	}
}

func protocolA2AInboundMappingSource(message a2a.InboundMessage) (map[string]any, error) {
	parts := make([]any, 0, len(message.Parts))
	sections := make([]string, 0, len(message.Parts))
	for _, part := range message.Parts {
		switch {
		case part.Text != "":
			parts = append(parts, part.Text)
			sections = append(sections, part.Text)
		case len(part.Data) != 0 && json.Valid(part.Data):
			var value any
			if err := json.Unmarshal(part.Data, &value); err != nil {
				return nil, err
			}
			compact, err := json.Marshal(value)
			if err != nil {
				return nil, err
			}
			section := "```json\n" + string(compact) + "\n```"
			parts = append(parts, section)
			sections = append(sections, section)
		case part.Kind == "file" && validInboundA2APartReference(part.Reference) &&
			validInboundA2APartDigest(part.Digest):
			section := fmt.Sprintf("A2A file reference: %s (sha256:%s)", part.Reference, part.Digest)
			parts = append(parts, section)
			sections = append(sections, section)
		default:
			return nil, fmt.Errorf("unsupported A2A message part")
		}
	}
	metadata := make(map[string]any, len(message.Metadata))
	for key, raw := range message.Metadata {
		if !json.Valid(raw) {
			return nil, fmt.Errorf("invalid A2A metadata")
		}
		var value any
		if err := json.Unmarshal(raw, &value); err != nil {
			return nil, err
		}
		metadata[key] = value
	}
	return map[string]any{
		"message.text": strings.Join(sections, "\n\n"), "message.parts": parts,
		"message.id": message.MessageID, "message.context_id": message.ContextID,
		"message.reference": message.MessageID, "message.metadata": metadata,
		"message.status": "submitted", "peer.subject": message.PeerSubject,
	}, nil
}

func protocolMCPTaskMappingSource(intent mcpc.DurableTaskIntent) map[string]any {
	summary := fmt.Sprintf("Tool reference: %q\nOrigin operation: %q\nOrigin effect digest: %q",
		intent.Tool, intent.OriginOperationID, intent.OriginEffectDigest)
	return map[string]any{
		"task.id": intent.TaskID, "task.tool": intent.Tool, "task.status": intent.InitialStatus,
		"task.summary":        summary,
		"task.required_scope": intent.RequiredScope,
		"task.origin_ref":     intent.OriginOperationID,
		"task.owner": map[string]any{
			"issuer": intent.Owner.Issuer, "subject": intent.Owner.Subject,
			"act_as": intent.Owner.ActAs, "client_id": intent.Owner.ClientID,
			"is_delegated": intent.Owner.IsDelegated,
		},
		"task.metadata": map[string]any{
			"destructive": intent.Destructive, "upstream_descriptor": intent.UpstreamDescriptor,
			"protocol_version": intent.ProtocolVersion, "effect_digest": intent.OriginEffectDigest,
		},
	}
}

func requiredProtocolMappedString(evaluation sessions.ProtocolMappingEvaluation, field string) (string, error) {
	value, ok := evaluation.Values[field].(string)
	if !ok || strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("protocol mapping did not produce required field %q", field)
	}
	return value, nil
}

func optionalProtocolMappedString(evaluation sessions.ProtocolMappingEvaluation, field string) (string, error) {
	value, exists := evaluation.Values[field]
	if !exists {
		return "", nil
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("protocol mapping field %q is not text", field)
	}
	return text, nil
}
