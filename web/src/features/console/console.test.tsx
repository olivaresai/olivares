// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactElement, ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { NetworkError } from '@/lib/api/errors'
import './i18n'

const { api, authState } = vi.hoisted(() => ({
  api: {
    onboard: vi.fn(),
    listInvites: vi.fn(),
    revokeInvite: vi.fn(),
    listMembers: vi.fn(),
    setMemberActive: vi.fn(),
    listSuperadmins: vi.fn(),
    setSuperadminActive: vi.fn(),
    listWorkspaces: vi.fn(),
    createWorkspace: vi.fn(),
    updateWorkspace: vi.fn(),
    listAgentGroups: vi.fn(),
    createAgentGroup: vi.fn(),
    deleteAgentGroup: vi.fn(),
    getSSO: vi.fn(),
    putSSO: vi.fn(),
    deleteSSO: vi.fn(),
    testSSO: vi.fn(),
    rbacCatalog: vi.fn(),
    delegationAuthority: vi.fn(),
    listRoles: vi.fn(),
    createRole: vi.fn(),
    updateRole: vi.fn(),
    deleteRole: vi.fn(),
    listPermGroups: vi.fn(),
    createPermGroup: vi.fn(),
    updatePermGroup: vi.fn(),
    deletePermGroup: vi.fn(),
    listGrants: vi.fn(),
    createGrant: vi.fn(),
    revokeGrant: vi.fn(),
    listInheritanceFilters: vi.fn(),
    createInheritanceFilter: vi.fn(),
    removeInheritanceFilter: vi.fn(),
    listGroups: vi.fn(),
    setGroupParent: vi.fn(),
    listModelGroups: vi.fn(),
    createModelGroup: vi.fn(),
    updateModelGroup: vi.fn(),
    deleteModelGroup: vi.fn(),
    listModelAccess: vi.fn(),
    createModelAccess: vi.fn(),
    updateModelAccess: vi.fn(),
    deleteModelAccess: vi.fn(),
    searchSubjects: vi.fn(),
    searchResources: vi.fn(),
    accessReviewExport: vi.fn(),
  },
  authState: {
    activeTenant: 't1' as string | null,
    activeRole: 'owner' as string | null,
    isSuperadmin: true,
    principal: { aal: 3 } as { aal?: number } | null,
    can: (_p: string): boolean => true,
  },
}))

vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))
vi.mock('@/features/identity/assurance', () => ({
  AAL: { PASSWORD: 1, MFA: 2, HARDWARE: 3 },
  // Passthrough gate (AAL3 enforcement is covered by the backend -race tests).
  RequireAssurance: ({ children }: { children: ReactNode }) => <>{children}</>,
}))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, consoleApi: api }
})

import { PeopleTab } from './people-tab'
import { RolesTab } from './roles-tab'
import { SSOTab } from './sso-tab'
import { ScopesTab } from './scopes-tab'

function wrap(ui: ReactElement) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

const emptyList = { items: [], has_more: false }

beforeEach(() => {
  vi.clearAllMocks()
  authState.isSuperadmin = true
  authState.activeRole = 'owner'
  authState.can = (_p: string) => true
  api.listInvites.mockResolvedValue(emptyList)
  api.listMembers.mockResolvedValue(emptyList)
  api.listSuperadmins.mockResolvedValue(emptyList)
  api.listWorkspaces.mockResolvedValue(emptyList)
  api.listAgentGroups.mockResolvedValue(emptyList)
  api.getSSO.mockResolvedValue({
    configured: false,
    provider_available: true,
    redirect_uri: 'https://panel.example/v1/auth/federation/callback',
    require_sso: false,
    network_allowlist: [],
    enforced_by: 'unavailable',
    claimed_domains: [],
  })
  api.rbacCatalog.mockResolvedValue({
    kinds: ['agent', 'model'],
    tree_kinds: ['agent', 'model'],
    permissions: [],
    verbs: ['read', 'write', 'admin'],
    builtin_roles: ['viewer', 'editor', 'admin', 'owner'],
    scope_trees: ['tenant', 'workspace', 'agent_group'],
  })
  api.delegationAuthority.mockResolvedValue({ superadmin: true, domains: [] })
  api.listRoles.mockResolvedValue(emptyList)
  api.listPermGroups.mockResolvedValue(emptyList)
  api.listGrants.mockResolvedValue(emptyList)
  api.listInheritanceFilters.mockResolvedValue(emptyList)
  api.listGroups.mockResolvedValue({ groups: [] })
  api.listModelGroups.mockResolvedValue(emptyList)
  api.listModelAccess.mockResolvedValue(emptyList)
})

describe('PeopleTab', () => {
  it('replaces a failed member load with an actionable error and retries', async () => {
    api.listMembers.mockRejectedValueOnce(new NetworkError('Failed to fetch'))
    const user = userEvent.setup()
    wrap(<PeopleTab />)

    expect(
      await screen.findByText('Control plane unreachable'),
    ).toBeInTheDocument()
    expect(
      screen.getByText(/check your connection and that the engine is running/i),
    ).toBeInTheDocument()
    expect(screen.queryByText('No active members.')).not.toBeInTheDocument()
    expect(
      screen.queryByRole('status', { name: /loading/i }),
    ).not.toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: /retry/i }))
    expect(await screen.findByText('No active members.')).toBeInTheDocument()
    expect(api.listMembers).toHaveBeenCalledTimes(2)
    expect(
      screen.queryByText('Control plane unreachable'),
    ).not.toBeInTheDocument()
  })

  it('opens the onboarding dialog in invite mode when the palette asks for it', async () => {
    const handled = vi.fn()
    const user = userEvent.setup()
    wrap(<PeopleTab inviteRequested onInviteHandled={handled} />)
    // The dialog is the button's own dialog, already set to send an invitation.
    expect(
      await screen.findByRole('combobox', { name: /how they sign in/i }),
    ).toHaveTextContent('Send an email invitation')
    expect(screen.queryByLabelText(/initial password/i)).toBeNull()
    await user.keyboard('{Escape}')
    expect(handled).toHaveBeenCalled()
  })

  it('onboards a user with an admin-set password', async () => {
    api.onboard.mockResolvedValue({
      user: {
        id: 'u1',
        email: 'new@acme.io',
        status: 'active',
        is_superadmin: false,
        created_at: '',
      },
      created: true,
      membership: { id: 'm1', user_id: 'u1', tenant: 't1', role: 'editor' },
    })
    const user = userEvent.setup()
    wrap(<PeopleTab />)

    await user.click(
      await screen.findByRole('button', { name: /onboard user/i }),
    )
    await user.type(screen.getByLabelText(/email/i), 'new@acme.io')
    await user.type(screen.getByLabelText(/initial password/i), 'newuserpass1')
    await user.click(screen.getByRole('button', { name: /^onboard$/i }))

    await waitFor(() =>
      expect(api.onboard).toHaveBeenCalledWith(
        expect.objectContaining({
          email: 'new@acme.io',
          mode: 'password',
          role: 'editor',
        }),
      ),
    )
  })

  it('shows pending invitations', async () => {
    api.listInvites.mockResolvedValue({
      items: [
        {
          id: 'i1',
          email: 'pending@acme.io',
          tenant: 't1',
          role: 'viewer',
          expires_at: '2026-07-01T00:00:00Z',
          created_at: '',
        },
      ],
      has_more: false,
    })
    wrap(<PeopleTab />)
    expect(await screen.findByText('pending@acme.io')).toBeInTheDocument()
  })

  it('disables an active superadmin', async () => {
    api.listSuperadmins.mockResolvedValue({
      items: [
        {
          id: 'a1',
          email: 'root@acme.io',
          status: 'active',
          is_superadmin: true,
          created_at: '',
        },
        {
          id: 'b1',
          email: 'two@acme.io',
          status: 'active',
          is_superadmin: true,
          created_at: '',
        },
      ],
      has_more: false,
    })
    api.setSuperadminActive.mockResolvedValue({
      id: 'b1',
      email: 'two@acme.io',
      status: 'inactive',
      is_superadmin: true,
      created_at: '',
    })
    const user = userEvent.setup()
    wrap(<PeopleTab />)

    const row = (await screen.findByText('two@acme.io')).closest('tr')!
    await user.click(within(row).getByRole('button', { name: /disable/i }))
    const dialog = await screen.findByRole('dialog')
    await user.click(within(dialog).getByRole('button', { name: /disable/i }))
    await waitFor(() =>
      expect(api.setSuperadminActive).toHaveBeenCalledWith('b1', false),
    )
  })

  it('hides the superadmins section from non-superadmins', async () => {
    authState.can = (p: string) => !p.startsWith('user:')
    wrap(<PeopleTab />)
    await screen.findByText(/pending invitations/i)
    expect(
      screen.queryByText(/^Installation administrators$/),
    ).not.toBeInTheDocument()
    expect(api.listSuperadmins).not.toHaveBeenCalled()
  })
})

describe('SSOTab', () => {
  it('is superadmin-only', async () => {
    authState.isSuperadmin = false
    wrap(<SSOTab />)
    expect(await screen.findByText(/only a superadmin/i)).toBeInTheDocument()
    expect(api.getSSO).not.toHaveBeenCalled()
  })

  it('renders the redirect URI and a configure action for a superadmin', async () => {
    wrap(<SSOTab />)
    expect(
      await screen.findByDisplayValue(
        'https://panel.example/v1/auth/federation/callback',
      ),
    ).toBeInTheDocument()
    expect(
      screen.getByRole('button', { name: /configure sso/i }),
    ).toBeInTheDocument()
  })

  it('saves an OIDC configuration', async () => {
    api.putSSO.mockResolvedValue({
      configured: true,
      provider_available: true,
      protocol: 'oidc',
      status: 'active',
      redirect_uri: 'x',
      require_sso: false,
      network_allowlist: [],
      enforced_by: 'unavailable',
    })
    const user = userEvent.setup()
    wrap(<SSOTab />)
    await user.click(
      await screen.findByRole('button', { name: /configure sso/i }),
    )
    await user.type(screen.getByLabelText(/issuer url/i), 'https://idp.example')
    await user.type(screen.getByLabelText(/client id/i), 'cid')
    await user.type(screen.getByLabelText(/^client secret$/i), 'shh')
    await user.click(
      screen.getByRole('button', { name: /save configuration/i }),
    )
    await waitFor(() =>
      expect(api.putSSO).toHaveBeenCalledWith(
        expect.objectContaining({
          protocol: 'oidc',
          enabled: false,
          oidc_issuer: 'https://idp.example',
          oidc_client_id: 'cid',
          oidc_client_secret: 'shh',
        }),
        undefined,
        'default',
      ),
    )
  })

  // THE BEHAVIOURAL HALF OF THE C2 GUARD.
  //
  // core/api/consolessopayload_test.go parses buildInput() out of this file. A contrast
  // refuted the first version of that parser by moving the signing cert to the OIDC
  // return: the SAML branch stopped sending it and the Go guard stayed green
  // (the Codex contrast of 2026-08-11, Q1). The parser is branch-aware
  // now, but a static reader can never see a value that is computed, conditional, or
  // spread from a helper.
  //
  // This one does not read source at all: it drives the real form and asserts what putSSO
  // ACTUALLY receives. Any edit that stops the SAML save from carrying both halves of the
  // signing keypair fails here, whatever shape the code takes.
  it('sends both halves of the SAML SP signing keypair', async () => {
    // Open the form already on SAML by loading a SAML config: the protocol control is a
    // custom Select, and driving it would test the widget rather than the payload.
    const samlCfg = {
      configured: true,
      provider_available: true,
      protocol: 'saml',
      status: 'active',
      redirect_uri: 'https://panel.example/cb',
      saml_metadata_url: 'https://idp.example/meta',
      saml_entity_id: 'sp-entity',
      saml_acs_url: 'https://sp.example/acs',
      saml_idp_sso_url: 'https://idp.example/sso',
      require_sso: false,
      network_allowlist: [],
      enforced_by: 'unavailable',
      claimed_domains: [],
    }
    api.getSSO.mockResolvedValue(samlCfg)
    api.putSSO.mockResolvedValue(samlCfg)
    const user = userEvent.setup()
    wrap(<SSOTab />)
    await user.click(await screen.findByRole('button', { name: /edit/i }))

    await user.type(
      await screen.findByLabelText(/^sp signing certificate/i),
      'SIGN-CERT',
    )
    await user.type(
      screen.getByLabelText(/^sp signing private key/i),
      'SIGN-KEY',
    )
    await user.click(
      screen.getByRole('button', { name: /save configuration/i }),
    )
    await waitFor(() =>
      expect(api.putSSO).toHaveBeenCalledWith(
        expect.objectContaining({
          protocol: 'saml',
          saml_sp_sign_cert_pem: 'SIGN-CERT',
          saml_sp_sign_key_pem: 'SIGN-KEY',
        }),
        undefined,
        'default',
      ),
    )
  })

  it('sends the stored posture, groups claim and domains back unchanged on save', async () => {
    api.getSSO.mockResolvedValue({
      configured: true,
      provider_available: true,
      protocol: 'oidc',
      status: 'active',
      oidc_issuer: 'https://idp.example',
      oidc_client_id: 'cid',
      oidc_groups_claim: 'groups',
      redirect_uri: 'https://panel.example/cb',
      require_sso: true,
      network_allowlist: ['10.0.0.0/8'],
      enforced_by: 'unavailable',
      claimed_domains: ['corp.example'],
    })
    api.putSSO.mockResolvedValue({ configured: true })
    const user = userEvent.setup()
    wrap(<SSOTab />)
    await user.click(await screen.findByRole('button', { name: /edit/i }))
    await user.click(
      await screen.findByRole('button', { name: /save configuration/i }),
    )
    await waitFor(() =>
      expect(api.putSSO).toHaveBeenCalledWith(
        expect.objectContaining({
          require_sso: true,
          network_allowlist: ['10.0.0.0/8'],
          oidc_groups_claim: 'groups',
          claimed_domains: ['corp.example'],
        }),
        undefined,
        'default',
      ),
    )
  })

  it('sends the stored SAML groups attribute and posture back unchanged on save', async () => {
    api.getSSO.mockResolvedValue({
      configured: true,
      provider_available: true,
      protocol: 'saml',
      status: 'active',
      redirect_uri: 'https://panel.example/cb',
      saml_metadata_url: 'https://idp.example/meta',
      saml_entity_id: 'sp-entity',
      saml_acs_url: 'https://sp.example/acs',
      saml_idp_sso_url: 'https://idp.example/sso',
      saml_groups_attr: 'memberOf',
      require_sso: true,
      network_allowlist: ['10.0.0.0/8'],
      enforced_by: 'unavailable',
      claimed_domains: ['corp.example'],
    })
    api.putSSO.mockResolvedValue({ configured: true })
    const user = userEvent.setup()
    wrap(<SSOTab />)
    await user.click(await screen.findByRole('button', { name: /edit/i }))
    await user.click(
      await screen.findByRole('button', { name: /save configuration/i }),
    )
    await waitFor(() =>
      expect(api.putSSO).toHaveBeenCalledWith(
        expect.objectContaining({
          protocol: 'saml',
          saml_groups_attr: 'memberOf',
          require_sso: true,
          network_allowlist: ['10.0.0.0/8'],
          claimed_domains: ['corp.example'],
        }),
        undefined,
        'default',
      ),
    )
  })
})

describe('ScopesTab', () => {
  it('lists workspaces', async () => {
    api.listWorkspaces.mockResolvedValue({
      items: [
        {
          id: 'w1',
          tenant_id: 't1',
          name: 'Payments',
          slug: 'payments',
          status: 'active',
          is_default: false,
          created_at: '',
          updated_at: '',
          version: 1,
        },
      ],
      has_more: false,
    })
    wrap(<ScopesTab />)
    expect(
      await screen.findByRole('cell', { name: 'Payments' }),
    ).toBeInTheDocument()
  })

  it('creates a workspace (owner)', async () => {
    api.createWorkspace.mockResolvedValue({
      id: 'w2',
      tenant_id: 't1',
      name: 'EU',
      slug: 'eu',
      status: 'active',
      is_default: false,
      created_at: '',
      updated_at: '',
      version: 1,
    })
    const user = userEvent.setup()
    wrap(<ScopesTab />)
    await user.click(
      await screen.findByRole('button', { name: /new workspace/i }),
    )
    const dialog = await screen.findByRole('dialog')
    // The slug is derived from the name ("EU" -> "eu"); no hand-typed slug.
    await user.type(within(dialog).getByLabelText(/name/i), 'EU')
    // The dialog's submit button (the create label, also "New workspace").
    await user.click(
      within(dialog).getByRole('button', { name: /new workspace/i }),
    )
    await waitFor(() =>
      expect(api.createWorkspace).toHaveBeenCalledWith({
        name: 'EU',
        slug: 'eu',
      }),
    )
  })
})

describe('RolesTab', () => {
  it('shows the superadmin delegation authority', async () => {
    wrap(<RolesTab />)
    expect(
      await screen.findByText(/you administer this installation/i),
    ).toBeInTheDocument()
  })

  it('shows a load error (never "no authority") when the ceiling fetch fails', async () => {
    api.delegationAuthority.mockRejectedValue(new Error('boom'))
    wrap(<RolesTab />)
    expect(
      await screen.findByText(/could not load your delegation authority/i),
    ).toBeInTheDocument()
    expect(
      screen.queryByText(/you have no delegation authority/i),
    ).not.toBeInTheDocument()
  })

  it('shows a scoped delegation ceiling for a non-superadmin', async () => {
    api.delegationAuthority.mockResolvedValue({
      superadmin: false,
      domains: [
        {
          scope_tree: 'workspace',
          scope_ref: 'payments',
          permissions: ['agent:read', 'agent:write'],
        },
      ],
    })
    wrap(<RolesTab />)
    expect(await screen.findByText('Workspace: payments')).toBeInTheDocument()
    expect(await screen.findByText('agent:write')).toBeInTheDocument()
  })

  it('shows the inheritance filters beside the scoped grants', async () => {
    wrap(<RolesTab />)
    expect(
      await screen.findByRole('heading', { name: 'Inheritance filters' }),
    ).toBeInTheDocument()
    expect(api.listInheritanceFilters).toHaveBeenCalled()
  })

  it('lists scoped grants', async () => {
    api.listGrants.mockResolvedValue({
      items: [
        {
          id: 'g1',
          subject_kind: 'user',
          subject_ref: 'u-123',
          role: 'admin',
          scope_tree: 'workspace',
          scope_ref: 'payments',
        },
      ],
      has_more: false,
    })
    wrap(<RolesTab />)
    expect(await screen.findByText('u-123')).toBeInTheDocument()
    expect(await screen.findByText('Workspace: payments')).toBeInTheDocument()
  })

  it('lists custom roles', async () => {
    api.listRoles.mockResolvedValue({
      items: [{ name: 'auditor', permissions: ['finding:read', 'cost:read'] }],
      has_more: false,
    })
    wrap(<RolesTab />)
    expect(await screen.findByText('auditor')).toBeInTheDocument()
  })

  it('keeps stored roles and grants readable and revocable without authoring', async () => {
    api.listRoles.mockResolvedValue({
      items: [{ name: 'auditor', permissions: ['agent:read'] }],
      has_more: false,
    })
    api.listGrants.mockResolvedValue({
      items: [
        {
          id: 'g1',
          subject_kind: 'user',
          subject_ref: 'u1',
          role: 'auditor',
          role_custom: true,
          scope_tree: 'tenant',
        },
      ],
      has_more: false,
    })
    wrap(<RolesTab />)
    expect(await screen.findAllByText('auditor')).not.toHaveLength(0)
    expect(screen.getByText(/editing.*requires business/i)).toBeInTheDocument()
    expect(
      screen.queryByRole('button', {
        name: /new role|new grant|new permission-group|edit role/i,
      }),
    ).not.toBeInTheDocument()
    expect(
      screen.getByRole('button', { name: /^revoke$/i }),
    ).toBeInTheDocument()
    expect(
      screen.getByRole('button', { name: /^delete$/i }),
    ).toBeInTheDocument()
  })

  it('does not grant access when can() is false (the client never decides)', async () => {
    authState.can = (_p: string) => false
    wrap(<RolesTab />)
    expect(await screen.findByText(/you need governance/i)).toBeInTheDocument()
    expect(api.rbacCatalog).not.toHaveBeenCalled()
    expect(api.listGrants).not.toHaveBeenCalled()
    expect(api.listInheritanceFilters).not.toHaveBeenCalled()
    expect(api.listGroups).not.toHaveBeenCalled()
  })
})

describe('RolesTab — S256 group hierarchy', () => {
  it('shows provisioned groups with their parent', async () => {
    api.listGroups.mockResolvedValue({
      groups: [
        {
          id: 'g1',
          display_name: 'Engineering',
          external_id: 'eng',
          mapped_role: 'editor',
          parent_group_id: '',
          members: 5,
        },
        {
          id: 'g2',
          display_name: 'Frontend',
          external_id: 'fe',
          mapped_role: '',
          parent_group_id: 'g1',
          members: 3,
        },
      ],
    })
    wrap(<RolesTab />)
    // 'Engineering' appears both as g1's name cell and as g2's parent reference cell
    expect(
      (await screen.findAllByText('Engineering')).length,
    ).toBeGreaterThanOrEqual(1)
    expect(await screen.findByText('Frontend')).toBeInTheDocument()
  })

  it('shows empty state when no groups are provisioned', async () => {
    wrap(<RolesTab />)
    expect(await screen.findByText(/no groups yet/i)).toBeInTheDocument()
  })
})

describe('RolesTab — model-access', () => {
  it('distinguishes allow from forbid in the rules table', async () => {
    api.listModelAccess.mockResolvedValue({
      items: [
        {
          id: 'ma1',
          subject_kind: 'user',
          subject_ref: 'u1',
          target_kind: 'model',
          target_ref: 'claude-opus-4-6',
          workspace_ref: '',
          surfaces: [],
          effect: 'allow',
          description: '',
        },
        {
          id: 'ma2',
          subject_kind: 'role',
          subject_ref: 'viewer',
          target_kind: 'model',
          target_ref: 'claude-opus-4-6',
          workspace_ref: '',
          surfaces: [],
          effect: 'forbid',
          description: '',
        },
      ],
      has_more: false,
    })
    wrap(<RolesTab />)
    expect(await screen.findByText('Allow')).toBeInTheDocument()
    expect(await screen.findByText('Forbid')).toBeInTheDocument()
  })
})

describe('RolesTab — access review', () => {
  it('does not render access review search UI when authz:read is missing', async () => {
    authState.can = (p: string) => !p.startsWith('authz:')
    wrap(<RolesTab />)
    await screen.findByText(/you administer this installation/i)
    // The gate notice is shown instead of the search tabs
    expect(
      await screen.findByText(/you need authz read access/i),
    ).toBeInTheDocument()
    // The interactive tab UI is not rendered (no tab elements in the DOM)
    expect(screen.queryByRole('tab')).not.toBeInTheDocument()
  })
})
