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
    listAgents: vi.fn(),
    getAgent: vi.fn(),
    createAgent: vi.fn(),
    updateAgent: vi.fn(),
    deleteAgent: vi.fn(),
    listWorkspaces: vi.fn(),
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
  RequireAssurance: ({ children }: { children: ReactNode }) => <>{children}</>,
}))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, consoleApi: api }
})

import { AgentsTab } from './agents-tab'
import { consoleKeys } from './api'

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
const agent = {
  id: 'a1',
  tenant_id: 't1',
  workspace_id: 'ws1',
  name: 'Build bot',
  kind: 'claude-code',
  external_id: 'ext-1',
  status: 'active',
  created_at: '',
  updated_at: '',
  version: 1,
}

function wrap(
  ui: ReactElement,
  qc = new QueryClient({ defaultOptions: { queries: { retry: false } } }),
) {
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

beforeEach(() => {
  vi.clearAllMocks()
  authState.can = (_p: string) => true
  api.listAgents.mockResolvedValue(emptyList)
  api.listWorkspaces.mockResolvedValue({ items: [workspace], has_more: false })
})

describe('AgentsTab', () => {
  it('shows a forbidden state and does not read without agent read access', async () => {
    authState.can = (permission: string) => permission !== 'agent:read'
    wrap(<AgentsTab />)

    expect(
      await screen.findByText(/need agent read access/i),
    ).toBeInTheDocument()
    expect(api.listAgents).not.toHaveBeenCalled()
  })

  it('renders the empty state', async () => {
    wrap(<AgentsTab />)

    expect(await screen.findByText(/no agents yet/i)).toBeInTheDocument()
    expect(api.listAgents).toHaveBeenCalledWith({ limit: 1000 })
  })

  it('renders a load error with retry', async () => {
    api.listAgents.mockRejectedValue(new Error('boom'))
    wrap(<AgentsTab />)

    expect(await screen.findByRole('alert')).toBeInTheDocument()
  })

  it('creates an agent through the POST wrapper', async () => {
    api.createAgent.mockResolvedValue({
      ...agent,
      id: 'a2',
      name: 'Deploy bot',
    })
    const user = userEvent.setup()
    wrap(<AgentsTab />)

    await user.click(await screen.findByRole('button', { name: /new agent/i }))
    const dialog = await screen.findByRole('dialog')
    await user.type(within(dialog).getByLabelText(/^name/i), 'Deploy bot')
    await user.type(within(dialog).getByLabelText(/^kind/i), 'codex')
    await user.type(within(dialog).getByLabelText(/external id/i), 'codex-prod')
    await user.click(within(dialog).getByRole('button', { name: /new agent/i }))

    await waitFor(() =>
      expect(api.createAgent).toHaveBeenCalledWith(
        expect.objectContaining({
          name: 'Deploy bot',
          kind: 'codex',
          external_id: 'codex-prod',
          status: 'active',
        }),
      ),
    )
  })

  it('deactivates an active agent with PATCH', async () => {
    api.listAgents.mockResolvedValue({ items: [agent], has_more: false })
    api.updateAgent.mockResolvedValue({ ...agent, status: 'inactive' })
    const user = userEvent.setup()
    wrap(<AgentsTab />)

    const row = (await screen.findByText('Build bot')).closest('tr')!
    await user.click(within(row).getByRole('button', { name: /deactivate/i }))
    const dialog = await screen.findByRole('dialog')
    await user.click(
      within(dialog).getByRole('button', { name: /deactivate/i }),
    )

    await waitFor(() =>
      expect(api.updateAgent).toHaveBeenCalledWith(
        'a1',
        expect.objectContaining({ status: 'inactive', name: 'Build bot' }),
      ),
    )
  })
})

describe('el techo se pide y el recorte se declara', () => {
  /** El techo va con la llamada: sin él el store pagina a 100 y el handler publica `has_more`. */
  it('pide el techo real del motor en agentes y workspaces', async () => {
    wrap(<AgentsTab />)
    await waitFor(() => expect(api.listAgents).toHaveBeenCalled())
    expect(api.listAgents).toHaveBeenLastCalledWith({ limit: 1000 })
    expect(api.listWorkspaces).toHaveBeenLastCalledWith({ limit: 1000 })
  })

  /** El aviso, en las dos direcciones y con la cifra CARGADA. */
  it('declara el recorte con has_more y no sin él', async () => {
    api.listAgents.mockResolvedValue({ items: [agent], has_more: true })
    wrap(<AgentsTab />)
    expect(
      await screen.findByText('Loaded 1 agents; there are more'),
    ).toBeVisible()
    // ⛔ CARDINALIDAD EXACTA, no presencia. Dos avisos distintos que dicen lo mismo pasan
    //    cualquier `findByText` —los textos difieren— y la pantalla enseña el recorte DOS veces.
    //    Es la clase del badge doble; la destapó el contraste (F-01) contando 2 aquí.
    expect(screen.getAllByText(/there are more/i)).toHaveLength(1)

    cleanup()
    api.listAgents.mockResolvedValue({ items: [agent], has_more: false })
    wrap(<AgentsTab />)
    await screen.findByText(agent.name)
    expect(screen.queryByText(/Loaded \d+ agents; there are more/i)).toBeNull()
  })
})

// --- el recorte de WORKSPACES: no oculta una fila, impide una operación -------------------

describe('AgentsTab — la lista de workspaces declara su recorte', () => {
  // ⛔ POR QUÉ ESTE AVISO ES DISTINTO DE LOS DEMÁS, y por eso la auditoría lo marcó ALTA:
  //    `workspaces` pedía el techo del almacén y NADIE leía su `has_more`. Un workspace más allá
  //    de la página no desaparece de la pantalla — sale como **id crudo** en la tabla, porque el
  //    mapa no puede resolver su nombre, y **no es elegible** al crear ni al editar un agente,
  //    porque el selector se alimenta de esa misma lista. El recorte bloquea una OPERACIÓN.
  it('con has_more, avisa y nombra las filas CARGADAS', async () => {
    api.listWorkspaces.mockResolvedValue({ items: [workspace], has_more: true })
    wrap(<AgentsTab />)

    // 1, no 1000: se pide el techo y se enseña lo que llegó. Imprimir el techo sería una medida
    // que nadie tomó — el defecto exacto que la auditoría encontró en otra de estas claves.
    expect(
      await screen.findByText('Loaded 1 workspaces; there are more'),
    ).toBeVisible()
  })

  // La otra mitad del par: sin ella, un aviso incondicional pasaría el caso de arriba.
  it('dirección NO disparadora: sin has_more no hay aviso', async () => {
    api.listWorkspaces.mockResolvedValue({
      items: [workspace],
      has_more: false,
    })
    wrap(<AgentsTab />)
    await screen.findByText(/no agents yet/i)

    expect(screen.queryByText(/there are more/i)).not.toBeInTheDocument()
  })

  // ⛔ EL CONTROL DE LA GUARDA (M-01): `has_more` se compara con el BOOLEANO, no por
  //    «verdad-por-conveniencia». Con la comparación laxa, un `has_more` que llegue como la
  //    CADENA "false" —truthy en JavaScript— pinta el aviso: el gate afirmaría un recorte
  //    justo cuando el motor dice que no lo hay. Es la dirección peligrosa: inventar recorte
  //    donde no lo hay entrena al operador a ignorar el aviso.
  it('un has_more que no es booleano NO dispara el aviso', async () => {
    api.listWorkspaces.mockResolvedValue({
      items: [workspace],
      has_more: 'false' as unknown as boolean,
    })
    wrap(<AgentsTab />)
    await screen.findByText(/no agents yet/i)

    expect(screen.queryByText(/there are more/i)).not.toBeInTheDocument()
  })

  // ⛔ ESTE CASO ESTABA MAL Y LO DIJO EL CONTRASTE (A-01). La primera versión montaba con un
  //    QueryClient vacío y una consulta que falla: entonces `data` es `undefined`, así que el
  //    aviso no se pinta **por falta de datos**, no por la guarda de error. Borrar
  //    `!workspaces.error` sobrevivía a ese test 3 de 3 — un caso que pasa por el motivo
  //    equivocado no prueba nada, sólo lo parece.
  //
  //    Lo que hay que construir es la coexistencia: datos TRUNCADOS ya en caché **y** un
  //    refetch que falla. Ahí `data.has_more` es true y `error` está puesto a la vez, que es
  //    exactamente el estado donde la guarda decide — y donde una consulta que falló NO SABE si
  //    sigue habiendo más, así que afirmarlo sería fabricar un hecho desde un dato ausente.
  it('con datos truncados EN CACHÉ y un refetch fallido, no afirma que haya más', async () => {
    const qc = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    qc.setQueryData(consoleKeys.workspaces('t1', { limit: 1000 }), {
      items: [workspace],
      has_more: true,
    })
    api.listWorkspaces.mockRejectedValue(new Error('boom'))
    wrap(<AgentsTab />, qc)

    // Se espera a que el fallo haya ocurrido de verdad: sin esto, la ausencia se mediría antes
    // de que el error exista, que es la otra forma de pasar por el motivo equivocado.
    await waitFor(() => expect(api.listWorkspaces).toHaveBeenCalled())
    await screen.findByText(/no agents yet/i)

    expect(screen.queryByText(/there are more/i)).not.toBeInTheDocument()
  })
})
