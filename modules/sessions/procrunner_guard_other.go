// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !linux

package sessions

import "os/exec"

// The session guard is Linux's: it needs a subreaper, which other systems do not
// have. There the session is the process the engine starts, as before.

type engineWatch struct{}

func watchEngine(*exec.Cmd) (*engineWatch, error) { return nil, nil }

func (*engineWatch) started() {}

func (*engineWatch) close() {}

func (*engineWatch) with(release func()) func() { return release }

func handlesSessionGuard([]string) bool { return false }

func runSessionGuard([]string) int { return 126 }
