// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package catalog

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

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

// writeStoreError maps a store error to an HTTP status. Everything except this
// module's own conflict wording is api.StoreErrorStatus (core/api/moduleerrors.go),
// the ONE mapping the whole product shares — see the note there for what the
// thirty-six hand-written copies had drifted into.
func writeStoreError(w http.ResponseWriter, err error) {
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, nil)
	case errors.Is(err, store.ErrConflict):
		// KEPT LOCAL, and the wording is the reason. ErrConflict covers a unique-key
		// clash (a duplicate entry kind/slug/version, or a duplicate instance name)
		// AND an optimistic-concurrency version mismatch, so this module says both
		// out loud rather than the shared "conflict" — it must not name one cause.
		writeJSON(w, http.StatusConflict, errorBody("conflict: a resource with these unique fields already exists, or it was modified concurrently"))
	default:
		status, msg, _ := api.StoreErrorStatus(err)
		writeJSON(w, status, errorBody(msg))
	}
}

// isNotFound reports whether err is the store's not-found error.
func isNotFound(err error) bool { return errors.Is(err, store.ErrNotFound) }

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
func eq(col, val string) model.Filter {
	return model.Filter{Column: col, Op: model.OpEq, Value: val}
}

// decodeJSON reads a JSON body into v, bounding the read so a malformed or huge
// body cannot exhaust memory, and rejecting unknown fields (so a client cannot
// smuggle, e.g., a credential value into a reference-only field). It writes a 400
// and returns false on failure.
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

// validSlug reports whether s is a clean machine slug: a lowercase identifier with
// hyphens (a-z, 0-9, -, _), bounded in length.
func validSlug(s string) bool {
	if len(s) == 0 || len(s) > 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' && c != '_' {
			return false
		}
	}
	return true
}

// validSemver reports whether v is a semantic version: MAJOR.MINOR.PATCH numeric
// core with an optional -prerelease and +build (a pragmatic subset of semver.org,
// enough to order and pin registry artifacts).
func validSemver(v string) bool {
	if v == "" || len(v) > 64 {
		return false
	}
	// Strip build metadata (+...) then prerelease (-...).
	core := v
	if i := strings.IndexByte(core, '+'); i >= 0 {
		if !validIdentifierSet(core[i+1:], true) {
			return false
		}
		core = core[:i]
	}
	if i := strings.IndexByte(core, '-'); i >= 0 {
		if !validIdentifierSet(core[i+1:], false) {
			return false
		}
		core = core[:i]
	}
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return false
	}
	for _, p := range parts {
		if !numericNoLeadingZero(p) {
			return false
		}
	}
	return true
}

// numericNoLeadingZero reports whether p is a non-empty run of digits with no
// leading zero (per semver core identifiers).
func numericNoLeadingZero(p string) bool {
	if p == "" {
		return false
	}
	if len(p) > 1 && p[0] == '0' {
		return false
	}
	for i := 0; i < len(p); i++ {
		if p[i] < '0' || p[i] > '9' {
			return false
		}
	}
	return true
}

// validIdentifierSet reports whether s is a dot-separated set of semver
// pre-release/build identifiers (alphanumerics and hyphens).
func validIdentifierSet(s string, _ bool) bool {
	if s == "" {
		return false
	}
	for _, id := range strings.Split(s, ".") {
		if id == "" {
			return false
		}
		for i := 0; i < len(id); i++ {
			c := id[i]
			if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '-' {
				return false
			}
		}
	}
	return true
}
