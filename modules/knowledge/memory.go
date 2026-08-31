// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package knowledge

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Governed agent memory (module VIII) — Hardening: the five OWASP ASI06
// memory-governance controls are expiry (lazy + purge), redact-before-store,
// tenant scope (the engine), INTEGRITY (self-check on every read + the ledger
// anchor in memory_integrity.go) and per-user/per-session ISOLATION (the
// memoryScope namespace; deny-closed across users and sessions, agent-global
// rows as the opt-in shared mode).
//
// Audit discipline ("audit-before-persist"): every mutation appends its
// anchored audit event IN THE SAME TRANSACTION as the row write — an append
// failure rolls the write back, so a mutation can never persist unaudited (the
// commit is the persist boundary; audit-atomic-with-persist). Inside the
// transaction every path takes the ROW lock first and the ledger's per-tenant
// append lock LAST (write/delete row, then Append): a mixed order would
// ABBA-deadlock concurrent put/delete of the same entry on Postgres, where
// Append holds pg_advisory_xact_lock(tenant) until commit. Put computes the
// anchor from the PERSISTED record (the store-normalized truth the verify
// endpoint will re-read).

// memoryRequest writes one governed agent memory entry. The content is REDACTED
// before storage (docs/SECURITY-HARDENING.md); ttl_seconds>0 sets an expiry (retention).
// user_ref/session_ref namespace the entry WITHIN the tenant+agent: a
// scoped entry is invisible to any read context that does not declare exactly
// that user/session. Omitting both writes the shared agent-global scope. They
// are POINTERS so a PRESENT-but-blank value is distinguishable from an omitted
// one and can be REJECTED: an upstream variable that evaluated empty must not
// silently demote an intended-scoped write into the shared namespace (an
// isolation control fails closed, never open).
type memoryRequest struct {
	AgentRef       string  `json:"agent_ref"`
	Key            string  `json:"key"`
	Content        string  `json:"content"`
	Classification string  `json:"classification,omitempty"`
	Residency      string  `json:"residency_region,omitempty"`
	TTLSeconds     int64   `json:"ttl_seconds,omitempty"`
	UserRef        *string `json:"user_ref,omitempty"`
	SessionRef     *string `json:"session_ref,omitempty"`
}

// scopeRef validates one DECLARED namespace dimension: nil = not declared (ok),
// declared-but-blank = rejected (the fail-closed rule above), and the bound is
// maxScopeRefLen (not maxRefLen: the 5-column unique index must stay under the
// Postgres btree index-tuple cap — see the constant).
func scopeRef(p *string, name string) (string, string) {
	if p == nil {
		return "", ""
	}
	v := strings.TrimSpace(*p)
	if v == "" {
		return "", name + " must not be blank when present (omit it for the shared agent scope)"
	}
	if len(v) > maxScopeRefLen {
		return "", name + " too long"
	}
	return v, ""
}

type memoryDTO struct {
	ID             string `json:"id"`
	AgentRef       string `json:"agent_ref"`
	UserRef        string `json:"user_ref,omitempty"`
	SessionRef     string `json:"session_ref,omitempty"`
	Key            string `json:"key"`
	Content        string `json:"content"` // redacted; withheld ("") when integrity == "failed"
	ContentHash    string `json:"content_hash"`
	Classification string `json:"classification"`
	Residency      string `json:"residency_region"`
	ExpiresAt      string `json:"expires_at,omitempty"`
	CreatedBy      string `json:"created_by"`
	// Integrity is set ("failed") only in the admin governance view for an entry
	// that failed the self-check — identifiable for remediation, content withheld.
	Integrity string `json:"integrity,omitempty"`
}

// memoryListResponse is the memory list envelope: the standard list shape plus
// the count of entries WITHHELD by the read-path integrity check (never a
// silent exclusion — docs/SECURITY-HARDENING.md).
type memoryListResponse struct {
	listResponse[memoryDTO]
	IntegrityExcluded int `json:"integrity_excluded,omitempty"`
}

func toMemoryDTO(rec model.Record) memoryDTO {
	return memoryDTO{
		ID: rec.String(model.ColID), AgentRef: rec.String(colAgentRef),
		UserRef: rec.String(colUserRef), SessionRef: rec.String(colSessionRef),
		Key: rec.String(colMemKey), Content: rec.String(colContent), ContentHash: rec.String(colContentHash),
		Classification: rec.String(colClassif), Residency: rec.String(colResidency),
		ExpiresAt: rec.String(colExpiresAt), CreatedBy: rec.String(colCreatedBy),
	}
}

// expired reports whether a memory record is past its expiry as of now. A record
// with no expiry never expires.
func (m *Module) expired(rec model.Record) bool {
	exp := strings.TrimSpace(rec.String(colExpiresAt))
	if exp == "" {
		return false
	}
	ts, err := model.ParseTimestamp(exp)
	if err != nil {
		return false
	}
	return ts.Before(m.clock.Now())
}

// putValidationError is a caller-input problem in a governed memory write that
// maps to 400 (a bad field) — distinct from clientError (422, the per-agent
// quota). Bulk import turns EITHER into a per-row rejection reason instead
// of aborting the whole import, so a single malformed row never blocks the rest.
type putValidationError struct{ msg string }

func (e *putValidationError) Error() string { return e.msg }

// putMemoryEntry is THE governed memory write, shared by the single-entry PUT
// handler and the bulk import. It runs the FULL write-path governance on
// caller-supplied input — fail-closed classification validation, redact-before-
// store (scrub), the per-agent quota, and the audit-atomic-with-persist anchor —
// so a bundle authored elsewhere is treated as untrusted input, never a bypass of
// any write-path control. It returns the persisted DTO and one of: *putValidationError
// (a bad field → 400), *clientError (quota → 422), or a store error.
func (m *Module) putMemoryEntry(ctx context.Context, mc api.ModuleContext, req memoryRequest) (memoryDTO, error) {
	req.AgentRef = strings.TrimSpace(req.AgentRef)
	req.Key = strings.TrimSpace(req.Key)
	if req.AgentRef == "" || req.Key == "" {
		return memoryDTO{}, &putValidationError{"agent_ref and key are required"}
	}
	if len(req.AgentRef) > maxRefLen || len(req.Key) > maxRefLen {
		return memoryDTO{}, &putValidationError{"agent_ref/key too long"}
	}
	userRef, badUser := scopeRef(req.UserRef, "user_ref")
	sessionRef, badSession := scopeRef(req.SessionRef, "session_ref")
	if badUser != "" || badSession != "" {
		return memoryDTO{}, &putValidationError{strings.TrimSpace(strings.Join([]string{badUser, badSession}, " "))}
	}
	if len(req.Content) > maxContentLen {
		return memoryDTO{}, &putValidationError{"content too long"}
	}
	classif := normClass(req.Classification)
	if _, ok := classificationRank[classif]; !ok {
		return memoryDTO{}, &putValidationError{"classification must be one of public, internal, confidential, secret"}
	}
	clean, _, _ := m.scrubWith(req.Content)
	expiresAt := ""
	if req.TTLSeconds > 0 {
		expiresAt = model.NewTimestamp(m.clock.Now().Time().Add(time.Duration(req.TTLSeconds) * time.Second)).String()
	}
	scope := memoryScope{user: userRef, session: sessionRef}
	kind := memoryKind
	if scope.scoped() {
		kind = scopedMemoryKind
	}

	var out memoryDTO
	err := mc.Data.Mutate(ctx, func(sc store.Scope) error {
		repo, err := sc.Ext(kind)
		if err != nil {
			return err
		}
		filters := []model.Filter{eq(colAgentRef, req.AgentRef), eq(colMemKey, req.Key)}
		fields := model.Record{
			colAgentRef: req.AgentRef, colMemKey: req.Key, colContent: clean, colContentHash: hashHex(clean),
			colClassif: classif, colResidency: defaultRegion(req.Residency), colExpiresAt: nullableTS(expiresAt),
			colCreatedBy: mc.Principal.Actor(),
		}
		if scope.scoped() {
			filters = append(filters, eq(colUserRef, scope.user), eq(colSessionRef, scope.session))
			fields[colUserRef], fields[colSessionRef] = scope.user, scope.session
		}
		existing, ok, err := findOne(ctx, repo, filters...)
		if err != nil {
			return err
		}
		var rec model.Record
		if ok {
			for k, v := range fields {
				existing[k] = v
			}
			if rec, err = repo.Update(ctx, existing); err != nil {
				return err
			}
		} else {
			// New key: enforce the per-agent quota across BOTH scopes (a
			// write-access DoS guard a namespaced write must not sidestep).
			count, err := countMemory(ctx, sc, req.AgentRef)
			if err != nil {
				return err
			}
			if count >= maxMemPerAgent {
				return &clientError{"memory quota exceeded for this agent; purge expired entries or remove some"}
			}
			if rec, err = repo.Create(ctx, fields); err != nil {
				return err
			}
		}
		out = toMemoryDTO(rec)
		// The anchor: PayloadHash = entry hash of the PERSISTED record, sealed in
		// the same transaction as the write (audit-atomic-with-persist).
		return auditMemoryEvent(ctx, sc, mc, actionMemoryPut, kind, model.ID(out.ID),
			memoryAuditMeta(rec), memoryEntryHash(kind, rec))
	})
	return out, err
}

// handlePutMemory upserts an agent memory entry (by agent_ref + namespace + key),
// redacting the content and enforcing the per-agent quota. A namespaced write
// (user_ref/session_ref) lands in the scoped entity; the same key coexists per
// namespace without collision. The governed write itself is putMemoryEntry (shared
// with the bulk import).
func (m *Module) handlePutMemory(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var req memoryRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	out, err := m.putMemoryEntry(r.Context(), mc, req)
	if ve, ok := err.(*putValidationError); ok {
		writeJSON(w, http.StatusBadRequest, errorBody(ve.msg))
		return
	}
	if ce, ok := err.(*clientError); ok {
		writeJSON(w, http.StatusUnprocessableEntity, errorBody(ce.msg))
		return
	}
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// countMemory counts an agent's memory entries across both scopes (global +
// user/session-namespaced) — the quota denominator.
func countMemory(ctx context.Context, sc store.Scope, agentRef string) (int, error) {
	total := 0
	for _, kind := range memoryKinds {
		repo, err := sc.Ext(kind)
		if err != nil {
			return 0, err
		}
		recs, err := listAll(ctx, repo, eq(colAgentRef, agentRef))
		if err != nil {
			return 0, err
		}
		total += len(recs)
	}
	return total, nil
}

// memoryReadGrants resolves the reader's retrieval grants for memory CLASSIFICATION
// filtering. It is DENY-CLOSED: a nil guard, a resolve error, or a guard denial all
// fall back to public-only clearance, so a classified entry never leaks to a reader whose
// clearance cannot be established. Shared by every memory read/act-by-visibility path
// (list/get/delete) so the clearance predicate cannot drift between them (E5). agentRef is
// the declared ?agent_ref= read context.
func (m *Module) memoryReadGrants(ctx context.Context, mc api.ModuleContext, agentRef string) Grants {
	publicOnly := Grants{Allowed: true, Clearance: classPublic}
	if m.guard == nil {
		return publicOnly
	}
	grants, err := m.guard.Resolve(ctx, mc.Tenant, mc.Principal.Actor(), agentRef, "")
	if err != nil || !grants.Allowed {
		return publicOnly
	}
	return grants
}

// handleListMemory lists the memory visible to the DECLARED read context
// (?agent_ref= plus the ?user_ref=/?session_ref= namespace), EXCLUDING
// expired entries (lazy expiry), entries above the reader's classification
// clearance, and entries that fail the integrity self-check (fail closed;
// withheld count reported, detection audited). Without a declared user/session
// the context sees only the shared agent-global scope — deny-closed: one user's
// (or session's) memory never leaks into another's context assembly.
func (m *Module) handleListMemory(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	agentRef := strings.TrimSpace(r.URL.Query().Get("agent_ref"))
	dc := scopeFromQuery(r)
	grants := m.memoryReadGrants(r.Context(), mc, agentRef)
	out := memoryListResponse{listResponse: listResponse[memoryDTO]{Items: []memoryDTO{}}}
	var tampered []tamperRef
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
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
				if m.expired(rec) || !recScope(rec).visibleIn(dc) ||
					!classificationAllowed(rec.String(colClassif), grants.Clearance) {
					continue
				}
				if !memoryContentIntact(rec) {
					tampered = append(tampered, tamperRef{kind: kind, id: model.ID(rec.String(model.ColID)),
						agent: rec.String(colAgentRef), key: rec.String(colMemKey)})
					continue
				}
				out.Items = append(out.Items, toMemoryDTO(rec))
			}
		}
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	out.IntegrityExcluded = len(tampered)
	writeJSON(w, http.StatusOK, out)
	// Evidence AFTER the response: the withholding already happened; recording
	// it (deduped, see reportMemoryTamper) must not sit on the read latency.
	m.reportMemoryTamper(r.Context(), mc, tampered)
}

// handleListAllMemory is the ADMIN governance view: every scope, no
// visibility predicate — the operator surface for remediation and for auditing
// what namespaces exist. ?agent_ref=/?user_ref=/?session_ref= are EXACT-match
// filters here (not a read context). An entry failing the self-check is listed
// for remediation with integrity="failed" and its content WITHHELD (tampered
// bytes never passed redaction and must not be served), and the detection is
// recorded exactly like any other read path.
func (m *Module) handleListAllMemory(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	agentRef := strings.TrimSpace(r.URL.Query().Get("agent_ref"))
	fc := scopeFromQuery(r)
	out := memoryListResponse{listResponse: listResponse[memoryDTO]{Items: []memoryDTO{}}}
	var tampered []tamperRef
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
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
				if m.expired(rec) {
					continue
				}
				es := recScope(rec)
				if (fc.user != "" && es.user != fc.user) || (fc.session != "" && es.session != fc.session) {
					continue
				}
				dto := toMemoryDTO(rec)
				if !memoryContentIntact(rec) {
					tampered = append(tampered, tamperRef{kind: kind, id: model.ID(dto.ID),
						agent: dto.AgentRef, key: dto.Key})
					dto.Content, dto.Integrity = "", "failed"
				}
				out.Items = append(out.Items, dto)
			}
		}
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	out.IntegrityExcluded = len(tampered)
	writeJSON(w, http.StatusOK, out)
	m.reportMemoryTamper(r.Context(), mc, tampered)
}

// findMemoryByID probes both memory entities for id. Returns the kind that held
// it. Absent-everywhere reports found=false (and the caller 404s, keeping
// other-tenant and absent indistinguishable).
func findMemoryByID(ctx context.Context, sc store.Scope, id model.ID) (model.Kind, model.Record, bool, error) {
	for _, kind := range memoryKinds {
		repo, err := sc.Ext(kind)
		if err != nil {
			return "", nil, false, err
		}
		rec, err := repo.Get(ctx, id)
		if err != nil {
			if isNotFound(err) {
				continue
			}
			return "", nil, false, err
		}
		return kind, rec, true, nil
	}
	return "", nil, false, nil
}

// handleGetMemory returns one memory entry. 404 if expired, absent, scoped OUTSIDE the
// declared read context (no existence leak across namespaces), or classified ABOVE the
// reader's clearance (E5 — the SAME classification filter handleListMemory applies, so a
// reader that knows/guesses a classified entry's id cannot retrieve it whole; the deny is
// indistinguishable from absent); 409 integrity_violation (entry withheld, detection
// recorded) if it fails the self-check.
func (m *Module) handleGetMemory(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	dc := scopeFromQuery(r)
	grants := m.memoryReadGrants(r.Context(), mc, strings.TrimSpace(r.URL.Query().Get("agent_ref")))
	var out memoryDTO
	found, tamperedRef := false, (*tamperRef)(nil)
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		kind, rec, ok, err := findMemoryByID(r.Context(), sc, id)
		if err != nil {
			return err
		}
		if !ok || m.expired(rec) || !recScope(rec).visibleIn(dc) ||
			!classificationAllowed(rec.String(colClassif), grants.Clearance) {
			return nil
		}
		if !memoryContentIntact(rec) {
			tamperedRef = &tamperRef{kind: kind, id: id, agent: rec.String(colAgentRef), key: rec.String(colMemKey)}
			return nil
		}
		found, out = true, toMemoryDTO(rec)
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if tamperedRef != nil {
		writeJSON(w, http.StatusConflict, integrityViolationBody())
		m.reportMemoryTamper(r.Context(), mc, []tamperRef{*tamperedRef})
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, errorBody("not found"))
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleDeleteMemory removes one memory entry. The declared read context gates
// a scoped entry exactly like a read (a session can only delete what it can
// see; mismatch is a 404, no existence leak), AND an entry classified ABOVE the
// caller's clearance is equally invisible (E5 — "you can only act on what you can
// see" holds on the classification axis too, so a low-clearance writer cannot
// destroy an entry it may not read). A tampered entry IS deletable — deletion is
// the remediation.
func (m *Module) handleDeleteMemory(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	dc := scopeFromQuery(r)
	grants := m.memoryReadGrants(r.Context(), mc, strings.TrimSpace(r.URL.Query().Get("agent_ref")))
	// an active legal hold vetoes the delete. The subjects are the row's
	// agent and its user/session namespace, so the row is read first (a
	// View — the gate itself runs OUTSIDE any store tx); an absent, out-of-context
	// or over-classified row falls through to the 404 below.
	var rowKind model.Kind
	var rowScope memoryScope
	agentRef, found := "", false
	if err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		kind, rec, ok, err := findMemoryByID(r.Context(), sc, id)
		if err != nil {
			return err
		}
		if !ok || !recScope(rec).visibleIn(dc) || !classificationAllowed(rec.String(colClassif), grants.Clearance) {
			return nil
		}
		rowKind, rowScope, agentRef, found = kind, recScope(rec), rec.String(colAgentRef), true
		return nil
	}); err != nil {
		writeStoreError(w, err)
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, errorBody("not found"))
		return
	}
	if m.enforceHold(r.Context(), w, mc.Tenant, holdSubjectAgent, agentRef, holdClassAgentMemory) {
		return
	}
	if rowScope.user != "" && m.enforceHold(r.Context(), w, mc.Tenant, holdSubjectUser, rowScope.user, holdClassAgentMemory) {
		return
	}
	if rowScope.session != "" && m.enforceHold(r.Context(), w, mc.Tenant, holdSubjectSession, rowScope.session, holdClassAgentMemory) {
		return
	}
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(rowKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		// Re-check the namespace AND classification clearance on the in-tx row (the probe
		// ran outside the tx) — a deny is ErrNotFound, indistinguishable from absent.
		if !recScope(rec).visibleIn(dc) || !classificationAllowed(rec.String(colClassif), grants.Clearance) {
			return store.ErrNotFound
		}
		if err := repo.Delete(r.Context(), id); err != nil {
			return err
		}
		// The delete event — anchored to the entry state just destroyed — seals
		// in the same atomic transaction (audit-atomic-with-persist): if the
		// append fails the delete rolls back. Row lock first, ledger append
		// last — the lock order every memory mutation shares (see file header).
		return auditMemoryEvent(r.Context(), sc, mc, actionMemoryDelete, rowKind, id,
			memoryAuditMeta(rec), memoryEntryHash(rowKind, rec))
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

// handlePurgeMemory materializes expiry: it deletes expired memory entries of
// BOTH scopes (optionally for one agent). Admin-tier. It is the explicit,
// tenant-scoped purge a module must use instead of a cross-tenant background
// sweep (a module cannot enumerate tenants). With the hold-gate
// wired, expired rows whose agent — or, whose user/session namespace — is
// under an active subject-hold are EXCLUDED row by row and reported as
// excluded_held (response + audit meta) — an unfiltered purge can never destroy
// what the agent-filtered purge would 423 on.
func (m *Module) handlePurgeMemory(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	agentRef := strings.TrimSpace(r.URL.Query().Get("agent_ref"))
	// an active legal hold vetoes the purge — class agent.memory always,
	// plus subject ("agent", agent_ref) when the purge is agent-filtered (one
	// call evaluates tenant-wide + class + subject, the §4 matching rule).
	subjectKind, subjectRef := "", ""
	if agentRef != "" {
		subjectKind, subjectRef = holdSubjectAgent, agentRef
	}
	if m.enforceHold(r.Context(), w, mc.Tenant, subjectKind, subjectRef, holdClassAgentMemory) {
		return
	}
	// heldSubjects caches one gate verdict per DISTINCT subject (kind, ref) for
	// this request. An agent-filtered purge that reached here already cleared its
	// agent subject via the upfront call, so it is seeded as not held.
	heldSubjects := map[string]bool{}
	subjKey := func(kind, ref string) string { return kind + "\x00" + ref }
	if agentRef != "" {
		heldSubjects[subjKey(holdSubjectAgent, agentRef)] = false
	}

	// Collect the expired candidates in a read, so the per-row subject-hold
	// exclusion below (gate calls into another module — outside the store tx)
	// concludes BEFORE any delete.
	type candidate struct {
		kind     model.Kind
		id       model.ID
		subjects [][2]string // the (kind, ref) hold subjects gating this row
	}
	rowSubjects := func(rec model.Record) [][2]string {
		subs := [][2]string{{holdSubjectAgent, rec.String(colAgentRef)}}
		if u := rec.String(colUserRef); u != "" {
			subs = append(subs, [2]string{holdSubjectUser, u})
		}
		if s := rec.String(colSessionRef); s != "" {
			subs = append(subs, [2]string{holdSubjectSession, s})
		}
		return subs
	}
	var candidates []candidate
	if err := mc.Data.View(r.Context(), func(sc store.Scope) error {
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
				if !m.expired(rec) {
					continue
				}
				candidates = append(candidates, candidate{kind: kind,
					id: model.ID(rec.String(model.ColID)), subjects: rowSubjects(rec)})
			}
		}
		return nil
	}); err != nil {
		writeStoreError(w, err)
		return
	}

	// Per-row exclusion (mirroring the compliance sweep's excluded_held):
	// an UNFILTERED purge must not destroy rows of a subject under a hold —
	// dropping ?agent_ref must never bypass the 423 the filtered purge gets.
	// Each distinct subject is checked once. A gate ERROR at ANY point denies
	// the WHOLE purge (503, fail closed): every check runs before any delete,
	// so nothing is destroyed.
	if m.holdGate != nil {
		for _, c := range candidates {
			for _, sub := range c.subjects {
				key := subjKey(sub[0], sub[1])
				if _, ok := heldSubjects[key]; ok {
					continue
				}
				held, _, err := m.holdGate.Check(r.Context(), mc.Tenant, sub[0], sub[1], holdClassAgentMemory)
				if err != nil {
					m.errorf("knowledge: legal-hold check failed; denying deletion (fail closed)",
						"subject_kind", sub[0], "data_class", holdClassAgentMemory, "err", err)
					writeJSON(w, http.StatusServiceUnavailable, errorBody("legal-hold check unavailable; deletion denied (fail closed)"))
					return
				}
				heldSubjects[key] = held
			}
		}
	}

	purged, excluded := 0, 0
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		for _, c := range candidates {
			repo, err := sc.Ext(c.kind)
			if err != nil {
				return err
			}
			rec, err := repo.Get(r.Context(), c.id)
			if err != nil {
				if isNotFound(err) {
					continue // already gone — purge is idempotent
				}
				return err
			}
			// Re-check inside the tx: a row refreshed (TTL extended/cleared)
			// since the read is no longer expired and must survive.
			if !m.expired(rec) {
				continue
			}
			if m.holdGate != nil {
				// Trust only the verdicts for the subjects the row carries NOW;
				// a subject never checked is over-preserved (the safe direction).
				blocked := false
				for _, sub := range rowSubjects(rec) {
					held, checked := heldSubjects[subjKey(sub[0], sub[1])]
					if !checked || held {
						blocked = true
						break
					}
				}
				if blocked {
					excluded++
					continue
				}
			}
			if err := repo.Delete(r.Context(), c.id); err != nil {
				return err
			}
			purged++
		}
		if purged > 0 || excluded > 0 {
			return auditEvent(r.Context(), sc, mc, actionMemoryPurge, memoryKind, "",
				map[string]any{"purged": purged, "excluded_held": excluded, "agent": agentRef})
		}
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"purged": purged, "excluded_held": excluded})
}

// defaultRegion returns the region or "global".
func defaultRegion(region string) string {
	if strings.TrimSpace(region) == "" {
		return "global"
	}
	return region
}

// nullableTS maps an empty timestamp string to nil (a NULL expires_at = no expiry).
func nullableTS(ts string) any {
	if ts == "" {
		return nil
	}
	return ts
}
