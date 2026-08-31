// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// listCap bounds an internal List page; it matches the store's own maximum.
const listCap = 1000

// listResponse is the paginated envelope every list endpoint returns: the ONE
// engine-wide shape (items + opaque cursor + has_more), aliased rather than
// re-declared so an empty page can never serialize as `{"items":null}` here
// while it serializes as `{"items":[]}` next door (core/api/listresponse.go).
type listResponse[T any] = api.ListResponse[T]

// writeJSON writes v as a JSON response. Modules cannot reach the core API's
// unexported render helper, so each module owns a tiny equivalent.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

// errorBody is the small error envelope module endpoints return.
func errorBody(msg string) map[string]any {
	return map[string]any{"error": map[string]string{"message": msg}}
}

// errorBodyCode is errorBody with a stable machine-readable code (the core API
// envelope shape), for denials a client routes on (e.g. step_up_required).
func errorBodyCode(code, msg string) map[string]any {
	return map[string]any{"error": map[string]string{"code": code, "message": msg}}
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

// isConflict reports whether err is the store's optimistic-concurrency / unique
// conflict (both surface as store.ErrConflict).
func isConflict(err error) bool { return errors.Is(err, store.ErrConflict) }

// isNotFound reports whether err is the store's not-found sentinel.
func isNotFound(err error) bool { return errors.Is(err, store.ErrNotFound) }

// decodeJSON reads a JSON body into v, bounding the read so a malformed or huge
// body cannot exhaust memory, and rejecting unknown fields so a client cannot
// smuggle a value into a field the typed DTO does not declare. It returns false
// (and writes a 400) on failure.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid JSON body"))
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
		writeJSON(w, http.StatusBadRequest, errorBody("invalid JSON body"))
		return false
	}
	return true
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

// eq is a shorthand for an equality filter.
func eq(col string, val any) model.Filter {
	return model.Filter{Column: col, Op: model.OpEq, Value: val}
}

// validationError is a validation failure raised from inside a Mutate closure
// (where the body is validated against a loaded record), mapped to a 400 by the
// caller. Distinct from a store error so the two map to different statuses.
type validationError string

func (e validationError) Error() string { return string(e) }

// asValidation returns (msg, true) when err is a validationError.
func asValidation(err error) (string, bool) {
	var v validationError
	if errors.As(err, &v) {
		return string(v), true
	}
	return "", false
}

// auditEvent appends a governance self-audit event attributed to the REAL
// principal, in the caller's transaction, so the ledger records WHO did what
// (docs/SECURITY-HARDENING.md) — never the system actor and never an email/secret.
func auditEvent(ctx context.Context, sc store.Scope, mc api.ModuleContext, action string, kind model.Kind, id model.ID, meta map[string]any) error {
	_, err := sc.Audit().Append(ctx, model.AuditDraft{
		Actor:      mc.Principal.Actor(),
		ActorKind:  mc.Principal.ActorKind(),
		Action:     action,
		TargetKind: kind,
		TargetID:   id,
		Meta:       meta,
	})
	return err
}

// findOne returns the first row matching the AND of filters, or ok=false. The
// engine validates each filter column against the descriptor (unknown column
// rejected) and binds the value. Use it for existence/lookup checks.
func findOne(ctx context.Context, repo store.GenericRepo, filters ...model.Filter) (model.Record, bool, error) {
	list, _, err := repo.List(ctx, model.Query{Filters: filters, Limit: 1})
	if err != nil {
		return nil, false, err
	}
	if len(list) == 0 {
		return nil, false, nil
	}
	return list[0], true, nil
}

// listAll drains every page of a module-entity query (bounded internally by
// listCap per page), so a count/scan over a small owned set is complete.
func listAll(ctx context.Context, repo store.GenericRepo, filters ...model.Filter) ([]model.Record, error) {
	var out []model.Record
	q := model.Query{Filters: filters, Limit: listCap}
	for {
		recs, page, err := repo.List(ctx, q)
		if err != nil {
			return nil, err
		}
		out = append(out, recs...)
		if !page.HasMore || page.Cursor == "" {
			return out, nil
		}
		q.Cursor = page.Cursor
	}
}
