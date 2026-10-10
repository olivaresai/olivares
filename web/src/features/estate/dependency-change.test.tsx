// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { StrictMode, type ComponentProps } from 'react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { configureApiClient } from '@/lib/api/client'
import { queryKeys } from '@/lib/api/query'
import { useTenantStore } from '@/stores/tenant'
import { DependencyChange } from './dependency-change'
import type { EstateNode } from './types'
import './i18n'

const work: EstateNode = {
  kind: 'work',
  ref: 'work-b',
  label: 'Build',
  status: 'draft',
}
const dependency: EstateNode = {
  kind: 'work',
  ref: 'work-a',
  label: 'Prepare',
  status: 'draft',
}
let client: QueryClient
let present: boolean
let loseReply: boolean
let failRead: boolean
let stale: boolean
let requests: {
  mode: string | null
  method: string
  headers: Headers
  body: unknown
}[]

function json(body: unknown, status = 200, extra: Record<string, string> = {}) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json', ...extra },
  })
}

beforeEach(() => {
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  client.setQueryData(queryKeys.whoami, {
    kind: 'user',
    actor: 'fixture-actor',
    superadmin: true,
    grants: [],
  })
  present = false
  loseReply = false
  failRead = false
  stale = false
  requests = []
  configureApiClient({
    getToken: () => null,
    getTenant: () => 'wrong-tenant',
    onUnauthorized: () => {},
    refreshSession: undefined,
    getExpiresAt: undefined,
  })
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string, init: RequestInit) => {
      const mode = new URL(url, 'https://fixture.invalid').searchParams.get(
        'mode',
      )
      const method = init.method ?? 'GET'
      requests.push({
        mode,
        method,
        headers: new Headers(init.headers),
        body: init.body ? JSON.parse(String(init.body)) : null,
      })
      if (method === 'GET') {
        if (failRead) throw new TypeError('fixture read unavailable')
        return json(
          {
            item: { id: work.ref },
            dependencies: [
              { id: 'dep-row', depends_on_id: dependency.ref, active: present },
            ],
            acceptance: [],
          },
          200,
          { ETag: '"native-v7"' },
        )
      }
      if (mode === 'plan')
        return json({
          verdict: 'LIMPIO',
          code: 'ok',
          plan_hash: 'native-plan',
          checks: [],
          expected_etag: '"native-v7"',
        })
      if (stale)
        return json(
          {
            verdict: 'ROTO',
            error: { code: 'version_mismatch', message: 'fixture changed' },
          },
          412,
        )
      present = method !== 'DELETE'
      if (loseReply) throw new TypeError('fixture response lost after commit')
      return json({ verdict: 'LIMPIO', code: 'ok' }, 200, {
        ETag: '"native-v8"',
      })
    }),
  )
})
afterEach(() => {
  cleanup()
  client.clear()
  vi.unstubAllGlobals()
})

function show(
  overrides: Partial<ComponentProps<typeof DependencyChange>> = {},
  strict = false,
) {
  const onConfirmed = vi.fn()
  const element = (
    <QueryClientProvider client={client}>
      <DependencyChange
        tenant="tenant-a"
        work={work}
        dependency={dependency}
        isCurrent={() => true}
        onClose={() => {}}
        onConfirmed={onConfirmed}
        {...overrides}
      />
    </QueryClientProvider>
  )
  const view = render(strict ? <StrictMode>{element}</StrictMode> : element)
  return { ...view, onConfirmed }
}

describe('estate dependency action uses the native Work contract', () => {
  it('requires review, pins ETag/hash/key/tenant and confirms with owner readback', async () => {
    const user = userEvent.setup()
    const { onConfirmed } = show({}, true)
    const apply = await screen.findByRole('button', { name: 'Apply' })
    expect(requests.filter((request) => request.mode === 'apply')).toHaveLength(
      0,
    )
    await user.click(apply)
    await screen.findByText('The native resource confirms this change.')
    const sent = requests.filter((request) => request.mode === 'apply')
    expect(sent).toHaveLength(1)
    expect(sent[0]?.headers.get('If-Match')).toBe('"native-v7"')
    expect(sent[0]?.headers.get('If-Plan-Hash')).toBe('native-plan')
    expect(sent[0]?.headers.get('Idempotency-Key')).toMatch(/^[a-f0-9-]{36}$/)
    expect(sent[0]?.headers.get('X-Olivares-Tenant')).toBe('tenant-a')
    expect(sent[0]?.body).toEqual({ depends_on_id: dependency.ref })
    expect(requests.at(-1)?.method).toBe('GET')
    expect(onConfirmed).toHaveBeenCalledOnce()
  })

  it('resolves a lost apply response with GET and never repeats the write', async () => {
    loseReply = true
    const { onConfirmed } = show()
    await userEvent.click(await screen.findByRole('button', { name: 'Apply' }))
    await screen.findByText('The native resource confirms this change.')
    expect(requests.filter((request) => request.mode === 'apply')).toHaveLength(
      1,
    )
    expect(onConfirmed).toHaveBeenCalledOnce()
  })

  it('keeps unavailable readback unknown and checks again without another apply', async () => {
    const { onConfirmed } = show()
    const apply = await screen.findByRole('button', { name: 'Apply' })
    failRead = true
    loseReply = true
    await userEvent.click(apply)
    await screen.findByRole('button', { name: 'Check result' })
    expect(onConfirmed).not.toHaveBeenCalled()
    failRead = false
    await userEvent.click(screen.getByRole('button', { name: 'Check result' }))
    await screen.findByText('The native resource confirms this change.')
    expect(requests.filter((request) => request.mode === 'apply')).toHaveLength(
      1,
    )
  })

  it('refuses a stale plan without claiming a configured relationship', async () => {
    stale = true
    const { onConfirmed } = show()
    await userEvent.click(await screen.findByRole('button', { name: 'Apply' }))
    expect(await screen.findByRole('alert')).toHaveTextContent(
      'This resource changed',
    )
    expect(onConfirmed).not.toHaveBeenCalled()
    expect(requests.filter((request) => request.mode === 'apply')).toHaveLength(
      1,
    )
  })

  it('retires a prepared action synchronously when its authority changes', async () => {
    let current = true
    show({ isCurrent: () => current })
    await screen.findByRole('button', { name: 'Apply' })
    act(() => {
      current = false
      useTenantStore.setState({ activeTenant: 'tenant-b' })
      current = true
    })
    expect(await screen.findByRole('alert')).toHaveTextContent(
      'This resource changed',
    )
    expect(
      screen.queryByRole('button', { name: 'Apply' }),
    ).not.toBeInTheDocument()
    await waitFor(() =>
      expect(
        requests.filter((request) => request.mode === 'apply'),
      ).toHaveLength(0),
    )
  })

  it.each(['ROTO', 'NO_HE_PODIDO_MIRAR'])(
    'cannot apply a native %s plan',
    async (verdict) => {
      vi.stubGlobal(
        'fetch',
        vi.fn(async (_url: string, init: RequestInit) =>
          (init.method ?? 'GET') === 'GET'
            ? json(
                { item: { id: work.ref }, dependencies: [], acceptance: [] },
                200,
                { ETag: '"native-v7"' },
              )
            : json({
                verdict,
                code: 'dependency_cycle',
                checks: [],
                plan_hash: '',
              }),
        ),
      )
      const { onConfirmed } = show()
      expect(await screen.findByRole('alert')).toHaveTextContent(
        'dependency_cycle',
      )
      expect(
        screen.queryByRole('button', { name: 'Apply' }),
      ).not.toBeInTheDocument()
      expect(onConfirmed).not.toHaveBeenCalled()
    },
  )

  it('removes the native dependency row and confirms its absence', async () => {
    present = true
    show({ removeId: 'dep-row' })
    await userEvent.click(await screen.findByRole('button', { name: 'Apply' }))
    await screen.findByText('The native resource confirms this change.')
    expect(requests.find((request) => request.mode === 'apply')?.method).toBe(
      'DELETE',
    )
    expect(present).toBe(false)
  })

  it('does not revive a prepared apply when the same principal loses and regains write permission', async () => {
    show()
    await screen.findByRole('button', { name: 'Apply' })
    act(() => {
      client.setQueryData(queryKeys.whoami, {
        kind: 'user',
        actor: 'fixture-actor',
        superadmin: false,
        grants: [],
      })
      client.setQueryData(queryKeys.whoami, {
        kind: 'user',
        actor: 'fixture-actor',
        superadmin: true,
        grants: [],
      })
    })
    expect(await screen.findByRole('alert')).toHaveTextContent(
      'This resource changed',
    )
    expect(
      screen.queryByRole('button', { name: 'Apply' }),
    ).not.toBeInTheDocument()
    expect(requests.filter((request) => request.mode === 'apply')).toHaveLength(
      0,
    )
  })

  it('resolves an already configured intention on reopening without planning or writing again', async () => {
    present = true
    const { onConfirmed } = show()
    await screen.findByText('The native resource confirms this change.')
    expect(requests).toHaveLength(1)
    expect(requests[0]?.method).toBe('GET')
    expect(
      screen.queryByRole('button', { name: 'Apply' }),
    ).not.toBeInTheDocument()
    expect(onConfirmed).toHaveBeenCalledOnce()
  })
})
