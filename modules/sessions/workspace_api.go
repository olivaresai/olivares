// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Permission tiers for the WORKSPACE / governed file surface. Read covers
// list/stat/read; write covers write/mkdir/move/delete (the file edits a session
// makes); admin covers register/configure/deregister (granting/revoking filesystem reach).
const (
	permWsRead  auth.Permission = "sessions:workspace:read"
	permWsWrite auth.Permission = "sessions:workspace:write"
	permWsAdmin auth.Permission = "sessions:workspace:admin"
)

// maxUploadBytes is the hard ceiling for a single write body before the per-workspace
// size limit applies (defense against a runaway upload).
const maxUploadBytes = 64 << 20

// workspacePermissions are the workspace-plane permissions, appended to the module set.
func workspacePermissions() []auth.Permission {
	return []auth.Permission{permWsRead, permWsWrite, permWsAdmin}
}

// workspaceRoutes mounts the workspace registry + governed file endpoints under
// /v1/m/sessions/. The relative file path travels as ?path=<rel> (never a URL
// segment) so the jail is unambiguous and a slash cannot break routing.
func (m *Module) workspaceRoutes(reg api.RouteRegistrar) {
	folder := api.EntityRef{Kind: workspaceKind, IDParam: "ref", LookupColumn: colWsRef, ResourceKind: "resource"}
	concealedFolder := folder
	concealedFolder.ConcealDeniedAsNotFound = true
	reg.HandleEntity("PUT", "/workspaces/{ref}/setup-command", permWsAdmin, concealedFolder, m.handleSetWorkspaceSetup)
	reg.HandleEntity("POST", "/workspaces/{ref}/setup-command/approve", permWsAdmin, concealedFolder, m.handleApproveWorkspaceSetup)
	reg.HandleEntity("POST", "/workspaces/{ref}/wiki", permWsAdmin, concealedFolder, m.handleInitWiki)
	reg.Handle("POST", "/workspaces", permWsAdmin, m.handleCreateWorkspace)
	reg.Handle("GET", "/workspaces", permWsRead, m.handleListWorkspaces)
	reg.HandleEntity("GET", "/workspaces/{ref}", permWsRead, folder, m.workspaceRight(permWsRead, auth.TrusteeRead, m.handleGetWorkspace))
	reg.HandleEntity("PATCH", "/workspaces/{ref}", permWsAdmin, folder, m.handlePatchWorkspace)
	reg.HandleEntity("DELETE", "/workspaces/{ref}", permWsAdmin, folder, m.handleDeleteWorkspace)
	reg.HandleEntity("GET", "/workspaces/{ref}/files", permWsRead, folder, m.workspaceRight(permWsRead, auth.TrusteeRead, m.handleListFiles))
	reg.HandleEntity("GET", "/workspaces/{ref}/files/stat", permWsRead, folder, m.workspaceRight(permWsRead, auth.TrusteeRead, m.handleStatFile))
	reg.HandleEntity("GET", "/workspaces/{ref}/files/raw", permWsRead, folder, m.workspaceRight(permWsRead, auth.TrusteeRead, m.handleReadFile))
	reg.HandleEntity("PUT", "/workspaces/{ref}/files/raw", permWsWrite, folder, m.workspaceRight(permWsWrite, auth.TrusteeWrite, m.handleWriteFile))
	reg.HandleEntity("POST", "/workspaces/{ref}/files/dir", permWsWrite, folder, m.workspaceRight(permWsWrite, auth.TrusteeCreate, m.handleMkdir))
	reg.HandleEntity("POST", "/workspaces/{ref}/files/move", permWsWrite, folder, m.workspaceRight(permWsWrite, auth.TrusteeErase, m.handleMoveFile))
	reg.HandleEntity("DELETE", "/workspaces/{ref}/files", permWsWrite, folder, m.workspaceRight(permWsWrite, auth.TrusteeErase, m.handleDeleteFile))
	reg.HandleEntity("GET", "/workspaces/{ref}/project-config", permWsAdmin, concealedFolder, m.handleGetProjectConfig)
	reg.HandleEntity("POST", "/workspaces/{ref}/project-config/approve", permWsAdmin, concealedFolder, m.handleApproveProjectConfig)
	m.terminalRoutes(reg, folder)
}

// workspaceRight checks the canonical trustee action at the folder Resource.
// The published module permission still bounds the caller's credential.
func (m *Module) workspaceRight(permission auth.Permission, right auth.TrusteeRight, next api.ModuleHandler) api.ModuleHandler {
	return func(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
		if m.workspaceAllowed(w, r, mc, permission, right) {
			next(w, r, mc)
		}
	}
}

// workspaceAllowed is the HTTP door shared by named administration handlers and
// file-route wrappers. Named handlers retain their published operation prose.
func (m *Module) workspaceAllowed(w http.ResponseWriter, r *http.Request, mc api.ModuleContext, permission auth.Permission, right auth.TrusteeRight) bool {
	if _, err := m.loadWorkspaceRec(r.Context(), mc.Tenant, chi.URLParam(r, "ref")); err != nil {
		writeRunErr(w, err)
		return false
	}
	if err := m.authorizeWorkspaceRight(r.Context(), mc, permission, right, mc.Resource); err != nil {
		var denied *runErr
		if permission == permWsAdmin && errors.As(err, &denied) && denied.status == http.StatusForbidden {
			err = &runErr{http.StatusNotFound, "not found"}
		}
		writeRunErr(w, err)
		return false
	}
	return true
}

func (m *Module) authorizeWorkspaceRight(ctx context.Context, mc api.ModuleContext, permission auth.Permission, right auth.TrusteeRight, node auth.ResourceAttrs) error {
	return CheckFolderRight(ctx, m.Data, auth.Request{Principal: mc.Principal, Tenant: mc.Tenant, Permission: permission, Resource: node}, right, func(q auth.Request) error {
		if mc.Admission == nil || !mc.Admission(ctx, q) {
			return forbiddenErr("you do not have this right on the folder")
		}
		return nil
	})
}

// CheckFolderRight validates the stored anchor and asks both the published module
// action and the canonical Resource right. CedarAction replaces an action; one
// decision cannot enforce forbids on both. The caller retains decision evidence.
func CheckFolderRight(ctx context.Context, data api.ModuleData, request auth.Request, right auth.TrusteeRight, decide func(auth.Request) error) error {
	if data == nil {
		return &runErr{http.StatusServiceUnavailable, "folder resource is unavailable"}
	}
	if err := data.View(ctx, request.Tenant, func(sc store.Scope) error {
		resource, err := sc.Resources().Get(ctx, model.ID(request.Resource.ID))
		if err != nil {
			return err
		}
		if resource.Kind != "folder" {
			return store.ErrConflict
		}
		return nil
	}); err != nil {
		return errors.Join(&runErr{http.StatusServiceUnavailable, "folder resource is unavailable"}, err)
	}
	canonical, ok := right.Question(request.Principal, request.Tenant, request.Resource)
	if !ok || decide == nil {
		return auth.ErrRouteDenied
	}
	if err := decide(request); err != nil {
		return err
	}
	request.Route.CedarAction = string(canonical.Permission)
	return decide(request)
}

// launchFolderRights are the rights a session exercises in a folder, as the file routes
// ask them for the same actions: it reads, and writes unless the mount is read-only.
func launchFolderRights(mountMode string) []auth.TrusteeRight {
	switch mountMode {
	case mountRO:
		return []auth.TrusteeRight{auth.TrusteeRead}
	case mountNoDelete:
		return []auth.TrusteeRight{auth.TrusteeRead, auth.TrusteeWrite, auth.TrusteeCreate}
	}
	return []auth.TrusteeRight{auth.TrusteeRead, auth.TrusteeWrite, auth.TrusteeCreate, auth.TrusteeErase}
}

// askWorkspaceRights is the launcher's question about the folder a session starts in:
// the module permission and the canonical right for each action it will take there, so a
// person cannot do through a session what the file routes refuse them. An unknown ref is
// left to the launch, which refuses it. admits is the caller's own admission: the request's
// for a person, the authorizer's for a launcher restored from a queue or the engine's resume.
func (m *Module) askWorkspaceRights(ctx context.Context, tenant model.TenantID, principal auth.Principal, admits func(auth.Request) bool) func(ref string) error {
	return func(ref string) error {
		rec, err := m.loadWorkspaceRec(ctx, tenant, ref)
		if err != nil {
			var missing *runErr
			if errors.As(err, &missing) && missing.status == http.StatusNotFound {
				return nil
			}
			return err
		}
		node := auth.ResourceAttrs{Kind: "resource", ID: rec.String(model.ColID)}
		for _, right := range launchFolderRights(rec.String(colWsMountMode)) {
			permission := permWsRead
			if right.Verb != auth.VerbRead {
				permission = permWsWrite
			}
			err := CheckFolderRight(ctx, m.Data, auth.Request{Principal: principal, Tenant: tenant, Permission: permission, Resource: node}, right, func(q auth.Request) error {
				if !admits(q) {
					return forbiddenErr("you do not have this right on the folder")
				}
				return nil
			})
			if err != nil {
				return err
			}
		}
		return nil
	}
}

func (m *Module) handleCreateWorkspace(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	if m.Data == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("workspace registry not available"))
		return
	}
	var body createWorkspaceRequest
	if !decodeJSONBody(w, r, &body) {
		return
	}
	dto, err := m.createWorkspace(r.Context(), mc.Tenant, CreateWorkspaceParams{
		Backup: body.Backup,
		Name:   body.Name, RootPath: body.RootPath, MountMode: body.MountMode,
		ContainerTarget: body.ContainerTarget, AllowSubpaths: body.AllowSubpaths,
		ReadOnlyFolders: body.ReadOnlyFolders,
		MaxReadBytes:    body.MaxReadBytes, DLPMode: body.DLPMode, DataClass: body.DataClass,
		Actor: mc.Principal.Actor(), ActorKind: mc.Principal.ActorKind(),
	})
	if err != nil {
		writeRunErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, dto)
}

// handlePatchWorkspace changes a workspace's read-only host folders, DLP mode or
// default permission.
// Requires workspace administrator permission; a setting left out is not changed
// and an empty folder array removes access.
func (m *Module) handlePatchWorkspace(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	if !m.workspaceAllowed(w, r, mc, permWsAdmin, auth.TrusteeModify) {
		return
	}
	if m.Data == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("workspace registry not available"))
		return
	}
	var body struct {
		ReadOnlyFolders   *[]string `json:"read_only_folders"`
		Backup            *bool     `json:"backup"`
		DataClass         *string   `json:"data_class"`
		DLPMode           *string   `json:"dlp_mode"`
		DefaultPermission *string   `json:"default_permission"`
	}
	if !decodeJSONBody(w, r, &body) {
		return
	}
	if body.ReadOnlyFolders == nil && body.DLPMode == nil && body.DefaultPermission == nil && body.DataClass == nil && body.Backup == nil {
		writeRunErr(w, badRequest("send read_only_folders, dlp_mode, default_permission, data_class and/or backup"))
		return
	}
	mayRunUnrestricted := body.DefaultPermission != nil && m.mayRunUnrestricted(r.Context(), mc)
	dto, err := m.patchWorkspace(r.Context(), mc.Tenant, chi.URLParam(r, "ref"), body.ReadOnlyFolders, body.DLPMode, body.DefaultPermission, mayRunUnrestricted, body.DataClass, body.Backup, mc.Principal.Actor(), mc.Principal.ActorKind())
	if err != nil {
		writeRunErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

func (m *Module) handleListWorkspaces(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(r)
	for _, column := range []string{colWsRootPath, colWsState} {
		if value := r.URL.Query().Get(column); value != "" {
			q.Filters = append(q.Filters, model.Filter{Column: column, Op: model.OpEq, Value: value})
		}
	}
	out, err := m.listWorkspaces(r.Context(), mc.Tenant, q)
	if err != nil {
		writeRunErr(w, err)
		return
	}
	visible := out.Items[:0]
	for _, folder := range out.Items {
		err := m.authorizeWorkspaceRight(r.Context(), mc, permWsRead, auth.TrusteeRead, auth.ResourceAttrs{Kind: "resource", ID: folder.resourceID.String()})
		if err == nil {
			visible = append(visible, folder)
		} else {
			var denied *runErr
			if !errors.As(err, &denied) || (denied.status != http.StatusForbidden && !(denied.status == http.StatusServiceUnavailable && (errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrConflict)))) {
				writeRunErr(w, err)
				return
			}
		}
	}
	out.Items = visible
	writeJSON(w, http.StatusOK, out)
}

func (m *Module) handleGetWorkspace(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	dto, err := m.getWorkspace(r.Context(), mc.Tenant, chi.URLParam(r, "ref"))
	if err != nil {
		writeRunErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

func (m *Module) handleDeleteWorkspace(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	if !m.workspaceAllowed(w, r, mc, permWsAdmin, auth.TrusteeModify) {
		return
	}
	if err := m.deleteWorkspace(r.Context(), mc.Tenant, chi.URLParam(r, "ref"), mc.Principal.Actor(), mc.Principal.ActorKind()); err != nil {
		writeRunErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

func (m *Module) handleListFiles(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(r)
	out, err := m.listFiles(r.Context(), mc.Tenant, chi.URLParam(r, "ref"),
		r.URL.Query().Get("path"), q.Limit, q.Cursor, mc.Principal.Actor(), mc.Principal.ActorKind())
	if err != nil {
		writeRunErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (m *Module) handleStatFile(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	entry, err := m.statFile(r.Context(), mc.Tenant, chi.URLParam(r, "ref"),
		r.URL.Query().Get("path"), mc.Principal.Actor(), mc.Principal.ActorKind())
	if err != nil {
		writeRunErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, entry)
}

func (m *Module) handleReadFile(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	// ?rev=HEAD asks for the content git HEAD holds for the file instead (404 when the
	// workspace has no readable git history or HEAD has no such file).
	read := m.readFile
	switch rev := r.URL.Query().Get("rev"); rev {
	case "":
	case headRev:
		read = m.readFileAtHead
	default:
		writeRunErr(w, badRequest("rev must be HEAD"))
		return
	}
	resp, err := read(r.Context(), mc.Tenant, chi.URLParam(r, "ref"),
		r.URL.Query().Get("path"), mc.Principal.Actor(), mc.Principal.ActorKind())
	if err != nil {
		writeRunErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (m *Module) handleWriteFile(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	content, err := io.ReadAll(io.LimitReader(r.Body, maxUploadBytes+1))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("could not read request body"))
		return
	}
	if int64(len(content)) > maxUploadBytes {
		writeJSON(w, http.StatusRequestEntityTooLarge, errorBody("upload exceeds the hard size ceiling"))
		return
	}
	resp, err := m.writeFile(r.Context(), mc.Tenant, chi.URLParam(r, "ref"),
		r.URL.Query().Get("path"), content, mc.Principal.Actor(), mc.Principal.ActorKind())
	if err != nil {
		writeRunErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (m *Module) handleMkdir(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	entry, err := m.mkdir(r.Context(), mc.Tenant, chi.URLParam(r, "ref"),
		r.URL.Query().Get("path"), mc.Principal.Actor(), mc.Principal.ActorKind())
	if err != nil {
		writeRunErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, entry)
}

func (m *Module) handleMoveFile(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var body moveRequest
	if !decodeJSONBody(w, r, &body) {
		return
	}
	if err := m.moveFile(r.Context(), mc.Tenant, chi.URLParam(r, "ref"), body.From, body.To,
		mc.Principal.Actor(), mc.Principal.ActorKind()); err != nil {
		writeRunErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"moved": true})
}

func (m *Module) handleDeleteFile(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	recursive := r.URL.Query().Get("recursive") == "true"
	if err := m.deleteFile(r.Context(), mc.Tenant, chi.URLParam(r, "ref"),
		r.URL.Query().Get("path"), recursive, mc.Principal.Actor(), mc.Principal.ActorKind()); err != nil {
		writeRunErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}
