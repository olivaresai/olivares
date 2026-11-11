// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE CODEMIRROR HALF OF A REPLY'S SYNTAX COLOURS: the stream parser, each language's grammar
// (a file of `@codemirror/legacy-modes`, loaded on first use) and the editors' ink. Only
// `code-highlight.ts` loads it, when a code block comes near the screen, so the
// conversation's first screen carries none of it.
import { StreamLanguage, type StreamParser } from '@codemirror/language'
import {
  classHighlighter,
  highlightCode,
  tagHighlighter,
} from '@lezer/highlight'
import type { CSSProperties } from 'react'
import { tokenHighlightStyle } from '@/components/ui/code-languages'
import type { CodePiece } from './code-highlight'

// THE INK OF A TOKEN KIND IS THE EDITORS' (`tokenHighlightStyle`), read from its specs and
// never copied, so a keyword or a comment has one colour across the console. A spec's
// index is its class name here, so a piece finds its ink again.
const SPECS = tokenHighlightStyle.specs
// A spec's tag and its fixed class name are not ink: the rest of it is.
const INKS: CSSProperties[] = SPECS.map(({ tag: _t, class: _c, ...ink }) => ink)
const inkHighlighter = tagHighlighter(
  SPECS.map((spec, i) => ({ tag: spec.tag, class: `ink-${i}` })),
)

/** A piece with its `tok-*` classes and the editors' ink its `ink-*` classes name. */
function pieceOf(text: string, classes: string): CodePiece {
  const names = classes.split(' ')
  const inks = names.filter((name) => name.startsWith('ink-'))
  const cls = names.filter((name) => name.startsWith('tok-')).join(' ') || null
  if (inks.length === 0) return { text, cls }
  const ink = inks.map((name) => INKS[Number(name.slice(4))])
  return { text, cls, ink: Object.assign({}, ...ink) }
}

type Grammar = () => Promise<StreamParser<unknown>>

const GRAMMARS = {
  shell: async () =>
    (await import('@codemirror/legacy-modes/mode/shell')).shell,
  javascript: async () =>
    (await import('@codemirror/legacy-modes/mode/javascript')).javascript,
  typescript: async () =>
    (await import('@codemirror/legacy-modes/mode/javascript')).typescript,
  json: async () =>
    (await import('@codemirror/legacy-modes/mode/javascript')).json,
  python: async () =>
    (await import('@codemirror/legacy-modes/mode/python')).python,
  go: async () => (await import('@codemirror/legacy-modes/mode/go')).go,
  rust: async () => (await import('@codemirror/legacy-modes/mode/rust')).rust,
  yaml: async () => (await import('@codemirror/legacy-modes/mode/yaml')).yaml,
  toml: async () => (await import('@codemirror/legacy-modes/mode/toml')).toml,
  diff: async () => (await import('@codemirror/legacy-modes/mode/diff')).diff,
  css: async () => (await import('@codemirror/legacy-modes/mode/css')).css,
  sql: async () =>
    (await import('@codemirror/legacy-modes/mode/sql')).standardSQL,
  c: async () => (await import('@codemirror/legacy-modes/mode/clike')).c,
  cpp: async () => (await import('@codemirror/legacy-modes/mode/clike')).cpp,
  java: async () => (await import('@codemirror/legacy-modes/mode/clike')).java,
  dockerfile: async () =>
    (await import('@codemirror/legacy-modes/mode/dockerfile')).dockerFile,
  xml: async () => (await import('@codemirror/legacy-modes/mode/xml')).xml,
} satisfies Record<string, Grammar>

/** A grammar's name, as the fence names map onto it (`code-highlight.ts`). */
export type GrammarName = keyof typeof GRAMMARS

/** A grammar name's language (a fence name `code-highlight.ts` has read). */
export async function loadLanguage(
  name: GrammarName,
): Promise<StreamLanguage<unknown>> {
  const grammar: Grammar = GRAMMARS[name]
  return StreamLanguage.define(await grammar())
}

/** The block as coloured pieces of that language. */
export function highlight(
  text: string,
  language: StreamLanguage<unknown>,
): CodePiece[] {
  const pieces: CodePiece[] = []
  highlightCode(
    text,
    language.parser.parse(text),
    [classHighlighter, inkHighlighter],
    (piece, classes) => pieces.push(pieceOf(piece, classes)),
    () => pieces.push({ text: '\n', cls: null }),
  )
  return pieces
}
