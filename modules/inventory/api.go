// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package inventory

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The module's permission: catalog:read gates the discovery catalog. The access
// graph (topology, the R/RW map and the permitted-vs-observed drift) is owned by
// module III (the access map), which exposes it as a privileged, audited
// read — inventory no longer serves it (decision A, 2026-06-03).
const permCatalogRead auth.Permission = "inventory:catalog:read"

// APINamespace returns the module's namespace; it equals the store namespace and
// roots the module's routes at /v1/m/inventory/.
func (m *Module) APINamespace() string { return Namespace }

// Permissions declares the permissions the module's routes require so the
// built-in roles grant them by verb tier (a viewer gets the read permissions).
func (m *Module) Permissions() []auth.Permission {
	return []auth.Permission{permCatalogRead}
}

// APIRoutes mounts the module's read endpoints. The engine wraps each with
// authentication, tenant resolution and the declared permission check before the
// handler runs, and pins the data handle to the resolved tenant.
func (m *Module) APIRoutes(reg api.RouteRegistrar) {
	reg.Handle("GET", "/summary", permCatalogRead, m.handleSummary)
	reg.Handle("GET", "/entities", permCatalogRead, m.handleListEntities)
	reg.Handle("GET", "/entities/{kind}/{id}", permCatalogRead, m.handleGetEntity)
	// The observation history is a collection SUBORDINATE to the (kind, id) pair,
	// mounted with Handle rather than HandleEntity: the path id is the core entity
	// id the catalog overlays, not the primary key of a stored inventory row, so
	// there is no row for an entity route to read lineage from. It carries the same
	// tenant-wide permission as the catalog it is a projection of (root ratified the
	// reuse, 2026-09-08); no source permission is inferred, and a workspace-confined
	// principal is refused the way it is on every inventory route, because these
	// tables declare no workspace lineage.
	reg.Handle("GET", "/entities/{kind}/{id}/observations", permCatalogRead, m.handleListEntityObservations)
}

// handleListEntities lists catalog entries, optionally filtered by kind and
// status, paginated by the default id keyset cursor.
func (m *Module) handleListEntities(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(r)
	if kind := r.URL.Query().Get("kind"); kind != "" {
		q.Filters = append(q.Filters, eq(colEntityKind, kind))
	}
	if status := r.URL.Query().Get("status"); status != "" {
		q.Filters = append(q.Filters, eq(colStatus, status))
	}
	out := listResponse[entryDTO]{Items: []entryDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(catalogEntryKind)
		if err != nil {
			return err
		}
		recs, page, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			out.Items = append(out.Items, toEntryDTO(rec))
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

// handleGetEntity returns one catalog entry plus a minimal projection of the
// core entity it overlays.
func (m *Module) handleGetEntity(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	kind := chi.URLParam(r, "kind")
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var out detailDTO
	found := false
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(catalogEntryKind)
		if err != nil {
			return err
		}
		recs, _, err := repo.List(r.Context(), model.Query{
			Filters: []model.Filter{eq(colEntityKind, kind), eq(colEntityID, id.String())},
			Limit:   1,
		})
		if err != nil {
			return err
		}
		if len(recs) == 0 {
			return nil
		}
		found = true
		out.Entry = toEntryDTO(recs[0])
		out.Detail = coreDetail(r.Context(), sc, kind, id)
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
	writeJSON(w, http.StatusOK, out)
}

// handleListEntityObservations lists the stored observation receipts that name one catalog
// entity, one item per distinct receipt in ascending receipt order, in pages of at most 25
// (?limit 1..25, ?cursor from the previous page): each item carries the receipt id, the event
// type, the registration snapshot recorded at reception (historical, not the source's current
// registration or health), the source-declared occurrence instant when declared, first and last
// reception of the same retained facts, the equal-facts delivery count and whether a conflicting
// redelivery is retained; names, references, labels, raw facts, hashes and event ids are
// withheld, a missing catalog entry is 404, and an empty page does not prove no observation.
//
// It is the HTTP half of a two-part module: everything about selection, validation
// and composition lives in listEntityObservations (provenance_read.go), so this
// function only turns the request into a validated query and the reader's outcome
// into a status. A page whose stored evidence cannot be proven consistent is a
// generic 500 for the whole response — never a shorter list — and the cause goes
// to the operator log, not to the client.
func (m *Module) handleListEntityObservations(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	kind := chi.URLParam(r, "kind")
	id, ok := canonicalNonzeroID(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	q, err := parseObservationQuery(r.URL.Query())
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody(err.Error()))
		return
	}
	out := listResponse[observationDTO]{Items: []observationDTO{}}
	err = mc.Data.View(r.Context(), func(sc store.Scope) error {
		page, err := listEntityObservations(r.Context(), sc, kind, id, q)
		if err != nil {
			return err
		}
		out.Items = append(out.Items, page.Items...)
		out.Cursor, out.HasMore = page.Cursor, page.HasMore
		return nil
	})
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, out)
	case errors.Is(err, errObservationEntityAbsent):
		writeJSON(w, http.StatusNotFound, errorBody("not found"))
	case errors.Is(err, errObservationEvidence), errors.Is(err, errObservationProjectorUnavailable):
		// The stored evidence, or the store's capability, is the problem — not the
		// request. The client gets the generic sentence; the reason (receipt id and
		// the invariant that failed, never facts or secrets) goes to the operator.
		m.warnf("inventory: observation history refused", "tenant", mc.Tenant.String(),
			"kind", kind, "entity_id", id.String(), "err", err.Error())
		writeJSON(w, http.StatusInternalServerError, errorBody("internal error"))
	default:
		writeStoreError(w, err)
	}
}

// handleSummary returns the estate overview: counts by entity kind and by signal
// source. It aggregates client-side over a bounded page (the store has no COUNT
// primitive); a tenant whose catalog exceeds the cap is reported as truncated.
func (m *Module) handleSummary(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	out := summaryDTO{ByKind: map[string]*kindCount{}, BySource: map[string]int{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(catalogEntryKind)
		if err != nil {
			return err
		}
		recs, page, err := repo.List(r.Context(), model.Query{Limit: listCap})
		if err != nil {
			return err
		}
		out.Truncated = page.HasMore
		for _, rec := range recs {
			kind := rec.String(colEntityKind)
			kc := out.ByKind[kind]
			if kc == nil {
				kc = &kindCount{}
				out.ByKind[kind] = kc
			}
			kc.Total++
			if rec.String(colStatus) == statusStale {
				kc.Stale++
			} else {
				kc.Active++
			}
			out.Total++
			for _, s := range parseSet(rec.String(colSignalSources)) {
				out.BySource[s]++
			}
		}
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// coreDetail projects the underlying core entity of a catalog entry to a small,
// non-sensitive map for the detail view. An unknown kind or a missing entity
// yields nil (the catalog entry alone is still returned).
func coreDetail(ctx context.Context, sc store.Scope, kind string, id model.ID) map[string]any {
	switch kind {
	case kindSession:
		if s, err := sc.Sessions().Get(ctx, id); err == nil {
			return map[string]any{
				"external_id": s.ExternalID, "state": string(s.State), "goal": s.Goal,
				"started_at": s.StartedAt.String(),
			}
		}
	case kindAgent:
		if a, err := sc.Agents().Get(ctx, id); err == nil {
			return map[string]any{"name": a.Name, "kind": a.Kind, "external_id": a.ExternalID, "status": string(a.Status)}
		}
	case kindIdentity:
		if i, err := sc.Identities().Get(ctx, id); err == nil {
			return map[string]any{"name": i.Name, "kind": i.Kind, "external_id": i.ExternalID, "provider": i.Provider}
		}
	case kindMCPServer:
		if s, err := sc.MCPServers().Get(ctx, id); err == nil {
			return map[string]any{"name": s.Name, "transport": s.Transport, "status": string(s.Status)}
		}
	case kindTool:
		if t, err := sc.Tools().Get(ctx, id); err == nil {
			return map[string]any{"name": t.Name, "read_only_hint": t.ReadOnlyHint, "destructive_hint": t.DestructiveHint}
		}
	case kindResource:
		if rs, err := sc.Resources().Get(ctx, id); err == nil {
			return map[string]any{"name": rs.Name, "kind": rs.Kind, "uri": rs.URI}
		}
	case kindSkill:
		if s, err := sc.Skills().Get(ctx, id); err == nil {
			return map[string]any{"name": s.Name, "source": s.Source, "status": string(s.Status)}
		}
	case kindModel:
		if md, err := sc.Models().Get(ctx, id); err == nil {
			return map[string]any{"name": md.Name, "family": md.Family}
		}
	case kindProvider:
		if p, err := sc.Providers().Get(ctx, id); err == nil {
			return map[string]any{"name": p.Name, "kind": p.Kind, "status": string(p.Status)}
		}
	}
	return nil
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

// errorBody is the small error envelope module endpoints return.
func errorBody(msg string) map[string]any {
	return map[string]any{"error": map[string]string{"message": msg}}
}

// writeStoreError maps a store error to an HTTP status. THE MAPPING ITSELF IS NOT
// HERE: it is api.StoreErrorStatus (core/api/moduleerrors.go), which derives the
// status from the same statusFor that answers core/api's own routes. This module
// therefore cannot answer a sentinel differently from core, or from the other
// thirty-five copies of this function, and a sentinel added to statusFor tomorrow
// reaches this module without anyone editing it.
//
// That is not hypothetical: on 2026-08-12 four sentinels core/api had long mapped —
// tenant_suspended, tenant_not_in_service, not_leader and residency_violation —
// were absent from all but two of the thirty-six copies, so the same refusal was
// answered 423/503/403 by a core route and 500 "internal error" by every module
// route. The per-arm reasoning (ADR-0024 Q2 for the audit spool/B-03 for
// workspace confinement for the standby) now lives beside statusFor, once.
func writeStoreError(w http.ResponseWriter, err error) {
	if err == nil {
		writeJSON(w, http.StatusOK, nil)
		return
	}
	status, msg, _ := api.StoreErrorStatus(err)
	writeJSON(w, status, errorBody(msg))
}
