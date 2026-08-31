// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
)

// Permission tiers for the WORKSPACE / governed file surface. Read covers
// list/stat/read; write covers write/mkdir/move/delete (the file edits a session
// makes); admin covers register/deregister (granting/revoking filesystem reach).
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
	reg.Handle("POST", "/workspaces", permWsAdmin, m.handleCreateWorkspace)
	reg.Handle("GET", "/workspaces", permWsRead, m.handleListWorkspaces)
	reg.Handle("GET", "/workspaces/{ref}", permWsRead, m.handleGetWorkspace)
	reg.Handle("DELETE", "/workspaces/{ref}", permWsAdmin, m.handleDeleteWorkspace)
	reg.Handle("GET", "/workspaces/{ref}/files", permWsRead, m.handleListFiles)
	reg.Handle("GET", "/workspaces/{ref}/files/stat", permWsRead, m.handleStatFile)
	reg.Handle("GET", "/workspaces/{ref}/files/raw", permWsRead, m.handleReadFile)
	reg.Handle("PUT", "/workspaces/{ref}/files/raw", permWsWrite, m.handleWriteFile)
	reg.Handle("POST", "/workspaces/{ref}/files/dir", permWsWrite, m.handleMkdir)
	reg.Handle("POST", "/workspaces/{ref}/files/move", permWsWrite, m.handleMoveFile)
	reg.Handle("DELETE", "/workspaces/{ref}/files", permWsWrite, m.handleDeleteFile)
}

func (m *Module) handleCreateWorkspace(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	if m.data == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("workspace registry not available"))
		return
	}
	var body createWorkspaceRequest
	if !decodeJSONBody(w, r, &body) {
		return
	}
	dto, err := m.createWorkspace(r.Context(), mc.Tenant, CreateWorkspaceParams{
		Name: body.Name, RootPath: body.RootPath, MountMode: body.MountMode,
		ContainerTarget: body.ContainerTarget, AllowSubpaths: body.AllowSubpaths,
		MaxReadBytes: body.MaxReadBytes, DLPMode: body.DLPMode,
		Actor: mc.Principal.Actor(), ActorKind: mc.Principal.ActorKind(),
	})
	if err != nil {
		writeRunErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, dto)
}

func (m *Module) handleListWorkspaces(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	out, err := m.listWorkspaces(r.Context(), mc.Tenant, listQuery(r))
	if err != nil {
		writeStoreError(w, err)
		return
	}
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
	resp, err := m.readFile(r.Context(), mc.Tenant, chi.URLParam(r, "ref"),
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
