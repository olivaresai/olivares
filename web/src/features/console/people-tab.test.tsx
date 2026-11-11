// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  cleanup,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactElement, ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import * as consoleLocales from './i18n'

const { api, authState, serverInfo, edition } = vi.hoisted(() => ({
  edition: { business: false },
  api: {
    onboard: vi.fn(),
    listInvites: vi.fn(),
    revokeInvite: vi.fn(),
    resendInvite: vi.fn(),
    listMembers: vi.fn(),
    removeMember: vi.fn(),
    listSuperadmins: vi.fn(),
    setSuperadminActive: vi.fn(),
    endMemberSessions: vi.fn(),
    setMemberSuspended: vi.fn(),
    changeMemberRole: vi.fn(),
  },
  authState: {
    activeTenant: 't1' as string | null,
    activeRole: 'owner' as string | null,
    isSuperadmin: true,
    principal: { aal: 3 } as { aal?: number } | null,
    confinedWorkspace: null as string | null,
    can: (_p: string, _opts?: unknown): boolean => true,
  },
  serverInfo: { data: {} as { invite_delivery_unavailable?: boolean } },
}))

const panels = vi.hoisted(() => ({
  peopleActions: [] as import('@/features/panels').PanelExtension[],
}))
vi.mock('@/features/extensions', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/features/extensions')>()
  return {
    ...actual,
    PANEL_EXTENSIONS: {
      ...actual.PANEL_EXTENSIONS,
      get peopleActions() {
        return panels.peopleActions
      },
    },
  }
})
vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))
vi.mock('@/features/business-build', () => ({
  get BUSINESS_BUILD() {
    return edition.business
  },
}))
vi.mock('@/lib/hooks/use-server-info', () => ({
  useServerInfo: () => serverInfo,
}))
vi.mock('@/features/identity/assurance', () => ({
  AAL: { PASSWORD: 1, MFA: 2, HARDWARE: 3 },
  RequireAssurance: ({ children }: { children: ReactNode }) => <>{children}</>,
}))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, consoleApi: api }
})

import { ApiError } from '@/lib/api/errors'
import { PeopleTab } from './people-tab'

const emptyList = { items: [], has_more: false }
const member = {
  user_id: 'u1',
  email: 'ada@acme.io',
  display_name: 'Ada Lovelace',
  status: 'active',
  external_id: 'okta-ada',
  sso_only: true,
  role: 'admin',
  workspace_ids: ['ws1'],
  groups: ['engineering', 'platform'],
}

function wrap(ui: ReactElement) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

/** A member row's actions sit behind its one menu button. */
async function openRowActions(
  user: ReturnType<typeof userEvent.setup>,
  email: string,
) {
  await user.click(
    await screen.findByRole('button', { name: `Row actions for ${email}` }),
  )
}

beforeEach(() => {
  vi.clearAllMocks()
  authState.can = (_p: string, _opts?: unknown) => true
  authState.activeRole = 'owner'
  authState.isSuperadmin = true
  authState.confinedWorkspace = null
  serverInfo.data = {}
  edition.business = false
  panels.peopleActions = []
  api.listInvites.mockResolvedValue(emptyList)
  api.listMembers.mockResolvedValue(emptyList)
  api.listSuperadmins.mockResolvedValue(emptyList)
})

/**
 * ⛔ UN TESTIGO POR CONSULTA, NO UNO POR PANTALLA. El contraste (F-02) midio que el
 *    trinquete acepta *un aviso cualquiera por feature*: al quitar el badge de `members`,
 *    `people-tab` + `registry.truncation-notice` quedaban en **10/10, rc 0**. Lo reproduje
 *    exactamente antes de escribir esto. Un aviso que puede desaparecer sin poner nada rojo no
 *    esta verificado: esta afirmado — la misma clase que yo mismo exigi cerrar en (A-09).
 *
 * ⛔ Y LA ASERCION ES DE CARDINALIDAD, con UNA sola consulta recortada por caso. Los tres avisos
 *    de esta pantalla comparten el texto generico (`intel:notices.listTruncated`), asi que la
 *    presencia no distingue cual se pinto: si hay exactamente uno y solo una lista dice
 *    `has_more`, ese uno es el suyo. Quitar cualquiera de los tres deja su caso en CERO.
 */
describe('PeopleTab — cada lista declara su recorte por separado', () => {
  const invite = {
    id: 'i1',
    email: 'grace@acme.io',
    tenant: 't1',
    role: 'member',
    expires_at: '2030-01-01T00:00:00Z',
    created_at: '2026-01-01T00:00:00Z',
  }
  const superadmin = { user_id: 'sa1', email: 'root@acme.io', active: true }

  it('members: con has_more sale UN aviso, y sin el ninguno', async () => {
    api.listMembers.mockResolvedValue({ items: [member], has_more: true })
    wrap(<PeopleTab />)
    await screen.findByText(member.email)
    expect(screen.getAllByText(/there are more/i)).toHaveLength(1)

    cleanup()
    api.listMembers.mockResolvedValue({ items: [member], has_more: false })
    wrap(<PeopleTab />)
    await screen.findByText(member.email)
    expect(screen.queryByText(/there are more/i)).not.toBeInTheDocument()
  })

  it('invitaciones: con has_more sale UN aviso, y sin el ninguno', async () => {
    api.listInvites.mockResolvedValue({ items: [invite], has_more: true })
    wrap(<PeopleTab />)
    await screen.findByText(invite.email)
    expect(screen.getAllByText(/there are more/i)).toHaveLength(1)

    cleanup()
    api.listInvites.mockResolvedValue({ items: [invite], has_more: false })
    wrap(<PeopleTab />)
    await screen.findByText(invite.email)
    expect(screen.queryByText(/there are more/i)).not.toBeInTheDocument()
  })

  it('superadmins: con has_more sale UN aviso, y sin el ninguno', async () => {
    api.listSuperadmins.mockResolvedValue({
      items: [superadmin],
      has_more: true,
    })
    wrap(<PeopleTab />)
    await screen.findByText(superadmin.email)
    expect(screen.getAllByText(/there are more/i)).toHaveLength(1)

    cleanup()
    api.listSuperadmins.mockResolvedValue({
      items: [superadmin],
      has_more: false,
    })
    wrap(<PeopleTab />)
    await screen.findByText(superadmin.email)
    expect(screen.queryByText(/there are more/i)).not.toBeInTheDocument()
  })

  /**
   * ⛔ VACIA Y RECORTADA A LA VEZ NO SE ANUNCIA. Con `{items: [], has_more: true}` la pantalla
   *    enseñaba «No active members» y «Loaded 0 …; there are more» AL MISMO TIEMPO: un mensaje
   *    que se contradice solo. Lo midio el contraste (F-04). El estado vacio ya lo explica
   *    todo; el aviso sobra y confunde.
   */
  it('lista vacia con has_more: sale el estado vacio y NINGUN aviso', async () => {
    api.listMembers.mockResolvedValue({ items: [], has_more: true })
    wrap(<PeopleTab />)
    await screen.findByText(/no active members/i)
    expect(screen.queryByText(/there are more/i)).not.toBeInTheDocument()
  })

  /** Direccion no disparadora en bloque: sin ningun recorte, la pantalla no avisa de nada. */
  it('sin recorte en ninguna de las tres, cero avisos', async () => {
    api.listMembers.mockResolvedValue({ items: [member], has_more: false })
    api.listInvites.mockResolvedValue({ items: [invite], has_more: false })
    api.listSuperadmins.mockResolvedValue({
      items: [superadmin],
      has_more: false,
    })
    wrap(<PeopleTab />)
    await screen.findByText(member.email)
    expect(screen.queryAllByText(/there are more/i)).toHaveLength(0)
  })
})

describe('PeopleTab members roster', () => {
  it('shows a forbidden state and does not read members without user read access', async () => {
    authState.can = (permission: string) => !permission.startsWith('user:')
    wrap(<PeopleTab />)

    expect(
      await screen.findByText(/need user read access/i),
    ).toBeInTheDocument()
    expect(api.listMembers).not.toHaveBeenCalled()
  })

  it('renders the empty members state', async () => {
    wrap(<PeopleTab />)

    expect(await screen.findByText(/no active members/i)).toBeInTheDocument()
    expect(api.listMembers).toHaveBeenCalled()
  })

  it('renders a members load error with retry', async () => {
    api.listMembers.mockRejectedValue(new Error('boom'))
    wrap(<PeopleTab />)

    expect(await screen.findByRole('alert')).toBeInTheDocument()
  })

  it('renders role, groups and status, then removes a member from the organization', async () => {
    api.listMembers.mockResolvedValue({ items: [member], has_more: false })
    api.removeMember.mockResolvedValue(undefined)
    const user = userEvent.setup()
    wrap(<PeopleTab />)

    expect(await screen.findByText('ada@acme.io')).toBeInTheDocument()
    expect(screen.getByText('Ada Lovelace')).toBeInTheDocument()
    expect(screen.getByText('Admin')).toBeInTheDocument()
    expect(screen.getByText('engineering')).toBeInTheDocument()
    expect(screen.getByText('platform')).toBeInTheDocument()
    expect(screen.getByText('Active')).toBeInTheDocument()
    expect(screen.getByText('Single sign-on')).toBeInTheDocument()
    // One control per row: three action buttons pushed the table past its card.
    const row = screen.getByText('ada@acme.io').closest('tr') as HTMLElement
    expect(within(row).getAllByRole('button')).toHaveLength(1)

    await openRowActions(user, 'ada@acme.io')
    await user.click(
      screen.getByRole('menuitem', {
        name: /remove ada@acme\.io from the organization/i,
      }),
    )
    const dialog = await screen.findByRole('dialog', {
      name: /remove from organization/i,
    })
    expect(
      within(dialog).getByText(/provisioned by an IdP/i),
    ).toBeInTheDocument()
    await user.click(within(dialog).getByRole('button', { name: /^remove$/i }))

    await waitFor(() => expect(api.removeMember).toHaveBeenCalledWith('u1'))
  })

  it('ends a member sign-in after confirmation', async () => {
    api.listMembers.mockResolvedValue({ items: [member], has_more: false })
    api.endMemberSessions.mockResolvedValue(undefined)
    const user = userEvent.setup()
    wrap(<PeopleTab />)

    await openRowActions(user, 'ada@acme.io')
    await user.click(
      await screen.findByRole('menuitem', {
        name: /end sign-ins for ada@acme\.io/i,
      }),
    )
    const dialog = await screen.findByRole('dialog', {
      name: /end sign-ins for ada@acme\.io/i,
    })
    await user.click(
      within(dialog).getByRole('button', { name: /^end sign-ins$/i }),
    )

    await waitFor(() =>
      expect(api.endMemberSessions).toHaveBeenCalledWith(
        'u1',
        expect.objectContaining({
          signal: expect.any(AbortSignal),
          dispatchGuard: expect.any(Function),
        }),
      ),
    )
  })

  it('never offers to remove yourself or the only owner', async () => {
    const owner = {
      ...member,
      user_id: 'u-owner',
      email: 'owner@acme.io',
      role: 'owner' as const,
    }
    const self = { ...member, user_id: 'u-self', email: 'me@acme.io' }
    api.listMembers.mockResolvedValue({
      items: [owner, self, member],
      has_more: false,
    })
    authState.principal = {
      aal: 3,
      user_id: 'u-self',
    } as typeof authState.principal
    const user = userEvent.setup()
    wrap(<PeopleTab />)

    expect(await screen.findByText('owner@acme.io')).toBeInTheDocument()
    // Your own row offers nothing: no removal, and your own sign-ins are ended from
    // Settings, never from your own People row.
    expect(
      screen.queryByRole('button', { name: 'Row actions for me@acme.io' }),
    ).toBeNull()
    await openRowActions(user, 'owner@acme.io')
    expect(
      screen.queryByRole('menuitem', { name: /remove owner@acme\.io/i }),
    ).toBeNull()
    await user.keyboard('{Escape}')
    await openRowActions(user, 'ada@acme.io')
    expect(
      screen.getByRole('menuitem', { name: /remove ada@acme\.io/i }),
    ).toBeInTheDocument()
    expect(
      screen.getByRole('menuitem', { name: /end sign-ins for ada@acme\.io/i }),
    ).toBeInTheDocument()
    expect(screen.queryByRole('switch')).toBeNull()
    authState.principal = { aal: 3 }
  })

  // The engine keeps an organization an owner who runs it: active, and over the whole
  // organization. Removal is offered exactly where another such owner remains.
  describe('the last owner, as the engine counts it', () => {
    const owner = {
      ...member,
      user_id: 'u-owner',
      email: 'owner@acme.io',
      role: 'owner' as const,
      workspace_ids: [],
    }

    it('never offers to remove the only owner of the whole organization, and still removes a confined one', async () => {
      const confined = {
        ...owner,
        user_id: 'u-o2',
        email: 'o2@acme.io',
        workspace_ids: ['ws1'],
      }
      api.listMembers.mockResolvedValue({
        items: [owner, confined],
        has_more: false,
      })
      const user = userEvent.setup()
      wrap(<PeopleTab />)
      await openRowActions(user, 'owner@acme.io')
      expect(
        screen.queryByRole('menuitem', { name: /remove owner@acme\.io/i }),
      ).toBeNull()
      await user.keyboard('{Escape}')
      await openRowActions(user, 'o2@acme.io')
      expect(
        screen.getByRole('menuitem', { name: /remove o2@acme\.io/i }),
      ).toBeInTheDocument()
    })

    it('removes a deactivated owner, and changes its role, while an active owner remains', async () => {
      const away = {
        ...owner,
        user_id: 'u-o2',
        email: 'o2@acme.io',
        status: 'inactive',
      }
      api.listMembers.mockResolvedValue({
        items: [owner, away],
        has_more: false,
      })
      const user = userEvent.setup()
      wrap(<PeopleTab />)
      expect(
        await screen.findByRole('combobox', { name: 'Role of o2@acme.io' }),
      ).toBeInTheDocument()
      await openRowActions(user, 'o2@acme.io')
      expect(
        screen.getByRole('menuitem', { name: /remove o2@acme\.io/i }),
      ).toBeInTheDocument()
    })
  })

  // Remove is absent where the engine refuses it: a role above the caller's own, and any
  // row for a caller confined to a workspace (a removal is a tenant-wide act).
  describe('Remove, where the engine accepts it', () => {
    const ana = {
      ...member,
      user_id: 'u2',
      email: 'ana@acme.io',
      role: 'editor' as const,
      workspace_ids: [],
    }

    it('offers no removal of a role above your own', async () => {
      authState.activeRole = 'admin'
      authState.isSuperadmin = false
      const owner = {
        ...ana,
        user_id: 'u-owner',
        email: 'owner@acme.io',
        role: 'owner' as const,
      }
      api.listMembers.mockResolvedValue({
        items: [owner, { ...owner, user_id: 'u-o2', email: 'o2@acme.io' }, ana],
        has_more: false,
      })
      const user = userEvent.setup()
      wrap(<PeopleTab />)
      await openRowActions(user, 'owner@acme.io')
      expect(
        screen.queryByRole('menuitem', { name: /remove owner@acme\.io/i }),
      ).toBeNull()
      await user.keyboard('{Escape}')
      await openRowActions(user, 'ana@acme.io')
      expect(
        screen.getByRole('menuitem', { name: /remove ana@acme\.io/i }),
      ).toBeInTheDocument()
    })

    it('offers no removal to a caller who works within a workspace', async () => {
      authState.confinedWorkspace = 'ws1'
      api.listMembers.mockResolvedValue({ items: [ana], has_more: false })
      const user = userEvent.setup()
      wrap(<PeopleTab />)
      await openRowActions(user, 'ana@acme.io')
      expect(
        screen.queryByRole('menuitem', { name: /remove ana@acme\.io/i }),
      ).toBeNull()
    })
  })

  it('offers no enable for a member the deployment suspended, only its removal', async () => {
    api.listMembers.mockResolvedValue({
      items: [{ ...member, status: 'inactive' }],
      has_more: false,
    })
    api.removeMember.mockResolvedValue(undefined)
    const user = userEvent.setup()
    wrap(<PeopleTab />)

    expect(await screen.findByText('ada@acme.io')).toBeInTheDocument()
    expect(screen.getByText(/suspended by the deployment/i)).toBeInTheDocument()
    expect(screen.queryByRole('switch')).not.toBeInTheDocument()
    expect(screen.queryByText(/^enable$/i)).not.toBeInTheDocument()

    await openRowActions(user, 'ada@acme.io')
    await user.click(
      screen.getByRole('menuitem', {
        name: /remove ada@acme\.io from the organization/i,
      }),
    )
    const dialog = await screen.findByRole('dialog', {
      name: /remove from organization/i,
    })
    await user.click(within(dialog).getByRole('button', { name: /^remove$/i }))

    await waitFor(() => expect(api.removeMember).toHaveBeenCalledWith('u1'))
  })
})

// The table of CONCEPT §4.15: who is waiting comes first, each person reads as a name over
// an address beside their mark, and how they sign in is a column. Directory groups are
// Business data: a Community table without them has no Groups column.
describe('PeopleTab — the people table', () => {
  it('lists the pending invitations above the people', async () => {
    api.listMembers.mockResolvedValue({ items: [member], has_more: false })
    wrap(<PeopleTab />)
    const pending = await screen.findByRole('heading', {
      name: 'Pending invitations',
    })
    const people = screen.getByRole('heading', { name: 'Active members' })
    expect(
      pending.compareDocumentPosition(people) &
        Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy()
  })

  it('names how each person signs in', async () => {
    api.listMembers.mockResolvedValue({
      items: [
        member,
        { ...member, user_id: 'u2', email: 'bo@acme.io', sso_only: false },
      ],
      has_more: false,
    })
    wrap(<PeopleTab />)
    const sso = (await screen.findByText('ada@acme.io')).closest('tr')
    const local = screen.getByText('bo@acme.io').closest('tr')
    expect(sso).toHaveTextContent('Single sign-on')
    expect(local).toHaveTextContent('Password')
    expect(local).not.toHaveTextContent('Single sign-on')
    expect(
      screen.getByRole('columnheader', { name: 'Sign-in' }),
    ).toBeInTheDocument()
  })

  it('shows the Groups column only where someone has directory groups', async () => {
    api.listMembers.mockResolvedValue({
      items: [{ ...member, groups: [] }],
      has_more: false,
    })
    wrap(<PeopleTab />)
    await screen.findByText('ada@acme.io')
    expect(screen.queryByRole('columnheader', { name: 'Groups' })).toBeNull()
    expect(screen.queryByText('No groups')).toBeNull()

    cleanup()
    api.listMembers.mockResolvedValue({ items: [member], has_more: false })
    wrap(<PeopleTab />)
    await screen.findByText('ada@acme.io')
    expect(
      screen.getByRole('columnheader', { name: 'Groups' }),
    ).toBeInTheDocument()
  })
})

// A role is changed where the person is listed, by whoever may write memberships, after a
// confirmation that names the change, and never into a lock-out: your own role and the
// organization's only owner are read with the reason beside them, never a control.
describe('PeopleTab — a role changes from its row', () => {
  // A tenant-wide member: the one whose role the route changes without touching where
  // they work (the route rewrites the confinement of the row it updates).
  const ana = {
    ...member,
    user_id: 'u2',
    email: 'ana@acme.io',
    display_name: 'Ana',
    role: 'editor' as const,
    workspace_ids: [],
  }
  const owner = {
    ...ana,
    user_id: 'u-owner',
    email: 'owner@acme.io',
    display_name: 'Olga',
    role: 'owner' as const,
  }

  async function pick(
    user: ReturnType<typeof userEvent.setup>,
    email: string,
    role: string,
  ) {
    await user.click(
      await screen.findByRole('combobox', { name: `Role of ${email}` }),
    )
    await user.click(await screen.findByRole('option', { name: role }))
  }

  it('changes a role only once its confirmation names the change', async () => {
    api.listMembers.mockResolvedValue({ items: [ana], has_more: false })
    api.changeMemberRole.mockResolvedValue({})
    const user = userEvent.setup()
    wrap(<PeopleTab />)
    await pick(user, 'ana@acme.io', 'Admin')
    const dialog = await screen.findByRole('dialog', {
      name: 'Change the role of Ana from Editor to Admin?',
    })
    expect(
      within(dialog).getByText(/add and remove people/),
    ).toBeInTheDocument()
    expect(api.changeMemberRole).not.toHaveBeenCalled()
    await user.click(
      within(dialog).getByRole('button', { name: 'Change role' }),
    )
    await waitFor(() => expect(api.changeMemberRole).toHaveBeenCalledTimes(1))
    expect(api.changeMemberRole.mock.calls[0][0]).toEqual({
      user_id: 'u2',
      tenant: 't1',
      role: 'admin',
    })
    expect(api.changeMemberRole.mock.calls[0][0]).not.toHaveProperty(
      'workspace_id',
    )
    await waitFor(() => expect(api.listMembers).toHaveBeenCalledTimes(2))
  })

  it('cancel sends nothing, keeps the old role and returns to the control', async () => {
    api.listMembers.mockResolvedValue({ items: [ana], has_more: false })
    const user = userEvent.setup()
    wrap(<PeopleTab />)
    await pick(user, 'ana@acme.io', 'Viewer')
    const dialog = await screen.findByRole('dialog', {
      name: 'Change the role of Ana from Editor to Viewer?',
    })
    expect(
      within(dialog).getByText(/no longer start sessions/),
    ).toBeInTheDocument()
    await user.click(within(dialog).getByRole('button', { name: 'Cancel' }))
    const control = screen.getByRole('combobox', {
      name: 'Role of ana@acme.io',
    })
    expect(control).toHaveTextContent('Editor')
    await waitFor(() => expect(control).toHaveFocus())
    expect(api.changeMemberRole).not.toHaveBeenCalled()
  })

  it('reads your own role and the only owner with the reason, and no key changes them', async () => {
    const self = {
      ...ana,
      user_id: 'u-self',
      email: 'me@acme.io',
      role: 'admin' as const,
    }
    api.listMembers.mockResolvedValue({ items: [owner, self], has_more: false })
    authState.principal = {
      aal: 3,
      user_id: 'u-self',
    } as typeof authState.principal
    const user = userEvent.setup()
    wrap(<PeopleTab />)
    await screen.findByText('me@acme.io')
    const own = screen.getByText('me@acme.io').closest('tr') as HTMLElement
    const only = screen.getByText('owner@acme.io').closest('tr') as HTMLElement
    expect(within(own).queryByRole('combobox')).toBeNull()
    expect(within(only).queryByRole('combobox')).toBeNull()
    expect(own).toHaveTextContent(
      'This is your own role. Another administrator or an owner can change it.',
    )
    expect(only).toHaveTextContent(
      'The only owner of the whole organization keeps this role. Make someone else an owner first.',
    )
    await openRowActions(user, 'owner@acme.io')
    await user.keyboard('{Escape}')
    await user.keyboard('v')
    expect(api.changeMemberRole).not.toHaveBeenCalled()
    expect(screen.queryByRole('dialog')).toBeNull()
    authState.principal = { aal: 3 }
  })

  it('counts only an owner of the whole organization as one who runs it', async () => {
    const confined = {
      ...owner,
      user_id: 'u-o2',
      email: 'o2@acme.io',
      workspace_ids: ['ws1'],
    }
    api.listMembers.mockResolvedValue({
      items: [owner, confined],
      has_more: false,
    })
    wrap(<PeopleTab />)
    const only = (await screen.findByText('owner@acme.io')).closest('tr')
    const other = screen.getByText('o2@acme.io').closest('tr')
    expect(
      screen.queryByRole('combobox', { name: 'Role of owner@acme.io' }),
    ).toBeNull()
    expect(only).toHaveTextContent(
      'The only owner of the whole organization keeps this role. Make someone else an owner first.',
    )
    expect(other).toHaveTextContent(
      'This person works within a workspace, so their role cannot change here yet.',
    )
    expect(other).not.toHaveTextContent('The only owner')
  })

  it.each(['inactive', 'suspended'] as const)(
    'counts no %s owner as one who runs the organization',
    async (status) => {
      api.listMembers.mockResolvedValue({
        items: [
          owner,
          { ...owner, user_id: 'u-o2', email: 'o2@acme.io', status },
        ],
        has_more: false,
      })
      wrap(<PeopleTab />)
      const only = (await screen.findByText('owner@acme.io')).closest('tr')
      expect(
        screen.queryByRole('combobox', { name: 'Role of owner@acme.io' }),
      ).toBeNull()
      expect(only).toHaveTextContent(
        'The only owner of the whole organization keeps this role. Make someone else an owner first.',
      )
    },
  )

  it("says who changes an owner's own role", async () => {
    api.listMembers.mockResolvedValue({
      items: [owner, { ...owner, user_id: 'u-o2', email: 'o2@acme.io' }],
      has_more: false,
    })
    authState.principal = {
      aal: 3,
      user_id: 'u-owner',
    } as typeof authState.principal
    wrap(<PeopleTab />)
    const own = (await screen.findByText('owner@acme.io')).closest('tr')
    expect(own).toHaveTextContent(
      'This is your own role. Another owner can change it.',
    )
    authState.principal = { aal: 3 }
  })

  it('offers no control for a member who works within a workspace, and says why', async () => {
    api.listMembers.mockResolvedValue({ items: [member], has_more: false })
    wrap(<PeopleTab />)
    const row = (await screen.findByText('ada@acme.io')).closest('tr')
    expect(screen.queryByRole('combobox', { name: /^Role of / })).toBeNull()
    expect(row).toHaveTextContent(
      'This person works within a workspace, so their role cannot change here yet.',
    )
  })

  it('offers no control to a caller who works within a workspace', async () => {
    authState.confinedWorkspace = 'ws1'
    api.listMembers.mockResolvedValue({ items: [ana], has_more: false })
    wrap(<PeopleTab />)
    expect(await screen.findByText('Editor')).toBeInTheDocument()
    expect(screen.queryByRole('combobox', { name: /^Role of / })).toBeNull()
  })

  it('offers no role above your own, and leaves a higher role as it is', async () => {
    authState.activeRole = 'admin'
    authState.isSuperadmin = false
    api.listMembers.mockResolvedValue({ items: [owner, ana], has_more: false })
    const user = userEvent.setup()
    wrap(<PeopleTab />)
    await user.click(
      await screen.findByRole('combobox', { name: 'Role of ana@acme.io' }),
    )
    const names = (await screen.findAllByRole('option')).map(
      (o) => o.textContent,
    )
    expect(names).toEqual(['Viewer', 'Editor', 'Admin'])
    expect(
      screen.queryByRole('combobox', { name: 'Role of owner@acme.io' }),
    ).toBeNull()
  })

  it('shows the role as read without the right to write memberships', async () => {
    authState.can = (p: string) => p !== 'membership:write'
    api.listMembers.mockResolvedValue({ items: [ana], has_more: false })
    wrap(<PeopleTab />)
    expect(await screen.findByText('Editor')).toBeInTheDocument()
    expect(screen.queryByRole('combobox', { name: /^Role of / })).toBeNull()
  })
})

/**
 * "INVITE SOMEONE TO ADD THE FIRST ONE" NAMED A CONTROL THIS PANEL DID NOT CARRY.
 *
 * Invite stands just above the invitations panel, yet an empty panel carries its own
 * next step instead of pointing at another control. The census counted this among
 * the ten empty states with no action.
 */
describe('PeopleTab — the empty invitations panel', () => {
  it('opens the onboard dialog, under a name of its own', async () => {
    const user = userEvent.setup()
    wrap(<PeopleTab />)
    const fromEmpty = await screen.findByRole('button', {
      name: 'Invite the first user',
    })
    // Two controls, one dialog, two names: a query for either finds exactly one.
    expect(screen.getAllByRole('button', { name: 'Invite' })).toHaveLength(1)
    await user.click(fromEmpty)
    expect(await screen.findByRole('dialog')).toBeInTheDocument()
  })

  it('offers no door to a reader who may not invite', async () => {
    authState.can = (p: string) => p !== 'membership:write'
    wrap(<PeopleTab />)
    await screen.findByText('No pending invitations.')
    expect(
      screen.queryByRole('button', { name: 'Invite the first user' }),
    ).toBeNull()
  })
})

// ID's account sweep (Root 2026-10-02): the onboarding form accepted "bad@" and said "User
// onboarded.". It now holds an address to the sign-in field's own rule, and a refusal from
// the engine reads in the form in the engine's words.
describe('PeopleTab — onboarding checks the address', () => {
  async function openOnboard() {
    const user = userEvent.setup()
    wrap(<PeopleTab />)
    await user.click(await screen.findByRole('button', { name: 'Invite' }))
    const dialog = await screen.findByRole('dialog')
    return { user, dialog }
  }

  it('an address the sign-in refuses is refused here too: "bad@" is not onboarded', async () => {
    const { user, dialog } = await openOnboard()
    await user.type(within(dialog).getByLabelText(/^Email/), 'bad@')
    expect(
      within(dialog).getByText('Enter a valid email address.'),
    ).toBeInTheDocument()
    expect(
      within(dialog).getByRole('button', { name: 'Create invitation' }),
    ).toBeDisabled()
    expect(api.onboard).not.toHaveBeenCalled()
  })

  it('an internal-domain address the engine accepts is not refused here', async () => {
    const { user, dialog } = await openOnboard()
    await user.type(
      within(dialog).getByLabelText(/^Email/),
      'ops@corp.internal',
    )
    expect(
      within(dialog).queryByText('Enter a valid email address.'),
    ).toBeNull()
    expect(
      within(dialog).getByRole('button', { name: 'Create invitation' }),
    ).toBeEnabled()
  })

  // ID's account sweep, P2: the Users table showed only the administrator after an
  // onboarding until the page was left.
  it('a new account appears in the Users table without leaving the page', async () => {
    api.onboard.mockResolvedValue({ user: { id: 'u-new' } })
    const { user, dialog } = await openOnboard()
    expect(api.listMembers).toHaveBeenCalledTimes(1)
    api.listMembers.mockResolvedValue({
      items: [
        {
          ...member,
          user_id: 'u-new',
          email: 'ana@acme.io',
          display_name: 'Ana',
        },
      ],
      has_more: false,
    })
    await user.type(within(dialog).getByLabelText(/^Email/), 'ana@acme.io')
    await user.click(
      within(dialog).getByRole('button', { name: 'Create invitation' }),
    )
    await waitFor(() => expect(api.listMembers).toHaveBeenCalledTimes(2))
    expect(await screen.findByText('ana@acme.io')).toBeInTheDocument()
  })

  it("the engine's refusal reads in the form, in its own words", async () => {
    api.onboard.mockRejectedValue(
      new ApiError(400, 'invalid_argument', 'email address is not valid'),
    )
    const { user, dialog } = await openOnboard()
    await user.type(within(dialog).getByLabelText(/^Email/), 'ana@acme.io')
    await user.click(
      within(dialog).getByRole('button', { name: 'Create invitation' }),
    )
    expect(await within(dialog).findByRole('alert')).toHaveTextContent(
      'email address is not valid',
    )
  })

  it('a password the administrator sets is still one choice away', async () => {
    api.onboard.mockResolvedValue({ user: { id: 'u-new' } })
    const { user, dialog } = await openOnboard()
    await user.click(within(dialog).getByLabelText('How they sign in'))
    await user.click(
      await screen.findByRole('option', { name: 'Set an initial password' }),
    )
    await user.type(within(dialog).getByLabelText(/^Email/), 'ana@acme.io')
    await user.type(
      within(dialog).getByLabelText(/^Initial password/),
      'long-enough-1',
    )
    await user.click(within(dialog).getByRole('button', { name: 'Add person' }))
    await waitFor(() => expect(api.onboard).toHaveBeenCalledTimes(1))
    expect(api.onboard.mock.calls[0][0]).toMatchObject({
      mode: 'password',
      password: 'long-enough-1',
    })
    expect(api.onboard.mock.calls[0][0].delivery).toBeUndefined()
  })
})

// #471: with no invitation mailer the engine answers a mailed invitation 409, and the
// console offered it anyway, ending in "Something went wrong.". server-info says so, and
// on such a server the invitation is a link the administrator sends themselves.
describe('PeopleTab — email invitations follow the engine', () => {
  const reason = /cannot send mail/i
  const invite = {
    id: 'i1',
    email: 'grace@acme.io',
    tenant: 't1',
    role: 'member',
    expires_at: '2030-01-01T00:00:00Z',
    created_at: '2026-01-01T00:00:00Z',
  }

  async function openModes() {
    const user = userEvent.setup()
    wrap(<PeopleTab />)
    await user.click(await screen.findByRole('button', { name: 'Invite' }))
    const dialog = await screen.findByRole('dialog')
    await user.click(within(dialog).getByLabelText('How they sign in'))
    const option = await screen.findByRole('option', {
      name: 'Send an email invitation',
    })
    return { user, dialog, option }
  }

  it('without a mailer the invitation is a link, offered first, with the reason', async () => {
    serverInfo.data = { invite_delivery_unavailable: true }
    const user = userEvent.setup()
    wrap(<PeopleTab />)
    await user.click(await screen.findByRole('button', { name: 'Invite' }))
    const dialog = await screen.findByRole('dialog')
    expect(
      within(dialog).getByRole('combobox', { name: 'How they sign in' }),
    ).toHaveTextContent('Create an invitation link')
    expect(within(dialog).queryByLabelText(/^Initial password/)).toBeNull()
    expect(within(dialog).getByText(reason)).toBeInTheDocument()
    await user.click(within(dialog).getByLabelText('How they sign in'))
    expect(
      screen.queryByRole('option', { name: 'Send an email invitation' }),
    ).toBeNull()
  })

  it('with a mailer the invitation is offered and no reason shows', async () => {
    const { dialog, option } = await openModes()
    expect(option).not.toHaveAttribute('aria-disabled')
    expect(within(dialog).queryByText(reason)).toBeNull()
  })

  it('the palette request opens on the invitation with a mailer', async () => {
    wrap(<PeopleTab inviteRequested />)
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).queryByLabelText(/^Initial password/)).toBeNull()
  })

  it('the palette request without a mailer opens on the invitation link', async () => {
    serverInfo.data = { invite_delivery_unavailable: true }
    wrap(<PeopleTab inviteRequested />)
    const dialog = await screen.findByRole('dialog')
    expect(
      within(dialog).getByRole('combobox', { name: 'How they sign in' }),
    ).toHaveTextContent('Create an invitation link')
    expect(within(dialog).queryByLabelText(/^Initial password/)).toBeNull()
  })

  // Journey 2 on a server with no mail: the administrator invites in one step and sends
  // the link themselves; the engine hands its secret to this session once.
  it('without a mailer one step gives the link once, to copy', async () => {
    serverInfo.data = { invite_delivery_unavailable: true }
    api.onboard.mockResolvedValue({
      user: { id: 'u-new', email: 'ana@acme.io' },
      created: true,
      membership: {},
      invite: {
        id: 'i9',
        expires_at: '2030-01-08T00:00:00Z',
        delivery: 'link',
        token: 'olvi_sel_secret',
      },
    })
    const user = userEvent.setup()
    wrap(<PeopleTab />)
    await user.click(await screen.findByRole('button', { name: 'Invite' }))
    const dialog = await screen.findByRole('dialog')
    await user.type(within(dialog).getByLabelText(/^Email/), 'ana@acme.io')
    await user.click(
      within(dialog).getByRole('button', { name: 'Create invitation' }),
    )
    await waitFor(() => expect(api.onboard).toHaveBeenCalledTimes(1))
    expect(api.onboard.mock.calls[0][0]).toMatchObject({
      email: 'ana@acme.io',
      mode: 'invite',
      delivery: 'link',
    })
    expect(api.onboard.mock.calls[0][0].password).toBeUndefined()

    const link = `${window.location.origin}/accept-invite#token=olvi_sel_secret`
    const field = await within(dialog).findByLabelText('Invitation link')
    expect(field).toHaveValue(link)
    expect(field).toHaveAttribute('readonly')
    expect(within(dialog).getByText(/works only once/)).toBeInTheDocument()
    await user.click(within(dialog).getByRole('button', { name: 'Copy link' }))
    expect(await navigator.clipboard.readText()).toBe(link)
    expect(
      await within(dialog).findByRole('button', { name: 'Copied' }),
    ).toBeInTheDocument()
  })

  it('with a mailer the invitation is mailed and no link comes back', async () => {
    api.onboard.mockResolvedValue({
      user: { id: 'u-new', email: 'ana@acme.io' },
      created: true,
      membership: {},
      invite: {
        id: 'i9',
        expires_at: '2030-01-08T00:00:00Z',
        delivery: 'sent',
      },
    })
    const user = userEvent.setup()
    wrap(<PeopleTab />)
    await user.click(await screen.findByRole('button', { name: 'Invite' }))
    const dialog = await screen.findByRole('dialog')
    await user.type(within(dialog).getByLabelText(/^Email/), 'ana@acme.io')
    await user.click(
      within(dialog).getByRole('button', { name: 'Create invitation' }),
    )
    expect(
      await within(dialog).findByText(/An invitation was sent to ana@acme.io/),
    ).toBeInTheDocument()
    expect(api.onboard.mock.calls[0][0].delivery).toBeUndefined()
    expect(within(dialog).queryByLabelText('Invitation link')).toBeNull()
  })

  it("the engine's 409 reads in the form when server-info had not said so", async () => {
    api.onboard.mockRejectedValue(
      new ApiError(
        409,
        'invite_delivery_unavailable',
        'auth: no invitation mailer is configured',
      ),
    )
    const { user, dialog } = await openModes()
    await user.click(
      screen.getByRole('option', { name: 'Send an email invitation' }),
    )
    await user.type(within(dialog).getByLabelText(/^Email/), 'ana@acme.io')
    await user.click(
      within(dialog).getByRole('button', { name: 'Create invitation' }),
    )
    expect(await within(dialog).findByRole('alert')).toHaveTextContent(
      'no invitation mailer is configured',
    )
  })

  it('a pending invitation is mailed again with a mailer, and gets a new link without one', async () => {
    api.listInvites.mockResolvedValue({ items: [invite], has_more: false })
    wrap(<PeopleTab />)
    await screen.findByText(invite.email)
    expect(screen.getByRole('button', { name: 'Resend' })).toBeEnabled()
    expect(screen.queryByRole('button', { name: 'New link' })).toBeNull()

    cleanup()
    serverInfo.data = { invite_delivery_unavailable: true }
    api.resendInvite.mockResolvedValue({
      id: 'i1',
      expires_at: '2030-01-08T00:00:00Z',
      delivery: 'link',
      token: 'olvi_sel_rotated',
    })
    const user = userEvent.setup()
    wrap(<PeopleTab />)
    await screen.findByText(invite.email)
    expect(screen.queryByRole('button', { name: 'Resend' })).toBeNull()
    await user.click(screen.getByRole('button', { name: 'New link' }))
    await waitFor(() => expect(api.resendInvite).toHaveBeenCalledTimes(1))
    expect(api.resendInvite.mock.calls[0]).toEqual(['i1', 'link'])
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByLabelText('Invitation link')).toHaveValue(
      `${window.location.origin}/accept-invite#token=olvi_sel_rotated`,
    )
    expect(
      within(dialog).getByText(/The link made before no longer works/),
    ).toBeInTheDocument()
  })
})

describe('PeopleTab — the TOTP reset names what it shows', () => {
  // A speech user says the text on the button: its name must hold it, in one run
  // (WCAG 2.5.3, axe label-content-name-mismatch), in every language.
  it.each(Object.entries(consoleLocales))('%s', (_language, bundle) => {
    const { totpReset, totpResetLabel } = bundle.members
    expect(totpResetLabel.replace('{{email}}', 'ana@example.com')).toContain(
      totpReset,
    )
  })
})

describe('PeopleTab — find people and suspend them (A4 rows 4.1, 4.7)', () => {
  const bob = {
    ...member,
    user_id: 'u2',
    email: 'bob@acme.io',
    display_name: 'Bob',
    status: 'suspended',
    external_id: undefined,
    sso_only: false,
    role: 'editor',
  }
  const ada = { ...member, external_id: undefined, sso_only: false }

  it('narrows the roster by search and by status', async () => {
    edition.business = true
    api.listMembers.mockResolvedValue({ items: [ada, bob], has_more: false })
    const user = userEvent.setup()
    wrap(<PeopleTab />)
    await screen.findByText(ada.email)
    await user.type(
      screen.getByRole('searchbox', { name: 'Search people' }),
      'bob',
    )
    expect(screen.queryByText(ada.email)).toBeNull()
    expect(screen.getByText(bob.email)).toBeTruthy()
    await user.clear(screen.getByRole('searchbox', { name: 'Search people' }))
    await user.click(screen.getByRole('combobox', { name: 'Filter by status' }))
    await user.click(await screen.findByRole('option', { name: 'Suspended' }))
    expect(screen.queryByText(ada.email)).toBeNull()
    expect(screen.getByText(bob.email)).toBeTruthy()
  })

  it('suspends an active person after confirmation and reactivates a suspended one', async () => {
    edition.business = true
    api.listMembers.mockResolvedValue({ items: [ada, bob], has_more: false })
    api.setMemberSuspended.mockResolvedValue(undefined)
    const user = userEvent.setup()
    wrap(<PeopleTab />)
    await screen.findByText(ada.email)
    await openRowActions(user, ada.email)
    await user.click(
      await screen.findByRole('menuitem', { name: `Suspend ${ada.email}` }),
    )
    await user.click(await screen.findByRole('button', { name: 'Suspend' }))
    await waitFor(() =>
      expect(api.setMemberSuspended).toHaveBeenCalledWith('u1', true),
    )

    await openRowActions(user, bob.email)
    await user.click(
      await screen.findByRole('menuitem', { name: `Reactivate ${bob.email}` }),
    )
    await waitFor(() =>
      expect(api.setMemberSuspended).toHaveBeenCalledWith('u2', false),
    )
  })

  it('never offers to suspend the only owner of the whole organization', async () => {
    edition.business = true
    const owner = {
      ...ada,
      user_id: 'u-owner',
      email: 'owner@acme.io',
      role: 'owner',
      workspace_ids: [],
    }
    const confined = {
      ...owner,
      user_id: 'u-o2',
      email: 'o2@acme.io',
      workspace_ids: ['ws1'],
    }
    api.listMembers.mockResolvedValue({
      items: [owner, confined],
      has_more: false,
    })
    const user = userEvent.setup()
    wrap(<PeopleTab />)
    await openRowActions(user, owner.email)
    expect(
      screen.queryByRole('menuitem', { name: `Suspend ${owner.email}` }),
    ).toBeNull()
    await user.keyboard('{Escape}')
    await openRowActions(user, confined.email)
    expect(
      screen.getByRole('menuitem', { name: `Suspend ${confined.email}` }),
    ).toBeInTheDocument()
  })

  it('offers no suspension in Community', async () => {
    api.listMembers.mockResolvedValue({ items: [ada], has_more: false })
    const user = userEvent.setup()
    wrap(<PeopleTab />)
    await screen.findByText(ada.email)
    await openRowActions(user, ada.email)
    expect(
      screen.queryByRole('menuitem', { name: `Suspend ${ada.email}` }),
    ).toBeNull()
    expect(
      screen.queryByRole('checkbox', { name: 'Select every person shown' }),
    ).toBeNull()
  })
})

describe('PeopleTab — an edition adds ways to add people (A4 row 4.2)', () => {
  it('shows an added action beside Invite, only with its permission', async () => {
    panels.peopleActions = [
      {
        id: 'import',
        permission: 'membership:write',
        Component: () => <button type="button">Import from a file</button>,
      },
    ]
    wrap(<PeopleTab />)
    expect(
      await screen.findByRole('button', { name: 'Import from a file' }),
    ).toBeTruthy()

    cleanup()
    authState.can = (p: string) => p !== 'membership:write'
    wrap(<PeopleTab />)
    await screen.findByText('No active members.')
    expect(
      screen.queryByRole('button', { name: 'Import from a file' }),
    ).toBeNull()
  })
})
