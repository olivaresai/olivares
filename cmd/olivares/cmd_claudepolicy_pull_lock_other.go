// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !unix

package main

import "fmt"

// lockPolicyOut refuses where flock does not exist, as lockUpgradeTarget does: a pull without
// it would let two pulls of one file admit a stale revision over a newer one. The published
// builds are linux and darwin (.goreleaser.yaml).
func lockPolicyOut(out string) (func(), error) {
	return nil, fmt.Errorf("serializing pulls of %s is not available on this platform; the published builds are linux and darwin", out)
}
