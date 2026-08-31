// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// C07-04 — la verificación de integridad de la memoria, y los dos estados que NO se funden.
//
// `POST /memory/verify` (`modules/knowledge/memory_integrity.go:293`) recalcula el hash de cada
// fila viva y lo compara con su ancla en el ledger firmado. La consola no lo llamaba.
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
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

const verifyMock = vi.fn()
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  const lista = () => Promise.resolve({ items: [], has_more: false })
  return {
    ...actual,
    knowledgeApi: {
      ...actual.knowledgeApi,
      verifyMemory: (...a: unknown[]) => verifyMock(...a),
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

async function verificar(user: ReturnType<typeof userEvent.setup>) {
  montar()
  await user.click(await screen.findByRole('tab', { name: /Ledger anchors/i }))
  await user.click(
    await screen.findByRole('button', { name: /Verify memory/i }),
  )
}

const SANO = {
  checked: 10,
  verified: 10,
  content_tampered: 0,
  ledger_mismatch: 0,
  deleted_resurrected: 0,
  unanchored: 0,
  legacy_unanchored: 0,
  entries: [],
  truncated: false,
}

beforeEach(() => {
  verifyMock.mockReset().mockResolvedValue(SANO)
})

describe('la verificación de integridad de la memoria', () => {
  it('se lanza desde la pantalla', async () => {
    const user = userEvent.setup()
    await verificar(user)
    await waitFor(() => expect(verifyMock).toHaveBeenCalled())
    expect(
      await screen.findByText(/Every checked row verified/i),
    ).toBeInTheDocument()
  })

  /**
   * ⛔ EL CONTROL QUE MÁS IMPORTA: `unanchored` (una fila SIN historia en el ledger, es decir
   * forjada) y `legacy_unanchored` (una fila ANTERIOR al anclaje, o sea dato viejo) son
   * estados DISTINTOS y se cuentan por separado.
   *
   * EL MUTANTE: sumarlos en un solo contador. Convierte «esto es de antes del anclaje» en
   * «alguien forjó una fila» — una acusación falsa contra una persona — y, en la otra
   * dirección, esconde una falsificación real entre las filas viejas.
   */
  it('no funde «forjada» con «anterior al anclaje»', async () => {
    verifyMock.mockResolvedValue({
      ...SANO,
      verified: 7,
      unanchored: 1,
      legacy_unanchored: 2,
      entries: [
        { id: 'm1', agent_ref: 'a-1', key: 'k1', status: 'unanchored' },
        { id: 'm2', agent_ref: 'a-2', key: 'k2', status: 'legacy_unanchored' },
      ],
    })
    const user = userEvent.setup()
    await verificar(user)

    // Los dos rótulos existen y son distintos.
    expect(
      await screen.findByText('Unanchored (no ledger history)'),
    ).toBeInTheDocument()
    expect(
      await screen.findByText('Legacy (predates anchoring)'),
    ).toBeInTheDocument()
  })

  /**
   * ⛔ EL SEGUNDO: `truncated` significa que **la lista de detalle está acotada, no que haya
   * pocos problemas** — las CUENTAS sí son completas. Leer una lista corta como «poca cosa» al
   * valorar un incidente subestima su alcance.
   *
   * EL MUTANTE: no pintar el aviso. La pantalla parece completa y no lo es.
   */
  it('dice cuando la lista de detalle está acotada', async () => {
    verifyMock.mockResolvedValue({
      ...SANO,
      verified: 0,
      content_tampered: 500,
      entries: [
        { id: 'm1', agent_ref: 'a-1', key: 'k1', status: 'content_tampered' },
      ],
      truncated: true,
    })
    const user = userEvent.setup()
    await verificar(user)
    expect(
      await screen.findByText(/COUNTS above are complete/i),
    ).toBeInTheDocument()
  })

  /**
   * LA DIRECCIÓN QUE NO DEBE DISPARAR: sin truncar, NO se avisa. Un aviso permanente se
   * convierte en ruido y deja de leerse justo el día que importa.
   */
  it('no avisa de truncado cuando no lo hay', async () => {
    const user = userEvent.setup()
    await verificar(user)
    await waitFor(() => expect(verifyMock).toHaveBeenCalled())
    expect(screen.queryByText(/COUNTS above are complete/i)).toBeNull()
  })
})
