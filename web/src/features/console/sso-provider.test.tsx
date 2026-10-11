// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The provider's own name on the status card and the sign-in button, and an editable MFA
// mapping whose round trip keeps "protocol default" (null), "trust none" ([]) and "only
// these" apart.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactElement, ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import './i18n'
import { ApiError, NetworkError } from '@/lib/api/errors'
import { consoleKeys, type SSOConfigDTO } from './api'

const { api } = vi.hoisted(() => ({
  api: {
    getSSO: vi.fn(),
    putSSO: vi.fn(),
    deleteSSO: vi.fn(),
    testSSO: vi.fn(),
  },
}))

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    activeTenant: 't1',
    activeRole: 'owner',
    isSuperadmin: true,
    principal: { aal: 3 },
    can: () => true,
  }),
}))
vi.mock('@/features/identity/assurance', () => ({
  AAL: { PASSWORD: 1, MFA: 2, HARDWARE: 3 },
  RequireAssurance: ({ children }: { children: ReactNode }) => <>{children}</>,
}))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, consoleApi: { ...actual.consoleApi, ...api } }
})

import { SSOTab } from './sso-tab'

function wrap(ui: ReactElement) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
  return qc
}

const base: SSOConfigDTO = {
  configured: true,
  provider_available: true,
  protocol: 'oidc',
  status: 'active',
  redirect_uri: 'https://panel.example/v1/auth/federation/callback',
  oidc_issuer: 'https://idp.example/realms/corp',
  oidc_client_id: 'olivares',
  require_sso: false,
  network_allowlist: [],
  enforced_by: 'unavailable',
  scim_authoritative: false,
  claimed_domains: [],
}
const primary: SSOConfigDTO = {
  ...base,
  alias: 'default',
  display_name: 'Corporate Okta',
  assurance_mapping: { amr: ['mfa', 'hwk'], acr: null, saml_contexts: null },
}

beforeEach(() => {
  vi.clearAllMocks()
  api.getSSO.mockResolvedValue(primary)
  api.putSSO.mockResolvedValue(primary)
})

async function openEditor() {
  const user = userEvent.setup()
  wrap(<SSOTab />)
  await user.click(
    await screen.findByRole('button', { name: /edit configuration/i }),
  )
  return user
}
const sent = () => api.putSSO.mock.calls.at(-1)![0] as Record<string, unknown>

describe('SSO: the provider by name on its status card', () => {
  it.each([
    ['server failure', new ApiError(503, 'unavailable', 'SSO read failed')],
    ['network failure', new NetworkError('SSO read failed')],
  ])(
    'shows a %s and retries the read before offering configuration',
    async (_, error) => {
      const user = userEvent.setup()
      api.getSSO.mockRejectedValueOnce(error)
      wrap(<SSOTab />)

      expect(await screen.findByRole('alert')).toBeInTheDocument()
      expect(screen.queryByText('Not configured')).not.toBeInTheDocument()
      expect(
        screen.queryByRole('button', {
          name: /configure sso|edit configuration|remove configuration/i,
        }),
      ).not.toBeInTheDocument()

      await user.click(screen.getByRole('button', { name: /retry/i }))
      expect(await screen.findByText('Corporate Okta')).toBeInTheDocument()
      expect(api.getSSO).toHaveBeenCalledTimes(2)
      expect(screen.queryByRole('alert')).not.toBeInTheDocument()
      expect(
        screen.getByRole('button', { name: /edit configuration/i }),
      ).toBeInTheDocument()
    },
  )

  it('a failed refresh after a successful read replaces the cached configuration with the error', async () => {
    const user = userEvent.setup()
    const qc = wrap(<SSOTab />)
    expect(await screen.findByText('Corporate Okta')).toBeInTheDocument()

    api.getSSO.mockRejectedValueOnce(
      new ApiError(503, 'unavailable', 'SSO read failed'),
    )
    await act(() => qc.refetchQueries({ queryKey: consoleKeys.sso() }))

    expect(await screen.findByRole('alert')).toBeInTheDocument()
    expect(screen.queryByText('Corporate Okta')).not.toBeInTheDocument()
    expect(
      screen.queryByRole('button', {
        name: /configure sso|edit configuration|remove configuration/i,
      }),
    ).not.toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: /retry/i }))
    expect(await screen.findByText('Corporate Okta')).toBeInTheDocument()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })

  it('offers no configuration action while the first read is pending', () => {
    api.getSSO.mockReturnValueOnce(new Promise<SSOConfigDTO>(() => {}))
    wrap(<SSOTab />)
    expect(
      screen.queryByRole('button', {
        name: /configure sso|edit configuration/i,
      }),
    ).not.toBeInTheDocument()
    expect(screen.queryByText('Not configured')).not.toBeInTheDocument()
  })

  it('offers a working Configure action after reading an unconfigured provider', async () => {
    const user = userEvent.setup()
    api.getSSO.mockResolvedValue({ ...base, configured: false })
    wrap(<SSOTab />)
    expect(await screen.findByText('Not configured')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: /configure sso/i }))
    expect(await screen.findByLabelText(/issuer url/i)).toBeInTheDocument()
  })

  it('the provider panel states its sign-in name and which values count as MFA', async () => {
    api.getSSO.mockResolvedValue({ ...primary, display_name: '' })
    wrap(<SSOTab />)
    expect(
      await screen.findByText('AMR values (OIDC): only mfa, hwk'),
    ).toBeInTheDocument()
    expect(
      screen.getByText('Name on the sign-in button').nextElementSibling,
    ).toHaveTextContent('Single sign-on')
    expect(
      screen.getByText('ACR values (OIDC): protocol default'),
    ).toBeInTheDocument()
  })
})

describe('SSO editor: display name and MFA mapping round trip', () => {
  it('an untouched edit keeps the stored name and mapping (sends neither)', async () => {
    const user = await openEditor()
    await user.click(
      screen.getByRole('button', { name: /save configuration/i }),
    )
    await waitFor(() => expect(api.putSSO).toHaveBeenCalled())
    expect(sent()).not.toHaveProperty('display_name')
    expect(sent()).not.toHaveProperty('assurance_mapping')
  })

  it('a new name and an exact empty ACR list are sent; the AMR list stays as stored', async () => {
    const user = await openEditor()
    const name = screen.getByLabelText('Name on the sign-in button')
    await user.clear(name)
    await user.type(name, 'Okta')
    await user.click(
      screen.getByRole('combobox', { name: 'ACR values (OIDC)' }),
    )
    await user.click(
      await screen.findByRole('option', { name: 'Only these values' }),
    )
    await user.click(
      screen.getByRole('button', { name: /save configuration/i }),
    )
    await waitFor(() => expect(api.putSSO).toHaveBeenCalled())
    expect(sent()).toMatchObject({
      display_name: 'Okta',
      assurance_mapping: { amr: ['mfa', 'hwk'], acr: [], saml_contexts: null },
    })
  })

  it('restoring protocol defaults sends three null lists', async () => {
    const user = await openEditor()
    await user.click(
      screen.getByRole('button', { name: 'Restore protocol defaults' }),
    )
    await user.click(
      screen.getByRole('button', { name: /save configuration/i }),
    )
    await waitFor(() => expect(api.putSSO).toHaveBeenCalled())
    expect(sent()).toMatchObject({
      assurance_mapping: { amr: null, acr: null, saml_contexts: null },
    })
  })

  it('a wildcard value is refused before Save', async () => {
    const user = await openEditor()
    // Only the AMR list is exact here, so it is the one values box.
    const values = screen.getByLabelText('Values')
    await user.type(values, '{Enter}otp*')
    expect(
      screen.getByText(
        'Up to 32 values, each at most 512 bytes, with no wildcard (*) and no control characters.',
      ),
    ).toBeInTheDocument()
    expect(
      screen.getByRole('button', { name: /save configuration/i }),
    ).toBeDisabled()
  })
})
