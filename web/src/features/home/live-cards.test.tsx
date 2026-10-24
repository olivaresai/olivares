// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// A LIVE CARD OPENS ITS SESSION, and Now holds at most LIVE_CARDS of them.
import type { ReactNode } from 'react'
import { act } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { fireEvent, renderIntel, screen, within } from '@/test/intel'
import type { RunDTO } from '@/features/agentops/types'
import { mergeSessions } from '@/features/sessions/provenance'
import type { LiveDTO } from '@/features/sessions/types'
import { usePreferencesStore } from '@/stores/preferences'
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

// An engine-observed live row in its active window: 10 s of events certified,
// last event 20 s before the fixture clock's now.
const observedActive = (): LiveDTO => ({
  session_ref: 'sess-observed',
  live_ref: 'lr-observed',
  attribution: 'legacy',
  cc_state: 'active',
  input_tokens: 40,
  output_tokens: 12,
  cost_micro_usd: 123_400,
  event_count: 2,
  tool_call_count: 0,
  first_event_at: new Date(2026, 9, 18, 11, 0, 0).toISOString(),
  last_event_at: new Date(2026, 9, 18, 11, 0, 10).toISOString(),
  duration_seconds: 10,
})

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

  // #2549: on Estate a card opens its session in the sheet over the page; a click that
  // asks for a new tab or window keeps the link's own way to the full page.
  it('opens its session in place on a plain click, by its address', () => {
    const sessions = mergeSessions([], [run(1)])
    const onOpen = vi.fn()
    renderIntel(
      <LiveCards sessions={sessions} showAll={false} onOpen={onOpen} />,
    )
    const card = screen.getByTestId('now-live-card')
    expect(fireEvent.click(card)).toBe(false) // the navigation was prevented
    expect(onOpen).toHaveBeenCalledWith(sessions[0]!.key)
  })

  it('leaves a modified click to the link', () => {
    const sessions = mergeSessions([], [run(1)])
    const onOpen = vi.fn()
    renderIntel(
      <LiveCards sessions={sessions} showAll={false} onOpen={onOpen} />,
    )
    const card = screen.getByTestId('now-live-card')
    expect(fireEvent.click(card, { ctrlKey: true })).toBe(true)
    expect(fireEvent.click(card, { metaKey: true })).toBe(true)
    expect(fireEvent.click(card, { altKey: true })).toBe(true)
    expect(fireEvent.click(card, { button: 1 })).toBe(true)
    expect(onOpen).not.toHaveBeenCalled()
  })

  it('an active card ticks its elapsed seconds each second and tags the figures', () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date(2026, 9, 18, 11, 0, 30))
    try {
      const sessions = mergeSessions([observedActive()], [])
      renderIntel(<LiveCards sessions={sessions} showAll={false} />)
      // The engine counted 10 s of events; 20 s have passed since the last one.
      expect(screen.getByText(/\b30s\b/)).toBeInTheDocument()
      act(() => {
        vi.advanceTimersByTime(1000)
      })
      expect(screen.getByText(/\b31s\b/)).toBeInTheDocument()
      act(() => {
        vi.advanceTimersByTime(1000)
      })
      const card = screen.getByTestId('now-live-card')
      expect(screen.getByText(/\b32s\b/)).toBeInTheDocument()
      expect(card.querySelector('[data-elapsed]')).toHaveTextContent(/\b32s\b/)
      // The cost stays the engine's figure between polls: the row reports no
      // rate, so nothing invents one. The span re-renders on the same second
      // and ticks when a poll certifies new usage.
      expect(card.querySelector('[data-cost]')).toHaveTextContent('$0.12')
    } finally {
      vi.useRealTimers()
    }
  })

  it('shows no money figure for a session that reported no cost, never a zero', () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date(2026, 9, 18, 11, 0, 30))
    try {
      const sessions = mergeSessions(
        [{ ...observedActive(), cost_micro_usd: 0 }],
        [],
      )
      renderIntel(<LiveCards sessions={sessions} showAll={false} />)
      const card = screen.getByTestId('now-live-card')
      expect(card.querySelector('[data-elapsed]')).toHaveTextContent(/\b30s\b/)
      expect(card.querySelector('[data-cost]')).toBeNull()
      expect(card).not.toHaveTextContent('$0')
    } finally {
      vi.useRealTimers()
    }
  })

  it('freezes the ticking figures under the ?still capture switch', () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date(2026, 9, 18, 11, 0, 30))
    try {
      window.history.replaceState(null, '', '/?still')
      renderIntel(
        <LiveCards
          sessions={mergeSessions([observedActive()], [])}
          showAll={false}
        />,
      )
      const card = screen.getByTestId('now-live-card')
      const before = card.querySelector('[data-elapsed]')!.textContent
      act(() => {
        vi.advanceTimersByTime(5000)
      })
      expect(card.querySelector('[data-elapsed]')).toHaveTextContent(before!)
    } finally {
      window.history.replaceState(null, '', '/')
      vi.useRealTimers()
    }
  })

  it('stops the ticking figures under the console Reduce motion setting', () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date(2026, 9, 18, 11, 0, 30))
    try {
      usePreferencesStore.setState({ reduceMotion: true })
      renderIntel(
        <LiveCards
          sessions={mergeSessions([observedActive()], [])}
          showAll={false}
        />,
      )
      const card = screen.getByTestId('now-live-card')
      const before = card.querySelector('[data-elapsed]')!.textContent
      act(() => {
        vi.advanceTimersByTime(5000)
      })
      expect(card.querySelector('[data-elapsed]')).toHaveTextContent(before!)
    } finally {
      usePreferencesStore.setState({ reduceMotion: false })
      vi.useRealTimers()
    }
  })
})
