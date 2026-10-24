// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !linux && !darwin

package tui

import "os"

// resizeSignals never fires here: the shell keeps the size it started with.
func resizeSignals() (<-chan os.Signal, func()) { return nil, func() {} }
