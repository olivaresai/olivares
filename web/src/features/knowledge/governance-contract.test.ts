// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// C07-04 — las veintitrés rutas de `knowledge` y `models` que la consola nunca llamaba.
//
// knowledge: 53 rutas en el motor, 40 llamadas. models: 67 y 57. Lo que faltaba no era
// accesorio: la memoria de agente exportable, importable y **verificable**, las reglas DLP, los
// escaneos de clasificación, los derechos por nivel de acceso y **la residencia por workspace**
// — que decide dónde se procesan los datos de un workspace y estaba sólo en `curl`.
//
// ⛔ LA RUTA ES LA ASERCIÓN: cliente real contra un `fetch` sustituido.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { configureApiClient } from '@/lib/api/client'
import { fetchMemoryExport, knowledgeApi } from './api'
import { modelsApi } from '@/features/models/api'

let sentUrl = ''
let sentMethod = ''
let sentBody: BodyInit | null | undefined
let sentContentType: string | null = null

function stubFetch() {
  globalThis.fetch = vi.fn(async (url: string, init?: RequestInit) => {
    sentUrl = String(url)
    sentMethod = String(init?.method ?? 'GET')
    sentBody = init?.body
    sentContentType = new Headers(init?.headers).get('Content-Type')
    return new Response(JSON.stringify({ items: [] }), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    })
  }) as never
}

const url = () => new URL(sentUrl, 'https://console.invalid')
const tenantOptions = { tenant: 'tenant-test' } as const

afterEach(() => {
  configureApiClient({
    getToken: () => null,
    getTenant: () => null,
    onUnauthorized: () => {},
  })
  sentUrl = ''
  sentMethod = ''
  sentBody = undefined
  sentContentType = null
})

const KNOWLEDGE: Array<{
  que: string
  invoca: () => Promise<unknown>
  metodo: string
  ruta: string
}> = [
  {
    que: 'importar memoria',
    invoca: () => knowledgeApi.importMemory('{}\n'),
    metodo: 'POST',
    ruta: '/v1/m/knowledge/memory/import',
  },
  {
    que: 'toda la memoria (ADMIN)',
    invoca: () => knowledgeApi.allMemory(),
    metodo: 'GET',
    ruta: '/v1/m/knowledge/memory/all',
  },
  {
    que: 'verificar la memoria (ADMIN)',
    invoca: () => knowledgeApi.verifyMemory(),
    metodo: 'POST',
    ruta: '/v1/m/knowledge/memory/verify',
  },
  {
    que: 'sincronizar una KB',
    invoca: () => knowledgeApi.syncKb('kb-1'),
    metodo: 'POST',
    ruta: '/v1/m/knowledge/kbs/kb-1/sync',
  },
  {
    que: 'escanear una KB',
    invoca: () => knowledgeApi.scanKb('kb-1'),
    metodo: 'POST',
    ruta: '/v1/m/knowledge/kbs/kb-1/scan',
  },
  {
    que: 'escanear una fuente por nombre',
    invoca: () => knowledgeApi.scanSource('s3-datalake', tenantOptions),
    metodo: 'POST',
    ruta: '/v1/m/knowledge/sources/s3-datalake/scan',
  },
  {
    que: 'las etiquetas de clasificación',
    invoca: () => knowledgeApi.labels(),
    metodo: 'GET',
    ruta: '/v1/m/knowledge/labels',
  },
  {
    que: 'el historial de escaneos',
    invoca: () => knowledgeApi.scans({}, tenantOptions),
    metodo: 'GET',
    ruta: '/v1/m/knowledge/scans',
  },
  {
    que: 'las reglas DLP',
    invoca: () => knowledgeApi.dlpRules(tenantOptions),
    metodo: 'GET',
    ruta: '/v1/m/knowledge/dlp/rules',
  },
  {
    que: 'fijar las reglas DLP (ADMIN)',
    invoca: () => knowledgeApi.setDlpRules({ rules: [] }, tenantOptions),
    metodo: 'PUT',
    ruta: '/v1/m/knowledge/dlp/rules',
  },
  {
    que: 'retirar una regla DLP (ADMIN)',
    invoca: () => knowledgeApi.deleteDlpRule('r-1', tenantOptions),
    metodo: 'DELETE',
    ruta: '/v1/m/knowledge/dlp/rules/r-1',
  },
  {
    que: 'el contrato ACTIVO de un producto de datos',
    invoca: () => knowledgeApi.activeContract('dp-1'),
    metodo: 'GET',
    ruta: '/v1/m/knowledge/data-products/dp-1/contracts/active',
  },
]

const MODELS: Array<{
  que: string
  invoca: () => Promise<unknown>
  metodo: string
  ruta: string
}> = [
  {
    que: 'ejecutar una política de enrutado (ADMIN)',
    invoca: () => modelsApi.executeRoutingPolicy('rp-1'),
    metodo: 'POST',
    ruta: '/v1/m/models/routing-policies/rp-1/execute',
  },
  {
    que: 'leer UNA política de enrutado',
    invoca: () => modelsApi.routingPolicy('rp-1'),
    metodo: 'GET',
    ruta: '/v1/m/models/routing-policies/rp-1',
  },
  {
    que: 'los derechos por nivel de acceso',
    invoca: () => modelsApi.accessTierEntitlements(),
    metodo: 'GET',
    ruta: '/v1/m/models/access-tier-entitlements',
  },
  {
    que: 'fijar los derechos por nivel',
    invoca: () => modelsApi.setAccessTierEntitlements({}),
    metodo: 'PUT',
    ruta: '/v1/m/models/access-tier-entitlements',
  },
  {
    que: 'la residencia por workspace',
    invoca: () => modelsApi.workspaceResidency(),
    metodo: 'GET',
    ruta: '/v1/m/models/workspace-residency',
  },
  {
    que: 'fijar la residencia por workspace',
    invoca: () => modelsApi.setWorkspaceResidency({}),
    metodo: 'PUT',
    ruta: '/v1/m/models/workspace-residency',
  },
  {
    que: 'actualizar UNA clave',
    invoca: () => modelsApi.updateKey('k-1', {}),
    metodo: 'PUT',
    ruta: '/v1/m/models/keys/k-1',
  },
  {
    que: 'el catálogo de gobierno de datos',
    invoca: () => modelsApi.dataGovernance(),
    metodo: 'GET',
    ruta: '/v1/m/models/data-governance',
  },
  {
    que: 'los tipos de herramienta',
    invoca: () => modelsApi.toolTypes(),
    metodo: 'GET',
    ruta: '/v1/m/models/tool-types',
  },
  {
    que: 'las capacidades',
    invoca: () => modelsApi.features(),
    metodo: 'GET',
    ruta: '/v1/m/models/features',
  },
]

describe('las trece rutas de knowledge que no se llamaban', () => {
  it.each(KNOWLEDGE)(
    '$que: $metodo $ruta',
    async ({ invoca, metodo, ruta }) => {
      stubFetch()
      await invoca()
      expect(sentMethod).toBe(metodo)
      expect(url().pathname).toBe(ruta)
    },
  )

  it('importa el paquete JSONL exacto, sin envolverlo ni serializarlo otra vez', async () => {
    const raw = '{"schema":"olivares.memory.v1","count":0}\n'
    stubFetch()
    await knowledgeApi.importMemory(raw)
    expect(sentBody).toBe(raw)
    expect(sentContentType).toBe('application/x-ndjson')
  })
})

describe('las diez rutas de models que no se llamaban', () => {
  it.each(MODELS)('$que: $metodo $ruta', async ({ invoca, metodo, ruta }) => {
    stubFetch()
    await invoca()
    expect(sentMethod).toBe(metodo)
    expect(url().pathname).toBe(ruta)
  })
})

describe('las dos parejas que se parecen y NO son la misma operación', () => {
  /**
   * ⛔ EL CONTROL: `memory` (la de un agente) y `memory/all` (la del tenant entero) son rutas
   * DISTINTAS con permisos DISTINTOS — la segunda es admin. Un cliente que resolviera las dos a
   * la misma ruta enseñaría la memoria de todos los agentes a quien sólo puede ver la de uno,
   * y el 403 llegaría del motor pero después de haberlo pedido.
   */
  it('la memoria de UNO y la de TODOS no van a la misma ruta', async () => {
    stubFetch()
    await knowledgeApi.listMemory({})
    const unaRuta = url().pathname
    stubFetch()
    await knowledgeApi.allMemory()
    expect(url().pathname).not.toBe(unaRuta)
    expect(url().pathname).toBe('/v1/m/knowledge/memory/all')
  })

  /**
   * ⛔ EL CONTROL: escanear una KB va por `id` y escanear una fuente va por NOMBRE
   * (`/sources/{name}/scan`). Son espacios de identificadores distintos: un nombre con `/`
   * mandado como id apuntaría a otra ruta. Por eso van percodificados, y esto lo fija.
   */
  it('un nombre de fuente con barra no cambia la ruta de destino', async () => {
    stubFetch()
    await knowledgeApi.scanSource('equipo/datos', tenantOptions)
    expect(url().pathname).toBe('/v1/m/knowledge/sources/equipo%2Fdatos/scan')
  })
})

/**
 * ⛔ LA EXPORTACIÓN NO CABE EN LA TABLA DE ARRIBA, y su historia es el hallazgo: `/memory/export`
 *    emite JSONL, el cliente HTTP hace `JSON.parse(text)` con un `catch` que deja
 *    `parsed = undefined` (`lib/api/client.ts:154-164`), y por tanto `knowledgeApi.exportMemory`
 *    **devolvía `undefined` en el caso de ÉXITO**. Nunca se notó porque ninguna pantalla lo
 *    llamaba. Ahora es `fetchMemoryExport`, con `fetch` propio como `fetchFocusExport`.
 */
describe('fetchMemoryExport — el paquete de portabilidad', () => {
  const MANIFIESTO = {
    schema: 'olivares.memory.v1',
    tenant: 't1',
    count: 12,
    integrity_excluded: 3,
    entries_sha256: 'abc',
    signature: 'sig',
  }
  const CUERPO = [
    JSON.stringify(MANIFIESTO),
    '{"key":"k1"}',
    '{"key":"k2"}',
  ].join('\n')

  let pedido = ''
  beforeEach(() => {
    pedido = ''
    vi.stubGlobal('fetch', (u: string) => {
      pedido = String(u)
      return Promise.resolve(
        new Response(CUERPO, {
          status: 200,
          headers: { 'Content-Type': 'application/x-ndjson' },
        }),
      )
    })
  })
  afterEach(() => vi.unstubAllGlobals())

  it('pide la ruta de exportación', async () => {
    await fetchMemoryExport()
    expect(new URL(pedido, 'https://console.invalid').pathname).toBe(
      '/v1/m/knowledge/memory/export',
    )
  })

  /**
   * ⛔ EL CONTROL QUE MÁS IMPORTA: el manifiesto trae `integrity_excluded` — el motor **DEJA
   * FUERA** las filas que fallan la comprobación de integridad y las cuenta. Un paquete entregado
   * a un interesado sin decir cuántas filas faltan se presenta como completo sin serlo.
   *
   * EL MUTANTE: devolver sólo el cuerpo crudo. La cifra existe en la línea 1 y nadie la lee.
   */
  it('devuelve el manifiesto con las filas EXCLUIDAS por integridad', async () => {
    const r = await fetchMemoryExport()
    expect(r.manifest?.count).toBe(12)
    expect(r.manifest?.integrity_excluded).toBe(3)
    expect(r.raw.split('\n')).toHaveLength(3)
  })

  /**
   * ⛔ EL SEGUNDO: aquí un **501 NO es la costura open-core**. `handleExportMemory` falla CERRADO
   * con 501 cuando la clave de firma de portabilidad no está cableada — «it never emits an
   * unsigned bundle». Leerlo como «tu edición no lo incluye» manda a alguien a comprar un add-on
   * por una clave que le falta.
   */
  it('clasifica el 501 como clave sin cablear, no como edición', async () => {
    vi.stubGlobal('fetch', () =>
      Promise.resolve(
        new Response('', { status: 501, statusText: 'Not Implemented' }),
      ),
    )
    await expect(fetchMemoryExport()).rejects.toMatchObject({
      status: 501,
      code: 'portability_key_unwired',
    })
  })

  /** Un manifiesto ilegible no se convierte en «no había manifiesto»: llega nulo y se dice. */
  it('un manifiesto ilegible llega nulo, no fabricado', async () => {
    vi.stubGlobal('fetch', () =>
      Promise.resolve(
        new Response('esto-no-es-json\n{"key":"k1"}', { status: 200 }),
      ),
    )
    const r = await fetchMemoryExport()
    expect(r.manifest).toBeNull()
    expect(r.raw).toContain('k1')
  })
})
