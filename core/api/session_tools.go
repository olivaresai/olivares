// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/auth"
)

// ServeSession serves r through the engine's own handler chain as p, a principal
// the session MCP edge already resolved from a session credential: one tenant,
// never superadmin, at most its launcher's role and assurance at launch, and no
// credential reference. The general authenticator still never admits a session
// bearer, and this entry admits nothing but such a principal. Every route then
// authorizes, confines, rate-limits, audits and refuses p exactly as it does any
// caller holding the same rights; a governed route stays undecided for it, since
// there is no credential to reconstruct.
func (s *Server) ServeSession(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	tenant := p.SessionScope()
	if p.Kind == "" || tenant.IsZero() || p.Superadmin || len(p.Tenants()) != 1 {
		s.writeError(w, r, auth.ErrUnauthenticated)
		return
	}
	// A fresh route context, as for every in-process loopback: the caller may be
	// served by chi itself, and chi would continue the outer match.
	ctx := context.WithValue(DetachRequestContext(r.Context()), chi.RouteCtxKey, nil)
	r = r.Clone(s.withStepUpPolicy(withPrincipal(ctx, p)))
	if r.Header == nil {
		r.Header = http.Header{}
	}
	// No header may name another credential or tenant: authenticate would replace p.
	r.Header.Del("Authorization")
	r.Header.Del("Cookie")
	r.Header.Del("X-Olivares-Session")
	r.Header.Set("X-Olivares-Tenant", tenant.String())
	s.handler.ServeHTTP(w, r)
}

// ModuleRequestSchema returns the JSON request-body schema that the module route
// method path publishes in the beta document (GET /openapi.beta.json).
func (s *Server) ModuleRequestSchema(method, path string) (json.RawMessage, bool) {
	op, _ := s.moduleOperation(method, path)
	raw, err := json.Marshal(op["requestBody"])
	var body struct {
		Content map[string]struct {
			Schema json.RawMessage `json:"schema"`
		} `json:"content"`
	}
	if err != nil || json.Unmarshal(raw, &body) != nil {
		return nil, false
	}
	schema := body.Content["application/json"].Schema
	return schema, len(schema) > 0
}

// ModuleRoutePublished reports whether the beta document publishes the module
// route method path, for a route such as a read that takes no request body.
func (s *Server) ModuleRoutePublished(method, path string) bool {
	_, ok := s.moduleOperation(method, path)
	return ok
}

// moduleOperation is the beta document's operation for method path. A module this
// node does not run publishes none: its routes answer module_not_enabled.
func (s *Server) moduleOperation(method, path string) (map[string]any, bool) {
	ns, _, _ := strings.Cut(strings.TrimPrefix(path, "/v1/m/"), "/")
	if slices.Contains(s.notEnabled, ns) {
		return nil, false
	}
	paths, _ := s.betaDocument()["paths"].(map[string]any)
	item, _ := paths[path].(map[string]any)
	op, ok := item[strings.ToLower(method)].(map[string]any)
	return op, ok
}
