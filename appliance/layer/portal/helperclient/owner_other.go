// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package helperclient

import "os"

// ownedBy cannot establish a socket's owner on this platform, so no helper is reached.
func ownedBy(os.FileInfo, uint32) bool { return false }
