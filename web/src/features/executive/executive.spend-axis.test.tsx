// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { ReactNode } from 'react'
import { afterEach, expect, it, vi } from 'vitest'
import { act, renderIntel } from '@/test/intel'
import i18n from '@/lib/i18n'
import type { TrendChartProps } from '@/components/charts'
import { SpendSection } from './components'
import type { CostKpi } from './derive'

const capture = vi.hoisted(() => ({
  props: undefined as TrendChartProps | undefined,
}))
vi.mock('@/components/charts', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/components/charts')>()),
  TrendChart: (props: TrendChartProps) => {
    capture.props = props
    return <div />
  },
}))
vi.mock('@tanstack/react-router', () => ({
  Link: ({ children, to }: { children: ReactNode; to: string }) => (
    <a href={to}>{children}</a>
  ),
}))

const cost: CostKpi = {
  totalMicroUsd: 2_600_000_000,
  inputTokens: 10,
  outputTokens: 20,
  samples: 3,
  trend: [
    { key: '2026-06-01', cost: 1 },
    { key: '2026-06-02', cost: 2_600_000_000 },
  ],
  deltaPct: null,
  projectedMicroUsd: null,
  projectedOver: false,
  activeModels: null,
  truncated: false,
}
const spaces = (s: string) => s.replace(/[\u00a0\u202f]/g, ' ')

afterEach(async () => {
  await act(() => i18n.changeLanguage('en'))
  capture.props = undefined
})

it('measures spend labels and uses integer micro-USD ticks without rewriting observations', () => {
  renderIntel(<SpendSection cost={cost} />)
  expect(capture.props?.yAxis).toEqual({ width: 'auto', allowDecimals: false })
  expect(capture.props?.data).toBe(cost.trend)
  expect(cost.trend.map((point) => point.cost)).toEqual([1, 2_600_000_000])
})

it.each([
  [
    'en',
    '$2,600.00',
    '$1.234567',
    '$246,913.56',
    '$0.000001',
    '$0.00',
    '-$0.000015',
  ],
  [
    'es',
    '2600,00 US$',
    '1,234567 US$',
    '246.913,56 US$',
    '0,000001 US$',
    '0,00 US$',
    '-0,000015 US$',
  ],
  [
    'ru',
    '2 600,00 $',
    '1,234567 $',
    '246 913,56 $',
    '0,000001 $',
    '0,00 $',
    '-0,000015 $',
  ],
])(
  'keeps currency, small nonzero values and full tooltip precision after switching to %s',
  async (locale, thousands, fractional, large, minimum, zero, credit) => {
    renderIntel(<SpendSection cost={cost} />)
    await act(() => i18n.changeLanguage(locale))
    const { valueFormatter: tick, tooltipValueFormatter: tip } = capture.props!
    expect(spaces(tick!(2_600_000_000))).toBe(thousands)
    expect(spaces(tick!(1))).toBe(minimum)
    expect(spaces(tick!(0))).toBe(zero)
    expect(spaces(tip!(1_234_567))).toBe(fractional)
    expect(spaces(tip!(246_913_560_000))).toBe(large)
    expect(tip!(246_913_560_000)).not.toBe(tick!(246_913_560_000))
    expect(spaces(tip!(1))).toBe(minimum)
    expect(spaces(tip!(0))).toBe(zero)
    expect(spaces(tip!(-15))).toBe(credit)
  },
)
