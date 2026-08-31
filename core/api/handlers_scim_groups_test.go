// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

const scimBase = "/v1/scim/v2"

// scimUser provisions one user through the SCIM Users surface and returns its id.
func (h *harness) scimUser(tok, userName string) string {
	h.t.Helper()
	r := h.scim("POST", scimBase+"/Users", tok, fmt.Sprintf(`{"userName":%q,"active":true}`, userName))
	if r.code != http.StatusCreated {
		h.t.Fatalf("scim user %s = %d %s", userName, r.code, r.raw)
	}
	return r.body["id"].(string)
}

// memberIDs extracts the members[].value set from a SCIM Group response.
func memberIDs(r scimResp) map[string]bool {
	out := map[string]bool{}
	ms, _ := r.body["members"].([]any)
	for _, m := range ms {
		mm, _ := m.(map[string]any)
		if v, _ := mm["value"].(string); v != "" {
			out[v] = true
		}
	}
	return out
}

// TestSCIMGroupLifecycle drives the full IdP group-push flow: pre-create
// existence check, create with members, rename via PUT carrying the full
// members array (the Okta shape), PATCH membership deltas in both Okta and
// Entra shapes, and delete.
func TestSCIMGroupLifecycle(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	super, err := h.authr.Authenticate(context.Background(), admin)
	if err != nil {
		t.Fatal(err)
	}
	tok := h.scimToken(super, tenant)
	alice := h.scimUser(tok, "alice@acme.com")
	bob := h.scimUser(tok, "bob@acme.com")

	// --- IdP pre-create existence check: 200 + empty ListResponse, NEVER 404 ---
	probe := h.scim("GET", scimBase+"/Groups?filter="+url.QueryEscape(`displayName eq "Engineering"`), tok, "")
	if probe.code != http.StatusOK || total(probe) != 0 {
		t.Fatalf("pre-create probe = %d total=%v, want 200/0", probe.code, probe.body["totalResults"])
	}

	// --- create with one member ---
	create := h.scim("POST", scimBase+"/Groups", tok, fmt.Sprintf(`{
		"schemas":["urn:ietf:params:scim:schemas:core:2.0:Group"],
		"displayName":"Engineering","externalId":"idp-g1",
		"members":[{"value":%q,"display":"Alice"}]}`, alice))
	if create.code != http.StatusCreated {
		t.Fatalf("create = %d %s", create.code, create.raw)
	}
	if create.hdr.Get("Location") == "" {
		t.Error("missing Location header on group create")
	}
	gid, _ := create.body["id"].(string)
	if gid == "" {
		t.Fatal("create returned no id")
	}
	if !memberIDs(create)[alice] {
		t.Errorf("created group lacks alice: %v", create.body["members"])
	}
	// mapped_role never appears on the SCIM wire.
	if strings.Contains(strings.ToLower(create.raw), "mapped_role") || strings.Contains(create.raw, "mappedRole") {
		t.Errorf("SCIM response leaks the role mapping: %s", create.raw)
	}

	// --- displayName filter resolves it now; externalId filter too ---
	byName := h.scim("GET", scimBase+"/Groups?filter="+url.QueryEscape(`displayName eq "engineering"`), tok, "")
	if total(byName) != 1 {
		t.Errorf("displayName filter (case-insensitive) total = %v, want 1", byName.body["totalResults"])
	}
	byExt := h.scim("GET", scimBase+"/Groups?filter="+url.QueryEscape(`externalId eq "idp-g1"`), tok, "")
	if total(byExt) != 1 {
		t.Errorf("externalId filter total = %v, want 1", byExt.body["totalResults"])
	}

	// --- duplicate create (same externalId) => 409 uniqueness, status STRING ---
	dup := h.scim("POST", scimBase+"/Groups", tok, `{"displayName":"Engineering copy","externalId":"idp-g1"}`)
	if dup.code != http.StatusConflict {
		t.Fatalf("duplicate create = %d %s", dup.code, dup.raw)
	}
	if st, _ := dup.body["status"].(string); st != "409" {
		t.Errorf("conflict status = %v, want STRING \"409\"", dup.body["status"])
	}

	// --- Okta rename: PUT carries the FULL members array; members must survive ---
	put := h.scim("PUT", scimBase+"/Groups/"+gid, tok, fmt.Sprintf(`{
		"displayName":"Platform Engineering","externalId":"idp-g1",
		"members":[{"value":%q},{"value":%q}]}`, alice, bob))
	if put.code != http.StatusOK {
		t.Fatalf("put = %d %s", put.code, put.raw)
	}
	if put.body["displayName"] != "Platform Engineering" {
		t.Errorf("rename lost: %v", put.body["displayName"])
	}
	if ms := memberIDs(put); !ms[alice] || !ms[bob] {
		t.Errorf("PUT member set wrong: %v", put.body["members"])
	}

	// --- Okta remove-one: PATCH remove members[value eq "<id>"] ---
	patch := h.scim("PATCH", scimBase+"/Groups/"+gid, tok, fmt.Sprintf(`{
		"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],
		"Operations":[{"op":"remove","path":"members[value eq \"%s\"]"}]}`, bob))
	if patch.code != http.StatusOK {
		t.Fatalf("patch remove-one = %d %s", patch.code, patch.raw)
	}
	if ms := memberIDs(patch); ms[bob] || !ms[alice] {
		t.Errorf("remove-one member set wrong: %v", patch.body["members"])
	}

	// --- Entra add: capitalized op, members as objects ---
	entraAdd := h.scim("PATCH", scimBase+"/Groups/"+gid, tok, fmt.Sprintf(`{
		"Operations":[{"op":"Add","path":"members","value":[{"value":%q}]}]}`, bob))
	if entraAdd.code != http.StatusOK {
		t.Fatalf("entra add = %d %s", entraAdd.code, entraAdd.raw)
	}
	if ms := memberIDs(entraAdd); !ms[bob] {
		t.Errorf("entra add member set wrong: %v", entraAdd.body["members"])
	}

	// --- Entra no-path object value ---
	entraObj := h.scim("PATCH", scimBase+"/Groups/"+gid, tok, `{
		"Operations":[{"op":"Replace","value":{"displayName":"Platform"}}]}`)
	if entraObj.code != http.StatusOK || entraObj.body["displayName"] != "Platform" {
		t.Fatalf("entra no-path replace = %d %v", entraObj.code, entraObj.body["displayName"])
	}
	if ms := memberIDs(entraObj); !ms[alice] || !ms[bob] {
		t.Errorf("no-path attribute replace must not clear members: %v", entraObj.body["members"])
	}

	// --- remove path members with NO value clears the whole set ---
	clearAll := h.scim("PATCH", scimBase+"/Groups/"+gid, tok, `{
		"Operations":[{"op":"remove","path":"members"}]}`)
	if clearAll.code != http.StatusOK || len(memberIDs(clearAll)) != 0 {
		t.Fatalf("remove-all = %d members=%v", clearAll.code, clearAll.body["members"])
	}

	// --- remove with no path at all => 400 noTarget ---
	noTarget := h.scim("PATCH", scimBase+"/Groups/"+gid, tok, `{"Operations":[{"op":"remove"}]}`)
	if noTarget.code != http.StatusBadRequest || noTarget.body["scimType"] != "noTarget" {
		t.Errorf("remove without path = %d %v, want 400 noTarget", noTarget.code, noTarget.body["scimType"])
	}

	// --- bare-string member values are accepted (some IdPs send them) ---
	bare := h.scim("PATCH", scimBase+"/Groups/"+gid, tok, fmt.Sprintf(`{
		"Operations":[{"op":"add","path":"members","value":[%q]}]}`, alice))
	if bare.code != http.StatusOK || !memberIDs(bare)[alice] {
		t.Fatalf("bare-string member add = %d %v", bare.code, bare.body["members"])
	}

	// --- delete => 204, then GET => 404 ---
	if del := h.scim("DELETE", scimBase+"/Groups/"+gid, tok, ""); del.code != http.StatusNoContent {
		t.Fatalf("delete = %d %s", del.code, del.raw)
	}
	if gone := h.scim("GET", scimBase+"/Groups/"+gid, tok, ""); gone.code != http.StatusNotFound {
		t.Errorf("get after delete = %d, want 404", gone.code)
	}
}

// TestSCIMGroupMemberValidation pins the skip-and-audit posture: an unknown
// member id and a FOREIGN tenant's member id behave identically (skipped, group
// persists, no oracle), and nested groups are rejected honestly.
func TestSCIMGroupMemberValidation(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	other := h.createOrg(admin, "globex")
	super, err := h.authr.Authenticate(context.Background(), admin)
	if err != nil {
		t.Fatal(err)
	}
	tok := h.scimToken(super, tenant)
	otherTok := h.scimToken(super, other)
	alice := h.scimUser(tok, "alice@acme.com")
	mallory := h.scimUser(otherTok, "mallory@globex.com") // member of globex only

	create := h.scim("POST", scimBase+"/Groups", tok, fmt.Sprintf(`{
		"displayName":"Mixed","members":[
			{"value":%q},{"value":"00000000-0000-0000-0000-000000000000"},{"value":%q}]}`, alice, mallory))
	if create.code != http.StatusCreated {
		t.Fatalf("create = %d %s", create.code, create.raw)
	}
	ms := memberIDs(create)
	if !ms[alice] || ms[mallory] || len(ms) != 1 {
		t.Errorf("member validation wrong: got %v, want only alice (unknown + foreign skipped identically)", ms)
	}

	// Nested group member => 400 invalidValue (honest: no nesting counterpart).
	nested := h.scim("POST", scimBase+"/Groups", tok, fmt.Sprintf(`{
		"displayName":"Nested","members":[{"value":%q,"type":"Group"}]}`, alice))
	if nested.code != http.StatusBadRequest || nested.body["scimType"] != "invalidValue" {
		t.Errorf("nested group = %d %v, want 400 invalidValue", nested.code, nested.body["scimType"])
	}
}

// TestSCIMGroupTenantIsolation pins the cross-tenant oracle rule for every
// /Groups/{id} verb: another tenant's group is indistinguishable from a missing
// one.
func TestSCIMGroupTenantIsolation(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenantA := h.createOrg(admin, "acme")
	tenantB := h.createOrg(admin, "globex")
	super, err := h.authr.Authenticate(context.Background(), admin)
	if err != nil {
		t.Fatal(err)
	}
	tokA := h.scimToken(super, tenantA)
	tokB := h.scimToken(super, tenantB)

	create := h.scim("POST", scimBase+"/Groups", tokA, `{"displayName":"Secret Group"}`)
	if create.code != http.StatusCreated {
		t.Fatalf("create = %d %s", create.code, create.raw)
	}
	gid := create.body["id"].(string)

	for _, tc := range []struct{ method, body string }{
		{"GET", ""},
		{"PUT", `{"displayName":"Stolen"}`},
		{"PATCH", `{"Operations":[{"op":"replace","path":"displayName","value":"Stolen"}]}`},
		{"DELETE", ""},
	} {
		if r := h.scim(tc.method, scimBase+"/Groups/"+gid, tokB, tc.body); r.code != http.StatusNotFound {
			t.Errorf("%s from tenant B = %d, want 404 (no cross-tenant oracle)", tc.method, r.code)
		}
	}
	// Tenant B's listing never shows it.
	if list := h.scim("GET", scimBase+"/Groups", tokB, ""); total(list) != 0 {
		t.Errorf("tenant B sees %v groups, want 0", list.body["totalResults"])
	}
}

// TestSCIMGroupRoleMappingCeiling drives the privilege boundary end-to-end: the
// operator maps a group to a role (ceiling-checked), the SCIM RoleAdmin token
// cannot add members to an OWNER-mapped group (403), can to an editor-mapped
// one, and a member's session acts with the elevated effective role while the
// mapping is invisible on the SCIM wire.
func TestSCIMGroupRoleMappingCeiling(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	super, err := h.authr.Authenticate(context.Background(), admin)
	if err != nil {
		t.Fatal(err)
	}
	tok := h.scimToken(super, tenant)
	alice := h.scimUser(tok, "alice@acme.com")

	create := h.scim("POST", scimBase+"/Groups", tok, `{"displayName":"Admins","externalId":"g-admins"}`)
	if create.code != http.StatusCreated {
		t.Fatalf("create = %d %s", create.code, create.raw)
	}
	gid := create.body["id"].(string)

	// Operator surface: superadmin maps the group to owner.
	set := h.do("PUT", "/v1/groups/"+gid+"/role", admin, map[string]any{"role": "owner"}, tenantHdr(tenant))
	if set.code != http.StatusOK || set.body["mapped_role"] != "owner" {
		t.Fatalf("set role = %d %s", set.code, set.raw)
	}
	// Unknown role => 400; the mapping listing shows the group.
	if bad := h.do("PUT", "/v1/groups/"+gid+"/role", admin, map[string]any{"role": "demigod"}, tenantHdr(tenant)); bad.code != http.StatusBadRequest {
		t.Errorf("unknown role = %d, want 400", bad.code)
	}
	list := h.do("GET", "/v1/groups", admin, nil, tenantHdr(tenant))
	if list.code != http.StatusOK || !strings.Contains(list.raw, `"mapped_role":"owner"`) {
		t.Errorf("operator listing = %d %s", list.code, list.raw)
	}

	// The RoleAdmin SCIM token may NOT add members to an owner-mapped group:
	// that would grant owner above its own ceiling.
	add := h.scim("PATCH", scimBase+"/Groups/"+gid, tok, fmt.Sprintf(`{
		"Operations":[{"op":"add","path":"members","value":[{"value":%q}]}]}`, alice))
	if add.code != http.StatusForbidden {
		t.Fatalf("member add to owner-mapped group with admin token = %d %s, want 403", add.code, add.raw)
	}

	// Downgrade the mapping to editor: the admin token now passes the ceiling.
	if r := h.do("PUT", "/v1/groups/"+gid+"/role", admin, map[string]any{"role": "editor"}, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("remap = %d %s", r.code, r.raw)
	}
	add = h.scim("PATCH", scimBase+"/Groups/"+gid, tok, fmt.Sprintf(`{
		"Operations":[{"op":"add","path":"members","value":[{"value":%q}]}]}`, alice))
	if add.code != http.StatusOK {
		t.Fatalf("member add to editor-mapped group = %d %s", add.code, add.raw)
	}

	// mapped_role is unsettable through SCIM: a body smuggling role-like
	// attributes is leniently ignored and the mapping is unchanged.
	smuggle := h.scim("PUT", scimBase+"/Groups/"+gid, tok, fmt.Sprintf(`{
		"displayName":"Admins","externalId":"g-admins","mappedRole":"owner","roles":["owner"],
		"members":[{"value":%q}]}`, alice))
	if smuggle.code != http.StatusOK {
		t.Fatalf("smuggle put = %d %s", smuggle.code, smuggle.raw)
	}
	after := h.do("GET", "/v1/groups", admin, nil, tenantHdr(tenant))
	if !strings.Contains(after.raw, `"mapped_role":"editor"`) {
		t.Errorf("SCIM body changed the mapping: %s", after.raw)
	}

	// Effective role end-to-end: alice (viewer by SCIM provisioning, member of
	// the editor-mapped group) logs in and her session principal acts as editor.
	aliceID := model.ID(alice)
	if err := h.authr.SetPassword(context.Background(), super, aliceID, "supersecret1"); err != nil {
		t.Fatalf("set password: %v", err)
	}
	login := h.do("POST", "/v1/auth/login", "", map[string]any{"email": "alice@acme.com", "password": "supersecret1"}, nil)
	if login.code != http.StatusOK {
		t.Fatalf("alice login = %d %s", login.code, login.raw)
	}
	p, err := h.authr.Authenticate(context.Background(), login.body["token"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if role, _ := p.RoleIn(tenant); role != auth.RoleEditor {
		t.Errorf("alice effective role = %q, want editor (viewer direct + editor-mapped group)", role)
	}
}

// TestSCIMGroupDiscovery pins that the discovery documents advertise Group and
// the per-item routes resolve it.
func TestSCIMGroupDiscovery(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	super, err := h.authr.Authenticate(context.Background(), admin)
	if err != nil {
		t.Fatal(err)
	}
	tok := h.scimToken(super, tenant)

	rt := h.scim("GET", scimBase+"/ResourceTypes", tok, "")
	if rt.code != http.StatusOK || total(rt) != 2 || !strings.Contains(rt.raw, `"Group"`) {
		t.Errorf("ResourceTypes = %d total=%v raw=%s", rt.code, rt.body["totalResults"], rt.raw)
	}
	one := h.scim("GET", scimBase+"/ResourceTypes/Group", tok, "")
	if one.code != http.StatusOK || one.body["endpoint"] != "/Groups" {
		t.Errorf("ResourceTypes/Group = %d %v", one.code, one.body["endpoint"])
	}
	// User + enterprise extension + agent extension (defensive/opt-in) + Group.
	// All four schemas are first-class since the discovery quartet is internally
	// consistent (declared==honored for enterprise; declared+opt-in for agent).
	sch := h.scim("GET", scimBase+"/Schemas", tok, "")
	if sch.code != http.StatusOK || total(sch) != 4 {
		t.Errorf("Schemas = %d total=%v", sch.code, sch.body["totalResults"])
	}
	groupURN := "urn:ietf:params:scim:schemas:core:2.0:Group"
	if byURN := h.scim("GET", scimBase+"/Schemas/"+groupURN, tok, ""); byURN.code != http.StatusOK {
		t.Errorf("Schemas/%s = %d", groupURN, byURN.code)
	}
}

// TestSCIMGroupListPagination pins ParsePage/Slice over groups.
func TestSCIMGroupListPagination(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	super, err := h.authr.Authenticate(context.Background(), admin)
	if err != nil {
		t.Fatal(err)
	}
	tok := h.scimToken(super, tenant)
	for i := 0; i < 5; i++ {
		r := h.scim("POST", scimBase+"/Groups", tok, fmt.Sprintf(`{"displayName":"g-%d","externalId":"ext-%d"}`, i, i))
		if r.code != http.StatusCreated {
			t.Fatalf("create g-%d = %d %s", i, r.code, r.raw)
		}
	}
	page := h.scim("GET", scimBase+"/Groups?startIndex=3&count=2", tok, "")
	if page.code != http.StatusOK || total(page) != 5 {
		t.Fatalf("page = %d total=%v", page.code, page.body["totalResults"])
	}
	if ipp, _ := page.body["itemsPerPage"].(float64); ipp != 2 {
		t.Errorf("itemsPerPage = %v, want 2", page.body["itemsPerPage"])
	}
	// count=0 is a valid count-only request.
	countOnly := h.scim("GET", scimBase+"/Groups?count=0", tok, "")
	if total(countOnly) != 5 {
		t.Errorf("count-only total = %v, want 5", countOnly.body["totalResults"])
	}
	if rs, _ := countOnly.body["Resources"].([]any); len(rs) != 0 {
		t.Errorf("count-only returned %d resources, want 0", len(rs))
	}
}

// TestSCIMSETGroupSubjectIgnored pins that a SET naming a Groups/{id} subject is
// acknowledged WITHOUT action — never misrouted into the user credential-cut
// path now that group ids are real resources.
func TestSCIMSETGroupSubjectIgnored(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	super, err := h.authr.Authenticate(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	tok := h.scimToken(super, tenant)
	g := h.scim("POST", scimBase+"/Groups", tok, `{"displayName":"Engineering"}`)
	if g.code != http.StatusCreated {
		t.Fatalf("create = %d %s", g.code, g.raw)
	}
	gid := g.body["id"].(string)

	priv, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	pubPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
	if err := h.authr.ConfigureSCIMSet(ctx, super, tenant, auth.SCIMSetConfig{
		SETPublisher: auth.SETPublisher{
			Enabled: true, Issuer: "https://idp.test", Audiences: []string{"https://cp.test"},
			Keys: []auth.SETVerificationKey{{Kid: "k1", Alg: "ES256", PEM: pubPEM}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	// A prov:delete naming the GROUP: were the resource type discarded, the group
	// id would be resolved as a user id (and made group ids real, so the
	// misroute is no longer hypothetical).
	set := signSETForTest(t, priv, map[string]any{
		"iss": "https://idp.test", "aud": []string{"https://cp.test"}, "iat": time.Now().Unix(), "jti": "jg1",
		"sub_id": map[string]any{"format": "scim", "uri": "Groups/" + gid},
		"events": map[string]any{"urn:ietf:params:scim:event:prov:delete": map[string]any{}},
	})
	r := h.scim("POST", scimBase+"/Events", tok, set)
	if r.code != http.StatusAccepted {
		t.Fatalf("group-subject SET = %d %s, want 202 acknowledged-no-action", r.code, r.raw)
	}
	// The group is untouched.
	if got := h.scim("GET", scimBase+"/Groups/"+gid, tok, ""); got.code != http.StatusOK {
		t.Errorf("group after SET = %d, want 200", got.code)
	}
}
