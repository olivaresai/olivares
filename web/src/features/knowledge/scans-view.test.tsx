// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// C07-04 — los escaneos de descubrimiento, y el cero que significa dos cosas opuestas.
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { act, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import {
  onlineManager,
  QueryClient,
  QueryClientProvider,
} from '@tanstack/react-query'
import type { ReactNode } from 'react'
import './i18n'

vi.mock('@tanstack/react-router', () => ({
  Link: ({ to, children }: { to: string; children: ReactNode }) => (
    <a href={to}>{children}</a>
  ),
  useNavigate: () => () => {},
  useRouterState: () => '/',
}))
let permisos: (p: string) => boolean = () => true
let activeTenant: string | null = 't1'
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant, can: (p: string) => permisos(p) }),
}))

const scansMock = vi.fn()
const scanSourceMock = vi.fn()
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  const lista = () => Promise.resolve({ items: [], has_more: false })
  return {
    ...actual,
    knowledgeApi: {
      ...actual.knowledgeApi,
      scans: (...a: unknown[]) => scansMock(...a),
      scanSource: (...a: unknown[]) => scanSourceMock(...a),
      dlpRules: () => Promise.resolve({ items: [] }),
      listKbs: lista,
      listLineage: lista,
      listPrompts: lista,
      listMemory: lista,
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

async function abrirEscaneos(user: ReturnType<typeof userEvent.setup>) {
  montar()
  await user.click(await screen.findByRole('tab', { name: /^Scans$/i }))
}

const BASE = {
  id: 's1',
  scope_ref: 'kb-1',
  docs_scanned: 40,
  docs_with_hits: 0,
  redacted_markers: 0,
}

beforeEach(() => {
  permisos = () => true
  activeTenant = 't1'
  onlineManager.setOnline(true)
  scanSourceMock.mockReset().mockResolvedValue({})
  scansMock.mockReset().mockResolvedValue({ items: [] })
})

describe('los escaneos de descubrimiento', () => {
  /**
   * ⛔ EL CONTROL QUE MÁS IMPORTA, y es el hallazgo entero de esta pantalla: **un cero sobre
   * `stored` NO es un corpus limpio.** `basis` (`discovery.go:41-44`) dice sobre qué se miró, y
   * `stored` es «the persisted (post-scrub) form» — lo que QUEDA después de redactar. El dato
   * sensible pudo existir y haber sido eliminado al ingerir.
   *
   * EL MUTANTE: enseñar el cero sin decir sobre qué base se midió. Alguien escribe «escaneamos
   * este corpus y no hay PII» en un informe de auditoría, y esa frase es falsa: lo que se ha
   * demostrado es que la redacción funcionó, no que el dato no estuviera.
   */
  it('un cero sobre lo ALMACENADO no se lee como «no hay dato sensible»', async () => {
    scansMock.mockResolvedValue({
      items: [{ ...BASE, scope_kind: 'kb', basis: 'stored' }],
    })
    const user = userEvent.setup()
    await abrirEscaneos(user)
    expect(
      await screen.findByText(/the redaction worked, NOT that the corpus/i),
    ).toBeInTheDocument()
  })

  /**
   * ⛔ LA OTRA MITAD, y por eso no basta con un aviso genérico: sobre `raw` el mismo cero SÍ dice
   * algo de la fuente — «the pre-redaction body … content not yet minimized».
   *
   * EL MUTANTE: usar el mismo texto para las dos bases. Un aviso que sale siempre no distingue
   * nada y se deja de leer; y aquí, además, negaría un resultado que es válido.
   */
  it('un cero sobre los cuerpos EN CRUDO sí habla de la fuente', async () => {
    scansMock.mockResolvedValue({
      items: [
        {
          ...BASE,
          scope_kind: 'source',
          scope_ref: 'sharepoint',
          basis: 'raw',
        },
      ],
    })
    const user = userEvent.setup()
    await abrirEscaneos(user)
    expect(
      await screen.findByText(/the source itself carried nothing/i),
    ).toBeInTheDocument()
    expect(
      screen.queryByText(/the redaction worked, NOT that the corpus/i),
    ).toBeNull()
  })

  /**
   * ⛔ Y LA CONFUSIÓN DE OPERACIÓN: escanear una fuente **NO la ingiere**
   * (`discovery.go:476-479` — los cuerpos se clasifican en memoria y «are never stored, logged or
   * embedded»). Sin decirlo, «escanear una fuente» se lee como un paso previo a importarla, y
   * nadie lo usaría sobre un repositorio que no quiere copiar — que es justo su mejor uso.
   */
  it('dice que escanear una fuente NO la ingiere', async () => {
    const user = userEvent.setup()
    await abrirEscaneos(user)
    expect(await screen.findByText(/WITHOUT ingesting it/i)).toBeInTheDocument()
  })

  it('lanza el escaneo de una fuente por nombre', async () => {
    const user = userEvent.setup()
    await abrirEscaneos(user)
    await user.type(await screen.findByLabelText(/Source name/i), 'sharepoint')
    await user.click(
      await screen.findByRole('button', { name: /Scan source/i }),
    )
    expect(scanSourceMock).toHaveBeenCalledWith('sharepoint', { tenant: 't1' })
  })

  it('conserva el tenant que inició una mutación pausada antes del transporte', async () => {
    const user = userEvent.setup()
    const qc = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    const tree = () => (
      <QueryClientProvider client={qc}>
        <KnowledgeView />
      </QueryClientProvider>
    )
    const view = render(tree())
    await user.click(await screen.findByRole('tab', { name: /^Scans$/i }))
    await user.type(await screen.findByLabelText(/Source name/i), 'sharepoint')

    onlineManager.setOnline(false)
    await user.click(
      await screen.findByRole('button', { name: /Scan source/i }),
    )
    expect(scanSourceMock).not.toHaveBeenCalled()

    activeTenant = 't2'
    view.rerender(tree())
    act(() => onlineManager.setOnline(true))

    await waitFor(() =>
      expect(scanSourceMock).toHaveBeenCalledWith('sharepoint', {
        tenant: 't1',
      }),
    )
  })

  /** El permiso de escritura es propio (`knowledge:scan:write`), no el de lectura. */
  it('quien sólo LEE no ve el lanzador', async () => {
    permisos = (p: string) => p !== 'knowledge:scan:write'
    const user = userEvent.setup()
    await abrirEscaneos(user)
    expect(
      await screen.findByText(/records what it looked AT/i),
    ).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /Scan source/i })).toBeNull()
  })
})
