// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package skills_test

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/skills"
)

func TestSkillsJSONBodiesStayBoundedAndSingleDocument(t *testing.T) {
	h := catalogOptions(t, skills.Options{Targets: workspaceAuthority{}, Workspace: workspaceSnapshotFixture{
		version: 7, files: []skills.WorkspaceFile{{Path: "SKILL.md", Mode: fs.FileMode(0644), Bytes: []byte(harmless)}},
	}})
	pack := h.install(t, "json-body-fixture", "retained support")
	target := h.workspace(t, false)
	assignment, _ := json.Marshal(skills.AssignmentRequest{Target: target, RevisionID: pack.Revision.ID})
	packName := model.NewID().String()
	importBody, _ := json.Marshal(map[string]any{"name": packName, "source": map[string]string{
		"kind": "workspace", "workspace_ref": model.NewID().String(), "directory": "packs/research",
	}})
	for _, route := range []struct{ name, path, body string }{
		{"assignment", "/v1/m/skills/assignments", string(assignment)},
		{"import", "/v1/m/skills/packs", string(importBody)},
	} {
		for _, tc := range []struct {
			name, body string
			status     int
		}{
			{"single", route.body, http.StatusCreated},
			{"whitespace", route.body + " \n\t", http.StatusCreated},
			{"exact-cap", route.body + strings.Repeat(" ", (16<<10)-len(route.body)), http.StatusCreated},
			{"over-cap", route.body + strings.Repeat(" ", (16<<10)+1-len(route.body)), http.StatusBadRequest},
			{"second-value", route.body + `{}`, http.StatusBadRequest},
			{"stray-brace", route.body + `}`, http.StatusBadRequest},
			{"stray-bracket", route.body + `]`, http.StatusBadRequest},
			{"unknown-field", route.body[:len(route.body)-1] + `,"unknown":true}`, http.StatusBadRequest},
			{"empty", "", http.StatusBadRequest},
		} {
			t.Run(route.name+"/"+tc.name, func(t *testing.T) {
				body := tc.body
				if route.name == "assignment" {
					body = strings.ReplaceAll(body, target.ID, h.workspace(t, false).ID)
				} else {
					body = strings.ReplaceAll(body, packName, model.NewID().String())
				}
				req := httptest.NewRequest(http.MethodPost, route.path, strings.NewReader(body))
				req.Header.Set("Authorization", "Bearer "+h.token)
				req.Header.Set("X-Olivares-Tenant", h.tenant.String())
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Idempotency-Key", route.name+"-"+tc.name)
				out := httptest.NewRecorder()
				h.server.ServeHTTP(out, req)
				if out.Code != tc.status {
					t.Fatalf("status = %d, want %d: %s", out.Code, tc.status, out.Body.String())
				}
				if tc.status == http.StatusBadRequest && !strings.Contains(out.Body.String(), `"code":"invalid_request"`) {
					t.Fatalf("decoder refusal changed: %s", out.Body.String())
				}
			})
		}
	}
}
