// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package spiffe

// SVID-based OAuth client authentication — the client half of
// draft-ietf-oauth-spiffe-client-auth-01 (OAuth WG, 2026-03-02, expires
// 2026-09-03): a SPIFFE workload authenticates to an OAuth 2.0 token endpoint
// with its SVID instead of a client secret, which is how the plane's SPIFFE
// roster federates into OAuth flows with ZERO static client credentials — the
// Five Eyes "ephemeral credentials" posture (Careful adoption of agentic AI
// services, 2026-05-01) applied to the plane's own egress.
//
// DRAFT STATUS — design-toward, no conformance claim (docs/SECURITY-HARDENING.md): the draft is
// WG-adopted but pre-RFC. This file implements the two methods an AS MUST choose
// from and a client MAY support:
//
//   - spiffe_jwt  (§4 of the draft): the JWT-SVID travels as the RFC 7523 §2.2
//     client_assertion with the dedicated assertion type
//     urn:ietf:params:oauth:client-assertion-type:jwt-spiffe. The draft requires
//     sub == the client's SPIFFE ID and aud to contain ONLY the authorization
//     server's issuer identifier — so the fetch here is audience-bound to the
//     issuer with no extra audiences, mirroring FetchAnthropicAssertion's shape.
//   - spiffe_x509 (§5): the X.509-SVID is the TLS client certificate per
//     RFC 8705 (PKI method), with client_id = the SVID's SPIFFE ID. The mTLS
//     transport half already exists (Workload.MTLSClientConfig, deny-by-default
//     authorizer); X509ClientID supplies the matching client_id.
//
// spiffe_wit (§6, WIT-SVID + OAuth-Client-Attestation PoP) is deliberately NOT
// implemented: the product has no WIMSE WIT issuance path (the
// EmergentIdentitySeam is the declared slot, deny-closed) and pretending a
// draft-of-a-draft is implemented would violate the honesty rule.
//
// Like every credential primitive here, the produced assertion is a bearer
// credential minted fresh per call: present it transiently, never log or
// persist it (docs/SECURITY-HARDENING.md). Opt-in by construction — nothing calls this path
// unless the host wires it explicitly.

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/spiffe/go-spiffe/v2/svid/jwtsvid"
)

// ClientAssertionTypeJWTSVID is the client_assertion_type for the spiffe_jwt
// method: draft-ietf-oauth-spiffe-client-auth-01 §4 — "MUST be set to
// urn:ietf:params:oauth:client-assertion-type:jwt-spiffe".
const ClientAssertionTypeJWTSVID = "urn:ietf:params:oauth:client-assertion-type:jwt-spiffe"

// The token_endpoint_auth_methods_supported values the draft registers (RFC 8414
// AS metadata). A host matches these against the AS's published metadata to pick
// a method; AuthMethodSpiffeWIT is recognized-but-unsupported here (see the
// package comment above).
const (
	AuthMethodSpiffeJWT  = "spiffe_jwt"
	AuthMethodSpiffeX509 = "spiffe_x509"
	AuthMethodSpiffeWIT  = "spiffe_wit"
)

// ClientAssertion is the spiffe_jwt client-authentication material for ONE token
// request: the two form parameters of RFC 7523 §2.2 plus the non-secret claims a
// host may want for its audit record. The Assertion field is the bearer JWT-SVID
// — transient, never logged, never persisted.
type ClientAssertion struct {
	// Type is always ClientAssertionTypeJWTSVID.
	Type string
	// Assertion is the raw JWT-SVID (SECRET — present once, do not retain).
	Assertion string
	// SpiffeID is the workload's SPIFFE ID (the draft's sub; also what an AS
	// matches against its registered spiffe_id metadata, wildcard-aware).
	SpiffeID string
}

// Apply stamps the assertion onto a token-request form (RFC 7523 §2.2). It never
// sets client_id: for spiffe_jwt the draft makes client_id OPTIONAL (the SPIFFE
// ID travels in sub), and inventing one here could contradict the AS's client
// registration — the host adds it only when its registration requires it.
func (a ClientAssertion) Apply(form url.Values) {
	form.Set("client_assertion_type", a.Type)
	form.Set("client_assertion", a.Assertion)
}

// NewClientAssertion fetches a fresh JWT-SVID bound to the authorization
// server's issuer identifier and packages it as spiffe_jwt client
// authentication. Per the draft, aud MUST contain only the AS issuer — so the
// audience is exactly asIssuer with no extras — and sub MUST be the SPIFFE ID,
// which is asserted locally (decode-only, the InspectAnthropicAssertion
// pre-flight pattern) so a misissued token surfaces as a clear local error, not
// an opaque invalid_client.
func NewClientAssertion(ctx context.Context, f jwtFetcher, asIssuer string) (ClientAssertion, error) {
	issuer := strings.TrimSpace(asIssuer)
	if issuer == "" {
		return ClientAssertion{}, fmt.Errorf("spiffe: client assertion: empty authorization-server issuer")
	}
	svid, err := f.FetchJWTSVID(ctx, issuer)
	if err != nil {
		return ClientAssertion{}, fmt.Errorf("spiffe: client assertion: fetch jwt-svid: %w", err)
	}
	token := svid.Marshal()
	// Decode-only pre-flight (ParseInsecure already guarantees a valid SPIFFE-ID
	// subject): the AS verifies the signature against the SPIFFE bundle endpoint
	// (draft §8.1); locally re-verifying would need the same bundle and adds no
	// safety to a token the agent just minted.
	parsed, err := jwtsvid.ParseInsecure(token, []string{issuer})
	if err != nil {
		return ClientAssertion{}, fmt.Errorf("spiffe: client assertion: pre-flight: %w", err)
	}
	// Draft §4: aud "MUST contain only the issuer identifier of the authorization
	// server" — containment is not enough; a multi-audience SVID (e.g. one minted
	// with extra audiences by a misconfigured fetcher) must be rejected locally
	// rather than surface as an opaque server-side invalid_client.
	if len(parsed.Audience) != 1 || parsed.Audience[0] != issuer {
		return ClientAssertion{}, fmt.Errorf("spiffe: client assertion: aud must contain only the AS issuer %q (draft §4), got %q", issuer, parsed.Audience)
	}
	return ClientAssertion{
		Type:      ClientAssertionTypeJWTSVID,
		Assertion: token,
		SpiffeID:  parsed.ID.String(),
	}, nil
}

// X509ClientID returns the client_id for the spiffe_x509 method: the SPIFFE ID
// of the workload's CURRENT X.509-SVID (draft §5 — the request's client_id MUST
// match the SVID's URI SAN). The transport half is Workload.MTLSClientConfig
// with an explicit deny-by-default authorizer; this helper only supplies the
// identifier, never key material.
func X509ClientID(w *Workload) (string, error) {
	svid, err := w.X509SVID()
	if err != nil {
		return "", fmt.Errorf("spiffe: x509 client id: %w", err)
	}
	id := svid.ID.String()
	if id == "" {
		return "", fmt.Errorf("spiffe: x509 client id: SVID carries no SPIFFE ID")
	}
	return id, nil
}
