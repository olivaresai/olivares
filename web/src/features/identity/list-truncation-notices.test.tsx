// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The identity screens say when a list they read came back cut short.
//
// The views are MOUNTED, not read as source: `{false && <ListTruncationBadge/>}` would leave the
// notice written and unreachable, and the census in registry.truncation-notice.test.ts (a source
// scan) would accept it. Each list is a pair: a `has_more: true` case that must show the notice
// and a `has_more: false` case that must not, so the notice follows the engine and is not
// painted unconditionally.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import './i18n'

const { api, authState } = vi.hoisted(() => ({
  api: {
    findings: vi.fn(),
  },
  authState: {
    activeTenant: 't1' as string | null,
    principal: null,
    can: (_permission: string) => true,
  },
}))

vi.mock('@tanstack/react-router', () => ({ useNavigate: () => vi.fn() }))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, identityApi: api }
})

// Import AFTER the mocks are declared.
import { identityKeys } from './api'
import { McpAuthTab } from './mcp-auth'

const FINDING = {
  id: 'f1',
  kind: 'mcp_auth',
  severity: 'medium',
  status: 'open',
  title: 'token-binding-verified',
}
function mount(ui: React.ReactElement) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  render(<QueryClientProvider client={client}>{ui}</QueryClientProvider>)
  return client
}

beforeEach(() => {
  api.findings.mockResolvedValue({ items: [], has_more: false })
})

afterEach(() => {
  vi.clearAllMocks()
})

describe('MCP authorization findings declare a cut-short list', () => {
  it('names the rows LOADED when the engine reports has_more', async () => {
    api.findings.mockResolvedValue({ items: [FINDING], has_more: true })
    mount(<McpAuthTab />)
    expect(
      await screen.findByText('Loaded 1 findings; there are more'),
    ).toBeVisible()
  })

  it('does not warn when the list is complete', async () => {
    api.findings.mockResolvedValue({ items: [FINDING], has_more: false })
    mount(<McpAuthTab />)
    await screen.findByText('token-binding-verified')
    expect(screen.queryByText(/there are more/i)).not.toBeInTheDocument()
  })

  it('does not contradict the empty state when no row loaded', async () => {
    api.findings.mockResolvedValue({ items: [], has_more: true })
    mount(<McpAuthTab />)
    await screen.findByText('No MCP authorization findings')
    expect(screen.queryByText(/there are more/i)).not.toBeInTheDocument()
  })

  it('drops the notice when a refetch fails over the stale page', async () => {
    api.findings.mockResolvedValue({ items: [FINDING], has_more: true })
    const client = mount(<McpAuthTab />)
    await screen.findByText('Loaded 1 findings; there are more')
    api.findings.mockRejectedValue(new Error('engine unavailable'))
    await act(() =>
      client.refetchQueries({
        queryKey: identityKeys.findings('t1', 'mcp_auth'),
      }),
    )
    await waitFor(() =>
      expect(screen.queryByText(/there are more/i)).not.toBeInTheDocument(),
    )
  })
})

// The SCIM leaver list's notice tests live in scim-leaver-latest.test.tsx: that card reads
// the ledger page by page, so its tests need a ledger and not a single `{ items, has_more }`.
