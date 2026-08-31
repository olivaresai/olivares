// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package evals

import (
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// evalEvent wraps a FindingReport as a bus event attributed to this module and the
// tenant. The regression-finding emit (runs.go) routes through it so deliver
// the signal; the raw detail is already reduced to a hex hash (minimal-data, docs/SECURITY-HARDENING.md
// §3).
func evalEvent(tenant model.TenantID, report sdkmodel.FindingReport) event.Event {
	return event.FromObservation(tenant.String(), Name, report)
}
