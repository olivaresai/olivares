// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// HU 049 (09b, fresh install): the first page asked the engine's rule which tool a
// session would run on for tools that were not installed; each answer was a 409 the
// browser logs as a console error. The rule is asked only when it could say yes.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { renderIntel, screen } from '@/test/intel'
import { agentOpsApi } from '@/features/agentops/api'
import { providersApi } from '@/features/providers/api'
import { useTenantStore } from '@/stores/tenant'
import { signInApi, type SignInTool } from './api'
import { useReadyTools } from './first-hour'

const auth = vi.hoisted(() => ({
  isSuperadmin: true,
  can: (_p: string): boolean => true,
}))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    ...auth,
    principal: { user_id: 'u1', aal: 1 },
    activeTenant: 'tnt-a',
  }),
}))

function Probe() {
  const { ready, isLoading } = useReadyTools()
  return <p>{isLoading ? 'deciding' : `ready=[${ready.join(',')}]`}</p>
}

function given(installed: SignInTool[], records: number) {
  vi.spyOn(signInApi, 'status').mockImplementation(async (driver) => ({
    driver,
    installed: installed.includes(driver),
    signed_in: installed.includes(driver),
  }))
  vi.spyOn(providersApi, 'list').mockResolvedValue({
    items: Array.from({ length: records }, (_, i) => ({
      provider_ref: `prv-${i}`,
    })),
  } as Awaited<ReturnType<typeof providersApi.list>>)
  return vi
    .spyOn(agentOpsApi, 'previewProfile')
    .mockResolvedValue({ reason: 'own_login' } as Awaited<
      ReturnType<typeof agentOpsApi.previewProfile>
    >)
}

const asked = (spy: ReturnType<typeof given>) =>
  spy.mock.calls.map(([driver]) => driver).sort()

beforeEach(() => useTenantStore.setState({ activeTenant: 'tnt-a' }))
afterEach(() => {
  vi.restoreAllMocks()
  auth.isSuperadmin = true
  auth.can = () => true
})

describe('which tools the first page asks the engine about', () => {
  it('asks about no tool on a fresh install', async () => {
    const resolve = given([], 0)
    renderIntel(<Probe />)
    expect(await screen.findByText('ready=[]')).toBeInTheDocument()
    expect(resolve).not.toHaveBeenCalled()
  })

  it('asks about an installed tool, and about OpenCode once a provider record exists', async () => {
    const resolve = given(['claude'], 1)
    renderIntel(<Probe />)
    expect(
      await screen.findByText('ready=[claude,opencode]'),
    ).toBeInTheDocument()
    expect(asked(resolve)).toEqual(['claude', 'opencode'])
  })

  it('asks about every tool, as before, when it cannot read whether they are installed', async () => {
    auth.isSuperadmin = false
    auth.can = () => false
    const resolve = given([], 0)
    renderIntel(<Probe />)
    expect(
      await screen.findByText('ready=[claude,codex,grok,opencode]'),
    ).toBeInTheDocument()
    expect(asked(resolve)).toEqual(['claude', 'codex', 'grok', 'opencode'])
    expect(signInApi.status).not.toHaveBeenCalled()
    expect(providersApi.list).not.toHaveBeenCalled()
  })
})
