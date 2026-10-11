// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE HEAD OF THE LIST: the name, the count, `+` and the `⋯` that holds the other views.
import {
  act,
  fireEvent,
  render as renderPlain,
  screen,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactElement } from 'react'
import { describe, expect, it, vi } from 'vitest'
import { TooltipProvider } from '@/components/ui/tooltip'
import { SessionsListHeader, SessionsViewHeader } from './list-header'
import './i18n'

/** The app mounts one tooltip provider; so does every test that paints an icon button. */
function render(ui: ReactElement) {
  return renderPlain(<TooltipProvider delayDuration={0}>{ui}</TooltipProvider>)
}

function renderHeader(
  over: Partial<Parameters<typeof SessionsListHeader>[0]> = {},
) {
  const onNew = vi.fn()
  const onView = vi.fn()
  render(
    <SessionsListHeader
      count={12}
      countLabel="12 sessions · 3 running"
      onNew={onNew}
      views={['table', 'workspaces', 'profiles']}
      onView={onView}
      streamFailed={false}
      {...over}
    />,
  )
  return { onNew, onView }
}

describe('SessionsListHeader', () => {
  it('is one 48 px row: the name as the heading and the count as a figure', () => {
    renderHeader()
    const heading = screen.getByRole('heading', { level: 1 })
    expect(heading).toHaveTextContent('Sessions')
    const row = heading.parentElement as HTMLElement
    expect(row.className).toContain('h-12')
    const count = screen.getByTestId('sessions-summary')
    expect(count).toHaveTextContent(/^12$/)
    // The figure is for the eye; the count in words is for a reader, once.
    expect(count).toHaveAttribute('aria-hidden', 'true')
    expect(screen.getByText('12 sessions · 3 running')).toHaveClass('sr-only')
    expect(count.className).toContain('tabular-nums')
  })

  it('prints no count for an empty list', () => {
    renderHeader({ count: 0, countLabel: '' })
    expect(screen.queryByTestId('sessions-summary')).toBeNull()
  })

  it('has no Refresh button and no Live chip: the list is live', () => {
    renderHeader()
    expect(screen.queryByRole('button', { name: /refresh/i })).toBeNull()
    expect(screen.queryByText('Live')).toBeNull()
  })

  it('starts a session from the + icon button, with a name and a hover', async () => {
    const user = userEvent.setup()
    const { onNew } = renderHeader()
    const add = screen.getByRole('button', { name: 'New session' })
    await user.hover(add)
    expect(await screen.findByRole('tooltip')).toHaveTextContent('New session')
    await user.click(add)
    expect(onNew).toHaveBeenCalledOnce()
  })

  it('offers no + when this person cannot start a session', () => {
    renderHeader({ onNew: undefined })
    expect(screen.queryByRole('button', { name: 'New session' })).toBeNull()
  })

  it('holds the table, the workspaces and the provider profiles in the ⋯ menu', async () => {
    const { onView } = renderHeader()
    const more = screen.getByRole('button', { name: 'More' })
    act(() => {
      fireEvent.keyDown(more, { key: 'Enter' })
    })
    const items = await screen.findAllByRole('menuitem')
    expect(items.map((i) => i.textContent)).toEqual([
      'Show as table',
      'Workspaces',
      'Provider profiles',
    ])
    act(() => {
      fireEvent.click(items[2])
    })
    expect(onView).toHaveBeenCalledWith('profiles')
  })

  it('lists only the views this person may open, and no menu when there are none', async () => {
    const first = render(
      <SessionsListHeader
        count={1}
        countLabel="1 session"
        views={['table']}
        onView={vi.fn()}
        streamFailed={false}
      />,
    )
    act(() => {
      fireEvent.keyDown(screen.getByRole('button', { name: 'More' }), {
        key: 'Enter',
      })
    })
    expect(await screen.findAllByRole('menuitem')).toHaveLength(1)
    first.unmount()
    render(
      <SessionsListHeader
        count={1}
        countLabel="1 session"
        views={[]}
        onView={vi.fn()}
        streamFailed={false}
      />,
    )
    expect(screen.queryByRole('button', { name: 'More' })).toBeNull()
  })

  it('says a failed stream in one quiet line, and nothing otherwise', () => {
    const { unmount } = render(
      <SessionsListHeader
        count={1}
        countLabel="1 session"
        views={[]}
        onView={vi.fn()}
        streamFailed
      />,
    )
    const note = screen.getByRole('status')
    expect(note).toHaveTextContent('Reconnecting…')
    expect(note.className).toContain('text-text-3')
    unmount()
    renderHeader()
    expect(screen.queryByRole('status')).toBeNull()
  })
})

describe('SessionsViewHeader', () => {
  it('names the view, shows the count and goes back to the list', async () => {
    const user = userEvent.setup()
    const onBack = vi.fn()
    render(
      <SessionsViewHeader
        title="Table"
        count={3}
        countLabel="3 sessions"
        onBack={onBack}
        actions={<button type="button">Extra</button>}
      />,
    )
    expect(screen.getByRole('heading', { level: 1 })).toHaveTextContent('Table')
    expect(screen.getByTestId('sessions-summary')).toHaveTextContent('3')
    await user.click(screen.getByRole('button', { name: 'Back to sessions' }))
    expect(onBack).toHaveBeenCalledOnce()
    expect(screen.getByRole('button', { name: 'Extra' })).toBeInTheDocument()
  })
})

describe('where the keyboard is', () => {
  it('lands on the menu button when the header appears for a person coming back', () => {
    renderHeader({ focusMenu: true })
    expect(screen.getByRole('button', { name: 'More' })).toHaveFocus()
  })

  it('leaves focus alone otherwise', () => {
    renderHeader()
    expect(screen.getByRole('button', { name: 'More' })).not.toHaveFocus()
  })

  it('lands on the back button of another view of the page', () => {
    render(
      <SessionsViewHeader
        title="Table"
        count={3}
        countLabel="3 sessions"
        onBack={() => {}}
      />,
    )
    expect(
      screen.getByRole('button', { name: 'Back to sessions' }),
    ).toHaveFocus()
  })

  it('shows a tooltip for an icon button on keyboard focus too', async () => {
    renderHeader()
    act(() => screen.getByRole('button', { name: 'New session' }).focus())
    expect(await screen.findByRole('tooltip')).toHaveTextContent('New session')
  })
})
