// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"crypto/ed25519"
	"encoding/base64"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/license"
)

// The engine's OWN license read — server-info's static fallback (core/api/server.go,
// licenseStatus), the tenth call site and the one that is not in the command tree at all.
//
// ⚠ WHAT THIS TEST DOES AND DOES NOT COVER, because the difference was measured and it is easy to
// overclaim. This exercises the STATIC FALLBACK, which is what an embedder or a test that wires
// Options.LicenseBlob without a LicenseService gets. In the SHIPPED binary that branch is
// unreachable: licenseStatus short-circuits on `s.license != nil` (server.go), boot.go constructs
// the license service unconditionally and always passes it, and Options.LicenseBlob is assigned
// nowhere in production. The live engine path is licenseService.LicenseDisplay() →
// holder.display(), covered in cmd/olivares/license_v3_consumers_test.go.
//
// It is still the right fix and the right test: the branch is public API for embedders, and a
// dead branch that reads licenses by a path the rest of the binary abandoned is how the next
// divergence starts. But "the engine now reads v3" is earned by the holder test, not by this one.
func TestServerInfoReadsAnAggregateCredential(t *testing.T) {
	priv := license.DevPrivateKey()
	if len(priv) != ed25519.PrivateKeySize {
		t.Skip("this build embeds no dev key")
	}
	// A term that does not rot: this endpoint reads the wall clock, and a fixture that expires
	// would turn a real regression and a passing date into the same red.
	payload := []byte(`{"schema":"olivares.commercial.credential.v3","serial":"cred_engine_0001",` +
		`"issue_seq":1,"key_id":"issuer-2026-08","key_epoch":1,` +
		`"issued_at":"2026-08-07T10:00:00Z","not_before":"2026-08-07T10:00:00Z",` +
		`"entity_id":"ent_acme_sl","deployment_id":"dep_prod_01","purpose":"production",` +
		`"licensee":{"display_name":"Acme GmbH"},"support_profile":"enterprise","grants":[` +
		`{"grant_id":"gr_base","order_line_id":"ol_base","product_id":"pdt_business","kind":"base",` +
		`"cadence":"year","paid_through":"2099-01-01T00:00:00Z","expires_at":"2099-01-01T00:00:00Z",` +
		`"issuance_phase":"term","guarantee_deadline":null,"promotion_hold_deadline":null,"lease_until":null},` +
		`{"grant_id":"gr_addon","order_line_id":"ol_addon","product_id":"adn_identity","kind":"addon",` +
		`"cadence":"year","paid_through":"2099-01-01T00:00:00Z","expires_at":"2099-01-01T00:00:00Z",` +
		`"issuance_phase":"term","guarantee_deadline":null,"promotion_hold_deadline":null,"lease_until":null}]}`)
	enc := base64.RawURLEncoding
	blob := enc.EncodeToString(payload) + "." + enc.EncodeToString(ed25519.Sign(priv, payload))

	h := newHarnessOpts(t, func(o *api.Options) {
		o.LicenseBlob = blob
		o.LicensePublicKey = license.DefaultPublicKey()
	})
	r := h.do("GET", "/v1/server-info", "", nil, nil)
	if r.code != http.StatusOK {
		t.Fatalf("server-info = %d %s", r.code, r.raw)
	}
	lic, ok := r.body["license"].(map[string]any)
	if !ok {
		t.Fatalf("no license object in %v", r.body)
	}
	if lic["status"] != "valid" {
		t.Fatalf("status = %v, want valid — the engine still cannot read the credential it is sold", lic["status"])
	}
	if lic["licensee"] != "Acme GmbH" {
		t.Fatalf("licensee = %v, want Acme GmbH (the envelope's licensee is an object; this is its display_name)", lic["licensee"])
	}
	// And NOTHING is invented for the two labels v3 does not carry. The badge omits them exactly
	// as it omits labels for an absent license (docs/SECURITY-HARDENING.md: unknown is never reported as a
	// concrete value).
	//
	// support_tier is the one worth spelling out, because the tempting mapping is wrong: the
	// credential's envelope has `support_profile`, whose value here is "enterprise" and in the
	// frozen vector is "business" — a PRODUCT tier, not a support relationship. Rendering it in
	// this slot would put a fact nobody signed under a label that means something else.
	for _, absent := range []string{"plan", "support_tier"} {
		if _, present := lic[absent]; present {
			t.Fatalf("%s = %v; a v3 credential carries none and the badge must not fabricate one", absent, lic[absent])
		}
	}
}

// The non-firing direction for the engine: a credential whose bytes were rewritten after signing
// is still "invalid". Without it, the test above would also pass on a server that stopped
// checking signatures at all.
func TestServerInfoStillRefusesATamperedCredential(t *testing.T) {
	priv := license.DevPrivateKey()
	if len(priv) != ed25519.PrivateKeySize {
		t.Skip("this build embeds no dev key")
	}
	payload := []byte(`{"schema":"olivares.commercial.credential.v3","serial":"cred_engine_0001",` +
		`"issue_seq":1,"key_id":"issuer-2026-08","key_epoch":1,` +
		`"issued_at":"2026-08-07T10:00:00Z","not_before":"2026-08-07T10:00:00Z",` +
		`"entity_id":"ent_acme_sl","deployment_id":"dep_prod_01","purpose":"production",` +
		`"licensee":{"display_name":"Acme GmbH"},"support_profile":"enterprise","grants":[` +
		`{"grant_id":"gr_base","order_line_id":"ol_base","product_id":"pdt_business","kind":"base",` +
		`"cadence":"year","paid_through":"2099-01-01T00:00:00Z","expires_at":"2099-01-01T00:00:00Z",` +
		`"issuance_phase":"term","guarantee_deadline":null,"promotion_hold_deadline":null,"lease_until":null}]}`)
	enc := base64.RawURLEncoding
	sig := ed25519.Sign(priv, payload)
	forged := []byte(string(payload))
	copy(forged[len(`{"schema":"olivares.commercial.credential.v3","serial":"`):], []byte("cred_engine_0002"))
	blob := enc.EncodeToString(forged) + "." + enc.EncodeToString(sig)

	h := newHarnessOpts(t, func(o *api.Options) {
		o.LicenseBlob = blob
		o.LicensePublicKey = license.DefaultPublicKey()
	})
	r := h.do("GET", "/v1/server-info", "", nil, nil)
	lic, ok := r.body["license"].(map[string]any)
	if !ok {
		t.Fatalf("no license object in %v", r.body)
	}
	if lic["status"] != "invalid" {
		t.Fatalf("status = %v for a credential rewritten after signing, want invalid", lic["status"])
	}
}
