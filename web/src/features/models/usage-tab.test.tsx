// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Models > Usage: what the runs the caller may read used, per model, account, provider or tool.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { renderIntel, screen, userEvent, within } from '@/test/intel'
import '@/features/_intel'
import './i18n'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', can: () => true }),
}))

// Usage does not open for this person: no cost and no word for it.
const state = vi.hoisted(() => ({ noMoney: false }))
vi.mock('@/features/navigation/authorization', async (importOriginal) => {
  const actual =
    await importOriginal<typeof import('@/features/navigation/authorization')>()
  const navigable = (v: { id: string }) => !(state.noMoney && v.id === 'usage')
  return {
    ...actual,
    useViewAccess: () => ({
      state: () => 'allowed',
      navigable,
      listed: navigable,
    }),
  }
})

const usageMock = vi.fn()
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return {
    ...actual,
    modelsApi: {
      ...actual.modelsApi,
      usage: (...a: unknown[]) => usageMock(...a),
    },
  }
})

const profilesMock = vi.fn()
vi.mock('@/features/agentops/api', async (importOriginal) => {
  const actual =
    await importOriginal<typeof import('@/features/agentops/api')>()
  return {
    ...actual,
    agentOpsApi: {
      ...actual.agentOpsApi,
      listProfiles: (...a: unknown[]) => profilesMock(...a),
    },
  }
})

const { UsageTab } = await import('./usage-tab')

const row = (over: Record<string, unknown>) => ({
  runs: 0,
  input_tokens: null,
  output_tokens: null,
  cost_micro_usd: null,
  runs_without_tokens: 0,
  runs_without_cost: 0,
  ...over,
})

const TOTALS = {
  group_by: 'model',
  items: [
    row({
      key: 'claude-opus-5-5',
      runs: 3,
      input_tokens: 1200,
      output_tokens: 300,
      cost_micro_usd: 1_500_000,
    }),
    // A tool that reports no cost: the sum is null, never 0.
    row({
      key: 'gpt-6.1',
      runs: 2,
      input_tokens: 800,
      output_tokens: 100,
      runs_without_cost: 2,
    }),
    // Priced runs beside unpriced ones: the sums cover only some runs.
    row({
      key: 'claude-sonnet-5-5',
      runs: 2,
      input_tokens: 500,
      output_tokens: 70,
      cost_micro_usd: 900_000,
      runs_without_tokens: 1,
      runs_without_cost: 1,
    }),
    // Runs that recorded no model at all.
    row({ runs: 1, runs_without_tokens: 1, runs_without_cost: 1 }),
  ],
}

const DAILY = {
  group_by: 'model',
  bucket: 'day',
  items: [
    row({ key: 'claude-opus-5-5', day: '2026-11-02', runs: 3 }),
    row({ key: 'gpt-6.1', day: '2026-11-03', runs: 2 }),
  ],
}

beforeEach(() => {
  state.noMoney = false
  vi.useFakeTimers({ toFake: ['Date'] })
  vi.setSystemTime(new Date('2026-11-03T10:00:00Z'))
  profilesMock.mockReset().mockResolvedValue({ items: [] })
  usageMock
    .mockReset()
    .mockImplementation((q: { bucket?: string }) =>
      Promise.resolve(q.bucket ? DAILY : TOTALS),
    )
})

afterEach(() => vi.useRealTimers())

describe('Models > Usage', () => {
  it('leaves a cost no tool reported empty, never 0 and never "not reported"', async () => {
    renderIntel(<UsageTab />)
    const opus = await screen.findByRole('row', { name: /claude-opus-5-5/ })
    expect(within(opus).getByText('$1.50')).toBeInTheDocument()

    const gpt = screen.getByRole('row', { name: /gpt-6\.1/ })
    expect(within(gpt).queryByText(/\$/)).toBeNull()
    expect(within(gpt).queryByText(/not reported|without cost/)).toBeNull()

    // A sum over some runs is not the row's total: it says nothing.
    const mixed = screen.getByRole('row', { name: /claude-sonnet-5-5/ })
    expect(within(mixed).queryByText(/\$/)).toBeNull()
    expect(within(mixed).queryByText('500')).toBeNull()
    expect(within(mixed).queryByText('70')).toBeNull()

    expect(
      screen.getByRole('row', { name: /Not recorded/ }),
    ).toBeInTheDocument()
  })

  it('tells the cost only to a person Usage opens for: no $, no cost, no spend for anyone else', async () => {
    const user = userEvent.setup()
    const shown = renderIntel(<UsageTab />)
    const priced = await screen.findByRole('row', { name: /claude-opus-5-5/ })
    expect(within(priced).getByText('$1.50')).toBeInTheDocument()
    expect(
      screen.getByRole('columnheader', { name: 'Cost' }),
    ).toBeInTheDocument()
    shown.unmount()

    state.noMoney = true
    renderIntel(<UsageTab />)
    const opus = await screen.findByRole('row', { name: /claude-opus-5-5/ })
    // The runs and the tokens stay; the money column goes.
    expect(within(opus).getByText('3')).toBeInTheDocument()
    expect(
      screen.getByRole('columnheader', { name: 'Input tokens' }),
    ).toBeInTheDocument()
    expect(screen.queryByRole('columnheader', { name: /cost/i })).toBeNull()
    expect(document.body).not.toHaveTextContent(/\$|cost|spend/i)

    await user.click(screen.getByRole('button', { name: /show as table/i }))
    await screen.findByRole('row', { name: /2026-11-02/ })
    expect(document.body).not.toHaveTextContent(/\$|cost|spend/i)
  })

  it('asks for 7 days by model, then 30 days by account when switched', async () => {
    const user = userEvent.setup()
    renderIntel(<UsageTab />)
    await screen.findByRole('row', { name: /claude-opus-5-5/ })
    expect(usageMock).toHaveBeenCalledWith(
      expect.objectContaining({
        group_by: 'model',
        since: '2026-10-28T00:00:00Z',
      }),
    )

    await user.click(screen.getByRole('radio', { name: 'Account' }))
    await user.click(screen.getByRole('radio', { name: '30 days' }))
    await screen.findByRole('row', { name: /claude-opus-5-5/ })
    expect(usageMock).toHaveBeenCalledWith(
      expect.objectContaining({
        group_by: 'account',
        since: '2026-10-05T00:00:00Z',
      }),
    )
  })

  it('gives the daily chart a text alternative and a table view', async () => {
    const user = userEvent.setup()
    renderIntel(<UsageTab />)
    const chart = await screen.findByRole('img', { name: /Total 5/ })
    expect(chart).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: /show as table/i }))
    const day = await screen.findByRole('row', { name: /2026-11-02/ })
    expect(within(day).getByText('3')).toBeInTheDocument()
  })

  it('names an account by its profile and keeps the reference of one it cannot name', async () => {
    const user = userEvent.setup()
    profilesMock.mockResolvedValue({
      items: [
        {
          profile_ref: 'ppf_lena',
          driver: 'claude-code',
          display_name: 'Lena Claude',
        },
      ],
    })
    usageMock.mockImplementation((q: { bucket?: string }) =>
      Promise.resolve({
        group_by: 'account',
        items: q.bucket
          ? []
          : [
              row({ key: 'ppf_lena', runs: 2 }),
              row({ key: 'ppf_retired', runs: 1 }),
            ],
      }),
    )
    renderIntel(<UsageTab />)
    await user.click(await screen.findByRole('radio', { name: 'Account' }))
    expect(
      await screen.findByRole('row', { name: /Lena Claude/ }),
    ).toBeInTheDocument()
    expect(screen.getByRole('row', { name: /ppf_retired/ })).toBeInTheDocument()
    expect(screen.queryByText('ppf_lena')).toBeNull()
  })

  it('draws the busiest five keys by name and folds the rest into Other', async () => {
    const many = ['a', 'b', 'c', 'd', 'e', 'f'].map((k, i) =>
      row({ key: `model-${k}`, day: '2026-11-03', runs: 6 - i }),
    )
    usageMock.mockImplementation((q: { bucket?: string }) =>
      Promise.resolve(
        q.bucket ? { group_by: 'model', bucket: 'day', items: many } : TOTALS,
      ),
    )
    renderIntel(<UsageTab />)
    expect(await screen.findByText('Other')).toBeInTheDocument()
    expect(screen.getByText('model-e')).toBeInTheDocument()
    expect(screen.queryByText('model-f')).toBeNull()
  })

  it('explains that usage appears after the first session on a new install', async () => {
    usageMock.mockResolvedValue({ group_by: 'model', items: [] })
    renderIntel(<UsageTab />)
    expect(
      await screen.findByText(/appears here after the first session/i),
    ).toBeInTheDocument()
    expect(screen.queryByRole('table')).toBeNull()
  })
})
