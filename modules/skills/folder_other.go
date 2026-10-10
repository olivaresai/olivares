// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !linux

package skills

import "context"

// ImportFolder refuses platforms without the qualified no-follow snapshotter.
// ZIP/tar.gz upload remains available there.
func ImportFolder(context.Context, string) (*ValidatedPack, error) {
	return nil, refuse("unsupported_source", "folder snapshot on this platform; upload an archive")
}
