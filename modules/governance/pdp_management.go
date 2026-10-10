// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"errors"
	"net/http"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/store"
)

// handlePdpPublish publishes and selects an authored policy revision. Cedar
// editing requires Business; Community answers 501. OPA remains externally enforced.
func (m *Module) handlePdpPublish(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var in pdpEngineSourceBody
	if !decodeJSON(w, r, &in) {
		return
	}
	engine, ok := normEngine(in.Engine)
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("engine must be cedar or opa"))
		return
	}
	if engine == surfaceCedar {
		m.handleCedarPublish(w, r, mc, in)
		return
	}
	if len(in.Source) > maxPolicyContentBytes {
		writeJSON(w, http.StatusBadRequest, errorBody("policy source too large"))
		return
	}
	if containsInlineKey(in.Source) {
		writeJSON(w, http.StatusBadRequest, errorBody("policy source must not contain an inline credential (sk-ant-…)"))
		return
	}
	if len(in.Note) > maxNoteLen {
		writeJSON(w, http.StatusBadRequest, errorBody("note too long"))
		return
	}
	if hasError(validateRego(in.Source)) {
		writeJSON(w, http.StatusBadRequest, errorBody("rego source failed the structural pre-check; not versioned"))
		return
	}
	var revision int64
	for attempt := 0; attempt < maxDecisionRetries; attempt++ {
		var candidate int64
		err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
			num, id, err := appendRevision(r.Context(), sc, engine, in.Source, mc.Principal.Actor(), true, true, in.Note)
			if err != nil {
				return err
			}
			if _, err = activateRevision(r.Context(), sc, engine, num, mc.Principal.Actor()); err != nil {
				return err
			}
			candidate = num
			return auditEvent(r.Context(), sc, mc, "governance.pdp.publish", revisionKind, id, map[string]any{"engine": engine, "revision": num, "active": true, "enforced_here": false})
		})
		if err == nil {
			revision = candidate
			break
		}
		if isConflict(err) {
			continue
		}
		writeStoreError(w, err)
		return
	}
	if revision == 0 {
		writeJSON(w, http.StatusConflict, errorBody("publish conflicted repeatedly; please retry"))
		return
	}
	writeJSON(w, http.StatusOK, pdpPublishResult{Engine: engine, Revision: revision, Active: true, LiveActivation: liveNotApplicable, Note: "versioned and selected as the current Rego revision in the authored history; OPA enforcement is the sidecar's — this revision is not pushed to OPA from here"})
}

// handlePdpRollback selects a prior immutable policy revision. Cedar editing
// requires Business; Community answers 501. OPA remains externally enforced.
func (m *Module) handlePdpRollback(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var in pdpRollbackBody
	if !decodeJSON(w, r, &in) {
		return
	}
	engine, ok := normEngine(in.Engine)
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("engine must be cedar or opa"))
		return
	}
	if engine == surfaceCedar {
		m.handleCedarRollback(w, r, mc, in)
		return
	}
	if in.Revision <= 0 {
		writeJSON(w, http.StatusBadRequest, errorBody("revision must be a positive integer"))
		return
	}
	var from int64
	var noop bool
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		target, found, err := getRevision(r.Context(), sc, engine, in.Revision)
		if err != nil {
			return err
		}
		if !found {
			return errPdpRevisionNotFound
		}
		if len(target.Content) > maxPolicyContentBytes {
			return validationError("policy source too large; activation refused")
		}
		if containsInlineKey(target.Content) {
			return validationError("policy source contains an inline credential; activation refused")
		}
		var has bool
		from, has, err = activeRevisionNumber(r.Context(), sc, engine)
		if err != nil {
			return err
		}
		if !has {
			from = 0
		}
		if has && from == in.Revision {
			noop = true
			return nil
		}
		if hasError(validateRego(target.Content)) {
			return validationError("rego revision failed the structural pre-check; activation refused")
		}
		id, err := activateRevision(r.Context(), sc, engine, in.Revision, mc.Principal.Actor())
		if err != nil {
			return err
		}
		return auditEvent(r.Context(), sc, mc, "governance.pdp.rollback", revisionKind, id, map[string]any{"actor": mc.Principal.Actor(), "engine": engine, "from_revision": from, "to_revision": in.Revision})
	})
	if errors.Is(err, errPdpRevisionNotFound) {
		writeJSON(w, http.StatusNotFound, errorBody("policy revision not found; activation refused"))
		return
	}
	if msg, ok := asValidation(err); ok {
		writeJSON(w, http.StatusBadRequest, errorBody(msg))
		return
	}
	if err != nil {
		writeStoreError(w, err)
		return
	}
	note := "revision selected in the authored history; OPA enforcement remains the external sidecar's and is not pushed from here"
	if noop {
		note = "requested revision was already selected; rollback performed zero durable writes and OPA remains externally enforced"
	}
	writeJSON(w, http.StatusOK, pdpRollbackResult{Engine: engine, FromRevision: from, ToRevision: in.Revision, Active: true, LiveActivation: liveNotApplicable, Note: note})
}
