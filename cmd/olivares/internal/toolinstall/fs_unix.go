// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package toolinstall

import (
	"fmt"
	"os"
	"syscall"
)

// ownedByCaller reports whether fi belongs to the effective uid, or the caller
// is root. A destination another account owns is refused: its owner could
// replace staging directories or receipts between our checks and our writes.
func ownedByCaller(fi os.FileInfo) error {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("cannot read the owner of %s", fi.Name())
	}
	euid := os.Geteuid()
	if euid == 0 || int(st.Uid) == euid {
		return nil
	}
	return fmt.Errorf("%s is owned by uid %d, not by this process (uid %d)", fi.Name(), st.Uid, euid)
}
