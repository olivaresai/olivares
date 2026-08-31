// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Las tres listas de la pestaña de gobierno —parque de modelos, políticas de enrutado y claves de
// proveedor— pedían sin `limit`, así que el repositorio genérico paginaba a 100 y la consola tiraba
// el `has_more` que los tres handlers publican.
//
// La guarda del aviso (`has_more && !error`) NO se prueba aquí: vive en un solo sitio,
// `_intel/notices.tsx`, y tiene su propia casilla con las cuatro combinaciones. Aquí se mide lo
// que ESTA pantalla decide: que el techo llega a las tres llamadas y que el aviso lleva la cifra
// CARGADA, no el techo que se pidió.
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { renderIntel, screen, userEvent } from '@/test/intel'
import { governedModelsFixture, routingPoliciesFixture } from './fixtures'
import '@/features/_intel'
import './i18n'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', can: () => true }),
}))

const estateMock = vi.fn()
const routingMock = vi.fn()
const keysMock = vi.fn()
const residencyMock = vi.fn()
const accesoMock = vi.fn()
const gruposMock = vi.fn()
const gpaiMock = vi.fn()

/** Una fila que sirve a las siete tablas: cada una lee campos distintos y ninguna revienta con
 *  campos de más. Para el parque hace falta el fixture real (la tabla lee más). */
const FILA = {
  id: 'x1',
  name: 'uno',
  workspace_ref: 'ws-uno',
  subject_kind: 'user',
  subject_ref: 'alice@demo',
  target_kind: 'model',
  target_ref: 'claude-opus-5',
  provider_ref: 'provider-one',
  alias: 'k1',
  provider: 'anthropic',
  strategy: 'cheapest',
  targets: [],
  members: [],
}
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return {
    ...actual,
    modelsApi: {
      ...actual.modelsApi,
      models: (...a: unknown[]) => estateMock(...a),
      routingPolicies: (...a: unknown[]) => routingMock(...a),
      keys: (...a: unknown[]) => keysMock(...a),
      catalog: () => Promise.resolve({ models: [], capabilities: [] }),
      modelAccess: (...a: unknown[]) => accesoMock(...a),
      modelGroups: (...a: unknown[]) => gruposMock(...a),
      workspaceResidency: (...a: unknown[]) => residencyMock(...a),
      gpaiPostures: (...a: unknown[]) => gpaiMock(...a),
    },
  }
})

const { ModelsView } = await import('./models-view')

beforeEach(() => {
  estateMock.mockReset().mockResolvedValue({ items: [], has_more: false })
  routingMock.mockReset().mockResolvedValue({ items: [], has_more: false })
  keysMock.mockReset().mockResolvedValue({ items: [], has_more: false })
  residencyMock.mockReset().mockResolvedValue({ items: [], has_more: false })
  accesoMock.mockReset().mockResolvedValue({ items: [], has_more: false })
  gruposMock.mockReset().mockResolvedValue({ items: [], has_more: false })
  gpaiMock.mockReset().mockResolvedValue({ items: [], has_more: false })
})

async function abrir(nombre: RegExp) {
  const user = userEvent.setup()
  renderIntel(<ModelsView />)
  await user.click(await screen.findByRole('tab', { name: nombre }))
  return user
}

describe('El parque, las rutas y las claves piden el techo del motor', () => {
  it('el parque de modelos', async () => {
    await abrir(/^Estate$/i)
    expect(estateMock).toHaveBeenLastCalledWith({
      tenant: 't1',
      query: { limit: 1000 },
    })
  })

  it('las políticas de enrutado', async () => {
    await abrir(/^Routing$/i)
    expect(routingMock).toHaveBeenLastCalledWith({ limit: 1000 })
  })

  it('las claves de proveedor', async () => {
    await abrir(/^Provider keys$/i)
    expect(keysMock).toHaveBeenLastCalledWith({ limit: 1000 })
  })

  it('la residencia por workspace', async () => {
    await abrir(/^Provider keys$/i)
    expect(residencyMock).toHaveBeenLastCalledWith({ limit: 1000 })
  })

  /** Reglas y grupos van en la misma pestaña pero son DOS llamadas: las dos llevan techo. */
  it('las reglas de acceso y los grupos de modelos', async () => {
    await abrir(/^Access$/i)
    expect(accesoMock).toHaveBeenLastCalledWith(
      { tenant: 't1' },
      { limit: 1000 },
    )
    expect(gruposMock).toHaveBeenLastCalledWith({
      tenant: 't1',
      query: { limit: 1000 },
    })
  })

  it('las posturas GPAI', async () => {
    await abrir(/^GPAI posture$/i)
    expect(gpaiMock).toHaveBeenLastCalledWith({ limit: 1000 })
  })

  /**
   * ⛔ LA CIFRA DEL AVISO ES LA CARGADA, no el techo que se pidió: interpolar la constante
   * convertiría el aviso en una medida inventada — diría «1000» con dos filas en pantalla.
   */
  it('el aviso del parque lleva la cifra que llegó', async () => {
    // Fixture REAL del módulo: la tabla lee campos que un objeto a mano no tiene, y un test que
    // revienta en el render mide el fixture, no la pantalla.
    estateMock.mockResolvedValue({
      items: governedModelsFixture.slice(0, 2),
      has_more: true,
    })
    await abrir(/^Estate$/i)
    expect(
      await screen.findByText('Loaded 2 models; there are more'),
    ).toBeVisible()
  })
})

/**
 * ⛔ EL TESTIGO DE CABLEADO, y lo pidió el contraste con el mutante escrito: cada aviso tiene que
 * mirar SU consulta. Sin esta tabla, cambiar `query={keysQ}` por `query={policiesQ}` —o borrar un
 * badge entero— pasaría, porque las casillas de arriba sólo prueban un aviso.
 *
 * El montaje es el que lo hace load-bearing: **sólo la lista bajo prueba devuelve `has_more:true`**
 * y todas las demás `false`. Así, un badge cableado a otra consulta no se enciende cuando debe, y
 * uno cableado de más se enciende cuando no debe. Mata las dos formas con una sola tabla.
 */
describe('cada aviso mira SU consulta', () => {
  const CASOS = [
    {
      lista: 'parque',
      tab: /^Estate$/i,
      mock: () => estateMock,
      aviso: 'Loaded 1 models; there are more',
      fila: governedModelsFixture[0],
    },
    {
      lista: 'rutas',
      tab: /^Routing$/i,
      mock: () => routingMock,
      aviso: 'Loaded 1 policies; there are more',
      fila: routingPoliciesFixture[0],
    },
    {
      lista: 'claves',
      tab: /^Provider keys$/i,
      mock: () => keysMock,
      aviso: 'Loaded 1 keys; there are more',
    },
    {
      lista: 'residencia',
      tab: /^Provider keys$/i,
      mock: () => residencyMock,
      aviso: 'Loaded 1 workspaces; there are more',
    },
    {
      lista: 'acceso',
      tab: /^Access$/i,
      mock: () => accesoMock,
      aviso: 'Loaded 1 rules; there are more',
    },
    {
      lista: 'grupos',
      tab: /^Access$/i,
      mock: () => gruposMock,
      aviso: 'Loaded 1 groups; there are more',
    },
    {
      lista: 'gpai',
      tab: /^GPAI posture$/i,
      mock: () => gpaiMock,
      aviso: 'Loaded 1 postures; there are more',
    },
  ]

  for (const caso of CASOS) {
    it(`el aviso de ${caso.lista} sale sólo cuando SU lista tiene más`, async () => {
      caso.mock().mockResolvedValue({
        items: [(caso as { fila?: unknown }).fila ?? FILA],
        has_more: true,
      })
      await abrir(caso.tab)
      expect(await screen.findByText(caso.aviso)).toBeVisible()

      // Y las demás no encienden el suyo: si dos badges compartieran consulta, esto lo caza.
      const otros = CASOS.filter((c) => c.aviso !== caso.aviso).map(
        (c) => c.aviso,
      )
      for (const ajeno of otros) expect(screen.queryByText(ajeno)).toBeNull()
    })
  }
})
