#!/usr/bin/env node
// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// test:structured-data — el grafo que este sitio publica, afirmado sobre el ARTEFACTO.
//
// ⛔ POR QUE EXISTE. Hasta el 2026-09-01 este sitio emitia CERO datos estructurados en sus 2.156
// paginas, mientras el sitio principal publicaba un grafo cuidado. Para un motor, las dos
// propiedades no eran la misma organizacion: nada aqui decia de quien es esto.
//
// Mira `dist/`, no las fuentes: un componente que deja de renderizar no cambia una sola linea de
// fuente, y esta clase de defecto —una declaracion que nadie cumple— es la que este arbol lleva
// todo el dia pagando.
//
// TRES RESPUESTAS por codigo de salida (canon §1.5): 0 limpio · 1 hallazgo · 2 NO PUDE MIRAR.

import { readFile } from 'node:fs/promises'
import { existsSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

import { OG_CLASS_CARDS } from '../src/site-locales.mjs'

const DIST = fileURLToPath(new URL('../dist', import.meta.url))
const APEX_ORG = 'https://olivares.ai/#org'
const LD = /<script type="application\/ld\+json">([\s\S]*?)<\/script>/g

/** Las tres paginas que sostienen las tres afirmaciones, y la cuarta es el control contrario. */
const CASOS = [
  { file: 'index.html', esperado: ['WebSite'], nota: 'la home' },
  { file: 'how-to/troubleshooting/index.html', esperado: ['WebSite', 'BreadcrumbList'], nota: 'una pagina profunda' },
  { file: 'es/start/quickstart/index.html', esperado: ['WebSite', 'BreadcrumbList'], nota: 'un locale traducido' },
  { file: '2026-06/reference/cli/index.html', esperado: [], nota: 'el archivo noindex — CONTROL CONTRARIO' },
]

function tipos(html) {
  const out = []
  for (const m of html.matchAll(LD)) {
    const j = JSON.parse(m[1])
    for (const n of j['@graph'] ?? [j]) out.push(n['@type'])
  }
  return out
}

if (!existsSync(DIST)) {
  console.error('structured-data: NO PUDE MIRAR — no existe dist/. Corre `astro build` antes.')
  process.exit(2)
}

const hallazgos = []

// ⛔ LA DECLARACION Y EL FICHERO, EN LAS DOS DIRECCIONES. Una clase declarada sin su PNG emite una
// `og:image` que da 404 —peor que la tarjeta generica—; un PNG sin declarar es trabajo del carril
// de marca que nadie ha enchufado y que nadie va a notar. Las dos mitades hacen falta: comprobar
// solo una deja pasar la otra en silencio.
for (const clase of OG_CLASS_CARDS) {
  if (!existsSync(`${DIST}/og/${clase}.png`)) {
    hallazgos.push(`la clase «${clase}» esta declarada en site-locales.mjs pero no hay dist/og/${clase}.png — su og:image daria 404`)
  }
}
if (existsSync(`${DIST}/og`)) {
  const { readdirSync } = await import('node:fs')
  for (const f of readdirSync(`${DIST}/og`)) {
    const clase = f.replace(/\.png$/, '')
    if (f.endsWith('.png') && !OG_CLASS_CARDS.includes(clase)) {
      hallazgos.push(`hay dist/og/${f} pero «${clase}» no esta en OG_CLASS_CARDS: la tarjeta existe y no la usa nadie`)
    }
  }
}

for (const caso of CASOS) {
  const path = `${DIST}/${caso.file}`
  if (!existsSync(path)) {
    console.error(`structured-data: NO PUDE MIRAR — falta ${caso.file}, que es el sujeto de una afirmacion.`)
    process.exit(2)
  }
  const html = await readFile(path, 'utf8')
  const t = tipos(html)
  if (JSON.stringify(t) !== JSON.stringify(caso.esperado)) {
    hallazgos.push(`${caso.nota} (${caso.file}): esperaba [${caso.esperado.join(', ')}], hay [${t.join(', ')}]`)
    continue
  }
  if (caso.esperado.length === 0) continue

  const j = JSON.parse([...html.matchAll(LD)][0][1])
  const web = j['@graph'].find((n) => n['@type'] === 'WebSite')
  if (web?.publisher?.['@id'] !== APEX_ORG) {
    hallazgos.push(`${caso.nota}: el WebSite no referencia la Organization del sitio principal (${APEX_ORG}) — sin eso, las dos propiedades no se declaran la misma entidad`)
  }
  if (j['@graph'].some((n) => n['@type'] === 'Organization')) {
    hallazgos.push(`${caso.nota}: republica una Organization. El sitio principal ya la publica con su gate; una segunda copia envejece por separado`)
  }
  const bc = j['@graph'].find((n) => n['@type'] === 'BreadcrumbList')
  if (bc) {
    for (const item of bc.itemListElement) {
      // ⛔ Una miga que enlaza un 404 promete una jerarquia navegable que no existe. Hoy
      // `/how-to/`, `/start/`, `/tutorials/` y `/reference/modules/` NO se construyen.
      const rel = item.item.replace('https://docs.olivares.ai/', '')
      const dest = rel === '' ? 'index.html' : `${rel}index.html`
      if (!existsSync(`${DIST}/${dest}`)) {
        hallazgos.push(`${caso.nota}: la miga «${item.name}» apunta a ${item.item}, que NO se construye`)
      }
    }
  }
}

if (hallazgos.length) {
  console.error('✗ structured-data: el grafo publicado no es el que se decidio.\n')
  for (const h of hallazgos) console.error(`  - ${h}`)
  console.error('\n  El grafo se emite en src/components/Head.astro, con el porque de cada mitad.')
  process.exit(1)
}
console.log(`✓ structured-data: OK — WebSite con publisher al apex, BreadcrumbList sin destinos rotos, y el archivo noindex sin grafo (${CASOS.length} casos).`)
process.exit(0)
