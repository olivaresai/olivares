// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudewif

import (
	"context"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/connectors/identitysource"
)

const testFederation = `[{"rule_id":"fdrl_1","service_account_id":"svac_1","service_account_name":"ci-deployer","issuer_id":"fdis_1","issuer_url":"https://oidc.spire.example","oauth_scope":"workspace:developer","workspace_id":"wrkspc_1"}]`

func TestSnapshotLive(t *testing.T) {
	s, doer := newLive(t, testFederation)
	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	// Every Admin API call must be a read-only GET.
	for _, r := range doer.reqs {
		if r.Method != http.MethodGet {
			t.Fatalf("non-GET request: %s %s", r.Method, r.URL.Path)
		}
	}

	// NHI and human identities keyed on the RAW Anthropic id (the external_id the
	// roster converges on).
	cases := []struct {
		ref      string
		kind     string
		typ      identitysource.PrincipalType
		disabled bool
	}{
		{"apikey_1", kindAPIKey, identitysource.PrincipalNHI, false},
		{"apikey_2", kindAPIKey, identitysource.PrincipalNHI, true}, // inactive => disabled
		{"user_1", kindMember, identitysource.PrincipalHuman, false},
		{"invite_1", kindInvite, identitysource.PrincipalHuman, false},
		{"svac_1", kindServiceAccount, identitysource.PrincipalNHI, false},
		{"fdis_1", kindIssuer, identitysource.PrincipalNHI, false},
	}
	for _, c := range cases {
		id, ok := g.FindIdentity(c.ref)
		if !ok {
			t.Fatalf("identity %q missing from roster", c.ref)
		}
		if id.Kind != c.kind {
			t.Errorf("%q kind = %q, want %q", c.ref, id.Kind, c.kind)
		}
		if id.Type != c.typ {
			t.Errorf("%q type = %q, want %q", c.ref, id.Type, c.typ)
		}
		if id.Disabled != c.disabled {
			t.Errorf("%q disabled = %v, want %v", c.ref, id.Disabled, c.disabled)
		}
		if id.Source != identitysource.SourceAnthropic {
			t.Errorf("%q source = %q", c.ref, id.Source)
		}
	}

	// The key carries the masked hint (the only sk-ant- shaped value the API returns),
	// NEVER a secret value.
	key, _ := g.FindIdentity("apikey_1")
	if key.Attributes["key_hint"] != "sk-ant-***xyz" {
		t.Errorf("api key hint = %q, want the masked hint", key.Attributes["key_hint"])
	}
	if key.Attributes["status"] != "active" || key.Attributes["workspace"] != "wrkspc_1" {
		t.Errorf("api key attrs = %+v", key.Attributes)
	}

	// Collections: org group, workspace group, workspace role, federation rule policy.
	wantColl := map[string]identitysource.CollectionKind{
		"11111111-1111-1111-1111-111111111111": identitysource.KindGroup,
		"wrkspc_1":                             identitysource.KindGroup,
		"wrkspc_1#role:workspace_admin":        identitysource.KindRole,
		"fdrl_1":                               identitysource.KindPolicy,
	}
	gotColl := map[string]identitysource.CollectionKind{}
	for _, c := range g.Collections {
		gotColl[c.Ref] = c.Kind
	}
	for ref, kind := range wantColl {
		if gotColl[ref] != kind {
			t.Errorf("collection %q kind = %q, want %q", ref, gotColl[ref], kind)
		}
	}

	// Memberships: api key in workspace; service account in its rule; user in role.
	if !hasMembership(g, "apikey_1", "wrkspc_1") {
		t.Error("api key should belong to its workspace")
	}
	if !hasMembership(g, "svac_1", "fdrl_1") {
		t.Error("service account should belong to its federation rule")
	}
	if !hasMembership(g, "user_1", "wrkspc_1#role:workspace_admin") {
		t.Error("workspace member should belong to its workspace-role collection")
	}
	if !hasMembership(g, "user_1", "11111111-1111-1111-1111-111111111111") {
		t.Error("org member should belong to the organization")
	}
}

func hasMembership(g identitysource.Graph, member, collection string) bool {
	for _, m := range g.Memberships {
		if m.MemberRef == member && m.CollectionRef == collection {
			return true
		}
	}
	return false
}
