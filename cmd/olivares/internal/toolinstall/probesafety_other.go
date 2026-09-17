// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !unix

package toolinstall

import "errors"

func probeSafety(string) error {
	return errors.New("probe ownership checks are implemented for Unix only in this release")
}
