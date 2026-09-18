// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE FRONT DOOR tells what happened, and never more than it was told.
import type { ReactNode } from 'react'
import { describe, expect, it, vi } from 'vitest'
import { renderIntel, screen } from '@/test/intel'
import { expectNoRawI18nKeys } from '@/test/i18n-keys'
import type { LiveDTO } from '@/features/sessions/types'
import { RecentWork } from './recent-work'
import { RECENT_WORK_ROWS, workLine } from './work-line'
import './i18n'
import '@/features/sessions/i18n'

// The anchor mock FORWARDS every prop, unlike the older copies of it in this
// directory. Those drop `data-testid`, so a test that asks for a link by test id
// finds nothing and the failure looks like a missing element rather than a missing
// mock — measured here, twice, before this comment existed.
vi.mock('@tanstack/react-router', () => ({
  useRouterState: () => '',
  // The row links carry a SEARCH param, so the double has to build the same href
  // the router would. Rendering `to` alone would have made every row look like a link
  // to the room, which is exactly the state this file exists to keep out — the
  // assertion would have passed on the defect.
  Link: ({
    children,
    to,
    search,
    ...rest
  }: {
    children: ReactNode
    to: string
    search?: Record<string, string>
  } & Record<string, unknown>) => {
    const query = new URLSearchParams(search ?? {}).toString()
    return (
      <a href={query ? `${to}?${query}` : to} {...rest}>
        {children}
      </a>
    )
  },
}))

/** A row shaped like the ones `serve --seed-demo` actually returns (measured). */
function row(over: Partial<LiveDTO> = {}): LiveDTO {
  return {
    session_ref: 'sess-coder-7a3f',
    live_ref: '01a0b0b2-e581-71b8-a163-5f73c7ed6aaa',
    attribution: 'legacy',
    cc_state: 'idle',
    input_tokens: 1200,
    output_tokens: 800,
    cost_micro_usd: 42_000,
    event_count: 3,
    tool_call_count: 2,
    first_event_at: '2026-09-17T18:47:59Z',
    last_event_at: '2026-09-17T18:48:14Z',
    duration_seconds: 15,
    ...over,
  }
}

describe('workLine — the sentence degrades, it never invents', () => {
  it('prefers the summary', () => {
    expect(
      workLine(row({ summary: 'Filed PR #7723', goal: 'ship it' })),
    ).toEqual({ text: 'Filed PR #7723', from: 'summary' })
  })

  it('falls back to the goal', () => {
    expect(workLine(row({ goal: 'ship the release' })).from).toBe('goal')
  })

  it('then to the action and the resource the connector reported', () => {
    expect(
      workLine(
        row({
          current_action: 'create_issue',
          current_resource: 'github/create_issue',
        }),
      ),
    ).toEqual({ text: 'create_issue · github/create_issue', from: 'action' })
  })

  it('and last to the session reference — never to prose', () => {
    // This is the case the DEMO ESTATE produces: measured 2026-09-17 against
    // `serve --seed-demo`, not one seeded row carries a summary or a goal.
    expect(workLine(row())).toEqual({ text: 'sess-coder-7a3f', from: 'ref' })
  })

  it('treats whitespace as absent', () => {
    expect(workLine(row({ summary: '   ', goal: '  ' })).from).toBe('ref')
  })
})

describe('RecentWork', () => {
  it('tells duration, tool calls, events and cost from the fields the engine sent', () => {
    const { container } = renderIntel(
      <RecentWork sessions={[row()]} state="ready" canStartSession />,
    )
    expect(screen.getByText(/worked for 15\.0s/)).toBeInTheDocument()
    expect(screen.getByText(/2 tool calls/)).toBeInTheDocument()
    expect(screen.getByText(/3 events/)).toBeInTheDocument()
    expectNoRawI18nKeys(container)
  })

  it('leaves a figure out rather than printing a zero', () => {
    renderIntel(
      <RecentWork
        sessions={[
          row({ tool_call_count: 0, event_count: 0, cost_micro_usd: 0 }),
        ]}
        state="ready"
        canStartSession
      />,
    )
    expect(screen.queryByText(/0 tool calls/)).toBeNull()
    expect(screen.queryByText(/0 events/)).toBeNull()
  })

  it('uses the singular form for one tool call', () => {
    // i18next falls back to `_other` when the category is missing, which is how
    // "1 open incidents" shipped once already (features/shared/i18n-plurals.test.ts).
    renderIntel(
      <RecentWork
        sessions={[row({ tool_call_count: 1, event_count: 1 })]}
        state="ready"
        canStartSession
      />,
    )
    expect(screen.getByText(/1 tool call\b/)).toBeInTheDocument()
    expect(screen.getByText(/1 event\b/)).toBeInTheDocument()
  })

  it('shows at most the five most recent, and links to the room with the rest', () => {
    const many = Array.from({ length: 9 }, (_, i) =>
      row({ session_ref: `s-${i}`, live_ref: `ref-${i}` }),
    )
    renderIntel(<RecentWork sessions={many} state="ready" canStartSession />)
    expect(
      screen.getByTestId('home-recent-rows').querySelectorAll('li'),
    ).toHaveLength(RECENT_WORK_ROWS)
    expect(screen.getByTestId('home-recent-all')).toHaveAttribute(
      'href',
      '/sessions',
    )
  })

  it('an empty estate offers the verb that fills it', () => {
    renderIntel(<RecentWork sessions={[]} state="ready" canStartSession />)
    expect(screen.getByText('No sessions yet')).toBeInTheDocument()
    expect(screen.getByTestId('home-recent-start')).toHaveAttribute(
      'href',
      '/agentops',
    )
  })

  it('offers nothing to a principal who cannot start a run', () => {
    renderIntel(
      <RecentWork sessions={[]} state="ready" canStartSession={false} />,
    )
    expect(screen.getByText('No sessions yet')).toBeInTheDocument()
    // Still says what the surface will show — the description is not conditional.
    expect(
      screen.getByText(/Every run a connected engine performs appears here/),
    ).toBeInTheDocument()
    expect(screen.queryByTestId('home-recent-start')).toBeNull()
  })

  it('a failed read is an alert, not an empty estate', () => {
    // The distinction this asserts is the product's own rule: a source
    // that errored must never render as "there is nothing".
    renderIntel(
      <RecentWork sessions={undefined} state="unavailable" canStartSession />,
    )
    expect(screen.getByRole('alert')).toHaveTextContent(
      'Couldn’t load recent work',
    )
    expect(screen.queryByText('No sessions yet')).toBeNull()
  })

  it('loading shows skeletons, not an empty estate', () => {
    renderIntel(
      <RecentWork sessions={undefined} state="loading" canStartSession />,
    )
    expect(screen.getByTestId('home-recent-loading')).toBeInTheDocument()
    expect(screen.queryByText('No sessions yet')).toBeNull()
  })
})

describe('the row opens the SESSION, not the room', () => {
  it('addresses each row by the key the join gives it', () => {
    // The front door first shipped rows that could only open `/sessions`, which a review
    // named "the single largest remaining gap". The address is not composed here: it is
    // `liveRowKey`, the same function that keys the row inside the surface.
    renderIntel(
      <RecentWork
        sessions={[
          row({ attribution: 'observed', live_ref: 'lr-9' }),
          row({ session_ref: 'sess-legacy', attribution: 'legacy' }),
        ]}
        state="ready"
        canStartSession
      />,
    )
    const links = screen.getAllByTestId('home-recent-row')
    expect(links[0]).toHaveAttribute('href', '/sessions?session=live%3Alr-9')
    expect(links[1]).toHaveAttribute(
      'href',
      '/sessions?session=sess%3Asess-legacy',
    )
  })

  it('gives the row ONE accessible name, and it is the sentence', () => {
    renderIntel(
      <RecentWork
        sessions={[row({ summary: 'Filed PR #7723' })]}
        state="ready"
        canStartSession
      />,
    )
    expect(
      screen.getByRole('link', { name: /Filed PR #7723/ }),
    ).toHaveAttribute('data-testid', 'home-recent-row')
    // One link per row: a badge link plus a text link plus a time link would be three
    // tab stops that all go to the same place.
    expect(screen.getAllByRole('link')).toHaveLength(2) // the row + "open all"
  })
})
