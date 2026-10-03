// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"errors"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestFederatedMFAPrincipalRetainsCredentialAssurance(t *testing.T) {
	for _, tc := range []struct {
		name        string
		aal         int
		methods     []string
		expiry      time.Duration
		instant     time.Duration
		missingAt   bool
		want        int
		unavailable bool
	}{
		{"fresh federated MFA", 2, []string{"sso", "mfa"}, 5 * time.Minute, -time.Minute, false, 2, false},
		{"expired federated MFA", 2, []string{"sso", "mfa"}, 0, -time.Minute, false, AAL1, false},
		{"missing MFA instant", 2, []string{"sso", "mfa"}, 5 * time.Minute, -time.Minute, true, AAL1, false},
		{"future MFA instant", 2, []string{"sso", "mfa"}, 5 * time.Minute, time.Hour, false, 0, true},
		{"SSO alone", 2, []string{"sso"}, 5 * time.Minute, -time.Minute, false, 0, true},
		{"MFA alone", 2, []string{"mfa"}, 5 * time.Minute, -time.Minute, false, 0, true},
		{"fresh local hardware", AAL3, []string{"pwd", "webauthn"}, 5 * time.Minute, -time.Minute, false, AAL3, false},
		{"expired local hardware", AAL3, []string{"pwd", "webauthn"}, 0, -time.Minute, false, AAL1, false},
		{"federated MFA cannot claim AAL3", AAL3, []string{"sso", "mfa"}, 5 * time.Minute, -time.Minute, false, 0, true},
		{"unknown assurance level", 4, []string{"sso", "mfa"}, 5 * time.Minute, -time.Minute, false, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newPrincipalEvidenceFixture(t)
			// The verified IdP authentication happened before this session opened.
			stamp := model.NewTimestamp(f.now.Add(tc.instant))
			until := model.NewTimestamp(f.now.Add(tc.expiry))
			if err := f.raw.AuthMutate(f.ctx, func(as store.AuthScope) error {
				row, err := as.Sessions().Get(f.ctx, f.session.ID)
				if err != nil {
					return err
				}
				row.AAL, row.AMR = tc.aal, tc.methods
				row.AALExpiresAt, row.AALAuthenticatedAt = &until, &stamp
				if tc.missingAt {
					row.AALAuthenticatedAt = nil
				}
				f.session, err = as.Sessions().Update(f.ctx, row)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			principal, err := f.a.ResolvePrincipalScope(f.deadline(10*time.Minute), f.sessionRef(), f.tenant)
			if tc.unavailable {
				if !errors.Is(err, ErrPrincipalEvidenceUnavailable) {
					t.Fatalf("resolve malformed assurance: %v", err)
				}
				return
			}
			if err != nil || principal.AAL != tc.want {
				t.Fatalf("resolved assurance = %d, %v; want %d", principal.AAL, err, tc.want)
			}
			evidence, ok := principal.AuthenticationEvidence()
			if !ok || evidence.AAL != tc.want {
				t.Fatal("resolved session did not carry its sealed authentication evidence")
			}
			wantAt, wantUntil := f.session.CreatedAt.Time(), f.now.Add(10*time.Minute)
			if tc.want > AAL1 {
				wantAt, wantUntil = stamp.Time(), until.Time()
			}
			if !evidence.AuthenticatedAt.Equal(wantAt) || !evidence.FreshUntil.Equal(wantUntil) {
				t.Fatalf("authentication evidence time/window = %s/%s; want %s/%s",
					evidence.AuthenticatedAt, evidence.FreshUntil, wantAt, wantUntil)
			}
			if _, err := evidence.AuthorityFor(f.now, principal, f.tenant); err != nil {
				t.Fatalf("native AuthorityFor refused the resolved session: %v", err)
			}
			if _, err := evidence.AuthorityFor(wantUntil, principal, f.tenant); !errors.Is(err, ErrPrincipalEvidenceUnavailable) {
				t.Fatalf("AuthorityFor admitted the expiry boundary: %v", err)
			}
			if tc.want == 2 {
				for _, edit := range []func(*Principal){
					func(p *Principal) { p.AAL = AAL3 },
					func(p *Principal) { p.AMR = []string{"sso"} },
				} {
					changed := cloneEvidencePrincipal(principal)
					edit(&changed)
					if _, ok := changed.AuthenticationEvidence(); ok {
						t.Fatal("edited assurance retained a valid seal")
					}
					if _, err := evidence.AuthorityFor(f.now, changed, f.tenant); !errors.Is(err, ErrPrincipalEvidenceUnavailable) {
						t.Fatalf("edited principal reused AAL2 authority: %v", err)
					}
				}
			}
		})
	}
}
