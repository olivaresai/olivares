// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// One provider destination (#3388): provider keys live in AI tools. Models is
// called Models and keeps no "Provider keys" tab of its own; the workspace residency that
// tab also carried stays in Models, under Governance.
import { describe, expect, it, vi } from 'vitest'
import { renderIntel, screen, userEvent } from '@/test/intel'
import '@/features/_intel'
import './i18n'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', can: () => true }),
}))

vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  const lista = () => Promise.resolve({ items: [], has_more: false })
  return {
    ...actual,
    modelsApi: {
      ...actual.modelsApi,
      workspaceResidency: lista,
      catalog: () => Promise.resolve({ models: [], capabilities: [] }),
      estate: lista,
      routingPolicies: lista,
      keys: lista,
    },
  }
})

const { ModelsView } = await import('./models-view')

describe('Models after the Tools fold', () => {
  it('is called Models and has no provider keys tab', async () => {
    renderIntel(<ModelsView />)
    expect(
      await screen.findByRole('heading', { level: 1, name: 'Models' }),
    ).toBeInTheDocument()
    expect(screen.queryByRole('tab', { name: /keys/i })).not.toBeInTheDocument()
  })

  it('keeps workspace residency under Governance', async () => {
    const user = userEvent.setup()
    renderIntel(<ModelsView />)
    await user.click(await screen.findByRole('tab', { name: 'Governance' }))
    await user.click(
      await screen.findByRole('tab', { name: 'Workspace residency' }),
    )
    expect(
      await screen.findByRole('heading', { name: 'Workspace residency' }),
    ).toBeInTheDocument()
  })
})
