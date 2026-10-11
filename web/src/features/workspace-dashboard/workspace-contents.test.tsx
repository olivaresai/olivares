// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { configureApiClient } from '@/lib/api/client'
import { WorkspaceContents } from './workspace-contents'

let requests: { url: string; tenant: string | null }[]

function serve(body: unknown, status = 200) {
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string, init: RequestInit) => {
      requests.push({
        url,
        tenant: new Headers(init.headers).get('X-Olivares-Tenant'),
      })
      return new Response(JSON.stringify(body), {
        status,
        headers: { 'Content-Type': 'application/json' },
      })
    }),
  )
}

function show() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return render(
    <QueryClientProvider client={client}>
      <WorkspaceContents tenant="tenant-a" workspaceId="ws/one" />
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  requests = []
  configureApiClient({
    getToken: () => null,
    getTenant: () => 'wrong-tenant',
    onUnauthorized: () => {},
    refreshSession: undefined,
    getExpiresAt: undefined,
  })
})
afterEach(() => vi.unstubAllGlobals())

describe('WorkspaceContents reads the engine contents read', () => {
  it('lists every held kind the engine returns, floors included, for the pinned organization', async () => {
    serve({
      workspace_id: 'ws/one',
      kinds: [
        { kind: 'core.agent', count: 2, capped: false },
        { kind: 'core.session', count: 0, capped: false },
        { kind: 'sessions.work_item', count: 1000, capped: true },
      ],
    })
    show()
    const table = await screen.findByRole('table')
    const rows = within(table).getAllByRole('row').slice(1)
    expect(rows.map((row) => row.textContent)).toEqual([
      'core.agent2',
      'sessions.work_item≥ 1,000',
    ])
    expect(requests).toEqual([
      { url: '/v1/workspaces/ws%2Fone/contents', tenant: 'tenant-a' },
    ])
    expect(
      screen.getByRole('region', { name: 'Workspace contents' }),
    ).toContainElement(table)
  })

  it('says the workspace is empty when every kind counts zero', async () => {
    serve({
      workspace_id: 'ws/one',
      kinds: [{ kind: 'core.agent', count: 0, capped: false }],
    })
    show()
    expect(
      await screen.findByText('This workspace holds nothing yet.'),
    ).toHaveAttribute('role', 'status')
    expect(screen.queryByRole('table')).toBeNull()
  })

  it('says the read failed instead of showing an empty workspace', async () => {
    serve(
      {
        error: {
          code: 'workspace_contents_unavailable',
          message: 'Workspace contents unavailable.',
        },
      },
      501,
    )
    show()
    expect(await screen.findByRole('alert')).toHaveTextContent(
      'Workspace contents are unavailable.',
    )
    expect(screen.queryByText('This workspace holds nothing yet.')).toBeNull()
  })
})
