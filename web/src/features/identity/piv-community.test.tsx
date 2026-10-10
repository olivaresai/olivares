// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const authState = vi.hoisted(() => ({
  principal: {
    aal: 1,
    amr: ['pwd'],
  } as {
    aal?: number
    amr?: string[]
    kind?: string
    user_id?: string
    actor?: string
    display_name?: string
    superadmin?: boolean
    grants?: unknown[]
    authentication_configuration?: { piv_configured: boolean }
  } | null,
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))

const api = vi.hoisted(() => ({
  pivStatus: vi.fn(),
  pivElevate: vi.fn(),
}))
vi.mock('./api', async (orig) => {
  const real = (await orig()) as Record<string, unknown>
  return { ...real, identityApi: { ...(real.identityApi as object), ...api } }
})

import { AAL, StepUpPanel } from './assurance'
import { queryKeys } from '@/lib/api/query'
import type { Whoami } from '@/lib/api/types'
import { useSessionStore } from '@/stores/session'
const actor: Whoami = {
  kind: 'user',
  user_id: 'u1',
  actor: 'u1',
  display_name: 'Operator',
  superadmin: true,
  grants: [],
  aal: 1,
}
function client() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  qc.setQueryData(queryKeys.whoami, actor)
  return qc
}

const wrap = (onElevated?: () => void, qc = client()) =>
  render(
    <QueryClientProvider client={qc}>
      <StepUpPanel
        minAal={AAL.HARDWARE}
        currentAal={AAL.PASSWORD}
        action="console"
        onElevated={onElevated}
      />
    </QueryClientProvider>,
  )

const boton = () => screen.queryByRole('button', { name: /PIV\/CAC/i })

describe('Community smart-card edition boundary', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    authState.principal = {
      ...actor,
      authentication_configuration: { piv_configured: true },
    }
    useSessionStore.setState({ credentialGeneration: 0 })
    api.pivStatus.mockResolvedValue({ presented: true, subject: 'CN=Ada' })
  })
  it('keeps passkey step-up without querying or offering a smart card', async () => {
    wrap()
    await screen.findByRole('button', { name: /passkey/i })
    expect(boton()).toBeNull()
    expect(api.pivStatus).not.toHaveBeenCalled()
  })
})
