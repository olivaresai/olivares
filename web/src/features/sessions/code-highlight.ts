// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// SYNTAX COLOURS FOR A REPLY'S CODE BLOCK, from CodeMirror's own grammars (the editor already
// ships its engine; `@codemirror/legacy-modes` are its stream grammars). Nothing of CodeMirror
// loads with the conversation: its parser, a language's grammar and the editors' ink load
// with the first block of that language that comes near the screen (`code-grammar.ts`). The
// result is plain pieces of text with a class and an ink each: the caller paints them as
// elements, so what the model wrote can never become markup.
import type { CSSProperties } from 'react'
import type { GrammarName } from './code-grammar'

export interface CodePiece {
  text: string
  /** A `tok-*` class from `classHighlighter`, or null for text that stays plain. */
  cls: string | null
  /** The editors' ink for the piece's token kind, where their map has one. */
  ink?: CSSProperties
}

/** The names a fence uses, onto the grammar that reads them (`code-grammar.ts`). */
const ALIASES: Record<string, GrammarName> = {
  sh: 'shell',
  bash: 'shell',
  zsh: 'shell',
  shell: 'shell',
  console: 'shell',
  shellscript: 'shell',
  js: 'javascript',
  jsx: 'javascript',
  mjs: 'javascript',
  javascript: 'javascript',
  ts: 'typescript',
  tsx: 'typescript',
  typescript: 'typescript',
  json: 'json',
  jsonc: 'json',
  py: 'python',
  python: 'python',
  go: 'go',
  golang: 'go',
  rs: 'rust',
  rust: 'rust',
  yaml: 'yaml',
  yml: 'yaml',
  toml: 'toml',
  diff: 'diff',
  patch: 'diff',
  css: 'css',
  sql: 'sql',
  c: 'c',
  h: 'c',
  cpp: 'cpp',
  'c++': 'cpp',
  java: 'java',
  dockerfile: 'dockerfile',
  docker: 'dockerfile',
  html: 'xml',
  xml: 'xml',
  svg: 'xml',
}

/** A reply is untrusted and may stream: a block past this size stays plain, never a frozen tab. */
const MAX_HIGHLIGHT_CHARS = 20_000

const SHELLS = new Set(['sh', 'bash', 'zsh', 'shell', 'shellscript'])

/** Whether a fence names a shell, the only language "Run in terminal" offers. */
export function isShellFence(lang: string | undefined): boolean {
  return !!lang && SHELLS.has(lang.toLowerCase())
}

/** Each language once loaded, as what paints its blocks; null for one that could not load
 * (its blocks stay plain). */
const grammars = new Map<GrammarName, ((text: string) => CodePiece[]) | null>()
const loads = new Map<GrammarName, Promise<void>>()

/** The language a fence names, among the fence names only: `constructor` or `__proto__`
 * is no language. */
function grammarName(lang: string | undefined): GrammarName | undefined {
  const key = lang?.toLowerCase()
  return key && Object.hasOwn(ALIASES, key) ? ALIASES[key] : undefined
}

/** Loads the grammar a fence names, once; resolves when `highlightPieces` can answer. */
export function loadGrammar(lang: string | undefined): Promise<void> {
  const name = grammarName(lang)
  if (!name) return Promise.resolve()
  let load = loads.get(name)
  if (!load) {
    load = import('./code-grammar')
      .then(async ({ loadLanguage, highlight }) => {
        const language = await loadLanguage(name)
        grammars.set(name, (text) => highlight(text, language))
      })
      // A grammar that cannot load leaves its blocks plain: the words are all there.
      .catch(() => void grammars.set(name, null))
    loads.set(name, load)
  }
  return load
}

/** The block as coloured pieces, at once: null when it stays plain (a language it does
 * not know, a block too big, a grammar that could not load), undefined while its grammar
 * loads (`loadGrammar`). */
export function highlightPieces(
  text: string,
  lang: string | undefined,
): CodePiece[] | null | undefined {
  const name = grammarName(lang)
  if (!name || text.length > MAX_HIGHLIGHT_CHARS) return null
  const paint = grammars.get(name)
  return paint ? paint(text) : paint
}
