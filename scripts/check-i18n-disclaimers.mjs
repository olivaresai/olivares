// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE GAP THIS CLOSES.
//
// `lint:i18n` proves that a t('key') RESOLVES, that the seven catalogs agree, and
// that a chunk registers the namespace it renders. All three start FROM THE CONSOLE,
// so none of them can see the defect that opened: a user-facing sentence that
// never reaches a catalog at all.
//
// The Compliance console's front page renders, above Spanish copy, the English
// sentence "Technical control-status mapping derived from observed platform evidence.
// NOT a certification and NOT legal advice (docs/SECURITY-HARDENING.md)." Every i18n gate was green,
// because the sentence is a BACKEND constant (modules/compliance/report.go) that the
// console prints verbatim — and the one sentence whose whole job is to say what the
// product is NOT reached the readers who need it most in a language they may not
// read.
//
// So this gate starts from the ENGINE and asks three questions the others cannot:
//
//   1. PARITY — every key declared in web/src/features/_intel/disclaimers.ts exists,
//      non-empty, in all seven `intel` catalogs, and no non-English catalog is a
//      byte-identical copy of the English (a copy is an untranslated string wearing a
//      translation's clothes, and it defeats the check above it).
//   2. ANCHORING — every canonical English string in that file still appears VERBATIM
//      in the engine sources. Edit the Go wording without touching the catalogs and
//      six translations would go on asserting a sentence the product no longer says.
//   3. THE RESIDUE, COUNTED — every disclaimer the engine can emit that has NO
//      translation is counted against a pinned baseline. A NEW English-only legal
//      notice fails this gate; what remains undone is a number printed on every run
//      rather than something somebody has to remember.
//
// Its red cases run FIRST (`--self-test`), on throwaway trees under TMPDIR: a gate
// nobody has watched fail is a gate that reports green because it looked at nothing.
//
// Pure Node (no deps), like the other i18n gates, so it runs inside the Go-toolchain
// gate. Run from the repository root.

import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import process from 'node:process'
import { fileURLToPath } from 'node:url'

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const LOCALES = ['en', 'es', 'de', 'fr', 'ja', 'ru', 'zh']
// Shorter than this is a field name or a fragment, not a notice to a reader.
const MIN_DISCLAIMER_LENGTH = 20

// UNTRANSLATED_BASELINE is how many engine disclaimers have no translation today. It
// is a RATCHET: the gate fails when the number grows, and equally when it shrinks
// without the baseline following, because a ratchet that does not ratchet stops
// catching anything.
//
// Measured 2026-08-08, and it took THREE passes to get right — the number is
// recorded with its method because two of those passes were wrong:
//
//   12  counting `const …Disclaimer` under modules/compliance only. Wrong scope: most
//       per-framework disclaimers are inline `Disclaimer:` struct fields, and
//       modules/models, governance and reporting carry their own.
//   38 distinct / 44 sites  with a single-literal regex. Wrong shape: 19 of the
//       declarations are CONCATENATED, so only the first fragment was captured —
//       collapsing four state-law texts into a shared prefix, four overlays likewise,
//       and dropping modules/models/spdx.go whose first fragment ("SPDX ") is 5 bytes.
//   45 distinct / 45 sites in 17 files  with the literal-chain scanner below. The
//       sol-max contrast measured this independently and the two agree exactly.
//
// 1 translated, 44 outstanding.
//
// The 37 are declared rather than guessed at: several are multi-paragraph legal texts
// whose entire content is a negation, and this repo routes translation of that kind
// to a Codex sol-max pass (CLAUDE.md, after measured ~60 defects from the
// cheaper route — including five pages where deny-closed behaviour came out
// INVERTED). Translating them badly would be worse than leaving them in English.
const UNTRANSLATED_BASELINE = 44

function read(file) {
  return fs.readFileSync(file, 'utf8')
}

/** Every non-test .go file under dir. */
function goFiles(dir, out = []) {
  for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
    const full = path.join(dir, entry.name)
    if (entry.isDirectory()) {
      if (entry.name === 'testdata' || entry.name === 'node_modules') continue
      goFiles(full, out)
    } else if (entry.name.endsWith('.go') && !entry.name.endsWith('_test.go')) {
      out.push(full)
    }
  }
  return out
}

// The three shapes a disclaimer is written in: a named constant, an inline struct
// field, and a map literal.
const DISCLAIMER_PREFIX =
  /(?:[A-Za-z][A-Za-z0-9_]*[Dd]isclaimer\s*=\s*|Disclaimer:\s*|"disclaimer":\s*)/g

/**
 * stripGoComments removes // and slash-star comments while respecting string, raw
 * and rune literals.
 *
 * It exists because the first version of this gate searched RAW source: a disclaimer
 * mentioned in a comment satisfied the anchoring check, so the gate could be green
 * about a sentence the engine no longer emits. Found by the sol-max contrast.
 */
function stripGoComments(src) {
  let out = '',
    i = 0
  while (i < src.length) {
    const c = src[i]
    if (c === '"' || c === "'" || c === '`') {
      const q = c
      out += c
      i++
      while (i < src.length) {
        if (q !== '`' && src[i] === '\\') {
          out += src[i] + (src[i + 1] ?? '')
          i += 2
          continue
        }
        out += src[i]
        if (src[i] === q) {
          i++
          break
        }
        i++
      }
      continue
    }
    if (c === '/' && src[i + 1] === '/') {
      while (i < src.length && src[i] !== '\n') i++
      continue
    }
    if (c === '/' && src[i + 1] === '*') {
      i += 2
      while (i < src.length && !(src[i] === '*' && src[i + 1] === '/')) i++
      i += 2
      out += ' '
      continue
    }
    out += c
    i++
  }
  return out
}

/** readGoLiteral reads one interpreted or raw Go string literal at i. */
function readGoLiteral(src, i) {
  if (src[i] === '`') {
    const end = src.indexOf('`', i + 1)
    return end < 0 ? null : { text: src.slice(i + 1, end), next: end + 1 }
  }
  if (src[i] !== '"') return null
  let j = i + 1,
    raw = ''
  while (j < src.length) {
    if (src[j] === '\\') {
      raw += src[j] + (src[j + 1] ?? '')
      j += 2
      continue
    }
    if (src[j] === '"') return { text: raw, next: j + 1 }
    raw += src[j]
    j++
  }
  return null
}

/**
 * readConcatenation reads a whole `"a" + "b" + ident + "c"` chain as ONE value.
 *
 * THE MEASUREMENT DEFECT THIS FIXES. The first version of this gate matched a single
 * literal after the assignment, so for the 19 concatenated declarations it captured
 * only the FIRST fragment. That silently collapsed four state-law disclaimers into
 * their shared prefix, did the same to four overlay disclaimers, and dropped
 * modules/models/spdx.go entirely (its first fragment, "SPDX ", is 5 characters and
 * fell under the length floor). The published residue was 38/44; the truth is 45/45.
 * The sol-max contrast measured it independently, and this scanner reproduces its
 * number exactly.
 *
 * A variable inside the chain becomes a placeholder rather than a stop: stopping
 * there is what lost spdx.go.
 */
function readConcatenation(src, i) {
  const first = readGoLiteral(src, i)
  if (!first) return null
  const parts = [first.text]
  let j = first.next
  for (;;) {
    let k = j
    while (k < src.length && /\s/.test(src[k])) k++
    if (src[k] !== '+') break
    k++
    while (k < src.length && /\s/.test(src[k])) k++
    const lit = readGoLiteral(src, k)
    if (lit) {
      parts.push(lit.text)
      j = lit.next
      continue
    }
    const ident = /^[A-Za-z_][A-Za-z0-9_.]*/.exec(src.slice(k))
    if (!ident) break
    parts.push('\u2026')
    j = k + ident[0].length
  }
  return { text: parts.join(''), next: j }
}

/** Distinct disclaimer strings the engine can emit, mapped to where each appears. */
function engineDisclaimerStrings(roots) {
  const found = new Map() // text -> [relative file paths]
  for (const root of roots) {
    if (!fs.existsSync(root)) continue
    for (const file of goFiles(root)) {
      const src = stripGoComments(read(file))
      DISCLAIMER_PREFIX.lastIndex = 0
      let m
      while ((m = DISCLAIMER_PREFIX.exec(src))) {
        const chain = readConcatenation(src, m.index + m[0].length)
        if (!chain || chain.text.length < MIN_DISCLAIMER_LENGTH) continue
        const rel = path.relative(ROOT, file)
        const at = found.get(chain.text)
        if (at) at.push(rel)
        else found.set(chain.text, [rel])
      }
    }
  }
  return found
}

/**
 * The canonical English strings declared in disclaimers.ts.
 *
 * The file is read as TEXT rather than imported: this gate is plain Node with no
 * bundler, and teaching it to parse TypeScript would make the gate harder to trust
 * than the thing it checks. The shape it matches is the one the file documents — a
 * quoted key followed by a quoted value — and parsing zero entries is itself a
 * failure, so a shape change cannot silently empty the gate.
 */
function declaredCanonicalStrings(mapFile, problems) {
  const src = read(mapFile)
  if (!src.includes('KNOWN_DISCLAIMERS')) {
    problems.push(`${path.relative(ROOT, mapFile)}: no KNOWN_DISCLAIMERS export found`)
    return []
  }
  const body = src.slice(src.indexOf('KNOWN_DISCLAIMERS'))
  const out = []
  for (const m of body.matchAll(/'((?:[^'\\]|\\.)*)':\s*\n?\s*'([A-Za-z0-9_.-]+)',/g)) {
    out.push({ text: m[1].replace(/\\'/g, "'").replace(/\\\\/g, '\\'), key: m[2] })
  }
  return out
}

/** runChecks returns the problems found; an empty list means clean. */
function runChecks({ mapFile, catalogDir, engineRoots, baseline }) {
  const problems = []
  // ⛔ LOS PUNTOS CIEGOS VAN APARTE DE LOS HALLAZGOS, Y LA RAZÓN ES EL CÓDIGO DE SALIDA.
  //
  //    Este gate YA distinguía «no he podido mirar» de «está limpio» — lo decía con todas las
  //    letras—, pero lo empujaba a `problems` y salía **1**, igual que un defecto. Quien consume el
  //    veredicto es un paso de CI, un hook u otro guion, y **ninguno lee prosa**: obligarles a
  //    parsear el mensaje convierte la distinción en una dependencia del TEXTO, que se rompe con la
  //    primera reescritura y se rompe en silencio.
  //
  //    Adjudicado por el carril de integración el 2026-08-17: la tercera respuesta la da el CÓDIGO DE SALIDA.
  //    ⇒ hallazgo real = 1 · punto ciego = 2 · limpio = 0.
  //
  // ⚠ DENY-CLOSED: si una rama pudiera ser lo uno o lo otro, es un HALLAZGO. Sólo entra aquí lo
  //   que es inequívocamente «el examen no ha ocurrido».
  const blindSpots = []

  const declared = declaredCanonicalStrings(mapFile, problems)
  if (declared.length === 0 && problems.length === 0) {
    blindSpots.push(
      `${path.relative(ROOT, mapFile)}: parsed ZERO canonical disclaimers. A gate that ` +
        `examines nothing reports no findings — that is not the same as clean.`,
    )
  }

  // --- 1. parity across the seven catalogs -----------------------------------
  const catalogs = new Map()
  for (const loc of LOCALES) {
    const file = path.join(catalogDir, `${loc}.json`)
    if (!fs.existsSync(file)) {
      problems.push(`missing catalog ${path.relative(ROOT, file)}`)
      continue
    }
    catalogs.set(loc, JSON.parse(read(file)))
  }
  for (const { key, text } of declared) {
    for (const [loc, catalog] of catalogs) {
      const value = catalog?.disclaimers?.[key]
      if (typeof value !== 'string' || value.trim() === '') {
        problems.push(
          `intel:${loc} has no disclaimers.${key} — that locale falls back to English ` +
            `for a notice whose entire purpose is to be understood`,
        )
        continue
      }
      if (loc !== 'en' && value.trim() === text.trim()) {
        problems.push(
          `intel:${loc} disclaimers.${key} is byte-identical to the English source. An ` +
            `untranslated copy inside a translated catalog is worse than an honest ` +
            `fallback: nothing downstream can tell them apart any more.`,
        )
      }
    }
  }

  // --- 2. anchoring: the canonical English is still what the engine emits -----
  // Compared against the EXTRACTED declarations, not a substring search over the raw
  // sources: the first version was satisfied by an appearance inside a comment, so it
  // could stay green about a sentence the engine had stopped emitting.
  const engineStrings = engineDisclaimerStrings(engineRoots)
  const emitted = new Set(engineStrings.keys())
  for (const { key, text } of declared) {
    if (!emitted.has(text.replace(/"/g, '\\"'))) {
      problems.push(
        `disclaimers.${key}: its canonical English is not among the strings the engine ` +
          `declares. Either the wording changed and seven translations now assert ` +
          `something the product does not say, or the key is dead. Text sought:\n` +
          `      ${text}`,
      )
    }
  }

  // --- 3. the residue, counted ----------------------------------------------
  const translated = new Set(declared.map((d) => d.text.replace(/"/g, '\\"')))
  const untranslatedTexts = [...engineStrings.keys()].filter((t) => !translated.has(t))
  const untranslated = untranslatedTexts.length

  if (engineStrings.size === 0) {
    blindSpots.push(
      `found ZERO disclaimer strings in the engine. A gate that examines nothing ` +
        `reports no findings — that is not the same as clean.`,
    )
  } else if (untranslated > baseline) {
    const sample = untranslatedTexts
      .slice(0, 5)
      .map((t) => `      ${engineStrings.get(t)[0]}: ${t.slice(0, 90)}…`)
      .join('\n')
    problems.push(
      `${untranslated} engine disclaimers have no translation, baseline is ${baseline}. ` +
        `A user-facing legal notice was added in English only. Translate it in ` +
        `web/src/features/_intel/disclaimers.ts plus the seven intel catalogs, or raise ` +
        `the baseline in this script and say why.\n${sample}`,
    )
  } else if (untranslated < baseline) {
    problems.push(
      `${untranslated} engine disclaimers are untranslated but the baseline still says ` +
        `${baseline}. Lower it — a ratchet that does not ratchet stops catching anything.`,
    )
  }

  return {
    problems,
    blindSpots,
    stats: {
      distinct: engineStrings.size,
      sites: [...engineStrings.values()].reduce((n, v) => n + v.length, 0),
      translated: declared.length,
      untranslated,
    },
  }
}

// --- the self-test -----------------------------------------------------------
// Each case builds a throwaway tree that is RED (or GREEN) by construction, under
// TMPDIR, touching nothing in the repository.

const CANON = 'Nothing here is a certification and nothing here is legal advice.'
const OTHER = 'A second notice that is also not a certification and not legal advice.'

function writeFixture(dir, { canon = CANON, catalogs, engineText = CANON, extra = [] }) {
  fs.mkdirSync(path.join(dir, 'web/src/features/_intel/i18n'), { recursive: true })
  fs.mkdirSync(path.join(dir, 'modules/probe'), { recursive: true })
  fs.writeFileSync(
    path.join(dir, 'web/src/features/_intel/disclaimers.ts'),
    `export const KNOWN_DISCLAIMERS: Readonly<Record<string, string>> = {\n` +
      `  '${canon}':\n    'report',\n}\n`,
  )
  for (const loc of LOCALES) {
    fs.writeFileSync(
      path.join(dir, `web/src/features/_intel/i18n/${loc}.json`),
      JSON.stringify(catalogs(loc), null, 2) + '\n',
    )
  }
  const decls = [`const probeDisclaimer = "${engineText}"`, ...extra].join('\n')
  fs.writeFileSync(path.join(dir, 'modules/probe/probe.go'), `package probe\n\n${decls}\n`)
  return {
    mapFile: path.join(dir, 'web/src/features/_intel/disclaimers.ts'),
    catalogDir: path.join(dir, 'web/src/features/_intel/i18n'),
    engineRoots: [path.join(dir, 'modules')],
  }
}

const translatedCatalogs = (loc) => ({
  disclaimers: { report: loc === 'en' ? CANON : `[${loc}] ${CANON}` },
})

function selfTest() {
  const base = fs.mkdtempSync(path.join(os.tmpdir(), 'i18n-disclaimers-selftest-'))
  const cases = [
    {
      name: 'GREEN: anchored canonical, seven locales translated, residue at baseline',
      baseline: 0,
      build: (d) => writeFixture(d, { catalogs: translatedCatalogs }),
      wantRed: false,
    },
    {
      name: 'RED: one locale is missing the key',
      expect: 'has no disclaimers.report',
      baseline: 0,
      build: (d) =>
        writeFixture(d, {
          catalogs: (loc) => (loc === 'ja' ? { disclaimers: {} } : translatedCatalogs(loc)),
        }),
      wantRed: true,
    },
    {
      name: 'RED: a locale holds an untranslated copy of the English',
      expect: 'byte-identical to the English source',
      baseline: 0,
      build: (d) =>
        writeFixture(d, {
          catalogs: (loc) =>
            loc === 'ru' ? { disclaimers: { report: CANON } } : translatedCatalogs(loc),
        }),
      wantRed: true,
    },
    {
      name: 'RED: the engine wording drifted from the canonical English',
      expect: 'not among the strings the engine declares',
      baseline: 0,
      build: (d) =>
        writeFixture(d, {
          catalogs: translatedCatalogs,
          engineText: CANON.replace('Nothing', 'Something'),
        }),
      wantRed: true,
    },
    {
      name: 'RED: a NEW English-only disclaimer appeared',
      expect: 'have no translation, baseline is',
      baseline: 0,
      build: (d) =>
        writeFixture(d, {
          catalogs: translatedCatalogs,
          extra: [`const secondDisclaimer = "${OTHER}"`],
        }),
      wantRed: true,
    },
    {
      name: 'RED: the residue shrank but the baseline did not follow',
      expect: 'but the baseline still says',
      baseline: 3,
      build: (d) => writeFixture(d, { catalogs: translatedCatalogs }),
      wantRed: true,
    },
    // ⛔ LOS DOS PUNTOS CIEGOS, Y VAN COMO CLASE PROPIA (`wantBlind`), no como un rojo más. La
    //    condición que exige el carril de integración es de DOS MITADES y aquí está entera: un punto ciego
    //    tiene que dar `blindSpots` no vacío **y `problems` vacío** (⇒ sale 2, no 1), y un hallazgo
    //    real al revés (⇒ sale 1, no 2). Afirmar sólo «es rojo» no distingue las dos salidas, que
    //    es exactamente lo que esta conversión viene a arreglar.
    {
      name: 'BLIND: the engine has no disclaimers at all (the gate examined nothing)',
      expect: 'found ZERO disclaimer strings',
      baseline: 0,
      build: (d) => {
        const cfg = writeFixture(d, { catalogs: translatedCatalogs })
        fs.writeFileSync(path.join(d, 'modules/probe/probe.go'), 'package probe\n')
        return cfg
      },
      wantBlind: true,
    },
    {
      name: 'BLIND: the canonical map parses to zero disclaimers (the lexer, not the tree)',
      expect: 'parsed ZERO canonical disclaimers',
      baseline: 0,
      build: (d) => {
        const cfg = writeFixture(d, { catalogs: translatedCatalogs })
        // Un mapa sintácticamente válido del que no se extrae NINGUNA cadena canónica: el gate
        // no tiene contra qué comparar, y eso no es un árbol limpio.
        fs.writeFileSync(cfg.mapFile, 'export const KNOWN_DISCLAIMERS = {}\n')
        return cfg
      },
      wantBlind: true,
    },
  ]

  let failures = 0
  for (const [i, c] of cases.entries()) {
    const dir = path.join(base, `case-${i}`)
    fs.mkdirSync(dir, { recursive: true })
    const cfg = c.build(dir)
    const { problems, blindSpots } = runChecks({ ...cfg, baseline: c.baseline })
    const red = problems.length > 0
    const blind = blindSpots.length > 0
    // A red case must be red FOR ITS OWN REASON. Asserting only `problems.length > 0`
    // was the weakness the sol-max contrast named: two of these fixtures trip more
    // than one predicate, so the case would have stayed "ok" with the very defence it
    // is named after removed. Now the expected message must be present.
    const lista = c.wantBlind ? blindSpots : problems
    const forItsOwnReason =
      (!c.wantRed && !c.wantBlind) || !c.expect || lista.some((p) => p.includes(c.expect))
    // ⛔ LAS DOS MITADES, SIEMPRE. Un caso de punto ciego debe dar blindSpots Y NO problems (⇒ 2),
    //    y un hallazgo debe dar problems Y NO blindSpots (⇒ 1). Comprobar sólo la mitad esperada
    //    dejaría pasar un gate que las mezcla, que es el defecto que esta conversión cierra.
    //    Se afirma sobre la CLASE DE SALIDA porque es lo único que consume un paso de CI, y el 2
    //    GANA al 1: si el examen no ocurrió, lo que el gate diga sobre hallazgos tampoco es fiable.
    //    Un caso de HALLAZGO, en cambio, no puede disparar punto ciego — saldria 2 y su defecto
    //    quedaria indistinguible de «no he podido mirar».
    const clase = blind ? 2 : red ? 1 : 0
    const claseEsperada = c.wantBlind ? 2 : c.wantRed ? 1 : 0
    const salidaCorrecta = clase === claseEsperada
    if (!salidaCorrecta || !forItsOwnReason) {
      failures++
      const nombre = (n) => (n === 2 ? 'BLIND(2)' : n === 1 ? 'RED(1)' : 'GREEN(0)')
      const esperado = nombre(claseEsperada)
      const obtenido = nombre(clase)
      const why = !salidaCorrecta
        ? `expected ${esperado}, got ${obtenido}`
        : `${obtenido}, but for the wrong reason — nothing mentioned ${JSON.stringify(c.expect)}`
      console.error(
        `  self-test FAILED: ${c.name}\n    ${why}` +
          (red || blind ? `:\n      ${[...problems, ...blindSpots].join('\n      ')}` : ''),
      )
    } else {
      console.log(`  self-test ok: ${c.name}${c.expect ? ` (${c.expect})` : ''}`)
    }
  }
  fs.rmSync(base, { recursive: true, force: true })
  if (failures > 0) {
    console.error(`check-i18n-disclaimers --self-test: ${failures} case(s) wrong`)
    process.exit(1)
  }
  console.log(`check-i18n-disclaimers --self-test: OK — ${cases.length} cases`)
}

if (process.argv.includes('--self-test')) {
  selfTest()
} else {
  const { problems, blindSpots, stats } = runChecks({
    mapFile: path.join(ROOT, 'web/src/features/_intel/disclaimers.ts'),
    catalogDir: path.join(ROOT, 'web/src/features/_intel/i18n'),
    // The engine's legal notices are NOT confined to modules/compliance.
    engineRoots: [path.join(ROOT, 'modules'), path.join(ROOT, 'core')],
    baseline: UNTRANSLATED_BASELINE,
  })
  // ⛔ EL PUNTO CIEGO SE MIRA ANTES QUE EL HALLAZGO Y SALE 2. Si el gate no ha podido examinar
  //    nada, lo que diga sobre hallazgos no significa nada — cero hallazgos sobre cero material no
  //    es un aprobado, y un `1` lo haría indistinguible de un defecto real para quien lo consuma.
  if (blindSpots.length > 0) {
    console.error('check-i18n-disclaimers: NO HE PODIDO MIRAR')
    for (const b of blindSpots) console.error(`  - ${b}`)
    process.exit(2)
  }
  if (problems.length > 0) {
    console.error('check-i18n-disclaimers: FAIL')
    for (const p of problems) console.error(`  - ${p}`)
    process.exit(1)
  }
  console.log(
    `check-i18n-disclaimers: OK — ${stats.distinct} distinct engine disclaimers over ` +
      `${stats.sites} declaration sites; ${stats.translated} translated into ` +
      `${LOCALES.length} locales, ${stats.untranslated} still English-only ` +
      `(baseline ${UNTRANSLATED_BASELINE}).`,
  )
}
