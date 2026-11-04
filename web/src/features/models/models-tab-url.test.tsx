// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Models keeps its tab in the address: a reload or a pasted link restores it, a click
// replaces the history entry, and Back follows the address instead of leaving the old tab up.
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { act, renderIntel, screen, userEvent } from '@/test/intel'
import '@/features/_intel'
import { fakeRouter } from '@/test/fake-router'
import './i18n'

vi.mock('@tanstack/react-router', async () =>
  (await import('@/test/fake-router')).fakeRouterModule(),
)
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', can: () => false }),
}))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  const lista = () => Promise.resolve({ items: [], has_more: false })
  return {
    ...actual,
    modelsApi: {
      ...actual.modelsApi,
      routingPolicies: lista,
      workspaceResidency: lista,
      catalog: () => Promise.resolve({ models: [], capabilities: [] }),
      estate: lista,
      keys: lista,
    },
  }
})

const { ModelsView } = await import('./models-view')

const selected = (name: RegExp) =>
  screen.getByRole('tab', { name }).getAttribute('aria-selected')

describe('ModelsView — the tab lives in the address', () => {
  beforeEach(() => {
    fakeRouter.reset('/models')
  })

  it('opens the tab a pasted or reloaded URL names', () => {
    fakeRouter.reset('/models?tab=estate')
    renderIntel(<ModelsView />)
    expect(selected(/^Estate$/)).toBe('true')
    expect(selected(/^Available$/)).toBe('false')
  })

  it('writes the chosen tab with replace and follows Back', async () => {
    const user = userEvent.setup()
    renderIntel(<ModelsView />)
    await user.click(screen.getByRole('tab', { name: /^Catalog$/ }))
    expect(fakeRouter.url()).toBe('/models?tab=catalog')
    expect(fakeRouter.depth()).toBe(1)

    act(() => fakeRouter.go('/models?tab=estate'))
    expect(selected(/^Estate$/)).toBe('true')
    act(() => fakeRouter.back())
    expect(selected(/^Catalog$/)).toBe('true')
  })

  it('falls back to the first tab for an unknown value', () => {
    fakeRouter.reset('/models?tab=nope')
    renderIntel(<ModelsView />)
    expect(selected(/^Available$/)).toBe('true')
    expect(fakeRouter.url()).toBe('/models?tab=nope')
  })
})
