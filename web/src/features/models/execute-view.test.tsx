// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// C07-04 — ejecutar una política de enrutado, que es el único botón de esta consola que GASTA
// dinero contra un proveedor. Las tres cosas que separan un botón responsable de uno peligroso.
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { renderIntel, screen, userEvent } from '@/test/intel'
import '@/features/_intel'
import { ApiError } from '@/lib/api/errors'
import './i18n'

// El conjunto de permisos es MUTABLE por casilla: el motor separa `models:routing:admin` (gastar)
// de `models:routing:write` (editar la política), y la pantalla tiene que separar lo mismo.
let permisos = new Set<string>()
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', can: (p: string) => permisos.has(p) }),
}))

const executeMock = vi.fn()
const POLITICA = {
  id: 'rp-1',
  name: 'coste-primero',
  strategy: 'cost',
  enabled: true,
  required_capabilities: [],
  preferred_providers: [],
  min_context_window: 0,
}

vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  const lista = () => Promise.resolve({ items: [], has_more: false })
  return {
    ...actual,
    modelsApi: {
      ...actual.modelsApi,
      executeRoutingPolicy: (...a: unknown[]) => executeMock(...a),
      routingPolicies: () =>
        Promise.resolve({ items: [POLITICA], has_more: false }),
      workspaceResidency: lista,
      catalog: () => Promise.resolve({ models: [], capabilities: [] }),
      estate: lista,
      keys: lista,
    },
  }
})

const { ModelsView } = await import('./models-view')

async function abrirEnrutado(user: ReturnType<typeof userEvent.setup>) {
  renderIntel(<ModelsView />)
  await user.click(await screen.findByRole('tab', { name: /routing/i }))
}

async function ejecutar(user: ReturnType<typeof userEvent.setup>) {
  await abrirEnrutado(user)
  await user.click(await screen.findByRole('button', { name: /^Execute$/i }))
  await user.type(await screen.findByLabelText(/Input/i), 'hola')
  await user.click(
    await screen.findByRole('button', { name: /Execute and spend/i }),
  )
}

beforeEach(() => {
  permisos = new Set(['models:routing:read', 'models:routing:admin'])
  executeMock.mockReset().mockResolvedValue({
    served: { model: 'claude-opus-5' },
    fallback_used: false,
    refusal: false,
    input_tokens: 10,
    output_tokens: 20,
  })
})

describe('la ejecución de una política de enrutado', () => {
  /**
   * ⛔ EL CONTROL MÁS IMPORTANTE, y es de PERMISO: el motor exige `models:routing:admin` para
   * ejecutar (`api.go:33-37`) y lo razona — «ADMIN-tier … distinct from the read-tier resolve. **A
   * viewer/editor cannot spend against a provider**». La vista gatea todo lo demás con
   * `models:routing:write`.
   *
   * EL MUTANTE: reutilizar el permiso de la pantalla. Un editor —que legítimamente edita
   * políticas— vería un botón de GASTO que el motor le va a negar con un 403. No es sólo una
   * frustración: enseñar un botón de gasto a quien no puede gastar afirma una autoridad que esa
   * persona no tiene, en la única acción de la consola que mueve dinero.
   */
  it('un editor SIN el permiso de administración no ve el botón de gasto', async () => {
    permisos = new Set(['models:routing:read', 'models:routing:write'])
    const user = userEvent.setup()
    await abrirEnrutado(user)
    expect(await screen.findByText('coste-primero')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /^Execute$/i })).toBeNull()
  })

  /** LA DIRECCIÓN QUE NO DEBE DISPARAR: con el permiso correcto, el botón SÍ está. */
  it('con models:routing:admin el botón está', async () => {
    const user = userEvent.setup()
    await abrirEnrutado(user)
    expect(
      await screen.findByRole('button', { name: /^Execute$/i }),
    ).toBeInTheDocument()
  })

  /**
   * ⛔ EL SEGUNDO: **503 no es una caída**. Es el estado deny-closed de fábrica — sin ejecutor
   * cableado «the control plane can resolve a routing decision but never spends against a
   * provider» (`models.go:79-82`).
   *
   * EL MUTANTE: dejarlo caer al manejador genérico. Saldría «Service Unavailable» y alguien se
   * pasaría la tarde buscando una avería que no existe, cuando lo que hay que hacer es
   * aprovisionar un ejecutor. Se clasifica por ESTADO, nunca por el texto del mensaje.
   */
  it('un 503 se dice «no hay ejecutor», no «servicio caído»', async () => {
    executeMock.mockRejectedValue(
      new ApiError(503, 'unwired', 'Service Unavailable'),
    )
    const user = userEvent.setup()
    await ejecutar(user)
    expect(
      await screen.findByText(/This is not an outage/i),
    ).toBeInTheDocument()
  })

  /**
   * ⛔ EL TERCERO: **402/429 es el tope de FinOps denegando el gasto ANTES de llamar al
   * proveedor** (Denial-of-Wallet). Es la puerta de gobierno funcionando.
   *
   * EL MUTANTE: presentarlo como error. Un error invita a reintentar, y reintentar es exactamente
   * lo que el tope acaba de frenar — el usuario empujaría contra la protección creyendo que
   * arregla un fallo.
   */
  it('un 402 se dice como veredicto de presupuesto, no como fallo', async () => {
    executeMock.mockRejectedValue(
      new ApiError(402, 'budget_exceeded', 'Payment Required'),
    )
    const user = userEvent.setup()
    await ejecutar(user)
    expect(
      await screen.findByText(/BEFORE any provider call/i),
    ).toBeInTheDocument()
  })

  /**
   * Y en el camino feliz: la salida NO se persiste (`execute.go:119-120` — al ledger sólo llega el
   * CostSample redactado). Decirlo es la diferencia entre que alguien copie el resultado ahora o
   * lo busque mañana en un sitio donde nunca estuvo.
   */
  it('dice que la salida no se guarda en ningún sitio', async () => {
    const user = userEvent.setup()
    await ejecutar(user)
    expect(await screen.findByText(/stored nowhere/i)).toBeInTheDocument()
  })
})
