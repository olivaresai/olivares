// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// One MCP servers destination (#3388): MCP & skills' Servers tab is where a
// server is added, tested and enabled (the tenant gateway) and where the servers the
// inventory discovered are listed. The gateway part is a tenant administrator's, as it was
// on its own page; the tab the address names opens first.
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

import CapabilitiesView from './capabilities-view'

function wrap(ui: ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

beforeEach(() => {
  for (const fn of Object.values(api))
    fn.mockResolvedValue({ items: [], has_more: false })
})
afterEach(() => window.history.replaceState(null, '', '/'))

describe('MCP servers in MCP & skills', () => {
  it('a tenant administrator adds and enables servers on the Servers tab', async () => {
    authState.perms = new Set(['capabilities:catalog:read', 'tenant:admin'])
    wrap(<CapabilitiesView />)
    expect(
      await screen.findByText('MCP gateway configuration'),
    ).toBeInTheDocument()
    expect(
      await screen.findByText('No MCP servers discovered yet'),
    ).toBeInTheDocument()
  })

  it('a catalog reader sees the discovered servers and no gateway', async () => {
    authState.perms = new Set(['capabilities:catalog:read'])
    wrap(<CapabilitiesView />)
    await screen.findByText('No MCP servers discovered yet')
    expect(
      screen.queryByText('MCP gateway configuration'),
    ).not.toBeInTheDocument()
  })

  it('opens the tab its address names', async () => {
    authState.perms = new Set(['capabilities:catalog:read'])
    window.history.replaceState(null, '', '/capabilities?tab=skills')
    wrap(<CapabilitiesView />)
    expect(
      await screen.findByRole('tab', { name: 'Skills', selected: true }),
    ).toBeInTheDocument()
  })
})
