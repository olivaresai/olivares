// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

type userAuthorityReadFaultScope struct {
	*principalEvidenceScope
	read func(context.Context, model.ID) (store.UserAuthorityFactRef, error)
}

func (s userAuthorityReadFaultScope) ReadUserAuthorityFact(ctx context.Context, id model.ID) (store.UserAuthorityFactRef, error) {
	return s.read(ctx, id)
}

func TestUserAuthorityReadProducerRequiresHumanEvidence(t *testing.T) {
	f := newPrincipalEvidenceFixture(t)
	ref := f.sessionRef()
	f.resetTrace()
	p, err := f.a.ResolvePrincipalScope(f.deadline(30*time.Minute), ref, f.tenant)
	if err != nil {
		t.Fatal(err)
	}
	if f.st.views != 1 || p.evidence.authorityMode != principalHumanAuthority || p.evidence.userAuthority.UserID != f.user.ID || p.evidence.userAuthority.Version < 1 {
		t.Fatal("human authority was not observed in one reconstruction")
	}
	before, h, after := slices.Index(f.hooks.trace, "directory-1"), slices.Index(f.hooks.trace, "user-authority"), slices.Index(f.hooks.trace, "directory-2")
	if before < 0 || h <= before || after <= h {
		t.Fatal("H was not inside the reconstruction snapshot")
	}
	for _, name := range []string{"missing-capability", "missing", "wrong-user", "zero-version", "negative-version", "cancelled", "poison"} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			cause := errors.New("binding poison")
			f.st.wrap = func(as store.AuthScope) store.AuthScope {
				base := &principalEvidenceScope{AuthScope: as, hooks: f.hooks}
				if name == "missing-capability" {
					return struct {
						store.AuthScope
						store.AuthPrincipalEvidenceScope
					}{as, base}
				}
				return userAuthorityReadFaultScope{base, func(ctx context.Context, id model.ID) (store.UserAuthorityFactRef, error) {
					calls++
					if id != f.user.ID {
						t.Fatal("observer received a caller-selected User")
					}
					h := p.evidence.userAuthority
					switch name {
					case "missing":
						return store.UserAuthorityFactRef{}, store.ErrDirectoryUnavailable
					case "wrong-user":
						h.UserID = model.NewID()
					case "zero-version":
						h.Version = 0
					case "negative-version":
						h.Version = -1
					case "cancelled":
						return store.UserAuthorityFactRef{}, context.Canceled
					case "poison":
						return store.UserAuthorityFactRef{}, cause
					}
					return h, nil
				}}
			}
			got, err := f.a.ResolvePrincipalScope(f.deadline(30*time.Minute), ref, f.tenant)
			if !errors.Is(err, ErrPrincipalEvidenceUnavailable) || got.evidence.seal != ([32]byte{}) || calls > 1 {
				t.Fatalf("invalid H escaped or retried: %v", err)
			}
			if name == "cancelled" && !errors.Is(err, context.Canceled) {
				t.Fatal("cancellation cause lost")
			}
			if name == "poison" && !errors.Is(err, cause) {
				t.Fatal("scope cause lost")
			}
		})
	}
	// Token owners are not implicitly promoted to the human User contract.
	f.st.wrap = func(as store.AuthScope) store.AuthScope {
		return struct {
			store.AuthScope
			store.AuthPrincipalEvidenceScope
		}{as, &principalEvidenceScope{AuthScope: as, hooks: f.hooks}}
	}
	token, err := f.a.ResolvePrincipalScope(f.deadline(30*time.Minute), f.tokenRef(), f.tenant)
	if err != nil || token.evidence.authorityMode != principalTokenDirectoryOnly || token.evidence.userAuthority != (store.UserAuthorityFactRef{}) {
		t.Fatalf("directory-only token changed: %v", err)
	}
}

func TestUserAuthorityReadSealAndCompleteDigest(t *testing.T) {
	f, p := resolvedPrincipalAuthorityEvidence(t)
	now := p.evidence.observedAt.Add(time.Second)
	deadline := model.NewTimestamp(now.Add(time.Hour))
	lease, err := store.NewLeaseFenceAuthorizationFactRef("test.lease", model.NewID(), 7, "subject", 9, deadline)
	if err != nil {
		t.Fatal(err)
	}
	scoped := &principalAuthorityScopedProducer{decision: principalAuthorityCleanScoped(p.evidence.observedAt, p.evidence.freshUntil, lease)}
	az := NewAuthorizer(nil, WithScopedGrants(scoped), WithClock(func() time.Time { return now }))
	req := principalAuthorityEvidenceRequest(p, f.tenant)
	d, err := az.DecideRouteRead(f.ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := d.AuthorityFor(now, req)
	if err != nil || len(bundle.UserAuthorities) != 1 {
		t.Fatalf("complete authority: %v", err)
	}
	if _, err := d.FactsFor(now, req); !errors.Is(err, ErrRouteUndecided) || d.AllowWitness().minted {
		t.Fatal("human receipt became a generic proof")
	}
	operation := req
	operation.Route.CedarAction = "agent:update"
	operationDecision, err := az.DecideRouteRead(f.ctx, operation)
	if err != nil || !operationDecision.Allowed() {
		t.Fatalf("operation question did not produce a read answer: %v", err)
	}
	if operationDecision.AllowWitness().VerifyFor(now, operation) {
		t.Fatal("human read answer became an operation witness")
	}
	ordinary, err := az.AuthorizeRoute(f.ctx, operation)
	if err != nil || !ordinary.VerifyFor(now, operation) {
		t.Fatalf("ordinary operation admission changed: %v", err)
	}
	for name, change := range map[string]func(*RouteReadDecision){
		"H-version":      func(v *RouteReadDecision) { v.userAuthority.Version++ },
		"H-user":         func(v *RouteReadDecision) { v.userAuthority.UserID = model.NewID() },
		"mode":           func(v *RouteReadDecision) { v.authorityMode = principalTokenDirectoryOnly },
		"principal-seal": func(v *RouteReadDecision) { v.principalSeal[0] ^= 1 },
		"outcome":        func(v *RouteReadDecision) { v.witness.Decision.Outcome = EvidenceDeny },
		"issuance":       func(v *RouteReadDecision) { v.issued = false },
	} {
		t.Run(name, func(t *testing.T) {
			v := d
			change(&v)
			if _, err := v.AuthorityFor(now, req); err == nil {
				t.Fatal("changed receipt verified")
			}
		})
	}
	for _, name := range []string{"presence", "subject", "fence", "deadline"} {
		t.Run("lease-"+name, func(t *testing.T) {
			v := d
			v.witness.Decision.Facts = append([]store.AuthorizationFactRef(nil), d.witness.Decision.Facts...)
			subject, fence, until := "subject", int64(9), deadline
			switch name {
			case "subject":
				subject = "changed"
			case "fence":
				fence++
			case "deadline":
				until = model.NewTimestamp(deadline.Time().Add(time.Second))
			}
			changed, err := store.NewLeaseFenceAuthorizationFactRef(lease.Kind, lease.ID, lease.Version, subject, fence, until)
			if err != nil {
				t.Fatal(err)
			}
			if name == "presence" {
				changed = store.AuthorizationFactRef{Kind: lease.Kind, ID: lease.ID, Version: lease.Version}
			}
			for i, f := range v.witness.Decision.Facts {
				if f.ID == lease.ID {
					v.witness.Decision.Facts[i] = changed
				}
			}
			if evidenceDigest(v.witness) == d.witness.EvidenceDigest {
				t.Fatal("generic evidence digest omitted lease coordinate")
			}
			if v.completeDigest() == d.authorityDigest {
				t.Fatal("complete digest omitted lease coordinate")
			}
			if _, err := v.AuthorityFor(now, req); err == nil {
				t.Fatal("changed lease verified")
			}
		})
	}
	bundle.Facts[0].Version++
	bundle.UserAuthorities[0].Version++
	if _, err := d.AuthorityFor(now, req); err != nil {
		t.Fatal("returned slices alias decision")
	}
	other := req
	other.Principal = cloneEvidencePrincipal(p)
	other.Principal.evidence.userAuthority.Version++
	other.Principal.evidence.seal, err = computePrincipalAuthoritySeal(other.Principal)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.AuthorityFor(now, other); err == nil {
		t.Fatal("different sealed reconstruction reused old receipt")
	}
	for _, mode := range []principalReadAuthorityMode{0, principalTokenDirectoryOnly, 255} {
		bad := cloneEvidencePrincipal(p)
		bad.evidence.authorityMode = mode
		if validPrincipalAuthoritySeal(bad) {
			t.Fatal("malformed applicability retained seal")
		}
	}
	old := cloneEvidencePrincipal(p)
	old.evidence.authorityMode = 0
	old.evidence.userAuthority = store.UserAuthorityFactRef{}
	if validPrincipalAuthoritySeal(old) {
		t.Fatal("v1-shaped provenance retained authority")
	}
	// Equal source sets are encoded once in canonical order.
	scoped.decision.Facts = []store.AuthorizationFactRef{lease, p.evidence.directoryEpoch}
	left, err := az.DecideRouteRead(f.ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	slices.Reverse(scoped.decision.Facts)
	right, err := az.DecideRouteRead(f.ctx, req)
	if err != nil || left.authorityDigest != right.authorityDigest {
		t.Fatal("input order changed complete digest")
	}
}

func TestUserAuthorityReadTokenLegacyCopies(t *testing.T) {
	f := newPrincipalEvidenceFixture(t)
	p, err := f.a.ResolvePrincipalScope(f.deadline(30*time.Minute), f.tokenRef(), f.tenant)
	if err != nil {
		t.Fatal(err)
	}
	now := p.evidence.observedAt.Add(time.Second)
	req := principalAuthorityEvidenceRequest(p, f.tenant)
	d, err := NewAuthorizer(nil, WithClock(func() time.Time { return now })).DecideRouteRead(f.ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	w := d.AllowWitness()
	if !w.VerifyFor(now, req) {
		t.Fatal("legacy token witness refused")
	}
	w.Decision.Facts[0].Version++
	facts, err := d.FactsFor(now, req)
	if err != nil {
		t.Fatal(err)
	}
	facts[0].Version++
	if _, err := d.AuthorityFor(now, req); err != nil {
		t.Fatal("legacy extraction aliased complete receipt")
	}
}
