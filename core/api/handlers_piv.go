// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"crypto/x509"
	"net/http"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// PIV/CAC route — the declared seam plus the elevation leg:
//
//	GET  /v1/auth/piv/status  -> {presented, subject?, issuer?, mapped_role?, ocsp?, not_after?}
//	POST /v1/auth/piv/elevate -> {ok, aal}
//
// The client certificate comes from the TLS handshake (the composition root
// configures the listener with VerifyClientCertIfGiven against the PIV CA), so
// the route requires DIRECT TLS at this listener — there is deliberately no
// header-borne (XFCC) certificate path: a forwarded header is an unauthenticated
// claim unless the proxy hop is itself mutually authenticated, which this
// engine cannot verify. Unconfigured -> 501 piv_not_configured (the panel
// renders the honest pending seam).

// peerCertificates returns the mTLS client chain, if any.
func peerCertificates(r *http.Request) []*x509.Certificate {
	if r.TLS == nil {
		return nil
	}
	return r.TLS.PeerCertificates
}

// handlePIVStatus reports the verifier's view of the presented certificate.
// It never elevates, and the read is self-audited (docs/SECURITY-HARDENING.md) when a
// certificate was actually presented — a coarse ledger event recording that
// this principal probed a credential's state (never key material); a certless
// probe of an armed deployment is just the empty state, not a ledger line.
func (s *Server) handlePIVStatus(w http.ResponseWriter, r *http.Request) {
	p, ok := principalFrom(r.Context())
	if !ok {
		s.writeError(w, r, auth.ErrUnauthenticated)
		return
	}
	if s.piv == nil || s.piv.Roots == nil {
		s.writeError(w, r, auth.ErrPIVNotConfigured)
		return
	}
	st := s.piv.Status(r.Context(), peerCertificates(r), s.clock.Now().Time())
	if st.Presented {
		if err := s.st.AuthMutate(r.Context(), func(as store.AuthScope) error {
			_, err := as.Audit().Append(r.Context(), model.AuditDraft{
				Actor: p.Actor(), ActorKind: p.ActorKind(),
				Action: "auth.piv.status", TargetKind: "core.auth_session", TargetID: p.CredID,
				Meta: map[string]any{"ocsp": st.OCSP, "mapped_role": st.MappedRole},
			})
			return err
		}); err != nil {
			s.writeError(w, r, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, st)
}

// handlePIVElevate verifies the presented certificate (chain, OCSP, user
// binding) and elevates the calling session to AAL3 (method "piv").
func (s *Server) handlePIVElevate(w http.ResponseWriter, r *http.Request) {
	p, ok := s.sessionPrincipal(w, r)
	if !ok {
		return
	}
	sess, _, err := s.authr.ElevatePIVSession(r.Context(), p, s.piv, peerCertificates(r))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "aal": sess.AAL})
}
