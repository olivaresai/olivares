// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// check-client-callers.mjs — ¿tiene LLAMANTE cada método del cliente de la consola?
//
// ⛔ POR QUÉ EXISTE, y lo escribo habiéndome pillado a mí mismo. El 2026-08-17 cerré cinco
//    namespaces de C07-04 añadiendo 97 métodos de cliente con su prueba de contrato — la ruta
//    afirmada, los mutantes muertos, todo verde. Y **ninguno de los 97 tenía una pantalla que lo
//    pulsara**. Mis mensajes de commit decían «get a client», que es cierto; la fila pedía que la
//    operación fuese OPERABLE DESDE LA CONSOLA, y sin llamante no lo es.
//
//    No es una hipótesis: es el mismo defecto que el contraste the model encontró en la pestaña
//    de regops (F4) y que `web/src/features/evals/ab-contract.test.ts:5-9` ya tenía escrito —
//    doce funciones de cliente perfectas, cero llamantes, todas las celdas verdes.
//
// ⚠ Y UNA PRUEBA DE CONTRATO NO CUENTA COMO LLAMANTE. Es deliberado: si contara, el instrumento
//    diría que está resuelto justo cuando lo que hay es cliente y prueba, que es el estado que
//    esto viene a hacer visible. Los ficheros `*.test.*` se excluyen a propósito.
//
// ⚠ LIMITACIÓN DECLARADA, y la sufrí yo mismo el 2026-08-17: esto mide una REFERENCIA en el
//    fuente, no un render ALCANZABLE. Escribí un panel que llamaba tres métodos, olvidé colgarlo
//    de su pestaña, y el contador bajó de 118 a 116 igual — con la pantalla invisible para
//    cualquiera. El `tsc` me salvó («declared but never read»), pero eso sólo funciona porque el
//    componente quedó sin usar: un panel referenciado desde código muerto no daría ni ese aviso.
//
//    No se arregla aquí. Saber si un componente se renderiza de verdad exige alcanzabilidad desde
//    las rutas, que es otro instrumento — y un trinquete que finge medir eso sería peor que uno
//    que declara lo que mide. Lo que este número dice, exactamente: **cuántos métodos de cliente
//    no aparecen en ningún fichero de pantalla**. Es una cota inferior de la deuda, nunca la
//    superior.
//
// Salida: 0 al día · 1 la deuda SUBE · 2 NO HE PODIDO MIRAR (nunca es un verde).
import { readFileSync, readdirSync, statSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

// ⛔ ANCLADO A LA UBICACIÓN DEL PROPIO GUION, NO AL `cwd` DEL QUE LO LLAMA. Con rutas relativas
//    este trinquete mide EL ÁRBOL DESDE DONDE SE LE INVOQUE, y el clon compartido de este
//    repositorio va cientos de commits por detrás.
//
//    Medido el 2026-08-17, y es mi propio defecto: llamándolo con el `cwd` en el clon compartido
//    contestó **651 métodos y 87 huérfanos**; desde el worktree correcto, **711 y 137**. Un
//    trinquete que se equivoca en esta dirección es peor que uno roto: dice «la deuda BAJÓ a 87,
//    apriétala», y apretarla dejaría el gate ROJO PARA SIEMPRE para todos los carriles.
//
//    Es exactamente el defecto que ya arreglé en `scripts/test-format-ratchet.sh` (C15-P7), donde
//    `FORMAT_RATCHET_PKG` era relativo y medía el cwd del llamante. Lo repetí en el guion
//    siguiente. La regla, ahora en dos sitios: un instrumento que mide un árbol se ancla al árbol,
//    nunca al directorio de trabajo.
const _RAIZ_REPO = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const RAIZ = join(_RAIZ_REPO, 'web/src')
const FEATURES = join(RAIZ, 'features')

// ⛔ TRINQUETE. Es la cuenta de HOY, y sólo puede BAJAR. Subirla exige decir en el commit qué
//    método nuevo se queda sin pantalla y por qué — que es justo la conversación que no ocurre
//    sola.
const BASE = 98

function ficheros(dir, out = []) {
  let entradas
  try {
    entradas = readdirSync(dir)
  } catch {
    return out
  }
  for (const e of entradas) {
    const p = join(dir, e)
    let st
    try {
      st = statSync(p)
    } catch {
      continue
    }
    if (st.isDirectory()) ficheros(p, out)
    else if (p.endsWith('.ts') || p.endsWith('.tsx')) out.push(p)
  }
  return out
}

function metodosDeCliente() {
  const defs = []
  for (const p of ficheros(FEATURES)) {
    if (!p.endsWith('/api.ts')) continue
    const s = readFileSync(p, 'utf8')
    for (const m of s.matchAll(/export const (\w*[Aa]pi)\s*=\s*\{/g)) {
      const obj = m.group ?? m[1]
      let i = m.index + m[0].length
      let d = 1
      while (i < s.length && d > 0) {
        if (s[i] === '{') d += 1
        else if (s[i] === '}') d -= 1
        i += 1
      }
      const cuerpo = s.slice(m.index + m[0].length, i)
      for (const mm of cuerpo.matchAll(/^ {2}(\w+):\s*(?:\(|async)/gm)) {
        defs.push({ obj, met: mm[1], def: p })
      }
    }
  }
  return defs
}

const defs = metodosDeCliente()
if (defs.length === 0) {
  console.error(
    'check-client-callers: ⛔ NO HE PODIDO MIRAR: cero métodos de cliente encontrados. ' +
      'El parser dejó de reconocer la forma `export const xApi = { … }` y un barrido que no ' +
      'encuentra nada NO es un árbol limpio.',
  )
  process.exit(2)
}

const fuentes = ficheros(RAIZ)
  .filter((p) => !/\.test\./.test(p))
  .map((p) => [p, readFileSync(p, 'utf8')])

const huerfanos = []
for (const { obj, met, def } of defs) {
  // ⛔ TOLERA EL SALTO DE LÍNEA, y no es cosmética: el idioma de este repo parte las cadenas
  //    largas —`consoleApi\n  .rotateToken(id)`— y un patrón que exigiera el punto PEGADO cuenta
  //    esa llamada como inexistente. Medido el 2026-08-17: `consoleApi.rotateToken` tiene un
  //    llamante completo (confirmación en tono peligro + diálogo de revelado con copia) y este
  //    trinquete lo daba por huérfano. El error va en la dirección PESIMISTA —infla la deuda—,
  //    que es la menos peligrosa de las dos, pero sigue siendo un número falso.
  const pat = new RegExp(`\\b${obj}\\s*\\.\\s*${met}\\b`)
  const hay = fuentes.some(([p, s]) => p !== def && pat.test(s))
  if (!hay) huerfanos.push(`${obj}.${met}  (${def.slice(_RAIZ_REPO.length + 1)})`)
}

const n = huerfanos.length
console.log(
  `check-client-callers: ${defs.length} método(s) de cliente · ${n} sin ninguna pantalla que los llame (línea base ${BASE})`,
)

// ⛔ `--list` NO es comodidad. Hasta ahora la lista sólo se imprimía cuando la deuda SUBÍA, es
//    decir: el instrumento enseñaba el detalle al que la empeora y se lo ocultaba al que viene a
//    bajarla. Quien quiere pagar deuda necesita saber CUÁL, y estaba obligado a reimplementar el
//    barrido por su cuenta para averiguarlo — que es la vía por la que las dos medidas se separan.
//    Es la misma regla que el hub se aplicó a sí mismo esta mañana con `lint:hub-web-fidelity`: un
//    gate sólo debe cobrarte por lo que puedes arreglar, y para arreglarlo hay que poder verlo.
if (process.argv.includes('--list')) {
  const porEspacio = new Map()
  for (const h of huerfanos) {
    const esp = h.split('.')[0]
    if (!porEspacio.has(esp)) porEspacio.set(esp, [])
    porEspacio.get(esp).push(h)
  }
  for (const [esp, lista] of [...porEspacio].sort((a, b) => b[1].length - a[1].length)) {
    console.log(`\n  ${esp}  (${lista.length})`)
    for (const h of lista) console.log(`    ${h.split('  ')[0]}`)
  }
  console.log('')
}

// `--list` es un INVENTARIO, no un veredicto: sale aqui, antes del trinquete. Lo aprendi
// rompiendo la bateria — al meter la linea base delante, `--list` empezo a exigirla y a
// salir 1 sobre un senuelo cuyos huerfanos varian por caso. Un modo que existe para
// ENSENAR lo que hay no puede depender de una linea base que describe lo que se acepta.
if (process.argv.includes('--list')) process.exit(0)

// ⛔ LA AUTORIDAD ES LA LISTA, NO EL NUMERO. `BASE` sigue abajo por continuidad del
// mensaje, pero lo que decide es el CONJUNTO. Un total permite la SUSTITUCION: un carril
// puede anadir dos huerfanos y quitar otros dos y el contador no se mueve mientras el
// conjunto ha cambiado entero. Medido el 2026-08-18 integrando #856 — el delta real
// (`complianceApi.aimsPack`, `complianceApi.fedrampPack`) hubo que calcularlo A MANO
// comparando las listas completas de dos arboles, porque el gate solo sabia decir 102>100.
//
// Fail-closed: sin fichero de linea base esto no es «limpio», es NO HE PODIDO MIRAR (2).
const BASELINE_PATH = new URL('./client-callers-baseline.txt', import.meta.url)
let baseline
try {
  baseline = new Set(
    readFileSync(BASELINE_PATH, 'utf8')
      .split('\n')
      .map((x) => x.trim())
      .filter(Boolean),
  )
} catch (e) {
  console.error(
    'check-client-callers: ⛔ NO HE PODIDO MIRAR: no puedo leer scripts/client-callers-baseline.txt. ' +
      'Sin la lista, «no hay nombres nuevos» seria cierto por vacuidad. ' + String(e.message ?? e),
  )
  process.exit(2)
}

const nombres = huerfanos.map((h) => h.split('  ')[0])
const nuevos = nombres.filter((x) => !baseline.has(x)).sort()
const resueltos = [...baseline].filter((x) => !nombres.includes(x)).sort()

if (nuevos.length > 0) {
  console.error('')
  for (const x of nuevos) console.error(`  ⛔ NUEVO sin llamante: ${x}`)
  console.error('')
  console.error(
    `check-client-callers: ⛔ ${nuevos.length} cliente(s) NUEVOS sin pantalla que los pulse ` +
      `(la lista base tiene ${baseline.size}). Un cliente sin llamante pasa todas sus pruebas de ` +
      'contrato y NO hace la operacion posible desde la consola, que es lo que se pedia. Cablea la ' +
      'pantalla, o anade el nombre a scripts/client-callers-baseline.txt DICIENDO en el commit que ' +
      'queda sin superficie y por que. Se nombran uno a uno a proposito: un numero no dice cual.',
  )
  process.exit(1)
}

if (resueltos.length > 0) {
  console.log('')
  for (const x of resueltos) console.log(`  ✔ ya tiene llamante: ${x}`)
  console.log(
    `check-client-callers: ✔ ${resueltos.length} resuelto(s) — quitalos de ` +
      'scripts/client-callers-baseline.txt EN ESTE MISMO COMMIT: un trinquete que no se aprieta no ' +
      'es un trinquete.',
  )
}

if (n > BASE) {
  console.error('')
  for (const h of huerfanos.slice(0, 40)) console.error(`  sin llamante: ${h}`)
  if (n > 40) console.error(`  … y ${n - 40} más`)
  console.error('')
  console.error(
    `check-client-callers: ⛔ la deuda SUBE (${n} > ${BASE}) — se ha añadido cliente sin ` +
      'pantalla que lo pulse. Un cliente sin llamante pasa todas sus pruebas de contrato y NO ' +
      'hace la operación posible desde la consola, que es lo que se pedía. Cablea la pantalla, ' +
      'o sube la línea base DICIENDO en el commit qué queda sin superficie y por qué.',
  )
  process.exit(1)
}

if (n < BASE) {
  console.log(
    `check-client-callers: ✔ la deuda BAJA — la línea base puede bajar a ${n}. ` +
      'Bájala en este mismo commit: un trinquete que no se aprieta no es un trinquete.',
  )
}
process.exit(0)
