// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// What an account's usage says where the console shows the account: the 5-hour and
// weekly windows the tool reported, nothing at all (never 0 %) when it reported none,
// and a window at 100 % marked with when it resets.
import { render, screen } from '@testing-library/react'
import axe from 'axe-core'
import { describe, expect, it } from 'vitest'
import { findRawI18nKeys } from '@/test/i18n-keys'
import { AccountUsage, AtLimit, limitReached } from './account-usage'
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
  it('names the 5-hour and the weekly window with their percent, rounded', () => {
    const { container } = render(
      <AccountUsage usage={[win('seven_day', 17.4), win('five_hour', 41.6)]} />,
    )
    // The 5-hour window comes first whatever order the engine sent.
    expect(container.textContent).toBe('5-hour 42% · Weekly 17%')
    expect(findRawI18nKeys(container)).toEqual([])
  })

  it('leaves out a window the tool did not report; it is unknown, not 0 %', () => {
    const { container } = render(<AccountUsage usage={[win('five_hour', 9)]} />)
    expect(container.textContent).toBe('5-hour 9%')
    expect(container.textContent).not.toContain('Weekly')
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
    expect(container.textContent).toBe('5-hour 99% · Weekly 100%')
  })

  it('shows additional windows with their provider label', () => {
    const { container } = render(
      <AccountUsage usage={[win('seven_day:opus', 55), win('five_hour', 3)]} />,
    )
    expect(container.textContent).toBe('5-hour 3% · seven_day:opus 55%')
  })
  it('shows a monthly cap and credit balance without inventing a percent', () => {
    const { container } = render(
      <AccountUsage
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
    expect(container.textContent).toBe(
      'Monthly extra usage 85% · Credits 12.50',
    )
  })
  it('keeps zero, unlimited and unknown credits distinct', () => {
    const row = {
      window: 'credits',
      label: 'Credits',
      observed_at: '2026-10-29T10:00:00Z',
    }
    const { container, rerender } = render(
      <AccountUsage
        usage={[{ ...row, credit: { balance: '0', unlimited: false } }]}
      />,
    )
    expect(container.textContent).toBe('Credits 0')
    expect(
      limitReached([{ ...row, credit: { balance: '0', unlimited: false } }]),
    ).toBeUndefined()
    rerender(<AccountUsage usage={[{ ...row, credit: { unlimited: true } }]} />)
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
    expect(container.querySelector('.text-warning')).toHaveTextContent('80%')
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
    expect(container.textContent).toBe('Weekly 20%')
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

  it('names a per-model window by the label the tool gave it', () => {
    render(
      <AtLimit
        usage={[win('seven_day:opus', 100, { label: 'Opus weekly' })]}
      />,
    )
    expect(screen.getByRole('status')).toHaveTextContent(
      'Opus weekly limit reached',
    )
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
