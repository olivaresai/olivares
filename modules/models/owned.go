// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package models

// Module XXIII — own-model / fine-tuning governance (README.mdbis): inventories
// and governs owned/fine-tuned models. By design it never trains models or holds weights.
// The control plane GOVERNS and INVENTORIES an organization's own models — it is
// not a training platform. So this models four governable entities and tracks
// their state/lineage; it never executes a fine-tune or holds weights:
//
//   - owned_model:          the registry entry for a hosted/fine-tuned/imported model
//   - model_version:        a version of an owned model, with lineage (parent/source)
//   - inference_deployment: a local inference endpoint (vLLM/Ollama/llama.cpp) that
//                           serves an owned model — a governable inventory entity
//   - finetune_job:         a RECORD of a fine-tuning job's state + the version it
//                           produced (lineage), tracked, not run, by the control plane
//
// Every reference field is MINIMAL-DATA by construction (docs/SECURITY-HARDENING.md): artifact_ref
// and dataset_ref are URIs/registry paths, never the weights or the dataset.

import (
	"context"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Entity kinds for module XXIII.
const (
	ownedModelKind          model.Kind = "models.owned_model"
	modelVersionKind        model.Kind = "models.model_version"
	inferenceDeploymentKind model.Kind = "models.inference_deployment"
	finetuneJobKind         model.Kind = "models.finetune_job"
)

// Physical tables for module XXIII's entities.
const (
	ownedModelTable          = "models_owned_model"
	modelVersionTable        = "models_model_version"
	inferenceDeploymentTable = "models_inference_deployment"
	finetuneJobTable         = "models_finetune_job"
)

// owned_model columns.
const (
	colOwnedName       = "name"
	colOwnedKind       = "model_kind"
	colOwnedBase       = "base_ref"
	colOwnedProvider   = "provider_ref"
	colOwnedVisibility = "visibility"
	colOwnedStatus     = "om_status"
	colOwnedOwner      = "owner_ref"
	colOwnedNote       = "note"
)

// model_version columns.
const (
	colVerOwned    = "owned_ref"
	colVerVersion  = "ver" // DB column ("version" is a reserved base column); JSON stays "version"
	colVerArtifact = "artifact_ref"
	colVerStatus   = "mv_status"
	colVerParent   = "parent_ref"
	colVerSource   = "source_ref"
	colVerNote     = "note"
)

// inference_deployment columns.
const (
	colDepName     = "name"
	colDepRuntime  = "runtime"
	colDepEndpoint = "endpoint_ref"
	colDepOwned    = "owned_ref"
	colDepVersion  = "version_ref"
	colDepStatus   = "dep_status"
	colDepGoverned = "governed"
	colDepNote     = "note"
	// colDepType (D-08) is the EXPLICIT discriminator that decides whether a
	// deployment is gated by signed-model admission. It exists precisely so the gate
	// never infers "not self-hosted" from the ABSENCE of a version_ref (the D-08
	// bypass): "local" serves a self-hosted owned model version and is admission-gated;
	// "brokered" calls a hosted provider (e.g. Claude), carries no version_ref and is
	// never gated (docs/contracts §"Runtime deny-closed gate"); "unclassified" is
	// the migration state for pre rows whose type cannot be proven — deny-closed at
	// serve time under require_signed until an operator classifies it.
	colDepType = "deployment_type"
)

// deployment_type discriminator values.
const (
	depTypeLocal        = "local"
	depTypeBrokered     = "brokered"
	depTypeUnclassified = "unclassified"
)

// finetune_job columns.
const (
	colJobName    = "name"
	colJobBase    = "base_ref"
	colJobDataset = "dataset_ref"
	colJobRuntime = "runtime"
	colJobStatus  = "job_status"
	colJobResult  = "result_version_ref"
	colJobStarted = "started_at"
	colJobEnded   = "ended_at"
	colJobNote    = "note"
)

// Allowed enum values (validated on write; unknown values are rejected so the
// inventory stays queryable by status/kind/runtime).
var (
	ownedKinds      = set("hosted", "fine_tuned", "imported")
	visibilities    = set("private", "internal")
	lifecycleStates = set("active", "deprecated", "draft")
	versionStates   = set("draft", "active", "deprecated")
	runtimes        = set("vllm", "ollama", "llamacpp", "other")
	depStates       = set("active", "stopped")
	deploymentTypes = set(depTypeLocal, depTypeBrokered, depTypeUnclassified)
	jobStates       = set("queued", "running", "succeeded", "failed", "canceled")
)

func set(vs ...string) map[string]bool {
	m := make(map[string]bool, len(vs))
	for _, v := range vs {
		m[v] = true
	}
	return m
}

// registerOwnedSchemas registers module XXIII's four entities. Each unique index
// leads with tenant_id so it can never couple tenants.
func registerOwnedSchemas(reg store.ExtensionRegistry) error {
	descs := []model.EntityDescriptor{
		{
			Kind: ownedModelKind, Table: ownedModelTable,
			Fields: []model.FieldSpec{
				{Name: colOwnedName, Kind: model.KindText, Indexed: true},
				{Name: colOwnedKind, Kind: model.KindText, Indexed: true},
				{Name: colOwnedBase, Kind: model.KindText, Nullable: true},
				{Name: colOwnedProvider, Kind: model.KindText, Nullable: true},
				{Name: colOwnedVisibility, Kind: model.KindText},
				{Name: colOwnedStatus, Kind: model.KindText, Indexed: true},
				{Name: colOwnedOwner, Kind: model.KindText, Nullable: true},
				{Name: colOwnedNote, Kind: model.KindText, Nullable: true},
			},
			Indexes: []model.IndexSpec{{Name: "models_owned_model_uniq", Columns: []string{model.ColTenantID, colOwnedName}, Unique: true}},
		},
		{
			Kind: modelVersionKind, Table: modelVersionTable,
			Fields: []model.FieldSpec{
				{Name: colVerOwned, Kind: model.KindText, Indexed: true},
				{Name: colVerVersion, Kind: model.KindText},
				{Name: colVerArtifact, Kind: model.KindText, Nullable: true},
				{Name: colVerStatus, Kind: model.KindText, Indexed: true},
				{Name: colVerParent, Kind: model.KindText, Nullable: true},
				{Name: colVerSource, Kind: model.KindText, Nullable: true},
				{Name: colVerNote, Kind: model.KindText, Nullable: true},
			},
			Indexes: []model.IndexSpec{{Name: "models_model_version_uniq", Columns: []string{model.ColTenantID, colVerOwned, colVerVersion}, Unique: true}},
		},
		{
			Kind: inferenceDeploymentKind, Table: inferenceDeploymentTable,
			Fields: []model.FieldSpec{
				{Name: colDepName, Kind: model.KindText, Indexed: true},
				{Name: colDepRuntime, Kind: model.KindText, Indexed: true},
				{Name: colDepEndpoint, Kind: model.KindText, Nullable: true},
				{Name: colDepOwned, Kind: model.KindText, Nullable: true},
				{Name: colDepVersion, Kind: model.KindText, Nullable: true},
				{Name: colDepStatus, Kind: model.KindText, Indexed: true},
				{Name: colDepGoverned, Kind: model.KindBool},
				{Name: colDepNote, Kind: model.KindText, Nullable: true},
				// D-08: the admission-gate discriminator. NULLABLE + Indexed so the
				// additive schema reconcile (sqlstore/schema.go) issues a plain ALTER TABLE
				// ADD COLUMN on an existing tenant DB — no hand-authored migration — and a
				// fresh DB generates it, exactly like models.model_admission.model_ref.
				// Existing rows read back "" and are resolved on read/serve to local (both
				// refs present) or unclassified (deny-closed under require_signed).
				{Name: colDepType, Kind: model.KindText, Nullable: true, Indexed: true},
			},
			Indexes: []model.IndexSpec{{Name: "models_inference_deployment_uniq", Columns: []string{model.ColTenantID, colDepName}, Unique: true}},
		},
		{
			Kind: finetuneJobKind, Table: finetuneJobTable,
			Fields: []model.FieldSpec{
				{Name: colJobName, Kind: model.KindText, Indexed: true},
				{Name: colJobBase, Kind: model.KindText, Nullable: true},
				{Name: colJobDataset, Kind: model.KindText, Nullable: true},
				{Name: colJobRuntime, Kind: model.KindText, Nullable: true},
				{Name: colJobStatus, Kind: model.KindText, Indexed: true},
				{Name: colJobResult, Kind: model.KindText, Nullable: true},
				{Name: colJobStarted, Kind: model.KindTimestamp, Nullable: true},
				{Name: colJobEnded, Kind: model.KindTimestamp, Nullable: true},
				{Name: colJobNote, Kind: model.KindText, Nullable: true},
			},
			Indexes: []model.IndexSpec{{Name: "models_finetune_job_uniq", Columns: []string{model.ColTenantID, colJobName}, Unique: true}},
		},
	}
	for _, d := range descs {
		if err := reg.Register(d); err != nil {
			return err
		}
	}
	return nil
}

// --- DTOs --------------------------------------------------------------------

type ownedModelDTO struct {
	ID          string `json:"id,omitempty"`
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	BaseRef     string `json:"base_ref,omitempty"`
	ProviderRef string `json:"provider_ref,omitempty"`
	Visibility  string `json:"visibility,omitempty"`
	Status      string `json:"status"`
	OwnerRef    string `json:"owner_ref,omitempty"`
	Note        string `json:"note,omitempty"`
}

type modelVersionDTO struct {
	ID          string `json:"id,omitempty"`
	OwnedRef    string `json:"owned_ref"`
	Version     string `json:"version"`
	ArtifactRef string `json:"artifact_ref,omitempty"`
	Status      string `json:"status"`
	ParentRef   string `json:"parent_ref,omitempty"`
	SourceRef   string `json:"source_ref,omitempty"`
	Note        string `json:"note,omitempty"`
}

type inferenceDeploymentDTO struct {
	ID             string `json:"id,omitempty"`
	Name           string `json:"name"`
	Runtime        string `json:"runtime"`
	DeploymentType string `json:"deployment_type,omitempty"`
	EndpointRef    string `json:"endpoint_ref,omitempty"`
	OwnedRef       string `json:"owned_ref,omitempty"`
	VersionRef     string `json:"version_ref,omitempty"`
	Status         string `json:"status"`
	Governed       bool   `json:"governed"`
	Note           string `json:"note,omitempty"`
}

type finetuneJobDTO struct {
	ID              string `json:"id,omitempty"`
	Name            string `json:"name"`
	BaseRef         string `json:"base_ref,omitempty"`
	DatasetRef      string `json:"dataset_ref,omitempty"`
	Runtime         string `json:"runtime,omitempty"`
	Status          string `json:"status"`
	ResultVersionID string `json:"result_version_ref,omitempty"`
	StartedAt       string `json:"started_at,omitempty"`
	EndedAt         string `json:"ended_at,omitempty"`
	Note            string `json:"note,omitempty"`
}

// maxRefLen bounds free-text reference fields (URIs/paths/names) defensively.
const maxRefLen = 512

func trimClamp(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > maxRefLen {
		return s[:maxRefLen]
	}
	return s
}

// --- owned_model handlers ----------------------------------------------------

func (d *ownedModelDTO) validate() string {
	d.Name = trimClamp(d.Name)
	if d.Name == "" {
		return "name is required"
	}
	d.Kind = strings.TrimSpace(d.Kind)
	if !ownedKinds[d.Kind] {
		return "kind must be hosted, fine_tuned or imported"
	}
	if d.Visibility == "" {
		d.Visibility = "private"
	}
	if !visibilities[d.Visibility] {
		return "visibility must be private or internal"
	}
	if d.Status == "" {
		d.Status = "active"
	}
	if !lifecycleStates[d.Status] {
		return "status must be active, deprecated or draft"
	}
	return ""
}

func (d ownedModelDTO) toRecord() model.Record {
	return model.Record{
		colOwnedName: d.Name, colOwnedKind: d.Kind, colOwnedBase: trimClamp(d.BaseRef),
		colOwnedProvider: trimClamp(d.ProviderRef), colOwnedVisibility: d.Visibility,
		colOwnedStatus: d.Status, colOwnedOwner: trimClamp(d.OwnerRef), colOwnedNote: trimClamp(d.Note),
	}
}

func toOwnedModelDTO(rec model.Record) ownedModelDTO {
	return ownedModelDTO{
		ID: rec.String(model.ColID), Name: rec.String(colOwnedName), Kind: rec.String(colOwnedKind),
		BaseRef: rec.String(colOwnedBase), ProviderRef: rec.String(colOwnedProvider),
		Visibility: rec.String(colOwnedVisibility), Status: rec.String(colOwnedStatus),
		OwnerRef: rec.String(colOwnedOwner), Note: rec.String(colOwnedNote),
	}
}

func (m *Module) handleListOwnedModels(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(r)
	if v := r.URL.Query().Get("kind"); v != "" {
		q.Filters = append(q.Filters, eq(colOwnedKind, v))
	}
	if v := r.URL.Query().Get("status"); v != "" {
		q.Filters = append(q.Filters, eq(colOwnedStatus, v))
	}
	out := listResponse[ownedModelDTO]{Items: []ownedModelDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(ownedModelKind)
		if err != nil {
			return err
		}
		recs, page, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			out.Items = append(out.Items, toOwnedModelDTO(rec))
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

func (m *Module) handleCreateOwnedModel(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var in ownedModelDTO
	if !decodeJSON(w, r, &in) {
		return
	}
	if msg := in.validate(); msg != "" {
		writeJSON(w, http.StatusBadRequest, errorBody(msg))
		return
	}
	var out ownedModelDTO
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(ownedModelKind)
		if err != nil {
			return err
		}
		rec, err := repo.Create(r.Context(), in.toRecord())
		if err != nil {
			return err
		}
		out = toOwnedModelDTO(rec)
		return auditOwned(r.Context(), sc, mc, ownedModelKind, "create", model.ID(rec.String(model.ColID)))
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (m *Module) handleGetOwnedModel(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var out ownedModelDTO
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(ownedModelKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		out = toOwnedModelDTO(rec)
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (m *Module) handleUpdateOwnedModel(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var in ownedModelDTO
	if !decodeJSON(w, r, &in) {
		return
	}
	if msg := in.validate(); msg != "" {
		writeJSON(w, http.StatusBadRequest, errorBody(msg))
		return
	}
	var out ownedModelDTO
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(ownedModelKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		for k, v := range in.toRecord() {
			rec[k] = v
		}
		rec, err = repo.Update(r.Context(), rec)
		if err != nil {
			return err
		}
		out = toOwnedModelDTO(rec)
		return auditOwned(r.Context(), sc, mc, ownedModelKind, "update", id)
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (m *Module) handleDeleteOwnedModel(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	// D-08 reverse-ref: refuse to delete a model still referenced by a deployment's
	// owned_ref (a dangling reference would let a later deployment falsify lineage
	// against a recreated id, and it orphans the served model).
	m.deleteExtGuarded(w, r, mc, ownedModelKind, colDepOwned)
}

// --- model_version handlers --------------------------------------------------

func (d *modelVersionDTO) validate() string {
	d.OwnedRef = strings.TrimSpace(d.OwnedRef)
	if d.OwnedRef == "" {
		return "owned_ref is required"
	}
	d.Version = trimClamp(d.Version)
	if d.Version == "" {
		return "version is required"
	}
	if d.Status == "" {
		d.Status = "draft"
	}
	if !versionStates[d.Status] {
		return "status must be draft, active or deprecated"
	}
	return ""
}

func (d modelVersionDTO) toRecord() model.Record {
	return model.Record{
		colVerOwned: d.OwnedRef, colVerVersion: d.Version, colVerArtifact: trimClamp(d.ArtifactRef),
		colVerStatus: d.Status, colVerParent: trimClamp(d.ParentRef), colVerSource: trimClamp(d.SourceRef),
		colVerNote: trimClamp(d.Note),
	}
}

func toModelVersionDTO(rec model.Record) modelVersionDTO {
	return modelVersionDTO{
		ID: rec.String(model.ColID), OwnedRef: rec.String(colVerOwned), Version: rec.String(colVerVersion),
		ArtifactRef: rec.String(colVerArtifact), Status: rec.String(colVerStatus),
		ParentRef: rec.String(colVerParent), SourceRef: rec.String(colVerSource), Note: rec.String(colVerNote),
	}
}

func (m *Module) handleListVersions(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(r)
	if v := r.URL.Query().Get("owned_ref"); v != "" {
		q.Filters = append(q.Filters, eq(colVerOwned, v))
	}
	if v := r.URL.Query().Get("status"); v != "" {
		q.Filters = append(q.Filters, eq(colVerStatus, v))
	}
	out := listResponse[modelVersionDTO]{Items: []modelVersionDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(modelVersionKind)
		if err != nil {
			return err
		}
		recs, page, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			out.Items = append(out.Items, toModelVersionDTO(rec))
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

func (m *Module) handleCreateVersion(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var in modelVersionDTO
	if !decodeJSON(w, r, &in) {
		return
	}
	if msg := in.validate(); msg != "" {
		writeJSON(w, http.StatusBadRequest, errorBody(msg))
		return
	}
	var out modelVersionDTO
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		// The owned model must exist (referential sanity, same tenant).
		om, err := sc.Ext(ownedModelKind)
		if err != nil {
			return err
		}
		if _, err := om.Get(r.Context(), model.ID(in.OwnedRef)); err != nil {
			return err
		}
		repo, err := sc.Ext(modelVersionKind)
		if err != nil {
			return err
		}
		rec, err := repo.Create(r.Context(), in.toRecord())
		if err != nil {
			return err
		}
		out = toModelVersionDTO(rec)
		return auditOwned(r.Context(), sc, mc, modelVersionKind, "create", model.ID(rec.String(model.ColID)))
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (m *Module) handleDeleteVersion(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	// D-08 reverse-ref: refuse to delete a version still referenced by a deployment's
	// version_ref — otherwise a live (possibly admitted) deployment is left pointing at
	// a deleted version, and the admission gate can no longer re-check it.
	m.deleteExtGuarded(w, r, mc, modelVersionKind, colDepVersion)
}

// --- inference_deployment handlers -------------------------------------------

// effectiveDeploymentType resolves the discriminator from a stored/absent value plus
// the refs, so a pre row (stored "") is NEVER silently treated as gate-skipping:
// a stored type wins; otherwise both refs present ⇒ local, else ⇒ unclassified. It is
// deliberately conservative — the absence of a version can only resolve to a gated
// (local) or deny-closed (unclassified) type, never to brokered.
func effectiveDeploymentType(stored, ownedRef, versionRef string) string {
	if stored != "" {
		return stored
	}
	if ownedRef != "" && versionRef != "" {
		return depTypeLocal
	}
	return depTypeUnclassified
}

// validate performs the PURE (store-free) structural checks and RESOLVES the
// discriminator. Membership (version.owned_ref == owned_ref) and the deny-closed
// admission gate need the store and run in the create/update handlers (gateDeployment).
func (d *inferenceDeploymentDTO) validate() string {
	d.Name = trimClamp(d.Name)
	if d.Name == "" {
		return "name is required"
	}
	d.Runtime = strings.TrimSpace(d.Runtime)
	if !runtimes[d.Runtime] {
		return "runtime must be vllm, ollama, llamacpp or other"
	}
	if d.Status == "" {
		d.Status = "active"
	}
	if !depStates[d.Status] {
		return "status must be active or stopped"
	}
	d.OwnedRef = trimClamp(d.OwnedRef)
	d.VersionRef = trimClamp(d.VersionRef)
	d.EndpointRef = trimClamp(d.EndpointRef)

	// D-08: an OMITTED type is DERIVED conservatively from the refs, never assumed
	// brokered. This closes the bypass where {name, runtime, active} with no refs used
	// to sail past the gate: it now resolves to "unclassified" (deny-closed under
	// require_signed), and a version_ref-only row resolves to "unclassified" too — only
	// a full local (both refs) or an explicit type is honored.
	d.DeploymentType = strings.TrimSpace(d.DeploymentType)
	if d.DeploymentType == "" {
		d.DeploymentType = effectiveDeploymentType("", d.OwnedRef, d.VersionRef)
	}
	if !deploymentTypes[d.DeploymentType] {
		return "deployment_type must be local, brokered or unclassified"
	}
	switch d.DeploymentType {
	case depTypeLocal:
		// A local deployment serves ONE self-hosted model version: it must name both the
		// owned model and the version so admission + lineage can be checked. Omitting a
		// ref would (a) blank the served version and (b) skip the gate — both are D-08.
		if d.OwnedRef == "" || d.VersionRef == "" {
			return "a local deployment requires both owned_ref and version_ref"
		}
	case depTypeBrokered:
		// Brokered inference (hosted provider, e.g. Claude) is not a self-hosted artifact:
		// it carries no model_version and is never admission-gated (contract). It must
		// be a POSITIVE declaration — an explicit provider endpoint — not the mere absence
		// of refs, so a version_ref/owned_ref here is a contradiction.
		if d.OwnedRef != "" || d.VersionRef != "" {
			return "a brokered deployment must not reference a self-hosted owned_ref/version_ref"
		}
		if d.EndpointRef == "" {
			return "a brokered deployment must name its provider endpoint (endpoint_ref)"
		}
	case depTypeUnclassified:
		// A migration/ambiguous state: it may be persisted (observe mode) but an ACTIVE
		// unclassified row is deny-closed under require_signed (gateDeployment) until an
		// operator classifies it as local (with an admitted version) or brokered.
	}
	return ""
}

func (d inferenceDeploymentDTO) toRecord() model.Record {
	return model.Record{
		colDepName: d.Name, colDepRuntime: d.Runtime, colDepType: d.DeploymentType,
		colDepEndpoint: trimClamp(d.EndpointRef),
		colDepOwned:    trimClamp(d.OwnedRef), colDepVersion: trimClamp(d.VersionRef),
		colDepStatus: d.Status, colDepGoverned: d.Governed, colDepNote: trimClamp(d.Note),
	}
}

func toInferenceDeploymentDTO(rec model.Record) inferenceDeploymentDTO {
	owned, version := rec.String(colDepOwned), rec.String(colDepVersion)
	return inferenceDeploymentDTO{
		ID: rec.String(model.ColID), Name: rec.String(colDepName), Runtime: rec.String(colDepRuntime),
		DeploymentType: effectiveDeploymentType(rec.String(colDepType), owned, version),
		EndpointRef:    rec.String(colDepEndpoint), OwnedRef: owned, VersionRef: version,
		Status: rec.String(colDepStatus), Governed: rec.Bool(colDepGoverned), Note: rec.String(colDepNote),
	}
}

func (m *Module) handleListDeployments(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(r)
	if v := r.URL.Query().Get("runtime"); v != "" {
		q.Filters = append(q.Filters, eq(colDepRuntime, v))
	}
	if v := r.URL.Query().Get("status"); v != "" {
		q.Filters = append(q.Filters, eq(colDepStatus, v))
	}
	out := listResponse[inferenceDeploymentDTO]{Items: []inferenceDeploymentDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(inferenceDeploymentKind)
		if err != nil {
			return err
		}
		recs, page, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			out.Items = append(out.Items, toInferenceDeploymentDTO(rec))
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

// gateDeployment applies the D-08 discriminated admission gate INSIDE the write
// transaction. For a local deployment it enforces membership (the referenced version
// must belong to the claimed owned model, else an admitted version of model B could
// falsify model A's lineage) and, when active, a verified signed admission under
// require_signed; an active unclassified/version-less deployment is deny-closed under
// require_signed; a brokered deployment is never gated. It returns (badReq→400,
// denyReason→422): both empty means allowed. It assumes in.validate() already ran, so
// a local deployment has both refs and a brokered one has none.
func (m *Module) gateDeployment(r *http.Request, sc store.Scope, in inferenceDeploymentDTO) (badReq, denyReason string, err error) {
	if in.DeploymentType == depTypeLocal {
		verRepo, verr := sc.Ext(modelVersionKind)
		if verr != nil {
			return "", "", verr
		}
		verRec, gerr := verRepo.Get(r.Context(), model.ID(in.VersionRef))
		if gerr != nil {
			return "", "", gerr
		}
		if verRec.String(colVerOwned) != in.OwnedRef {
			return "version_ref does not belong to owned_ref: this model version is owned by a different model (refusing to falsify lineage)", "", nil
		}
	}
	denied, reason, derr := m.admissionDeniesDeployment(r, sc, in.DeploymentType, in.Status, in.VersionRef)
	if derr != nil {
		return "", "", derr
	}
	if denied {
		return "", reason, nil
	}
	return "", "", nil
}

func (m *Module) handleCreateDeployment(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var in inferenceDeploymentDTO
	if !decodeJSON(w, r, &in) {
		return
	}
	if msg := in.validate(); msg != "" {
		writeJSON(w, http.StatusBadRequest, errorBody(msg))
		return
	}
	var (
		out        inferenceDeploymentDTO
		denyReason string
		badReq     string
	)
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		// A deployment's owned_ref/version_ref must resolve if set.
		if err := checkRef(r.Context(), sc, ownedModelKind, in.OwnedRef); err != nil {
			return err
		}
		if err := checkRef(r.Context(), sc, modelVersionKind, in.VersionRef); err != nil {
			return err
		}
		// Deny-closed gate: membership + a verified signed-model admission for a
		// self-hosted (local) version; brokered is never gated; unclassified is deny-closed.
		if bad, reason, derr := m.gateDeployment(r, sc, in); derr != nil {
			return derr
		} else if bad != "" {
			badReq = bad
			return nil
		} else if reason != "" {
			denyReason = reason
			return nil
		}
		repo, err := sc.Ext(inferenceDeploymentKind)
		if err != nil {
			return err
		}
		rec, err := repo.Create(r.Context(), in.toRecord())
		if err != nil {
			return err
		}
		out = toInferenceDeploymentDTO(rec)
		return auditOwned(r.Context(), sc, mc, inferenceDeploymentKind, "create", model.ID(rec.String(model.ColID)))
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if badReq != "" {
		writeJSON(w, http.StatusBadRequest, errorBody(badReq))
		return
	}
	if denyReason != "" {
		writeJSON(w, http.StatusUnprocessableEntity, errorBody(denyReason))
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (m *Module) handleUpdateDeployment(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var in inferenceDeploymentDTO
	if !decodeJSON(w, r, &in) {
		return
	}
	if msg := in.validate(); msg != "" {
		writeJSON(w, http.StatusBadRequest, errorBody(msg))
		return
	}
	var (
		out        inferenceDeploymentDTO
		denyReason string
		badReq     string
	)
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(inferenceDeploymentKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		// D-08 anti-blanking: PUT is a full replace (api.go), so a body that DROPS the
		// version_ref of an ACTIVE local deployment would silently un-serve/re-point it
		// past the gate. Refuse rather than blank — the operator must resend the refs,
		// stop the deployment, or convert it to brokered.
		existingType := effectiveDeploymentType(rec.String(colDepType), rec.String(colDepOwned), rec.String(colDepVersion))
		if existingType == depTypeLocal && rec.String(colDepStatus) == "active" && in.VersionRef == "" {
			badReq = "refusing to blank the served model version of an active local deployment: resend owned_ref and version_ref (or set status=stopped, or switch to a brokered deployment_type)"
			return nil
		}
		if err := checkRef(r.Context(), sc, ownedModelKind, in.OwnedRef); err != nil {
			return err
		}
		if err := checkRef(r.Context(), sc, modelVersionKind, in.VersionRef); err != nil {
			return err
		}
		// Deny-closed gate on the NEW desired state (re-pointing a deployment at
		// an unverified/mismatched version is refused under require_signed).
		if bad, reason, derr := m.gateDeployment(r, sc, in); derr != nil {
			return derr
		} else if bad != "" {
			badReq = bad
			return nil
		} else if reason != "" {
			denyReason = reason
			return nil
		}
		for k, v := range in.toRecord() {
			rec[k] = v
		}
		rec, err = repo.Update(r.Context(), rec)
		if err != nil {
			return err
		}
		out = toInferenceDeploymentDTO(rec)
		return auditOwned(r.Context(), sc, mc, inferenceDeploymentKind, "update", id)
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if badReq != "" {
		writeJSON(w, http.StatusBadRequest, errorBody(badReq))
		return
	}
	if denyReason != "" {
		writeJSON(w, http.StatusUnprocessableEntity, errorBody(denyReason))
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (m *Module) handleDeleteDeployment(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	m.deleteExt(w, r, mc, inferenceDeploymentKind)
}

// --- finetune_job handlers ---------------------------------------------------

func (d *finetuneJobDTO) validate() string {
	d.Name = trimClamp(d.Name)
	if d.Name == "" {
		return "name is required"
	}
	if d.Status == "" {
		d.Status = "queued"
	}
	if !jobStates[d.Status] {
		return "status must be queued, running, succeeded, failed or canceled"
	}
	// runtime is optional, but if set it must be a known runtime (keeps the indexed
	// column queryable — same closed-enum discipline as inference_deployment).
	if d.Runtime != "" && !runtimes[d.Runtime] {
		return "runtime must be vllm, ollama, llamacpp or other"
	}
	return ""
}

func (d finetuneJobDTO) toRecord() model.Record {
	rec := model.Record{
		colJobName: d.Name, colJobBase: trimClamp(d.BaseRef), colJobDataset: trimClamp(d.DatasetRef),
		colJobRuntime: trimClamp(d.Runtime), colJobStatus: d.Status, colJobResult: trimClamp(d.ResultVersionID),
		colJobNote: trimClamp(d.Note),
	}
	if ts, err := model.ParseTimestamp(d.StartedAt); err == nil && !ts.IsZero() {
		rec[colJobStarted] = ts.String()
	}
	if ts, err := model.ParseTimestamp(d.EndedAt); err == nil && !ts.IsZero() {
		rec[colJobEnded] = ts.String()
	}
	return rec
}

func toFinetuneJobDTO(rec model.Record) finetuneJobDTO {
	return finetuneJobDTO{
		ID: rec.String(model.ColID), Name: rec.String(colJobName), BaseRef: rec.String(colJobBase),
		DatasetRef: rec.String(colJobDataset), Runtime: rec.String(colJobRuntime), Status: rec.String(colJobStatus),
		ResultVersionID: rec.String(colJobResult), StartedAt: rec.String(colJobStarted), EndedAt: rec.String(colJobEnded),
		Note: rec.String(colJobNote),
	}
}

func (m *Module) handleListJobs(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(r)
	if v := r.URL.Query().Get("status"); v != "" {
		q.Filters = append(q.Filters, eq(colJobStatus, v))
	}
	out := listResponse[finetuneJobDTO]{Items: []finetuneJobDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(finetuneJobKind)
		if err != nil {
			return err
		}
		recs, page, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			out.Items = append(out.Items, toFinetuneJobDTO(rec))
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

func (m *Module) handleCreateJob(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var in finetuneJobDTO
	if !decodeJSON(w, r, &in) {
		return
	}
	if msg := in.validate(); msg != "" {
		writeJSON(w, http.StatusBadRequest, errorBody(msg))
		return
	}
	var out finetuneJobDTO
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		// If the job already records the version it produced, that version must exist.
		if err := checkRef(r.Context(), sc, modelVersionKind, in.ResultVersionID); err != nil {
			return err
		}
		repo, err := sc.Ext(finetuneJobKind)
		if err != nil {
			return err
		}
		rec, err := repo.Create(r.Context(), in.toRecord())
		if err != nil {
			return err
		}
		out = toFinetuneJobDTO(rec)
		return auditOwned(r.Context(), sc, mc, finetuneJobKind, "create", model.ID(rec.String(model.ColID)))
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (m *Module) handleGetJob(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var out finetuneJobDTO
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(finetuneJobKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		out = toFinetuneJobDTO(rec)
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleUpdateJob records a status/lineage transition on a fine-tune job (the
// control plane TRACKS the job; it does not run it). It is the place a succeeded
// job is linked to the model_version it produced (result_version_ref lineage).
func (m *Module) handleUpdateJob(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var in finetuneJobDTO
	if !decodeJSON(w, r, &in) {
		return
	}
	if msg := in.validate(); msg != "" {
		writeJSON(w, http.StatusBadRequest, errorBody(msg))
		return
	}
	var out finetuneJobDTO
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		// The version a succeeded job links to (lineage) must exist if set.
		if err := checkRef(r.Context(), sc, modelVersionKind, in.ResultVersionID); err != nil {
			return err
		}
		repo, err := sc.Ext(finetuneJobKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		for k, v := range in.toRecord() {
			rec[k] = v
		}
		rec, err = repo.Update(r.Context(), rec)
		if err != nil {
			return err
		}
		out = toFinetuneJobDTO(rec)
		return auditOwned(r.Context(), sc, mc, finetuneJobKind, "update", id)
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// --- shared helpers ----------------------------------------------------------

// deleteExt deletes an XXIII entity by id and self-audits, shared by the
// entities whose delete is a plain remove.
func (m *Module) deleteExt(w http.ResponseWriter, r *http.Request, mc api.ModuleContext, kind model.Kind) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(kind)
		if err != nil {
			return err
		}
		if err := repo.Delete(r.Context(), id); err != nil {
			return err
		}
		return auditOwned(r.Context(), sc, mc, kind, "delete", id)
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}

// deleteExtGuarded deletes an XXIII entity by id ONLY if no inference_deployment still
// references it through depCol (owned_ref / version_ref). A live reference returns 409
// Conflict (delete or repoint the deployment first) — the reverse-ref integrity D-08
// requires so a deployment can never be left with a dangling model/version reference.
func (m *Module) deleteExtGuarded(w http.ResponseWriter, r *http.Request, mc api.ModuleContext, kind model.Kind, depCol string) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var referenced bool
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		depRepo, err := sc.Ext(inferenceDeploymentKind)
		if err != nil {
			return err
		}
		recs, _, err := depRepo.List(r.Context(), model.Query{Filters: []model.Filter{eq(depCol, id.String())}, Limit: 1})
		if err != nil {
			return err
		}
		if len(recs) > 0 {
			referenced = true
			return nil
		}
		repo, err := sc.Ext(kind)
		if err != nil {
			return err
		}
		if err := repo.Delete(r.Context(), id); err != nil {
			return err
		}
		return auditOwned(r.Context(), sc, mc, kind, "delete", id)
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if referenced {
		writeJSON(w, http.StatusConflict, errorBody("cannot delete: still referenced by an inference deployment (delete or repoint the deployment first)"))
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}

// checkRef verifies an OPTIONAL reference points to an existing entity of kind
// within the same tenant scope. An empty id is allowed (the field is optional);
// a non-empty id that does not resolve returns the store's not-found error, so a
// deployment/job can never carry a dangling owned-model / version reference.
func checkRef(ctx context.Context, sc store.Scope, kind model.Kind, id string) error {
	if id == "" {
		return nil
	}
	repo, err := sc.Ext(kind)
	if err != nil {
		return err
	}
	_, err = repo.Get(ctx, model.ID(id))
	return err
}

// auditOwned appends an XXIII governance audit event attributed to the real
// principal, in the caller's transaction (docs/SECURITY-HARDENING.md self-audit).
func auditOwned(ctx context.Context, sc store.Scope, mc api.ModuleContext, kind model.Kind, verb string, id model.ID) error {
	_, err := sc.Audit().Append(ctx, model.AuditDraft{
		Actor:      mc.Principal.Actor(),
		ActorKind:  mc.Principal.ActorKind(),
		Action:     string(kind) + "." + verb,
		TargetKind: kind,
		TargetID:   id,
	})
	return err
}
