// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !linux && !darwin && !freebsd

package serverhandover

import "syscall"

const reuseSupported = false

// controlReusePort is a no-op where SO_REUSEPORT is unavailable: Listen returns a
// plain listener and Supported() is false, so the caller falls back to a
// drain-then-restart (a brief accept gap) instead of an overlapping handover.
func controlReusePort(_, _ string, _ syscall.RawConn) error { return nil }
