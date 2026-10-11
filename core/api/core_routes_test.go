// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// Walk the actual router, including nested groups. A raw chi registration must
// fail even when its handler happens to remember an authorization call today.
func TestCoreRoutesDeclarePolicy(t *testing.T) {
	s := &Server{}
	h, err := s.buildRouter(nil)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	err = chi.Walk(h.(chi.Routes), func(method, route string, handler http.Handler, _ ...func(http.Handler) http.Handler) error {
		count++
		if _, ok := handler.(interface{ routePolicyDeclared() }); !ok {
			t.Errorf("%s %s has no registration policy", method, route)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if count == 0 {
		t.Fatal("no routes checked")
	}
}

func TestRoutePolicyRejectsRawRegistration(t *testing.T) {
	r := chi.NewRouter()
	coreRoute(r, "GET", "/public", publicRoute, func(http.ResponseWriter, *http.Request, ModuleContext) {})
	r.Route("/nested", func(r chi.Router) {
		r.Get("/forgotten", func(http.ResponseWriter, *http.Request) {})
	})
	if err := checkRoutePolicies(r); err == nil || !strings.Contains(err.Error(), "GET /nested/forgotten") {
		t.Fatalf("unprotected route must prevent startup: %v", err)
	}
}

func TestCoreRouteRejectsMissingPolicy(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("registered a handler without an admission policy")
		}
	}()
	coreRoute(chi.NewRouter(), "GET", "/forgotten", nil, func(http.ResponseWriter, *http.Request, ModuleContext) {})
}

func TestCoreRegistrationAuthorizesBeforeHandler(t *testing.T) {
	s := &Server{authz: auth.NewAuthorizer(nil)}
	for _, tc := range []struct {
		name      string
		principal auth.Principal
		status    int
	}{
		{"anonymous", auth.Principal{}, http.StatusUnauthorized},
		{"ordinary user", auth.Principal{Kind: auth.KindUser, UserID: model.NewID()}, http.StatusForbidden},
		{"system administrator", auth.Principal{Kind: auth.KindUser, UserID: model.NewID(), Superadmin: true}, http.StatusNoContent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := chi.NewRouter()
			called := false
			coreRoute(r, "POST", "/admin", s.systemRoute("system:admin"), func(w http.ResponseWriter, _ *http.Request, mc ModuleContext) {
				called = true
				if mc.Principal.UserID != tc.principal.UserID || mc.Tenant != model.SystemTenantID {
					t.Fatal("handler did not receive the admitted principal and scope")
				}
				w.WriteHeader(http.StatusNoContent)
			})
			req := httptest.NewRequest("POST", "/admin", nil)
			req = req.WithContext(withPrincipal(req.Context(), tc.principal))
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != tc.status || called != (tc.status == http.StatusNoContent) {
				t.Fatalf("status=%d called=%t: %s", w.Code, called, w.Body.String())
			}
		})
	}
}
