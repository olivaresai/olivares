// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"fmt"
	"net/http"
	"reflect"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/internal/oastest"
)

// PATCH /workspaces/{ref} must accept the dlp_mode that POST /workspaces accepts,
// with the same validation, and the change must be what the next read returns.
func TestPatchWorkspaceDLPModeRoundTrip(t *testing.T) {
	h := newHarness(t, New())
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "dlp-patch")
	other := h.createOrg(admin, "dlp-patch-other")
	viewer := h.viewerToken(admin, tenant, "dlp-viewer@example.invalid")
	editor := dlpPatchToken(h, admin, tenant, "dlp-editor@example.invalid", auth.RoleEditor)
	tools := t.TempDir()
	created := h.doJSON("POST", "/v1/m/sessions/workspaces", admin, map[string]any{
		"root_path": t.TempDir(), "dlp_mode": "label", "read_only_folders": []string{tools},
	}, tenantHdr(tenant))
	if created.code != http.StatusCreated {
		t.Fatalf("register=%d %s", created.code, created.raw)
	}
	ref := created.body["workspace_ref"].(string)
	path := "/v1/m/sessions/workspaces/" + ref
	read := func() resp { return h.do("GET", path, admin, tenantHdr(tenant)) }

	// The value POST accepted is accepted by PATCH; the next GET shows it and the
	// settings the request did not name are untouched.
	for _, mode := range []string{"deny", "off", "label"} {
		if r := h.doJSON("PATCH", path, admin, map[string]any{"dlp_mode": mode}, tenantHdr(tenant)); r.code != http.StatusOK || r.body["dlp_mode"] != mode {
			t.Fatalf("PATCH dlp_mode=%q -> %d %s", mode, r.code, r.raw)
		}
		got := read()
		if got.code != http.StatusOK || got.body["dlp_mode"] != mode {
			t.Fatalf("GET after PATCH dlp_mode=%q -> %d %s", mode, got.code, got.raw)
		}
		if folders, _ := got.body["read_only_folders"].([]any); len(folders) != 1 || folders[0] != tools {
			t.Fatalf("PATCH dlp_mode dropped read_only_folders: %s", got.raw)
		}
	}

	// An empty string is POST's "use the default" (off): same validation, same result.
	if r := h.doJSON("PATCH", path, admin, map[string]any{"dlp_mode": ""}, tenantHdr(tenant)); r.code != http.StatusOK || r.body["dlp_mode"] != "off" {
		t.Fatalf("PATCH dlp_mode=\"\" -> %d %s, want 200 and off", r.code, r.raw)
	}
	// dlp_mode and read_only_folders can change in one request.
	both := h.doJSON("PATCH", path, admin, map[string]any{"dlp_mode": "deny", "read_only_folders": []string{}}, tenantHdr(tenant))
	if folders, _ := both.body["read_only_folders"].([]any); both.code != http.StatusOK || both.body["dlp_mode"] != "deny" || len(folders) != 0 {
		t.Fatalf("PATCH both -> %d %s", both.code, both.raw)
	}
	// A null setting is not changed, whichever field carries it.
	if r := h.doJSON("PATCH", path, admin, map[string]any{"dlp_mode": nil, "read_only_folders": []string{tools}}, tenantHdr(tenant)); r.code != http.StatusOK || r.body["dlp_mode"] != "deny" {
		t.Fatalf("PATCH dlp_mode=null with folders -> %d %s, want 200 and deny kept", r.code, r.raw)
	}
	if r := h.doJSON("PATCH", path, admin, map[string]any{"dlp_mode": "off", "read_only_folders": nil}, tenantHdr(tenant)); r.code != http.StatusOK || r.body["dlp_mode"] != "off" {
		t.Fatalf("PATCH folders=null with dlp_mode -> %d %s", r.code, r.raw)
	} else if folders, _ := r.body["read_only_folders"].([]any); len(folders) != 1 || folders[0] != tools {
		t.Fatalf("null read_only_folders changed the folders: %s", r.raw)
	}

	// Refusals change nothing: a bad mode (POST says 400 to the same value), no
	// setting at all, a bad setting beside a good one (all or nothing), a role below
	// administrator, no token, and another organization.
	before := read().raw
	for _, tc := range []struct {
		name   string
		token  string
		hdr    map[string]string
		body   map[string]any
		status int
	}{
		{"unknown mode", admin, tenantHdr(tenant), map[string]any{"dlp_mode": "strict"}, http.StatusBadRequest},
		{"number", admin, tenantHdr(tenant), map[string]any{"dlp_mode": 3}, http.StatusBadRequest},
		{"empty body", admin, tenantHdr(tenant), map[string]any{}, http.StatusBadRequest},
		{"null only", admin, tenantHdr(tenant), map[string]any{"dlp_mode": nil}, http.StatusBadRequest},
		{"unknown setting", admin, tenantHdr(tenant), map[string]any{"mount_mode": "ro"}, http.StatusBadRequest},
		{"good mode, bad folder", admin, tenantHdr(tenant), map[string]any{"dlp_mode": "deny", "read_only_folders": []string{"relative"}}, http.StatusBadRequest},
		{"bad mode, good folders", admin, tenantHdr(tenant), map[string]any{"dlp_mode": "strict", "read_only_folders": []string{}}, http.StatusBadRequest},
		{"editor", editor, tenantHdr(tenant), map[string]any{"dlp_mode": "off"}, http.StatusForbidden},
		{"viewer", viewer, tenantHdr(tenant), map[string]any{"dlp_mode": "off"}, http.StatusForbidden},
		{"anonymous", "", tenantHdr(tenant), map[string]any{"dlp_mode": "off"}, http.StatusUnauthorized},
		{"other organization", admin, tenantHdr(other), map[string]any{"dlp_mode": "off"}, http.StatusNotFound},
	} {
		if r := h.doJSON("PATCH", path, tc.token, tc.body, tc.hdr); r.code != tc.status {
			t.Fatalf("%s: PATCH -> %d %s, want %d", tc.name, r.code, r.raw, tc.status)
		}
	}
	if after := read().raw; after != before {
		t.Fatalf("refused PATCH changed the workspace:\nbefore %s\nafter  %s", before, after)
	}
	// POST refuses the same bad value with the same status (one validation).
	if r := h.doJSON("POST", "/v1/m/sessions/workspaces", admin, map[string]any{"root_path": t.TempDir(), "dlp_mode": "strict"}, tenantHdr(tenant)); r.code != http.StatusBadRequest {
		t.Fatalf("POST dlp_mode=strict -> %d %s, want 400", r.code, r.raw)
	}
}

// A change of DLP posture is evidence: each accepted setting seals its own
// configure event in the verified chain. The event commits to the new value and
// states the new and the previous mode, so a downgrade is readable in the ledger.
func TestPatchWorkspaceDLPModeIsAudited(t *testing.T) {
	m := New()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "dlp-audit")
	root := t.TempDir()
	created := h.doJSON("POST", "/v1/m/sessions/workspaces", admin, map[string]any{"root_path": root, "dlp_mode": "deny"}, tenantHdr(tenant))
	if created.code != http.StatusCreated {
		t.Fatalf("register=%d %s", created.code, created.raw)
	}
	ref := created.body["workspace_ref"].(string)
	patch := func(body map[string]any) {
		t.Helper()
		if r := h.doJSON("PATCH", "/v1/m/sessions/workspaces/"+ref, admin, body, tenantHdr(tenant)); r.code != http.StatusOK {
			t.Fatalf("PATCH %v -> %d %s", body, r.code, r.raw)
		}
	}
	configured := func() []acctAuditedMeta {
		return acctAuditsOf(t, m, model.TenantID(tenant), "sessions.workspace.configure")
	}
	dlpEvent := func(events []acctAuditedMeta, mode, previous string) {
		t.Helper()
		want := sha256Hex([]byte(fmt.Sprintf(`{"dlp_mode":%q}`, mode)))
		for _, ev := range events {
			if ev.meta["sha256"] == want && ev.meta["dlp_mode"] == mode && ev.meta["previous_dlp_mode"] == previous && ev.meta["workspace_ref"] == ref {
				return
			}
		}
		t.Fatalf("no configure event commits to dlp_mode=%s after %s: %v", mode, previous, events)
	}

	// A DLP-only change seals exactly one event, with the downgrade named.
	patch(map[string]any{"dlp_mode": "off"})
	events := configured()
	if len(events) != 1 {
		t.Fatalf("dlp-only PATCH sealed %d configure events, want 1: %v", len(events), events)
	}
	dlpEvent(events, "off", "deny")
	// An empty string anchors the mode it selected, not the input.
	patch(map[string]any{"dlp_mode": ""})
	events = configured()
	if len(events) != 2 {
		t.Fatalf("second PATCH sealed %d configure events in total, want 2", len(events))
	}
	dlpEvent(events, "off", "off")
	// Both settings in one request: one event each; the folders event stays the
	// bare folder-list anchor it always was.
	patch(map[string]any{"dlp_mode": "label", "read_only_folders": []string{}})
	events = configured()
	if len(events) != 4 {
		t.Fatalf("combined PATCH sealed %d configure events, want 2", len(events)-2)
	}
	dlpEvent(events, "label", "off")
	foldersAnchor := sha256Hex([]byte("[]"))
	if events[2].meta["sha256"] != foldersAnchor && events[3].meta["sha256"] != foldersAnchor {
		t.Fatalf("combined PATCH left no folder-list event: %v", events[2:])
	}
	// The chain verifies end to end.
	want := workspacePayloadHash(wsMutationInput{op: "configure", workspaceRef: ref, path: root, contentHash: sha256Hex([]byte(`{"dlp_mode":"label"}`))})
	if err := h.st.View(t.Context(), model.TenantID(tenant), func(sc store.Scope) error {
		found := false
		if err := sc.Audit().Walk(t.Context(), 0, func(ev model.AuditEvent) error {
			found = found || (ev.Action == "sessions.workspace.configure" && bytes.Equal(ev.PayloadHash, want[:]))
			return nil
		}); err != nil {
			return err
		}
		if !found {
			t.Errorf("no event carries the payload hash of dlp_mode=label")
		}
		rep, err := sc.Audit().Verify(t.Context(), 0)
		if err == nil && !rep.OK {
			t.Errorf("audit chain broken: %s", rep.Reason)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// A refused transaction leaves neither the new mode nor an audit event.
func TestPatchWorkspaceDLPModeRollsBackWithItsAudit(t *testing.T) {
	m := New()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "dlp-rollback")
	created := h.doJSON("POST", "/v1/m/sessions/workspaces", admin, map[string]any{"root_path": t.TempDir(), "dlp_mode": "label"}, tenantHdr(tenant))
	if created.code != http.StatusCreated {
		t.Fatalf("register=%d %s", created.code, created.raw)
	}
	ref := created.body["workspace_ref"].(string)
	original := m.Data
	m.Data = metadataRollbackData{original}
	r := h.doJSON("PATCH", "/v1/m/sessions/workspaces/"+ref, admin, map[string]any{"dlp_mode": "off", "read_only_folders": []string{t.TempDir()}}, tenantHdr(tenant))
	m.Data = original
	if r.code < 400 {
		t.Fatalf("refused transaction succeeded=%d %s", r.code, r.raw)
	}
	stored := h.do("GET", "/v1/m/sessions/workspaces/"+ref, admin, tenantHdr(tenant))
	if folders, _ := stored.body["read_only_folders"].([]any); stored.code != http.StatusOK || stored.body["dlp_mode"] != "label" || len(folders) != 0 {
		t.Fatalf("refused transaction left a new setting: %s", stored.raw)
	}
	if events := acctAuditsOf(t, m, model.TenantID(tenant), "sessions.workspace.configure"); len(events) != 0 {
		t.Fatalf("refused transaction left %d configuration audits", len(events))
	}
}

// What the PATCH schema publishes is what the route does: the same dlp_mode values
// as registration, no required setting, a closed body that wants at least one.
func TestPatchWorkspaceMatchesItsOpenAPISchema(t *testing.T) {
	m := New()
	schemaOf := func(method, path string) map[string]any {
		op := oastest.Op(t, m, method, path)
		body := oastest.Map(t, op["requestBody"], "request body")
		content := oastest.Map(t, body["content"], "content")
		jsonBody := oastest.Map(t, content["application/json"], "JSON body")
		return oastest.Map(t, jsonBody["schema"], "schema")
	}
	enumOf := func(schema map[string]any) []any {
		props := oastest.Map(t, schema["properties"], "properties")
		dlp := oastest.Map(t, props["dlp_mode"], "dlp_mode")
		str := oastest.Map(t, dlp["anyOf"].([]any)[0], "DLP string")
		return str["enum"].([]any)
	}
	patch, post := schemaOf("patch", "/v1/m/sessions/workspaces/{ref}"), schemaOf("post", "/v1/m/sessions/workspaces")

	if patch["additionalProperties"] != false || fmt.Sprint(patch["minProperties"]) != "1" {
		t.Fatalf("PATCH body is not a closed object that wants one setting: %v", patch)
	}
	if _, required := patch["required"]; required {
		t.Fatalf("PATCH body requires a setting: %v", patch["required"])
	}
	if got := oastest.Keys(oastest.Map(t, patch["properties"], "properties")); !reflect.DeepEqual(got, []string{"backup", "data_class", "default_permission", "dlp_mode", "read_only_folders"}) {
		t.Fatalf("PATCH properties = %v, want backup, data_class, default_permission, dlp_mode and read_only_folders", got)
	}
	if !reflect.DeepEqual(enumOf(patch), enumOf(post)) {
		t.Fatalf("PATCH dlp_mode enum %v differs from registration's %v", enumOf(patch), enumOf(post))
	}

	// Every published value is accepted at runtime.
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "dlp-patch-schema")
	created := h.doJSON("POST", "/v1/m/sessions/workspaces", admin, map[string]any{"root_path": t.TempDir()}, tenantHdr(tenant))
	if created.code != http.StatusCreated {
		t.Fatalf("register=%d %s", created.code, created.raw)
	}
	for _, mode := range enumOf(patch) {
		if r := h.doJSON("PATCH", "/v1/m/sessions/workspaces/"+created.body["workspace_ref"].(string), admin, map[string]any{"dlp_mode": mode}, tenantHdr(tenant)); r.code != http.StatusOK {
			t.Fatalf("published dlp_mode %q -> %d %s", mode, r.code, r.raw)
		}
	}
	// So is every published data_class, the empty undeclared value included.
	classProps := oastest.Map(t, patch["properties"], "properties")
	class := oastest.Map(t, classProps["data_class"], "data_class")
	publishedClasses := oastest.Map(t, class["anyOf"].([]any)[0], "data_class string")["enum"].([]any)
	if len(publishedClasses) != len(dataClassValues) {
		t.Fatalf("published data_class values = %v, want %v", publishedClasses, dataClassValues)
	}
	for _, value := range publishedClasses {
		if r := h.doJSON("PATCH", "/v1/m/sessions/workspaces/"+created.body["workspace_ref"].(string), admin, map[string]any{"data_class": value}, tenantHdr(tenant)); r.code != http.StatusOK {
			t.Fatalf("published data_class %q -> %d %s", value, r.code, r.raw)
		}
	}
	// So is every published default_permission: the contract lists exactly what
	// ResolvePermission accepts, including labels, older phrases and the empty string.
	props := oastest.Map(t, patch["properties"], "properties")
	perm := oastest.Map(t, props["default_permission"], "default_permission")
	published := oastest.Map(t, perm["anyOf"].([]any)[0], "default_permission string")["enum"].([]any)
	wantPublished := map[string]bool{"": true}
	for _, n := range PermissionNames() {
		wantPublished[n.Name] = true
		wantPublished[n.Label] = true
		for _, alias := range n.Aliases {
			wantPublished[alias] = true
		}
	}
	gotPublished := map[string]bool{}
	for _, v := range published {
		gotPublished[v.(string)] = true
	}
	if !reflect.DeepEqual(gotPublished, wantPublished) || len(published) != len(wantPublished) {
		t.Fatalf("published default_permission values = %v, want empty and every accepted name, label and alias: %v", published, wantPublished)
	}
	for _, name := range published {
		if r := h.doJSON("PATCH", "/v1/m/sessions/workspaces/"+created.body["workspace_ref"].(string), admin, map[string]any{"default_permission": name}, tenantHdr(tenant)); r.code != http.StatusOK {
			t.Fatalf("published default_permission %q -> %d %s", name, r.code, r.raw)
		}
	}
}

func dlpPatchToken(h *harness, admin string, tenant model.TenantID, email, role string) string {
	h.t.Helper()
	r := h.doJSON("POST", "/v1/users", admin, map[string]any{"email": email, "password": "memberpass1", "tenant": tenant.String(), "role": role}, nil)
	if r.code != http.StatusCreated {
		h.t.Fatalf("user %s = %d %s", email, r.code, r.raw)
	}
	r = h.doJSON("POST", "/v1/auth/login", "", map[string]any{"email": email, "password": "memberpass1"}, nil)
	if r.code != http.StatusOK {
		h.t.Fatalf("login %s = %d %s", email, r.code, r.raw)
	}
	return r.body["token"].(string)
}
