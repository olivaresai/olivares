// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package skills

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	obstrace "github.com/olivaresai/olivares/core/observability/trace"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk"
)

var errInvalidRequest = errors.New("invalid skills request")

const Namespace = "skills"

// A full admitted revision includes 4096 bounded paths (also repeated in
// scripts/extensions) and up to 128 bounded frontmatters. Their metadata fits
// this ceiling even with JSON escaping. Source file bodies are not returned.
const MaxCatalogResponseBytes = 256 << 20

const (
	PackKind     model.Kind      = "skills.pack"
	RevisionKind model.Kind      = "skills.revision"
	receiptKind  model.Kind      = "skills.import_receipt"
	permRead     auth.Permission = "skills:catalog:read"
	permWrite    auth.Permission = "skills:catalog:write"
	permAdmin    auth.Permission = "skills:catalog:admin"
	permAssign   auth.Permission = "skills:assignment:write"
)

// SkillLoadTracer starts the GenAI load_skill span of one skill member a saved
// assignment pins; the caller ends the returned span (nil-safe). It is
// satisfied by the engine's trace provider (core/observability/trace). Emission
// is telemetry only: it can never fail the assignment.
type SkillLoadTracer interface {
	LoadSkill(conversationID, skillName string) *obstrace.AgentSpan
}

type Options struct {
	ArtifactRoot string
	Git          *GitImporter
	Targets      TargetAuthority
	Workspace    WorkspaceImporter
	// Tracer, when wired by the composition root, receives one load_skill span
	// per selected member of a saved assignment (issue #429).
	Tracer SkillLoadTracer
	// RefuseReferencedPack is supplied with native target integration. It must
	// fence recorded conversation pins and live uses through this transaction.
	// Catalog-only installations have no native pins; connecting Targets without
	// this check refuses retirement even after assignments are removed.
	RefuseReferencedPack func(context.Context, store.Scope, model.ID) error
}
type Module struct {
	opts                 Options
	data                 api.ModuleData
	assignmentTargetRefs map[string]api.EntityRef
}

func New(opts Options) *Module {
	m := &Module{opts: opts}
	m.UseTargets(opts.Targets, opts.RefuseReferencedPack)
	return m
}
func (m *Module) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{Name: "olivares.skills", Version: "0.1.0", APIVersion: sdk.APIVersion, Type: sdk.TypeModule, Title: "Skills", Description: "Immutable skills catalog and pinned assignments."}
}
func (m *Module) Init(context.Context, sdk.Host) error { return nil }
func (m *Module) Start(context.Context) error          { return nil }
func (m *Module) Stop(context.Context) error           { return nil }
func (m *Module) UseData(d api.ModuleData)             { m.data = d }

// UseTargets binds the native target authority and the recorded-use fence once
// the composition root has the authorizer they need, before serving. Both
// together: a target authority without the fence refuses every retirement.
func (m *Module) UseTargets(targets TargetAuthority, refuseReferenced func(context.Context, store.Scope, model.ID) error) {
	m.opts.Targets, m.opts.RefuseReferencedPack = targets, refuseReferenced
	// Resolve route metadata at binding, so APIRoutes never invokes authority
	// methods whose effects could re-enter the registration being enumerated.
	m.assignmentTargetRefs = nil
	if authority, ok := targets.(AssignmentAuthority); ok {
		m.assignmentTargetRefs = authority.AssignmentTargetRefs()
	}
}
func (m *Module) APINamespace() string { return Namespace }
func (m *Module) Permissions() []auth.Permission {
	return []auth.Permission{permRead, permWrite, permAdmin, permAssign}
}
func (m *Module) APIRoutes(reg api.RouteRegistrar) {
	reg.Handle("GET", "/packs", permRead, m.listPacks)
	reg.Handle("POST", "/packs", permWrite, m.installPack)
	ref := api.EntityRef{Kind: PackKind, IDParam: "id", ConcealDeniedAsNotFound: true}
	reg.HandleEntity("GET", "/packs/{id}", permRead, ref, m.getPack)
	reg.HandleEntity("POST", "/packs/{id}/revisions", permWrite, ref, m.installPack)
	reg.HandleEntity("DELETE", "/packs/{id}", permAdmin, ref, m.retirePack)
	reg.HandleEntity("GET", "/packs/{id}/assignments", permRead, ref, m.listPackAssignments)
	reg.Handle("GET", "/assignments", permRead, m.listAssignments)
	{
		reg := reg
		if _, ok := m.opts.Targets.(AssignmentAuthority); !ok {
			reg = collectionAssignmentRegistrar{RouteRegistrar: reg}
		}
		ref := api.EntityRef{BodyKindField: "target_kind", BodyKinds: m.assignmentTargetRefs, ConcealDeniedAsNotFound: true}
		for _, selected := range ref.BodyKinds {
			if selected.DeniedReadPermission != "" {
				ref.DeniedReadRoleOnly = true
				break
			}
		}
		reg.HandleEntity("POST", "/assignments", permAssign, ref, m.saveAssignment)
	}
	assignment := api.EntityRef{Kind: AssignmentKind, IDParam: "id", WorkspaceColumn: "workspace_id", ConcealDeniedAsNotFound: true}
	reg.HandleEntity("PUT", "/assignments/{id}", permAssign, assignment, m.saveAssignment)
	reg.HandleEntity("DELETE", "/assignments/{id}", permAssign, assignment, m.deleteAssignment)
}

// collectionAssignmentRegistrar keeps the collection door for targets without
// native assignment authorization. It wraps only the POST assignment registration.
type collectionAssignmentRegistrar struct{ api.RouteRegistrar }

func (r collectionAssignmentRegistrar) HandleEntity(method, pattern string, perm auth.Permission, _ api.EntityRef, handler api.ModuleHandler) {
	r.RouteRegistrar.Handle(method, pattern, perm, handler)
}

type Pack struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	State            string `json:"state"`
	LatestRevisionID string `json:"latest_revision_id"`
	LatestRevision   int64  `json:"latest_revision"`
	Version          int64  `json:"version"`
	CreatedBy        string `json:"created_by"`
	CreatedAt        string `json:"created_at"`
	UpdatedAt        string `json:"updated_at"`
}
type Source struct {
	Kind                string `json:"kind"`
	Origin              string `json:"origin,omitempty"`
	RequestedRef        string `json:"requested_ref,omitempty"`
	ResolvedCommit      string `json:"resolved_commit,omitempty"`
	RegistrationVersion int64  `json:"registration_version,omitempty"`
	SourceDigest        string `json:"source_digest"`
	PublisherVerified   bool   `json:"publisher_verified"`
}
type Revision struct {
	ID             string      `json:"id"`
	PackID         string      `json:"pack_id"`
	Number         int64       `json:"number"`
	Source         Source      `json:"source"`
	ManifestDigest string      `json:"manifest_digest"`
	Manifest       []FileEntry `json:"manifest"`
	Members        []Member    `json:"members"`
	Validator      string      `json:"validator"`
	CreatedBy      string      `json:"created_by"`
	CreatedAt      string      `json:"created_at"`
}
type InstallResult struct {
	State    string   `json:"state"`
	Pack     Pack     `json:"pack"`
	Revision Revision `json:"revision"`
}
type PackDetail struct {
	Pack      Pack       `json:"pack"`
	Revisions []Revision `json:"revisions"`
	Cursor    string     `json:"revisions_cursor,omitempty"`
	HasMore   bool       `json:"has_more_revisions"`
}
type packList = api.ListResponse[Pack]
type importRequest struct {
	Name   string `json:"name"`
	Source struct {
		Kind           string `json:"kind"`
		URL            string `json:"url"`
		Ref            string `json:"ref"`
		Subdir         string `json:"subdir,omitempty"`
		ExpectedDigest string `json:"expected_digest,omitempty"`
		WorkspaceRef   string `json:"workspace_ref,omitempty"`
		Directory      string `json:"directory,omitempty"`
	} `json:"source"`
}

func packDTO(row model.Record) Pack {
	return Pack{ID: row.String(model.ColID), Name: row.String("name"), State: row.String("state"), LatestRevisionID: row.String("latest_revision_id"), LatestRevision: row.Int("latest_revision"), Version: row.Int(model.ColVersion), CreatedBy: row.String("created_by"), CreatedAt: row.String(model.ColCreatedAt), UpdatedAt: row.String(model.ColUpdatedAt)}
}
func revisionDTO(row model.Record) (Revision, error) {
	var rev Revision
	if err := json.Unmarshal([]byte(row.String("payload")), &rev); err != nil {
		return rev, store.ErrStoreUnavailable
	}
	rev.ID = row.String(model.ColID)
	rev.PackID = row.String("pack_id")
	rev.CreatedAt = row.String(model.ColCreatedAt)
	rev.CreatedBy = row.String("created_by")
	return rev, nil
}
func query(r *http.Request) (model.Query, error) {
	q := model.Query{Limit: 50, Cursor: r.URL.Query().Get("cursor")}
	if value := r.URL.Query().Get("limit"); value != "" {
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 || n > 200 {
			return q, errInvalidRequest
		}
		q.Limit = n
	}
	return q, nil
}

// listPacks lists the tenant's skills packs, including retired packs, with
// cursor pagination.
func (m *Module) listPacks(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q, err := query(r)
	if err != nil {
		respondError(w, err)
		return
	}
	out := packList{}
	err = mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(PackKind)
		if err != nil {
			return err
		}
		rows, page, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		for _, row := range rows {
			out.Items = append(out.Items, packDTO(row))
		}
		out.Cursor = page.Cursor
		out.HasMore = page.HasMore
		return nil
	})
	if err != nil {
		respondError(w, err)
		return
	}
	respond(w, http.StatusOK, out)
}

// getPack returns a skills pack and a cursor-paged history of its complete
// immutable revisions, including manifests and import provenance.
func (m *Module) getPack(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	out := PackDetail{Revisions: []Revision{}}
	q, err := query(r)
	if err != nil {
		respondError(w, err)
		return
	}
	q.Filters = []model.Filter{{Column: "pack_id", Op: model.OpEq, Value: chi.URLParam(r, "id")}}
	limit := q.Limit
	q.Limit = 1
	err = mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(PackKind)
		if err != nil {
			return err
		}
		row, err := repo.Get(r.Context(), model.ID(chi.URLParam(r, "id")))
		if err != nil {
			return err
		}
		out.Pack = packDTO(row)
		revisions, err := sc.Ext(RevisionKind)
		if err != nil {
			return err
		}
		// Read one immutable row at a time. A page targets 1 MiB, but always
		// includes one full admitted revision; large histories advance by cursor
		// without allocating all their manifests together.
		responseBytes := 0
		for len(out.Revisions) < limit {
			rows, page, err := revisions.List(r.Context(), q)
			if err != nil {
				return err
			}
			if len(rows) == 0 {
				break
			}
			rev, err := revisionDTO(rows[0])
			if err != nil {
				return err
			}
			encoded, err := json.Marshal(rev)
			if err != nil || len(encoded) > MaxCatalogResponseBytes-(1<<20) {
				return store.ErrStoreUnavailable
			}
			if responseBytes+len(encoded) > MaxCatalogResponseBytes-(1<<20) {
				out.HasMore = true
				break
			}
			out.Revisions = append(out.Revisions, rev)
			responseBytes += len(encoded)
			out.Cursor, out.HasMore = page.Cursor, page.HasMore
			if !page.HasMore || responseBytes >= 1<<20 {
				break
			}
			if page.Cursor == "" || page.Cursor == q.Cursor {
				return store.ErrStoreUnavailable
			}
			q.Cursor = page.Cursor
		}
		return nil
	})
	if err != nil {
		respondError(w, err)
		return
	}
	respond(w, http.StatusOK, out)
}
func allRows(ctx context.Context, repo store.GenericRepo, filters ...model.Filter) ([]model.Record, error) {
	var out []model.Record
	q := model.Query{Limit: 200, Filters: filters}
	for {
		rows, page, err := repo.List(ctx, q)
		if err != nil {
			return nil, err
		}
		out = append(out, rows...)
		if !page.HasMore {
			return out, nil
		}
		if page.Cursor == "" {
			return nil, errInvalidRequest
		}
		q.Cursor = page.Cursor
	}
}
func respond(w http.ResponseWriter, status int, value any) {
	if value == nil {
		value = json.RawMessage("null")
	}
	api.WriteJSON(w, status, value, "application/json")
}
func respondError(w http.ResponseWriter, err error) {
	status, code, message := http.StatusServiceUnavailable, "skills_unavailable", "Skills could not complete this operation. Retry after checking the service."
	var refusal *ImportError
	switch {
	case errors.As(err, &refusal):
		status, code, message = http.StatusBadRequest, refusal.Code, refusal.Message
		if code == "source_unavailable" || code == "target_authority_unavailable" || code == "usage_authority_unavailable" {
			status = http.StatusServiceUnavailable
		} else if code == "pack_in_use" {
			status = http.StatusConflict
		}
	case errors.Is(err, store.ErrNotFound):
		status, code, message = http.StatusNotFound, "not_found", "Skills pack not found."
	case errors.Is(err, auth.ErrRouteDenied):
		status, code, message = http.StatusNotFound, "not_found", "Skills target not found."
	case errors.Is(err, store.ErrConflict):
		status, code, message = http.StatusConflict, "conflict", "The request conflicts with the recorded version or import. Read the current state and retry."
	case errors.Is(err, errInvalidRequest):
		status, code, message = http.StatusBadRequest, "invalid_request", "The skills request is invalid."
	}
	respond(w, status, map[string]any{"error": map[string]string{"code": code, "message": api.RequestBodyErrorMessage(err, message)}})
}

// retirePack retires an unreferenced skills pack at the expected version while
// retaining its immutable revisions and provenance.
func (m *Module) retirePack(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	version, err := expectedVersion(r)
	if err != nil {
		respondError(w, err)
		return
	}
	id := model.ID(chi.URLParam(r, "id"))
	var result Pack
	err = mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		packs, err := sc.Ext(PackKind)
		if err != nil {
			return err
		}
		row, err := packs.Get(r.Context(), id)
		if err != nil {
			return err
		}
		if row.Int(model.ColVersion) != version || row.String("state") != "enabled" {
			return store.ErrConflict
		}
		// Withdraw eligibility under the pack's OCC lock before checking pins.
		// Assignment creation touches this same row; neither operation can commit
		// against the other's stale enabled state. Any refusal rolls this back.
		row["state"] = "retired"
		row, err = packs.Update(r.Context(), row)
		if err != nil {
			return err
		}
		assignments, err := sc.Ext(AssignmentKind)
		if err != nil {
			return err
		}
		pins, _, err := assignments.List(r.Context(), model.Query{Limit: 1, Filters: []model.Filter{{Column: "pack_id", Op: model.OpEq, Value: id.String()}}})
		if err != nil {
			return err
		}
		if len(pins) != 0 {
			return &ImportError{Code: "pack_in_use", Message: "This skills pack is still assigned or used by recorded conversations. Unassign it and stop or abandon the affected conversations before removing it."}
		}
		if m.opts.RefuseReferencedPack != nil {
			if err := m.opts.RefuseReferencedPack(r.Context(), sc, id); err != nil {
				return err
			}
		} else if m.opts.Targets != nil {
			return &ImportError{Code: "usage_authority_unavailable", Message: "Recorded skills usage could not be checked. No pack was removed."}
		}
		result = packDTO(row)
		return catalogAudit(r.Context(), sc, mc, "skills.catalog.retire", PackKind, id, map[string]any{"pack_version": result.Version})
	})
	if err != nil {
		respondError(w, err)
		return
	}
	respond(w, http.StatusOK, map[string]any{"state": "catalog_retired", "pack": result})
}

// An import plan binds the caller's request before fetching remote bytes. A
// committed retry must not depend on a branch still pointing at its old commit,
// the source remaining online, or a workspace still containing its old files.
type importPlan struct {
	name        string
	fingerprint string
	read        func() (*ValidatedPack, Source, error)
}

func (m *Module) readImport(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) (importPlan, error) {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		return importPlan{}, errInvalidRequest
	}
	r.Body = http.MaxBytesReader(w, r.Body, MaxSourceBytes+(64<<10))
	if media == "application/json" {
		var req importRequest
		if err := api.DecodeRequestBody(w, r, &req, api.RequestBodySpec{MaxBytes: 16 << 10}); err != nil {
			return importPlan{}, errors.Join(errInvalidRequest, err)
		}
		var read func() (*ValidatedPack, Source, error)
		switch req.Source.Kind {
		case "workspace":
			if req.Source.URL != "" || req.Source.Ref != "" || req.Source.Subdir != "" || !canonicalID(req.Source.WorkspaceRef) || !safePath(req.Source.Directory) {
				return importPlan{}, errInvalidRequest
			}
			read = func() (*ValidatedPack, Source, error) {
				pack, version, err := m.importWorkspace(r.Context(), mc, req.Source.WorkspaceRef, req.Source.Directory)
				if err != nil {
					return nil, Source{}, err
				}
				if req.Source.ExpectedDigest != "" && req.Source.ExpectedDigest != pack.SourceDigest {
					return nil, Source{}, refuse("source_changed", "workspace snapshot")
				}
				return pack, Source{Kind: "workspace", Origin: req.Source.WorkspaceRef + "/" + req.Source.Directory, SourceDigest: pack.SourceDigest, RegistrationVersion: version}, nil
			}
		case "git":
			if req.Source.WorkspaceRef != "" || req.Source.Directory != "" {
				return importPlan{}, errInvalidRequest
			}
			origin, err := validateGitSource(req.Source.URL, req.Source.Ref, req.Source.Subdir)
			if err != nil {
				return importPlan{}, err
			}
			req.Source.URL = origin.String()
			read = func() (*ValidatedPack, Source, error) {
				if m.opts.Git == nil {
					return nil, Source{}, refuse("source_unavailable", "git transport not configured")
				}
				pack, commit, err := m.opts.Git.Import(r.Context(), mc.Tenant, req.Source.URL, req.Source.Ref, req.Source.Subdir)
				if err != nil {
					return nil, Source{}, err
				}
				if req.Source.ExpectedDigest != "" && req.Source.ExpectedDigest != pack.SourceDigest {
					return nil, Source{}, refuse("source_changed", "source digest")
				}
				return pack, Source{Kind: "git", Origin: req.Source.URL, RequestedRef: req.Source.Ref, ResolvedCommit: commit, SourceDigest: pack.SourceDigest}, nil
			}
		case "builtin":
			if req.Source.URL != "" || req.Source.Subdir != "" || req.Source.WorkspaceRef != "" || req.Source.Directory != "" || req.Source.Ref == "" {
				return importPlan{}, errInvalidRequest
			}
			read = func() (*ValidatedPack, Source, error) {
				pack, source, err := importBuiltin(r.Context(), req.Source.Ref)
				if err != nil {
					return nil, Source{}, err
				}
				if req.Source.ExpectedDigest != "" && req.Source.ExpectedDigest != pack.SourceDigest {
					return nil, Source{}, refuse("source_changed", "source digest")
				}
				return pack, source, nil
			}
		default:
			return importPlan{}, refuse("unsupported_source", "source kind")
		}
		encoded, _ := json.Marshal(req)
		return importPlan{name: req.Name, fingerprint: digest(encoded), read: read}, nil
	}
	if media != "multipart/form-data" {
		return importPlan{}, errInvalidRequest
	}
	reader, err := r.MultipartReader()
	if err != nil {
		return importPlan{}, errInvalidRequest
	}
	fields := map[string]string{}
	seen := map[string]bool{}
	var raw []byte
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return importPlan{}, refuse("unsafe_pack", "multipart archive")
		}
		field := part.FormName()
		if seen[field] {
			_ = part.Close()
			return importPlan{}, errInvalidRequest
		}
		seen[field] = true
		max := 1024
		if field == "archive" {
			max = MaxSourceBytes
		} else if field != "name" && field != "format" && field != "expected_digest" {
			_ = part.Close()
			return importPlan{}, errInvalidRequest
		}
		body, readErr := boundedRead(r.Context(), part, max)
		_ = part.Close()
		if readErr != nil {
			return importPlan{}, readErr
		}
		if field == "archive" {
			raw = body
		} else {
			fields[field] = string(body)
		}
	}
	if !seen["archive"] {
		return importPlan{}, errInvalidRequest
	}
	fields["archive_digest"] = digest(raw)
	encoded, _ := json.Marshal(fields)
	return importPlan{name: fields["name"], fingerprint: digest(encoded), read: func() (*ValidatedPack, Source, error) {
		pack, err := ImportArchive(r.Context(), bytes.NewReader(raw), fields["format"], fields["expected_digest"])
		if err != nil {
			return nil, Source{}, err
		}
		return pack, Source{Kind: "archive", SourceDigest: pack.SourceDigest}, nil
	}}, nil
}

// installPack validates a Git, registered-workspace, or uploaded archive source
// and publishes an immutable skills revision, creating or updating a pack.
// Idempotency keys replay the committed import result.
func (m *Module) installPack(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	key := r.Header.Get("Idempotency-Key")
	if key == "" || len(key) > 128 || strings.IndexFunc(key, func(c rune) bool { return c < 33 || c > 126 }) >= 0 {
		respondError(w, errInvalidRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	plan, err := m.readImport(w, r, mc)
	if err != nil {
		respondError(w, err)
		return
	}
	name := plan.name
	if (chi.URLParam(r, "id") == "" && strings.TrimSpace(name) == "") || len(name) > 128 || strings.IndexFunc(name, func(c rune) bool { return c < 32 || c == 127 }) >= 0 {
		respondError(w, errInvalidRequest)
		return
	}
	requestDigest := digest([]byte(chi.URLParam(r, "id") + "\x00" + plan.fingerprint))
	keyHash := digest([]byte(mc.Principal.Actor() + "\x00" + chi.URLParam(r, "id") + "\x00" + key))
	var result InstallResult
	replay := false
	err = mc.Data.View(ctx, func(sc store.Scope) error {
		var e error
		result, replay, e = findReceipt(ctx, sc, keyHash, requestDigest)
		return e
	})
	if err != nil {
		respondError(w, err)
		return
	}
	if replay {
		respond(w, http.StatusOK, result)
		return
	}
	pack, source, err := plan.read()
	if err != nil {
		respondError(w, err)
		return
	}
	packID := model.ID(chi.URLParam(r, "id"))
	newPack := packID.IsZero()
	if newPack {
		packID = model.NewID()
	}
	revisionID := model.NewID()
	// Rebuild metadata from the importer's private byte snapshot before staging.
	tree := newTree()
	for _, entry := range pack.Manifest {
		body, ok := pack.File(entry.Path)
		if !ok || int64(len(body)) != entry.Size || digest(body) != entry.Digest {
			respondError(w, refuse("source_changed", "manifest"))
			return
		}
		if err := tree.entry(entry.Path, false); err != nil {
			respondError(w, err)
			return
		}
		if err := tree.file(entry.Path, body, os.FileMode(entry.Mode)); err != nil {
			respondError(w, err)
			return
		}
	}
	verified, err := tree.validate(pack.SourceDigest)
	if err != nil || verified.ManifestDigest != pack.ManifestDigest {
		if err == nil {
			err = refuse("source_changed", "manifest")
		}
		respondError(w, err)
		return
	}
	remove, err := m.stageArtifact(mc.Tenant, revisionID, verified)
	if err != nil {
		respondError(w, err)
		return
	}
	committed := false
	defer func() {
		if !committed {
			_ = remove()
		}
	}()
	err = mc.Data.Mutate(ctx, func(sc store.Scope) error {
		var err error
		result, replay, err = findReceipt(ctx, sc, keyHash, requestDigest)
		if err != nil || replay {
			return err
		}
		receipts, err := sc.Ext(receiptKind)
		if err != nil {
			return err
		}
		receipt, err := receipts.Create(ctx, model.Record{"key_hash": keyHash, "request_digest": requestDigest, "pack_id": packID.String(), "revision_id": revisionID.String(), "pack_snapshot": "{}"})
		if err != nil {
			return err
		}
		packs, err := sc.Ext(PackKind)
		if err != nil {
			return err
		}
		var row model.Record
		if newPack {
			row, err = packs.CreateWithID(ctx, packID, model.Record{"name": name, "state": "enabled", "latest_revision_id": revisionID.String(), "latest_revision": int64(1), "created_by": mc.Principal.Actor()})
		} else {
			row, err = packs.Get(ctx, packID)
			if err == nil {
				if row.String("state") != "enabled" {
					return store.ErrConflict
				}
				row["latest_revision_id"] = revisionID.String()
				row["latest_revision"] = row.Int("latest_revision") + 1
				row, err = packs.Update(ctx, row)
			}
		}
		if err != nil {
			return err
		}
		rev := Revision{PackID: packID.String(), Number: row.Int("latest_revision"), Source: source, ManifestDigest: verified.ManifestDigest, Manifest: verified.Manifest, Members: verified.Members, Validator: ValidatorVersion}
		payload, err := json.Marshal(rev)
		if err != nil {
			return err
		}
		revisions, err := sc.Ext(RevisionKind)
		if err != nil {
			return err
		}
		revisionRow, err := revisions.CreateWithID(ctx, revisionID, model.Record{"pack_id": packID.String(), "payload": string(payload), "created_by": mc.Principal.Actor()})
		if err != nil {
			return err
		}
		rev, err = revisionDTO(revisionRow)
		if err != nil {
			return err
		}
		result = InstallResult{State: "catalog_published", Pack: packDTO(row), Revision: rev}
		snapshot, err := json.Marshal(result.Pack)
		if err != nil {
			return err
		}
		receipt["pack_snapshot"] = string(snapshot)
		if _, err := receipts.Update(ctx, receipt); err != nil {
			return err
		}
		return catalogAudit(ctx, sc, mc, "skills.catalog.publish", PackKind, packID, map[string]any{"revision_id": revisionID.String(), "manifest_digest": rev.ManifestDigest, "source_kind": source.Kind, "resolved_commit": source.ResolvedCommit})
	})
	if err != nil {
		if errors.Is(err, store.ErrCommitOutcomeUnknown) {
			committed = true
		} // retain exact bytes for idempotent read-back
		respondError(w, err)
		return
	}
	if replay {
		respond(w, http.StatusOK, result)
		return
	}
	committed = true
	respond(w, http.StatusCreated, result)
}
func findReceipt(ctx context.Context, sc store.Scope, keyHash, requestDigest string) (InstallResult, bool, error) {
	var result InstallResult
	receipts, err := sc.Ext(receiptKind)
	if err != nil {
		return result, false, err
	}
	rows, _, err := receipts.List(ctx, model.Query{Limit: 1, Filters: []model.Filter{{Column: "key_hash", Op: model.OpEq, Value: keyHash}}})
	if err != nil || len(rows) == 0 {
		return result, false, err
	}
	row := rows[0]
	if row.String("request_digest") != requestDigest {
		return result, false, store.ErrConflict
	}
	if err := json.Unmarshal([]byte(row.String("pack_snapshot")), &result.Pack); err != nil {
		return result, false, store.ErrStoreUnavailable
	}
	revisions, err := sc.Ext(RevisionKind)
	if err != nil {
		return result, false, err
	}
	rev, err := revisions.Get(ctx, model.ID(row.String("revision_id")))
	if err != nil {
		return result, false, err
	}
	result.Revision, err = revisionDTO(rev)
	result.State = "catalog_published"
	return result, true, err
}
func catalogAudit(ctx context.Context, sc store.Scope, mc api.ModuleContext, action string, kind model.Kind, id model.ID, meta map[string]any) error {
	event, err := sc.Audit().Append(ctx, model.AuditDraft{Actor: mc.Principal.Actor(), ActorKind: mc.Principal.ActorKind(), Action: action, TargetKind: kind, TargetID: id, Meta: meta})
	if err == nil && event.Seq == 0 {
		return errors.New("skills audit was not persisted")
	}
	return err
}

func (m *Module) stageArtifact(tenant model.TenantID, id model.ID, pack *ValidatedPack) (func() error, error) {
	if m.opts.ArtifactRoot == "" || !filepath.IsAbs(m.opts.ArtifactRoot) {
		return nil, refuse("source_unavailable", "catalog artifact root not configured")
	}
	// MkdirAll can create more than the artifact root. Persist each new entry
	// through its parent, including the first ancestor that already existed.
	var parents []string
	for dir := m.opts.ArtifactRoot; ; dir = filepath.Dir(dir) {
		parents = append(parents, dir)
		if _, err := os.Stat(dir); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, refuse("source_unavailable", "catalog artifact root unavailable")
		}
	}
	if err := os.MkdirAll(m.opts.ArtifactRoot, 0700); err != nil {
		return nil, refuse("source_unavailable", "catalog artifact root unavailable")
	}
	tenantPath := filepath.Join(m.opts.ArtifactRoot, string(tenant))
	if err := os.Mkdir(tenantPath, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, refuse("source_unavailable", "catalog artifact root unavailable")
	}
	info, err := os.Lstat(tenantPath)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, refuse("source_unavailable", "catalog artifact root changed")
	}
	root, err := os.OpenRoot(tenantPath)
	if err != nil {
		return nil, refuse("source_unavailable", "catalog artifact root unavailable")
	}
	defer root.Close()
	name := id.String()
	if err := root.Mkdir(name, 0700); err != nil {
		return nil, refuse("source_unavailable", "catalog revision path occupied")
	}
	remove := func() error {
		owned, err := os.OpenRoot(tenantPath)
		if err != nil {
			return err
		}
		defer owned.Close()
		return owned.RemoveAll(name)
	}
	complete := false
	defer func() {
		if !complete {
			_ = root.RemoveAll(name)
		}
	}()
	directories := map[string]bool{name: true}
	for _, entry := range pack.Manifest {
		target := path.Join(name, entry.Path)
		if err := root.MkdirAll(path.Dir(target), 0700); err != nil {
			return nil, refuse("source_unavailable", "catalog artifact write failed")
		}
		for dir := path.Dir(target); dir != "."; dir = path.Dir(dir) {
			directories[dir] = true
		}
		file, err := root.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return nil, refuse("source_unavailable", "catalog artifact write failed")
		}
		body, _ := pack.File(entry.Path)
		_, writeErr := file.Write(body)
		mode := os.FileMode(0400)
		if entry.Mode&0111 != 0 {
			mode = 0500
		}
		modeErr := file.Chmod(mode)
		syncErr := file.Sync()
		closeErr := file.Close()
		if writeErr != nil || modeErr != nil || syncErr != nil || closeErr != nil {
			return nil, refuse("source_unavailable", "catalog artifact write failed")
		}
		reread, err := root.ReadFile(target)
		if err != nil || digest(reread) != entry.Digest {
			return nil, refuse("source_changed", "catalog artifact")
		}
	}
	var ordered []string
	for dir := range directories {
		ordered = append(ordered, dir)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(ordered)))
	for _, dir := range append(ordered, ".") {
		if err := syncDir(root, dir); err != nil {
			return nil, refuse("source_unavailable", "catalog artifact sync failed")
		}
	}
	for _, dir := range parents {
		parent, err := os.OpenRoot(dir)
		if err != nil {
			return nil, refuse("source_unavailable", "catalog artifact sync failed")
		}
		syncErr, closeErr := syncDir(parent, "."), parent.Close()
		if syncErr != nil || closeErr != nil {
			return nil, refuse("source_unavailable", "catalog artifact sync failed")
		}
	}
	complete = true
	return remove, nil
}

// Same rooted directory-sync pattern used by cmd/olivares/internal/toolinstall.
func syncDir(root *os.Root, name string) error {
	dir, err := root.Open(name)
	if err != nil {
		return err
	}
	syncErr, closeErr := dir.Sync(), dir.Close()
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}
