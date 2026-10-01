// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
package sessions

import (
	"encoding/json"
	"github.com/olivaresai/olivares/core/model"
	"io"
	"strings"
)

// A launch snapshot, not a current authorization verdict. The scope survives
// stop/revocation; each action still revalidates its purpose and live Claim.
// Null on older rows: a current profile must never rewrite historical authority.
const colRunWorkScope = "session_work_scope"

type runWorkScopeDTO struct {
	Role         string   `json:"role"`
	WorkspaceID  model.ID `json:"workspace_id"`
	Capabilities []string `json:"capabilities,omitempty"`
	GrantID      model.ID `json:"grant_id,omitempty"`
}

func setRunWorkScope(rec model.Record, credentials runtimeCredentials) error {
	rec[colRunWorkScope] = nil
	if credentials.work.ID.IsZero() {
		return nil
	}
	workspace := rec.String(colRunAuthzWorkspaceID)
	// Historical runs without a recorded authorization lineage remain unknown.
	if workspace == "" {
		return nil
	}
	if !validRuntimeUUIDv7(workspace) {
		return forbiddenErr("run work scope lineage is invalid")
	}
	scope := runWorkScopeDTO{Role: "worker", WorkspaceID: model.ID(workspace)}
	if credentials.orchestrationGrant != "" {
		grant, err := decodeProfileWorkGrant(credentials.orchestrationGrant)
		if err != nil {
			return err
		}
		if grant == nil || grant.WorkspaceID.String() != workspace {
			return forbiddenErr("run work grant does not match its authorization lineage")
		}
		scope = runWorkScopeDTO{Role: grant.Role, WorkspaceID: grant.WorkspaceID, Capabilities: append([]string(nil), grant.Capabilities...), GrantID: grant.GrantID}
	}
	raw, err := json.Marshal(scope)
	if err != nil {
		return err
	}
	rec[colRunWorkScope] = string(raw)
	return nil
}

func runWorkScope(rec model.Record) *runWorkScopeDTO {
	raw := rec.String(colRunWorkScope)
	if raw == "" {
		return nil
	}
	var scope runWorkScopeDTO
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&scope) != nil || decoder.Decode(new(any)) != io.EOF || !validRuntimeUUIDv7(scope.WorkspaceID.String()) || scope.WorkspaceID.String() != rec.String(colRunAuthzWorkspaceID) {
		return nil
	}
	switch scope.Role {
	case "worker":
		if len(scope.Capabilities) != 0 || !scope.GrantID.IsZero() {
			return nil
		}
	case "orchestrator":
		if _, err := decodeProfileWorkGrant(raw); err != nil {
			return nil
		}
	default:
		return nil
	}
	return &scope
}
