// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE ASSISTANT'S MARKDOWN, READ INTO DATA AND NEVER INTO HTML (#1784). What the model says
// can be shaped by what it read, so the reply is parsed into a small tree that the console
// paints as elements: there is no HTML string, no pass-through of tags and no sanitizer to
// get wrong. A link survives only with a web or mail address; any other destination stays in
// the text as it was written, so nothing the model said is dropped.
//
// The subset is the one replies are written in: paragraphs, headings, lists (nested), fenced
// code, quotes, tables, rules, and inline code, bold, italic and links. Setext headings,
// indented code, footnotes and raw HTML are not read; they stay as the characters they were
// sent in. ponytail: a subset parser, not CommonMark; adopt a parser through the
// `olivares-html` Trusted Types policy only when a reply this one cannot read matters.

export type Inline =
  | string
  | { kind: 'code'; text: string }
  | { kind: 'strong' | 'em'; children: Inline[] }
  | { kind: 'link'; href: string; children: Inline[] }

export type Block =
  | { kind: 'paragraph'; inline: Inline[] }
  | { kind: 'heading'; level: number; inline: Inline[] }
  | { kind: 'code'; text: string }
  | { kind: 'quote'; blocks: Block[] }
  | { kind: 'list'; ordered: boolean; start: number; items: Block[][] }
  | { kind: 'table'; head: Inline[][]; rows: Inline[][][] }
  | { kind: 'rule' }

// A reply is untrusted input: nesting and size are bounded so a hostile or runaway one costs
// a plain paragraph, never a frozen tab or a blown stack.
const MAX_DEPTH = 8
const MAX_INLINE_CHARS = 20_000
const MAX_LABEL_CHARS = 1_000
const MAX_URL_CHARS = 2_000

// Only what would otherwise be read as markup inside a line: `\.` and `\h` in a path stay.
const ESCAPABLE = '\\`*_[]()<>|~'
const WEB_OR_MAIL = /^(?:https?:\/\/|mailto:)[^\s<>]+$/i
const AUTOLINK = /<((?:https?:\/\/|mailto:)[^\s<>]+)>/y
const WORD = /\w/
const PUNCT = /[!-/:-@[-`{-~]/

// A fence names at most a language: a line like ```ls -la``` or ~~~note~~~ is prose, and
// taking it for a fence would hide the words after it inside a code box. Trailing blanks
// are cut by `fenceOf`, not matched here: a `[ \t]*` before `$` gives back one blank at a
// time to a language run that refuses them, which is quadratic on a long line of blanks.
const FENCE = /^ {0,3}(`{3,}|~{3,})[ \t]*([\w+#.-]*)$/
const HEADING = /^ {0,3}(#{1,6})[ \t]+([\s\S]*)$/
const RULE = /^ {0,3}([-*_])(?:\s*\1){2,}\s*$/
const QUOTE = /^ {0,3}>\s?/
const ITEM = /^( *)([-*+]|\d{1,9}[.)])\s+([\s\S]*)$/

const isSpace = (ch: string | undefined) => ch === undefined || /\s/.test(ch)
const isPunct = (ch: string | undefined) => ch !== undefined && PUNCT.test(ch)
const indentOf = (line: string) => /^ */.exec(line)![0].length

/** The address a link may point at, or null: only web and mail addresses are links. */
export function safeHref(url: string): string | null {
  return WEB_OR_MAIL.test(url) ? url : null
}

interface Hit {
  nodes: Inline[]
  end: number
}

function codeSpan(text: string, at: number): Hit | null {
  let run = 1
  while (text[at + run] === '`') run++
  const mark = '`'.repeat(run)
  let from = at + run
  for (;;) {
    const close = text.indexOf(mark, from)
    if (close < 0) return null
    if (text[close + run] === '`') {
      // a longer run of backticks is not this span's closer
      from = close + run
      while (text[from] === '`') from++
      continue
    }
    let body = text.slice(at + run, close).replace(/\n/g, ' ')
    if (
      body.length > 2 &&
      body.trim() &&
      body.startsWith(' ') &&
      body.endsWith(' ')
    )
      body = body.slice(1, -1)
    return { nodes: [{ kind: 'code', text: body }], end: close + run }
  }
}

function emphasis(
  text: string,
  at: number,
  depth: number,
  inLink: boolean,
  dead: Set<string>,
): Hit | null {
  const ch = text[at]!
  const double = text[at + 1] === ch
  const mark = double ? ch + ch : ch
  const open = at + mark.length
  // a mark that found no closer once finds none after it: no rescan of the rest per opener
  if (dead.has(mark)) return null
  const next = text[open]
  const prev = text[at - 1]
  // an opener hugs the word that follows it; "2 * 3" and "*.go *.ts" are not emphasis
  if (isSpace(next) || next === ch) return null
  if (isPunct(next) && !isSpace(prev) && !isPunct(prev)) return null
  // snake_case is a name, not emphasis
  if (ch === '_' && at > 0 && WORD.test(prev!)) return null
  for (let from = open; ;) {
    const close = text.indexOf(mark, from)
    if (close < 0) {
      dead.add(mark)
      return null
    }
    from = close + 1
    if (!double) {
      // a single mark never closes on half of a double one: skip the whole run
      let n = 1
      while (text[close + n] === ch) n++
      if (n > 1) {
        from = close + n
        continue
      }
    }
    if (close === open) continue
    // a closer hugs the word before it
    const before = text[close - 1]
    const after = text[close + mark.length]
    if (isSpace(before)) continue
    if (isPunct(before) && !isSpace(after) && !isPunct(after)) continue
    if (ch === '_' && after !== undefined && WORD.test(after)) continue
    return {
      nodes: [
        {
          kind: double ? 'strong' : 'em',
          children: parseInline(text.slice(open, close), depth + 1, inLink),
        },
      ],
      end: close + mark.length,
    }
  }
}

/** The index of the `]` that closes the `[` at `open`, or -1. */
function closingBracket(text: string, open: number): number {
  let depth = 0
  const end = Math.min(text.length, open + MAX_LABEL_CHARS)
  for (let i = open; i < end; i++) {
    const ch = text[i]
    if (ch === '\\') i++
    else if (ch === '[') depth++
    else if (ch === ']' && --depth === 0) return i
  }
  return -1
}

/** `(url "title")` after a link label: the url and where the link ends, or null. */
function destination(
  text: string,
  from: number,
): { url: string; end: number } | null {
  let k = from
  while (text[k] === ' ') k++
  let url: string
  if (text[k] === '<') {
    const gt = text.indexOf('>', k)
    if (gt < 0) return null
    url = text.slice(k + 1, gt)
    k = gt + 1
  } else {
    const start = k
    let depth = 0
    while (k < text.length && k - start < MAX_URL_CHARS && !isSpace(text[k])) {
      if (text[k] === '(') depth++
      else if (text[k] === ')' && depth-- === 0) break
      k++
    }
    url = text.slice(start, k)
  }
  while (k < text.length && isSpace(text[k])) k++
  const quote = text[k]
  if (quote === '"' || quote === "'") {
    const q = text.indexOf(quote, k + 1)
    if (q < 0) return null
    k = q + 1
    while (k < text.length && isSpace(text[k])) k++
  }
  return text[k] === ')' ? { url, end: k + 1 } : null
}

/** The words of inline nodes, without their styling. */
function wordsOf(nodes: Inline[]): string {
  return nodes
    .map((n) =>
      typeof n === 'string'
        ? n
        : n.kind === 'code'
          ? n.text
          : wordsOf(n.children),
    )
    .join('')
}

/**
 * A label that reads as an address must not hide where the link goes: when it names a
 * different host than the link does, the real host is added in the words. The destination is
 * also the link's title, but a keyboard or a finger never sees a title.
 */
function showRealHost(children: Inline[], href: string): Inline[] {
  const label = wordsOf(children).trim()
  const named = /^(?:https?:\/\/|www\.)([^\s/?#:]+)/i.exec(label)?.[1]
  if (!named) return children
  let host: string
  try {
    host = new URL(href).hostname
  } catch {
    return children
  }
  const bare = (h: string) => h.toLowerCase().replace(/^www\./, '')
  return host && bare(host) !== bare(named)
    ? [...children, ` (${host})`]
    : children
}

function link(
  text: string,
  at: number,
  depth: number,
  inLink: boolean,
): Hit | null {
  const open = text[at] === '!' ? at + 1 : at
  const labelEnd = closingBracket(text, open)
  if (labelEnd < 0 || text[labelEnd + 1] !== '(') return null
  const dest = destination(text, labelEnd + 2)
  if (!dest) return null
  const children = parseInline(text.slice(open + 1, labelEnd), depth + 1, true)
  const href = safeHref(dest.url)
  // A link inside a link label is one anchor, with its address kept in the words; an
  // address that is not a web or mail one stays in the text as written, never dropped.
  if (inLink) return { nodes: [...children, ` (${dest.url})`], end: dest.end }
  if (!href) return { nodes: [text.slice(at, dest.end)], end: dest.end }
  return {
    nodes: [
      {
        kind: 'link',
        href,
        children: children.length ? showRealHost(children, href) : [href],
      },
    ],
    end: dest.end,
  }
}

export function parseInline(text: string, depth = 0, inLink = false): Inline[] {
  if (depth > MAX_DEPTH || text.length > MAX_INLINE_CHARS) return [text]
  const out: Inline[] = []
  const dead = new Set<string>()
  let plain = ''
  const flush = () => {
    if (plain) out.push(plain)
    plain = ''
  }
  for (let at = 0; at < text.length;) {
    const ch = text[at]!
    const next = text[at + 1]
    if (ch === '\\' && next !== undefined && ESCAPABLE.includes(next)) {
      plain += next
      at += 2
      continue
    }
    let hit: Hit | null = null
    if (ch === '`') hit = codeSpan(text, at)
    else if (ch === '*' || ch === '_')
      hit = emphasis(text, at, depth, inLink, dead)
    else if (ch === '[' || (ch === '!' && next === '['))
      hit = link(text, at, depth, inLink)
    else if (ch === '<' && !inLink) {
      AUTOLINK.lastIndex = at
      const m = AUTOLINK.exec(text)
      const href = m ? safeHref(m[1]!) : null
      if (m && href)
        hit = {
          nodes: [{ kind: 'link', href, children: [href] }],
          end: at + m[0].length,
        }
    }
    if (hit) {
      flush()
      out.push(...hit.nodes)
      at = hit.end
      continue
    }
    if (ch === '`') {
      // an unclosed run is the characters themselves, all of them
      let n = 1
      while (text[at + n] === '`') n++
      plain += text.slice(at, at + n)
      at += n
      continue
    }
    plain += ch
    at++
  }
  flush()
  return out
}

function tableCells(line: string): string[] {
  const body = line.trim().replace(/^\|/, '')
  const cells: string[] = []
  let cell = ''
  for (let i = 0; i < body.length; i++) {
    if (body[i] === '\\' && body[i + 1] === '|') {
      cell += '|'
      i++
    } else if (body[i] === '|') {
      cells.push(cell.trim())
      cell = ''
    } else cell += body[i]
  }
  // text after the last pipe is a cell; the empty tail of a closing pipe is not
  if (cell.trim()) cells.push(cell.trim())
  return cells
}

const isTableRule = (line: string | undefined, width: number) => {
  if (line === undefined || !line.includes('-')) return false
  const cells = tableCells(line)
  return cells.length === width && cells.every((c) => /^:?-+:?$/.test(c))
}

/** The marker and language of a line that opens a fence, else null. */
function fenceOf(line: string): RegExpExecArray | null {
  let end = line.length
  while (end > 0 && (line[end - 1] === ' ' || line[end - 1] === '\t')) end--
  return FENCE.exec(end === line.length ? line : line.slice(0, end))
}

function startsBlock(line: string): boolean {
  if (fenceOf(line) || HEADING.test(line) || RULE.test(line)) return true
  if (QUOTE.test(line)) return true
  const item = ITEM.exec(line)
  // only a list that starts at 1 may interrupt a paragraph: "in\n2019. it" is prose
  return item !== null && (!/\d/.test(item[2]!) || parseInt(item[2]!, 10) === 1)
}

function closesFence(line: string, mark: string): boolean {
  const t = line.trim()
  return t.length >= mark.length && t === mark[0]!.repeat(t.length)
}

/** The heading's words, without the optional closing `###` of "## Title ##". */
function headingText(raw: string): string {
  const text = raw.trimEnd()
  // walk back over the closing run: a `/#+$/` search restarts at every `#` of a long run
  let end = text.length
  while (end > 0 && text[end - 1] === '#') end--
  if (end < text.length && /\s/.test(text[end - 1] ?? '')) {
    return text.slice(0, end).trimEnd()
  }
  return text
}

function parseList(
  lines: string[],
  start: number,
  depth: number,
): { block: Block; end: number } {
  const first = ITEM.exec(lines[start]!)!
  const base = first[1]!.length
  const ordered = /\d/.test(first[2]!)
  const items: Block[][] = []
  let i = start
  for (;;) {
    // a blank line between two items of one list does not end it
    let j = i
    while (j < lines.length && !lines[j]!.trim()) j++
    const m = j < lines.length ? ITEM.exec(lines[j]!) : null
    if (!m || m[1]!.length !== base || /\d/.test(m[2]!) !== ordered) break
    i = j + 1
    const content = [m[3]!]
    const contentIndent = base + m[2]!.length + 1
    while (i < lines.length) {
      const line = lines[i]!
      if (!line.trim()) {
        // the whole run of blank lines at once: one scan, however long the run
        let k = i + 1
        while (k < lines.length && !lines[k]!.trim()) k++
        if (k >= lines.length || indentOf(lines[k]!) <= base) break
        for (; i < k; i++) content.push('')
        continue
      }
      const indent = indentOf(line)
      if (indent <= base) break
      content.push(line.slice(Math.min(contentIndent, indent)))
      i++
    }
    items.push(parseBlocks(content, depth + 1))
  }
  return {
    block: {
      kind: 'list',
      ordered,
      start: ordered ? parseInt(first[2]!, 10) : 1,
      items,
    },
    end: i,
  }
}

function parseBlocks(lines: string[], depth: number): Block[] {
  if (depth > MAX_DEPTH)
    return [{ kind: 'paragraph', inline: [lines.join('\n')] }]
  const blocks: Block[] = []
  let i = 0
  while (i < lines.length) {
    const line = lines[i]!
    if (!line.trim()) {
      i++
      continue
    }
    const fence = fenceOf(line)
    if (fence) {
      const body: string[] = []
      i++
      while (i < lines.length && !closesFence(lines[i]!, fence[1]!))
        body.push(lines[i++]!)
      i++ // the closing fence; a block still streaming runs to the end
      blocks.push({ kind: 'code', text: body.join('\n') })
      continue
    }
    const heading = HEADING.exec(line)
    if (heading) {
      blocks.push({
        kind: 'heading',
        level: heading[1]!.length,
        inline: parseInline(headingText(heading[2]!)),
      })
      i++
      continue
    }
    if (RULE.test(line)) {
      blocks.push({ kind: 'rule' })
      i++
      continue
    }
    if (QUOTE.test(line)) {
      const inner: string[] = []
      while (i < lines.length && QUOTE.test(lines[i]!))
        inner.push(lines[i++]!.replace(QUOTE, ''))
      blocks.push({ kind: 'quote', blocks: parseBlocks(inner, depth + 1) })
      continue
    }
    if (ITEM.test(line)) {
      const list = parseList(lines, i, depth)
      blocks.push(list.block)
      i = list.end
      continue
    }
    const head = line.includes('|') ? tableCells(line) : []
    if (head.length > 0 && isTableRule(lines[i + 1], head.length)) {
      // Rows keep the cells they have: a short row stays short and a wide one is not cut,
      // so a row costs what its line costs and nothing it said is dropped.
      const rows: Inline[][][] = []
      i += 2
      while (i < lines.length && lines[i]!.trim() && lines[i]!.includes('|'))
        rows.push(tableCells(lines[i++]!).map((c) => parseInline(c)))
      blocks.push({
        kind: 'table',
        head: head.map((c) => parseInline(c)),
        rows,
      })
      continue
    }
    // The lines stay as they were written (the painter keeps line breaks and indentation),
    // so a pasted log or an aligned block reads as it was sent.
    const text: string[] = [line]
    i++
    while (
      i < lines.length &&
      lines[i]!.trim() &&
      !startsBlock(lines[i]!) &&
      !(
        lines[i]!.includes('|') &&
        isTableRule(lines[i + 1], tableCells(lines[i]!).length)
      )
    )
      text.push(lines[i++]!)
    blocks.push({ kind: 'paragraph', inline: parseInline(text.join('\n')) })
  }
  return blocks
}

/** The reply as blocks, ready to paint. Never throws; never produces markup. */
export function parseMarkdown(text: string): Block[] {
  const lines = text
    .replace(/\r\n?/g, '\n')
    .split('\n')
    .map((line) => line.replace(/^\t+/, (tabs) => '    '.repeat(tabs.length)))
  return parseBlocks(lines, 0)
}
