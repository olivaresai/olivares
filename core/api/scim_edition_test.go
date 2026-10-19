//go:build !enterprise || !addon_ids

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/api/scim"
	"github.com/olivaresai/olivares/core/auth"
)

// The inbound SCIM server (users, group push and the SET receiver) is a Business
// capability. Community keeps every route mounted, so a published URL never turns
// into a 404, authenticates the token as before, and then answers 501 in the SCIM
// error shape an identity provider parses, naming Business. Nothing is written.
func TestCommunitySCIMServerAnswersBusinessAfterAuthentication(t *testing.T) {
	h := newHarness(t)
	admin := h.elevatedAdmin()
	tenant := h.createOrg(admin, "scim-edition")
	member := h.onboardPassword(admin, tenant, "member@scim-edition.test", auth.RoleViewer, fixtureSecret)
	bearer := h.scimToken(h.principalOf(admin), tenant)

	const base = "/v1/scim/v2"
	const user = `{"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],"userName":"joiner@scim-edition.test","active":true}`
	const group = `{"schemas":["urn:ietf:params:scim:schemas:core:2.0:Group"],"displayName":"Engineering"}`
	const patch = `{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"replace","path":"active","value":false}]}`
	id := member.String()
	// Every SCIM route the router mounts, read from the classification table that
	// cmd/olivares pins to the real router in both directions: a route added to the
	// router without a row there, or a row without a route, is red there, so this test
	// cannot silently miss one.
	var routes []struct{ method, path, body string }
	for key := range api.ClassifiedRoutes() {
		method, path, _ := strings.Cut(key, " ")
		if !strings.HasPrefix(path, base+"/") {
			continue
		}
		path = strings.NewReplacer("{id}", id, "{type}", "User", "{urn}", "urn:ietf:params:scim:schemas:core:2.0:User").Replace(path)
		body := ""
		switch {
		case method == "POST" && strings.HasSuffix(path, "/Events"):
			body = "not-a-jws"
		case method == "PATCH":
			body = patch
		case (method == "POST" || method == "PUT") && strings.Contains(path, "/Groups"):
			body = group
		case method == "POST" || method == "PUT":
			body = user
		}
		routes = append(routes, struct{ method, path, body string }{method, path, body})
	}
	sort.Slice(routes, func(i, j int) bool {
		return routes[i].path+routes[i].method < routes[j].path+routes[j].method
	})
	if len(routes) < 18 {
		t.Fatalf("the classification table lists %d SCIM routes, want the 18 the server mounts", len(routes))
	}
	roster := func() string { return h.do("GET", "/v1/members", admin, nil, tenantHdr(tenant)).raw }
	groups := func() string { return h.do("GET", "/v1/groups", admin, nil, tenantHdr(tenant)).raw }
	before, groupsBefore := roster(), groups()

	for _, tc := range routes {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			path := tc.path
			res := h.scim(tc.method, path, bearer, tc.body)
			if res.code != http.StatusNotImplemented {
				t.Fatalf("Community %s %s = %d %s, want 501", tc.method, path, res.code, res.raw)
			}
			if ct := res.hdr.Get("Content-Type"); !strings.HasPrefix(ct, scim.ContentType) {
				t.Errorf("Content-Type = %q, want %s", ct, scim.ContentType)
			}
			if cc := res.hdr.Get("Cache-Control"); cc != "no-store" {
				t.Errorf("Cache-Control = %q, want no-store on a refusal", cc)
			}
			if res.body["status"] != "501" {
				t.Errorf("status = %v, want the string \"501\" an identity provider reads", res.body["status"])
			}
			if schemas, _ := res.body["schemas"].([]any); len(schemas) != 1 || schemas[0] != scim.MsgError {
				t.Errorf("schemas = %v, want [%s]", res.body["schemas"], scim.MsgError)
			}
			if detail, _ := res.body["detail"].(string); !strings.Contains(detail, "Business") {
				t.Errorf("detail = %q, want it to name Business", detail)
			}
			if anon := h.scim(tc.method, path, "", tc.body); anon.code != http.StatusUnauthorized || anon.body["status"] != "401" {
				t.Errorf("unauthenticated %s %s = %d %s, want a SCIM 401", tc.method, path, anon.code, anon.raw)
			}
		})
	}
	if after := roster(); after != before {
		t.Errorf("a refused SCIM request changed the roster:\nbefore %s\nafter  %s", before, after)
	}
	if after := groups(); after != groupsBefore {
		t.Errorf("a refused SCIM request changed the groups:\nbefore %s\nafter  %s", groupsBefore, after)
	}
	if !h.memberOf(member, tenant) {
		t.Error("a refused SCIM DELETE removed the member")
	}
}

// The refusal comes after the admission, not instead of it: a token that lacks the
// permission, or a caller that names no tenant, is still told so in the SCIM shape.
func TestCommunitySCIMRefusalFollowsAdmission(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	super := h.principalOf(admin)
	tenant := h.createOrg(admin, "scim-admission")
	viewer, _, err := h.authr.IssueToken(context.Background(), super, auth.TokenSpec{
		Name: "viewer", BoundTenant: tenant, Role: auth.RoleViewer,
	})
	if err != nil {
		t.Fatal(err)
	}
	const user = `{"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],"userName":"joiner@scim-admission.test","active":true}`

	if res := h.scim("POST", "/v1/scim/v2/Users", viewer, user); res.code != http.StatusForbidden || res.body["status"] != "403" {
		t.Errorf("a token without user:write: POST /Users = %d %s, want a SCIM 403", res.code, res.raw)
	}
	// A superadmin session names no tenant here, and the SCIM server serves exactly one.
	if res := h.scim("GET", "/v1/scim/v2/Users", admin, ""); res.code != http.StatusForbidden || res.body["status"] != "403" {
		t.Errorf("a caller that names no tenant: GET /Users = %d %s, want a SCIM 403", res.code, res.raw)
	}
}
