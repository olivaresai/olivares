// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Design FH 016: New session > More options lets a tenant administrator give the
// session vault secrets (env/…) as environment variables, by name. Nobody else
// sees the choice, and no value is ever read back.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { SecretEnvChoice } from './api'

const auth = vi.hoisted(() => ({ admin: true }))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    can: (p: string) => p === 'tenant:admin' && auth.admin,
    activeTenant: 'tnt-a',
    principal: { user_id: 'user-a' },
    isSuperadmin: false,
  }),
}))
const consoleApi = vi.hoisted(() => ({ listSecrets: vi.fn() }))
vi.mock('@/features/console/api', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/features/console/api')>()),
  consoleApi,
}))

import { SessionSecrets, envNameFor } from './session-secrets'
import './i18n'

// Every value the picker hands back, in order; the last one is what a launch would send.
const changes: SecretEnvChoice[][] = []
const chosen = () => changes[changes.length - 1] ?? []
function Harness() {
  const [value, setValue] = useState<SecretEnvChoice[]>([])
  return (
    <SessionSecrets
      value={value}
      onChange={(next) => {
        changes.push(next)
        setValue(next)
      }}
    />
  )
}
function wrap() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <Harness />
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  auth.admin = true
  changes.length = 0
  consoleApi.listSecrets.mockReset()
  consoleApi.listSecrets.mockResolvedValue({
    sealer_available: true,
    secrets: [
      { name: 'env/github', hint: 'ab12' },
      { name: 'mcp/jira', hint: 'cd34' },
    ],
  })
})

describe('New session: vault secrets as environment variables', () => {
  it('offers the session secrets only, and gives one by name with a variable', async () => {
    wrap()
    const use = await screen.findByRole('checkbox', { name: /env\/github/ })
    expect(screen.queryByText('mcp/jira')).toBeNull()
    await userEvent.click(use)
    expect(chosen()).toEqual([{ env: 'GITHUB', secret: 'env/github' }])
    const variable = screen.getByRole('textbox', { name: /env\/github/ })
    await userEvent.clear(variable)
    await userEvent.type(variable, 'GITHUB_TOKEN')
    expect(chosen()).toEqual([{ env: 'GITHUB_TOKEN', secret: 'env/github' }])
    await userEvent.click(use)
    expect(chosen()).toEqual([])
  })

  it('is not offered to someone who may not manage the tenant secrets', () => {
    auth.admin = false
    wrap()
    expect(screen.queryByRole('checkbox')).toBeNull()
    expect(consoleApi.listSecrets).not.toHaveBeenCalled()
  })

  it('derives a variable name a shell accepts from the secret name', () => {
    expect(envNameFor('env/github-token')).toBe('GITHUB_TOKEN')
    expect(envNameFor('env/aws/secret.key')).toBe('AWS_SECRET_KEY')
    expect(envNameFor('env/1password')).toBe('_1PASSWORD')
  })
})
