// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package knowledge

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"net/http"
	"strings"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// This file is the memory-governance hardening (OWASP
// ASI06 "Memory & Context Poisoning"): cryptographic integrity verification of
// the agent-memory store, anchored to the append-only audit ledger, plus the
// per-user/per-session namespace model the handlers in memory.go enforce.
//
// The integrity model has two layers, each honest about what it defends:
//
//   - SELF-CHECK (every read): SHA-256 of the stored content must equal the
//     stored content_hash. Cheap, runs on the hot path, catches a direct edit of
//     the content column. An entry that fails is NEVER served to an agent (fail
//     closed: poisoned memory must not reshape behavior — the ASI06 vector); the
//     detection is recorded (audit + finding) so tampering is an incident, not a
//     silent filter. Bypassable by an attacker who recomputes the hash — which is
//     exactly what the second layer exists for.
//   - LEDGER ANCHOR (verify endpoint): every memory mutation's audit event
//     carries PayloadHash = the ENTRY HASH — a domain-separated SHA-256 over all
//     governed fields (scope, key, content hash, classification, residency,
//     expiry, author). The ledger is append-only, hash-chained and signed
//     (docs/SECURITY-HARDENING.md), so the anchor cannot be silently rewritten with the row.
//     POST /memory/verify recomputes each live row's entry hash and compares it
//     to the row's LATEST put anchor in the chain: a coordinated rewrite of
//     content+hash, a classification/expiry/scope edit, a forged row with no
//     ledger history, or a row whose last ledger event is a delete, are all
//     detected. PayloadHash is used (not Meta) because the ledger's Meta is
//     write-only on read paths (Walk returns Meta=nil; the recording module set
//     the precedent).
//
// Honest limits (documented, not hidden): the anchor proves a live row against
// the chain — it does not prove ABSENCE (purge appends one summary event, not
// per-row deletes: a ledger-size tradeoff; an expired row resurrected with its
// original content is inert anyway — still expired). Chain integrity itself is
// proven by the security module (GET /v1/m/security/integrity/verify); compose
// the two for the full forensic statement. This contract — verify-before-trust
// of a memory store — is what (Dreams) consumes for admitting a dream's
// output store: machine-curated memory is untrusted until verified + admitted.

// memoryHashDomain domain-separates the entry hash from every other SHA-256 in
// the product (the canon.EventHash discipline). Version-suffixed: a future
// change to the field set bumps it rather than silently re-hashing.
const memoryHashDomain = "olivares.knowledge.memory.v1"

// Memory audit actions (the ledger vocabulary; put/delete/purge predate).
const (
	actionMemoryPut    = "knowledge.memory.put"
	actionMemoryDelete = "knowledge.memory.delete"
	actionMemoryPurge  = "knowledge.memory.purge"
	actionMemoryTamper = "knowledge.memory.tamper_detected"
	actionMemoryVerify = "knowledge.memory.verify"
	actionMemoryExport = "knowledge.memory.export" // Portability
	actionMemoryImport = "knowledge.memory.import" // Portability
)

// actionErasureRow is the per-row deletion evidence the COMPLIANCE module
// appends when an RTBF erasure hard-deletes a memory row (a cross-module
// contract literal, like holdClassAgentMemory — knowledge never imports
// compliance). The verify collector treats it as a deletion anchor so a
// legally-erased row that reappears (backup replay) classifies as
// deleted_resurrected instead of inheriting its stale put anchor as
// "verified" — the one deletion class where resurrection has legal weight.
const actionErasureRow = "compliance.erasure.row"

// Verify statuses, per live entry. Only statusVerified is healthy.
const (
	statusVerified        = "verified"
	statusContentTampered = "content_tampered"    // stored content no longer matches content_hash
	statusLedgerMismatch  = "ledger_mismatch"     // row internally consistent but differs from its ledger anchor
	statusResurrected     = "deleted_resurrected" // the row's LAST ledger event is a delete, yet the row exists
	statusUnanchored      = "unanchored"          // no put/delete event for this row at all (out-of-band insert)
	statusLegacy          = "legacy_unanchored"   // anchored by a pre put (no PayloadHash); verifiable after its next put
)

// maxVerifyEntries bounds how many NON-verified entries the verify response
// lists in detail; counts are always complete and `truncated` reports the cap
// (docs/SECURITY-HARDENING.md — never a silent partial that looks complete).
const maxVerifyEntries = 200

// memoryKinds are the two memory entities in read order: the agent-global table
// first (the original), then the user/session-scoped one.
var memoryKinds = []model.Kind{memoryKind, scopedMemoryKind}

// ----------------------------------------------------------------------------
// Namespace (per-user / per-session isolation)
// ----------------------------------------------------------------------------

// memoryScope is an entry's namespace within the tenant+agent: the user and/or
// session it is isolated to. The zero scope is the shared agent-global
// namespace (every original knowledge.memory row) — the documented opt-in
// shared mode. Scope refs are caller-declared context, like agent_ref and the
// lineage session_ref: the module enforces separation BY CONSTRUCTION given the
// declared context; binding the declaration to an authenticated identity is the
// PEP/runtime's duty, not re-implemented here.
type memoryScope struct {
	user    string
	session string
}

// scoped reports whether any namespace dimension is set (such entries live in
// knowledge.memory_scoped).
func (s memoryScope) scoped() bool { return s.user != "" || s.session != "" }

// visibleIn reports whether an entry with scope s is readable from the DECLARED
// read context dc. Deny-closed per dimension: an entry scoped to a user/session
// is visible ONLY to a context declaring exactly that user/session — a read
// that declares no user sees no user-scoped entries, and one user's context
// never sees another's. An entry unscoped on a dimension is shared across it.
func (s memoryScope) visibleIn(dc memoryScope) bool {
	return (s.user == "" || s.user == dc.user) && (s.session == "" || s.session == dc.session)
}

// recScope reads an entry's namespace. Agent-global rows (the original kind,
// which has no scope columns) read as the zero scope.
func recScope(rec model.Record) memoryScope {
	return memoryScope{user: rec.String(colUserRef), session: rec.String(colSessionRef)}
}

// scopeFromQuery reads the declared read context from ?user_ref= / ?session_ref=.
func scopeFromQuery(r *http.Request) memoryScope {
	return memoryScope{
		user:    strings.TrimSpace(r.URL.Query().Get("user_ref")),
		session: strings.TrimSpace(r.URL.Query().Get("session_ref")),
	}
}

// ----------------------------------------------------------------------------
// Entry hash + anchored audit
// ----------------------------------------------------------------------------

// memoryEntryHash is the canonical SHA-256 over an entry's GOVERNED fields:
// kind, namespace, key, content hash, classification, residency, expiry and
// author — domain-separated and length-prefixed (no field-concatenation
// ambiguity). It is computed from the PERSISTED record (the store-normalized
// truth), anchored into the put/delete audit events as PayloadHash, and
// recomputed at verify time: tampering with ANY governed field — not just the
// content — breaks the comparison. It hashes content_hash rather than the
// content so the anchor chains to the content transitively while staying cheap.
func memoryEntryHash(kind model.Kind, rec model.Record) []byte {
	h := sha256.New()
	for _, f := range []string{
		memoryHashDomain, string(kind),
		rec.String(colAgentRef), rec.String(colUserRef), rec.String(colSessionRef),
		rec.String(colMemKey), rec.String(colContentHash), rec.String(colClassif),
		rec.String(colResidency), normTimestamp(rec.String(colExpiresAt)), rec.String(colCreatedBy),
	} {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(f)))
		h.Write(n[:])
		h.Write([]byte(f))
	}
	return h.Sum(nil)
}

// normTimestamp normalizes a stored timestamp string to the model's canonical
// form so the entry hash is engine-independent (SQLite returns the TEXT
// verbatim; another engine may reformat on scan). Empty stays empty (= no
// expiry); an unparseable value hashes as-is (never silently dropped).
func normTimestamp(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if ts, err := model.ParseTimestamp(s); err == nil {
		return ts.String()
	}
	return s
}

// memoryContentIntact is the read-path SELF-CHECK: the stored content re-hashes
// to the stored content_hash. content_hash has been written on every put since
// the table's first version, so an empty or mismatching hash on a live row is
// tampering, never a legacy state (fail closed).
func memoryContentIntact(rec model.Record) bool {
	return rec.String(colContentHash) == hashHex(rec.String(colContent))
}

// auditMemoryEvent appends one memory audit event with the entry-hash anchor in
// PayloadHash, attributed to the real principal, in the caller's transaction
// (helpers.go auditEvent plus the anchor).
func auditMemoryEvent(ctx context.Context, sc store.Scope, mc api.ModuleContext, action string, kind model.Kind, id model.ID, meta map[string]any, payloadHash []byte) error {
	_, err := sc.Audit().Append(ctx, model.AuditDraft{
		Actor:       mc.Principal.Actor(),
		ActorKind:   mc.Principal.ActorKind(),
		Action:      action,
		TargetKind:  kind,
		TargetID:    id,
		Meta:        meta,
		PayloadHash: payloadHash,
	})
	return err
}

// memoryAuditMeta is the put/delete/tamper Meta: agent + key only — NEVER the
// user/session namespace. Those refs are RTBF SUBJECT identifiers (the very
// dimensions the erasure targets key on): written into the immutable,
// hash-committed, WORM-archived ledger Meta they would survive the erasure
// they identify — the exact defect the kb.create fix removed for
// caller-supplied names. The namespace stays cryptographically COMMITTED via
// the PayloadHash anchor (provable against a claimed scope) and readable on
// the live row; it is just never spelled out in unerasable media (docs/SECURITY-HARDENING.md).
func memoryAuditMeta(rec model.Record) map[string]any {
	return map[string]any{
		"agent": rec.String(colAgentRef),
		"key":   rec.String(colMemKey),
	}
}

// ----------------------------------------------------------------------------
// Tamper detection evidence (read paths)
// ----------------------------------------------------------------------------

// tamperRef identifies one entry that failed the read-path self-check.
type tamperRef struct {
	kind  model.Kind
	id    model.ID
	agent string
	key   string
}

// maxTamperSeen bounds the per-process tamper-evidence dedup set; when full it
// resets (a re-report after reset is harmless — the dedup is a noise bound,
// not a correctness invariant).
const maxTamperSeen = 4096

// reportMemoryTamper records read-path tamper detections: one audit event per
// entry (action knowledge.memory.tamper_detected — the forensic trail) plus one
// HIGH finding per entry tagged ASI06 so the security console correlates it
// with the memory-poisoning detectors. It runs AFTER the read transaction
// (audit appends need a write scope; the read itself stays a View) and is
// best-effort: the entry was ALREADY withheld from the response — a failure to
// record the evidence is surfaced in the log, never swallowed, and never turns
// a deny back into an allow.
//
// Evidence is DEDUPLICATED once per (tenant, kind, id) per process: an agent
// polling a tampered entry must not append a ledger event + persist a HIGH
// security finding on EVERY read (unbounded append-only growth + console
// flooding from a single tampered row). The deny itself is NOT deduplicated —
// every read still withholds the entry. Honest limit: the dedup is
// per-replica and resets on restart/overflow, so the evidence is "at least
// once, bounded", not "exactly once".
func (m *Module) reportMemoryTamper(ctx context.Context, mc api.ModuleContext, refs []tamperRef) {
	for _, ref := range refs {
		key := mc.Tenant.String() + "\x00" + string(ref.kind) + "\x00" + ref.id.String()
		m.tamperMu.Lock()
		if m.tamperSeen == nil || len(m.tamperSeen) >= maxTamperSeen {
			m.tamperSeen = map[string]struct{}{}
		}
		if _, seen := m.tamperSeen[key]; seen {
			m.tamperMu.Unlock()
			continue
		}
		m.tamperSeen[key] = struct{}{}
		m.tamperMu.Unlock()

		err := mc.Data.Mutate(ctx, func(sc store.Scope) error {
			return auditMemoryEvent(ctx, sc, mc, actionMemoryTamper, ref.kind, ref.id,
				map[string]any{"agent": ref.agent, "key": ref.key}, nil)
		})
		if err != nil {
			m.errorf("knowledge: failed to audit memory tamper detection", "id", ref.id, "err", err)
		}
		m.emitMemoryTamperFinding(ctx, mc.Tenant, string(ref.kind), ref.id.String(),
			"agent memory entry failed integrity self-check (content/hash mismatch); entry withheld agent="+ref.agent+" key="+ref.key+" id="+ref.id.String())
	}
}

// integrityViolationBody is the 409 envelope a direct GET of a tampered entry
// returns: machine-readable code, no content (the entry is withheld).
func integrityViolationBody() map[string]any {
	return map[string]any{"error": map[string]any{
		"code":    "integrity_violation",
		"message": "memory entry failed integrity verification; entry withheld",
	}}
}

// ----------------------------------------------------------------------------
// POST /memory/verify — the ledger-anchored verification (admin)
// ----------------------------------------------------------------------------

// memoryVerifyEntry is one NON-verified entry in the verify report: identity +
// status, never content.
type memoryVerifyEntry struct {
	ID         string `json:"id"`
	AgentRef   string `json:"agent_ref"`
	UserRef    string `json:"user_ref,omitempty"`
	SessionRef string `json:"session_ref,omitempty"`
	Key        string `json:"key"`
	Status     string `json:"status"`
}

// memoryVerifyResponse is the verify report: complete counts per status plus a
// bounded detail list of the unhealthy entries.
type memoryVerifyResponse struct {
	Checked         int                 `json:"checked"`
	Verified        int                 `json:"verified"`
	ContentTampered int                 `json:"content_tampered"`
	LedgerMismatch  int                 `json:"ledger_mismatch"`
	Resurrected     int                 `json:"deleted_resurrected"`
	Unanchored      int                 `json:"unanchored"`
	Legacy          int                 `json:"legacy_unanchored"`
	Entries         []memoryVerifyEntry `json:"entries"`
	Truncated       bool                `json:"truncated"`
}

// memAnchor is the LAST ledger event observed for one entry id: which action
// and with which entry-hash anchor.
type memAnchor struct {
	action  string
	payload []byte
}

// collectMemoryAnchors walks the tenant's audit chain ONCE and returns the last
// put/deletion event per memory entry id (tamper-detection and verify events do
// not move the anchor; an RTBF per-row erasure event counts as a deletion).
// O(chain) by design — the ledger has no by-target query; this is the
// security-timeline discipline and the endpoint is admin-tier.
func collectMemoryAnchors(ctx context.Context, sc store.Scope) (map[string]memAnchor, error) {
	anchors := map[string]memAnchor{}
	err := sc.Audit().Walk(ctx, 1, func(ev model.AuditEvent) error {
		if ev.TargetKind != memoryKind && ev.TargetKind != scopedMemoryKind {
			return nil
		}
		if ev.Action != actionMemoryPut && ev.Action != actionMemoryDelete && ev.Action != actionErasureRow {
			return nil
		}
		anchors[string(ev.TargetKind)+"\x00"+ev.TargetID.String()] = memAnchor{action: ev.Action, payload: ev.PayloadHash}
		return nil
	})
	return anchors, err
}

// classifyMemoryEntry assigns one live row its verify status against the
// collected anchors. Order matters: a broken self-check is content tampering
// regardless of what the ledger says.
func classifyMemoryEntry(kind model.Kind, rec model.Record, anchors map[string]memAnchor) string {
	if !memoryContentIntact(rec) {
		return statusContentTampered
	}
	anchor, ok := anchors[string(kind)+"\x00"+rec.String(model.ColID)]
	switch {
	case !ok:
		return statusUnanchored
	case anchor.action == actionMemoryDelete || anchor.action == actionErasureRow:
		return statusResurrected
	case len(anchor.payload) == 0:
		return statusLegacy
	case !hashEqual(anchor.payload, memoryEntryHash(kind, rec)):
		return statusLedgerMismatch
	default:
		return statusVerified
	}
}

// hashEqual compares two hashes byte-for-byte (no timing concern: both sides
// are non-secret digests already visible to the caller's tier).
func hashEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// handleVerifyMemory is the verification endpoint: prove every live memory
// entry (optionally one agent's) against its ledger anchor. Admin-tier and
// SELF-AUDITED FIRST (the act of verifying is itself recorded into the chain it
// reads — the security forensic precedent). The self-audit runs in its OWN
// short Mutate and the O(chain) walk in a read-only View: Append holds the
// per-tenant pg_advisory_xact_lock until commit on Postgres (and the sole
// connection on SQLite), so one transaction spanning the whole walk would
// block every other write for its duration. Any unhealthy outcome emits ONE
// summary finding (HIGH, ASI06); the per-entry verdicts travel in the response.
func (m *Module) handleVerifyMemory(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	agentRef := strings.TrimSpace(r.URL.Query().Get("agent_ref"))
	if err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		return auditEvent(r.Context(), sc, mc, actionMemoryVerify, memoryKind, "",
			map[string]any{"agent": agentRef})
	}); err != nil {
		writeStoreError(w, err)
		return
	}
	out := memoryVerifyResponse{Entries: []memoryVerifyEntry{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		anchors, err := collectMemoryAnchors(r.Context(), sc)
		if err != nil {
			return err
		}
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
				out.Checked++
				status := classifyMemoryEntry(kind, rec, anchors)
				switch status {
				case statusVerified:
					out.Verified++
					continue
				case statusContentTampered:
					out.ContentTampered++
				case statusLedgerMismatch:
					out.LedgerMismatch++
				case statusResurrected:
					out.Resurrected++
				case statusUnanchored:
					out.Unanchored++
				case statusLegacy:
					out.Legacy++
				}
				if len(out.Entries) >= maxVerifyEntries {
					out.Truncated = true
					continue
				}
				out.Entries = append(out.Entries, memoryVerifyEntry{
					ID: rec.String(model.ColID), AgentRef: rec.String(colAgentRef),
					UserRef: rec.String(colUserRef), SessionRef: rec.String(colSessionRef),
					Key: rec.String(colMemKey), Status: status,
				})
			}
		}
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	// statusLegacy is unverifiable-by-age, not an indicator of compromise; the
	// finding fires on the states only tampering produces.
	if bad := out.ContentTampered + out.LedgerMismatch + out.Resurrected + out.Unanchored; bad > 0 {
		subject := agentRef
		if subject == "" {
			subject = "tenant"
		}
		m.emitMemoryTamperFinding(r.Context(), mc.Tenant, string(memoryKind), subject,
			"memory verify found unhealthy entries: content_tampered="+itoa(int64(out.ContentTampered))+
				" ledger_mismatch="+itoa(int64(out.LedgerMismatch))+
				" deleted_resurrected="+itoa(int64(out.Resurrected))+
				" unanchored="+itoa(int64(out.Unanchored))+
				" checked="+itoa(int64(out.Checked))+" agent="+agentRef)
	}
	writeJSON(w, http.StatusOK, out)
}
