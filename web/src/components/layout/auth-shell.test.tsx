// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it, vi } from 'vitest'
import { renderIntel, screen, userEvent } from '@/test/intel'
import { AuthShell } from './auth-shell'

// The server answers with a version. The signed-out footer must not print it: the
// version belongs to Settings › About and the avatar menu only.
vi.mock('@/lib/hooks/use-server-info', () => ({
  useServerInfo: () => ({ data: { version: '0.7' } }),
}))

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

  it('reaches the card in ONE tab stop past the skip link and the theme toggle', async () => {
    // The point of the statement column is that it costs a keyboard user nothing.
    // It holds no control, so the tab order is unchanged: skip link, theme toggle,
    // then the form. A link or a button in that column would make the signed-out
    // screen slower to use from a keyboard than it was before the redesign.
    const user = userEvent.setup()
    renderIntel(
      <AuthShell>
        <button type="button">Sign in</button>
      </AuthShell>,
    )
    await user.tab()
    expect(document.activeElement).toHaveTextContent('Skip to content')
    await user.tab()
    expect(document.activeElement).toHaveAttribute('aria-label', 'Toggle theme')
    await user.tab()
    expect(document.activeElement).toHaveTextContent('Sign in')
  })
})

describe('AuthShell statement column', () => {
  it('says what the product is, as text and not as a heading', () => {
    renderIntel(
      <AuthShell>
        <h1>Sign in</h1>
      </AuthShell>,
    )
    expect(
      screen.getByText('One place to govern every AI agent you run.'),
    ).toBeInTheDocument()
    // Exactly one h1 on the page, and it is the card's — the at:gate rule for every
    // route is `h1=1`, and a statement rendered as a heading would break it on three
    // unauthenticated screens at once.
    expect(screen.getAllByRole('heading', { level: 1 })).toHaveLength(1)
  })

  it('adds no landmark', () => {
    // `<aside>` would put a `complementary` landmark on the one screen a screen-reader
    // user wants to leave immediately, and change the landmark inventory of every
    // unauthenticated route.
    const { container } = renderIntel(
      <AuthShell>
        <p>card</p>
      </AuthShell>,
    )
    expect(container.querySelector('aside')).toBeNull()
    expect(screen.queryAllByRole('complementary')).toHaveLength(0)
  })

  it('prints neither host nor version on signed-out screens', () => {
    renderIntel(
      <AuthShell>
        <p>card</p>
      </AuthShell>,
    )
    expect(screen.queryByTestId('deployment-identity')).toBeNull()
    expect(screen.queryByText(window.location.host)).toBeNull()
    expect(screen.queryByText('0.7')).toBeNull()
  })
})

it('contains the statement and card in one primary landmark', () => {
  renderIntel(
    <AuthShell>
      <h1>Pair this device</h1>
    </AuthShell>,
  )
  const main = screen.getByRole('main')
  expect(main).toContainElement(
    screen.getByText('One place to govern every AI agent you run.'),
  )
  expect(main).toContainElement(
    screen.getByRole('heading', { name: 'Pair this device' }),
  )
})
