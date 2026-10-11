// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import "github.com/olivaresai/olivares/core/license"

// These callback types adapt the live holder to the overlay's existing consumers.
// Boot passes the holder once through the seatPolicy edition port, not these
// projections separately. No build may turn Claims.MaxUsers into a user cap.
type licenseClaimsFunc = func() (license.Claims, bool)

// A live flat license returns (nil, true); a live v3 returns every signed grant
// in wire order. Community never consults either projection.
type licenseGrantsFunc = func() ([]license.Grant, bool)
