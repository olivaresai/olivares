// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package federation

import (
	"slices"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/russellhaering/gosaml2/types"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// assuranceMapping keeps one exact-value mapping for both verified protocols.
// Individual methods and arbitrary numeric ACRs do not establish upstream MFA.
func assuranceMapping(configured *model.FederationAssuranceMapping) model.FederationAssuranceMapping {
	mapping := model.FederationAssuranceMapping{
		AMR:          []string{"mfa"},
		SAMLContexts: []string{"https://refeds.org/profile/mfa"},
	}
	if configured != nil {
		if configured.AMR != nil {
			mapping.AMR = slices.Clone(configured.AMR)
		}
		mapping.ACR = slices.Clone(configured.ACR)
		if configured.SAMLContexts != nil {
			mapping.SAMLContexts = slices.Clone(configured.SAMLContexts)
		}
	}
	return mapping
}

func (o *oidcProvider) assertionAssurance(idTokenClaims *oidc.IDToken) (int, time.Time) {
	var claims struct {
		AMR      []string `json:"amr"`
		ACR      string   `json:"acr"`
		AuthTime int64    `json:"auth_time"`
	}
	if idTokenClaims.Claims(&claims) != nil || claims.AuthTime <= 0 {
		return auth.AAL1, time.Time{}
	}
	matched := claims.ACR != "" && slices.Contains(o.assurance.ACR, claims.ACR)
	knowledge, possession := false, false
	for _, method := range claims.AMR {
		matched = matched || slices.Contains(o.assurance.AMR, method)
		if o.allowAMRCombination {
			// RFC 8176 methods describe factors; repetition or two methods in
			// one category does not establish MFA. SMS stays opt-in.
			switch method {
			case "pwd", "pin":
				knowledge = true
			case "otp", "hwk", "swk":
				possession = true
			}
		}
	}
	matched = matched || knowledge && possession
	if !matched {
		return auth.AAL1, time.Time{}
	}
	return auth.AAL2, time.Unix(claims.AuthTime, 0).UTC()
}

func (s *samlProvider) assertionAssurance(assertion *types.Assertion) (int, time.Time) {
	mapping := assuranceMapping(s.assurance)
	statement := assertion.AuthnStatement
	if statement == nil || statement.AuthnContext == nil || statement.AuthnInstant == nil || statement.AuthnInstant.IsZero() {
		return auth.AAL1, time.Time{}
	}
	ref := statement.AuthnContext.AuthnContextClassRef
	if ref == nil || !slices.Contains(mapping.SAMLContexts, ref.Value) {
		return auth.AAL1, time.Time{}
	}
	if statement.SessionNotOnOrAfter != nil && !s.sp.Clock.Now().Before(*statement.SessionNotOnOrAfter) {
		return auth.AAL1, time.Time{}
	}
	// Context and event belong to the same signature-verified statement.
	return auth.AAL2, statement.AuthnInstant.UTC()
}
