// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !linux

package plugjail

import "os/exec"

// applyOS on a non-Linux host applies NO OS-level isolation: dedicated uid, cgroup,
// seccomp and landlock are Linux primitives. Env scoping (C1, done in Apply) and the
// bounded lifecycle remain in force. It records the honest degrade — the attestation
// reports LevelMinimal, never a control it did not apply. It also records that env
// scoping without a uid drop is bypassable (a same-uid plugin can read the engine's
// /proc/<pid>/environ), so the reader is never misled that C1 alone protects secrets.
func applyOS(_ *exec.Cmd, _ Confinement, att *Attestation) (Cleanup, error) {
	att.Degraded = append(att.Degraded,
		"os: non-Linux host — dedicated uid, cgroup, seccomp and landlock are unavailable; only env scoping applies, and it is BYPASSABLE without uid isolation (/proc/<engine>/environ)")
	return noopCleanup, nil
}

// CloseSpawnFD is a no-op off Linux (no CgroupFD is set).
func CloseSpawnFD(_ *exec.Cmd) {}
