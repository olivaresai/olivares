// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md.
//
// EL SUJETO SOBRE EL QUE SE DECIDE, EN LA PANTALLA QUE DECIDE.
//
// Puso en esta cola el PERFIL —de qué a qué— y quedaba la otra mitad: SOBRE QUÉ. La fila
// pintaba `target_id` CRUDO (`bindings-tab.tsx`, celda de operación), un identificador opaco al
// lado de una decisión de seguridad: no dice si la asignación es de lectura o de escritura, ni a
// qué espacio va, ni si está activa.
//
// ⛔ CUATRO RESPUESTAS, Y NINGUNA PUEDE COLAPSARSE EN OTRA:
//   resuelta · sin permiso · no encontrada · no se sabe
//
// Las dos que la tentación funde son «sin permiso» y «no encontrada»: la primera se sabe sin
// mirar el listado; la segunda es una AFIRMACIÓN sobre el estado del mundo que sólo se puede
// hacer con el listado COMPLETO. Y `handleListAssignments` devuelve `has_more` sin drenar el
// cursor (`assignment.go:109`) sobre un repositorio que pagina a 100, así que la lista puede
// venir truncada — la misma trampa que documentó para `guard-postures`.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import type { ReactElement, ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import './i18n'

const { api, authState } = vi.hoisted(() => ({
  api: {
    listBindings: vi.fn(),
    listPostureRequests: vi.fn(),
    listGuardPostures: vi.fn(),
    listAssignments: vi.fn(),
    listWorkspaces: vi.fn(),
    listAgentGroups: vi.fn(),
    listGroups: vi.fn(),
    rbacCatalog: vi.fn(),
    listRoles: vi.fn(),
    listAgents: vi.fn(),
    listResources: vi.fn(),
    resolvePreview: vi.fn(),
  },
  authState: {
    activeTenant: 't1' as string | null,
    activeRole: 'owner' as string | null,
    isSuperadmin: true,
    principal: { aal: 3 } as { aal?: number } | null,
    can: (_p: string): boolean => true,
  },
}))

vi.mock('@tanstack/react-router', () => ({ useNavigate: () => vi.fn() }))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))
vi.mock('@/features/identity/assurance', () => ({
  AAL: { PASSWORD: 1, MFA: 2, HARDWARE: 3 },
  RequireAssurance: ({ children }: { children: ReactNode }) => <>{children}</>,
}))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, consoleApi: api }
})

import { BindingsTab } from './bindings-tab'

const emptyList = { items: [], has_more: false }

/** La petición canónica: relajar una fuente, apuntando a una asignación concreta. */
const PETICION = {
  id: 'pr-1',
  source_type: 'knowledge' as const,
  source_ref: 'kb-prod',
  op: 'assignment_update',
  target_id: 'asg-42',
  reason: 'soporte necesita el catálogo',
  proposer: 'usr-9',
  status: 'pending' as const,
  guard_profile: 'public_only',
}

const ASIGNACION = {
  id: 'asg-42',
  connector_name: 'confluence',
  workspace_ref: 'ws-soporte',
  mode: 'rw' as const,
  enabled: true,
}

function wrap(ui: ReactElement) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

async function fila(): Promise<HTMLElement> {
  wrap(<BindingsTab />)
  const f = await screen.findByText('knowledge:kb-prod')
  const tr = f.closest('tr')
  if (!tr) throw new Error('la fuente no está en una fila')
  return tr as HTMLElement
}

beforeEach(() => {
  vi.clearAllMocks()
  authState.can = (_p: string) => true
  api.listBindings.mockResolvedValue(emptyList)
  api.listPostureRequests.mockResolvedValue({
    items: [PETICION],
    has_more: false,
  })
  api.listGuardPostures.mockResolvedValue(emptyList)
  api.listAssignments.mockResolvedValue(emptyList)
  api.listWorkspaces.mockResolvedValue(emptyList)
  api.listAgentGroups.mockResolvedValue(emptyList)
  api.listGroups.mockResolvedValue(emptyList)
  api.listRoles.mockResolvedValue(emptyList)
  api.listAgents.mockResolvedValue(emptyList)
  api.listResources.mockResolvedValue(emptyList)
  api.rbacCatalog.mockResolvedValue({ permissions: [], roles: [] })
})

describe('el target_id, resuelto o dicho', () => {
  it('resuelve conector → espacio, y marca la ESCRITURA', async () => {
    api.listAssignments.mockResolvedValue({
      items: [ASIGNACION],
      has_more: false,
    })
    const txt = (await fila()).textContent ?? ''
    expect(txt).toMatch(/confluence/)
    expect(txt).toMatch(/ws-soporte/)
    // `rw` es el modo que ABRE: se marca distinto de `r`, porque aprobar una relajación sobre
    // una asignación de escritura no es lo mismo que sobre una de lectura.
    expect(txt).toMatch(/Read\/write/i)
  })

  it('⛔ SIN PERMISO no dice «no encontrada», y ni siquiera pregunta', async () => {
    authState.can = (p: string) => p !== 'sourcescope:assignment:read'
    const txt = (await fila()).textContent ?? ''
    // FIRES IF: alguien deja caer el caso sin permiso en la rama del «no encontrada». Es una
    // afirmación sobre el estado del mundo hecha por quien no ha podido mirarlo — y la ruta
    // exige `sourcescope:assignment:read`, que NO es el permiso con el que se monta esta cola.
    expect(txt).toMatch(/lack sourcescope:assignment:read/i)
    expect(txt).not.toMatch(/no matching assignment/i)
    expect(api.listAssignments).not.toHaveBeenCalled()
  })

  it('⛔ una lista TRUNCADA no autoriza a decir «no encontrada»', async () => {
    api.listAssignments.mockResolvedValue({
      items: [{ ...ASIGNACION, id: 'asg-otra' }],
      has_more: true,
    })
    const txt = (await fila()).textContent ?? ''
    // FIRES IF: se ignora `has_more`. Paginar sólo puede fabricar AUSENCIAS falsas —jamás una
    // asignación falsa—, así que la ausencia deja de ser un hecho en cuanto la lista viene
    // truncada. `assignment.go:109` devuelve has_more sin drenar el cursor.
    expect(txt).toMatch(/did not load in full/i)
    expect(txt).not.toMatch(/no matching assignment/i)
  })

  it('⛔ ni una consulta que falló', async () => {
    api.listAssignments.mockRejectedValue(new Error('503'))
    const txt = (await fila()).textContent ?? ''
    expect(txt).toMatch(/did not load in full/i)
    expect(txt).not.toMatch(/no matching assignment/i)
  })

  it('y con la lista COMPLETA sí lo afirma — el contrafactual', async () => {
    api.listAssignments.mockResolvedValue({
      items: [{ ...ASIGNACION, id: 'asg-otra' }],
      has_more: false,
    })
    const txt = (await fila()).textContent ?? ''
    // Si «no se sabe» se pintara siempre, el aprobador perdería el único caso en que la ausencia
    // ES un dato: la asignación que la petición nombra ya no existe.
    expect(txt).toMatch(/no matching assignment/i)
    expect(txt).not.toMatch(/did not load in full/i)
  })

  it('el id CRUDO no desaparece: se resuelva o no', async () => {
    api.listAssignments.mockResolvedValue({
      items: [ASIGNACION],
      has_more: false,
    })
    const txt = (await fila()).textContent ?? ''
    // Resolver no es sustituir: el id es lo que casa con el audit trail y con la petición
    // original, y quitarlo rompería el rastro por hacer la fila más bonita.
    expect(txt).toMatch(/asg-42/)
  })

  it('una asignación DESACTIVADA se dice', async () => {
    api.listAssignments.mockResolvedValue({
      items: [{ ...ASIGNACION, enabled: false }],
      has_more: false,
    })
    expect((await fila()).textContent ?? '').toMatch(/Disabled/i)
  })
})

// ─────────────────────────────────────────────────────────────────────────────────────
// LO QUE EL CONTRASTE CODEX `sol max` TUMBÓ, Y ERA LA PREMISA DE LA UNIDAD.
//
// ⛔ `target_id` NO SIGNIFICA SIEMPRE «ID DE ASIGNACIÓN». El motor no tipa el campo: cada
// operación mete lo suyo y el aplicador lo resuelve en un repositorio distinto —
// `update`/`delete` contra BINDINGS (`posture.go:934-952,993-1029`), `assignment_*` contra
// asignaciones (`posture.go:1097-1152`)—. Mandar cualquiera al índice de asignaciones pinta un
// binding como «ninguna asignación coincide» (falso) y, ante una colisión de ids, como LA
// ASIGNACIÓN EQUIVOCADA, que es peor que no resolver.
//
// ⛔ Y LA FRONTERA DE PERMISO VA EN EL RENDER, no sólo en la consulta: `enabled` evita la llamada
// nueva, pero el consumidor observa la misma entrada de caché, cuya clave no lleva principal.
// Es la MISMA clase que el contraste me devolvió horas antes en el panel de egreso.
describe('lo que el contraste tumbó', () => {
  it('⛔ B · un objetivo que NO es una asignación no se resuelve contra asignaciones', async () => {
    api.listPostureRequests.mockResolvedValue({
      items: [{ ...PETICION, op: 'update', target_id: 'bnd-7' }],
      has_more: false,
    })
    api.listAssignments.mockResolvedValue({
      items: [ASIGNACION],
      has_more: false,
    })
    const txt = (await fila()).textContent ?? ''
    // FIRES IF: alguien vuelve a mandar cualquier target_id al índice de asignaciones. `update`
    // apunta a un BINDING; decir «ninguna asignación coincide» es afirmar sobre una clase que
    // nadie ha buscado.
    expect(txt).toMatch(/this resolver does not apply/i)
    expect(txt).not.toMatch(/no matching assignment/i)
    expect(txt).toMatch(/bnd-7/)
  })

  it('⛔ B · y una COLISIÓN de id entre clases no se pinta como la asignación', async () => {
    api.listPostureRequests.mockResolvedValue({
      items: [{ ...PETICION, op: 'delete', target_id: 'asg-42' }],
      has_more: false,
    })
    api.listAssignments.mockResolvedValue({
      items: [ASIGNACION],
      has_more: false,
    })
    const txt = (await fila()).textContent ?? ''
    // El caso más caro del hallazgo: el id CASA con una asignación real, pero la operación apunta
    // a un binding. Resolverlo enseñaría datos de otro objeto con toda seguridad.
    expect(txt).not.toMatch(/confluence/)
    expect(txt).not.toMatch(/ws-soporte/)
    expect(txt).toMatch(/this resolver does not apply/i)
  })

  it('⛔ A · sin permiso NO se pinta, aunque la CACHÉ ya traiga la asignación', async () => {
    // ⛔ LA CACHÉ SE SIEMBRA, NO SE MOCKEA, y la primera versión de este caso no medía nada por
    //    eso: con el permiso denegado el `enabled` es false, la consulta no corre y `assignment`
    //    llega undefined, así que la rama que se quiere vigilar no se alcanza por ninguno de los
    //    dos caminos. Se estaba probando el `enabled`, no la caché — que es el defecto real.
    //    Lo destapó un mutante que quitaba la guarda del render y NO mataba a nadie.
    //
    //    Sembrando la entrada —clave ['console', tenant, 'sourcescope', 'assignments'], SIN
    //    principal— se reproduce el escenario exacto del hallazgo: el dato que dejó alguien con
    //    permiso, observado por alguien que ya no lo tiene.
    authState.can = (p: string) => p !== 'sourcescope:assignment:read'
    const qc = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    qc.setQueryData(['console', 't1', 'sourcescope', 'assignments'], {
      items: [ASIGNACION],
      has_more: false,
    })
    render(
      <QueryClientProvider client={qc}>
        <BindingsTab />
      </QueryClientProvider>,
    )
    const f = await screen.findByText('knowledge:kb-prod')
    const txt = f.closest('tr')?.textContent ?? ''
    expect(txt).not.toMatch(/confluence/)
    expect(txt).not.toMatch(/ws-soporte/)
    expect(txt).toMatch(/lack sourcescope:assignment:read/i)
  })
})
