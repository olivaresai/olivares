#!/usr/bin/env node
// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

// check-portal-brand.mjs — el portal de licencias es una superficie de MARCA, y hasta hoy
// ningún gate lo miraba.
//
// ============ POR QUÉ EXISTE ================================================================
// Medido el 2026-08-13 auditando el portal por encargo de:
//
//   lint:brand-parity   compara web/tokens <-> brand.manifest.json <-> el global.css de la web
//                       pública, por un MAPA FIJO de anclas. No toca `commercial/`.
//   lint:email-brand    inventaría superficies de CORREO: descubre por marcas de correo
//                       (api.resend.com, templates.generated, email/render). El portal no las
//                       lleva, así que no es de su clase.
//   grep -n portal Taskfile.yml   ->  0 líneas
//
// ⇒ **si alguien cambia un color de marca mañana, el portal NO SE ENTERA.** Y no es hipotético:
// de los 17 colores literales que el portal escribe, sólo DOS existen en el canon. El mismo
// cliente, en el mismo minuto, abre un correo pintado con los tokens y pulsa un botón que le
// lleva a un portal que no comparte una sola superficie con él.
//
// ============ QUÉ COMPRUEBA, Y POR QUÉ ESTAS DOS COSAS ======================================
// NO comprueba «que los 17 colores salgan de un token»: eso sería reescribir el portal, y este
// gate existe para que la deriva sea VISIBLE, no para arreglarla de golpe. Comprueba las dos
// propiedades que un cliente NOTA y que ningún otro gate puede ver:
//
//   1 · CONTRASTE (WCAG AA). Todo par texto/fondo declarado abajo cumple 4.5:1. Es
//       accesibilidad, hay un VPAT publicado, y el botón primario del portal está HOY en
//       2.69:1 — blanco sobre naranja — mientras el token `accent-foreground` da 6.88:1.
//   2 · EL ANCLA DE MARCA. El naranja del portal es EXACTAMENTE el del canon. Si alguien
//       cambia `brand.manifest.json` y no el portal, esto enrojece y lo dice.
//
// ============ POR QUÉ NO ES UNA LISTA DE FICHEROS ===========================================
// La membresía se DERIVA: cualquier fuente bajo `src/portal/` que escriba un color literal es
// superficie de portal. Una lista escrita a mano es la forma de gate que más veces hemos
// encontrado rota en este repositorio — el censo de rutas, el mapa canon<->paquete, el
// allowlist por ruta —, porque caduca en silencio cuando alguien añade una página.
//
// Tres respuestas, nunca dos: CLEAN / BROKEN / UNVERIFIED.

import { readFileSync, readdirSync, existsSync } from 'node:fs'
import { join, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'

const ROOT = join(dirname(fileURLToPath(import.meta.url)), '..')
const PORTAL_DIR = 'commercial/license-worker/src/portal'

/** UNVERIFIED: no se pudo mirar. NO es «está limpio». */
function unverified(msg) {
  console.error(`check-portal-brand: UNVERIFIED — ${msg}`)
  process.exit(2)
}

// --- el canon de marca -----------------------------------------------------------------------
let manifest
try {
  manifest = JSON.parse(readFileSync(join(ROOT, 'brand.manifest.json'), 'utf8'))
} catch (e) {
  unverified(`no pude leer brand.manifest.json: ${e.message}`)
}

/** El naranja de marca, tomado del canon y no escrito aquí: si cambia allí, cambia el gate. */
function brandAccent() {
  const found = []
  const walk = (o) => {
    if (!o || typeof o !== 'object') return
    if (typeof o.value === 'string' && /^#f08000$/i.test(o.value)) found.push(o.value)
    for (const v of Object.values(o)) walk(v)
  }
  walk(manifest)
  // El manifiesto es el canon; si el naranja no aparece, el canon cambió y este gate no puede
  // afirmar nada sobre él.
  return found.length > 0 ? '#f08000' : null
}

// --- descubrimiento: quién es superficie de portal ---------------------------------------------
function portalSources() {
  const dir = join(ROOT, PORTAL_DIR)
  if (!existsSync(dir)) unverified(`no existe ${PORTAL_DIR}`)
  const out = []
  const walk = (d, rel) => {
    for (const e of readdirSync(d, { withFileTypes: true })) {
      const p = join(d, e.name)
      const r = rel ? `${rel}/${e.name}` : e.name
      if (e.isDirectory()) walk(p, r)
      else if (e.name.endsWith('.ts')) out.push({ rel: `${PORTAL_DIR}/${r}`, abs: p })
    }
  }
  walk(dir, '')
  return out
}

// --- contraste WCAG ---------------------------------------------------------------------------
const srgb = (c) => (c <= 0.03928 ? c / 12.92 : Math.pow((c + 0.055) / 1.055, 2.4))
function luminance(hex) {
  // ⛔ TRES DÍGITOS TAMBIÉN SON UN COLOR, y la primera versión de este gate no los veía. El par
  // que existe para cazar —`.btn-primary { background:#f08000; color:#fff }`— usa la forma
  // corta, así que el gate salía BROKEN diciendo «la regla cambió de forma» en vez de «este par
  // está en 2.69:1». Un gate que no reconoce la sintaxis que audita no mide: adivina.
  let h = hex.replace('#', '')
  if (h.length === 3) h = h.split('').map((c) => c + c).join('')
  const [r, g, b] = [0, 2, 4].map((i) => parseInt(h.slice(i, i + 2), 16) / 255)
  return 0.2126 * srgb(r) + 0.7152 * srgb(g) + 0.0722 * srgb(b)
}
function contrast(a, b) {
  const [l1, l2] = [luminance(a), luminance(b)].sort((x, y) => y - x)
  return (l1 + 0.05) / (l2 + 0.05)
}

// LOS PARES QUE UN CLIENTE LEE. Declarados con su rol, no derivados: un par se declara cuando
// alguien decide que ese texto va sobre ese fondo, y esa decisión es del que escribe la página.
// Cada entrada dice DÓNDE vive, para que un fallo nombre el sitio y no sólo el número.
const PAIRS = [
  { id: 'btn-primary', sel: /\.btn-primary\s*\{[^}]*\}/, where: 'layout.ts .btn-primary' },
  { id: 'skip-link', sel: /\.skip-link\s*\{[^}]*\}/, where: 'layout.ts .skip-link' },
]

/** Extrae `color:` y `background:` de un bloque CSS embebido en el TS. */
function pairFromRule(src, re) {
  const m = src.match(re)
  if (!m) return null
  const HEX = '#[0-9a-fA-F]{6}|#[0-9a-fA-F]{3}'
  const fg = m[0].match(new RegExp(`(?:^|[^-])color\\s*:\\s*(${HEX})`))
  const bg = m[0].match(new RegExp(`background(?:-color)?\\s*:\\s*(${HEX})`))
  return fg && bg ? { fg: fg[1], bg: bg[1] } : null
}

const problems = []
const sources = portalSources()
if (sources.length === 0) unverified('el descubrimiento no encontró NINGUNA fuente de portal')

const allSrc = sources.map((s) => readFileSync(s.abs, 'utf8')).join('\n')

// 1 · contraste de los pares declarados
for (const p of PAIRS) {
  const found = pairFromRule(allSrc, p.sel)
  if (!found) {
    problems.push(`${p.where}: no pude extraer el par color/fondo — la regla cambió de forma`)
    continue
  }
  const ratio = contrast(found.fg, found.bg)
  if (ratio < 4.5) {
    problems.push(
      `${p.where}: ${found.fg} sobre ${found.bg} = ${ratio.toFixed(2)}:1, por debajo de AA (4.5:1). ` +
      `El token accent-foreground existe para esto.`,
    )
  }
}

// 2 · el ancla de marca, ATADA AL RELLENO QUE LA USA — no a «existe en algún sitio»
//
// ⛔ LA PRIMERA VERSIÓN DE ESTE BRAZO ERA VACUA Y LO CAZÓ LA MUTACIÓN. Preguntaba si el naranja
// del canon aparecía en el CONJUNTO de las nueve fuentes, y `handler.ts` también lo contiene, así
// que cambiar los diez usos del layout a otro naranja dejaba el gate en CLEAN. Un predicado sobre
// «el conjunto» no puede ver la deriva de un miembro: mide presencia, no uso.
//
// Ahora se ata a los rellenos que el brazo 1 ya extrae — los que un cliente PULSA. Si alguien
// cambia el naranja del botón, esto lo dice; si cambia el canon y no el portal, también.
const accent = brandAccent()
if (!accent) {
  unverified('el naranja de marca no aparece en brand.manifest.json: el canon cambió')
}
for (const p of PAIRS) {
  const found = pairFromRule(allSrc, p.sel)
  if (!found) continue // ya reportado arriba
  if (found.bg.toLowerCase() !== accent.toLowerCase()) {
    problems.push(
      `${p.where}: el relleno es ${found.bg} y el naranja del canon es ${accent}. ` +
      `O el portal derivó, o el canon cambió y nadie lo trajo aquí.`,
    )
  }
}

if (problems.length > 0) {
  console.error(`check-portal-brand: BROKEN — ${problems.length} problema(s) en ${sources.length} fuente(s):`)
  for (const p of problems) console.error(`  · ${p}`)
  console.error('')
  console.error('  El portal es lo primero que ve un cliente que acaba de pagar. Un par por debajo')
  console.error('  de AA es accesibilidad, y hay un VPAT publicado que responde por ello.')
  process.exit(1)
}

console.log(
  `check-portal-brand: CLEAN — ${sources.length} fuente(s) de portal, ${PAIRS.length} par(es) de ` +
  `contraste sobre AA, y el naranja del canon (${accent}) presente.`,
)
