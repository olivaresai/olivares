// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package compliance

import (
	"net/http"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
)

// Legal holds, the right to erasure with key shred (/holds, /erasure, /data-subjects and the
// governed Claude Files erase), and authoring retention schedules and sweeping under them are
// Business capabilities (docs/editions.md). The composition root passes WithBusinessRecords
// for a Business build, as it passes recording.WithBusinessEdition. Without it the module
// keeps the published routes and their permissions, and each answers 501 business_required
// after the role check; the engine's sweep destroys nothing, so a schedule a Business engine
// stored stays readable and inert. The rest of the records plane stays Community: reading the
// retention classes, schedules and certificates, risk and residency. A hold a Business engine
// stored still vetoes every destruction path here (CheckHold reads the store, not these
// routes), so a Community engine never drops a preservation order.

// WithBusinessRecords enables legal holds, erasure and retention schedules; the Community
// composition omits it.
func WithBusinessRecords() Option {
	return func(m *Module) { m.businessRecords = true }
}

// businessOnly mounts every route with the Community refusal, naming its feature, in place of
// its handler.
type businessOnly struct {
	api.RouteRegistrar
	message string
}

func (r businessOnly) Handle(method, pattern string, perm auth.Permission, _ api.ModuleHandler) {
	r.RouteRegistrar.Handle(method, pattern, perm, r.refuse)
}

func (r businessOnly) HandleEntity(method, pattern string, perm auth.Permission, entity api.EntityRef, _ api.ModuleHandler) {
	r.RouteRegistrar.HandleEntity(method, pattern, perm, entity, r.refuse)
}

func (r businessOnly) refuse(w http.ResponseWriter, _ *http.Request, _ api.ModuleContext) {
	writeJSON(w, http.StatusNotImplemented, api.ErrorBody("business_required", r.message))
}
