// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

// CommunicationCursorTokenKey is one configured cursor signing/verification
// key. The constructor copies Material; callers may erase their buffer after
// construction without changing tokens minted by the snapshot.
type CommunicationCursorTokenKey struct {
	KID      string
	Material []byte
}

// CommunicationCursorTokenKeyring is the immutable verification snapshot the
// composition root binds after loading operator custody. It is the ONLY public
// handle to the private c2v1 keyring: it exposes key identifiers and a
// self-test, never key bytes. The C2 cursor service consumes it through
// Module.communicationCursorTokenKeyring once the route owner mounts it.
type CommunicationCursorTokenKeyring struct {
	inner *communicationCursorTokenKeyring
}

// CommunicationCursorTokenRetentionWindow is how long a rotated-out key must
// remain in the verification set: every token it signed is dead after the
// token TTL plus the accepted clock skew, so a key retired longer ago verifies
// nothing that is still live.
const CommunicationCursorTokenRetentionWindow = communicationCursorTokenReserve

// NewCommunicationCursorTokenKeyring validates the complete rotation snapshot
// and copies every key. A missing current KID, a duplicate KID, an empty ring
// or a key outside the accepted size keeps the feature unavailable rather than
// selecting an arbitrary verifier as the signer.
func NewCommunicationCursorTokenKeyring(
	signingKID string,
	keys []CommunicationCursorTokenKey,
) (*CommunicationCursorTokenKeyring, error) {
	configured := make([]communicationCursorTokenKey, 0, len(keys))
	for _, key := range keys {
		configured = append(configured, communicationCursorTokenKey{kid: key.KID, material: key.Material})
	}
	inner, err := newCommunicationCursorTokenKeyring(signingKID, configured)
	if err != nil {
		return nil, err
	}
	return &CommunicationCursorTokenKeyring{inner: inner}, nil
}

// SigningKID names the current signing key. It is a reference, not material.
func (k *CommunicationCursorTokenKeyring) SigningKID() string {
	if k == nil || k.inner == nil {
		return ""
	}
	return k.inner.signingKID
}

// VerificationKIDs lists every key identifier the snapshot can verify, sorted.
func (k *CommunicationCursorTokenKeyring) VerificationKIDs() []string {
	if k == nil || k.inner == nil {
		return nil
	}
	kids := make([]string, 0, len(k.inner.keys))
	for kid := range k.inner.keys {
		kids = append(kids, kid)
	}
	sort.Strings(kids)
	return kids
}

// SelfTest mints and verifies one synthetic navigation token under the current
// signing key and checks that a token signed by a KID outside the ring is
// refused. A snapshot that fails this is never bound: readiness reads the
// binding, and an unbound ring keeps the C2 surface unavailable.
func (k *CommunicationCursorTokenKeyring) SelfTest(observedAt time.Time) error {
	if k == nil || k.inner == nil {
		return communicationCursorTokenUnavailable("keyring is not configured")
	}
	if observedAt.IsZero() {
		observedAt = time.Now().UTC()
	}
	filterHash, err := directNoticeCursorFilterHash()
	if err != nil {
		return communicationCursorTokenUnavailable("canonical filter hash is unavailable")
	}
	reader := model.NewID()
	claims := communicationCursorTokenClaims{
		tenantID: model.NewTenantID(), workspaceID: model.NewID(),
		readerKind: communicationCursorTokenReaderKind, readerRef: reader,
		mailboxKind: communicationCursorTokenMailboxKind, mailboxRef: reader,
		carrierClass: string(CursorCarrierDirectNoticeV1), filterHash: filterHash[:],
	}
	token, err := k.inner.mint(claims, observedAt)
	if err != nil {
		return err
	}
	verified, err := k.inner.verify(token, observedAt)
	if err != nil {
		return err
	}
	if verified.tenantID != claims.tenantID || verified.workspaceID != claims.workspaceID ||
		verified.readerRef != claims.readerRef {
		return communicationCursorTokenUnavailable("self-test round trip changed the claims")
	}
	foreign, err := newCommunicationCursorTokenKeyring("self-test-foreign", []communicationCursorTokenKey{{
		kid: "self-test-foreign", material: append([]byte("olivares.cursor.self-test.foreign."), filterHash[:]...),
	}})
	if err != nil {
		return err
	}
	foreignToken, err := foreign.mint(claims, observedAt)
	if err != nil {
		return err
	}
	if _, err := k.inner.verify(foreignToken, observedAt); !errors.Is(err, errCommunicationCursorTokenInvalid) {
		return communicationCursorTokenUnavailable("self-test accepted a token signed outside the ring")
	}
	return nil
}

// UseCommunicationCursorTokenKeyring late-binds the durable cursor key source.
// Nil or a ring that fails its self-test removes the binding and leaves the C2
// surface unavailable; binding never activates K3 by itself.
func (m *Module) UseCommunicationCursorTokenKeyring(keyring *CommunicationCursorTokenKeyring) {
	if keyring == nil || keyring.inner == nil || keyring.inner.validate() != nil {
		m.communicationCursorKeyring = nil
		return
	}
	m.communicationCursorKeyring = keyring.inner
}

// CommunicationCursorTokenKeyringBound reports whether a valid cursor key
// snapshot is bound. It is the readiness fact the composition root logs; it
// exposes no identifier and no material.
func (m *Module) CommunicationCursorTokenKeyringBound() bool {
	return m != nil && m.communicationCursorKeyring != nil && m.communicationCursorKeyring.validate() == nil
}

// communicationCursorTokenKeyring is the private accessor the C2 service uses
// to construct newDirectNoticeCursorService with the bound snapshot.
func (m *Module) communicationCursorTokenKeyring() *communicationCursorTokenKeyring {
	if m == nil || m.communicationCursorKeyring == nil {
		return nil
	}
	return m.communicationCursorKeyring
}

// CommunicationCursorTokenKeyringReady is the narrow readiness probe for the
// bound cursor key snapshot; it re-validates the snapshot shape and nothing
// else, so it cannot be mistaken for the full K3 readiness conjunction.
func (m *Module) CommunicationCursorTokenKeyringReady(ctx context.Context) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return m.CommunicationCursorTokenKeyringBound(), nil
}
