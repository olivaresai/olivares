// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// PartialSourceNote + PartialCoverageDisclosure: a tile whose figure is a floor keeps a
// COMPACT, named caption line inside its link (source, "Partial data", a few words
// naming the mechanism) and carries the COMPLETE existing sentence in a native
// disclosure OUTSIDE the link. Native `title` is not how keyboard, touch or
// assistive-technology users read a caption, and a button or <details> inside a
// tile-wide <a> is invalid markup — so the disclosure is a sibling of the link, the
// link keeps its destination and stays the only tab stop inside itself, and nothing
// is hover-only. These cases pin both kinds, the Home EstateTile cell and the Executive
// KpiTiles cell, and the no-partial rendering (a plain link, no disclosure).
//
// THE PRINTED REPORT gets the SAME rows from a print-only sibling OUTSIDE the closed
// <details> (`PartialCoveragePrint`, `hidden print:block` like the report cover header),
// and the interactive disclosure is hidden in print — the report never depends on the
// details' open state or on `::details-content`. Both copies come from one renderer and
// one `sources` array, so they say the same thing and retire together. jsdom applies
// no stylesheet, so the print/screen split is pinned here by the utility classes and
// by structure; the printed output itself is measured in the browser laboratory.
//
// jsdom toggles a <details> on summary click and makes the summary focusable; it does
// not implement Enter/Space activation, so real keyboard and touch are measured in the
// browser laboratory, not asserted here.
import type { ReactNode } from 'react'
import { describe, expect, it, vi } from 'vitest'
import userEvent from '@testing-library/user-event'
import { renderIntel, screen, within } from '@/test/intel'
import '@/features/_intel'
import './i18n'
import { EstateTile } from '@/features/home/components'
import { deriveUsage } from './derive'
import { inventorySummaryFixture, sessionsLiveFixture } from './fixtures'
import {
  CoverageLinkTile,
  KpiTiles,
  PartialCoverageDisclosure,
  PartialCoveragePrint,
  PartialSourceNote,
  type PartialCoverage,
} from './components'

vi.mock('@tanstack/react-router', () => ({
  useRouterState: () => '',
  Link: ({ children, to }: { children: ReactNode; to: string }) => (
    <a href={to}>{children}</a>
  ),
}))

// The compact captions (a few words naming the mechanism).
const SCAN_BRIEF = /scan ceiling reached/i
const PAGE_BRIEF = /one page loaded; there are more/i
// The complete existing sentences (unchanged `intel` strings).
const SCAN_FULL =
  /This aggregate reached the scan ceiling — it is partial, not an exact total\./
const PAGE_FULL =
  /The engine returned one page, not the whole set\. The total CANNOT be inferred from the rows loaded, and rows that are not shown may exist\./
const WHY = 'Why partial data?'

const INVENTORY = {
  source: 'Inventory',
  kind: 'aggregate' as const,
  testId: 'home-inventory-partial-note',
}
const SESSIONS = {
  source: 'Sessions',
  kind: 'page' as const,
  testId: 'home-sessions-partial-note',
}

function nestedTabStops(root: HTMLElement): Element[] {
  return [
    ...root.querySelectorAll('a, button, summary, details, [tabindex]'),
  ].filter((el) => el !== root)
}

/** The disclosure must be a SIBLING of the tile link — same grid cell, outside the <a>,
 *  after it in document (and so tab) order — and hidden in print. */
function expectSiblingDisclosure(link: HTMLElement, details: HTMLElement) {
  expect(details.tagName).toBe('DETAILS')
  expect(details.closest('a')).toBeNull()
  expect(link.contains(details)).toBe(false)
  expect(details.parentElement).toBe(link.parentElement)
  expect(
    link.compareDocumentPosition(details) & Node.DOCUMENT_POSITION_FOLLOWING,
  ).toBeTruthy()
  expect(details).not.toHaveAttribute('open')
  const summary = details.querySelector('summary')!
  expect(summary).toHaveTextContent(WHY)
  expect(summary.tabIndex).toBe(0)
  // The interactive disclosure is dropped from the printed report as a whole; the
  // report does not depend on the details' open state or on `::details-content`.
  expect(details).toHaveClass('print:hidden')
  expect(details.className).not.toMatch(/details-content/)
  expect(summary.className).not.toMatch(/details-content/)
}

/** The print copy: a plain block, sibling of the disclosure OUTSIDE it (and outside the
 *  link), after it in document order, `display: none` on screen and a block in print,
 *  with no control — carrying EXACTLY the rows the disclosure carries. */
function expectPrintTwin(link: HTMLElement, details: HTMLElement) {
  const print = details.parentElement!.querySelector(
    `[data-testid="${details.getAttribute('data-testid')}-print"]`,
  ) as HTMLElement
  expect(print).not.toBeNull()
  expect(print.tagName).toBe('DIV')
  expect(print.closest('a, details')).toBeNull()
  expect(details.contains(print)).toBe(false)
  expect(link.contains(print)).toBe(false)
  expect(print.parentElement).toBe(link.parentElement)
  expect(
    details.compareDocumentPosition(print) & Node.DOCUMENT_POSITION_FOLLOWING,
  ).toBeTruthy()
  expect(print).toHaveClass('hidden')
  expect(print).toHaveClass('print:block')
  expect(print.className).not.toMatch(/details-content/)
  expect(
    print.querySelector('a, button, summary, details, [tabindex]'),
  ).toBeNull()
  expect(print.querySelector('[title]')).toBeNull()
  expect(
    print.querySelector(
      '[data-testid$="-explanation"]:not([data-testid$="-print-explanation"])',
    ),
  ).toBeNull()
  expect(print).not.toHaveTextContent(WHY)
  // Same rows, same order, same text — one renderer behind both copies.
  const screenRows = [
    ...details.querySelectorAll('[data-testid$="-explanation"]'),
  ].filter(
    (r) => !r.getAttribute('data-testid')!.endsWith('-print-explanation'),
  )
  const printRows = [
    ...print.querySelectorAll('[data-testid$="-print-explanation"]'),
  ]
  expect(screenRows.length).toBeGreaterThan(0)
  expect(printRows.map((r) => r.getAttribute('data-testid'))).toEqual(
    screenRows.map((r) =>
      r
        .getAttribute('data-testid')!
        .replace(/-explanation$/, '-print-explanation'),
    ),
  )
  expect(printRows.map((r) => r.textContent)).toEqual(
    screenRows.map((r) => r.textContent),
  )
  for (const [i, row] of printRows.entries()) {
    expect(row.querySelector('dt')!.textContent).toBe(
      screenRows[i]!.querySelector('dt')!.textContent,
    )
    expect(row.querySelector('dd')!.textContent).toBe(
      screenRows[i]!.querySelector('dd')!.textContent,
    )
  }
  // No test id is rendered twice anywhere on the page.
  const ids = [...document.querySelectorAll('[data-testid]')].map((el) =>
    el.getAttribute('data-testid'),
  )
  expect(new Set(ids).size).toBe(ids.length)
  return print
}

/** A full sentence appears exactly twice on the page: once in the screen disclosure and
 *  once in its print copy — never in a caption, never a third time. */
function expectFullSentenceTwice(
  full: RegExp,
  details: HTMLElement,
  print: HTMLElement,
) {
  const all = screen.getAllByText(full)
  expect(all).toHaveLength(2)
  expect(all.filter((el) => details.contains(el))).toHaveLength(1)
  expect(all.filter((el) => print.contains(el))).toHaveLength(1)
}

describe('PartialSourceNote — the compact caption line', () => {
  it('aggregate: names the source, keeps Partial data, and a scan-ceiling brief — not the full sentence', () => {
    renderIntel(
      <PartialSourceNote source="Inventory" testId="note-aggregate" />,
    )
    const note = screen.getByTestId('note-aggregate')
    expect(note).toHaveTextContent(
      'Inventory — Partial data · scan ceiling reached',
    )
    expect(note).not.toHaveTextContent(SCAN_FULL)
    expect(note).not.toHaveTextContent(PAGE_BRIEF)
    expect(note).not.toHaveAttribute('title')
    expect(note.querySelector('button, details, summary')).toBeNull()
    expect(note).not.toHaveAttribute('tabindex')
  })

  it('page: names the source, keeps Partial data, and a one-page brief — not the full sentence', () => {
    renderIntel(
      <PartialSourceNote source="Sessions" kind="page" testId="note-page" />,
    )
    const note = screen.getByTestId('note-page')
    expect(note).toHaveTextContent(
      'Sessions — Partial data · one page loaded; there are more',
    )
    expect(note).not.toHaveTextContent(PAGE_FULL)
    expect(note).not.toHaveTextContent(SCAN_BRIEF)
    expect(note).not.toHaveAttribute('title')
    expect(note.querySelector('button, details, summary')).toBeNull()
    expect(note).not.toHaveAttribute('tabindex')
  })
})

describe('PartialCoverageDisclosure — the complete explanation', () => {
  it('is a closed native disclosure with one labelled row per source carrying that source’s full sentence', async () => {
    renderIntel(
      <PartialCoverageDisclosure
        sources={[SESSIONS, INVENTORY]}
        testId="details"
      />,
    )
    const details = screen.getByTestId('details') as HTMLDetailsElement
    expect(details.tagName).toBe('DETAILS')
    expect(details.open).toBe(false)
    const summary = within(details).getByText(WHY).closest('summary')!
    expect(summary).not.toBeNull()
    expect(summary.tabIndex).toBe(0)
    expect(within(details).queryByRole('button')).toBeNull()

    const sessionsRow = within(details).getByTestId(
      'home-sessions-partial-note-explanation',
    )
    const inventoryRow = within(details).getByTestId(
      'home-inventory-partial-note-explanation',
    )
    // Labelled relationship: the source is the term, the sentence its definition.
    expect(sessionsRow.querySelector('dt')).toHaveTextContent(
      'Sessions — Partial data',
    )
    expect(sessionsRow.querySelector('dd')).toHaveTextContent(PAGE_FULL)
    expect(sessionsRow).not.toHaveTextContent(SCAN_FULL)
    expect(sessionsRow).not.toHaveTextContent('Inventory')
    expect(inventoryRow.querySelector('dt')).toHaveTextContent(
      'Inventory — Partial data',
    )
    expect(inventoryRow.querySelector('dd')).toHaveTextContent(SCAN_FULL)
    expect(inventoryRow).not.toHaveTextContent(PAGE_FULL)
    expect(inventoryRow).not.toHaveTextContent('Sessions')
    // Order follows the caller: live first, as the caption does.
    expect(
      sessionsRow.compareDocumentPosition(inventoryRow) &
        Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy()
    expect(details.querySelector('[title]')).toBeNull()
    // Hidden in print as a whole; no `::details-content` reveal anywhere in it.
    expect(details).toHaveClass('print:hidden')
    expect(details.outerHTML).not.toMatch(/details-content/)
    // Standalone, it renders no print copy: the cell composes that beside it.
    expect(document.querySelector('[data-testid$="-print"]')).toBeNull()

    // A tap/click on the summary opens and closes it (jsdom implements this part).
    await userEvent.click(summary)
    expect(details.open).toBe(true)
    await userEvent.click(summary)
    expect(details.open).toBe(false)
  })
})

describe('PartialCoveragePrint — the print-only copy of the same rows', () => {
  it('is a plain block (no details, no control), hidden on screen and shown in print, with one labelled row per source', () => {
    renderIntel(
      <PartialCoveragePrint sources={[SESSIONS, INVENTORY]} testId="print" />,
    )
    const print = screen.getByTestId('print')
    expect(print.tagName).toBe('DIV')
    expect(print).toHaveClass('hidden')
    expect(print).toHaveClass('print:block')
    expect(print.outerHTML).not.toMatch(/details-content/)
    expect(
      print.querySelector('details, summary, button, a, [tabindex]'),
    ).toBeNull()
    expect(print.querySelector('[title]')).toBeNull()
    expect(print).not.toHaveTextContent(WHY)

    const sessionsRow = within(print).getByTestId(
      'home-sessions-partial-note-print-explanation',
    )
    const inventoryRow = within(print).getByTestId(
      'home-inventory-partial-note-print-explanation',
    )
    expect(sessionsRow.querySelector('dt')).toHaveTextContent(
      'Sessions — Partial data',
    )
    expect(sessionsRow.querySelector('dd')).toHaveTextContent(PAGE_FULL)
    expect(sessionsRow).not.toHaveTextContent(SCAN_FULL)
    expect(sessionsRow).not.toHaveTextContent('Inventory')
    expect(inventoryRow.querySelector('dt')).toHaveTextContent(
      'Inventory — Partial data',
    )
    expect(inventoryRow.querySelector('dd')).toHaveTextContent(SCAN_FULL)
    expect(inventoryRow).not.toHaveTextContent(PAGE_FULL)
    expect(inventoryRow).not.toHaveTextContent('Sessions')
    expect(
      sessionsRow.compareDocumentPosition(inventoryRow) &
        Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy()
    // The screen row ids are not reused by the print copy.
    expect(
      screen.queryByTestId('home-sessions-partial-note-explanation'),
    ).toBeNull()
    expect(
      screen.queryByTestId('home-inventory-partial-note-explanation'),
    ).toBeNull()
  })

  it('renders exactly the sources it is given — one source, one row; the other mechanism nowhere', () => {
    renderIntel(<PartialCoveragePrint sources={[INVENTORY]} testId="print" />)
    const print = screen.getByTestId('print')
    expect(
      print.querySelectorAll('[data-testid$="-print-explanation"]'),
    ).toHaveLength(1)
    expect(print).toHaveTextContent(SCAN_FULL)
    expect(print).not.toHaveTextContent(PAGE_FULL)
    expect(print).not.toHaveTextContent('Sessions')
  })
})

describe('CoverageLinkTile — the cell: link, disclosure, print copy; all from one value', () => {
  it('composes the print copy as a sibling after the disclosure, outside both the link and the details, from the same sources', () => {
    renderIntel(
      <CoverageLinkTile
        to="/inventory"
        partial={{ testId: 'cell-details', sources: [SESSIONS, INVENTORY] }}
      >
        <span>tile body</span>
      </CoverageLinkTile>,
    )
    const link = screen.getByRole('link')
    expect(link).toHaveAttribute('href', '/inventory')
    expect(nestedTabStops(link)).toEqual([])
    const details = screen.getByTestId('cell-details')
    expectSiblingDisclosure(link, details)
    const print = expectPrintTwin(link, details)
    expect(print).toHaveAttribute('data-testid', 'cell-details-print')
    // Three siblings in the cell, in this order: link, disclosure, print copy.
    expect([...link.parentElement!.children].map((el) => el.tagName)).toEqual([
      'A',
      'DETAILS',
      'DIV',
    ])
    expectFullSentenceTwice(PAGE_FULL, details, print)
    expectFullSentenceTwice(SCAN_FULL, details, print)
    // The compact caption is the caller's; here the link carries no full sentence.
    expect(within(link).queryByText(PAGE_FULL)).toBeNull()
    expect(within(link).queryByText(SCAN_FULL)).toBeNull()
  })

  it('retires BOTH copies together when the value loses a source or goes away — no stale print row', () => {
    const both: PartialCoverage = {
      testId: 'cell-details',
      sources: [SESSIONS, INVENTORY],
    }
    const { rerender } = renderIntel(
      <CoverageLinkTile to="/inventory" partial={both}>
        <span>tile body</span>
      </CoverageLinkTile>,
    )
    expect(
      screen.getByTestId('home-sessions-partial-note-print-explanation'),
    ).toBeInTheDocument()
    expect(
      screen.getByTestId('home-inventory-partial-note-print-explanation'),
    ).toBeInTheDocument()

    // Sessions' answer is no longer current: its row leaves both copies, Inventory stays.
    rerender(
      <CoverageLinkTile
        to="/inventory"
        partial={{ testId: 'cell-details', sources: [INVENTORY] }}
      >
        <span>tile body</span>
      </CoverageLinkTile>,
    )
    const details = screen.getByTestId('cell-details')
    const print = screen.getByTestId('cell-details-print')
    expect(
      screen.queryByTestId('home-sessions-partial-note-explanation'),
    ).toBeNull()
    expect(
      screen.queryByTestId('home-sessions-partial-note-print-explanation'),
    ).toBeNull()
    expect(screen.queryAllByText(PAGE_FULL)).toHaveLength(0)
    expect(screen.queryByText(/Sessions/)).toBeNull()
    expect(
      within(print).getByTestId(
        'home-inventory-partial-note-print-explanation',
      ),
    ).toHaveTextContent(SCAN_FULL)
    expectPrintTwin(screen.getByRole('link'), details)
    expectFullSentenceTwice(SCAN_FULL, details, print)

    // Nothing partial any more: the cell is exactly the plain link again.
    rerender(
      <CoverageLinkTile
        to="/inventory"
        partial={{ testId: 'cell-details', sources: [] }}
      >
        <span>tile body</span>
      </CoverageLinkTile>,
    )
    expect(screen.queryByTestId('cell-details')).toBeNull()
    expect(screen.queryByTestId('cell-details-print')).toBeNull()
    expect(document.querySelector('details, summary')).toBeNull()
    expect(document.querySelector('[data-testid$="-explanation"]')).toBeNull()
    expect(screen.queryAllByText(SCAN_FULL)).toHaveLength(0)
    expect(screen.queryByText(/Partial data/)).toBeNull()
    expect(screen.getByRole('link').parentElement!.children).toHaveLength(1)

    rerender(
      <CoverageLinkTile to="/inventory">
        <span>tile body</span>
      </CoverageLinkTile>,
    )
    expect(screen.queryByTestId('cell-details-print')).toBeNull()
    expect(document.querySelector('[data-testid$="-explanation"]')).toBeNull()
  })
})

describe('PartialSourceNote — Home EstateTile cell', () => {
  it('Inventory: compact line inside the /inventory link, full sentence in a sibling disclosure, no extra stop inside the link', () => {
    renderIntel(
      <EstateTile
        to="/inventory"
        icon={<span />}
        label="Inventory"
        value="25"
        caption="4 agents · 3 active"
        state="ready"
        partial={{
          testId: 'home-inventory-partial-details',
          sources: [INVENTORY],
        }}
      />,
    )
    const link = screen.getByRole('link')
    expect(link).toHaveAttribute('href', '/inventory')
    expect(within(link).getByText('4 agents · 3 active')).toBeInTheDocument()
    const note = within(link).getByTestId('home-inventory-partial-note')
    expect(note).toHaveTextContent(SCAN_BRIEF)
    expect(note).not.toHaveTextContent(SCAN_FULL)
    expect(note).not.toHaveTextContent(PAGE_BRIEF)
    expect(note).not.toHaveAttribute('title')
    expect(within(link).queryByRole('button')).toBeNull()
    expect(nestedTabStops(link)).toEqual([])

    const details = screen.getByTestId('home-inventory-partial-details')
    expectSiblingDisclosure(link, details)
    const row = within(details).getByTestId(
      'home-inventory-partial-note-explanation',
    )
    expect(row).toHaveTextContent('Inventory — Partial data')
    expect(row).toHaveTextContent(SCAN_FULL)
    expect(row).not.toHaveTextContent(PAGE_FULL)
    // The full sentence lives in the disclosure and in its print copy — never in
    // the caption, never a third time.
    const print = expectPrintTwin(link, details)
    expect(print).toHaveAttribute(
      'data-testid',
      'home-inventory-partial-details-print',
    )
    expect(print).not.toHaveTextContent(PAGE_FULL)
    expectFullSentenceTwice(SCAN_FULL, details, print)
  })

  it('Sessions: compact line inside the /sessions link, full page sentence in a sibling disclosure', () => {
    renderIntel(
      <EstateTile
        to="/sessions"
        icon={<span />}
        label="Live sessions"
        value="3"
        caption="0 idle now"
        state="ready"
        partial={{
          testId: 'home-sessions-partial-details',
          sources: [SESSIONS],
        }}
      />,
    )
    const link = screen.getByRole('link')
    expect(link).toHaveAttribute('href', '/sessions')
    const note = within(link).getByTestId('home-sessions-partial-note')
    expect(note).toHaveTextContent(PAGE_BRIEF)
    expect(note).not.toHaveTextContent(PAGE_FULL)
    expect(note).not.toHaveTextContent(SCAN_BRIEF)
    expect(note).not.toHaveAttribute('title')
    expect(within(link).queryByRole('button')).toBeNull()
    expect(nestedTabStops(link)).toEqual([])

    const details = screen.getByTestId('home-sessions-partial-details')
    expectSiblingDisclosure(link, details)
    const row = within(details).getByTestId(
      'home-sessions-partial-note-explanation',
    )
    expect(row).toHaveTextContent('Sessions — Partial data')
    expect(row).toHaveTextContent(PAGE_FULL)
    expect(row).not.toHaveTextContent(SCAN_FULL)
    const print = expectPrintTwin(link, details)
    expect(print).not.toHaveTextContent(SCAN_FULL)
    expectFullSentenceTwice(PAGE_FULL, details, print)
  })

  it('nothing partial: a plain link with no caption line and no disclosure — the previous rendering', () => {
    const { container } = renderIntel(
      <EstateTile
        to="/inventory"
        icon={<span />}
        label="Inventory"
        value="25"
        caption="4 agents · 3 active"
        state="ready"
      />,
    )
    const link = screen.getByRole('link')
    expect(link).toHaveAttribute('href', '/inventory')
    expect(container.querySelector('details, summary')).toBeNull()
    expect(container.querySelector('[data-testid$="-print"]')).toBeNull()
    expect(container.querySelector('[data-testid$="-explanation"]')).toBeNull()
    expect(screen.queryByText(WHY)).toBeNull()
    expect(screen.queryByText(/Partial data/)).toBeNull()
    expect(screen.queryByText(SCAN_FULL)).toBeNull()
    expect(screen.queryByText(PAGE_FULL)).toBeNull()
    expect(nestedTabStops(link)).toEqual([])
  })
})

describe('PartialSourceNote — Executive KpiTiles cell', () => {
  const usage = deriveUsage(
    { ...inventorySummaryFixture, truncated: true },
    {
      items: sessionsLiveFixture.items.map((row, i) => ({
        ...row,
        session_ref: `row-${i}`,
        live_ref: `lr-row-${i}`,
        cc_state: 'active' as const,
      })),
      has_more: true,
    },
  )
  const both: PartialCoverage = {
    testId: 'executive-usage-partial-details',
    sources: [
      {
        source: 'Sessions',
        kind: 'page',
        testId: 'executive-usage-sessions-partial',
      },
      {
        source: 'Inventory',
        kind: 'aggregate',
        testId: 'executive-usage-inventory-partial',
      },
    ],
  }

  it('both compact lines sit inside the usage link to /inventory; ONE sibling disclosure carries both full sentences', () => {
    renderIntel(<KpiTiles usage={usage} usagePartial={both} />)
    const link = screen.getByRole('link')
    expect(link).toHaveAttribute('href', '/inventory')
    expect(within(link).getByText('3 live · 25 tracked')).toBeInTheDocument()

    const sessionsNote = within(link).getByTestId(
      'executive-usage-sessions-partial',
    )
    const inventoryNote = within(link).getByTestId(
      'executive-usage-inventory-partial',
    )
    expect(sessionsNote).toHaveTextContent('Sessions')
    expect(sessionsNote).toHaveTextContent(PAGE_BRIEF)
    expect(sessionsNote).not.toHaveTextContent('Inventory')
    expect(sessionsNote).not.toHaveTextContent(SCAN_BRIEF)
    expect(sessionsNote).not.toHaveTextContent(PAGE_FULL)
    expect(inventoryNote).toHaveTextContent('Inventory')
    expect(inventoryNote).toHaveTextContent(SCAN_BRIEF)
    expect(inventoryNote).not.toHaveTextContent('Sessions')
    expect(inventoryNote).not.toHaveTextContent(PAGE_BRIEF)
    expect(inventoryNote).not.toHaveTextContent(SCAN_FULL)
    expect(sessionsNote).not.toHaveAttribute('title')
    expect(inventoryNote).not.toHaveAttribute('title')
    expect(within(link).queryByRole('button')).toBeNull()
    expect(nestedTabStops(link)).toEqual([])

    const details = screen.getByTestId('executive-usage-partial-details')
    expectSiblingDisclosure(link, details)
    expect(screen.getAllByText(WHY)).toHaveLength(1)
    const sessionsRow = within(details).getByTestId(
      'executive-usage-sessions-partial-explanation',
    )
    const inventoryRow = within(details).getByTestId(
      'executive-usage-inventory-partial-explanation',
    )
    expect(sessionsRow).toHaveTextContent('Sessions — Partial data')
    expect(sessionsRow).toHaveTextContent(PAGE_FULL)
    expect(sessionsRow).not.toHaveTextContent(SCAN_FULL)
    expect(inventoryRow).toHaveTextContent('Inventory — Partial data')
    expect(inventoryRow).toHaveTextContent(SCAN_FULL)
    expect(inventoryRow).not.toHaveTextContent(PAGE_FULL)
    // ONE print copy beside the one disclosure, with the same two rows, live first.
    const print = expectPrintTwin(link, details)
    expect(print).toHaveAttribute(
      'data-testid',
      'executive-usage-partial-details-print',
    )
    expect(screen.getAllByTestId(/-print$/)).toHaveLength(1)
    const printSessions = within(print).getByTestId(
      'executive-usage-sessions-partial-print-explanation',
    )
    const printInventory = within(print).getByTestId(
      'executive-usage-inventory-partial-print-explanation',
    )
    expect(printSessions).toHaveTextContent('Sessions — Partial data')
    expect(printSessions).toHaveTextContent(PAGE_FULL)
    expect(printSessions).not.toHaveTextContent(SCAN_FULL)
    expect(printInventory).toHaveTextContent('Inventory — Partial data')
    expect(printInventory).toHaveTextContent(SCAN_FULL)
    expect(printInventory).not.toHaveTextContent(PAGE_FULL)
    expect(
      printSessions.compareDocumentPosition(printInventory) &
        Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy()
    expectFullSentenceTwice(PAGE_FULL, details, print)
    expectFullSentenceTwice(SCAN_FULL, details, print)
  })

  it('nothing partial: the usage tile is a plain link — no caption line, no disclosure', () => {
    const complete = deriveUsage(inventorySummaryFixture, {
      items: sessionsLiveFixture.items,
      has_more: false,
    })
    const { container } = renderIntel(<KpiTiles usage={complete} />)
    const link = screen.getByRole('link')
    expect(link).toHaveAttribute('href', '/inventory')
    expect(container.querySelector('details, summary')).toBeNull()
    expect(container.querySelector('[data-testid$="-print"]')).toBeNull()
    expect(container.querySelector('[data-testid$="-explanation"]')).toBeNull()
    expect(screen.queryByText(WHY)).toBeNull()
    expect(screen.queryByText(/Partial data/)).toBeNull()
    expect(screen.queryByText(SCAN_FULL)).toBeNull()
    expect(screen.queryByText(PAGE_FULL)).toBeNull()
    expect(nestedTabStops(link)).toEqual([])
  })
})
