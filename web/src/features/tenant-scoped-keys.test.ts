// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Una clave de caché que no nombra al inquilino atribuye el dato de uno a otro.
//
// ⛔ EL CONTRATO ESTÁ ESCRITO, y era el código el que no lo cumplía. `lib/api/query.ts`, literal:
// «Tenant-scoped data MUST include the active tenant id in its key (via `tenantScope`), so
// switching tenant cache-isolates and refetches cleanly».
//
// ⛔ POR QUÉ IMPORTA, con las tres piezas medidas:
//   1 · la petición autenticada va marcada — el cliente pone `X-Olivares-Tenant`
//       (`lib/api/client.ts:213`) salvo en las anónimas, así que la MISMA ruta se evalúa bajo
//       ámbitos distintos y PUEDE devolver contenido distinto (puede coincidir por casualidad;
//       lo que no se puede es contar con ello);
//   2 · la caché NO se vacía al cambiar de inquilino — `app/providers.tsx` sólo limpia el
//       workspace, y `queryClient.clear()` vive en el `logout` (`lib/auth/context.tsx:217`);
//   3 · sin el hueco del inquilino, cambiar de tenant NO cambia la clave, así que **mientras la
//       entrada siga fresca react-query no dispara otra petición**: la pantalla sigue pintando lo
//       del anterior con el nombre del nuevo arriba. Pasados los `staleTime: 30_000` la entrada
//       queda obsoleta, pero react-query no agenda nada por su cuenta: hace falta un disparador
//       —foco de ventana o remontaje— para corregirlo.
//
// Salieron 18 claves así en seis features —reglas de acceso a modelos, centros de coste, extractos
// de reparto, tarifas, asientos, escaneos de conocimiento, reglas DLP, ítems de calibración y
// ejecuciones de informe—, y en las mismas seis la fábrica correcta YA EXISTÍA. El testigo de
// conducta está en `models/access-view.test.tsx`; esta guarda cierra la CLASE.
//
// LA REGLA: un `queryKey` escrito como ARRAY LITERAL vale sólo si lleva un identificador de
// inquilino, o si la ruta que lee es de despliegue y no de inquilino (lista explícita abajo).
// Lo demás pasa por la fábrica de la feature, que ya reserva el hueco.
import { readFileSync, readdirSync, statSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

const RAIZ = resolve(__dirname, '..')

/**
 * Claves de DESPLIEGUE, no de inquilino. Cada una con la razón por la que su dato es el mismo
 * para todos los inquilinos — no «es que no la sé arreglar».
 */
const GLOBALES: Record<string, string> = {
  // El documento OpenAPI que sirve el motor: idéntico para todo inquilino.
  'openapi-spec': 'el contrato del motor no depende del inquilino',
  // `/v1/server-info` es NO autenticado (`lib/hooks/use-server-info.ts`).
  'server-info': 'ruta sin autenticar, previa a cualquier inquilino',
  // `/v1/console/activation*` — «global, superadmin-gated» (`console/api.ts`).
  'console|activation-preview':
    'la activación es del despliegue, no del inquilino',
}

/** Un identificador que nombra al inquilino dentro de la clave. */
const LLEVA_INQUILINO = /\b(activeTenant|tenantId|tenant)\b/

function ficheros(dir: string, out: string[] = []): string[] {
  for (const n of readdirSync(dir)) {
    const p = join(dir, n)
    if (statSync(p).isDirectory()) ficheros(p, out)
    else if (/\.tsx?$/.test(n) && !n.includes('.test.')) out.push(p)
  }
  return out
}

/** Los `queryKey:` escritos como array literal que no nombran al inquilino. */
export function clavesSinInquilino(
  src: string,
): { clave: string; motivo: string }[] {
  const fuera: { clave: string; motivo: string }[] = []
  for (const m of src.matchAll(/queryKey:\s*\[/g)) {
    // Cierra el corchete contando anidamiento; una clave puede llevar objetos y arrays.
    let prof = 0
    let j = m.index + m[0].length - 1
    for (; j < src.length; j++) {
      if (src[j] === '[') prof++
      else if (src[j] === ']') {
        prof--
        if (prof === 0) break
      }
    }
    const clave = src.slice(m.index + m[0].length - 1, j + 1)
    // ⛔ EL SEÑUELO: `['knowledge', 'tenant', 'scans']` satisfacía la expresión sin segregar
    // nada, porque la palabra estaba DENTRO de una cadena. Se vacían las cadenas antes de
    // preguntar, así que sólo cuenta un identificador de verdad.
    const sinCadenas = clave.replace(/'[^']*'/g, "''").replace(/"[^"]*"/g, '""')
    if (LLEVA_INQUILINO.test(sinCadenas)) continue
    const segmentos = [...clave.matchAll(/'([^']*)'/g)].map((s) => s[1])
    const uno = segmentos[0] ?? ''
    const dos = segmentos.slice(0, 2).join('|')
    if (uno in GLOBALES || dos in GLOBALES) continue
    fuera.push({
      clave: clave.replace(/\s+/g, ' '),
      motivo: uno || '(sin literal)',
    })
  }
  return fuera
}

describe('las claves de caché nombran al inquilino', () => {
  it('ninguna pantalla escribe una clave literal sin el hueco del inquilino', () => {
    const infractores: string[] = []
    let visitados = 0
    let clavesVistas = 0
    for (const f of ficheros(RAIZ)) {
      visitados++
      const src = readFileSync(f, 'utf8')
      clavesVistas += [...src.matchAll(/queryKey:/g)].length
      for (const c of clavesSinInquilino(src)) {
        infractores.push(`${f.slice(RAIZ.length + 1)}  ${c.clave}`)
      }
    }
    // ⛔ ESTAS DOS COTAS VAN ANTES DEL VEREDICTO, y las puso un mutante: neutralizar el recorrido
    // —una raíz mal resuelta, un filtro que excluye todo, un `slice` de más— dejaba la lista de
    // infractores vacía y la casilla VERDE sobre un árbol lleno. Una guarda que no ha demostrado
    // que MIRÓ no falla: certifica. Se comprueba que recorrió ficheros Y que vio claves en ellos.
    expect(visitados).toBeGreaterThan(300)
    expect(clavesVistas).toBeGreaterThan(30)

    expect(infractores).toEqual([])
  })

  /**
   * ⛔ Y EL CONTROL POSITIVO DEL BARRIDO, que es lo que faltaba y lo descubrió un mutante: si el
   * recorrido de ficheros devuelve la lista VACÍA —una raíz mal resuelta, un filtro que lo excluye
   * todo, un `slice` de más—, la casilla de arriba sale VERDE sobre un árbol lleno de infracciones.
   * Un vigía que no ha demostrado que MIRA no falla: certifica.
   *
   * Se ancla en un hecho real y estable del árbol: `finops-view.tsx` escribe una clave literal que
   * SÍ lleva el inquilino. Tiene que estar entre los ficheros recorridos, el detector tiene que
   * verla, y tiene que dejarla pasar. Si alguien la convierte a fábrica algún día, esta casilla se
   * pone roja y se cambia el ancla — que es justo lo contrario de enterarse por un silencio.
   */
  it('el barrido alcanza el árbol de verdad', () => {
    const fs = ficheros(RAIZ)
    expect(fs.length).toBeGreaterThan(300)

    const ancla = fs.find((f) => f.endsWith('finops/finops-view.tsx'))
    expect(ancla).toBeDefined()
    const src = readFileSync(ancla as string, 'utf8')
    expect(src).toContain("queryKey: ['finops', tenant, 'value', 'summary']")
    expect(clavesSinInquilino(src)).toEqual([])
  })

  /**
   * ⛔ EL AUTOTEST DE LA GUARDA. Un vigía que no se ha probado contra el caso MALO certifica en
   * vez de vigilar: si el corchete se cerrara mal, o la expresión no casara, esta guarda saldría
   * verde sobre un árbol lleno de infracciones y nadie lo notaría. Se prueba en las dos
   * direcciones, con el caso bueno incluido.
   */
  it('la guarda caza el caso malo y deja pasar el bueno', () => {
    // MALO: literal sin inquilino.
    expect(
      clavesSinInquilino(`useQuery({ queryKey: ['finops', 'statements'] })`),
    ).toHaveLength(1)
    // BUENO 1: el literal lleva el inquilino.
    expect(
      clavesSinInquilino(
        `useQuery({ queryKey: ['finops', activeTenant, 'statements'] })`,
      ),
    ).toEqual([])
    // BUENO 2: clave de despliegue declarada.
    expect(
      clavesSinInquilino(`useQuery({ queryKey: ['openapi-spec', 'beta'] })`),
    ).toEqual([])
    // BUENO 3: la fábrica no es un literal y no se mira.
    expect(
      clavesSinInquilino(`useQuery({ queryKey: finopsKeys.statements(x) })`),
    ).toEqual([])
    // MALO con anidamiento: el corchete se cierra bien y sigue cazándose.
    expect(
      clavesSinInquilino(
        `useQuery({ queryKey: ['knowledge', 'scans', { limit: 25 }] })`,
      ),
    ).toHaveLength(1)
    // MALO con SEÑUELO: la palabra dentro de una cadena no segrega nada y no vale como excusa.
    expect(
      clavesSinInquilino(
        `useQuery({ queryKey: ['knowledge', 'tenant', 'scans'] })`,
      ),
    ).toHaveLength(1)
  })
})

/**
 * ⛔ Y LA MITAD QUE FALTABA, que la señaló el contraste externo: la guarda de arriba mira los
 * arrays LITERALES, y hoy **474 de 523** registros de clave pasan por una FÁBRICA. Un mutante que
 * quitara el inquilino DENTRO de `knowledgeKeys.scans` no toca ningún llamante, no lo ve la guarda
 * textual, no lo ve el testigo de conducta de Models — y reintroduce la fuga entera.
 *
 * Se comprueba en dos mitades, porque ninguna sola basta:
 *
 *   1 · ESTÁTICA — qué entradas no DECLARAN el inquilino como primer parámetro. Cada una tiene
 *       que estar abajo con su razón MEDIDA, no supuesta.
 *   2 · EN EJECUCIÓN — para las que sí lo declaran, se las llama con un centinela y el centinela
 *       tiene que APARECER en la clave. Declararlo y tirarlo es el mutante que la mitad estática
 *       no ve.
 *
 * La regla que decide, y se puede comprobar desde el cliente: una entrada cuya ruta es
 * `/v1/m/<ns>/…` es de inquilino; `/v1/console/…` y `/v1/system/…` son del despliegue.
 */
const FABRICAS = import.meta.glob('./*/api.ts', { eager: true }) as Record<
  string,
  Record<string, unknown>
>

/** Entradas sin inquilino, con la razón por la que su dato NO es de inquilino. */
const NO_SON_DE_INQUILINO: Record<string, string> = {
  // Las tres de `lib/api/query.ts` que de verdad son del DESPLIEGUE, ahora que el trinquete
  // por fin mira ese directorio. Cada una con su razón, que es lo que la exención cuesta.
  'lib-api.serverInfo':
    '`/v1/server-info` es NO autenticado: se lee antes de que haya inquilino',
  'lib-api.whoami':
    '`/v1/whoami` describe al PRINCIPAL, no a un inquilino: es el mismo dato mire donde mire',
  'lib-api.orgs':
    '`/v1/system/orgs` es la lista de inquilinos — no puede vivir dentro de uno',
  'backups.*':
    'BASE `/v1/console/dr` — la copia de seguridad es del despliegue',
  'logs.*': 'BASE `/v1/console/logs` — el búfer de log es del proceso',
  'tenants.*':
    '`/v1/system/orgs` — la lista de inquilinos no vive dentro de uno',
  'residency.*':
    '`/v1/system/residency` y `/v1/system/orgs` — registro de sistema',
  'console.*':
    '`/v1/console/…`, rotuladas «global, superadmin-gated» en el propio cliente. Las dos con ' +
    '`scope` SÍ son de inquilino: `scope` es el id del inquilino, sólo que no se llama así.',
  'health.publicStatus': '`/status`, la página pública y sin autenticar',
  'reporting.reports':
    '`handleListReports(w, _ *http.Request, _ api.ModuleContext)` — catálogo estático en código, ' +
    'ignora petición y contexto (modules/reporting/api.go)',
  'governance.emergingStandards':
    '`handleEmergingStandards(w, _, _)` — mismo patrón, registro «design-toward» en código ' +
    '(modules/governance/emerging.go)',
}

const CENTINELA = 'T-CENTINELA'

/** Entradas de fábrica cuyo PRIMER parámetro no es el inquilino. */
function entradasSinInquilinoDeclarado(src: string): string[] {
  const fuera: string[] = []
  for (const f of src.matchAll(/export const \w*Keys\s*=\s*\{/g)) {
    let prof = 0
    let j = f.index + f[0].length - 1
    for (; j < src.length; j++) {
      if (src[j] === '{') prof++
      else if (src[j] === '}') {
        prof--
        if (prof === 0) break
      }
    }
    const cuerpo = src.slice(f.index + f[0].length - 1, j + 1)
    for (const e of cuerpo.matchAll(/^ {2}(\w+):\s*(\([^)]*\)\s*=>|\[)/gm)) {
      const firma = e[2]
      const args = firma.startsWith('(')
        ? firma.slice(1, firma.indexOf(')'))
        : ''
      const primero = (args.split(',')[0] ?? '').split(':')[0].trim()
      if (!['tenant', 't', 'tenantId'].includes(primero)) fuera.push(e[1])
    }
  }
  return fuera
}

// ⛔ EL FILTRO ERA POR NOMBRE, Y ESO DEJABA `lib/api/` FUERA POR CONSTRUCCIÓN.
//
// `ficheros(RAIZ)` YA recorre `web/src` entero — `RAIZ` es `src/`, no `src/features/` —, así que el
// paseo SÍ llegaba a `lib/api/`. Lo que lo excluía era el `.endsWith('/api.ts')` de abajo: en ese
// directorio NO HAY ningún fichero llamado `api.ts`. Las dos fábricas compartidas viven en
// `lib/api/search.ts` (`searchKeys`) y `lib/api/query.ts` (`queryKeys`), y ninguna de las dos se
// llamaba así, de modo que este trinquete nunca las miró.
//
// ⚠ Y por eso el arreglo NO era «añadir `lib/api/api.ts` al patrón»: ESE PATRÓN CASA CERO FICHEROS.
// La batería seguiría verde y el hueco parecería cerrado — la clase «un gate ciego no falla:
// CERTIFICA». Se filtra por lo que el fichero ES (exporta una fábrica `*Keys`), no por cómo se llama.
function exportaFabricaDeClaves(p: string): boolean {
  if (!/\.ts$/.test(p)) return false
  return /export const \w*Keys\s*=\s*\{/.test(readFileSync(p, 'utf8'))
}

/** La etiqueta con la que se nombra una entrada: la feature, o el directorio compartido. */
function etiquetaDe(p: string): string {
  const rel = p.slice(RAIZ.length + 1)
  const partes = rel.split('/')
  if (partes[0] === 'features') return partes[1]
  // `lib/api/query.ts` -> `lib-api`: no es una feature y no debe fingir serlo.
  return partes.slice(0, -1).join('-')
}

describe('las fábricas de claves reservan el hueco del inquilino', () => {
  it('toda entrada sin inquilino tiene una razón medida escrita', () => {
    const sinRazon: string[] = []
    let vistas = 0
    for (const f of ficheros(RAIZ).filter(exportaFabricaDeClaves)) {
      const feature = etiquetaDe(f)
      for (const entrada of entradasSinInquilinoDeclarado(
        readFileSync(f, 'utf8'),
      )) {
        vistas++
        const id = `${feature}.${entrada}`
        if (`${feature}.*` in NO_SON_DE_INQUILINO || id in NO_SON_DE_INQUILINO)
          continue
        sinRazon.push(id)
      }
    }
    // La cota, por lo mismo que arriba: una guarda que no demuestra que MIRÓ certifica.
    expect(vistas).toBeGreaterThan(20)
    expect(sinRazon).toEqual([])
  })

  it('la que declara el inquilino lo pone DE VERDAD en la clave', () => {
    const tiran: string[] = []
    let comprobadas = 0
    for (const [ruta, mod] of Object.entries(FABRICAS)) {
      const feature = ruta.split('/')[1]
      for (const [nombre, fabrica] of Object.entries(mod)) {
        if (!/Keys$/.test(nombre)) continue
        if (typeof fabrica !== 'object' || fabrica === null) continue
        for (const [entrada, f] of Object.entries(
          fabrica as Record<string, unknown>,
        )) {
          const id = `${feature}.${entrada}`
          if (
            `${feature}.*` in NO_SON_DE_INQUILINO ||
            id in NO_SON_DE_INQUILINO
          )
            continue
          if (typeof f !== 'function') {
            tiran.push(`${id}: no es función`)
            continue
          }
          comprobadas++
          let clave: unknown
          try {
            clave = (f as (...a: unknown[]) => unknown)(CENTINELA, 'a', 'b')
          } catch {
            tiran.push(`${id}: lanzó`)
            continue
          }
          if (!JSON.stringify(clave)?.includes(CENTINELA)) tiran.push(id)
        }
      }
    }
    expect(comprobadas).toBeGreaterThan(300)
    expect(tiran).toEqual([])
  })
})
