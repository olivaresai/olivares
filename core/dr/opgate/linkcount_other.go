// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !unix

package opgate

import "io/fs"

// hardLinkCount has no implementation off unix. It reports "unknown" rather than
// "one", because a platform that cannot answer must not be recorded as having
// answered no. flockTry already refuses on such a platform, so nothing reaches a
// destination this could have judged.
func hardLinkCount(fs.FileInfo) (uint64, bool) { return 0, false }
