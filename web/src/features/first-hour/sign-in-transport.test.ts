// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient } from '@tanstack/react-query'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  __resetRefreshState,
  configureApiClient,
  type RequestOptions,
} from '@/lib/api/client'
import { queryKeys } from '@/lib/api/query'
import { useSessionStore } from '@/stores/session'
import { createStepUpOwner, type StepUpOwner } from '@/stores/step-up'
import { useTenantStore } from '@/stores/tenant'
import { signInApi } from './api'

const requests = {
  status: (options: RequestOptions) =>
    signInApi.status('claude', 'tenant-a', undefined, 'ppf_a', options),
  start: (options: RequestOptions) =>
    signInApi.start('claude', 'tenant-a', 'ppf_a', options),
  get: (options: RequestOptions) => signInApi.get('flow-a', undefined, options),
  code: (options: RequestOptions) =>
    signInApi.code('flow-a', 'fixture-code', options),
  cancel: (options: RequestOptions) => signInApi.cancel('flow-a', options),
}
const verbs = ['status', 'start', 'get', 'code', 'cancel'] as const
let owners: StepUpOwner[] = []
let refreshes = 0
let unauthorized = 0

function owner() {
  const qc = new QueryClient()
  qc.setQueryData(queryKeys.whoami, {
    kind: 'user',
    user_id: 'actor-a',
    actor: 'user:actor-a',
    superadmin: true,
  })
  const captured = createStepUpOwner(qc)
  owners.push(captured)
  return captured
}

beforeEach(() => {
  useTenantStore.setState({ activeTenant: 'tenant-a' })
  useSessionStore.setState({ credentialGeneration: 0 })
  refreshes = 0
  unauthorized = 0
  configureApiClient({
    getToken: () => null,
    getCSRFToken: () => 'csrf-fixture',
    getTenant: () => useTenantStore.getState().activeTenant,
    getExpiresAt: () => null,
    refreshSession: async () => {
      refreshes++
      return false
    },
    onUnauthorized: () => {
      unauthorized++
    },
  })
  __resetRefreshState()
})

afterEach(() => {
  owners.forEach((captured) => captured.retire())
  owners = []
  vi.unstubAllGlobals()
  configureApiClient({
    getToken: () => null,
    getCSRFToken: undefined,
    getTenant: () => null,
    getExpiresAt: undefined,
    refreshSession: undefined,
    onUnauthorized: () => {},
  })
  __resetRefreshState()
})

describe('account sign-in through the real HTTP client', () => {
  it('keeps session-expiry handling for default status requests', async () => {
    const transport = vi.fn(
      async () =>
        new Response('{"error":{"code":"unauthenticated"}}', { status: 401 }),
    )
    vi.stubGlobal('fetch', transport)
    await expect(signInApi.status('claude', 'tenant-a')).rejects.toMatchObject({
      status: 401,
    })
    expect(refreshes).toBe(1)
    expect(unauthorized).toBe(1)
  })

  it.each([200, 401])(
    'rejects an account status body settling after retirement (HTTP %s)',
    async (status) => {
      let finish!: (body: string) => void
      const body = new ReadableStream<Uint8Array>({
        start(controller) {
          finish = (text) => {
            controller.enqueue(new TextEncoder().encode(text))
            controller.close()
          }
        },
      })
      const response = new Response(body, { status })
      vi.stubGlobal(
        'fetch',
        vi.fn(async () => response),
      )
      const options = owner().begin()
      const pending = requests.status(options)
      await vi.waitFor(() => expect(response.body?.locked).toBe(true))
      useSessionStore.setState((s) => ({
        credentialGeneration: s.credentialGeneration + 1,
      }))
      finish(
        status === 200
          ? '{"driver":"claude","installed":true,"signed_in":true,"account":"retired account"}'
          : '{"error":{"code":"unauthenticated","message":"expired"}}',
      )
      await expect(pending).rejects.toMatchObject({ name: 'AbortError' })
      expect(refreshes).toBe(0)
      expect(unauthorized).toBe(0)
    },
  )

  it('cancels without a body and forwards the captured signal to fetch', async () => {
    const transport = vi.fn(
      async () => new Response('{"ok":true}', { status: 200 }),
    )
    vi.stubGlobal('fetch', transport)
    const options = owner().begin()
    await signInApi.cancel('flow-a', options)
    expect(transport).toHaveBeenCalledOnce()
    const [url, init] = vi.mocked(fetch).mock.calls[0]
    expect(url).toBe('/v1/m/agenttools/sign-in/flow-a')
    expect(init?.method).toBe('DELETE')
    expect(init?.body).toBeUndefined()
    expect(init?.signal).toBe(options.signal)
    expect(new Headers(init?.headers).has('Content-Type')).toBe(false)
  })

  it.each(verbs)(
    'checks the captured %s guard before actual dispatch',
    async (verb) => {
      const transport = vi.fn(
        async () => new Response('{"ok":true}', { status: 200 }),
      )
      vi.stubGlobal('fetch', transport)
      const options = owner().begin(() => false)
      expect(options.signal.aborted).toBe(false)
      await expect(requests[verb](options)).rejects.toMatchObject({
        name: 'AbortError',
      })
      expect(transport).not.toHaveBeenCalled()
    },
  )

  it.each(verbs)(
    'rejects a late %s response after the credential changes without affecting the new session',
    async (verb) => {
      let finish!: (response: Response) => void
      const transport = vi.fn(
        () =>
          new Promise<Response>((resolve) => {
            finish = resolve
          }),
      )
      vi.stubGlobal('fetch', transport)
      const options = owner().begin()
      const pending = requests[verb](options)
      expect(transport).toHaveBeenCalledOnce()
      useSessionStore.setState((s) => ({
        credentialGeneration: s.credentialGeneration + 1,
      }))
      expect(options.signal.aborted).toBe(true)
      finish(
        new Response(
          '{"error":{"code":"unauthenticated","message":"expired"}}',
          { status: 401 },
        ),
      )
      await expect(pending).rejects.toMatchObject({ name: 'AbortError' })
      expect(transport).toHaveBeenCalledOnce()
      expect(refreshes).toBe(0)
      expect(unauthorized).toBe(0)
    },
  )
})
