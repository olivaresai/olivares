// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"slices"
	"time"

	"github.com/olivaresai/olivares/core/store"
)

// RouteReadDecision records a definite answer, including an omitted row. Its
// private value cannot be promoted from a denial into an authorization witness.
// Verification binds the full question and finite window; callers must also
// revalidate every returned fact in the page's final View.
type RouteReadDecision struct {
	witness         RouteAuthorizationWitness
	issued          bool
	authorityMode   principalReadAuthorityMode
	userAuthority   store.UserAuthorityFactRef
	principalSeal   [sha256.Size]byte
	authorityDigest [sha256.Size]byte
}

func (d RouteReadDecision) Allowed() bool {
	return d.issued && d.witness.Decision.Outcome == EvidenceAllow
}

func (d RouteReadDecision) AllowWitness() RouteAuthorizationWitness {
	if !d.Allowed() || d.authorityMode != principalTokenDirectoryOnly {
		return RouteAuthorizationWitness{}
	}
	w := d.witness
	w.Decision.Facts = append([]store.AuthorizationFactRef(nil), w.Decision.Facts...)
	return w
}

func (d RouteReadDecision) FactsFor(now time.Time, req Request) ([]store.AuthorizationFactRef, error) {
	if d.authorityMode != principalTokenDirectoryOnly || !d.intact(now) || !d.witness.AnswersQuestion(req) {
		return nil, ErrRouteUndecided
	}
	return append([]store.AuthorizationFactRef(nil), d.witness.Decision.Facts...), nil
}

// AuthorityFor binds the complete read receipt to the exact reconstructed
// principal and question. Current authority still requires a final View check.
func (d RouteReadDecision) AuthorityFor(now time.Time, req Request) (store.AuthoritySnapshotBundle, error) {
	if !d.intact(now) || !d.witness.AnswersQuestion(req) {
		return store.AuthoritySnapshotBundle{}, ErrRouteUndecided
	}
	bundle, _, ok := principalCompleteAuthorizationEvidence(req.Principal, req.Tenant)
	if !ok || d.principalSeal != req.Principal.evidence.seal ||
		d.authorityMode != req.Principal.evidence.authorityMode || d.userAuthority != req.Principal.evidence.userAuthority {
		return store.AuthoritySnapshotBundle{}, ErrRouteUndecided
	}
	bundle.Facts = append([]store.AuthorizationFactRef(nil), d.witness.Decision.Facts...)
	return bundle, nil
}

func (d RouteReadDecision) intact(now time.Time) bool {
	w := d.witness
	if !d.issued || (w.Decision.Outcome != EvidenceAllow && w.Decision.Outcome != EvidenceDeny) ||
		!w.IsFresh(now) || evidenceDigest(w) != w.EvidenceDigest {
		return false
	}
	facts, ok := canonicalEvidenceFacts(w.Decision.Facts)
	if !ok || !slices.Equal(facts, w.Decision.Facts) {
		return false
	}
	switch d.authorityMode {
	case principalHumanAuthority:
		if !validPrincipalEvidenceID(d.userAuthority.UserID) || d.userAuthority.Version < 1 {
			return false
		}
	case principalTokenDirectoryOnly:
		if d.userAuthority != (store.UserAuthorityFactRef{}) {
			return false
		}
	default:
		return false
	}
	return d.completeDigest() == d.authorityDigest
}

func (d RouteReadDecision) completeDigest() [sha256.Size]byte {
	h := sha256.New()
	h.Write([]byte("olivares.auth.route-read-authority.v1\x00"))
	h.Write(d.witness.EvidenceDigest[:])
	h.Write(d.principalSeal[:])
	h.Write([]byte{byte(d.authorityMode)})
	num := func(v int64) {
		var b [8]byte
		binary.BigEndian.PutUint64(b[:], uint64(v))
		h.Write(b[:])
	}
	text := func(v string) { num(int64(len(v))); h.Write([]byte(v)) }
	if d.authorityMode == principalHumanAuthority {
		num(1)
		text(d.userAuthority.UserID.String())
		num(d.userAuthority.Version)
	} else {
		num(0)
	}
	num(int64(len(d.witness.Decision.Facts)))
	for _, f := range d.witness.Decision.Facts {
		text(string(f.Kind))
		text(f.ID.String())
		num(f.Version)
		if subject, fence, deadline, ok := f.LeaseFenceWitness(); ok {
			h.Write([]byte{1})
			text(subject)
			num(fence)
			text(deadline.String())
		} else {
			h.Write([]byte{0})
		}
	}
	var digest [sha256.Size]byte
	copy(digest[:], h.Sum(nil))
	return digest
}

// DecideRouteRead evaluates once and retains all consulted contributions for a
// later page barrier, while preserving the authorization algebra's outcome.
// Contradictory observations cannot justify a coherent page, even if one was
// independently sufficient to deny. AuthorizeRoute's denial API is unchanged.
func (az *Authorizer) DecideRouteRead(ctx context.Context, req Request) (RouteReadDecision, error) {
	if az == nil {
		return RouteReadDecision{}, ErrAuthorizerUnavailable
	}
	principalBundle, principalWindow, ok := principalCompleteAuthorizationEvidence(req.Principal, req.Tenant)
	if !ok {
		return RouteReadDecision{}, ErrRouteUndecided
	}
	contributions := readEvidenceContributions{}
	var ev AuthorizationEvidence
	if req.Route.RequiresStepUp(req.Principal.AAL) {
		ev = AuthorizationEvidence{Outcome: EvidenceDeny, ScopedEffect: EffectAbstain}
	} else {
		ev = az.authorizeEvidence(ctx, req, &contributions)
	}
	if ev.Outcome == EvidenceUnknown {
		return RouteReadDecision{}, DenialFor(ev)
	}
	facts, ok := canonicalEvidenceFacts(principalBundle.Facts, ev.Facts,
		contributions.scoped.decision.Facts, contributions.policy.decision.Facts)
	if !ok {
		return RouteReadDecision{}, ErrRouteUndecided
	}
	window := evidenceWindow{}
	if !window.addWindow(principalWindow) || !window.addWindow(contributions.scoped.window) ||
		!window.addWindow(contributions.policy.window) || !window.validIntersection() {
		return RouteReadDecision{}, ErrRouteUndecided
	}
	ev.Facts = facts
	ev.ObservedAt, ev.FreshUntil = window.observedAt, window.freshUntil
	w := RouteAuthorizationWitness{Decision: ev, CedarAction: resolveCedarAction(req), ScopedEffect: ev.ScopedEffect,
		ResourceDigest: ResourceDigest(req.Tenant, req.Resource), PolicyVersion: policyVersionOf(facts), QuestionDigest: questionDigest(req), minted: ev.Outcome == EvidenceAllow}
	w.EvidenceDigest = evidenceDigest(w)
	if !w.IsFresh(az.clock()) {
		return RouteReadDecision{}, fmt.Errorf("%w: read decision window expired", ErrRouteUndecided)
	}
	d := RouteReadDecision{witness: w, issued: true, authorityMode: req.Principal.evidence.authorityMode,
		userAuthority: req.Principal.evidence.userAuthority, principalSeal: req.Principal.evidence.seal}
	d.authorityDigest = d.completeDigest()
	return d, nil
}
