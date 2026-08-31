// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// el TECHO de las listas del nucleo, medido en la URL que el cliente construye.
//
// ⛔ POR QUE UN TESTIGO DE TRANSPORTE Y NO UNA LECTURA DEL FUENTE. `scripts/check-list-truncation-
//    witness.sh` comprueba que la palabra `limit` aparece cerca de la llamada — es un censo de
//    TEXTO y lo dice de si mismo. Un `limit` en un comentario, en una rama muerta o en un objeto
//    que nadie serializa lo satisface igual. Lo unico que prueba que el techo LLEGA es mirar la
//    query string que sale.
//
// ⛔ Y LA CELDA QUE DE VERDAD IMPORTA ES LA DEL ORDEN. `{ limit: X, ...params }` y
//    `{ ...params, limit: X }` se leen casi igual y hacen lo contrario: con el segundo, el techo
//    PISA en silencio el limite que pida la vista. Ningun lint lo ve, el testigo de recorte
//    tampoco —los dos contienen la palabra— y la consola seguiria "funcionando". Por eso hay una
//    celda que fija que el llamante GANA.
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { useSessionStore } from '@/stores/session'
import { useTenantStore } from '@/stores/tenant'
import {
  agentsApi,
  auditApi,
  iamApi,
  systemApi,
  AUDIT_PAGE_MAX,
  LIST_CEILING,
} from './endpoints'

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })
}

/** Interviene `fetch` y devuelve el mock, para leer la peticion que el cliente construyo. */
function stubFetch() {
  const mock = vi.fn(() =>
    Promise.resolve(jsonResponse({ items: [], has_more: false })),
  )
  vi.stubGlobal('fetch', mock)
  return mock
}

/** La peticion que salio: RUTA y query.
 *
 *  ⛔ LA RUTA TAMBIEN, y faltaba. La primera version devolvia solo `searchParams`, asi que
 *     apuntar `listUsers` a una ruta equivocada con la misma query dejaba las celdas VERDES
 *     mientras sus nombres afirmaban `/v1/users`. Lo nombro el contraste `sol max` del
 *     2026-08-27 (hallazgo F): un test que afirma en su nombre lo que no mira en su cuerpo. */
function peticion(mock: ReturnType<typeof stubFetch>, call = 0) {
  const [url] = mock.mock.calls[call] as unknown as [string]
  const parsed = new URL(String(url), 'https://console.invalid')
  return { ruta: parsed.pathname, query: parsed.searchParams }
}

beforeEach(() => {
  vi.unstubAllGlobals()
  useSessionStore.setState({ token: 'tok' } as never)
  useTenantStore.setState({ activeTenant: 'acme' } as never)
})

describe('el techo de las listas del nucleo llega a la URL', () => {
  it('GET /v1/users pide el techo explicito, y a ESA ruta', async () => {
    const mock = stubFetch()
    await iamApi.listUsers()
    const { ruta, query } = peticion(mock)
    // El motor lo LEE: `parseListQuery` (core/api/handlers_core.go:782-793), consumido por
    // `handleListUsers` (:233-254), que ademas publica el campo de recorte.
    expect(ruta).toBe('/v1/users')
    expect(query.get('limit')).toBe(String(LIST_CEILING))
  })

  it('GET /v1/agents pide el techo explicito, y a ESA ruta', async () => {
    const mock = stubFetch()
    await agentsApi.list()
    const { ruta, query } = peticion(mock)
    // `parseFilteredListQuery` (:797-830) extiende `parseListQuery`: mismo caso.
    expect(ruta).toBe('/v1/agents')
    expect(query.get('limit')).toBe(String(LIST_CEILING))
  })

  it('EL LLAMANTE GANA · users', async () => {
    const mock = stubFetch()
    await iamApi.listUsers({ limit: 25 })
    expect(peticion(mock).query.get('limit')).toBe('25')
  })

  it('EL LLAMANTE GANA · agents', async () => {
    // ⛔ CELDA SEPARADA A PROPOSITO. Con el orden probado SOLO en users, invertir unicamente
    //    esta llamada dejaba la bateria entera verde, porque agents nunca se invocaba con un
    //    limite propio. Lo midio el contraste `sol max` (hallazgo F) como mutante vivo.
    const mock = stubFetch()
    await agentsApi.list({ limit: 7 })
    expect(peticion(mock).query.get('limit')).toBe('7')
  })

  it('UN `limit: undefined` EXPLICITO NO borra el techo · users', async () => {
    // ⛔ EL ESCAPE QUE EL ORDEN NO CERRABA (hallazgo C del contraste). `ListParams.limit` es
    //    opcional, asi que `{ limit: undefined }` es una llamada legal — y con
    //    `{ limit: X, ...params }` el spread borraba el techo, el cliente omitia el `undefined`
    //    y el motor volvia a su omision de CIEN. El techo desaparecia en la forma que mas se
    //    parece a «no me importa el limite». Se resuelve con `??`, no con el orden.
    const mock = stubFetch()
    await iamApi.listUsers({ limit: undefined })
    expect(peticion(mock).query.get('limit')).toBe(String(LIST_CEILING))
  })

  it('UN `limit: undefined` EXPLICITO NO borra el techo · agents', async () => {
    const mock = stubFetch()
    await agentsApi.list({ limit: undefined })
    expect(peticion(mock).query.get('limit')).toBe(String(LIST_CEILING))
  })

  it('y el cursor del llamante viaja con el techo puesto', async () => {
    const mock = stubFetch()
    await iamApi.listUsers({ cursor: 'c-42' })
    const { query } = peticion(mock)
    expect(query.get('cursor')).toBe('c-42')
    // COTA: el techo sigue puesto cuando el llamante NO trae el suyo. Sin esta mitad, la celda
    // pasaria igual con un objeto que se hubiera comido el literal.
    expect(query.get('limit')).toBe(String(LIST_CEILING))
  })

  it('CONTRAFACTUAL · /v1/system/orgs NO pide techo, y es a proposito', async () => {
    const mock = stubFetch()
    await systemApi.listOrgs()
    const { ruta, query } = peticion(mock)
    // `handleListOrgs` (core/api/handlers_core.go:739-761) no parsea la consulta: DRENA. Un
    // `?limit` aqui seria decorativo. Esta celda es tambien el control negativo de las de
    // arriba: si diera `1000` en las tres, no estaria discriminando nada.
    expect(ruta).toBe('/v1/system/orgs')
    expect(query.get('limit')).toBeNull()
  })

  it('EL LEDGER · GET /v1/audit pide SU techo, que no es el del nucleo', async () => {
    // ⛔ ESTAS DOS ERAN INVISIBLES. Escriben el alias `AuditListResponse`, y la sonda de la capa
    //    compartida casaba el envoltorio generico literal: el censo decia TRES listas y habia
    //    CINCO. No estaban exentas — no se miraban. Se arreglo la sonda en el mismo lote.
    const mock = stubFetch()
    await auditApi.list()
    const { ruta, query } = peticion(mock)
    expect(ruta).toBe('/v1/audit')
    expect(query.get('limit')).toBe(String(AUDIT_PAGE_MAX))
  })

  it('EL LEDGER · GET /v1/audit/system, lo mismo', async () => {
    const mock = stubFetch()
    await auditApi.systemList()
    const { ruta, query } = peticion(mock)
    expect(ruta).toBe('/v1/audit/system')
    expect(query.get('limit')).toBe(String(AUDIT_PAGE_MAX))
  })

  it('EL LEDGER · el llamante gana, que es lo que hace inerte al techo hoy', async () => {
    // Los dos llamantes reales traen el suyo a proposito: `notification-bell` pide 10 (preview)
    // y `audit-view` pagina de 100 con «cargar mas». Si el techo los pisara, la campana se
    // traeria mil eventos y la vista perderia su paginacion.
    const mock = stubFetch()
    await auditApi.list({ from: 42, limit: 100 })
    const { query } = peticion(mock)
    expect(query.get('limit')).toBe('100')
    expect(query.get('from')).toBe('42')
  })

  it('EL LEDGER · y `limit: undefined` tampoco lo borra', async () => {
    const mock = stubFetch()
    await auditApi.list({ limit: undefined })
    expect(peticion(mock).query.get('limit')).toBe(String(AUDIT_PAGE_MAX))
  })

  it('EL LEDGER · su techo es SUYO: distinto contrato que el del nucleo', () => {
    // ⛔ Coinciden en el numero y NO en la regla: el almacen generico ACOTA a su maximo, y el
    //    ledger CAE DE VUELTA A 100 si le piden mas (`handlers_audit.go`, guarda
    //    `limit <= 0 || limit > N`). Compartir constante haria que subir la del nucleo dejara a
    //    esta pidiendo el minimo justo cuando cree pedir el maximo.
    //    Quien ata cada numero a SU motor es un test Go que lee esta constante:
    //    `core/api/audit_page_parity_test.go` y
    //    `core/internal/store/sqlstore/core_list_page_parity_test.go`. Los dos con mutante probado.
    expect(Number.isInteger(AUDIT_PAGE_MAX)).toBe(true)
    expect(AUDIT_PAGE_MAX).toBeGreaterThan(0)
  })

  it('el techo NO se ata aqui al maximo del motor: eso vive del lado de Go', () => {
    // ⛔ AQUI HABIA UN `expect(LIST_CEILING).toBe(1000)` que decia atar el numero al
    //    maximo REAL del store... comparandolo con OTRO LITERAL DE TYPESCRIPT. No ataba nada:
    //    mutar `maxLimit` de 1000 a 500 dejaba esta bateria entera en verde. Es el defecto que
    //    `core/internal/store/sqlstore/evidence_page_parity_test.go` ya lleva escrito para
    //    `EVIDENCE_PAGE`, repetido aqui con otro nombre y cazado por el contraste `sol max`.
    //
    //    La paridad REAL vive donde nace el valor y se comprueba en la otra direccion:
    //    `core/internal/store/sqlstore/core_list_page_parity_test.go` LEE esta constante desde
    //    Go y falla si se separa de `generic.go:maxLimit` (mutante probado: 1000 -> 500 la mata).
    //
    //    Lo que SI se puede afirmar desde aqui, y es lo unico: que la constante existe, es un
    //    entero positivo y es la que viaja en la URL — eso lo fijan las celdas de arriba.
    expect(Number.isInteger(LIST_CEILING)).toBe(true)
    expect(LIST_CEILING).toBeGreaterThan(0)
  })
})
