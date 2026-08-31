// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// ProtocolLocalResourceRequest is the exact local entity selected by a
// non-WorkItem ProtocolBindingSpec. ID is parsed from the closed {"id":...}
// selector before this request crosses the composition seam.
type ProtocolLocalResourceRequest struct {
	WorkspaceID model.ID
	Kind        BindingLocalKind
	ID          model.ID
}

// ProtocolLocalResourceProjection is the authoritative, non-sensitive flat
// field set consumed by the declarative mapping preview.
type ProtocolLocalResourceProjection struct {
	WorkspaceID model.ID
	Kind        BindingLocalKind
	ID          model.ID
	Version     int64
	Fields      map[string]any
}

// ProtocolLocalResourceResolver keeps core Agent/Identity and Model lookups in
// composition while letting sessions validate non-WorkItem composer specs.
type ProtocolLocalResourceResolver interface {
	ResolveProtocolLocalResource(
		context.Context,
		model.TenantID,
		ProtocolLocalResourceRequest,
	) (ProtocolLocalResourceProjection, error)
}

// UseProtocolLocalResourceResolver late-binds the read-only composition seam.
func (m *Module) UseProtocolLocalResourceResolver(resolver ProtocolLocalResourceResolver) {
	m.mu.Lock()
	m.protocolLocalResourceResolver = resolver
	m.mu.Unlock()
}

func protocolLocalResourceID(input ProtocolBindingSpecInput) (model.ID, error) {
	if input.LocalKind == BindingLocalWorkItem {
		return model.ID(""), nil
	}
	var selector map[string]json.RawMessage
	if err := json.Unmarshal(input.LocalSelector, &selector); err != nil || len(selector) != 1 {
		return model.ID(""), protocolBindingInvalid("invalid_local_resource_selector")
	}
	raw, ok := selector["id"]
	if !ok {
		return model.ID(""), protocolBindingInvalid("invalid_local_resource_selector")
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return model.ID(""), protocolBindingInvalid("invalid_local_resource_selector")
	}
	id, err := model.ParseID(strings.TrimSpace(value))
	if err != nil || id.IsZero() || id.String() != value {
		return model.ID(""), protocolBindingInvalid("invalid_local_resource_selector")
	}
	return id, nil
}

func (m *Module) validateProtocolLocalResourcePreview(
	ctx context.Context,
	tenant model.TenantID,
	input ProtocolBindingSpecInput,
) *ProtocolBindingValidation {
	switch input.LocalKind {
	case BindingLocalWorkItem:
		return nil
	case BindingLocalAgent, BindingLocalModel, BindingLocalChannel:
		// These are the non-WorkItem kinds owned by this seam.
	default:
		// Complete API desired state is normalized before this method. Keep the
		// capability-validator composition seam usable with partial probe values;
		// invalid/unknown kinds are rejected by normalizeProtocolSpecInput.
		return nil
	}
	id, err := protocolLocalResourceID(input)
	if err != nil {
		return &ProtocolBindingValidation{Verdict: ProtocolObservationBroken, Code: "local_resource_selector_invalid"}
	}
	m.mu.Lock()
	resolver := m.protocolLocalResourceResolver
	m.mu.Unlock()
	if resolver == nil {
		return &ProtocolBindingValidation{Verdict: ProtocolObservationUnknown, Code: "local_resource_resolver_unwired"}
	}
	projection, err := resolver.ResolveProtocolLocalResource(ctx, tenant, ProtocolLocalResourceRequest{
		WorkspaceID: input.WorkspaceID, Kind: input.LocalKind, ID: id,
	})
	if err != nil {
		return &ProtocolBindingValidation{Verdict: ProtocolObservationUnknown, Code: "local_resource_unavailable"}
	}
	if projection.WorkspaceID != input.WorkspaceID || projection.Kind != input.LocalKind ||
		projection.ID != id || projection.Version < 1 || projection.Fields == nil {
		return &ProtocolBindingValidation{Verdict: ProtocolObservationUnknown, Code: "local_resource_evidence_invalid"}
	}
	// An outbound A2A mapping consumes the local entity as its source, so preview
	// the exact evaluator now. Inbound A2A and MCP task mappings have a remote
	// source that cannot be fabricated during configuration; their target field
	// coverage was already proven by the closed mapping contract.
	if input.Protocol == BindingProtocolA2A &&
		(input.Direction == BindingOutbound || input.Direction == BindingBidirectional) {
		if _, err := PreviewProtocolBindingMapping(input, BindingOutbound, projection.Fields); err != nil {
			return &ProtocolBindingValidation{Verdict: ProtocolObservationBroken, Code: "local_mapping_preview_failed"}
		}
	}
	return nil
}

// ResolveProtocolChannelLocalResource is the narrow sessions-owned half of the
// composition resolver. It exposes a bounded Channel projection, never a raw
// store scope or protected communication payload.
func (m *Module) ResolveProtocolChannelLocalResource(
	ctx context.Context,
	tenant model.TenantID,
	request ProtocolLocalResourceRequest,
) (ProtocolLocalResourceProjection, error) {
	if request.Kind != BindingLocalChannel || request.ID.IsZero() || request.WorkspaceID.IsZero() {
		return ProtocolLocalResourceProjection{}, protocolBindingInvalid("invalid_channel_resource_ref")
	}
	var channel Channel
	err := m.workData(tenant).View(ctx, func(sc store.Scope) error {
		repo, err := sc.Ext(channelKind)
		if err != nil {
			return err
		}
		record, err := repo.Get(ctx, request.ID)
		if err != nil {
			return err
		}
		channel, err = channelFromRecord(record)
		return err
	})
	if err != nil {
		return ProtocolLocalResourceProjection{}, err
	}
	if channel.WorkspaceID != request.WorkspaceID || channel.ID != request.ID {
		return ProtocolLocalResourceProjection{}, store.ErrNotFound
	}
	return ProtocolLocalResourceProjection{
		WorkspaceID: channel.WorkspaceID, Kind: BindingLocalChannel,
		ID: channel.ID, Version: channel.Version,
		Fields: map[string]any{
			"channel.id": channel.ID.String(), "channel.workspace_id": channel.WorkspaceID.String(),
			"channel.name": channel.Name, "channel.description": channel.Description,
			"channel.kind": string(channel.Kind), "channel.status": string(channel.State),
			"channel.sensitivity":          string(channel.Sensitivity),
			"channel.retention_policy_ref": channel.RetentionPolicyRef,
			"channel.metadata": map[string]any{
				"ack_policy": channel.DefaultAckPolicy, "wake_policy": channel.DefaultWake,
				"route_revision": channel.RouteRevision, "acl_revision": channel.ACLRevision,
			},
		},
	}, nil
}
