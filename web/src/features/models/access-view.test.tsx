// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// C07-04 — acceso a modelos: la pantalla que decide quién puede usar qué, y cuya semántica va en la
// dirección peligrosa. Conceder CONFINA.
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { renderIntel, screen, userEvent } from '@/test/intel'
import '@/features/_intel'
import './i18n'

let permisos: (p: string) => boolean = () => true
let tenantActivo: string | null = 't1'
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    activeTenant: tenantActivo,
    can: (p: string) => permisos(p),
  }),
}))

const accesoMock = vi.fn()
const gruposMock = vi.fn()
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  const lista = () => Promise.resolve({ items: [], has_more: false })
  return {
    ...actual,
    modelsApi: {
      ...actual.modelsApi,
      modelAccess: (...a: unknown[]) => accesoMock(...a),
      modelGroups: (...a: unknown[]) => gruposMock(...a),
      routingPolicies: lista,
      workspaceResidency: lista,
      catalog: () => Promise.resolve({ models: [], capabilities: [] }),
      estate: lista,
      keys: lista,
    },
  }
})

const { ModelsView } = await import('./models-view')

const REGLA = {
  id: 'ma-1',
  subject_kind: 'user',
  subject_ref: 'alice@demo',
  target_kind: 'model',
  target_ref: 'claude-opus-5',
}

async function abrirAcceso(user: ReturnType<typeof userEvent.setup>) {
  renderIntel(<ModelsView />)
  await user.click(await screen.findByRole('tab', { name: /^Access$/i }))
}

beforeEach(() => {
  permisos = () => true
  tenantActivo = 't1'
  accesoMock.mockReset().mockResolvedValue({ items: [REGLA] })
  gruposMock.mockReset().mockResolvedValue({ items: [], has_more: false })
})

describe('las reglas de acceso a modelos', () => {
  it('liga las dos lecturas vivas al inquilino de su clave', async () => {
    const user = userEvent.setup()
    await abrirAcceso(user)
    await screen.findByText('allow')
    expect(accesoMock).toHaveBeenCalledWith({ tenant: 't1' }, { limit: 1000 })
    expect(gruposMock).toHaveBeenCalledWith({
      tenant: 't1',
      query: { limit: 1000 },
    })
  })

  /**
   * ⛔ EL CONTROL QUE MÁS IMPORTA, y es lo contrario de lo que la palabra «conceder» sugiere.
   * `modules/models/modelgovernance.go:416-418`, literal: «a subject NAMED by any allow is CONFINED
   * to its allows (deny-closed), a subject named by NONE is unrestricted».
   *
   * ⇒ Crear el PRIMER allow para alguien le RETIRA el resto del catálogo.
   *
   * EL MUTANTE: quitar el aviso y titular la lista «concesiones». Quien añade «permitir a Alice
   * claude-opus-5» cree que suma y en realidad resta, y el incidente de acceso llega el lunes.
   */
  it('avisa de que un allow CONFINA al sujeto, no sólo le concede', async () => {
    const user = userEvent.setup()
    await abrirAcceso(user)
    expect(
      await screen.findByText(/TAKES AWAY the rest of the catalogue/i),
    ).toBeInTheDocument()
  })

  /**
   * ⛔ EL SEGUNDO: **`effect` vacío es un `allow`**, no «sin efecto» (`:415`, back-compat).
   *
   * EL MUTANTE: pintar el campo tal cual. La fila saldría sin veredicto y una regla que SÍ
   * permite se leería como incompleta — en la pantalla que decide accesos.
   */
  it('una regla sin effect se pinta como allow, no en blanco', async () => {
    const user = userEvent.setup()
    await abrirAcceso(user)
    expect(await screen.findByText('allow')).toBeInTheDocument()
  })

  /**
   * ⛔ EL TERCERO: un `forbid` **RESTA** — anula cualquier allow que nombre al mismo sujeto
   * (forbid-overrides-allow, deny-closed, `:418-419`). No es una fila más de la lista.
   */
  it('un forbid se dice como lo que es: una resta', async () => {
    accesoMock.mockResolvedValue({
      items: [{ ...REGLA, id: 'ma-2', effect: 'forbid' }],
    })
    const user = userEvent.setup()
    await abrirAcceso(user)
    expect(await screen.findByText(/SUBTRACTS/i)).toBeInTheDocument()
  })

  /**
   * Y las dos ausencias restantes: `workspace_ref` vacío es **todo el tenant** y `surfaces` vacío
   * es **todas las superficies**. Leerlas como «ninguno» invierte el alcance de la regla.
   */
  it('los vacíos se dicen como TODO, no como ninguno', async () => {
    const user = userEvent.setup()
    await abrirAcceso(user)
    expect(await screen.findByText(/tenant-wide/i)).toBeInTheDocument()
    expect(await screen.findByText(/all surfaces/i)).toBeInTheDocument()
  })

  /**
   * LA DIRECCIÓN QUE NO DEBE DISPARAR: con `workspace_ref` y `surfaces` puestos NO se dice «todo».
   * Sin esta casilla, una pantalla que dijera siempre «tenant-wide» pasaría la de arriba y
   * ampliaría el alcance de toda regla acotada.
   */
  it('una regla acotada no se lee como global', async () => {
    accesoMock.mockResolvedValue({
      items: [{ ...REGLA, workspace_ref: 'ws-eu', surfaces: ['api'] }],
    })
    const user = userEvent.setup()
    await abrirAcceso(user)
    expect(await screen.findByText(/in ws-eu/i)).toBeInTheDocument()
    expect(screen.queryByText(/tenant-wide/i)).toBeNull()
    expect(screen.queryByText(/all surfaces/i)).toBeNull()
  })

  /**
   * ⛔ EL TECHO SE PIDE Y EL RECORTE SE DICE, y en ESTA pantalla el recorte no es cosmético: la
   * marca de CONFINADO se calcula con las reglas CARGADAS. La dirección del error no es una sola
   * —lo afirmé así y el contraste lo refutó—: falta un `forbid` y parece más ancho; falta un
   * `allow` del mismo sujeto y faltan destinos, o sea parece más estrecho; y un sujeto sin
   * ninguna fila cargada no aparece. El aviso lo dice en las dos direcciones.
   */
  it('pide el techo real del motor en reglas y en grupos', async () => {
    const user = userEvent.setup()
    await abrirAcceso(user)
    expect(accesoMock).toHaveBeenLastCalledWith(
      { tenant: 't1' },
      { limit: 1000 },
    )
  })

  /** El aviso, con la cifra CARGADA y en las dos direcciones. */
  it('declara el recorte con has_more y no sin él', async () => {
    accesoMock.mockResolvedValue({ items: [REGLA], has_more: true })
    const user = userEvent.setup()
    await abrirAcceso(user)
    expect(
      await screen.findByText('Loaded 1 rules; there are more'),
    ).toBeVisible()
  })

  it('sin has_more no hay aviso', async () => {
    accesoMock.mockResolvedValue({ items: [REGLA], has_more: false })
    const user = userEvent.setup()
    await abrirAcceso(user)
    await screen.findByText('user:alice@demo')
    expect(screen.queryByText(/Loaded \d+ rules; there are more/i)).toBeNull()
  })

  /** El permiso de escritura es `models:model-access:admin`, por encima del de rutas. */
  it('sin el permiso de administración se declara sólo lectura', async () => {
    permisos = (p: string) => p !== 'models:model-access:admin'
    const user = userEvent.setup()
    await abrirAcceso(user)
    expect(
      await screen.findByText(/requires models:model-access:admin/i),
    ).toBeInTheDocument()
  })
  /**
   * ⛔ LA CLAVE DE CACHÉ NO NOMBRA AL TENANT, y en esta pantalla eso atribuye reglas de acceso al
   * inquilino equivocado.
   *
   * El contrato está escrito en `lib/api/query.ts`: «Tenant-scoped data MUST include the active
   * tenant id in its key … so switching tenant cache-isolates and refetches cleanly». La petición
   * SÍ va marcada —el cliente pone `X-Olivares-Tenant` (`lib/api/client.ts:213`)—, así que dos
   * inquilinos devuelven respuestas distintas BAJO LA MISMA CLAVE.
   *
   * Y la caché no se limpia al cambiar de inquilino: `app/providers.tsx` sólo vacía el workspace, y
   * `queryClient.clear()` vive en el `logout` (`lib/auth/context.tsx:217`). Con la clave sin
   * inquilino, cambiar de tenant NO cambia la clave, así que **no se vuelve a pedir nada**: la
   * pantalla sigue pintando las reglas del anterior con el nombre del nuevo arriba.
   *
   * EL MUTANTE: devolver la clave literal `['models', 'model-access']`. Esta casilla muere.
   */
  it('al cambiar de tenant no sirve las reglas del anterior', async () => {
    const user = userEvent.setup()
    const vista = renderIntel(<ModelsView />)
    await user.click(await screen.findByRole('tab', { name: /^Access$/i }))
    expect(await screen.findByText('user:alice@demo')).toBeInTheDocument()

    tenantActivo = 't2'
    accesoMock.mockResolvedValue({
      items: [{ ...REGLA, id: 'ma-9', subject_ref: 'bob@otro' }],
    })
    vista.rerender(<ModelsView />)

    expect(await screen.findByText('user:bob@otro')).toBeInTheDocument()
    expect(screen.queryByText('user:alice@demo')).toBeNull()
  })

  /**
   * LA DIRECCIÓN QUE NO DEBE DISPARAR. Sin esta casilla, «arreglar» la de arriba metiendo algo
   * cambiante en la clave —un contador, un reloj— la pondría verde y dejaría la consola pidiendo
   * de nuevo cada lista en cada repintado. Con el MISMO inquilino, un repintado no vuelve a pedir.
   */
  it('con el mismo tenant un repintado no vuelve a pedir', async () => {
    const user = userEvent.setup()
    const vista = renderIntel(<ModelsView />)
    await user.click(await screen.findByRole('tab', { name: /^Access$/i }))
    expect(await screen.findByText('user:alice@demo')).toBeInTheDocument()
    const llamadas = accesoMock.mock.calls.length

    vista.rerender(<ModelsView />)
    await screen.findByText('user:alice@demo')
    expect(accesoMock.mock.calls.length).toBe(llamadas)
  })
})
