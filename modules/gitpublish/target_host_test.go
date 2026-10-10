// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package gitpublish

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func readTargetRoute(t *testing.T, h *harness, pattern string, c Caller) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	route := chi.NewRouteContext()
	route.URLParams.Add("id", h.target.ID.String())
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, route))
	rec := httptest.NewRecorder()
	handlerFor(t, h.m, http.MethodGet, pattern)(rec, req, api.ModuleContext{
		Principal: c.Principal, Tenant: c.Tenant, Data: api.NewScopedData(h.st, c.Tenant),
	})
	return rec
}

func TestTargetReadHost(t *testing.T) {
	for _, host := range []string{"git", "github", "gitlab"} {
		t.Run(host, func(t *testing.T) {
			h := newScopeHarnessOn(t, store.Config{Engine: store.EngineSQLite, DSN: ":memory:"})
			h.custody.hostKind = host
			// Metadata is available to readers without revealing binding IDs.
			h.authz.deny[h.ws] = auth.ErrRouteDenied
			for _, pattern := range []string{"/targets", "/targets/{id}"} {
				rec := readTargetRoute(t, h, pattern, h.user())
				if rec.Code != http.StatusOK {
					t.Fatalf("%s: %d %s", pattern, rec.Code, rec.Body.String())
				}
				var body map[string]any
				if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				if pattern == "/targets" {
					body = body["items"].([]any)[0].(map[string]any)
				}
				if body["host"] != host {
					t.Errorf("%s host = %v; want %s", pattern, body["host"], host)
				}
				for _, key := range []string{"credential_binding_id", "repository_binding_id", "local_path", "secret"} {
					if _, exposed := body[key]; exposed {
						t.Errorf("%s exposes %s", pattern, key)
					}
				}
			}
		})
	}
}

type failingTargetCustody struct {
	Custody
	err error
}

func (c failingTargetCustody) CredentialBinding(context.Context, model.TenantID, model.ID, string) (CredentialBinding, error) {
	return CredentialBinding{}, c.err
}

func TestTargetReadUnavailableHost(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status int
	}{
		{"revoked", ErrBindingNotApproved, http.StatusOK},
		{"lookup_failed", errors.New("source read failed"), http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newScopeHarnessOn(t, store.Config{Engine: store.EngineSQLite, DSN: ":memory:"})
			h.m.opts.Custody = failingTargetCustody{Custody: h.custody, err: tc.err}
			for _, pattern := range []string{"/targets", "/targets/{id}"} {
				rec := readTargetRoute(t, h, pattern, h.user())
				if rec.Code != tc.status {
					t.Errorf("%s: %d; want %d", pattern, rec.Code, tc.status)
				}
				if strings.Contains(rec.Body.String(), `"host"`) || strings.Contains(rec.Body.String(), tc.err.Error()) {
					t.Errorf("%s fabricated a host or leaked the error: %s", pattern, rec.Body.String())
				}
			}
		})
	}
}

func TestTargetReadUnknownHost(t *testing.T) {
	for _, host := range []string{"unknown", "GitHub"} {
		t.Run(host, func(t *testing.T) {
			h := newScopeHarnessOn(t, store.Config{Engine: store.EngineSQLite, DSN: ":memory:"})
			h.custody.hostKind = host
			for _, pattern := range []string{"/targets", "/targets/{id}"} {
				rec := readTargetRoute(t, h, pattern, h.user())
				if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), `"host"`) {
					t.Fatalf("%s fabricated a host: %d %s", pattern, rec.Code, rec.Body.String())
				}
			}
		})
	}
}
