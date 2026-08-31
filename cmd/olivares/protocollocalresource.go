// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"fmt"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sessions"
)

type protocolChannelLocalResourceReader interface {
	ResolveProtocolChannelLocalResource(
		context.Context,
		model.TenantID,
		sessions.ProtocolLocalResourceRequest,
	) (sessions.ProtocolLocalResourceProjection, error)
}

// protocolLocalResourceResolver is the read-only composition adapter for
// Protocol Composer previews. Agent selectors use the canonical Identity ID,
// models come from the governed core catalog, and Channels remain sessions-
// owned behind a narrow projection reader.
type protocolLocalResourceResolver struct {
	store    store.Store
	channels protocolChannelLocalResourceReader
}

var _ sessions.ProtocolLocalResourceResolver = protocolLocalResourceResolver{}

func (r protocolLocalResourceResolver) ResolveProtocolLocalResource(
	ctx context.Context,
	tenant model.TenantID,
	request sessions.ProtocolLocalResourceRequest,
) (sessions.ProtocolLocalResourceProjection, error) {
	if r.store == nil || tenant.IsZero() || tenant.IsSystem() ||
		request.WorkspaceID.IsZero() || request.ID.IsZero() {
		return sessions.ProtocolLocalResourceProjection{}, fmt.Errorf("protocol local resource resolver is unavailable")
	}
	switch request.Kind {
	case sessions.BindingLocalAgent:
		return r.resolveAgent(ctx, tenant, request)
	case sessions.BindingLocalModel:
		return r.resolveModel(ctx, tenant, request)
	case sessions.BindingLocalChannel:
		if r.channels == nil {
			return sessions.ProtocolLocalResourceProjection{}, fmt.Errorf("protocol channel resolver is unavailable")
		}
		return r.channels.ResolveProtocolChannelLocalResource(ctx, tenant, request)
	default:
		return sessions.ProtocolLocalResourceProjection{}, fmt.Errorf("unsupported protocol local resource kind")
	}
}

func (r protocolLocalResourceResolver) resolveAgent(
	ctx context.Context,
	tenant model.TenantID,
	request sessions.ProtocolLocalResourceRequest,
) (sessions.ProtocolLocalResourceProjection, error) {
	var result sessions.ProtocolLocalResourceProjection
	err := r.store.View(ctx, tenant, func(sc store.Scope) error {
		identity, err := sc.Identities().Get(ctx, request.ID)
		if err != nil {
			return err
		}
		agents, page, err := sc.Agents().List(ctx, model.Query{
			Filters: []model.Filter{{Column: "identity_id", Op: model.OpEq, Value: request.ID.String()}},
			Limit:   100,
		})
		if err != nil {
			return err
		}
		if page.HasMore {
			return fmt.Errorf("agent identity projection is truncated")
		}
		matching := make([]model.Agent, 0, 1)
		for _, agent := range agents {
			workspaceID := agent.WorkspaceID
			if workspaceID.IsZero() {
				workspace, err := sc.DefaultWorkspace(ctx)
				if err != nil {
					return err
				}
				workspaceID = workspace.ID
			}
			if workspaceID == request.WorkspaceID {
				matching = append(matching, agent)
			}
		}
		if len(matching) != 1 {
			return store.ErrNotFound
		}
		agent := matching[0]
		labels := agent.Labels
		if labels == nil {
			labels = map[string]any{}
		}
		metadata := agent.Metadata
		if metadata == nil {
			metadata = map[string]any{}
		}
		result = sessions.ProtocolLocalResourceProjection{
			WorkspaceID: request.WorkspaceID, Kind: request.Kind, ID: request.ID,
			Version: max(agent.Version, identity.Version),
			Fields: map[string]any{
				"agent.id": agent.ID.String(), "agent.workspace_id": request.WorkspaceID.String(),
				"agent.name": agent.Name, "agent.kind": agent.Kind,
				"agent.status": string(agent.Status), "agent.external_id": identity.ExternalID,
				"agent.identity_id": identity.ID.String(), "agent.labels": labels, "agent.metadata": metadata,
			},
		}
		return nil
	})
	return result, err
}

func (r protocolLocalResourceResolver) resolveModel(
	ctx context.Context,
	tenant model.TenantID,
	request sessions.ProtocolLocalResourceRequest,
) (sessions.ProtocolLocalResourceProjection, error) {
	var result sessions.ProtocolLocalResourceProjection
	err := r.store.View(ctx, tenant, func(sc store.Scope) error {
		entry, err := sc.Models().Get(ctx, request.ID)
		if err != nil {
			return err
		}
		metadata := entry.Metadata
		if metadata == nil {
			metadata = map[string]any{}
		}
		providerID := ""
		if !entry.ProviderID.IsZero() {
			providerID = entry.ProviderID.String()
		}
		result = sessions.ProtocolLocalResourceProjection{
			WorkspaceID: request.WorkspaceID, Kind: request.Kind, ID: entry.ID, Version: entry.Version,
			Fields: map[string]any{
				"model.id": entry.ID.String(), "model.name": entry.Name, "model.family": entry.Family,
				"model.status": string(entry.Status), "model.provider_id": providerID,
				"model.modality": entry.Modality, "model.context_window": entry.ContextWindow,
				"model.metadata": metadata,
			},
		}
		return nil
	})
	return result, err
}
