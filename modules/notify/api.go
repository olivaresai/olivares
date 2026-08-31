// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package notify

import (
	"net/http"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// APINamespace roots the module's routes at /v1/m/notify/.
func (m *Module) APINamespace() string { return Namespace }

// Permissions declares the module's permissions so the built-in roles grant them
// by verb tier (viewer→read, editor→write, admin/owner→admin).
func (m *Module) Permissions() []auth.Permission {
	return []auth.Permission{permRouteRead, permRouteWrite, permRouteAdmin, permDeliveryRead}
}

// APIRoutes mounts the module's routes. The engine wraps each with authentication,
// tenant resolution and the declared permission check, and pins the data handle to
// the resolved tenant. Configuring routes/destinations is a privileged, audited
// action (docs/SECURITY-HARDENING.md), so create/update is write-tier and delete/test is admin-tier.
func (m *Module) APIRoutes(reg api.RouteRegistrar) {
	reg.Handle("GET", "/routes", permRouteRead, m.handleListRoutes)
	reg.Handle("POST", "/routes", permRouteWrite, m.handleCreateRoute)
	reg.Handle("GET", "/routes/{id}", permRouteRead, m.handleGetRoute)
	reg.Handle("PUT", "/routes/{id}", permRouteWrite, m.handleUpdateRoute)
	reg.Handle("DELETE", "/routes/{id}", permRouteAdmin, m.handleDeleteRoute)
	reg.Handle("POST", "/routes/{id}/test", permRouteAdmin, m.handleTestRoute)
	reg.Handle("GET", "/routes/{id}/revisions", permRouteRead, m.handleListRevisions)
	reg.Handle("POST", "/routes/{id}/restore", permRouteWrite, m.handleRestoreRoute)
	reg.Handle("POST", "/routes/evaluate", permRouteRead, m.handleEvaluateRoutes)

	reg.Handle("GET", "/match-types", permRouteRead, m.handleMatchTypes)
	reg.Handle("GET", "/destinations", permRouteRead, m.handleDestinations)
	reg.Handle("GET", "/deliveries", permDeliveryRead, m.handleListDeliveries)

	// Durable outbox: the DLQ view (?status=dead) is read-tier; requeuing
	// re-triggers external delivery, so it is admin-tier + audited.
	//
	// The requeue accepts ANY TERMINAL row, not only a dead-lettered one: `delivered` is
	// terminal too and handleRedeliverOutbox admits it deliberately (outbox_api.go:111-117
	// — a delivered row that the recipient never acted on is the ack-and-retry case). This
	// comment used to say "a dead-lettered delivery", which understated what the handler
	// below will actually do, and a console reading only the comment would hide a state
	// the engine serves.
	reg.Handle("GET", "/outbox", permDeliveryRead, m.handleListOutbox)
	reg.Handle("POST", "/outbox/{id}/redeliver", permRouteAdmin, m.handleRedeliverOutbox)
}

// destinationsResponse lists the provisioned destination names a route may target.
type destinationsResponse struct {
	Destinations []string `json:"destinations"`
}

// handleDestinations returns the provisioned destination names (from the wired
// transport). It never returns a credential. An empty list means no destination is
// configured yet (the dispatcher warns once at Start).
func (m *Module) handleDestinations(w http.ResponseWriter, _ *http.Request, mc api.ModuleContext) {
	// Scoped to the CALLER's tenant. It used to discard the module context and return
	// the global namespace, which told every tenant the names of every other tenant's
	// destinations — and those names are what a route addresses.
	dests := m.dispatch.DestinationsFor(mc.Tenant)
	if dests == nil {
		dests = []string{}
	}
	writeJSON(w, http.StatusOK, destinationsResponse{Destinations: dests})
}

// handleListDeliveries lists the append-only delivery ledger, optionally filtered
// by ?status, ?finding_kind, ?destination and ?route (a route id).
func (m *Module) handleListDeliveries(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(r)
	if s := r.URL.Query().Get("status"); s != "" {
		q.Filters = append(q.Filters, eq(colDelStatus, s))
	}
	if k := r.URL.Query().Get("finding_kind"); k != "" {
		q.Filters = append(q.Filters, eq(colDelKind, k))
	}
	if d := r.URL.Query().Get("destination"); d != "" {
		q.Filters = append(q.Filters, eq(colDelDestination, d))
	}
	if rt := r.URL.Query().Get("route"); rt != "" {
		q.Filters = append(q.Filters, eq(colDelRouteRef, rt))
	}
	out := []deliveryDTO{}
	var page model.Page
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(deliveryKind)
		if err != nil {
			return err
		}
		recs, p, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		page = p
		for _, rec := range recs {
			out = append(out, toDeliveryDTO(rec))
		}
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, listResponse[deliveryDTO]{Items: out, Cursor: page.Cursor, HasMore: page.HasMore})
}
