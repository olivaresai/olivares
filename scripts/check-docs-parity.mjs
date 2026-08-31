// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// check-docs-parity.mjs — fail when a docs-site page exists in English but not in
// a published locale (or the other way round), unless an explicit, dated waiver
// says so.
//
// THE GAP THIS CLOSES. The repo already had two i18n gates and NEITHER could see a
// missing PAGE:
//   * `lint:i18n`         — console UI translation KEYS (web/src/lib/i18n), not docs.
//   * `lint:i18n-anchors` — in-page #anchors INSIDE pages that already exist.
// So the docs-site could (and did) drift a whole page at a time. On 2026-07-28 the
// count was 44 English pages with no translation in any of the six locales; hours
// after that was cleared, commit 98c1cbcc landed two new ADRs and reopened the gap
// in all six locales at once. Nobody noticed either time until somebody measured by
// hand. Every English page added from now on opens six holes; this check is what
// turns that from "somebody eventually notices" into a finding on the next push.
//
// WHAT IT COMPARES.
//   Identity is the ROUTE Astro publishes, reproduced from the pinned loader rather
//   than approximated: a frontmatter `slug` wins outright; otherwise every path
//   segment is github-slugged, joined with `/`, and a terminal `/index` is dropped.
//   Starlight additionally drops `draft: true` pages in production, so they are not
//   demanded and a DRAFT translation does not count as present. Two files resolving
//   to one route is a `route-collision` finding on whichever side it happens.
//   `_`-prefixed files are skipped — Starlight's loader ignores them — and every
//   extension that loader accepts is honoured, not just md/mdx.
//
//   Approximating this with "strip the extension" gave both silent gaps and false
//   positives: `topic/index.md` against `topic.md` was a missing page AND an orphan
//   for ONE route, and a `draft: true` translation counted as present while
//   production served the English fallback.
//
//   English page set = every page under docs-site/src/content/docs MINUS the
//   per-locale directories and MINUS the archived version snapshots. Each published
//   locale must carry the same routes.
//
//   The locale list and the archived-snapshot list are IMPORTED from
//   docs-site/src/site-locales.mjs — the same declaration astro.config.mjs and
//   sync-adr.mjs use. They are never re-derived here: an earlier version text-scanned
//   astro.config.mjs, and an adversarial round broke that with five legal Astro
//   configs (a quoted key like Starlight's documented `'zh-cn'`, a `}` inside a label
//   string, an unrelated `locales:` object appearing first, …). Every break produced a
//   NON-EMPTY WRONG locale set — zero findings while checking the wrong thing.
//
//   An archived snapshot such as `2026-06/` is an English-authored point-in-time
//   archive by design, so it is excluded structurally rather than waived — on BOTH
//   sides, because starlight-versions copies each locale into `<locale>/<slug>/` when
//   it cuts one.
//
// FINDINGS.
//   missing        an English page has no counterpart in a locale.
//   orphan         a locale page has no English counterpart (source renamed or
//                  deleted — the translation is now unreachable, which the "missing"
//                  direction alone would never show).
//   route-collision two source files resolve to the same published route.
//   locale-missing a published locale has no directory at all.
//   waiver-invalid a waiver entry is malformed (see the schema below).
//   waiver-stale   a waiver that suppresses nothing — the page IS translated.
//   waiver-expired a waiver past its own `expires` date (it stops suppressing).
//
//   NOTES (printed, never counted as findings):
//   format-drift   both sides exist for one route in different source formats. Starlight
//                  routes .md and .mdx identically and a locale may legitimately need no
//                  MDX syntax, so this is a convention note, not a parity gap.
//
// WAIVERS — explicit, versioned, dated. There is no silent exemption: a page that is
// not translated and not waived is a finding, and a waiver that has stopped doing
// anything is ALSO a finding, so the file cannot rot into a blanket mute.
//   File: docs-site/i18n-parity-waivers.json
//   {
//     "waivers": [
//       { "path": "explanation/adr/index.md",   // relative to content/docs, POSIX
//         "locales": ["ja"],                    // explicit; "*" is REJECTED
//         "reason": "at least 20 characters saying WHY",
//         "date": "2026-07-29",                 // real calendar date, not in the future
//         "expires": "2026-12-31" }             // optional; must be after `date`
//     ]
//   }
//   `"*"` is rejected deliberately: it expands from whatever locale list exists at RUN
//   time, so a waiver granted before a locale existed would silently mute that locale
//   too. Two entries covering the same (locale, route) are rejected as duplicates —
//   otherwise both would count as "used" and neither could ever go stale.
//
// MODES (the shape used for the commerce lint).
//   --report   NOT the default, and NOT how the push gate is wired: since 2026-08-01 the
//              Taskfile passes --informed --summary (Taskfile.yml:1575). Left documented
//              because it is still a valid flag, but a gate that cannot fail is not a
//              gate -- which is why it was rejected. Print every finding, exit 0.
//   (historic) This is how it USED to be wired into the
//              push gate today, so landing it cannot turn the gate red for work that
//              is already in flight. NOTE, honestly: in this mode it is a REPORT, not
//              an enforcement point — only a human reading the log stops a regression.
//   --strict   Exit 1 on any finding. The tree is clean under --strict as of.
//   --informed WHAT THE GATE RUNS since 2026-08-01. Project policy from that date:
//              public surface is authored in US English only, and the translations
//              already published are kept as they are. Exit 1 on the BLOCKING
//              findings only; report the English-only backlog and exit 0 for it.
//              The split is DERIVED, not declared: a route missing in EVERY published
//              locale is the backlog; a route missing in SOME but not all was being
//              kept in parity and lost a locale — a regression, still fatal. Orphans,
//              route collisions, a missing locale directory and every waiver defect
//              are blocking in both modes. The backlog goes to stdout AND to
//              $GITHUB_STEP_SUMMARY when set, because a report nobody reads is no
//              report. NOT the same as --report: --report cannot fail at all, and the
//              regression this checker was written for would sail through it.
//              DECLARED LIMIT: deleting every locale of one page in a single commit
//              drops that route to zero coverage, so it lands in the backlog instead
//              of going red. This reads the tree, not its history; the diff of
//              removing six files is what catches it, and `orphan` does not (orphans
//              are translations whose English source went away — the other direction).
//
//   A HARD ERROR exits 2 in BOTH modes: the locale manifest missing, malformed, or
//   naming anything that is not a safe immediate-child directory; ANY directory under
//   the content root unreadable at any depth; an empty English tree; a waiver file that
//   is not valid JSON. A parity checker that quietly discovered zero locales — or read
//   an unreadable subtree as empty, or accepted a "locale" of `.` and so compared the
//   content root with itself — would report zero findings forever, which is worse than
//   having no check at all.
//
// Usage (run from the repo root):
//   node scripts/check-docs-parity.mjs                 # report, exit 0
//   node scripts/check-docs-parity.mjs --strict        # exit 1 on any finding
//   node scripts/check-docs-parity.mjs --summary       # add a per-locale tally
//   node scripts/check-docs-parity.mjs --json          # machine-readable findings
//   node scripts/check-docs-parity.mjs --root DIR      # alternate tree (fixtures)
//
// Pure Node, no dependencies — runs inside the Go-toolchain push gate the same way
// scripts/check-i18n-parity.mjs and scripts/check-docs-anchors.mjs do.

import fs from 'node:fs'
import path from 'node:path'
import process from 'node:process'
import { pathToFileURL } from 'node:url'

// Every extension Starlight's own content loader accepts (loaders.ts), not just the
// two this repo happens to use — a page in a format the site publishes but the gate
// does not recognise is an untranslated page nobody is told about.
const PAGE_EXT = new Set(['.md', '.mdx', '.markdown', '.mdown', '.mkdn', '.mkd', '.mdwn'])
const WAIVER_REL = 'docs-site/i18n-parity-waivers.json'
const MANIFEST_REL = 'docs-site/src/site-locales.mjs'
const CONTENT_REL = 'docs-site/src/content/docs'
// A REAL calendar date, not just the shape of one. `^\d{4}-\d{2}-\d{2}$` alone accepts
// 2026-13-45, and an `expires` like that compares lexicographically — it would sit in
// the future forever and the waiver would never expire.
const isoDate = (s) => {
  if (typeof s !== 'string' || !/^\d{4}-\d{2}-\d{2}$/.test(s)) return false
  const d = new Date(`${s}T00:00:00Z`)
  return !Number.isNaN(d.getTime()) && d.toISOString().slice(0, 10) === s
}
const MIN_REASON = 20

// --- argv --------------------------------------------------------------------
const argv = process.argv.slice(2)
const has = (f) => argv.includes(f)
const valueOf = (f) => {
  const i = argv.indexOf(f)
  return i === -1 ? null : argv[i + 1]
}
const STRICT = has('--strict')
const INFORMED = has('--informed')
const SUMMARY = has('--summary')
const JSON_OUT = has('--json')
const SCRIPT_DIR = path.dirname(new URL(import.meta.url).pathname)
const ROOT = path.resolve(valueOf('--root') ?? path.join(SCRIPT_DIR, '..'))

function hardError(msg) {
  process.stderr.write(`check-docs-parity: FATAL ${msg}\n`)
  process.exit(2)
}

// --- the site's own locale declaration -----------------------------------------
// IMPORTED, not parsed. The first version of this checker text-scanned
// astro.config.mjs; an adversarial round broke that with five legal Astro configs,
// and every break produced a NON-EMPTY WRONG locale set — zero findings while
// checking the wrong thing. docs-site/src/site-locales.mjs is now the single
// declaration that astro.config.mjs, this gate and sync-adr.mjs all read.
//
// The PRIMARY facts (LOCALES, VERSIONS) are what this derives from; the manifest's
// convenience projections are then cross-checked against that derivation, so the two
// cannot drift apart inside the file itself.
//
// Every name is validated as a safe immediate child directory. A second adversarial
// round showed why: `PUBLISHED_LOCALES = ['.']` made path.join(contentRoot, '.') the
// content root, so the "locale" walk and the English walk inspected the SAME tree and
// the checker reported a clean, fully-translated site. A fail-closed boundary that
// accepts an unsafe value is not fail-closed.
const SAFE_SEGMENT = /^[A-Za-z0-9][A-Za-z0-9._-]*$/
function assertSegments(list, what) {
  const seen = new Set()
  for (const v of list) {
    if (typeof v !== 'string' || !SAFE_SEGMENT.test(v) || v === '.' || v === '..')
      hardError(`${MANIFEST_REL}: ${what} ${JSON.stringify(v)} is not a safe directory name`)
    if (seen.has(v)) hardError(`${MANIFEST_REL}: ${what} ${JSON.stringify(v)} is declared twice`)
    seen.add(v)
  }
  return seen
}

async function readSiteLocales(root) {
  const p = path.join(root, MANIFEST_REL)
  if (!fs.existsSync(p)) hardError(`cannot find ${MANIFEST_REL} (looked in ${root})`)
  let mod
  try {
    mod = await import(pathToFileURL(p).href)
  } catch (e) {
    hardError(`${MANIFEST_REL} could not be imported: ${e.message}`)
  }
  if (typeof mod.LOCALES !== 'object' || mod.LOCALES === null)
    hardError(`${MANIFEST_REL} must export a LOCALES object`)
  if (!Array.isArray(mod.VERSIONS)) hardError(`${MANIFEST_REL} must export a VERSIONS array`)

  // `root` is English (Starlight's defaultLocale) and is not a directory.
  const locales = Object.keys(mod.LOCALES).filter((l) => l !== 'root')
  const archived = mod.VERSIONS.map((v) => v?.slug)
  if (locales.length === 0) hardError(`${MANIFEST_REL} declares ZERO published locales`)
  const locSet = assertSegments(locales, 'locale')
  assertSegments(archived, 'archived version slug')
  for (const a of archived)
    if (locSet.has(a)) hardError(`${MANIFEST_REL}: ${JSON.stringify(a)} is both a locale and an archive slug`)

  // The convenience projections must agree with the primary facts, or one of the three
  // consumers is reading a different list from the other two.
  const same = (a, b) => Array.isArray(a) && JSON.stringify(a) === JSON.stringify(b)
  if (!same(mod.PUBLISHED_LOCALES, locales))
    hardError(`${MANIFEST_REL}: PUBLISHED_LOCALES disagrees with LOCALES minus root`)
  if (!same(mod.ARCHIVED_SLUGS, archived))
    hardError(`${MANIFEST_REL}: ARCHIVED_SLUGS disagrees with the VERSIONS slugs`)
  return { locales, archived }
}

// --- page discovery -----------------------------------------------------------
// Identity is the ROUTE Astro publishes, reproduced from the pinned loader rather
// than approximated. Astro's generateIdDefault(): a frontmatter `slug` wins outright;
// otherwise every path segment is github-slugged, joined with `/`, and a terminal
// `/index` is dropped. Starlight additionally drops `draft: true` pages in production.
//
// Approximating this with "strip the extension" produced BOTH silent gaps and false
// positives: `topic/index.md` against `topic.md` was reported as a missing page plus
// an orphan for ONE route, and a translated page carrying `draft: true` counted as
// present while production served the English fallback.
//
// Same strip set as scripts/check-docs-anchors.mjs (github-slugger): keep letters,
// numbers, marks, `-` and `_`; replace each single space with `-`.
const SLUG_STRIP = /[^\p{L}\p{N}\p{M} _-]/gu
const githubSlug = (t) => t.trim().toLowerCase().replace(SLUG_STRIP, '').replace(/ /g, '-')

function isPage(name) {
  if (name.startsWith('_')) return false // Starlight's loader ignores these
  return PAGE_EXT.has(path.extname(name))
}

// Minimal frontmatter read: the leading `---` block, top-level `slug` and `draft` only.
// No YAML dependency — the gate runs where nothing is installed.
function frontmatter(file) {
  let src
  try {
    src = fs.readFileSync(file, 'utf8')
  } catch (e) {
    hardError(`cannot read ${path.relative(ROOT, file) || file}: ${e.message}`)
  }
  if (!/^---\r?\n/.test(src)) return {}
  const end = src.indexOf('\n---', 3)
  if (end === -1) return {}
  const out = {}
  for (const line of src.slice(4, end).split('\n')) {
    const m = /^(slug|draft)\s*:\s*(.*?)\s*$/.exec(line)
    if (!m) continue
    out[m[1]] = m[2].replace(/^['"]|['"]$/g, '')
  }
  return out
}

/** The route Astro publishes for `rel`, or null when the page is not published. */
function routeId(dir, rel) {
  const fm = frontmatter(path.join(dir, rel))
  if (String(fm.draft).toLowerCase() === 'true') return null // Starlight drops drafts
  if (fm.slug !== undefined) return fm.slug.replace(/^\/+/, '')
  return rel
    .replace(/\.[^./]+$/, '')
    .split('/')
    .map(githubSlug)
    .join('/')
    .replace(/\/index$/, '')
}

// An I/O failure is NOT "no pages here". Swallowing it made an unreadable English
// subtree look fully translated (rc=0, zero findings) — the exact silent green this
// gate exists to remove. Every read failure is fatal, at any depth.
// The sort is by CODE UNIT, not localeCompare. readdir order is filesystem-defined, so
// this gate sorts to be deterministic — but `a.name.localeCompare(b.name)` with no
// locale argument reads the environment's LANG/LC_ALL and the runtime's ICU data, which
// differ between the dev container and the CI runners. That makes the ORDER an ambient
// input, and the order decides which of two files claiming one route (`.md` vs `.mdx`)
// is seen first, i.e. whether the finding reads as format-drift or as a route
// collision. A gate whose verdict depends on the caller's locale is not reproducible.
function readDirOrDie(dir) {
  try {
    return fs.readdirSync(dir, { withFileTypes: true })
      .sort((a, b) => (a.name < b.name ? -1 : a.name > b.name ? 1 : 0))
  } catch (e) {
    hardError(`cannot read ${path.relative(ROOT, dir) || dir}: ${e.message}`)
  }
}

/** Every published page below `dir`, as { id, rel }. `skipDirs` matches at the top level. */
function walkPages(dir, base = dir, skipDirs = new Set(), out = [], top = true) {
  for (const e of readDirOrDie(dir)) {
    if (e.isDirectory()) {
      if (top && skipDirs.has(e.name)) continue
      walkPages(path.join(dir, e.name), base, skipDirs, out, false)
    } else if (e.isFile() && isPage(e.name)) {
      const rel = path.relative(base, path.join(dir, e.name)).split(path.sep).join('/')
      const id = routeId(base, rel)
      if (id !== null) out.push({ id, rel })
    }
  }
  return out
}

/** Two pages resolving to one route is a site defect, whichever side it happens on. */
function collisions(pages, where, findings) {
  const byId = new Map()
  for (const p of pages) {
    if (byId.has(p.id))
      findings.push({
        kind: 'route-collision',
        where: `${where}/${p.rel}`,
        detail: `resolves to the same route as ${byId.get(p.id)} (${p.id || '<root>'})`,
      })
    else byId.set(p.id, p.rel)
  }
  return byId
}

/** The route a waiver's source path resolves to (no frontmatter: it may not exist). */
function waiverRoute(rel) {
  return rel
    .replace(/\.[^./]+$/, '')
    .split('/')
    .map(githubSlug)
    .join('/')
    .replace(/\/index$/, '')
}

// --- waivers ------------------------------------------------------------------
function readWaivers(root, locales, findings) {
  const p = path.join(root, WAIVER_REL)
  if (!fs.existsSync(p)) return []
  let parsed
  try {
    parsed = JSON.parse(fs.readFileSync(p, 'utf8'))
  } catch (e) {
    hardError(`${WAIVER_REL} is not valid JSON: ${e.message}`)
  }
  const list = parsed?.waivers
  if (!Array.isArray(list)) hardError(`${WAIVER_REL} must have a top-level "waivers" array`)

  const today = new Date().toISOString().slice(0, 10)
  const ok = []
  const claimed = new Map() // "<locale> <id>" -> the index that already covers it
  list.forEach((w, i) => {
    const where = `${WAIVER_REL}[${i}]`
    const bad = (why) => findings.push({ kind: 'waiver-invalid', where, detail: why })
    if (typeof w !== 'object' || w === null) return bad('entry is not an object')
    if (typeof w.path !== 'string' || w.path.length === 0) return bad('missing "path"')
    // Canonical, POSIX, in-tree. A `..` SEGMENT escapes; the substring `..` inside a
    // filename (`v1..2.md`) does not, and rejecting it was noise.
    const segs = w.path.split('/')
    if (
      w.path.includes('\\') ||
      w.path !== path.posix.normalize(w.path) ||
      w.path.startsWith('/') ||
      segs.some((x) => x === '' || x === '.' || x === '..') ||
      w.path.normalize('NFC') !== w.path
    )
      return bad(`"path" must be a canonical, NFC, repo-relative POSIX path: ${w.path}`)
    // A waiver names a real source file, so it must carry a real page extension.
    // Without this, `guide.xyz` resolved to the same route as `guide.md` and quietly
    // suppressed it, while looking like it referred to something else entirely.
    if (!PAGE_EXT.has(path.extname(w.path)))
      return bad(`"path" must name a page file (${[...PAGE_EXT].join(', ')}): ${w.path}`)
    // NO "*". It expands from whatever locale list exists AT RUN TIME, so a waiver
    // granted before a locale existed would silently mute that locale too — an
    // exemption nobody reviewed. The list must name the locales it exempts.
    if (w.locales === '*')
      return bad('"locales" must name each locale explicitly; "*" would silently cover locales added later')
    const locs = w.locales
    if (!Array.isArray(locs) || locs.length === 0) return bad('"locales" must be a non-empty array of locale codes')
    const unknown = locs.filter((l) => !locales.includes(l))
    if (unknown.length) return bad(`unknown locale(s): ${unknown.join(', ')}`)
    if (typeof w.reason !== 'string' || w.reason.trim().length < MIN_REASON)
      return bad(`"reason" must be at least ${MIN_REASON} characters saying why the page is not translated`)
    if (!isoDate(w.date)) return bad('"date" must be a real ISO YYYY-MM-DD date')
    if (w.date > today) return bad(`"date" is in the future (${w.date}) — a waiver cannot be granted ahead of time`)
    if (w.expires !== undefined && !isoDate(w.expires))
      return bad('"expires", when present, must be a real ISO YYYY-MM-DD date')
    if (w.expires && w.expires <= w.date)
      return bad(`"expires" (${w.expires}) must be after "date" (${w.date})`)
    // Two entries covering the same (locale, page) would BOTH count as used, so the
    // second could never go stale — the file would accumulate dead mutes invisibly.
    const wid = waiverRoute(w.path)
    const dup = locs.map((l) => `${l} ${wid}`).filter((k) => claimed.has(k))
    if (dup.length) return bad(`duplicate coverage of ${dup.join(', ')} (already waived at index ${claimed.get(dup[0])})`)
    if (w.expires && w.expires < today) {
      findings.push({
        kind: 'waiver-expired',
        where,
        detail: `${w.path} for ${locs.join(', ')} expired on ${w.expires} — it no longer suppresses anything`,
      })
      return
    }
    for (const l of locs) claimed.set(`${l} ${wid}`, i)
    ok.push({ ...w, locales: locs, id: wid, where })
  })
  return ok
}

// --- run ----------------------------------------------------------------------
const findings = [] // count toward the verdict
const notes = [] // reported, but not a parity gap — see format-drift below
const { locales, archived } = await readSiteLocales(ROOT)
const contentRoot = path.join(ROOT, CONTENT_REL)
// English = everything outside the locale directories and outside the archived
// version snapshots (English-only point-in-time archives, by design).
const skip = new Set([...locales, ...archived])
const enPages = walkPages(contentRoot, contentRoot, skip)
if (enPages.length === 0) hardError(`found ZERO English pages under ${CONTENT_REL}`)
const enById = collisions(enPages, CONTENT_REL, findings)
const waivers = readWaivers(ROOT, locales, findings)

const waived = new Map() // "<locale> <route>" -> waiver
for (const w of waivers) for (const l of w.locales) waived.set(`${l} ${w.id}`, w)
const waiverUsed = new Set()

const perLocale = []
for (const loc of locales) {
  const dir = path.join(contentRoot, loc)
  if (!fs.existsSync(dir)) {
    findings.push({
      kind: 'locale-missing',
      where: `${CONTENT_REL}/${loc}`,
      detail: `locale "${loc}" is published by ${MANIFEST_REL} but has no directory — all ${enPages.length} pages are untranslated`,
    })
    perLocale.push({ locale: loc, have: 0, missing: enPages.length, orphan: 0, waived: 0, formatDrift: 0 })
    continue
  }
  // starlight-versions copies each locale into <locale>/<slug>/ when it cuts a
  // snapshot, so the archived slugs must be skipped on BOTH sides. Skipping only the
  // root copy turned a future localized snapshot into a tree of phantom orphans.
  const pages = walkPages(dir, dir, new Set(archived))
  const have = collisions(pages, `${CONTENT_REL}/${loc}`, findings)
  let missing = 0
  let waivedHere = 0
  let formatDrift = 0
  for (const [id, rel] of enById) {
    const mine = have.get(id)
    if (mine !== undefined) {
      if (path.extname(mine) !== path.extname(rel)) {
        formatDrift++
        notes.push({
          kind: 'format-drift',
          where: `${CONTENT_REL}/${loc}/${mine}`,
          detail: `same route as English ${rel} in a different source format — routed identically by Starlight, so this is a convention note, not a parity gap`,
        })
      }
      continue
    }
    const key = `${loc} ${id}`
    if (waived.has(key)) {
      waiverUsed.add(key)
      waivedHere++
      continue
    }
    missing++
    findings.push({
      kind: 'missing',
      // `route` carries the published route id so --informed can group a page's
      // misses ACROSS locales afterwards. Missing in every locale is the
      // English-only backlog; missing in some but not all is a regression.
      route: id,
      where: `${CONTENT_REL}/${loc}/${rel}`,
      detail: `English page ${rel} has no ${loc} translation (and no waiver)`,
    })
  }
  let orphan = 0
  for (const [id, rel] of [...have].sort()) {
    if (enById.has(id)) continue
    orphan++
    findings.push({
      kind: 'orphan',
      where: `${CONTENT_REL}/${loc}/${rel}`,
      detail: `${loc} page ${rel} has no English source — renamed or deleted upstream`,
    })
  }
  perLocale.push({ locale: loc, have: have.size, missing, orphan, waived: waivedHere, formatDrift })
}

// A waiver that suppresses nothing is itself a finding: it either names a page that
// no longer exists in English, or a page that HAS been translated since. Either way
// the file must not be allowed to accumulate dead mutes.
for (const w of waivers) {
  for (const loc of w.locales) {
    if (waiverUsed.has(`${loc} ${w.id}`)) continue
    findings.push({
      kind: 'waiver-stale',
      where: w.where,
      detail: enById.has(w.id)
        ? `${w.path} IS translated into ${loc} — remove this waiver`
        : `${w.path} is not an English page — the waiver names a path that does not exist`,
    })
  }
}

// --- informed-parity classification -------------------------------------------
// Split the `missing` findings by how many locales a route is absent from. A route
// absent from EVERY published locale is the English-only backlog that decision
// policy allows; a route absent from some but not all was being kept in
// parity and lost a locale, which is a regression and stays blocking. Every other
// finding kind (orphan, route-collision, locale-missing, waiver-*) is blocking in
// both modes — none of them describes untranslated new work.
//
// DECLARED LIMIT, because it is real: if every locale of a page were deleted in one
// commit, the route drops to zero coverage and lands in the backlog rather than
// going red. This classifier reads the tree, not its history, so the two are
// indistinguishable here. The diff of deleting six files is what catches that, and
// `orphan` does NOT cover it (orphans are translations whose English source went
// away, the opposite direction).
const missingByRoute = new Map()
for (const f of findings) {
  if (f.kind !== 'missing' || !f.route) continue
  missingByRoute.set(f.route, (missingByRoute.get(f.route) || 0) + 1)
}
const backlogRoutes = new Set()
for (const [route, n] of missingByRoute) if (n === locales.length) backlogRoutes.add(route)
const blocking = findings.filter(
  (f) => !(f.kind === 'missing' && f.route && backlogRoutes.has(f.route))
)

// --- output -------------------------------------------------------------------
const totalWaived = perLocale.reduce((n, r) => n + r.waived, 0)
if (JSON_OUT) {
  process.stdout.write(
    JSON.stringify(
      {
        mode: STRICT ? 'strict' : 'report',
        locales,
        archived,
        english: enPages.length,
        waived: totalWaived,
        perLocale,
        findings,
        notes,
      },
      null,
      2
    ) + '\n'
  )
} else {
  const order = ['locale-missing', 'missing', 'orphan', 'route-collision', 'waiver-invalid', 'waiver-expired', 'waiver-stale']
  for (const kind of order) {
    const of = findings.filter((f) => f.kind === kind)
    if (!of.length) continue
    process.stdout.write(`\n${kind} (${of.length})\n`)
    for (const f of of) process.stdout.write(`  ${f.where}\n    ${f.detail}\n`)
  }
  if (notes.length) {
    process.stdout.write(`\nnotes — reported, NOT counted as findings (${notes.length})\n`)
    for (const n of notes) process.stdout.write(`  ${n.kind}: ${n.where}\n    ${n.detail}\n`)
  }
  if (SUMMARY || findings.length === 0) {
    process.stdout.write(
      `\ncheck-docs-parity: ${enPages.length} English pages x ${locales.length} locales (${locales.join(', ')})` +
        (archived.length ? `; archived English-only snapshots excluded: ${archived.join(', ')}` : '') +
        '\n'
    )
    for (const r of perLocale)
      process.stdout.write(
        `  ${r.locale}: ${r.have} pages, missing ${r.missing}, orphan ${r.orphan}, format-drift ${r.formatDrift}, waived ${r.waived}\n`
      )
  }
  if (INFORMED) {
    // INFORMED PARITY. Public surface is authored in US English only from now
    // on, and the translations that exist are kept as they are. So a page that
    // exists only in English is expected work, not a defect — but everything
    // that was ALREADY translated stays under guard.
    //
    // The split is derived, not declared: a route missing in EVERY locale is the
    // English-only backlog; a route missing in SOME but not all was being
    // translated and lost a locale, which is the regression this gate exists for.
    // No manifest to maintain and nothing to keep in sync.
    process.stdout.write(`\n${'-'.repeat(72)}\n`)
    if (blocking.length) {
      process.stdout.write(
        `check-docs-parity: ${blocking.length} BLOCKING finding(s) — FAILING (--informed).\n` +
          'These are not the English-only backlog: a page translated in some locales and\n' +
          'not others, an orphaned translation, a route collision or a broken waiver.\n'
      )
    }
    if (backlogRoutes.size) {
      process.stdout.write(
        `check-docs-parity: ${backlogRoutes.size} English page(s) with no translation in ANY locale ` +
          `(${backlogRoutes.size * locales.length} page-translations of backlog).\n` +
          'Reported, NOT blocking: US-English-only authoring is the decision in force.\n'
      )
      for (const r of [...backlogRoutes].sort()) process.stdout.write(`  ${r}\n`)
    }
    if (!blocking.length && !backlogRoutes.size) {
      process.stdout.write(
        totalWaived === 0
          ? 'check-docs-parity: OK — every English page has a translation in every locale.\n'
          : `check-docs-parity: OK — translated everywhere or covered by one of ${totalWaived} active waiver(s).\n`
      )
    }
    // The backlog has to be READ to be acted on, so it goes where a job reader
    // actually looks. A report nobody sees is the same as no report.
    if (process.env.GITHUB_STEP_SUMMARY) {
      const lines = [
        `### docs parity — informed (${enPages.length} English pages x ${locales.length} locales)`,
        '',
        `- blocking findings: **${blocking.length}**`,
        `- English-only backlog: **${backlogRoutes.size}** page(s)`,
        '',
      ]
      if (backlogRoutes.size)
        lines.push('<details><summary>backlog</summary>', '', ...[...backlogRoutes].sort().map((r) => `- ${r}`), '', '</details>', '')
      try {
        fs.appendFileSync(process.env.GITHUB_STEP_SUMMARY, lines.join('\n') + '\n')
      } catch {
        // A summary we could not write is not a verdict we got wrong; the same
        // content already went to stdout, which is the log the job keeps.
      }
    }
  } else if (findings.length === 0) {
    // NEVER claim more than was proved. With active waivers the tree is not "fully
    // translated" — it is fully translated OR explicitly, datedly exempted, and the
    // count of exemptions is part of the verdict.
    process.stdout.write(
      totalWaived === 0
        ? 'check-docs-parity: OK — every English page has a translation in every locale.\n'
        : `check-docs-parity: OK — every English page is translated in every locale, or covered by one of ${totalWaived} active waiver(s).\n`
    )
  } else if (STRICT) {
    process.stdout.write(`\ncheck-docs-parity: ${findings.length} finding(s) — FAILING (--strict).\n`)
  } else {
    process.stdout.write(
      `\ncheck-docs-parity: ${findings.length} finding(s) — REPORT mode, not failing the gate.\n` +
        'Translate the page, or add a dated waiver with a reason to docs-site/i18n-parity-waivers.json.\n' +
        'Run with --strict (or flip Taskfile lint:docs-parity) to make this a hard gate.\n'
    )
  }
}

// --informed fails ONLY on the blocking set; --strict still fails on everything.
// Neither can turn a hard error into a pass: hardError() exits 2 before reaching here.
process.exit((INFORMED ? blocking.length > 0 : STRICT && findings.length > 0) ? 1 : 0)
