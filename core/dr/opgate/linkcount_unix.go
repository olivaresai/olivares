// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package opgate

import (
	"io/fs"
	"syscall"
)

// hardLinkCount reports how many names an existing file has.
//
// known=false means the platform did not expose it. The caller then does NOT refuse:
// this check is a refusal for a proven multi-name destination, not a demand that
// every platform be able to prove the opposite. Inventing coverage where the number
// is unavailable would be the same overclaim the pathname protocol already avoids.
func hardLinkCount(info fs.FileInfo) (links uint64, known bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return uint64(st.Nlink), true
}
