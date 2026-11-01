// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// skillsOnADepartment is the real engine with an administrator and an editor (a person who may
// write templates but not administer them), and two templates: the one Engineering is given and
// one nobody assigned.
type skillsOnADepartment struct {
	admin, editor skillsComposition
	given, free   string
}

func newSkillsOnADepartment(t *testing.T) skillsOnADepartment {
	t.Helper()
	d := newDepartmentEngine(t)
	admin := skillsComposition{t: t, eng: d.s.eng, h: d.s.eng.api.Handler(), token: d.login("dormant@olivares.ai"), tenant: d.s.tenant}
	const editorLogin = "editor@example.invalid"
	d.user(editorLogin)
	editor := admin
	editor.token = d.login(editorLogin)
	d.workspace("engineering", "")
	given, free := admin.template("Engineering default"), admin.template("Free")
	d.assignToDepartment("template", given, "engineering", "allow")
	return skillsOnADepartment{admin: admin, editor: editor, given: given, free: free}
}

// A skill pinned to a template that a department is given is delivered to every launch of the
// department that names no template, so writing that pin is changing the template: it asks what
// the template's own edit asks (#530). The permission to write templates is not enough, and the
// answer is the same whether the pin is made, narrowed or taken off.
func TestASkillOnADepartmentsDefaultTemplateNeedsTemplateAdministration(t *testing.T) {
	w := newSkillsOnADepartment(t)
	admin, editor, given, free := w.admin, w.editor, w.given, w.free
	revision := admin.installPackMembers("standards-pack", "research", "review")

	pin := func(c skillsComposition, template, revision string) (int, []byte) {
		return c.assign(c, "template", template, revision)
	}
	pinned := func(raw []byte) (id, version string) {
		var out struct {
			Assignment struct {
				ID      string `json:"id"`
				Version int64  `json:"version"`
			} `json:"assignment"`
		}
		if err := json.Unmarshal(raw, &out); err != nil || out.Assignment.ID == "" {
			t.Fatalf("the pin answered %s: %v", raw, err)
		}
		return out.Assignment.ID, strconv.FormatInt(out.Assignment.Version, 10)
	}

	// An editor writes templates, not what a department is told.
	if code, raw := pin(editor, given, revision); code != http.StatusForbidden || !strings.Contains(string(raw), "template administration") {
		t.Errorf("an editor pinning a skill to the department's template = %d %s, want 403 naming template administration", code, raw)
	}
	// A template nobody assigned stays as open as it was.
	if code, raw := pin(editor, free, revision); code != http.StatusCreated {
		t.Errorf("an editor pinning a skill to a free template = %d %s, want 201", code, raw)
	}
	code, raw := pin(admin, given, revision)
	if code != http.StatusCreated {
		t.Fatalf("an administrator pinning a skill to the department's template = %d %s, want 201", code, raw)
	}
	id, version := pinned(raw)

	// Narrowing the pin to one skill, and taking it off, change what the department gets too.
	path := "/v1/m/skills/assignments/" + id
	narrow := map[string]any{"target_kind": "template", "target_id": given, "pack_revision_id": revision, "members": []string{"review"}}
	if code, raw := editor.doHeader("PUT", path, editor.token, version, narrow); code != http.StatusForbidden {
		t.Errorf("an editor narrowing the pin = %d %s, want 403", code, raw)
	}
	if code, raw := editor.doHeader("DELETE", path, editor.token, version, nil); code != http.StatusForbidden {
		t.Errorf("an editor taking the pin off = %d %s, want 403", code, raw)
	}
	code, raw = admin.doHeader("PUT", path, admin.token, version, narrow)
	if code != http.StatusOK {
		t.Fatalf("an administrator narrowing the pin = %d %s, want 200", code, raw)
	}
	_, version = pinned(raw)
	if code, raw := admin.doHeader("DELETE", path, admin.token, version, nil); code != http.StatusNoContent && code != http.StatusOK {
		t.Errorf("an administrator taking the pin off = %d %s, want 204", code, raw)
	}
}

// The skills a session's configure tool pins are written as the session's launcher, through the
// same routes, and held to the same question: a launcher who writes templates but does not
// administer them cannot change what a department is given.
func TestASessionOfAnEditorCannotPinASkillOnADepartmentsTemplate(t *testing.T) {
	w := newSkillsOnADepartment(t)
	revision := w.admin.installPack("standards-session")
	pinAs := func(token, template string) (int, string) {
		t.Helper()
		launcher := skillsSessionLauncher(t, w.admin, token)
		raw, err := json.Marshal(map[string]any{"target_kind": "template", "target_id": template, "pack_revision_id": revision})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest("POST", "/v1/m/skills/assignments", bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		w.admin.eng.api.ServeSession(rec, req, launcher)
		return rec.Code, rec.Body.String()
	}

	if code, raw := pinAs(w.editor.token, w.given); code != http.StatusForbidden || !strings.Contains(raw, "template administration") {
		t.Errorf("an editor's session pinning a skill to the department's template = %d %s, want 403 naming template administration", code, raw)
	}
	if code, raw := pinAs(w.editor.token, w.free); code != http.StatusCreated {
		t.Errorf("an editor's session pinning a skill to a free template = %d %s, want 201", code, raw)
	}
	if code, raw := pinAs(w.admin.token, w.given); code != http.StatusCreated {
		t.Errorf("an administrator's session pinning a skill to the department's template = %d %s, want 201", code, raw)
	}
}
