// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !unix

package main

import "errors"

const canReexec = false

func reexecSelf([]string) error {
	return errors.New("this platform cannot re-execute a running process; restart the engine with its service manager")
}
