// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !linux

package confine

import (
	coreconfine "github.com/olivaresai/olivares/core/runtime/confine"
	"runtime"
)

func probe() State {
	return State{Mode: ModeNone, Reason: "process confinement is implemented with Linux Landlock; on " + runtime.GOOS +
		" a session runs as the engine user and can read what that user can"}
}

func sessionPolicy(p Policy, _ string) coreconfine.Policy {
	return coreconfine.Policy{ReadWrite: p.ReadWrite, ReadOnly: p.ReadOnly, Protect: p.Protect, Sealed: p.Sealed}
}

// defaultWritable is empty: this build grants nothing by default.
func defaultWritable() []string { return nil }
