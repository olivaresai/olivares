// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repo root.
//
// ⛔ LOS SUJETOS SE DERIVAN DEL MOTOR, NO DE UNA LISTA ESCRITA A MANO (H14 del contraste).
//
// `step-up-behind-a-stale-pregate.test.tsx` comprueba cuatro ficheros que YO enumeré. Eso vale
// mientras la lista esté al día y deja de valer en silencio en cuanto alguien añade un llamante:
// convertir mañana el onboarding en una mutación manual no pondría nada en rojo. Aquí la lista se
// CALCULA: se lee qué handlers de `core/api` llaman `requireAAL3`, con qué rutas están
// registrados, y qué método de la consola pide cada una de esas rutas.
//
// Lo que se fija es la CONTABILIDAD, no la implementación: toda ruta gateada por el motor tiene
// que tener llamante localizable en la consola, y ninguna puede desaparecer del radar sin que
// esta celda lo diga. Quién responde bien a la ceremonia lo comprueban las otras celdas.
import { readdirSync, readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

const AQUI = dirname(fileURLToPath(import.meta.url))
const RAIZ = join(AQUI, '..', '..', '..', '..')
const CORE_API = join(RAIZ, 'core', 'api')
const WEB_SRC = join(RAIZ, 'web', 'src')

const goFuentes = () =>
  readdirSync(CORE_API)
    .filter((f) => f.endsWith('.go') && !f.endsWith('_test.go'))
    .map((f) => readFileSync(join(CORE_API, f), 'utf8'))

/** Cuerpo de cada `func (s *Server) X(...)`, hasta el siguiente `func`. */
function cuerposDeHandler(src: string): Map<string, string> {
  const out = new Map<string, string>()
  const re = /func \(s \*Server\) (\w+)\(/g
  let m: RegExpExecArray | null
  while ((m = re.exec(src)) !== null) {
    const fin = src.indexOf('\nfunc ', m.index + m[0].length)
    out.set(m[1], src.slice(m.index, fin === -1 ? src.length : fin))
  }
  return out
}

/**
 * Handlers gateados por AAL3.
 *
 * ⛔ DOS NIVELES, Y LOS DOS SALIERON DE MEDIR, no de suponer:
 *  · DIRECTO — el cuerpo llama `s.requireAAL3(...)`.
 *  · HEREDADO (un nivel) — `handleSetSuperadminActive` NO se registra en ninguna ruta: lo llaman
 *    `handleEnableSuperadmin` y `handleDisableSuperadmin`, que sí. Sin este paso, dos rutas
 *    gateadas de verdad quedaban fuera del censo y nadie lo notaba.
 *
 * TECHO: sólo UN nivel de herencia. Una cadena de tres saltos no se vería, y se dice aquí en vez
 * de dejar que el número parezca completo.
 */
function handlersGateados(): { directos: Set<string>; todos: Set<string> } {
  const cuerpos = new Map<string, string>()
  for (const src of goFuentes())
    for (const [n, c] of cuerposDeHandler(src)) cuerpos.set(n, c)

  const directos = new Set<string>()
  for (const [n, c] of cuerpos) if (c.includes('.requireAAL3(')) directos.add(n)

  const todos = new Set(directos)
  for (const [n, c] of cuerpos) {
    if (todos.has(n)) continue
    for (const g of directos)
      if (c.includes(`s.${g}(`)) {
        todos.add(n)
        break
      }
  }
  return { directos, todos }
}

/**
 * Constantes de ruta: literales y concatenaciones de una constante conocida.
 * `authzenExportPath = authzenBasePath + "/access-review/export"` es el caso real que obligó a
 * resolver la concatenación; sin ella, la ruta del export de access-review salía como `<const>`.
 * TECHO: sólo concatenación `CONST + "literal"`, resuelta hasta punto fijo.
 */
function constantesDeRuta(): Map<string, string> {
  const consts = new Map<string, string>()
  const fuentes = goFuentes()
  for (const src of fuentes)
    for (const m of src.matchAll(/(\w+)\s*=\s*"(\/[^"]*)"/g))
      if (!consts.has(m[1])) consts.set(m[1], m[2])
  for (let i = 0; i < 4; i++)
    for (const src of fuentes)
      for (const m of src.matchAll(/(\w+)\s*=\s*(\w+)\s*\+\s*"([^"]*)"/g)) {
        if (consts.has(m[1])) continue
        const base = consts.get(m[2])
        if (base !== undefined) consts.set(m[1], base + m[3])
      }
  return consts
}

interface RutaGateada {
  verbo: string
  ruta: string
  handler: string
}

/** Rutas de `server.go` cuyo handler está gateado, con el prefijo de los `Route(...)` anidados. */
function rutasGateadas(gated: Set<string>): RutaGateada[] {
  const consts = constantesDeRuta()
  const server = readFileSync(join(CORE_API, 'server.go'), 'utf8')
  const pila: Array<[number, string]> = []
  const out: RutaGateada[] = []
  for (const linea of server.split('\n')) {
    const ind = linea.length - linea.trimStart().length
    while (pila.length && pila[pila.length - 1][0] >= ind) pila.pop()
    const ruta = /\w+\.Route\("([^"]+)"/.exec(linea)
    if (ruta) {
      pila.push([ind, ruta[1]])
      continue
    }
    const verbo =
      /(\w+)\.(Get|Put|Post|Delete|Patch)\((?:"([^"]*)"|(\w+)),\s*s\.(\w+)\)/.exec(
        linea,
      )
    if (!verbo || !gated.has(verbo[5])) continue
    const p = verbo[3] ?? consts.get(verbo[4] ?? '') ?? `<${verbo[4]}>`
    const prefijo = pila.map(([, x]) => x).join('')
    // ⛔ EL `/v1` NO ES UNIVERSAL. `authzenExportPath` se registra sobre el router RAÍZ
    // (`r.Post(authzenExportPath, …)`, server.go:586) y ya es absoluto: `/access/v1/...`.
    // Prefijarlo daba `/v1/access/v1/...`, una ruta que no existe — y el censo la contaba como
    // huérfana, es decir, inventaba un hallazgo. Sólo se prefija lo que cuelga de `v1`.
    const enV1 = verbo[1] === 'v1' || pila.length > 0
    const base = enV1 && !p.startsWith('/access/') ? `/v1${prefijo}${p}` : p
    out.push({
      verbo: verbo[2].toUpperCase(),
      ruta: base.replace(/\/\//g, '/').replace(/\/$/, ''),
      handler: verbo[5],
    })
  }
  return out
}

/** Rutas literales que la consola pide, con el fichero que las pide. */
function rutasDeLaConsola(): Map<string, string[]> {
  const out = new Map<string, string[]>()
  const norm = (r: string) =>
    r
      .replace(/\$\{[^}]*\}/g, '')
      .replace(/\/$/, '')
      .replace(/\?.*$/, '')
  const anda = (dir: string) => {
    for (const e of readdirSync(dir, { withFileTypes: true })) {
      const p = join(dir, e.name)
      if (e.isDirectory()) {
        anda(p)
        continue
      }
      if (!e.name.endsWith('.ts') && !e.name.endsWith('.tsx')) continue
      if (e.name.includes('.test.')) continue
      const src = readFileSync(p, 'utf8')
      for (const m of src.matchAll(/['"`](\/(?:v1|access)\/[^'"`\s]*)['"`]/g)) {
        const k = norm(m[1])
        if (!k) continue
        out.set(k, [...(out.get(k) ?? []), p])
      }
    }
  }
  anda(WEB_SRC)
  return out
}

describe('el censo de escrituras gateadas por AAL3 se DERIVA del motor', () => {
  const { directos, todos } = handlersGateados()
  const rutas = rutasGateadas(todos)
  const consola = rutasDeLaConsola()

  it('el motor gatea handlers, y la derivación los encuentra', () => {
    // ⛔ Anti-vacuidad: si `requireAAL3` se renombrara o la ruta de `core/api` cambiara, todo lo
    //    demás daría verde sobre CERO handlers. El número exacto no se fija —crecerá— pero un
    //    censo que cae a cero no es un censo.
    expect(directos.size).toBeGreaterThanOrEqual(20)
    // Y la herencia aporta algo real: los dos envoltorios de superadmin.
    expect(todos.size).toBeGreaterThan(directos.size)
  })

  it('toda ruta derivada resuelve a una ruta de verdad, no a una constante sin resolver', () => {
    // ⛔ El fallo silencioso que esto corta: `authzenExportPath` salía como `<authzenExportPath>`
    //    y esa "ruta" no casa con nada de la consola, así que el censo la habría dado por
    //    inexistente en vez de por no resuelta.
    const sinResolver = rutas.filter((r) => r.ruta.includes('<'))
    expect(sinResolver.map((r) => `${r.handler} → ${r.ruta}`)).toEqual([])
    expect(rutas.length).toBeGreaterThanOrEqual(20)
  })

  it('⛔ y cada ruta gateada tiene llamante localizable en la consola', () => {
    // Éste es H14: la lista de sujetos deja de ser mía. Si mañana aparece un endpoint gateado
    // que la consola pide desde un fichero nuevo, entra solo; si desaparece el llamante de uno,
    // esta celda lo dice en vez de seguir comprobando cuatro ficheros que yo escribí un día.
    // ⛔ TECHO DECLARADO: las rutas con parámetro (`/idps/{alias}`) NUNCA aparecen como literal
    //    en la consola — las COMPONE un helper (`ssoConfigPath(scope)` + `/idps/${alias}`), así
    //    que un barrido de literales no puede verlas enteras. Se comparan por el prefijo ESTABLE
    //    (lo anterior al primer `{`), que es lo máximo que este método puede afirmar. Una ruta
    //    parametrizada cuyo prefijo sí esté en la consola cuenta como localizada.
    const prefijoEstable = (r: string) => r.split('/{')[0].replace(/\/$/, '')
    const literales = [...consola.keys()]
    const localizada = (r: string) =>
      consola.has(r) || literales.some((l) => l.startsWith(prefijoEstable(r)))
    const huerfanas = rutas
      .filter((r) => !localizada(r.ruta))
      .map((r) => `${r.verbo} ${r.ruta} (${r.handler})`)

    // ⚠ DOS EXCEPCIONES MEDIDAS, cada una con su motivo. Ninguna es un salto en blanco.
    //
    // 1. `/v1/console/sources` (PUT/DELETE) no tiene flujo dedicado en la consola: sólo se
    //    consume `runtime/reload`. Está en el motor y no en la interfaz — un hecho del producto,
    //    no un fallo del barrido. Si algún día la consola lo pide, la excepción sobra.
    //
    // 2. `/v1/console/sso/idps/{alias}` (y su `/test`) NO APARECEN como literal en ningún sitio:
    //    la consola los COMPONE — `${ssoConfigPath(scope)}/idps/${encodeURIComponent(alias)}`
    //    (features/console/api.ts:136-142) — así que ni la ruta entera ni su prefijo son texto
    //    buscable. Es el techo del método, no una ausencia de llamante: `sso-tab.tsx` sí conoce
    //    la ceremonia y lo fija `step-up-behind-a-stale-pregate.test.tsx`. Lo que este censo NO
    //    puede afirmar de esas tres rutas es que las haya localizado él.
    const COMPUESTAS_NO_LITERALES = ['/v1/console/sso/idps']
    const SIN_INTERFAZ = ['/v1/console/sources']
    const reales = huerfanas.filter(
      (h) =>
        !SIN_INTERFAZ.some((s) => h.includes(s)) &&
        !COMPUESTAS_NO_LITERALES.some((s) => h.includes(s)),
    )
    expect(reales).toEqual([])

    // ⛔ Y LA EXCEPCIÓN 2 SE JUSTIFICA CON SU CAUSA, no de palabra: si algún día la ruta pasa a
    //    escribirse literal, el compositor desaparece y esta comprobación se pone roja para que
    //    la excepción se retire en vez de quedarse fosilizada.
    const apiConsola = readFileSync(join(AQUI, 'api.ts'), 'utf8')
    expect(apiConsola).toContain('/idps/${encodeURIComponent(alias)}')
  })
})
