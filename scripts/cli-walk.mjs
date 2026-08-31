// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// cli-walk.mjs — recorre TODOS los mandatos del binario y reporta lo que un operador se
// encontraría. El hermano de `console-walk.mjs`, para la otra mitad del producto.
//
// ⛔ POR QUÉ EXISTE. `console:walk` abre un navegador contra un motor vivo y por eso ve lo que
//    ningún test unitario ve: un cliente tipado llamando a un endpoint que el motor NUNCA
//    REGISTRA — la consola pregunta, el motor responde 404, y todos los tests siguen verdes
//    porque el mock contestó. **Para el CLI no existía nada equivalente**: medido el 2026-08-19,
//    185 mandatos en tres niveles y ninguna herramienta los recorría contra un motor.
//
// ⛔⛔ Y LA LECCIÓN QUE ESTE GUION LLEVA DENTRO ES UN ERROR MÍO, del mismo día. La primera pasada
//     a mano corrió los mandatos de sólo lectura contra el motor y dio «0 hallazgos en 33». Falso:
//     **25 de los 33 salieron con usage error por falta de argumentos y NUNCA TOCARON EL MOTOR**.
//     Reportar eso como cobertura es exactamente el test vacuo que este repositorio lleva el día
//     cazando. Por eso aquí **el ALCANCE es parte del veredicto**: se dice cuántos mandatos
//     llegaron de verdad al motor, y un recorrido que no alcanza a ninguno **no es limpio, es
//     ciego** (rc=2).
//
// ⛔ Y LA SEGUNDA LECCIÓN, del mismo día y peor: crucé los literales `/v1/…` del CLI contra el
//    `/openapi.json` del motor y saqué «21 rutas no registradas». Se lo pregunté al motor y
//    cuatro de las que miré respondían **401** — registradas y guardadas. El núcleo publica 53
//    rutas estables y **cero de módulo**; las 522 de módulo viven en `/openapi.beta.json`, que el
//    motor TAMBIÉN sirve. ⇒ **La verdad la tiene el motor, no un documento.** Este guion clasifica
//    por el CÓDIGO QUE DEVUELVE la ruta: 404 es un hallazgo, 401/403 es una puerta funcionando.
//
// TRES RESPUESTAS: 0 limpio · 1 hallazgos · 2 no he podido mirar.
//
// USO
//   OLIVARES_CLI_BIN=/ruta/al/olivares [OLIVARES_CLI_BASE=https://127.0.0.1:8443] \
//   [OLIVARES_CLI_PIN=<pin_sha256>] node scripts/cli-walk.mjs
//
//   Sin OLIVARES_CLI_BASE recorre sólo el contrato local (ayuda + códigos de salida) y lo DICE.
import { execFileSync } from 'node:child_process'
import { existsSync } from 'node:fs'

const BIN = process.env.OLIVARES_CLI_BIN || ''
const BASE = process.env.OLIVARES_CLI_BASE || ''
const PIN = process.env.OLIVARES_CLI_PIN || ''
const PROFUNDIDAD = Number(process.env.OLIVARES_CLI_DEPTH || '3')

function morir(msg) {
  console.error(`cli-walk: NO HE PODIDO MIRAR — ${msg}`)
  process.exit(2)
}
if (!BIN) morir('falta OLIVARES_CLI_BIN. Sin binario no hay nada que recorrer, y eso no es un verde.')
if (!existsSync(BIN)) morir(`OLIVARES_CLI_BIN=${BIN} no existe.`)

function correr(args, timeout = 90_000) {
  try {
    const out = execFileSync(BIN, args, { encoding: 'utf8', timeout, stdio: ['ignore', 'pipe', 'pipe'] })
    return { rc: 0, out, err: '' }
  } catch (e) {
    if (e.code === 'ETIMEDOUT' || e.signal) return { rc: -1, out: '', err: `tiempo agotado tras ${timeout} ms` }
    return { rc: typeof e.status === 'number' ? e.status : -1, out: e.stdout || '', err: e.stderr || '' }
  }
}

// ── descubrimiento: del propio binario, NUNCA de una lista escrita a mano ────────────────────
// Una lista a mano envejece el día que alguien añade un mandato, y el recorrido sigue verde sin
// haberlo visto. Es el mismo motivo por el que console-walk deriva sus rutas de la navegación.
// Guarda la ayuda de cada mandato: se pide UNA vez y se usa para descubrir hijos, para comprobar
// el contrato y para saber si el mandato habla por RED. Pedirla tres veces sería tres veces el
// coste y, peor, tres oportunidades de que las tres pasadas no coincidan.
const ayudaDe = new Map()
function ayuda(ruta) {
  const k = ruta.join(' ')
  if (!ayudaDe.has(k)) ayudaDe.set(k, correr([...ruta, '--help'], 60_000))
  return ayudaDe.get(k)
}

function hijos(ruta) {
  const { rc, out } = ayuda(ruta)
  if (rc !== 0) return []
  const cmds = []
  let dentro = false
  for (const linea of out.split('\n')) {
    if (/^[A-Z][^\n]*:\s*$/.test(linea) && !/^(Usage|Flags|Global Flags|Examples)/.test(linea)) { dentro = true; continue }
    if (/^(Flags|Global Flags|Usage|Examples|Additional help)/.test(linea)) { dentro = false; continue }
    if (!dentro) continue
    const m = /^ {2}([a-z][a-z0-9-]*) {2,}/.exec(linea)
    if (m && m[1] !== 'help' && m[1] !== 'completion') cmds.push(m[1])
  }
  return [...new Set(cmds)].sort()
}

const mandatos = []
const pila = [[]]
while (pila.length) {
  const r = pila.pop()
  for (const c of hijos(r)) {
    const n = [...r, c]
    mandatos.push(n)
    if (n.length < PROFUNDIDAD) pila.push(n)
  }
}
if (mandatos.length === 0) morir('cero mandatos descubiertos. Un recorrido vacío no es limpio.')
console.log(`cli-walk: ${mandatos.length} mandato(s) descubierto(s) del propio binario (profundidad ${PROFUNDIDAD}).`)

// ── 1. el contrato que el binario PUBLICA en su propia ayuda ─────────────────────────────────
// «2 usage error (unknown flag or bad arguments)» — es un contrato escrito, así que se comprueba.
const hallazgos = []
let ayudaOK = 0
let usageOK = 0
for (const r of mandatos) {
  const h = ayuda(r)
  if (h.rc !== 0 || !h.out.includes('Usage:')) {
    hallazgos.push({ tipo: 'ayuda', cmd: r.join(' '), detalle: `--help rc=${h.rc}${h.out.includes('Usage:') ? '' : ', sin línea Usage:'}` })
  } else ayudaOK++
  const f = correr([...r, '--esta-bandera-no-existe-jamas'], 60_000)
  if (f.rc !== 2) {
    hallazgos.push({ tipo: 'usage', cmd: r.join(' '), detalle: `bandera desconocida devolvió ${f.rc}, el binario promete 2` })
  } else usageOK++
}
console.log(`cli-walk: contrato local — ayuda ${ayudaOK}/${mandatos.length}, «bandera desconocida ⇒ 2» ${usageOK}/${mandatos.length}.`)

// ── 2. contra un motor vivo: 404 es hallazgo, 401/403 es una puerta funcionando ───────────────
let alcanzados = 0
let noAlcanzados = 0
if (!BASE) {
  console.log('cli-walk: SIN OLIVARES_CLI_BASE — no se ha recorrido nada contra un motor. El contrato')
  console.log('          local NO cubre «el CLI llama a una ruta que el motor no registra», que es')
  console.log('          justo lo que esta herramienta existe para ver.')
} else {
  // ⛔ NO TODO MANDATO HABLA POR RED, y suponerlo fue un error medido el 2026-08-19: la primera
  //    versión pasaba `--server` a todos los de lectura, y `audit ls`, `connector ls`, `keys ls` y
  //    `migrate status` respondían «unknown flag: --server» — son mandatos LOCALES, sobre el
  //    directorio de datos o la base, no sobre el plano de control. El recorrido los contaba como
  //    «les faltan argumentos», que es una causa INVENTADA: el argumento sobraba, no faltaba.
  //    ⇒ Se pregunta a la ayuda de cada mandato si declara `--server`. Quien no lo declara no es
  //      candidato a hablar con el motor y NO se cuenta ni como alcanzado ni como fallo.
  const LECTURA = new Set(['ls', 'list', 'get', 'status', 'show', 'inspect', 'check', 'versions', 'stat'])
  const deRed = (r) => /^\s+--server\s/m.test(ayuda(r).out)
  const lectura = mandatos.filter((r) => LECTURA.has(r[r.length - 1]))
  const locales = lectura.filter((r) => !deRed(r))
  const candidatos = lectura.filter(deRed)
  for (const r of candidatos) {
    const args = [...r, '--server', BASE]
    if (PIN) args.push('--pin-sha256', PIN)
    const p = correr(args, 90_000)
    const texto = `${p.out}\n${p.err}`
    // 2 = usage con las banderas de red puestas: le faltan OTROS argumentos, así que no llegó.
    if (p.rc === 2) { noAlcanzados++; continue }
    alcanzados++
    if (/\b(404|501)\b/.test(texto) || /not found|no such route|unknown endpoint/i.test(texto)) {
      hallazgos.push({ tipo: 'ruta', cmd: r.join(' '), detalle: texto.trim().replace(/\s+/g, ' ').slice(0, 200) })
    }
  }
  console.log(`cli-walk: contra el motor — ${alcanzados} de ${candidatos.length} mandato(s) de RED llegaron;`)
  console.log(`          ${noAlcanzados} se quedaron en usage error por argumentos que este recorrido no sabe`)
  console.log(`          inventar (NO son cobertura, y decirlo es el punto de esta línea).`)
  console.log(`cli-walk: ${locales.length} mandato(s) de lectura son LOCALES (no declaran --server): fuera de`)
  console.log(`          alcance de la mitad viva, y no se cuentan ni a favor ni en contra.`)
  if (alcanzados === 0) {
    console.error('cli-walk: NO HE PODIDO MIRAR — con motor configurado, NINGÚN mandato llegó a él.')
    console.error('          Un recorrido que no alcanza el sujeto no es limpio: es ciego.')
    process.exit(2)
  }
}

if (hallazgos.length > 0) {
  console.error(`cli-walk: ⛔ ${hallazgos.length} hallazgo(s):`)
  for (const h of hallazgos) console.error(`    [${h.tipo}] ${h.cmd} — ${h.detalle}`)
  process.exit(1)
}
console.log(`cli-walk: LIMPIO — ${mandatos.length} mandato(s), ${alcanzados} contra el motor${BASE ? '' : ' (0: sin motor)'}.`)
process.exit(0)
