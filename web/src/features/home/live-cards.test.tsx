// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// A LIVE CARD OPENS ITS SESSION, and Now holds at most LIVE_CARDS of them.
import type { ReactNode } from 'react'
import { describe, expect, it, vi } from 'vitest'
import { renderIntel, screen, within } from '@/test/intel'
import type { RunDTO } from '@/features/agentops/types'
import { mergeSessions } from '@/features/sessions/provenance'
import { LiveCards } from './live-cards'
import { LIVE_CARDS } from './now-sessions'
import './i18n'
import '@/features/sessions/i18n'

// The double builds the href the router would, search included, and forwards every prop:
// a card that lost its session address would otherwise look like a link to the room.
vi.mock('@tanstack/react-router', () => ({
  useRouterState: () => '',
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

const run = (i: number, over: Partial<RunDTO> = {}): RunDTO =>
  ({
    run_ref: `run-${i}`,
    tenant_id: 'demo',
    state: 'running',
    name: `Task ${i}`,
    provider_driver: 'codex',
    created_at: new Date(2026, 9, 16, 9, i).toISOString(),
    ...over,
  }) as RunDTO

describe('LiveCards', () => {
  it('each card opens its own session by its address, and names it', () => {
    const sessions = mergeSessions([], [run(1, { cost_micro_usd: 420_000 })])
    renderIntel(<LiveCards sessions={sessions} showAll={false} />)
    const card = screen.getByTestId('now-live-card')
    expect(card).toHaveAttribute(
      'href',
      `/sessions?session=${encodeURIComponent(sessions[0]!.key)}`,
    )
    // The link is named by what it shows, tool first (label in name).
    expect(card).toHaveAccessibleName(/^Codex.*Task 1/)
    expect(within(card).getByText('Codex')).toBeInTheDocument()
    expect(within(card).getByTestId('now-live-card-state')).toHaveTextContent(
      'Working',
    )
    expect(within(card).getByText('$0.42')).toBeInTheDocument()
    expect(screen.queryByTestId('now-live-all')).toBeNull()
  })

  it(`holds at most ${LIVE_CARDS} cards; the link reaches the rest`, () => {
    const sessions = mergeSessions(
      [],
      Array.from({ length: LIVE_CARDS + 1 }, (_, i) => run(i)),
    )
    renderIntel(<LiveCards sessions={sessions} showAll />)
    expect(screen.getAllByTestId('now-live-card')).toHaveLength(LIVE_CARDS)
    expect(screen.getByTestId('now-live-all')).toHaveAttribute(
      'href',
      '/sessions',
    )
  })
})
