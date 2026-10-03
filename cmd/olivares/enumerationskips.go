// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"errors"
	"log/slog"
	"sync/atomic"

	"github.com/olivaresai/olivares/core/store"
)

// enumerationSkips logs, once per state change, that a job which must cover every
// tenant skips its ticks because the store cannot list every tenant (PostgreSQL with
// neither the tenant inventory nor the administration role). That state is
// deliberate and lasts until an operator installs the inventory, and server-info
// already names the job, so a warning on every tick repeats what is known. The
// first skipped tick warns; the following ones are silent; the first tick that
// lists every tenant again says so. Any other enumeration error is a fault and is
// warned on every tick, as before.
type enumerationSkips struct{ skipping atomic.Bool }

// skip reports a tick that could not enumerate every tenant.
func (s *enumerationSkips) skip(log *slog.Logger, job string, err error) {
	if !errors.Is(err, store.ErrEnumerationNotAuthoritative) {
		log.Warn(job+": cannot enumerate orgs; skipping this tick", "err", err)
		return
	}
	if s.skipping.CompareAndSwap(false, true) {
		log.Warn(job+": this database cannot list every tenant (no tenant inventory and no administration role); skipping every tick until it can, without repeating this line",
			"err", err)
	}
}

// listed reports a tick that enumerated every tenant.
func (s *enumerationSkips) listed(log *slog.Logger, job string) {
	if s.skipping.CompareAndSwap(true, false) {
		log.Info(job + ": this database lists every tenant again; the job runs")
	}
}
