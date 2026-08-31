// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Las listas de `/v1/m/sessions` DICEN que vienen recortadas.
//
// ⛔ POR QUE UN AVISO Y NO UN TECHO, que es lo que diferencia a esta familia de la de modelos.
//    `/v1/m/models` pagina por offset y admite `limit` hasta 1000 (el maximo del store), asi que
//    ahi un techo alto COMPLETA la lista. `/v1/m/sessions` pagina por KEYSET: `queryLimit`
//    (`modules/sessions/work_api.go:798-807`) sirve CIEN por omision y responde **400
//    `invalid_command`** por encima de 200. Subir el numero solo agranda la primera pagina — la
//    lista sigue incompleta. Lo unico honesto es que la pantalla lo declare.
//
// ⛔ Y SE MONTA LA VISTA, no se mira el fuente. Un `{false && <ListTruncationBadge …/>}` engana a
//    cualquier sonda de texto: el aviso estaria escrito y no seria ALCANZABLE. Esto lo pinta.
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { renderIntel, screen } from '@/test/intel'
import '@/features/_intel'
import './i18n'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    activeTenant: 't1',
    can: () => true,
    confinedWorkspace: null,
  }),
}))

const api = vi.hoisted(() => ({ listWorkItems: vi.fn() }))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, ...api }
})

const { WorkView } = await import('./work-view')

const page = (n: number, hasMore = false) => ({
  items: Array.from({ length: n }, (_, i) => ({
    id: `id-${i}`,
    title: `t${i}`,
    work_kind: 'task',
    owner_kind: 'agent',
    owner_ref: 'a1',
    status: 'open',
    priority: 'p2',
  })),
  has_more: hasMore,
})

beforeEach(() => {
  vi.clearAllMocks()
  api.listWorkItems.mockResolvedValue(page(0))
})

describe('la lista de work items declara su recorte', () => {
  it('con has_more, el aviso nombra las filas CARGADAS, no el techo pedido', async () => {
    api.listWorkItems.mockResolvedValue(page(17, true))
    renderIntel(<WorkView />)
    // 17, no 100: el llamante pide 100 y lo que se enseña es lo que llegó. Interpolar la
    // constante convertiría el aviso en una medida inventada.
    expect(
      await screen.findByText('Loaded 17 rows; there are more'),
    ).toBeVisible()
  })

  it('direccion NO disparadora: sin has_more no hay aviso', async () => {
    api.listWorkItems.mockResolvedValue(page(17, false))
    renderIntel(<WorkView />)
    await screen.findByText('t0')
    expect(screen.queryByText(/there are more/i)).not.toBeInTheDocument()
  })
})
