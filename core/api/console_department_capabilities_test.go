// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
)

func TestConsoleDepartmentCreationCapabilities(t *testing.T) {
	h := newHarnessOpts(t, func(o *api.Options) {
		o.PrincipalEvidenceProducer = o.Authenticator
	})
	owner := h.adminLogin()
	tenant := h.createOrg(owner, "console-department")
	h.elevate(owner)
	hdr := tenantHdr(tenant)
	ws := h.do("POST", "/v1/workspaces", owner, map[string]any{"name": "Engineering", "slug": "engineering"}, hdr)
	if ws.code != http.StatusCreated {
		t.Fatalf("workspace: %d %s", ws.code, ws.raw)
	}
	workspace := ws.body["id"].(string)
	user := h.do("POST", "/v1/users", owner, map[string]any{"email": "admin@console.example", "password": "memberpass1", "tenant": tenant.String(), "role": "admin", "workspace_id": workspace}, hdr)
	if user.code != http.StatusCreated {
		t.Fatalf("confined admin: %d %s", user.code, user.raw)
	}
	login := h.do("POST", "/v1/auth/login", "", map[string]any{"email": "admin@console.example", "password": "memberpass1"}, nil)
	if login.code != http.StatusOK {
		t.Fatalf("login: %d %s", login.code, login.raw)
	}
	token := login.body["token"].(string)
	for _, route := range []string{"agents", "agent-groups"} {
		t.Run(route, func(t *testing.T) {
			questions := []map[string]any{
				{"id": "own", "kind": "surface", "operation": "POST /v1/" + route, "workspace_id": workspace},
				{"id": "foreign", "kind": "surface", "operation": "POST /v1/" + route, "workspace_id": model.NewID().String()},
				{"id": "absent", "kind": "surface", "operation": "POST /v1/" + route},
			}
			reply := h.do("POST", "/v1/auth/capabilities", token, map[string]any{"schema_version": 2, "questions": questions}, hdr)
			if reply.code != http.StatusOK {
				t.Fatalf("self rights: %d %s", reply.code, reply.raw)
			}
			for _, result := range reply.body["results"].([]any) {
				row := result.(map[string]any)
				want := map[string]string{"own": "reachable", "foreign": "not_reachable", "absent": "unknown"}[row["id"].(string)]
				if row["state"] != want {
					t.Errorf("%v: got %v (%v), want %s", row["id"], row["state"], row["code"], want)
				}
				if row["id"] == "own" {
					if budget, ok := row["refresh_after_ms"].(float64); !ok || budget <= 0 {
						t.Error("positive answer has no usable freshness budget")
					}
				} else if _, ok := row["refresh_after_ms"]; ok {
					t.Error("non-positive answer carries a permit budget")
				}
			}
			rows := h.do("GET", "/v1/"+route, owner, nil, hdr)
			if rows.code != http.StatusOK || len(rows.body["items"].([]any)) != 0 {
				t.Fatal("asking self rights executed a creation")
			}
		})
	}
}
