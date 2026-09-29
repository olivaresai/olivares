// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func freshnessSession(t *testing.T, f *principalEvidenceFixture, edit func(*model.AuthSession)) {
	t.Helper()
	if err := f.raw.AuthMutate(f.ctx, func(as store.AuthScope) error {
		s, err := as.Sessions().Get(f.ctx, f.session.ID)
		if err != nil {
			return err
		}
		edit(&s)
		f.session, err = as.Sessions().Update(f.ctx, s)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func freshnessResolved(t *testing.T, f *principalEvidenceFixture) (Principal, AuthenticationEvidence) {
	t.Helper()
	p, err := f.a.ResolvePrincipalScope(f.deadline(30*time.Minute), f.sessionRef(), f.tenant)
	if err != nil {
		t.Fatal(err)
	}
	e, ok := p.AuthenticationEvidence()
	if !ok {
		t.Fatal("resolved human session has no authentication evidence")
	}
	return p, e
}

func TestAuthenticationEvidenceBindsExactPrincipalAndDefensiveBundle(t *testing.T) {
	f := newPrincipalEvidenceFixture(t)
	p, e := freshnessResolved(t, f)
	ref, _ := p.Ref()
	if e.Credential != ref || e.AAL != AAL1 || !e.AuthenticatedAt.Equal(f.session.CreatedAt.Time()) ||
		!e.ObservedAt.Equal(f.now) || !e.FreshUntil.Equal(f.now.Add(30*time.Minute)) {
		t.Fatal("evidence substituted observation time, identity, or expiry")
	}
	b, err := e.AuthorityFor(f.now, p, f.tenant)
	if err != nil || len(b.Facts) != 1 || len(b.UserAuthorities) != 1 ||
		b.Facts[0].Kind != model.DirectoryEpochKind || b.Facts[0].ID != model.ID(f.tenant) ||
		b.UserAuthorities[0].UserID != p.UserID || b.UserAuthorities[0].Version < 1 {
		t.Fatalf("complete native authority bundle: %+v, %v", b, err)
	}
	b.Facts[0].Version++
	b.UserAuthorities[0].Version++
	again, err := e.AuthorityFor(f.now, p, f.tenant)
	if err != nil || again.Facts[0] == b.Facts[0] || again.UserAuthorities[0] == b.UserAuthorities[0] {
		t.Fatal("returned bundle aliases retained evidence")
	}
	for _, edit := range []func(*AuthenticationEvidence){
		func(v *AuthenticationEvidence) { v.Credential = PrincipalRef{} },
		func(v *AuthenticationEvidence) { v.AAL = AAL3 },
		func(v *AuthenticationEvidence) { v.AuthenticatedAt = v.AuthenticatedAt.Add(time.Nanosecond) },
		func(v *AuthenticationEvidence) { v.ObservedAt = v.ObservedAt.Add(time.Nanosecond) },
		func(v *AuthenticationEvidence) { v.FreshUntil = v.FreshUntil.Add(time.Nanosecond) },
		func(v *AuthenticationEvidence) {
			*v = AuthenticationEvidence{Credential: e.Credential, AAL: e.AAL, AuthenticatedAt: e.AuthenticatedAt, ObservedAt: e.ObservedAt, FreshUntil: e.FreshUntil}
		},
	} {
		bad := e
		edit(&bad)
		bundle, err := bad.AuthorityFor(f.now, p, f.tenant)
		if !errors.Is(err, ErrPrincipalEvidenceUnavailable) || len(bundle.Facts) != 0 || len(bundle.UserAuthorities) != 0 {
			t.Fatal("edited or caller-constructed evidence acquired authority")
		}
	}
	for _, edit := range []func(*Principal){
		func(v *Principal) { v.UserID = model.NewID() },
		func(v *Principal) { v.CredID = model.NewID() },
		func(v *Principal) { v.AAL = AAL3 },
		func(v *Principal) { v.AMR = []string{"piv"} },
		func(v *Principal) { v.evidence.authenticatedAt = v.evidence.authenticatedAt.Add(time.Nanosecond) },
	} {
		bad := cloneEvidencePrincipal(p)
		edit(&bad)
		if _, ok := bad.AuthenticationEvidence(); ok {
			t.Fatal("edited principal minted authentication evidence")
		}
		if _, err := e.AuthorityFor(f.now, bad, f.tenant); err == nil {
			t.Fatal("copied evidence matched another principal")
		}
	}
	if _, err := e.AuthorityFor(f.now, p, model.NewTenantID()); err == nil {
		t.Fatal("cross-tenant evidence accepted")
	}
	for _, now := range []time.Time{e.ObservedAt.Add(-time.Nanosecond), e.FreshUntil, e.FreshUntil.Add(time.Nanosecond)} {
		if _, err := e.AuthorityFor(now, p, f.tenant); err == nil {
			t.Fatal("evidence accepted outside its observed window")
		}
	}
	if _, err := e.AuthorityFor(e.FreshUntil.Add(-time.Nanosecond), p, f.tenant); err != nil {
		t.Fatal(err)
	}

	// A genuine new reconstruction of the same credential is still different
	// evidence. Reusing scalar identity cannot transplant the old sealed value.
	f.hooks.now = model.NewTimestamp(f.now.Add(time.Second))
	reconstructed, _ := freshnessResolved(t, f)
	if _, err := e.AuthorityFor(f.now.Add(time.Second), reconstructed, f.tenant); err == nil {
		t.Fatal("evidence transplanted into a newer reconstruction")
	}
	f.hooks.now = model.NewTimestamp(f.now)

	authenticated, err := f.a.Authenticate(f.ctx, f.sessRaw)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := authenticated.AuthenticationEvidence(); ok {
		t.Fatal("Authenticate-only principal minted evidence")
	}
	token, err := f.a.ResolvePrincipalScope(f.deadline(time.Minute), f.tokenRef(), f.tenant)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := token.AuthenticationEvidence(); ok {
		t.Fatal("API token acquired human evidence")
	}
	if _, ok := (Principal{Kind: KindUser, UserID: p.UserID, CredID: p.CredID, AAL: AAL3}).AuthenticationEvidence(); ok {
		t.Fatal("synthetic human acquired evidence")
	}
}

func TestAuthenticationEvidenceLegacyFutureAndExpiry(t *testing.T) {
	for _, tc := range []struct {
		name        string
		witness     string
		expiry      time.Duration
		wantAAL     int
		unavailable bool
	}{
		{"legacy", "nil", time.Minute, AAL1, false},
		{"zero", "zero", time.Minute, 0, true},
		{"future", "future", time.Minute, 0, true},
		{"negative", "negative", time.Minute, 0, true},
		{"exact expiry", "valid", 0, AAL1, false},
		{"one tick before expiry", "valid", time.Nanosecond, AAL3, false},
		{"six minute ceremony is evidence, age policy is separate", "valid", 9 * time.Minute, AAL3, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newPrincipalEvidenceFixture(t)
			f.now = f.session.CreatedAt.Time().Add(10 * time.Minute)
			f.hooks.now = model.NewTimestamp(f.now)
			stamp := model.NewTimestamp(f.now.Add(-6 * time.Minute))
			until := model.NewTimestamp(f.now.Add(tc.expiry))
			freshnessSession(t, f, func(s *model.AuthSession) {
				s.AAL, s.AMR, s.AALExpiresAt = AAL3, []string{"pwd", "piv"}, &until
				s.AALAuthenticatedAt = &stamp
				switch tc.witness {
				case "nil":
					s.AALAuthenticatedAt = nil
				case "zero":
					s.AALAuthenticatedAt = &model.Timestamp{}
				case "future":
					v := model.NewTimestamp(f.now.Add(time.Nanosecond))
					s.AALAuthenticatedAt = &v
				case "negative":
					v := model.NewTimestamp(time.Unix(-1, 0))
					s.AALAuthenticatedAt = &v
				}
			})
			wantHot := tc.wantAAL
			if tc.unavailable {
				wantHot = AAL1
			}
			if got := effectiveAAL(f.session, model.NewTimestamp(f.now)); got != wantHot {
				t.Fatalf("hot authentication assurance = %d, want %d", got, wantHot)
			}
			p, err := f.a.ResolvePrincipalScope(f.deadline(30*time.Minute), f.sessionRef(), f.tenant)
			if tc.unavailable {
				if !errors.Is(err, ErrPrincipalEvidenceUnavailable) {
					t.Fatalf("malformed witness: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			e, ok := p.AuthenticationEvidence()
			if !ok || e.AAL != tc.wantAAL {
				t.Fatalf("assurance %d, evidence %t", p.AAL, ok)
			}
			want := stamp.Time()
			if tc.wantAAL == AAL1 {
				want = f.session.CreatedAt.Time()
			}
			if !e.AuthenticatedAt.Equal(want) {
				t.Fatal("authentication time was inferred or refreshed")
			}
			if tc.wantAAL == AAL3 && !e.FreshUntil.Equal(until.Time()) {
				t.Fatal("assurance deadline was replaced with a five-minute age window")
			}
		})
	}
}

func TestAuthenticationEvidenceRefreshAndDegradePreserveEventMeaning(t *testing.T) {
	f := newPrincipalEvidenceFixture(t)
	f.now = f.session.CreatedAt.Time().Add(time.Minute)
	f.hooks.now = model.NewTimestamp(f.now)
	a := NewAuthenticator(f.st, principalEvidenceAppClock{now: model.NewTimestamp(f.now)})
	p, err := a.Authenticate(f.ctx, f.sessRaw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.ElevateSession(f.ctx, p, "piv", AAL3); err != nil {
		t.Fatal(err)
	}
	f.a = a
	resolved, e := freshnessResolved(t, f)
	if !e.AuthenticatedAt.Equal(f.now.UTC().Truncate(time.Microsecond)) {
		t.Fatal("trusted ceremony entry did not preserve canonical capture time")
	}
	old := e.Credential
	next, row, err := a.RefreshSession(f.ctx, resolved)
	if err != nil {
		t.Fatal(err)
	}
	if row.AALAuthenticatedAt == nil || !row.AALAuthenticatedAt.Time().Equal(e.AuthenticatedAt) {
		t.Fatal("refresh changed ceremony time")
	}
	f.sessRaw = next
	if _, err := a.ResolvePrincipalScope(f.deadline(time.Minute), old, f.tenant); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal("rotated credential reference remained valid")
	}
	p, after := freshnessResolved(t, f)
	if after.Credential == old || !after.AuthenticatedAt.Equal(e.AuthenticatedAt) || !after.FreshUntil.Equal(e.FreshUntil) {
		t.Fatal("refresh renewed assurance or reused old reference")
	}
	if err := a.DegradeSessionAssurance(f.ctx, p, p.UserID); err != nil {
		t.Fatal(err)
	}
	if err := f.raw.AuthView(context.Background(), func(as store.AuthScope) error {
		s, err := as.Sessions().Get(f.ctx, p.CredID)
		if err == nil && (s.AALAuthenticatedAt != nil || s.AALExpiresAt != nil || s.AAL != AAL1) {
			t.Fatal("CAEP retained assurance witness")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
