// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package toolinstall

import "path/filepath"

// muslLoaderPresent reports whether a musl dynamic loader is installed. Alpine
// and other musl distributions ship /lib/ld-musl-<arch>.so.1; glibc systems do
// not. Reading a directory listing needs no execution, unlike `ldd --version`.
func muslLoaderPresent() bool {
	for _, pattern := range []string{"/lib/ld-musl-*.so.1", "/usr/lib/ld-musl-*.so.1"} {
		if matches, err := filepath.Glob(pattern); err == nil && len(matches) > 0 {
			return true
		}
	}
	return false
}
