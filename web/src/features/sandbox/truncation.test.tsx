// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { renderIntel, screen, userEvent } from '@/test/intel'
import '@/features/_intel'
import './i18n'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    activeTenant: 't1',
    can: () => true,
    confinedWorkspace: null,
  }),
}))
vi.mock('@/features/_intel', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/features/_intel')>()
  return { ...actual, AsyncSection: () => null }
})

const api = vi.hoisted(() => ({
  scenarios: vi.fn(),
  runs: vi.fn(),
  comparisons: vi.fn(),
}))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, sandboxApi: { ...actual.sandboxApi, ...api } }
})

const { SandboxView } = await import('./sandbox-view')
const page = (n: number, hasMore = false) => ({
  items: Array.from({ length: n }, () => ({})),
  has_more: hasMore,
})

beforeEach(() => {
  vi.clearAllMocks()
  api.scenarios.mockResolvedValue(page(0))
  api.runs.mockResolvedValue(page(0))
  api.comparisons.mockResolvedValue(page(0))
})

describe('SandboxView truncation badges are mounted and query-specific', () => {
  it.each([
    ['runs', /Runs/i, api.runs, 17],
    ['comparisons', /Comparisons/i, api.comparisons, 23],
    ['scenarios', /Scenarios/i, api.scenarios, 31],
  ])('%s shows exactly its loaded rows', async (_name, tab, mock, loaded) => {
    mock.mockResolvedValue(page(loaded, true))
    const user = userEvent.setup()
    renderIntel(<SandboxView />)
    if (!screen.getByRole('tab', { name: tab }).hasAttribute('data-state')) {
      await user.click(screen.getByRole('tab', { name: tab }))
    } else if (
      screen.getByRole('tab', { name: tab }).getAttribute('data-state') !==
      'active'
    ) {
      await user.click(screen.getByRole('tab', { name: tab }))
    }
    expect(
      await screen.findByText(`Loaded ${loaded} rows; there are more`),
    ).toBeVisible()
    expect(screen.getAllByText(/there are more/i)).toHaveLength(1)
  })

  it('has_more false is the non-firing direction', async () => {
    api.runs.mockResolvedValue(page(1000, false))
    renderIntel(<SandboxView />)
    expect(screen.queryByText(/there are more/i)).not.toBeInTheDocument()
  })
})
