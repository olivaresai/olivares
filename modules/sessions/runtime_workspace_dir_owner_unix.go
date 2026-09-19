// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package sessions

import (
	"os"
	"syscall"
)

// dirOwnedByEngineUser reports whether a directory belongs to the user this
// engine process runs as, and whether that could be determined at all.
//
// The second return value is the honest part: on a platform whose FileInfo does
// not carry a numeric owner, "not owned" and "cannot tell" are different
// answers, and the caller must not turn the second into the first. A release
// that refused every root on such a host would be a deny-closed posture
// protecting nothing — the other four conditions of
// validateSessionWorkspaceRoot carry the guard there.
func dirOwnedByEngineUser(info os.FileInfo) (owned, known bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false, false
	}
	return int(st.Uid) == os.Getuid(), true
}
