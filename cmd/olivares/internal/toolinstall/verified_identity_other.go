// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !linux

package toolinstall

import "os"

// fileIdentity is not read off Linux: nothing is cached there and every read
// checks the release in full.
func fileIdentity(os.FileInfo) (string, bool) { return "", false }
