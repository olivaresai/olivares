// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md
//
// the unscanned-content verdict cannot be inferred from one page.
//
// The asymmetric contract needs three cases. The rules list is one page:
// `handleListDLPRules` (`modules/knowledge/dlp.go:168`) uses `listQuery(r)`, and the generic
// store returns its default page (`sqlstore/generic.go:28`) with `has_more`.
//   - Finding the permissive rule proves unscanned content is allowed; truncation cannot
//     invalidate positive evidence.
//   - Not finding it proves nothing when rows are missing; it may be beyond the page.
//
// Claiming DENIED in the second case asserts an unverified safe posture. The `sol max`
// review of found this reversed polarity in five governed-data pages.
//
// Render the view: a source probe can approve the ternary, but rendering proves the text
// appears. This is step 4 of `scripts/check-list-truncation-witness.sh`.
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactNode } from 'react'
import './i18n'

vi.mock('@tanstack/react-router', () => ({
  Link: ({ to, children }: { to: string; children: ReactNode }) => (
    <a href={to}>{children}</a>
  ),
  useNavigate: () => () => {},
  useRouterState: () => '/',
  // No RouterProvider in this test: the shared Tabs strip consults useRouter, and the real
  // hook answers undefined here (console-tab-scroll-restoration R2, 2026-09-06).
  useRouter: () => undefined,
}))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', can: () => true }),
}))

const dlpMock = vi.fn()
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  const lista = () => Promise.resolve({ items: [], has_more: false })
  return {
    ...actual,
    knowledgeApi: {
      ...actual.knowledgeApi,
      dlpRules: (...a: unknown[]) => dlpMock(...a),
      listKbs: lista,
      listLineage: lista,
      listPrompts: lista,
      listMemory: lista,
      listDataProducts: lista,
    },
  }
})

const KnowledgeView = (await import('./knowledge-view')).default

async function abrirDlp(sembrar?: (qc: QueryClient) => void) {
  const user = userEvent.setup()
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  sembrar?.(qc)
  render(
    <QueryClientProvider client={qc}>
      <KnowledgeView />
    </QueryClientProvider>,
  )
  await user.click(await screen.findByRole('tab', { name: /^DLP$/i }))
}

const REGLA_PERMISIVA = {
  id: 'r-unscanned',
  class: 'unscanned',
  action: 'allow',
}
const REGLA_CUALQUIERA = { id: 'r-pii', class: 'pii', action: 'block' }

beforeEach(() => {
  dlpMock.mockReset()
})

describe('el veredicto de lo no analizado, frente a una lista recortada', () => {
  it('CASO 1 — recortada y sin la regla permisiva: NO puede decir «denegado»', async () => {
    dlpMock.mockResolvedValue({ items: [REGLA_CUALQUIERA], has_more: true })
    await abrirDlp()

    expect(
      await screen.findByText(/cannot be determined from this page/i),
      'Efecto: con filas que faltan, la ausencia de la regla permisiva no prueba nada',
    ).toBeVisible()
    expect(
      screen.queryByText(/unscanned content is DENIED/i),
      '⛔ afirmar la postura SEGURA sin verificarla es el fallo, no un matiz',
    ).toBeNull()
  })

  it('CASO 2 — la regla aparece: «permitido» aunque la lista esté recortada', async () => {
    dlpMock.mockResolvedValue({ items: [REGLA_PERMISIVA], has_more: true })
    await abrirDlp()

    expect(
      await screen.findByText(/unscanned content is ALLOWED/i),
      'Una prueba POSITIVA no la invalida el recorte: la regla está, y con eso basta',
    ).toBeVisible()
    expect(
      screen.queryByText(/cannot be determined from this page/i),
    ).toBeNull()
  })

  it('CASO 3 — lista completa y sin la regla: entonces sí, «denegado»', async () => {
    dlpMock.mockResolvedValue({ items: [REGLA_CUALQUIERA], has_more: false })
    await abrirDlp()

    expect(
      await screen.findByText(/unscanned content is DENIED/i),
      'Sin filas que falten, la ausencia SÍ es prueba',
    ).toBeVisible()
    expect(
      screen.queryByText(/cannot be determined from this page/i),
    ).toBeNull()
  })
})

describe('el aviso de reemplazo, frente a una lista recortada', () => {
  // ⛔ AQUI LA ASIMETRIA MUERDE AL ESCRIBIR, no al leer. `PUT /dlp/rules` hace upsert por
  //    clase: si la regla existente quedo fuera de la pagina, callar el aviso sustituye una
  //    politica de egreso vigente sin decirlo. El motor no expone consulta por clase, asi
  //    que la unica salida honesta es declarar la duda.
  async function abrirNuevaRegla(clase: string) {
    const user = userEvent.setup()
    await abrirDlp()
    await user.click(
      await screen.findByRole('button', { name: /New DLP rule/i }),
    )
    await user.type(await screen.findByLabelText(/^Class$/i), clase)
  }

  it('CASO 4 — recortada y la clase no está en la página: avisa de que PUEDE reemplazar', async () => {
    dlpMock.mockResolvedValue({ items: [REGLA_CUALQUIERA], has_more: true })
    await abrirNuevaRegla('secretos')

    expect(
      await screen.findByText(/may already exist beyond this page/i),
      'Efecto: no verla en la página no prueba que no exista, y guardar hace upsert igual',
    ).toBeVisible()
  })

  it('CASO 5 — lista completa y clase nueva: no avisa de nada', async () => {
    dlpMock.mockResolvedValue({ items: [REGLA_CUALQUIERA], has_more: false })
    await abrirNuevaRegla('secretos')

    expect(screen.queryByText(/may already exist beyond this page/i)).toBeNull()
    expect(
      screen.queryByText(/Saving REPLACES it/i),
      'Un aviso permanente en cada alta se deja de leer el día que sí reemplaza algo',
    ).toBeNull()
  })
})

describe('un fallo de lectura no es una prueba de ausencia', () => {
  // ⛔ LA PUERTA DE ATRAS. `listaRecortada` exige `!error` a proposito: no declara un
  //    recorte que no puede confirmar. Pero el veredicto lo usaba como unica condicion,
  //    asi que con datos RANCIOS conservados y un refetch fallido devolvia `false` y el
  //    resultado caia a «denegado» — la misma polaridad invertida entrando por otro lado.
  it('CASO 6 — datos recortados en caché y refetch fallido: «no determinable», nunca «denegado»', async () => {
    dlpMock.mockRejectedValue(new Error('la lectura falló'))
    await abrirDlp((qc) => {
      qc.setQueryData(['knowledge', 't1', 'dlp', 'rules'], {
        items: [REGLA_CUALQUIERA],
        has_more: true,
      })
    })

    expect(
      await screen.findByText(/cannot be determined from this page/i),
      'Efecto: no haber podido leer no prueba que la regla permisiva no exista',
    ).toBeVisible()
    expect(
      screen.queryByText(/unscanned content is DENIED/i),
      '⛔ afirmar la postura segura tras un fallo de lectura es el mismo defecto por otra puerta',
    ).toBeNull()
  })
})
