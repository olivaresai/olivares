// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// CodeMirror 6 language + theme wiring for the governance editors. CM6 was
// chosen over Monaco because it keeps the CSP L3 + Trusted Types posture INTACT
// (verified: zero eval/new Function → no `unsafe-eval`; zero Worker/blob: → no
// worker-src; zero innerHTML/outerHTML → never trips `require-trusted-types-for
// 'script'`; styles inject via a <style>/CSSOM sink permitted by the
// `style-src 'unsafe-inline'` Keeps). DO NOT swap in an engine that needs any
// of those relaxations — the guardrail must not be weakened (docs/SECURITY-HARDENING.md §d).
//
// The theme references the Warm-Terminal DTCG tokens by CSS var so it re-resolves
// automatically on light/dark toggle (no per-theme CM theme, no getComputedStyle
// snapshot to invalidate).
import { json, jsonParseLinter } from '@codemirror/lang-json'
import {
  HighlightStyle,
  LanguageSupport,
  StreamLanguage,
  syntaxHighlighting,
} from '@codemirror/language'
import type { Extension } from '@codemirror/state'
import { EditorView } from '@codemirror/view'
import { tags as t } from '@lezer/highlight'

export type CodeLanguage = 'json' | 'cedar' | 'rego' | 'text'

// --- Cedar (AWS policy language embedded PDP) -------------------------------
// Cedar is a deny-only forbid-overlay in our PDP: a `permit` an operator
// writes can never widen an RBAC grant. The highlighter is presentation only.
const CEDAR_KEYWORDS = new Set([
  'permit',
  'forbid',
  'when',
  'unless',
  'if',
  'then',
  'else',
  'in',
  'has',
  'like',
  'is',
])
const CEDAR_ATOMS = new Set(['true', 'false'])
const CEDAR_VARS = new Set(['principal', 'action', 'resource', 'context'])

const cedarLanguage = StreamLanguage.define<{ inBlockComment: boolean }>({
  name: 'cedar',
  startState: () => ({ inBlockComment: false }),
  token(stream, state) {
    if (state.inBlockComment) {
      if (stream.match(/^.*?\*\//)) state.inBlockComment = false
      else stream.skipToEnd()
      return 'comment'
    }
    if (stream.match(/^\/\*/)) {
      state.inBlockComment = true
      return 'comment'
    }
    if (stream.match(/^\/\/.*/)) return 'comment'
    if (stream.match(/^"(?:[^"\\]|\\.)*"?/)) return 'string'
    if (stream.match(/^[0-9]+/)) return 'number'
    if (stream.match(/^[A-Za-z_][A-Za-z0-9_]*/)) {
      const w = stream.current()
      if (CEDAR_KEYWORDS.has(w)) return 'keyword'
      if (CEDAR_ATOMS.has(w)) return 'atom'
      if (CEDAR_VARS.has(w)) return 'variableName'
      return null
    }
    if (stream.match(/^(::|==|!=|<=|>=|&&|\|\||[(){}[\];,.<>!+\-*/@?])/)) {
      return 'operator'
    }
    stream.next()
    return null
  },
  languageData: {
    commentTokens: { line: '//', block: { open: '/*', close: '*/' } },
  },
})

// --- Rego (Open Policy Agent OPA-over-HTTP adapter) -------------------------
const REGO_KEYWORDS = new Set([
  'package',
  'import',
  'default',
  'some',
  'every',
  'in',
  'with',
  'as',
  'not',
  'contains',
  'if',
  'else',
])
const REGO_ATOMS = new Set(['true', 'false', 'null'])

const regoLanguage = StreamLanguage.define({
  name: 'rego',
  token(stream) {
    if (stream.match(/^#.*/)) return 'comment'
    if (stream.match(/^"(?:[^"\\]|\\.)*"?/)) return 'string'
    if (stream.match(/^-?[0-9]+(?:\.[0-9]+)?/)) return 'number'
    if (stream.match(/^[A-Za-z_][A-Za-z0-9_]*/)) {
      const w = stream.current()
      if (REGO_KEYWORDS.has(w)) return 'keyword'
      if (REGO_ATOMS.has(w)) return 'atom'
      return null
    }
    if (stream.match(/^(:=|==|!=|<=|>=|\[|\]|[(){}.,;=<>!+\-*/%|&])/)) {
      return 'operator'
    }
    stream.next()
    return null
  },
  languageData: { commentTokens: { line: '#' } },
})

/** Resolve the CodeMirror language support for a governance editor language. */
export function languageExtension(language: CodeLanguage): Extension {
  switch (language) {
    case 'json':
      return json()
    case 'cedar':
      return new LanguageSupport(cedarLanguage)
    case 'rego':
      return new LanguageSupport(regoLanguage)
    case 'text':
      // Plain text — no language grammar (the workspace editor views/edits
      // arbitrary files; only JSON is highlighted, everything else is plain text).
      return []
  }
}

/** The JSON syntax linter (parse errors as inline diagnostics) for json editors. */
export { jsonParseLinter }

/** Token → DTCG-var highlight map; re-resolves per theme because it uses CSS vars. */
export const tokenHighlightStyle = HighlightStyle.define([
  { tag: t.keyword, color: 'var(--accent-text)', fontWeight: '600' },
  { tag: [t.atom, t.bool, t.null], color: 'var(--accent-text)' },
  { tag: [t.string, t.special(t.string)], color: 'var(--success)' },
  { tag: t.number, color: 'var(--confidence-attributed)' },
  {
    tag: [t.propertyName, t.definition(t.propertyName)],
    color: 'var(--foreground)',
  },
  { tag: [t.variableName, t.attributeName], color: 'var(--accent-text)' },
  {
    tag: [t.comment, t.lineComment, t.blockComment],
    color: 'var(--muted-foreground)',
    fontStyle: 'italic',
  },
  {
    tag: [t.operator, t.punctuation, t.bracket],
    color: 'var(--muted-foreground)',
  },
  { tag: t.invalid, color: 'var(--danger)' },
])

/** The shared editor chrome theme — all colours come from Warm-Terminal tokens. */
export const editorTheme = EditorView.theme({
  '&': {
    fontSize: '0.8125rem',
    backgroundColor: 'var(--surface)',
    color: 'var(--foreground)',
    borderRadius: 'var(--radius-md)',
  },
  '&.cm-focused': { outline: 'none' },
  '.cm-scroller': {
    fontFamily: 'var(--font-mono, ui-monospace, monospace)',
    lineHeight: '1.6',
  },
  // The caret is a FOREGROUND mark on the editor surface, not a brand fill, so it
  // takes --accent-text: on light that is the deepened #b45500 (4.95:1 on #ffffff),
  // where the fill orange #f08000 would drop to 2.69:1. In dark the two tokens are
  // the same #f08000, so this is a no-op there. Selection below stays on --accent:
  // it IS a fill.
  '.cm-content': { caretColor: 'var(--accent-text)' },
  '.cm-cursor, .cm-dropCursor': { borderLeftColor: 'var(--accent-text)' },
  '.cm-gutters': {
    backgroundColor: 'var(--surface)',
    color: 'var(--muted-foreground)',
    border: 'none',
    borderRight: '1px solid var(--border)',
  },
  '.cm-activeLine': {
    backgroundColor: 'color-mix(in oklab, var(--muted) 45%, transparent)',
  },
  '.cm-activeLineGutter': {
    backgroundColor: 'color-mix(in oklab, var(--muted) 45%, transparent)',
  },
  '&.cm-focused .cm-selectionBackground, .cm-selectionBackground, ::selection':
    {
      backgroundColor: 'color-mix(in oklab, var(--accent) 22%, transparent)',
    },
  '.cm-lintRange-error': {
    textDecoration: 'underline wavy var(--danger)',
  },
  '.cm-lintRange-warning': {
    textDecoration: 'underline wavy var(--warning)',
  },
  '.cm-tooltip': {
    backgroundColor: 'var(--elevated)',
    color: 'var(--foreground)',
    border: '1px solid var(--border-strong)',
    borderRadius: 'var(--radius-md)',
  },
  '.cm-tooltip.cm-tooltip-lint .cm-diagnostic': {
    padding: '0.25rem 0.5rem',
    fontFamily: 'var(--font-sans)',
  },
})

/** Base extensions shared by editor + diff (theme + highlight). */
export const baseHighlightExtensions: Extension = [
  editorTheme,
  syntaxHighlighting(tokenHighlightStyle),
]
