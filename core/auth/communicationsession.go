// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const (
	CommunicationSessionCredentialPurpose = "communication-session"

	CommunicationSessionDeliveryRead         Permission = "sessions:delivery:read"
	CommunicationSessionDeliveryWrite        Permission = "sessions:delivery:write"
	CommunicationSessionMessageSendWrite     Permission = "sessions:message-send:write"
	CommunicationSessionHandoffResponseWrite Permission = "sessions:handoff-response:write"

	communicationSessionRole  = "communication-session"
	communicationSessionScope = "sessions:delivery:read sessions:delivery:write sessions:message-send:write sessions:handoff-response:write"
	communicationSessionName  = "sessions-runtime-communication"

	DefaultCommunicationSessionCredentialTTL = 30 * time.Minute
)

// CommunicationSessionCredentialSpec is the complete server-proven identity
// of one supervised communication runtime. AgentRef may be empty only as the
// exact server-authored fact that the launch has no authenticated agent; it is
// never accepted from request payloads or recovered from a display name.
type CommunicationSessionCredentialSpec struct {
	Tenant      model.TenantID
	WorkspaceID model.ID
	SessionRef  string
	RunRef      string
	AgentRef    string
	ClaimFence  int64
}

// CommunicationSessionCredential is the show-once bearer plus its durable,
// non-sensitive revocation binding. The raw Token must not be persisted.
type CommunicationSessionCredential struct {
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

// IssueCommunicationSessionCredential mints a private HTTP bearer for the
// exact runtime generation. It is deliberately separate from work-session:
// neither issuer accepts a purpose parameter and neither ceiling can grow into
// the other. The composition root is the only caller and must invoke this only
// after sessions has acquired the live Claim and resolved its authz workspace.
func (a *Authenticator) IssueCommunicationSessionCredential(
	ctx context.Context,
	actor Principal,
	spec CommunicationSessionCredentialSpec,
) (CommunicationSessionCredential, error) {
	if !actor.IsSystemOperator() {
		return CommunicationSessionCredential{}, ErrRoleCeiling
	}
	if err := validateCommunicationSessionCredentialSpec(spec); err != nil {
		return CommunicationSessionCredential{}, err
	}

	cred, err := NewCredential(PrefixToken)
	if err != nil {
		return CommunicationSessionCredential{}, err
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
			{Column: "purpose", Op: model.OpEq, Value: CommunicationSessionCredentialPurpose},
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
			return fmt.Errorf("%w: stale communication-session claim fence", ErrUnauthenticated)
		}
		if spec.ClaimFence == maxFence {
			for _, old := range previous {
				if old.SessionFence == maxFence &&
					!communicationSessionBindingMatches(old, spec) {
					return fmt.Errorf("%w: communication-session binding changed without a new claim fence", ErrUnauthenticated)
				}
			}
		}
		for _, old := range previous {
			if old.Revoked || old.ExpiresAt == nil ||
				communicationSessionCredentialExpired(*old.ExpiresAt, now) {
				continue
			}
			if err := revokeTokenTree(ctx, as, actor, old.ID); err != nil {
				return err
			}
		}

		expiresAt = now.Time().Add(DefaultCommunicationSessionCredentialTTL)
		expiresTS := model.NewTimestamp(expiresAt)
		token, err := as.Tokens().Create(ctx, model.APIToken{
			Name: communicationSessionName, Selector: cred.Selector, SecretHash: cred.SecretHash,
			BoundTenantID: spec.Tenant, Role: communicationSessionRole, ExpiresAt: &expiresTS,
			Purpose: CommunicationSessionCredentialPurpose, Scope: communicationSessionScope,
			AgentRef: spec.AgentRef, SessionRef: spec.SessionRef, WorkspaceID: spec.WorkspaceID,
			SessionRunRef: spec.RunRef, SessionFence: spec.ClaimFence,
		})
		if err != nil {
			return err
		}
		stored = token
		return metaAudit(ctx, as, actor, "communication_session_credential.issue",
			"core.api_token", token.ID,
			map[string]any{"purpose": CommunicationSessionCredentialPurpose})
	})
	if err != nil {
		return CommunicationSessionCredential{}, err
	}
	return CommunicationSessionCredential{
		Token: cred.Token, ID: stored.ID, Tenant: stored.BoundTenantID,
		WorkspaceID: stored.WorkspaceID, SessionRef: stored.SessionRef,
		RunRef: stored.SessionRunRef, AgentRef: stored.AgentRef,
		ClaimFence: stored.SessionFence, ExpiresAt: expiresAt,
	}, nil
}

// RevokeCommunicationSessionCredential revokes only the exact credential named
// by expected. A missing handle is idempotent; a crossed sibling handle is not.
func (a *Authenticator) RevokeCommunicationSessionCredential(
	ctx context.Context,
	actor Principal,
	id model.ID,
	expected CommunicationSessionCredentialSpec,
) error {
	if !actor.IsSystemOperator() || id.IsZero() {
		return ErrRoleCeiling
	}
	if err := validateCommunicationSessionCredentialSpec(expected); err != nil {
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
		if _, ok := communicationSessionPrincipal(token); !ok ||
			!communicationSessionBindingMatches(token, expected) {
			return ErrUnauthenticated
		}
		return revokeTokenTree(ctx, as, actor, id)
	})
}

// RenewCommunicationSessionCredential slides the SAME bearer expiry only after
// the runtime has renewed its Claim. It never rotates or returns secret material
// and never resurrects an expired or revoked credential.
func (a *Authenticator) RenewCommunicationSessionCredential(
	ctx context.Context,
	actor Principal,
	id model.ID,
	expected CommunicationSessionCredentialSpec,
) (time.Time, error) {
	if !actor.IsSystemOperator() || id.IsZero() {
		return time.Time{}, ErrRoleCeiling
	}
	if err := validateCommunicationSessionCredentialSpec(expected); err != nil {
		return time.Time{}, err
	}
	var expiresAt time.Time
	err := a.st.AuthMutate(ctx, func(as store.AuthScope) error {
		token, err := as.Tokens().Get(ctx, id)
		if err != nil {
			return err
		}
		now := a.clock.Now()
		if _, ok := communicationSessionPrincipal(token); !ok || token.Revoked ||
			token.ExpiresAt == nil || communicationSessionCredentialExpired(*token.ExpiresAt, now) ||
			!communicationSessionBindingMatches(token, expected) {
			return ErrUnauthenticated
		}
		expiresAt = now.Time().Add(DefaultCommunicationSessionCredentialTTL)
		expiresTS := model.NewTimestamp(expiresAt)
		token.ExpiresAt = &expiresTS
		updated, err := as.Tokens().Update(ctx, token)
		if err != nil {
			return err
		}
		return metaAudit(ctx, as, actor, "communication_session_credential.renew",
			"core.api_token", updated.ID,
			map[string]any{"purpose": CommunicationSessionCredentialPurpose})
	})
	return expiresAt, err
}

func validateCommunicationSessionCredentialSpec(spec CommunicationSessionCredentialSpec) error {
	if !validCommunicationSessionTenantID(spec.Tenant) ||
		!validCommunicationSessionWorkspaceID(spec.WorkspaceID) ||
		!validCommunicationSessionRef(spec.SessionRef) ||
		!validCommunicationSessionRunRef(spec.RunRef) ||
		spec.ClaimFence < 1 {
		return fmt.Errorf("%w: UUIDv7 business tenant, workspace_id, session_ref, run_ref, and claim_fence required", ErrInvalidToken)
	}
	if !validCommunicationSessionAgentRef(spec.AgentRef) {
		return fmt.Errorf("%w: invalid agent_ref", ErrInvalidToken)
	}
	return nil
}

func validCommunicationSessionTenantID(tenant model.TenantID) bool {
	if tenant.IsZero() || tenant.IsSystem() {
		return false
	}
	raw := tenant.String()
	parsed, err := model.ParseTenantID(raw)
	return err == nil && parsed == tenant && validCommunicationSessionUUIDv7(raw)
}

func validCommunicationSessionWorkspaceID(id model.ID) bool {
	if id.IsZero() {
		return false
	}
	raw := id.String()
	parsed, err := model.ParseID(raw)
	return err == nil && parsed == id && validCommunicationSessionUUIDv7(raw)
}

func validCommunicationSessionRef(ref string) bool {
	const prefix = "osn_"
	return strings.HasPrefix(ref, prefix) &&
		validCommunicationSessionUUIDv7(strings.TrimPrefix(ref, prefix))
}

func validCommunicationSessionRunRef(ref string) bool {
	return validCommunicationSessionUUIDv7(ref)
}

func validCommunicationSessionUUIDv7(raw string) bool {
	parsed, err := uuid.Parse(raw)
	return err == nil && parsed != uuid.Nil && parsed.String() == raw &&
		parsed.Version() == uuid.Version(7) && parsed.Variant() == uuid.RFC4122
}

func validCommunicationSessionAgentRef(ref string) bool {
	return !strings.ContainsAny(ref, "\n\r\x00") && len(ref) <= 512
}

func communicationSessionBindingMatches(
	token model.APIToken,
	expected CommunicationSessionCredentialSpec,
) bool {
	return token.BoundTenantID == expected.Tenant && token.WorkspaceID == expected.WorkspaceID &&
		token.SessionRef == expected.SessionRef && token.SessionRunRef == expected.RunRef &&
		token.AgentRef == expected.AgentRef && token.SessionFence == expected.ClaimFence
}

// communicationSessionCredentialExpired uses a closed upper bound: the bearer
// is dead at its exact deadline. Ordinary legacy tokens retain their historical
// strict-Before behavior.
func communicationSessionCredentialExpired(expiresAt, now model.Timestamp) bool {
	return !now.Time().Before(expiresAt.Time())
}

func communicationSessionPrincipal(token model.APIToken) (Principal, bool) {
	if token.Purpose != CommunicationSessionCredentialPurpose || token.IsSuperadmin ||
		!token.UserID.IsZero() || !validCommunicationSessionTenantID(token.BoundTenantID) ||
		!validCommunicationSessionWorkspaceID(token.WorkspaceID) || token.Role != communicationSessionRole ||
		token.Scope != communicationSessionScope || token.Name != communicationSessionName ||
		token.ExpiresAt == nil || token.Audience != "" || !token.ActAsUserID.IsZero() ||
		!token.ParentTokenID.IsZero() || !validCommunicationSessionRef(token.SessionRef) ||
		!validCommunicationSessionRunRef(token.SessionRunRef) || token.SessionFence < 1 ||
		!validCommunicationSessionAgentRef(token.AgentRef) {
		return Principal{}, false
	}
	p := newPrincipal(KindToken, "", token.ID, false, token.Name,
		map[model.TenantID]string{token.BoundTenantID: communicationSessionRole}, nil)
	p.AgentIdentity = token.AgentRef
	p.SessionIdentity = token.SessionRef
	p.SessionWorkspaceID = token.WorkspaceID
	p.SessionRunRef = token.SessionRunRef
	p.SessionFence = token.SessionFence
	p = p.withConfinements(map[model.TenantID]model.ID{
		token.BoundTenantID: token.WorkspaceID,
	})
	return p.withRestrictedPermissions(token.BoundTenantID,
		CommunicationSessionDeliveryRead,
		CommunicationSessionDeliveryWrite,
		CommunicationSessionMessageSendWrite,
		CommunicationSessionHandoffResponseWrite,
	), true
}
