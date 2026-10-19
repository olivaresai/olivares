#!/usr/bin/env node
// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// test:llms — the built /llms.txt and /llms-full.txt say what ships and point at real pages.
//
// It reads dist/, the tree that is deployed (src/llms.mjs renders both files at build time), and
// checks that:
//   - every docs.olivares.ai URL in llms.txt is a file in dist/, and the pages listed are exactly
//     the English pages of src/content/docs (none missing, no locale or archived page), grouped in
//     the five sections, in order;
//   - llms.txt links back to https://olivares.ai and https://olivares.ai/llms.txt;
//   - the tools it says Olivares AI runs are the sessions in core/driverfacts/facts.go, and no
//     tool that is not shipped appears in the summary;
//   - dist/_headers serves both files as UTF-8 plain text;
//   - llms-full.txt has one section per listed page, in the same order, each with the code blocks
//     of its source page and no JSX, directive, comment, import, template token or root-relative
//     link left in the prose.
//
// The origins and the title are restated here and not imported: if the generator got one wrong,
// a check that read it from the generator would agree with the mistake.
//
// Three answers, given by the EXIT CODE: 0 clean · 1 finding · 2 could not look.
// `--self-test` runs the converter on fixtures, mutates the real output and checks that every
// mutant is found and that the program exits with each code. `--llms <file>` / `--full <file>`
// audit another file (self-test).

import { existsSync, readFileSync, readdirSync } from 'node:fs'
import { execFileSync } from 'node:child_process'
import { fileURLToPath } from 'node:url'
import { join, relative, sep } from 'node:path'
import { countCorePaths } from '../src/core-paths-plugin.mjs'
import { ARCHIVED_SLUGS, PUBLISHED_LOCALES } from '../src/site-locales.mjs'

let SESSION_TOOLS, pagesBySection, toMarkdown
try {
  ;({ SESSION_TOOLS, pagesBySection, toMarkdown } = await import('../src/llms.mjs'))
} catch (error) {
  console.error(`llms: COULD NOT LOOK — src/llms.mjs does not load: ${error.message}`)
  process.exit(2)
}

const DOCS_ORIGIN = 'https://docs.olivares.ai'
const SITE_ORIGIN = 'https://olivares.ai'
const TITLE = 'Olivares AI documentation'
const SECTIONS = [['start', 'Start here'], ['tutorials', 'Tutorials'], ['how-to', 'How-to guides'], ['explanation', 'Explanation'], ['reference', 'Reference']]

const here = (path) => fileURLToPath(new URL(path, import.meta.url))
const DIST = here('../dist')
const FACTS = here('../../core/driverfacts/facts.go')
const DOCS = here('../src/content/docs')
const OTHER_TREES = new Set([...PUBLISHED_LOCALES, ...ARCHIVED_SLUGS])
// Tools that are not shipped as sessions: planned ones, and ones only observed through connectors.
const NOT_SHIPPED = /\b(Cursor|Antigravity|OpenHands|goose|Cline|Windsurf)\b/g
const FENCE = /^[\s>]*(`{3,}|~{3,})/
// What must not survive in the prose of llms-full.txt.
const LEAKS = [
  ['JSX component line', /^[\s>]*<\/?[A-Z][A-Za-z0-9]*(\s|\/|>|$)/],
  ['Starlight ::: directive', /^[\s>]*:::/],
  ['HTML or MDX comment', /<!--|-->|\{\/\*/],
  ['unreplaced {{core-paths}} token', /\{\{core-paths\}\}/],
  ['raw <img> tag', /^[\s>]*<img\b/],
  ['MDX import line', /^import\s+(\{|\*|\w+\s+from|['"])/],
  ['root-relative link', /\]\(\//],
]

/** Map of URL to source text for the English pages in the source tree, as llms.txt must list them. */
function sourcePages() {
  const pages = new Map()
  const walk = (dir) => {
    for (const entry of readdirSync(dir, { withFileTypes: true })) {
      const path = join(dir, entry.name)
      if (entry.isDirectory()) walk(path)
      else if (/\.mdx?$/.test(entry.name) && !entry.name.startsWith('_')) {
        const id = relative(DOCS, path).split(sep).join('/').replace(/\.mdx?$/, '').replace(/(^|\/)index$/, '')
        if (!id || OTHER_TREES.has(id.split('/')[0])) continue // the splash home page, locales, snapshots
        const text = readFileSync(path, 'utf8')
        const [, frontmatter = '', body = text] = /^---\n([\s\S]*?)\n---\n?([\s\S]*)$/.exec(text) ?? []
        if (/^draft:\s*true\s*$/m.test(frontmatter)) continue
        pages.set(`${DOCS_ORIGIN}/${id}/`, body)
      }
    }
  }
  walk(DOCS)
  return pages
}

/** The tool names of the session rows in core/driverfacts/facts.go. */
const sessionToolsOf = (facts) => [...facts.matchAll(/Name: "([^"]+)",[^\n]*Session: true/g)].map((m) => m[1])

/** Lines of `text` outside fenced code blocks (a fence inside a blockquote counts). */
function proseLines(text) {
  const out = []
  let fence = null
  for (const line of text.split('\n')) {
    const marker = FENCE.exec(line)?.[1]
    if (fence) {
      if (marker && marker[0] === fence[0] && marker.length >= fence.length) fence = null
    } else if (marker) fence = marker
    else out.push(line)
  }
  return out
}

// A plain source line: nothing the converter rewrites (markup, links, comments, imports, attributes).
// Every one of them must survive in its section, so a swallowed table or paragraph cannot go unseen.
const normalize = (line) => line.replace(/^[\s>]*/, '').trimEnd()
const isPlain = (line) => line.length >= 20 && !/[<{]|:::|\]\(\/|^#|^import |="|-->/.test(line)
const plainLines = (body) => body.split('\n').map(normalize).filter(isPlain)

const fenceLines = (text) => text.split('\n').filter((line) => FENCE.test(line)).length

/** @returns {string[]} findings — empty means clean. */
function audit({ llms, full, headers, hasFile, expected, factsTools, corePaths }) {
  const found = []
  const lines = llms.split('\n')
  if (lines[0] !== `# ${TITLE}`) found.push(`llms.txt does not start with "# ${TITLE}"`)
  if (!lines[2]?.startsWith('> ')) found.push('llms.txt has no "> " summary after the title (llmstxt.org)')
  const date = lines.find((l) => l.startsWith('Last updated: '))?.slice('Last updated: '.length)
  if (!/^\d{4}-\d{2}-\d{2}$/.test(date ?? '') || Number.isNaN(Date.parse(date))) found.push(`llms.txt has no valid "Last updated: YYYY-MM-DD" line (found: ${date ?? 'none'})`)

  const [pagesPart, optionalPart = ''] = llms.split('\n## Optional')
  const entryOf = (line) => /^- \[([^\]]+)\]\(([^)\s]+)\)(: .+)?$/.exec(line)
  const bad = lines.filter((l) => l.startsWith('- ') && !entryOf(l))
  if (bad.length) found.push(`${bad.length} list line(s) are not "- [title](url): description": ${bad[0]}`)
  const pages = pagesPart.split('\n').map(entryOf).filter(Boolean)
  const optionalUrls = optionalPart.split('\n').map(entryOf).filter(Boolean).map((m) => m[2])
  const pageUrls = pages.map((m) => m[2])

  if (expected.size === 0) found.push('src/content/docs has no English pages to list: the source walk found nothing')
  const missing = [...expected.keys()].filter((u) => !pageUrls.includes(u))
  const extra = pageUrls.filter((u) => !expected.has(u))
  if (missing.length) found.push(`${missing.length} English page(s) of src/content/docs are not listed: ${missing.slice(0, 3).join(', ')}`)
  if (extra.length) found.push(`${extra.length} listed page(s) are not English pages of src/content/docs: ${extra.slice(0, 3).join(', ')}`)
  const undescribed = pages.filter((m) => !m[3])
  if (undescribed.length) found.push(`${undescribed.length} listed page(s) have no one-line description: ${undescribed[0][2]}`)

  // Sections: the five headings, in order, each holding the pages of its folder in ascending order.
  const headings = pagesPart.split('\n').filter((l) => l.startsWith('## ')).map((l) => l.slice(3))
  if (headings.join('|') !== SECTIONS.map(([, h]) => h).join('|')) found.push(`llms.txt sections are [${headings.join(', ')}], expected [${SECTIONS.map(([, h]) => h).join(', ')}]`)
  let dir
  let previous = ''
  for (const line of pagesPart.split('\n')) {
    if (line.startsWith('## ')) { dir = SECTIONS.find(([, h]) => h === line.slice(3))?.[0]; previous = ''; continue }
    const url = entryOf(line)?.[2]
    if (!url) continue
    if (new URL(url).pathname.split('/')[1] !== dir) found.push(`${url} is listed under the wrong section`)
    const id = new URL(url).pathname.slice(1, -1) // ids, as the generator sorts them: '-' sorts before the trailing '/'
    if (id <= previous) found.push(`${url} is not in ascending order after /${previous}/`)
    previous = id
  }

  const docsUrls = [...pageUrls, ...optionalUrls].filter((u) => u.startsWith(`${DOCS_ORIGIN}/`))
  const foreign = docsUrls.filter((u) => OTHER_TREES.has(new URL(u).pathname.split('/')[1]))
  if (foreign.length) found.push(`${foreign.length} locale or archived URL(s) are listed: ${foreign[0]}`)
  const distPath = (url) => {
    const path = new URL(url).pathname.slice(1)
    return path === '' || path.endsWith('/') ? `${path}index.html` : path
  }
  const dead = docsUrls.filter((u) => !hasFile(distPath(u)))
  if (dead.length) found.push(`${dead.length} docs URL(s) are not in dist/: ${dead.slice(0, 3).join(', ')}`)
  for (const url of [SITE_ORIGIN, `${SITE_ORIGIN}/llms.txt`]) {
    if (!optionalUrls.includes(url)) found.push(`llms.txt does not link back to ${url}`)
  }

  // The files carry non-ASCII text: the Worker must serve them as UTF-8 plain text (a path with a
  // rule here gets the charset; robots.txt without one is served without it).
  for (const path of ['/llms.txt', '/llms-full.txt']) {
    if (!new RegExp(`^${path}\\n\\s+Content-Type: text/plain; charset=utf-8$`, 'm').test(headers)) {
      found.push(`dist/_headers has no "Content-Type: text/plain; charset=utf-8" rule for ${path}`)
    }
  }

  const summary = lines.slice(0, lines.findIndex((l) => l.startsWith('## '))).join('\n')
  if ([...SESSION_TOOLS].sort().join() !== [...factsTools].sort().join()) {
    found.push(`SESSION_TOOLS (${SESSION_TOOLS.join(', ')}) differs from the session tools in core/driverfacts/facts.go (${factsTools.join(', ')})`)
  }
  for (const tool of factsTools) if (!summary.includes(tool)) found.push(`the llms.txt summary does not name the shipped tool ${tool}`)
  const unshipped = [...new Set(summary.match(NOT_SHIPPED) ?? [])]
  if (unshipped.length) found.push(`the llms.txt summary names tool(s) that are not shipped sessions: ${unshipped.join(', ')}`)

  if (!full.startsWith(`# ${TITLE} - full text\n`)) found.push(`llms-full.txt does not start with "# ${TITLE} - full text"`)
  // [preface, url, body, url, body, ...]: each section is "## Title", "Source: url", then its body.
  const parts = full.split(/^## .*\n\nSource: (\S+)\n\n/m)
  const sections = []
  for (let i = 1; i < parts.length; i += 2) sections.push([parts[i], parts[i + 1]])
  if (sections.map(([url]) => url).join('\n') !== pageUrls.join('\n')) {
    found.push(`llms-full.txt has ${sections.length} section(s), llms.txt lists ${pageUrls.length}: the pages or their order differ`)
  }
  const empty = sections.filter(([, body]) => body.trim().length < 40)
  if (empty.length) found.push(`llms-full.txt has ${empty.length} page section(s) with (almost) no text: ${empty[0][0]}`)
  const lostCode = sections.filter(([url, body]) => expected.has(url) && fenceLines(body) !== fenceLines(expected.get(url)))
  if (lostCode.length) found.push(`${lostCode.length} page section(s) do not have the code blocks of their source page: ${lostCode[0][0]}`)
  const stated = /\*\*(\d+) core paths\*\*/.exec(full)?.[1]
  if (Number(stated) !== corePaths) found.push(`llms-full.txt says ${stated ?? 'nothing about'} core paths, the OpenAPI contract has ${corePaths}`)
  const lost = sections.flatMap(([url, body]) => {
    if (!expected.has(url)) return []
    const kept = new Set(body.split('\n').map(normalize))
    return plainLines(expected.get(url)).filter((line) => !kept.has(line)).map((line) => `${url}: ${line.slice(0, 80)}`)
  })
  if (lost.length) found.push(`${lost.length} plain source line(s) are missing from their section: ${lost[0]}`)
  const prose = proseLines(full)
  for (const [name, pattern] of LEAKS) {
    const hits = prose.filter((l) => pattern.test(l))
    if (hits.length) found.push(`llms-full.txt keeps ${hits.length} ${name}(s): ${hits[0].trim().slice(0, 100)}`)
  }

  return found
}

const fail = (message) => { console.error(`llms: COULD NOT LOOK — ${message}`); process.exit(2) }
const flag = (name) => {
  const at = process.argv.indexOf(name)
  if (at === -1) return undefined
  return process.argv[at + 1] ?? fail(`${name} needs a value`)
}
const LLMS = flag('--llms') ?? join(DIST, 'llms.txt')
const FULL = flag('--full') ?? join(DIST, 'llms-full.txt')

function inputs() {
  const missing = [LLMS, FULL, join(DIST, '_headers'), FACTS, DOCS].filter((p) => !existsSync(p))
  if (missing.length) {
    console.error(`llms: COULD NOT LOOK — missing: ${missing.join(', ')}`)
    console.error('  Either the build did not run or the endpoint stopped emitting the file — which is the defect, not the excuse.')
    process.exit(2)
  }
  try {
    return {
      llms: readFileSync(LLMS, 'utf8'),
      full: readFileSync(FULL, 'utf8'),
      headers: readFileSync(join(DIST, '_headers'), 'utf8'),
      hasFile: (path) => existsSync(join(DIST, path)),
      expected: sourcePages(),
      factsTools: sessionToolsOf(readFileSync(FACTS, 'utf8')),
      corePaths: countCorePaths(),
    }
  } catch (error) {
    return fail(error.message)
  }
}

if (process.argv.includes('--self-test')) {
  // 1. The converter, on fixtures with exact expected output.
  const fixtures = [
    ['an Aside with a title, a link and a code block', '<Aside type="tip" title="T">\nSee [x](/a/b/).\n\n```sh\nls\n```\n</Aside>\nafter', '> **T**\n>\n> See [x](https://docs.olivares.ai/a/b/).\n>\n> ```sh\n> ls\n> ```\n\nafter'],
    ['an Aside inside a list item', '1. Step\n\n   <Aside type="note">\n   Body.\n   </Aside>', '1. Step\n\n   > **Note**\n   >\n   > Body.'],
    ['a ::: directive with a title', ':::caution[Business]\nText.\n:::\nNext', '> **Business**\n>\n> Text.\n\nNext'],
    ['a ::: directive without a title', ':::note\nText.\n:::', '> **Note**\n>\n> Text.'],
    ['a multi-line LinkCard', '<LinkCard\n  title="Quickstart"\n  href="/start/quickstart/"\n  description="Five minutes."\n/>', '- [Quickstart](https://docs.olivares.ai/start/quickstart/): Five minutes.'],
    ['headings shifted outside fences only', '## A\n~~~sh\n# not a heading\n~~~\n### B', '### A\n~~~sh\n# not a heading\n~~~\n#### B'],
    ['a light and dark screenshot pair', '<img class="light:sl-hidden" src="/console/a-dark.png" alt="Alt text.">\n<img class="dark:sl-hidden" src="/console/a-light.png" alt="Alt text.">', '![Alt text.](https://docs.olivares.ai/console/a-light.png)'],
    ['inline and multi-line comments', 'A <!-- release -->1<!-- /release --> B\n<!-- start\nhidden\n--> tail\nC', 'A 1 B\ntail\nC'],
    ['a generated-block marker that holds backticks', '<!-- BEGIN GENERATED x — regenerate with `bash a.sh --write`; do not edit by hand -->\nKept row\n<!-- END GENERATED x -->', 'Kept row'],
    ['comment markers inside inline code', 'Use `<!-- x -->` and `<!--` here.', 'Use `<!-- x -->` and `<!--` here.'],
    ['prose that starts with the word import', 'import the config first', 'import the config first'],
    ['single-quoted Aside attributes', "<Aside type='tip' title='Mind'>\nText.\n</Aside>", '> **Mind**\n>\n> Text.'],
    ['an import and an MDX comment', "import { Aside } from '@astrojs/starlight/components'\n{/* note */}\nText", 'Text'],
    ['a component name inside a code block', '```mdx\n<Tabs>\n```', '```mdx\n<Tabs>\n```'],
  ]
  for (const [name, input, expected] of fixtures) {
    const got = toMarkdown(input, 'fixture')
    if (got !== expected) { console.error(`✗ self-test: toMarkdown on ${name}\n  expected: ${JSON.stringify(expected)}\n  got:      ${JSON.stringify(got)}`); process.exit(1) }
    console.log(`  ok  toMarkdown: ${name}`)
  }
  const token = toMarkdown('There are {{core-paths}} paths.', 'fixture', 7)
  if (token !== 'There are 7 paths.') { console.error(`✗ self-test: toMarkdown did not replace the core-paths token: ${token}`); process.exit(1) }
  console.log('  ok  toMarkdown: the core-paths token becomes the count it is given')
  const throwing = [
    ['the core-paths token with no count', () => toMarkdown('There are {{core-paths}} paths.', 'fixture')],
    ['a component it does not convert', () => toMarkdown('<Tabs>\nx\n</Tabs>', 'fixture')],
    ['an Aside that is never closed', () => toMarkdown('<Aside type="note">\nx', 'fixture')],
    ['a comment that is never closed', () => toMarkdown('Text <!-- oops\nrest of the page', 'fixture')],
    ['an Aside inside a ::: block', () => toMarkdown(':::note\n<Aside type="tip">\nx\n</Aside>\n:::', 'fixture')],
    ['a ::: with nothing to close', () => toMarkdown('Text\n:::', 'fixture')],
    ['a LinkCard that is never closed', () => toMarkdown('<LinkCard title="a"\n href="/x/"', 'fixture')],
    ['a LinkCard without a title', () => toMarkdown('<LinkCard href="/x/" />', 'fixture')],
    ['a page outside the five sections', () => pagesBySection([{ id: 'blog/post', data: { title: 'x' } }])],
  ]
  for (const [name, run] of throwing) {
    let threw = false
    try { run() } catch { threw = true }
    if (!threw) { console.error(`✗ self-test: toMarkdown/pagesBySection accepted ${name}`); process.exit(1) }
    console.log(`  ok  throws on ${name}`)
  }
  const kept = pagesBySection([
    { id: 'index', data: { title: 'home' } },
    { id: 'es/start/quickstart', data: { title: 'es' } },
    { id: '2026-06/start/quickstart', data: { title: 'archived' } },
    { id: 'start/draft', data: { title: 'draft', draft: true } },
    { id: 'start/kept', data: { title: 'kept' } },
  ]).flatMap((section) => section.pages.map((page) => page.id))
  if (kept.join() !== 'start/kept') { console.error(`✗ self-test: pagesBySection kept [${kept}], expected only start/kept`); process.exit(1) }
  console.log('  ok  pagesBySection drops the home page, locales, snapshots and drafts')

  // 2. The check, on mutants of the real output. A check that always returned [] would pass, and a
  // gate that cannot fail is not a gate.
  const real = inputs()
  const clean = audit(real)
  if (clean.length) { console.error(`✗ self-test: the REAL output has findings and should not:\n  - ${clean.join('\n  - ')}`); process.exit(1) }
  const pageLines = real.llms.split('\n').filter((l) => /^- \[[^\]]+\]\(https:\/\/docs\.olivares\.ai\/start\//.test(l))
  const firstPage = /\((https:\/\/docs\.olivares\.ai\/[^)]+\/)\)/.exec(pageLines[0])[1]
  const afterSource = new RegExp(`(^Source: ${firstPage}\\n\\n)`, 'm')
  const lostLine = plainLines(real.expected.get(firstPage))[3]
  const sectionStart = real.full.indexOf(`Source: ${firstPage}\n\n`)
  const sectionEnd = real.full.indexOf('\n## ', sectionStart)
  const withoutLine = real.full.slice(0, sectionStart) + real.full.slice(sectionStart, sectionEnd).split('\n').filter((l) => normalize(l) !== lostLine).join('\n') + real.full.slice(sectionEnd)
  const inFirstPage = (text) => ({ full: real.full.replace(afterSource, `$1${text}\n\n`) })
  const mutants = [
    ['without the link back to olivares.ai/llms.txt', { llms: real.llms.replace(/^- \[[^\]]+\]\(https:\/\/olivares\.ai\/llms\.txt\).*\n/m, '') }],
    ['with a docs URL that is not built', { llms: real.llms.replace('## Optional\n', `## Optional\n\n- [Gone](${DOCS_ORIGIN}/no/such/page/): a page that was never built.`) }],
    ['with a page missing from the list', { llms: real.llms.replace(pageLines[0] + '\n', '') }],
    ['with a locale page listed', { llms: real.llms.replace('## Optional\n', `## Optional\n\n- [Quickstart](${DOCS_ORIGIN}/es/start/quickstart/): the Spanish copy.`) }],
    ['with an archived page listed', { llms: real.llms.replace('## Optional\n', `## Optional\n\n- [Quickstart](${DOCS_ORIGIN}/2026-06/start/quickstart/): the snapshot.`) }],
    ['with a page on the wrong host', { llms: real.llms.replace(firstPage, firstPage.replace('docs.olivares.ai', 'docs.olivares.com')) }],
    ['with two sections swapped', { llms: real.llms.replace('## Tutorials', '## @@').replace('## How-to guides', '## Tutorials').replace('## @@', '## How-to guides') }],
    ['with two pages out of order', { llms: real.llms.replace(`${pageLines[0]}\n${pageLines[1]}`, `${pageLines[1]}\n${pageLines[0]}`) }],
    ['with an unshipped tool in the summary', { llms: real.llms.replace('Claude Code,', 'Cursor, Claude Code,') }],
    ['with a list line that has no description', { llms: real.llms.replace(pageLines[0], pageLines[0].replace(/\):.*$/, ')')) }],
    ['with an invalid date', { llms: real.llms.replace(/^Last updated: .*$/m, 'Last updated: 18/10/2026') }],
    ['with a tool list that differs from facts.go', { factsTools: real.factsTools.slice(1) }],
    ['without the /llms-full.txt charset rule in _headers', { headers: real.headers.replace('/llms-full.txt\n  Content-Type: text/plain; charset=utf-8\n', '') }],
    ['with a core-paths count that differs from the contract', { full: real.full.replace(/\*\*(\d+) core paths\*\*/, (_, n) => `**${Number(n) + 1} core paths**`) }],
    ['with no English pages in the source walk', { expected: new Map() }],
    ['with a JSX component left in the full text', inFirstPage('<Aside type="note">')],
    ['with a ::: directive left in the full text', inFirstPage(':::note[Business]')],
    ['with a comment left in the full text', inFirstPage('Text <!-- release -->1<!-- /release -->.')],
    ['with the core-paths token left in the full text', inFirstPage('The contract has {{core-paths}} paths.')],
    ['with a raw image left in the full text', inFirstPage('<img class="dark:sl-hidden" src="/console/a.png" alt="x">')],
    ['with a root-relative link left in the full text', inFirstPage('See [the guide](/how-to/self-hosting/).')],
    ['with an MDX import left in the full text', inFirstPage("import { Aside } from '@astrojs/starlight/components'")],
    ['with an empty page section in the full text', { full: real.full.replace(new RegExp(`(^Source: ${firstPage}\\n\\n)[\\s\\S]*?(?=\\n## )`, 'm'), '$1') }],
    ['with one plain line swallowed from a page', { full: withoutLine }],
    ['with a code fence dropped from a page', { full: real.full.replace(/^```.*\n/m, '') }],
    ['with a page section missing from the full text', { full: real.full.replace(afterSource, 'Source: https://example.invalid/\n\n') }],
  ]
  for (const [name, change] of mutants) {
    const mutated = { ...real, ...change }
    if (['llms', 'full', 'headers', 'factsTools', 'expected', 'corePaths'].every((key) => mutated[key] === real[key])) {
      console.error(`✗ self-test: the mutant «${name}» was NOT APPLIED — the control would prove nothing`); process.exit(1)
    }
    if (!audit(mutated).length) { console.error(`✗ self-test: the mutant «${name}» SURVIVES — the gate does not see it`); process.exit(1) }
    console.log(`  ok  mutant «${name}» dies`)
  }

  // 3. The exit codes, measured by RUNNING the program, not by calling audit().
  const { mkdtemp, writeFile, rm } = await import('node:fs/promises')
  const { tmpdir } = await import('node:os')
  const dir = await mkdtemp(join(process.env.TMPDIR || tmpdir(), 'llms-selftest-'))
  const run = (args) => {
    try { execFileSync(process.execPath, [fileURLToPath(import.meta.url), ...args], { stdio: 'pipe' }); return { code: 0, stderr: '' } }
    catch (err) { return { code: err.status, stderr: String(err.stderr) } }
  }
  try {
    const broken = join(dir, 'llms.txt'); await writeFile(broken, real.llms.replace(/^Last updated: .*$/m, 'Last updated: never'))
    const cases = [
      ['0 on the real output', [], 0, ''],
      ['1 on an output with a finding', ['--llms', broken], 1, 'Last updated'],
      ['2 on an output that does not exist', ['--llms', join(dir, 'missing.txt')], 2, 'COULD NOT LOOK'],
      ['2 on a flag without a value', ['--llms'], 2, 'needs a value'],
    ]
    for (const [name, args, expected, text] of cases) {
      const { code, stderr } = run(args)
      if (code !== expected || !stderr.includes(text)) { console.error(`✗ self-test: «${name}» exited ${code} with «${stderr.trim().slice(0, 120)}», expected ${expected} with «${text}»`); process.exit(1) }
      console.log(`  ok  exit code ${expected}: ${name}`)
    }
  } finally {
    await rm(dir, { recursive: true, force: true })
  }
  console.log(`✓ llms self-test: ${fixtures.length + throwing.length + 2} converter cases pass, the ${mutants.length} mutants die and the exit codes are measured by RUNNING the program`)
  process.exit(0)
}

const data = inputs()
const findings = audit(data)
if (findings.length) {
  console.error('✗ llms: the built files do not say what the project decided.\n')
  for (const f of findings) console.error(`  - ${f}`)
  console.error('\n  They are rendered by docs-site/src/llms.mjs from the docs collection (src/pages/llms*.txt.ts).')
  process.exit(1)
}
console.log(`✓ llms: OK — ${data.expected.size} pages listed and built, ${Buffer.byteLength(data.full)} bytes of full text, tools = core/driverfacts/facts.go (${data.factsTools.join(', ')}).`)
process.exit(0)
