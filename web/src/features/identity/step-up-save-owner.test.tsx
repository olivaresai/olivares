// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { AuthProvider } from '@/lib/auth/context'
import { queryKeys } from '@/lib/api/query'
import { configureApiClient } from '@/lib/api/client'
import { useSessionStore } from '@/stores/session'
import { useTenantStore } from '@/stores/tenant'
import { useStepUpStore } from '@/stores/step-up'
import { ConnectorsTab } from '@/features/console/connectors-tab'
import { StepUpHost } from '@/components/layout/step-up-host'
import '@/features/console/i18n'

// This counterexample also runs unchanged against R29. Every layer from the actual
// Save to the host, panel, whoami reader and fetch is real; only network/device I/O
// is a fixture. The positive and retirement cases cross the same final-POST await.
const actor = {
  kind: 'user',
  user_id: 'u1',
  actor: 'u1',
  display_name: 'Operator',
  superadmin: true,
  grants: [],
  aal: 3,
}
const bytes = new Uint8Array([1]).buffer
let qc: QueryClient
let finish: (value: Response) => void
let finishReached: boolean
let putCount: number
let elevated: boolean
const json = (value: unknown, status = 200) =>
  new Response(JSON.stringify(value), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })
beforeEach(() => {
  qc = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  qc.setQueryData(queryKeys.whoami, actor)
  finishReached = false
  putCount = 0
  elevated = false
  const finalResponse = new Promise<Response>((resolve) => {
    finish = resolve
  })
  useStepUpStore.getState().clear()
  useTenantStore.setState({ activeTenant: 't1' })
  useSessionStore
    .getState()
    .setSession({ token: 'fixture', sessionId: 'fixture', expiresAt: '' })
  configureApiClient({
    getToken: () => useSessionStore.getState().token,
    getTenant: () => useTenantStore.getState().activeTenant,
    onUnauthorized: vi.fn(),
    getExpiresAt: () => null,
    refreshSession: async () => false,
  })
  vi.stubGlobal('PublicKeyCredential', function PublicKeyCredential() {})
  Object.defineProperty(navigator, 'credentials', {
    configurable: true,
    value: {
      get: async () => ({
        id: 'key1',
        type: 'public-key',
        rawId: bytes,
        response: {
          clientDataJSON: bytes,
          authenticatorData: bytes,
          signature: bytes,
        },
      }),
    },
  })
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string, init?: RequestInit) => {
      if (url === '/v1/auth/piv/status') return json({ presented: false })
      if (url === '/v1/auth/webauthn/authenticate/options')
        return json({ publicKey: { challenge: 'AQ' } })
      if (url === '/v1/auth/webauthn/authenticate') {
        finishReached = true
        return finalResponse
      }
      if (url === '/v1/auth/whoami') return json(actor)
      if (url === '/v1/console/connectors' && init?.method === 'PUT') {
        putCount++
        return elevated
          ? json({ applied: true })
          : json(
              {
                error: {
                  code: 'step_up_required',
                  message: 'fixture assurance refusal',
                },
              },
              403,
            )
      }
      if (url === '/v1/console/connectors')
        return json({
          connectors: [{ kind: 'fixture', fields_known: true, fields: [] }],
        })
      if (url === '/v1/console/sources') return json({ sources: [] })
      throw new Error(`Unhandled fixture request: ${url}`)
    }),
  )
})
afterEach(() => {
  cleanup()
  qc.clear()
  vi.unstubAllGlobals()
})
function app(showOwner: boolean) {
  return (
    <QueryClientProvider client={qc}>
      <AuthProvider>
        {showOwner && <ConnectorsTab />}
        <StepUpHost />
      </AuthProvider>
    </QueryClientProvider>
  )
}
describe('actual Save owner lifetime across host elevation', () => {
  for (const remains of [true, false]) {
    it(
      remains
        ? 'control: live enrolled Save resumes exactly once'
        : 'retired Save owner never writes after already-sent elevation completes',
      async () => {
        const view = render(app(true))
        const user = userEvent.setup()
        await user.click(
          await screen.findByRole('button', { name: /add connector/i }),
        )
        await user.type(
          screen.getByRole('textbox', { name: /^name/i }),
          'owned-save',
        )
        await user.type(screen.getByRole('textbox', { name: /^tenant/i }), 't1')
        await user.click(screen.getByRole('button', { name: /^save/i }))
        await user.click(
          await screen.findByRole('button', {
            name: /authenticate with (passkey|security key)/i,
          }),
        )
        await waitFor(() => expect(finishReached).toBe(true))
        expect(putCount).toBe(1)
        if (!remains) view.rerender(app(false))
        await act(async () => {
          elevated = true
          finish(json({ ok: true, aal: 3 }))
          await new Promise((r) => setTimeout(r, 30))
        })
        if (remains) await waitFor(() => expect(putCount).toBe(2))
        else expect(putCount).toBe(1)
      },
    )
  }
})
