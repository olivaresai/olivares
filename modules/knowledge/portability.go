// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package knowledge

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/sigbundle"
	"github.com/olivaresai/olivares/core/store"
)

// Governed-memory PORTABILITY (E1) — anti-lock-in export/import of agent
// memory as signed JSONL, per caller clearance.
//
// The single non-negotiable invariant: the export is a per-CALLER-clearance copy,
// NOT a privileged cross-scope dump. handleExportMemory applies the EXACT deny-
// closed predicate handleListMemory applies (memoryReadGrants → visibleIn(scope)
// → classificationAllowed → memoryContentIntact) so a portable export can never
// surface an entry above the caller's clearance or outside its declared user/
// session namespace. It is deliberately built on the FILTERED list path, never on
// handleListAllMemory (the admin cross-scope view has NO clearance predicate;
// exporting through it would be a direct clearance bypass). A full-fidelity
// operator migration artifact — should one ever be wanted — is a SEPARATE,
// admin-tier, explicitly-labeled surface, not this subject-access copy.
//
// Import is the inverse and equally deny-closed: it treats the bundle as UNTRUSTED
// input. It verifies the manifest signature (Ed25519, its own sigbundle domain)
// and the entries digest BEFORE parsing any entry, then routes every row through
// putMemoryEntry — the same governed write PUT uses — so fail-closed classification
// validation, redact-before-store, the per-agent quota and the audit-atomic-with-
// persist anchor all re-run on the imported data. A bundle's declared labels are
// never trusted; the target ledger gets a FRESH local anchor (a cross-tenant
// origin hash would never match, and is never carried).
//
// The manifest is signed under sigbundle.TagMemoryPortability — a DEDICATED domain
// distinct from the license/OTA/DDIL keys — so a portability signature can never be
// replayed as any other signed document, and vice versa. Both endpoints fail CLOSED
// (501) when their key half is unwired: export never emits an unsigned bundle,
// import never persists an unverifiable one.

const (
	// memPortSchema is the wire schema id; it MUST track the sigbundle tag major.
	memPortSchema = "olivares.memory-portability.v1"
	// maxImportBytes caps an import body (a full per-agent export is bounded by the
	// per-agent quota × the 64 KiB entry cap; 64 MiB is comfortably above that while
	// refusing an unbounded upload).
	maxImportBytes = 64 << 20
)

// memPortRow is one exported memory entry — exactly the fields import needs to
// reconstruct the governed write, and NOTHING target-local (no id, no created_by:
// the target stamps its own author and mints its own anchor). user_ref/session_ref
// are pointers so an unscoped (agent-global) row emits neither and re-imports into
// the shared scope, while a scoped row round-trips its exact namespace.
type memPortRow struct {
	AgentRef       string  `json:"agent_ref"`
	Key            string  `json:"key"`
	Content        string  `json:"content"`
	Classification string  `json:"classification"`
	Residency      string  `json:"residency_region,omitempty"`
	UserRef        *string `json:"user_ref,omitempty"`
	SessionRef     *string `json:"session_ref,omitempty"`
	// TTLSeconds carries the REMAINING lifetime of an entry that has an expiry, so
	// import re-evaluates it against its own clock (the absolute expiry drifts by the
	// export→import gap by design). 0 = no expiry. Expired entries are never exported
	// (the read predicate skips them), so a re-import never resurrects a dead row.
	TTLSeconds int64 `json:"ttl_seconds,omitempty"`
}

// memPortScope records the caller's declared export context for provenance (it is
// signed as part of the manifest, so a bundle states the namespace it was drawn from).
type memPortScope struct {
	UserRef    string `json:"user_ref,omitempty"`
	SessionRef string `json:"session_ref,omitempty"`
}

// memPortManifest is the signed sidecar (JSONL line 1). Signature covers the
// manifest with Signature blanked (omitempty drops it), and EntriesSHA256 binds the
// JSONL body, so the one signature authenticates the whole export transitively.
type memPortManifest struct {
	Schema            string       `json:"schema"`
	Tenant            string       `json:"tenant"`
	AgentRef          string       `json:"agent_ref,omitempty"`
	Scope             memPortScope `json:"scope"`
	Count             int          `json:"count"`
	IntegrityExcluded int          `json:"integrity_excluded"`
	EntriesSHA256     string       `json:"entries_sha256"`
	Signature         string       `json:"signature,omitempty"`
}

// signingBytes returns the deterministic bytes the manifest signature covers: the
// manifest marshaled with the signature field blanked (omitempty removes it). Both
// signer and verifier compute it the same way from the same struct, so they can
// never disagree on what was signed.
func (mm memPortManifest) signingBytes() ([]byte, error) {
	mm.Signature = ""
	return json.Marshal(mm)
}

// handleExportMemory streams the caller's clearance-visible memory as signed JSONL:
// line 1 is the signed manifest, each following line one memPortRow. It fails closed
// (501) when the portability signing key is unwired — it never emits an unsigned
// bundle.
func (m *Module) handleExportMemory(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	if len(m.memPortSignKey) == 0 {
		writeJSON(w, http.StatusNotImplemented, errorBody("memory portability export is not configured (no signing key wired)"))
		return
	}
	agentRef := strings.TrimSpace(r.URL.Query().Get("agent_ref"))
	dc := scopeFromQuery(r)
	grants := m.memoryReadGrants(r.Context(), mc, agentRef)

	rows := []memPortRow{}
	excluded := 0
	// Export, not View: this is the anti-lock-in / subject-access copy this file
	// describes, so it must survive a withdrawal of service rather than be the first
	// thing lost to it.
	err := mc.Data.Export(r.Context(), func(sc store.ExportScope) error {
		rows = rows[:0]
		excluded = 0
		for _, kind := range memoryKinds {
			repo, err := sc.Ext(kind)
			if err != nil {
				return err
			}
			filters := []model.Filter{}
			if agentRef != "" {
				filters = append(filters, eq(colAgentRef, agentRef))
			}
			recs, err := listAll(r.Context(), repo, filters...)
			if err != nil {
				return err
			}
			for _, rec := range recs {
				// THE deny-closed predicate, verbatim from handleListMemory: expired,
				// out-of-namespace, or over-clearance entries are never exported.
				if m.expired(rec) || !recScope(rec).visibleIn(dc) ||
					!classificationAllowed(rec.String(colClassif), grants.Clearance) {
					continue
				}
				// A tampered entry is withheld from the export (its bytes never passed
				// redaction) and counted, exactly as the list path withholds it.
				if !memoryContentIntact(rec) {
					excluded++
					continue
				}
				rows = append(rows, m.exportRow(rec))
			}
		}
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}

	// Canonicalize the body and the entries digest from the SAME marshaled bytes.
	var body strings.Builder
	sum := sha256.New()
	entryLines := make([][]byte, 0, len(rows))
	for _, row := range rows {
		b, merr := json.Marshal(row)
		if merr != nil {
			writeStoreError(w, merr)
			return
		}
		sum.Write(b)
		entryLines = append(entryLines, b)
	}

	manifest := memPortManifest{
		Schema: memPortSchema, Tenant: mc.Tenant.String(), AgentRef: agentRef,
		Scope:             memPortScope{UserRef: dc.user, SessionRef: dc.session},
		Count:             len(rows),
		IntegrityExcluded: excluded,
		EntriesSHA256:     hex.EncodeToString(sum.Sum(nil)),
	}
	toSign, err := manifest.signingBytes()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	sig := sigbundle.Sign(sigbundle.TagMemoryPortability, toSign, m.memPortSignKey)
	manifest.Signature = base64.StdEncoding.EncodeToString(sig)
	manifestLine, err := json.Marshal(manifest)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	body.Write(manifestLine)
	body.WriteByte('\n')
	for _, b := range entryLines {
		body.Write(b)
		body.WriteByte('\n')
	}

	// Audit-BEFORE-serve (fail closed): the export event is the ONLY trace of this
	// egress — unlike import, whose per-row puts each self-audit atomically. Seal it
	// FIRST; if it cannot be recorded (spool full, not leader, canceled), serve
	// NOTHING. A governed-memory export must never leave the box without a durable
	// record that it happened, mirroring handleVerifyMemory's self-audit-first.
	if err := m.exportAudit(r.Context(), mc, agentRef, len(rows), excluded); err != nil {
		m.errorf("knowledge: memory export blocked; its audit record could not be sealed", "agent", agentRef, "err", err)
		writeJSON(w, http.StatusServiceUnavailable, errorBody("export blocked: its audit record could not be sealed (fail closed)"))
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Content-Disposition", `attachment; filename="memory-export.jsonl"`)
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, body.String())
}

// exportRow projects one live record into a portable row, carrying the REMAINING
// TTL (not the absolute expiry) so a re-import re-evaluates lifetime on its own clock.
func (m *Module) exportRow(rec model.Record) memPortRow {
	row := memPortRow{
		AgentRef: rec.String(colAgentRef), Key: rec.String(colMemKey), Content: rec.String(colContent),
		Classification: rec.String(colClassif), Residency: rec.String(colResidency),
	}
	if u := rec.String(colUserRef); u != "" {
		row.UserRef = &u
	}
	if s := rec.String(colSessionRef); s != "" {
		row.SessionRef = &s
	}
	if exp := strings.TrimSpace(rec.String(colExpiresAt)); exp != "" {
		if ts, perr := model.ParseTimestamp(exp); perr == nil {
			remaining := int64(ts.Time().Sub(m.clock.Now().Time()).Seconds())
			if remaining < 1 {
				remaining = 1 // survived the export predicate (not expired) → keep it alive
			}
			row.TTLSeconds = remaining
		}
	}
	return row
}

// importResult is the per-row outcome envelope.
type importResult struct {
	Imported         int              `json:"imported"`
	Rejected         []importRejected `json:"rejected"`
	IntegrityChecked bool             `json:"integrity_verified"`
}

type importRejected struct {
	Index  int    `json:"index"`
	Key    string `json:"key,omitempty"`
	Reason string `json:"reason"`
}

// handleImportMemory ingests a signed memory-portability bundle. It verifies the
// manifest signature and the entries digest FIRST (fail closed: a bad signature,
// an unwired verify key, or a digest mismatch rejects the WHOLE import before any
// write), then routes each row through the governed write path. A row that fails
// validation (unknown label, blank scope, quota, …) is REJECTED individually and
// reported; a store error aborts.
func (m *Module) handleImportMemory(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	if len(m.memPortVerifyKey) == 0 {
		writeJSON(w, http.StatusNotImplemented, errorBody("memory portability import is not configured (no verify key wired)"))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxImportBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	var manifest memPortManifest
	if err := dec.Decode(&manifest); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid import bundle: manifest line unreadable"))
		return
	}
	if manifest.Schema != memPortSchema {
		writeJSON(w, http.StatusBadRequest, errorBody("unsupported import bundle schema"))
		return
	}
	sig, err := base64.StdEncoding.DecodeString(manifest.Signature)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid import bundle: manifest signature not base64"))
		return
	}
	toVerify, err := manifest.signingBytes()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if verr := sigbundle.Verify(sigbundle.TagMemoryPortability, toVerify, sig, m.memPortVerifyKey); verr != nil {
		// A forged/altered manifest, a wrong key, or a signature minted under another
		// domain all land here — reject the whole bundle, nothing is written.
		writeJSON(w, http.StatusBadRequest, errorBody("import bundle signature did not verify"))
		return
	}

	// Read every entry, recomputing the digest over the SAME canonical marshaling the
	// exporter signed. The digest is checked BEFORE any persist, so a body that does
	// not match the signed manifest never touches the store.
	sum := sha256.New()
	var rows []memPortRow
	for {
		var row memPortRow
		derr := dec.Decode(&row)
		if errors.Is(derr, io.EOF) {
			break
		}
		if derr != nil {
			writeJSON(w, http.StatusBadRequest, errorBody("invalid import bundle: entry line unreadable"))
			return
		}
		b, merr := json.Marshal(row)
		if merr != nil {
			writeStoreError(w, merr)
			return
		}
		sum.Write(b)
		rows = append(rows, row)
	}
	if len(rows) != manifest.Count {
		writeJSON(w, http.StatusBadRequest, errorBody("import bundle entry count does not match its manifest"))
		return
	}
	if hex.EncodeToString(sum.Sum(nil)) != manifest.EntriesSHA256 {
		writeJSON(w, http.StatusBadRequest, errorBody("import bundle entries digest does not match its signed manifest"))
		return
	}

	// Signature + digest verified: the bundle is authentic and intact. Each row is
	// still UNTRUSTED write-path input — putMemoryEntry re-validates the label
	// (fail-closed), re-scrubs the content, re-enforces the quota and audits atomically.
	out := importResult{Rejected: []importRejected{}, IntegrityChecked: true}
	for i, row := range rows {
		req := memoryRequest{
			AgentRef: row.AgentRef, Key: row.Key, Content: row.Content,
			Classification: row.Classification, Residency: row.Residency,
			TTLSeconds: row.TTLSeconds, UserRef: row.UserRef, SessionRef: row.SessionRef,
		}
		_, perr := m.putMemoryEntry(r.Context(), mc, req)
		switch e := perr.(type) {
		case nil:
			out.Imported++
		case *putValidationError:
			out.Rejected = append(out.Rejected, importRejected{Index: i, Key: row.Key, Reason: e.msg})
		case *clientError:
			out.Rejected = append(out.Rejected, importRejected{Index: i, Key: row.Key, Reason: e.msg})
		default:
			// A store/infra error is not a per-row rejection — abort honestly.
			writeStoreError(w, perr)
			return
		}
	}
	m.importAudit(r.Context(), mc, manifest.AgentRef, out.Imported, len(out.Rejected))
	writeJSON(w, http.StatusOK, out)
}

// exportAudit / importAudit record the portability EVENT (real principal, non-
// anchored) with counts only — never the user/session namespace (RTBF subject
// identifiers must not enter the ledger, memoryAuditMeta discipline).
//
// exportAudit RETURNS its error: the caller seals it before serving and fails
// closed on failure (the export is the only trace of the egress).
func (m *Module) exportAudit(ctx context.Context, mc api.ModuleContext, agentRef string, count, excluded int) error {
	return mc.Data.Mutate(ctx, func(sc store.Scope) error {
		return auditEvent(ctx, sc, mc, actionMemoryExport, memoryKind, "",
			map[string]any{"agent": agentRef, "exported": count, "integrity_excluded": excluded})
	})
}

// importAudit is a best-effort SUMMARY: the authoritative trail is the per-row
// anchored put audit each putMemoryEntry already sealed atomically, so a lost
// summary is LOGGED (never silently swallowed), not fail-closed.
func (m *Module) importAudit(ctx context.Context, mc api.ModuleContext, agentRef string, imported, rejected int) {
	if err := mc.Data.Mutate(ctx, func(sc store.Scope) error {
		return auditEvent(ctx, sc, mc, actionMemoryImport, memoryKind, "",
			map[string]any{"agent": agentRef, "imported": imported, "rejected": rejected})
	}); err != nil {
		m.errorf("knowledge: failed to record memory import summary", "agent", agentRef, "err", err)
	}
}
