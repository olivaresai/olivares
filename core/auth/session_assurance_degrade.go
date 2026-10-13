// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// degradeScopedSessions lowers the account's elevated sessions scoped to t to
// AAL1.
func degradeScopedSessions(ctx context.Context, as store.AuthScope, user model.ID, t model.TenantID) error {
	sessions, err := drainList(ctx, as.Sessions().List, byEq("user_id", user.String(), 0))
	if err != nil {
		return err
	}
	for _, s := range sessions {
		if s.Revoked || s.TenantScope != t || s.AAL <= 1 {
			continue
		}
		s.AAL = 1
		s.AALExpiresAt = nil
		s.AALAuthenticatedAt = nil
		if _, err := as.Sessions().Update(ctx, s); err != nil {
			return err
		}
	}
	return nil
}

// excludeAccountSessionsOnly excludes every live account-scope session of the
// account from t, and leaves the sessions scoped to t in place.
func excludeAccountSessionsOnly(ctx context.Context, as store.AuthScope, actor Principal, user model.ID, t model.TenantID) error {
	sessions, err := drainList(ctx, as.Sessions().List, byEq("user_id", user.String(), 0))
	if err != nil {
		return err
	}
	for _, s := range sessions {
		if s.Revoked || !s.TenantScope.IsZero() {
			continue
		}
		if err := excludeSession(ctx, as, actor, user, s.ID, t); err != nil {
			return err
		}
	}
	return nil
}

// DegradeSessionAssurance downgrades all non-revoked elevated sessions for the
// user to AAL1 (step-up boundary for device-compliance events). Sessions already
// at AAL ≤ 1 are left untouched; revoked sessions are skipped.
func (a *Authenticator) DegradeSessionAssurance(ctx context.Context, actor Principal, userID model.ID) error {
	return a.st.AuthMutate(ctx, func(as store.AuthScope) error {
		if err := prepareUserAuthorityWrite(ctx, as, userID); err != nil {
			return err
		}
		sessions, _, err := as.Sessions().List(ctx, model.Query{Filters: []model.Filter{
			{Column: "user_id", Op: model.OpEq, Value: userID.String()},
		}, Limit: 1000})
		if err != nil {
			return err
		}
		for _, s := range sessions {
			if s.Revoked || s.AAL <= 1 {
				continue
			}
			s.AAL = 1
			s.AALExpiresAt = nil
			s.AALAuthenticatedAt = nil
			if _, err := as.Sessions().Update(ctx, s); err != nil {
				return err
			}
		}
		_, err = as.Audit().Append(ctx, model.AuditDraft{
			Actor: actor.Actor(), ActorKind: actor.ActorKind(),
			Action: "caep.assurance.degraded", TargetKind: "core.user", TargetID: userID,
		})
		return err
	})
}
