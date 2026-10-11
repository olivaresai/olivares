// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"regexp"
)

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
	// Authorize is the private composition root's live entitlement check.
	// A missing policy refuses new elevation; diagnostic reads remain available.
	Authorize func(operation string) error
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

// String renders a compact operational summary (no key material).
func (s PIVStatus) String() string {
	return fmt.Sprintf("piv{presented:%t ocsp:%s role:%q}", s.Presented, s.OCSP, s.MappedRole)
}
