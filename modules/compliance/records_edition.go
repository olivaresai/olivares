// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package compliance

import (
	"net/http"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
)

// Legal holds and the right to erasure with key shred (/holds, /erasure, /data-subjects and
// the governed Claude Files erase) are Business capabilities (docs/editions.md). The
// composition root passes WithBusinessRecords for a Business build, as it passes
// recording.WithBusinessEdition. Without it the module keeps the published routes and their
// permissions, and each answers 501 business_required after the role check. The rest of the
// records plane stays Community: retention schedules and sweeps, risk and residency. A hold a
// Business engine stored still vetoes every destruction path here (CheckHold reads the store,
// not these routes), so a Community engine never drops a preservation order.

// WithBusinessRecords enables legal holds and erasure; the Community composition omits it.
func WithBusinessRecords() Option {
	return func(m *Module) { m.businessRecords = true }
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
		"Legal holds and erasure require Olivares Business."))
}
