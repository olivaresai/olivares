// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
//
// las invariantes honestas del panel de caminos de ataque.
//
// Cada celda fija una frase que sería MENTIRA si el código cambiara debajo, y está
// escrita para morir con una mutación concreta. Las que importan son las formas de que
// esta pantalla parezca correcta y engañe en una superficie de seguridad: consultar sin
// sujeto o con el parámetro equivocado (el motor da 400 y parecería un vacío),
// dibujar un camino sin decir que su atribución es la del ESLABÓN MÁS DÉBIL, y leer una
// sensibilidad ausente como «no es sensible».
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { StrictMode, type ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { AttackPath } from './types'

const api = {
  attackReachability: vi.fn(),
  attackEscalation: vi.fn(),
  attackExfil: vi.fn(),
}

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', can: () => true }),
}))
vi.mock('./api', async () => {
  const actual = await vi.importActual<typeof import('./api')>('./api')
  return { ...actual, accessMapApi: { ...actual.accessMapApi, ...api } }
})

const { AttackPathsPanel } = await import('./attack-paths')
const { AccessDetailSheet } = await import('./detail')
const { DRIFT_UNREAD } = await import('./authority')
await import('./i18n')

/** Cada analítica SELLA UNA FILA DE AUDITORÍA, así que no se dispara al seleccionar:
 *  se pide. Todas las celdas que esperan datos pasan por aquí. */
async function pedir() {
  await userEvent.click(
    await screen.findByRole('button', { name: /analyse attack paths/i }),
  )
}

function wrap(node: ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{node}</QueryClientProvider>)
}

/** Cuatro saltos firmes y uno sin atribuir: el motor lo compone a `unknown` porque toma
 *  el ESLABÓN MÁS DÉBIL (`weakestAttribution`). */
const WEAK: AttackPath = {
  kind: 'escalation',
  steps: [
    { node_kind: 'agent', node_id: 'a1', node_name: 'billing-bot' },
    { node_kind: 'role', node_id: 'r9', node_name: 'finance-admin' },
  ],
  attribution: 'unknown',
  min_confidence: 'approximate',
}

/** Sin `max_sensitivity`: el campo es `omitempty`, así que ausente y vacío son los
 *  mismos bytes — y ninguno de los dos afirma que no haya nada sensible. */
const NO_SENS: AttackPath = {
  kind: 'reachability',
  steps: [{ node_kind: 'agent', node_id: 'a2', node_name: 'sync-bot' }],
  attribution: 'firm',
  min_confidence: 'attributed',
}

beforeEach(() => {
  api.attackReachability.mockReset()
  api.attackEscalation.mockReset()
  api.attackExfil.mockReset()
})

describe('attack paths — sin sujeto no se consulta', () => {
  it('no pide NADA al motor cuando no hay sujeto elegido', async () => {
    wrap(<AttackPathsPanel subject={null} />)

    expect(await screen.findByTestId('attack-paths-idle')).toBeInTheDocument()
    // La mutación que mata: quitar la guarda de `subject`. Los handlers devuelven 400 sin
    // su id raíz y ese fallo no se puede dibujar como «no hay caminos».
    expect(api.attackReachability).not.toHaveBeenCalled()
    expect(api.attackEscalation).not.toHaveBeenCalled()
    expect(api.attackExfil).not.toHaveBeenCalled()
  })

  it('un agente pide reachability y escalation, nunca exfil', async () => {
    api.attackReachability.mockResolvedValue({ paths: [] })
    api.attackEscalation.mockResolvedValue({ paths: [] })
    api.attackExfil.mockResolvedValue({ paths: [] })
    wrap(<AttackPathsPanel subject={{ id: 'a1', kind: 'agent' }} />)
    await pedir()

    await waitFor(() =>
      expect(api.attackReachability).toHaveBeenCalledWith('a1'),
    )
    expect(api.attackEscalation).toHaveBeenCalledWith('a1')
    expect(
      api.attackExfil,
      'EXFIL_SUBJECT_CONTRACT: agent selection must not invoke resource-scoped exfil',
    ).not.toHaveBeenCalled()
  })

  it('un recurso pide exfil con su id, nunca los análisis de agente', async () => {
    api.attackExfil.mockResolvedValue({ paths: [] })
    wrap(<AttackPathsPanel subject={{ id: 'r7', kind: 'resource' }} />)
    await pedir()

    await waitFor(() => expect(api.attackExfil).toHaveBeenCalledWith('r7'))
    expect(api.attackReachability).not.toHaveBeenCalled()
    expect(api.attackEscalation).not.toHaveBeenCalled()
  })

  it('una lectura auditada no se reintenta ni se repite al reenfocar', async () => {
    const qc = new QueryClient({
      defaultOptions: {
        queries: {
          retry: 1,
          retryDelay: 0,
          staleTime: 0,
          refetchOnWindowFocus: true,
          refetchOnReconnect: true,
        },
      },
    })
    api.attackExfil.mockRejectedValue(new Error('boom'))
    render(
      <StrictMode>
        <QueryClientProvider client={qc}>
          <AttackPathsPanel subject={{ id: 'r7', kind: 'resource' }} />
        </QueryClientProvider>
      </StrictMode>,
    )

    await pedir()
    expect(await screen.findByRole('alert')).toBeInTheDocument()
    window.dispatchEvent(new Event('focus'))
    window.dispatchEvent(new Event('online'))
    await new Promise((resolve) => setTimeout(resolve, 50))

    expect(
      api.attackExfil,
      'EXFIL_AUDIT_CONTRACT: one deliberate analysis click must produce exactly one audited GET',
    ).toHaveBeenCalledTimes(1)
  })

  it('volver a abrir y pulsar hace una lectura auditada nueva', async () => {
    const qc = new QueryClient()
    api.attackExfil.mockResolvedValue({ paths: [] })
    const first = render(
      <QueryClientProvider client={qc}>
        <AttackPathsPanel subject={{ id: 'r7', kind: 'resource' }} />
      </QueryClientProvider>,
    )
    await pedir()
    await waitFor(() => expect(api.attackExfil).toHaveBeenCalledTimes(1))
    first.unmount()

    render(
      <QueryClientProvider client={qc}>
        <AttackPathsPanel subject={{ id: 'r7', kind: 'resource' }} />
      </QueryClientProvider>,
    )
    await pedir()
    await waitFor(() =>
      expect(
        api.attackExfil,
        'EXFIL_AUDIT_CONTRACT: every deliberate reopen and click must produce one new audited GET',
      ).toHaveBeenCalledTimes(2),
    )
  })
})

describe('attack paths — una lectura auditada se PIDE', () => {
  it('con agente elegido NO consulta hasta que el operador lo pide', async () => {
    api.attackReachability.mockResolvedValue({ paths: [NO_SENS] })
    api.attackEscalation.mockResolvedValue({ paths: [] })
    api.attackExfil.mockResolvedValue({ paths: [] })
    wrap(<AttackPathsPanel subject={{ id: 'a1', kind: 'agent' }} />)

    // Ancla POSITIVA primero: el boton existe, luego la ausencia significa algo.
    await screen.findByRole('button', { name: /analyse attack paths/i })
    // La mutación que mata: montar los análisis al recibir `subject`. Cada consulta sella
    // una fila, así que seleccionar escribiría DOS para un agente o UNA para un recurso
    // y el libro dejaría de distinguir «investigó» de «pasó el ratón».
    expect(api.attackReachability).not.toHaveBeenCalled()
    expect(api.attackEscalation).not.toHaveBeenCalled()
    expect(api.attackExfil).not.toHaveBeenCalled()
  })

  it('con recurso elegido tampoco consulta hasta una petición explícita', async () => {
    api.attackExfil.mockResolvedValue({ paths: [] })
    wrap(<AttackPathsPanel subject={{ id: 'r7', kind: 'resource' }} />)

    // Leave React Query enough time to expose an eager mount. The custom message is
    // evaluated after the effect window so a first-tick absence cannot pass vacuously.
    await new Promise((resolve) => setTimeout(resolve, 50))
    expect(
      api.attackExfil,
      'EXFIL_RESOURCE_IDLE_CONTRACT: resource selection must not auto-run audited exfil',
    ).not.toHaveBeenCalled()
    expect(
      screen.getByRole('button', { name: /analyse attack paths/i }),
    ).toBeInTheDocument()
  })
})

describe('attack paths — la atribución es la del eslabón más débil', () => {
  it('rotula el eslabón más débil y dibuja `unknown` como «sin atribuir»', async () => {
    api.attackReachability.mockResolvedValue({ paths: [] })
    api.attackEscalation.mockResolvedValue({ paths: [WEAK] })
    api.attackExfil.mockResolvedValue({ paths: [] })
    wrap(<AttackPathsPanel subject={{ id: 'a1', kind: 'agent' }} />)
    await pedir()

    expect(await screen.findByText(/weakest link/i)).toBeInTheDocument()
    expect(await screen.findByText('not attributed')).toBeInTheDocument()
  })
})

describe('attack paths — una sensibilidad ausente no es «no sensible»', () => {
  it('dice «not stated» en vez de callarse', async () => {
    api.attackReachability.mockResolvedValue({ paths: [NO_SENS] })
    api.attackEscalation.mockResolvedValue({ paths: [] })
    api.attackExfil.mockResolvedValue({ paths: [] })
    wrap(<AttackPathsPanel subject={{ id: 'a1', kind: 'agent' }} />)
    await pedir()

    expect(await screen.findByText('not stated')).toBeInTheDocument()
  })

  it('separa «ningún camino» de «no se pudo leer»', async () => {
    api.attackReachability.mockRejectedValue(new Error('boom'))
    api.attackEscalation.mockResolvedValue({ paths: [] })
    api.attackExfil.mockResolvedValue({ paths: [] })
    wrap(<AttackPathsPanel subject={{ id: 'a1', kind: 'agent' }} />)
    await pedir()

    const alert = await screen.findByRole('alert')
    expect(alert.textContent).toMatch(/could not be read/i)
    // No dentro de waitFor a propósito: una aserción de ausencia ahí se cumple en el
    // primer tick y pasaría con el defecto puesto.
    expect(alert.textContent).not.toMatch(/No paths found/i)
  })

  it('un 200 sin array paths es ilegible, nunca un cero', async () => {
    api.attackExfil.mockResolvedValue({})
    wrap(<AttackPathsPanel subject={{ id: 'r7', kind: 'resource' }} />)
    await pedir()

    await waitFor(() =>
      expect(
        screen.queryByRole('alert'),
        'EXFIL_SHAPE_CONTRACT: a successful response without a paths array must render unreadable, never empty',
      ).not.toBeNull(),
    )
    expect(screen.queryByText('No paths found.')).not.toBeInTheDocument()
  })

  it('un 200 null es ilegible, nunca un cero ni una excepción de render', async () => {
    api.attackExfil.mockResolvedValue(null)
    wrap(<AttackPathsPanel subject={{ id: 'r7', kind: 'resource' }} />)
    await pedir()

    await waitFor(() =>
      expect(
        screen.queryByRole('alert'),
        'EXFIL_NULL_SHAPE_CONTRACT: a successful null response must render unreadable, never empty or crash',
      ).not.toBeNull(),
    )
    expect(screen.queryByText('No paths found.')).not.toBeInTheDocument()
  })

  it('un camino sin forma contractual es ilegible y no rompe PathRow', async () => {
    api.attackExfil.mockResolvedValue({ paths: [{}] })
    wrap(<AttackPathsPanel subject={{ id: 'r7', kind: 'resource' }} />)
    await pedir()

    await waitFor(() =>
      expect(
        screen.queryByRole('alert'),
        'EXFIL_PATH_SHAPE_CONTRACT: malformed path entries must render unreadable, never empty or crash',
      ).not.toBeNull(),
    )
    expect(screen.queryByText('No paths found.')).not.toBeInTheDocument()
  })

  it('un camino sin ningún eslabón es ilegible, nunca una cadena real', async () => {
    api.attackExfil.mockResolvedValue({
      paths: [
        {
          kind: 'exfil',
          steps: [],
          attribution: 'firm',
          min_confidence: 'attributed',
        },
      ],
    })
    wrap(<AttackPathsPanel subject={{ id: 'r7', kind: 'resource' }} />)
    await pedir()

    await waitFor(() =>
      expect(
        screen.queryByRole('alert'),
        'EXFIL_EMPTY_STEPS_CONTRACT: a path without a chain must render unreadable',
      ).not.toBeNull(),
    )
  })

  it('un camino válido de otro análisis no se presenta bajo el rótulo pedido', async () => {
    api.attackReachability.mockResolvedValue({
      paths: [{ ...NO_SENS, kind: 'exfil' }],
    })
    api.attackEscalation.mockResolvedValue({ paths: [] })
    wrap(<AttackPathsPanel subject={{ id: 'a1', kind: 'agent' }} />)
    await pedir()

    await waitFor(() =>
      expect(
        screen.queryByRole('alert'),
        'EXFIL_KIND_CONTRACT: response paths must match the requested analysis kind',
      ).not.toBeNull(),
    )
    expect(screen.queryByText('sync-bot')).not.toBeInTheDocument()
  })
})

// --- el cableado ---------------------------------------------------------------
//
// Un componente que nadie renderiza pasa TODAS sus celdas de componente. Esta empieza en
// la hoja de detalle y comprueba las dos raíces contractuales: agente para
// reachability/escalation y recurso para exfiltration.

const sheetProps = {
  onClose: () => {},
  // Make the cluster test exercise the actual `!selection.cluster` expansion guard.
  // Without this callback, the button is absent regardless of the guard.
  onExpand: () => {},
  // La constante NOMBRADA del propio módulo: fabricar el objeto a mano fue lo que
  // rompió el typecheck, y dos copias de un tipo son dos sitios donde discrepar.
  drift: DRIFT_UNREAD,
  recheck: { status: 'idle' as const },
  onRecheck: () => {},
  onCheckDrift: () => {},
  canDrift: false,
}

describe('el panel se alcanza desde la hoja de detalle con la raíz correcta', () => {
  it('aparece cuando el nodo elegido es un AGENTE', async () => {
    api.attackReachability.mockResolvedValue({ paths: [NO_SENS] })
    api.attackEscalation.mockResolvedValue({ paths: [] })
    api.attackExfil.mockResolvedValue({ paths: [] })
    wrap(
      <AccessDetailSheet
        {...sheetProps}
        selection={{
          type: 'node',
          id: 'a2',
          kind: 'agent',
          ref: 'sync-bot',
          role: 'origin',
        }}
      />,
    )
    await pedir()

    expect(await screen.findByText('not stated')).toBeInTheDocument()
  })

  it('un recurso alcanza exfil con resource_id', async () => {
    api.attackExfil.mockResolvedValue({ paths: [] })
    wrap(
      <AccessDetailSheet
        {...sheetProps}
        selection={{
          type: 'node',
          id: 'r7',
          kind: 'postgres.table',
          ref: 'kb:invoices',
          role: 'resource',
        }}
      />,
    )

    await pedir()
    await waitFor(() => expect(api.attackExfil).toHaveBeenCalledWith('r7'))
    expect(api.attackReachability).not.toHaveBeenCalled()
    expect(api.attackEscalation).not.toHaveBeenCalled()
  })

  it('un cluster no ofrece exfil porque su id no existe en el motor', async () => {
    wrap(
      <AccessDetailSheet
        {...sheetProps}
        selection={{
          type: 'node',
          id: 'cluster:appdb.public',
          kind: 'postgres.table',
          ref: 'appdb.public (1201)',
          role: 'resource',
          cluster: true,
        }}
      />,
    )

    await screen.findByText('appdb.public (1201)')
    expect(
      screen.queryByRole('button', { name: /expand neighbors/i }),
      'EXFIL_CLUSTER_EXPAND_CONTRACT: synthetic cluster selection must not expose expansion',
    ).toBeNull()
    expect(
      screen.queryByRole('button', { name: /analyse attack paths/i }),
      'EXFIL_CLUSTER_PANEL_CONTRACT: synthetic cluster selection must expose neither expand nor attack-path actions',
    ).toBeNull()
    expect(api.attackExfil).not.toHaveBeenCalled()
  })

  it('un cluster que conserva kind=agent tampoco ofrece análisis', async () => {
    wrap(
      <AccessDetailSheet
        {...sheetProps}
        selection={{
          type: 'node',
          id: 'cluster:agents',
          kind: 'agent',
          ref: 'agents (1201)',
          role: 'origin',
          cluster: true,
        }}
      />,
    )

    await screen.findByText('agents (1201)')
    expect(
      screen.queryByRole('button', { name: /analyse attack paths/i }),
      'EXFIL_AGENT_CLUSTER_CONTRACT: synthetic agent clusters must expose no attack-path action',
    ).toBeNull()
    expect(api.attackReachability).not.toHaveBeenCalled()
    expect(api.attackEscalation).not.toHaveBeenCalled()
  })
})
