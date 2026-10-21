#!/usr/bin/env node
// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// test:robots — the published robots.txt says what the project decided it says.
//
// The project decision of 2026-10-17 (the same as olivares.ai): the public docs are open to search,
// answer AND training crawlers of the major assistants; crawlers that ignore robots rules or
// resell data are refused for the whole site. Until 2026-09-01 this site served no robots.txt of
// its own and nothing noticed, so this check reads dist/ — the tree that is deployed — and not
// public/: a file in public/ is a promise that only the build keeps.
//
// Three answers, given by the EXIT CODE, not by prose:
//   0  clean · 1  finding · 2  could not look
// `--self-test` forces both halves and checks that each exits with its code.

import { readFile } from 'node:fs/promises'
import { existsSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const DIST_ROBOTS = fileURLToPath(new URL('../dist/robots.txt', import.meta.url))
const SITEMAP = 'https://docs.olivares.ai/sitemap-index.xml'

// The same lists as olivares.ai's robots.txt, restated here and not derived: the other repository
// is not on disk when this runs, and a derivation that cannot read its source would report
// "clean" without having looked. This check proves THIS file matches the lists below; it cannot
// see whether the apex moved. Parity between the two hosts is a declared risk, not a covered one.
// Names are matched case-insensitively, as crawlers match them.
const MUST_ALLOW = [
  '*', 'Googlebot', 'Bingbot', 'OAI-SearchBot', 'ChatGPT-User', 'Claude-SearchBot', 'Claude-User',
  'PerplexityBot', 'Perplexity-User', 'Applebot', 'DuckAssistBot', 'YouBot',
  'ClaudeBot', 'GPTBot', 'Google-Extended', 'Applebot-Extended', 'CCBot', 'meta-externalagent',
  'FacebookBot', 'cohere-ai', 'cohere-training-data-crawler', 'AI2Bot', 'Ai2Bot-Dolma',
]
const MUST_REFUSE = [
  'Bytespider', 'Diffbot', 'Omgili', 'Omgilibot', 'ImagesiftBot', 'Webzio-Extended', 'Timpibot', 'PanguBot',
]

/**
 * Parse robots.txt into groups. Consecutive `User-agent:` lines share one rule block, and a new
 * group starts at the first `User-agent:` after a rule. Blank lines and comments do not end a group:
 * crawlers apply a rule that follows a blank line to the group above it, so a parser that closed
 * the group there would report a crawler as open while it is not. Agent names are lower-cased.
 * @returns {{agents: string[], rules: {directive: string, value: string}[]}[]}
 */
function parseGroups(text) {
  const groups = []
  let current = null
  let expectingAgents = true
  for (const raw of text.split('\n')) {
    const line = raw.replace(/#.*$/, '').trim()
    if (!line) continue
    const idx = line.indexOf(':')
    if (idx === -1) continue
    const directive = line.slice(0, idx).trim().toLowerCase()
    const value = line.slice(idx + 1).trim()
    if (directive === 'user-agent') {
      if (!current || !expectingAgents) { current = { agents: [], rules: [] }; groups.push(current) }
      current.agents.push(value.toLowerCase())
      expectingAgents = true
    } else if (directive === 'sitemap') {
      groups.push({ agents: ['__sitemap__'], rules: [{ directive, value }] })
    } else if (current) {
      current.rules.push({ directive, value })
      expectingAgents = false
    } else {
      groups.push({ agents: ['__orphan__'], rules: [{ directive, value }] })
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
    found.push(`does not declare \`Sitemap: ${SITEMAP}\` (found: ${sitemaps.length ? sitemaps.join(', ') : 'none'})`)
  }

  if (groups.some((g) => g.agents.includes('__orphan__'))) found.push('a rule comes before the first `User-agent:` line')
  const known = new Set(['user-agent', 'allow', 'disallow', 'sitemap'])
  const unknown = [...new Set(groups.flatMap((g) => g.rules).map((r) => r.directive).filter((d) => !known.has(d)))]
  if (unknown.length) found.push(`directive(s) outside the project decision: ${unknown.join(', ')}`)

  // A crawler that is named in two groups is read by each parser differently: refuse it.
  const seen = new Map()
  for (const group of groups) for (const agent of group.agents) seen.set(agent, (seen.get(agent) ?? 0) + 1)
  const repeated = [...seen].filter(([agent, n]) => n > 1 && !agent.startsWith('__')).map(([agent]) => agent)
  if (repeated.length) found.push(`named in more than one group: ${repeated.join(', ')}`)

  const groupOf = (agent) => groups.find((g) => g.agents.includes(agent.toLowerCase()))
  const allows = (g, path) => g.rules.some((r) => r.directive === 'allow' && r.value === path)
  const disallowed = (g) => g.rules.filter((r) => r.directive === 'disallow' && r.value !== '')

  // "Has an Allow: /" is not "is open": a group that also disallows a path takes pages out of the
  // index. A crawler counts as open only if its group allows the root and disallows nothing.
  const notOpen = MUST_ALLOW.filter((agent) => {
    const g = groupOf(agent)
    return !g || !allows(g, '/') || disallowed(g).length > 0
  })
  if (notOpen.length) found.push(`${notOpen.length} crawler(s) not fully open (no group, no \`Allow: /\`, or a Disallow): ${notOpen.join(', ')}`)

  // "Has a Disallow: /" is not "is refused": any Allow in the group (`/`, `/*`, `/llms.txt`) lets the
  // crawler in somewhere, and the longest matching rule wins.
  const notRefused = MUST_REFUSE.filter((agent) => {
    const g = groupOf(agent)
    return !g || !g.rules.some((r) => r.directive === 'disallow' && r.value === '/') || g.rules.some((r) => r.directive === 'allow')
  })
  if (notRefused.length) found.push(`${notRefused.length} crawler(s) not refused for the whole site: ${notRefused.join(', ')}`)

  return found
}

if (process.argv.includes('--exit-probe')) {
  // Internal mode of the self-test: audit the file given and exit with the real code, so the
  // self-test can measure EXIT CODES and not return values.
  const target = process.argv[process.argv.indexOf('--exit-probe') + 1]
  if (!target || !existsSync(target)) {
    console.error('robots: COULD NOT LOOK — the probe cannot find the file.')
    process.exit(2)
  }
  const findings = audit(await readFile(target, 'utf8'))
  if (findings.length) { console.error('✗ ' + findings.join('\n  - ')); process.exit(1) }
  console.log('✓ clean'); process.exit(0)
}

if (process.argv.includes('--self-test')) {
  // A POSITIVE CONTROL FOR EACH HALF. An `audit()` that always returned `[]` would pass, and a
  // gate that cannot fail is not a gate.
  const good = await readFile(new URL('../public/robots.txt', import.meta.url), 'utf8')
  const clean = audit(good)
  if (clean.length) { console.error(`✗ self-test: the REAL robots.txt has findings and should not:\n  - ${clean.join('\n  - ')}`); process.exit(1) }

  const mutants = [
    ['without the Sitemap line', good.replace(/^Sitemap:.*$/m, '')],
    ['with GPTBot moved to the refused group', good.replace('User-agent: GPTBot\n', '').replace('User-agent: Bytespider\n', 'User-agent: GPTBot\nUser-agent: Bytespider\n')],
    ['with the catch-all closed', good.replace('User-agent: *\n', '').replace('User-agent: Bytespider\n', 'User-agent: *\nUser-agent: Bytespider\n')],
    ['with Bytespider moved to the open group', good.replace('User-agent: Bytespider\n', '').replace('User-agent: Googlebot\n', 'User-agent: Googlebot\nUser-agent: Bytespider\n')],
    ['with the open group disallowing one path', good.replace('Allow: /\n', 'Allow: /\nDisallow: /reference/\n')],
    ['with the open group without Allow', good.replace('Allow: /\n', '')],
    ['with a rule after a blank line in the open group', good.replace('Allow: /\n', 'Allow: /\n\nDisallow: /reference/\n')],
    ['with a Content-Signal line in the open group', good.replace('Allow: /\n', 'Allow: /\nContent-Signal: ai-train=no\n')],
    ['with a rule before the first User-agent', `Disallow: /\n${good}`],
    ['with a second group for ClaudeBot', `${good}\nUser-agent: ClaudeBot\nDisallow: /reference/\n`],
    // The two below are the ones that slipped through a predicate which only looked for `Disallow: /`.
    ['with a refused crawler re-opened in its own group', good.replace('Disallow: /\n', 'Disallow: /\nAllow: /\n')],
    ['with a refused crawler allowed one file', good.replace('Disallow: /\n', 'Disallow: /\nAllow: /llms.txt\n')],
    ['without the refused group', good.replace(/User-agent: Bytespider[\s\S]*?Disallow: \/\n/, '')],
  ]
  for (const [name, text] of mutants) {
    if (text === good) { console.error(`✗ self-test: the mutant «${name}» was NOT APPLIED — the control would prove nothing`); process.exit(1) }
    if (!audit(text).length) { console.error(`✗ self-test: the mutant «${name}» SURVIVES — the gate does not see it`); process.exit(1) }
    console.log(`  ok  mutant «${name}» dies`)
  }
  // And the half that the first version of this self-test skipped: it only called `audit()` and
  // exited 0 — it proved the FUNCTION, not the PROGRAM, while its message promised three codes.
  // This launches the process for real.
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
    const ok = join(dir, 'ok.txt'); await writeFile(ok, good)
    const bad = join(dir, 'bad.txt'); await writeFile(bad, good.replace(/^Sitemap:.*$/m, ''))
    const cases = [
      ['0 on the real file', ['--exit-probe', ok], 0],
      ['1 on a file with a finding', ['--exit-probe', bad], 1],
      ['2 on a file that does not exist', ['--exit-probe', join(dir, 'missing.txt')], 2],
    ]
    for (const [name, args, expected] of cases) {
      const code = run(args)
      if (code !== expected) { console.error(`✗ self-test: «${name}» exited ${code}, expected ${expected}`); process.exit(1) }
      console.log(`  ok  exit code ${expected}: ${name}`)
    }
  } finally {
    await rm(dir, { recursive: true, force: true })
  }

  console.log(`✓ robots self-test: the real file is clean, the ${mutants.length} mutants die and the three exit codes are measured by RUNNING the program`)
  process.exit(0)
}

if (!existsSync(DIST_ROBOTS)) {
  console.error('robots: COULD NOT LOOK — dist/robots.txt does not exist.')
  console.error('  Either the build did not run or `public/robots.txt` stopped being copied — which is the defect, not the excuse.')
  process.exit(2)
}

const text = await readFile(DIST_ROBOTS, 'utf8')
const findings = audit(text)
if (findings.length) {
  console.error('✗ robots: the published policy is not the one the project decided.\n')
  for (const f of findings) console.error(`  - ${f}`)
  console.error('\n  The policy lives in docs-site/public/robots.txt and is the apex one (web repo, public/robots.txt).')
  process.exit(1)
}
console.log(`✓ robots: OK — Sitemap declared, ${MUST_ALLOW.length} crawlers open and ${MUST_REFUSE.length} refused.`)
process.exit(0)
