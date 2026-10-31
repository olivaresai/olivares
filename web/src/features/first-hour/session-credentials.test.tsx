// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// New session > More options lets a tenant administrator give the session
// managed credentials (credential/…), by name. The engine's broker hands the
// session a helper; the sealed value is never read back, by this picker or
// anything else.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const auth = vi.hoisted(() => ({
  admin: true,
  community: false as boolean | undefined,
}))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    can: (p: string) => p === 'tenant:admin' && auth.admin,
    activeTenant: 'tnt-a',
    principal: { user_id: 'user-a' },
    isSuperadmin: false,
  }),
}))
vi.mock('@/lib/hooks/use-server-info', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/lib/hooks/use-server-info')>()),
  useCommunityBuild: () => auth.community,
}))
const consoleApi = vi.hoisted(() => ({ listSecrets: vi.fn() }))
vi.mock('@/features/console/api', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/features/console/api')>()),
  consoleApi,
}))

import { SessionCredentials, credentialNameFor } from './session-credentials'
import './i18n'

// Every value the picker hands back, in order; the last one is what a launch would send.
const changes: string[][] = []
const chosen = () => changes[changes.length - 1] ?? []
function Harness({ initial = [] }: { initial?: string[] }) {
  const [value, setValue] = useState<string[]>(initial)
  return (
    <SessionCredentials
      value={value}
      onChange={(next) => {
        changes.push(next)
        setValue(next)
      }}
    />
  )
}
function wrap(initial?: string[]) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <Harness initial={initial} />
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  auth.admin = true
  auth.community = false
  changes.length = 0
  consoleApi.listSecrets.mockReset()
  consoleApi.listSecrets.mockResolvedValue({
    sealer_available: true,
    secrets: [
      { name: 'credential/prod-db', hint: 'ab12' },
      { name: 'env/github', hint: 'cd34' },
      { name: 'credential/build-host', hint: 'ef56' },
    ],
  })
})

describe('New session: managed credentials by name', () => {
  it('offers the managed credentials only, and gives one by its name', async () => {
    wrap()
    const first = await screen.findByRole('checkbox', { name: /prod-db/ })
    expect(screen.queryByText('env/github')).toBeNull()
    await userEvent.click(first)
    expect(chosen()).toEqual(['prod-db'])
    const second = screen.getByRole('checkbox', { name: /build-host/ })
    await userEvent.click(second)
    expect(chosen()).toEqual(['prod-db', 'build-host'])
    await userEvent.click(first)
    expect(chosen()).toEqual(['build-host'])
  })

  it('offers no more than the engine allows a launch to name', async () => {
    consoleApi.listSecrets.mockResolvedValue({
      sealer_available: true,
      secrets: Array.from({ length: 9 }, (_, i) => ({
        name: `credential/c${i + 1}`,
        hint: 'ab12',
      })),
    })
    wrap()
    const boxes = []
    for (let i = 1; i <= 9; i++)
      boxes.push(
        await screen.findByRole('checkbox', { name: new RegExp(`c${i}$`) }),
      )
    for (let i = 0; i < 8; i++) await userEvent.click(boxes[i])
    expect(chosen()).toHaveLength(8)
    expect(boxes[8]).toBeDisabled()
  })

  it('says the list failed instead of showing an empty choice', async () => {
    consoleApi.listSecrets.mockRejectedValue(new Error('refused'))
    wrap()
    expect(
      await screen.findByText(/did not answer the secrets list/i),
    ).toBeInTheDocument()
    expect(screen.queryByText(/No managed credentials yet/i)).toBeNull()
    expect(screen.queryByRole('checkbox')).toBeNull()
  })

  it('keeps a chosen name the list no longer shows, visible and removable', async () => {
    // A name chosen from an earlier list (deleted since, or another tenant's):
    // it stays chosen and shown, never silently dropped from the launch.
    wrap(['prod-db', 'retired-host'])
    const gone = await screen.findByRole('checkbox', {
      name: /retired-host/,
    })
    expect(gone).toBeChecked()
    await userEvent.click(gone)
    expect(chosen()).toEqual(['prod-db'])
  })

  it('is not offered to someone who may not manage the tenant secrets', () => {
    auth.admin = false
    wrap()
    expect(screen.queryByRole('checkbox')).toBeNull()
    expect(consoleApi.listSecrets).not.toHaveBeenCalled()
  })

  it('is not offered by a Community engine, which would refuse the launch', () => {
    auth.community = true
    wrap()
    expect(screen.queryByRole('checkbox')).toBeNull()
    expect(screen.queryByRole('button')).toBeNull()
    expect(consoleApi.listSecrets).not.toHaveBeenCalled()
  })

  it('is offered while the engine edition is not yet known', async () => {
    auth.community = undefined
    wrap()
    expect(
      await screen.findByRole('checkbox', { name: /prod-db/ }),
    ).toBeInTheDocument()
  })

  it('reads the launch name out of the sealed one', () => {
    expect(credentialNameFor('credential/prod-db')).toBe('prod-db')
    expect(credentialNameFor('credential/build.host-1')).toBe('build.host-1')
  })
})
