// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// newTemplateHarness creates a module, store, and tenant for template tests.
func newTemplateHarness(t *testing.T) (*Module, store.Store, model.TenantID) {
	t.Helper()
	m, st, tenant, _ := newRuntimeHarness(t)
	return m, st, tenant
}

func TestTemplateCreate(t *testing.T) {
	m := New()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	hdr := tenantHdr(tenant)

	r := h.doJSON("POST", "/v1/m/sessions/templates", admin, map[string]any{
		"name":        "My Template",
		"description": "A test template",
		"body": map[string]any{
			"settings": map[string]any{"effort": "high"},
		},
	}, hdr)
	if r.code != http.StatusCreated {
		t.Fatalf("create template = %d %s", r.code, r.raw)
	}
	if r.body["name"] != "My Template" {
		t.Errorf("name = %v, want My Template", r.body["name"])
	}
	if r.body["builtin"] != false {
		t.Errorf("builtin = %v, want false", r.body["builtin"])
	}
	ver, _ := r.body["version"].(float64)
	if ver != 1 {
		t.Errorf("version = %v, want 1", ver)
	}
	if r.body["id"] == "" {
		t.Error("id is empty")
	}
}

func TestTemplateCreate_NameRequired(t *testing.T) {
	m := New()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	r := h.doJSON("POST", "/v1/m/sessions/templates", admin, map[string]any{
		"description": "No name",
	}, tenantHdr(tenant))
	if r.code != http.StatusBadRequest {
		t.Fatalf("create without name = %d, want 400", r.code)
	}
}

func TestTemplateGet(t *testing.T) {
	m := New()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	hdr := tenantHdr(tenant)

	cr := h.doJSON("POST", "/v1/m/sessions/templates", admin, map[string]any{
		"name": "Get Me",
	}, hdr)
	if cr.code != http.StatusCreated {
		t.Fatalf("create = %d %s", cr.code, cr.raw)
	}
	id := cr.body["id"].(string)

	r := h.do("GET", "/v1/m/sessions/templates/"+id, admin, hdr)
	if r.code != http.StatusOK {
		t.Fatalf("get = %d %s", r.code, r.raw)
	}
	if r.body["name"] != "Get Me" {
		t.Errorf("name = %v, want Get Me", r.body["name"])
	}
}

func TestTemplateUpdate_BumpsVersion(t *testing.T) {
	m := New()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	hdr := tenantHdr(tenant)

	cr := h.doJSON("POST", "/v1/m/sessions/templates", admin, map[string]any{
		"name": "Updatable",
	}, hdr)
	if cr.code != http.StatusCreated {
		t.Fatalf("create = %d %s", cr.code, cr.raw)
	}
	id := cr.body["id"].(string)

	newName := "Updated Name"
	r := h.doJSON("PUT", "/v1/m/sessions/templates/"+id, admin, map[string]any{
		"name": &newName,
	}, hdr)
	if r.code != http.StatusOK {
		t.Fatalf("update = %d %s", r.code, r.raw)
	}
	if r.body["name"] != "Updated Name" {
		t.Errorf("name = %v, want Updated Name", r.body["name"])
	}
	ver, _ := r.body["version"].(float64)
	if ver != 2 {
		t.Errorf("version = %v, want 2", ver)
	}
}

func TestTemplateUpdate_BlocksBuiltin(t *testing.T) {
	m := New()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	hdr := tenantHdr(tenant)

	// Trigger lazy seeding by listing with builtin=true.
	r := h.do("GET", "/v1/m/sessions/templates?builtin=true", admin, hdr)
	if r.code != http.StatusOK {
		t.Fatalf("list builtins = %d %s", r.code, r.raw)
	}
	items := r.body["items"].([]any)
	if len(items) == 0 {
		t.Fatal("no built-in templates after seeding")
	}
	builtinID := items[0].(map[string]any)["id"].(string)

	// Try to update a built-in template.
	newDesc := "Hacked description"
	r = h.doJSON("PUT", "/v1/m/sessions/templates/"+builtinID, admin, map[string]any{
		"description": &newDesc,
	}, hdr)
	if r.code != http.StatusForbidden {
		t.Fatalf("update builtin = %d, want 403: %s", r.code, r.raw)
	}
}

func TestTemplateDelete_SoftArchives(t *testing.T) {
	m := New()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	hdr := tenantHdr(tenant)

	cr := h.doJSON("POST", "/v1/m/sessions/templates", admin, map[string]any{
		"name": "Deletable",
	}, hdr)
	if cr.code != http.StatusCreated {
		t.Fatalf("create = %d %s", cr.code, cr.raw)
	}
	id := cr.body["id"].(string)

	r := h.do("DELETE", "/v1/m/sessions/templates/"+id, admin, hdr)
	if r.code != http.StatusNoContent {
		t.Fatalf("delete = %d %s", r.code, r.raw)
	}

	// Verify archived_at is set by re-reading.
	r = h.do("GET", "/v1/m/sessions/templates/"+id, admin, hdr)
	if r.code != http.StatusOK {
		t.Fatalf("get after delete = %d %s", r.code, r.raw)
	}
	if r.body["archived_at"] == nil || r.body["archived_at"] == "" {
		t.Error("archived_at should be set after delete")
	}
}

func TestTemplateDelete_BlocksBuiltin(t *testing.T) {
	m := New()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	hdr := tenantHdr(tenant)

	// Trigger lazy seeding by listing with builtin=true.
	r := h.do("GET", "/v1/m/sessions/templates?builtin=true", admin, hdr)
	if r.code != http.StatusOK {
		t.Fatalf("list builtins = %d %s", r.code, r.raw)
	}
	items := r.body["items"].([]any)
	if len(items) == 0 {
		t.Fatal("no built-in templates after seeding")
	}
	builtinID := items[0].(map[string]any)["id"].(string)

	r = h.do("DELETE", "/v1/m/sessions/templates/"+builtinID, admin, hdr)
	if r.code != http.StatusForbidden {
		t.Fatalf("delete builtin = %d, want 403: %s", r.code, r.raw)
	}
}

func TestTemplateDuplicate(t *testing.T) {
	m := New()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	hdr := tenantHdr(tenant)

	cr := h.doJSON("POST", "/v1/m/sessions/templates", admin, map[string]any{
		"name":        "Original",
		"description": "The original",
		"body": map[string]any{
			"settings": map[string]any{"effort": "max"},
		},
	}, hdr)
	if cr.code != http.StatusCreated {
		t.Fatalf("create = %d %s", cr.code, cr.raw)
	}
	id := cr.body["id"].(string)

	r := h.doJSON("POST", "/v1/m/sessions/templates/"+id+"/duplicate", admin, map[string]any{
		"name": "Copy of Original",
	}, hdr)
	if r.code != http.StatusCreated {
		t.Fatalf("duplicate = %d %s", r.code, r.raw)
	}
	if r.body["name"] != "Copy of Original" {
		t.Errorf("name = %v, want Copy of Original", r.body["name"])
	}
	if r.body["builtin"] != false {
		t.Errorf("duplicated template should not be builtin")
	}
	ver, _ := r.body["version"].(float64)
	if ver != 1 {
		t.Errorf("version = %v, want 1", ver)
	}
	if r.body["description"] != "The original" {
		t.Errorf("description not carried over: %v", r.body["description"])
	}
}

func TestTemplateList_FilterBuiltin(t *testing.T) {
	m := New()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	hdr := tenantHdr(tenant)

	// Create a custom template (the list endpoint seeds builtins lazily).
	h.doJSON("POST", "/v1/m/sessions/templates", admin, map[string]any{
		"name": "Custom One",
	}, hdr)

	// List all (includes builtins + 1 custom).
	r := h.do("GET", "/v1/m/sessions/templates", admin, hdr)
	if r.code != http.StatusOK {
		t.Fatalf("list all = %d %s", r.code, r.raw)
	}
	allItems := r.body["items"].([]any)
	if want := len(builtinTemplates) + 1; len(allItems) != want {
		t.Fatalf("all templates = %d, want %d (builtins + 1 custom)", len(allItems), want)
	}

	// Filter by builtin=true.
	r = h.do("GET", "/v1/m/sessions/templates?builtin=true", admin, hdr)
	if r.code != http.StatusOK {
		t.Fatalf("list builtin = %d %s", r.code, r.raw)
	}
	builtinItems := r.body["items"].([]any)
	if want := len(builtinTemplates); len(builtinItems) != want {
		t.Errorf("builtin items = %d, want %d", len(builtinItems), want)
	}

	// Filter by builtin=false.
	r = h.do("GET", "/v1/m/sessions/templates?builtin=false", admin, hdr)
	if r.code != http.StatusOK {
		t.Fatalf("list custom = %d %s", r.code, r.raw)
	}
	customItems := r.body["items"].([]any)
	if len(customItems) != 1 {
		t.Errorf("custom items = %d, want 1", len(customItems))
	}
}

func TestTemplateList_ExcludesArchived(t *testing.T) {
	m := New()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	hdr := tenantHdr(tenant)

	// Create and archive a template.
	cr := h.doJSON("POST", "/v1/m/sessions/templates", admin, map[string]any{
		"name": "Will Archive",
	}, hdr)
	if cr.code != http.StatusCreated {
		t.Fatalf("create = %d %s", cr.code, cr.raw)
	}
	id := cr.body["id"].(string)
	h.do("DELETE", "/v1/m/sessions/templates/"+id, admin, hdr)

	// List without include_archived: should not find "Will Archive" in custom.
	r := h.do("GET", "/v1/m/sessions/templates?builtin=false", admin, hdr)
	if r.code != http.StatusOK {
		t.Fatalf("list = %d %s", r.code, r.raw)
	}
	for _, item := range r.body["items"].([]any) {
		m := item.(map[string]any)
		if m["name"] == "Will Archive" {
			t.Error("archived template should be excluded from default list")
		}
	}

	// List with include_archived=true: should find it.
	r = h.do("GET", "/v1/m/sessions/templates?builtin=false&include_archived=true", admin, hdr)
	if r.code != http.StatusOK {
		t.Fatalf("list archived = %d %s", r.code, r.raw)
	}
	found := false
	for _, item := range r.body["items"].([]any) {
		m := item.(map[string]any)
		if m["name"] == "Will Archive" {
			found = true
		}
	}
	if !found {
		t.Error("archived template should appear with include_archived=true")
	}
}

func TestTemplateApply(t *testing.T) {
	m := New()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	hdr := tenantHdr(tenant)

	// This template declares CONNECTORS, which no launch binds. Until the endpoint
	// answered `applied:true, conflicts:[]` for it — and for everything else, because
	// both fields were constants. It now reports what is true of THIS template.
	cr := h.doJSON("POST", "/v1/m/sessions/templates", admin, map[string]any{
		"name": "Apply Me",
		"body": map[string]any{
			"settings":   map[string]any{"effort": "high"},
			"connectors": []string{"github", "jira"},
		},
	}, hdr)
	if cr.code != http.StatusCreated {
		t.Fatalf("create = %d %s", cr.code, cr.raw)
	}
	id := cr.body["id"].(string)

	r := h.doJSON("POST", "/v1/m/sessions/templates/"+id+"/apply", admin, nil, hdr)
	if r.code != http.StatusOK {
		t.Fatalf("apply = %d %s", r.code, r.raw)
	}
	if r.body["applied"] != false {
		t.Errorf("a template declaring connectors cannot be applied; applied = %v", r.body["applied"])
	}
	un, _ := r.body["unenforceable"].([]any)
	if len(un) == 0 || !strings.Contains(un[0].(string), "connectors") {
		t.Errorf("the response must name the field it cannot keep: %v", r.body["unenforceable"])
	}
	tpl, ok := r.body["template"].(map[string]any)
	if !ok || tpl["name"] != "Apply Me" {
		t.Errorf("template not returned in apply response: %v", r.body)
	}
}

// TestTemplateApply_RefusesWhatTheLaunchWouldRefuse: a preview that promises a
// configuration the very next call rejects is worse than no preview.
func TestTemplateApply_RefusesWhatTheLaunchWouldRefuse(t *testing.T) {
	m := New()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	hdr := tenantHdr(tenant)

	cr := h.doJSON("POST", "/v1/m/sessions/templates", admin, map[string]any{
		"name": "Retired",
		"body": map[string]any{
			"settings": map[string]any{"effort": "high"},
			"policies": map[string]any{"require_read_only_folders": true},
		},
	}, hdr)
	if cr.code != http.StatusCreated {
		t.Fatalf("create = %d %s", cr.code, cr.raw)
	}
	id := cr.body["id"].(string)

	// No-fire first: while it is live, the preview applies it.
	r := h.doJSON("POST", "/v1/m/sessions/templates/"+id+"/apply", admin, nil, hdr)
	if r.code != http.StatusOK || r.body["applied"] != true {
		t.Fatalf("a live template must preview as applicable: %d %s", r.code, r.raw)
	}

	if dr := h.do("DELETE", "/v1/m/sessions/templates/"+id, admin, hdr); dr.code != http.StatusNoContent {
		t.Fatalf("archive = %d %s", dr.code, dr.raw)
	}
	r = h.doJSON("POST", "/v1/m/sessions/templates/"+id+"/apply", admin, nil, hdr)
	if r.code != http.StatusOK {
		t.Fatalf("apply = %d %s", r.code, r.raw)
	}
	if r.body["applied"] != false {
		t.Errorf("an archived template cannot govern a launch, so the preview must not say applied: %v", r.body)
	}
	un, _ := r.body["unenforceable"].([]any)
	if len(un) == 0 || !strings.Contains(un[0].(string), "archived") {
		t.Errorf("the preview must say WHY: %v", r.body["unenforceable"])
	}
}

// TestTemplateCreate_RejectsAnUnknownKey: the body is decoded into a typed struct and
// marshaled straight back to storage, so a misspelled policy key used to vanish on the
// way in and leave a template that looked authored and governed nothing.
func TestTemplateCreate_RejectsAnUnknownKey(t *testing.T) {
	m := New()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	hdr := tenantHdr(tenant)

	r := h.doJSON("POST", "/v1/m/sessions/templates", admin, map[string]any{
		"name": "Typo",
		"body": map[string]any{
			"policies": map[string]any{"allowed_tool": []string{"Read"}}, // singular: a typo
		},
	}, hdr)
	if r.code != http.StatusBadRequest {
		t.Fatalf("a misspelled policy key must be rejected, not dropped: %d %s", r.code, r.raw)
	}
	// No-fire: the correctly-spelled key is accepted, so this is about the typo and not
	// about rejecting policies in general.
	ok := h.doJSON("POST", "/v1/m/sessions/templates", admin, map[string]any{
		"name": "Correct",
		"body": map[string]any{
			"policies": map[string]any{"allowed_tools": []string{"Read"}},
		},
	}, hdr)
	if ok.code != http.StatusCreated {
		t.Fatalf("the correct key must be accepted: %d %s", ok.code, ok.raw)
	}
}

// TestTemplateApply_RealMergeAndRealConflicts is the no-fire direction of the test
// above, and the one that would have caught the original defect: an ENFORCEABLE
// template applies, and a target that contradicts it produces a conflict that names the
// field. A handler hard-coding `applied:false` passes the test above; nothing passes
// both.
func TestTemplateApply_RealMergeAndRealConflicts(t *testing.T) {
	m := New()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	hdr := tenantHdr(tenant)

	cr := h.doJSON("POST", "/v1/m/sessions/templates", admin, map[string]any{
		"name": "Read Only",
		"body": map[string]any{
			"settings": map[string]any{"effort": "high", "permission_mode": permModeDontAsk},
			"policies": map[string]any{"allowed_tools": []string{"Read"}},
		},
	}, hdr)
	if cr.code != http.StatusCreated {
		t.Fatalf("create = %d %s", cr.code, cr.raw)
	}
	id := cr.body["id"].(string)

	// No target: the template is applicable and contradicts nothing.
	r := h.doJSON("POST", "/v1/m/sessions/templates/"+id+"/apply", admin, nil, hdr)
	if r.code != http.StatusOK || r.body["applied"] != true {
		t.Fatalf("apply = %d %s", r.code, r.raw)
	}
	if got, _ := r.body["conflicts"].([]any); len(got) != 0 {
		t.Errorf("no target chose anything, so nothing is contradicted; got %v", got)
	}
	merged, ok := r.body["merged"].(map[string]any)
	if !ok || merged["permission_mode"] != permModeDontAsk {
		t.Errorf("merged configuration not returned: %v", r.body["merged"])
	}

	// A target that contradicts the template on a field it CHOSE.
	r = h.doJSON("POST", "/v1/m/sessions/templates/"+id+"/apply", admin, map[string]any{
		"target": map[string]any{"effort": "low"},
	}, hdr)
	if r.code != http.StatusOK {
		t.Fatalf("apply = %d %s", r.code, r.raw)
	}
	conflicts, _ := r.body["conflicts"].([]any)
	if len(conflicts) != 1 {
		t.Fatalf("conflicts = %v, want exactly the one the target caused", conflicts)
	}
	c := conflicts[0].(map[string]any)
	if c["field"] != "effort" || c["old_value"] != "low" || c["new_value"] != "high" {
		t.Errorf("conflict does not describe the real contradiction: %v", c)
	}
}

func TestSeedBuiltins_Idempotent(t *testing.T) {
	t.Parallel()

	m, _, tenant := newTemplateHarness(t)
	ctx := context.Background()

	// Seed twice (reset the sync.Map to force re-seed).
	seededTenants.Delete(tenant)
	if err := m.seedBuiltins(ctx, tenant); err != nil {
		t.Fatalf("first seed: %v", err)
	}

	// Count.
	count1 := countBuiltins(t, m, ctx, tenant)

	// Seed again.
	if err := m.seedBuiltins(ctx, tenant); err != nil {
		t.Fatalf("second seed: %v", err)
	}
	count2 := countBuiltins(t, m, ctx, tenant)

	if count1 != count2 || count1 != len(builtinTemplates) {
		t.Errorf("builtins count: seed1=%d seed2=%d want=%d", count1, count2, len(builtinTemplates))
	}
}

func TestSeedBuiltins_UpdatesContent(t *testing.T) {
	t.Parallel()

	m, _, tenant := newTemplateHarness(t)
	ctx := context.Background()

	seededTenants.Delete(tenant)
	if err := m.seedBuiltins(ctx, tenant); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Verify a known builtin's body contains expected settings.
	var body string
	_ = m.Data.View(ctx, tenant, func(sc store.Scope) error {
		repo, _ := sc.Ext(templateKind)
		recs, _, _ := repo.List(ctx, model.Query{
			Filters: []model.Filter{
				eq(colTplName, "Code Review"),
				{Column: colTplBuiltin, Op: model.OpEq, Value: true},
			},
			Limit: 1,
		})
		if len(recs) > 0 {
			body = recs[0].String(colTplBody)
		}
		return nil
	})
	if body == "" {
		t.Fatal("Code Review builtin body is empty")
	}
	var parsed tplBody
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if parsed.Settings == nil || parsed.Settings.Effort != "high" {
		t.Errorf("Code Review settings.effort = %v, want high", parsed.Settings)
	}
}

func countBuiltins(t *testing.T, m *Module, ctx context.Context, tenant model.TenantID) int {
	t.Helper()
	var count int
	_ = m.Data.View(ctx, tenant, func(sc store.Scope) error {
		repo, _ := sc.Ext(templateKind)
		recs, _, _ := repo.List(ctx, model.Query{
			Filters: []model.Filter{{Column: colTplBuiltin, Op: model.OpEq, Value: true}},
			Limit:   100,
		})
		count = len(recs)
		return nil
	})
	return count
}

// Course presets use the ordinary template merge and never widen a plan launch.
func TestAIASPresets(t *testing.T) {
	for _, tc := range []struct {
		name, mode                  string
		disabled, recording, strict bool
	}{
		{"AIAS 1 — No AI", "", true, false, false},
		{"AIAS 2 — Plan only tutor", "plan", false, false, true},
		{"AIAS 3 — Scratch practice", "acceptEdits", false, false, true},
		{"AIAS 4 — AI with review", "acceptEdits", false, true, false},
		{"AIAS 5 — Full AI", "bypassPermissions", false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &fakeRunner{}
			m, _, tenant, _ := newRuntimeHarness(t, WithRunner(r), WithCredentialSource(staticCred()), WithConfinement([]string{t.TempDir()}, false))
			var body *tplBody
			for i := range builtinTemplates {
				if builtinTemplates[i].name == tc.name {
					body = &builtinTemplates[i].body
					break
				}
			}
			if body == nil {
				t.Fatal("missing course preset")
			}
			id := seedTemplate(t, m, tenant, tc.name, *body)
			if tc.name == "AIAS 3 — Scratch practice" {
				_, err := createProfiledTestRun(t, m, t.Context(), tenant, CreateRunParams{TemplateID: id, Actor: actorU, ActorKind: actorKindU})
				if err == nil || !strings.Contains(err.Error(), "read_only_folders") {
					t.Fatalf("unconfigured scratch launch: %v", err)
				}
			}
			course, scratch := t.TempDir(), t.TempDir()
			folder := course
			var references []string
			if tc.name == "AIAS 3 — Scratch practice" {
				folder, references = scratch, []string{course}
			}
			ws := mkWorkspace(t, m, tenant, CreateWorkspaceParams{RootPath: folder, ReadOnlyFolders: references})
			run, err := createProfiledTestRun(t, m, t.Context(), tenant, CreateRunParams{TemplateID: id, WorkspaceRef: ws.WorkspaceRef, Transport: TransportStreamJSON, PermissionMode: "bypassPermissions", MayRunUnrestricted: true, Actor: actorU, ActorKind: actorKindU})
			if tc.disabled {
				if err == nil || !strings.Contains(err.Error(), "sessions are disabled") {
					t.Fatalf("no-AI launch: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			spec := r.lastSpec()
			if mode, _ := argvFlag(spec.Args, "--permission-mode"); mode != tc.mode {
				t.Fatalf("mode=%q want=%q", mode, tc.mode)
			}
			if run.RecordIO != tc.recording || spec.ConfinementRequireTruncateProtection != tc.strict {
				t.Fatalf("recording=%v strict=%v", run.RecordIO, spec.ConfinementRequireTruncateProtection)
			}
			if tc.mode == "plan" && (spec.Confinement == nil || len(spec.Confinement.Sealed) == 0 || !spec.ConfinementRequired) {
				t.Fatal("tutor folder is not confined read-only")
			}
			if len(references) > 0 {
				if spec.Dir != scratch || len(spec.ConfinementFiles) != 1 || !spec.ConfinementRequired {
					t.Fatal("scratch workspace did not carry the read-only course boundary")
				}
				for _, path := range spec.Confinement.ReadWrite {
					if pathsOverlap(path, course) {
						t.Fatal("course folder received a writable grant")
					}
				}
			}
			if !strings.Contains(body.Settings.CustomInstructions, "final answer") {
				t.Fatal("missing assessment instructions")
			}
		})
	}
}

// Ownership is a current responsibility; authorship remains historical evidence.
func TestOwnershipTransferPreservesTemplateAuthorship(t *testing.T) {
	m := New()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "ownership")
	created := h.doJSON("POST", "/v1/m/sessions/templates", admin, map[string]any{"name": "Owned template"}, tenantHdr(tenant))
	if created.code != http.StatusCreated {
		t.Fatalf("create: %d %s", created.code, created.raw)
	}
	if created.body["owner"] == nil || created.body["owner"] != created.body["author"] {
		t.Fatalf("current ownership is missing: %s", created.raw)
	}
	ctx := context.Background()
	source, err := model.ParseID(strings.TrimPrefix(created.body["author"].(string), "user:"))
	if err != nil {
		t.Fatal(err)
	}
	manager := h.userToken(admin, tenant, "manager@ownership.test", auth.RoleAdmin)
	who := h.do("GET", "/v1/auth/whoami", manager, nil)
	target, err := model.ParseID(who.body["user_id"].(string))
	if err != nil {
		t.Fatal(err)
	}
	m.WorkIdentity = allowWorkIdentity{}
	WithWorkContentGuard(allowWorkContent{})(m)
	principal := WorkPrincipal{Actor: "user:" + source.String(), ActorKind: model.ActorUser, ActorRef: source.String(), Admin: true}
	var folder model.Resource
	var workspace model.ID
	if err := h.st.Mutate(ctx, tenant, func(sc store.Scope) error {
		ws, e := sc.DefaultWorkspace(ctx)
		if e != nil {
			return e
		}
		workspace = ws.ID
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := m.UsePersonalFilesRoot(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	personalRef, err := m.ensurePersonalFolder(ctx, tenant, auth.Principal{Kind: auth.KindUser, UserID: source})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(m.rt.personalFilesRoot, source.String())); !os.IsNotExist(err) {
		t.Fatalf("fresh allocation created the shared account-only path: %v", err)
	}

	if err := h.st.View(ctx, tenant, func(sc store.Scope) error {
		repo, e := sc.Ext(workspaceKind)
		if e != nil {
			return e
		}
		rows, _, e := repo.List(ctx, model.Query{Filters: []model.Filter{eq(colWsRef, personalRef)}, Limit: 1})
		if e != nil {
			return e
		}
		folder, e = sc.Resources().Get(ctx, recordID(rows[0]))
		return e
	}); err != nil {
		t.Fatal(err)
	}
	f := workFixture{m: m, st: h.st, tenant: tenant, workspace: workspace, principal: principal}
	for _, title := range []string{"First task", "Second task"} {
		applyCreate(t, f, title)
	}
	inventory, err := m.OwnershipInventory(ctx, tenant, source)
	if err != nil || len(inventory.Folders) != 1 || len(inventory.WorkItems) != 2 || len(inventory.Templates) != 1 {
		t.Fatalf("inventory: %+v, %v", inventory, err)
	}
	prepared, err := m.PrepareOwnershipTransfer(ctx, tenant, principal, source, target, inventory.Digest)
	if err != nil {
		t.Fatal(err)
	}
	// The caller's completion audit and all ownership writes share one transaction.
	sentinel := errors.New("completion audit failed")
	if err := h.st.Mutate(ctx, tenant, func(sc store.Scope) error {
		if e := m.ApplyOwnershipTransfer(ctx, tenant, sc, prepared); e != nil {
			return e
		}
		return sentinel
	}); !errors.Is(err, sentinel) {
		t.Fatalf("rollback: %v", err)
	}
	after, err := m.OwnershipInventory(ctx, tenant, source)
	if err != nil || after.Digest != inventory.Digest {
		t.Fatalf("partial transfer escaped rollback: %+v %v", after, err)
	}
	if err := h.st.Mutate(ctx, tenant, func(sc store.Scope) error { return m.ApplyOwnershipTransfer(ctx, tenant, sc, prepared) }); err != nil {
		t.Fatal(err)
	}
	targetInventory, err := m.OwnershipInventory(ctx, tenant, target)
	if err != nil || len(targetInventory.Folders) != 1 || len(targetInventory.WorkItems) != 2 || len(targetInventory.Templates) != 1 {
		t.Fatalf("recipient inventory: %+v %v", targetInventory, err)
	}
	got := h.do("GET", "/v1/m/sessions/templates/"+created.body["id"].(string), admin, tenantHdr(tenant))
	if got.code != 200 || got.body["author"] != created.body["author"] || got.body["owner"] != "user:"+target.String() {
		t.Fatalf("authorship or owner: %s", got.raw)
	}
	if err := h.st.View(ctx, tenant, func(sc store.Scope) error {
		r, e := sc.Resources().Get(ctx, folder.ID)
		if e != nil {
			return e
		}
		if e = personalFolderAdmits(r, auth.Principal{UserID: target}, auth.TrusteeRead); e != nil {
			return e
		}
		if e = personalFolderAdmits(r, auth.Principal{UserID: source}, auth.TrusteeRead); e == nil {
			return errors.New("old owner can still read")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.st.Mutate(ctx, tenant, func(sc store.Scope) error { return m.ApplyOwnershipTransfer(ctx, tenant, sc, prepared) }); err == nil {
		t.Fatal("stale review applied twice")
	}
	replacement, err := m.ensurePersonalFolder(ctx, tenant, auth.Principal{Kind: auth.KindUser, UserID: source})
	if err != nil || replacement == personalRef {
		t.Fatalf("mover cannot get fresh personal files: %s %v", replacement, err)
	}
	stable, err := m.ensurePersonalFolder(ctx, tenant, auth.Principal{Kind: auth.KindUser, UserID: source})
	if err != nil || stable != replacement {
		t.Fatalf("replacement folder is not stable: %s %v", stable, err)
	}
	// A later organization must not adopt the physical folder handed over here.
	otherTenant := h.createOrg(admin, "later-organization")
	otherRef, err := m.ensurePersonalFolder(ctx, otherTenant, auth.Principal{Kind: auth.KindUser, UserID: source})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.View(ctx, otherTenant, func(sc store.Scope) error {
		repo, e := sc.Ext(workspaceKind)
		if e != nil {
			return e
		}
		rows, _, e := repo.List(ctx, model.Query{Filters: []model.Filter{eq(colWsRef, otherRef)}, Limit: 1})
		if e != nil {
			return e
		}
		if !strings.Contains(rows[0].String(colWsRootPath), "tenant-"+otherTenant.String()) {
			return errors.New("later tenant adopted the transferred account folder")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

}

// Template ownership is a retained obligation, not authority that blocks retirement.
// Community has no route to change the owner, and DELETE only archives the template.
func TestArchivedTemplateDoesNotBlockRetirement(t *testing.T) {
	m := New()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "tpl-retire")
	created := h.doJSON("POST", "/v1/m/sessions/templates", admin, map[string]any{"name": "Leaver template"}, tenantHdr(tenant))
	if created.code != http.StatusCreated {
		t.Fatalf("create: %d %s", created.code, created.raw)
	}
	id := created.body["id"].(string)
	if got := h.do("DELETE", "/v1/m/sessions/templates/"+id, admin, tenantHdr(tenant)); got.code != http.StatusNoContent {
		t.Fatalf("archive: %d %s", got.code, got.raw)
	}
	author, err := model.ParseID(strings.TrimPrefix(created.body["author"].(string), "user:"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	var blocking []string
	if err := h.st.Mutate(ctx, tenant, func(sc store.Scope) error {
		var e error
		blocking, _, e = retirementBlockers(ctx, sc, auth.RetirementRequest{Tenant: tenant, User: author, Generation: 1})
		return e
	}); err != nil {
		t.Fatal(err)
	}
	for _, ref := range blocking {
		if strings.HasPrefix(ref, string(templateKind)+":") {
			t.Fatalf("archived template blocks its author's retirement: %v", blocking)
		}
	}
}
