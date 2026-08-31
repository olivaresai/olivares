// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { renderIntel, screen, userEvent } from '@/test/intel'
import '@/features/_intel'
import './i18n'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', can: () => true }),
}))

// The badge is mounted for real. Only the list body is suppressed so a fixture with the
// engine's actual 1000-row ceiling does not build thousands of unrelated table cells.
vi.mock('@/features/_intel', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/features/_intel')>()
  return { ...actual, AsyncSection: () => null }
})

const api = vi.hoisted(() => ({
  scorecards: vi.fn(),
  suites: vi.fn(),
  runs: vi.fn(),
  runResults: vi.fn(),
  calibrationItems: vi.fn(),
  calibrationReports: vi.fn(),
  gates: vi.fn(),
}))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, evalsApi: { ...actual.evalsApi, ...api } }
})

const { EvalsView } = await import('./evals-view')
const page = (n: number, hasMore = false) => ({
  items: Array.from({ length: n }, () => ({})),
  has_more: hasMore,
})

beforeEach(() => {
  vi.clearAllMocks()
  api.scorecards.mockResolvedValue(page(0))
  api.suites.mockResolvedValue(page(0))
  api.runs.mockResolvedValue(page(0))
  api.runResults.mockResolvedValue(page(0))
  api.calibrationItems.mockResolvedValue(page(0))
  api.calibrationReports.mockResolvedValue(page(0))
  api.gates.mockResolvedValue(page(0))
})

async function open(tab: RegExp) {
  const user = userEvent.setup()
  renderIntel(<EvalsView />)
  await user.click(screen.getByRole('tab', { name: tab }))
}

describe('EvalsView truncation badges are mounted and query-specific', () => {
  it.each([
    ['runs', /Runs/i, api.runs, 17],
    ['A/B suites', /^A\/B$/i, api.suites, 11],
    ['drift suites', /Drift/i, api.suites, 13],
    ['gates', /CI gate/i, api.gates, 7],
    ['calibration reports', /Judge calibration/i, api.calibrationReports, 19],
    ['calibration items', /Judge calibration/i, api.calibrationItems, 6],
  ])(
    '%s shows the loaded count only for its has_more response',
    async (_name, tab, mock, loaded) => {
      mock.mockResolvedValue(page(loaded, true))
      await open(tab)
      expect(
        await screen.findByText(`Loaded ${loaded} rows; there are more`),
      ).toBeVisible()
      expect(screen.getAllByText(/there are more/i)).toHaveLength(1)
    },
  )

  it('the same full-sized page without has_more renders no badge', async () => {
    api.runs.mockResolvedValue(page(1000, false))
    await open(/Runs/i)
    expect(screen.queryByText(/there are more/i)).not.toBeInTheDocument()
  })
})
