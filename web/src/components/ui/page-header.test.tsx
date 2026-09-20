// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE HEADER CONTRACT, and only the parts of it that are CONTRACT.
//
// A review of the console recorded the front door as "six read-only cards, no action
// anywhere on the page". The repair is not a button; it is a header that has a place
// for the verb, so that a screen offering nothing shows it by leaving that place empty
// rather than by burying the verb among filters. What is asserted below is therefore
// the ORDER and the SLOT, never the spacing: a test that pins padding turns every
// design change into a red cell and teaches the next author to delete the test.
import { afterEach, describe, expect, it } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { Boxes } from 'lucide-react'
import { clippingAncestors } from '@/test/clipping'
import { stubViewportWidth } from '@/test/viewport'
import { Button } from './button'
import { PageHeader, WORK_CHROME_ROW, plainText } from './page-header'

/** The title row itself — the element the `as` prop emits, not the block around it. */
function titleRow(container: HTMLElement): HTMLElement {
  const header = container.querySelector(
    '[data-slot="page-header"]',
  ) as HTMLElement
  return header.firstElementChild as HTMLElement
}

afterEach(() => stubViewportWidth(1440))

/**
 * EVERY TRUNCATING ELEMENT IN A PAGE HEADER OWES THE READER A `title`.
 *
 * The header truncates its description BY DESIGN — that is what made it one line
 * instead of three — so the full text has to be reachable from it or the line is the
 * console hiding what it chose not to paint. Measured at 1440: the two session screens
 * cut 1454 px of description into 797 with no tooltip, and the round before had one.
 * This walks the rendered header, so the next description written as a node is caught
 * here and not in a browser.
 */
function truncatedWithoutTitle(root: HTMLElement): string[] {
  const out: string[] = []
  for (const el of root.querySelectorAll('.truncate')) {
    if (el.closest('[title]')) continue
    out.push((el.textContent ?? '').trim().slice(0, 40))
  }
  return out
}

describe('PageHeader', () => {
  it('renders the title as the page h1', () => {
    render(<PageHeader title="Inventory" />)
    expect(
      screen.getByRole('heading', { level: 1, name: 'Inventory' }),
    ).toBeInTheDocument()
  })

  it('puts the primary action LAST, after the secondary controls', () => {
    render(
      <PageHeader
        title="Policies"
        actions={<Button>Filter</Button>}
        primaryAction={<Button variant="primary">New policy</Button>}
      />,
    )
    const buttons = screen.getAllByRole('button').map((b) => b.textContent)
    expect(buttons).toEqual(['Filter', 'New policy'])
  })

  it('renders no control row at all when the screen offers nothing', () => {
    // The honest empty header: a screen with no verb must not grow a placeholder.
    render(<PageHeader title="Read-only view" />)
    expect(screen.queryByRole('button')).toBeNull()
  })

  it('renders notices under the header, not inside the title row', () => {
    render(
      <PageHeader
        title="Compliance"
        description="What the platform can evidence."
        notices={<p>Aggregates are a floor.</p>}
      />,
    )
    const h1 = screen.getByRole('heading', { level: 1 })
    const notice = screen.getByText('Aggregates are a floor.')
    // DOCUMENT_POSITION_FOLLOWING: the notice comes after the heading in the document,
    // and is not a descendant of the title row.
    expect(
      h1.compareDocumentPosition(notice) & Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy()
    expect(h1.parentElement?.contains(notice)).toBe(false)
  })

  it('emits the element the caller asks for, so consolidating the two headers moved no DOM', () => {
    // `IntelPage` has always emitted `<header>`; this primitive has always emitted a
    // `<div>`. Both now come from here, and the prop is what keeps that true.
    const { container, rerender } = render(<PageHeader title="A" />)
    expect(container.querySelector('header')).toBeNull()
    rerender(<PageHeader as="header" title="A" />)
    expect(container.querySelector('header')).not.toBeNull()
  })

  it('takes its size from the type scale, not from a hand-written stack', () => {
    // The one class assertion worth keeping: `text-title` is the token
    // (web/tokens/primitives.tokens.json, the `type` group) that carries size, leading,
    // tracking AND weight together. Six places used to spell that stack by hand with
    // three different sizes; this is the check that says the heading reads the ladder.
    //
    // It was `text-display` until the work-first pass stepped the heading down one
    // rung. The
    // assertion is UPDATED rather than deleted, and that is the point of it: the value
    // moved in the token source and in this one line, not in 77 views.
    render(<PageHeader title="Estate" />)
    expect(screen.getByRole('heading', { level: 1 })).toHaveClass('text-title')
  })

  it('keeps the description in the document, on the title line', () => {
    // The description is not deleted and not hidden — it moves onto the
    // heading's own line as one truncating element. A screen-reader user reads exactly
    // what they read before. This is the check that says so.
    render(
      <PageHeader
        title="Sessions"
        description="Every agent session on this plane."
      />,
    )
    const h1 = screen.getByRole('heading', { level: 1 })
    const description = screen.getByText('Every agent session on this plane.')
    expect(description).toBeInTheDocument()
    expect(h1.parentElement?.contains(description)).toBe(true)
  })

  it('carries the whole description on `title`, string or node', () => {
    const sentence =
      'Every agent session on this plane — discovered and launched.'
    const { container, rerender } = render(
      <PageHeader title="Sessions" description={sentence} />,
    )
    const header = container.querySelector(
      '[data-slot="page-header"]',
    ) as HTMLElement
    expect(screen.getByText(sentence)).toHaveAttribute('title', sentence)
    expect(truncatedWithoutTitle(header)).toEqual([])

    // The shape the session screens use: the sentence, then the counts and the scope
    // note as nodes. `title` takes a string, so this is the case that had none.
    rerender(
      <PageHeader
        title="Sessions"
        description={
          <>
            {sentence}{' '}
            <span data-testid="summary">
              6 total{' · '}
              <span data-testid="scope">Tenant-wide</span>
            </span>
          </>
        }
      />,
    )
    const described = screen.getByTestId('summary').parentElement as HTMLElement
    expect(described.getAttribute('title')).toBe(
      `${sentence} 6 total · Tenant-wide`,
    )
    expect(
      truncatedWithoutTitle(
        container.querySelector('[data-slot="page-header"]') as HTMLElement,
      ),
    ).toEqual([])
  })

  it('offers no tooltip at all rather than half of one', () => {
    // A component can render anything — a relative time, a live count — and half a
    // sentence presented as the whole of a truncated one is worse than none.
    function Live() {
      return <span>12 running</span>
    }
    expect(plainText(<>Sessions · {'6 total'}</>)).toBe('Sessions · 6 total')
    expect(plainText(<>{['a', 'b']}</>)).toBe('ab')
    expect(plainText(undefined)).toBe('')
    expect(
      plainText(
        <>
          Sessions · <Live />
        </>,
      ),
    ).toBeUndefined()
  })

  /**
   * THE ONE-LINE SHAPE, WHICH HAD NO ORACLE AT ALL.
   *
   * The round before this restored `flex items-center justify-between` here after a pass
   * had made it `flex flex-col … sm:flex-row sm:items-center`, and the whole test file
   * stayed green through both: 4 files / 49 passed either way. Stacking is the obvious
   * answer to "it does not fit" and it is the wrong one on this element — measured in a
   * browser at 390 px, `flex-col` put the controls UNDER the title and took the header
   * block from 28 px to 55, on a phone, which has LESS vertical room than a desktop, not
   * more. The budget is 40.
   */
  it('is ONE row at every width: the controls never stack under the title', () => {
    const { container } = render(
      <PageHeader
        title="Inventory"
        description="Everything the estate holds"
        primaryAction={<Button>New item</Button>}
      />,
    )
    const row = titleRow(container)
    const classes = row.className.split(/\s+/)
    expect(classes).toContain('flex')
    expect(classes).toContain('items-center')
    // The two forms that reintroduce the second row, named rather than implied: a
    // column at every width, and a column that becomes a row only at `sm`.
    expect(classes).not.toContain('flex-col')
    expect(classes).not.toContain('sm:flex-row')
    // Two children and no more: the title group and the control group. A third would be
    // something laid out beside them with nothing deciding which gives way.
    expect(row.children).toHaveLength(2)
    // The title group holds the heading and the description on ONE baseline, and the
    // heading is the one that does not give way.
    const titleGroup = row.children[0] as HTMLElement
    expect(titleGroup.className).toContain('items-baseline')
    expect(titleGroup.querySelector('h1')!.className).toContain('shrink-0')
  })

  it('the controls give way before the screen name does', () => {
    const { container } = render(
      <PageHeader
        title="Audit ledger"
        description="The tamper-evident evidence chain."
        actions={<Button>Filters</Button>}
        primaryAction={<Button>Export</Button>}
      />,
    )
    const h1 = screen.getByRole('heading', {
      level: 1,
      name: 'Audit ledger',
    })
    expect(h1.className.split(/\s+/)).toEqual(
      expect.arrayContaining(['shrink-0', 'whitespace-nowrap']),
    )
    expect(h1.className.split(/\s+/)).not.toContain('truncate')
    const titleGroup = h1.parentElement as HTMLElement
    expect(titleGroup.className.split(/\s+/)).not.toContain('overflow-hidden')
    expect(titleGroup.className.split(/\s+/)).not.toContain('min-w-0')
    const actions = container.querySelector('[data-slot="page-actions"]')!
      .parentElement as HTMLElement
    expect(actions.className.split(/\s+/)).toContain('min-w-0')
    expect(actions.className.split(/\s+/)).not.toContain('shrink-0')
  })

  it('does not render the deprecated icon chip', () => {
    // 33 views still pass `icon`. It compiles and it paints nothing: 36 px of every
    // route's viewport for a third copy of a glyph the rail and the breadcrumb carry.
    const { container } = render(<PageHeader title="Estate" icon={Boxes} />)
    expect(container.querySelector('svg')).toBeNull()
  })
})

/**
 * THE PHONE DISCLOSURE, WHICH HAD NO ORACLE EITHER.
 *
 * Forcing `useIsPhone` to `false` left this file and `page-frames.test.tsx` green —
 * nothing pinned the toggle, its `aria-expanded`, or the rule that makes the collapse
 * safe: the panel is HIDDEN, not unmounted, so a control that registers something on
 * mount keeps doing it on a phone. That rule is the reason this is a disclosure and not
 * a narrower header, and it was the part nothing could see.
 */
describe('PageHeader — below sm the controls collapse, and nothing leaves', () => {
  it('phone: the verbs move behind a toggle that says whether it is open', async () => {
    const user = userEvent.setup()
    stubViewportWidth(390)
    render(
      <PageHeader
        title="Inventory"
        actions={<Button>Refresh</Button>}
        primaryAction={<Button>New item</Button>}
      />,
    )
    const toggle = screen.getByTestId('page-actions-toggle')
    expect(toggle).toHaveAttribute('aria-expanded', 'false')
    await user.click(toggle)
    expect(toggle).toHaveAttribute('aria-expanded', 'true')
    await user.click(toggle)
    expect(toggle).toHaveAttribute('aria-expanded', 'false')
  })

  it('phone, collapsed: the panel is hidden and its controls are still MOUNTED', () => {
    stubViewportWidth(390)
    const { container } = render(
      <PageHeader
        title="Inventory"
        actions={<Button>Refresh</Button>}
        primaryAction={<Button>New item</Button>}
      />,
    )
    const panel = container.querySelector(
      '[data-slot="page-actions"]',
    ) as HTMLElement
    expect(panel.className.split(/\s+/)).toContain('hidden')
    // Unmounting would be the easy collapse and the wrong one: a control that syncs
    // state on mount would stop syncing it on a phone.
    expect(screen.getByRole('button', { name: 'Refresh' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'New item' })).toBeInTheDocument()
  })

  it('desktop: no toggle at all, and the controls are in the row', () => {
    stubViewportWidth(1440)
    const { container } = render(
      <PageHeader
        title="Inventory"
        primaryAction={<Button>New item</Button>}
      />,
    )
    expect(screen.queryByTestId('page-actions-toggle')).toBeNull()
    const panel = container.querySelector(
      '[data-slot="page-actions"]',
    ) as HTMLElement
    expect(panel.className.split(/\s+/)).toContain('flex-row')
    // The row wraps and never scrolls: at 1024 px a seven-control screen put three actions
    // behind a scrollbar when this said `overflow-x-auto` (measured 2026-09-19).
    expect(panel.className.split(/\s+/)).toContain('flex-wrap')
    expect(panel.className.split(/\s+/)).not.toContain('overflow-x-auto')
    expect(panel.className.split(/\s+/)).not.toContain('hidden')
  })

  /**
   * WHERE THE CLIP LIVES, AND WHERE IT MUST NOT.
   *
   * ⛔ The panel opens `absolute top-full` inside this header, so anything between it and
   *    the page that clips overflow paints it and lets nothing touch it — measured at
   *    390×844 on four screens whose chrome row was `h-9 … overflow-hidden`:
   *    `elementFromPoint` at the panel's centre answered `th`, `td` or the document, and
   *    Launch, Refresh, Register and Export were unreachable below 639 px.
   *    The title still has to be cut, so the cut is on the block that overruns.
   */
  it("phone: the clip is on the title block, never on the panel's own ancestors", async () => {
    const user = userEvent.setup()
    stubViewportWidth(390)
    const { container } = render(
      <PageHeader
        title="Inventory"
        description="Everything the estate holds"
        actions={<Button>Refresh</Button>}
        primaryAction={<Button>New item</Button>}
      />,
    )
    const row = titleRow(container)
    const titleGroup = row.children[0] as HTMLElement
    // The name is not clipped here; the description still truncates on its own.
    expect(titleGroup.className.split(/\s+/)).not.toContain('overflow-hidden')
    expect(titleGroup.querySelector('p')!.className.split(/\s+/)).toContain(
      'truncate',
    )
    await user.click(screen.getByTestId('page-actions-toggle'))
    const panel = container.querySelector(
      '[data-slot="page-actions"]',
    ) as HTMLElement
    // Joined, so a failure NAMES the element that cut the panel on its one line.
    expect(clippingAncestors(panel, container).join(' | ')).toBe('')
  })

  it('phone with nothing to offer: no toggle that opens an empty panel', () => {
    stubViewportWidth(390)
    render(<PageHeader title="Inventory" />)
    // The control row always renders (a tabbed screen fills it through a portal after
    // this header mounts), so "is it empty" is a DOM question — and a toggle over an
    // empty panel is a control that does nothing on every route that offers nothing.
    expect(screen.queryByTestId('page-actions-toggle')).toBeNull()
  })
})

// WHO GIVES WAY ON A ROW SHARED WITH A TAB STRIP. jsdom lays nothing out, so what is pinned
// here is the structure the browser measurement depends on; the measurement itself (no
// control over another control, no strip scrolling while the description still holds
// room, at 1440 and 1180) is a browser instrument, because only a browser can see it.
describe('PageHeader on a row shared with a tab strip', () => {
  it('the description asks for no width of its own and takes what is left', () => {
    render(
      <PageHeader
        title="Agents"
        description="A long sentence about this screen."
      />,
    )
    const sentence = screen.getByText('A long sentence about this screen.')
    const classes = sentence.className.split(/\s+/)
    expect(classes).toEqual(
      expect.arrayContaining(['w-0', 'min-w-0', 'flex-1', 'truncate']),
    )
    // …and it still owes the reader the whole sentence.
    expect(sentence).toHaveAttribute(
      'title',
      'A long sentence about this screen.',
    )
  })

  it('the name does not wrap, so the smallest the header can be is the whole name', () => {
    render(<PageHeader title="Control console" description="Who gets in." />)
    const name = screen.getByRole('heading', {
      level: 1,
      name: 'Control console',
    })
    expect(name.className.split(/\s+/)).toEqual(
      expect.arrayContaining(['shrink-0', 'whitespace-nowrap']),
    )
  })

  it('the shared row never lets the header fall below its name', () => {
    const classes = WORK_CHROME_ROW.split(/\s+/)
    expect(classes).toContain('grid')
    expect(classes).toContain(
      'grid-cols-[minmax(min-content,1fr)_minmax(0,auto)]',
    )
    expect(classes).toContain('h-9')
  })

  // WHERE A PHONE'S PANEL OF COLLAPSED CONTROLS HANGS FROM. Beside a tab strip the header is
  // only as wide as its name, so a panel hung from it starts mid-screen and runs off the
  // left edge of a 390 px view (measured: two screens of four, the verbs out of reach).
  // There the ROW is the positioned ancestor; alone, the header's own controls are.
  it('hangs the phone panel from the row when asked, and from its own controls otherwise', () => {
    const { unmount } = render(
      <PageHeader title="Agents" actions={<Button>Refresh</Button>} />,
    )
    const own = document.querySelector(
      '[data-slot="page-actions"]',
    )!.parentElement!
    expect(own.className.split(/\s+/)).toContain('relative')
    unmount()

    render(
      <PageHeader
        title="Agents"
        actionsPanelAnchor="row"
        actions={<Button>Refresh</Button>}
      />,
    )
    const inRow = document.querySelector(
      '[data-slot="page-actions"]',
    )!.parentElement!
    expect(inRow.className.split(/\s+/)).not.toContain('relative')
    expect(WORK_CHROME_ROW.split(/\s+/)).toContain('relative')
  })
})
