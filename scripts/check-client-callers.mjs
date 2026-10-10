// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// check-client-callers.mjs — does every console client method have a caller?
//
// On 2026-08-17, five C07-04 namespaces gained 97 client methods with passing contract
// tests and killed mutants, but none had a screen caller. Client coverage alone did
// not make the operations usable from the console. The the model regops review (F4)
// and `web/src/features/evals/ab-contract.test.ts:5-9` documented the same failure:
// twelve correct client functions, zero callers, all checks green.
//
// Exclude `*.test.*`: counting contract tests would certify the exact incomplete state
// this census must expose. Count source references, not reachable renders. A panel
// calling three methods reduced the count from 118 to 116 despite never being mounted
// on its tab. tsc caught that unused component, but references from dead code may pass.
// Route reachability needs another instrument. This count reports methods absent from
// all screen files and is a lower bound on debt.
// Exit: 0 current · 1 debt increases · 2 could not check.
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
const BASE = 97

function source_files(dir, out = []) {
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
    if (st.isDirectory()) source_files(p, out)
    else if (p.endsWith('.ts') || p.endsWith('.tsx')) out.push(p)
  }
  return out
}

function metodosDeCliente() {
  const defs = []
  for (const p of source_files(FEATURES)) {
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
    'check-client-callers: ⛔ COULD NOT LOOK: no client methods found. ' +
      'The parser no longer recognizes `export const xApi = { … }`; a scan that ' +
      'finds nothing does NOT demonstrate a clean tree.',
  )
  process.exit(2)
}

const fuentes = source_files(RAIZ)
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
  `check-client-callers: ${defs.length} client method(s) · ${n} with no screen calling them (baseline ${BASE})`,
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
    'check-client-callers: ⛔ COULD NOT LOOK: cannot read scripts/client-callers-baseline.txt. ' +
      'Without the list, claiming there are no new names would be vacuously true. ' + String(e.message ?? e),
  )
  process.exit(2)
}

const nombres = huerfanos.map((h) => h.split('  ')[0])
const nuevos = nombres.filter((x) => !baseline.has(x)).sort()
const resueltos = [...baseline].filter((x) => !nombres.includes(x)).sort()

if (nuevos.length > 0) {
  console.error('')
  for (const x of nuevos) console.error(`  ⛔ NEW with no caller: ${x}`)
  console.error('')
  console.error(
    `check-client-callers: ⛔ ${nuevos.length} NEW client method(s) without a screen calling them ` +
      `(the baseline has ${baseline.size}). A client without a caller passes all its ` +
      'contract tests but does NOT make the operation available from the console as required. Wire up the ' +
      'screen, or add the name to scripts/client-callers-baseline.txt and STATE in the commit what ' +
      'remains unavailable and why. Names are listed individually because a count cannot identify them.',
  )
  process.exit(1)
}

if (resueltos.length > 0) {
  console.log('')
  for (const x of resueltos) console.log(`  ✔ now has a caller: ${x}`)
  console.log(
    `check-client-callers: ✔ ${resueltos.length} resolved — remove them from ` +
      'scripts/client-callers-baseline.txt IN THIS COMMIT: a ratchet that is not tightened is not ' +
      'a ratchet.',
  )
}

if (n > BASE) {
  console.error('')
  for (const h of huerfanos.slice(0, 40)) console.error(`  no caller: ${h}`)
  if (n > 40) console.error(`  … and ${n - 40} more`)
  console.error('')
  console.error(
    `check-client-callers: ⛔ debt INCREASED (${n} > ${BASE}) — a client method was added without a ` +
      'screen calling it. A client without a caller passes all its contract tests but does NOT ' +
      'make the operation available from the console as required. Wire up the screen, ' +
      'or increase the baseline and STATE in the commit what remains unavailable and why.',
  )
  process.exit(1)
}

if (n < BASE) {
  console.log(
    `check-client-callers: ✔ debt DECREASED — the baseline can be reduced to ${n}. ` +
      'Lower it in this commit so the ratchet keeps enforcing the measured baseline.',
  )
}
process.exit(0)
