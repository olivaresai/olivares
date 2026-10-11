// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package webui embeds the built web UI into the olivares binary so the
// product ships as a single static artifact.
//
// The embed directive can only reference files at or below this file's own
// directory, so the web (Vite) build MUST output to core/internal/webui/dist/
// (its build.outDir). task web:check builds and verifies the generated bundle;
// PR Go builds reuse that output. Only dist/PLACEHOLDER is committed so Go can
// compile before the web app is built. The embed pattern fails at compile time
// if dist/ has no matching files. The "all:" pattern prefix also includes files
// whose names start with "." or "_"
// (Vite emits such asset dirs).
package webui

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var distFS embed.FS

// FS returns the embedded web UI rooted at the web app's top level (so files
// are served at "index.html", "assets/…"), with the internal "dist/" prefix
// stripped.
func FS() fs.FS {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		// Unreachable in a correctly built binary: "dist" is always embedded.
		panic("webui: embedded dist subtree missing: " + err.Error())
	}
	return sub
}
