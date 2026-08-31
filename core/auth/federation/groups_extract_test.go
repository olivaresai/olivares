// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package federation

import (
	"reflect"
	"testing"

	"github.com/crewjam/saml"
)

// TestClaimStrings covers the OIDC groups-claim coercion (U1): a JSON array of
// strings (the usual shape), a single unwrapped string, and the fail-inert cases
// (non-string members, wrong type, empty) that must yield nil rather than error.
func TestClaimStrings(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want []string
	}{
		{"array", []any{"a", "b", " c "}, []string{"a", "b", "c"}},
		{"single string", "solo", []string{"solo"}},
		{"array with blanks and non-strings", []any{"", "x", 42, "  "}, []string{"x"}},
		{"empty array", []any{}, nil},
		{"empty string", "  ", nil},
		{"wrong type", 7, nil},
		{"nil", nil, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := claimStrings(c.in); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("claimStrings(%v) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

// TestSAMLExtractGroups proves the SAML groups attribute is read as a MULTI-valued
// attribute (unlike single-valued email), matched by Name or FriendlyName, with
// blanks dropped — and that an unconfigured groupsAttr reads nothing.
func TestSAMLExtractGroups(t *testing.T) {
	assertion := &saml.Assertion{
		AttributeStatements: []saml.AttributeStatement{{
			Attributes: []saml.Attribute{
				{Name: "http://schemas.xmlsoap.org/claims/Group", FriendlyName: "memberOf", Values: []saml.AttributeValue{
					{Value: "eng"}, {Value: "  "}, {Value: "admins"},
				}},
				{Name: "email", Values: []saml.AttributeValue{{Value: "u@x.io"}}},
			},
		}},
	}

	// Matched by FriendlyName.
	sp := &samlProvider{groupsAttr: "memberOf"}
	if got := sp.extractGroups(assertion); !reflect.DeepEqual(got, []string{"eng", "admins"}) {
		t.Fatalf("extractGroups (by friendly name) = %v, want [eng admins]", got)
	}
	// Matched by full Name.
	sp2 := &samlProvider{groupsAttr: "http://schemas.xmlsoap.org/claims/Group"}
	if got := sp2.extractGroups(assertion); !reflect.DeepEqual(got, []string{"eng", "admins"}) {
		t.Fatalf("extractGroups (by name) = %v, want [eng admins]", got)
	}
	// An unmatched attribute name yields nothing.
	sp3 := &samlProvider{groupsAttr: "nope"}
	if got := sp3.extractGroups(assertion); got != nil {
		t.Fatalf("extractGroups (no match) = %v, want nil", got)
	}
}
