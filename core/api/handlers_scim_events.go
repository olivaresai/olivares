// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/olivaresai/olivares/core/api/scim"
	"github.com/olivaresai/olivares/core/auth"
)

// SCIM Security Event Token receiver (RFC 9967 events over the RFC 8935 SET Push
// Delivery profile), the API glue of IDN-11. It is mounted at
// POST /v1/scim/v2/Events and bearer-authed with the same tenant-bound SCIM token
// as the rest of the provider (RFC 8935 §2.1, transport authentication). The
// publisher's SET signature is verified in core/auth against the tenant's
// configured publisher key; an access-affecting event (prov:delete /
// prov:deactivate / prov:activate) drives the same credential cut the polled
// lifecycle performs — turning offboarding-on-next-poll into offboarding-on-event.

// scimReceiveEvents accepts a SET (application/secevent+jwt), verifies it, and
// applies its access effect. Success is 202 Accepted (RFC 8935 §2.3); a problem
// with the SET returns 400 with the {err, description} body (RFC 8935 §2.4).
func (s *Server) scimReceiveEvents(w http.ResponseWriter, r *http.Request) {
	// Deprovisioning is a destructive lifecycle action, so it takes user:write —
	// the same permission as SCIM DELETE.
	p, tenant, aerr := s.scimAuthz(r, "user:write")
	if aerr != nil {
		writeSCIMError(w, *aerr)
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		writeSETError(w, "invalid_request", "could not read the SET body")
		return
	}
	hdr, payload, signingInput, signature, err := scim.ParseCompactJWS(body)
	if err != nil {
		writeSETError(w, "invalid_request", "malformed Security Event Token")
		return
	}
	set, err := scim.DecodeSET(payload)
	if err != nil {
		writeSETError(w, "invalid_request", "could not decode the SET claims")
		return
	}

	// Pick the first access-affecting event; non-access events (create/patch/put/
	// feed/misc) are acknowledged without action (RFC 8935 §2.4 — a receiver need
	// not act on every event class).
	action := auth.SCIMSetIgnore
	for _, uri := range set.EventURIs() {
		if a := setActionFor(uri); a != auth.SCIMSetIgnore {
			action = a
			break
		}
	}

	var subjectID, subjectExternalID string
	if set.SubjectID != nil {
		var resourceType string
		resourceType, subjectID = set.SubjectID.ResourcePath()
		subjectExternalID = set.SubjectID.ExternalID
		// The receiver acts on USER subjects only (the credential-cut actions have
		// no group counterpart). A Groups/{id} — or any other non-Users — subject
		// must never be resolved AS a user id (group ids are real resources
		// now): acknowledge without action rather than misroute.
		if resourceType != "" && !strings.EqualFold(resourceType, "Users") {
			action = auth.SCIMSetIgnore
		}
	}

	env := auth.SCIMEventEnvelope{
		Alg: hdr.Alg, Kid: hdr.Kid, SigningInput: signingInput, Signature: signature,
		Issuer: set.Issuer, Audience: []string(set.Audience), JTI: set.JTI, IssuedAt: set.IssuedAt,
		SubjectID: subjectID, SubjectExternalID: subjectExternalID, Action: action,
	}
	if _, err := s.authr.SCIMReceiveEvent(r.Context(), p, tenant, env); err != nil {
		switch {
		case errors.Is(err, auth.ErrSCIMSetDisabled):
			writeSETError(w, "access_denied", "SET receiver is not configured for this tenant")
		case errors.Is(err, auth.ErrSCIMSetSignature):
			writeSETError(w, "invalid_key", "SET signature verification failed")
		case errors.Is(err, auth.ErrSCIMSetIssuer):
			writeSETError(w, "invalid_issuer", "SET issuer or audience did not match the configured publisher")
		case errors.Is(err, auth.ErrSCIMSetSubject):
			writeSETError(w, "invalid_request", "SET subject is not a member of this tenant")
		default:
			s.scimInternal(w, r, err)
		}
		return
	}
	// RFC 8935 §2.3: a SET that was successfully processed (including one that was
	// acknowledged but required no action) returns 202 with no body.
	w.WriteHeader(http.StatusAccepted)
}

// setActionFor maps a SCIM SET event URI to the core/auth access action,
// bridging the wire taxonomy (core/api/scim) to the credential-cut layer
// (core/auth) so neither package depends on the other's vocabulary.
func setActionFor(uri string) auth.SCIMSetAction {
	switch scim.ActionForEvent(uri) {
	case scim.ActionDeprovision:
		return auth.SCIMSetDeprovision
	case scim.ActionDisable:
		return auth.SCIMSetDisable
	case scim.ActionActivate:
		return auth.SCIMSetActivate
	default:
		return auth.SCIMSetIgnore
	}
}

// writeSETError writes the RFC 8935 §2.4 SET delivery error response: HTTP 400
// with a machine-readable err code and a human description.
func writeSETError(w http.ResponseWriter, errCode, description string) {
	writeSCIM(w, http.StatusBadRequest, map[string]string{"err": errCode, "description": description})
}
