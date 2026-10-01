// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, expect, it, vi } from 'vitest'
import './i18n'
const { api, auth } = vi.hoisted(() => ({
  api: { listProfiles: vi.fn(), getProfile: vi.fn(), patchProfile: vi.fn() },
  auth: { can: () => true, activeTenant: 'tenant', principal: null },
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => auth }))
vi.mock('@/features/agentops/api', async (original) => ({
  ...(await original<typeof import('@/features/agentops/api')>()),
  agentOpsApi: api,
}))
import { ProviderBindDialog } from './provider-bind-dialog'
const profile = {
  profile_ref: 'pp_codex',
  driver: 'codex',
  display_name: 'Local agent',
  state: 'active',
  local_environment: true,
  operable: true,
  environment_ref: 'env1',
}
beforeEach(() => {
  vi.clearAllMocks()
  auth.can = () => true
  api.listProfiles.mockResolvedValue({
    items: [
      profile,
      {
        ...profile,
        profile_ref: 'pp_claude',
        driver: 'claude',
        display_name: 'Other agent',
      },
    ],
    has_more: false,
  })
  api.getProfile.mockResolvedValue(profile)
  api.patchProfile.mockResolvedValue(profile)
})
function show() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <ProviderBindDialog
        record={{
          provider_ref: 'prv_local',
          kind: 'ollama',
          display_name: 'Ollama local',
          state: 'active',
        }}
        open
        onOpenChange={() => {}}
      />
    </QueryClientProvider>,
  )
}
it('binds only a compatible profile through the existing managed provider contract', async () => {
  show()
  const user = userEvent.setup()
  await user.click(await screen.findByRole('combobox'))
  await user.click(await screen.findByRole('option', { name: /Local agent/ }))
  expect(
    screen.queryByRole('option', { name: /Other agent/ }),
  ).not.toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: 'Bind profile' }))
  await waitFor(() =>
    expect(api.patchProfile).toHaveBeenCalledWith(
      'pp_codex',
      {
        provider_record_ref: 'prv_local',
        auth_source: 'managed_injection',
      },
      expect.objectContaining({ dispatchGuard: expect.any(Function) }),
    ),
  )
})
it('rechecks profile state before changing a binding', async () => {
  show()
  const user = userEvent.setup()
  await user.click(await screen.findByRole('combobox'))
  await user.click(await screen.findByRole('option', { name: /Local agent/ }))
  api.getProfile.mockResolvedValue({ ...profile, state: 'retired' })
  await user.click(screen.getByRole('button', { name: 'Bind profile' }))
  await waitFor(() => expect(api.getProfile).toHaveBeenCalled())
  expect(api.patchProfile).not.toHaveBeenCalled()
})
