// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// QueuedCredential names the authenticated credential that authorized a durable
// engine request. Its fields are server-owned references, never bearer material.
// It is not an authentication mechanism and must not be accepted from clients.
type QueuedCredential struct {
	ID      model.ID
	UserID  model.ID
	Kind    PrincipalKind
	Version int64
	Seal    string
}

func QueuedCredentialFrom(p Principal) (QueuedCredential, bool) {
	ref, ok := p.Ref()
	if !ok {
		return QueuedCredential{}, false
	}
	return QueuedCredential{ID: ref.credentialID, Kind: ref.kind, Version: ref.version, UserID: p.UserID}, true
}

// RevalidateQueuedCredential reloads a server-recorded request's exact credential
// and current user/grants. Its Ref names the CURRENT version of that same
// credential, so the continuation can mint the one confined session credential.
// Only an engine continuation of an already-authorized durable request may call
// it; QueuedCredential is never accepted as authentication from a client.
func (a *Authenticator) RevalidateQueuedCredential(ctx context.Context, q QueuedCredential) (Principal, error) {
	if !validPrincipalRef(PrincipalRef{kind: q.Kind, credentialID: q.ID, version: q.Version}) {
		return Principal{}, ErrUnauthenticated
	}
	var p Principal
	err := a.st.AuthView(ctx, func(sc store.AuthScope) error {
		switch q.Kind {
		case KindUser:
			s, err := sc.Sessions().Get(ctx, q.ID)
			if err != nil {
				return err
			}
			if s.UserID != q.UserID || q.Seal == "" || queuedCredentialSeal(s.SecretHash) != q.Seal || s.Revoked || !a.clock.Now().Before(s.ExpiresAt) || s.DeletedAt != nil {
				return ErrUnauthenticated
			}
			p, err = a.principalFromSession(ctx, sc, s)
			return err
		case KindToken:
			t, err := sc.Tokens().Get(ctx, q.ID)
			if err != nil {
				return err
			}
			if t.UserID != q.UserID || t.Version != q.Version || q.Seal == "" || queuedCredentialSeal(t.SecretHash) != q.Seal || t.Revoked || t.DeletedAt != nil || (t.ExpiresAt != nil && !a.clock.Now().Before(*t.ExpiresAt)) {
				return ErrUnauthenticated
			}
			// Purpose-specific and delegated credentials require their own binding
			// contracts. They cannot become ordinary asynchronous launch authority.
			if t.Purpose != "" || t.SessionRef != "" || !t.WorkspaceID.IsZero() || t.SessionRunRef != "" || t.SessionFence != 0 || t.Audience != "" || !t.ActAsUserID.IsZero() {
				return ErrUnauthenticated
			}
			var found bool
			p, found, err = a.principalFromToken(ctx, sc, t)
			if err != nil {
				return err
			}
			if !found {
				return ErrUnauthenticated
			}
			p = p.withCredentialRef(t.Version)
		default:
			return ErrUnauthenticated
		}
		return nil
	})
	return p, err
}

// BindQueuedCredential records an irreversible verifier fingerprint of the exact
// authenticated credential. Assurance updates may bump row Version without changing
// this credential; rotation/revocation must still invalidate the continuation.
func (a *Authenticator) BindQueuedCredential(ctx context.Context, q QueuedCredential) (QueuedCredential, error) {
	err := a.st.AuthView(ctx, func(sc store.AuthScope) error {
		switch q.Kind {
		case KindUser:
			row, err := sc.Sessions().Get(ctx, q.ID)
			if err != nil {
				return err
			}
			if row.Version != q.Version || row.Revoked || row.DeletedAt != nil || !a.clock.Now().Before(row.ExpiresAt) {
				return ErrUnauthenticated
			}
			q.Seal = queuedCredentialSeal(row.SecretHash)
			q.UserID = row.UserID
		case KindToken:
			row, err := sc.Tokens().Get(ctx, q.ID)
			if err != nil {
				return err
			}
			if row.Version != q.Version || row.Revoked || row.DeletedAt != nil {
				return ErrUnauthenticated
			}
			q.Seal = queuedCredentialSeal(row.SecretHash)
			q.UserID = row.UserID
		default:
			return ErrUnauthenticated
		}
		if q.Seal == "" {
			return ErrUnauthenticated
		}
		return nil
	})
	return q, err
}
func queuedCredentialSeal(verifier []byte) string {
	if len(verifier) == 0 {
		return ""
	}
	sum := sha256.Sum256(verifier)
	return hex.EncodeToString(sum[:])
}
