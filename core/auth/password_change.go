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

var (
	ErrCurrentPasswordIncorrect = errors.New("The current password is incorrect.")
	ErrNoLocalPassword          = errors.New("This account uses an identity provider. Change your password there.")
)

// verifyPasswordLoginTx keeps a password proof and session issuance in the
// same User authority order as a password change. A proof of a retired hash
// cannot mint a session or spend its pending second factor.
func verifyPasswordLoginTx(ctx context.Context, as store.AuthScope, userID model.ID, verifiedHash string) error {
	if verifiedHash == "" {
		return ErrInvalidCredentials
	}
	if err := prepareUserAuthorityWrite(ctx, as, userID); err != nil {
		return err
	}
	user, err := as.Users().Get(ctx, userID)
	if errors.Is(err, store.ErrNotFound) {
		return ErrInvalidCredentials
	}
	if err != nil {
		return err
	}
	if user.Status != model.StatusActive || user.DeletedAt != nil || user.PasswordHash != verifiedHash {
		return ErrInvalidCredentials
	}
	return nil
}

// ChangeOwnPassword requires a live human session and its account's current
// password. Password work runs outside transactions. The final write rechecks
// both proofs under the existing User authority lock and commits the hash,
// sibling-session revocations and audit together. The calling session is untouched.
func (a *Authenticator) ChangeOwnPassword(ctx context.Context, actor Principal, current, next, ip string, forwarded []string) error {
	ref, ok := actor.Ref()
	if !ok || actor.Kind != KindUser {
		return ErrUnauthenticated
	}
	var user model.User
	if err := a.st.AuthView(ctx, func(as store.AuthScope) error {
		var err error
		user, err = passwordChangeUser(ctx, as, actor, ref)
		return err
	}); err != nil {
		return err
	}
	if user.PasswordHash == "" {
		return ErrNoLocalPassword
	}
	accountKey := "email:" + normalizeEmail(user.Email)
	addressKey := "ip:" + a.trustedLoginProxies.clientAddress(ip, forwarded)
	verdict, wait := a.throttle.decide(accountKey, addressKey)
	if verdict == loginRefuse {
		return ErrLockedOut
	}
	outcome := loginAbandoned
	defer func() { a.throttle.record(accountKey, addressKey, verdict, outcome) }()
	if verdict == loginDelay {
		if err := a.throttle.waitOut(ctx, wait); err != nil {
			return err
		}
	}
	match, err := VerifyPassword(current, user.PasswordHash)
	if err != nil || !match {
		outcome = loginFailed
		return ErrCurrentPasswordIncorrect
	}
	if len(next) < MinPasswordLen {
		return ErrWeakPassword
	}
	hash, err := HashPassword(next)
	if err != nil {
		return err
	}
	err = a.st.AuthMutate(ctx, func(as store.AuthScope) error {
		if err := prepareUserAuthorityWrite(ctx, as, user.ID); err != nil {
			return err
		}
		live, err := passwordChangeUser(ctx, as, actor, ref)
		if err != nil {
			return err
		}
		if live.PasswordHash != user.PasswordHash {
			return ErrCurrentPasswordIncorrect
		}
		sessions, err := drainList(ctx, as.Sessions().List, byEq("user_id", user.ID.String(), 0))
		if err != nil {
			return err
		}
		live.PasswordHash = hash
		if _, err := as.Users().Update(ctx, live); err != nil {
			return err
		}
		revoked := 0
		for _, session := range sessions {
			if session.ID == actor.CredID || session.Revoked {
				continue
			}
			session.Revoked = true
			if _, err := as.Sessions().Update(ctx, session); err != nil {
				return err
			}
			revoked++
		}
		event, err := as.Audit().Append(ctx, model.AuditDraft{
			Actor: "user:" + live.ID.String(), ActorKind: model.ActorUser,
			Action: "account.password.changed", TargetKind: "core.user", TargetID: live.ID,
			Meta: map[string]any{"revoked_sessions": revoked},
		})
		if err != nil {
			return err
		}
		if event.Seq == 0 {
			return store.ErrDirectoryUnavailable
		}
		return nil
	})
	if err == nil {
		outcome = loginSucceeded
	}
	return err
}

// passwordChangeUser derives the account from the exact stored session, never
// a request-selected user or a caller's carried grants.
func passwordChangeUser(ctx context.Context, as store.AuthScope, actor Principal, ref PrincipalRef) (model.User, error) {
	session, err := as.Sessions().Get(ctx, ref.credentialID)
	if errors.Is(err, store.ErrNotFound) {
		return model.User{}, ErrUnauthenticated
	}
	if err != nil {
		return model.User{}, err
	}
	clock, ok := as.(store.AuthPrincipalEvidenceScope)
	if !ok {
		return model.User{}, ErrPrincipalEvidenceUnavailable
	}
	now, err := clock.TransactionNow(ctx)
	if err != nil {
		return model.User{}, err
	}
	if now.IsZero() || !exactSessionCredentialRow(session, ref) || session.DeletedAt != nil || session.Revoked || !now.Before(session.ExpiresAt) || session.UserID != actor.UserID {
		return model.User{}, ErrUnauthenticated
	}
	user, err := as.Users().Get(ctx, session.UserID)
	if errors.Is(err, store.ErrNotFound) {
		return model.User{}, ErrUnauthenticated
	}
	if err != nil {
		return model.User{}, err
	}
	if user.Status != model.StatusActive || user.DeletedAt != nil {
		return model.User{}, ErrUnauthenticated
	}
	if !session.TenantScope.IsZero() {
		excluded, err := subjectExcluded(ctx, as, session.TenantScope, user.ID)
		if err != nil {
			return model.User{}, err
		}
		if excluded {
			return model.User{}, ErrUnauthenticated
		}
		if _, found, err := membershipOf(ctx, as, user.ID, session.TenantScope); err != nil {
			return model.User{}, err
		} else if !found {
			return model.User{}, ErrUnauthenticated
		}
	}
	return user, nil
}
