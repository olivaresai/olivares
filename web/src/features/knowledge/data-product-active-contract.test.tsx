// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md
//
// cuál contrato RIGE no se deduce de una página: se pregunta.
//
// ⛔ EL DEFECTO QUE ESTO FIJA. La hoja derivaba el contrato activo con
//    `contracts.find((c) => c.status === 'active')` sobre `listContracts`, que es UNA
//    página. Un producto cuyo contrato activo quedara más allá de la página se mostraba
//    como «ningún contrato activo» — una afirmación FALSA sobre lo que gobierna ahora, y
//    en la dirección tranquilizadora: no ver el contrato se leía como no tenerlo.
//
// ⚠ EL ESCENARIO ES EXACTAMENTE ÉSE, y por eso la lista NO trae el activo: si lo trajera,
//   la prueba pasaría también con el código viejo y no probaría nada. El motor responde
//   200 con el contrato o 404 cuando de verdad no hay (`dataproduct.go:897`).
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactNode } from 'react'
import { ApiError } from '@/lib/api/errors'
import './i18n'

vi.mock('@tanstack/react-router', () => ({
  Link: ({ to, children }: { to: string; children: ReactNode }) => (
    <a href={to}>{children}</a>
  ),
  useNavigate: () => () => {},
  useRouterState: () => '/',
}))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', can: () => true }),
}))

const api = vi.hoisted(() => ({
  getDataProduct: vi.fn(),
  dataProductHealth: vi.fn(),
  listContracts: vi.fn(),
  activeContract: vi.fn(),
  listDPEvents: vi.fn(),
}))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return {
    ...actual,
    knowledgeApi: { ...actual.knowledgeApi, ...api },
  }
})

const { DataProductDetailSheet } = await import('./data-product-detail')

const PRODUCTO = {
  id: 'dp1',
  name: 'Ventas',
  status: 'published',
  owner_ref: 'user:admin',
  kb_ref: 'kb1',
  description: 'un producto',
  enforcement_mode: 'advisory',
  freshness_sla_seconds: 3600,
  availability_target: 99,
}
const ACTIVO = {
  id: 'c-activo',
  version: 7,
  status: 'active',
  validation_mode: 'strict',
  completeness_threshold: 95,
  freshness_override_seconds: 600,
}
// La página que ve la consola: contratos ANTIGUOS, ninguno activo.
const PAGINA_SIN_ACTIVO = [
  { id: 'c1', version: 1, status: 'superseded', validation_mode: 'advisory' },
  { id: 'c2', version: 2, status: 'superseded', validation_mode: 'advisory' },
]

function montar() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <DataProductDetailSheet productId="dp1" open onOpenChange={() => {}} />
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  api.getDataProduct.mockReset().mockResolvedValue(PRODUCTO)
  api.dataProductHealth.mockReset().mockResolvedValue({
    overall_health: 'healthy',
    freshness: { status: 'fresh', age_seconds: 60, sla_seconds: 3600 },
    quality: { status: 'ok', score: 99, threshold: 95 },
    kb: { name: 'kb', doc_count: 1, chunk_count: 2 },
    usage: { total: 0 },
  })
  api.listDPEvents.mockReset().mockResolvedValue({ items: [] })
  api.listContracts
    .mockReset()
    .mockResolvedValue({ items: PAGINA_SIN_ACTIVO, has_more: true })
  api.activeContract.mockReset().mockResolvedValue(ACTIVO)
})

describe('el contrato activo', () => {
  it('sale del endpoint exacto aunque la página de contratos no lo traiga', async () => {
    montar()

    expect(
      await screen.findByText(/^v7$/),
      'Efecto: la hoja muestra el contrato que RIGE, no el que cupo en la página',
    ).toBeVisible()
    expect(
      api.activeContract,
      'Causa: se pregunta al motor por el activo, no se busca en la lista',
    ).toHaveBeenCalledWith('dp1')
  })

  it('declara contratos y eventos recortados con las filas cargadas', async () => {
    api.listDPEvents.mockResolvedValue({
      items: [
        {
          id: 'ev1',
          event_type: 'contract_violation',
          severity: 'high',
          occurred_at: '2026-08-28T10:00:00Z',
        },
      ],
      has_more: true,
    })
    montar()

    expect(
      await screen.findByText('Loaded 2 contract versions; there are more'),
    ).toBeVisible()
    expect(
      await screen.findByText('Loaded 1 enforcement events; there are more'),
    ).toBeVisible()
  })

  // ⛔ LOS DOS CASOS ASIMÉTRICOS SON EL CONTRATO, y existen por un defecto que encontró la
  //    auditoría de significado: moviendo AMBOS `has_more` en la misma dirección, intercambiar
  //    `contractsQuery` y `eventsQuery` entre los dos avisos sobrevivía al positivo Y al negativo.
  //    Un par simétrico prueba que hay avisos; no prueba que cada aviso mire SU lista.
  //
  // ⛔ Y SE ESPERA UNA FILA DE CADA CONSULTA, no el título del producto: el título viene de OTRA
  //    llamada, así que esperarlo permite dictaminar «no hay aviso» antes de que estas dos
  //    asienten — una ausencia medida demasiado pronto es indistinguible de una ausencia real.
  const UN_EVENTO = [
    {
      id: 'ev1',
      event_type: 'contract_violation',
      severity: 'high',
      occurred_at: '2026-08-28T10:00:00Z',
    },
  ]

  it('contratos recortados y eventos completos: sólo avisa el de contratos', async () => {
    api.listContracts.mockResolvedValue({
      items: PAGINA_SIN_ACTIVO,
      has_more: true,
    })
    api.listDPEvents.mockResolvedValue({ items: UN_EVENTO, has_more: false })
    montar()

    // Espera a que AMBAS listas hayan pintado: una fila de cada una.
    await screen.findByText('Contract violation')
    expect(
      await screen.findByText('Loaded 2 contract versions; there are more'),
    ).toBeVisible()
    expect(
      screen.queryByText('Loaded 1 enforcement events; there are more'),
    ).toBeNull()
  })

  it('eventos recortados y contratos completos: sólo avisa el de eventos', async () => {
    api.listContracts.mockResolvedValue({
      items: PAGINA_SIN_ACTIVO,
      has_more: false,
    })
    api.listDPEvents.mockResolvedValue({ items: UN_EVENTO, has_more: true })
    montar()

    await screen.findByText('Contract violation')
    expect(
      await screen.findByText('Loaded 1 enforcement events; there are more'),
    ).toBeVisible()
    expect(
      screen.queryByText('Loaded 2 contract versions; there are more'),
    ).toBeNull()
  })

  it('ambas completas: ningún aviso, medido cuando las dos han asentado', async () => {
    api.listContracts.mockResolvedValue({
      items: PAGINA_SIN_ACTIVO,
      has_more: false,
    })
    api.listDPEvents.mockResolvedValue({ items: UN_EVENTO, has_more: false })
    montar()
    await screen.findByText('Contract violation')

    expect(
      screen.queryByText('Loaded 2 contract versions; there are more'),
    ).toBeNull()
    expect(
      screen.queryByText('Loaded 1 enforcement events; there are more'),
    ).toBeNull()
  })

  it('cuando el motor dice 404, la ausencia es un hecho suyo y no una deducción', async () => {
    api.activeContract.mockRejectedValue(
      new ApiError(404, 'not_found', 'not found'),
    )
    montar()

    expect(await screen.findByText(PRODUCTO.name)).toBeVisible()
    expect(screen.queryByText(/^v7$/)).toBeNull()
  })
  it('cuando el error NO es 404, no puede decir «ningún contrato»: no lo sabe', async () => {
    // ⛔ 404 ES UNA RESPUESTA; un 500 no lo es. Pintar «ningún contrato activo» ante un
    //    fallo de lectura afirma la ausencia sin haberla comprobado, y en una ficha de
    //    gobierno esa afirmación se lee como un hecho.
    api.activeContract.mockRejectedValue(new ApiError(500, 'internal', 'boom'))
    montar()

    expect(await screen.findByText(/cannot be determined/i)).toBeVisible()
    expect(
      screen.queryByText(/No active contract/i),
      'La ausencia sólo se afirma cuando el motor la afirma',
    ).toBeNull()
  })
})
