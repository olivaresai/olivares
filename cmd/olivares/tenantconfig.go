// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

// tenantconfig.go holds the ONE place that decides what makes an operator-configured
// (or decision-carried) tenant reference usable. Before the same predicate was
// hand-written at every reader, and the copies had drifted: five refused a broken
// tenant, one warned and widened, and two of the six never checked the reserved SYSTEM
// tenant at all. Every reader now asks this function for the POLICY and keeps its own
// REACTION — refuse to mount, skip the entry, or decline to anchain evidence.

import (
	"fmt"
	"strings"

	"github.com/olivaresai/olivares/core/model"
)

// parseBusinessTenant resolves an operator-configured or decision-carried tenant
// reference into a BUSINESS tenant, deny-closed.
//
// It separates the two cases that must never be conflated:
//
//   - ABSENT (raw is blank): present=false, err=nil. This is a legitimate
//     configuration — "no fixed tenant, infer per credential" — and is the documented
//     default of the inference proxy's `tenant` field. Callers decide whether an
//     absent tenant means "mount nothing" or "mount without a fixed tenant".
//   - PRESENT AND INVALID: err names the field and the offending value. Invalid means
//     unparseable, the nil UUID (the "unset" sentinel), or the reserved SYSTEM tenant.
//   - PRESENT AND VALID: the parsed tenant, present=true.
//
// The system-tenant leg is not symmetry for its own sake. model.ParseTenantID has an
// explicit special case that returns the system tenant with a NIL error
// (core/model/ids.go:56-58), and the system tenant is non-zero by design (ids.go:28) —
// so the common `err == nil && !tid.IsZero()` shape admits it silently. The system
// tenant is reserved for cross-tenant/system rows; a governed surface bound to it
// would authorize, budget and attribute business traffic outside every business
// boundary. Its exclusion is the same rule the estate-wide loops already apply when
// they enumerate business tenants (see the businessTenants helpers).
func parseBusinessTenant(field, raw string) (model.TenantID, bool, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", false, nil
	}
	tid, err := model.ParseTenantID(trimmed)
	if err != nil {
		return "", false, fmt.Errorf("%s: %q is not a valid tenant id: %w", field, raw, err)
	}
	if tid.IsZero() {
		return "", false, fmt.Errorf("%s: %q is the unset tenant, not a business tenant", field, raw)
	}
	if tid.IsSystem() {
		return "", false, fmt.Errorf("%s: %q is the reserved system tenant, not a business tenant", field, raw)
	}
	return tid, true, nil
}
