// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// La lista de Work nombra a su propietario, y cuando no puede, dice el ref y no miente.
//
// ⛔ POR QUE EXISTE. La captura del 2026-08-26 enseñaba las cinco filas repitiendo
//    `user:01a03ed2-7563-7851-83de-88fe017065fd`. No era un hueco de sembrado —el usuario demo SI
//    tiene `display_name` (medido: 'Administrator' en `/v1/auth/whoami`)— sino que la vista pintaba
//    `owner_kind:owner_ref` en crudo, porque el work item NO TRAE el nombre
//    (`modules/sessions/work_read.go:98` proyecta solo kind y ref).
//
// ⛔ LAS DOS DIRECCIONES, que es lo que hace util a esta celda. Un test que solo comprobara el caso
//    bonito pasaria igual con un `display_name` inventado por defecto. Aqui se afirma tambien la
//    CAIDA: un miembro sin nombre —y un `owner_kind` que no es `user`— tienen que seguir enseñando
//    el ref, porque el ref es cierto siempre y un hueco en blanco no lo es.
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

const CON_NOMBRE = '01a03ed2-7563-7851-83de-88fe017065fd'
const SIN_NOMBRE = '01a03ed2-0000-7851-83de-000000000000'

const consola = vi.hoisted(() => ({ listMembers: vi.fn() }))
vi.mock('@/features/console/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/features/console/api')>()
  return { ...actual, consoleApi: { ...actual.consoleApi, ...consola } }
})

const api = vi.hoisted(() => ({ listWorkItems: vi.fn() }))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, ...api }
})

const { WorkView } = await import('./work-view')

const fila = (id: string, kind: string, ref: string) => ({
  id,
  title: `titulo-${id}`,
  work_kind: 'implementation',
  owner_kind: kind,
  owner_ref: ref,
  status: 'ready',
  priority: 'p2',
})

beforeEach(() => {
  vi.clearAllMocks()
  consola.listMembers.mockResolvedValue({
    items: [
      {
        user_id: CON_NOMBRE,
        email: 'admin@olivares.local',
        display_name: 'Administrator',
        status: 'active',
        sso_only: false,
        role: 'owner',
      },
      // Sin `display_name`: el padron lo declara opcional, asi que este caso EXISTE en produccion.
      {
        user_id: SIN_NOMBRE,
        email: 'nameless@olivares.local',
        status: 'active',
        sso_only: false,
        role: 'viewer',
      },
    ],
    has_more: false,
  })
})

describe('el propietario de un work item se lee', () => {
  it('un usuario del padron se nombra, y su UUID NO se enseña', async () => {
    api.listWorkItems.mockResolvedValue({
      items: [fila('a', 'user', CON_NOMBRE)],
      has_more: false,
    })
    renderIntel(<WorkView />)
    expect(await screen.findByText(/Administrator/)).toBeVisible()
    // La otra mitad: que el nombre aparezca no prueba que el UUID se haya ido.
    expect(screen.queryByText(new RegExp(CON_NOMBRE))).not.toBeInTheDocument()
  })

  it('un usuario SIN display_name cae al ref', async () => {
    api.listWorkItems.mockResolvedValue({
      items: [fila('b', 'user', SIN_NOMBRE)],
      has_more: false,
    })
    renderIntel(<WorkView />)
    expect(await screen.findByText(new RegExp(SIN_NOMBRE))).toBeVisible()
  })

  it('un owner_kind que no es user cae al ref: no se finge cobertura de los tres', async () => {
    api.listWorkItems.mockResolvedValue({
      items: [fila('c', 'agent', 'agent-claude-review-3')],
      has_more: false,
    })
    renderIntel(<WorkView />)
    expect(await screen.findByText(/agent-claude-review-3/)).toBeVisible()
  })
})
