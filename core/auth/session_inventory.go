// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"errors"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

var ErrCurrentSession = errors.New("use sign out to end the current session")

type sessionClientKindContextKey struct{}

// WithSessionClientKind records the transport observed at the HTTP edge. It is
// display-only request metadata: transport hints are client-asserted and must
// never grant authority or serve as evidence of a verified device type.
func WithSessionClientKind(ctx context.Context, kind string) context.Context {
	return context.WithValue(ctx, sessionClientKindContextKey{}, kind)
}

func sessionClientKind(ctx context.Context) string {
	if kind, _ := ctx.Value(sessionClientKindContextKey{}).(string); kind == model.SessionClientBrowser {
		return model.SessionClientBrowser
	}
	return model.SessionClientToken
}

// ownSessionActor reports whether actor is the person themself. A token, or an
// agent acting on a person's behalf (a delegated handle carries the person's
// source session), has no human session inventory to read or end.
func ownSessionActor(actor Principal) bool {
	if actor.Kind != KindUser || actor.UserID.IsZero() || actor.AgentIdentity != "" {
		return false
	}
	_, delegated := actor.ActAs()
	return !delegated
}

// A tenant-scoped sign-in cannot reach account-wide sign-ins or another tenant.
func ownsSession(actor Principal, session model.AuthSession) bool {
	return session.UserID == actor.UserID &&
		(actor.SessionScope().IsZero() || session.TenantScope == actor.SessionScope())
}

// ListOwnSessions returns only live sessions belonging to the authenticated
// person.
func (a *Authenticator) ListOwnSessions(ctx context.Context, actor Principal) ([]model.AuthSession, error) {
	if !ownSessionActor(actor) {
		return nil, ErrUnauthenticated
	}
	now := a.clock.Now()
	out := make([]model.AuthSession, 0)
	err := a.st.AuthView(ctx, func(as store.AuthScope) error {
		sessions, err := drainList(ctx, as.Sessions().List, byEq("user_id", actor.UserID.String(), 0))
		if err != nil {
			return err
		}
		for _, session := range sessions {
			if !ownsSession(actor, session) || session.Revoked || session.DeletedAt != nil || !now.Before(session.ExpiresAt) {
				continue
			}
			out = append(out, session)
		}
		return nil
	})
	return out, err
}

// RevokeOwnSession ends one of the caller's other sessions. Cross-user ids are
// concealed as not found; ending the calling session remains logout's job.
func (a *Authenticator) RevokeOwnSession(ctx context.Context, actor Principal, sessionID model.ID) error {
	if !ownSessionActor(actor) {
		return ErrUnauthenticated
	}
	if actor.CredID == sessionID {
		return ErrCurrentSession
	}
	return a.revokeSession(ctx, actor, sessionID, true)
}

// RevokeSessionsInTenant ends a member's access from the selected tenant. The
// HTTP route checks user:write; the guards below are those of ResetTOTPInTenant
// and RemoveMember, so an administrator cannot reach a member above their own
// role, or themself, and the authority lock and membership check keep them from
// selecting an account outside that organization.
func (a *Authenticator) RevokeSessionsInTenant(ctx context.Context, actor Principal, tenant model.TenantID, userID model.ID) error {
	if !ownSessionActor(actor) || userID.IsZero() {
		return ErrUnauthenticated
	}
	if !a.stepUpSatisfied(ctx, actor) {
		return ErrStepUpRequired
	}
	if tenant.IsZero() || tenant.IsSystem() {
		return ErrInvalidToken
	}
	if _, confined := actor.ConfinedWorkspaceIn(tenant); confined {
		return ErrWorkspaceConfined
	}
	if actor.UserID == userID {
		return ErrCurrentSession
	}
	// Match member removal: conceal nonmembers before taking an authority lock.
	if _, err := a.SCIMGetMember(ctx, tenant, userID); err != nil {
		return err
	}
	return a.st.AuthMutate(ctx, func(as store.AuthScope) error {
		if err := prepareUserAuthorityWrite(ctx, as, userID); err != nil {
			return err
		}
		_, member, err := membershipOf(ctx, as, userID, tenant)
		if err != nil {
			return err
		}
		if !member {
			return store.ErrNotFound
		}
		target, err := as.Users().Get(ctx, userID)
		if err != nil {
			return err
		}
		grants, _, _, err := loadGrants(ctx, as, userID, target.IsSuperadmin)
		if err != nil {
			return err
		}
		ceiling := grants[tenant]
		if target.IsSuperadmin {
			ceiling = RoleOwner
		}
		if err := checkRoleCeiling(actor, tenant, ceiling); err != nil {
			return err
		}
		// A tenant ends only the sessions it governs: scoped rows are revoked and
		// account-scope rows are excluded from this tenant, never from another.
		sessions, err := drainList(ctx, as.Sessions().List, byEq("user_id", userID.String(), 0))
		if err != nil {
			return err
		}
		exclusions, err := drainList(ctx, as.TenantExclusions().List, byEq("user_id", userID.String(), 0))
		if err != nil {
			return err
		}
		alreadyExcluded := make(map[model.ID]bool, len(exclusions))
		for _, exclusion := range exclusions {
			if exclusion.Kind == model.ExclusionSession && exclusion.TargetTenantID == tenant {
				alreadyExcluded[exclusion.SessionID] = true
			}
		}
		revoked, excluded := 0, 0
		for _, session := range sessions {
			if session.Revoked {
				continue
			}
			if session.TenantScope == tenant {
				revoked++
			}
			if session.TenantScope.IsZero() && !alreadyExcluded[session.ID] {
				excluded++
			}
		}
		if err := excludeAccountSessions(ctx, as, actor, userID, tenant); err != nil {
			return err
		}
		_, err = as.Audit().Append(ctx, model.AuditDraft{
			Actor: actor.Actor(), ActorKind: actor.ActorKind(),
			Action: "auth.sessions.admin_revoke", TargetKind: "core.user", TargetID: userID,
			Meta: map[string]any{"tenant": tenant.String(), "revoked": revoked, "excluded": excluded},
		})
		return err
	})
}
