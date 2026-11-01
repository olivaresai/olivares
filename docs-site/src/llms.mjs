// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// llms — renders /llms.txt (llmstxt.org format) and /llms-full.txt from the docs collection.
//
// Both files are generated from the same pages the site builds, so there is no second hand-kept
// copy: the pages are the English, current ones (no locale, no archived snapshot, no draft).
// src/pages/llms.txt.ts and src/pages/llms-full.txt.ts only feed the collection in.
// scripts/check-llms.mjs checks the built files.
//
// Why not the starlight-llms-txt plugin: version 0.12.0 fits this Astro and Starlight, but it
// declares 12 direct dependencies (npm view, 2026-10-18) for a docs build, and this module is the
// whole job. Revisit if the page set needs per-page opt-out or custom sets.
//
// Plain ESM whose only import is site-locales, so the check can import it without Astro. It reads no
// file: Vite bundles the endpoints, which breaks paths built from import.meta.url, so the OpenAPI
// path count arrives as an argument.

import { ARCHIVED_SLUGS, PUBLISHED_LOCALES } from './site-locales.mjs'

export const DOCS_ORIGIN = 'https://docs.olivares.ai'
export const SITE_ORIGIN = 'https://olivares.ai'
export const TITLE = 'Olivares AI documentation'

/**
 * The tools Olivares AI starts as governed sessions today: the rows of core/driverfacts/facts.go with
 * `Session: true`. scripts/check-llms.mjs compares this list with that file. A tool is added here
 * when the table gains its row, never before: the other tools the docs name are observed through
 * connectors, which is a different capability.
 */
export const SESSION_TOOLS = ['Claude Code', 'Codex', 'Grok Build', 'OpenCode', 'Gemini CLI', 'Cursor CLI']

/** Top-level docs folders, in reading order, with the heading each gets in llms.txt. */
const SECTIONS = [
  ['start', 'Start here'],
  ['tutorials', 'Tutorials'],
  ['how-to', 'How-to guides'],
  ['explanation', 'Explanation'],
  ['reference', 'Reference'],
]

// Build date in UTC; SOURCE_DATE_EPOCH (integer seconds) makes a reproducible build print the same day.
const epoch = process.env.SOURCE_DATE_EPOCH || undefined
if (epoch !== undefined && !/^\d+$/.test(epoch)) {
  throw new TypeError(`SOURCE_DATE_EPOCH must be integer seconds, got ${JSON.stringify(epoch)}`)
}
export const UPDATED = new Date(epoch === undefined ? Date.now() : Number(epoch) * 1000).toISOString().slice(0, 10)

const OTHER_TREES = new Set([...PUBLISHED_LOCALES, ...ARCHIVED_SLUGS])
const oneLine = (text) => text.replace(/\s+/g, ' ').trim()
const urlOf = (id) => `${DOCS_ORIGIN}/${id}/`

/**
 * The pages llms.txt lists, grouped by section in reading order, each section sorted by id.
 * A page outside the known sections throws: a new top-level folder gets a heading on purpose.
 * @param {{id: string, body?: string, data: {title: string, description?: string, draft?: boolean}}[]} entries
 */
export function pagesBySection(entries) {
  const pages = entries.filter((e) => !e.data.draft && e.id !== 'index' && !OTHER_TREES.has(e.id.split('/')[0]))
  const known = new Set(SECTIONS.map(([dir]) => dir))
  const stray = pages.filter((e) => !known.has(e.id.split('/')[0]))
  if (stray.length) throw new Error(`llms: docs pages outside the llms sections: ${stray.map((e) => e.id).join(', ')}`)
  return SECTIONS.map(([dir, heading]) => ({
    heading,
    pages: pages.filter((e) => e.id.split('/')[0] === dir).sort((a, b) => (a.id < b.id ? -1 : 1)),
  }))
}

const OPTIONAL = [
  [SITE_ORIGIN, 'Olivares AI', 'The product site: what Olivares AI is, editions, pricing and downloads.'],
  [`${SITE_ORIGIN}/llms.txt`, 'Olivares AI for assistants (llms.txt)', "The product site's summary for AI assistants."],
  [`${DOCS_ORIGIN}/llms-full.txt`, 'These docs as one file', 'The full text of every page listed above, as Markdown.'],
  [`${DOCS_ORIGIN}/reference/api/`, 'REST API reference', 'Every route of the control plane API, generated from its OpenAPI document.'],
  [`${DOCS_ORIGIN}/reference/api-beta/`, 'Module routes (beta)', 'The beta module routes, generated from their OpenAPI document.'],
  [`${DOCS_ORIGIN}/asyncapi/asyncapi.yaml`, 'Event bus (AsyncAPI)', 'The AsyncAPI document of the event bus.'],
  [`${DOCS_ORIGIN}/sitemap-index.xml`, 'Sitemap', 'The current pages of this site, in every language. Archived snapshots are not in it.'],
  ['https://github.com/olivaresai/olivares', 'Source code (GitHub)', 'The AGPL-3.0 source and the signed releases.'],
]

const toolList = `${SESSION_TOOLS.slice(0, -1).join(', ')} and ${SESSION_TOOLS.at(-1)}`

const intro = [
  `> Documentation for Olivares AI, a self-hosted control plane that runs and governs the AI tools a team already uses, on the team's own infrastructure. Tutorials, how-to guides, explanation and reference, organized by Diataxis. English is authoritative; the other languages are machine-translated and are not listed here.`,
  `Olivares AI starts these tools as governed sessions today: ${toolList}. Other tools that the docs name are observed through connectors, which is a different capability. The Go binary includes the web console. Community is the complete product for one person or one team: the engine, modules and web console are AGPL-3.0-only, and the SDK and connectors are Apache-2.0. Business and Enterprise add what companies need, under a commercial license. Some capabilities are partial or planned; the page "Honesty & limits" says which.`,
  `The product site is ${SITE_ORIGIN}, with its own summary for assistants at ${SITE_ORIGIN}/llms.txt. The full text of these docs is ${DOCS_ORIGIN}/llms-full.txt.`,
  `Last updated: ${UPDATED}`,
]

export function renderLlmsTxt(entries) {
  const lines = [`# ${TITLE}`, '']
  for (const paragraph of intro) lines.push(paragraph, '')
  for (const { heading, pages } of pagesBySection(entries)) {
    lines.push(`## ${heading}`, '')
    for (const { id, data } of pages) {
      lines.push(`- [${oneLine(data.title)}](${urlOf(id)})${data.description ? `: ${oneLine(data.description)}` : ''}`)
    }
    lines.push('')
  }
  lines.push('## Optional', '')
  for (const [url, title, description] of OPTIONAL) lines.push(`- [${title}](${url}): ${description}`)
  return `${lines.join('\n')}\n`
}

export function renderLlmsFullTxt(entries, corePaths) {
  const parts = [`# ${TITLE} - full text`, ...intro]
  for (const { pages } of pagesBySection(entries)) {
    for (const { id, body, data } of pages) {
      if (typeof body !== 'string') throw new Error(`llms: ${id} has no body in the docs collection, so its section would be empty`)
      parts.push(`## ${oneLine(data.title)}\n\nSource: ${urlOf(id)}\n\n${toMarkdown(body, id, corePaths)}`)
    }
  }
  return `${parts.join('\n\n')}\n`
}

// The English pages are Markdown with a few MDX components and Starlight directives. MDX would
// render them; here each becomes plain Markdown. A component line that is not handled below throws,
// so a new component is a build failure and never JSX leaking into what assistants read.
const STRAY_COMPONENT = /^\s*<\/?[A-Z][A-Za-z0-9]*(\s|\/|>|$)/
const IMPORT = /^import\s+(?:[\w*{},\s]+\s+from\s+)?['"]/ // a module import, not prose that starts with the word
const DIRECTIVE_OPEN = /^(\s*):::(note|tip|caution|danger)(?:\[(.*)\])?\s*$/

const attrsOf = (text) => Object.fromEntries([...text.matchAll(/(\w+)=(?:"([^"]*)"|'([^']*)')/g)].map(([, name, double, single]) => [name, double ?? single]))
const absolute = (href) => (href.startsWith('/') ? `${DOCS_ORIGIN}${href}` : href)
const capitalize = (word) => word[0].toUpperCase() + word.slice(1)

/**
 * Markdown of a page body: no imports, comments, components or directives; root-relative links and
 * images made absolute; headings one level down. Read line by line because an Aside can hold a code
 * fence.
 */
export function toMarkdown(body, id, corePaths) {
  const out = []
  let fence = null // marker of the open code fence
  let aside = null // indent of the open Aside or directive, whose lines become a blockquote
  let linkCardText = null // a LinkCard whose attributes span lines
  let inComment = false // inside a multi-line HTML comment
  const fail = (what) => { throw new Error(`llms: ${id} ${what}`) }
  const push = (line) => {
    if (aside === null) return out.push(line)
    out.push(`${aside}> ${line.startsWith(aside) ? line.slice(aside.length) : line.trimStart()}`.trimEnd())
  }
  const quote = (indent, label) => {
    if (aside !== null) fail('has an Aside or ::: block inside another one')
    aside = indent
    out.push(`${indent}> **${label}**`, `${indent}>`)
  }
  for (const raw of body.split('\n')) {
    if (fence) {
      push(raw)
      const marker = /^\s*(`{3,}|~{3,})/.exec(raw)?.[1]
      if (marker && marker[0] === fence[0] && marker.length >= fence.length) fence = null
      continue
    }
    let line = raw
    if (inComment) {
      const end = line.indexOf('-->')
      if (end === -1) continue
      inComment = false
      line = line.slice(end + 3).trimStart()
    }
    ;({ text: line, open: inComment } = withoutComments(line))
    if (line !== raw && !line.trim()) continue // a line that was only a comment
    const marker = /^\s*(`{3,}|~{3,})/.exec(line)?.[1]
    if (marker) {
      fence = marker
      push(line)
      continue
    }
    if (linkCardText !== null) {
      linkCardText += ` ${line.trim()}`
      if (line.includes('/>')) out.push(linkCard(linkCardText, fail)), (linkCardText = null)
      continue
    }
    const asideOpen = /^(\s*)<Aside\b([^>]*)>\s*$/.exec(line)
    const directive = DIRECTIVE_OPEN.exec(line)
    if (asideOpen) {
      const { title, type = 'note' } = attrsOf(asideOpen[2])
      quote(asideOpen[1], title ?? capitalize(type))
    } else if (directive) {
      quote(directive[1], directive[3] ?? capitalize(directive[2]))
    } else if (aside !== null && (/^\s*<\/Aside>\s*$/.test(line) || /^\s*:::\s*$/.test(line))) {
      aside = null
      out.push('')
    } else if (/^\s*:::\s*$/.test(line)) {
      fail('has a ::: with no block to close')
    } else if (/^\s*<\/?(CardGrid|Steps)>\s*$/.test(line)) {
      out.push('')
    } else if (/^\s*<LinkCard\b/.test(line)) {
      if (line.includes('/>')) out.push(linkCard(line, fail))
      else linkCardText = line.trim()
    } else if (/^\s*<img\b[^>]*>\s*$/.test(line)) {
      // The pages carry a light and a dark copy of each screenshot: keep one, with its alt text.
      const { class: cls = '', src, alt = '' } = attrsOf(line)
      if (!src) fail(`has an <img> without src: ${line.trim()}`)
      if (!cls.includes('light:sl-hidden')) push(`![${alt}](${absolute(src)})`)
    } else if (IMPORT.test(line) || /^\s*\{\/\*.*\*\/\}\s*$/.test(line)) {
      // module imports and MDX comments are not content
    } else {
      const text = line
        .replace(/\{\/\*.*?\*\/\}/g, '')
        .replaceAll(CORE_PATHS_TOKEN, () => (corePaths === undefined ? fail('has {{core-paths}} and was given no path count') : String(corePaths)))
        .replace(/\]\((\/[^)\s]*)\)/g, (_, path) => `](${DOCS_ORIGIN}${path})`)
        .replace(/^(#{1,5}) /, '#$1 ')
      if (STRAY_COMPONENT.test(text)) fail(`has a component line that toMarkdown does not convert: ${text.trim()}`)
      push(text)
    }
  }
  if (inComment) fail('has an HTML comment that is never closed')
  if (linkCardText !== null) fail('has a <LinkCard> that is never closed with />')
  if (aside !== null) fail('has an <Aside> or ::: block that is never closed')
  return out.join('\n').replace(/\n{3,}/g, '\n\n').trim()
}

/**
 * `line` without HTML comments; `open` when a comment starts and does not end on this line. A comment
 * is found before any code span that follows it, because generated-block markers hold backticks;
 * a `<!--` inside a code span is text.
 */
function withoutComments(line) {
  let text = ''
  let at = 0
  while (at < line.length) {
    const tick = line.indexOf('`', at)
    const start = line.indexOf('<!--', at)
    if (start === -1 && tick === -1) return { text: text + line.slice(at), open: false }
    if (tick !== -1 && (start === -1 || tick < start)) {
      const end = line.indexOf('`', tick + 1)
      const stop = end === -1 ? line.length : end + 1
      text += line.slice(at, stop)
      at = stop
      continue
    }
    text += line.slice(at, start)
    const end = line.indexOf('-->', start + 4)
    if (end === -1) return { text, open: true }
    at = end + 3
  }
  return { text, open: false }
}

function linkCard(text, fail) {
  const { title, href, description } = attrsOf(text)
  if (!title || !href) fail(`has a <LinkCard> without a title or href: ${text}`)
  return `- [${title}](${absolute(href)})${description ? `: ${description}` : ''}`
}

// The site fills this token from the OpenAPI contract when it renders a page (src/core-paths-plugin.mjs);
// the endpoint passes the same count, from the same document.
const CORE_PATHS_TOKEN = '{{core-paths}}'

/** The number of paths in the OpenAPI document; an empty or missing `paths` is a lost measurement, not zero. */
export function corePathCount(document) {
  const paths = document?.paths
  if (paths === null || typeof paths !== 'object' || Array.isArray(paths) || Object.keys(paths).length === 0) {
    throw new Error('llms: the OpenAPI document declares no paths; refusing to publish a count of zero')
  }
  return Object.keys(paths).length
}
