// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !linux && !darwin

package tui

// inputPending has no portable descriptor poll here, so an Esc is read as soon
// as it arrives. Arrow keys still decode when the terminal delivers the whole
// sequence in one read, which is the usual case; the frame itself is supported
// on the platforms this repository builds.
func inputPending(int, int) bool { return false }
