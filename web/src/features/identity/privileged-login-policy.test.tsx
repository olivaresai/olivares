// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { ReactElement } from 'react'
import { useSessionStore } from '@/stores/session'
const { api, authState } = vi.hoisted(() => ({
  api: { pivStatus: vi.fn(), webauthnCredentials: vi.fn() },
  authState: {
    activeTenant: 't1',
    principal: null as Record<string, unknown> | null,
    can: () => true,
  },
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))
vi.mock('@/features/identity/api', async (orig) => {
  const real = (await orig()) as Record<string, unknown>
  return { ...real, identityApi: { ...(real.identityApi as object), ...api } }
})
import { PrivilegedLoginTab } from '@/features/identity/privileged-login'
import { en } from '@/features/identity/i18n'
function wrap(
  node: ReactElement = <PrivilegedLoginTab />,
  qc = new QueryClient({ defaultOptions: { queries: { retry: false } } }),
) {
  return render(<QueryClientProvider client={qc}>{node}</QueryClientProvider>)
}
beforeEach(() => {
  vi.clearAllMocks()
  useSessionStore.setState({ credentialGeneration: 0 })
  api.webauthnCredentials.mockResolvedValue({ items: [] })
})
// Root 19:15Z: the section said a hardware step-up and AAL3 were required whatever this
// deployment asked for; the administrative step-up is off by default (FH 33f41d5e).
describe('what this deployment asks for before administrative actions', () => {
  const signedIn = (policy: string, satisfied: boolean) => ({
    aal: 1,
    amr: ['pwd'],
    kind: 'user',
    user_id: 'u1',
    actor: 'u1',
    admin_step_up: policy,
    step_up_satisfied: satisfied,
    authentication_configuration: { piv_configured: false },
  })

  it('says no extra check is asked, and offers no step-up, when it is off', async () => {
    authState.principal = signedIn('none', true)
    wrap()
    expect(await screen.findByText(en.login.policy.none)).toBeInTheDocument()
    expect(screen.queryByText(en.assurance.stepUpTitle)).toBeNull()
    expect(screen.queryByText(/AAL3 \(NIST|hardware step-up/)).toBeNull()
  })

  it('offers the step-up when it asks for a passkey this session has not given', async () => {
    authState.principal = signedIn('passkey', false)
    wrap()
    expect(await screen.findByText(en.login.policy.passkey)).toBeInTheDocument()
    expect(screen.getByText(en.assurance.stepUpTitle)).toBeInTheDocument()
  })

  it('names the authenticator code when that is what it asks for', async () => {
    authState.principal = signedIn('totp', true)
    wrap()
    expect(await screen.findByText(en.login.policy.totp)).toBeInTheDocument()
    expect(screen.queryByText(en.assurance.stepUpTitle)).toBeNull()
  })
})
