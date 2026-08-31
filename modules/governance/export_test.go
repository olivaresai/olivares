// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"

	"github.com/olivaresai/olivares/core/model"
)

// SwapReloadGrantsForTest replaces the live grant-engine swap and returns a restore
// func. It exists to reach the DEFERRED activation outcome — publish/rollback committed
// to the store while the running evaluator kept the PREVIOUS policy. In production that
// branch opens only on a store fault or a union-compile failure, neither of which a test
// can trigger reliably, so without this seam the `live_activation: "deferred"` contract
// would ship unpinned. Compiled only under `go test`; not part of the module's API.
func (m *Module) SwapReloadGrantsForTest(fn func(context.Context, model.TenantID) error) (restore func()) {
	prev := m.reloadGrantsFn
	m.reloadGrantsFn = fn
	return func() { m.reloadGrantsFn = prev }
}
