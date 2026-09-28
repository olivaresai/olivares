// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !linux

package repair

import (
	"context"
	"errors"
	"os"
	"time"
)

var errNoLocks = errors.New("the recovery gates read Linux locks only")

// lockHeld cannot read a lock here, so no backend is proven idle.
func lockHeld(string) (bool, error) { return false, errNoLocks }

// exclusiveWithin cannot hold a lock here, so nothing is admitted.
func exclusiveWithin(context.Context, *os.File, time.Duration) error { return errNoLocks }

// unlock has nothing to release here.
func unlock(*os.File) error { return errNoLocks }
