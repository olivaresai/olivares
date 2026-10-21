// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !linux

package sessions

import (
	"errors"
	"os"
)

// fileScopeSupported is false off Linux: sessions are confined by Landlock there and
// the engine has no descriptor path to check, so a file request stays refused.
const fileScopeSupported = false

func openedPath(*os.File) (string, error) { return "", errors.New("not supported on this platform") }
