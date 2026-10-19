//go:build !enterprise || !addon_ids

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"net/http"

	"github.com/olivaresai/olivares/core/api/scim"
)

// The inbound SCIM 2.0 server (users, group push and the SET event receiver) is a
// Business capability. Community mounts every route (server.go) behind the same
// admission as the Business server (scimRoute: a tenant-bound token that holds
// user:read or user:write) and answers 501 in the SCIM error shape, naming Business,
// so a published URL never turns into a 404 and nothing is read or written. The
// Business build supplies the same methods with the implementation.

const scimBusinessDetail = "The inbound SCIM server (users, groups and event push) requires Business."

func scimRequiresBusiness(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	writeSCIMError(w, scim.NewError(http.StatusNotImplemented, "", scimBusinessDetail))
}

func (s *Server) scimListUsers(w http.ResponseWriter, _ *http.Request, _ ModuleContext) {
	scimRequiresBusiness(w)
}

func (s *Server) scimCreateUser(w http.ResponseWriter, _ *http.Request, _ ModuleContext) {
	scimRequiresBusiness(w)
}

func (s *Server) scimGetUser(w http.ResponseWriter, _ *http.Request, _ ModuleContext) {
	scimRequiresBusiness(w)
}

func (s *Server) scimReplaceUser(w http.ResponseWriter, _ *http.Request, _ ModuleContext) {
	scimRequiresBusiness(w)
}

func (s *Server) scimPatchUser(w http.ResponseWriter, _ *http.Request, _ ModuleContext) {
	scimRequiresBusiness(w)
}

func (s *Server) scimDeleteUser(w http.ResponseWriter, _ *http.Request, _ ModuleContext) {
	scimRequiresBusiness(w)
}

func (s *Server) scimListGroups(w http.ResponseWriter, _ *http.Request, _ ModuleContext) {
	scimRequiresBusiness(w)
}

func (s *Server) scimCreateGroup(w http.ResponseWriter, _ *http.Request, _ ModuleContext) {
	scimRequiresBusiness(w)
}

func (s *Server) scimGetGroup(w http.ResponseWriter, _ *http.Request, _ ModuleContext) {
	scimRequiresBusiness(w)
}

func (s *Server) scimReplaceGroup(w http.ResponseWriter, _ *http.Request, _ ModuleContext) {
	scimRequiresBusiness(w)
}

func (s *Server) scimPatchGroup(w http.ResponseWriter, _ *http.Request, _ ModuleContext) {
	scimRequiresBusiness(w)
}

func (s *Server) scimDeleteGroup(w http.ResponseWriter, _ *http.Request, _ ModuleContext) {
	scimRequiresBusiness(w)
}

func (s *Server) scimSPConfig(w http.ResponseWriter, _ *http.Request, _ ModuleContext) {
	scimRequiresBusiness(w)
}

func (s *Server) scimResourceTypes(w http.ResponseWriter, _ *http.Request, _ ModuleContext) {
	scimRequiresBusiness(w)
}

func (s *Server) scimResourceType(w http.ResponseWriter, _ *http.Request, _ ModuleContext) {
	scimRequiresBusiness(w)
}

func (s *Server) scimSchemas(w http.ResponseWriter, _ *http.Request, _ ModuleContext) {
	scimRequiresBusiness(w)
}

func (s *Server) scimSchema(w http.ResponseWriter, _ *http.Request, _ ModuleContext) {
	scimRequiresBusiness(w)
}

func (s *Server) scimReceiveEvents(w http.ResponseWriter, _ *http.Request, _ ModuleContext) {
	scimRequiresBusiness(w)
}
