// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !(linux && (amd64 || arm64))

package invocation

import "errors"

var errUnsupported = errors.New("the helper seam attests connections on Linux amd64 and arm64 only")

// Linux answers nothing here, so no connection is attested and every request is refused.
type Linux struct{}

// PeerCred implements Kernel.
func (Linux) PeerCred(int) (int, uint32, error) { return 0, 0, errUnsupported }

// PeerPidfd implements Kernel.
func (Linux) PeerPidfd(int) (int, error) { return 0, errUnsupported }

// PidOf implements Kernel.
func (Linux) PidOf(int) (int, error) { return 0, errUnsupported }

// Cgroup implements Kernel.
func (Linux) Cgroup(int) (string, error) { return "", errUnsupported }

// Stat implements Kernel.
func (Linux) Stat(int) (string, error) { return "", errUnsupported }

// AccountOf implements Kernel.
func (Linux) AccountOf(uint32) (string, error) { return "", errUnsupported }

// Alive implements Kernel.
func (Linux) Alive(int) error { return errUnsupported }

// Close implements Kernel.
func (Linux) Close(int) {}
