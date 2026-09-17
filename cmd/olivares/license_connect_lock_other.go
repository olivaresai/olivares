// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !unix

package main

import (
	"fmt"
	"os"
)

// acquireConnectLease refuses where the flock lease does not exist: a no-op lease would let two
// invocations persist conflicting operations. The published builds are linux and darwin.
func acquireConnectLease(connectDir string) (*os.File, error) {
	return nil, fmt.Errorf("%w: the connected client lease in %s is not supported on this platform", errConnectBusy, connectDir)
}
