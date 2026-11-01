// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !linux

package agenttoolsapi

// ollamaListenerHeldBy has no procfs to read off Linux: the readiness answer stands alone.
// ponytail: no listener ownership proof on macOS, add one (libproc) when macOS runs a shared node.
func ollamaListenerHeldBy(int, string) error { return nil }
