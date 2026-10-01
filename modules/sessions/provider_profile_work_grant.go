// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"sort"
	"strings"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const colPPSessionWorkGrant = "session_work_grant"

// SessionWorkGrant is an operator-owned workspace grant. GrantID is generated
// on every grant replacement so withdrawing and restoring the same capabilities
// cannot revive an old bearer. It is output-only at the profile API.
type SessionWorkGrant struct {
	Role         string   `json:"role"`
	WorkspaceID  model.ID `json:"workspace_id"`
	Capabilities []string `json:"capabilities"`
	GrantID      model.ID `json:"grant_id"`
}

var orchestrationCapabilities = map[string]auth.Permission{
	"work.read":      permWorkRead,
	"work.create":    permWorkWrite,
	"work.assign":    permWorkAdmin,
	"work.review":    permWorkWrite,
	"decision.read":  permDecisionRead,
	"decision.write": permDecisionWrite,
}

func normalizeProfileWorkGrant(raw json.RawMessage) (*SessionWorkGrant, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	var input struct {
		Role         string   `json:"role"`
		WorkspaceID  model.ID `json:"workspace_id"`
		Capabilities []string `json:"capabilities"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return nil, badRequest("invalid session_work_grant")
	}
	if err := decoder.Decode(new(any)); err != io.EOF || input.Role != "orchestrator" ||
		!validRuntimeUUIDv7(input.WorkspaceID.String()) || len(input.Capabilities) == 0 || len(input.Capabilities) > len(orchestrationCapabilities) {
		return nil, badRequest("session_work_grant requires orchestrator role, workspace_id and capabilities")
	}
	seen := map[string]bool{}
	for _, capability := range input.Capabilities {
		if _, known := orchestrationCapabilities[capability]; !known || seen[capability] {
			return nil, badRequest("invalid or duplicate session work capability")
		}
		seen[capability] = true
	}
	sort.Strings(input.Capabilities)
	return &SessionWorkGrant{
		Role: input.Role, WorkspaceID: input.WorkspaceID,
		Capabilities: input.Capabilities, GrantID: model.NewID(),
	}, nil
}

func encodeProfileWorkGrant(grant *SessionWorkGrant) string {
	if grant == nil {
		return ""
	}
	raw, _ := json.Marshal(grant)
	return string(raw)
}

func decodeProfileWorkGrant(raw string) (*SessionWorkGrant, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var grant SessionWorkGrant
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&grant); err != nil || grant.Role != "orchestrator" ||
		!validRuntimeUUIDv7(grant.WorkspaceID.String()) || !validRuntimeUUIDv7(grant.GrantID.String()) || len(grant.Capabilities) == 0 {
		return nil, forbiddenErr("stored session work grant is invalid")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return nil, forbiddenErr("stored session work grant is invalid")
	}
	seen := map[string]bool{}
	for _, capability := range grant.Capabilities {
		if _, known := orchestrationCapabilities[capability]; !known || seen[capability] {
			return nil, forbiddenErr("stored session work grant is invalid")
		}
		seen[capability] = true
	}
	return &grant, nil
}

func (m *Module) authorizeProfileWorkGrant(ctx context.Context, actor auth.Principal, tenant model.TenantID, raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	grant, err := normalizeProfileWorkGrant(raw)
	if err != nil {
		return err
	}
	if actor.IsPurposeRestricted() || m.workAuthz == nil {
		return forbiddenErr("session work grants require operator authority")
	}
	if !m.workAuthz.Authorize(ctx, auth.Request{Principal: actor, Tenant: tenant, Permission: permProfileAdmin, Resource: auth.ResourceFor(permProfileAdmin)}).Allow {
		return forbiddenErr("session work grants require profile administration")
	}
	if grant != nil {
		for _, capability := range grant.Capabilities {
			permission := orchestrationCapabilities[capability]
			resource := auth.ResourceFor(permission)
			resource.WorkspaceID = grant.WorkspaceID
			if !m.workAuthz.Authorize(ctx, auth.Request{Principal: actor, Tenant: tenant, Permission: permission, Resource: resource}).Allow {
				return forbiddenErr("operator cannot delegate this session work capability")
			}
		}
	}
	return nil
}

func appendProfileWorkGrantAudit(ctx context.Context, sc store.Scope, actor auth.Principal, id model.ID, grant *SessionWorkGrant) error {
	meta := map[string]any{"revoked": grant == nil}
	if grant != nil {
		meta["workspace_id"], meta["grant_id"], meta["capabilities"] = grant.WorkspaceID.String(), grant.GrantID.String(), grant.Capabilities
	}
	_, err := sc.Audit().Append(ctx, model.AuditDraft{
		Actor: orSystem(actor.Actor()), ActorKind: orSystemKind(actor.ActorKind()),
		Action: "sessions.provider_profile.work_grant", TargetKind: providerProfileKind, TargetID: id, Meta: meta,
	})
	return err
}
