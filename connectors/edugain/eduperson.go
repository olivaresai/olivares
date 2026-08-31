// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package edugain

import "strings"

// eduPerson SAML2 attribute Names (urn:oid form, NameFormat
// urn:oasis:names:tc:SAML:2.0:attrname-format:uri) under the eduPerson OID arc
// 1.3.6.1.4.1.5923.1.1.1.*, plus the OASIS SAML V2.0 Subject Identifier Attributes
// (subject-id / pairwise-id). Verified against the eduPerson schema and the OASIS
// Subject Identifier Attributes Profile (CS01, 2019-01-16). These are the
// attributes an R&E IdP releases that map to a local federated identity.
const (
	attrEPPN              = "urn:oid:1.3.6.1.4.1.5923.1.1.1.6"  // eduPersonPrincipalName
	attrEntitlement       = "urn:oid:1.3.6.1.4.1.5923.1.1.1.7"  // eduPersonEntitlement
	attrScopedAffiliation = "urn:oid:1.3.6.1.4.1.5923.1.1.1.9"  // eduPersonScopedAffiliation
	attrTargetedID        = "urn:oid:1.3.6.1.4.1.5923.1.1.1.10" // eduPersonTargetedID (DEPRECATED → pairwise-id)
	attrAssurance         = "urn:oid:1.3.6.1.4.1.5923.1.1.1.11" // eduPersonAssurance
	attrSubjectID         = "urn:oasis:names:tc:SAML:attribute:subject-id"
	attrPairwiseID        = "urn:oasis:names:tc:SAML:attribute:pairwise-id"
	attrMail              = "urn:oid:0.9.2342.19200300.100.1.3" // mail
	attrDisplayName       = "urn:oid:2.16.840.1.113730.3.1.241" // displayName
)

// Entity-category attribute values (verified). Sirtfi is asserted as an
// assurance-certification; R&S as an entity category. The scheme differs by value,
// so each is matched exactly (R&S is http://, Sirtfi https://).
const (
	// catResearchScholarship is the REFEDS Research & Scholarship category value.
	catResearchScholarship = "http://refeds.org/category/research-and-scholarship"
	// catSirtfi is the Sirtfi v1 incident-response trust framework value.
	catSirtfi = "https://refeds.org/sirtfi"
	// catSirtfi2 is the Sirtfi v2 value.
	catSirtfi2 = "https://refeds.org/sirtfi2"
)

// FederatedSubject is the normalized identity an eduPerson assertion maps to. It
// carries only non-sensitive directory metadata — never a credential. The
// principal name / subject-id is the convergence key for the roster (external_id).
type FederatedSubject struct {
	// PrincipalName is eduPersonPrincipalName (ePPN), the scoped "user@scope" name.
	PrincipalName string
	// SubjectID is the OASIS subject-id (a stable, non-reassigned, non-targeted id),
	// preferred over the deprecated eduPersonTargetedID.
	SubjectID string
	// PairwiseID is the OASIS pairwise-id (a per-RP targeted id).
	PairwiseID string
	// ScopedAffiliations are eduPersonScopedAffiliation values ("staff@uni.edu").
	ScopedAffiliations []string
	// Entitlements are eduPersonEntitlement URIs (authorization grants).
	Entitlements []string
	// Mail and DisplayName are optional human labels.
	Mail        string
	DisplayName string
	// AssuranceValues are eduPersonAssurance values (REFEDS Assurance Framework
	// profiles); the OpenID-Federation assurance mapper interprets them.
	AssuranceValues []string
}

// Ref returns the best stable reference for the subject (for roster convergence):
// subject-id, then ePPN, then pairwise-id.
func (f FederatedSubject) Ref() string {
	for _, v := range []string{f.SubjectID, f.PrincipalName, f.PairwiseID} {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// MapEduPerson maps a released SAML attribute set (Name → values) to a
// FederatedSubject. It reads only the eduPerson/subject-identifier attributes; any
// other released attribute is ignored (minimal data). It is a pure function.
func MapEduPerson(attrs map[string][]string) FederatedSubject {
	first := func(name string) string {
		if v := attrs[name]; len(v) > 0 {
			return strings.TrimSpace(v[0])
		}
		return ""
	}
	return FederatedSubject{
		PrincipalName:      first(attrEPPN),
		SubjectID:          first(attrSubjectID),
		PairwiseID:         first(attrPairwiseID),
		ScopedAffiliations: trimAll(attrs[attrScopedAffiliation]),
		Entitlements:       trimAll(attrs[attrEntitlement]),
		Mail:               first(attrMail),
		DisplayName:        first(attrDisplayName),
		AssuranceValues:    trimAll(attrs[attrAssurance]),
	}
}

// EntityCategories is the set of trust/release categories an entity asserts.
type EntityCategories struct {
	// Sirtfi reports whether the entity asserts the Sirtfi incident-response trust
	// framework (v1 or v2) — the security-contact / incident-response commitment.
	Sirtfi bool
	// SirtfiVersion is "1", "2" or "" depending on which value was asserted.
	SirtfiVersion string
	// ResearchScholarship reports whether the entity asserts REFEDS R&S.
	ResearchScholarship bool
}

// classifyCategories interprets a set of entity-attribute values into the known
// trust categories.
func classifyCategories(values []string) EntityCategories {
	var c EntityCategories
	for _, v := range values {
		switch strings.TrimSpace(v) {
		case catResearchScholarship:
			c.ResearchScholarship = true
		case catSirtfi:
			c.Sirtfi = true
			if c.SirtfiVersion == "" {
				c.SirtfiVersion = "1"
			}
		case catSirtfi2:
			c.Sirtfi = true
			c.SirtfiVersion = "2"
		}
	}
	return c
}

// trimAll trims whitespace from each value, dropping empties.
func trimAll(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, v := range in {
		if t := strings.TrimSpace(v); t != "" {
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
