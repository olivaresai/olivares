// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package deploy

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
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// listCap bounds an internal List page; it matches the store's own maximum.
const listCap = 1000

// Bounds on operator-supplied strings. A deployment spec is structured and
// re-serialized from the typed struct, so these caps keep a single field from
// ballooning the JSON or the audit Meta (docs/SECURITY-HARDENING.md).
const (
	maxNameLen    = 200
	maxRefLen     = 512
	maxNoteLen    = 4096
	maxWirings    = 200
	maxEnvRefs    = 200
	maxSpecStrLen = 2048
	maxSpecBytes  = 1 << 18 // 256 KiB serialized cap on a desired spec
)

// errNoExecutor is the fail-closed sentinel an un-wired executor returns.
var errNoExecutor = errors.New("deploy: no runtime executor configured; cannot reconcile to infrastructure")

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

// requireStepUp gates a CRITICAL infrastructure mutation (apply/retire)
// on the operator's session assurance: a HUMAN session must carry a fresh
// hardware step-up (WebAuthn/PIV, AAL3) or the call is refused with the
// machine-readable step_up_required denial. A non-session principal
// (automation: Terraform/GitOps tokens) passes THIS gate — its authority is
// still the plan-bound dual-control approval, whose CRITICAL decisions are
// themselves AAL3-gated in the governance engine, so a human cannot launder an
// under-assured mutation through a token: two OTHER step-up-verified humans
// must still approve the plan.
func requireStepUp(w http.ResponseWriter, mc api.ModuleContext) bool {
	if mc.Principal.Kind == auth.KindUser && mc.Principal.AAL < auth.AAL3 {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": map[string]string{
			"code":    "step_up_required",
			"message": "this deployment mutation requires a hardware-verified (AAL3) session; complete the WebAuthn/PIV step-up and retry",
		}})
		return false
	}
	return true
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

// isNotFound reports the store's not-found sentinel.
func isNotFound(err error) bool { return errors.Is(err, store.ErrNotFound) }

// decodeJSON reads a JSON body into v, bounding the read so a malformed or huge
// body cannot exhaust memory, and rejecting unknown fields so a client cannot
// smuggle a value into a field the typed DTO does not declare.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(io.LimitReader(r.Body, maxSpecBytes))
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

// findOne returns the first row matching the AND of filters, or ok=false.
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

// listAll drains every page of a module-entity query (bounded by listCap per
// page) so a scan over a definition's owned rows is complete.
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

// auditEvent appends a deploy self-audit event attributed to the REAL principal,
// in the caller's transaction, so the append-only ledger records WHO did what to
// which deployment at which version (docs/SECURITY-HARDENING.md,§5) — never the system actor and
// never a secret. This is the hash-chain layer; the deploy.operation entity is
// the queryable change-management view reads.
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

// hashHex returns the hex SHA-256 of s — used for the spec hash and (composed)
// the plan hash. It is a content fingerprint, never a place a secret could leak:
// specs are guarded to carry references only before they are ever hashed.
func hashHex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// planHashOf binds an approval to an exact transition: the definition, the
// version delta and the desired spec hash. A re-plan that changes any of these
// changes the hash, so a stale approval can never authorize a different change.
func planHashOf(definitionID string, fromVer, toVer int64, specHash string) string {
	return hashHex(definitionID + "|" + strconv.FormatInt(fromVer, 10) + "|" + strconv.FormatInt(toVer, 10) + "|" + specHash)
}

// containsInlineCredential heuristically rejects a string that looks like raw
// secret material rather than a reference. It is a defense-in-depth guard, not a
// classifier: the structural defense is that secrets travel as secret_ref
// references (validateSecretRef) and the spec is a typed struct. It catches the
// obvious mistakes — PEM blocks, AWS keys, bearer/JWT-shaped tokens, long opaque
// high-entropy blobs — so an operator cannot paste a credential into a free
// field and have it persisted in cleartext (docs/SECURITY-HARDENING.md,§4).
func containsInlineCredential(s string) bool {
	t := strings.TrimSpace(s)
	if t == "" {
		return false
	}
	lower := strings.ToLower(t)
	for _, marker := range []string{
		"-----begin", "private key", "aws_secret_access_key", "akia", "asia",
		"bearer ", "authorization:", "password=", "passwd=", "secret=", "token=", "apikey=", "api_key=",
		"xoxb-", "xoxp-", "ghp_", "github_pat_", "sk-", "sk_live_", "sk_test_", "eyj",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	// A single long unbroken token (no spaces) with high alphanumeric density is
	// almost certainly raw material, not a human-meaningful reference.
	if len(t) >= 40 && !strings.ContainsAny(t, " \t\n") && alnumDensity(t) > 0.85 {
		return true
	}
	return false
}

// alnumDensity is the fraction of base64/hex-ish characters in s — a cheap proxy
// for "this looks like an opaque secret blob".
func alnumDensity(s string) float64 {
	n := 0
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '+' || r == '/' || r == '=' || r == '_' || r == '-' {
			n++
		}
	}
	return float64(n) / float64(len(s))
}

// secretRefSchemes is the closed allow-list of secret-store reference schemes. A
// secret_ref MUST be "<scheme>:<locator>" with a known scheme — never the secret
// itself. This is the structural guarantee that no cleartext secret is persisted
// (docs/SECURITY-HARDENING.md): the field can only ever hold a pointer into a secret-store.
var secretRefSchemes = map[string]bool{
	"vault":              true,
	"infisical":          true,
	"aws-secretsmanager": true,
	"gcp-secretmanager":  true,
	"azure-keyvault":     true,
	"k8s-secret":         true,
	"env":                true,
	"file":               true,
}

// validateSecretRef returns a non-empty message when ref is not a valid
// secret-store reference. An empty ref is allowed (a wiring may need no secret);
// a non-empty ref must be "<scheme>:<locator>" with an allow-listed scheme, be
// bounded, and not itself look like raw credential material.
func validateSecretRef(ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	if len(ref) > maxRefLen {
		return "secret_ref too long"
	}
	scheme, locator, ok := strings.Cut(ref, ":")
	if !ok || !secretRefSchemes[strings.ToLower(strings.TrimSpace(scheme))] || strings.TrimSpace(locator) == "" {
		return "secret_ref must be a secret-store reference of the form <scheme>:<locator> (e.g. vault:secret/data/db#dsn); a cleartext secret is never accepted"
	}
	if containsInlineCredential(locator) {
		return "secret_ref locator looks like a raw credential, not a reference"
	}
	return ""
}

// itoa formats an int64 as a decimal string (a small local alias for readability).
func itoa(n int64) string { return strconv.FormatInt(n, 10) }
