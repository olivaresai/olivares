// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package confine

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// The session limits. The helper applies them to the session process before it
// runs the program, and everything that process starts inherits them. They are
// generous on purpose: they keep one runaway process from taking the node and
// the console with it, not a build from running.
//
// There is no memory limit here on purpose: RLIMIT_DATA and RLIMIT_AS count
// address space a process reserves without using, and the Go race detector,
// AddressSanitizer and V8 reserve terabytes of it, so those builds would fail. A
// memory, CPU or process-count limit for a whole session needs cgroups.

// applySessionLimits sets the session limits on this process. A limit is only
// ever lowered, so a node that already runs the engine tighter keeps its own.
func applySessionLimits() error {
	if err := lowerLimit(unix.RLIMIT_CORE, 0); err != nil {
		return fmt.Errorf("turn off core dumps: %w", err)
	}
	if err := lowerLimit(unix.RLIMIT_FSIZE, SessionFileSizeLimit); err != nil {
		return fmt.Errorf("limit the file size: %w", err)
	}
	// getpriority returns 20-nice; raising the nice value needs no privilege.
	prio, err := unix.Getpriority(unix.PRIO_PROCESS, 0)
	if err != nil {
		return fmt.Errorf("read the priority: %w", err)
	}
	if err := unix.Setpriority(unix.PRIO_PROCESS, 0, min(20-prio+SessionNice, 19)); err != nil {
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
