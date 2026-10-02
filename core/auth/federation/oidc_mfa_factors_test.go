// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package federation

import (
	"context"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

func TestOIDC_DefaultMFARequiresDistinctFactors(t *testing.T) {
	idp := newOIDCTestIDP(t)
	for _, tc := range []struct {
		name    string
		methods []string
		mapping *model.FederationAssuranceMapping
		want    int
	}{
		{"password alone", []string{"pwd"}, nil, auth.AAL1},
		{"OTP alone", []string{"otp"}, nil, auth.AAL1},
		{"hardware key alone", []string{"hwk"}, nil, auth.AAL1},
		{"software key alone", []string{"swk"}, nil, auth.AAL1},
		{"repeated OTP", []string{"otp", "otp"}, nil, auth.AAL1},
		{"two knowledge methods", []string{"pwd", "pin"}, nil, auth.AAL1},
		{"two possession methods", []string{"hwk", "swk", "otp"}, nil, auth.AAL1},
		{"explicit MFA", []string{"mfa"}, nil, auth.AAL2},
		{"password and OTP", []string{"pwd", "otp"}, nil, auth.AAL2},
		{"password and hardware key", []string{"pwd", "hwk"}, nil, auth.AAL2},
		{"hardware key and PIN", []string{"hwk", "pin"}, nil, auth.AAL2},
		{"unrecognized methods", []string{"pwd", "custom-key"}, nil, auth.AAL1},
		{"empty mapping uses defaults", []string{"pwd", "otp"}, &model.FederationAssuranceMapping{}, auth.AAL2},
		{"empty AMR disables combinations", []string{"pwd", "otp"}, &model.FederationAssuranceMapping{AMR: []string{}}, auth.AAL1},
		{"operator exact mapping", []string{"hwk"}, &model.FederationAssuranceMapping{AMR: []string{"hwk"}}, auth.AAL2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := FromConfig(context.Background(), auth.FederationParams{
				Protocol: auth.ProtocolOIDC, OIDCIssuer: idp.srv.URL,
				OIDCClientID: testClientID, OIDCClientSecret: "test-secret",
				AssuranceMapping: tc.mapping,
			})
			if err != nil {
				t.Fatal(err)
			}
			authenticatedAt := time.Now().Add(-time.Minute).Truncate(time.Second)
			claims := idp.baseClaims("distinct-factor-nonce")
			claims["amr"], claims["auth_time"] = tc.methods, authenticatedAt.Unix()
			idp.idToken = idp.signRS256(claims)
			identity, err := p.ValidateAssertion(context.Background(), auth.Assertion{
				Protocol: auth.ProtocolOIDC, Raw: "code", Nonce: "distinct-factor-nonce",
				PKCEVerifier: "verifier", RedirectURI: "https://app.example/callback",
			})
			if err != nil {
				t.Fatalf("valid signed login refused: %v", err)
			}
			if identity.AAL != tc.want {
				t.Fatalf("signed methods %v yielded AAL%d; want AAL%d", tc.methods, identity.AAL, tc.want)
			}
			if tc.want == auth.AAL2 && !identity.AuthenticatedAt.Equal(authenticatedAt) {
				t.Fatalf("verified authentication event changed: %v", identity.AuthenticatedAt)
			}
			if tc.want == auth.AAL1 && !identity.AuthenticatedAt.IsZero() {
				t.Fatalf("single-factor login retained an elevated event: %v", identity.AuthenticatedAt)
			}
		})
	}
}
