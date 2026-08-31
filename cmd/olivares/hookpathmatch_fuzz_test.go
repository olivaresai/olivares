// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func FuzzNormalizePath(f *testing.F) {
	f.Add("/repo/a..b/file.txt", "")
	f.Add("../secrets/file.txt", "/repo/work")
	f.Add("nested/../file.txt", "/repo/work")
	f.Add("relative/file.txt", "relative/root")

	f.Fuzz(func(t *testing.T, ref, root string) {
		got, ok := normalizePath(ref, root)
		if !ok {
			return
		}
		if !filepath.IsAbs(got) {
			t.Fatalf("normalizePath(%q, %q) returned non-absolute path %q", ref, root, got)
		}
		for _, segment := range strings.Split(got, "/") {
			if segment == ".." {
				t.Fatalf("normalizePath(%q, %q) retained an exact .. segment in %q", ref, root, got)
			}
		}
	})
}
