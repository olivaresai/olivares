// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it } from 'vitest'
import { renderIntel, userEvent } from '@/test/intel'
import { AuthShell } from './auth-shell'

describe('AuthShell keyboard bypass', () => {
  it('exposes skip-to-content that focuses the main landmark', async () => {
    const user = userEvent.setup()
    renderIntel(
      <AuthShell>
        <button type="button">Sign in</button>
      </AuthShell>,
    )
    const skip = document.querySelector<HTMLAnchorElement>(
      'a[href="#main-content"]',
    )
    expect(skip).not.toBeNull()
    expect(skip).toHaveTextContent('Skip to content')
    skip!.focus()
    await user.click(skip!)
    expect(document.getElementById('main-content')).toHaveFocus()
  })
})
