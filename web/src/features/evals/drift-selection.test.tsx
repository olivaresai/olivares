// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  act,
  createTestQueryClient,
  renderIntel,
  screen,
  userEvent,
} from '@/test/intel'
import '@/features/_intel'
import './i18n'
import { evalsKeys } from './api'
import { EvalsView } from './evals-view'
import { suitesFixture } from './fixtures'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', can: () => true }),
}))
// Observe chart inputs at the renderer boundary; the real HTTP adapter, queries,
// picker, DriftChart and suite lookup all run. jsdom cannot draw Recharts geometry.
vi.mock('@/components/charts', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/components/charts')>()),
  TrendChart: ({
    reference,
    data,
  }: {
    reference?: { y: number }
    data: { score: number }[]
  }) => (
    <output aria-label="Chart inputs">
      {JSON.stringify({
        threshold: reference?.y,
        scores: data.map((p) => p.score),
      })}
    </output>
  ),
}))

const row = {
  suite_ref: 'suite-b',
  subject_ref: 'reviewer',
  subject_kind: 'agent',
  runs: 2,
  mean_score: 0.9,
  pass_rate: 0.9,
  last_score: 0.9,
  regressed: false,
  trend: [{ at: '2026-10-01T00:00:00Z', score: 0.9, pass_rate: 0.9 }],
}
let rows: (typeof row & { prompt_variant?: string })[] = [row]
let suites = [
  {
    ...suitesFixture[0],
    id: 'suite-a',
    name: 'reviewer checks',
    pass_threshold: 0.5,
  },
  {
    ...suitesFixture[0],
    id: 'suite-b',
    name: 'General checks',
    pass_threshold: 0.9,
  },
]

beforeEach(() => {
  rows = [row]
  suites = [
    {
      ...suitesFixture[0],
      id: 'suite-a',
      name: 'reviewer checks',
      pass_threshold: 0.5,
    },
    {
      ...suitesFixture[0],
      id: 'suite-b',
      name: 'General checks',
      pass_threshold: 0.9,
    },
  ]
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string) => {
      const path = new URL(String(url), 'http://test').pathname
      const items = path.endsWith('/scorecards')
        ? rows
        : path.endsWith('/suites')
          ? suites
          : []
      return new Response(JSON.stringify({ items, has_more: false }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    }),
  )
})
afterEach(() => vi.unstubAllGlobals())

async function openDrift() {
  const user = userEvent.setup()
  const queryClient = createTestQueryClient()
  renderIntel(<EvalsView />, { queryClient })
  await user.click(screen.getByRole('tab', { name: 'Drift' }))
  await screen.findByRole('combobox', { name: 'Subject' })
  return { user, queryClient }
}

describe('drift scorecard identity', () => {
  it('keeps an explicitly selected card after a sibling appears on refresh', async () => {
    rows = [{ ...row, subject_ref: 'other', regressed: true }, row]
    const { user, queryClient } = await openDrift()
    await user.click(screen.getByRole('combobox', { name: 'Subject' }))
    await user.click(screen.getByRole('option', { name: 'reviewer' }))
    expect(screen.getByText('Drift — reviewer')).toBeVisible()
    rows = [
      { ...row, subject_ref: 'other', regressed: true },
      { ...row, suite_ref: 'suite-a' },
      row,
    ]
    await act(async () => {
      await queryClient.invalidateQueries({
        queryKey: evalsKeys.scorecards('t1', 'agent'),
      })
    })
    await user.click(screen.getByRole('combobox', { name: 'Subject' }))
    await screen.findByRole('option', { name: 'reviewer · suite-a' })
    await user.keyboard('{Escape}')
    expect(screen.getByRole('combobox', { name: 'Subject' })).toHaveTextContent(
      'reviewer · suite-b',
    )
    expect(screen.getByText('Drift — reviewer · suite-b')).toBeVisible()
    expect(screen.getByLabelText('Chart inputs')).toHaveTextContent(
      '"scores":[0.9]',
    )
    expect(screen.queryByText('Drift — other')).not.toBeInTheDocument()
  })

  it('selects each separator-colliding card independently', async () => {
    rows = [
      { ...row, subject_ref: 'reviewer', prompt_variant: 'baseline · tuned' },
      {
        ...row,
        subject_ref: 'reviewer · baseline',
        prompt_variant: 'tuned',
        trend: [{ ...row.trend[0], score: 0.4 }],
      },
    ]
    const { user } = await openDrift()
    await user.click(screen.getByRole('combobox', { name: 'Subject' }))
    const options = screen.getAllByRole('option')
    expect(options).toHaveLength(2)
    await user.click(options[1])
    expect(screen.getByLabelText('Chart inputs')).toHaveTextContent(
      '"scores":[0.4]',
    )
    await user.click(screen.getByRole('combobox', { name: 'Subject' }))
    await user.click(screen.getAllByRole('option')[0])
    expect(screen.getByLabelText('Chart inputs')).toHaveTextContent(
      '"scores":[0.9]',
    )
  })
})

describe('drift suite thresholds', () => {
  it.each(['', 'tuned'])(
    'uses the exact suite even with variant %s and duplicate subjects',
    async (variant) => {
      rows = [
        { ...row, prompt_variant: variant },
        { ...row, suite_ref: 'suite-a', prompt_variant: variant },
      ]
      await openDrift()
      expect(screen.getByLabelText('Chart inputs')).toHaveTextContent(
        '"threshold":0.9',
      )
    },
  )

  it.each(['missing', 'failed', 'empty'])(
    'draws no threshold when the suite is %s',
    async (state) => {
      suites = state === 'empty' ? [] : [suites[0]]
      if (state === 'failed') {
        vi.stubGlobal(
          'fetch',
          vi.fn(async (url: string) => {
            const isSuites = String(url).includes('/suites')
            return new Response(
              JSON.stringify(
                isSuites
                  ? { error: 'unavailable' }
                  : { items: rows, has_more: false },
              ),
              {
                status: isSuites ? 503 : 200,
                headers: { 'Content-Type': 'application/json' },
              },
            )
          }),
        )
      }
      await openDrift()
      expect(screen.getByLabelText('Chart inputs')).not.toHaveTextContent(
        'threshold',
      )
    },
  )
})
