// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"net/http"
	"strings"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// SystemRouteRegistrar is the optional door for a composition-root module that
// manages deployment-wide resources. It uses the console's system:admin gate,
// requires a superadmin, ignores tenant selection, and pins data to the system
// audit ledger. It cannot delegate host authority to a tenant role or grant.
// Handlers enforce any additional assurance floor before an effect.
// All responses, including authentication refusals, carry Cache-Control: no-store.
type SystemRouteRegistrar interface {
	HandleSystem(method, pattern string, handler ModuleHandler)
}

func (cr chiRegistrar) HandleSystem(method, pattern string, h ModuleHandler) {
	cr = cr.withResponseHeaders(NoStoreResponseHeaders())
	method = strings.ToUpper(method)
	cr.declareRouteResponseHeaders(method, pattern)
	cr.r.MethodFunc(method, pattern, func(w http.ResponseWriter, r *http.Request) {
		for name, value := range cr.responseHeaders {
			w.Header().Set(name, value)
		}
		p, ok := cr.s.authzSystem(w, r, "system:admin")
		if !ok {
			return
		}
		// Keep the system role explicit even if a policy adapter someday delegates a permission.
		if !p.Superadmin {
			cr.s.writeError(w, r, errForbidden)
			return
		}
		h(w, r, ModuleContext{Principal: p, Tenant: model.SystemTenantID,
			Resource: auth.ResourceFor("system:admin"), Data: NewScopedData(cr.s.st, model.SystemTenantID), Standing: cr.s.standing})
	})
}
func (r recordingRegistrar) HandleSystem(method, pattern string, _ ModuleHandler) {
	*r.out = append(*r.out, moduleRoute{ns: r.ns, method: strings.ToUpper(method), pattern: pattern, perm: "system:admin", system: true})
}
