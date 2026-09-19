// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !unix

package sessions

import "os"

// dirOwnedByEngineUser cannot read a numeric owner on this platform, and says so
// rather than guessing. See the unix build of this file for why "cannot tell"
// must not collapse into "not owned".
func dirOwnedByEngineUser(os.FileInfo) (owned, known bool) { return false, false }
