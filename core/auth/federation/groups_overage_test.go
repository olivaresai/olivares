// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package federation

import (
	"reflect"
	"testing"
)

// Group overage (Entra: more than 200 groups in a token, 150 in a SAML assertion). The
// IdP drops the groups and says so; the provider must carry that fact instead of
// returning an empty list that reads as "this user is in no group".

// overageNames is the aggregated-claims marker Entra sends in place of `groups`
// (OIDC Core 5.6.2): the claim is named in _claim_names and fetched from a source.
func overageNames(claim string) map[string]any {
	return map[string]any{
		"_claim_names":   map[string]any{claim: "src1"},
		"_claim_sources": map[string]any{"src1": map[string]any{"endpoint": "https://graph.example/v1.0/users/u/getMemberObjects"}},
	}
}

func TestOIDC_GroupsOverage(t *testing.T) {
	cases := []struct {
		name        string
		groupsClaim string // "" = the config reads no groups
		extra       map[string]any
		userinfo    map[string]any
		wantGroups  []string
		wantUnknown bool
	}{
		{"claim listed in _claim_names", "groups", overageNames("groups"), nil, nil, true},
		{"UserInfo with some groups does not complete a listed claim", "groups", overageNames("groups"),
			map[string]any{"sub": "user-123", "groups": []any{"only-one"}}, []string{"only-one"}, true},
		{"marker delivered by UserInfo", "groups", nil,
			map[string]any{"sub": "user-123", "_claim_names": map[string]any{"groups": "src1"}}, nil, true},
		{"custom groups claim named in the marker", "memberOf", overageNames("memberOf"), nil, nil, true},
		{"token lists another claim, UserInfo lists the groups", "groups", overageNames("roles"),
			map[string]any{"sub": "user-123", "_claim_names": map[string]any{"groups": "src1"}}, nil, true},
		{"UserInfo marker that is not an object cannot be read", "groups", nil,
			map[string]any{"sub": "user-123", "_claim_names": "groups"}, nil, true},
		{"a different claim in the marker", "groups", overageNames("roles"), nil, nil, false},
		{"no marker and no groups is a known empty list", "groups", nil, nil, nil, false},
		{"groups present, no marker", "groups", map[string]any{"groups": []any{"eng", "admins"}}, nil, []string{"eng", "admins"}, false},
		{"config reads no groups", "", overageNames("groups"), nil, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			idp := newOIDCTestIDP(t)
			claims := idp.baseClaims("n-1")
			for k, v := range tc.extra {
				claims[k] = v
			}
			idp.idToken = idp.signRS256(claims)
			idp.userinfo = tc.userinfo
			p := idp.provider(t)
			if tc.groupsClaim != "" {
				p = idp.providerWithGroups(t, tc.groupsClaim)
			}

			id, err := validateWith(t, p, "n-1")
			if err != nil {
				t.Fatalf("validate: %v", err)
			}
			if id.GroupsIndeterminate != tc.wantUnknown {
				t.Fatalf("GroupsIndeterminate = %v, want %v", id.GroupsIndeterminate, tc.wantUnknown)
			}
			if !reflect.DeepEqual(id.Groups, tc.wantGroups) {
				t.Fatalf("Groups = %v, want %v", id.Groups, tc.wantGroups)
			}
		})
	}
}

// groupsLinkAttr is what Entra puts in a SAML assertion once the user has more groups
// than it will send: a link to fetch them from, in place of the groups attribute.
const groupsLinkAttr = "http://schemas.microsoft.com/claims/groups.link"

func TestJourneySAML_GroupsOverage(t *testing.T) {
	link := jnyAttr{name: groupsLinkAttr, values: []string{"https://graph.windows.net/t/users/u/getMemberObjects"}}
	cases := []struct {
		name        string
		groupsAttr  string // "" = the config reads no groups
		attrs       []jnyAttr
		wantGroups  []string
		wantUnknown bool
	}{
		{"groups.link instead of the groups", "groups", []jnyAttr{link}, nil, true},
		{"groups.link matched case-insensitively", "groups",
			[]jnyAttr{{name: "HTTP://schemas.microsoft.com/claims/GROUPS.LINK", values: []string{"x"}}}, nil, true},
		{"groups.link beside some groups", "groups",
			[]jnyAttr{link, {name: "groups", values: []string{"eng"}}}, []string{"eng"}, true},
		{"no link and no groups is a known empty list", "groups", nil, nil, false},
		{"groups and no link", "groups", []jnyAttr{{name: "groups", values: []string{"eng", "admins"}}}, []string{"eng", "admins"}, false},
		{"config reads no groups", "", []jnyAttr{link}, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			j := newJourney(t, func(c *jnyConfig) { c.groupsAttr = tc.groupsAttr })
			r, id := j.answer()
			r.attrs = append(r.attrs, tc.attrs...)
			identity := j.mustLogin(r, id)
			if identity.GroupsIndeterminate != tc.wantUnknown {
				t.Fatalf("GroupsIndeterminate = %v, want %v", identity.GroupsIndeterminate, tc.wantUnknown)
			}
			if !reflect.DeepEqual(identity.Groups, tc.wantGroups) {
				t.Fatalf("Groups = %v, want %v", identity.Groups, tc.wantGroups)
			}
		})
	}
}
