// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package sessions

import (
	"os"
	"strconv"
)

// fileScopeSupported: the engine can ask an open descriptor where it really is.
const fileScopeSupported = true

// openedPath is where the open file is, links resolved, as the kernel sees it now.
func openedPath(file *os.File) (string, error) {
	return os.Readlink("/proc/self/fd/" + strconv.Itoa(int(file.Fd())))
}
