// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactElement, ReactNode } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '@/lib/api/errors'
import './i18n'

// --- mocks (hoisted so the vi.mock factories below can reference them) -------

const { api, mockNavigate, authState } = vi.hoisted(() => ({
  api: {
    ssoStatus: vi.fn(),
    scimServiceProviderConfig: vi.fn(),
    scimUsers: vi.fn(),
    identities: vi.fn(),
    groups: vi.fn(),
    findings: vi.fn(),
    audit: vi.fn(),
    wifGraph: vi.fn(),
    externalKeys: vi.fn(),
    workspaceResidency: vi.fn(),
    tlsPosture: vi.fn(),
    cryptoInventory: vi.fn(),
    pivStatus: vi.fn(),
    webauthnRegisterOptions: vi.fn(),
    webauthnRegister: vi.fn(),
    webauthnCredentials: vi.fn(),
    webauthnDelete: vi.fn(),
    webauthnRename: vi.fn(),
    webauthnAuthOptions: vi.fn(),
    webauthnAuthenticate: vi.fn(),
  },
  mockNavigate: vi.fn(),
  authState: {
    activeTenant: 't1' as string | null,
    principal: null as { aal?: number; amr?: string[] } | null,
    can: (_p: string) => true,
  },
}))

vi.mock('@tanstack/react-router', () => ({ useNavigate: () => mockNavigate }))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, identityApi: api }
})
// The WIF graph renders a WebGL SigmaGraph; stub it so the live-graph path is testable
// in jsdom (every other export of the shared barrel is preserved).
vi.mock('@/features/shared', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/features/shared')>()),
  SigmaGraph: ({
    children,
    ariaLabel,
  }: {
    children?: ReactNode
    ariaLabel?: string
  }) => (
    <div data-testid="wif-graph-canvas" aria-label={ariaLabel}>
      {children}
    </div>
  ),
}))

// Import AFTER mocks are declared.
import { FederationTab } from './federation'
import { NhiRosterTab } from './nhi-roster'
import { PostureTab } from './posture'
import { PrivilegedLoginTab } from './privileged-login'
import { WifGraphTab } from './wif/wif-graph'

const empty = { items: [], has_more: false }
const ROSTER_FILTER_CURSOR_CONTRACT =
  'NHI_ROSTER_FILTER_CURSOR_CONTRACT: principal filter must not assert an empty roster while matching identities remain on unloaded pages'
const ROSTER_MATCH_STOP_CONTRACT =
  'NHI_ROSTER_MATCH_STOP_CONTRACT: principal filter must stop auto-follow after a loaded page contains a match'
const scimConfig = {
  schemas: ['urn:ietf:params:scim:schemas:core:2.0:ServiceProviderConfig'],
  patch: { supported: true },
  bulk: { supported: false, maxOperations: 0, maxPayloadSize: 0 },
  filter: { supported: true, maxResults: 200 },
  changePassword: { supported: false },
  sort: { supported: false },
  etag: { supported: false },
  authenticationSchemes: [
    {
      type: 'oauthbearertoken',
      name: 'OAuth Bearer Token',
      description: '…',
      primary: true,
    },
  ],
}

function wrap(ui: ReactElement) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

beforeEach(() => {
  authState.principal = null
  api.ssoStatus.mockRejectedValue(
    new ApiError(501, 'sso_not_configured', 'SSO not configured'),
  )
  api.scimServiceProviderConfig.mockResolvedValue(scimConfig)
  api.scimUsers.mockResolvedValue({
    schemas: [],
    totalResults: 0,
    startIndex: 1,
    itemsPerPage: 0,
    Resources: [],
  })
  api.identities.mockResolvedValue(empty)
  api.groups.mockResolvedValue(empty)
  api.findings.mockResolvedValue(empty)
  api.audit.mockResolvedValue(empty)
  // WIF graph is LIVE: the route always answers 200. Default to an empty
  // federation (the honest empty state); the populated case overrides per-test.
  api.wifGraph.mockResolvedValue({
    issuers: [],
    rules: [],
    service_accounts: [],
  })
  // LIVE since: the routes are mounted and answer 200 with `available`/`reason`.
  // They used to be mocked as 404 — a mock that answers for a route the engine never
  // registered is precisely the fiction that hid from every test in this file.
  api.externalKeys.mockResolvedValue({
    items: [],
    has_more: false,
    available: false,
    reason: 'the Claude Admin-API connector is not wired',
  })
  api.workspaceResidency.mockResolvedValue({
    items: [],
    has_more: false,
    available: false,
    reason: 'the Claude Admin-API connector is not wired',
  })
  api.tlsPosture.mockRejectedValue(new ApiError(404, 'not_found', 'pending'))
  api.cryptoInventory.mockRejectedValue(
    new ApiError(404, 'not_found', 'pending'),
  )
  api.pivStatus.mockRejectedValue(new ApiError(404, 'not_found', 'pending'))
  api.webauthnCredentials.mockResolvedValue({ items: [] })
})

afterEach(() => {
  vi.clearAllMocks()
})

describe('ADM-IDN-01 — SSO config', () => {
  it('rejects a non-exact redirect URI and accepts the exact one', async () => {
    const user = userEvent.setup()
    wrap(<FederationTab />)
    const expected = `${window.location.origin}/v1/auth/federation/callback`
    const input = await screen.findByPlaceholderText(expected)

    await user.type(input, 'https://evil.example.com/callback')
    expect(screen.getByText(/does not match exactly/i)).toBeInTheDocument()

    await user.clear(input)
    await user.type(input, expected)
    expect(screen.getByText(/exact match/i)).toBeInTheDocument()
  })

  // El estado se dice en el idioma del operador, NO con el nombre del símbolo Go.
  // El backend nunca manda `ErrSSONotConfigured`: mapea ese error a 501 con el
  // código de cable `sso_not_configured` y el mensaje «SSO is not configured»
  // (core/api/errors_honestseam_test.go:48). El símbolo lo añadían los siete
  // catálogos, y este caso lo CERTIFICABA. Lo accionable ya vive en el hint
  // contiguo: devuelve 501, define las variables y reinicia.
  it("says the state in the operator's language, not with a Go symbol name", async () => {
    wrap(<FederationTab />)
    expect(await screen.findByText('Not configured')).toBeInTheDocument()
    expect(screen.queryByText(/ErrSSONotConfigured/)).not.toBeInTheDocument()
    expect(
      await screen.findByText(/federation requests return 501/i),
    ).toBeInTheDocument()
  })

  it('shows a retryable error when the SSO posture read genuinely fails', async () => {
    api.ssoStatus.mockReset()
    api.ssoStatus.mockRejectedValue(
      new ApiError(500, 'internal', 'engine unavailable'),
    )
    wrap(<FederationTab />)

    const heading = await screen.findByText('Single sign-on (OIDC / SAML)')
    const card = heading.closest('.rounded-lg') as HTMLElement
    expect(
      await within(card).findByText('Something went wrong'),
    ).toBeInTheDocument()
    // Contra la cadena REAL: la que llevaba el símbolo ya no existe, así que
    // buscarla aquí era un control que no podía fallar.
    expect(within(card).queryByText('Not configured')).not.toBeInTheDocument()

    await userEvent.click(within(card).getByRole('button', { name: 'Retry' }))
    await waitFor(() => expect(api.ssoStatus).toHaveBeenCalledTimes(2))
  })

  it('never renders the SCIM bearer token in clear — only its reference', async () => {
    wrap(<FederationTab />)
    // The bearer is shown via SecretRef (name only) — no token value anywhere.
    expect(await screen.findByText(/tenant SCIM bearer/i)).toBeInTheDocument()
    expect(screen.queryByText(/olvk_/)).not.toBeInTheDocument()
    expect(screen.queryByText(/sk-ant/)).not.toBeInTheDocument()
  })

  it('shows the SCIM leaver as sessions+tokens revoked', async () => {
    api.audit.mockResolvedValue({
      items: [
        {
          id: 'a1',
          action: 'scim.user.deprovision',
          target_id: 'usr_42',
          at: '2026-06-07T00:00:00Z',
        },
      ],
      has_more: false,
    })
    wrap(<FederationTab />)
    expect(await screen.findByText('usr_42')).toBeInTheDocument()
    expect(screen.getByText(/sessions \+ tokens revoked/i)).toBeInTheDocument()
  })

  it('renders the SCIM users as an honest pending seam (not a red error) when not provisioned', async () => {
    api.scimServiceProviderConfig.mockResolvedValue(scimConfig) // config is fine…
    api.scimUsers.mockRejectedValue(
      new ApiError(501, 'not_implemented', 'pending'),
    ) // …users not yet
    wrap(<FederationTab />)
    await waitFor(() =>
      expect(
        screen.getAllByText(/backend endpoint is not live yet/i).length,
      ).toBeGreaterThan(0),
    )
    // The forbidden read-first contract: a not-provisioned endpoint is NEVER a red error.
    expect(screen.queryByText(/server error/i)).not.toBeInTheDocument()
  })
})

describe('ADM-IDN-02 — NHI roster', () => {
  it('shows a Claude api_key NHI converged by external_id and deep-links to the access map', async () => {
    const user = userEvent.setup()
    api.identities.mockResolvedValue({
      items: [
        {
          id: 'i1',
          ref: 'apikey_abc123',
          name: 'ci-key',
          kind: 'api_key',
          source: 'anthropic',
          principal_type: 'nhi',
          disabled: false,
        },
      ],
      has_more: false,
    })
    wrap(<NhiRosterTab />)
    const cell = await screen.findByText('apikey_abc123')
    expect(cell).toBeInTheDocument()

    await user.click(cell)
    const sheet = await screen.findByRole('dialog')
    expect(
      within(sheet).getByText(/converges by its external ID/i),
    ).toBeInTheDocument()
    const link = within(sheet).getByRole('button', {
      name: /view in access map/i,
    })
    await user.click(link)
    expect(mockNavigate).toHaveBeenCalledWith(
      expect.objectContaining({
        to: '/access-map',
        search: { focus: 'apikey_abc123' },
      }),
    )
  })

  it('walks cursor pages instead of presenting the first roster page as complete', async () => {
    api.identities.mockReset()
    api.identities.mockImplementation(async (params?: { cursor?: string }) =>
      params?.cursor === 'roster-page-2'
        ? {
            items: [
              {
                id: 'i2',
                ref: 'svac_second',
                kind: 'service_account',
                principal_type: 'nhi',
                disabled: false,
              },
            ],
            has_more: false,
          }
        : {
            items: [
              {
                id: 'i1',
                ref: 'apikey_first',
                kind: 'api_key',
                principal_type: 'nhi',
                disabled: false,
              },
            ],
            cursor: 'roster-page-2',
            has_more: true,
          },
    )
    wrap(<NhiRosterTab />)

    expect(await screen.findAllByText('apikey_first')).toHaveLength(2)
    await userEvent.click(screen.getByRole('button', { name: 'Load more' }))
    expect(await screen.findAllByText('svac_second')).toHaveLength(2)
    expect(api.identities).toHaveBeenLastCalledWith(
      expect.objectContaining({ cursor: 'roster-page-2', limit: 50 }),
    )
  })

  it('auto-follows cursor pages until a client-only principal filter finds a match', async () => {
    const user = userEvent.setup()
    api.identities.mockReset()
    api.identities.mockImplementation(async (params?: { cursor?: string }) => {
      if (params?.cursor === 'human-page') {
        return {
          items: [
            {
              id: 'human-2',
              ref: 'human_second',
              kind: 'member',
              principal_type: 'human',
              disabled: false,
            },
          ],
          has_more: false,
        }
      }
      if (params?.cursor === 'nhi-page-2') {
        return {
          items: [
            {
              id: 'nhi-2',
              ref: 'nhi_second',
              kind: 'service_account',
              principal_type: 'nhi',
              disabled: false,
            },
          ],
          cursor: 'human-page',
          has_more: true,
        }
      }
      return {
        items: [
          {
            id: 'nhi-1',
            ref: 'nhi_first',
            kind: 'api_key',
            principal_type: 'nhi',
            disabled: false,
          },
        ],
        cursor: 'nhi-page-2',
        has_more: true,
      }
    })
    wrap(<NhiRosterTab />)

    await screen.findAllByText('nhi_first')
    await user.click(
      screen.getByRole('combobox', { name: 'Filter by principal' }),
    )
    await user.click(screen.getByRole('option', { name: 'Human' }))

    await waitFor(() =>
      expect(
        api.identities,
        ROSTER_FILTER_CURSOR_CONTRACT,
      ).toHaveBeenLastCalledWith(
        expect.objectContaining({ cursor: 'human-page', limit: 50 }),
      ),
    )
    expect(api.identities, ROSTER_FILTER_CURSOR_CONTRACT).toHaveBeenCalledTimes(
      3,
    )
    expect(
      (await screen.findAllByText('human_second')).length,
      ROSTER_FILTER_CURSOR_CONTRACT,
    ).toBeGreaterThan(0)
  })

  it('does not auto-follow after the loaded pages already contain a match', async () => {
    const user = userEvent.setup()
    api.identities.mockReset()
    api.identities.mockImplementation(async (params?: { cursor?: string }) =>
      params?.cursor === 'unused-page'
        ? { items: [], has_more: false }
        : {
            items: [
              {
                id: 'nhi-1',
                ref: 'nhi_first',
                kind: 'api_key',
                principal_type: 'nhi',
                disabled: false,
              },
            ],
            cursor: 'unused-page',
            has_more: true,
          },
    )
    wrap(<NhiRosterTab />)

    await screen.findAllByText('nhi_first')
    await user.click(
      screen.getByRole('combobox', { name: 'Filter by principal' }),
    )
    await user.click(screen.getByRole('option', { name: 'Non-human' }))
    await waitFor(() =>
      expect(
        screen.getByRole('combobox', { name: 'Filter by principal' }),
        ROSTER_MATCH_STOP_CONTRACT,
      ).toHaveTextContent('Non-human'),
    )
    await act(async () => {
      await Promise.resolve()
    })

    expect(api.identities, ROSTER_MATCH_STOP_CONTRACT).toHaveBeenCalledTimes(1)
  })

  it('shows the honest empty response after filtered cursor pages are exhausted', async () => {
    const user = userEvent.setup()
    api.identities.mockReset()
    api.identities.mockImplementation(async (params?: { cursor?: string }) =>
      params?.cursor === 'terminal-page'
        ? {
            items: [
              {
                id: 'nhi-2',
                ref: 'nhi_second',
                kind: 'service_account',
                principal_type: 'nhi',
                disabled: false,
              },
            ],
            has_more: false,
          }
        : {
            items: [
              {
                id: 'nhi-1',
                ref: 'nhi_first',
                kind: 'api_key',
                principal_type: 'nhi',
                disabled: false,
              },
            ],
            cursor: 'terminal-page',
            has_more: true,
          },
    )
    wrap(<NhiRosterTab />)

    await screen.findAllByText('nhi_first')
    await user.click(
      screen.getByRole('combobox', { name: 'Filter by principal' }),
    )
    await user.click(screen.getByRole('option', { name: 'Human' }))

    await waitFor(() =>
      expect(
        api.identities,
        ROSTER_FILTER_CURSOR_CONTRACT,
      ).toHaveBeenCalledTimes(2),
    )
    expect(
      screen.getByText('No human or non-human identities yet'),
      ROSTER_FILTER_CURSOR_CONTRACT,
    ).toBeInTheDocument()
  })

  it('stops auto-follow on a failed cursor page and retries only on request', async () => {
    const user = userEvent.setup()
    let retryRequested = false
    let cursorCalls = 0
    api.identities.mockReset()
    api.identities.mockImplementation(async (params?: { cursor?: string }) => {
      if (params?.cursor === 'human-page') {
        cursorCalls += 1
        if (!retryRequested) {
          throw new ApiError(500, 'internal', 'roster cursor unavailable')
        }
        return {
          items: [
            {
              id: 'human-2',
              ref: 'human_after_retry',
              kind: 'member',
              principal_type: 'human',
              disabled: false,
            },
          ],
          has_more: false,
        }
      }
      return {
        items: [
          {
            id: 'nhi-1',
            ref: 'nhi_first',
            kind: 'api_key',
            principal_type: 'nhi',
            disabled: false,
          },
        ],
        cursor: 'human-page',
        has_more: true,
      }
    })
    wrap(<NhiRosterTab />)

    await screen.findAllByText('nhi_first')
    await user.click(
      screen.getByRole('combobox', { name: 'Filter by principal' }),
    )
    await user.click(screen.getByRole('option', { name: 'Human' }))

    expect(await screen.findByText('Something went wrong')).toBeInTheDocument()
    await new Promise((resolve) => setTimeout(resolve, 20))
    expect(cursorCalls, ROSTER_FILTER_CURSOR_CONTRACT).toBe(1)

    retryRequested = true
    await user.click(screen.getByRole('button', { name: 'Retry' }))
    await waitFor(() =>
      expect(cursorCalls, ROSTER_FILTER_CURSOR_CONTRACT).toBe(2),
    )
    expect(
      (await screen.findAllByText('human_after_retry')).length,
      ROSTER_FILTER_CURSOR_CONTRACT,
    ).toBeGreaterThan(0)
  })
})

describe('AAL gate (fail-closed)', () => {
  it('DENIES the WIF graph without AAL3 and shows the step-up panel', async () => {
    authState.principal = { aal: 1 } // password-only
    wrap(<WifGraphTab />)
    expect(
      await screen.findByText(/step-up authentication required/i),
    ).toBeInTheDocument()
    // The gated content (Console-only note) must NOT be present.
    expect(
      screen.queryByText(/managed only in the Anthropic Console/i),
    ).not.toBeInTheDocument()
  })

  it('ALLOWS the WIF graph with AAL3', async () => {
    authState.principal = { aal: 3, amr: ['webauthn'] }
    wrap(<WifGraphTab />)
    expect(
      await screen.findByText(/managed only in the Anthropic Console/i),
    ).toBeInTheDocument()
    expect(
      screen.queryByText(/step-up authentication required/i),
    ).not.toBeInTheDocument()
  })

  it('routes an unenrolled operator to passkey registration (no_webauthn_credential is guidance, not a red error)', async () => {
    authState.principal = { aal: 1 }
    // jsdom has no WebAuthn; provide the minimal surface isWebAuthnSupported checks.
    vi.stubGlobal('PublicKeyCredential', function PublicKeyCredential() {})
    Object.defineProperty(navigator, 'credentials', {
      value: { get: vi.fn(), create: vi.fn() },
      configurable: true,
    })
    api.webauthnAuthOptions.mockRejectedValue(
      new ApiError(400, 'no_webauthn_credential', 'no registered credential'),
    )
    wrap(<WifGraphTab />)
    await userEvent.click(
      await screen.findByRole('button', { name: /authenticate/i }),
    )
    expect(
      await screen.findByText(/no passkey is registered/i),
    ).toBeInTheDocument()
    expect(
      screen.queryByText(/step-up did not complete/i),
    ).not.toBeInTheDocument()
    vi.unstubAllGlobals()
  })

  it('renders the explicit PIV-not-configured state (501 piv_not_configured is real, not a pending seam)', async () => {
    authState.principal = { aal: 3, amr: ['webauthn'] }
    api.pivStatus.mockRejectedValue(
      new ApiError(501, 'piv_not_configured', 'piv not configured'),
    )
    wrap(<PrivilegedLoginTab />)
    expect(
      await screen.findByText(/not configured on this deployment/i),
    ).toBeInTheDocument()
    expect(screen.queryByText(/backend pending/i)).not.toBeInTheDocument()
  })

  it('keeps passkey actions closed when the credential inventory read fails', async () => {
    authState.principal = { aal: 3, amr: ['webauthn'] }
    api.webauthnCredentials.mockReset()
    api.webauthnCredentials.mockRejectedValue(
      new ApiError(500, 'internal', 'inventory unavailable'),
    )
    wrap(<PrivilegedLoginTab />)

    const heading = await screen.findByText('Registered passkeys')
    const card = heading.closest('.rounded-lg') as HTMLElement
    expect(
      await within(card).findByText('Something went wrong'),
    ).toBeInTheDocument()
    expect(
      within(card).queryByText('No passkeys registered'),
    ).not.toBeInTheDocument()
    expect(
      within(card).queryByRole('button', { name: 'Register passkey' }),
    ).not.toBeInTheDocument()

    await userEvent.click(within(card).getByRole('button', { name: 'Retry' }))
    await waitFor(() =>
      expect(api.webauthnCredentials).toHaveBeenCalledTimes(2),
    )
  })
})

describe('ANT2-08/07 — WIF graph: visualise & lint, never create', () => {
  it('surfaces the REAL key-shadow footgun with the `ant auth status` remedy', async () => {
    authState.principal = { aal: 3 }
    api.findings.mockImplementation((params?: { subject_kind?: string }) =>
      params?.subject_kind === 'anthropic.federation'
        ? Promise.resolve({
            items: [
              {
                id: 'f1',
                kind: 'governance',
                severity: 'high',
                status: 'open',
                subject_kind: 'anthropic.federation',
                subject_ref: 'ANTHROPIC_API_KEY',
                title:
                  'Static Anthropic key shadows Workload Identity Federation',
              },
            ],
            has_more: false,
          })
        : Promise.resolve(empty),
    )
    wrap(<WifGraphTab />)
    expect(await screen.findByText(/ant auth status/i)).toBeInTheDocument()
  })

  it('offers NO affordance to create fdis_/fdrl_/svac_ (Console-only)', async () => {
    authState.principal = { aal: 3 }
    wrap(<WifGraphTab />)
    await screen.findByText(/managed only in the Anthropic Console/i)
    expect(
      screen.queryByRole('button', { name: /create|new|add|crear|nuevo/i }),
    ).not.toBeInTheDocument()
    // The only CTA is a link out to the Console (never a CRUD control).
    expect(
      screen.getByRole('link', { name: /open in anthropic console/i }),
    ).toBeInTheDocument()
  })

  it('shows an honest empty state when no federation is declared (live route, empty)', async () => {
    authState.principal = { aal: 3 }
    // The default mock resolves an empty graph (200, no federation declared).
    wrap(<WifGraphTab />)
    expect(
      await screen.findByText(/No federation declared/i),
    ).toBeInTheDocument()
    // It is NOT the old pending seam — the backend is live now.
    expect(
      screen.queryByText(/backend endpoint is not live yet/i),
    ).not.toBeInTheDocument()
  })

  it('renders the live WIF graph when federation is declared (flipped from seam)', async () => {
    authState.principal = { aal: 3 }
    api.wifGraph.mockResolvedValue({
      issuers: [
        {
          id: 'fdis_1',
          issuer_url: 'https://idp.example.com',
          jwks_mode: 'discovery',
          ca_cert_configured: false,
        },
      ],
      rules: [
        {
          rule_id: 'fdrl_1',
          issuer_id: 'fdis_1',
          service_account_id: 'svac_1',
          service_account_name: 'ci',
          oauth_scope: 'workspace:developer',
          token_lifetime_seconds: 3600,
          ca_cert_configured: false,
        },
      ],
      service_accounts: [
        {
          id: 'svac_1',
          name: 'ci',
          oauth_scope: 'workspace:developer',
          issuer_id: 'fdis_1',
          rule_id: 'fdrl_1',
        },
      ],
    })
    wrap(<WifGraphTab />)
    // The live graph renders (stubbed canvas) — never the empty state or a pending seam.
    expect(await screen.findByTestId('wif-graph-canvas')).toBeInTheDocument()
    expect(
      screen.queryByText(/No federation declared/i),
    ).not.toBeInTheDocument()
    expect(
      screen.queryByText(/backend endpoint is not live yet/i),
    ).not.toBeInTheDocument()
  })
})

//the posture tab's three answers. "No customer-managed keys" and "we could not
// look" are different facts; before this they rendered alike, because the route 404'd
// and every test in this file mocked the 404 rather than the engine.
describe('External Keys / residency posture — the three answers', () => {
  it('says WHY when the Admin connector is not wired, instead of an empty inventory', async () => {
    wrap(<PostureTab />)
    expect(
      await screen.findByText(/the Claude Admin-API connector is not wired/i),
    ).toBeInTheDocument()
    // The empty-state copy would tell the operator their org HAS no keys. Match the
    // REAL string — an earlier draft asserted /no external keys/i, which never matches
    // "No external key references reported yet" and so asserted nothing at all.
    expect(
      screen.queryByText(/no external key references reported yet/i),
    ).not.toBeInTheDocument()
  })

  it('renders the inventory when the posture IS available', async () => {
    api.externalKeys.mockResolvedValue({
      items: [
        {
          id: 'ekey_01ABC',
          provider: 'aws_kms',
          name: 'prod-cmek',
          state: 'active',
          in_use: true,
        },
      ],
      has_more: false,
      available: true,
    })
    api.workspaceResidency.mockResolvedValue({
      items: [],
      has_more: false,
      available: true,
    })
    wrap(<PostureTab />)
    expect(await screen.findByText('ekey_01ABC')).toBeInTheDocument()
    // With BOTH postures available, no "could not read" notice survives anywhere.
    expect(
      screen.queryByText(/the Claude Admin-API connector is not wired/i),
    ).not.toBeInTheDocument()
  })

  it('an AVAILABLE but empty inventory is a real zero, not a failure', async () => {
    api.externalKeys.mockResolvedValue({
      items: [],
      has_more: false,
      available: true,
    })
    wrap(<PostureTab />)
    // The honest empty state — the inventory WAS read and the org genuinely has none.
    expect(
      await screen.findByText(/no external key references reported yet/i),
    ).toBeInTheDocument()
  })
})
