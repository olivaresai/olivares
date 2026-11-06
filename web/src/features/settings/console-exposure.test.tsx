// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { expect, it, vi } from 'vitest'
import { renderIntel, screen, userEvent } from '@/test/intel'
import { ConsoleExposure, exposureApi } from './console-exposure'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    activeTenant: 't1',
    principal: { actor: 'admin' },
    isSuperadmin: true,
    can: () => true,
  }),
}))

it('reports the server listeners and policy without claiming external reachability', async () => {
  vi.spyOn(exposureApi, 'get').mockResolvedValue({
    listen: '0.0.0.0:8443',
    grpc_listen: '127.0.0.1:9090',
    public_url: 'https://running.example.test',
    saved_public_url: 'https://console.example.test',
    tls: true,
    admin_step_up: 'none',
    external_reachability: 'not verified',
    addresses: ['https://100.64.0.2:8443'],
    managed_public: false,
  })
  renderIntel(<ConsoleExposure />)
  expect(await screen.findByText('0.0.0.0:8443')).toBeInTheDocument()
  expect(screen.getByText('https://running.example.test')).toBeInTheDocument()
  expect(screen.getByLabelText('Public URL')).toHaveValue(
    'https://console.example.test',
  )
  expect(screen.getByText('127.0.0.1:9090')).toBeInTheDocument()
  expect(screen.getByText('https://100.64.0.2:8443')).toBeInTheDocument()
  expect(
    screen.getByText('External reachability is not verified.'),
  ).toBeInTheDocument()
  expect(
    screen.getByText(/Enable an authenticator code or passkey/),
  ).toBeInTheDocument()
})

it('says why Save is unavailable until the public URL is edited', async () => {
  vi.spyOn(exposureApi, 'get').mockResolvedValue({
    listen: '0.0.0.0:8443',
    grpc_listen: '',
    public_url: '',
    saved_public_url: '',
    tls: false,
    admin_step_up: 'totp',
    external_reachability: 'not verified',
    addresses: [],
    managed_public: false,
  })
  const user = userEvent.setup()
  renderIntel(<ConsoleExposure />)
  const save = await screen.findByRole('button', { name: 'Save' })
  expect(save).toBeDisabled()
  expect(save).toHaveAccessibleDescription('Edit the public URL to save it.')
  await user.type(
    screen.getByLabelText('Public URL'),
    'https://console.example.test',
  )
  expect(screen.getByRole('button', { name: 'Save' })).toBeEnabled()
})
