// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !linux

package localinstall

import (
	"errors"
	"time"
)

// Placed is what Place put in place: the manifest's inode and modification time.
type Placed struct {
	Inode   uint64
	ModTime time.Time
}

func (p Placed) String() string { return "" }

// Place exists only on Linux, where the package scripts run.
func Place(stage, target string, gid int) (Placed, error) {
	return Placed{}, errors.New("placing the install manifest is supported only on Linux")
}
