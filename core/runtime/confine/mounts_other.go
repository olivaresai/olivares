// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !linux

package confine

import (
	"fmt"
	"os/exec"
	"runtime"
)

// WrapMounts: mounts are Linux's; elsewhere the child runs without them.
func WrapMounts(*exec.Cmd, Mounts) error {
	return fmt.Errorf("%w (%s)", ErrMountsUnavailable, runtime.GOOS)
}

// ConsumeMounts returns args unchanged: no other system produces the layer.
func ConsumeMounts(args []string) ([]string, error) { return args, nil }

// IsMountsProbe is always false off Linux.
func IsMountsProbe([]string) bool { return false }

// RunMountsProbe is never reached off Linux.
func RunMountsProbe([]string) int { return 126 }
