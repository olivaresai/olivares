// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Root 20:12Z: Models > Routing crashed. The engine's routingPolicyDTO
// (modules/models/api.go) embeds routingSpec, whose list, number and string fields are all
// `omitempty` (modules/models/routing.go): a policy with only a strategy is sent as the
// four fields below and nothing else, and the card read `.length` of an absent list.
import { describe, expect, it, vi } from 'vitest'
import { renderIntel, screen, userEvent } from '@/test/intel'
import '@/features/_intel'
import './i18n'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    activeTenant: 't1',
    can: (p: string) => p === 'models:routing:read',
  }),
}))

// Exactly what the engine marshals for a cost policy with no other setting.
const STRATEGY_ONLY = {
  id: 'rp-wire',
  name: 'Cheapest first',
  enabled: true,
  strategy: 'cost',
}
const WITH_LISTS = {
  id: 'rp-lists',
  name: 'Vision only',
  enabled: true,
  strategy: 'capability',
  required_capabilities: ['vision'],
  preferred_providers: ['anthropic'],
  min_context_window: 200000,
}

const policies = vi.hoisted(() => ({ items: [] as unknown[] }))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  const lista = () => Promise.resolve({ items: [], has_more: false })
  return {
    ...actual,
    modelsApi: {
      ...actual.modelsApi,
      routingPolicies: () =>
        Promise.resolve({ items: policies.items, has_more: false }),
      workspaceResidency: lista,
      catalog: () => Promise.resolve({ models: [], capabilities: [] }),
      estate: lista,
      keys: lista,
    },
  }
})

const { ModelsView } = await import('./models-view')

async function openRouting() {
  const user = userEvent.setup()
  renderIntel(<ModelsView />)
  await user.click(await screen.findByRole('tab', { name: /routing/i }))
}

describe('a routing policy as the engine sends it', () => {
  it('shows a policy whose empty lists the engine left out', async () => {
    policies.items = [STRATEGY_ONLY]
    await openRouting()
    expect(await screen.findByText('Cheapest first')).toBeInTheDocument()
    expect(screen.queryByText(/Required capabilities/i)).toBeNull()
    expect(screen.queryByText(/Preferred providers/i)).toBeNull()
  })

  it('still shows the lists and the context floor when they are sent', async () => {
    policies.items = [WITH_LISTS]
    await openRouting()
    expect(await screen.findByText('Vision only')).toBeInTheDocument()
    expect(screen.getByText('vision')).toBeInTheDocument()
    expect(screen.getByText('anthropic')).toBeInTheDocument()
  })
})
