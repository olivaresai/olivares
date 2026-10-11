// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// policyRoute is the common registration boundary for core and module routes.
// Only the registrars construct it, after supplying the route's admission policy.
// buildRouter checks the finished tree so a raw chi registration fails at boot.
type policyRoute struct{ http.Handler }

func (policyRoute) routePolicyDeclared() {}

func registerPolicyRoute(r chi.Router, method, pattern, policy string, h http.Handler) {
	if policy == "" || h == nil {
		panic("api: route requires an explicit policy and handler")
	}
	h = policyRoute{h}
	if method == "*" {
		r.Handle(pattern, h)
	} else {
		r.Method(method, pattern, h)
	}
}

func checkRoutePolicies(r chi.Routes) error {
	return chi.Walk(r, func(method, pattern string, h http.Handler, _ ...func(http.Handler) http.Handler) error {
		if _, ok := h.(policyRoute); !ok {
			return fmt.Errorf("api: route %s %s has no registration policy", method, pattern)
		}
		return nil
	})
}

type coreAdmission func(http.ResponseWriter, *http.Request) (ModuleContext, bool)

// Core routes share the registration boundary, authorization helpers and handler
// context with modules. They do not opt into module-only lineage loading, store
// confinement or privileged-session recording as a side effect of registration.
func coreRoute(r chi.Router, method, pattern string, admission coreAdmission, h ModuleHandler) {
	if admission == nil || h == nil {
		panic("api: core route requires an explicit admission policy and handler")
	}
	registerPolicyRoute(r, method, pattern, "core", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if mc, ok := admission(w, r); ok {
			h(w, r, mc)
		}
	}))
}

// Public endpoints include login/setup and credential exchanges whose proof is
// in the body. Their existing proof validation remains part of the operation.
func publicRoute(http.ResponseWriter, *http.Request) (ModuleContext, bool) {
	return ModuleContext{}, true
}

func (s *Server) tenantRoute(perm auth.Permission) coreAdmission {
	return s.entityRoute(perm, "", "")
}

func (s *Server) entityRoute(perm auth.Permission, kind, param string) coreAdmission {
	if perm == "" {
		panic("api: tenant route requires a permission")
	}
	return func(w http.ResponseWriter, r *http.Request) (ModuleContext, bool) {
		id := model.ID(chi.URLParam(r, param))
		var p auth.Principal
		var tenant model.TenantID
		var ok bool
		if kind != "" {
			p, tenant, ok = s.authzTenantEntityKind(w, r, perm, kind, id)
		} else if param != "" {
			p, tenant, ok = s.authzTenantEntity(w, r, perm, id)
		} else {
			p, tenant, ok = s.authzTenant(w, r, perm)
		}
		return ModuleContext{Principal: p, Tenant: tenant}, ok
	}
}

func (s *Server) systemRoute(perm auth.Permission) coreAdmission {
	if perm == "" {
		panic("api: system route requires a permission")
	}
	return func(w http.ResponseWriter, r *http.Request) (ModuleContext, bool) {
		p, ok := s.authzSystem(w, r, perm)
		return ModuleContext{Principal: p, Tenant: model.SystemTenantID}, ok
	}
}

// Authentication is the route policy for self-service and for operations whose
// authority depends on a decoded body or stored object. Those narrower checks
// still run in the operation, in their original order and transaction.
func (s *Server) authenticatedRoute(w http.ResponseWriter, r *http.Request) (ModuleContext, bool) {
	p, ok := principalFrom(r.Context())
	if !ok {
		s.writeError(w, r, auth.ErrUnauthenticated)
	}
	return ModuleContext{Principal: p}, ok
}

func (s *Server) sessionRoute(w http.ResponseWriter, r *http.Request) (ModuleContext, bool) {
	p, ok := s.sessionPrincipal(w, r)
	return ModuleContext{Principal: p}, ok
}

func (s *Server) browserRoute(w http.ResponseWriter, r *http.Request) (ModuleContext, bool) {
	p, ok := principalFrom(r.Context())
	if !ok || p.Kind != auth.KindUser {
		s.writeError(w, r, auth.ErrUnauthenticated)
		return ModuleContext{}, false
	}
	return ModuleContext{Principal: p}, true
}

func (s *Server) tokenExchangeRoute(w http.ResponseWriter, r *http.Request) (ModuleContext, bool) {
	p, ok := principalFrom(r.Context())
	if !ok {
		writeOAuthError(w, http.StatusUnauthorized, "invalid_client", "authentication required")
	}
	return ModuleContext{Principal: p}, ok
}

func (s *Server) capabilitiesRoute(w http.ResponseWriter, r *http.Request) (ModuleContext, bool) {
	for name, value := range NoStoreResponseHeaders() {
		w.Header().Set(name, value)
	}
	return s.authenticatedRoute(w, r)
}

func (s *Server) authzenRoute(kind authzenSurfaceKind, perm auth.Permission) coreAdmission {
	if kind != azKindConfig && perm == "" {
		panic("api: AuthZEN route requires a permission")
	}
	return func(w http.ResponseWriter, r *http.Request) (ModuleContext, bool) {
		if !s.allowSurface(w, r, kind) {
			return ModuleContext{}, false
		}
		if kind == azKindConfig {
			return publicRoute(w, r)
		}
		return s.tenantRoute(perm)(w, r)
	}
}

func (s *Server) scimRoute(perm auth.Permission) coreAdmission {
	if perm == "" {
		panic("api: SCIM route requires a permission")
	}
	return func(w http.ResponseWriter, r *http.Request) (ModuleContext, bool) {
		p, tenant, err := s.scimAuthz(r, perm)
		if err != nil {
			writeSCIMError(w, *err)
		}
		return ModuleContext{Principal: p, Tenant: tenant}, err == nil
	}
}

func (s *Server) secretRoute(w http.ResponseWriter, r *http.Request) (ModuleContext, bool) {
	p, tenant, ok := s.secretScope(w, r)
	return ModuleContext{Principal: p, Tenant: tenant}, ok
}

func (s *Server) mcpManagementRoute(write bool) coreAdmission {
	return func(w http.ResponseWriter, r *http.Request) (ModuleContext, bool) {
		p, tenant, ok := s.mcpGatewayAdmission(w, r, write)
		return ModuleContext{Principal: p, Tenant: tenant}, ok
	}
}

func (s *Server) osAccountRoute(w http.ResponseWriter, r *http.Request) (ModuleContext, bool) {
	p, ok := s.osAccountPrincipal(w, r)
	return ModuleContext{Principal: p}, ok
}

func (s *Server) osAccountTargetRoute(w http.ResponseWriter, r *http.Request) (ModuleContext, bool) {
	p, tenant, id, ok := s.osAccountTarget(w, r)
	return ModuleContext{Principal: p, Tenant: tenant, Resource: auth.ResourceAttrs{ID: id.String()}}, ok
}

func (s *Server) metricsRoute(w http.ResponseWriter, r *http.Request) (ModuleContext, bool) {
	return ModuleContext{}, s.allowMetrics(w, r)
}
