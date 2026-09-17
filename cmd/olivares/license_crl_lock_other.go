// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !unix

package main

import (
	"fmt"
	"path/filepath"
)

// acquireCRLStoreLock refuses on a platform without the flock lease (CR1 §3), for the same
// reason upgradelock_other.go refuses: a no-op lock would read as serialized while two
// recorders overwrite each other's observation. The published builds are linux and darwin.
func acquireCRLStoreLock(dataDir string, _ crlPersistOps) (*crlStoreLease, error) {
	return nil, fmt.Errorf("%w: %s cannot be taken here, so this observation was not recorded; the published builds are linux and darwin",
		errCRLLockUnsupported, filepath.Join(dataDir, crlLockFileName))
}
