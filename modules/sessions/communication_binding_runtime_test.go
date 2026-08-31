// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

func protocolRuntimeInputForTest() ProtocolBindingSpecInput {
	return ProtocolBindingSpecInput{
		WorkspaceID: model.NewID(), BindingKey: "runtime-a2a", Generation: 1,
		Protocol: BindingProtocolA2A, ProtocolVersion: "1.0.1",
		Direction: BindingOutbound, LocalKind: BindingLocalWorkItem,
		LocalSelector: json.RawMessage(`{"work_kind":"operations"}`),
		PeerAuthority: "https://runtime.example", RemoteResourceKind: "agent",
		RemoteResourceRef: "agent:remote", MappingSchema: ProtocolBindingMappingSchemaV1,
		Mapping: []ProtocolMappingRule{{
			Source: "work.title", Target: "message.text",
			Cardinality: ProtocolMappingOneToOne, Transform: ProtocolTransformText,
		}},
		KnownLosses: []ProtocolBindingLoss{}, RuleRefs: []string{"rule:remote-work"},
		PermissionProfileRef: "permission:remote-work", CurrencyPolicy: BindingCurrencyPinned,
		Validation: ProtocolBindingValidation{Verdict: ProtocolObservationClean, Code: "validated"},
	}
}

func TestProtocolBindingPlanPinsMappingLossesAndVersion(t *testing.T) {
	t.Parallel()

	valid := protocolRuntimeInputForTest()
	if _, err := normalizeProtocolSpecInput(valid); err != nil {
		t.Fatalf("valid mapping contract: %v", err)
	}

	rows := []struct {
		name   string
		mutate func(*ProtocolBindingSpecInput)
	}{
		{name: "implicit version", mutate: func(input *ProtocolBindingSpecInput) {
			input.ProtocolVersion = "latest"
		}},
		{name: "required field without mapping or accepted loss", mutate: func(input *ProtocolBindingSpecInput) {
			input.Mapping[0].Source = "work.status"
			input.Mapping[0].Transform = ProtocolTransformText
		}},
		{name: "executable transform", mutate: func(input *ProtocolBindingSpecInput) {
			input.Mapping[0].Transform = ProtocolMappingTransform("template")
		}},
		{name: "unknown mapping schema", mutate: func(input *ProtocolBindingSpecInput) {
			input.MappingSchema = "custom-script-v9"
		}},
		{name: "unknown catalog field", mutate: func(input *ProtocolBindingSpecInput) {
			input.Mapping[0].Source = "work.prompt"
		}},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			input := protocolRuntimeInputForTest()
			row.mutate(&input)
			if _, err := normalizeProtocolSpecInput(input); !errors.Is(err, ErrInvalidProtocolBinding) {
				t.Fatalf("normalize mutated contract = %v, want invalid binding", err)
			}
		})
	}

	accepted := protocolRuntimeInputForTest()
	accepted.Mapping = []ProtocolMappingRule{{
		Source: "work.status", Target: "message.status",
		Cardinality: ProtocolMappingOneToOne, Transform: ProtocolTransformStatus,
	}}
	accepted.KnownLosses = []ProtocolBindingLoss{
		{Field: "work.title", ReasonCode: "not-required-by-peer", Accepted: true, AcceptanceRef: "approval:m106"},
		{Field: "message.text", ReasonCode: "not-required-by-peer", Accepted: true, AcceptanceRef: "approval:m106"},
	}
	if _, err := normalizeProtocolSpecInput(accepted); err != nil {
		t.Fatalf("accepted required-field losses: %v", err)
	}
}

func TestEvaluateProtocolBindingMappingUsesClosedTransformsAndPolicyPins(t *testing.T) {
	t.Parallel()

	input := protocolRuntimeInputForTest()
	input.Mapping = []ProtocolMappingRule{
		{Source: "work.id", Target: "message.reference", Cardinality: ProtocolMappingOneToOne, Transform: ProtocolTransformReference},
		{Source: "work.title", Target: "message.text", Cardinality: ProtocolMappingOneToOne, Transform: ProtocolTransformText},
		{Source: "work.status", Target: "message.status", Cardinality: ProtocolMappingOneToOne, Transform: ProtocolTransformStatus},
		{Source: "work.owner_ref", Target: "message.context_id", Cardinality: ProtocolMappingOneToOne, Transform: ProtocolTransformIdentity},
		{Source: "work.context_refs", Target: "message.metadata", Cardinality: ProtocolMappingOneToOne, Transform: ProtocolTransformMetadata},
	}
	normalized, err := normalizeProtocolSpecInput(input)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	mappingHash, lossesHash, specHash, err := protocolBindingSpecHashes(normalized)
	if err != nil {
		t.Fatalf("hashes: %v", err)
	}
	specID := model.NewID()
	spec := ProtocolBindingSpec{
		MutableCommunicationEntity: MutableCommunicationEntity{CommunicationEntity: CommunicationEntity{
			ID: specID, TenantID: model.NewTenantID(), WorkspaceID: input.WorkspaceID, Version: 2,
		}},
		BindingKey: normalized.BindingKey, Generation: normalized.Generation,
		Protocol: normalized.Protocol, ProtocolVersion: normalized.ProtocolVersion,
		Direction: normalized.Direction, LocalKind: normalized.LocalKind,
		LocalSelector: normalized.LocalSelector, PeerAuthority: normalized.PeerAuthority,
		RemoteResourceKind: normalized.RemoteResourceKind, RemoteResourceRef: normalized.RemoteResourceRef,
		MappingSchema: normalized.MappingSchema, Mapping: normalized.Mapping, MappingHash: mappingHash,
		KnownLosses: normalized.KnownLosses, LossesHash: lossesHash,
		RuleRefs: normalized.RuleRefs, PermissionProfileRef: normalized.PermissionProfileRef,
		CurrencyPolicy: normalized.CurrencyPolicy, Validation: normalized.Validation,
		State: ProtocolBindingSpecActive, SpecHash: specHash,
	}
	expect := ProtocolBindingRuntimeExpectation{
		TenantID: spec.TenantID, WorkspaceID: spec.WorkspaceID, SpecID: spec.ID, Generation: spec.Generation,
		Protocol: spec.Protocol, ProtocolVersion: spec.ProtocolVersion,
		Direction: BindingOutbound, LocalKind: spec.LocalKind,
		PeerAuthority: spec.PeerAuthority, RemoteResourceKind: spec.RemoteResourceKind,
		RemoteResourceRef: spec.RemoteResourceRef, RuleRefs: []string{"rule:remote-work"},
		PermissionProfileRef: "permission:remote-work",
	}
	result, err := EvaluateProtocolBindingMapping(spec, expect, map[string]any{
		"work.id": "work:42", "work.title": "Governed report", "work.status": "active",
		"work.owner_ref": "agent:local", "work.context_refs": map[string]any{"artifact": "report:1"},
	})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if result.Values["message.text"] != "Governed report" ||
		result.Values["message.reference"] != "work:42" ||
		result.Values["message.context_id"] != "agent:local" ||
		result.Values["message.status"] != "working" || result.EvidenceHash == "" {
		t.Fatalf("evaluation = %#v", result)
	}
	metadata, ok := result.Values["message.metadata"].(map[string]any)
	if !ok || metadata["artifact"] != "report:1" {
		t.Fatalf("metadata mapping = %#v", result.Values["message.metadata"])
	}

	changedPolicy := expect
	changedPolicy.RuleRefs = []string{"rule:other"}
	if _, err := EvaluateProtocolBindingMapping(spec, changedPolicy, map[string]any{}); !errors.Is(err, ErrProtocolBindingConflict) {
		t.Fatalf("changed runtime policy = %v, want conflict", err)
	}
}

func TestProtocolMappingIdentityPreservesClosedValueKind(t *testing.T) {
	t.Parallel()

	input := protocolRuntimeInputForTest()
	input.Mapping[0].Transform = ProtocolTransformIdentity
	if _, err := PreviewProtocolBindingMapping(input, BindingOutbound, map[string]any{
		"work.title": map[string]any{"not": "text"},
	}); !errors.Is(err, ErrInvalidProtocolBinding) {
		t.Fatalf("identity accepted a non-text value = %v", err)
	}
	values, err := PreviewProtocolBindingMapping(input, BindingOutbound, map[string]any{
		"work.title": "typed title",
	})
	if err != nil || values["message.text"] != "typed title" {
		t.Fatalf("typed identity preview = %#v, %v", values, err)
	}
}

func TestProtocolMappingMCPStatusProjectsRemoteTaskToLocalWork(t *testing.T) {
	t.Parallel()

	input := protocolRuntimeInputForTest()
	input.Protocol = BindingProtocolMCP
	input.ProtocolVersion = "2025-11-25"
	input.PeerAuthority = "mcp.example"
	input.RemoteResourceKind = "tasks"
	input.RemoteResourceRef = "resource-server:primary"
	input.RuleRefs = []string{"rule:mcp-task"}
	input.PermissionProfileRef = "permission:mcp-task"
	input.Mapping = []ProtocolMappingRule{
		{Source: "task.summary", Target: "work.brief", Cardinality: ProtocolMappingOneToOne, Transform: ProtocolTransformText},
		{Source: "task.status", Target: "work.status", Cardinality: ProtocolMappingOneToOne, Transform: ProtocolTransformStatus},
	}
	values, err := PreviewProtocolBindingMapping(input, BindingOutbound, map[string]any{
		"task.summary": "governed task", "task.status": "working",
	})
	if err != nil || values["work.brief"] != "governed task" || values["work.status"] != "active" {
		t.Fatalf("MCP task projection = %#v, %v", values, err)
	}
}
