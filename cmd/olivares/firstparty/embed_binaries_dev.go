// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !release

package firstparty

import "embed"

// bins holds the plugin binaries available to a development/test build. `all:`
// keeps files the toolchain would otherwise skip; the committed placeholder lets
// a plain `go build` remain connector-free for fast local iteration.
//
//go:embed all:bins
var bins embed.FS
