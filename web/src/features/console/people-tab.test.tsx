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
  },
  authState: {
    activeTenant: 't1' as string | null,
    activeRole: 'owner' as string | null,
    isSuperadmin: true,
    principal: { aal: 3 } as { aal?: number } | null,
    can: (_p: string, _opts?: unknown): boolean => true,
  },
}))

vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))
vi.mock('@/features/identity/assurance', () => ({
  AAL: { PASSWORD: 1, MFA: 2, HARDWARE: 3 },
  RequireAssurance: ({ children }: { children: ReactNode }) => <>{children}</>,
}))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, consoleApi: api }
})

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

beforeEach(() => {
  vi.clearAllMocks()
  authState.can = (_p: string, _opts?: unknown) => true
  api.listInvites.mockResolvedValue(emptyList)
  api.listMembers.mockResolvedValue(emptyList)
  api.listSuperadmins.mockResolvedValue(emptyList)
})

/**
 * ⛔ UN TESTIGO POR CONSULTA, NO UNO POR PANTALLA. El contraste (F-02) midio que el
 *    trinquete acepta *un aviso cualquiera por feature*: al quitar el badge de `members`,
 *    `people-tab` + `registry.truncation-notice` quedaban en **10/10, rc 0**. Lo reproduje
 *    exactamente antes de escribir esto. Un aviso que puede desaparecer sin poner nada rojo no
 *    esta verificado: esta afirmado — la misma clase que yo mismo exigi cerrar en #2031 (A-09).
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

  it('renders role, groups and status, then disables a member through SCIM', async () => {
    api.listMembers.mockResolvedValue({ items: [member], has_more: false })
    api.setMemberActive.mockResolvedValue({})
    const user = userEvent.setup()
    wrap(<PeopleTab />)

    expect(await screen.findByText('ada@acme.io')).toBeInTheDocument()
    expect(screen.getByText('Ada Lovelace')).toBeInTheDocument()
    expect(screen.getByText('Admin')).toBeInTheDocument()
    expect(screen.getByText('engineering')).toBeInTheDocument()
    expect(screen.getByText('platform')).toBeInTheDocument()
    expect(screen.getByText('Active')).toBeInTheDocument()
    expect(screen.getByText('SSO only')).toBeInTheDocument()

    await user.click(
      screen.getByRole('switch', { name: /disable ada@acme\.io/i }),
    )
    const dialog = await screen.findByRole('dialog', {
      name: /disable member/i,
    })
    expect(
      within(dialog).getByText(/provisioned by an IdP/i),
    ).toBeInTheDocument()
    await user.click(within(dialog).getByRole('button', { name: /^disable$/i }))

    await waitFor(() =>
      expect(api.setMemberActive).toHaveBeenCalledWith('u1', false),
    )
  })
})
