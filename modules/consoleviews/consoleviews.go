// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package consoleviews owns SAVED CONSOLE VIEWS: named, shareable
// snapshots of a console view's URL-state (filters, ranges, selections) so an
// operator can save an investigation ("failed admissions last 24h") and a team
// can share it — the Temporal saved-searches pattern, server-side so views
// survive the browser and cross machines.
//
// It is deliberately console FURNITURE, not a governance plane: it stores only
// the parameters the console would put in a URL (a size-capped JSON object),
// never query results, and the console treats stored params strictly as data.
// Ownership is the audit actor ("user:<id>"/"token:<id>"); a view is visible to
// its owner and, when shared, to every reader in the tenant. Mutation is
// owner-only; tenant admins may delete any view (cleanup after offboarding).
// Every write is recorded in the tenant's audit ledger.
package consoleviews

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk"
)

// Name is the module's globally unique identifier.
const Name = "olivares.consoleviews"

// Namespace roots the module's routes at /v1/m/consoleviews/.
const Namespace = "consoleviews"

// Permissions: read lists/gets views (viewer+); write creates/updates/deletes
// own views (editor+). Delete-any is NOT a separate permission — it is the
// admin/owner ROLE acting through the write route (see handleDelete).
const (
	permViewRead  auth.Permission = "consoleviews:view:read"
	permViewWrite auth.Permission = "consoleviews:view:write"
)

// SavedViewKind is the saved-view entity.
const SavedViewKind model.Kind = "consoleviews.saved_view"

const savedViewTable = "consoleviews_saved_view"

// Saved-view columns.
const (
	colFeature = "feature_id"
	colName    = "name"
	colDesc    = "description"
	colParams  = "params"
	colOwner   = "owner"
	colShared  = "shared"
)

// Bounds. The caps make the tenant's saved-view cardinality small by
// construction, so list handlers may drain all pages (bounded work) and the
// authoring path can refuse honestly instead of growing without limit.
const (
	maxParamsBytes = 4096
	maxNameLen     = 120
	maxDescLen     = 500
	maxPerOwner    = 200
	maxPerTenant   = 2000
	listCap        = 1000
)

// featureIDPattern keeps feature ids to console-registry-shaped slugs. The
// server intentionally does NOT pin the console's feature list (the registry is
// the authority and changes per release); the console ignores unknown ids.
var featureIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// Module is the saved-console-views module.
type Module struct {
	log *slog.Logger
}

// Compile-time proofs.
var (
	_ sdk.Module = (*Module)(nil)
	_ api.Module = (*Module)(nil)
)

// New returns the consoleviews module.
func New() *Module { return &Module{} }

// Descriptor returns the module's self-description.
func (m *Module) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeModule,
		Title:       "Saved console views",
		Description: "Named, shareable snapshots of console view state (filters, ranges) stored server-side per tenant: save an investigation, share it with the team. Params are one JSON object capped at 4096 bytes, validated as JSON and not against a schema. Edits are owner-only; a non-confined tenant admin or superadmin may also delete, which is how views left behind by departed users get cleaned up. Every write is audited.",
	}
}

// Init keeps the host logger; it subscribes to nothing.
func (m *Module) Init(_ context.Context, host sdk.Host) error {
	m.log = host.Logger()
	return nil
}

// Start / Stop are no-ops (no owned goroutines).
func (m *Module) Start(context.Context) error { return nil }
func (m *Module) Stop(context.Context) error  { return nil }

// APINamespace roots the routes.
func (m *Module) APINamespace() string { return Namespace }

// Permissions declares the module's permissions.
func (m *Module) Permissions() []auth.Permission {
	return []auth.Permission{permViewRead, permViewWrite}
}

// RegisterSchema declares the saved-view entity. The unique index leads with
// tenant_id and makes (feature, owner, name) a natural key so "save as"
// conflicts surface as 409, never as silent duplicates.
func (m *Module) RegisterSchema(reg store.ExtensionRegistry) error {
	return reg.Register(model.EntityDescriptor{
		Kind:  SavedViewKind,
		Table: savedViewTable,
		Fields: []model.FieldSpec{
			{Name: colFeature, Kind: model.KindText, Indexed: true},
			{Name: colName, Kind: model.KindText},
			{Name: colDesc, Kind: model.KindText, Nullable: true},
			{Name: colParams, Kind: model.KindText},
			{Name: colOwner, Kind: model.KindText, Indexed: true},
			{Name: colShared, Kind: model.KindBool, Indexed: true},
		},
		Indexes: []model.IndexSpec{{
			Name: "consoleviews_saved_view_uniq", Columns: []string{model.ColTenantID, colFeature, colOwner, colName}, Unique: true,
		}},
	})
}

// APIRoutes mounts the CRUD surface. The engine wraps each route with auth,
// tenant resolution and the permission check, and pins the data handle.
func (m *Module) APIRoutes(reg api.RouteRegistrar) {
	reg.Handle("GET", "/views", permViewRead, m.handleList)
	reg.Handle("GET", "/views/{id}", permViewRead, m.handleGet)
	reg.Handle("POST", "/views", permViewWrite, m.handleCreate)
	reg.Handle("PUT", "/views/{id}", permViewWrite, m.handleUpdate)
	reg.Handle("DELETE", "/views/{id}", permViewWrite, m.handleDelete)
}

// savedViewDTO is a saved view as the console consumes it. Params round-trips
// verbatim (it is the view's own URL-state); Mine is computed per caller.
type savedViewDTO struct {
	ID          string          `json:"id"`
	FeatureID   string          `json:"feature_id"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Params      json.RawMessage `json:"params"`
	Owner       string          `json:"owner"`
	Shared      bool            `json:"shared"`
	Mine        bool            `json:"mine"`
	CreatedAt   string          `json:"created_at"`
	UpdatedAt   string          `json:"updated_at"`
}

// savedViewInput is the writable subset of a saved view.
type savedViewInput struct {
	FeatureID   string          `json:"feature_id"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Params      json.RawMessage `json:"params"`
	Shared      bool            `json:"shared"`
}

func toDTO(rec model.Record, caller string) savedViewDTO {
	return savedViewDTO{
		ID:          rec.String(model.ColID),
		FeatureID:   rec.String(colFeature),
		Name:        rec.String(colName),
		Description: rec.String(colDesc),
		Params:      json.RawMessage(rec.String(colParams)),
		Owner:       rec.String(colOwner),
		Shared:      rec.Bool(colShared),
		Mine:        rec.String(colOwner) == caller,
		CreatedAt:   rec.String(model.ColCreatedAt),
		UpdatedAt:   rec.String(model.ColUpdatedAt),
	}
}

// validate normalizes and validates the writable fields, returning a
// human-readable refusal or "".
func (in *savedViewInput) validate() string {
	in.FeatureID = strings.TrimSpace(in.FeatureID)
	in.Name = strings.TrimSpace(in.Name)
	in.Description = strings.TrimSpace(in.Description)
	switch {
	case !featureIDPattern.MatchString(in.FeatureID):
		return "feature_id must be a lowercase slug (max 64 chars)"
	case in.Name == "" || len(in.Name) > maxNameLen:
		return "name is required (max 120 chars)"
	case len(in.Description) > maxDescLen:
		return "description too long (max 500 chars)"
	case len(in.Params) == 0 || len(in.Params) > maxParamsBytes:
		return "params is required (JSON object, max 4096 bytes)"
	}
	trimmed := strings.TrimSpace(string(in.Params))
	if !strings.HasPrefix(trimmed, "{") || !json.Valid([]byte(trimmed)) {
		return "params must be a JSON object"
	}
	in.Params = json.RawMessage(trimmed)
	return ""
}

// visible reports whether the caller may see the record: their own, or shared.
func visible(rec model.Record, caller string) bool {
	return rec.String(colOwner) == caller || rec.Bool(colShared)
}

// handleList returns the caller's own views plus the tenant's shared views,
// optionally scoped with ?feature_id=. Visibility is pushed down as two
// indexed queries (owner = caller; shared = true) rather than draining the
// whole tenant and filtering in Go — a plain viewer must never force the
// server to materialize other users' private views (adversarial review).
func (m *Module) handleList(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var filters []model.Filter
	if v := strings.TrimSpace(r.URL.Query().Get("feature_id")); v != "" {
		filters = append(filters, eq(colFeature, v))
	}
	caller := mc.Principal.Actor()
	out := struct {
		Items api.JSONArray[savedViewDTO] `json:"items"`
	}{Items: []savedViewDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		own, err := drain(r.Context(), sc, append([]model.Filter{eq(colOwner, caller)}, filters...)...)
		if err != nil {
			return err
		}
		shared, err := drain(r.Context(), sc, append([]model.Filter{eqBool(colShared, true)}, filters...)...)
		if err != nil {
			return err
		}
		seen := make(map[string]bool, len(own)+len(shared))
		for _, rec := range append(own, shared...) {
			id := rec.String(model.ColID)
			if seen[id] {
				continue // an own shared view matches both queries
			}
			seen[id] = true
			out.Items = append(out.Items, toDTO(rec, caller))
		}
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	// Newest-touched first; stable on equal timestamps for deterministic pages.
	sort.SliceStable(out.Items, func(i, j int) bool {
		if out.Items[i].UpdatedAt != out.Items[j].UpdatedAt {
			return out.Items[i].UpdatedAt > out.Items[j].UpdatedAt
		}
		return out.Items[i].ID < out.Items[j].ID
	})
	writeJSON(w, http.StatusOK, out)
}

// handleGet returns one view. A view the caller may not see is a 404, not a
// 403 — visibility must not leak existence (mirrors the store's not-found ==
// other-tenant rule).
func (m *Module) handleGet(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	caller := mc.Principal.Actor()
	var out savedViewDTO
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(SavedViewKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		if !visible(rec, caller) {
			return store.ErrNotFound
		}
		out = toDTO(rec, caller)
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleCreate saves a new view owned by the caller. Caps are enforced in the
// same transaction as the insert; the (feature, owner, name) natural key
// refuses duplicates with a clear 409 (DB unique index as backstop). The caps
// are SOFT under concurrent Postgres writers (READ COMMITTED: two concurrent
// creates can each observe count < cap) — bounded marginal overshoot, while
// duplicate names stay hard-refused by the unique index.
func (m *Module) handleCreate(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var in savedViewInput
	if !decodeJSON(w, r, &in) {
		return
	}
	if msg := in.validate(); msg != "" {
		writeJSON(w, http.StatusBadRequest, errorBody(msg))
		return
	}
	caller := mc.Principal.Actor()
	var out savedViewDTO
	var refuse refusal
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		all, err := drain(r.Context(), sc)
		if err != nil {
			return err
		}
		mine := 0
		for _, rec := range all {
			if rec.String(colOwner) == caller {
				mine++
				if rec.String(colFeature) == in.FeatureID && rec.String(colName) == in.Name {
					refuse = refusal{http.StatusConflict, "a saved view with this name already exists for this feature"}
					return errRefused
				}
			}
		}
		if len(all) >= maxPerTenant {
			refuse = refusal{http.StatusUnprocessableEntity, "tenant saved-view cap reached (2000)"}
			return errRefused
		}
		if mine >= maxPerOwner {
			refuse = refusal{http.StatusUnprocessableEntity, "per-user saved-view cap reached (200)"}
			return errRefused
		}
		repo, err := sc.Ext(SavedViewKind)
		if err != nil {
			return err
		}
		rec, err := repo.Create(r.Context(), model.Record{
			colFeature: in.FeatureID, colName: in.Name, colDesc: in.Description,
			colParams: string(in.Params), colOwner: caller, colShared: in.Shared,
		})
		if err != nil {
			return err
		}
		out = toDTO(rec, caller)
		return m.audit(r.Context(), sc, mc, "consoleviews.view.create", rec)
	})
	if errors.Is(err, errRefused) {
		writeJSON(w, refuse.code, errorBody(refuse.msg))
		return
	}
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

// handleUpdate replaces the writable fields of the caller's OWN view.
// feature_id is immutable (a view's params only make sense on its feature).
// A visible-but-not-owned view refuses with 403; an invisible one is 404.
func (m *Module) handleUpdate(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	var in savedViewInput
	if !decodeJSON(w, r, &in) {
		return
	}
	if msg := in.validate(); msg != "" {
		writeJSON(w, http.StatusBadRequest, errorBody(msg))
		return
	}
	caller := mc.Principal.Actor()
	var out savedViewDTO
	var refuse refusal
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(SavedViewKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		if !visible(rec, caller) {
			return store.ErrNotFound
		}
		if rec.String(colOwner) != caller {
			refuse = refusal{http.StatusForbidden, "only the owner can edit a saved view"}
			return errRefused
		}
		if in.FeatureID != rec.String(colFeature) {
			refuse = refusal{http.StatusBadRequest, "feature_id is immutable"}
			return errRefused
		}
		rec[colName] = in.Name
		rec[colDesc] = in.Description
		rec[colParams] = string(in.Params)
		rec[colShared] = in.Shared
		rec, err = repo.Update(r.Context(), rec)
		if err != nil {
			return err
		}
		out = toDTO(rec, caller)
		return m.audit(r.Context(), sc, mc, "consoleviews.view.update", rec)
	})
	if errors.Is(err, errRefused) {
		writeJSON(w, refuse.code, errorBody(refuse.msg))
		return
	}
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleDelete removes a view: the owner always may; a tenant admin/owner (or
// superadmin) may delete ANY view — the cleanup power for views left behind by
// departed users. A workspace-CONFINED admin is excluded from the tenant-wide
// override (defense-in-depth: the scoped-grant engine already forbids confined
// writes at the gate, but this module must not depend on that upstream forbid
// alone for a cross-workspace destructive power). Invisible views 404 for
// non-admin callers.
func (m *Module) handleDelete(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	caller := mc.Principal.Actor()
	role, _ := mc.Principal.RoleIn(mc.Tenant)
	_, confined := mc.Principal.ConfinedWorkspaceIn(mc.Tenant)
	isAdmin := mc.Principal.Superadmin ||
		((role == auth.RoleAdmin || role == auth.RoleOwner) && !confined)
	var refuse refusal
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(SavedViewKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		if !isAdmin {
			if !visible(rec, caller) {
				return store.ErrNotFound
			}
			if rec.String(colOwner) != caller {
				refuse = refusal{http.StatusForbidden, "only the owner or a tenant admin can delete a saved view"}
				return errRefused
			}
		}
		if err := repo.Delete(r.Context(), model.ID(rec.String(model.ColID))); err != nil {
			return err
		}
		return m.audit(r.Context(), sc, mc, "consoleviews.view.delete", rec)
	})
	if errors.Is(err, errRefused) {
		writeJSON(w, refuse.code, errorBody(refuse.msg))
		return
	}
	if err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// audit records a saved-view write in the tenant ledger, attributed to the real
// principal. Meta carries identifying fields only — never the params content.
func (m *Module) audit(ctx context.Context, sc store.Scope, mc api.ModuleContext, action string, rec model.Record) error {
	_, err := sc.Audit().Append(ctx, model.AuditDraft{
		Actor: mc.Principal.Actor(), ActorKind: mc.Principal.ActorKind(), Action: action,
		TargetKind: SavedViewKind, TargetID: model.ID(rec.String(model.ColID)),
		Meta: map[string]any{
			"feature_id": rec.String(colFeature), "name": rec.String(colName),
			"shared": rec.Bool(colShared), "owner": rec.String(colOwner),
		},
	})
	return err
}

// drain lists ALL saved views matching filters by walking keyset pages. The
// per-tenant cap makes this bounded work (≤ maxPerTenant rows) by construction.
func drain(ctx context.Context, sc store.Scope, filters ...model.Filter) ([]model.Record, error) {
	repo, err := sc.Ext(SavedViewKind)
	if err != nil {
		return nil, err
	}
	var all []model.Record
	q := model.Query{Filters: filters, Limit: listCap}
	for {
		recs, page, err := repo.List(ctx, q)
		if err != nil {
			return nil, err
		}
		all = append(all, recs...)
		if !page.HasMore || page.Cursor == "" {
			return all, nil
		}
		q.Cursor = page.Cursor
	}
}

// refusal is a handler-level refusal surfaced through the errRefused sentinel
// so the transaction rolls back with a clean, non-store status.
type refusal struct {
	code int
	msg  string
}

var errRefused = errors.New("consoleviews: refused")

// eq is a shorthand for a string equality filter.
func eq(col, val string) model.Filter {
	return model.Filter{Column: col, Op: model.OpEq, Value: val}
}

// eqBool is a shorthand for a boolean equality filter.
func eqBool(col string, val bool) model.Filter {
	return model.Filter{Column: col, Op: model.OpEq, Value: val}
}

// decodeJSON reads a JSON body into v, bounding the read so a malformed or huge
// body cannot exhaust memory. It returns false (and writes a 400) on failure.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid request body"))
		return false
	}
	// A BODY IS ONE JSON DOCUMENT (2026-08-06). Decode reads the FIRST value and stops,
	// so `{...}{...}` used to decode the first, silently discard the rest and perform a
	// durable mutation returning 201. Measured against a live engine on the models route,
	// with the created row read back by a separate GET; core/api/render.go has rejected
	// this since it was written, and 21 of the 22 copies of this helper had drifted from
	// it. A concatenation error becomes an apparently correct action, and two layers can
	// disagree about which document the request meant.
	if dec.More() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid request body"))
		return false
	}
	return true
}

// errorBody is the small error envelope module endpoints return.
func errorBody(msg string) map[string]any {
	return map[string]any{"error": map[string]string{"message": msg}}
}

// writeJSON writes v as a JSON response.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
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
