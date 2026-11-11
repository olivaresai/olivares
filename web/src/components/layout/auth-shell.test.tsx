// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it, vi } from 'vitest'
import { renderIntel, screen, userEvent, within } from '@/test/intel'
import { AuthSheet, AuthShell } from './auth-shell'

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

describe('AuthShell page', () => {
  it('opens with the lockup: the four-row mark and the traced name', () => {
    renderIntel(
      <AuthShell>
        <h1>Sign in</h1>
      </AuthShell>,
    )
    const main = screen.getByRole('main')
    // One lockup on the page, and the name is the traced wordmark (an image
    // named "Olivares AI"), never live text a font could redraw.
    const name = within(main).getByRole('img', { name: 'Olivares AI' })
    expect(within(main).queryByText('Olivares AI')).toBeNull()
    // Beside it, the mark: the arc and four ledger rows, one of them orange.
    const mark = name.previousElementSibling
    expect(mark?.tagName.toLowerCase()).toBe('svg')
    expect(mark).toHaveAttribute('aria-hidden', 'true')
    expect(mark?.querySelectorAll('path')).toHaveLength(5)
    expect(mark?.querySelectorAll('path.stroke-accent')).toHaveLength(1)
    // The lockup comes before the page it names.
    const heading = screen.getByRole('heading', { level: 1 })
    expect(
      name.compareDocumentPosition(heading) & Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy()
  })

  it('is one column on the frame ground, with no slogan beside the sheet', () => {
    const { container } = renderIntel(
      <AuthShell>
        <h1>Sign in</h1>
      </AuthShell>,
    )
    expect(container.firstElementChild).toHaveClass('bg-frame')
    expect(
      screen.queryByText('One place to govern every AI agent you run.'),
    ).toBeNull()
    expect(screen.queryAllByRole('list')).toHaveLength(0)
    // Exactly one h1 on the page, and it is the sheet's: the rule for every
    // route is `h1=1`.
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

it('contains the lockup and the sheet in one primary landmark', () => {
  renderIntel(
    <AuthShell>
      <AuthSheet>
        <h1>Pair this device</h1>
      </AuthSheet>
    </AuthShell>,
  )
  const main = screen.getByRole('main')
  expect(main).toContainElement(
    screen.getByRole('img', { name: 'Olivares AI' }),
  )
  expect(main).toContainElement(
    screen.getByRole('heading', { name: 'Pair this device' }),
  )
})

describe('AuthSheet', () => {
  it('is one sheet on the canvas, raised by its shadow and drawn with no outline', () => {
    renderIntel(
      <AuthShell>
        <AuthSheet>
          <h1>Sign in</h1>
        </AuthSheet>
      </AuthShell>,
    )
    const sheets = document.querySelectorAll('[data-slot="auth-sheet"]')
    expect(sheets).toHaveLength(1)
    const sheet = sheets[0]
    expect(sheet).toContainElement(screen.getByRole('heading', { level: 1 }))
    expect(sheet).toHaveClass(
      'bg-canvas',
      'rounded-panel',
      'shadow-[var(--shadow-raised)]',
    )
    expect(sheet.className).not.toMatch(/(^|\s)border(-|\s|$)/)
  })

  it('keeps the classes a page adds', () => {
    renderIntel(
      <AuthSheet className="gap-6">
        <p>card</p>
      </AuthSheet>,
    )
    expect(screen.getByText('card').parentElement).toHaveClass('gap-6')
  })
})
