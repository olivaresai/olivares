// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !linux

package storage

import "errors"

var errUnsupported = errors.New("the storage inventory reads a Linux kernel only")

// LinuxKernel answers nothing here, so every inventory read fails at the boot id.
type LinuxKernel struct{}

// BootID implements Kernel.
func (LinuxKernel) BootID() (string, error) { return "", errUnsupported }

// LoopStatus implements Kernel.
func (LinuxKernel) LoopStatus(string, uint64) (LoopStatus, error) {
	return LoopStatus{}, errUnsupported
}

// FirstMiB implements Kernel.
func (LinuxKernel) FirstMiB(string, uint64) (string, error) { return "", errUnsupported }

// Holders implements Kernel.
func (LinuxKernel) Holders(string, uint64) ([]string, error) { return nil, errUnsupported }
