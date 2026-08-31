// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package knowledge

import (
	"context"
	"net/http"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The DLP egress policy: per sensitivity CLASS, may content bearing that
// class leave the perimeter? "Egress" here is the two surfaces the knowledge
// plane exposes: the retrieval response (redacted chunk text handed to the
// caller) and the ingest-time embed call when the wired embedder transmits
// content out (model_backed). The policy is enforced deny-closed, mirroring the
// always-on residency gates — NOT the security module's opt-in detective
// enforcement:
//
//   - NO rules configured  => DLP is not configured; the gate is inert (the
//     residency precedent: an unpinned tenant is unrestricted — a tenant that
//     wants enforcement writes rules).
//   - ≥1 rule configured   => DLP is ENABLED. A labeled class with no exact rule
//     falls to the "*" rule; with no "*" rule it DENIES. Content with NO label
//     row (never scanned) is unprovable and DENIES unless an explicit
//     "unscanned" rule allows it. Unknown/garbage actions deny.
//
// Reserved classes: "*" (any labeled class without an exact rule) and
// "unscanned" (content without a label). Every enforcement action appends an
// append-only knowledge_dlp_event row (the evidence) — classes and counts
// only, never content.

// DLP rule actions and reserved classes.
const (
	dlpAllow          = "allow"
	dlpDeny           = "deny"
	dlpClassAny       = "*"
	dlpClassUnscanned = "unscanned"
)

// DLP event actions (the colDLPAction column).
const (
	dlpActionFiltered     = "filtered"      // chunks withheld from a retrieval
	dlpActionDeniedIngest = "denied_ingest" // an ingest refused before embed egress
)

// dlpPolicy is one tenant's loaded DLP rule set (class → action).
type dlpPolicy struct {
	rules map[string]string
}

// enabled reports whether the tenant configured ANY rule (the gate is inert
// otherwise — see the file comment for the posture rationale).
func (p dlpPolicy) enabled() bool { return len(p.rules) > 0 }

// decide returns the classes among the given ones whose egress the policy
// DENIES. Deny-closed: a class with neither an exact rule nor a "*" rule denies,
// and an unrecognized action denies. Clean content (no classes) is allowed.
func (p dlpPolicy) decide(classes []string) (denied []string) {
	if !p.enabled() {
		return nil
	}
	for _, c := range classes {
		action, ok := p.rules[c]
		if !ok {
			action = p.rules[dlpClassAny]
		}
		if action != dlpAllow {
			denied = append(denied, c)
		}
	}
	sort.Strings(denied)
	return denied
}

// unscannedDenied reports whether content WITHOUT a sensitivity label may
// egress. Deny-closed: only an explicit {"class":"unscanned","action":"allow"}
// rule permits it — "*" deliberately does not cover unscanned content (it
// matches labeled classes; unprovable sensitivity needs its own opt-out).
func (p dlpPolicy) unscannedDenied() bool {
	if !p.enabled() {
		return false
	}
	return p.rules[dlpClassUnscanned] != dlpAllow
}

// loadDLPPolicy reads the tenant's DLP rules inside an open scope. An error from
// the store fails the caller's transaction (and with it the request) — the gate
// never degrades to allow.
func loadDLPPolicy(ctx context.Context, sc store.Scope) (dlpPolicy, error) {
	repo, err := sc.Ext(dlpRuleKind)
	if err != nil {
		return dlpPolicy{}, err
	}
	recs, err := listAll(ctx, repo)
	if err != nil {
		return dlpPolicy{}, err
	}
	p := dlpPolicy{rules: make(map[string]string, len(recs))}
	for _, rec := range recs {
		p.rules[rec.String(colClass)] = rec.String(colAction)
	}
	return p, nil
}

// writeDLPEvent appends one append-only DLP enforcement event inside the
// caller's transaction (the evidence: action + classes + counts, never
// content).
func (m *Module) writeDLPEvent(ctx context.Context, sc store.Scope, kbRef, action string, classes []string, chunks int64, agentRef, lineageRef, reason string) error {
	repo, err := sc.Ext(dlpEventKind)
	if err != nil {
		return err
	}
	_, err = repo.Create(ctx, model.Record{
		colKBRef: kbRef, colDLPAction: action, colDLPClasses: marshalStrings(classes),
		colChunksHeld: chunks, colAgentRef: agentRef, colLineageRef: lineageRef,
		colReason: reason, colOccurredAt: m.clock.Now().String(),
	})
	return err
}

// dlpDeniedError is returned by the ingest pipeline when the DLP policy forbids
// embedding (egressing) content bearing a denied class; the handler maps it to a
// 409 — the content never left the perimeter.
type dlpDeniedError struct {
	classes []string
	reason  string
}

func (e *dlpDeniedError) Error() string { return e.reason }

// ---------------------------------------------------------------------------
// DLP rule management (admin-tier writes; reads are viewer-tier).
// ---------------------------------------------------------------------------

// dlpRuleRequest declares one DLP rule: a sensitivity class (or "*"/"unscanned")
// and the egress action for content bearing it.
type dlpRuleRequest struct {
	Class  string `json:"class"`
	Action string `json:"action"`
	Note   string `json:"note,omitempty"`
}

type dlpRuleDTO struct {
	ID        string `json:"id"`
	Class     string `json:"class"`
	Action    string `json:"action"`
	Note      string `json:"note,omitempty"`
	CreatedBy string `json:"created_by"`
}

func toDLPRuleDTO(rec model.Record) dlpRuleDTO {
	return dlpRuleDTO{
		ID: rec.String(model.ColID), Class: rec.String(colClass), Action: rec.String(colAction),
		Note: rec.String(colNote), CreatedBy: rec.String(colCreatedBy),
	}
}

// handleListDLPRules lists the tenant's DLP rules.
func (m *Module) handleListDLPRules(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	out := listResponse[dlpRuleDTO]{Items: []dlpRuleDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(dlpRuleKind)
		if err != nil {
			return err
		}
		recs, page, err := repo.List(r.Context(), listQuery(r))
		if err != nil {
			return err
		}
		for _, rec := range recs {
			out.Items = append(out.Items, toDLPRuleDTO(rec))
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

// handlePutDLPRule upserts one DLP rule by class (admin-tier: writing egress
// policy is privileged governance; self-audited).
func (m *Module) handlePutDLPRule(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var req dlpRuleRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	class := strings.ToLower(strings.TrimSpace(req.Class))
	action := strings.ToLower(strings.TrimSpace(req.Action))
	if class == "" || len(class) > maxNameLen {
		writeJSON(w, http.StatusBadRequest, errorBody("class is required and must be short"))
		return
	}
	if action != dlpAllow && action != dlpDeny {
		writeJSON(w, http.StatusBadRequest, errorBody("action must be allow or deny"))
		return
	}
	if len(req.Note) > maxNameLen {
		writeJSON(w, http.StatusBadRequest, errorBody("note too long"))
		return
	}

	var out dlpRuleDTO
	created := false
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(dlpRuleKind)
		if err != nil {
			return err
		}
		fields := model.Record{
			colClass: class, colAction: action, colNote: req.Note, colCreatedBy: mc.Principal.Actor(),
		}
		existing, ok, err := findOne(r.Context(), repo, eq(colClass, class))
		if err != nil {
			return err
		}
		if ok {
			for k, v := range fields {
				existing[k] = v
			}
			updated, err := repo.Update(r.Context(), existing)
			if err != nil {
				return err
			}
			out = toDLPRuleDTO(updated)
		} else {
			rec, err := repo.Create(r.Context(), fields)
			if err != nil {
				return err
			}
			created = true
			out = toDLPRuleDTO(rec)
		}
		return auditEvent(r.Context(), sc, mc, "knowledge.dlp.put", dlpRuleKind, model.ID(out.ID),
			map[string]any{"class": class, "action": action})
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, out)
}

// handleDeleteDLPRule removes one DLP rule by id (admin-tier; self-audited).
// Removing the last rule disables the gate entirely — the audit row is the
// record of WHO opened the perimeter.
func (m *Module) handleDeleteDLPRule(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	found := false
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(dlpRuleKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			if isNotFound(err) {
				return nil
			}
			return err
		}
		if err := repo.Delete(r.Context(), id); err != nil {
			return err
		}
		found = true
		return auditEvent(r.Context(), sc, mc, "knowledge.dlp.delete", dlpRuleKind, id,
			map[string]any{"class": rec.String(colClass)})
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, errorBody("not found"))
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}
