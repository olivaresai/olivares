// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package deploy

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Allowed deployment subjects.
const (
	subjectAgent     = "agent"
	subjectMCPServer = "mcp_server"
)

// Definition lifecycle statuses (desired_status).
const (
	desiredActive  = "active"
	desiredRetired = "retired"
)

// definitionDTO is the desired-vs-real view of a deployment definition. The
// desired side is the current declared revision; the real side is reflected by
// applied_version and the linked core Deployment snapshot (filled by apply/verify).
type definitionDTO struct {
	ID             string        `json:"id"`
	SubjectKind    string        `json:"subject_kind"`
	SubjectRef     string        `json:"subject_ref"`
	Name           string        `json:"name"`
	Environment    string        `json:"environment"`
	Target         string        `json:"target"`
	Runtime        string        `json:"runtime"`
	DesiredStatus  string        `json:"desired_status"`
	CurrentVersion int64         `json:"current_version"`
	AppliedVersion int64         `json:"applied_version"`
	SpecHash       string        `json:"spec_hash,omitempty"`
	SourceRef      string        `json:"source_ref,omitempty"`
	UpToDate       bool          `json:"up_to_date"`     // applied_version == current_version
	Real           *realStateDTO `json:"real,omitempty"` // the core Deployment snapshot, when applied
	Spec           *deploySpec   `json:"spec,omitempty"` // current desired spec (single-get only)
}

// realStateDTO is the applied state read back from the core Deployment entity.
type realStateDTO struct {
	Status     string `json:"status"`
	Version    string `json:"version,omitempty"`
	DeployedAt string `json:"deployed_at,omitempty"`
}

func toDefinitionDTO(rec model.Record) definitionDTO {
	return definitionDTO{
		ID: rec.String(model.ColID), SubjectKind: rec.String(colSubjectKind), SubjectRef: rec.String(colSubjectRef),
		Name: rec.String(colDefName), Environment: rec.String(colEnvironment), Target: rec.String(colTarget),
		Runtime: rec.String(colRuntime), DesiredStatus: rec.String(colDesiredStatus),
		CurrentVersion: rec.Int(colCurrentVer), AppliedVersion: rec.Int(colAppliedVer),
		SpecHash: rec.String(colSpecHash), SourceRef: rec.String(colSourceRef),
		UpToDate: rec.Int(colAppliedVer) == rec.Int(colCurrentVer),
	}
}

// createDefinitionRequest declares a new deployment's desired state. Declaring a
// definition is a CONTROL-PLANE action, not an infrastructure mutation: it does
// not touch the customer estate and is therefore write-tier, not HITL-gated. The
// governed mutation is the later apply.
type createDefinitionRequest struct {
	SubjectKind string          `json:"subject_kind"`
	SubjectRef  string          `json:"subject_ref"`
	Name        string          `json:"name"`
	Environment string          `json:"environment"`
	Target      string          `json:"target"`
	Runtime     string          `json:"runtime"`
	SourceRef   string          `json:"source_ref,omitempty"`
	Spec        json.RawMessage `json:"spec"`
}

// handleCreateDefinition declares a new deployment definition and its first
// revision. Write-tier; self-audited. It validates the typed spec (no cleartext
// secrets) before persisting.
func (m *Module) handleCreateDefinition(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var in createDefinitionRequest
	if !decodeJSON(w, r, &in) {
		return
	}
	in.SubjectKind = strings.TrimSpace(strings.ToLower(in.SubjectKind))
	in.SubjectRef = strings.TrimSpace(in.SubjectRef)
	in.Name = strings.TrimSpace(in.Name)
	in.Environment = strings.TrimSpace(in.Environment)
	in.Target = strings.TrimSpace(in.Target)
	in.Runtime = strings.TrimSpace(strings.ToLower(in.Runtime))

	if in.SubjectKind != subjectAgent && in.SubjectKind != subjectMCPServer {
		writeJSON(w, http.StatusBadRequest, errorBody("subject_kind must be one of agent, mcp_server"))
		return
	}
	if in.Name == "" || in.Environment == "" || in.Target == "" || in.SubjectRef == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("subject_ref, name, environment and target are required"))
		return
	}
	if len(in.Name) > maxNameLen || len(in.Environment) > maxNameLen || len(in.Target) > maxRefLen ||
		len(in.SubjectRef) > maxRefLen || len(in.Runtime) > maxNameLen || len(in.SourceRef) > maxRefLen {
		writeJSON(w, http.StatusBadRequest, errorBody("a field exceeds its length bound"))
		return
	}
	if containsInlineCredential(in.SourceRef) || containsInlineCredential(in.Target) {
		writeJSON(w, http.StatusBadRequest, errorBody("target and source_ref must not contain a credential"))
		return
	}
	spec, msg := parseSpec(in.Spec)
	if msg != "" {
		writeJSON(w, http.StatusBadRequest, errorBody(msg))
		return
	}
	canonical, specHash, err := spec.canonical()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody("internal error"))
		return
	}

	var out definitionDTO
	err = mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		defRepo, err := sc.Ext(definitionKind)
		if err != nil {
			return err
		}
		created, err := defRepo.Create(r.Context(), model.Record{
			colSubjectKind: in.SubjectKind, colSubjectRef: in.SubjectRef, colDefName: in.Name,
			colEnvironment: in.Environment, colTarget: in.Target, colRuntime: in.Runtime,
			colDesiredStatus: desiredActive, colCurrentVer: int64(1), colAppliedVer: int64(0),
			colSpecHash: specHash, colSourceRef: in.SourceRef,
		})
		if err != nil {
			return err // ErrConflict on the (name, environment) unique index -> 409
		}
		defID := model.ID(created.String(model.ColID))
		if err := writeRevision(r.Context(), sc, defID, 1, canonical, specHash, in.SourceRef, "initial revision", mc.Principal.Actor()); err != nil {
			return err
		}
		out = toDefinitionDTO(created)
		out.Spec = &spec
		return auditEvent(r.Context(), sc, mc, "deploy.definition.create", definitionKind, defID, map[string]any{
			"name": in.Name, "environment": in.Environment, "subject_kind": in.SubjectKind, "version": 1,
		})
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

// updateDefinitionRequest replaces the desired spec (and optionally target/
// source_ref), producing a NEW immutable revision. It does not apply.
type updateDefinitionRequest struct {
	Target    *string         `json:"target,omitempty"`
	SourceRef *string         `json:"source_ref,omitempty"`
	Note      string          `json:"note,omitempty"`
	Spec      json.RawMessage `json:"spec"`
}

// handleUpdateDefinition declares a new desired revision. Write-tier; self-audited.
func (m *Module) handleUpdateDefinition(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var in updateDefinitionRequest
	if !decodeJSON(w, r, &in) {
		return
	}
	if len(in.Note) > maxNoteLen {
		writeJSON(w, http.StatusBadRequest, errorBody("note too long"))
		return
	}
	spec, msg := parseSpec(in.Spec)
	if msg != "" {
		writeJSON(w, http.StatusBadRequest, errorBody(msg))
		return
	}
	canonical, specHash, err := spec.canonical()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody("internal error"))
		return
	}

	var (
		out      definitionDTO
		notFound bool
		badReq   string
	)
	err = mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		defRepo, err := sc.Ext(definitionKind)
		if err != nil {
			return err
		}
		rec, err := defRepo.Get(r.Context(), id)
		if err != nil {
			if isNotFound(err) {
				notFound = true
				return nil
			}
			return err
		}
		sourceRef := rec.String(colSourceRef)
		if in.SourceRef != nil {
			sourceRef = strings.TrimSpace(*in.SourceRef)
			if len(sourceRef) > maxRefLen || containsInlineCredential(sourceRef) {
				badReq = "source_ref invalid"
				return nil
			}
		}
		if in.Target != nil {
			t := strings.TrimSpace(*in.Target)
			if t == "" || len(t) > maxRefLen || containsInlineCredential(t) {
				badReq = "target invalid"
				return nil
			}
			rec[colTarget] = t
		}
		next := rec.Int(colCurrentVer) + 1
		if err := writeRevision(r.Context(), sc, id, next, canonical, specHash, sourceRef, in.Note, mc.Principal.Actor()); err != nil {
			return err
		}
		rec[colCurrentVer] = next
		rec[colSpecHash] = specHash
		rec[colSourceRef] = sourceRef
		rec, err = defRepo.Update(r.Context(), rec)
		if err != nil {
			return err // ErrConflict -> 409 (concurrent update)
		}
		out = toDefinitionDTO(rec)
		out.Spec = &spec
		return auditEvent(r.Context(), sc, mc, "deploy.definition.update", definitionKind, id, map[string]any{"version": next})
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if notFound {
		writeJSON(w, http.StatusNotFound, errorBody("not found"))
		return
	}
	if badReq != "" {
		writeJSON(w, http.StatusBadRequest, errorBody(badReq))
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleListDefinitions lists definitions, optionally filtered by environment or
// desired_status. Read-tier.
func (m *Module) handleListDefinitions(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(r)
	if v := strings.TrimSpace(r.URL.Query().Get("environment")); v != "" {
		q.Filters = append(q.Filters, eq(colEnvironment, v))
	}
	if v := strings.TrimSpace(r.URL.Query().Get("status")); v != "" {
		q.Filters = append(q.Filters, eq(colDesiredStatus, v))
	}
	out := listResponse[definitionDTO]{Items: []definitionDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(definitionKind)
		if err != nil {
			return err
		}
		recs, page, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			out.Items = append(out.Items, toDefinitionDTO(rec))
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

// handleGetDefinition returns one definition with its current desired spec and,
// when applied, the real-state snapshot from the core Deployment entity. Read-tier.
func (m *Module) handleGetDefinition(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var (
		out   definitionDTO
		found bool
	)
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		defRepo, err := sc.Ext(definitionKind)
		if err != nil {
			return err
		}
		rec, err := defRepo.Get(r.Context(), id)
		if err != nil {
			if isNotFound(err) {
				return nil
			}
			return err
		}
		found = true
		out = toDefinitionDTO(rec)
		if spec, _, ok, err := currentSpec(r.Context(), sc, rec); err != nil {
			return err
		} else if ok {
			out.Spec = &spec
		}
		if depID := model.ID(rec.String(colDeploymentID)); !depID.IsZero() {
			if dep, err := sc.Deployments().Get(r.Context(), depID); err == nil {
				out.Real = &realStateDTO{Status: dep.Status, Version: dep.Version, DeployedAt: dep.DeployedAt.String()}
			} else if !isNotFound(err) {
				return err
			}
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

// handleDeleteDefinition removes a definition's desired-state record. It is the
// CONTROL-PLANE delete (what `terraform destroy` of an olivares_deployment maps
// to); it does NOT touch infrastructure — tearing the deployment down on the
// estate is the governed `retire` action. To avoid orphaning a live deployment,
// it refuses (409) while the definition is still applied: retire it first. The
// append-only revision and operation history is retained as change evidence
//; only the mutable definition + its wiring declarations are removed.
func (m *Module) handleDeleteDefinition(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var (
		notFound  bool
		stillLive bool
	)
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		defRepo, err := sc.Ext(definitionKind)
		if err != nil {
			return err
		}
		rec, err := defRepo.Get(r.Context(), id)
		if err != nil {
			if isNotFound(err) {
				notFound = true
				return nil
			}
			return err
		}
		if rec.String(colDesiredStatus) == desiredActive && rec.Int(colAppliedVer) != 0 {
			stillLive = true
			return nil
		}
		// Drop the wiring declarations (not append-only); keep the revision and
		// operation ledgers as immutable change evidence.
		wRepo, err := sc.Ext(wiringKind)
		if err != nil {
			return err
		}
		wirings, err := listAll(r.Context(), wRepo, eq(colDefinitionRef, id.String()))
		if err != nil {
			return err
		}
		for _, wr := range wirings {
			if err := wRepo.Delete(r.Context(), model.ID(wr.String(model.ColID))); err != nil {
				return err
			}
		}
		// Re-check live status immediately before the delete: under a read-committed
		// engine a concurrent apply could have advanced applied_version after the
		// first check, and we must never orphan a live deployment's record. (SQLite's
		// single writer already serializes this; the re-read is the Postgres backstop.)
		fresh, err := defRepo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		if fresh.String(colDesiredStatus) == desiredActive && fresh.Int(colAppliedVer) != 0 {
			return store.ErrConflict // a concurrent apply went live: roll back the wiring deletes
		}
		if err := defRepo.Delete(r.Context(), id); err != nil {
			return err
		}
		return auditEvent(r.Context(), sc, mc, "deploy.definition.delete", definitionKind, id, nil)
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if notFound {
		writeJSON(w, http.StatusNotFound, errorBody("not found"))
		return
	}
	if stillLive {
		writeJSON(w, http.StatusConflict, errorBody("deployment is still applied; retire it before deleting its definition"))
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}

// revisionDTO is one immutable version of a definition's desired spec.
type revisionDTO struct {
	Version   int64       `json:"version"`
	SpecHash  string      `json:"spec_hash"`
	SourceRef string      `json:"source_ref,omitempty"`
	Note      string      `json:"note,omitempty"`
	CreatedBy string      `json:"created_by,omitempty"`
	CreatedAt string      `json:"created_at,omitempty"`
	Spec      *deploySpec `json:"spec,omitempty"`
}

// handleListRevisions returns the append-only revision history of a definition —
// the rollback targets. Read-tier.
func (m *Module) handleListRevisions(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	out := listResponse[revisionDTO]{Items: []revisionDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(revisionKind)
		if err != nil {
			return err
		}
		recs, err := listAll(r.Context(), repo, eq(colDefinitionRef, id.String()))
		if err != nil {
			return err
		}
		for _, rec := range recs {
			out.Items = append(out.Items, toRevisionDTO(rec, false))
		}
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func toRevisionDTO(rec model.Record, withSpec bool) revisionDTO {
	dto := revisionDTO{
		Version: rec.Int(colRevNum), SpecHash: rec.String(colSpecHash), SourceRef: rec.String(colSourceRef),
		Note: rec.String(colNote), CreatedBy: rec.String(colCreatedByCol), CreatedAt: rec.String(model.ColCreatedAt),
	}
	if withSpec {
		if s, ok := decodeSpec(rec.String(colSpec)); ok {
			dto.Spec = &s
		}
	}
	return dto
}

// rollbackRequest selects a prior known-good revision to roll back to.
type rollbackRequest struct {
	ToVersion int64  `json:"to_version"`
	Note      string `json:"note,omitempty"`
}

// handleRollback rolls the DESIRED state back to a prior revision by declaring a
// NEW revision whose spec equals that known-good version (forward-only, reversible
// history). It is write-tier and NOT gated, because — like any desired-state
// declaration — it does not touch infrastructure: the subsequent apply is the
// governed mutation that actually restores the prior state. Self-audited.
func (m *Module) handleRollback(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var in rollbackRequest
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.ToVersion < 1 || len(in.Note) > maxNoteLen {
		writeJSON(w, http.StatusBadRequest, errorBody("to_version must be >= 1 and note must be bounded"))
		return
	}
	var (
		out        definitionDTO
		notFound   bool
		targetMiss bool
	)
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		defRepo, err := sc.Ext(definitionKind)
		if err != nil {
			return err
		}
		rec, err := defRepo.Get(r.Context(), id)
		if err != nil {
			if isNotFound(err) {
				notFound = true
				return nil
			}
			return err
		}
		target, ok, err := getRevision(r.Context(), sc, id, in.ToVersion)
		if err != nil {
			return err
		}
		if !ok {
			targetMiss = true
			return nil
		}
		canonical, specHash := target.String(colSpec), target.String(colSpecHash)
		sourceRef := target.String(colSourceRef)
		next := rec.Int(colCurrentVer) + 1
		note := in.Note
		if note == "" {
			note = "rollback to v" + itoa(in.ToVersion)
		}
		if err := writeRevision(r.Context(), sc, id, next, canonical, specHash, sourceRef, note, mc.Principal.Actor()); err != nil {
			return err
		}
		rec[colCurrentVer] = next
		rec[colSpecHash] = specHash
		rec[colSourceRef] = sourceRef
		rec[colDesiredStatus] = desiredActive // rolling back implies the deployment is desired-active again
		rec, err = defRepo.Update(r.Context(), rec)
		if err != nil {
			return err
		}
		out = toDefinitionDTO(rec)
		return auditEvent(r.Context(), sc, mc, "deploy.definition.rollback", definitionKind, id, map[string]any{
			"to_version": in.ToVersion, "new_version": next,
		})
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if notFound {
		writeJSON(w, http.StatusNotFound, errorBody("not found"))
		return
	}
	if targetMiss {
		writeJSON(w, http.StatusConflict, errorBody("target revision not found"))
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// ---- internal definition/revision helpers -------------------------------------

// writeRevision inserts one immutable revision row (append-only).
func writeRevision(ctx context.Context, sc store.Scope, defID model.ID, version int64, canonicalSpec, specHash, sourceRef, note, actor string) error {
	repo, err := sc.Ext(revisionKind)
	if err != nil {
		return err
	}
	_, err = repo.Create(ctx, model.Record{
		colDefinitionRef: defID.String(), colRevNum: version, colSpec: canonicalSpec, colSpecHash: specHash,
		colSourceRef: sourceRef, colNote: note, colCreatedByCol: actor,
	})
	return err
}

// getRevision loads a specific (definition, version) revision row.
func getRevision(ctx context.Context, sc store.Scope, defID model.ID, version int64) (model.Record, bool, error) {
	repo, err := sc.Ext(revisionKind)
	if err != nil {
		return nil, false, err
	}
	return findOne(ctx, repo, eq(colDefinitionRef, defID.String()), eq(colRevNum, version))
}

// currentSpec loads the desired spec of a definition's current revision.
func currentSpec(ctx context.Context, sc store.Scope, defRec model.Record) (deploySpec, string, bool, error) {
	rec, ok, err := getRevision(ctx, sc, model.ID(defRec.String(model.ColID)), defRec.Int(colCurrentVer))
	if err != nil || !ok {
		return deploySpec{}, "", false, err
	}
	spec, ok := decodeSpec(rec.String(colSpec))
	if !ok {
		return deploySpec{}, "", false, nil
	}
	return spec, rec.String(colSpecHash), true, nil
}

// decodeSpec parses a stored canonical spec back into the typed struct. A stored
// spec was re-serialized from the struct on write, so a decode failure is a
// corruption signal, surfaced as ok=false (never a panic).
func decodeSpec(s string) (deploySpec, bool) {
	var spec deploySpec
	if s == "" {
		return spec, false
	}
	if err := json.Unmarshal([]byte(s), &spec); err != nil {
		return deploySpec{}, false
	}
	return spec, true
}
