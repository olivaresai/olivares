// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactElement, ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import './i18n'

const { api, authState, navigate } = vi.hoisted(() => ({
  api: {
    listBindings: vi.fn(),
    createBinding: vi.fn(),
    updateBinding: vi.fn(),
    deleteBinding: vi.fn(),
    disableScoping: vi.fn(),
    listPostureRequests: vi.fn(),
    listGuardPostures: vi.fn(),
    approvePostureRequest: vi.fn(),
    rejectPostureRequest: vi.fn(),
    listWorkspaces: vi.fn(),
    listAgentGroups: vi.fn(),
    listGroups: vi.fn(),
    rbacCatalog: vi.fn(),
    listRoles: vi.fn(),
    listAgents: vi.fn(),
    listResources: vi.fn(),
  },
  authState: {
    activeTenant: 't1' as string | null,
    activeRole: 'owner' as string | null,
    isSuperadmin: true,
    principal: { aal: 3 } as { aal?: number } | null,
    can: (_p: string): boolean => true,
  },
  navigate: vi.fn(),
}))

vi.mock('@tanstack/react-router', () => ({ useNavigate: () => navigate }))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))
vi.mock('@/features/identity/assurance', () => ({
  AAL: { PASSWORD: 1, MFA: 2, HARDWARE: 3 },
  RequireAssurance: ({ children }: { children: ReactNode }) => <>{children}</>,
}))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, consoleApi: api }
})

import { BindingsTab } from './bindings-tab'

const emptyList = { items: [], has_more: false }

function wrap(ui: ReactElement) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

async function chooseSource(user: ReturnType<typeof userEvent.setup>) {
  wrap(<BindingsTab />)
  await user.type(screen.getByLabelText(/source reference/i), 'kb-prod')
  await screen.findByText(/no bindings for this source/i)
}

beforeEach(() => {
  vi.clearAllMocks()
  authState.can = (_p: string) => true
  api.listBindings.mockResolvedValue(emptyList)
  api.listPostureRequests.mockResolvedValue(emptyList)
  api.listGuardPostures.mockResolvedValue(emptyList)
  api.listWorkspaces.mockResolvedValue({
    items: [
      {
        id: 'ws1',
        tenant_id: 't1',
        name: 'Engineering',
        slug: 'eng',
        status: 'active',
        is_default: true,
        created_at: '',
        updated_at: '',
        version: 1,
      },
    ],
    has_more: false,
  })
  api.listAgentGroups.mockResolvedValue(emptyList)
  api.listGroups.mockResolvedValue({ groups: [] })
  api.rbacCatalog.mockResolvedValue({
    kinds: [],
    tree_kinds: [],
    permissions: [],
    verbs: [],
    builtin_roles: ['viewer', 'editor'],
    scope_trees: [],
  })
  api.listRoles.mockResolvedValue(emptyList)
  api.listAgents.mockResolvedValue(emptyList)
  api.listResources.mockResolvedValue(emptyList)
})

describe('BindingsTab', () => {
  it('shows a forbidden state and does not read when the actor lacks binding read', async () => {
    authState.can = (permission: string) =>
      permission !== 'sourcescope:binding:read'
    wrap(<BindingsTab />)
    expect(
      await screen.findByText(/need source-scope binding access/i),
    ).toBeInTheDocument()
    expect(api.listBindings).not.toHaveBeenCalled()
  })

  it('renders the empty state after a source is selected', async () => {
    const user = userEvent.setup()
    await chooseSource(user)
    // El `limit` va SIEMPRE: es el techo del store, no un filtro. Sin él el motor pagina a 100
    // y publica un `has_more` que esta pantalla tiraba.
    expect(api.listBindings).toHaveBeenCalledWith({
      source_type: 'mcp',
      source_ref: 'kb-prod',
      limit: 1000,
    })
  })

  it('renders a load error with retry', async () => {
    api.listBindings.mockRejectedValue(new Error('boom'))
    const user = userEvent.setup()
    wrap(<BindingsTab />)
    await user.type(screen.getByLabelText(/source reference/i), 'kb-prod')
    expect(await screen.findByRole('alert')).toBeInTheDocument()
  })

  it('creates a binding through the real create wrapper', async () => {
    api.createBinding.mockResolvedValue({
      kind: 'binding',
      status: 201,
      binding: {
        id: 'b1',
        source_type: 'mcp',
        source_ref: 'kb-prod',
        scope_tree: 'session',
        scope_ref: 'sess-1',
        effect: 'allow',
        enabled: true,
      },
    })
    const user = userEvent.setup()
    await chooseSource(user)

    await user.click(screen.getByRole('button', { name: /new binding/i }))
    const dialog = await screen.findByRole('dialog')
    await user.click(
      within(dialog).getByRole('combobox', { name: /scope axis/i }),
    )
    await user.click(screen.getByRole('option', { name: /session/i }))
    await user.type(
      within(dialog).getByLabelText(/session external id/i),
      'sess-1',
    )
    await user.click(
      within(dialog).getByRole('button', { name: /save binding/i }),
    )

    await waitFor(() =>
      expect(api.createBinding).toHaveBeenCalledWith(
        expect.objectContaining({
          source_type: 'mcp',
          source_ref: 'kb-prod',
          scope_tree: 'session',
          scope_ref: 'sess-1',
          effect: 'allow',
          enabled: true,
        }),
      ),
    )
  })

  it('shows the dual-control queued state on a relaxing update', async () => {
    api.listBindings.mockResolvedValue({
      items: [
        {
          id: 'b1',
          source_type: 'mcp',
          source_ref: 'kb-prod',
          scope_tree: 'session',
          scope_ref: 'sess-1',
          effect: 'allow',
          enabled: true,
        },
      ],
      has_more: false,
    })
    api.updateBinding.mockResolvedValue({
      kind: 'posture_request',
      status: 202,
      posture_request: {
        id: 'pr1',
        source_type: 'mcp',
        source_ref: 'kb-prod',
        op: 'update',
        target_id: 'b1',
        reason: 'allow scope broadened',
        proposer: 'alice',
        status: 'pending',
      },
    })
    const user = userEvent.setup()
    wrap(<BindingsTab />)
    await user.type(screen.getByLabelText(/source reference/i), 'kb-prod')
    const row = (await screen.findAllByText('sess-1'))[0].closest('tr')!
    await user.click(within(row).getByRole('button', { name: /edit/i }))
    const dialog = await screen.findByRole('dialog')
    await user.click(
      within(dialog).getByRole('button', { name: /save binding/i }),
    )

    expect(await screen.findByText(/relaxation queued/i)).toBeInTheDocument()
    expect(screen.queryByText(/binding updated/i)).not.toBeInTheDocument()
  })

  it('deletes a binding through the delete wrapper', async () => {
    api.listBindings.mockResolvedValue({
      items: [
        {
          id: 'b1',
          source_type: 'mcp',
          source_ref: 'kb-prod',
          scope_tree: 'session',
          scope_ref: 'sess-1',
          effect: 'allow',
          enabled: false,
        },
      ],
      has_more: false,
    })
    api.deleteBinding.mockResolvedValue({ kind: 'deleted', status: 204 })
    const user = userEvent.setup()
    wrap(<BindingsTab />)
    await user.type(screen.getByLabelText(/source reference/i), 'kb-prod')
    const row = (await screen.findByText('sess-1')).closest('tr')!
    await user.click(within(row).getByRole('button', { name: /delete/i }))
    const dialog = await screen.findByRole('dialog')
    await user.click(within(dialog).getByRole('button', { name: /delete/i }))

    await waitFor(() => expect(api.deleteBinding).toHaveBeenCalledWith('b1'))
  })

  it('approves a pending posture request from the queue', async () => {
    api.listPostureRequests.mockResolvedValue({
      items: [
        {
          id: 'pr1',
          source_type: 'mcp',
          source_ref: 'kb-prod',
          op: 'delete',
          target_id: 'b1',
          reason: 'forbid deleted',
          proposer: 'alice',
          status: 'pending',
        },
      ],
      has_more: false,
    })
    api.approvePostureRequest.mockResolvedValue({
      id: 'pr1',
      source_type: 'mcp',
      source_ref: 'kb-prod',
      op: 'delete',
      status: 'approved',
    })
    const user = userEvent.setup()
    wrap(<BindingsTab />)
    await user.type(screen.getByLabelText(/source reference/i), 'kb-prod')
    const queueRow = (await screen.findByText(/forbid deleted/i)).closest('tr')!
    await user.click(within(queueRow).getByRole('button', { name: /approve/i }))

    await waitFor(() =>
      expect(api.approvePostureRequest).toHaveBeenCalledWith('pr1'),
    )
  })
})

describe('el techo se pide y el recorte se declara', () => {
  /**
   * ⛔ TESTIGO DE TRANSPORTE de las cuatro listas restantes de esta pantalla. Sin `limit` el
   * repositorio genérico pagina a 100 y los handlers publican un `has_more` que se tiraba.
   * Se mide sobre la LLAMADA, no sobre la clave: es la mitad que de verdad viaja.
   */
  it('las cuatro listas piden el techo del motor', async () => {
    const user = userEvent.setup()
    await chooseSource(user)
    await waitFor(() => expect(api.listPostureRequests).toHaveBeenCalled())
    expect(api.listPostureRequests).toHaveBeenLastCalledWith(
      expect.objectContaining({ limit: 1000 }),
    )
    // Los tres selectores viven en el diálogo de alta, y cada uno se consulta con su eje.
    await user.click(screen.getByRole('button', { name: /new binding/i }))
    const dialog = await screen.findByRole('dialog')
    await user.click(
      within(dialog).getByRole('combobox', { name: /scope axis/i }),
    )
    await user.click(screen.getByRole('option', { name: /^workspace$/i }))
    await waitFor(() => expect(api.listWorkspaces).toHaveBeenCalled())
    expect(api.listWorkspaces).toHaveBeenLastCalledWith({ limit: 1000 })

    // ⛔ Y EL DE AGENTES, que llevaba un 200 puesto a mano: ni pedía lo que el motor da, ni lo
    //    decía cuando se quedaba corto. Sin esta línea el mutante que vuelve al 200 ESCAPA.
    await user.click(
      within(dialog).getByRole('combobox', { name: /scope axis/i }),
    )
    await user.click(screen.getByRole('option', { name: /^Agent$/i }))
    await waitFor(() => expect(api.listAgents).toHaveBeenCalled())
    expect(api.listAgents).toHaveBeenLastCalledWith({
      tenant: 't1',
      limit: 1000,
    })
  })

  /**
   * ⛔ EL AVISO DE LA COLA DE APROBACIÓN no tenía casilla: retirarlo escapaba. Y es la lista con
   * la frase más cara de la pantalla —«no hay peticiones pendientes» es con la que alguien se va
   * a casa—, así que el aviso tiene que estar cuando el motor dice que hay más.
   */
  it('la cola de aprobación declara su recorte', async () => {
    api.listPostureRequests.mockResolvedValue({
      items: [
        {
          id: 'pr1',
          operation: 'update',
          source_type: 'mcp',
          source_ref: 'kb-prod',
          target_id: 'b1',
          status: 'pending',
          reason: 'porque sí',
          requested_by: 'alice',
        },
      ],
      has_more: true,
    })
    const user = userEvent.setup()
    await chooseSource(user)
    expect(
      await screen.findByText('Loaded 1 requests; there are more'),
    ).toBeVisible()
  })

  /** El aviso de la tabla, en las dos direcciones y con la cifra CARGADA. */
  it('la lista de enlaces declara su recorte', async () => {
    api.listBindings.mockResolvedValue({
      items: [
        {
          id: 'b1',
          source_type: 'mcp',
          source_ref: 'kb-prod',
          scope_type: 'workspace',
          scope_ref: 'ws1',
          mode: 'read',
        },
      ],
      has_more: true,
    })
    const user = userEvent.setup()
    wrap(<BindingsTab />)
    await user.type(screen.getByLabelText(/source reference/i), 'kb-prod')
    expect(
      await screen.findByText('Loaded 1 bindings; there are more'),
    ).toBeVisible()
  })

  it('sin has_more no hay aviso', async () => {
    const user = userEvent.setup()
    await chooseSource(user)
    expect(
      screen.queryByText(/Loaded \d+ bindings; there are more/i),
    ).toBeNull()
  })

  /**
   * ⛔ Y EL SELECTOR: aquí el recorte no oculta filas, IMPIDE ELEGIR. El operador que no encuentra
   * su workspace no ve una lista corta: ve una entidad que «no existe».
   */
  /**
   * ⛔ Y CADA SELECTOR MIRA SU CONSULTA. Sin esta tabla, cablear el aviso de grupos a la consulta
   * de workspaces —o al revés— pasaría: los tres avisos son idénticos salvo por el `query`. El
   * montaje lo hace load-bearing: SÓLO la lista bajo prueba devuelve `has_more:true`.
   */
  const SELECTORES = [
    {
      eje: /^Workspace$/i,
      mock: () => api.listWorkspaces,
      aviso: 'The workspace list is incomplete',
    },
    {
      eje: /^Agent-group$/i,
      mock: () => api.listAgentGroups,
      aviso: 'The agent-group list is incomplete',
    },
    {
      eje: /^Agent$/i,
      mock: () => api.listAgents,
      aviso: 'The agent list is incomplete',
    },
  ]
  for (const sel of SELECTORES) {
    it(`el aviso de ${sel.aviso} sale sólo cuando SU lista tiene más`, async () => {
      sel.mock().mockResolvedValue({ items: [], has_more: true })
      const user = userEvent.setup()
      await chooseSource(user)
      await user.click(screen.getByRole('button', { name: /new binding/i }))
      const dialog = await screen.findByRole('dialog')
      await user.click(
        within(dialog).getByRole('combobox', { name: /scope axis/i }),
      )
      await user.click(screen.getByRole('option', { name: sel.eje }))
      expect(await screen.findByText(sel.aviso)).toBeVisible()
      for (const otro of SELECTORES.filter((x) => x.aviso !== sel.aviso)) {
        expect(screen.queryByText(otro.aviso)).toBeNull()
      }
    })
  }

  it('un selector recortado lo dice', async () => {
    api.listWorkspaces.mockResolvedValue({
      items: [
        {
          id: 'ws1',
          tenant_id: 't1',
          name: 'Engineering',
          slug: 'eng',
          status: 'active',
          is_default: true,
          created_at: '',
          updated_at: '',
          version: 1,
        },
      ],
      has_more: true,
    })
    const user = userEvent.setup()
    await chooseSource(user)
    await user.click(screen.getByRole('button', { name: /new binding/i }))
    const dialog = await screen.findByRole('dialog')
    // El selector de workspaces sólo se consulta con ese eje elegido.
    await user.click(
      within(dialog).getByRole('combobox', { name: /scope axis/i }),
    )
    await user.click(screen.getByRole('option', { name: /^workspace$/i }))
    expect(
      await screen.findByText('The workspace list is incomplete'),
    ).toBeVisible()
  })
})
