// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// permLiveRead gates every live-operation read, including the SSE stream. Live
// operation is a privileged read (docs/SECURITY-HARDENING.md); the stream open is audited.
const permLiveRead auth.Permission = "sessions:live:read"

const (
	permWorkRead      auth.Permission = "sessions:work:read"
	permWorkWrite     auth.Permission = "sessions:work:write"
	permWorkAdmin     auth.Permission = "sessions:work:admin"
	permDecisionRead  auth.Permission = "sessions:decision:read"
	permDecisionWrite auth.Permission = "sessions:decision:write"
	permDecisionAdmin auth.Permission = "sessions:decision:admin"
	permLeaseRead     auth.Permission = "sessions:lease:read"
	permLeaseWrite    auth.Permission = "sessions:lease:write"
	permLeaseAdmin    auth.Permission = "sessions:lease:admin"

	permProtocolBindingRead  auth.Permission = "sessions:protocol-binding:read"
	permProtocolBindingWrite auth.Permission = "sessions:protocol-binding:write"
	permProtocolBindingAdmin auth.Permission = "sessions:protocol-binding:admin"
)

const (
	permChannelRead  auth.Permission = "sessions:channel:read"
	permChannelWrite auth.Permission = "sessions:channel:write"
	permChannelAdmin auth.Permission = "sessions:channel:admin"

	permMessageRead      auth.Permission = "sessions:message:read"
	permMessageWrite     auth.Permission = "sessions:message:write"
	permMessageAdmin     auth.Permission = "sessions:message:admin"
	permMessageSendWrite auth.Permission = auth.CommunicationSessionMessageSendWrite

	permDeliveryRead  auth.Permission = auth.CommunicationSessionDeliveryRead
	permDeliveryWrite auth.Permission = auth.CommunicationSessionDeliveryWrite
	permDeliveryAdmin auth.Permission = "sessions:delivery:admin"

	permDecisionRequestRead  auth.Permission = "sessions:decision-request:read"
	permDecisionRequestWrite auth.Permission = "sessions:decision-request:write"
	permDecisionRequestAdmin auth.Permission = "sessions:decision-request:admin"

	permHandoffRead          auth.Permission = "sessions:handoff:read"
	permHandoffWrite         auth.Permission = "sessions:handoff:write"
	permHandoffAdmin         auth.Permission = "sessions:handoff:admin"
	permHandoffResponseWrite auth.Permission = auth.CommunicationSessionHandoffResponseWrite

	permRouteRead  auth.Permission = "sessions:route:read"
	permRouteWrite auth.Permission = "sessions:route:write"
	permRouteAdmin auth.Permission = "sessions:route:admin"

	permSubscriptionRead  auth.Permission = "sessions:subscription:read"
	permSubscriptionWrite auth.Permission = "sessions:subscription:write"
	permSubscriptionAdmin auth.Permission = "sessions:subscription:admin"

	permEndpointRead  auth.Permission = "sessions:endpoint:read"
	permEndpointWrite auth.Permission = "sessions:endpoint:write"
	permEndpointAdmin auth.Permission = "sessions:endpoint:admin"
)

// communicationPermissionCatalog is the complete K3 (not K5) public catalog.
// The two route-specific write permissions are kept alongside the resource
// triads because they are part of communication-session's immutable ceiling.
var communicationPermissionCatalog = [...]auth.Permission{
	permChannelRead, permChannelWrite, permChannelAdmin,
	permMessageRead, permMessageWrite, permMessageAdmin, permMessageSendWrite,
	permDeliveryRead, permDeliveryWrite, permDeliveryAdmin,
	permDecisionRequestRead, permDecisionRequestWrite, permDecisionRequestAdmin,
	permHandoffRead, permHandoffWrite, permHandoffAdmin, permHandoffResponseWrite,
	permRouteRead, permRouteWrite, permRouteAdmin,
	permSubscriptionRead, permSubscriptionWrite, permSubscriptionAdmin,
	permEndpointRead, permEndpointWrite, permEndpointAdmin,
}

func communicationPermissions() []auth.Permission {
	return append([]auth.Permission(nil), communicationPermissionCatalog[:]...)
}

// communicationPermissionsReady validates the K3 slice inside the module's
// larger sessions catalog. Missing, duplicate and invented verbs on a K3
// resource all fail closed; unrelated K1/K2/runtime permissions are ignored.
func communicationPermissionsReady(declared []auth.Permission) bool {
	want := make(map[auth.Permission]struct{}, len(communicationPermissionCatalog))
	resources := make(map[string]struct{})
	for _, permission := range communicationPermissionCatalog {
		want[permission] = struct{}{}
		parts := strings.Split(string(permission), ":")
		resources[parts[1]] = struct{}{}
	}
	seen := make(map[auth.Permission]struct{}, len(want))
	for _, permission := range declared {
		parts := strings.Split(string(permission), ":")
		if len(parts) != 3 || parts[0] != Namespace {
			continue
		}
		if _, communicationResource := resources[parts[1]]; !communicationResource {
			continue
		}
		if _, expected := want[permission]; !expected {
			return false
		}
		if _, duplicate := seen[permission]; duplicate {
			return false
		}
		seen[permission] = struct{}{}
	}
	return len(seen) == len(want)
}

// APINamespace roots the module's routes at /v1/m/sessions/.
func (m *Module) APINamespace() string { return Namespace }

// Permissions declares the module's permissions so roles grant them by verb tier.
// The observe overlay's live-read, the operate tiers (run read/write/admin),
// the workspace-plane tiers (workspace read/write/admin), and the
// template tiers (template read/write/admin).
func (m *Module) Permissions() []auth.Permission {
	perms := append([]auth.Permission{permLiveRead}, runtimePermissions()...)
	perms = append(perms, workspacePermissions()...)
	perms = append(perms, templatePermissions()...)
	perms = append(perms, permWorkRead, permWorkWrite, permWorkAdmin,
		permDecisionRead, permDecisionWrite, permDecisionAdmin,
		permLeaseRead, permLeaseWrite, permLeaseAdmin,
		permProtocolBindingRead, permProtocolBindingWrite, permProtocolBindingAdmin)
	return append(perms, communicationPermissions()...)
}

// APIRoutes mounts the module's observe read/stream endpoints, the operate
// (managed-run lifecycle) endpoints, and the workspace-plane endpoints.
func (m *Module) APIRoutes(reg api.RouteRegistrar) {
	reg.Handle("GET", "/live", permLiveRead, m.handleListLive)
	reg.Handle("GET", "/live/{ref}", permLiveRead, m.handleGetLive)
	reg.Handle("GET", "/live/{ref}/timeline", permLiveRead, m.handleTimeline)
	reg.Handle("GET", "/stream", permLiveRead, m.handleStream)
	m.runtimeRoutes(reg)
	m.workspaceRoutes(reg)
	m.templateRoutes(reg)
	m.workRoutes(reg)
	m.protocolBindingRoutes(reg)
}

// handleListLive lists live sessions, most-recently-active first. The recency
// sort is a custom order (no keyset cursor); for "more", raise the limit. The
// Claude Code state is derived per row at read time.
func (m *Module) handleListLive(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(r)
	// Recency order is a custom sort, which the store forbids combining with a
	// keyset cursor; this endpoint paginates by raising the limit, so a
	// client-supplied cursor is ignored rather than rejected.
	q.Cursor = ""
	q.Sort = []model.Sort{{Column: colLastEventAt, Desc: true}}
	// cc_state is derived at read time (not a stored column), so an optional
	// cc_state filter is applied in-memory over the returned page.
	ccFilter := r.URL.Query().Get("cc_state")
	out := listResponse[liveDTO]{Items: []liveDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(liveKind)
		if err != nil {
			return err
		}
		recs, page, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			dto := m.toLiveDTO(rec)
			if ccFilter != "" && dto.CCState != ccFilter {
				continue
			}
			out.Items = append(out.Items, dto)
		}
		out.HasMore = page.HasMore
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleGetLive returns the full live operation of one session by its reference.
func (m *Module) handleGetLive(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	ref := chi.URLParam(r, "ref")
	if ref == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("session ref required"))
		return
	}
	var dto liveDTO
	found := false
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(liveKind)
		if err != nil {
			return err
		}
		recs, _, err := repo.List(r.Context(), model.Query{Filters: []model.Filter{eq(colSessionRef, ref)}, Limit: 1})
		if err != nil {
			return err
		}
		if len(recs) == 0 {
			return nil
		}
		found = true
		dto = m.toLiveDTO(recs[0])
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, errorBody("not found"))
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

// handleTimeline returns a session's reconstructable timeline in chronological
// (ingestion) order, keyset-paginated by the time-ordered row id.
func (m *Module) handleTimeline(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	ref := chi.URLParam(r, "ref")
	if ref == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("session ref required"))
		return
	}
	q := listQuery(r)
	q.Filters = append(q.Filters, eq(colTLSessionRef, ref))
	out := listResponse[timelineDTO]{Items: []timelineDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(timelineKind)
		if err != nil {
			return err
		}
		recs, page, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			out.Items = append(out.Items, toTimelineDTO(rec))
		}
		out.Cursor, out.HasMore = page.Cursor, page.HasMore
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// listQuery builds a List query from ?limit and ?cursor.
func listQuery(r *http.Request) model.Query {
	q := model.Query{}
	if c := r.URL.Query().Get("cursor"); c != "" {
		q.Cursor = c
	}
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil {
			q.Limit = n
		}
	}
	return q
}

// writeStoreError maps a store error to an HTTP status. Everything except this
// module's own not-found grouping is api.StoreErrorStatus
// (core/api/moduleerrors.go), the ONE mapping the whole product shares. Its
// residency-violation and not-leader arms used to live here and are gone from this
// function because the shared mapping now carries both, with the same status and
// the same sentence — this module was one of only two of thirty-six that had them,
// which is exactly the drift that mapping exists to end.
func writeStoreError(w http.ResponseWriter, err error) {
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, nil)
	case errors.Is(err, store.ErrNotFound), errors.Is(err, store.ErrUnknownEntity):
		// KEPT LOCAL, deliberately, and it is the arm that disagrees with the other
		// thirty-five. Aligned with core/api/errors.go, which answers 404 for BOTH.
		// This module used to fold ErrUnknownEntity in with the bad-query cases, so a
		// missing entity read as a client's malformed request; the shared mapping still
		// answers 400 there because twenty-three copies do and moving them is a
		// semantic decision about the sentinel, not a refactor (see moduleerrors.go).
		writeJSON(w, http.StatusNotFound, errorBody("not found"))
	default:
		status, msg, _ := api.StoreErrorStatus(err)
		writeJSON(w, status, errorBody(msg))
	}
}
