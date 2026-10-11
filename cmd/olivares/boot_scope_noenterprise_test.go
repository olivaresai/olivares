//go:build !enterprise

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package main

import "testing"

// resetBootProcessScope is the seam a test calls before it boots an engine. The
// Business composition keeps some state per process, not per engine, and refuses
// a second boot in one process; there it returns that state to never-booted.
// Community keeps none, so there is nothing to reset.
func resetBootProcessScope(*testing.T) {}
