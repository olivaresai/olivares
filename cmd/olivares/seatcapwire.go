// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import "github.com/olivaresai/olivares/core/license"

// licenseClaimsFunc returns the engine's VERIFIED license claims (ok=false when
// there is no valid, unexpired license). It is the seam by which the enterprise
// overlay reads the attested claims; the default build's seat policy ignores it.
// Since B10 no build may turn Claims.MaxUsers into a user cap — accounts are
// unlimited in every self-hosted tier (core/auth/seatcap.go). Defined in a
// build-independent file so both wire_{enterprise,noenterprise}.go share the
// signature without either importing the other's dependencies.
type licenseClaimsFunc = func() (license.Claims, bool)

// licenseGrantsFunc is the container-aware half of the add-on entitlement seam.
// ok=false is the same answer as licenseClaimsFunc: there is no live license.
// A live FLAT license returns (nil, true) — it has no grant list, and the
// overlay must not invent one. A live v3 returns the signed lines, in wire
// order. The default build never consults it; bindEnterpriseEntitlement is a
// no-op there.
type licenseGrantsFunc = func() ([]license.Grant, bool)
