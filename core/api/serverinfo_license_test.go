// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/license"
)

// The unauthenticated server-info license badge surfaces the attested display-only
// labels (plan, support tier) when a license verifies — and the surfacing gates
// nothing (LICENSING.md): it only mirrors what the signed license attests.
func TestServerInfoSurfacesLicenseLabels(t *testing.T) {
	// CHANGED BY: a TERM, because v8 is term-only. The point of the test — that the
	// badge mirrors the attested labels and gates nothing — is unaffected.
	licNow := time.Now().UTC()
	blob, err := license.Sign(license.Claims{
		Licensee: "Acme GmbH", Plan: "commercial", SupportTier: "enterprise",
		IssuedAt: licNow, ExpiresAt: licNow.Add(365 * 24 * time.Hour),
	}, license.DevPrivateKey())
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
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
	if lic["status"] != "valid" || lic["licensee"] != "Acme GmbH" {
		t.Fatalf("license status/licensee = %v", lic)
	}
	if lic["plan"] != "commercial" || lic["support_tier"] != "enterprise" {
		t.Fatalf("license labels = %v; want plan=commercial support_tier=enterprise", lic)
	}
}

// With no license the badge reports status "none" and OMITS the plan/support-tier
// keys entirely — an absent license never renders a fabricated label (docs/SECURITY-HARDENING.md).
func TestServerInfoOmitsLabelsWithoutLicense(t *testing.T) {
	h := newHarness(t)
	r := h.do("GET", "/v1/server-info", "", nil, nil)
	if r.code != http.StatusOK {
		t.Fatalf("server-info = %d %s", r.code, r.raw)
	}
	lic, ok := r.body["license"].(map[string]any)
	if !ok {
		t.Fatalf("no license object in %v", r.body)
	}
	if lic["status"] != "none" {
		t.Fatalf("status = %v, want none", lic["status"])
	}
	if _, present := lic["plan"]; present {
		t.Fatalf("plan must be omitted with no license: %v", lic)
	}
	if _, present := lic["support_tier"]; present {
		t.Fatalf("support_tier must be omitted with no license: %v", lic)
	}
}
