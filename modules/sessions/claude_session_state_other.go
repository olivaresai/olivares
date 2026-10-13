// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !unix

package sessions

import (
	"io/fs"
	"os"
)

// openStateFile is the portable open. The operated session runtime is a Linux
// feature; this keeps the package building elsewhere and checks what it can.
func openStateFile(root *os.Root, rel string) (*os.File, error) {
	return root.Open(rel)
}

func singleLink(fs.FileInfo) bool { return true }
