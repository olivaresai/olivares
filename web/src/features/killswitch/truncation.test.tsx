// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Las tres listas del kill switch declaran su propio recorte. Aquí la afirmación por
// omisión es «esto es lo que hay congelado» y «el guardián no ha hecho nada más», que son
// las dos frases con las que alguien decide que puede seguir.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import type { ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import '@/features/_intel'

const toast = vi.hoisted(() => ({
  success: vi.fn(),
  error: vi.fn(),
  warning: vi.fn(),
}))
vi.mock('@/components/ui/toaster', () => ({ toast, Toaster: () => null }))
const authState = vi.hoisted(() => ({
  activeTenant: 't1' as string | null,
  can: (_p: string): boolean => true,
  principal: null as { actor: string; kind: string } | null,
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))
vi.mock('@tanstack/react-router', () => ({
  useRouterState: () => '',
  Link: ({ children, to }: { children: ReactNode; to: string }) => (
    <a href={to}>{children}</a>
  ),
}))

const api = vi.hoisted(() => ({
  list: vi.fn(),
  state: vi.fn(),
  listGuardianRules: vi.fn(),
  listGuardianActions: vi.fn(),
}))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, killswitchApi: { ...actual.killswitchApi, ...api } }
})

import { killswitchKeys } from './api'
import { calmStateFixture } from './fixtures'
import KillswitchView from './killswitch-view'
import './i18n'

function wrap(ui: ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return {
    qc,
    ...render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>),
  }
}

beforeEach(() => {
  vi.clearAllMocks()
  authState.can = () => true
  api.state.mockResolvedValue(calmStateFixture)
  api.list.mockResolvedValue({ items: [], has_more: false })
  api.listGuardianRules.mockResolvedValue({ items: [], has_more: false })
  api.listGuardianActions.mockResolvedValue({ items: [], has_more: false })
})

describe('Paradas — la lista dice cuánto NO se está viendo', () => {
  it('pide el techo real del motor, no el de por defecto', async () => {
    wrap(<KillswitchView />)
    await waitFor(() => expect(api.list).toHaveBeenCalled())
    // FIRES IF: alguien quita el `limit` — el motor pagina a 100 en silencio.
    expect(api.list).toHaveBeenCalledWith({ limit: 1000 })
  })

  it('DECLARA el recorte con la cifra, y dice QUÉ mil son', async () => {
    api.list.mockResolvedValue({ items: [], has_more: true })
    wrap(<KillswitchView />)
    const aviso = await screen.findByText(/Loaded the first/i)
    expect(aviso.textContent).toMatch(/1000/)
    expect(aviso.textContent).not.toMatch(/\b100\b/)
    const title = aviso.getAttribute('title') ?? ''
    // La página va por id, NO por cuándo se levantó la parada.
    expect(title).toMatch(/by id/i)
    expect(title).toMatch(/recent freeze can be missing/i)
    // ⛔ Y la negación es EPISTEMOLÓGICA. La versión anterior decía «lo que no ves no es lo
    //    que no está congelado», y la consulta por defecto trae `active` Y `reenabled`: una
    //    parada omitida puede estar precisamente reactivada.
    expect(title).toMatch(/does not demonstrate/i)
    expect(title).toMatch(/active and reenabled/i)
  })

  it('y NO lo declara cuando no lo hay — el contrafactual en la otra dirección', async () => {
    wrap(<KillswitchView />)
    await waitFor(() => expect(api.list).toHaveBeenCalled())
    expect(screen.queryByText(/Loaded the first/i)).not.toBeInTheDocument()
  })

  it('el aviso no sobrevive a un error posterior', async () => {
    api.list.mockResolvedValue({ items: [], has_more: true })
    const { qc } = wrap(<KillswitchView />)
    expect(await screen.findByText(/Loaded the first/i)).toBeInTheDocument()
    api.list.mockRejectedValue(new Error('boom'))
    await qc.refetchQueries()
    await waitFor(() =>
      expect(screen.queryByText(/Loaded the first/i)).not.toBeInTheDocument(),
    )
  })
})

describe('Guardián — reglas y rastro de contención', () => {
  // No hay pestañas: `GuardianSection` se monta directamente en la vista cuando el
  // principal tiene `governance:guardian:read`. Mi primera versión pulsaba una pestaña
  // inexistente y los dos casos morían por eso, no por el sujeto.

  it('las dos listas piden el techo real', async () => {
    wrap(<KillswitchView />)
    await waitFor(() => expect(api.listGuardianRules).toHaveBeenCalled())
    expect(api.listGuardianRules).toHaveBeenCalledWith({ limit: 1000 })
    expect(api.listGuardianActions).toHaveBeenCalledWith({ limit: 1000 })
  })

  it('el rastro cortado se declara, y dice que no significa «no pasó nada»', async () => {
    api.listGuardianActions.mockResolvedValue({ items: [], has_more: true })
    wrap(<KillswitchView />)
    const aviso = await screen.findByText(/Loaded the first 1000 actions/i)
    const title = aviso.getAttribute('title') ?? ''
    // ⛔ ESTE ASERTO FIJABA UNA FRASE FALSA. Decía «una acción que no ves aquí no es una
    //    acción que no ocurriera» — y el rastro lleva `pending`, `rejected`, `expired` y
    //    `failed`: una fila omitida PUEDE ser justamente una acción que no ocurrió. Lo
    //    devolvió el contraste. La forma correcta es epistemológica: no DEMUESTRA nada.
    expect(title).toMatch(/does not demonstrate that it does not exist/i)
    expect(title).toMatch(/pending, executed, rejected, expired or failed/i)
    // Y dice QUÉ mil son, que es lo que faltaba en los avisos del guardián.
    expect(title).toMatch(/by id, not by `executed_at`/i)
  })
})

describe('Guardián — el aviso de REGLAS, que no tenía testigo propio', () => {
  it('sale con has_more y dice que una regla omitida no está probada como desarmada', async () => {
    api.listGuardianRules.mockResolvedValue({ items: [], has_more: true })
    wrap(<KillswitchView />)
    const aviso = await screen.findByText(/Loaded the first 1000 rules/i)
    const title = aviso.getAttribute('title') ?? ''
    // FIRES IF: el badge de reglas desaparece — hasta ahora sólo tenía mutante NEGATIVO
    //           (que la petición llevara el limit), no uno que lo pintara.
    expect(title).toMatch(/does not demonstrate that it is disarmed/i)
    expect(title).toMatch(/enabled and disabled/i)
  })

  it('y NO sale cuando no hay más — contrafactual', async () => {
    wrap(<KillswitchView />)
    await waitFor(() => expect(api.listGuardianRules).toHaveBeenCalled())
    expect(
      screen.queryByText(/Loaded the first 1000 rules/i),
    ).not.toBeInTheDocument()
  })
})

describe('killswitchKeys — la clave base es PREFIJO de la filtrada', () => {
  it('invalidar la lista alcanza a la consulta viva con params', async () => {
    // La fábrica de esta feature YA usaba la forma correcta; el testigo existe porque el
    // día que alguien la cambie a `?? null`, la invalidación deja de alcanzarla en
    // silencio — que es exactamente lo que me pasó en `security`.
    const qc = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    const viva = killswitchKeys.stops('t1', { limit: 1000 })
    qc.setQueryData(viva, { items: [], has_more: false })
    await qc.invalidateQueries({ queryKey: killswitchKeys.stops('t1') })
    expect(qc.getQueryState(viva)?.isInvalidated).toBe(true)

    // Control POSITIVO de que el aserto discrimina.
    const ajena = killswitchKeys.state('t1')
    qc.setQueryData(ajena, {})
    await qc.invalidateQueries({ queryKey: killswitchKeys.stops('t1') })
    expect(qc.getQueryState(ajena)?.isInvalidated).toBe(false)
  })
})
