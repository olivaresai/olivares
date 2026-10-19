// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package recording

import (
	"net/http"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
)

// Recording privileged sessions (policy, consent, replay, export, seal) is a Business
// capability (docs/editions.md). The composition root passes WithBusinessEdition for a
// Business build, as it sets Dependencies.SteerOthers for the sessions module. Without it
// the module keeps its published routes and permissions, each answers 501 business_required
// after the role check (the same answer as break-glass and steering), and Gate records
// nothing: a surface never waits on evidence a Community engine cannot write. Stored
// sessions stay in the store for the Business edition to read.

// WithBusinessEdition enables recording; the Community composition omits it.
func WithBusinessEdition() Option {
	return func(m *Module) { m.business = true }
}

// businessOnly mounts every route with the Community refusal in place of its handler.
type businessOnly struct{ api.RouteRegistrar }

func (r businessOnly) Handle(method, pattern string, perm auth.Permission, _ api.ModuleHandler) {
	r.RouteRegistrar.Handle(method, pattern, perm, businessRequired)
}

func (r businessOnly) HandleEntity(method, pattern string, perm auth.Permission, entity api.EntityRef, _ api.ModuleHandler) {
	r.RouteRegistrar.HandleEntity(method, pattern, perm, entity, businessRequired)
}

func businessRequired(w http.ResponseWriter, _ *http.Request, _ api.ModuleContext) {
	writeJSON(w, http.StatusNotImplemented, api.ErrorBody("business_required",
		"Privileged-session recording requires Olivares Business."))
}
