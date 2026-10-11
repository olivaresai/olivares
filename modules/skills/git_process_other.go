// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !linux

package skills

import (
	"context"
	"os/exec"
)

func boundedGitCommand(context.Context, string, ...string) (*exec.Cmd, error) {
	return nil, refuse("unsupported_source", "bounded git is unavailable on this platform; upload an archive")
}
func stopGitGroup(*exec.Cmd) {}
