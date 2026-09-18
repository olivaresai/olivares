// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { PageHeader } from './page-header'
import {
  PageActionsProvider,
  PagePrimaryAction,
  PageSecondaryActions,
} from './page-actions'

/** A page: the header, then content that declares the screen's verb from inside it. */
function Page({
  verb = true,
  secondary = false,
  onClick = () => {},
}: {
  verb?: boolean
  secondary?: boolean
  onClick?: () => void
}) {
  return (
    <PageActionsProvider>
      <PageHeader
        title="Catalog"
        actions={<button type="button">Refresh</button>}
      />
      <main>
        <p>rows</p>
        {verb && (
          <PagePrimaryAction>
            <button type="button" onClick={onClick}>
              New entry
            </button>
          </PagePrimaryAction>
        )}
        {secondary && (
          <PageSecondaryActions>
            <button type="button">Run sweep</button>
          </PageSecondaryActions>
        )}
      </main>
    </PageActionsProvider>
  )
}

function controlRow(): HTMLElement {
  const heading = screen.getByRole('heading', { level: 1 })
  // The header's control row is the sibling of the title block inside the title row.
  const row =
    heading.closest('div')?.parentElement?.parentElement?.lastElementChild
  if (!(row instanceof HTMLElement)) throw new Error('no control row')
  return row
}

describe('a tab declares the verb, the page header renders it', () => {
  it('puts the declared verb inside the page header', () => {
    render(<Page />)
    const verb = screen.getByRole('button', { name: 'New entry' })
    expect(controlRow().contains(verb)).toBe(true)
  })

  it('leaves it LAST, after the secondary controls', () => {
    render(<Page secondary />)
    const names = [...controlRow().querySelectorAll('button')].map(
      (b) => b.textContent,
    )
    expect(names).toEqual(['Refresh', 'Run sweep', 'New entry'])
  })

  it('takes the verb away when the tab that declared it unmounts', () => {
    const { rerender } = render(<Page />)
    expect(screen.queryByRole('button', { name: 'New entry' })).not.toBeNull()
    rerender(<Page verb={false} />)
    expect(screen.queryByRole('button', { name: 'New entry' })).toBeNull()
  })

  it('keeps the click on the button the tab wrote', async () => {
    // The portal moves the DOM node, never the handler: a verb that renders in the
    // header and fires nothing would look identical in a capture.
    const onClick = vi.fn()
    render(<Page onClick={onClick} />)
    await userEvent.click(screen.getByRole('button', { name: 'New entry' }))
    expect(onClick).toHaveBeenCalledTimes(1)
  })

  it('renders the verb IN PLACE when no provider hosts it, never nothing', () => {
    // The fallback. Deleting the verb silently is the failure this console paid for once
    // already (`cn` and the type scale); wrong-but-visible is the safe direction.
    render(
      <main>
        <PagePrimaryAction>
          <button type="button">New entry</button>
        </PagePrimaryAction>
      </main>,
    )
    expect(screen.getByRole('button', { name: 'New entry' })).not.toBeNull()
  })

  it('renders the control row even when the page declares nothing', () => {
    // The header cannot see a tab that has not mounted yet, so the row is
    // unconditional; it must still be an empty box rather than a gap.
    render(
      <PageActionsProvider>
        <PageHeader title="Audit" />
      </PageActionsProvider>,
    )
    expect(controlRow().querySelectorAll('button')).toHaveLength(0)
  })
})

describe('the slot does not disturb what it hosts', () => {
  it('keeps focus on the verb across a header re-render', async () => {
    // ⛔ THE DEFECT THIS PINS. `ref={(el) => …}` is a NEW function on every render, so
    //    React detaches and re-attaches it each time the header re-renders. If the slot
    //    re-appends its host unconditionally, that MOVES a live DOM subtree, and moving
    //    a node that contains the focused element blurs it — an operator who tabbed to
    //    *New entry* loses focus the next time anything in the header changes, which on
    //    a list screen is every poll.
    function Screen({ n }: { n: number }) {
      return (
        <PageActionsProvider>
          <PageHeader title="Catalog" actions={<span>{n} rows</span>} />
          <main>
            <PagePrimaryAction>
              <button type="button">New entry</button>
            </PagePrimaryAction>
          </main>
        </PageActionsProvider>
      )
    }
    const { rerender } = render(<Screen n={1} />)
    const verb = screen.getByRole('button', { name: 'New entry' })
    verb.focus()
    expect(document.activeElement).toBe(verb)
    rerender(<Screen n={2} />)
    expect(screen.getByText('2 rows')).not.toBeNull()
    expect(
      document.activeElement,
      'the verb still has focus after the header re-rendered',
    ).toBe(screen.getByRole('button', { name: 'New entry' }))
  })
})
