// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The Sessions rail keeps its Working section shut, so a test that reaches for
// the row of a running session opens the fold first, as a person would.
import { fireEvent, screen } from '@testing-library/react'

export function openWorkingFold(): void {
  const toggle = screen.queryByRole('button', { name: /^Working\s*\d+$/ })
  if (toggle?.getAttribute('aria-expanded') === 'false') fireEvent.click(toggle)
}
