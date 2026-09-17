#!/usr/bin/env node
// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// test:robots — the published robots.txt says what the project decided it says.
//
// ⛔ POR QUÉ EXISTE. Hasta el 2026-09-01 este sitio no servía `robots.txt` propio:
// lo que respondía en producción era el fichero de "content signals" que Cloudflare
// inyecta cuando el origen no sirve ninguno — 1248 bytes, 24 líneas y CERO
// directivas. Nadie lo notó porque NADA lo comprobaba. El efecto medido: el sitemap
// (2156 URLs) no se anunciaba en ninguna parte, y la postura anti-entrenamiento que
// `olivares.ai` aplica a veinte rastreadores no regía sobre las 2156 páginas de
// documentación de producto.
//
// Un fichero en `public/` es una promesa que sólo se cumple si el build lo copia,
// así que esto NO mira `public/`: mira `dist/`, que es lo que se despliega. Es la
// misma lección que dejó escrita `check-hub-fidelity.mjs` en el repo web — la ruta
// de publicación es la que hay que vigilar, no la de merge.
//
// TRES RESPUESTAS, y las da el CÓDIGO DE SALIDA, no la prosa (canon §1.5):
//   0  limpio · 1  hallazgo · 2  NO HE PODIDO MIRAR
// `--self-test` fuerza las dos mitades y comprueba que cada una sale con su código.

import { readFile } from 'node:fs/promises'
import { existsSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const DIST_ROBOTS = fileURLToPath(new URL('../dist/robots.txt', import.meta.url))
const SITEMAP = 'https://docs.olivares.ai/sitemap-index.xml'

// La misma lista que publica el sitio principal en su propio `robots.txt`. Se restata aquí, y no
// se deriva, porque el otro repositorio NO está en disco cuando esto corre: una derivación que no
// puede leer su fuente respondería "limpio" sin haber mirado.
//
// ⚠ Y HAY QUE DECIR LO QUE ESTO **NO** GARANTIZA, porque la versión anterior de este comentario
// afirmaba que «si la política del apex cambia, cambian las dos listas en el mismo commit» — y eso
// es imposible: son dos repositorios independientes con dos historias. Esta puerta comprueba que
// ESTE fichero cumple la lista de abajo; **no puede ver** si el sitio principal se ha movido. La
// paridad entre los dos hosts sólo la puede cerrar una comprobación con los dos árboles delante, y
// mientras no exista, la divergencia es un riesgo declarado, no uno cubierto.
const MUST_BLOCK = [
  'GPTBot', 'Google-Extended', 'CCBot', 'Bytespider', 'Applebot-Extended',
  'meta-externalagent', 'Meta-ExternalAgent', 'FacebookBot', 'cohere-ai',
  'cohere-training-data-crawler', 'Diffbot', 'Omgili', 'Omgilibot',
  'ImagesiftBot', 'YouBot', 'Timpibot', 'PanguBot', 'Webzio-Extended',
  'AI2Bot', 'Ai2Bot-Dolma',
]

/**
 * Parse robots.txt into groups. A blank line ends a group; consecutive
 * `User-agent:` lines share one rule block, which is what the standard says and
 * what a naive "last user-agent wins" parser gets wrong.
 * @returns {{agents: string[], rules: {directive: string, value: string}[]}[]}
 */
function parseGroups(text) {
  const groups = []
  let current = null
  let expectingAgents = true
  for (const raw of text.split('\n')) {
    const line = raw.replace(/#.*$/, '').trim()
    if (!line) { current = null; expectingAgents = true; continue }
    const idx = line.indexOf(':')
    if (idx === -1) continue
    const directive = line.slice(0, idx).trim().toLowerCase()
    const value = line.slice(idx + 1).trim()
    if (directive === 'user-agent') {
      if (!current || !expectingAgents) { current = { agents: [], rules: [] }; groups.push(current) }
      current.agents.push(value)
      expectingAgents = true
    } else if (directive === 'sitemap') {
      groups.push({ agents: ['__sitemap__'], rules: [{ directive, value }] })
      current = null; expectingAgents = true
    } else if (current) {
      current.rules.push({ directive, value })
      expectingAgents = false
    }
  }
  return groups
}

/** @returns {string[]} findings — empty means clean. */
function audit(text) {
  const found = []
  const groups = parseGroups(text)

  const sitemaps = groups.flatMap((g) => g.rules).filter((r) => r.directive === 'sitemap').map((r) => r.value)
  if (!sitemaps.includes(SITEMAP)) {
    found.push(`no declara \`Sitemap: ${SITEMAP}\` (encontrados: ${sitemaps.length ? sitemaps.join(', ') : 'ninguno'})`)
  }

  const catchAll = groups.find((g) => g.agents.includes('*'))
  if (!catchAll) found.push('no hay grupo `User-agent: *` — el genérico es el que leen Googlebot y los motores de respuesta')
  else {
    // ⛔ NO BASTA CON RECHAZAR `Disallow: /`. Esta comprobación sólo miraba eso, así que un
    // catch-all SIN `Allow: /`, o con un `Disallow: /alguna-ruta`, pasaba en verde mientras
    // sacaba páginas del índice. Se exige lo que de verdad se decidió: catch-all ABIERTO.
    const disallows = catchAll.rules.filter((r) => r.directive === 'disallow' && r.value !== '')
    if (disallows.length) {
      found.push(
        `el grupo \`User-agent: *\` bloquea ${disallows.length} ruta(s) — este sitio se publica entero: ` +
          disallows.map((r) => `Disallow: ${r.value}`).join(' · '),
      )
    }
    if (!catchAll.rules.some((r) => r.directive === 'allow' && r.value === '/')) {
      found.push('el grupo `User-agent: *` no declara `Allow: /` — la apertura del sitio debe estar dicha, no supuesta')
    }
  }

  // ⛔ «TIENE UN Disallow: /» NO ES «ESTÁ BLOQUEADO». Un grupo con `Disallow: /` Y `Allow: /`
  // deja al bot dentro por la regla más específica, y la versión anterior lo contaba como
  // bloqueado. Un bot sólo cuenta si su grupo bloquea la raíz y NO la vuelve a abrir.
  const blocked = new Set(
    groups
      .filter(
        (g) =>
          g.rules.some((r) => r.directive === 'disallow' && r.value === '/') &&
          !g.rules.some((r) => r.directive === 'allow' && (r.value === '/' || r.value === '*')),
      )
      .flatMap((g) => g.agents),
  )
  const missing = MUST_BLOCK.filter((ua) => !blocked.has(ua))
  if (missing.length) {
    found.push(`${missing.length} rastreador(es) de entrenamiento sin bloquear, contra la política del apex: ${missing.join(', ')}`)
  }

  // ClaudeBot es la EXCEPCIÓN declarada del proyecto: si apareciera bloqueado,
  // alguien habría cambiado la política sin decirlo.
  if (blocked.has('ClaudeBot')) found.push('ClaudeBot aparece bloqueado — el proyecto lo permite a propósito; esto es un cambio de política sin declarar')

  return found
}

if (process.argv.includes('--exit-probe')) {
  // Modo interno del self-test: audita el fichero que le pasen y sale con el código real, para
  // que el control de abajo pueda medir CÓDIGOS DE SALIDA en vez de valores de retorno.
  const target = process.argv[process.argv.indexOf('--exit-probe') + 1]
  if (!target || !existsSync(target)) {
    console.error('robots: NO HE PODIDO MIRAR — la sonda no encuentra el fichero.')
    process.exit(2)
  }
  const findings = audit(await readFile(target, 'utf8'))
  if (findings.length) { console.error('✗ ' + findings.join('\n  - ')); process.exit(1) }
  console.log('✓ limpio'); process.exit(0)
}

if (process.argv.includes('--self-test')) {
  // CONTROL POSITIVO POR CADA MITAD. Sin esto, un `audit()` que devolviera
  // siempre `[]` pasaría, y un gate que no puede fallar no es un gate.
  const good = await readFile(new URL('../public/robots.txt', import.meta.url), 'utf8')
  const clean = audit(good)
  if (clean.length) { console.error(`✗ self-test: el robots.txt REAL sale con hallazgos y no debería:\n  - ${clean.join('\n  - ')}`); process.exit(1) }

  const mutants = [
    ['sin la línea Sitemap', good.replace(/^Sitemap:.*$/m, '')],
    ['sin el bloqueo de GPTBot', good.replace(/User-agent: GPTBot\nDisallow: \/\n/, '')],
    ['con el catch-all cerrado', good.replace('User-agent: *\nAllow: /', 'User-agent: *\nDisallow: /')],
    ['con ClaudeBot bloqueado', good.replace('User-agent: ClaudeBot\nAllow: /', 'User-agent: ClaudeBot\nDisallow: /')],
    // Los dos siguientes son los que el contraste demostró que se colaban con la versión anterior
    // del predicado. Sin ellos, el endurecimiento sería una afirmación sin control.
    ['con el catch-all bloqueando UNA ruta', good.replace('User-agent: *\nAllow: /', 'User-agent: *\nAllow: /\nDisallow: /reference/')],
    ['con el catch-all sin declarar Allow', good.replace('User-agent: *\nAllow: /', 'User-agent: *')],
    ['con GPTBot bloqueado y RE-ABIERTO en el mismo grupo', good.replace('User-agent: GPTBot\nDisallow: /', 'User-agent: GPTBot\nDisallow: /\nAllow: /')],
  ]
  for (const [name, text] of mutants) {
    if (text === good) { console.error(`✗ self-test: el mutante «${name}» NO SE APLICÓ — el control no probaría nada`); process.exit(1) }
    if (!audit(text).length) { console.error(`✗ self-test: el mutante «${name}» SOBREVIVE — el gate no lo ve`); process.exit(1) }
    console.log(`  ok  mutante «${name}» muere`)
  }
  // ⛔ Y AHORA LA MITAD QUE FALTABA, señalada por el contraste: hasta aquí el self-test sólo
  // llamaba a `audit()` y salía 0 — probaba la FUNCIÓN, no el PROGRAMA, mientras su mensaje
  // prometía los tres códigos. Un control que no ejecuta lo que promete medir es exactamente el
  // «no he podido mirar» disfrazado de verde. Esto lanza el proceso de verdad.
  const { execFileSync } = await import('node:child_process')
  const { mkdtemp, writeFile, rm } = await import('node:fs/promises')
  const { tmpdir } = await import('node:os')
  const { join } = await import('node:path')
  const dir = await mkdtemp(join(process.env.TMPDIR || tmpdir(), 'robots-selftest-'))
  const run = (args) => {
    try { execFileSync(process.execPath, [fileURLToPath(import.meta.url), ...args], { stdio: 'pipe' }); return 0 }
    catch (err) { return err.status }
  }
  try {
    const limpio = join(dir, 'ok.txt'); await writeFile(limpio, good)
    const roto = join(dir, 'bad.txt'); await writeFile(roto, good.replace(/^Sitemap:.*$/m, ''))
    const casos = [
      ['0 sobre el fichero real', ['--exit-probe', limpio], 0],
      ['1 sobre un fichero con hallazgo', ['--exit-probe', roto], 1],
      ['2 sobre un fichero que no existe', ['--exit-probe', join(dir, 'no-existe.txt')], 2],
    ]
    for (const [nombre, args, esperado] of casos) {
      const code = run(args)
      if (code !== esperado) { console.error(`✗ self-test: «${nombre}» salió ${code}, esperado ${esperado}`); process.exit(1) }
      console.log(`  ok  código de salida ${esperado}: ${nombre}`)
    }
  } finally {
    await rm(dir, { recursive: true, force: true })
  }

  console.log(`✓ robots self-test: el fichero real sale limpio, los ${mutants.length} mutantes mueren y los tres códigos se miden EJECUTANDO el programa`)
  process.exit(0)
}

if (!existsSync(DIST_ROBOTS)) {
  console.error('robots: NO HE PODIDO MIRAR — no existe dist/robots.txt.')
  console.error('  O el build no ha corrido, o `public/robots.txt` dejó de copiarse — que es el fallo, no la excusa.')
  process.exit(2)
}

const text = await readFile(DIST_ROBOTS, 'utf8')
const findings = audit(text)
if (findings.length) {
  console.error('✗ robots: la política publicada no es la que el proyecto decidió.\n')
  for (const f of findings) console.error(`  - ${f}`)
  console.error('\n  La política vive en docs-site/public/robots.txt y es la del apex (repo web, public/robots.txt).')
  process.exit(1)
}
console.log(`✓ robots: OK — Sitemap declarado, catch-all abierto y ${MUST_BLOCK.length} rastreadores de entrenamiento bloqueados.`)
process.exit(0)
