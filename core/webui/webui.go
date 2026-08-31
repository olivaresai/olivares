// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package webui is the PUBLIC accessor for the embedded web UI bundle. The bytes
// live in core/internal/webui (internal, so only the engine owns the go:embed); this
// thin, behavior-free re-export lets a composition root outside /core (the binary)
// link the embed and list its assets without reaching into an internal package — the
// same visibility seam core/engine is for the store.
package webui

import (
	"io/fs"

	internalwebui "github.com/olivaresai/olivares/core/internal/webui"
)

// FS returns the embedded web UI filesystem.
func FS() fs.FS { return internalwebui.FS() }
