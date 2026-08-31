// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package catalog

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// validEntryKinds is the set of curated entry kinds.
var validEntryKinds = map[string]bool{kindAgent: true, kindMCP: true, kindSkill: true, kindTemplate: true, kindModel: true, kindConnector: true}

// entryDTO is one catalog entry version: its identity, the reusable spec, its
// lifecycle status and the integrity/signature metadata.
type entryDTO struct {
	ID          string         `json:"id,omitempty"`
	Kind        string         `json:"kind"`
	Name        string         `json:"name"`
	Slug        string         `json:"slug"`
	Version     string         `json:"version"`
	Status      string         `json:"status,omitempty"`
	Summary     string         `json:"summary,omitempty"`
	Spec        map[string]any `json:"spec,omitempty"`
	OwnerRef    string         `json:"owner_ref,omitempty"`
	ContentHash string         `json:"content_hash,omitempty"`
	Signed      bool           `json:"signed"`
	SigAlg      string         `json:"sig_alg,omitempty"`
	SignedBy    string         `json:"signed_by,omitempty"` // key fingerprint (display)
	ApprovedBy  string         `json:"approved_by,omitempty"`
	ApprovedAt  string         `json:"approved_at,omitempty"`
}

// validate normalizes and checks an incoming entry definition.
func (d *entryDTO) validate() string {
	d.Kind = strings.TrimSpace(strings.ToLower(d.Kind))
	if !validEntryKinds[d.Kind] {
		return "kind must be one of agent, mcp, skill, template, model, connector"
	}
	d.Name = strings.TrimSpace(d.Name)
	if d.Name == "" {
		return "name is required"
	}
	d.Slug = strings.TrimSpace(strings.ToLower(d.Slug))
	if !validSlug(d.Slug) {
		return "slug must be a lowercase identifier (a-z, 0-9, -, _), up to 64 chars"
	}
	d.Version = strings.TrimSpace(d.Version)
	if !validSemver(d.Version) {
		return "version must be a semantic version (e.g. 1.2.3, 1.0.0-beta.1)"
	}
	d.OwnerRef = strings.TrimSpace(d.OwnerRef)
	if specBytes, err := json.Marshal(d.Spec); err == nil && containsInlineCredential(string(specBytes)) {
		return "spec must not contain inline credentials; reference secrets by name/locator instead"
	}
	return ""
}

// entryFields renders the entry's authored fields to store columns (the engine
// stamps the base columns; status/hash/signature are managed by the lifecycle).
func (d entryDTO) entryFields() model.Record {
	return model.Record{
		colEntryKind: d.Kind, colName: d.Name, colSlug: d.Slug, colVersion: d.Version,
		colSummary: d.Summary, colSpec: marshalSpec(d.Spec), colOwnerRef: d.OwnerRef,
	}
}

// toEntryDTO renders a stored entry record to the DTO (the signature column holds
// the verifier public key; the DTO surfaces only its display fingerprint).
func toEntryDTO(rec model.Record) entryDTO {
	d := entryDTO{
		ID: rec.String(model.ColID), Kind: rec.String(colEntryKind), Name: rec.String(colName),
		Slug: rec.String(colSlug), Version: rec.String(colVersion), Status: rec.String(colStatus),
		Summary: rec.String(colSummary), Spec: parseSpec(rec.String(colSpec)), OwnerRef: rec.String(colOwnerRef),
		ContentHash: rec.String(colContentHash), SigAlg: rec.String(colSigAlg),
		ApprovedBy: rec.String(colApprovedBy), ApprovedAt: rec.String(colApprovedAt),
	}
	if sig := rec.String(colSignature); sig != "" {
		d.Signed = true
		if pub, err := base64.StdEncoding.DecodeString(rec.String(colSignedBy)); err == nil && len(pub) == ed25519.PublicKeySize {
			d.SignedBy = keyFingerprint(ed25519.PublicKey(pub))
		}
	}
	return d
}

// marshalSpec serializes a spec map to a JSON column value ("" when empty so the
// nullable column stays NULL; the hash treats nil and {} identically).
func marshalSpec(spec map[string]any) string {
	if len(spec) == 0 {
		return ""
	}
	b, err := json.Marshal(spec)
	if err != nil {
		return ""
	}
	return string(b)
}

// parseSpec decodes a stored spec JSON column to a map.
func parseSpec(s string) map[string]any {
	if s == "" {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil
	}
	return out
}

// handleListEntries lists catalog entries, optionally filtered by kind/status/slug.
func (m *Module) handleListEntries(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(r)
	for _, f := range []struct{ param, col string }{{"kind", colEntryKind}, {"status", colStatus}, {"slug", colSlug}} {
		if v := r.URL.Query().Get(f.param); v != "" {
			q.Filters = append(q.Filters, eq(f.col, v))
		}
	}
	out := listResponse[entryDTO]{Items: []entryDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(entryKind)
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

// handleGetEntry returns one catalog entry.
func (m *Module) handleGetEntry(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	m.getOne(w, r, mc, func(rec model.Record) any { return toEntryDTO(rec) })
}

// handleCreateEntry creates a draft catalog entry.
func (m *Module) handleCreateEntry(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var in entryDTO
	if !decodeJSON(w, r, &in) {
		return
	}
	if msg := in.validate(); msg != "" {
		writeJSON(w, http.StatusBadRequest, errorBody(msg))
		return
	}
	var out entryDTO
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(entryKind)
		if err != nil {
			return err
		}
		fields := in.entryFields()
		fields[colStatus] = statusDraft
		rec, err := repo.Create(r.Context(), fields)
		if err != nil {
			return err
		}
		out = toEntryDTO(rec)
		return auditEntry(r.Context(), sc, mc, "create", model.ID(rec.String(model.ColID)), nil)
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

// handleUpdateEntry updates a DRAFT entry in place. An approved or deprecated
// entry is immutable (a new version is a new entry) — updating it is rejected.
func (m *Module) handleUpdateEntry(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var in entryDTO
	if !decodeJSON(w, r, &in) {
		return
	}
	if msg := in.validate(); msg != "" {
		writeJSON(w, http.StatusBadRequest, errorBody(msg))
		return
	}
	var (
		out      entryDTO
		notDraft bool
	)
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(entryKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		if rec.String(colStatus) != statusDraft {
			notDraft = true
			return nil
		}
		// A recorded MCP admission verdict certifies the served spec as it was at admit
		// time; the MCP approve gate does not re-bind it to the current spec (see
		// invalidateMCPAdmission). The verdict is keyed by entry id, NOT kind, so it must
		// be invalidated on a served-spec change AND on a kind change — otherwise an
		// mcp→other→mcp "kind-flip" (flip out with the spec intact, flip back with a
		// fresh spec) would re-use a stale verdict the old-kind check would miss. Compute
		// both deltas here, before the overwrite. invalidateMCPAdmission is a safe no-op
		// when the entry has no MCP verdict, so the kind need not be special-cased.
		specChanged := rec.String(colSpec) != marshalSpec(in.Spec)
		kindChanged := rec.String(colEntryKind) != in.Kind
		for k, v := range in.entryFields() {
			rec[k] = v
		}
		rec, err = repo.Update(r.Context(), rec)
		if err != nil {
			return err
		}
		var meta map[string]any
		if specChanged || kindChanged {
			n, err := invalidateMCPAdmission(r, sc, id)
			if err != nil {
				return err
			}
			if n > 0 {
				meta = map[string]any{"mcp_admission_invalidated": n}
			}
		}
		out = toEntryDTO(rec)
		return auditEntry(r.Context(), sc, mc, "update", id, meta)
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if notDraft {
		writeJSON(w, http.StatusConflict, errorBody("only a draft entry can be edited; approved versions are immutable"))
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleDeleteEntry deletes a DRAFT entry. Approved/deprecated entries are kept
// (deprecate to retire them) so the registry's approved history is not erasable
// through this path.
func (m *Module) handleDeleteEntry(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	notDraft := false
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(entryKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		if rec.String(colStatus) != statusDraft {
			notDraft = true
			return nil
		}
		if err := repo.Delete(r.Context(), id); err != nil {
			return err
		}
		return auditEntry(r.Context(), sc, mc, "delete", id, nil)
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if notDraft {
		writeJSON(w, http.StatusConflict, errorBody("only a draft entry can be deleted; deprecate an approved entry instead"))
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}

// handleSubmitEntry moves a draft entry to pending review.
func (m *Module) handleSubmitEntry(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	m.transitionEntry(w, r, mc, "submit", func(cur string) (string, string) {
		if cur != statusDraft {
			return "", "only a draft entry can be submitted for review"
		}
		return statusPending, ""
	})
}

// handleDeprecateEntry retires an approved entry. Its content hash and signature
// are preserved, so it remains verifiable — it is retired, not erased.
func (m *Module) handleDeprecateEntry(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	m.transitionEntry(w, r, mc, "deprecate", func(cur string) (string, string) {
		if cur != statusApproved {
			return "", "only an approved entry can be deprecated"
		}
		return statusDeprecated, ""
	})
}

// transitionEntry is the shared status-only lifecycle transition (submit/
// deprecate): it loads the entry, applies the guard, sets the new status and
// self-audits. Approval is separate (it computes the hash + signs).
func (m *Module) transitionEntry(w http.ResponseWriter, r *http.Request, mc api.ModuleContext, verb string, next func(cur string) (newStatus, errMsg string)) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var (
		out    entryDTO
		errMsg string
	)
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(entryKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		newStatus, msg := next(rec.String(colStatus))
		if msg != "" {
			errMsg = msg
			return nil
		}
		rec[colStatus] = newStatus
		rec, err = repo.Update(r.Context(), rec)
		if err != nil {
			return err
		}
		out = toEntryDTO(rec)
		return auditEntry(r.Context(), sc, mc, verb, id, map[string]any{"status": newStatus})
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if errMsg != "" {
		writeJSON(w, http.StatusConflict, errorBody(errMsg))
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleApproveEntry approves a draft/pending entry: it pins the content hash,
// signs it when a catalog signing key is configured, records the approver and
// freezes the entry. This is the privileged curation action (docs/SECURITY-HARDENING.md) — RBAC
// admin-tier and self-audited.
func (m *Module) handleApproveEntry(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var (
		out    entryDTO
		errMsg string
	)
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(entryKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		// refuse records the approval refusal in the audit ledger — a durable answer
		// to "why was this refused?" (the transient 409 body is otherwise the only
		// signal) — and sets errMsg for the caller to surface as 409. The refusal is
		// the transaction's only write; no entry state changes.
		refuse := func(reason string) error {
			errMsg = reason
			return auditEntry(r.Context(), sc, mc, "approve_refused", id, map[string]any{
				"kind":   rec.String(colEntryKind),
				"reason": reason,
			})
		}
		switch rec.String(colStatus) {
		case statusDraft, statusPending:
		default:
			return refuse("only a draft or pending entry can be approved")
		}
		// Deny-closed gate (XIV): a MODEL entry may only be approved into the
		// catalog when the tenant's signed-model-admission policy is satisfied for the
		// model_version it curates (modeladmission.go). Policy default off ⇒ no change
		// for non-model entries or tenants that have not opted in.
		if rec.String(colEntryKind) == kindModel {
			if reason, derr := modelAdmissionRefusal(r, sc, parseSpec(rec.String(colSpec))); derr != nil {
				return derr
			} else if reason != "" {
				return refuse(reason)
			}
		}
		// Deny-closed gate: an MCP entry may only be approved (and thus served
		// by the embedded sub-registry) when the tenant's MCP-entry admission policy
		// is satisfied — a verified provenance/SBOM attestation (mcpadmission.go).
		// Same observe-by-default semantics as the model gate. Unlike model/connector
		// the gate does not re-bind to the current spec; handleUpdateEntry invalidates
		// the verdict on a served-spec edit instead (see invalidateMCPAdmission).
		if rec.String(colEntryKind) == kindMCP {
			if reason, derr := mcpAdmissionRefusal(r, sc, id); derr != nil {
				return derr
			} else if reason != "" {
				return refuse(reason)
			}
		}
		// S142 gate — a CONNECTOR entry may only be approved (and thus listed as a
		// verified connector) with a verified provenance/SBOM attestation admission
		// (connectoradmission.go); observe-by-default, like the model and MCP gates.
		// Pass the entry's CURRENT spec so the gate can confirm the recorded verdict
		// was bound to the artifact the entry now curates (spec.artifact_digest) — a
		// draft's curated digest is editable, so a stale verdict over a different
		// build must not certify the entry (mirrors modelAdmissionRefusal's spec pass).
		if rec.String(colEntryKind) == kindConnector {
			if reason, derr := connectorAdmissionRefusal(r, sc, id, parseSpec(rec.String(colSpec))); derr != nil {
				return derr
			} else if reason != "" {
				return refuse(reason)
			}
		}
		hash, err := contentHash(
			rec.String(colName), rec.String(colEntryKind), rec.String(colSlug), rec.String(colVersion),
			rec.String(colSummary), rec.String(colOwnerRef), parseSpec(rec.String(colSpec)),
		)
		if err != nil {
			return err
		}
		rec[colContentHash] = hexEncode(hash)
		rec[colStatus] = statusApproved
		rec[colApprovedBy] = mc.Principal.Actor()
		rec[colApprovedAt] = model.NewTimestamp(m.clock.Now().Time()).String()
		signed := false
		var fp string
		if priv := m.signingKey(); priv != nil {
			sigB64, pubB64, fingerprint := sign(priv, hash)
			rec[colSignature] = sigB64
			rec[colSigAlg] = "ed25519"
			rec[colSignedBy] = pubB64
			signed, fp = true, fingerprint
		} else {
			rec[colSignature] = ""
			rec[colSigAlg] = ""
			rec[colSignedBy] = ""
		}
		rec, err = repo.Update(r.Context(), rec)
		if err != nil {
			return err
		}
		out = toEntryDTO(rec)
		return auditEntry(r.Context(), sc, mc, "approve", id, map[string]any{
			"content_hash": rec.String(colContentHash), "signed": signed, "signed_by": fp,
		})
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if errMsg != "" {
		writeJSON(w, http.StatusConflict, errorBody(errMsg))
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// verifyDTO is the integrity-verification result for one entry.
type verifyDTO struct {
	Status      string `json:"status"`
	HashOK      bool   `json:"hash_ok"`
	Signed      bool   `json:"signed"`
	SignatureOK bool   `json:"signature_ok"`
	Verified    bool   `json:"verified"`
	SignedBy    string `json:"signed_by,omitempty"`
	ContentHash string `json:"content_hash,omitempty"`
	Recomputed  string `json:"recomputed_hash,omitempty"`
	Reason      string `json:"reason"`
}

// handleVerifyEntry recomputes and verifies an approved entry's integrity (hash +
// signature). It is read-only and not audited (a verification has observer effect,
// mirroring the core /audit/verify).
func (m *Module) handleVerifyEntry(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var (
		out   verifyDTO
		found bool
	)
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(entryKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		found = true
		status := rec.String(colStatus)
		if status != statusApproved && status != statusDeprecated {
			out = verifyDTO{Status: status, Reason: "entry is not approved; nothing is pinned to verify yet"}
			return nil
		}
		res := verify(
			rec.String(colContentHash), rec.String(colSignature), rec.String(colSignedBy), m.expectedFingerprint(),
			rec.String(colName), rec.String(colEntryKind), rec.String(colSlug), rec.String(colVersion),
			rec.String(colSummary), rec.String(colOwnerRef), parseSpec(rec.String(colSpec)),
		)
		out = verifyDTO{
			Status: status, HashOK: res.HashOK, Signed: res.Signed, SignatureOK: res.SignatureOK,
			Verified: res.Verified, SignedBy: res.SignedByFP, ContentHash: res.StoredHash,
			Recomputed: res.Recomputed, Reason: res.Reason,
		}
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

// handlePubkey returns the catalog signing public key (for external verification),
// or reports that signing is not configured.
func (m *Module) handlePubkey(w http.ResponseWriter, r *http.Request, _ api.ModuleContext) {
	priv := m.signingKey()
	if priv == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"signing_enabled": false,
			"note":            "no catalog signing key configured; approved entries are hash-pinned and ledger-attested but unsigned",
		})
		return
	}
	pub := priv.Public().(ed25519.PublicKey)
	writeJSON(w, http.StatusOK, map[string]any{
		"signing_enabled": true,
		"algorithm":       "ed25519",
		"public_key":      base64.StdEncoding.EncodeToString(pub),
		"fingerprint":     keyFingerprint(pub),
	})
}

// getOne is the shared single-entry read by id.
func (m *Module) getOne(w http.ResponseWriter, r *http.Request, mc api.ModuleContext, render func(model.Record) any) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var (
		out   any
		found bool
	)
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(entryKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		found, out = true, render(rec)
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

// auditEntry appends an entry-governance audit event attributed to the real
// principal, in the caller's transaction (docs/SECURITY-HARDENING.md self-audit).
func auditEntry(ctx context.Context, sc store.Scope, mc api.ModuleContext, verb string, id model.ID, meta map[string]any) error {
	_, err := sc.Audit().Append(ctx, model.AuditDraft{
		Actor:      mc.Principal.Actor(),
		ActorKind:  mc.Principal.ActorKind(),
		Action:     "catalog.entry." + verb,
		TargetKind: entryKind,
		TargetID:   id,
		Meta:       meta,
	})
	return err
}
