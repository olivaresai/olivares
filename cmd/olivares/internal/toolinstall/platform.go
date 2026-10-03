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

// glibcLoaderPresent reports whether a glibc dynamic loader is installed, under
// any of the layouts distributions use: /lib64 (RHEL/Fedora), /lib and /usr/lib
// (Alpine-compat, Arch), and the Debian/Ubuntu multiarch directory. Reading a
// directory listing needs no execution.
func glibcLoaderPresent() bool {
	for _, pattern := range []string{
		"/lib64/ld-linux-*.so.*", "/lib/ld-linux-*.so.*",
		"/usr/lib64/ld-linux-*.so.*", "/usr/lib/ld-linux-*.so.*",
		"/lib/*-linux-gnu/ld-linux-*.so.*", "/usr/lib/*-linux-gnu/ld-linux-*.so.*",
	} {
		if matches, err := filepath.Glob(pattern); err == nil && len(matches) > 0 {
			return true
		}
	}
	return false
}

// hostLoaderPresence reads the real filesystem. loaderFilesExist is the same
// probe as a package var, so a test can pin both loader layouts without writing
// to the host's /lib or /lib64 (an internal seam; production keeps the probe).
func hostLoaderPresence() (glibc, musl bool) { return glibcLoaderPresent(), muslLoaderPresent() }

var loaderFilesExist = hostLoaderPresence

// hostLibc decides the libc a downloaded binary must link against (HU-R13):
// glibc, unless the host has NO glibc loader AND has a musl loader. A musl
// loader beside a glibc one (an Alpine chroot, a static-musl tool collection)
// does not make the host musl, and a host with neither loader still gets the
// default most hosts can exec.
func hostLibc() string {
	glibc, musl := loaderFilesExist()
	if !glibc && musl {
		return "musl"
	}
	return "glibc"
}
