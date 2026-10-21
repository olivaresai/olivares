// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package skills_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/skills"
)

// statusRefusal is a refusal that carries its own HTTP status, the way the
// registered-folder owner reports one.
type statusRefusal struct {
	status int
	msg    string
}

func (e statusRefusal) Error() string       { return e.msg }
func (e statusRefusal) HTTPStatusCode() int { return e.status }

type refusingWorkspace struct{ err error }

func (f refusingWorkspace) ReadSkillsWorkspace(context.Context, api.ModuleContext, string, string) (skills.WorkspaceSnapshot, error) {
	return skills.WorkspaceSnapshot{}, f.err
}

// A refusal from the registered-folder owner (not authorized, not active, not
// found) reaches the caller as its own 4xx status and message. Anything else,
// including a 5xx refusal, stays the generic 503 and never echoes its text.
func TestSkillsRegisteredFolderRefusalKeepsItsStatusAndMessage(t *testing.T) {
	cases := []struct {
		name    string
		err     error
		status  int
		code    string
		message string
	}{
		{"forbidden", statusRefusal{http.StatusForbidden, "workspace snapshot read is not authorized"}, http.StatusForbidden, "forbidden", "workspace snapshot read is not authorized"},
		{"conflict", statusRefusal{http.StatusConflict, "workspace registration is not active"}, http.StatusConflict, "conflict", "workspace registration is not active"},
		{"not found", statusRefusal{http.StatusNotFound, "path not found"}, http.StatusNotFound, "not_found", "path not found"},
		{"wrapped", fmt.Errorf("read: %w", statusRefusal{http.StatusForbidden, "workspace snapshot denied by DLP policy"}), http.StatusForbidden, "forbidden", "workspace snapshot denied by DLP policy"},
		{"server refusal", statusRefusal{http.StatusInternalServerError, "filesystem error"}, http.StatusServiceUnavailable, "skills_unavailable", ""},
		{"plain error", errors.New("dial tcp 10.0.0.9: connection refused"), http.StatusServiceUnavailable, "skills_unavailable", ""},
		{"unmapped client status", statusRefusal{http.StatusUnprocessableEntity, "workspace is on fire"}, http.StatusServiceUnavailable, "skills_unavailable", ""},
	}
	const generic = "Skills could not complete this operation. Retry after checking the service."
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := catalogOptions(t, skills.Options{Workspace: refusingWorkspace{err: tc.err}})
			body, _ := json.Marshal(map[string]any{"name": "refused", "source": map[string]string{"kind": "workspace", "workspace_ref": model.NewID().String(), "directory": "packs/research"}})
			req := httptest.NewRequest("POST", "/v1/m/skills/packs", bytes.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+h.token)
			req.Header.Set("X-Olivares-Tenant", h.tenant.String())
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Idempotency-Key", "refused-folder-"+strconv.Itoa(i))
			out := httptest.NewRecorder()
			h.server.ServeHTTP(out, req)
			var got struct {
				Error struct{ Code, Message string }
			}
			if err := json.Unmarshal(out.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode %d %s: %v", out.Code, out.Body.String(), err)
			}
			if out.Code != tc.status || got.Error.Code != tc.code {
				t.Fatalf("status/code = %d %q, want %d %q: %s", out.Code, got.Error.Code, tc.status, tc.code, out.Body.String())
			}
			if tc.message != "" && got.Error.Message != tc.message {
				t.Fatalf("message = %q, want %q", got.Error.Message, tc.message)
			}
			if tc.message == "" && got.Error.Message != generic {
				t.Fatalf("generic 503 message = %q, want %q", got.Error.Message, generic)
			}
			// The wiki recorder reads the same mapping through OperationErrorStatus.
			if status := skills.OperationErrorStatus(tc.err); status != tc.status {
				t.Fatalf("OperationErrorStatus = %d, want %d", status, tc.status)
			}
		})
	}
}
