// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !linux

package hostops

import (
	"errors"
	"os"
)

func lockFile(*os.File) error { return errors.New("host operation locks require Linux") }

func lockShared(*os.File) error { return errors.New("host operation locks require Linux") }

func unlockFile(*os.File) error { return errors.New("host operation locks require Linux") }

func tryLockShared(*os.File) error { return errors.New("host operation locks require Linux") }
