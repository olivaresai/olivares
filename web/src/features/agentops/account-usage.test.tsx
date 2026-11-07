// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// What an account's usage says where the console shows the account: its share of the week
// and of five hours, nothing at all (never 0 %) when the tool reported none, and a window at
// 100 % marked with when it resets. Every window by name lives in the Usage detail.
import { render, renderHook, screen, within } from '@testing-library/react'
import axe from 'axe-core'
import { describe, expect, it } from 'vitest'
import { findRawI18nKeys } from '@/test/i18n-keys'
import {
  AccountUsage,
  AtLimit,
  limitReached,
  UsageWindows,
  useWindowName,
} from './account-usage'
import type { ProviderAccountUsage } from './types'

const win = (
  window: string,
  utilization: number,
  over: Partial<ProviderAccountUsage> = {},
): ProviderAccountUsage => ({
  window,
  label: window,
  utilization,
  observed_at: '2026-10-29T10:00:00Z',
  ...over,
})

describe('AccountUsage', () => {
  it('says the share of the week, then of five hours, rounded', () => {
    const { container } = render(
      <AccountUsage usage={[win('five_hour', 41.6), win('seven_day', 17.4)]} />,
    )
    // The week comes first whatever order the engine sent.
    expect(container.textContent).toBe('17% of the week · 42% of 5 hours')
    expect(findRawI18nKeys(container)).toEqual([])
  })

  it('leaves out a window the tool did not report; it is unknown, not 0 %', () => {
    const { container } = render(<AccountUsage usage={[win('five_hour', 9)]} />)
    expect(container.textContent).toBe('9% of 5 hours')
    expect(container.textContent).not.toContain('week')
  })

  it('says nothing for an account that reported nothing, unless the row asks for the word', () => {
    const { container, rerender } = render(<AccountUsage usage={undefined} />)
    expect(container).toBeEmptyDOMElement()
    rerender(<AccountUsage usage={[]} />)
    expect(container).toBeEmptyDOMElement()
    rerender(<AccountUsage usage={undefined} showUnknown />)
    expect(screen.getByText('Usage unknown')).toBeInTheDocument()
    expect(container.textContent).not.toContain('0%')
  })

  it('says 100% only for a window at its limit, never for one just under it', () => {
    const { container } = render(
      <AccountUsage usage={[win('five_hour', 99.6), win('seven_day', 100)]} />,
    )
    expect(container.textContent).toBe('100% of the week · 99% of 5 hours')
  })

  it("leaves a model's weekly cap out of the share line; the Usage detail names it", () => {
    const usage = [
      win('seven_day', 75, { resets_at: '2030-01-03T10:00:00Z' }),
      win('seven_day:fable', 0, { label: 'Weekly (Fable)' }),
    ]
    expect(render(<AccountUsage usage={usage} />).container.textContent).toBe(
      '75% of the week',
    )
    const detail = render(<UsageWindows usage={usage} />).container
    const [week, fable] = within(detail).getAllByRole('listitem')
    expect(week).toHaveTextContent('Weekly 75%')
    expect(within(week!).getByText(/^Resets /)).toBeInTheDocument()
    // In the console's words, never the engine's English label.
    expect(fable).toHaveTextContent('Fable only 0%')
    expect(detail).not.toHaveTextContent('Weekly (Fable)')
    expect(findRawI18nKeys(detail)).toEqual([])
    // The provider snapshot carries the engine's label alone: the same words.
    const name = renderHook(() => useWindowName()).result.current
    expect(name({ label: '5-hour' })).toBe('5-hour')
    expect(name({ label: 'Weekly' })).toBe('Weekly')
    expect(name({ label: 'Weekly (Fable)' })).toBe('Fable only')
    expect(name({ label: 'Monthly spend' })).toBe('Monthly spend')
  })

  it('details a monthly cap and credit balance without inventing a percent', () => {
    const { container } = render(
      <UsageWindows
        usage={[
          win('monthly_extra_usage', 85, { label: 'Monthly extra usage' }),
          {
            window: 'credits',
            label: 'Credits',
            credit: { balance: '12.50', unlimited: false },
            observed_at: '2026-10-29T10:00:00Z',
          },
        ]}
      />,
    )
    expect(
      within(container)
        .getAllByRole('listitem')
        .map((li) => li.textContent),
    ).toEqual(['Monthly extra usage 85%', 'Credits 12.50'])
  })
  it('keeps zero, unlimited and unknown credits distinct', () => {
    const row = {
      window: 'credits',
      label: 'Credits',
      observed_at: '2026-10-29T10:00:00Z',
    }
    const { container, rerender } = render(
      <UsageWindows
        usage={[{ ...row, credit: { balance: '0', unlimited: false } }]}
      />,
    )
    expect(container.textContent).toBe('Credits 0')
    expect(
      limitReached([{ ...row, credit: { balance: '0', unlimited: false } }]),
    ).toBeUndefined()
    rerender(<UsageWindows usage={[{ ...row, credit: { unlimited: true } }]} />)
    expect(container.textContent).toBe('Credits ∞')
    rerender(
      <AccountUsage
        usage={[{ ...row, credit: { unlimited: false } }]}
        showUnknown
      />,
    )
    expect(container.textContent).toBe('Usage unknown')
  })
  it('warns at eighty percent without marking a limit reached', () => {
    const { container } = render(
      <AccountUsage usage={[win('five_hour', 80)]} />,
    )
    expect(container.querySelector('.text-warning')).toHaveTextContent(
      '80% of 5 hours',
    )
    expect(limitReached([win('five_hour', 80)])).toBeUndefined()
  })
})

describe('a window whose reset time has passed', () => {
  const stale = win('five_hour', 100, { resets_at: '2020-01-01T10:00:00Z' })

  it("is the tool's reading from before the reset: no figure, no limit", () => {
    expect(limitReached([stale])).toBeUndefined()
    const { container } = render(
      <AccountUsage usage={[stale, win('seven_day', 20)]} />,
    )
    expect(container.textContent).toBe('20% of the week')
    expect(render(<AtLimit usage={[stale]} />).container).toBeEmptyDOMElement()
  })
})

describe('limitReached', () => {
  it('is the first window at 100 % or more, whichever window it is', () => {
    expect(limitReached([win('five_hour', 99.9)])).toBeUndefined()
    expect(limitReached(undefined)).toBeUndefined()
    expect(limitReached([win('five_hour', 20), win('seven_day', 100)])).toEqual(
      win('seven_day', 100),
    )
    expect(limitReached([win('seven_day:opus', 130)])?.window).toBe(
      'seven_day:opus',
    )
  })
})

describe('AtLimit', () => {
  it('names the window and when it resets', () => {
    render(
      <AtLimit
        usage={[win('five_hour', 100, { resets_at: '2030-01-01T10:00:00Z' })]}
      />,
    )
    expect(screen.getByRole('status')).toHaveTextContent(
      /5-hour limit reached, resets /,
    )
  })

  it('states the limit without a time when the tool gave no reset', () => {
    render(<AtLimit usage={[win('seven_day', 100)]} />)
    expect(screen.getByRole('status')).toHaveTextContent('Weekly limit reached')
    expect(screen.getByRole('status')).not.toHaveTextContent('resets')
  })

  it("says another window's limit is reached without the provider's window name", () => {
    render(
      <AtLimit
        usage={[win('seven_day:opus', 100, { label: 'Weekly (Opus)' })]}
      />,
    )
    expect(screen.getByRole('status')).toHaveTextContent(/^Limit reached$/)
  })

  it('renders nothing below the limit', () => {
    const { container } = render(<AtLimit usage={[win('five_hour', 99)]} />)
    expect(container).toBeEmptyDOMElement()
  })
})

describe('accessibility', () => {
  it('has no axe violation in a row that shows the figures and the limit mark', async () => {
    const { container } = render(
      <ul>
        <li>
          claude-b
          <AccountUsage
            usage={[
              win('five_hour', 100, { resets_at: '2030-01-01T10:00:00Z' }),
              win('seven_day', 62),
            ]}
          />
          <AtLimit
            usage={[
              win('five_hour', 100, { resets_at: '2030-01-01T10:00:00Z' }),
            ]}
          />
        </li>
        <li>
          claude-c
          <AccountUsage usage={undefined} showUnknown />
        </li>
      </ul>,
    )
    // jsdom has no layout: color contrast is judged on the captures, not here.
    const results = await axe.run(container, {
      rules: { 'color-contrast': { enabled: false } },
    })
    expect(results.violations.map((v) => v.id)).toEqual([])
  })
})
