// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const PrefixPairing = "olvp"

var ErrPairingName = errors.New("device name must contain 1 to 80 characters without control characters")
var ErrPairingLimit = errors.New("end a pending pairing before creating another link")

// CreateSessionPairing creates a ten-minute, single-use link for an owner-named
// device. The pending row has no authenticating credential, even if the token's
// prefix is changed. It remains visible and revocable in the owner's inventory.
func (a *Authenticator) CreateSessionPairing(ctx context.Context, actor Principal, name string) (string, model.AuthSession, error) {
	if !ownSessionActor(actor) {
		return "", model.AuthSession{}, ErrUnauthenticated
	}
	name = strings.TrimSpace(name)
	if len([]rune(name)) < 1 || len([]rune(name)) > 80 || strings.ContainsFunc(name, unicode.IsControl) {
		return "", model.AuthSession{}, ErrPairingName
	}
	ctx = a.withStepUpPolicy(ctx)
	if !a.stepUpSatisfied(ctx, actor) {
		return "", model.AuthSession{}, ErrStepUpRequired
	}
	credential, err := NewCredential(PrefixPairing)
	if err != nil {
		return "", model.AuthSession{}, err
	}
	var pending model.AuthSession
	err = a.st.AuthMutate(ctx, func(as store.AuthScope) error {
		if err := a.guardNewLoginSession(ctx, as); err != nil {
			return err
		}
		if err := prepareUserAuthorityWrite(ctx, as, actor.UserID); err != nil {
			return err
		}
		source, err := as.Sessions().Get(ctx, actor.CredID)
		if err != nil {
			return err
		}
		current, err := a.principalFromSession(ctx, as, source)
		if err != nil {
			return err
		}
		ref, valid := actor.Ref()
		if !valid || ref.version != source.Version || !ownsSession(actor, source) || !StepUpSatisfied(ctx, current) {
			return ErrUnauthenticated
		}
		rows, err := drainList(ctx, as.Sessions().List, byEq("user_id", actor.UserID.String(), 0))
		if err != nil {
			return err
		}
		count := 0
		now := a.clock.Now()
		for _, row := range rows {
			if !row.PairingSourceID.IsZero() && !row.Revoked && now.Before(row.ExpiresAt) {
				count++
			}
		}
		if count >= 5 {
			return ErrPairingLimit
		}
		expires := model.NewTimestamp(now.Time().Add(10 * time.Minute))
		if source.ExpiresAt.Before(expires) {
			expires = source.ExpiresAt
		}
		pending, err = as.Sessions().Create(ctx, model.AuthSession{UserID: actor.UserID, Selector: credential.Selector, SecretHash: credential.SecretHash,
			ExpiresAt: expires, TenantScope: source.TenantScope, AAL: 1, ClientKind: model.SessionClientPairing, DeviceName: name, PairingSourceID: source.ID})
		if err != nil {
			return err
		}
		_, err = as.Audit().Append(ctx, model.AuditDraft{Actor: actor.Actor(), ActorKind: actor.ActorKind(), Action: "auth.session.pairing", TargetKind: "core.auth_session", TargetID: pending.ID})
		return err
	})
	if err != nil {
		return "", model.AuthSession{}, err
	}
	return credential.Token, pending, nil
}

// AcceptSessionPairing consumes a link and rotates its pending row into a named
// AAL1 session. The user-authority lock serializes consume/revoke and exclusions;
// the versioned update and changed selector make every replay fail. No assurance
// or authority is copied from a stale browser principal.
func (a *Authenticator) AcceptSessionPairing(ctx context.Context, token, ip string) (string, model.AuthSession, error) {
	prefix, selector, secret, ok := ParseToken(token)
	if !ok || prefix != PrefixPairing {
		return "", model.AuthSession{}, ErrUnauthenticated
	}
	if _, err := a.beginLogin(ctx, ip); err != nil {
		return "", model.AuthSession{}, err
	}
	// Policy implementations may read their own store: never invoke them while
	// holding the directory writer. Pin the source version for transactional revalidation.
	var admittedSource model.AuthSession
	var admittedUser model.User
	if err := a.st.AuthView(ctx, func(as store.AuthScope) error {
		rows, _, err := as.Sessions().List(ctx, byEq("selector", selector, 1))
		if err != nil {
			return err
		}
		if len(rows) != 1 || !SecretMatches(secret, rows[0].SecretHash) || rows[0].PairingSourceID.IsZero() {
			return ErrUnauthenticated
		}
		admittedSource, err = as.Sessions().Get(ctx, rows[0].PairingSourceID)
		if err != nil {
			return err
		}
		admittedUser, err = as.Users().Get(ctx, admittedSource.UserID)
		return err
	}); err != nil {
		return "", model.AuthSession{}, err
	}
	if !slices.Contains(admittedSource.AMR, "sso") {
		if err := a.enforceRequireSSO(ctx, admittedUser); err != nil {
			return "", model.AuthSession{}, err
		}
	}
	var result model.AuthSession
	var bearer string
	err := a.st.AuthMutate(ctx, func(as store.AuthScope) error {
		if err := a.guardNewLoginSession(ctx, as); err != nil {
			return err
		}
		rows, _, err := as.Sessions().List(ctx, byEq("selector", selector, 1))
		if err != nil {
			return err
		}
		if len(rows) != 1 || !SecretMatches(secret, rows[0].SecretHash) {
			return ErrUnauthenticated
		}
		if err := prepareUserAuthorityWrite(ctx, as, rows[0].UserID); err != nil {
			return err
		}
		pending, err := as.Sessions().Get(ctx, rows[0].ID)
		if err != nil {
			return err
		}
		now := a.clock.Now()
		if pending.Selector != selector || !SecretMatches(secret, pending.SecretHash) || pending.PairingSourceID.IsZero() || pending.Revoked || pending.DeletedAt != nil || !now.Before(pending.ExpiresAt) {
			return ErrUnauthenticated
		}
		source, err := as.Sessions().Get(ctx, pending.PairingSourceID)
		if err != nil {
			return err
		}
		current, err := a.principalFromSession(ctx, as, source)
		if err != nil {
			return err
		}
		if source.ID != admittedSource.ID || source.Version != admittedSource.Version || source.UserID != pending.UserID || source.TenantScope != pending.TenantScope {
			return ErrUnauthenticated
		}
		user, err := as.Users().Get(ctx, source.UserID)
		if err != nil {
			return err
		}
		if user.Version != admittedUser.Version {
			return ErrUnauthenticated
		}
		var credential Credential
		if pending.TenantScope.IsZero() {
			credential, err = NewCredential(PrefixSession)
		} else {
			credential, err = NewCredential(PrefixScopedSession)
		}
		if err != nil {
			return err
		}
		exclusions, err := drainList(ctx, as.TenantExclusions().List, byEq("user_id", source.UserID.String(), 0))
		if err != nil {
			return err
		}
		for _, exclusion := range exclusions {
			if exclusion.Kind == model.ExclusionSession && exclusion.SessionID == source.ID {
				if err := excludeSession(ctx, as, current, source.UserID, pending.ID, exclusion.TargetTenantID); err != nil {
					return err
				}
			}
		}
		pending.Selector, pending.SecretHash = credential.Selector, credential.SecretHash
		pending.PairingSourceID = ""
		pending.CreatedIP, pending.ClientKind = ip, sessionClientKind(ctx)
		pending.AAL, pending.AMR, pending.AALExpiresAt, pending.AALAuthenticatedAt = 1, []string{"pairing"}, nil, nil
		pending.LastUsedAt = &now
		pending.ExpiresAt = model.NewTimestamp(now.Time().Add(a.sessionTTL))
		if source.ExpiresAt.Before(pending.ExpiresAt) {
			pending.ExpiresAt = source.ExpiresAt
		}
		result, err = as.Sessions().Update(ctx, pending)
		if err != nil {
			return err
		}
		_, err = as.Audit().Append(ctx, model.AuditDraft{Actor: current.Actor(), ActorKind: current.ActorKind(), Action: "auth.session.paired", TargetKind: "core.auth_session", TargetID: pending.ID})
		if err != nil {
			return err
		}
		bearer = credential.Token
		return nil
	})
	if err != nil {
		return "", model.AuthSession{}, err
	}
	return bearer, result, nil
}
