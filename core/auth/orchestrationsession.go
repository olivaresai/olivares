// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const (
	OrchestrationSessionCredentialPurpose = "orchestration-session"

	orchestrationSessionRole = "orchestration-session"
	orchestrationSessionName = "sessions-runtime-orchestration"

	DefaultOrchestrationSessionCredentialTTL = 30 * time.Minute
)

// OrchestrationSessionCredentialSpec is the complete server-proven identity
// of one supervised orchestrator. AgentRef is required for attributable
// decisions. Every binding and capability is server-derived from its profile.
type OrchestrationSessionCredentialSpec struct {
	Tenant       model.TenantID
	WorkspaceID  model.ID
	SessionRef   string
	RunRef       string
	AgentRef     string
	ClaimFence   int64
	ProfileRef   string
	GrantID      model.ID
	Capabilities []string
}

// OrchestrationSessionCredential is the show-once bearer plus its durable,
// non-sensitive revocation binding. The raw Token must not be persisted.
type OrchestrationSessionCredential struct {
	Token       string
	ID          model.ID
	Tenant      model.TenantID
	WorkspaceID model.ID
	SessionRef  string
	RunRef      string
	AgentRef    string
	ClaimFence  int64
	ExpiresAt   time.Time
}

// IssueOrchestrationSessionCredential mints a private HTTP bearer for the
// exact runtime generation. It is deliberately separate from work-session:
// no public caller supplies a purpose or capability override. The composition
// root is the only caller and must invoke this only
// after sessions has acquired the live Claim and resolved its authz workspace.
func (a *Authenticator) IssueOrchestrationSessionCredential(
	ctx context.Context,
	actor Principal,
	spec OrchestrationSessionCredentialSpec,
) (OrchestrationSessionCredential, error) {
	if !actor.IsSystemOperator() {
		return OrchestrationSessionCredential{}, ErrRoleCeiling
	}
	if err := validateOrchestrationSessionCredentialSpec(spec); err != nil {
		return OrchestrationSessionCredential{}, err
	}

	cred, err := NewCredential(PrefixToken)
	if err != nil {
		return OrchestrationSessionCredential{}, err
	}
	var expiresAt time.Time
	var stored model.APIToken
	err = a.st.AuthMutate(ctx, func(as store.AuthScope) error {
		now := a.clock.Now()
		// Resume reuses SID+run_ref with a newer Claim fence. Drain every live
		// predecessor for that runtime atomically, including a row whose other
		// binding columns were corrupted, so an old generation cannot revive.
		previous, err := drainList(ctx, as.Tokens().List, model.Query{Filters: []model.Filter{
			{Column: "bound_tenant_id", Op: model.OpEq, Value: spec.Tenant.String()},
			{Column: "purpose", Op: model.OpEq, Value: OrchestrationSessionCredentialPurpose},
			{Column: "session_ref", Op: model.OpEq, Value: spec.SessionRef},
			{Column: "session_run_ref", Op: model.OpEq, Value: spec.RunRef},
		}})
		if err != nil {
			return err
		}
		var maxFence int64
		for _, old := range previous {
			if old.SessionFence > maxFence {
				maxFence = old.SessionFence
			}
		}
		// A delayed issuer from an older Claim generation must not revoke the
		// successor and recreate stale authority. Equal fences remain valid for
		// retry/concurrent mint and still converge to one active bearer.
		if spec.ClaimFence < maxFence {
			return fmt.Errorf("%w: stale orchestration-session claim fence", ErrUnauthenticated)
		}
		if spec.ClaimFence == maxFence {
			for _, old := range previous {
				if old.SessionFence == maxFence &&
					!orchestrationSessionBindingMatches(old, spec) {
					return fmt.Errorf("%w: orchestration-session binding changed without a new claim fence", ErrUnauthenticated)
				}
			}
		}
		for _, old := range previous {
			if old.Revoked || old.ExpiresAt == nil ||
				orchestrationSessionCredentialExpired(*old.ExpiresAt, now) {
				continue
			}
			if err := revokeTokenTree(ctx, as, actor, old.ID); err != nil {
				return err
			}
		}

		expiresAt = now.Time().Add(DefaultOrchestrationSessionCredentialTTL)
		expiresTS := model.NewTimestamp(expiresAt)
		token, err := as.Tokens().Create(ctx, model.APIToken{
			Name: orchestrationSessionCredentialName(spec), Selector: cred.Selector, SecretHash: cred.SecretHash,
			BoundTenantID: spec.Tenant, Role: orchestrationSessionRole, ExpiresAt: &expiresTS,
			Purpose: OrchestrationSessionCredentialPurpose, Scope: strings.Join(spec.Capabilities, " "),
			AgentRef: spec.AgentRef, SessionRef: spec.SessionRef, WorkspaceID: spec.WorkspaceID,
			SessionRunRef: spec.RunRef, SessionFence: spec.ClaimFence,
		})
		if err != nil {
			return err
		}
		stored = token
		return metaAudit(ctx, as, actor, "orchestration_session_credential.issue",
			"core.api_token", token.ID,
			map[string]any{"purpose": OrchestrationSessionCredentialPurpose})
	})
	if err != nil {
		return OrchestrationSessionCredential{}, err
	}
	return OrchestrationSessionCredential{
		Token: cred.Token, ID: stored.ID, Tenant: stored.BoundTenantID,
		WorkspaceID: stored.WorkspaceID, SessionRef: stored.SessionRef,
		RunRef: stored.SessionRunRef, AgentRef: stored.AgentRef,
		ClaimFence: stored.SessionFence, ExpiresAt: expiresAt,
	}, nil
}

// RevokeOrchestrationSessionCredential revokes only the exact credential named
// by expected. A missing handle is idempotent; a crossed sibling handle is not.
func (a *Authenticator) RevokeOrchestrationSessionCredential(
	ctx context.Context,
	actor Principal,
	id model.ID,
	expected OrchestrationSessionCredentialSpec,
) error {
	if !actor.IsSystemOperator() || id.IsZero() {
		return ErrRoleCeiling
	}
	if err := validateOrchestrationSessionCredentialSpec(expected); err != nil {
		return err
	}
	return a.st.AuthMutate(ctx, func(as store.AuthScope) error {
		token, err := as.Tokens().Get(ctx, id)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if _, ok := orchestrationSessionPrincipal(token); !ok ||
			!orchestrationSessionBindingMatches(token, expected) {
			return ErrUnauthenticated
		}
		return revokeTokenTree(ctx, as, actor, id)
	})
}

// RevokeOrchestrationRuntimeCredential is recovery cleanup after the profile
// grant has been withdrawn. It validates the stored purpose and exact durable
// runtime tuple; a current grant is neither needed nor allowed to prevent
// withdrawal. It cannot revoke a worker, operator, or sibling runtime bearer.
func (a *Authenticator) RevokeOrchestrationRuntimeCredential(ctx context.Context, actor Principal, id model.ID, expected WorkSessionCredentialSpec) error {
	if !actor.IsSystemOperator() || id.IsZero() {
		return ErrRoleCeiling
	}
	if err := validateWorkSessionCredentialSpec(expected); err != nil {
		return err
	}
	return a.st.AuthMutate(ctx, func(sc store.AuthScope) error {
		token, err := sc.Tokens().Get(ctx, id)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if _, ok := orchestrationSessionPrincipal(token); !ok || token.BoundTenantID != expected.Tenant || token.SessionRef != expected.SessionRef || token.SessionRunRef != expected.RunRef || token.AgentRef != expected.AgentRef || token.SessionFence != expected.ClaimFence {
			return ErrUnauthenticated
		}
		return revokeTokenTree(ctx, sc, actor, id)
	})
}

// RenewOrchestrationSessionCredential slides the SAME bearer expiry only after
// the runtime has renewed its Claim. It never rotates or returns secret material
// and never resurrects an expired or revoked credential.
func (a *Authenticator) RenewOrchestrationSessionCredential(
	ctx context.Context,
	actor Principal,
	id model.ID,
	expected OrchestrationSessionCredentialSpec,
) (time.Time, error) {
	if !actor.IsSystemOperator() || id.IsZero() {
		return time.Time{}, ErrRoleCeiling
	}
	if err := validateOrchestrationSessionCredentialSpec(expected); err != nil {
		return time.Time{}, err
	}
	var expiresAt time.Time
	err := a.st.AuthMutate(ctx, func(as store.AuthScope) error {
		token, err := as.Tokens().Get(ctx, id)
		if err != nil {
			return err
		}
		now := a.clock.Now()
		if _, ok := orchestrationSessionPrincipal(token); !ok || token.Revoked ||
			token.ExpiresAt == nil || orchestrationSessionCredentialExpired(*token.ExpiresAt, now) ||
			!orchestrationSessionBindingMatches(token, expected) {
			return ErrUnauthenticated
		}
		expiresAt = now.Time().Add(DefaultOrchestrationSessionCredentialTTL)
		expiresTS := model.NewTimestamp(expiresAt)
		token.ExpiresAt = &expiresTS
		updated, err := as.Tokens().Update(ctx, token)
		if err != nil {
			return err
		}
		return metaAudit(ctx, as, actor, "orchestration_session_credential.renew",
			"core.api_token", updated.ID,
			map[string]any{"purpose": OrchestrationSessionCredentialPurpose})
	})
	return expiresAt, err
}

func validateOrchestrationSessionCredentialSpec(spec OrchestrationSessionCredentialSpec) error {
	if !validCommunicationSessionTenantID(spec.Tenant) ||
		!validCommunicationSessionWorkspaceID(spec.WorkspaceID) ||
		!validCommunicationSessionRef(spec.SessionRef) ||
		!validCommunicationSessionRunRef(spec.RunRef) ||
		spec.ClaimFence < 1 {
		return fmt.Errorf("%w: UUIDv7 business tenant, workspace_id, session_ref, run_ref, and claim_fence required", ErrInvalidToken)
	}
	if spec.AgentRef == "" || !validCommunicationSessionAgentRef(spec.AgentRef) {
		return fmt.Errorf("%w: invalid agent_ref", ErrInvalidToken)
	}
	if !validOrchestrationGrant(spec.ProfileRef, spec.GrantID, spec.Capabilities) {
		return ErrInvalidToken
	}
	return nil
}

func orchestrationSessionBindingMatches(
	token model.APIToken,
	expected OrchestrationSessionCredentialSpec,
) bool {
	return token.BoundTenantID == expected.Tenant && token.WorkspaceID == expected.WorkspaceID &&
		token.SessionRef == expected.SessionRef && token.SessionRunRef == expected.RunRef &&
		token.AgentRef == expected.AgentRef && token.SessionFence == expected.ClaimFence &&
		token.Name == orchestrationSessionCredentialName(expected) && token.Scope == strings.Join(expected.Capabilities, " ")
}

// orchestrationSessionCredentialExpired uses a closed upper bound: the bearer
// is dead at its exact deadline. Ordinary legacy tokens retain their historical
// strict-Before behavior.
func orchestrationSessionCredentialExpired(expiresAt, now model.Timestamp) bool {
	return !now.Time().Before(expiresAt.Time())
}

func orchestrationSessionPrincipal(token model.APIToken) (Principal, bool) {
	binding, validGrant := parseOrchestrationSessionCredentialName(token.Name)
	if !validGrant || token.Scope != strings.Join(binding.Capabilities, " ") {
		return Principal{}, false
	}
	if token.Purpose != OrchestrationSessionCredentialPurpose || token.IsSuperadmin ||
		!token.UserID.IsZero() || !validCommunicationSessionTenantID(token.BoundTenantID) ||
		!validCommunicationSessionWorkspaceID(token.WorkspaceID) || token.Role != orchestrationSessionRole ||

		token.ExpiresAt == nil || token.Audience != "" || !token.ActAsUserID.IsZero() ||
		!token.ParentTokenID.IsZero() || !validCommunicationSessionRef(token.SessionRef) ||
		!validCommunicationSessionRunRef(token.SessionRunRef) || token.SessionFence < 1 ||
		(token.AgentRef == "" || !validCommunicationSessionAgentRef(token.AgentRef)) {
		return Principal{}, false
	}
	p := newPrincipal(KindToken, "", token.ID, false, token.Name,
		map[model.TenantID]string{token.BoundTenantID: orchestrationSessionRole}, nil)
	p.AgentIdentity = token.AgentRef
	p.SessionIdentity = token.SessionRef
	p.SessionWorkspaceID = token.WorkspaceID
	p.SessionRunRef = token.SessionRunRef
	p.SessionFence = token.SessionFence
	p = p.withConfinements(map[model.TenantID]model.ID{
		token.BoundTenantID: token.WorkspaceID,
	})
	permissions := orchestrationGrantPermissions(binding.Capabilities)
	return p.withRestrictedPermissions(token.BoundTenantID, permissions...), true
}

// OrchestrationSessionGrantBinding is immutable credential evidence. It must be
// compared with the current operator-owned launch profile on every operation.
type OrchestrationSessionGrantBinding struct {
	ProfileRef   string
	GrantID      model.ID
	Capabilities []string
}

// OrchestrationCapabilityPermission maps the closed orchestration vocabulary
// to route admission. Domain handlers must also check the exact capability.
func OrchestrationCapabilityPermission(capability string) (Permission, bool) {
	switch capability {
	case "work.read":
		return "sessions:work:read", true
	case "work.create", "work.review":
		return "sessions:work:write", true
	case "work.assign":
		return "sessions:work:admin", true
	case "decision.read":
		return "sessions:decision:read", true
	case "decision.write":
		return "sessions:decision:write", true
	default:
		return "", false
	}
}

func validOrchestrationGrant(profile string, id model.ID, capabilities []string) bool {
	if !strings.HasPrefix(profile, "ppf_") || !validCommunicationSessionUUIDv7(strings.TrimPrefix(profile, "ppf_")) ||
		!validCommunicationSessionWorkspaceID(id) || len(capabilities) == 0 || len(capabilities) > 6 || !sort.StringsAreSorted(capabilities) {
		return false
	}
	previous := ""
	for _, cap := range capabilities {
		if _, ok := OrchestrationCapabilityPermission(cap); !ok || cap == previous {
			return false
		}
		previous = cap
	}
	return true
}

func orchestrationSessionCredentialName(spec OrchestrationSessionCredentialSpec) string {
	return orchestrationSessionName + ":" + spec.ProfileRef + ":" + spec.GrantID.String() + ":" + strings.Join(spec.Capabilities, ",")
}

func parseOrchestrationSessionCredentialName(name string) (OrchestrationSessionGrantBinding, bool) {
	parts := strings.Split(name, ":")
	if len(parts) != 4 || parts[0] != orchestrationSessionName {
		return OrchestrationSessionGrantBinding{}, false
	}
	id, err := model.ParseID(parts[2])
	binding := OrchestrationSessionGrantBinding{ProfileRef: parts[1], GrantID: id, Capabilities: strings.Split(parts[3], ",")}
	return binding, err == nil && validOrchestrationGrant(binding.ProfileRef, binding.GrantID, binding.Capabilities)
}

func orchestrationGrantPermissions(capabilities []string) []Permission {
	permissions := []Permission{}
	seen := map[Permission]bool{}
	for _, cap := range capabilities {
		permission, ok := OrchestrationCapabilityPermission(cap)
		if ok && !seen[permission] {
			permissions = append(permissions, permission)
			seen[permission] = true
		}
	}
	return permissions
}

// OrchestrationSessionGrant returns the separate, exact profile capability
// binding. Parsing display text alone never confers the authenticated marker.
func (p Principal) OrchestrationSessionGrant() (OrchestrationSessionGrantBinding, bool) {
	binding, ok := parseOrchestrationSessionCredentialName(p.DisplayName)
	tenant := singleRestrictedTenant(p)
	if !ok || tenant.IsZero() || p.AgentIdentity == "" || p.SessionIdentity == "" || p.SessionRunRef == "" || p.SessionFence < 1 {
		return OrchestrationSessionGrantBinding{}, false
	}
	workspace, confined := p.ConfinedWorkspaceIn(tenant)
	if !confined || workspace != p.SessionWorkspaceID || !exactRestrictedPermissionSet(p.restricted, tenant, orchestrationGrantPermissions(binding.Capabilities)...) {
		return OrchestrationSessionGrantBinding{}, false
	}
	return binding, true
}

func (p Principal) IsOrchestrationSessionCredential() bool {
	_, ok := p.OrchestrationSessionGrant()
	return ok
}
