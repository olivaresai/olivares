// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { expect, it, vi } from 'vitest'
import { renderIntel, screen } from '@/test/intel'
import '@/features/_intel'
import './i18n'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', can: () => true }),
}))
vi.mock('@tanstack/react-router', () => ({ Link: () => null }))
vi.mock('@/features/_intel', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/features/_intel')>()
  return { ...actual, AsyncSection: () => null }
})

const api = vi.hoisted(() => ({ findings: vi.fn(), inventory: vi.fn() }))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, rateLimitsApi: api }
})
const { RateLimitsView } = await import('./rate-limits-view')

it('mounts the findings badge from the loaded rows and has_more', async () => {
  api.findings.mockResolvedValue({
    items: Array.from({ length: 37 }, () => ({})),
    has_more: true,
  })
  api.inventory.mockResolvedValue({ available: false, rate_limits: [] })
  renderIntel(<RateLimitsView />)
  expect(
    await screen.findByText('Loaded 37 findings; there are more'),
  ).toBeVisible()
})
