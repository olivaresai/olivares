// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !(aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris)

package localinstall

import (
	"fmt"
	"os"
)

func validateManifestOwner(_ os.FileInfo, _ string) error {
	return fmt.Errorf("cannot establish local install manifest ownership on this platform")
}
