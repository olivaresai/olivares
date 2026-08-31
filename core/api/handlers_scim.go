// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api/scim"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// SCIM 2.0 inbound service provider (RFC 7643/7644). It is mounted under
// /v1/scim/v2 and authenticates with a normal tenant-bound admin/owner API token
// (no separate bearer scheme): the bound token's tenant IS the SCIM target
// tenant, so a SCIM connection provisions exactly one tenant. SCIM errors carry
// the SCIM Error envelope (status as a STRING), not the core API envelope, so an
// IdP parses them correctly. The credential-touching work lives in
// auth.Authenticator.SCIM* (users/tokens are reachable only from core).

// scimAuthz authenticates and authorizes a SCIM request, returning the principal,
// the bound tenant, and a SCIM-shaped error on failure (so the caller writes a
// SCIM error body, never the core envelope).
func (s *Server) scimAuthz(r *http.Request, perm auth.Permission) (auth.Principal, model.TenantID, *scim.Error) {
	p, ok := principalFrom(r.Context())
	if !ok {
		e := scim.NewError(http.StatusUnauthorized, "", "authentication required")
		return auth.Principal{}, "", &e
	}
	tenant, err := s.resolveTenant(r, p)
	if err != nil {
		e := scim.NewError(http.StatusForbidden, "", "could not resolve a single bound tenant for this token")
		return auth.Principal{}, "", &e
	}
	if dec := s.authz.Authorize(r.Context(), auth.Request{Principal: p, Permission: perm, Tenant: tenant, Resource: auth.ResourceFor(perm)}); !dec.Allow {
		e := scim.NewError(http.StatusForbidden, "", "the SCIM token lacks the required permission")
		return auth.Principal{}, "", &e
	}
	return p, tenant, nil
}

// scimUsersURL returns the absolute URL of the Users collection for meta.location.
func scimUsersURL(r *http.Request) string {
	return scimBaseURL(r) + "/Users"
}

func scimBaseURL(r *http.Request) string {
	return schemeHost(r) + "/v1/scim/v2"
}

func writeSCIM(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", scim.ContentType)
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

func writeSCIMError(w http.ResponseWriter, e scim.Error) {
	writeSCIM(w, e.HTTPStatus(), e)
}

// decodeSCIMBody decodes a SCIM JSON body leniently (IdPs send many attributes
// the provider does not model, so unknown fields are NOT rejected), under the
// body-size cap.
func decodeSCIMBody(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	// ONE JSON DOCUMENT. Decode reads the first value and stops, so a body of two
	// concatenated objects would decode the first and silently discard the rest — on a
	// SCIM provisioning route, that is an identity mutation taken from a body nobody
	// agreed on. See scripts/check-json-decoders.sh.
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(v); err != nil {
		return err
	}
	if dec.More() {
		return errors.New("request body carries more than one JSON document")
	}
	return nil
}

// scimUserAttr returns a getter over a user's filterable attributes for in-memory
// filter evaluation.
func scimUserAttr(u model.User) func(string) (string, bool) {
	return func(attr string) (string, bool) {
		switch attr {
		case "username":
			return u.Email, true
		case "externalid":
			return u.ExternalID, u.ExternalID != ""
		case "displayname":
			return u.DisplayName, u.DisplayName != ""
		case "active":
			if u.Status == model.StatusActive {
				return "true", true
			}
			return "false", true
		case "id":
			return u.ID.String(), true
		default:
			return "", false
		}
	}
}

// scimInput maps a decoded SCIM InboundUser to the engine's SCIMUserInput,
// carrying every modeled attribute (core + enterprise extension + agent extension)
// so create, replace and patch all write the same set. Agent extension attributes
// are carried defensively — present when the IdP sends them, empty otherwise,
// never mandatory (draft-abbey-scim-agent-extension-00).
func scimInput(in scim.InboundUser) auth.SCIMUserInput {
	return auth.SCIMUserInput{
		UserName: in.UserName, ExternalID: in.ExternalID, DisplayName: in.DisplayName, Active: in.Active,
		EmployeeNumber: in.EmployeeNumber, Department: in.Department, Manager: in.Manager,
		AgentKind: in.AgentKind, AgentSponsorRef: in.AgentSponsorRef, AgentDelegation: in.AgentDelegation,
	}
}

// --- Users -------------------------------------------------------------------

func (s *Server) scimListUsers(w http.ResponseWriter, r *http.Request) {
	_, tenant, aerr := s.scimAuthz(r, "user:read")
	if aerr != nil {
		writeSCIMError(w, *aerr)
		return
	}
	page := scim.ParsePage(r.URL.Query().Get("startIndex"), r.URL.Query().Get("count"), r.URL.Query().Has("count"))
	filterStr := strings.TrimSpace(r.URL.Query().Get("filter"))

	var matched []model.User
	if filterStr != "" {
		f, ferr := scim.ParseFilter(filterStr)
		if ferr != nil {
			writeSCIMError(w, scim.NewError(http.StatusBadRequest, scim.TypeInvalidFilter, ferr.Error()))
			return
		}
		// Fast path: `userName eq` / `externalId eq` use the indexed lookup.
		if attr, val, ok := scim.SingleEq(f); ok && (attr == "username" || attr == "externalid") {
			col := "email"
			lookup := val
			if attr == "externalid" {
				col = "external_id"
			} else {
				lookup = strings.ToLower(strings.TrimSpace(val))
			}
			u, found, err := s.authr.SCIMFindMember(r.Context(), tenant, col, lookup)
			if err != nil {
				s.scimInternal(w, r, err)
				return
			}
			if found {
				matched = []model.User{u}
			}
		} else {
			all, err := s.authr.SCIMListMembers(r.Context(), tenant)
			if err != nil {
				s.scimInternal(w, r, err)
				return
			}
			for _, u := range all {
				if f.Match(scimUserAttr(u)) {
					matched = append(matched, u)
				}
			}
		}
	} else {
		all, err := s.authr.SCIMListMembers(r.Context(), tenant)
		if err != nil {
			s.scimInternal(w, r, err)
			return
		}
		matched = all
	}

	usersURL := scimUsersURL(r)
	pageItems := scim.Slice(matched, page)
	resources := make([]map[string]any, 0, len(pageItems))
	for _, u := range pageItems {
		resources = append(resources, scim.EncodeUser(u, usersURL))
	}
	writeSCIM(w, http.StatusOK, scim.ListResponse(len(matched), page, resources))
}

func (s *Server) scimCreateUser(w http.ResponseWriter, r *http.Request) {
	p, tenant, aerr := s.scimAuthz(r, "user:write")
	if aerr != nil {
		writeSCIMError(w, *aerr)
		return
	}
	var body scim.UserBodyType
	if err := decodeSCIMBody(w, r, &body); err != nil {
		writeSCIMError(w, scim.NewError(http.StatusBadRequest, scim.TypeInvalidSyntax, "invalid SCIM body"))
		return
	}
	in := scim.DecodeUser(body)
	if in.UserName == "" {
		writeSCIMError(w, scim.NewError(http.StatusBadRequest, scim.TypeInvalidValue, "userName is required"))
		return
	}
	// POST creates: a userName already a member of this tenant is a 409 uniqueness.
	if _, found, err := s.authr.SCIMFindMember(r.Context(), tenant, "email", strings.ToLower(in.UserName)); err != nil {
		s.scimInternal(w, r, err)
		return
	} else if found {
		writeSCIMError(w, scim.NewError(http.StatusConflict, scim.TypeUniqueness, "a user with this userName already exists"))
		return
	}
	u, _, err := s.authr.SCIMProvisionUser(r.Context(), p, tenant, scimInput(in))
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeSCIMError(w, scim.NewError(http.StatusConflict, scim.TypeUniqueness, "a user with this userName already exists"))
			return
		}
		if errors.Is(err, auth.ErrInvalidScimUser) {
			writeSCIMError(w, scim.NewError(http.StatusBadRequest, scim.TypeInvalidValue, "userName is required"))
			return
		}
		s.scimInternal(w, r, err)
		return
	}
	usersURL := scimUsersURL(r)
	w.Header().Set("Location", usersURL+"/"+u.ID.String())
	writeSCIM(w, http.StatusCreated, scim.EncodeUser(u, usersURL))
}

func (s *Server) scimGetUser(w http.ResponseWriter, r *http.Request) {
	_, tenant, aerr := s.scimAuthz(r, "user:read")
	if aerr != nil {
		writeSCIMError(w, *aerr)
		return
	}
	u, err := s.authr.SCIMGetMember(r.Context(), tenant, model.ID(chi.URLParam(r, "id")))
	if err != nil {
		s.scimNotFoundOr(w, r, err)
		return
	}
	writeSCIM(w, http.StatusOK, scim.EncodeUser(u, scimUsersURL(r)))
}

func (s *Server) scimReplaceUser(w http.ResponseWriter, r *http.Request) {
	p, tenant, aerr := s.scimAuthz(r, "user:write")
	if aerr != nil {
		writeSCIMError(w, *aerr)
		return
	}
	id := model.ID(chi.URLParam(r, "id"))
	var body scim.UserBodyType
	if err := decodeSCIMBody(w, r, &body); err != nil {
		writeSCIMError(w, scim.NewError(http.StatusBadRequest, scim.TypeInvalidSyntax, "invalid SCIM body"))
		return
	}
	in := scim.DecodeUser(body)
	u, err := s.authr.SCIMUpdateUser(r.Context(), p, tenant, id, scimInput(in))
	if err != nil {
		s.scimNotFoundOr(w, r, err)
		return
	}
	writeSCIM(w, http.StatusOK, scim.EncodeUser(u, scimUsersURL(r)))
}

func (s *Server) scimPatchUser(w http.ResponseWriter, r *http.Request) {
	p, tenant, aerr := s.scimAuthz(r, "user:write")
	if aerr != nil {
		writeSCIMError(w, *aerr)
		return
	}
	id := model.ID(chi.URLParam(r, "id"))
	cur, err := s.authr.SCIMGetMember(r.Context(), tenant, id)
	if err != nil {
		s.scimNotFoundOr(w, r, err)
		return
	}
	var body scim.PatchBody
	if err := decodeSCIMBody(w, r, &body); err != nil {
		writeSCIMError(w, scim.NewError(http.StatusBadRequest, scim.TypeInvalidSyntax, "invalid PatchOp body"))
		return
	}
	current := scim.InboundUser{
		UserName: cur.Email, ExternalID: cur.ExternalID, DisplayName: cur.DisplayName,
		Active:         cur.Status == model.StatusActive,
		EmployeeNumber: cur.EmployeeNumber, Department: cur.Department, Manager: cur.Manager,
	}
	patched, perr := scim.ApplyPatch(current, body)
	if perr != nil {
		st := scim.TypeInvalidSyntax
		if errors.Is(perr, scim.ErrNoTarget) {
			st = scim.TypeNoTarget
		}
		writeSCIMError(w, scim.NewError(http.StatusBadRequest, st, perr.Error()))
		return
	}
	u, err := s.authr.SCIMUpdateUser(r.Context(), p, tenant, id, scimInput(patched))
	if err != nil {
		s.scimNotFoundOr(w, r, err)
		return
	}
	writeSCIM(w, http.StatusOK, scim.EncodeUser(u, scimUsersURL(r)))
}

func (s *Server) scimDeleteUser(w http.ResponseWriter, r *http.Request) {
	p, tenant, aerr := s.scimAuthz(r, "user:write")
	if aerr != nil {
		writeSCIMError(w, *aerr)
		return
	}
	id := model.ID(chi.URLParam(r, "id"))
	if _, err := s.authr.SCIMGetMember(r.Context(), tenant, id); err != nil {
		s.scimNotFoundOr(w, r, err)
		return
	}
	if err := s.authr.SCIMDeprovisionUser(r.Context(), p, tenant, id); err != nil {
		s.scimInternal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- Discovery ---------------------------------------------------------------

func (s *Server) scimSPConfig(w http.ResponseWriter, r *http.Request) {
	if _, _, aerr := s.scimAuthz(r, "user:read"); aerr != nil {
		writeSCIMError(w, *aerr)
		return
	}
	writeSCIM(w, http.StatusOK, scim.ServiceProviderConfig(scimBaseURL(r)+"/ServiceProviderConfig"))
}

func (s *Server) scimResourceTypes(w http.ResponseWriter, r *http.Request) {
	if _, _, aerr := s.scimAuthz(r, "user:read"); aerr != nil {
		writeSCIMError(w, *aerr)
		return
	}
	types := scim.ResourceTypes(scimBaseURL(r))
	page := scim.Page{StartIndex: 1, Count: scim.MaxPageSize}
	writeSCIM(w, http.StatusOK, scim.ListResponse(len(types), page, types))
}

func (s *Server) scimResourceType(w http.ResponseWriter, r *http.Request) {
	if _, _, aerr := s.scimAuthz(r, "user:read"); aerr != nil {
		writeSCIMError(w, *aerr)
		return
	}
	if rt, ok := scim.ResourceTypeByID(scimBaseURL(r), chi.URLParam(r, "type")); ok {
		writeSCIM(w, http.StatusOK, rt)
		return
	}
	writeSCIMError(w, scim.NewError(http.StatusNotFound, "", "unknown resource type"))
}

func (s *Server) scimSchemas(w http.ResponseWriter, r *http.Request) {
	if _, _, aerr := s.scimAuthz(r, "user:read"); aerr != nil {
		writeSCIMError(w, *aerr)
		return
	}
	schemas := scim.Schemas(scimBaseURL(r))
	page := scim.Page{StartIndex: 1, Count: scim.MaxPageSize}
	writeSCIM(w, http.StatusOK, scim.ListResponse(len(schemas), page, schemas))
}

func (s *Server) scimSchema(w http.ResponseWriter, r *http.Request) {
	if _, _, aerr := s.scimAuthz(r, "user:read"); aerr != nil {
		writeSCIMError(w, *aerr)
		return
	}
	if sc, ok := scim.SchemaByID(scimBaseURL(r), chi.URLParam(r, "urn")); ok {
		writeSCIM(w, http.StatusOK, sc)
		return
	}
	writeSCIMError(w, scim.NewError(http.StatusNotFound, "", "unknown schema"))
}

// scimNotFoundOr writes a SCIM 404 for a not-found/other-tenant member, else an
// internal error.
func (s *Server) scimNotFoundOr(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, store.ErrNotFound) {
		writeSCIMError(w, scim.NewError(http.StatusNotFound, "", "resource not found"))
		return
	}
	s.scimInternal(w, r, err)
}

// scimInternal logs and writes a SCIM 500 without leaking internals.
func (s *Server) scimInternal(w http.ResponseWriter, r *http.Request, err error) {
	s.log.Error("api: scim request failed", "err", err, "path", r.URL.Path, "request_id", requestID(r.Context()))
	writeSCIMError(w, scim.NewError(http.StatusInternalServerError, "", "internal error"))
}
