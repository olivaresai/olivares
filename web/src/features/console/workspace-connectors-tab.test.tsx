// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactElement, ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import './i18n'

const { api, authState, filtro } = vi.hoisted(() => ({
  api: {
    listWorkspaces: vi.fn(),
    getWorkspaceByID: vi.fn(),
    listAssignments: vi.fn(),
    createAssignment: vi.fn(),
    updateAssignment: vi.fn(),
    deleteAssignment: vi.fn(),
    listSources: vi.fn(),
    listWsConnectors: vi.fn(),
    createWsConnector: vi.fn(),
    updateWsConnector: vi.fn(),
    deleteWsConnector: vi.fn(),
    listConnectors: vi.fn(),
  },
  filtro: { workspaceId: undefined as string | undefined },
  authState: {
    activeTenant: 't1' as string | null,
    activeRole: 'owner' as string | null,
    isSuperadmin: true,
    principal: { aal: 3 } as { aal?: number } | null,
    can: (_p: string): boolean => true,
  },
}))

vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))
vi.mock('@/lib/hooks/use-workspace-filter', () => ({
  useWorkspaceFilter: () => filtro,
}))
vi.mock('@/features/identity/assurance', () => ({
  AAL: { PASSWORD: 1, MFA: 2, HARDWARE: 3 },
  RequireAssurance: ({ children }: { children: ReactNode }) => <>{children}</>,
}))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, consoleApi: api }
})

import { consoleKeys } from './api'
import { WorkspaceConnectorsTab } from './workspace-connectors-tab'

const emptyList = { items: [], has_more: false }
const workspace = {
  id: 'ws1',
  tenant_id: 't1',
  name: 'Engineering',
  slug: 'eng',
  status: 'active',
  is_default: true,
  created_at: '',
  updated_at: '',
  version: 1,
}

function wrap(ui: ReactElement) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

beforeEach(() => {
  vi.clearAllMocks()
  authState.can = (_p: string) => true
  filtro.workspaceId = undefined
  api.getWorkspaceByID.mockResolvedValue(workspace)
  api.listWorkspaces.mockResolvedValue({ items: [workspace], has_more: false })
  api.listAssignments.mockResolvedValue(emptyList)
  api.listSources.mockResolvedValue({
    sources: [
      { name: 'github', tenant: 't1', enabled: true, status: 'running' },
    ],
  })
  api.listWsConnectors.mockResolvedValue(emptyList)
  api.listConnectors.mockResolvedValue({
    connectors: [
      {
        kind: 'github',
        transport: 'in_process',
        fields_known: true,
        fields: [],
      },
    ],
  })
})

describe('WorkspaceConnectorsTab', () => {
  it('shows forbidden states when read permissions are missing', async () => {
    authState.can = (permission: string) => permission.endsWith(':write')
    wrap(<WorkspaceConnectorsTab />)
    expect(
      await screen.findByText(/connector-assignment read access/i),
    ).toBeInTheDocument()
    expect(
      await screen.findByText(/workspace-connector read access/i),
    ).toBeInTheDocument()
    expect(api.listAssignments).not.toHaveBeenCalled()
    expect(api.listWsConnectors).not.toHaveBeenCalled()
  })

  it('edits an assignment with PUT instead of delete and recreate', async () => {
    api.listAssignments.mockResolvedValue({
      items: [
        {
          id: 'a1',
          connector_name: 'github',
          workspace_ref: 'eng',
          mode: 'rw',
          enabled: true,
          note: 'old',
        },
      ],
      has_more: false,
    })
    api.updateAssignment.mockResolvedValue({
      id: 'a1',
      connector_name: 'github',
      workspace_ref: 'eng',
      mode: 'r',
      enabled: true,
      note: 'read only',
    })
    const user = userEvent.setup()
    wrap(<WorkspaceConnectorsTab />)

    const row = (await screen.findByText('github')).closest('tr')!
    await user.click(within(row).getByRole('button', { name: /^edit$/i }))
    const dialog = await screen.findByRole('dialog')
    await user.clear(within(dialog).getByLabelText(/note/i))
    await user.type(within(dialog).getByLabelText(/note/i), 'read only')
    await user.click(within(dialog).getByRole('button', { name: /save/i }))

    await waitFor(() =>
      expect(api.updateAssignment).toHaveBeenCalledWith(
        'a1',
        expect.objectContaining({
          connector_name: 'github',
          workspace_ref: 'eng',
          note: 'read only',
        }),
      ),
    )
    expect(api.deleteAssignment).not.toHaveBeenCalled()
    expect(api.createAssignment).not.toHaveBeenCalled()
  })

  it('edits a workspace connector with the existing PUT wrapper', async () => {
    api.listWsConnectors.mockResolvedValue({
      items: [
        {
          id: 'wc1',
          name: 'github-eng',
          kind: 'github',
          workspace_ref: 'eng',
          config: { base_url: 'https://api.github.com' },
          secrets: { token: '***' },
          poll_seconds: 60,
          enabled: true,
          note: 'old',
          status: 'pending',
        },
      ],
      has_more: false,
    })
    api.updateWsConnector.mockResolvedValue({
      id: 'wc1',
      name: 'github-eng',
      kind: 'github',
      workspace_ref: 'eng',
      poll_seconds: 60,
      enabled: true,
      note: 'new note',
    })
    const user = userEvent.setup()
    wrap(<WorkspaceConnectorsTab />)

    const row = (await screen.findByText('github-eng')).closest('tr')!
    await user.click(within(row).getByRole('button', { name: /^edit$/i }))
    const dialog = await screen.findByRole('dialog')
    await user.clear(within(dialog).getByLabelText(/note/i))
    await user.type(within(dialog).getByLabelText(/note/i), 'new note')
    await user.click(within(dialog).getByRole('button', { name: /save/i }))

    await waitFor(() =>
      expect(api.updateWsConnector).toHaveBeenCalledWith(
        'wc1',
        expect.objectContaining({
          name: 'github-eng',
          kind: 'github',
          workspace_ref: 'eng',
          note: 'new note',
          secrets: { token: '' },
        }),
      ),
    )
    expect(api.deleteWsConnector).not.toHaveBeenCalled()
    expect(api.createWsConnector).not.toHaveBeenCalled()
  })
})

describe('la referencia del workspace, cuando no cabe en la página', () => {
  // ⛔ EL DEFECTO QUE ESTO FIJA. La referencia se buscaba dentro de `listWorkspaces()`, que
  //    sirve UNA página: con el workspace fuera de ella, `find` devolvía `undefined` y el
  //    `??` caía al **id crudo** como `workspace_ref`. El motor no lo reconoce, la consulta
  //    no casa nada, y la pantalla afirma «no hay conectores» — falso y tranquilizador.
  //
  // ⚠ POR ESO LA PÁGINA DE ESTE TESTIGO NO CONTIENE EL WORKSPACE. Si lo contuviera, el
  //   código viejo también pasaría y la prueba no probaría nada.
  const otro = { ...workspace, id: 'ws-otro', slug: 'otro', is_default: false }

  it('las asignaciones se piden por el SLUG preguntado, no por el id crudo', async () => {
    filtro.workspaceId = 'ws1'
    api.listWorkspaces.mockResolvedValue({ items: [otro], has_more: true })
    api.getWorkspaceByID.mockResolvedValue(workspace)

    wrap(<WorkspaceConnectorsTab />)

    await waitFor(() =>
      expect(
        api.listAssignments,
        'Efecto: el filtro es el slug que el motor conoce, no el id que nadie reconoce',
      ).toHaveBeenCalledWith({ workspace_ref: 'eng' }),
    )
    expect(
      api.getWorkspaceByID,
      'Causa: se pregunta por el workspace en vez de buscarlo en una página',
    ).toHaveBeenCalledWith('ws1')
    // ⛔ Y NUNCA con el id crudo. `toHaveBeenCalledWith` acepta CUALQUIER llamada, así que
    //    sin esta línea el código viejo pasaba: pedía primero con 'ws1' y luego, cuando la
    //    lista llegaba, otra vez con 'eng'. Lo que hay que fijar es que la petición mala
    //    NO SE HAGA, no que la buena acabe haciéndose.
    expect(api.listAssignments).not.toHaveBeenCalledWith({
      workspace_ref: 'ws1',
    })
  })

  // ⛔ LO QUE ESTE PAR FIJA, y que el caso de abajo NO veía. Aquél prueba que `listAssignments`
  //    no se LLAMA, que es la mitad barata: con `enabled: false` React Query no PIDE, pero
  //    SIGUE LEYENDO LA CACHÉ, y la clave caía a `'__all__'` — la misma que usa la vista sin
  //    filtro. Un resolver en rojo tenía entonces dos salidas, y las dos mentían hacia el lado
  //    tranquilizador: «no hay conectores», o las filas de TODOS los workspaces pintadas como
  //    si fueran las de éste. Ninguna de las dos la observa una aserción sobre la llamada.
  it('si el resolver falla, las DOS secciones dicen ERROR y ninguna dice «no hay»', async () => {
    filtro.workspaceId = 'ws1'
    api.listWorkspaces.mockResolvedValue({ items: [otro], has_more: true })
    api.getWorkspaceByID.mockRejectedValue(new Error('no se pudo resolver'))

    wrap(<WorkspaceConnectorsTab />)

    // Efecto: lo que el usuario LEE. `ErrorState` es role="alert"; `EmptyState`, role="status".
    // Se exigen DOS porque el tab monta las dos secciones y las dos dependen del resolver:
    // con una sola aserción, arreglar `AssignmentSection` y olvidar `WsConnectorSection`
    // seguiría pasando en verde.
    await waitFor(async () =>
      expect(
        await screen.findAllByRole('alert'),
        'un fallo del resolver es «no he podido mirar», no «no hay»',
      ).toHaveLength(2),
    )
    expect(screen.queryByRole('status')).not.toBeInTheDocument()
  })

  it('con la caché de «todos» caliente, ninguna sección pinta filas ajenas', async () => {
    filtro.workspaceId = 'ws1'
    api.listWorkspaces.mockResolvedValue({ items: [otro], has_more: true })
    api.getWorkspaceByID.mockRejectedValue(new Error('no se pudo resolver'))

    const qc = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    // Exactamente lo que dejaría una visita previa a la vista SIN filtro, en las DOS claves.
    qc.setQueryData([...consoleKeys.connectorAssignments('t1'), '__all__'], {
      items: [
        {
          id: 'a-ajena',
          connector_name: 'ASIGNACION-AJENA',
          workspace_ref: 'otro',
          mode: 'rw',
          enabled: true,
          note: '',
        },
      ],
      has_more: false,
    })
    qc.setQueryData([...consoleKeys.workspaceConnectors('t1'), '__all__'], {
      items: [
        {
          id: 'c-ajeno',
          name: 'CONECTOR-AJENO',
          kind: 'github',
          workspace_ref: 'otro',
          config: {},
          secrets: {},
          poll_seconds: 60,
          enabled: true,
          note: '',
          status: 'pending',
        },
      ],
      has_more: false,
    })
    render(
      <QueryClientProvider client={qc}>
        <WorkspaceConnectorsTab />
      </QueryClientProvider>,
    )

    await waitFor(() => expect(api.getWorkspaceByID).toHaveBeenCalled())
    expect(
      screen.queryByText('ASIGNACION-AJENA'),
      '⛔ la clave sin resolver no puede compartirse con la de «todos»',
    ).not.toBeInTheDocument()
    expect(screen.queryByText('CONECTOR-AJENO')).not.toBeInTheDocument()

    // ⛔ Y LA CLAVE SE OBSERVA DIRECTAMENTE, no por lo que se pinta. Medido: con los guardas de
    //    render puestos, revertir la clave a `'__all__'` NO enrojece nada — el `ErrorState` sale
    //    antes de que la tabla llegue a leer la caché, así que el guarda TAPA el defecto de la
    //    clave y las dos mitades del arreglo no se distinguen por lo que se ve.
    //
    //    Se mira el número de OBSERVADORES, no la existencia de la entrada: la entrada la
    //    sembramos nosotros tres líneas más arriba, así que «existe» es cierto en los dos casos
    //    y no discrimina nada. Lo que distingue es si la vista SE SUSCRIBE a ella.
    const enTodos = qc
      .getQueryCache()
      .findAll()
      .filter(
        (q) =>
          JSON.stringify(q.queryKey).includes('connectorAssignments') &&
          JSON.stringify(q.queryKey).includes('__all__'),
      )
    expect(
      enTodos,
      'la siembra tiene que seguir ahí, si no el caso no prueba nada',
    ).toHaveLength(1)
    expect(
      enTodos[0].getObserversCount(),
      '⛔ con un workspace preguntado, la vista NO puede suscribirse a la clave de «todos»',
    ).toBe(0)
  })

  it('mientras no se resuelve, NO se consulta con un filtro inventado', async () => {
    filtro.workspaceId = 'ws1'
    api.listWorkspaces.mockResolvedValue({ items: [otro], has_more: true })
    api.getWorkspaceByID.mockRejectedValue(new Error('no se pudo resolver'))

    wrap(<WorkspaceConnectorsTab />)

    await waitFor(() => expect(api.getWorkspaceByID).toHaveBeenCalled())
    expect(
      api.listAssignments,
      '⛔ preguntar con un filtro que no se pudo resolver produce un «no hay» falso',
    ).not.toHaveBeenCalled()
    // Y la MISMA aserción para el segundo consumidor: los cinco casos de este bloque sólo
    // observaban `listAssignments`, así que `WsConnectorSection` podía romperse sin que
    // ninguno enrojeciera.
    expect(api.listWsConnectors).not.toHaveBeenCalled()
  })
})
