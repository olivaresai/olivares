// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Verify final HTML hashes and CSP placement independently of seal-csp.
// The catch-all exclusively owns required response headers. Additional rules
// may set unrelated headers. Check dist/_headers against the source copy.
// Self-tests include accepted controls and rejected policy mutations.
// Exit: 0 valid, 1 invalid artifact, 2 artifact unavailable.

import { readdir, readFile, mkdtemp, writeFile, mkdir, rm } from 'node:fs/promises'
import { existsSync } from 'node:fs'
import { createHash } from 'node:crypto'
import { fileURLToPath, pathToFileURL } from 'node:url'
import { spawnSync } from 'node:child_process'
import { tmpdir } from 'node:os'
import path from 'node:path'

const HERE = path.dirname(fileURLToPath(import.meta.url))
const DIST = path.join(HERE, '..', 'dist')
const DIST_HEADERS = path.join(DIST, '_headers')
const PUBLIC_HEADERS = path.join(HERE, '..', 'public', '_headers')

const CSP_META = /<meta http-equiv="content-security-policy" content="([^"]*)"\s*\/?>/i
const SCRIPT_EL = /<script([^>]*)>([\s\S]*?)<\/script>/gi
const STYLE_EL = /<style([^>]*)>([\s\S]*?)<\/style>/gi
/** Anything whose loading the policy must already govern when the parser reaches it. */
const FIRST_GOVERNED = /<script|<style|<link[^>]+rel="?stylesheet/i

/** Limits the Worker's own `_headers` parser enforces (wrangler 4.128.0, workers-shared). */
const MAX_HEADER_RULES = 100
const MAX_HEADER_LINE = 2000
/** Same predicate the asset runtime uses to open a rule (miniflare parseHeaders). */
const LINE_IS_PROBABLY_A_PATH = /^([^\s]+:\/\/|^\/)/
const UNSET_OPERATOR = '! '

/**
 * The required response headers. Values are exact: a policy that drifts to a weaker value should
 * fail here rather than be discovered in a browser.
 */
const REQUIRED_HEADERS = {
  'x-frame-options': 'DENY',
  'x-content-type-options': 'nosniff',
  'referrer-policy': 'strict-origin-when-cross-origin',
  'content-security-policy': "frame-ancestors 'none'",
}

/** The RFC 9116 override this work must not disturb. */
const SECURITY_TXT_PATH = '/.well-known/security.txt'
const SECURITY_TXT_TYPE = 'text/plain; charset=utf-8'

/** Directives whose value the built pages were verified against in a real browser. */
const REQUIRED_DIRECTIVES = {
  'default-src': "'none'",
  'object-src': "'none'",
  'base-uri': "'none'",
  'frame-src': "'none'",
  'img-src': "'self' data:",
  'font-src': "'self'",
  'connect-src': "'self'",
  'worker-src': "'self'",
  'form-action': "'self'",
  'style-src-attr': "'unsafe-inline'",
}

const FORBIDDEN_SCRIPT_SOURCES = ["'unsafe-inline'", "'unsafe-eval'", '*', 'http:', 'https:', 'data:']

const sha256 = (body) => `'sha256-${createHash('sha256').update(body, 'utf8').digest('base64')}'`

/** First-wins Map, matching CSP3. Later duplicates are reported separately, not overwritten. */
export function parseDirectives(content) {
  const out = new Map()
  for (const part of content.split(';')) {
    const trimmed = part.trim()
    if (!trimmed) continue
    const [name, ...values] = trimmed.split(/\s+/)
    const key = name.toLowerCase()
    if (!out.has(key)) out.set(key, values)
  }
  return out
}

export function duplicateDirectiveNames(content) {
  const seen = new Set()
  const dups = []
  for (const part of content.split(';')) {
    const trimmed = part.trim()
    if (!trimmed) continue
    const key = trimmed.split(/\s+/)[0].toLowerCase()
    if (seen.has(key)) {
      if (!dups.includes(key)) dups.push(key)
    } else {
      seen.add(key)
    }
  }
  return dups
}

/** Every finding this document produces. Empty means the page is clean. */
export function checkDocument(html, label) {
  const problems = []
  const found = CSP_META.exec(html)
  if (!found) return [`${label}: no Content-Security-Policy meta element`]

  const governed = FIRST_GOVERNED.exec(html)
  if (governed && governed.index < found.index) {
    problems.push(
      `${label}: the policy is at byte ${found.index}, after a ${governed[0]} at byte ${governed.index} — ` +
        'a meta policy does not govern what the parser already read',
    )
  }

  const dups = duplicateDirectiveNames(found[1])
  if (dups.length) {
    problems.push(
      `${label}: duplicate CSP directive(s) ${dups.map((n) => `\`${n}\``).join(', ')} — ` +
        'browsers honour the first occurrence and ignore the rest',
    )
  }

  const directives = parseDirectives(found[1])

  for (const [name, expected] of Object.entries(REQUIRED_DIRECTIVES)) {
    const values = directives.get(name)
    if (!values) problems.push(`${label}: missing directive \`${name}\``)
    else if (values.join(' ') !== expected)
      problems.push(`${label}: \`${name}\` is \`${values.join(' ')}\`, expected \`${expected}\``)
  }

  const scriptSrc = directives.get('script-src')
  if (!scriptSrc) problems.push(`${label}: missing directive \`script-src\``)
  else
    for (const forbidden of FORBIDDEN_SCRIPT_SOURCES)
      if (scriptSrc.includes(forbidden)) problems.push(`${label}: \`script-src\` allows \`${forbidden}\``)

  const styleSrc = directives.get('style-src')
  if (!styleSrc) problems.push(`${label}: missing directive \`style-src\``)
  else if (styleSrc.includes("'unsafe-inline'"))
    problems.push(`${label}: \`style-src\` allows \`'unsafe-inline'\` — element styles are hash-only here`)

  // Every inline script and style in the bytes we ship must be covered, derived independently.
  const scriptHashes = new Set(scriptSrc || [])
  let n = 0
  for (const m of html.matchAll(SCRIPT_EL)) {
    if (/\ssrc\s*=/i.test(m[1])) continue
    n++
    if (!scriptHashes.has(sha256(m[2])))
      problems.push(
        `${label}: inline <script${m[1].trim() ? ' ' + m[1].trim().slice(0, 40) : ''}> #${n} ` +
          `is not covered by \`script-src\` (${sha256(m[2])})`,
      )
  }
  const styleHashes = new Set(styleSrc || [])
  let s = 0
  for (const m of html.matchAll(STYLE_EL)) {
    s++
    if (!styleHashes.has(sha256(m[2])))
      problems.push(`${label}: inline <style> #${s} is not covered by \`style-src\` (${sha256(m[2])})`)
  }

  return problems
}

function isAcceptedRulePath(line) {
  return line.startsWith('/') || /^https:\/\//i.test(line)
}

/**
 * Reads `_headers` the way the Worker's parser opens rules: a line matching
 * `LINE_IS_PROBABLY_A_PATH` starts a rule (`/` or `scheme://`). Following `name: value`
 * lines belong to it; `! Name` unsets. Repeated names inside one rule join with `, `.
 * A path form this checker does not interpret is a finding — it is not skipped as a header.
 */
export function parseHeadersFile(text) {
  const rules = []
  const problems = []
  let current = null
  let skipUntilNextPath = false
  const lines = text.split('\n')
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i].trim()
    if (!line || line.startsWith('#')) continue
    if (line.length > MAX_HEADER_LINE) {
      problems.push(`_headers line ${i + 1} is ${line.length} chars — the Worker ignores lines over ${MAX_HEADER_LINE}`)
      continue
    }
    if (LINE_IS_PROBABLY_A_PATH.test(line)) {
      if (!isAcceptedRulePath(line)) {
        problems.push(`_headers line ${i + 1} is a path the checker cannot interpret: ${line}`)
        current = null
        skipUntilNextPath = true
        continue
      }
      skipUntilNextPath = false
      current = { path: line, headers: new Map(), unset: [], line: i + 1 }
      rules.push(current)
      continue
    }
    if (skipUntilNextPath) {
      problems.push(`_headers line ${i + 1} follows an uninterpretable path: ${line}`)
      continue
    }
    if (line.startsWith(UNSET_OPERATOR)) {
      if (!current) {
        problems.push(`_headers line ${i + 1} unsets a header before any path`)
        continue
      }
      const name = line.slice(UNSET_OPERATOR.length).trim().toLowerCase()
      if (!name || name.includes(' ') || name.includes(':')) {
        problems.push(`_headers line ${i + 1} is not an interpretable unset: ${line}`)
        continue
      }
      current.unset.push(name)
      continue
    }
    const at = line.indexOf(':')
    if (at < 0) {
      problems.push(`_headers line ${i + 1} is neither a path nor a \`name: value\` pair: ${line}`)
      continue
    }
    if (!current) {
      problems.push(`_headers line ${i + 1} declares a header before any path`)
      continue
    }
    const name = line.slice(0, at).trim().toLowerCase()
    const value = line.slice(at + 1).trim()
    if (!name || name.includes(' ')) {
      problems.push(`_headers line ${i + 1} is not an interpretable header name: ${line}`)
      continue
    }
    if (!value) {
      problems.push(`_headers line ${i + 1} has no header value`)
      continue
    }
    const existing = current.headers.get(name)
    current.headers.set(name, existing ? `${existing}, ${value}` : value)
  }
  if (rules.length > MAX_HEADER_RULES)
    problems.push(`_headers declares ${rules.length} rules — the Worker keeps the first ${MAX_HEADER_RULES}`)
  return { rules, problems }
}

export function checkHeaders(text) {
  const { rules, problems } = parseHeadersFile(text)

  const catchAll = rules.filter((r) => r.path === '/*')
  if (catchAll.length === 0) {
    problems.push('_headers has no `/*` rule — the security headers would reach no page')
    return problems
  }
  const owner = catchAll[0]

  for (const [name, expected] of Object.entries(REQUIRED_HEADERS)) {
    if (owner.unset.includes(name)) {
      problems.push(`_headers: \`/*\` unsets required \`${name}\``)
      continue
    }
    const actual = owner.headers.get(name)
    if (actual === undefined) problems.push(`_headers: \`/*\` does not set \`${name}\``)
    else if (actual !== expected) problems.push(`_headers: \`${name}\` is \`${actual}\`, expected \`${expected}\``)
  }

  for (const rule of rules) {
    if (rule === owner) continue
    for (const name of Object.keys(REQUIRED_HEADERS)) {
      const sets = rule.headers.has(name)
      const unsets = rule.unset.includes(name)
      if (!sets && !unsets) continue
      problems.push(
        `_headers: rule \`${rule.path}\` (line ${rule.line}) ${unsets ? 'unsets' : 'sets'} required \`${name}\`; ` +
          'only the `/*` catch-all may own those names',
      )
    }
  }

  const securityTxt = rules.find((r) => r.path === SECURITY_TXT_PATH)
  if (!securityTxt) problems.push(`_headers: the RFC 9116 rule for \`${SECURITY_TXT_PATH}\` is gone`)
  else if (securityTxt.headers.get('content-type') !== SECURITY_TXT_TYPE)
    problems.push(
      `_headers: \`${SECURITY_TXT_PATH}\` sets \`${securityTxt.headers.get('content-type')}\`, ` +
        `expected \`${SECURITY_TXT_TYPE}\``,
    )
  else if (rules.indexOf(securityTxt) > rules.indexOf(catchAll[0]))
    problems.push(
      `_headers: the \`${SECURITY_TXT_PATH}\` rule comes after \`/*\`, which also matches it. ` +
        'The asset runtime sets a header from the first matching rule and appends later ones, so a ' +
        'Content-Type added to the catch-all would lead the value and push the RFC 9116 charset behind it',
    )

  return problems
}

/**
 * Asserts the shipped `dist/_headers` artifact. Optional `publicPath` must be byte-identical
 * when present; a build that failed to copy `public/` is a finding, not a skip.
 */
export async function checkHeadersArtifact(distPath, publicPath) {
  const problems = []
  if (!existsSync(distPath)) {
    problems.push('dist/_headers does not exist — the shipped artifact has no header rules')
    return problems
  }
  const distText = await readFile(distPath, 'utf8')
  problems.push(...checkHeaders(distText))
  if (publicPath !== undefined) {
    if (!existsSync(publicPath)) {
      problems.push('public/_headers does not exist to compare with dist/_headers')
    } else if ((await readFile(publicPath, 'utf8')) !== distText) {
      problems.push('dist/_headers differs from public/_headers')
    }
  }
  return problems
}

async function htmlFiles(dir, acc = []) {
  for (const entry of await readdir(dir, { withFileTypes: true })) {
    const full = path.join(dir, entry.name)
    if (entry.isDirectory()) await htmlFiles(full, acc)
    else if (entry.name.endsWith('.html')) acc.push(full)
  }
  return acc
}

// ---------------------------------------------------------------------------------------------
// Self-test: fixtures that each break exactly one rule. The gate must reject them and
// accept the controls. Without this, "the gate passed" says nothing about whether it can fail.
// ---------------------------------------------------------------------------------------------

const GOOD_SCRIPT = 'console.log(1)'
const GOOD_STYLE = 'body{color:red}'
function fixturePage({ meta = true, scriptBody = GOOD_SCRIPT, hashScript = true, late = false, directives } = {}) {
  const dirs =
    directives ??
    [
      ...Object.entries(REQUIRED_DIRECTIVES).map(([k, v]) => `${k} ${v}`),
      `script-src 'self'${hashScript ? ' ' + sha256(GOOD_SCRIPT) : ''}`,
      `style-src 'self' ${sha256(GOOD_STYLE)}`,
    ].join(';')
  const metaTag = meta ? `<meta http-equiv="content-security-policy" content="${dirs}">` : ''
  const body = `<script>${scriptBody}</script><style>${GOOD_STYLE}</style>`
  return late
    ? `<html><head><meta charset="utf-8"/>${body}${metaTag}</head><body></body></html>`
    : `<html><head><meta charset="utf-8"/>${metaTag}${body}</head><body></body></html>`
}

const GOOD_HEADERS = `${SECURITY_TXT_PATH}
  Content-Type: ${SECURITY_TXT_TYPE}
/*
  X-Frame-Options: DENY
  X-Content-Type-Options: nosniff
  Referrer-Policy: strict-origin-when-cross-origin
  Content-Security-Policy: frame-ancestors 'none'
`

function requiredExcept(skip) {
  return Object.entries(REQUIRED_DIRECTIVES)
    .filter(([k]) => k !== skip)
    .map(([k, v]) => `${k} ${v}`)
}

async function selfTest() {
  const cases = [
    ['control: a sealed page', () => checkDocument(fixturePage(), 'fixture'), false],
    ['a page with no policy at all', () => checkDocument(fixturePage({ meta: false }), 'fixture'), true],
    [
      'an inline script the policy does not cover',
      () => checkDocument(fixturePage({ hashScript: false }), 'fixture'),
      true,
    ],
    [
      'an inline script edited after the hash was taken',
      () => checkDocument(fixturePage({ scriptBody: GOOD_SCRIPT + ';fetch("/x")' }), 'fixture'),
      true,
    ],
    [
      'a policy that arrives after the scripts it governs',
      () => checkDocument(fixturePage({ late: true }), 'fixture'),
      true,
    ],
    [
      "a script-src widened with 'unsafe-inline'",
      () =>
        checkDocument(
          fixturePage({
            directives: [
              ...Object.entries(REQUIRED_DIRECTIVES).map(([k, v]) => `${k} ${v}`),
              `script-src 'self' 'unsafe-inline' ${sha256(GOOD_SCRIPT)}`,
              `style-src 'self' ${sha256(GOOD_STYLE)}`,
            ].join(';'),
          }),
          'fixture',
        ),
      true,
    ],
    [
      'a duplicated object-src, permissive first',
      () =>
        checkDocument(
          fixturePage({
            directives: [
              ...requiredExcept('object-src'),
              "object-src https:",
              "object-src 'none'",
              `script-src 'self' ${sha256(GOOD_SCRIPT)}`,
              `style-src 'self' ${sha256(GOOD_STYLE)}`,
            ].join(';'),
          }),
          'fixture',
        ),
      true,
    ],
    [
      'a duplicated script-src, permissive first',
      () =>
        checkDocument(
          fixturePage({
            directives: [
              ...Object.entries(REQUIRED_DIRECTIVES).map(([k, v]) => `${k} ${v}`),
              `script-src 'self' 'unsafe-inline'`,
              `script-src 'self' ${sha256(GOOD_SCRIPT)}`,
              `style-src 'self' ${sha256(GOOD_STYLE)}`,
            ].join(';'),
          }),
          'fixture',
        ),
      true,
    ],
    ['control: the required header rules', () => checkHeaders(GOOD_HEADERS), false],
    [
      '_headers without frame-ancestors',
      () => checkHeaders(GOOD_HEADERS.replace(/\n  Content-Security-Policy: [^\n]*/, '')),
      true,
    ],
    [
      '_headers with the security.txt charset dropped',
      () => checkHeaders(GOOD_HEADERS.replace('text/plain; charset=utf-8', 'text/plain')),
      true,
    ],
    [
      '_headers with a weakened referrer policy',
      () => checkHeaders(GOOD_HEADERS.replace('strict-origin-when-cross-origin', 'unsafe-url')),
      true,
    ],
    [
      '_headers with the narrow security.txt rule moved behind the catch-all',
      () =>
        checkHeaders(
          `/*\n  X-Frame-Options: DENY\n  X-Content-Type-Options: nosniff\n` +
            `  Referrer-Policy: strict-origin-when-cross-origin\n` +
            `  Content-Security-Policy: frame-ancestors 'none'\n` +
            `${SECURITY_TXT_PATH}\n  Content-Type: ${SECURITY_TXT_TYPE}\n`,
        ),
      true,
    ],
    [
      '_headers with a rule line the Worker would drop for length',
      () => checkHeaders(GOOD_HEADERS + `/long\n  X-Test: ${'a'.repeat(2100)}\n`),
      true,
    ],
    [
      'a later /* rule that weakens Referrer-Policy',
      () => checkHeaders(GOOD_HEADERS + '\n/*\n  Referrer-Policy: unsafe-url\n'),
      true,
    ],
    [
      'a later /start/* rule that weakens Referrer-Policy',
      () => checkHeaders(GOOD_HEADERS + '\n/start/*\n  Referrer-Policy: unsafe-url\n'),
      true,
    ],
    [
      'a later /* rule that adds X-Frame-Options: SAMEORIGIN',
      () => checkHeaders(GOOD_HEADERS + '\n/*\n  X-Frame-Options: SAMEORIGIN\n'),
      true,
    ],
    [
      'a later /* rule that adds a second Content-Security-Policy',
      () => checkHeaders(GOOD_HEADERS + "\n/*\n  Content-Security-Policy: default-src *\n"),
      true,
    ],
    [
      'a later absolute-URL rule that sets a required header',
      () => checkHeaders(GOOD_HEADERS + '\nhttps://docs.olivares.ai/*\n  Referrer-Policy: unsafe-url\n'),
      true,
    ],
    [
      'a later rule that unsets a required header',
      () => checkHeaders(GOOD_HEADERS + '\n/start/*\n  ! X-Frame-Options\n'),
      true,
    ],
    [
      'an uninterpretable http:// path line',
      () => checkHeaders(GOOD_HEADERS + '\nhttp://example.com/*\n  X-Test: 1\n'),
      true,
    ],
    [
      'control: an unrelated header on a narrow path',
      () => checkHeaders(GOOD_HEADERS + '\n/assets/*\n  Cache-Control: max-age=3600\n'),
      false,
    ],
    [
      'control: an unrelated header on a second /* rule',
      () => checkHeaders(GOOD_HEADERS + '\n/*\n  Cache-Control: max-age=3600\n'),
      false,
    ],
    [
      'control: an absolute-URL line opens a rule, not a header named https',
      () => {
        const { rules, problems } = parseHeadersFile(
          'https://docs.olivares.ai/*\n  Cache-Control: max-age=1\n',
        )
        if (problems.length) return problems
        if (rules.length !== 1 || rules[0].path !== 'https://docs.olivares.ai/*') {
          return ['did not open an absolute-URL rule']
        }
        if (rules[0].headers.has('https')) return ['parsed the URL as a header named https']
        if (rules[0].headers.get('cache-control') !== 'max-age=1') return ['lost the header on the absolute-URL rule']
        return []
      },
      false,
    ],
  ]

  let failed = 0
  for (const [name, run, mustFail] of cases) {
    const problems = run()
    const rejected = problems.length > 0
    if (rejected !== mustFail) {
      failed++
      console.error(`✗ self-test: "${name}" — expected ${mustFail ? 'rejection' : 'acceptance'}, got ${problems.length} problem(s)`)
      for (const p of problems.slice(0, 3)) console.error(`    ${p}`)
    } else {
      console.log(`  ok  ${mustFail ? 'rejected' : 'accepted'}: ${name}`)
    }
  }

  // The end-to-end half: a real directory, so the walk and the file reads are exercised too.
  const dir = await mkdtemp(path.join(tmpdir(), 'olv-policy-'))
  try {
    await mkdir(path.join(dir, 'ok'), { recursive: true })
    await writeFile(path.join(dir, 'ok', 'index.html'), fixturePage())
    const walked = await htmlFiles(dir)
    const problems = (await Promise.all(walked.map(async (f) => checkDocument(await readFile(f, 'utf8'), f)))).flat()
    if (problems.length) {
      failed++
      console.error(`✗ self-test: the directory walk rejected a clean page: ${problems[0]}`)
    } else {
      console.log('  ok  accepted: a clean page found by the directory walk')
    }
  } finally {
    await rm(dir, { recursive: true, force: true })
  }

  const artifactDir = await mkdtemp(path.join(tmpdir(), 'olv-headers-'))
  let extraCases = 0
  try {
    const distOk = path.join(artifactDir, 'dist-ok', '_headers')
    const distBad = path.join(artifactDir, 'dist-bad', '_headers')
    const distMissing = path.join(artifactDir, 'dist-missing', '_headers')
    const publicOk = path.join(artifactDir, 'public-ok', '_headers')
    await mkdir(path.dirname(distOk), { recursive: true })
    await mkdir(path.dirname(distBad), { recursive: true })
    await mkdir(path.dirname(publicOk), { recursive: true })
    await writeFile(distOk, GOOD_HEADERS)
    await writeFile(publicOk, GOOD_HEADERS)
    await writeFile(distBad, GOOD_HEADERS.replace('strict-origin-when-cross-origin', 'unsafe-url'))

    const artifactCases = [
      ['control: dist/_headers matches public/_headers', await checkHeadersArtifact(distOk, publicOk), false],
      ['dist/_headers is missing', await checkHeadersArtifact(distMissing, publicOk), true],
      ['dist/_headers is weakened', await checkHeadersArtifact(distBad, undefined), true],
      ['dist/_headers differs from public/_headers', await checkHeadersArtifact(distBad, publicOk), true],
    ]
    extraCases = artifactCases.length
    for (const [name, problems, mustFail] of artifactCases) {
      const rejected = problems.length > 0
      if (rejected !== mustFail) {
        failed++
        console.error(`✗ self-test: "${name}" — expected ${mustFail ? 'rejection' : 'acceptance'}, got ${problems.length} problem(s)`)
        for (const p of problems.slice(0, 3)) console.error(`    ${p}`)
      } else {
        console.log(`  ok  ${mustFail ? 'rejected' : 'accepted'}: ${name}`)
      }
    }
  } finally {
    await rm(artifactDir, { recursive: true, force: true })
  }

  extraCases += 1
  const child = spawnSync(
    process.execPath,
    [
      '--input-type=module',
      '-e',
      `import { checkDocument, checkHeaders, parseDirectives, parseHeadersFile } from ${JSON.stringify(pathToFileURL(fileURLToPath(import.meta.url)).href)};
       console.log('imported', typeof checkDocument, typeof checkHeaders, typeof parseDirectives, typeof parseHeadersFile);
       process.exit(42);`,
    ],
    { encoding: 'utf8', timeout: 15000 },
  )
  if (child.status !== 42) {
    failed++
    console.error(
      `✗ self-test: "importing helpers does not run the CLI" — expected exit 42, got ${child.status}` +
        (child.stdout ? `\n    stdout: ${child.stdout.trim().slice(0, 200)}` : '') +
        (child.stderr ? `\n    stderr: ${child.stderr.trim().slice(0, 200)}` : ''),
    )
  } else if (!child.stdout.includes('imported function function function function')) {
    failed++
    console.error(`✗ self-test: "importing helpers does not run the CLI" — missing export types: ${child.stdout.trim()}`)
  } else {
    console.log('  ok  accepted: importing helpers does not run the CLI')
  }

  const total = cases.length + 1 + extraCases
  if (failed) {
    console.error(`✗ check-response-policy --self-test: ${failed} case(s) behaved wrongly.`)
    process.exit(1)
  }
  console.log(`✓ check-response-policy --self-test: ${total} cases, the gate fails where it must.`)
  process.exit(0)
}

async function main() {
  if (process.argv.includes('--self-test')) return selfTest()

  if (!existsSync(DIST)) {
    console.error('check-response-policy: COULD NOT LOOK — dist/ does not exist. Run `npm run build` first.')
    process.exit(2)
  }
  const files = await htmlFiles(DIST)
  if (files.length === 0) {
    console.error('check-response-policy: COULD NOT LOOK — dist/ contains no HTML.')
    process.exit(2)
  }

  const problems = await checkHeadersArtifact(DIST_HEADERS, PUBLIC_HEADERS)
  let pagesWithProblems = 0
  for (const file of files) {
    const found = checkDocument(await readFile(file, 'utf8'), path.relative(DIST, file))
    if (found.length) {
      pagesWithProblems++
      problems.push(...found)
    }
  }

  if (problems.length) {
    console.error(`✗ check-response-policy: ${problems.length} problem(s) across ${pagesWithProblems} of ${files.length} page(s).`)
    for (const p of problems.slice(0, 12)) console.error(`  - ${p}`)
    if (problems.length > 12) console.error(`  … and ${problems.length - 12} more`)
    process.exit(1)
  }

  console.log(
    `✓ check-response-policy: ${files.length} page(s) carry a hash-complete policy ahead of every ` +
      'script and style, and dist/_headers sets the four required headers with the RFC 9116 rule intact.',
  )
  process.exit(0)
}

const invokedAsCli =
  Boolean(process.argv[1]) && fileURLToPath(import.meta.url) === path.resolve(process.argv[1])
if (invokedAsCli) await main()
