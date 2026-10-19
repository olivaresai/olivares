// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// registeredFolderImporter boots the composition, registers a folder that holds
// one skill and returns the caller plus a function that imports that folder as a
// skills pack under the given name.
func registeredFolderImporter(t *testing.T) (skillsComposition, func(name string) (int, []byte)) {
	t.Helper()
	c := bootSkillsComposition(t)
	folder := t.TempDir()
	skill := filepath.Join(folder, ".agents", "skills", "note")
	if err := os.MkdirAll(skill, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("---\nname: note\ndescription: Keep a note.\nlicense: MIT\n---\nWrite the note.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, raw := c.do("POST", "/v1/m/sessions/workspaces", c.token, map[string]any{"root_path": folder, "name": "skills folder"})
	if code != http.StatusCreated {
		t.Fatalf("register folder = %d %s", code, raw)
	}
	var workspace struct {
		Ref string `json:"workspace_ref"`
	}
	if err := json.Unmarshal(raw, &workspace); err != nil || workspace.Ref == "" {
		t.Fatalf("workspace ref in %s: %v", raw, err)
	}
	return c, func(name string) (int, []byte) {
		t.Helper()
		body, err := json.Marshal(map[string]any{"name": name, "source": map[string]any{"kind": "workspace", "workspace_ref": workspace.Ref, "directory": ".agents/skills"}})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest("POST", "/v1/m/skills/packs", bytes.NewReader(body))
		req.RemoteAddr = "10.0.0.1:1234"
		req.Header.Set("Authorization", "Bearer "+c.token)
		req.Header.Set("X-Olivares-Tenant", c.tenant)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", "import-"+name)
		rec := httptest.NewRecorder()
		c.h.ServeHTTP(rec, req)
		return rec.Code, rec.Body.Bytes()
	}
}

func createAbacDenyPolicy(t *testing.T, c skillsComposition, name string, rule map[string]any) string {
	t.Helper()
	code, raw := c.do("POST", "/v1/m/governance/policies", c.token, map[string]any{
		"name": name, "kind": "abac", "enabled": true,
		"spec": map[string]any{"rules": []any{rule}},
	})
	if code != http.StatusCreated {
		t.Fatalf("create ABAC deny policy %q = %d %s", name, code, raw)
	}
	return decodeField(t, raw, "id")
}

// putAbacDenyPolicy rewrites a policy in place; every call changes the spec or the
// enabled flag, so each one advances the authorization epoch.
func putAbacDenyPolicy(t *testing.T, c skillsComposition, id, name string, enabled bool, rule map[string]any) {
	t.Helper()
	code, raw := c.do("PUT", "/v1/m/governance/policies/"+id, c.token, map[string]any{
		"name": name, "kind": "abac", "enabled": enabled,
		"spec": map[string]any{"rules": []any{rule}},
	})
	if code != http.StatusOK {
		t.Fatalf("update ABAC policy %q = %d %s", name, code, raw)
	}
}

// A runtime deny (ABAC) policy must not stop a registered folder from being
// imported as a skills pack: the folder read asks the request authorizer for a
// receipt on every call, so a policy that does not concern the read must leave
// that receipt valid.
func TestSkillsImportFromFolderSurvivesAbacDenyPolicy(t *testing.T) {
	c, importPack := registeredFolderImporter(t)
	if code, raw := importPack("before"); code != http.StatusCreated {
		t.Fatalf("import before any policy = %d %s", code, raw)
	}
	createAbacDenyPolicy(t, c, "deny-secret-admin", map[string]any{"deny": true, "principal_kind": "token", "resource": "secret", "verb": "admin"})
	if code, raw := importPack("after"); code != http.StatusCreated {
		t.Fatalf("import after an unrelated ABAC deny policy = %d %s", code, raw)
	}
}

// Control: a deny policy that does concern the folder read still refuses it, and
// the refusal reaches the caller as its own 403 and message, not as the generic
// 503 that asks for a retry.
func TestSkillsImportFromFolderStaysDeniedByAbacPolicyOnTheRead(t *testing.T) {
	c, importPack := registeredFolderImporter(t)
	if code, raw := importPack("before"); code != http.StatusCreated {
		t.Fatalf("import before any policy = %d %s", code, raw)
	}
	createAbacDenyPolicy(t, c, "deny-workspace-read", map[string]any{"deny": true, "permission": "sessions:workspace:read"})
	code, raw := importPack("after")
	if code != http.StatusForbidden || !bytes.Contains(raw, []byte(`"code":"forbidden"`)) || !bytes.Contains(raw, []byte("workspace snapshot read is not authorized")) {
		t.Fatalf("import after a deny policy on the read = %d %s, want 403 forbidden with the owner's message", code, raw)
	}
}

// Update, disable and delete advance the authorization epoch just as create does,
// so each of them must leave the folder read decidable without a restart: the read
// follows the policy as it changes, denied while a deny on it is enabled and
// allowed again as soon as that deny is rewritten, disabled or removed.
func TestSkillsImportFromFolderFollowsAbacPolicyUpdateDisableAndDelete(t *testing.T) {
	c, importPack := registeredFolderImporter(t)
	denyRead := map[string]any{"deny": true, "permission": "sessions:workspace:read"}
	unrelated := map[string]any{"deny": true, "principal_kind": "token", "resource": "secret", "verb": "admin"}
	step := 0
	imports := func(want int, what string) {
		t.Helper()
		step++
		if code, raw := importPack("step-" + strconv.Itoa(step)); code != want {
			t.Fatalf("import %s = %d %s, want %d", what, code, raw, want)
		}
	}
	imports(http.StatusCreated, "before any policy")
	id := createAbacDenyPolicy(t, c, "deny-workspace-read", denyRead)
	imports(http.StatusForbidden, "while a deny on the read is enabled")
	putAbacDenyPolicy(t, c, id, "deny-workspace-read", true, unrelated)
	imports(http.StatusCreated, "after the deny is rewritten to an unrelated rule")
	putAbacDenyPolicy(t, c, id, "deny-workspace-read", true, denyRead)
	imports(http.StatusForbidden, "after the deny on the read is restored")
	putAbacDenyPolicy(t, c, id, "deny-workspace-read", false, denyRead)
	imports(http.StatusCreated, "after the deny is disabled")
	putAbacDenyPolicy(t, c, id, "deny-workspace-read", true, denyRead)
	imports(http.StatusForbidden, "after the deny is enabled again")
	if code, raw := c.do("DELETE", "/v1/m/governance/policies/"+id, c.token, nil); code != http.StatusNoContent {
		t.Fatalf("delete ABAC policy = %d %s", code, raw)
	}
	imports(http.StatusCreated, "after the deny is deleted")
}
