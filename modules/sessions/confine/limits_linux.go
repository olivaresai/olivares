// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package confine

import coreconfine "github.com/olivaresai/olivares/core/runtime/confine"

// Session defaults are policy: no core dumps, files up to 64 GiB and nice +10.
// No address-space ceiling here: race/sanitizer runtimes reserve large address
// spaces. The memory and task ceilings are the session's systemd scope, which counts
// resident memory (modules/sessions/session_scope_linux.go).
func sessionLimits() *coreconfine.Limits {
	return &coreconfine.Limits{CoreSize: 0, FileSize: SessionFileSizeLimit, Nice: SessionNice}
}
