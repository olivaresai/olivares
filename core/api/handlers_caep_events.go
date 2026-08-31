// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	caepwire "github.com/olivaresai/olivares/core/api/caep"
	"github.com/olivaresai/olivares/core/api/scim"
	"github.com/olivaresai/olivares/core/auth"
)

// caepReceiveEvents accepts a SET (application/secevent+jwt) for CAEP/RISC
// event processing. Bearer-authed with a tenant-bound API token. Success is
// 202 Accepted (RFC 8935 §2.3); problems return 400 with a machine-readable
// {err, description} body (RFC 8935 §2.4).
//
// SSF subject identifier formats (email, iss_sub, opaque) are resolved by
// re-decoding the sub_id field as caep.SubjectIdentifier so the email and
// iss_sub-specific fields are not lost via the scim.SubjectID mapping.
func (s *Server) caepReceiveEvents(w http.ResponseWriter, r *http.Request) {
	// CAEP events are access-affecting, so they take user:write —
	// the same permission as SCIM DELETE / deprovisioning.
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

	// Pick the first CAEP/RISC access-affecting event. Unknown or non-access
	// events are acknowledged without action (RFC 8935 §2.4).
	action := auth.CAEPIgnore
	var eventPayload []byte
	for _, uri := range set.EventURIs() {
		if a := caepActionFor(uri); a != auth.CAEPIgnore {
			action = a
			if raw, ok := set.Events[uri]; ok {
				eventPayload = raw
			}
			break
		}
	}

	// Resolve the SSF subject identifier. scim.SubjectID does not carry the
	// "email" or "sub" fields used by CAEP/SSF subject formats, so we re-decode
	// sub_id as caep.SubjectIdentifier for the CAEP-specific formats; fall back
	// to scim.SubjectID for the "scim" format (SCIM resource path) and unknowns.
	var subjectEmail, subjectExternalID, subjectUserID string
	var rawClaims struct {
		SubjectRaw json.RawMessage `json:"sub_id"`
	}
	_ = json.Unmarshal(payload, &rawClaims)
	if len(rawClaims.SubjectRaw) > 0 {
		var caepSubj caepwire.SubjectIdentifier
		if err := json.Unmarshal(rawClaims.SubjectRaw, &caepSubj); err == nil {
			switch caepSubj.Format {
			case "email":
				subjectEmail = caepSubj.ResolveSubjectEmail()
			case "iss_sub":
				_, subjectExternalID = caepSubj.ResolveSubjectIssSub()
			case "opaque":
				subjectUserID = caepSubj.ID
			default:
				// "scim" format or unknown: use scim.SubjectID resource path
				if set.SubjectID != nil {
					_, subjectUserID = set.SubjectID.ResourcePath()
					subjectExternalID = set.SubjectID.ExternalID
				}
			}
		} else if set.SubjectID != nil {
			// Sub-decode failed; fall back to whatever scim.SubjectID captured.
			_, subjectUserID = set.SubjectID.ResourcePath()
			subjectExternalID = set.SubjectID.ExternalID
		}
	}

	env := auth.CAEPEventEnvelope{
		SETEnvelope: auth.SETEnvelope{
			Alg: hdr.Alg, Kid: hdr.Kid, SigningInput: signingInput,
			Signature: signature, Issuer: set.Issuer,
			Audience: []string(set.Audience), JTI: set.JTI, IssuedAt: set.IssuedAt,
		},
		Action:            action,
		SubjectEmail:      subjectEmail,
		SubjectExternalID: subjectExternalID,
		SubjectUserID:     subjectUserID,
		EventPayload:      eventPayload,
	}
	if _, err := s.authr.CAEPReceiveEvent(r.Context(), p, tenant, env); err != nil {
		switch {
		case errors.Is(err, auth.ErrCAEPSetDisabled):
			writeSETError(w, "access_denied", "CAEP/SSF receiver is not configured for this tenant")
		case errors.Is(err, auth.ErrSCIMSetSignature):
			writeSETError(w, "invalid_key", "SET signature verification failed")
		case errors.Is(err, auth.ErrSCIMSetIssuer):
			writeSETError(w, "invalid_issuer", "SET issuer or audience did not match the configured publisher")
		case errors.Is(err, auth.ErrSETJTIDuplicate):
			writeSETError(w, "duplicate_event", "this SET has already been processed")
		case errors.Is(err, auth.ErrSCIMSetSubject):
			writeSETError(w, "invalid_request", "SET subject is not a member of this tenant")
		default:
			writeJSON(w, http.StatusInternalServerError, map[string]string{
				"err": "server_error", "description": "internal error processing the event",
			})
			s.log.Error("caep: receive event", "err", err, "path", r.URL.Path, "request_id", requestID(r.Context()))
		}
		return
	}
	// RFC 8935 §2.3: a SET that was successfully processed (including one that
	// was acknowledged but required no action) returns 202 with no body.
	w.WriteHeader(http.StatusAccepted)
}

// caepActionFor maps a CAEP/RISC event URI to the core/auth action type,
// bridging the wire taxonomy (core/api/caep) to the revocation layer
// (core/auth) so neither package depends on the other's vocabulary.
func caepActionFor(uri string) auth.CAEPEventAction {
	switch caepwire.ActionForCAEPEvent(uri) {
	case caepwire.ActionSessionRevoke:
		return auth.CAEPSessionRevoke
	case caepwire.ActionTokenRevoke:
		return auth.CAEPTokenRevoke
	case caepwire.ActionCredentialRevoke:
		return auth.CAEPCredentialRevoke
	case caepwire.ActionDeviceNonCompliant:
		return auth.CAEPDeviceNonCompliant
	case caepwire.ActionAccountDisable:
		return auth.CAEPAccountDisable
	case caepwire.ActionCredentialCompromise:
		return auth.CAEPCredentialCompromise
	default:
		return auth.CAEPIgnore
	}
}
