// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Los dos filos de `TenantListOptions`: que el techo VIAJE y que el inquilino no se pueda soltar.
//
// ⛔ POR QUE ESTE FICHERO EXISTE, y es una correccion a mi propio commit. El mensaje de
//    `e03317146` decia que las sondas «se escriben ahora con @ts-expect-error». Era FALSO en el
//    arbol: las escribi, las corri, y las BORRE al terminar (`rm -f __sonda-tenantlist.ts`). Un
//    control que solo existio durante su propia ejecucion no es un control, y el mensaje del
//    commit afirmaba mas que el codigo. Lo encontro el contraste `sol max`, no yo.
//
// ⛔ Y POR QUE HAY DOS MITADES. Los tipos NO se pueden usar como frontera de ejecucion:
//    TypeScript no crea tipos exactos ni borra propiedades en tiempo de ejecucion, y la
//    comprobacion de propiedades en exceso SOLO actua sobre literales frescos. Una variable de
//    tipo ancho pasa sin ruido. Asi que la mitad de arriba prueba lo que el tipo rechaza al
//    COMPILAR, y la mitad de abajo prueba lo que la capa de API borra al EJECUTAR.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { modelsApi } from './api'
import { consoleApi } from '@/features/console/api'
import type { RequestOptions } from '@/lib/api/client'

// ---------------------------------------------------------------------------
// MITAD 1 — lo que el tipo rechaza al compilar.
//
// Se escribe con `@ts-expect-error` a proposito: si un rechazo dejara de rechazar, el propio
// `@ts-expect-error` pasa a ser un error y `tsc -b` cae. Una sonda que solo comprueba «compila»
// no puede detectar que la guarda se afloje, que es como se me colo el hueco de `anonymous`.
// ---------------------------------------------------------------------------

export function sondasDeTipo(): void {
  // Formas VALIDAS: atadura sola, y atadura + filtros.
  void (() => modelsApi.models({ tenant: 'acme' }))
  void (() => modelsApi.models({ tenant: 'acme', query: { estado: 'activo' } }))

  // @ts-expect-error falta `tenant`: la familia de lista obliga a nombrarlo
  void (() => modelsApi.models({}))

  // @ts-expect-error `anonymous` retira la cabecera de inquilino: fuera de la lista blanca
  void (() => modelsApi.models({ tenant: 'acme', anonymous: true }))

  // @ts-expect-error `body` no tiene sentido en una lista y no esta en la lista blanca
  void (() => modelsApi.models({ tenant: 'acme', body: { a: 1 } }))

  // @ts-expect-error `headers` permitiria reponer a mano lo que el tipo quita
  void (() => modelsApi.models({ tenant: 'acme', headers: { a: 'b' } }))
}

// ---------------------------------------------------------------------------
// MITAD 2 — lo que la capa de API borra al ejecutar, que es lo que el tipo NO puede garantizar.
// ---------------------------------------------------------------------------

let peticiones: { url: string; init: RequestInit | undefined }[] = []

function capturaFetch(): void {
  globalThis.fetch = vi.fn(async (url: string, init?: RequestInit) => {
    peticiones.push({ url: String(url), init })
    return new Response(JSON.stringify({ items: [], has_more: false }), {
      status: 200,
      headers: new Headers({ 'Content-Type': 'application/json' }),
    })
  }) as never
}

/** El VALOR del parametro, nunca una subcadena: `limit=1000oops` falla el Atoi del motor y el
 *  store vuelve al 100 por omision — el techo revertido con la bateria en verde. */
function limitDe(url: string): string | null {
  return new URL(url, 'http://test').searchParams.get('limit')
}

function cabecera(
  init: RequestInit | undefined,
  nombre: string,
): string | null {
  return new Headers(init?.headers).get(nombre)
}

describe('TenantListOptions: el techo viaja y el inquilino no se suelta', () => {
  afterEach(() => {
    peticiones = []
    vi.restoreAllMocks()
  })

  it('un `limit: undefined` explicito NO borra el techo', async () => {
    // ⛔ ESTE ES EL DEFECTO QUE EL CONTRASTE ENCONTRO EN MI DISENO. Con la composicion vieja
    //    —`{ limit: TECHO, ...options.query }`— la propiedad de la derecha gana, y el spread
    //    copia las claves propias AUNQUE valgan `undefined`. `buildUrl` descarta los `undefined`,
    //    asi que la peticion salia SIN `?limit=` y el motor devolvia su pagina de 100: una lista
    //    recortada con el techo puesto y la bateria verde.
    capturaFetch()
    await modelsApi.models({ tenant: 't1', query: { limit: undefined } })
    expect(limitDe(peticiones[0].url)).toBe('1000')
  })

  it('un `limit` que recortaria tampoco gana al techo', async () => {
    capturaFetch()
    await modelsApi.models({ tenant: 't1', query: { limit: 1 } })
    expect(limitDe(peticiones[0].url)).toBe('1000')
  })

  it('los filtros del llamante SI viajan, junto al techo', async () => {
    capturaFetch()
    await modelsApi.models({ tenant: 't1', query: { estado: 'activo' } })
    const u = new URL(peticiones[0].url, 'http://test')
    expect(u.searchParams.get('estado')).toBe('activo')
    expect(u.searchParams.get('limit')).toBe('1000')
  })

  // ⛔ LOS CUATRO ENVOLTORIOS, NO SOLO UNO. `models` era el unico con testigo de transporte, y
  //    los otros tres comparten exactamente la composicion que fallaba. Un techo sin testigo se
  //    revierte sin que nada se ponga rojo: es la mitad del defecto que este fichero corrige.
  it.each([
    ['modelsApi.modelGroups', (o: never) => modelsApi.modelGroups(o)],
    ['consoleApi.listModelGroups', (o: never) => consoleApi.listModelGroups(o)],
    ['consoleApi.listModelAccess', (o: never) => consoleApi.listModelAccess(o)],
  ])(
    '%s manda el techo aunque le pasen `limit: undefined`',
    async (_n, llamar) => {
      capturaFetch()
      await llamar({ tenant: 't1', query: { limit: undefined } } as never)
      expect(limitDe(peticiones[0].url)).toBe('1000')
      expect(cabecera(peticiones[0].init, 'X-Olivares-Tenant')).toBe('t1')
    },
  )

  it('una variable ANCHA con `anonymous` no consigue soltar el inquilino', async () => {
    // La comprobacion de propiedades en exceso no mira aqui: esto compila. La unica razon de que
    // la cabecera sobreviva es que la capa de API RECONSTRUYE el objeto en vez de expandirlo.
    const ancho = { tenant: 't1', anonymous: true } as RequestOptions & {
      tenant: string
    }
    capturaFetch()
    await modelsApi.models(ancho)
    expect(cabecera(peticiones[0].init, 'X-Olivares-Tenant')).toBe('t1')
    expect(limitDe(peticiones[0].url)).toBe('1000')
  })
})
