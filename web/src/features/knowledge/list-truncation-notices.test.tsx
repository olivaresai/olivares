// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md
//
// las listas de conocimiento DICEN cuando vienen recortadas.
//
// ⛔ SE MONTA LA VISTA, no se mira el fuente. Un `{false && <ListTruncationBadge …/>}` dejaría el
//    aviso escrito y NO alcanzable, y cualquier sonda de texto lo daría por bueno. Es el paso 4 de
//    la receta de `scripts/check-list-truncation-witness.sh`, y la misma razón por la que el
//    testigo de DLP de monta en vez de leer.
//
// ⛔ EL PAR ES EL PUNTO. Un solo positivo lo pasa un aviso pintado SIEMPRE; sólo la dirección no
//    disparadora demuestra que el aviso sigue a `has_more` y no al azar.
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

const kbsMock = vi.fn()
const lineageMock = vi.fn()
const promptsMock = vi.fn()
const contextPoliciesMock = vi.fn()
const dataProductsMock = vi.fn()
const scansMock = vi.fn()
const dlpRulesMock = vi.fn()
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  const lista = () => Promise.resolve({ items: [], has_more: false })
  return {
    ...actual,
    knowledgeApi: {
      ...actual.knowledgeApi,
      listKbs: (...a: unknown[]) => kbsMock(...a),
      listLineage: (...a: unknown[]) => lineageMock(...a),
      listPrompts: (...a: unknown[]) => promptsMock(...a),
      listMemory: lista,
      listDataProducts: (...a: unknown[]) => dataProductsMock(...a),
      listContextPolicies: (...a: unknown[]) => contextPoliciesMock(...a),
      scans: (...a: unknown[]) => scansMock(...a),
      dlpRules: (...a: unknown[]) => dlpRulesMock(...a),
    },
  }
})

const KnowledgeView = (await import('./knowledge-view')).default

function montar() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <KnowledgeView />
    </QueryClientProvider>,
  )
}

const UNA_KB = { id: 'kb1', name: 'manuales', status: 'ready' }

beforeEach(() => {
  for (const mock of [
    kbsMock,
    lineageMock,
    promptsMock,
    contextPoliciesMock,
    dataProductsMock,
    scansMock,
    dlpRulesMock,
  ]) {
    mock.mockReset().mockResolvedValue({ items: [], has_more: false })
  }
})

describe('la lista de bases de conocimiento declara su recorte', () => {
  it('con has_more, el aviso nombra las filas CARGADAS, no el techo pedido', async () => {
    kbsMock.mockResolvedValue({ items: [UNA_KB], has_more: true })
    montar()
    // 1, no 1000: se pide el techo y se enseña lo que llegó. Interpolar la constante convertiría
    // el aviso en una medida inventada.
    expect(
      await screen.findByText('Loaded 1 knowledge bases; there are more'),
    ).toBeVisible()
  })

  it('dirección NO disparadora: sin has_more no hay aviso', async () => {
    kbsMock.mockResolvedValue({ items: [UNA_KB], has_more: false })
    montar()
    await screen.findByText('manuales')
    expect(screen.queryByText(/there are more/i)).not.toBeInTheDocument()
  })
})

const ROOT_LISTS = [
  {
    name: 'lineage',
    tab: /^Lineage$/i,
    mock: lineageMock,
    item: { id: 'ln1', agent_ref: 'agent:planner' },
    marker: 'agent:planner',
    label: 'Loaded 1 lineage records; there are more',
  },
  {
    name: 'prompts',
    tab: /^Prompts$/i,
    mock: promptsMock,
    item: { id: 'p1', name: 'system-base' },
    marker: 'system-base',
    label: 'Loaded 1 prompts; there are more',
  },
  {
    name: 'context policies',
    tab: /^Context$/i,
    mock: contextPoliciesMock,
    item: {
      id: 'cp1',
      scope_kind: 'agent',
      scope_ref: 'agent:planner',
      strategy: 'summarize',
      max_tokens: 8000,
      redaction_required: true,
    },
    marker: 'agent:planner',
    label: 'Loaded 1 context policies; there are more',
  },
  {
    name: 'scans',
    tab: /^Scans$/i,
    mock: scansMock,
    item: {
      id: 'scan1',
      scope_kind: 'kb',
      scope_ref: 'kb1',
      basis: 'stored',
      docs_scanned: 1,
      docs_with_hits: 1,
      redacted_markers: 0,
    },
    marker: 'kb1',
    label: 'Loaded 1 scans; there are more',
  },
  {
    name: 'DLP rules',
    tab: /^DLP$/i,
    mock: dlpRulesMock,
    item: { id: 'rule1', class: 'pii', action: 'deny' },
    marker: 'pii',
    label: 'Loaded 1 DLP rules; there are more',
  },
  {
    name: 'data products',
    tab: /^Data products$/i,
    mock: dataProductsMock,
    item: {
      id: 'dp1',
      name: 'sales',
      owner_ref: 'user:admin',
      status: 'published',
      quality_score: 90,
      usage_count: 0,
      freshness_sla_seconds: 3600,
      enforcement_mode: 'observe',
    },
    marker: 'sales',
    label: 'Loaded 1 data products; there are more',
  },
] as const

describe.each(ROOT_LISTS)(
  'la lista de $name declara su recorte',
  ({ tab, mock, item, marker, label }) => {
    async function abrir() {
      const user = userEvent.setup()
      montar()
      await user.click(await screen.findByRole('tab', { name: tab }))
    }

    it('con has_more muestra el número de filas cargadas', async () => {
      mock.mockResolvedValue({ items: [item], has_more: true })
      await abrir()
      expect(await screen.findByText(label)).toBeVisible()
    })

    it('sin has_more no muestra el aviso', async () => {
      mock.mockResolvedValue({ items: [item], has_more: false })
      await abrir()
      await screen.findByText(marker)
      await waitFor(() => expect(mock).toHaveBeenCalled())
      expect(screen.queryByText(label)).not.toBeInTheDocument()
    })
  },
)
