// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"crypto/sha256"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// AuthenticationEvidence is a snapshot of one resolved human credential. Its
// visible fields alone establish nothing: only AuthorityFor validates its
// private provenance against the exact principal, tenant and current DB time.
// It grants no permission and is not a serialized or caller-mintable token.
type AuthenticationEvidence struct {
	Credential      PrincipalRef
	AAL             int
	AuthenticatedAt time.Time
	ObservedAt      time.Time
	FreshUntil      time.Time

	tenant        model.TenantID
	principalSeal [sha256.Size]byte
}

// AuthenticationEvidence returns the stored ceremony instant (AAL3), or the
// original session creation instant (AAL1), from sealed resolver provenance.
// New AAL3 events are canonical UTC microseconds at capture; reads never round.
// Authenticate-only, synthetic and token principals cannot supply it. This does
// not read a clock, refresh evidence or apply a consumer's opening-age policy.
func (p Principal) AuthenticationEvidence() (AuthenticationEvidence, bool) {
	if p.Kind != KindUser || !validPrincipalAuthoritySeal(p) ||
		!validAuthenticationInstant(p.evidence.authenticatedAt) {
		return AuthenticationEvidence{}, false
	}
	return AuthenticationEvidence{
		Credential: p.evidence.ref, AAL: p.AAL,
		AuthenticatedAt: p.evidence.authenticatedAt,
		ObservedAt:      p.evidence.observedAt, FreshUntil: p.evidence.freshUntil,
		tenant: p.evidence.tenant, principalSeal: p.evidence.seal,
	}, true
}

// AuthorityFor validates the entire snapshot and returns defensive copies of
// both native authority kinds: DirectoryEpoch and UserAuthority. The caller
// supplies authoritative transaction time and must still acquire the complete
// bundle on that transaction and obtain the separate permission to act.
func (e AuthenticationEvidence) AuthorityFor(now time.Time, principal Principal, tenant model.TenantID) (store.AuthoritySnapshotBundle, error) {
	current, ok := principal.AuthenticationEvidence()
	if !ok || e.tenant != tenant || e.tenant != current.tenant ||
		e.principalSeal != current.principalSeal || e.Credential != current.Credential || e.AAL != current.AAL ||
		!e.AuthenticatedAt.Equal(current.AuthenticatedAt) || !e.ObservedAt.Equal(current.ObservedAt) ||
		!e.FreshUntil.Equal(current.FreshUntil) || now.Before(e.ObservedAt) || !now.Before(e.FreshUntil) {
		return store.AuthoritySnapshotBundle{}, ErrPrincipalEvidenceUnavailable
	}
	bundle, _, ok := principalCompleteAuthorizationEvidence(principal, tenant)
	if !ok {
		return store.AuthoritySnapshotBundle{}, ErrPrincipalEvidenceUnavailable
	}
	return bundle, nil
}

func validAuthenticationInstant(t time.Time) bool {
	return !t.IsZero() && t.Unix() >= 0 && t.Year() <= 9999
}
