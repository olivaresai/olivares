// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package compliance

import (
	"context"
	"errors"
	"strings"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// This file is the ERASURE-TARGET REGISTRY: a fixed, in-code catalog (the
// dataclass.go discipline — versioned in the binary, every kind string and column
// VERIFIED against the owning module's schema before being pinned, file references
// inline) of WHERE a data subject's PII lives in the control plane's own stores and
// HOW it is physically erased. The registry probes by KIND string only — compliance
// never imports a sibling module; a kind whose module is not registered in this
// deployment is an honest no-op (nothing of it exists to erase).
//
// Two modes:
//   - erase: the subject's rows are HARD-deleted (the row IS the subject's data).
//   - scrub: only the matching identifier column(s) are nulled — the row's
//     operational value (counts, costs, lifecycle state) is retained, de-identified.
//
// Verification notes (2026-06-11, this tree):
//   - knowledge.memory: agent_ref/content at modules/knowledge/schema.go:117-122.
//   - sessions.live: session_ref/agent_ref/goal/summary at modules/sessions/schema.go:26-43;
//     sessions.timeline: session_ref at modules/sessions/schema.go:46-55.
//   - voice.session: session_ref/agent_ref/principal_ref at modules/voice/schema.go:25-31
//     (principal_ref is the real opener — an audit-actor string).
//   - finops.cost_sample: actor (raw org-internal email from the Claude Code
//     Analytics ingest, connectors/claude-api/wire.go) at modules/finops/schema.go:131;
//     cost_record_id links the canonical ledger row whose Metadata["actor"]
//     duplicates it (modules/finops/ingest.go:301-306). The dedup sample_key is
//     deliberately KEPT on scrub: a re-delivery of the same bucket then collapses
//     onto the scrubbed row (applySampleValues touches value columns only,
//     modules/finops/ingest.go:219-226) instead of re-creating the identifier.
//   - governance.nhi_lifecycle: owner_ref/sponsor_ref (external_ids of roster HUMAN
//     identities — directory external ids are often emails) at
//     modules/governance/schema.go:53-56.
//   - knowledge.base/document/chunk + knowledge.sensitivity_label: the document
//     cascade columns at modules/knowledge/schema.go:66-96,147-163 (kb_ref, doc_ref,
//     chunk doc_ref, label subject_kind/subject_ref; KB doc_count/chunk_count
//     counters at schema.go:62-63). Chunks carry their embedding in-row
//     (schema.go:92), so deleting chunks deletes the vectors.

// Erasure subject kinds (the §4 open hold vocabulary contract). "identity"
// addresses a roster identity by its external_id convergence anchor.
const (
	erasureSubjectUser     = "user"
	erasureSubjectAgent    = "agent"
	erasureSubjectSession  = "session"
	erasureSubjectDocument = "document"
	erasureSubjectIdentity = "identity"
)

// erasureSubjectKinds is the closed set of supported subject kinds, in
// presentation order.
var erasureSubjectKinds = []string{
	erasureSubjectUser, erasureSubjectAgent, erasureSubjectSession,
	erasureSubjectDocument, erasureSubjectIdentity,
}

// Erasure target modes.
const (
	eraseModeDelete = "erase"
	eraseModeScrub  = "scrub"
)

// erasureTarget declares one erasable store: the ext kind, the §2 data class it
// belongs to ("" when unclassified — the tenant/subject hold checks still apply),
// the per-subject-kind identifier columns (exact-equality match, like holdCovers)
// and the mode. ScrubExtra are non-identifier columns nulled alongside a scrub
// (derived prose that may embed the identifier).
type erasureTarget struct {
	Label          string
	Kind           model.Kind
	DataClass      string
	SubjectColumns map[string][]string // subject kind -> columns carrying its ref
	Mode           string
}

// erasureTargetRegistry is the fixed catalog, in execution order.
var erasureTargetRegistry = []erasureTarget{
	{
		Label:          "knowledge.memory",
		Kind:           "knowledge.memory",
		DataClass:      classAgentMemory,
		SubjectColumns: map[string][]string{erasureSubjectAgent: {"agent_ref"}},
		Mode:           eraseModeDelete,
	},
	{
		// the per-user/per-session memory namespace (modules/knowledge/
		// schema.go scopedMemoryKind — user_ref/session_ref columns). The finer
		// subject mappings are the point: an RTBF request for a USER or SESSION
		// subject now erases exactly that namespace's memory, which the
		// agent-global table could never address.
		Label:     "knowledge.memory_scoped",
		Kind:      "knowledge.memory_scoped",
		DataClass: classAgentMemory,
		SubjectColumns: map[string][]string{
			erasureSubjectAgent:   {"agent_ref"},
			erasureSubjectUser:    {"user_ref"},
			erasureSubjectSession: {"session_ref"},
		},
		Mode: eraseModeDelete,
	},
	{
		Label:     "sessions.live",
		Kind:      "sessions.live",
		DataClass: classSessionTimeline,
		SubjectColumns: map[string][]string{
			erasureSubjectAgent:   {"agent_ref"},
			erasureSubjectSession: {"session_ref"},
		},
		Mode: eraseModeDelete,
	},
	{
		Label:          "sessions.timeline",
		Kind:           "sessions.timeline",
		DataClass:      classSessionTimeline,
		SubjectColumns: map[string][]string{erasureSubjectSession: {"session_ref"}},
		Mode:           eraseModeDelete,
	},
	{
		Label:     "voice.session",
		Kind:      "voice.session",
		DataClass: classVoiceSession,
		SubjectColumns: map[string][]string{
			erasureSubjectAgent:   {"agent_ref"},
			erasureSubjectSession: {"session_ref"},
			erasureSubjectUser:    {"principal_ref"},
		},
		Mode: eraseModeDelete,
	},
	{
		Label:          "finops.cost_sample",
		Kind:           "finops.cost_sample",
		DataClass:      classCostSample,
		SubjectColumns: map[string][]string{erasureSubjectUser: {"actor"}},
		Mode:           eraseModeScrub,
	},
	{
		Label: "governance.nhi_lifecycle",
		Kind:  "governance.nhi_lifecycle",
		SubjectColumns: map[string][]string{
			erasureSubjectIdentity: {"owner_ref", "sponsor_ref"},
		},
		Mode: eraseModeScrub,
	},
}

// Bounded-batch discipline (the sweep precedent): one bounded transaction per
// batch, holds re-evaluated INSIDE each destructive transaction, an iteration cap so
// one execute can never scan unboundedly. A capped run reports truncated and the
// next execute continues — matching is idempotent.
const (
	maxEraseBatch      = 200
	maxEraseIterations = 50
)

// targetOutcome is one target's honest result in the receipt.
type targetOutcome struct {
	Target       string `json:"target"`
	Mode         string `json:"mode"`
	Examined     int64  `json:"examined"`
	Erased       int64  `json:"erased"`
	Scrubbed     int64  `json:"scrubbed"`
	ExcludedHeld int64  `json:"excluded_held"`
	Truncated    bool   `json:"truncated,omitempty"`
	Status       string `json:"status"` // ok | absent | truncated | failed
	Detail       string `json:"detail,omitempty"`
}

// affectedClasses returns the distinct §2 class ids the subject kind can touch —
// the classes the hold-gate must clear in addition to the subject itself. The
// document cascade is registry-external but class-mapped.
func affectedClasses(subjectKind string) []string {
	seen := map[string]struct{}{}
	var out []string
	add := func(class string) {
		if class == "" {
			return
		}
		if _, dup := seen[class]; dup {
			return
		}
		seen[class] = struct{}{}
		out = append(out, class)
	}
	for _, t := range erasureTargetRegistry {
		if _, ok := t.SubjectColumns[subjectKind]; ok {
			add(t.DataClass)
		}
	}
	if subjectKind == erasureSubjectDocument {
		add(classKnowledgeContent)
	}
	return out
}

// erasureRowAction is the per-row deletion evidence appended for an erased
// AGENT-MEMORY row (class agent.memory): the ledger-anchor model proves a
// live row against its LAST put/delete event, so an RTBF deletion with no
// per-row event would leave the stale put anchor in place and a resurrected
// (backup-replayed) legally-erased row would verify CLEAN. Memory erasures are
// subject-scoped and batch-capped, so the per-row events are bounded; the
// retention sweep keeps its summary-only evidence (the documented
// ledger-size tradeoff — Contract §6). knowledge/memory_integrity.go
// quotes this literal as a cross-module contract (modules never import each
// other).
const erasureRowAction = "compliance.erasure.row"

// eraseRegistryTarget runs ONE registry target for a subject's identifiers, in
// bounded keyset-paginated batches, re-checking holds inside every destructive
// transaction (a hold set mid-run stops the next batch — over-preservation is
// the safe direction).: a row attributable to MULTIPLE hold subjects
// (knowledge.memory_scoped carries agent + user + session) is excluded —
// preserved and counted in excluded_held — when ANY of its mapped subjects is
// under an active hold, not just the request's own subject: every other
// destruction path over the same row (sweep, knowledge delete/purge) honors
// those holds, and the irreversible path must never be the permissive one.
// It returns an honest outcome; err is a transport/store failure (the caller
// marks the run failed and shreds nothing).
func (m *Module) eraseRegistryTarget(ctx context.Context, tenant model.TenantID, t erasureTarget, actor, actorKind, subjectKind string, refs []string) (targetOutcome, error) {
	out := targetOutcome{Target: t.Label, Mode: t.Mode, Status: "ok"}
	cols, ok := t.SubjectColumns[subjectKind]
	if !ok {
		out.Status = "absent"
		out.Detail = "target carries no " + subjectKind + " identifier"
		return out, nil
	}

	// rowHeldByOtherSubject reports whether ANY of the row's mapped subject
	// dimensions (other than the request's own, already cleared for this batch)
	// is covered by an active hold — the §4 exact-match rule per (kind, ref),
	// plus the target's class.
	rowHeldByOtherSubject := func(holds []model.Record, rec model.Record, reqRef string) bool {
		for subKind, subCols := range t.SubjectColumns {
			for _, c := range subCols {
				v := rec.String(c)
				if v == "" || (subKind == subjectKind && v == reqRef) {
					continue
				}
				sub := HoldSubject{Kind: subKind, Ref: v, DataClass: t.DataClass}
				for _, h := range holds {
					if holdCovers(h, sub) {
						return true
					}
				}
			}
		}
		return false
	}

	for _, col := range cols {
		for _, ref := range refs {
			// The iteration cap bounds the BATCHES PER (column, identifier) pair —
			// not the probe count — so a subject with many aliases cannot trip the
			// cap on empty probes alone (every pair stays individually bounded).
			iterations := 0
			cursor := ""
			for {
				if iterations >= maxEraseIterations {
					out.Truncated, out.Status = true, "truncated"
					out.Detail = "batch cap reached; re-execute to continue"
					return out, nil
				}
				iterations++
				stop := false
				err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
					// Hold re-check INSIDE the destructive transaction (§6 order:
					// holds first, then candidates — the sweep discipline).
					dec, err := evalHolds(ctx, sc, HoldSubject{Kind: subjectKind, Ref: ref, DataClass: t.DataClass})
					if err != nil {
						return err
					}
					if dec.Held {
						return errErasureHeld
					}
					// One active-holds load per batch for the per-row OTHER-subject
					// exclusion (the sweep's subjectHeld discipline, in-tx).
					holds, err := activeHoldRows(ctx, sc)
					if err != nil {
						return err
					}
					repo, err := sc.Ext(t.Kind)
					if err != nil {
						if errors.Is(err, store.ErrUnknownEntity) {
							// The owning module is not registered: nothing of this
							// kind exists here. The receipt must say ABSENT, not
							// "ok" — an "ok" would read as examined-and-clean.
							out.Status = "absent"
							out.Detail = "owning module not registered in this deployment"
							stop = true
							return nil
						}
						return err
					}
					// Keyset pagination (not re-query-from-start): an excluded row
					// STAYS in the match set, so restarting would re-examine it
					// every batch and a page full of held rows would loop to the
					// cap; the cursor advances past exclusions exactly once.
					recs, page, err := repo.List(ctx, model.Query{
						Filters: []model.Filter{eq(col, ref)},
						Limit:   maxEraseBatch,
						Cursor:  cursor,
					})
					if err != nil {
						return err
					}
					for _, rec := range recs {
						out.Examined++
						if rowHeldByOtherSubject(holds, rec, ref) {
							out.ExcludedHeld++
							continue
						}
						switch t.Mode {
						case eraseModeDelete:
							if err := repo.Delete(ctx, model.ID(rec.String(model.ColID))); err != nil {
								return err
							}
							if t.DataClass == classAgentMemory {
								// Per-row deletion anchor for memory (see
								// erasureRowAction) — row delete first, ledger
								// append last (the knowledge lock-order rule).
								if _, err := sc.Audit().Append(ctx, model.AuditDraft{
									Actor: actor, ActorKind: actorKind,
									Action:     erasureRowAction,
									TargetKind: t.Kind,
									TargetID:   model.ID(rec.String(model.ColID)),
								}); err != nil {
									return err
								}
							}
							out.Erased++
						case eraseModeScrub:
							if err := m.scrubRow(ctx, sc, repo, t, rec, cols, ref); err != nil {
								return err
							}
							out.Scrubbed++
						}
					}
					cursor = page.Cursor
					stop = !page.HasMore || page.Cursor == ""
					return nil
				})
				if err != nil {
					return out, err
				}
				if stop {
					break
				}
			}
		}
	}
	if out.ExcludedHeld > 0 && out.Detail == "" {
		out.Detail = "rows under another subject's active legal hold were preserved (excluded_held)"
	}
	return out, nil
}

// errErasureHeld signals a hold appeared between the gate check and a destructive
// batch: the batch transaction aborts having destroyed nothing.
var errErasureHeld = errors.New("compliance: subject became legally held mid-erasure; batch aborted")

// scrubRow nulls the matching identifier column(s) on one row. For
// finops.cost_sample it also de-identifies the linked canonical cost_records row
// (Metadata["actor"], modules/finops/ingest.go:301-306) — the same identifier
// duplicated at write time must fall in the same act.
func (m *Module) scrubRow(ctx context.Context, sc store.Scope, repo store.GenericRepo, t erasureTarget, rec model.Record, cols []string, ref string) error {
	for _, c := range cols {
		if rec.String(c) == ref {
			rec[c] = nil
		}
	}
	if _, err := repo.Update(ctx, rec); err != nil {
		return err
	}
	if t.Kind == "finops.cost_sample" {
		if crID := rec.String("cost_record_id"); crID != "" {
			cr, err := sc.Costs().Get(ctx, model.ID(crID))
			if err != nil {
				if errors.Is(err, store.ErrNotFound) {
					return nil // id drift tolerated, like updateCostRecord
				}
				return err
			}
			if _, has := cr.Metadata["actor"]; has {
				delete(cr.Metadata, "actor")
				if _, err := sc.Costs().Update(ctx, cr); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// eraseDocumentCascade erases ONE knowledge document by row id: chunks (embeddings
// live in-row), its sensitivity labels, the KB counters and finally the document
// row — mirroring the knowledge KB-delete cascade (modules/knowledge/kb.go) at
// document granularity. Append-only knowledge evidence (lineage, pii_scan,
// dlp_event) is deliberately retained: ids, hashes and counts only (docs/SECURITY-HARDENING.md).
// The per-document hold was already cleared by the gate (the subject IS the
// document); it is re-checked here inside the destructive transaction.
func (m *Module) eraseDocumentCascade(ctx context.Context, tenant model.TenantID, docID string) (targetOutcome, error) {
	out := targetOutcome{Target: "knowledge.document", Mode: eraseModeDelete, Status: "ok"}
	err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		dec, err := evalHolds(ctx, sc, HoldSubject{Kind: erasureSubjectDocument, Ref: docID, DataClass: classKnowledgeContent})
		if err != nil {
			return err
		}
		if dec.Held {
			return errErasureHeld
		}
		docs, err := sc.Ext(model.Kind("knowledge.document"))
		if err != nil {
			if errors.Is(err, store.ErrUnknownEntity) {
				out.Status, out.Detail = "absent", "knowledge module not registered"
				return nil
			}
			return err
		}
		doc, err := docs.Get(ctx, model.ID(docID))
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				out.Status, out.Detail = "absent", "document not found (already erased?)"
				return nil
			}
			return err
		}
		out.Examined++

		chunks, err := sc.Ext(model.Kind("knowledge.chunk"))
		if err != nil {
			return err
		}
		var chunksGone int64
		for {
			recs, _, err := chunks.List(ctx, model.Query{
				Filters: []model.Filter{eq("doc_ref", docID)}, Limit: maxEraseBatch,
			})
			if err != nil {
				return err
			}
			if len(recs) == 0 {
				break
			}
			for _, rec := range recs {
				if err := chunks.Delete(ctx, model.ID(rec.String(model.ColID))); err != nil {
					return err
				}
				chunksGone++
			}
		}

		// The document's CURRENT sensitivity labels name the document — they
		// go with it. The append-only pii_scan coverage evidence stays (counts only).
		labels, err := sc.Ext(model.Kind("knowledge.sensitivity_label"))
		if err == nil {
			recs, lerr := listAll(ctx, labels, eq(colSubjectKind, "document"), eq(colSubjectRef, docID))
			if lerr != nil {
				return lerr
			}
			for _, rec := range recs {
				if err := labels.Delete(ctx, model.ID(rec.String(model.ColID))); err != nil {
					return err
				}
			}
		} else if !errors.Is(err, store.ErrUnknownEntity) {
			return err
		}

		// Keep the owning KB's denormalized counters honest
		// (modules/knowledge/schema.go:62-63).
		if kbRef := doc.String("kb_ref"); kbRef != "" {
			kbs, err := sc.Ext(model.Kind("knowledge.base"))
			if err == nil {
				kb, kerr := kbs.Get(ctx, model.ID(kbRef))
				if kerr == nil {
					if n := kb.Int("doc_count"); n > 0 {
						kb["doc_count"] = n - 1
					}
					if n := kb.Int("chunk_count"); n >= chunksGone {
						kb["chunk_count"] = n - chunksGone
					} else {
						kb["chunk_count"] = int64(0)
					}
					if _, err := kbs.Update(ctx, kb); err != nil {
						return err
					}
				} else if !errors.Is(kerr, store.ErrNotFound) {
					return kerr
				}
			} else if !errors.Is(err, store.ErrUnknownEntity) {
				return err
			}
		}

		if err := docs.Delete(ctx, model.ID(docID)); err != nil {
			return err
		}
		out.Erased = 1 + chunksGone
		out.Detail = "document + " + itoa(chunksGone) + " chunk(s) + labels erased"
		return nil
	})
	return out, err
}

// eraseRosterIdentity scrubs ONE core roster identity (the NHI/HUMAN identities
// table, core/internal/store/sqlstore/catalog.go:405-415) addressed by its
// external_id: the directory display name (a person's name for HUMAN identities),
// the free-form metadata AND the external_id itself (frequently an email/UPN — a
// direct identifier) are de-identified in place; the row id and its lifecycle
// survive as a tombstone. PRECONDITION (documented in docs/RIGHT-TO-ERASURE.md):
// the identity must be erased at the source IdP FIRST — the roster converges on
// the directory, and once the source stops carrying the external_id nothing
// re-imports it; the historic owner/sponsor references in APPEND-ONLY governance
// trails are retained-with-basis (the receipt's reconciliation).
func (m *Module) eraseRosterIdentity(ctx context.Context, tenant model.TenantID, externalID string) (targetOutcome, error) {
	out := targetOutcome{Target: "core.identities", Mode: eraseModeScrub, Status: "ok"}
	err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		dec, err := evalHolds(ctx, sc, HoldSubject{Kind: erasureSubjectIdentity, Ref: externalID})
		if err != nil {
			return err
		}
		if dec.Held {
			return errErasureHeld
		}
		ids, _, err := sc.Identities().List(ctx, model.Query{
			Filters: []model.Filter{eq("external_id", externalID)}, Limit: maxEraseBatch,
		})
		if err != nil {
			return err
		}
		for _, id := range ids {
			out.Examined++
			id.Name = erasedTokenDisplay
			id.Metadata = nil
			// The scrubbed anchor stays unique per row and non-identifying; a
			// re-execute finds nothing by the original ref (naturally idempotent).
			id.ExternalID = "erased:" + strings.ToLower(id.ID.String())
			if _, err := sc.Identities().Update(ctx, id); err != nil {
				return err
			}
			out.Scrubbed++
		}
		if out.Examined == 0 {
			out.Status, out.Detail = "absent", "no roster identity with that external_id (already scrubbed, or never imported)"
		}
		return nil
	})
	return out, err
}

// eraseCostLedgerActor de-identifies the canonical cost_records ledger DIRECTLY for
// a user subject: the registry's cost_sample scrub propagates through
// cost_record_id, but a ledger row whose read-model sample was already
// retention-purged would otherwise keep Metadata["actor"] forever. Bounded pages,
// holds re-checked per destructive transaction.
func (m *Module) eraseCostLedgerActor(ctx context.Context, tenant model.TenantID, refs []string) (targetOutcome, error) {
	out := targetOutcome{Target: "core.cost_records", Mode: eraseModeScrub, Status: "ok"}
	matches := func(meta map[string]any, refs []string) bool {
		actor, _ := meta["actor"].(string)
		if actor == "" {
			return false
		}
		for _, ref := range refs {
			if actor == ref {
				return true
			}
		}
		return false
	}
	iterations := 0
	cursor := ""
	for {
		if iterations >= maxEraseIterations {
			out.Truncated, out.Status = true, "truncated"
			out.Detail = "batch cap reached; re-execute to continue"
			return out, nil
		}
		iterations++
		stop := false
		err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
			// One evalHolds per identifier covers tenant + class + subject holds
			// (the §4 matching rule) inside this destructive transaction.
			for _, ref := range refs {
				dec, err := evalHolds(ctx, sc, HoldSubject{Kind: erasureSubjectUser, Ref: ref, DataClass: classCostSample})
				if err != nil {
					return err
				}
				if dec.Held {
					return errErasureHeld
				}
			}
			recs, page, err := sc.Costs().List(ctx, model.Query{Limit: maxEraseBatch, Cursor: cursor})
			if err != nil {
				return err
			}
			for _, cr := range recs {
				if !matches(cr.Metadata, refs) {
					continue
				}
				out.Examined++
				delete(cr.Metadata, "actor")
				if _, err := sc.Costs().Update(ctx, cr); err != nil {
					return err
				}
				out.Scrubbed++
			}
			cursor = page.Cursor
			stop = !page.HasMore || page.Cursor == ""
			return nil
		})
		if err != nil {
			return out, err
		}
		if stop {
			return out, nil
		}
	}
}

// runErasureTargets executes every applicable physical erasure for the subject,
// SCOPED to the request's data classes (the set the plan hash bound and the
// approvers saw — execution must never exceed the approved/hold-checked scope; a
// registry target whose class is outside it is skipped and the receipt says so).
// It stops at the first transport/store failure (the caller marks the request
// failed; nothing is shredded) but maps a mid-run hold appearance to a clean held
// outcome.
func (m *Module) runErasureTargets(ctx context.Context, tenant model.TenantID, actor, actorKind string, key subjectKey, classes []string) ([]targetOutcome, error) {
	inScope := func(class string) bool {
		if class == "" {
			return true // unclassified targets (roster/NHI overlay) are subject-gated only
		}
		for _, c := range classes {
			if c == class {
				return true
			}
		}
		return false
	}
	refs := key.identifiers()
	var outcomes []targetOutcome
	for _, t := range erasureTargetRegistry {
		if !inScope(t.DataClass) {
			if _, applies := t.SubjectColumns[key.Kind]; applies {
				outcomes = append(outcomes, targetOutcome{Target: t.Label, Mode: t.Mode,
					Status: "skipped", Detail: "outside the request's data_class scope (" + t.DataClass + ")"})
			}
			continue
		}
		o, err := m.eraseRegistryTarget(ctx, tenant, t, actor, actorKind, key.Kind, refs)
		if err != nil {
			if errors.Is(err, errErasureHeld) {
				o.Status, o.Detail = "failed", "blocked by a legal hold set mid-run"
				outcomes = append(outcomes, o)
			}
			return outcomes, err
		}
		outcomes = append(outcomes, o)
	}
	switch key.Kind {
	case erasureSubjectUser:
		if inScope(classCostSample) {
			o, err := m.eraseCostLedgerActor(ctx, tenant, refs)
			if err != nil {
				if errors.Is(err, errErasureHeld) {
					o.Status, o.Detail = "failed", "blocked by a legal hold set mid-run"
					outcomes = append(outcomes, o)
				}
				return outcomes, err
			}
			outcomes = append(outcomes, o)
		}
	case erasureSubjectDocument:
		if inScope(classKnowledgeContent) {
			for _, ref := range refs {
				o, err := m.eraseDocumentCascade(ctx, tenant, ref)
				if err != nil {
					if errors.Is(err, errErasureHeld) {
						o.Status, o.Detail = "failed", "blocked by a legal hold set mid-run"
						outcomes = append(outcomes, o)
					}
					return outcomes, err
				}
				outcomes = append(outcomes, o)
			}
		}
	case erasureSubjectIdentity:
		for _, ref := range refs {
			o, err := m.eraseRosterIdentity(ctx, tenant, ref)
			if err != nil {
				if errors.Is(err, errErasureHeld) {
					o.Status, o.Detail = "failed", "blocked by a legal hold set mid-run"
					outcomes = append(outcomes, o)
				}
				return outcomes, err
			}
			outcomes = append(outcomes, o)
		}
	}
	return outcomes, nil
}

// residualScan re-runs every applicable match AFTER the erasure and reports any
// surviving identifier occurrence — the workflow's "verify" step. It is read-only,
// SCOPED to the same data classes the execution was (out-of-scope rows are
// deliberate retention, not residues), and honest: a non-empty result fails the
// receipt's verification.
func (m *Module) residualScan(ctx context.Context, tenant model.TenantID, key subjectKey, classes []string) ([]string, error) {
	var residues []string
	err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		var verr error
		residues, _, verr = residualScanIn(ctx, sc, key.Kind, key.identifiers(), classes)
		return verr
	})
	if err != nil {
		return nil, err
	}
	return residues, nil
}

// residualScanIn is residualScan bound to an EXISTING scope, so the shred
// transaction can re-scan post-shred through the enterprise coordinator's
// evidence probe (CryptoShredProbes.ResidualScan) without opening a nested view.
// It also counts the targets actually examined — the coordinator reports that
// number instead of inventing one.
func residualScanIn(ctx context.Context, sc store.Scope, subjectKind string, refs []string, classes []string) ([]string, int, error) {
	inScope := func(class string) bool {
		if class == "" {
			return true
		}
		for _, c := range classes {
			if c == class {
				return true
			}
		}
		return false
	}
	var residues []string
	scanned := 0
	for _, t := range erasureTargetRegistry {
		cols, ok := t.SubjectColumns[subjectKind]
		if !ok || !inScope(t.DataClass) {
			continue
		}
		repo, err := sc.Ext(t.Kind)
		if err != nil {
			if errors.Is(err, store.ErrUnknownEntity) {
				continue
			}
			return nil, scanned, err
		}
		scanned++
		for _, col := range cols {
			for _, ref := range refs {
				recs, _, err := repo.List(ctx, model.Query{
					Filters: []model.Filter{eq(col, ref)}, Limit: 1,
				})
				if err != nil {
					return nil, scanned, err
				}
				if len(recs) > 0 {
					residues = append(residues, t.Label+"."+col)
				}
			}
		}
	}
	if subjectKind == erasureSubjectDocument && inScope(classKnowledgeContent) {
		docs, err := sc.Ext(model.Kind("knowledge.document"))
		if err == nil {
			scanned++
			for _, ref := range refs {
				if _, gerr := docs.Get(ctx, model.ID(ref)); gerr == nil {
					residues = append(residues, "knowledge.document")
				} else if !errors.Is(gerr, store.ErrNotFound) {
					return nil, scanned, gerr
				}
			}
		} else if !errors.Is(err, store.ErrUnknownEntity) {
			return nil, scanned, err
		}
	}
	if subjectKind == erasureSubjectIdentity {
		// The scrub renames the external_id anchor itself, so ANY row still
		// matching the original ref is a residue.
		scanned++
		for _, ref := range refs {
			ids, _, err := sc.Identities().List(ctx, model.Query{
				Filters: []model.Filter{eq("external_id", ref)}, Limit: 1,
			})
			if err != nil {
				return nil, scanned, err
			}
			if len(ids) > 0 {
				residues = append(residues, "core.identities.external_id")
			}
		}
	}
	if subjectKind == erasureSubjectUser && inScope(classCostSample) {
		// The canonical cost ledger has no filterable actor column: page it
		// within the same bound the erasure pass used. An unfinished scan is
		// reported as such — honest non-verification, never a silent pass.
		scanned++
		iterations, cursor := 0, ""
		for {
			if iterations >= maxEraseIterations {
				residues = append(residues, "core.cost_records (scan truncated; re-execute)")
				break
			}
			iterations++
			recs, page, err := sc.Costs().List(ctx, model.Query{Limit: maxEraseBatch, Cursor: cursor})
			if err != nil {
				return nil, scanned, err
			}
			hit := false
			for _, cr := range recs {
				actor, _ := cr.Metadata["actor"].(string)
				for _, ref := range refs {
					if actor != "" && actor == ref {
						hit = true
					}
				}
			}
			if hit {
				residues = append(residues, "core.cost_records.metadata.actor")
				break
			}
			cursor = page.Cursor
			if !page.HasMore || page.Cursor == "" {
				break
			}
		}
	}
	return dedupeStrings(residues), scanned, nil
}

// dedupeStrings returns the distinct entries of a list, order-preserving.
func dedupeStrings(in []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, dup := seen[s]; dup {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}
