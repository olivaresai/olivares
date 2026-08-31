// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package scim

import (
	"encoding/json"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

// decodeBody is a test helper mirroring how the handler decodes a SCIM body.
func decodeBody(t *testing.T, raw string) InboundUser {
	t.Helper()
	var b userBody
	if err := json.Unmarshal([]byte(raw), &b); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return DecodeUser(b)
}

func TestDecodeUserEnterpriseComplexAndBareManager(t *testing.T) {
	const urn = `"urn:ietf:params:scim:schemas:extension:enterprise:2.0:User"`

	// Complex manager (the RFC / Entra form) + employeeNumber + department.
	complexForm := decodeBody(t, `{"userName":"a@x.com",`+urn+`:{
		"employeeNumber":"E1","department":"Eng","manager":{"value":"m-1","displayName":"Boss"}}}`)
	if complexForm.EmployeeNumber != "E1" || complexForm.Department != "Eng" || complexForm.Manager != "m-1" {
		t.Errorf("complex form decoded %+v", complexForm)
	}

	// Bare-string manager (the loose form some IdPs send) is accepted.
	bareForm := decodeBody(t, `{"userName":"a@x.com",`+urn+`:{"manager":"m-2"}}`)
	if bareForm.Manager != "m-2" {
		t.Errorf("bare manager = %q, want m-2", bareForm.Manager)
	}

	// No extension at all → all extension fields empty.
	none := decodeBody(t, `{"userName":"a@x.com"}`)
	if none.EmployeeNumber != "" || none.Department != "" || none.Manager != "" {
		t.Errorf("absent extension decoded non-empty: %+v", none)
	}
}

func TestEncodeUserOmitsEmptyExtension(t *testing.T) {
	now := model.BaseFields{}
	// A user with no extension attributes must NOT carry the extension URN in schemas
	// nor an extension object (declared==honored, and never an empty object).
	plain := EncodeUser(model.User{BaseFields: now, Email: "a@x.com", Status: model.StatusActive}, "https://h/Users")
	schemas, _ := plain["schemas"].([]string)
	if len(schemas) != 1 || schemas[0] != SchemaUser {
		t.Errorf("plain user schemas = %v, want [User]", plain["schemas"])
	}
	if _, has := plain[SchemaEnterpriseUser]; has {
		t.Error("plain user carries an enterprise extension object")
	}

	// With an attribute set, the URN appears and the object carries the complex manager.
	withExt := EncodeUser(model.User{
		BaseFields: now, Email: "a@x.com", Status: model.StatusActive,
		Department: "Eng", Manager: "m-1",
	}, "https://h/Users")
	schemas2, _ := withExt["schemas"].([]string)
	if len(schemas2) != 2 || schemas2[1] != SchemaEnterpriseUser {
		t.Errorf("ext user schemas = %v, want [User, Enterprise]", withExt["schemas"])
	}
	ext, _ := withExt[SchemaEnterpriseUser].(map[string]any)
	if ext["department"] != "Eng" {
		t.Errorf("ext department = %v", ext["department"])
	}
	mgr, _ := ext["manager"].(map[string]any)
	if mgr["value"] != "m-1" {
		t.Errorf("ext manager = %v, want {value:m-1}", ext["manager"])
	}
}

// TestUserSchemaEmailsMutabilityHonest pins declared==honored for emails: value is
// writable (it seeds userName), but type/primary are server-determined, so they must
// be declared readOnly rather than over-promising a write the provider ignores.
func TestUserSchemaEmailsMutabilityHonest(t *testing.T) {
	sch := userSchema("https://h/v1/scim/v2")
	attrs, _ := sch["attributes"].([]map[string]any)
	var emails map[string]any
	for _, a := range attrs {
		if a["name"] == "emails" {
			emails = a
		}
	}
	if emails == nil {
		t.Fatal("userSchema declares no emails attribute")
	}
	sub, _ := emails["subAttributes"].([]map[string]any)
	got := map[string]string{}
	for _, s := range sub {
		got[s["name"].(string)] = s["mutability"].(string)
	}
	if got["value"] != "readWrite" {
		t.Errorf("emails.value mutability = %q, want readWrite", got["value"])
	}
	for _, ro := range []string{"type", "primary"} {
		if got[ro] != "readOnly" {
			t.Errorf("emails.%s mutability = %q, want readOnly (server-determined, not honored as a write)", ro, got[ro])
		}
	}
}

func TestApplyPatchEnterprisePaths(t *testing.T) {
	const urn = "urn:ietf:params:scim:schemas:extension:enterprise:2.0:User"
	base := InboundUser{UserName: "a@x.com", Active: true, EmployeeNumber: "E1", Department: "Eng", Manager: "m-1"}

	// Path-scoped replace of a scalar extension attribute.
	patched, err := ApplyPatch(base, PatchBody{Operations: []PatchOp{
		{Op: "replace", Path: urn + ":department", Value: json.RawMessage(`"Security"`)},
	}})
	if err != nil || patched.Department != "Security" {
		t.Fatalf("dept patch = %+v err=%v", patched, err)
	}

	// manager.value sub-path (string value).
	patched, err = ApplyPatch(base, PatchBody{Operations: []PatchOp{
		{Op: "replace", Path: urn + ":manager.value", Value: json.RawMessage(`"m-9"`)},
	}})
	if err != nil || patched.Manager != "m-9" {
		t.Fatalf("manager.value patch = %+v err=%v", patched, err)
	}

	// Whole-extension REPLACE by path replaces the whole value (RFC 7644 §3.5.2.3):
	// the unmentioned department must be cleared, not carried over from base.
	patched, err = ApplyPatch(base, PatchBody{Operations: []PatchOp{
		{Op: "replace", Path: urn, Value: json.RawMessage(`{"employeeNumber":"E2","manager":{"value":"m-2"}}`)},
	}})
	if err != nil || patched.EmployeeNumber != "E2" || patched.Manager != "m-2" {
		t.Fatalf("whole-ext patch = %+v err=%v", patched, err)
	}
	if patched.Department != "" {
		t.Errorf("whole-ext replace left stale department=%q; a replace must drop unmentioned sub-attributes", patched.Department)
	}

	// Whole-extension ADD by path MERGES (department survives).
	added, aerr := ApplyPatch(base, PatchBody{Operations: []PatchOp{
		{Op: "add", Path: urn, Value: json.RawMessage(`{"employeeNumber":"E3"}`)},
	}})
	if aerr != nil || added.EmployeeNumber != "E3" || added.Department != "Eng" {
		t.Errorf("whole-ext add should merge (keep department): %+v err=%v", added, aerr)
	}

	// Remove the whole extension clears every extension attribute.
	patched, err = ApplyPatch(base, PatchBody{Operations: []PatchOp{
		{Op: "remove", Path: urn},
	}})
	if err != nil || patched.EmployeeNumber != "" || patched.Department != "" || patched.Manager != "" {
		t.Fatalf("remove-ext = %+v err=%v", patched, err)
	}
}
