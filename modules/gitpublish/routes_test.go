// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package gitpublish

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
)

type mounted struct {
	method, pattern string
	perm            auth.Permission
	sealed          bool
	meta            api.RouteMetadata
	entity          *api.EntityRef
	h               api.ModuleHandler
}

// sealedRecorder is a registrar with the governed door.
type sealedRecorder struct{ routes *[]mounted }

func (r sealedRecorder) Handle(method, pattern string, perm auth.Permission, h api.ModuleHandler) {
	*r.routes = append(*r.routes, mounted{method: method, pattern: pattern, perm: perm, h: h})
}

func (r sealedRecorder) HandleEntity(method, pattern string, perm auth.Permission, ref api.EntityRef, h api.ModuleHandler) {
	ref2 := ref
	*r.routes = append(*r.routes, mounted{method: method, pattern: pattern, perm: perm, entity: &ref2, h: h})
}

func (r sealedRecorder) HandleSealed(method, pattern string, perm auth.Permission, s api.SealedRoute, h api.ModuleHandler) {
	if !s.IsSealed() {
		panic("unsealed")
	}
	*r.routes = append(*r.routes, mounted{method: method, pattern: pattern, perm: perm, sealed: true, meta: s.Metadata(), h: h})
}

// plainRecorder has no governed door.
type plainRecorder struct{ routes *[]mounted }

func (r plainRecorder) Handle(method, pattern string, perm auth.Permission, h api.ModuleHandler) {
	*r.routes = append(*r.routes, mounted{method: method, pattern: pattern, perm: perm, h: h})
}

func (r plainRecorder) HandleEntity(method, pattern string, perm auth.Permission, _ api.EntityRef, h api.ModuleHandler) {
	*r.routes = append(*r.routes, mounted{method: method, pattern: pattern, perm: perm, h: h})
}

func TestMutationRoutesMountOnlyThroughTheSealedDoor(t *testing.T) {
	m := New(Options{})
	var routes []mounted
	m.APIRoutes(sealedRecorder{routes: &routes})
	want := map[string]struct {
		perm   auth.Permission
		action auth.CedarAction
		aal    int
	}{
		"POST /targets":                    {permTargetAdmin, actionTarget, auth.AAL3},
		"PUT /targets/{id}":                {permTargetAdmin, actionTarget, auth.AAL3},
		"DELETE /targets/{id}":             {permTargetAdmin, actionTarget, auth.AAL3},
		"POST /targets/{id}/pushes":        {permPush, actionPush, 0},
		"POST /targets/{id}/pull-requests": {permPullRequest, actionPullRequest, 0},
		"POST /targets/{id}/merges":        {permMerge, actionMerge, auth.AAL3},
		"POST /intents/{id}/reconcile":     {permTargetRead, actionReconcile, 0},
		"POST /intents/{id}/abandon":       {permTargetAdmin, actionAbandon, auth.AAL3},
	}
	declared := map[auth.Permission]bool{}
	for _, p := range m.Permissions() {
		declared[p] = true
	}
	actions := map[auth.CedarAction]bool{}
	for _, a := range m.Actions() {
		actions[a] = true
	}
	seen := map[string]bool{}
	for _, r := range routes {
		key := r.method + " " + r.pattern
		if !declared[r.perm] {
			t.Fatalf("%s uses undeclared permission %s", key, r.perm)
		}
		if r.method == http.MethodGet {
			if r.sealed {
				t.Fatalf("%s: reads need no seal", key)
			}
			continue
		}
		w, ok := want[key]
		if !ok {
			t.Fatalf("unexpected mutation route %s", key)
		}
		if !r.sealed || r.perm != w.perm || r.meta.CedarAction != string(w.action) || r.meta.MinimumAAL != w.aal || !actions[w.action] {
			t.Fatalf("%s mounted as %+v", key, r)
		}
		seen[key] = true
	}
	if len(seen) != len(want) {
		t.Fatalf("mounted %d of %d mutation routes", len(seen), len(want))
	}
	// A registrar without the governed door gets no mutation route at all:
	// never a fallback to Handle.
	var plain []mounted
	m.APIRoutes(plainRecorder{routes: &plain})
	for _, r := range plain {
		if r.method != http.MethodGet {
			t.Fatalf("mutation route %s %s mounted without the sealed door", r.method, r.pattern)
		}
	}
}

func handlerFor(t *testing.T, m *Module, method, pattern string) api.ModuleHandler {
	t.Helper()
	var routes []mounted
	m.APIRoutes(sealedRecorder{routes: &routes})
	for _, r := range routes {
		if r.method == method && r.pattern == pattern {
			return r.h
		}
	}
	t.Fatalf("no route %s %s", method, pattern)
	return nil
}

func call(h api.ModuleHandler, c Caller, method, id string, body any) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(method, "/x", bytes.NewReader(b))
	rc := chi.NewRouteContext()
	if id != "" {
		rc.URLParams.Add("id", id)
	}
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rc))
	rec := httptest.NewRecorder()
	h(rec, req, api.ModuleContext{Principal: c.Principal, Tenant: c.Tenant})
	return rec
}

func TestRequestFieldsForCustodyAreRefused(t *testing.T) {
	h := newHarness(t)
	create := handlerFor(t, h.m, http.MethodPost, "/targets")
	for _, f := range []string{"credential_ref", "api_base", "installation_id", "local_path", "secret"} {
		body := map[string]any{"workspace_id": h.ws.String(), "credential_binding_id": "cb1", "repository_binding_id": "rb1", "push_prefix": "olivares/", "merge_bases": []string{"main"}, f: "x"}
		rec := call(create, h.admin(), http.MethodPost, "", body)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "field_not_accepted") {
			t.Fatalf("%s: %d %s", f, rec.Code, rec.Body.String())
		}
	}
	push := handlerFor(t, h.m, http.MethodPost, "/targets/{id}/pushes")
	rec := call(push, h.user(), http.MethodPost, h.target.ID.String(), map[string]any{"operation_id": "o", "ref": "refs/heads/olivares/z", "commit": shaCommit, "tree": shaTree, "repo_path": "/etc"})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "field_not_accepted") {
		t.Fatalf("push with a path: %d %s", rec.Code, rec.Body.String())
	}
}

func TestPushRouteRejectsTrailingJSONWithoutApplying(t *testing.T) {
	for _, tc := range []struct{ name, tail string }{
		{"object", "{}"}, {"array", "[]"}, {"null", "null"},
		{"true", "true"}, {"false", "false"}, {"number", "42"}, {"string", `"other"`},
		{"whitespace_then_object", " \n\t{}"}, {"nested_object", `{"x":[1,2]}`},
		{"close_object", "}"}, {"close_array", "]"},
		{"close_then_object", "\n]{}"}, {"close_then_null", "\n}null"},
	} {
		t.Run(tc.name, func(t *testing.T) { checkPushEnvelope(t, tc.tail, http.StatusBadRequest) })
	}
}

func TestPushRouteAcceptsSingleJSONDocumentWhitespace(t *testing.T) {
	for _, tc := range []struct{ name, tail string }{{"no_tail", ""}, {"whitespace", " \r\n\t "}} {
		t.Run(tc.name, func(t *testing.T) { checkPushEnvelope(t, tc.tail, http.StatusOK) })
	}
}

func checkPushEnvelope(t *testing.T, tail string, wantStatus int) {
	t.Helper()
	h := newHarness(t)
	push := handlerFor(t, h.m, http.MethodPost, "/targets/{id}/pushes")
	body, err := json.Marshal(map[string]any{
		"operation_id": "op-trailing", "ref": "refs/heads/olivares/trailing",
		"expected_old": "", "commit": shaCommit, "tree": shaTree,
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/x", bytes.NewReader(append(body, []byte(tail)...)))
	rc := chi.NewRouteContext()
	rc.URLParams.Add("id", h.target.ID.String())
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rc))
	rec := httptest.NewRecorder()
	c := h.user()
	push(rec, req, api.ModuleContext{Principal: c.Principal, Tenant: c.Tenant})
	if rec.Code != wantStatus {
		t.Errorf("HTTP=%d, want %d; body %s", rec.Code, wantStatus, rec.Body.String())
	}
	intents, err := h.m.Intents(context.Background(), c, h.target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if wantStatus == http.StatusBadRequest {
		var response map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if len(response) != 1 || response["error"] != "invalid_request" {
			t.Errorf("refusal response: %v", response)
		}
		if len(intents) != 0 || h.git.count() != 0 || h.host.mints != 0 || h.host.ref("olivares/trailing") != "" {
			t.Errorf("refused envelope applied: %d intents, %d pushes, %d mints, ref %q", len(intents), h.git.count(), h.host.mints, h.host.ref("olivares/trailing"))
		}
	} else if len(intents) != 1 || h.git.count() != 1 || h.host.ref("olivares/trailing") != shaCommit {
		t.Errorf("valid envelope effects: %d intents, %d pushes, ref %q", len(intents), h.git.count(), h.host.ref("olivares/trailing"))
	}
}

func TestPushRouteReturnsContractDTO(t *testing.T) {
	h := newHarness(t)
	push := handlerFor(t, h.m, http.MethodPost, "/targets/{id}/pushes")
	rec := call(push, h.user(), http.MethodPost, h.target.ID.String(), map[string]any{"operation_id": "op-dto", "ref": "refs/heads/olivares/z", "expected_old": "", "commit": shaCommit, "tree": shaTree})
	if rec.Code != http.StatusOK {
		t.Fatalf("push: %d %s", rec.Code, rec.Body.String())
	}
	var dto map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &dto)
	if dto["state"] != StateApplied || dto["receipt"] != ReceiptCaused || dto["effect"] != "push" {
		t.Fatalf("dto = %v", dto)
	}
	for _, k := range []string{"requested", "observed", "acknowledged"} {
		if _, ok := dto[k].(map[string]any); !ok {
			t.Fatalf("dto lacks %s: %v", k, dto)
		}
	}
	raw := rec.Body.String()
	for _, leak := range []string{"/srv/olivares", "local_path", "credential_binding", "subject_user"} {
		if strings.Contains(raw, leak) {
			t.Fatalf("dto leaks %q: %s", leak, raw)
		}
	}
	// An unsupported exact-result requirement is refused by name.
	merge := handlerFor(t, h.m, http.MethodPost, "/targets/{id}/merges")
	rec = call(merge, h.admin(), http.MethodPost, h.target.ID.String(), map[string]any{"operation_id": "m", "number": 1, "expected_head": shaCommit, "method": "merge", "expected_result_tree": shaTree})
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "unsupported_requirement") {
		t.Fatalf("exact tree: %d %s", rec.Code, rec.Body.String())
	}
}
