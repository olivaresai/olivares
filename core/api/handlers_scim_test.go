// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/api/scim"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

type scimResp struct {
	code int
	body map[string]any
	hdr  http.Header
	raw  string
}

func (h *harness) scim(method, path, token, jsonBody string) scimResp {
	h.t.Helper()
	var rdr io.Reader = http.NoBody
	if jsonBody != "" {
		rdr = strings.NewReader(jsonBody)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.Header.Set("Content-Type", scim.ContentType)
	req.RemoteAddr = "10.0.0.1:1234"
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.srv.Handler().ServeHTTP(rec, req)
	out := scimResp{code: rec.Code, hdr: rec.Header(), raw: rec.Body.String()}
	_ = json.Unmarshal(rec.Body.Bytes(), &out.body)
	return out
}

// scimToken provisions a tenant-bound admin token to authenticate SCIM requests.
func (h *harness) scimToken(super auth.Principal, tenant model.TenantID) string {
	h.t.Helper()
	tok, _, err := h.authr.IssueToken(context.Background(), super, auth.TokenSpec{
		Name: "scim", BoundTenant: tenant, Role: auth.RoleAdmin,
	})
	if err != nil {
		h.t.Fatal(err)
	}
	return tok
}

func TestSCIMUserLifecycle(t *testing.T) {
	h := newHarness(t)
	adminTok := h.adminLogin()
	tenant := h.createOrg(adminTok, "acme")
	super, err := h.authr.Authenticate(context.Background(), adminTok)
	if err != nil {
		t.Fatal(err)
	}
	tok := h.scimToken(super, tenant)
	const base = "/v1/scim/v2"

	// --- joiner: POST creates ---
	create := h.scim("POST", base+"/Users", tok, `{
		"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],
		"userName":"alice@acme.com","externalId":"idp-123","displayName":"Alice","active":true}`)
	if create.code != http.StatusCreated {
		t.Fatalf("create = %d %s", create.code, create.raw)
	}
	if ct := create.hdr.Get("Content-Type"); !strings.HasPrefix(ct, scim.ContentType) {
		t.Errorf("Content-Type = %q, want %s", ct, scim.ContentType)
	}
	if create.hdr.Get("Location") == "" {
		t.Error("missing Location header on create")
	}
	id, _ := create.body["id"].(string)
	if id == "" {
		t.Fatal("create returned no id")
	}

	// --- existence check: userName eq returns 200 + ListResponse (NEVER 404) ---
	filterURL := func(expr string) string {
		return base + "/Users?filter=" + url.QueryEscape(expr)
	}
	present := h.scim("GET", filterURL(`userName eq "alice@acme.com"`), tok, "")
	if present.code != http.StatusOK || total(present) != 1 {
		t.Errorf("present filter = %d total=%v", present.code, present.body["totalResults"])
	}
	absent := h.scim("GET", filterURL(`userName eq "ghost@acme.com"`), tok, "")
	if absent.code != http.StatusOK || total(absent) != 0 {
		t.Errorf("absent filter = %d total=%v, want 200 / 0 (not 404)", absent.code, absent.body["totalResults"])
	}
	// externalId filter resolves the same user.
	byExt := h.scim("GET", filterURL(`externalId eq "idp-123"`), tok, "")
	if total(byExt) != 1 {
		t.Errorf("externalId filter total = %v, want 1", byExt.body["totalResults"])
	}

	// --- duplicate POST -> 409 uniqueness, status as a STRING ---
	dup := h.scim("POST", base+"/Users", tok, `{"userName":"alice@acme.com","active":true}`)
	if dup.code != http.StatusConflict {
		t.Fatalf("duplicate = %d %s", dup.code, dup.raw)
	}
	if st, isStr := dup.body["status"].(string); !isStr || st != "409" {
		t.Errorf("error status = %v (%T), want string \"409\"", dup.body["status"], dup.body["status"])
	}
	if dup.body["scimType"] != scim.TypeUniqueness {
		t.Errorf("scimType = %v, want uniqueness", dup.body["scimType"])
	}

	// --- GET by id ---
	got := h.scim("GET", base+"/Users/"+id, tok, "")
	if got.code != http.StatusOK || got.body["active"] != true {
		t.Errorf("get = %d active=%v", got.code, got.body["active"])
	}

	// --- mover: PATCH active=false disables but keeps the resource retrievable ---
	patch := h.scim("PATCH", base+"/Users/"+id, tok,
		`{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"replace","path":"active","value":false}]}`)
	if patch.code != http.StatusOK || patch.body["active"] != false {
		t.Errorf("patch active=false = %d active=%v", patch.code, patch.body["active"])
	}
	after := h.scim("GET", base+"/Users/"+id, tok, "")
	if after.code != http.StatusOK || after.body["active"] != false {
		t.Errorf("post-disable get = %d active=%v, want 200 active:false", after.code, after.body["active"])
	}

	// --- PATCH remove without path -> 400 noTarget ---
	noTarget := h.scim("PATCH", base+"/Users/"+id, tok,
		`{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"remove"}]}`)
	if noTarget.code != http.StatusBadRequest || noTarget.body["scimType"] != scim.TypeNoTarget {
		t.Errorf("remove no path = %d scimType=%v, want 400 noTarget", noTarget.code, noTarget.body["scimType"])
	}

	// --- leaver: DELETE removes; subsequent GET -> 404 ---
	del := h.scim("DELETE", base+"/Users/"+id, tok, "")
	if del.code != http.StatusNoContent {
		t.Fatalf("delete = %d %s", del.code, del.raw)
	}
	gone := h.scim("GET", base+"/Users/"+id, tok, "")
	if gone.code != http.StatusNotFound {
		t.Errorf("get after delete = %d, want 404", gone.code)
	}
}

func TestSCIMAzureDisableShapeAndDiscovery(t *testing.T) {
	h := newHarness(t)
	adminTok := h.adminLogin()
	tenant := h.createOrg(adminTok, "acme")
	super, _ := h.authr.Authenticate(context.Background(), adminTok)
	tok := h.scimToken(super, tenant)
	const base = "/v1/scim/v2"

	create := h.scim("POST", base+"/Users", tok, `{"userName":"bob@acme.com","active":true}`)
	id, _ := create.body["id"].(string)

	// Azure/Entra no-path object shape: {op:replace, value:{active:false}}.
	patch := h.scim("PATCH", base+"/Users/"+id, tok,
		`{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"replace","value":{"active":false}}]}`)
	if patch.code != http.StatusOK || patch.body["active"] != false {
		t.Errorf("azure disable shape = %d active=%v", patch.code, patch.body["active"])
	}

	// Discovery: ServiceProviderConfig advertises patch support.
	spc := h.scim("GET", base+"/ServiceProviderConfig", tok, "")
	if spc.code != http.StatusOK {
		t.Fatalf("spconfig = %d", spc.code)
	}
	pm, _ := spc.body["patch"].(map[string]any)
	if pm["supported"] != true {
		t.Errorf("ServiceProviderConfig patch.supported = %v, want true", pm["supported"])
	}

	// Unauthenticated SCIM request -> 401 with a SCIM error body (status string).
	un := h.scim("GET", base+"/Users", "", "")
	if un.code != http.StatusUnauthorized {
		t.Errorf("unauth = %d, want 401", un.code)
	}
	if _, isStr := un.body["status"].(string); !isStr {
		t.Errorf("SCIM error status must be a string, got %T", un.body["status"])
	}
}

// TestSCIMEnterpriseExtensionWriteThrough proves the enterprise User extension
// (manager, department, employeeNumber) is read- AND write-through: a create that
// carries the extension persists it, a GET returns it (with the extension URN in
// schemas), a PATCH on an extension attribute mutates it, and a PATCH remove of
// the extension clears it. It is the regression guard for the goal that the
// extension be honored, not merely declared.
func TestSCIMEnterpriseExtensionWriteThrough(t *testing.T) {
	h := newHarness(t)
	adminTok := h.adminLogin()
	tenant := h.createOrg(adminTok, "acme")
	super, _ := h.authr.Authenticate(context.Background(), adminTok)
	tok := h.scimToken(super, tenant)
	const base = "/v1/scim/v2"
	const entURN = "urn:ietf:params:scim:schemas:extension:enterprise:2.0:User"

	// --- create carrying the enterprise extension (manager as the complex form) ---
	create := h.scim("POST", base+"/Users", tok, `{
		"schemas":["urn:ietf:params:scim:schemas:core:2.0:User","`+entURN+`"],
		"userName":"carol@acme.com","active":true,
		"`+entURN+`":{"employeeNumber":"E-42","department":"Platform","manager":{"value":"mgr-001","displayName":"Boss"}}}`)
	if create.code != http.StatusCreated {
		t.Fatalf("create = %d %s", create.code, create.raw)
	}
	id, _ := create.body["id"].(string)
	assertEnterprise := func(label string, body map[string]any, num, dept, mgr string) {
		t.Helper()
		ext, ok := body[entURN].(map[string]any)
		if !ok {
			t.Fatalf("%s: no enterprise extension in body: %v", label, body)
		}
		if ext["employeeNumber"] != num || ext["department"] != dept {
			t.Errorf("%s: employeeNumber/department = %v/%v, want %s/%s", label, ext["employeeNumber"], ext["department"], num, dept)
		}
		m, _ := ext["manager"].(map[string]any)
		if m == nil || m["value"] != mgr {
			t.Errorf("%s: manager.value = %v, want %s", label, ext["manager"], mgr)
		}
		// The extension URN must appear in schemas alongside the core URN.
		schemas, _ := body["schemas"].([]any)
		var hasExt bool
		for _, s := range schemas {
			if s == entURN {
				hasExt = true
			}
		}
		if !hasExt {
			t.Errorf("%s: schemas %v missing the enterprise extension URN", label, schemas)
		}
	}
	assertEnterprise("create", create.body, "E-42", "Platform", "mgr-001")

	// --- read-through: GET returns the persisted extension ---
	got := h.scim("GET", base+"/Users/"+id, tok, "")
	assertEnterprise("get", got.body, "E-42", "Platform", "mgr-001")

	// --- write-through: PATCH department (path carries the full extension URN) ---
	patch := h.scim("PATCH", base+"/Users/"+id, tok,
		`{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[
			{"op":"replace","path":"`+entURN+`:department","value":"Security"}]}`)
	if patch.code != http.StatusOK {
		t.Fatalf("patch department = %d %s", patch.code, patch.raw)
	}
	assertEnterprise("patch-department", patch.body, "E-42", "Security", "mgr-001")

	// --- write-through: PATCH manager via the no-path nested-object form (Entra) ---
	patchMgr := h.scim("PATCH", base+"/Users/"+id, tok,
		`{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[
			{"op":"replace","value":{"`+entURN+`":{"manager":{"value":"mgr-002"}}}}]}`)
	if patchMgr.code != http.StatusOK {
		t.Fatalf("patch manager = %d %s", patchMgr.code, patchMgr.raw)
	}
	assertEnterprise("patch-manager", patchMgr.body, "E-42", "Security", "mgr-002")

	// --- a PATCH that does not mention the extension must NOT clear it ---
	patchName := h.scim("PATCH", base+"/Users/"+id, tok,
		`{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[
			{"op":"replace","path":"displayName","value":"Carol R"}]}`)
	assertEnterprise("patch-unrelated", patchName.body, "E-42", "Security", "mgr-002")

	// --- remove a single extension attribute ---
	rm := h.scim("PATCH", base+"/Users/"+id, tok,
		`{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[
			{"op":"remove","path":"`+entURN+`:department"}]}`)
	if rm.code != http.StatusOK {
		t.Fatalf("remove department = %d %s", rm.code, rm.raw)
	}
	if ext, _ := rm.body[entURN].(map[string]any); ext["department"] != nil {
		t.Errorf("after remove, department = %v, want absent", ext["department"])
	}
}

// TestSCIMDiscoveryDeclaresEnterpriseExtension proves the discovery triad is
// internally consistent: the User ResourceType references the enterprise
// extension, and that schema is actually served by /Schemas and /Schemas/{urn}
// (the conformance gap an Entra/Okta connection check would otherwise hit).
func TestSCIMDiscoveryDeclaresEnterpriseExtension(t *testing.T) {
	h := newHarness(t)
	adminTok := h.adminLogin()
	tenant := h.createOrg(adminTok, "acme")
	super, _ := h.authr.Authenticate(context.Background(), adminTok)
	tok := h.scimToken(super, tenant)
	const base = "/v1/scim/v2"
	const entURN = "urn:ietf:params:scim:schemas:extension:enterprise:2.0:User"

	// /Schemas lists the enterprise extension as a first-class schema.
	schemas := h.scim("GET", base+"/Schemas", tok, "")
	if schemas.code != http.StatusOK {
		t.Fatalf("schemas = %d", schemas.code)
	}
	res, _ := schemas.body["Resources"].([]any)
	ids := map[string]bool{}
	for _, r := range res {
		if m, ok := r.(map[string]any); ok {
			if id, _ := m["id"].(string); id != "" {
				ids[id] = true
			}
		}
	}
	for _, want := range []string{scim.SchemaUser, entURN, scim.SchemaGroup} {
		if !ids[want] {
			t.Errorf("/Schemas missing %q (have %v)", want, ids)
		}
	}

	// /Schemas/{enterprise urn} resolves (not 404) and declares manager/department/employeeNumber.
	one := h.scim("GET", base+"/Schemas/"+entURN, tok, "")
	if one.code != http.StatusOK {
		t.Fatalf("GET /Schemas/%s = %d, want 200", entURN, one.code)
	}
	attrs, _ := one.body["attributes"].([]any)
	names := map[string]bool{}
	for _, a := range attrs {
		if m, ok := a.(map[string]any); ok {
			if n, _ := m["name"].(string); n != "" {
				names[n] = true
			}
		}
	}
	for _, want := range []string{"employeeNumber", "department", "manager"} {
		if !names[want] {
			t.Errorf("enterprise schema missing attribute %q (have %v)", want, names)
		}
	}

	// The User ResourceType references the extension it declares.
	rt := h.scim("GET", base+"/ResourceTypes/User", tok, "")
	exts, _ := rt.body["schemaExtensions"].([]any)
	var refsEnt bool
	for _, e := range exts {
		if m, ok := e.(map[string]any); ok && m["schema"] == entURN {
			refsEnt = true
		}
	}
	if !refsEnt {
		t.Errorf("User ResourceType does not reference the enterprise extension: %v", exts)
	}
}

func total(r scimResp) int {
	if v, ok := r.body["totalResults"].(float64); ok {
		return int(v)
	}
	return -1
}
