// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/asn1"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"time"

	"golang.org/x/crypto/ocsp"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// PIV/CAC client-certificate route: the second AAL3 path for
// gov/regulated estates — a human presents a smartcard X.509 certificate over
// mTLS; the engine verifies the chain against the configured agency CA, checks
// revocation over OCSP, maps the certificate to a panel role (informational)
// and binds it to the calling session's user before elevating. FIPS 201-3 is
// the TARGET standard; no conformance is claimed (docs/SECURITY-HARDENING.md).
//
// Fail-closed invariants: not configured -> refuse; no/invalid certificate ->
// refuse; OCSP revoked -> refuse; OCSP unavailable ("unknown") -> refuse unless
// the deployment explicitly opts into AllowOCSPUnknown (labs without a
// responder); certificate not bound to the calling user -> refuse. Every
// refusal is ledgered (auth.stepup.failed, method "piv").

// PIV route errors.
var (
	// ErrPIVNotConfigured means this deployment has no PIV client-CA configured;
	// the route reports the honest 501 seam.
	ErrPIVNotConfigured = errors.New("auth: piv/cac is not configured on this deployment")
	// ErrPIVVerification means the presented certificate failed verification
	// (absent, untrusted chain, revoked/unknown OCSP, or not bound to the
	// calling user). Deliberately coarse toward the client; the ledger records
	// the category.
	ErrPIVVerification = errors.New("auth: piv verification failed")
)

// PIVRoleRule maps a certificate subject to a panel role label. The first rule
// whose pattern matches the leaf's subject DN string wins. The label is
// surfaced on the status endpoint (cert-to-role mapping); it grants
// nothing by itself — authorization stays with RBAC/ABAC.
type PIVRoleRule struct {
	// Subject is a compiled pattern matched against the leaf subject DN
	// (pkix.Name.String() form, e.g. "CN=Jane Doe,OU=Agency,O=U.S. Government").
	Subject *regexp.Regexp
	// Role is the label reported for a matching certificate.
	Role string
}

// PIVConfig configures the PIV/CAC route. Built by the composition root from
// OLIVARES_PIV_CONFIG; nil disables the route (fail-closed).
type PIVConfig struct {
	// Roots is the trust anchor pool the client chain must verify against (the
	// agency/issuing CA, NOT the web PKI).
	Roots *x509.CertPool
	// RoleMap is the ordered cert-to-role mapping (first match wins).
	RoleMap []PIVRoleRule
	// AllowOCSPUnknown lets a deployment WITHOUT a reachable OCSP responder
	// elevate on an otherwise-valid chain ("unknown" revocation state). Default
	// false: unknown is treated as unverified and refused.
	AllowOCSPUnknown bool
	// HTTPClient performs the OCSP fetch; nil uses a 5-second-timeout client.
	HTTPClient *http.Client
}

// PIVStatus is the verifier's view of the presented client certificate — the
// exact shape the panel renders (subject / cert-to-role / OCSP).
type PIVStatus struct {
	Presented  bool   `json:"presented"`
	Subject    string `json:"subject,omitempty"`
	Issuer     string `json:"issuer,omitempty"`
	MappedRole string `json:"mapped_role,omitempty"`
	// OCSP is "good", "revoked" or "unknown" (closed union shared with the panel).
	OCSP     string `json:"ocsp,omitempty"`
	NotAfter string `json:"not_after,omitempty"`
	// chainOK is verifier-internal: the chain verified against Roots.
	chainOK bool
	// chain is the verified chain (leaf first) for the OCSP issuer lookup.
	chain []*x509.Certificate
}

// ocspClient returns the configured OCSP fetcher or a conservative default.
func (c *PIVConfig) ocspClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return &http.Client{Timeout: 5 * time.Second}
}

// Status verifies the presented peer chain and reports the panel-facing state.
// It is a pure read: chain verification + OCSP + role mapping, no elevation.
// With no certificate presented it reports {presented:false}.
func (c *PIVConfig) Status(ctx context.Context, peer []*x509.Certificate, now time.Time) PIVStatus {
	if len(peer) == 0 {
		return PIVStatus{Presented: false}
	}
	leaf := peer[0]
	st := PIVStatus{
		Presented: true,
		Subject:   leaf.Subject.String(),
		Issuer:    leaf.Issuer.String(),
		OCSP:      "unknown",
		NotAfter:  leaf.NotAfter.UTC().Format(time.RFC3339),
	}
	for _, rule := range c.RoleMap {
		if rule.Subject != nil && rule.Subject.MatchString(st.Subject) {
			st.MappedRole = rule.Role
			break
		}
	}
	inter := x509.NewCertPool()
	for _, ic := range peer[1:] {
		inter.AddCert(ic)
	}
	chains, err := leaf.Verify(x509.VerifyOptions{
		Roots: c.Roots, Intermediates: inter, CurrentTime: now,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})
	if err != nil || len(chains) == 0 {
		// Untrusted chain: report presented + subject, but no revocation claim
		// beyond "unknown" — and never elevation (chainOK stays false).
		return st
	}
	st.chainOK = true
	st.chain = chains[0]
	st.OCSP = c.ocspStatus(ctx, st.chain, now)
	return st
}

// ocspClockSkew tolerates responder/engine clock drift on ThisUpdate.
const ocspClockSkew = 5 * time.Minute

// ocspStatus checks the leaf's revocation over its AIA OCSP responder. Any
// failure to obtain a definitive answer is "unknown" — the caller decides
// whether unknown is acceptable (it is not, by default).
//
// Two checks the library leaves to the caller (verified against x/crypto
// v0.52.0): FRESHNESS — CreateRequest supports no nonce, so the validity
// window [ThisUpdate, NextUpdate] is the only replay defense (RFC 6960 §3.2);
// a response with no NextUpdate or outside its window is "unknown", never
// "good", or a captured answer could vouch for a since-revoked card forever.
// DELEGATED-RESPONDER AUTHORITY — ParseResponseForCert accepts any embedded
// responder cert the issuer signed; RFC 6960 §4.2.2.2 requires it to carry
// id-kp-OCSPSigning, or any end-entity cert under the agency CA could forge a
// "good" answer for someone else's serial.
func (c *PIVConfig) ocspStatus(ctx context.Context, chain []*x509.Certificate, now time.Time) string {
	leaf := chain[0]
	if len(leaf.OCSPServer) == 0 || len(chain) < 2 {
		return "unknown"
	}
	issuer := chain[1]
	reqDER, err := ocsp.CreateRequest(leaf, issuer, nil)
	if err != nil {
		return "unknown"
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, leaf.OCSPServer[0], bytes.NewReader(reqDER))
	if err != nil {
		return "unknown"
	}
	httpReq.Header.Set("Content-Type", "application/ocsp-request")
	resp, err := c.ocspClient().Do(httpReq)
	if err != nil {
		return "unknown"
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "unknown"
	}
	parsed, err := ocsp.ParseResponseForCert(body, leaf, issuer)
	if err != nil {
		return "unknown"
	}
	if parsed.Certificate != nil && !hasOCSPSigningEKU(parsed.Certificate) {
		return "unknown" // delegated responder without id-kp-OCSPSigning (§4.2.2.2)
	}
	if parsed.NextUpdate.IsZero() || now.After(parsed.NextUpdate) ||
		now.Before(parsed.ThisUpdate.Add(-ocspClockSkew)) {
		return "unknown" // stale, replayed or not-yet-valid — never a "good"
	}
	switch parsed.Status {
	case ocsp.Good:
		return "good"
	case ocsp.Revoked:
		return "revoked"
	default:
		return "unknown"
	}
}

// hasOCSPSigningEKU reports whether cert is authorized to sign OCSP responses.
func hasOCSPSigningEKU(cert *x509.Certificate) bool {
	for _, eku := range cert.ExtKeyUsage {
		if eku == x509.ExtKeyUsageOCSPSigning {
			return true
		}
	}
	return false
}

// ElevatePIVSession verifies the presented client certificate and elevates the
// calling session to AAL3 (method "piv"). Beyond chain + revocation, the
// certificate must be BOUND to the calling user: one of its SAN rfc822 emails
// must equal the session user's email — otherwise any valid agency card could
// elevate any session (the binding is the whole point of a personal identity
// credential). Every refusal is ledgered with a coarse category.
func (a *Authenticator) ElevatePIVSession(ctx context.Context, actor Principal, cfg *PIVConfig, peer []*x509.Certificate) (model.AuthSession, PIVStatus, error) {
	if cfg == nil || cfg.Roots == nil {
		return model.AuthSession{}, PIVStatus{}, ErrPIVNotConfigured
	}
	if actor.Kind != KindUser || actor.CredID.IsZero() {
		return model.AuthSession{}, PIVStatus{}, ErrUnauthenticated
	}
	now := a.clock.Now().Time()
	st := cfg.Status(ctx, peer, now)
	if !st.Presented {
		a.auditStepUpFailure(ctx, actor, "piv", "no_certificate")
		return model.AuthSession{}, st, ErrPIVVerification
	}
	if !st.chainOK {
		a.auditStepUpFailure(ctx, actor, "piv", "untrusted_chain")
		return model.AuthSession{}, st, ErrPIVVerification
	}
	switch st.OCSP {
	case "good":
	case "unknown":
		if !cfg.AllowOCSPUnknown {
			a.auditStepUpFailure(ctx, actor, "piv", "ocsp_unknown")
			return model.AuthSession{}, st, ErrPIVVerification
		}
	default: // revoked
		a.auditStepUpFailure(ctx, actor, "piv", "ocsp_revoked")
		return model.AuthSession{}, st, ErrPIVVerification
	}
	var email string
	if err := a.st.AuthView(ctx, func(as store.AuthScope) error {
		u, err := as.Users().Get(ctx, actor.UserID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return ErrUnauthenticated
			}
			return err
		}
		email = normalizeEmail(u.Email)
		return nil
	}); err != nil {
		return model.AuthSession{}, st, err
	}
	if !certBindsEmail(peer[0], email) {
		a.auditStepUpFailure(ctx, actor, "piv", "subject_mismatch")
		return model.AuthSession{}, st, ErrPIVVerification
	}
	sess, err := a.ElevateSession(ctx, actor, "piv", AAL3)
	if err != nil {
		return model.AuthSession{}, st, err
	}
	return sess, st, nil
}

// oidSANOtherNameUPN is the Microsoft User Principal Name otherName SAN type
// (1.3.6.1.4.1.311.20.2.3) — the identity slot real PIV/CAC authentication
// certificates carry (FIPS 201-3 deployments often have NO rfc822 email SAN).
var oidSANOtherNameUPN = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 311, 20, 2, 3}

// oidExtensionSAN is the subjectAltName extension (2.5.29.17).
var oidExtensionSAN = asn1.ObjectIdentifier{2, 5, 29, 17}

// certBindsEmail reports whether the certificate names email as its subject:
// an rfc822Name SAN, or a UPN otherName whose value equals the email
// (normalized comparison). Gov/CAC cards typically carry the principal as a
// UPN (user@agency.gov) rather than an email SAN; both bind here.
func certBindsEmail(cert *x509.Certificate, email string) bool {
	if email == "" {
		return false
	}
	for _, san := range cert.EmailAddresses {
		if normalizeEmail(san) == email {
			return true
		}
	}
	for _, upn := range certUPNs(cert) {
		if normalizeEmail(upn) == email {
			return true
		}
	}
	return false
}

// certUPNs extracts the UPN otherName SAN values (best-effort: a malformed
// extension yields none — fail-closed, the binding just does not match).
func certUPNs(cert *x509.Certificate) []string {
	var sanDER []byte
	for _, ext := range cert.Extensions {
		if ext.Id.Equal(oidExtensionSAN) {
			sanDER = ext.Value
			break
		}
	}
	if len(sanDER) == 0 {
		return nil
	}
	var seq asn1.RawValue
	if rest, err := asn1.Unmarshal(sanDER, &seq); err != nil || len(rest) != 0 || !seq.IsCompound {
		return nil
	}
	var upns []string
	rest := seq.Bytes
	for len(rest) > 0 {
		var gn asn1.RawValue
		var err error
		rest, err = asn1.Unmarshal(rest, &gn)
		if err != nil {
			return upns
		}
		// otherName is GeneralName [0] (context-specific, constructed):
		// SEQUENCE { type-id OBJECT IDENTIFIER, value [0] EXPLICIT ANY }.
		if gn.Class != asn1.ClassContextSpecific || gn.Tag != 0 || !gn.IsCompound {
			continue
		}
		var oid asn1.ObjectIdentifier
		valDER, err := asn1.Unmarshal(gn.Bytes, &oid)
		if err != nil || !oid.Equal(oidSANOtherNameUPN) {
			continue
		}
		var outer asn1.RawValue
		if _, err := asn1.Unmarshal(valDER, &outer); err != nil {
			continue
		}
		var upn string
		if _, err := asn1.UnmarshalWithParams(outer.Bytes, &upn, "utf8"); err != nil {
			continue
		}
		upns = append(upns, upn)
	}
	return upns
}

// String renders a compact operational summary (no key material).
func (s PIVStatus) String() string {
	return fmt.Sprintf("piv{presented:%t ocsp:%s role:%q}", s.Presented, s.OCSP, s.MappedRole)
}
