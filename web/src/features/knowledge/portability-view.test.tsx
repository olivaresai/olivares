// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// C07-04 — portabilidad de memoria (art. 20), y las dos veces que un «✔» miente: un paquete que
// no lo trae todo, y una importación que sólo entró a medias.
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
}))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', can: () => true }),
}))
vi.mock('@/components/ui/toaster', () => ({
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() },
  Toaster: () => null,
}))

const listaMemoria = vi.fn()
const exportMock = vi.fn()
const importMock = vi.fn()
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  const lista = () => Promise.resolve({ items: [], has_more: false })
  return {
    ...actual,
    fetchMemoryExport: (...a: unknown[]) => exportMock(...a),
    knowledgeApi: {
      ...actual.knowledgeApi,
      importMemory: (...a: unknown[]) => importMock(...a),
      dlpRules: () => Promise.resolve({ items: [] }),
      scans: () => Promise.resolve({ items: [] }),
      listKbs: lista,
      listLineage: lista,
      listPrompts: lista,
      listMemory: (...a: unknown[]) => listaMemoria(...a),
      listDataProducts: lista,
    },
  }
})

const KnowledgeView = (await import('./knowledge-view')).default

function montar() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <KnowledgeView />
    </QueryClientProvider>,
  )
}

async function abrirMemoria(user: ReturnType<typeof userEvent.setup>) {
  montar()
  await user.click(await screen.findByRole('tab', { name: /^Memory$/i }))
}

beforeEach(() => {
  exportMock.mockReset().mockResolvedValue({
    manifest: { count: 12, integrity_excluded: 0 },
    raw: '{}',
  })
  importMock.mockReset().mockResolvedValue({ imported: 5, rejected: [] })
  listaMemoria
    .mockReset()
    .mockResolvedValue({ items: [], has_more: false, integrity_excluded: 0 })
})

// ⚠ La carga es `null` —JSON válido— y no `{}` ni `[]` a propósito: `user.type` interpreta
//   `{`, `}`, `[` y `]` como DESCRIPTORES DE TECLA y aborta con «Expected key descriptor».
//   Escaparlos (`{{`, `[[`) funcionaría, pero una carga sin caracteres especiales deja la celda
//   hablando de lo que prueba en vez de de cómo teclea.
describe('la portabilidad de la memoria', () => {
  it('conserva y reenvía byte por byte el paquete JSONL exportado', async () => {
    const raw = [
      '{"schema":"olivares.memory.v1","count":1,"signature":"sig"}',
      '{"agent_ref":"agent-l9","key":"preference","content":"dark"}',
      '',
    ].join('\n')
    exportMock.mockResolvedValue({
      manifest: { count: 1, integrity_excluded: 0 },
      raw,
    })
    const user = userEvent.setup()
    await abrirMemoria(user)

    await user.click(
      await screen.findByRole('button', { name: /Export bundle/i }),
    )
    expect(await screen.findByLabelText(/Bundle to import/i)).toHaveValue(raw)

    await user.click(screen.getByRole('button', { name: /Import bundle/i }))
    expect(importMock).toHaveBeenCalledWith(raw)
  })

  /**
   * ⛔ EL CONTROL QUE MÁS IMPORTA: el manifiesto trae `integrity_excluded` — el motor **DEJA
   * FUERA** las filas que fallan la comprobación de integridad y las cuenta.
   *
   * EL MUTANTE: enseñar sólo `count`. El paquete se presenta como una copia completa cuando no lo
   * es, y quien se lo entrega a un interesado bajo el art. 20 afirma una completitud que el
   * propio manifiesto desmiente en su línea 1.
   */
  it('dice cuántas entradas se DEJARON FUERA del paquete', async () => {
    exportMock.mockResolvedValue({
      manifest: { count: 12, integrity_excluded: 3 },
      raw: '{}',
    })
    const user = userEvent.setup()
    await abrirMemoria(user)
    await user.click(
      await screen.findByRole('button', { name: /Export bundle/i }),
    )
    expect(await screen.findByText(/not a complete copy/i)).toBeInTheDocument()
  })

  /**
   * LA DIRECCIÓN QUE NO DEBE DISPARAR: sin exclusiones NO se avisa, y se dice que no las hubo.
   * Un aviso permanente de incompletitud se deja de leer justo el día que el paquete sí está
   * incompleto.
   */
  it('sin exclusiones no avisa de incompletitud', async () => {
    const user = userEvent.setup()
    await abrirMemoria(user)
    await user.click(
      await screen.findByRole('button', { name: /Export bundle/i }),
    )
    expect(
      await screen.findByText(/Nothing was excluded for integrity/i),
    ).toBeInTheDocument()
    expect(screen.queryByText(/not a complete copy/i)).toBeNull()
  })

  /**
   * ⛔ EL SEGUNDO: la importación puede tener **ÉXITO PARCIAL**. La firma se verifica antes de
   * escribir nada, pero después «a row that fails validation is REJECTED individually and
   * reported» (`portability.go`).
   *
   * EL MUTANTE: enseñar sólo `imported`. Las filas que no entraron desaparecen de la pantalla, y
   * son justo las que hay que arreglar a mano — con su motivo, que el motor sí manda.
   */
  it('una importación parcial dice qué filas NO entraron y por qué', async () => {
    importMock.mockResolvedValue({
      imported: 5,
      rejected: [
        { index: 6, key: 'k6', reason: 'unknown label' },
        { index: 9, key: 'k9', reason: 'quota exceeded' },
      ],
    })
    const user = userEvent.setup()
    await abrirMemoria(user)
    await user.type(await screen.findByLabelText(/Bundle to import/i), 'null')
    await user.click(
      await screen.findByRole('button', { name: /Import bundle/i }),
    )
    expect(await screen.findByText(/2 rows were REJECTED/i)).toBeInTheDocument()
    expect(await screen.findByText(/unknown label/i)).toBeInTheDocument()
    expect(await screen.findByText(/quota exceeded/i)).toBeInTheDocument()
  })

  /** Y sin rechazos NO se habla de éxito parcial: la importación entró entera. */
  it('una importación limpia no habla de rechazos', async () => {
    const user = userEvent.setup()
    await abrirMemoria(user)
    await user.type(await screen.findByLabelText(/Bundle to import/i), 'null')
    await user.click(
      await screen.findByRole('button', { name: /Import bundle/i }),
    )
    expect(await screen.findByText(/5 entries imported/i)).toBeInTheDocument()
    expect(screen.queryByText(/REJECTED/i)).toBeNull()
  })
  /**
   * ⛔ DEFECTO VIVO EN LA LISTA QUE YA EXISTÍA: en `GET /memory` las entradas que fallan la
   * comprobación de integridad **se RETIRAN de `items`** y sólo se cuentan en
   * `integrity_excluded` — lo dice el tipo de esta misma consola (`types.ts:351-356`) y el motor
   * (`memory.go:94-98`). La lista no pintaba esa cuenta.
   *
   * EL MUTANTE: no enseñarla. La lista parece completa y no lo es, y **lo que falta es justo lo
   * que más importa**: las entradas cuyo contenido no cuadra con su ancla. Quien audita la
   * memoria de un agente se lleva la cuenta equivocada sin saber que hay algo que mirar.
   */
  it('dice cuántas entradas se RETUVIERON de la lista', async () => {
    listaMemoria.mockResolvedValue({
      items: [],
      has_more: false,
      integrity_excluded: 4,
    })
    const user = userEvent.setup()
    await abrirMemoria(user)
    expect(
      await screen.findByText(/4 entries are NOT in this list/i),
    ).toBeInTheDocument()
  })

  /** LA DIRECCIÓN QUE NO DEBE DISPARAR: sin retenciones no se avisa. */
  it('sin retenciones la lista no avisa', async () => {
    const user = userEvent.setup()
    await abrirMemoria(user)
    expect(screen.queryByText(/are NOT in this list/i)).toBeNull()
  })
})
