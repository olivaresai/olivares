// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !unix

package opgate

import (
	"fmt"
	"os"
)

// flockTry REFUSES on a platform without flock, and that refusal is the whole
// point of this file.
//
// The published builds are linux and darwin (.goreleaser.yaml), and both satisfy
// the `unix` constraint, so the real implementation is what ships. A no-op stub
// here would be the worse defect: every caller would read as fenced, every test of
// the fenced path would pass, and a store could be published over a running
// restore with no symptom pointing at the cause.
func flockTry(f *os.File, _ bool) (bool, error) {
	return false, fmt.Errorf(
		"opgate: this platform has no filesystem lock implementation, so the restore control at %s cannot be fenced; the published builds are linux and darwin",
		f.Name())
}

// openNoFollow is zero off unix: the constant is not portable, and flockTry above
// refuses on such a platform before any lock is taken anyway.
const openNoFollow = 0

// openNonBlock is likewise zero off unix, where flockTry above refuses before any
// lock is taken.
const openNonBlock = 0
