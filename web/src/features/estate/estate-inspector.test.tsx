// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'
import { configureApiClient } from '@/lib/api/client'
import { EstateInspector } from './estate-inspector'
import type { EstateNode } from './types'
import './i18n'

afterEach(() => vi.unstubAllGlobals())

it('shows only dependencies whose endpoints are in the readable projection, without hidden counts or references', async () => {
  configureApiClient({
    getToken: () => null,
    getTenant: () => 'tenant-a',
    onUnauthorized: () => {},
    refreshSession: undefined,
    getExpiresAt: undefined,
  })
  vi.stubGlobal(
    'fetch',
    vi.fn(
      async () =>
        new Response(
          JSON.stringify({
            item: { id: 'work-b' },
            acceptance: [],
            dependencies: [
              { id: 'visible-dep', depends_on_id: 'work-a' },
              { id: 'concealed-row', depends_on_id: 'concealed-work' },
              {
                id: 'removed-dep',
                depends_on_id: 'removed-work',
                active: false,
              },
            ],
          }),
          { headers: { 'Content-Type': 'application/json', ETag: '"v1"' } },
        ),
    ),
  )
  const nodes: EstateNode[] = [
    {
      kind: 'work',
      ref: 'work-a',
      label: 'Prepare',
      status: 'draft',
      workspaceId: 'workspace-a',
    },
    {
      kind: 'work',
      ref: 'work-b',
      label: 'Build',
      status: 'draft',
      workspaceId: 'workspace-a',
    },
    {
      kind: 'work',
      ref: 'removed-work',
      label: 'Removed dependency',
      status: 'draft',
      workspaceId: 'workspace-a',
    },
  ]
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <EstateInspector
        tenant="tenant-a"
        lifetime={1}
        node={nodes[1]!}
        nodes={nodes}
        canWriteWork={false}
        canOpenOwner={false}
        isCurrent={() => true}
        onClose={() => {}}
        onRefresh={() => {}}
        onRestoreFocus={() => {}}
      />
    </QueryClientProvider>,
  )
  expect(
    await screen.findByText('Build depends on Prepare'),
  ).toBeInTheDocument()
  expect(screen.getByRole('dialog')).not.toHaveTextContent('concealed')
  expect(screen.getByRole('dialog')).not.toHaveTextContent('Removed dependency')
  expect(
    screen.queryByRole('button', { name: 'Remove dependency' }),
  ).not.toBeInTheDocument()
  expect(
    screen.queryByRole('button', { name: 'Add dependency' }),
  ).not.toBeInTheDocument()
  expect(
    screen.queryByRole('link', { name: 'Open resource' }),
  ).not.toBeInTheDocument()
})
