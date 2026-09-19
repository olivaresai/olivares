// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The id the work composer's field carries, in its own module so the KEYBOARD side of
// the shell can focus it without importing the composer — which would pull the agentops
// queries into every chunk that only wanted to move focus.
//
// It is also how `/` behaves honestly now that the composer is not in the shell: the
// key focuses the field WHERE ONE IS MOUNTED, and where none is, `shortcuts.tsx` sends
// the operator to the screen that mounts one. A missing element is the signal, so this
// module is the whole coupling.
export const COMPOSER_INPUT_ID = 'work-composer-input'
