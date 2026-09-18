// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The id the shell launcher's field carries, in its own module so the KEYBOARD side of
// the shell can focus it without importing the launcher — which would pull the agentops
// queries into every chunk that only wanted to move focus.
export const LAUNCHER_INPUT_ID = 'shell-launcher-input'
