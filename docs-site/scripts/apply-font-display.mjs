// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// apply-font-display — hace EFECTIVO el `display: 'optional'` que astro.config.mjs declara.
//
// ⛔ POR QUÉ HACE FALTA ESTO, y no es una preferencia: la opción de Astro está declarada y NO SE
// APLICA. Medido el 2026-09-01 sobre Astro 6.4.4, en tres pasos y cada uno comprobable:
//
//   1. `@fontsource-variable/inter/index.css` declara `font-display: swap` (7 veces).
//   2. `unifont/dist/index.mjs:9` mapea ese `font-display` del CSS al campo `display` de la cara.
//   3. `astro/dist/assets/fonts/core/collect-component-data.js:19` compone
//      `display: data.display ?? family.display`.
//
// ⇒ El `swap` que viene del PAQUETE gana siempre al `display` que pone el USUARIO. Y el comentario
// de la línea 18 de ese mismo fichero dice literalmente *«User settings override the generated font
// settings»* — es decir, el código hace lo contrario de lo que declara. Verificado además por el
// artefacto: con `display: 'optional'` puesto en las tres familias y la caché de fuentes borrada,
// el build seguía emitiendo `font-display:swap` 19 veces por página.
//
// Se conserva el `display: 'optional'` en la configuración A PROPÓSITO: es la declaración correcta
// de la intención, y el día que Astro arregle la precedencia este paso se vuelve un no-op inocuo.
//
// LO QUE ARREGLA, medido con Chromium sobre el dist servido en local y el régimen fijado por CDP:
//
//   ruta                          swap (antes)   optional (después)
//   /start/quickstart/            0,1552 x5      0,0000 x5
//   /how-to/troubleshooting/      0,1344         0,0000
//   /start/what-is-olivares-ai/   0,1160         0,0000
//   /explanation/work-plane/      0,0988         0,0001
//
// Y la alternativa evidente está REFUTADA: precargar las tres caras críticas deja el CLS en 0,1552
// (una corrida salió peor, 0,4288) y combinado duplica el LCP. No es preload: es `optional`.
//
// TRES RESPUESTAS por código de salida: 0 aplicado · 1 no se aplicó donde debía · 2 no pude mirar.

import { readdir, readFile, writeFile } from 'node:fs/promises'
import { existsSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import path from 'node:path'

const DIST = fileURLToPath(new URL('../dist', import.meta.url))
const FROM = /font-display:\s*swap/g
const TO = 'font-display:optional'

async function htmlFiles(dir, acc = []) {
  for (const e of await readdir(dir, { withFileTypes: true })) {
    const full = path.join(dir, e.name)
    if (e.isDirectory()) await htmlFiles(full, acc)
    else if (e.name.endsWith('.html') || e.name.endsWith('.css')) acc.push(full)
  }
  return acc
}

if (!existsSync(DIST)) {
  console.error('font-display: NO PUDE MIRAR — no existe dist/. Corre `astro build` antes.')
  process.exit(2)
}

const files = await htmlFiles(DIST)
if (files.length === 0) {
  console.error('font-display: NO PUDE MIRAR — dist/ no contiene HTML ni CSS.')
  process.exit(2)
}

let tocados = 0
let sustituciones = 0
for (const f of files) {
  const before = await readFile(f, 'utf8')
  if (!FROM.test(before)) { FROM.lastIndex = 0; continue }
  FROM.lastIndex = 0
  const after = before.replace(FROM, TO)
  const n = (before.match(FROM) ?? []).length
  FROM.lastIndex = 0
  await writeFile(f, after)
  tocados++
  sustituciones += n
}

// CONTROL: después de pasar, no puede quedar NI UNO. Si queda, el paso no hizo su trabajo y decirlo
// «aplicado» sería la misma clase de defecto que esto vino a arreglar.
const restantes = []
for (const f of files) {
  const t = await readFile(f, 'utf8')
  if (/font-display:\s*swap/.test(t)) restantes.push(path.relative(DIST, f))
}
if (restantes.length) {
  console.error(`✗ font-display: quedan ${restantes.length} fichero(s) con \`swap\` tras aplicar.`)
  for (const r of restantes.slice(0, 5)) console.error(`  - ${r}`)
  process.exit(1)
}

if (sustituciones === 0) {
  // No es un fallo: es lo que pasará el día que Astro respete `display` en la configuración.
  console.log('✓ font-display: nada que hacer — el build ya emite `optional` (Astro respetó la configuración).')
  process.exit(0)
}
console.log(`✓ font-display: optional aplicado — ${sustituciones} declaración(es) en ${tocados} fichero(s); cero \`swap\` restantes.`)
process.exit(0)
