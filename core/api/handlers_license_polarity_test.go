// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/license"
)

// THE POLARITY TEST. /v1/console/license must answer 200 for EVERY commercial state —
// including the ones made reachable (a license past its term, and a blob that attests
// no term at all). The open binary reports the state; it never refuses on it
// (LICENSING.md §ADR-0010; core/api/server.go:674-686, "pure edition plumbing — never a feature
// gate"). A 403 here would be the inverted polarity the whole term-only change exists to
// avoid.
//
// WHY IT IS NEW. an internal design note (not shipped) claimed this row was pinned by
// core/api/serverinfo_license_test.go and core/api/grpc_test.go. It was not: those exercise
// /v1/server-info and the gRPC GetServerInfo, which are a DIFFERENT surface — unauthenticated
// display, not the superadmin console endpoint. An adversarial contrast caught the
// attribution, and deleting the claim would have left the row unproven. This proves it.

// stubLicenseService is the smallest LicenseService that lets the endpoint run: it reports a
// fixed status and refuses to mutate. It is deliberately NOT the engine's implementation —
// the property under test belongs to the HTTP layer (does the route answer 200?), not to how
// a status is derived, which core/license's own tests own.
type stubLicenseService struct{ status string }

func (s stubLicenseService) LicenseStatus(context.Context) (api.LicenseStatus, error) {
	return api.LicenseStatus{Status: s.status, Licensee: "Acme GmbH", Plan: "commercial", Source: "data-dir"}, nil
}

func (s stubLicenseService) InstallLicense(context.Context, string, bool) (api.LicenseStatus, error) {
	return api.LicenseStatus{}, api.ErrLicenseInvalid
}

func (s stubLicenseService) UninstallLicense(context.Context, bool) (api.LicenseStatus, error) {
	return api.LicenseStatus{}, api.ErrLicenseInvalid
}

func (s stubLicenseService) LicenseDisplay() api.LicenseDisplayInfo {
	return api.LicenseDisplayInfo{Status: s.status, Licensee: "Acme GmbH", Plan: "commercial"}
}

func (s stubLicenseService) Reconcile(context.Context) {}

func TestConsoleLicenseEndpointStays200InEveryCommercialState(t *testing.T) {
	// "expired" and the termless case both route through StatusExpired since "grace"
	// is only reachable with an attested window; "none"/"invalid" are the no-license paths.
	for _, status := range []string{"none", "invalid", "valid", "grace", "expired"} {
		t.Run(status, func(t *testing.T) {
			h := newHarnessOpts(t, func(o *api.Options) { o.License = stubLicenseService{status: status} })
			admin := h.adminLogin()

			r := h.do("GET", "/v1/console/license", admin, nil, nil)
			if r.code != http.StatusOK {
				t.Fatalf("GET /v1/console/license with status %q = %d %s; the open binary must REPORT a commercial state, never refuse on it", status, r.code, r.raw)
			}
			if got := r.body["status"]; got != status {
				t.Fatalf("reported status = %v, want %q", got, status)
			}
			if got := r.body["licensee"]; got != "Acme GmbH" {
				t.Fatalf("reported licensee = %v, want the attested one", got)
			}
		})
	}
}

// The AddonGate refusal must reach the wire as 403 with its own code, NOT as the generic
// 500 it used to become. Asserted on the ONE mapper both REST and gRPC share, so the two
// cannot answer differently — which is the reason statusFor exists at all.
//
// The precedent is in the same switch: multi_idp_requires_enterprise was added because the
// second-IdP refusal "previously fell through to a generic 500". This is that defect again,
// for add-ons, and measured it on three separate paths.
func TestAddonRefusalIsA403NotA500(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"with add-on and operation", license.AddonRequired("compliance-packs", "compliance.depth.export")},
		{"bare sentinel", license.ErrAddonRequiresLicense},
		{"wrapped by a handler", fmt.Errorf("depth pack: %w", license.AddonRequired("regulated", "x"))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, slug := api.StatusForTest(tc.err)
			if code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403 — an entitlement refusal is not a server fault", code)
			}
			if slug != "addon_requires_license" {
				t.Fatalf("code = %q, want addon_requires_license (stable across REST and gRPC)", slug)
			}
		})
	}

	// The control: an unrelated error still maps to 500, so the new arm did not widen
	// into a catch-all that hides real faults.
	if code, _ := api.StatusForTest(errors.New("a genuine internal fault")); code != http.StatusInternalServerError {
		t.Fatalf("an unrelated error mapped to %d, want 500 — the new arm must not swallow faults", code)
	}
}
