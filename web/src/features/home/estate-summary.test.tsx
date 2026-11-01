// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE ESTATE IN ONE SENTENCE (#2546): what runs, for whom, what it cost today and what
// needs a person, then today against yesterday and the month. A clause whose source has
// no answer is left out; nothing in it is a made-up zero.
import { describe, expect, it } from 'vitest'
import { renderIntel, screen } from '@/test/intel'
import { expectNoRawI18nKeys } from '@/test/i18n-keys'
import { EstateSummary, type EstateSummaryProps } from './estate-summary'

const FULL: EstateSummaryProps = {
  state: 'ready',
  running: 7,
  people: 6,
  today: {
    sessions: 9,
    costMicroUsd: 6_840_000,
    unpriced: 0,
    inputTokens: null,
    outputTokens: null,
    tokensUnreported: 0,
  },
  todayFloor: false,
  yesterday: {
    sessions: 5,
    costMicroUsd: 5_100_000,
    unpriced: 0,
    inputTokens: null,
    outputTokens: null,
    tokensUnreported: 0,
  },
  needs: { count: 3, more: false },
  month: {
    spentMicroUsd: 186_400_000,
    limitMicroUsd: 400_000_000,
    atLeast: false,
  },
}

const sentence = () => screen.getByTestId('estate-summary-sentence')

describe('EstateSummary', () => {
  it('says the estate in one sentence, then today against yesterday and the month', () => {
    const { container } = renderIntel(<EstateSummary {...FULL} />)
    expect(sentence()).toHaveTextContent(
      '7 sessions open for 6 people · $6.84 today · 3 need you',
    )
    const spend = screen.getByTestId('estate-summary-spend')
    expect(spend).toHaveTextContent('Yesterday $5.10')
    expect(spend).toHaveTextContent('$186.40 of $400.00 this month')
    const meter = screen.getByRole('meter', { name: /this month/ })
    expect(meter).toHaveAttribute('aria-valuenow', '186.4')
    expect(meter).toHaveAttribute('aria-valuemax', '400')
    expectNoRawI18nKeys(container)
  })

  it('leaves out whom the sessions act for when one of them does not say', () => {
    renderIntel(<EstateSummary {...FULL} people={null} />)
    expect(sentence()).toHaveTextContent(
      '7 sessions open · $6.84 today · 3 need you',
    )
  })

  it('a cut page makes today a floor, and a session with no cost is said', () => {
    renderIntel(
      <EstateSummary
        {...FULL}
        todayFloor
        today={{
          sessions: 9,
          costMicroUsd: 6_840_000,
          unpriced: 2,
          inputTokens: null,
          outputTokens: null,
          tokensUnreported: 0,
        }}
      />,
    )
    expect(sentence()).toHaveTextContent('at least $6.84 today')
    expect(screen.getByTestId('estate-summary-spend')).toHaveTextContent(
      '2 reported no cost',
    )
  })

  it('a quiet estate says so, with no $0 and no "0 need you"', () => {
    renderIntel(
      <EstateSummary
        {...FULL}
        running={0}
        people={0}
        today={{
          sessions: 0,
          costMicroUsd: null,
          unpriced: 0,
          inputTokens: null,
          outputTokens: null,
          tokensUnreported: 0,
        }}
        yesterday={{
          sessions: 0,
          costMicroUsd: null,
          unpriced: 0,
          inputTokens: null,
          outputTokens: null,
          tokensUnreported: 0,
        }}
        needs={{ count: 0, more: false }}
        month={null}
      />,
    )
    expect(sentence()).toHaveTextContent(/^Nothing is running$/)
    expect(screen.queryByTestId('estate-summary-spend')).toBeNull()
    expect(screen.queryByRole('meter')).toBeNull()
  })

  it('a page with more requests behind it says so, never a count it did not read', () => {
    renderIntel(<EstateSummary {...FULL} needs={{ count: 20, more: true }} />)
    expect(sentence()).toHaveTextContent('20+ need you')
  })

  it('a live page cut short says at least how many run, and not for how many people', () => {
    renderIntel(<EstateSummary {...FULL} runningMore />)
    expect(sentence()).toHaveTextContent('7+ sessions open · $6.84 today')
    expect(sentence()).not.toHaveTextContent('6 people')
  })

  it('a cut live page with no live session on it never says nothing is running', () => {
    renderIntel(<EstateSummary {...FULL} running={0} runningMore />)
    expect(sentence()).toHaveTextContent('$6.84 today · 3 need you')
    expect(sentence()).not.toHaveTextContent('Nothing is running')
  })

  it('while the sessions are read there is no figure at all', () => {
    renderIntel(
      <EstateSummary
        {...FULL}
        state="loading"
        running={null}
        today={null}
        yesterday={null}
      />,
    )
    expect(screen.queryByTestId('estate-summary-sentence')).toBeNull()
    expect(screen.getByTestId('estate-summary')).not.toHaveTextContent(/\d/)
  })

  it.each([
    ['pendingPaused', 'Query paused — waiting to resume'],
    ['pendingIdle', 'Query not started — open to load'],
  ] as const)(
    'a read with no answer that is not fetching says why (%s), never a bare skeleton',
    (pending, words) => {
      renderIntel(
        <EstateSummary
          {...FULL}
          state="loading"
          pending={pending}
          running={null}
          today={null}
          yesterday={null}
        />,
      )
      expect(screen.getByTestId('estate-summary')).toHaveTextContent(words)
      expect(screen.queryByTestId('estate-summary-sentence')).toBeNull()
    },
  )

  it('a failed read is said, never a zero', () => {
    renderIntel(
      <EstateSummary
        {...FULL}
        state="unavailable"
        running={null}
        today={null}
        yesterday={null}
      />,
    )
    expect(screen.getByTestId('estate-summary')).toHaveTextContent(
      'The sessions could not be read.',
    )
    expect(screen.queryByTestId('estate-summary-sentence')).toBeNull()
  })

  it('a proven floor of the month says "at least", and past most of the budget the meter warns', () => {
    renderIntel(
      <EstateSummary
        {...FULL}
        month={{
          spentMicroUsd: 350_000_000,
          limitMicroUsd: 400_000_000,
          atLeast: true,
        }}
      />,
    )
    expect(screen.getByTestId('estate-summary-spend')).toHaveTextContent(
      'at least $350.00 of $400.00 this month',
    )
    const meter = screen.getByRole('meter', { name: /this month/ })
    expect(meter.firstElementChild).toHaveClass('bg-warning')
  })

  it('over the budget the meter is full and says danger, in words too', () => {
    renderIntel(
      <EstateSummary
        {...FULL}
        month={{
          spentMicroUsd: 450_000_000,
          limitMicroUsd: 400_000_000,
          atLeast: false,
        }}
      />,
    )
    const meter = screen.getByRole('meter', { name: /this month/ })
    expect(meter).toHaveAttribute('aria-valuenow', '400')
    expect(meter).toHaveAttribute(
      'aria-valuetext',
      '$450.00 of $400.00 this month',
    )
    expect(meter.firstElementChild).toHaveClass('bg-danger')
  })
})
