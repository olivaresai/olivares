// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Toda pantalla que llama a una operacion que el CONTRATO declara truncable avisa del recorte.
//
// ⛔ POR QUE EXISTE. La consola pide listas al motor, que pagina por omision. Si la pantalla no
// dice que la lista viene recortada, el operador lee una lista incompleta CREYENDO que es
// completa. `an internal design note (not shipped)` (seccion «P · Calidad UX de consola»)
// nombra este hueco: de las OCHO propiedades con testigo por pantalla, la declaracion de recorte
// no tenia ninguno. Esta celda es la novena.
//
// ⛔ POR QUE EL DENOMINADOR SALE DEL CONTRATO Y NO DE UNA HEURISTICA, que es lo que hace
//    defendible a esta guarda. La misma seccion de la Matriz documenta una sonda REFUTADA: emparejar
//    cada `useQuery` con su aviso da «8 avisos en todo el arbol» mientras un `grep` de `has_more`
//    encuentra 84 en 32 ficheros de vista — el emparejamiento por variable esta lleno de falsos
//    negativos sobre la forma EN LINEA (`{q.data?.has_more && …}`) que `main` usa hoy.
//    Medido el 2026-08-24 al escribir esta celda: un detector por «useQuery + items» daba 50
//    features y 24 sin aviso, contra las 22/11 de la Matriz. **Cuatro heuristicas dan cuatro
//    cifras y todas parecen ciertas.** Asi que aqui NO se hereda ninguna: el denominador son las
//    operaciones cuya respuesta 200 declara `has_more` EN `web/openapi/openapi.json`, que es un
//    hecho del contrato y no una opinion sobre la forma del codigo.
//
// ⛔ Y LOS TESTS NO CUENTAN COMO AVISO, que es la trampa que casi me come. `evals` aparece con 17
//    menciones de `has_more` si se cuenta el arbol entero — y las 17 estan en `.test.tsx`,
//    mockeando `has_more: false`. Un test que simula el campo no es una pantalla que lo ENSEÑE.
//    Solo cuentan ficheros de vista.
//
// ⛔ ALCANCE, dicho aqui para que nadie lea esta celda como «el recorte esta cubierto». El
//    contrato embebido declara `has_more` en DIEZ operaciones; la Matriz cuenta 82 consultas de
//    lista. Esta guarda es un SUELO exacto, no el censo completo: cubre lo que el contrato puede
//    probar. Las features que la Matriz lista como abiertas y que no llaman a estas diez siguen
//    abiertas y son trabajo aparte.
import { existsSync, readFileSync, readdirSync, statSync } from 'node:fs'
import { join, resolve, sep } from 'node:path'
import { describe, expect, it } from 'vitest'
import openapi from '../../openapi/openapi.json'

const RAIZ_FEATURES = resolve(__dirname)

/** Operaciones cuya respuesta 200 declara `has_more`: el contrato dice que PUEDEN truncar. */
function operacionesTruncables(): string[] {
  const vistos: string[] = []
  const tiene = (o: unknown, prof = 0): boolean => {
    if (prof > 6 || o === null || typeof o !== 'object') return false
    if (!Array.isArray(o)) {
      const props = (o as Record<string, unknown>).properties
      if (props && typeof props === 'object' && 'has_more' in (props as object))
        return true
    }
    return Object.values(o as Record<string, unknown>).some((v) =>
      tiene(v, prof + 1),
    )
  }
  const paths =
    (openapi as { paths?: Record<string, Record<string, unknown>> }).paths ?? {}
  for (const metodos of Object.values(paths)) {
    for (const op of Object.values(metodos ?? {})) {
      if (!op || typeof op !== 'object') continue
      const r200 = (
        (op as Record<string, unknown>).responses as Record<string, unknown>
      )?.['200']
      const oid = (op as Record<string, string>).operationId
      if (oid && tiene(r200)) vistos.push(oid)
    }
  }
  return [...new Set(vistos)].sort()
}

/** Ficheros de VISTA de una feature: `.tsx` y NUNCA tests.
 *
 *  ⛔ ESTA FUNCION PROMETIA «vistas» Y DEVOLVIA TAMBIEN `.ts`, y eso absolvia features por
 *     tokens que no ensena nadie. Medido por el retro-contraste `sol max` del 2026-08-25, con
 *     cuatro falsos verdes NOMBRADOS: `automations` (tipos en `workflows/api.ts:55-61`),
 *     `executive` (un fixture en `fixtures.ts:22-25`), `knowledge` (una interfaz en
 *     `types.ts:364-368`) y `models` (un COMENTARIO de api en `api.ts:47-51`). Las cuatro
 *     consumen listas truncables y ninguna lo dice en una pantalla.
 *
 *     Es la misma clase que el mock en `.test.tsx` que ya excluiamos, un nivel mas abajo: un
 *     tipo, un fixture o un comentario no avisan a un operador. Contar `.tsx` no arregla el
 *     significado —el aviso sigue siendo por FEATURE y no por lista, y `console` lo demuestra—
 *     pero quita los cuatro falsos verdes demostrados.
 */
function ficherosDeVista(dir: string): string[] {
  const salida: string[] = []
  const recorrer = (d: string) => {
    for (const e of readdirSync(d)) {
      const p = join(d, e)
      if (statSync(p).isDirectory()) {
        recorrer(p)
        continue
      }
      if (!/\.tsx$/.test(e)) continue // `.ts` NO: un tipo o un fixture no avisan a nadie
      if (/\.(test|spec)\./.test(e)) continue
      salida.push(p)
    }
  }
  recorrer(dir)
  return salida
}

function featuresQueLlaman(oid: string): string[] {
  const encontradas = new Set<string>()
  for (const f of readdirSync(RAIZ_FEATURES)) {
    const dir = join(RAIZ_FEATURES, f)
    if (!statSync(dir).isDirectory()) continue
    for (const v of ficherosDeVista(dir)) {
      if (new RegExp(`\\b${oid}\\b`).test(readFileSync(v, 'utf8'))) {
        encontradas.add(f)
        break
      }
    }
  }
  return [...encontradas].sort()
}

/** Las DOS formas de avisar, y aceptar sólo una es como se fabrica un falso positivo.
 *
 *  ⛔ MEDIDO el 2026-08-24, y contra mi propia guarda. Mi primera version buscaba solo `has_more`
 *     —la forma EN LINEA que `main` usa hoy— y con ella marque `redteam` como «no avisa». Es
 *     FALSO: su rama declara el recorte con `<ListTruncationBadge>`, el componente compartido, y
 *     lo prueba MONTANDO la vista (`redteam-view-truncation.test.tsx`) justo porque
 *     `{false && <ListTruncationBadge …/>}` engaña a cualquier sonda de fuente.
 *
 *  `ListTruncationBadge` todavia no esta en `main`: llega con la pila de consola. Cuando aterrice,
 *  una guarda que solo mirara `has_more` empezaria a acusar a features que SI avisan — un falso
 *  positivo nacido el dia que el arbol mejora. Se acepta cualquiera de las dos formas.
 *
 *  ⚠ Y lo que ninguna de las dos prueba: que el aviso sea ALCANZABLE. Eso solo lo prueba montar la
 *    vista, y es trabajo por pantalla, no de esta celda. Esta guarda dice «lo declara», nunca
 *    «lo enseña».
 */
function avisaDelRecorte(feature: string): boolean {
  const dir = join(RAIZ_FEATURES, feature)
  return ficherosDeVista(dir).some((v) => {
    const src = readFileSync(v, 'utf8')
    return src.includes('has_more') || src.includes('ListTruncationBadge')
  })
}

/** Metodos del `api.ts` de una feature que devuelven una lista TRUNCABLE (`ListResponse<…>`).
 *
 *  Se lee la fuente, no el contrato: `web/openapi/openapi.json` describe DIEZ operaciones del
 *  nucleo y ninguna de los modulos —lo comprobe hoy: 37 GET en total y cero de finops o redteam—,
 *  asi que un denominador sacado solo de ahi no puede ver estas llamadas.
 */
function metodosTruncablesDe(feature: string): Set<string> {
  const api = join(RAIZ_FEATURES, feature, 'api.ts')
  const salida = new Set<string>()
  if (!existsSync(api)) return salida
  const lineas = readFileSync(api, 'utf8').split('\n')
  for (let i = 0; i < lineas.length; i += 1) {
    const def = /^[ \t]+([A-Za-z][A-Za-z0-9_]*): \(/.exec(lineas[i])
    if (!def) continue
    // El cuerpo cabe en las dos lineas siguientes en todas las formas que usa el arbol
    // (`m: (a) =>\n  http.get<T>(…)`). Se corta en la siguiente definicion para no leer la de al
    // lado: `securityKeys.findings` existe ademas de `securityApi.findings`, y confundirlas es
    // justo el fallo de medir por NOMBRE en vez de por su cuerpo.
    // ⛔ AQUI HABIA UNA VENTANA FIJA DE TRES LINEAS Y SE DEJABA FUERA 16 METODOS REALES.
    //    Lo midio el contraste terra del 2026-08-25 y los nombro con `fichero:linea`:
    //    `models.deployments` (:182→:187), `models.modelAdmissions` (:213→:218),
    //    `eventing.deliveries` (:88→:95), `knowledge.listLineage` (:90→:97),
    //    `finops.statements` (:259→:264), `console.listAgents` (:1450→:1455)… El formato
    //    vertical de un `http.get<…>` con varias opciones basta para salirse de tres lineas,
    //    y un detector que no los ve deja su feature FUERA del denominador: falso negativo
    //    silencioso, que es peor que un falso positivo ruidoso.
    //
    //    La ventana correcta no es un numero: es «hasta la siguiente definicion». El tope de
    //    12 lineas existe solo para que un fichero mal formado no se lea entero de una vez.
    let cuerpo = lineas[i]
    for (let j = i + 1; j < lineas.length && j <= i + 12; j += 1) {
      if (/^[ \t]+[A-Za-z][A-Za-z0-9_]*: \(/.test(lineas[j])) break
      cuerpo += '\n' + lineas[j]
    }
    if (/http\.[a-z]+<ListResponse</.test(cuerpo)) salida.add(def[1])
  }
  return salida
}

/** Features que alcanzan una lista truncable a traves del `api` de OTRA feature.
 *
 *  ⛔ ESTE ES EL PUNTO CIEGO QUE CERRO ESTA CELDA, y lo encontre midiendo, no razonando.
 *     `featuresQueLlaman` busca el `operationId` en los ficheros de vista. `home` no nombra
 *     ninguno: importa `securityApi`, `sessionsApi` y `healthApi` y llama a sus metodos. Medido
 *     el 2026-08-24 sobre el arbol de la pila de consola, `home` llama a CUATRO listas
 *     truncables —`securityApi.findings`, `sessionsApi.live`, `healthApi.incidents` y
 *     `healthApi.status`, las cuatro `http.get<ListResponse<…>>`— y a cinco agregados que NO son
 *     listas (`finopsApi.summary/trend/forecast`, `inventoryApi.summary`,
 *     `complianceApi.summary`). Por eso NO basta con seguir el import: seguir el import a secas
 *     habria acusado a `home` por los cinco agregados tambien, que es un falso positivo con la
 *     forma de un hallazgo. Se exige el metodo CONCRETO y que su cuerpo devuelva `ListResponse<`.
 */
function alcanceCruzado(feature: string): string[] {
  const dir = join(RAIZ_FEATURES, feature)
  const alcanzados: string[] = []
  for (const v of ficherosDeVista(dir)) {
    const src = readFileSync(v, 'utf8')
    for (const imp of src.matchAll(
      /import \{([^}]*)\} from '@\/features\/([a-z-]+)\/api'/g,
    )) {
      const ajena = imp[2]
      if (ajena === feature) continue
      const truncables = metodosTruncablesDe(ajena)
      if (truncables.size === 0) continue
      for (const ident of imp[1]
        .split(',')
        .map((s) => s.trim())
        .filter(Boolean)) {
        for (const m of truncables) {
          if (new RegExp(`\\b${ident}\\.${m}\\(`).test(src))
            alcanzados.push(`${ajena}.${m}`)
        }
      }
    }
  }
  return [...new Set(alcanzados)].sort()
}

/** Listas truncables que una feature consume de su PROPIO `api.ts`.
 *
 *  ⛔ ESTE ERA EL TERCER PUNTO CIEGO, y explica por que mi censo del 2026-08-24 salio mal.
 *     Los otros dos caminos miran el CONTRATO (diez operaciones del nucleo) y el api AJENO
 *     —y el cruzado excluye la propia por construccion—, asi que una feature que se consume
 *     a si misma era invisible para los dos. Medido el 2026-08-25: de las nueve features que
 *     consumen listas truncables sin avisar, SIETE lo hacen por su propio api
 *     (`compliance` con 19 metodos, `capabilities` 5, `deploy` 4, `catalog` 3,
 *     `inference-proxy`, `residency` y `tenants` con 1 cada una).
 */
function alcanceAlPropio(feature: string): string[] {
  const truncables = metodosTruncablesDe(feature)
  if (truncables.size === 0) return []
  const dir = join(RAIZ_FEATURES, feature)
  // Los objetos que el `api.ts` de la feature EXPORTA: son los unicos receptores validos.
  const api = join(dir, 'api.ts')
  const receptores = existsSync(api)
    ? [
        ...readFileSync(api, 'utf8').matchAll(
          /^export const ([A-Za-z][A-Za-z0-9_]*) = \{/gm,
        ),
      ].map((m) => m[1])
    : []
  if (receptores.length === 0) return []
  const alcanzados: string[] = []
  for (const v of ficherosDeVista(dir)) {
    if (v.endsWith(`${sep}api.ts`)) continue // el api se DECLARA ahi; consumirlo es otra cosa
    const src = readFileSync(v, 'utf8')
    for (const m of truncables) {
      // ⛔ LIGA EL RECEPTOR. Antes bastaba `.metodo(` en cualquier texto, asi que otro objeto
      //    con el mismo nombre de metodo fabricaba alcance. Lo nombro el retro-contraste
      //    `sol max`: «no liga el receptor … puede fabricar alcance si otro objeto tiene el
      //    mismo metodo». Se exige el objeto exportado por el `api.ts` de la propia feature.
      if (receptores.some((r) => new RegExp(`\\b${r}\\.${m}\\b`).test(src))) {
        alcanzados.push(`${feature}.${m}`)
      }
    }
  }
  return [...new Set(alcanzados)].sort()
}

/** Empareja AVISO ↔ LISTA dentro de un fichero de vista. Devuelve [consumidas, avisadas].
 *
 *  ⛔ POR QUE HACE FALTA, y no es refinamiento: el aviso se medía por FEATURE y no por LISTA, así
 *     que una feature que avisa de UNA queda absuelta de todas las demás. El retro-contraste
 *     `sol max` del 2026-08-25 lo demostró con `console`: avisa en dos listas de bindings y con
 *     eso no se la acusa, mientras `roles-tab` consume workspaces —que truncan— sin mirar el
 *     campo. Por eso dejó el total semántico en «≥ 21» y no en un número.
 *
 *  El idioma que empareja es el que el árbol usa:
 *      const NOMBRE = useQuery({ … queryFn: () => algunApi.metodo(…) … })
 *      NOMBRE.data?.has_more            ó   <ListTruncationBadge query={NOMBRE} …/>
 *
 *  ⚠ SE IGNORAN LOS COMENTARIOS, y no por estilo: de las cinco menciones de `has_more` en
 *    `console/bindings-tab.tsx`, TRES están en comentarios que explican la trampa. Contarlas
 *    daría por avisadas listas que nadie avisa — el mismo error que el mock en un `.test.tsx`.
 *
 *  Esto REPORTA; todavía no acusa. Convertir el «≥ 21» en cifra exige triar las veinte, y una
 *  guarda que acusa antes de ese triaje sólo produce exenciones en masa.
 */
function avisoPorLista(feature: string): {
  consumidas: string[]
  avisadas: string[]
} {
  const truncables = metodosTruncablesDe(feature)
  const consumidas: string[] = []
  const avisadas: string[] = []
  if (truncables.size === 0) return { consumidas, avisadas }
  for (const v of ficherosDeVista(join(RAIZ_FEATURES, feature))) {
    const lineas = readFileSync(v, 'utf8').split('\n')
    const codigo = lineas.map((l) => (/^\s*(\/\/|\*|\/\*)/.test(l) ? '' : l))
    const src = codigo.join('\n')
    // NOMBRE -> metodo, leyendo el cuerpo del useQuery que sigue a la definicion
    const porNombre = new Map<string, string>()
    for (let i = 0; i < codigo.length; i += 1) {
      const def = /^\s*const ([A-Za-z][A-Za-z0-9_]*)\s*=\s*useQuery\(\{/.exec(
        codigo[i],
      )
      if (!def) continue
      const cuerpo = codigo.slice(i + 1, i + 10).join('\n')
      const fn = /queryFn:[^\n]*?\.([A-Za-z][A-Za-z0-9_]*)\(/.exec(cuerpo)
      if (fn && truncables.has(fn[1])) porNombre.set(def[1], fn[1])
    }
    for (const [nombre, metodo] of porNombre) {
      consumidas.push(`${feature}.${metodo}`)
      const inline = new RegExp(`\\b${nombre}\\.data\\??\\.has_more`).test(src)
      const badge = new RegExp(`query=\\{${nombre}\\}`).test(src)
      if (inline || badge) avisadas.push(`${feature}.${metodo}`)
    }
  }
  return {
    consumidas: [...new Set(consumidas)].sort(),
    avisadas: [...new Set(avisadas)].sort(),
  }
}

/** Una feature alcanza una lista truncable por CUALQUIERA de los dos caminos. */
function alcanzaTruncable(feature: string, truncables: string[]): boolean {
  if (truncables.some((oid) => featuresQueLlaman(oid).includes(feature)))
    return true
  if (alcanceCruzado(feature).length > 0) return true
  return alcanceAlPropio(feature).length > 0
}

// Features que HOY llaman a una operacion truncable y no avisan. La lista solo puede ENCOGER.
// No es un contador: `C07-06` ya enseño que un numero no distingue formas conocidas de formas
// nuevas. Cada entrada lleva su razon, y cuando se arregla se borra la linea.
const SIN_AVISO_CONOCIDAS: Record<string, string> = {
  // ⛔ NO ES UN HUECO: su motor DRENA. Medido el 2026-08-25 (triaje de motor, paso-0 de la
  //    receta). `handleListOrgs` (core/api/handlers_core.go:739-761) no toma limite ni cursor
  //    y no fija `HasMore`; el store (sqlstore/system.go:497-507) devuelve todo lo visible O
  //    ERROR (`ErrEnumerationNotAuthoritative`), nunca un parcial silencioso. `has_more` de
  //    cliente es siempre false. Se queda declarada porque la guarda mide FUENTE y el tipo
  //    `ListResponse<>` no distingue: el wrapper es el CONTENEDOR, no la evidencia.
  // ⛔ #1622 ATERRIZO EL 2026-08-25 Y LA LISTA ENCOGIO SOLA, que era el diseno. Aqui vivian
  //    `claude-policy`, `finops` y `redteam`, exentas «hasta que aterrice la pila de consola».
  //    La tercera celda dijo que SOBRABAN y se borraron el 2026-08-27, junto con otras seis que
  //    el mismo aterrizaje dejo obsoletas. Se deja escrito porque el valor de esta lista es que
  //    ENCOJA: una exencion que nadie retira envejece hasta volverse permiso.
  // ⛔ LAS TRES SIGUIENTES ESTABAN FALSAMENTE ABSUELTAS y las nombro el retro-contraste
  //    `sol max` del 2026-08-25: su unico token de aviso vivia en un fichero que NO es una
  //    pantalla, y `ficherosDeVista` los contaba. Entran ahora que «vista» significa `.tsx`.
  executive:
    'alcanza listas truncables por el api de CINCO features; su unico token estaba en fixtures.ts',
  // Y las cuatro que consumen SUS PROPIAS listas y no avisan en ninguna vista. El numero es
  // el de metodos `ListResponse<…>` de su `api.ts`, medido el 2026-08-25, no estimado.
  // ⛔ LA MAYOR DE LAS VEINTE, Y TAMPOCO ES UN HUECO. `handleListUSStatePacks`
  //    (modules/compliance/depthhandlers.go) llama a `listAll` y devuelve `listResponse{Items}`
  //    sin `HasMore` ni `Cursor`; el modulo hace 57 llamadas a `listAll` y CERO asignaciones de
  //    `HasMore` a una respuesta. Su unico `page.HasMore` (helpers.go:190, dentro de
  //    `pageCount`) alimenta `CapabilityEvidence.More` — evidencia de capacidad, no paginacion.
  //    Lo compruebo porque mi primera sonda dijo 0 y la ancha dijo 1: gana la que encuentra mas.
  compliance:
    'N/A: sus listas DRENAN con listAll (depthhandlers.go); 0 HasMore en respuesta',
  // ⛔ `deploy` VIVIA AQUI Y SE RETIRA EL 2026-08-28, porque #1902 (restauracion de) le da
  //    su aviso: la tercera celda de esta bateria dijo que la exencion SOBRA, y ese rojo es la
  //    forma que tiene este gate de celebrar un arreglo. Se borra en el mismo aterrizaje que la
  //    vuelve falsa: una exencion que nadie retira envejece hasta volverse permiso.
  // Misma lista y mismo motor que `residency`: las dos piden `/v1/system/orgs`.
  home: 'alcanza CUATRO listas truncables por el api de otras features (securityApi.findings, sessionsApi.live, healthApi.incidents, healthApi.status) y el panel no declara ningun techo',
}

describe('toda pantalla que llama a una operacion truncable avisa del recorte', () => {
  it('el contrato declara al menos una operacion truncable', () => {
    // Control positivo: si el contrato dejara de declarar `has_more`, esta celda pasaria vacia
    // y no lo notariamos. La Matriz llama a eso «un gate ciego que certifica».
    expect(operacionesTruncables().length).toBeGreaterThan(0)
  })

  it('las features que llaman a una operacion truncable avisan, salvo las declaradas', () => {
    const truncables = operacionesTruncables()
    const sinAviso: string[] = []
    const acusar = (f: string) => {
      if (!avisaDelRecorte(f) && !sinAviso.includes(f)) sinAviso.push(f)
    }
    for (const oid of truncables)
      for (const f of featuresQueLlaman(oid)) acusar(f)
    // Y el segundo camino, el que `home` enseño: llegar a la lista por el api de otra feature.
    for (const f of readdirSync(RAIZ_FEATURES)) {
      if (!statSync(join(RAIZ_FEATURES, f)).isDirectory()) continue
      if (alcanceCruzado(f).length > 0 || alcanceAlPropio(f).length > 0)
        acusar(f)
    }
    const nuevas = sinAviso.filter((f) => !(f in SIN_AVISO_CONOCIDAS)).sort()
    expect(
      nuevas,
      `features NUEVAS que llaman a una operacion truncable y no avisan: ${nuevas.join(', ')}`,
    ).toEqual([])
  })

  it('el camino cruzado esta CALIBRADO: ve una lista truncable conocida y no ve un agregado', () => {
    // Sin este control, `metodosTruncablesDe` podria devolver vacio —un regex que deja de casar
    // porque el arbol cambia de forma— y las otras celdas pasarian sin mirar nada. Un gate ciego
    // no falla: certifica. Los dos lados a proposito: el que DEBE ver y el que NO debe ver.
    const deSecurity = metodosTruncablesDe('security')
    expect(
      [...deSecurity],
      'securityApi.findings devuelve ListResponse<Finding>',
    ).toContain('findings')
    const deFinops = metodosTruncablesDe('finops')
    expect([...deFinops], 'finopsApi.summary NO es una lista').not.toContain(
      'summary',
    )
  })

  it('la ventana llega mas alla de tres lineas: ve un metodo en formato VERTICAL', () => {
    // ⛔ CONTROL DEL ARREGLO, y existe porque el defecto era INVISIBLE. Con la ventana fija de
    //    tres lineas, `models.deployments` (api.ts :182 → :187) quedaba fuera del denominador
    //    y su feature no se contaba: un falso negativo que ninguna celda notaba, porque una
    //    lista que no se ve no acusa a nadie. El contraste terra nombro 16 asi.
    //
    //    Los DOS lados a proposito: uno que la ventana vieja NO veia y otro que si, para que
    //    esta celda no pase por medir de mas.
    const deModels = metodosTruncablesDe('models')
    expect(
      [...deModels],
      'models.deployments vive en :182 y su ListResponse en :187',
    ).toContain('deployments')
    expect(
      [...deModels],
      'models.modelAdmissions, lo mismo (:213 → :218)',
    ).toContain('modelAdmissions')
  })

  it('el emparejamiento AVISO<->LISTA esta CALIBRADO sobre el caso que lo motivo', () => {
    // `console` es el caso con el que `sol max` demostro que el aviso por FEATURE absuelve de
    // mas: avisa de DOS listas —postures y assignments, con `.data?.has_more === true`— y
    // consume muchas mas. Las dos direcciones, para que la celda no pase por medir de mas.
    const c = avisoPorLista('console')
    expect(c.avisadas.length, 'console avisa de alguna lista').toBeGreaterThan(
      0,
    )
    expect(
      c.consumidas.length,
      'console consume MAS listas truncables de las que avisa: ese es el hueco',
    ).toBeGreaterThan(c.avisadas.length)
    // Y el control negativo del propio emparejador: una feature sin `api.ts` no fabrica pares.
    expect(
      avisoPorLista('home').consumidas,
      'home no tiene api.ts propio',
    ).toEqual([])
  })

  it('las exenciones declaradas siguen siendo ciertas: ninguna sobra', () => {
    // Una exencion que ya no aplica es deuda que se ve como cobertura. Si alguien arregla
    // `onboarding` y no borra su linea, esta celda lo dice.
    const truncables = operacionesTruncables()
    const sobran = Object.keys(SIN_AVISO_CONOCIDAS).filter((f) => {
      return !alcanzaTruncable(f, truncables) || avisaDelRecorte(f)
    })
    expect(
      sobran,
      `exenciones que ya no hacen falta (borra su linea): ${sobran.join(', ')}`,
    ).toEqual([])
  })
})
