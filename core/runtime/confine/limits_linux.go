// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package confine

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// applyLimits only lowers the limits explicitly supplied by the caller.
func applyLimits(l Limits) error {
	if err := lowerLimit(unix.RLIMIT_CORE, l.CoreSize); err != nil {
		return fmt.Errorf("turn off core dumps: %w", err)
	}
	if err := lowerLimit(unix.RLIMIT_FSIZE, l.FileSize); err != nil {
		return fmt.Errorf("limit the file size: %w", err)
	}
	// getpriority returns 20-nice; raising the nice value needs no privilege.
	prio, err := unix.Getpriority(unix.PRIO_PROCESS, 0)
	if err != nil {
		return fmt.Errorf("read the priority: %w", err)
	}
	if err := unix.Setpriority(unix.PRIO_PROCESS, 0, min(20-prio+l.Nice, 19)); err != nil {
		return fmt.Errorf("lower the priority: %w", err)
	}
	return nil
}

func lowerLimit(resource int, value uint64) error {
	var cur unix.Rlimit
	if err := unix.Getrlimit(resource, &cur); err != nil {
		return err
	}
	next := unix.Rlimit{Cur: min(cur.Cur, value), Max: min(cur.Max, value)}
	return unix.Setrlimit(resource, &next)
}
