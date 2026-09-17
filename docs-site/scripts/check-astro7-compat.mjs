#!/usr/bin/env node
// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit
// liability: see DISCLAIMER.md at the repository root.
//
// test:astro7-compat — bounded Astro 7.3.2 / Starlight 0.42 assertions on the
// lockfile, config and built dist/. Exit 0 clean, 1 finding, 2 could not look.

import { readFile } from 'node:fs/promises'
import { existsSync, readdirSync, readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const here = (p) => fileURLToPath(new URL(p, import.meta.url))
const DIST = here('../dist')
const NM = here('../node_modules')

const fail = (m) => {
  console.error(`✗ astro7-compat: ${m}`)
  process.exit(1)
}
const cannot = (m) => {
  console.error(`astro7-compat: NO PUDE MIRAR — ${m}`)
  process.exit(2)
}

if (!existsSync(DIST)) cannot('no existe dist/. Corre `astro build` antes.')

function pkgVersion(name) {
  const p = `${NM}/${name}/package.json`
  if (!existsSync(p)) cannot(`falta ${name}/package.json en node_modules`)
  try {
    return JSON.parse(readFileSync(p, 'utf8')).version
  } catch (err) {
    cannot(`no pude leer ${name}/package.json: ${err.message}`)
  }
}

const resolved = {
  astro: pkgVersion('astro'),
  starlight: pkgVersion('@astrojs/starlight'),
  openapi: pkgVersion('starlight-openapi'),
  versions: pkgVersion('starlight-versions'),
  sitemap: pkgVersion('@astrojs/sitemap'),
  satteri: pkgVersion('@astrojs/markdown-satteri'),
}

if (resolved.astro !== '7.3.2') fail(`astro resuelto ${resolved.astro}, se exige 7.3.2`)
if (resolved.starlight !== '0.42.0') fail(`starlight resuelto ${resolved.starlight}, se exige 0.42.0`)
if (resolved.openapi !== '0.26.2') fail(`starlight-openapi resuelto ${resolved.openapi}, se exige 0.26.2`)
if (resolved.versions !== '0.10.1') fail(`starlight-versions resuelto ${resolved.versions}, se exige 0.10.1`)
if (resolved.sitemap !== '3.7.4') fail(`@astrojs/sitemap resuelto ${resolved.sitemap}, se exige 3.7.4`)
if (resolved.satteri !== '0.4.1') fail(`@astrojs/markdown-satteri resuelto ${resolved.satteri}, se exige 0.4.1`)

const config = await readFile(here('../astro.config.mjs'), 'utf8')
if (!/^[\t ]*compressHTML:\s*true,?$/m.test(config)) {
  fail('astro.config.mjs debe fijar compressHTML: true (whitespace HTML de Astro 6)')
}
if (!config.includes("from '@astrojs/sitemap'")) {
  fail('astro.config.mjs debe importar @astrojs/sitemap de forma directa')
}

const required = [
  'start/quickstart/index.html',
  'es/start/quickstart/index.html',
  'zh/start/quickstart/index.html',
  'ru/start/quickstart/index.html',
  'ja/start/quickstart/index.html',
  'de/start/quickstart/index.html',
  'fr/start/quickstart/index.html',
  'reference/api/index.html',
  'reference/api-beta/index.html',
  '2026-06/start/quickstart/index.html',
  'es/2026-06/start/quickstart/index.html',
]
for (const rel of required) {
  if (!existsSync(`${DIST}/${rel}`)) fail(`falta la ruta construida ${rel}`)
}

function html(rel) {
  return readFile(`${DIST}/${rel}`, 'utf8')
}

const en = await html('start/quickstart/index.html')
if (!en.includes('sl-menu-button')) fail('Starlight 0.42: falta .sl-menu-button en quickstart')
if (en.includes('starlight-menu-button')) fail('sigue el markup móvil de Starlight 0.39 (starlight-menu-button)')
if (en.includes('data-mobile-menu-expanded')) fail('sigue data-mobile-menu-expanded, retirado en Starlight 0.42')
if (!en.includes('popovertarget="starlight__sidebar"')) {
  fail('el botón de menú móvil no apunta al popover starlight__sidebar')
}
if (!en.includes('pagefind')) fail('falta pagefind en quickstart')
if (!en.includes('starlight-theme-select')) fail('falta el selector de tema')
if (!en.includes('--olv-font')) fail('faltan las variables de fuente de marca')
if (!en.includes('font-display:optional')) fail('el artefacto no emite font-display:optional')
if (/font-display:\s*swap/.test(en)) fail('queda font-display:swap en quickstart')
if (!en.includes('Olivares')) fail('falta la marca Olivares en quickstart')
if (!en.includes('lang="en"')) fail('quickstart inglés no declara lang=en')

const es = await html('es/start/quickstart/index.html')
if (!es.includes('lang="es"')) fail('quickstart es no declara lang=es')
if (!es.includes('sl-mt-banner')) fail('falta el aviso de traducción en /es/start/quickstart/')
if (!es.includes('Traducción automática')) {
  fail('el aviso de traducción no conserva el texto español')
}

const archived = await html('2026-06/start/quickstart/index.html')
if (!archived.includes('name="robots" content="noindex, follow"')) {
  fail('el archivo 2026-06 no lleva noindex, follow')
}
const liveRobots = /name="robots" content="noindex/.test(en)
if (liveRobots) fail('quickstart vivo no debe llevar noindex')

const sitemap0 = `${DIST}/sitemap-0.xml`
if (!existsSync(sitemap0)) cannot('falta dist/sitemap-0.xml')
const sitemap = await readFile(sitemap0, 'utf8')
if (sitemap.includes('2026-06')) fail('el sitemap incluye URLs archivadas /2026-06/')

const apiOps = `${DIST}/reference/api/operations`
const betaOps = `${DIST}/reference/api-beta/operations`
if (!existsSync(apiOps)) fail('falta dist/reference/api/operations')
if (!existsSync(betaOps)) fail('falta dist/reference/api-beta/operations')
const apiCount = readdirSync(apiOps).filter((n) => n.endsWith('.html') || existsSync(`${apiOps}/${n}/index.html`)).length
const betaCount = readdirSync(betaOps).filter((n) => n.endsWith('.html') || existsSync(`${betaOps}/${n}/index.html`)).length
if (apiCount < 1) fail('la referencia estable no tiene operaciones renderizadas')
if (betaCount < 1) fail('la referencia beta no tiene operaciones renderizadas')

console.log(
  `✓ astro7-compat: astro ${resolved.astro}, starlight ${resolved.starlight}, ` +
    `openapi ${resolved.openapi}, versions ${resolved.versions}; ` +
    `compressHTML true; menú 0.42; noindex archivo; 7 locales; API ${apiCount}+${betaCount} ops.`,
)
process.exit(0)
