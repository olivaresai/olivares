// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// One skills destination (#3388): MCP & skills' Skills tab holds the skill-pack
// catalog (install a pack, open one, see where it is assigned) beside the skills the
// inventory found. The catalog is a skills reader's, as it was on its own page.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import type { ReactNode } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

const authState = vi.hoisted(() => ({
  activeTenant: 't1' as string | null,
  perms: new Set<string>(),
}))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    activeTenant: authState.activeTenant,
    can: (p: string) => authState.perms.has(p),
  }),
}))
vi.mock('@/features/console/mcp-gateway-tab', () => ({
  MCPGatewayTab: () => <p>MCP gateway configuration</p>,
}))
vi.mock('@/features/skills/skills-view', () => ({
  SkillsView: () => <p>Skill pack catalog</p>,
}))

const api = vi.hoisted(() => ({
  listServers: vi.fn(),
  listTools: vi.fn(),
  listSkills: vi.fn(),
  wiring: vi.fn(),
  listConfigs: vi.fn(),
}))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, capabilitiesApi: { ...actual.capabilitiesApi, ...api } }
})

import { useModulesStore } from '@/stores/modules'
import CapabilitiesView from './capabilities-view'

function wrap(ui: ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

beforeEach(() => {
  for (const fn of Object.values(api))
    fn.mockResolvedValue({ items: [], has_more: false })
})
afterEach(() => {
  window.history.replaceState(null, '', '/')
  useModulesStore.setState({ off: new Set<string>() })
})

describe('skills in MCP & skills', () => {
  it('a skills reader installs and opens packs on the Skills tab', async () => {
    authState.perms = new Set([
      'capabilities:catalog:read',
      'skills:catalog:read',
    ])
    window.history.replaceState(null, '', '/capabilities?tab=skills')
    wrap(<CapabilitiesView />)
    expect(await screen.findByText('Skill pack catalog')).toBeInTheDocument()
    expect(
      await screen.findByText('No skills discovered yet'),
    ).toBeInTheDocument()
  })

  it('a catalog reader without skills read sees only what was found', async () => {
    authState.perms = new Set(['capabilities:catalog:read'])
    window.history.replaceState(null, '', '/capabilities?tab=skills')
    wrap(<CapabilitiesView />)
    await screen.findByText('No skills discovered yet')
    expect(screen.queryByText('Skill pack catalog')).not.toBeInTheDocument()
  })

  it('follows the skills module: off, the catalog is not offered', async () => {
    authState.perms = new Set([
      'capabilities:catalog:read',
      'skills:catalog:read',
    ])
    useModulesStore.setState({ off: new Set(['skills']) })
    window.history.replaceState(null, '', '/capabilities?tab=skills')
    wrap(<CapabilitiesView />)
    await screen.findByText('No skills discovered yet')
    expect(screen.queryByText('Skill pack catalog')).not.toBeInTheDocument()
  })
})
