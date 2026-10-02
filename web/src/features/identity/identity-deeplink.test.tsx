// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// the `?tab=` seam on Identity & NHI.
//
// The access map sends an operator here to review the origin identity of an observed access,
// and the roster lives on the `inventory` tab. With the tab as plain local state seeded at
// `federation`, that link landed on a different surface — the same "leads nowhere" this work
// removes. The tabs are stubbed: what is under test is which tab a URL selects.
//
// and the seam only READ the parameter. Console and Claude policy write the new tab
// back with `replace: true`; this one did not, and nothing said so, because the suite tested
// only the landing. A shared deep link therefore reopened the roster whatever tab was on
// screen. The write is here now, and so is the case that turns red without it.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import { queryKeys } from '@/lib/api/query'
import { liveCapabilityContext } from '@/lib/auth/capabilities'
import { useCommandStore } from '@/stores/command'
import { useSessionStore } from '@/stores/session'
import { useTenantStore } from '@/stores/tenant'
import { useWorkspaceStore } from '@/stores/workspace'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import './i18n'

const navigate = vi.fn()
vi.mock('@tanstack/react-router', () => ({
  useNavigate: () => navigate,
  // No RouterProvider in this test: the shared Tabs strip consults useRouter, and the real
  // hook answers undefined here (console-tab-scroll-restoration R2, 2026-09-06).
  useRouter: () => undefined,
}))

// The Identity page's administration sections need `tenant:admin`; by default this principal
// lacks it, so every case below sees exactly the identity sections /identity always had.
const auth = vi.hoisted(() => ({ admin: false }))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    can: (p: string) =>
      (p === 'tenant:admin' || p === 'membership:write') && auth.admin,
    activeTenant: 't1',
  }),
}))
vi.mock('@/features/console/people-tab', () => ({
  PeopleTab: ({ inviteRequested }: { inviteRequested?: boolean }) => (
    <div>
      {inviteRequested ? 'PeopleTab mounted · invite' : 'PeopleTab mounted'}
    </div>
  ),
}))
vi.mock('@/features/console/roles-tab', () => ({
  RolesTab: () => <div>RolesTab mounted</div>,
}))
vi.mock('@/features/console/sso-tab', () => ({
  SSOTab: () => <div>SSOTab mounted</div>,
}))
vi.mock('@/features/console/api-keys-tab', () => ({
  ApiKeysTab: () => <div>ApiKeysTab mounted</div>,
}))

vi.mock('@/features/recordings/recording-notice', () => ({
  RecordingNotice: () => null,
}))
vi.mock('./federation', () => ({
  FederationTab: () => <div>FederationTab mounted</div>,
}))
vi.mock('./nhi-roster', () => ({
  NhiRosterTab: () => <div>NhiRosterTab mounted</div>,
}))
vi.mock('./nhi-lifecycle', () => ({
  NhiLifecycleTab: () => <div>NhiLifecycleTab mounted</div>,
}))
vi.mock('./mcp-auth', () => ({
  McpAuthTab: () => <div>McpAuthTab mounted</div>,
}))
vi.mock('./wif/wif-graph', () => ({
  WifGraphTab: () => <div>WifGraphTab mounted</div>,
}))
vi.mock('./posture', () => ({
  PostureTab: () => <div>PostureTab mounted</div>,
}))
vi.mock('./privileged-login', () => ({
  PrivilegedLoginTab: () => <div>PrivilegedLoginTab mounted</div>,
}))

import IdentityView from './identity-view'

function renderAt(search: string, client?: QueryClient) {
  window.history.replaceState({}, '', `/identity${search}`)
  // The view listens for its ⌘K verb (Invite people), whose authority reads the query
  // cache, so it mounts inside a client as it does in the app.
  const qc =
    client ?? new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <IdentityView />
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  window.history.replaceState({}, '', '/identity')
  navigate.mockClear()
  auth.admin = false
})

describe('IdentityView — ?tab= deep link', () => {
  it('opens the NHI roster when the access map links to ?tab=inventory', () => {
    renderAt('?tab=inventory')
    expect(screen.getByText('NhiRosterTab mounted')).toBeInTheDocument()
    expect(screen.queryByText('FederationTab mounted')).toBeNull()
  })

  it('falls back to federation with no tab parameter', () => {
    // Non-firing direction: a seam honouring any value would pass the case above and
    // change what every plain visit to /identity shows.
    renderAt('')
    expect(screen.getByText('FederationTab mounted')).toBeInTheDocument()
  })

  it('falls back to federation for a tab that does not exist', () => {
    renderAt('?tab=nope')
    expect(screen.getByText('FederationTab mounted')).toBeInTheDocument()
  })
})

describe('IdentityView — the URL follows a manual tab change', () => {
  it('writes the new tab, REPLACING the history entry rather than pushing one', async () => {
    const user = userEvent.setup()
    renderAt('')
    await user.click(screen.getByRole('tab', { name: /inventory|roster/i }))

    expect(navigate).toHaveBeenCalledTimes(1)
    const arg = navigate.mock.calls[0]![0] as {
      search: (p: Record<string, unknown>) => Record<string, unknown>
      replace: boolean
      resetScroll: boolean
    }
    expect(arg.replace).toBe(true)
    expect(arg.search({})).toEqual({ tab: 'inventory' })
    // Out of scroll restoration too, like the console's setter: a tab switch is not a
    // page change (console-tab-scroll-restoration, 2026-09-06).
    expect(arg.resetScroll).toBe(false)
  })

  it('PRESERVES the parameters already on the URL instead of clearing them', async () => {
    const user = userEvent.setup()
    renderAt('?tab=federation')
    await user.click(screen.getByRole('tab', { name: /inventory|roster/i }))

    const arg = navigate.mock.calls[0]![0] as {
      search: (p: Record<string, unknown>) => Record<string, unknown>
    }
    expect(arg.search({ q: 'svc', tab: 'federation' })).toEqual({
      q: 'svc',
      tab: 'inventory',
    })
  })

  it('does NOT navigate on the initial deep-linked render', () => {
    // Non-firing direction: writing while reading would fight the caller's own navigation.
    renderAt('?tab=inventory')
    expect(navigate).not.toHaveBeenCalled()
  })
})

describe('IdentityView — Identities & access (console remake slice 3)', () => {
  it('for an administrator, adds people, roles, identity providers and API keys and opens People', () => {
    auth.admin = true
    renderAt('')
    expect(screen.getByText('PeopleTab mounted')).toBeInTheDocument()
    const tabs = screen.getAllByRole('tab').map((t) => t.textContent)
    for (const name of [
      'Users & groups',
      'Roles & delegation',
      'SSO / IdP',
      'API keys',
    ])
      expect(tabs).toContain(name)
    // The identity sections are all still there, after people and roles.
    expect(tabs.indexOf('Users & groups')).toBe(0)
    expect(tabs).toContain('Identity inventory')
  })

  it('without tenant:admin, offers exactly the identity sections and mounts no administration panel', () => {
    renderAt('')
    const tabs = screen.getAllByRole('tab').map((t) => t.textContent)
    expect(tabs).toHaveLength(8)
    expect(tabs).not.toContain('Users & groups')
    expect(
      screen.queryByText(/PeopleTab|RolesTab|SSOTab|ApiKeysTab/),
    ).toBeNull()
  })

  it('opens an administration section by deep link, and ignores it without the permission', () => {
    auth.admin = true
    const first = renderAt('?tab=apiKeys')
    expect(screen.getByText('ApiKeysTab mounted')).toBeInTheDocument()
    first.unmount()
    auth.admin = false
    renderAt('?tab=apiKeys')
    expect(screen.getByText('FederationTab mounted')).toBeInTheDocument()
    expect(screen.queryByText('ApiKeysTab mounted')).toBeNull()
  })
})

describe('IdentityView — the Invite people verb (console remake slice 12)', () => {
  it('opens People with its onboarding dialog requested in invite mode', async () => {
    auth.admin = true
    useTenantStore.setState({ activeTenant: 't1' })
    useSessionStore.setState({ credentialGeneration: 0 })
    useWorkspaceStore.setState({ activeWorkspace: null })
    useCommandStore.setState({ pendingAction: null, open: false, opener: null })
    const qc = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    qc.setQueryData(queryKeys.whoami, {
      kind: 'user',
      user_id: 'u-1',
      actor: 'u-1',
      display_name: 'Ada',
      superadmin: false,
      grants: [],
    })
    useCommandStore
      .getState()
      .setPendingAction('identity', 'invite', liveCapabilityContext(qc))
    renderAt('?tab=federation', qc)
    expect(
      await screen.findByText('PeopleTab mounted · invite'),
    ).toBeInTheDocument()
    expect(useCommandStore.getState().pendingAction).toBeNull()
  })
})
