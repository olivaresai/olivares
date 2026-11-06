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

// UserBusExposed is false off Linux: the user bus is systemd's.
func UserBusExposed(*exec.Cmd) bool { return false }

// HideUserBus does nothing off Linux.
func HideUserBus(*exec.Cmd) error { return nil }

// UserBusUnhideable is nil off Linux: there is no user manager to hide.
func UserBusUnhideable() error { return nil }

// UserBusDirs is empty off Linux.
func UserBusDirs() []string { return nil }

// IsMountsProbe is always false off Linux.
func IsMountsProbe([]string) bool { return false }

// RunMountsProbe is never reached off Linux.
func RunMountsProbe([]string) int { return 126 }
