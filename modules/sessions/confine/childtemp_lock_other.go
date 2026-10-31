// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !unix

package confine

import "os"

// lockChildTemp holds nothing where flock does not exist; no confined child runs there.
func lockChildTemp(string) (*os.File, error) { return nil, nil }

// waitChildTemp has nothing to wait for where flock does not exist.
func waitChildTemp(*os.File) error { return nil }
