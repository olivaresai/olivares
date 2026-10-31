// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package federation

import (
	"bytes"
	"log/slog"
	"reflect"
	"strings"
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
		{"a different claim in the marker", "groups", overageNames("roles"), answersNoGroups, nil, false},
		{"UserInfo answers without groups: a known empty list", "groups", nil, answersNoGroups, nil, false},
		{"UserInfo supplies the groups", "groups", nil,
			map[string]any{"sub": "user-123", "groups": []any{"eng"}}, []string{"eng"}, false},
		// An empty claim in the ID token is what sends the provider to UserInfo for the
		// groups, so what UserInfo answers must count: dropping it would sign the user in
		// with fewer groups than the IdP just said they hold.
		{"empty groups in the token, UserInfo supplies them", "groups", map[string]any{"groups": []any{}},
			map[string]any{"sub": "user-123", "groups": []any{"eng"}}, []string{"eng"}, false},
		{"null groups in the token, UserInfo supplies them", "groups", map[string]any{"groups": nil},
			map[string]any{"sub": "user-123", "groups": []any{"eng"}}, []string{"eng"}, false},
		{"groups in the token are not replaced by UserInfo's", "groups", map[string]any{"groups": []any{"eng"}},
			map[string]any{"sub": "user-123", "groups": []any{"admins"}}, []string{"eng"}, false},
		// The token had no groups and UserInfo, the other place they could come from,
		// did not deliver them: unknown, never "no groups".
		{"UserInfo fails", "groups", nil, nil, nil, true},
		{"UserInfo of another subject is discarded with its groups", "groups", nil,
			map[string]any{"sub": "someone-else", "groups": []any{"admins"}}, nil, true},
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

// answersNoGroups is a UserInfo response for the test subject that carries no groups.
var answersNoGroups = map[string]any{"sub": "user-123"}

// TestOIDC_GroupsIndeterminateWhy: each way the list is unknown carries its own stable
// code for the operator's log (the person signing in only reads sso_groups_indeterminate),
// and a known list carries none.
func TestOIDC_GroupsIndeterminateWhy(t *testing.T) {
	cases := []struct {
		name     string
		token    map[string]any // extra ID-token claims
		userinfo map[string]any
		want     string
	}{
		{"overage marker", overageNames("groups"), nil, "groups_overage"},
		{"unreadable claim", map[string]any{"groups": 7}, nil, "groups_claim_unreadable"},
		{"UserInfo fails", nil, nil, "userinfo_failed"},
		{"an empty claim and UserInfo fails", map[string]any{"groups": []any{}}, nil, "userinfo_failed"},
		{"UserInfo of another subject", nil, map[string]any{"sub": "someone-else"}, "userinfo_subject_mismatch"},
		{"a known list", map[string]any{"groups": []any{"eng"}}, nil, ""},
		{"UserInfo answers without groups", nil, answersNoGroups, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			idp := newOIDCTestIDP(t)
			claims := idp.baseClaims("n-1")
			for k, v := range tc.token {
				claims[k] = v
			}
			idp.idToken = idp.signRS256(claims)
			idp.userinfo = tc.userinfo
			id, err := validateWith(t, idp.providerWithGroups(t, "groups"), "n-1")
			if err != nil {
				t.Fatalf("validate: %v", err)
			}
			if id.GroupsIndeterminateWhy != tc.want || id.GroupsIndeterminate != (tc.want != "") {
				t.Fatalf("GroupsIndeterminate = %v, why = %q; want why %q", id.GroupsIndeterminate, id.GroupsIndeterminateWhy, tc.want)
			}
		})
	}
}

func TestOIDC_UserInfoFailureLogOmitsResponseBody(t *testing.T) {
	idp := newOIDCTestIDP(t)
	idp.idToken = idp.signRS256(idp.baseClaims("n-1"))
	idp.userinfoBody = `{"access_token":"userinfo-secret-marker","sub":"userinfo-subject-marker","email":"userinfo@example.invalid","groups":["group-secret-marker"]}`

	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	id, err := validateWith(t, idp.providerWithGroups(t, "groups"), "n-1")
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if !id.GroupsIndeterminate || id.GroupsIndeterminateWhy != "userinfo_failed" {
		t.Fatalf("GroupsIndeterminate = %v, why = %q; want userinfo_failed", id.GroupsIndeterminate, id.GroupsIndeterminateWhy)
	}
	got := logs.String()
	for _, marker := range []string{"userinfo-secret-marker", "userinfo-subject-marker", "userinfo@example.invalid", "group-secret-marker"} {
		if strings.Contains(got, marker) {
			t.Fatalf("UserInfo failure log contains response-body marker %q: %s", marker, got)
		}
	}
	if !strings.Contains(got, "reason=userinfo_failed") {
		t.Fatalf("UserInfo failure log lacks the stable reason: %s", got)
	}
}

func TestOIDC_NoUserInfoEndpointDoesNotWarn(t *testing.T) {
	idp := newOIDCTestIDP(t)
	idp.noUserinfoEndpoint = true
	idp.idToken = idp.signRS256(idp.baseClaims("n-1"))

	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	id, err := validateWith(t, idp.providerWithGroups(t, "groups"), "n-1")
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if id.GroupsIndeterminate {
		t.Fatalf("GroupsIndeterminate = true, why = %q; want a known empty list without an endpoint", id.GroupsIndeterminateWhy)
	}
	if got := logs.String(); strings.Contains(got, "userinfo_failed") {
		t.Fatalf("known empty groups without a UserInfo endpoint logged a failure: %s", got)
	}
}

// TestOIDC_GroupsUnreadable: a groups claim that is present but cannot be read as a list
// of strings is unknown, not "no groups". A claim
// that is absent, null, empty or blank sends the provider to UserInfo, and is a known
// empty list only when UserInfo answers (or the IdP has no UserInfo to ask).
func TestOIDC_GroupsUnreadable(t *testing.T) {
	cases := []struct {
		name        string
		token       any // value of the groups claim in the ID token; omitted when noClaim
		noClaim     bool
		userinfo    map[string]any
		noEndpoint  bool // discovery does not advertise a UserInfo endpoint
		wantGroups  []string
		wantUnknown bool
	}{
		{name: "an object", token: map[string]any{"a": 1}, wantUnknown: true},
		{name: "a number", token: 7, wantUnknown: true},
		{name: "a boolean", token: true, wantUnknown: true},
		{name: "an array of objects", token: []any{map[string]any{"id": "eng"}}, wantUnknown: true},
		{name: "an array with a non-string member keeps the rest but is partial", token: []any{"eng", 7}, wantGroups: []string{"eng"}, wantUnknown: true},
		{name: "null, UserInfo fails", token: nil, wantUnknown: true},
		{name: "an empty array, UserInfo fails", token: []any{}, wantUnknown: true},
		{name: "a blank string, UserInfo fails", token: "  ", wantUnknown: true},
		{name: "null, UserInfo answers without groups", token: nil, userinfo: answersNoGroups},
		{name: "an empty array, UserInfo answers without groups", token: []any{}, userinfo: answersNoGroups},
		{name: "an empty array and no UserInfo endpoint: absence is the answer", token: []any{}, noEndpoint: true},
		{name: "one group as a string", token: "eng", wantGroups: []string{"eng"}},
		{name: "blank members are dropped", token: []any{"eng", "  "}, wantGroups: []string{"eng"}},
		{name: "UserInfo delivers an unreadable claim", noClaim: true,
			userinfo: map[string]any{"sub": "user-123", "groups": map[string]any{"a": 1}}, wantUnknown: true},
		{name: "UserInfo delivers a null claim", noClaim: true,
			userinfo: map[string]any{"sub": "user-123", "groups": nil}, wantUnknown: true},
		{name: "no claim and no UserInfo endpoint: absence is the answer", noClaim: true, noEndpoint: true},
		// With nowhere else to look, an unreadable claim is still unreadable.
		{name: "an unreadable claim and no UserInfo endpoint", token: 7, noEndpoint: true, wantUnknown: true},
		// UserInfo's list replaces an unreadable token claim; its null is no list.
		{name: "an unreadable claim, UserInfo supplies the groups", token: 7,
			userinfo: map[string]any{"sub": "user-123", "groups": []any{"eng"}}, wantGroups: []string{"eng"}},
		{name: "an unreadable claim, UserInfo says null", token: 7,
			userinfo: map[string]any{"sub": "user-123", "groups": nil}, wantUnknown: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			idp := newOIDCTestIDP(t)
			idp.noUserinfoEndpoint = tc.noEndpoint
			claims := idp.baseClaims("n-1")
			if !tc.noClaim {
				claims["groups"] = tc.token
			}
			idp.idToken = idp.signRS256(claims)
			idp.userinfo = tc.userinfo
			p := idp.providerWithGroups(t, "groups")

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
			// The operator's log names the cause by this code.
			if wantWhy := map[bool]string{true: "groups_overage"}[tc.wantUnknown]; identity.GroupsIndeterminateWhy != wantWhy {
				t.Fatalf("GroupsIndeterminateWhy = %q, want %q", identity.GroupsIndeterminateWhy, wantWhy)
			}
			if !reflect.DeepEqual(identity.Groups, tc.wantGroups) {
				t.Fatalf("Groups = %v, want %v", identity.Groups, tc.wantGroups)
			}
		})
	}
}
