// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package residency

// This file is the COORDINATION seam between the control-plane data-residency pin
// (orgs.data_region) and the model/inference-residency already modeled elsewhere
// (inference_geo on cost samples; the workspace DataResidency.AllowedInferenceGeos
// in connectors/modelprovider). It is a pure policy — it judges compatibility, it
// does not capture, route, or enforce anything itself. The compliance module's
// residency scan uses it to flag a tenant whose inference crosses its pinned region
// (reusing the existing residency_violation Finding + compliance_residency_violation
// bus signal), and a model router could consult it at admission.

// InferenceGeoCompatible reports whether an observed inference_geo is compatible
// with a tenant's control-plane residency pin. The rule is deny-closed and honest:
//
//   - An unpinned tenant (pin == "") has no residency requirement → always compatible.
//   - A pinned tenant is compatible ONLY when the inference geo equals the pinned
//     region. So "us" inference for a "us"-pinned tenant is compatible; "eu" for "eu"
//     is compatible. Anything else is NOT: a specific pin is incompatible with the
//     "global" geo (which may route anywhere, so it does not keep data in-region),
//     with another region's geo (e.g. "us" inference for an "eu" tenant), and with an
//     empty / "not_available" geo (residency cannot be proven). This is strict on
//     purpose — residency means inference stays IN the pinned region; a deployment
//     that wants looser behavior simply leaves the tenant unpinned.
func InferenceGeoCompatible(pin, geo string) bool {
	p := Normalize(pin)
	if p == "" {
		return true
	}
	return Normalize(geo) == p
}

// AllowedInferenceGeos returns the inference geos compatible with a residency pin:
// nil for an unpinned region (no constraint — every geo is allowed), or the single
// in-region geo for a pinned region. It is the constructive form of
// InferenceGeoCompatible, for surfacing in reports and provisioning checks.
func AllowedInferenceGeos(pin Region) []Region {
	p := Normalize(string(pin))
	if p == "" {
		return nil
	}
	return []Region{p}
}
