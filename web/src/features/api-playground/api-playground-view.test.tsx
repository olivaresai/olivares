// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import type { ReactElement } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { TooltipProvider } from '@/components/ui/tooltip'
import './i18n'

const { authState } = vi.hoisted(() => ({
  authState: { can: (_p: string): boolean => true },
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))

import { ApiPlaygroundView } from './api-playground-view'
import { usePlayground } from './use-playground'
import { FEATURE_VIEWS } from '../registry'

const coreSpec = {
  openapi: '3.1.0',
  info: { title: 'core', version: 'v1' },
  tags: [{ name: 'health', description: 'Health' }],
  paths: {
    '/healthz': {
      get: {
        operationId: 'healthz',
        summary: 'Liveness',
        tags: ['health'],
        'x-stability': 'stable',
        'x-required-permission': 'system:admin',
        responses: { '200': { description: 'OK' } },
      },
    },
  },
}
const betaSpec = {
  openapi: '3.1.0',
  info: { title: 'modules (beta)', version: 'v1' },
  paths: {
    '/v1/m/finops/budgets': {
      get: {
        summary: 'finops module route',
        'x-stability': 'beta',
        'x-required-permission': 'finops:budget:read',
        security: [{ bearerAuth: [] }],
        responses: { '200': { description: 'OK' } },
      },
    },
    '/v1/m/platform/system-status': {
      get: {
        summary: 'system-only module route',
        'x-stability': 'beta',
        'x-required-permission': 'system:admin',
        security: [{ bearerAuth: [] }],
        responses: { '200': { description: 'OK' } },
      },
    },
    '/v1/m/unclassified/orphan': {
      get: {
        summary: 'route without permission metadata',
        'x-stability': 'beta',
        security: [{ bearerAuth: [] }],
        responses: { '200': { description: 'OK' } },
      },
    },
  },
}

function wrap(ui: ReactElement) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <TooltipProvider delayDuration={0}>{ui}</TooltipProvider>
    </QueryClientProvider>,
  )
}

function mockFetch(betaOk: boolean) {
  return vi.fn(async (url: string) => {
    if (url === '/openapi.json')
      return { ok: true, json: async () => coreSpec } as Response
    if (url === '/openapi.beta.json') {
      if (!betaOk)
        return { ok: false, status: 404, json: async () => ({}) } as Response
      return { ok: true, json: async () => betaSpec } as Response
    }
    return { ok: false, status: 404, json: async () => ({}) } as Response
  })
}

beforeEach(() => {
  authState.can = () => true
  usePlayground.getState().reset()
})
afterEach(() => {
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

describe('ApiPlaygroundView beta module routes', () => {
  it('uses the tenant-admin permission in the feature registry', () => {
    expect(
      FEATURE_VIEWS.find((view) => view.id === 'apiPlayground')?.permission,
    ).toBe('tenant:admin')
  })

  it('forbids a principal without tenant-admin access', () => {
    authState.can = () => false
    wrap(<ApiPlaygroundView />)
    expect(screen.queryByText(/finops/)).not.toBeInTheDocument()
  })

  it('lists every operation for a system admin', async () => {
    vi.stubGlobal('fetch', mockFetch(true))
    wrap(<ApiPlaygroundView />)

    expect(await screen.findByText('health')).toBeInTheDocument()
    expect(await screen.findByText('finops')).toBeInTheDocument()
    expect(await screen.findByText('platform')).toBeInTheDocument()
    expect(await screen.findByText('unclassified')).toBeInTheDocument()
    expect(screen.getAllByText('beta')).toHaveLength(3)
    expect(await screen.findByText(/4 endpoints/)).toBeInTheDocument()
  })

  it('filters system-only and unclassified operations for a tenant admin', async () => {
    authState.can = (permission) => permission === 'tenant:admin'
    vi.stubGlobal('fetch', mockFetch(true))
    wrap(<ApiPlaygroundView />)

    expect(await screen.findByText('finops')).toBeInTheDocument()
    expect(screen.queryByText('health')).not.toBeInTheDocument()
    expect(screen.queryByText('platform')).not.toBeInTheDocument()
    expect(screen.queryByText('unclassified')).not.toBeInTheDocument()
    expect(await screen.findByText(/1 endpoints/)).toBeInTheDocument()
  })

  it('shows the selected operation required permission as a badge', async () => {
    vi.stubGlobal('fetch', mockFetch(true))
    wrap(<ApiPlaygroundView />)

    fireEvent.click(await screen.findByText('/v1/m/finops/budgets'))
    const permissionBadge = await screen.findByLabelText(
      'Required permission: finops:budget:read',
    )
    expect(permissionBadge).toBeInTheDocument()

    fireEvent.focus(permissionBadge)
    expect(await screen.findByRole('tooltip')).toHaveTextContent(
      'Required permission: finops:budget:read',
    )
  })

  it('still renders the core API when the beta doc 404s', async () => {
    vi.stubGlobal('fetch', mockFetch(false))
    wrap(<ApiPlaygroundView />)

    expect(await screen.findByText('health')).toBeInTheDocument()
    await waitFor(() =>
      expect(screen.queryByText('finops')).not.toBeInTheDocument(),
    )
  })
})
