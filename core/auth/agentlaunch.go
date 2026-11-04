// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"strings"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// AgentLaunchCredentialPurpose marks the identity credential the engine mints
// for an agent that launches a session with no person present: a scheduled
// run a second administrator approved. It is the launcher SessionCredentials
// narrows to the one session; it is never an HTTP bearer (authToken refuses
// every purpose it does not name), and its secret is discarded at issue.
const AgentLaunchCredentialPurpose = "agent-launch"

// agentLaunchRole is the ceiling of an agent launch: a session edits its own
// workspace and administers nothing.
const agentLaunchRole = RoleEditor

// AgentLaunchSpec is the server-derived input of one agent launch: the run, the
// agent that owns its work item, and the schedule and approval that allowed it.
type AgentLaunchSpec struct {
	Tenant      model.TenantID
	AgentRef    string
	RunRef      string
	ScheduleRef string
	ApprovalRef string
}

// IssueAgentLaunchCredential mints the agent's own credential for one run and
// returns its authenticated principal. Only a locally authenticated SYSTEM
// path may call it, after the run's approval was verified. The row carries no
// person (UserID is empty), names the run, and expires with the session
// credential minted from it, so it lends no human authority and outlives
// nothing.
func (a *Authenticator) IssueAgentLaunchCredential(ctx context.Context, actor Principal, spec AgentLaunchSpec) (Principal, error) {
	if !actor.IsSystemOperator() {
		return Principal{}, ErrRoleCeiling
	}
	if spec.Tenant.IsZero() || spec.Tenant.IsSystem() || strings.TrimSpace(spec.AgentRef) == "" ||
		!validRuntimeAgentIdentity(spec.AgentRef) || strings.TrimSpace(spec.RunRef) == "" || containsSealControl(spec.RunRef) ||
		strings.TrimSpace(spec.ScheduleRef) == "" || strings.TrimSpace(spec.ApprovalRef) == "" {
		return Principal{}, ErrUnauthenticated
	}
	cred, err := NewCredential(PrefixToken)
	if err != nil {
		return Principal{}, err
	}
	var p Principal
	err = a.st.AuthMutate(ctx, func(as store.AuthScope) error {
		expires := model.NewTimestamp(a.clock.Now().Time().Add(sessionCredentialTTL))
		row, err := as.Tokens().Create(ctx, model.APIToken{
			Name: "agent-launch:" + spec.RunRef, Selector: cred.Selector, SecretHash: cred.SecretHash,
			BoundTenantID: spec.Tenant, Role: agentLaunchRole, ExpiresAt: &expires,
			Purpose: AgentLaunchCredentialPurpose, AgentRef: spec.AgentRef,
		})
		if err != nil {
			return err
		}
		var ok bool
		if p, ok = agentLaunchPrincipal(row); !ok {
			return ErrUnauthenticated
		}
		p = p.withCredentialRef(row.Version)
		return metaAudit(ctx, as, actor, "agent_launch_credential.issue", "core.api_token", row.ID,
			map[string]any{"purpose": AgentLaunchCredentialPurpose, "agent_ref": spec.AgentRef, "run_ref": spec.RunRef,
				"schedule_ref": spec.ScheduleRef, "approval_ref": spec.ApprovalRef})
	})
	if err != nil {
		return Principal{}, err
	}
	return p, nil
}

// agentLaunchPrincipal rebuilds the principal of an agent launch row: the
// agent, its one tenant at the fixed ceiling, no person, no session binding.
func agentLaunchPrincipal(t model.APIToken) (Principal, bool) {
	if t.Purpose != AgentLaunchCredentialPurpose || t.IsSuperadmin || !t.UserID.IsZero() ||
		t.BoundTenantID.IsZero() || t.BoundTenantID.IsSystem() || t.Role != agentLaunchRole ||
		t.AgentRef == "" || !validRuntimeAgentIdentity(t.AgentRef) || t.ExpiresAt == nil ||
		t.Scope != "" || !t.ParentTokenID.IsZero() || t.Audience != "" || !t.ActAsUserID.IsZero() ||
		t.SessionRef != "" || !t.WorkspaceID.IsZero() || t.SessionRunRef != "" || t.SessionFence != 0 {
		return Principal{}, false
	}
	p := newPrincipal(KindToken, "", t.ID, false, t.Name, map[model.TenantID]string{t.BoundTenantID: t.Role}, nil)
	return p.WithAgentIdentity(t.AgentRef), true
}
