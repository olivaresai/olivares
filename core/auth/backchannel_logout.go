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

// EndIdPSessions applies a verified OIDC back-channel logout token from the
// provider selected for scope and alias (OIDC Back-Channel Logout 1.0 section 2.7).
// A token naming the person (sub) ends every session of that person, with or without
// a sid: an administrator's sign-out in Keycloak posts one token carrying the person
// and the sid of only one of their IdP sessions, and a person signed out at the
// provider must have no session left. A token with only a sid, or whose sub names no
// account (sign-in binds the subject after minting the session, best-effort), ends
// the sessions minted from that IdP session. Both keys are qualified by the token's
// verified issuer and resolve only through the issuer-qualified sso_subject and
// idp_session columns, never by external id or email, so no other provider's subject
// or session can be named. A tenant's provider ends only sessions confined to that
// tenant. It returns how many sessions it ended; one audit record per person names
// the provider.
func (a *Authenticator) EndIdPSessions(ctx context.Context, scope model.TenantID, alias string, tok LogoutToken) (int, error) {
	if tok.Subject == "" && tok.SessionID == "" {
		return 0, ErrInvalidLogoutToken
	}
	var subject, sid QualifiedKey
	var err error
	if tok.Subject != "" {
		if subject, err = QualifiedSubjectKey(tok.Issuer, tok.Subject); err != nil {
			return 0, errors.Join(ErrInvalidLogoutToken, err)
		}
	}
	if tok.SessionID != "" {
		if sid, err = QualifiedSubjectKey(tok.Issuer, tok.SessionID); err != nil {
			return 0, errors.Join(ErrInvalidLogoutToken, err)
		}
	}
	ended := 0
	err = a.st.AuthMutate(ctx, func(as store.AuthScope) error {
		var person model.ID
		if subject != "" {
			users, _, err := as.Users().List(ctx, byEq("sso_subject", string(subject), 1))
			if err != nil {
				return err
			}
			if len(users) > 0 {
				person = users[0].ID
			}
		}
		query := byEq("user_id", person.String(), 0)
		if person.IsZero() {
			if sid == "" {
				return nil // an unknown subject without a sid has nothing to end
			}
			query = byEq("idp_session", string(sid), 0)
		}
		sessions, err := drainList(ctx, as.Sessions().List, query)
		if err != nil {
			return err
		}
		perPerson := map[model.ID]int{}
		if !person.IsZero() {
			perPerson[person] = 0 // a known person's logout is recorded even with nothing left to end
		}
		for _, s := range sessions {
			if s.Revoked || isTenantScope(scope) && s.TenantScope != scope {
				continue
			}
			s.Revoked = true
			if _, err := as.Sessions().Update(ctx, s); err != nil {
				return err
			}
			perPerson[s.UserID]++
		}
		for user, n := range perPerson {
			if _, err := as.Audit().Append(ctx, model.AuditDraft{
				Actor: model.ActorSystem, ActorKind: model.ActorSystem,
				Action: "sso.logout.backchannel", TargetKind: "core.user", TargetID: user,
				Meta: map[string]any{"scope": scope.String(), "idp": alias, "sessions": n},
			}); err != nil {
				return err
			}
			ended += n
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return ended, nil
}
