// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package compliance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

// listCap bounds an internal List page; it matches the store's own maximum.
const listCap = 1000

// Bounds on caller-supplied strings (docs/SECURITY-HARDENING.md — minimal data). Every value is
// clamped before it reaches Create so a ref, label or note cannot grow unbounded.
const (
	maxNameLen  = 200
	maxRefLen   = 1024
	maxNoteLen  = 2048
	maxReqBytes = 1 << 20 // 1 MiB cap on a request body
)

// listResponse is the paginated envelope every list endpoint returns: the ONE
// engine-wide shape (items + opaque cursor + has_more), aliased rather than
// re-declared so an empty page can never serialize as `{"items":null}` here
// while it serializes as `{"items":[]}` next door (core/api/listresponse.go).
type listResponse[T any] = api.ListResponse[T]

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

func writeCSV(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(body))
}

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

// decodeOptionalJSON decodes a body that the route allows to be ABSENT, and decides
// "absent" by reading rather than by trusting Content-Length. It returns false only
// when it has already written an error response.
//
// WHY IT EXISTS (raised by the the model contrast). The CCM snapshot route
// gated its decode on `r.ContentLength > 0`, and Go reports an unknown length as
// **-1**, not 0 — a chunked or streamed request carrying
// `{"frameworks":["eu_ai_act"]}` therefore skipped decoding entirely and the handler
// fell through to "no selection", which on that route means EVERY catalog framework.
// The operator narrowed the scope, the engine widened it, and the answer was 201: a
// governed action that succeeds and does something else, which is the defect class
// this module has been paying for all week.
//
// The five sibling call sites in this module already guard with `!= 0`, which does
// not have the bug, so the fix is not a new convention — it is one outlier brought
// in line, plus the one case `!= 0` still gets wrong: an unknown-length request whose
// body really is empty would reach the decoder and be rejected as invalid JSON. Here
// io.EOF on the FIRST read is the only thing that means "no body"; every other error
// is a malformed document and is rejected, and unknown fields and trailing documents
// stay rejected exactly as in decodeJSON.
func decodeOptionalJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if r.Body == nil {
		return true
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, maxReqBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		if errors.Is(err, io.EOF) {
			// No document at all. The caller keeps its zero value, which is what
			// "the operator did not narrow anything" means on these routes.
			return true
		}
		writeJSON(w, http.StatusBadRequest, errorBody("invalid JSON body"))
		return false
	}
	// A BODY IS ONE JSON DOCUMENT — same rule as decodeJSON, same reason.
	if dec.More() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid JSON body"))
		return false
	}
	return true
}

func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(io.LimitReader(r.Body, maxReqBytes))
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

func eq(col string, val any) model.Filter {
	return model.Filter{Column: col, Op: model.OpEq, Value: val}
}

// listAll pages a GenericRepo (module ext entity) fully via the keyset cursor so an
// aggregation is never silently truncated at the store's max page.
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

// pageCount lists one bounded page of a typed Repository and reports the row count
// and whether more rows exist beyond the page — enough to evidence a capability's
// presence (count >= 1) without unbounded reads. It returns the page too for callers
// that need the rows.
func pageCount[T any](ctx context.Context, list func(context.Context, model.Query) ([]T, model.Page, error), filters ...model.Filter) ([]T, bool, error) {
	rows, page, err := list(ctx, model.Query{Filters: filters, Limit: listCap})
	if err != nil {
		return nil, false, err
	}
	return rows, page.HasMore, nil
}

// auditEvent appends a compliance self-audit attributed to the REAL principal in the
// caller's transaction. Sealing/consulting/exporting evidence, classifying or
// reviewing risk and attesting residency are privileged and auditable (docs/SECURITY-HARDENING.md).
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

func hashHex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func hashBytes(s string) []byte {
	sum := sha256.Sum256([]byte(s))
	return sum[:]
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func idParam(s string) (model.ID, bool) {
	id := model.ID(strings.TrimSpace(s))
	return id, !id.IsZero()
}

func clamp(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// tooLong reports whether s exceeds n runes. IDENTITY fields (a matter or
// subject reference — anything exact-equality matching depends on) must be
// REJECTED when over-length, never clamped: clamp appends an ellipsis, so a
// truncated reference would persist as a DIFFERENT identity that the matching
// rule can never reach (e.g. an active hold that never covers its subject).
// Display-only prose (a title, a note) keeps clamp.
func tooLong(s string, n int) bool {
	return len([]rune(s)) > n
}

// encodeJSON encodes a value to a canonical JSON string for a KindJSON column, or nil
// when empty so the column is NULL.
func encodeJSON(v any) any {
	if v == nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	s := string(b)
	if s == "null" || s == "[]" || s == "{}" {
		return nil
	}
	return s
}

// jsonUnmarshal unmarshals JSON text into v (a thin wrapper so callers needn't import
// encoding/json just for a decode).
func jsonUnmarshal(s string, v any) error {
	return json.Unmarshal([]byte(s), v)
}

// decodeStrings decodes a KindJSON column's text back into a []string.
func decodeStrings(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil
	}
	return out
}

// decodeCaps decodes a KindJSON column's text back into a []CapabilityEvidence.
func decodeCaps(s string) []CapabilityEvidence {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var out []CapabilityEvidence
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil
	}
	return out
}

func wantsCSV(r *http.Request) bool {
	return strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("format")), "csv")
}

// csvField quotes a CSV field when it contains a comma, quote or newline (RFC 4180).
func csvField(s string) string {
	if strings.ContainsAny(s, ",\"\n\r") {
		return "\"" + strings.ReplaceAll(s, "\"", "\"\"") + "\""
	}
	return s
}
