// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !unix

package repair

import "os"

// fileFacts cannot state an owner here, so no custody holds.
func fileFacts(os.FileInfo) (uint32, uint64, bool) { return 0, 0, false }
