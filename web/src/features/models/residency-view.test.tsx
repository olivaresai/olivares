// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// C07-04 — la residencia por workspace, y las dos ausencias que NO significan «no».
//
// Es una decisión de soberanía de datos —dónde se procesa la inferencia y dónde reposa el dato—
// que `modules/models/api.go:118-119` sirve y que sólo era alcanzable por `curl`.
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { renderIntel, screen, userEvent } from '@/test/intel'
import '@/features/_intel'
import './i18n'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', can: () => true }),
}))

const residencyMock = vi.fn()
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  const lista = () => Promise.resolve({ items: [], has_more: false })
  return {
    ...actual,
    modelsApi: {
      ...actual.modelsApi,
      workspaceResidency: (...a: unknown[]) => residencyMock(...a),
      catalog: () => Promise.resolve({ models: [], capabilities: [] }),
      estate: lista,
      routingPolicies: lista,
      keys: lista,
    },
  }
})

const { ModelsView } = await import('./models-view')

async function abrirClaves(user: ReturnType<typeof userEvent.setup>) {
  renderIntel(<ModelsView />)
  await user.click(await screen.findByRole('tab', { name: /keys/i }))
}

beforeEach(() => {
  residencyMock.mockReset().mockResolvedValue({
    items: [
      {
        workspace_ref: 'ws-restringido',
        allowed_geos: ['eu', 'us'],
        default_geo: 'eu',
        workspace_geo: 'us',
      },
    ],
  })
})

describe('la residencia por workspace', () => {
  /**
   * EL CONTROL: la pantalla la pide. Antes de esto, la decisión de dónde se procesan los datos
   * de un workspace no se podía ni consultar desde la consola.
   */
  it('se consulta al abrir la pestaña de claves', async () => {
    const user = userEvent.setup()
    await abrirClaves(user)
    expect(residencyMock).toHaveBeenCalled()
  })

  it('enseña las geografías permitidas cuando las hay', async () => {
    const user = userEvent.setup()
    await abrirClaves(user)
    expect(await screen.findByText(/Allowed: eu, us/i)).toBeInTheDocument()
  })

  /**
   * ⛔ EL CONTROL QUE MÁS IMPORTA, y lo dice el DTO del motor
   * (`modules/models/residency.go:77-79`): `allowed_geos` VACÍO significa **«el proveedor no
   * reporta restricción»**, nunca «denegado».
   *
   * EL MUTANTE: pintar la lista vacía como «ninguna geografía permitida», o dejar el hueco. La
   * pantalla diría lo CONTRARIO de lo que el motor afirma, y lo haría sobre la pregunta donde
   * equivocarse es más caro — un operador leería que un workspace tiene la inferencia bloqueada
   * cuando lo que pasa es que nadie ha declarado restricción.
   */
  it('una lista vacía es «sin restricción», nunca «denegado»', async () => {
    residencyMock.mockResolvedValue({
      items: [{ workspace_ref: 'ws-sin-datos', allowed_geos: [] }],
    })
    const user = userEvent.setup()
    await abrirClaves(user)
    expect(
      await screen.findByText(/reports no restriction/i),
    ).toBeInTheDocument()
    // Y NO se afirma lo contrario en ninguna parte de esa fila.
    expect(screen.queryByText(/Allowed:/i)).toBeNull()
  })

  /**
   * EL SEGUNDO: `default_geo` vacío es «no reportado», no «sin defecto». Pintar un hueco haría
   * indistinguible «no lo sabemos» de «no tiene».
   */
  it('un defecto ausente se dice «no reportado»', async () => {
    residencyMock.mockResolvedValue({
      items: [{ workspace_ref: 'ws-sin-defecto', allowed_geos: ['eu'] }],
    })
    const user = userEvent.setup()
    await abrirClaves(user)
    expect(
      await screen.findByText(/default: not reported/i),
    ).toBeInTheDocument()
  })

  /**
   * ⛔ EL TECHO SE PIDE Y EL RECORTE SE DICE. `handleListWorkspaceResidency` publica `has_more` y
   * sin `limit` el motor pagina a 100: una decisión de SOBERANÍA DE DATOS —dónde se procesa cada
   * workspace— se leía completa estando recortada.
   */
  it('pide el techo real del motor', async () => {
    const user = userEvent.setup()
    await abrirClaves(user)
    expect(residencyMock).toHaveBeenLastCalledWith({ limit: 1000 })
  })

  /** El aviso, con la cifra CARGADA y en las dos direcciones. */
  it('declara el recorte con has_more y no sin él', async () => {
    residencyMock.mockResolvedValue({
      items: [{ workspace_ref: 'ws-uno', allowed_geos: ['eu'] }],
      has_more: true,
    })
    const user = userEvent.setup()
    await abrirClaves(user)
    expect(
      await screen.findByText('Loaded 1 workspaces; there are more'),
    ).toBeVisible()
  })

  it('sin has_more no hay aviso', async () => {
    residencyMock.mockResolvedValue({
      items: [{ workspace_ref: 'ws-uno', allowed_geos: ['eu'] }],
      has_more: false,
    })
    const user = userEvent.setup()
    await abrirClaves(user)
    await screen.findByText('ws-uno')
    expect(
      screen.queryByText(/Loaded \d+ workspaces; there are more/i),
    ).toBeNull()
  })
})
