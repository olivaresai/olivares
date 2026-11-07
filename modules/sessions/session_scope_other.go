// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !linux

package sessions

import "os/exec"

// A transient systemd scope is a Linux feature: off Linux the memory and task
// ceilings are not applied, and there is no runtime directory to keep from a session.
func sessionRuntimeDir() string { return "" }

func applySessionScope(*exec.Cmd, string, string, bool) string { return "" }
