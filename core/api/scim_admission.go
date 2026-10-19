// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"net/http"

	"github.com/olivaresai/olivares/core/api/scim"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// The inbound SCIM 2.0 server is a Business capability, but its URLs and its
// admission are part of every build: a Community server authenticates the tenant-bound
// token like any other and then answers 501 (handlers_scim_noenterprise.go), so a
// published URL never turns into a 404. SCIM errors carry the SCIM Error envelope
// (status as a STRING), not the core API envelope, so an identity provider parses them.

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

func writeSCIM(w http.ResponseWriter, status int, v any) {
	WriteJSON(w, status, v, scim.ContentType)
}

func writeSCIMError(w http.ResponseWriter, e scim.Error) {
	writeSCIM(w, e.HTTPStatus(), e)
}
