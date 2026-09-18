// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE EMPTY-STATE CENSUS — the second instrument over the console's empty states.
//
// The FIRST is the compiler: once `EmptyStateProps.description` is required and typed
// `NonNullable<ReactNode>` (the commit that fixes the call sites), an `<EmptyState>`
// written without one does not build. This module exists for the three things a type
// cannot see:
//   · a description that is present and EMPTY (`description=""`, `description={''}`),
//   · how many empty states offer the operator an action at all,
//   · the LIST, file:line, that the backlog rule in CLAUDE.md demands instead of a count.
//
// It is a text scan, not a TypeScript program, and it says so. A scan can be fooled —
// by a call site built from a spread, by JSX inside a template literal — so the numbers
// here are a FLOOR on what exists and the gate is written to fail closed: an element
// whose opening tag it cannot parse to the end is reported as `unparsed`, never
// silently skipped. `empty-state.census.test.ts` drives it with mutants for exactly
// that reason.
import { readFileSync, readdirSync, statSync } from 'node:fs'
import { join } from 'node:path'

/**
 * Blank every comment in `src`, preserving length and line breaks so every offset and
 * every line number stays exactly what it was.
 *
 * ⛔ IT EXISTS BECAUSE THE CENSUS COUNTED ITS OWN PROSE. The first run of this module
 *    reported two bare empty states in `components/ui/empty-state.tsx` — at lines 15
 *    and 30, which are inside the doc comment that explains the rule. That is the
 *    defect CLAUDE.md names for exactly this class of instrument ("un regex de tokens
 *    cuenta la prosa del fichero que mide"), found in the first five minutes of using
 *    it, and it would have made every future mention of `<EmptyState…>` in a comment a
 *    permanent red.
 *
 *    Strings are skipped before comments are looked for, so a `'https://…'` in a
 *    literal is not mistaken for the start of a line comment.
 */
export function stripComments(src: string): string {
  const out = src.split('')
  let quote: string | null = null
  for (let i = 0; i < src.length; i++) {
    const c = src[i]
    if (quote) {
      if (c === '\\') i++
      else if (c === quote) quote = null
      continue
    }
    if (c === '"' || c === "'" || c === '`') {
      quote = c
      continue
    }
    if (c === '/' && src[i + 1] === '/') {
      while (i < src.length && src[i] !== '\n') out[i++] = ' '
      continue
    }
    if (c === '/' && src[i + 1] === '*') {
      const end = src.indexOf('*/', i + 2)
      const stop = end === -1 ? src.length : end + 2
      for (; i < stop; i++) if (src[i] !== '\n') out[i] = ' '
      i--
      continue
    }
  }
  return out.join('')
}

export interface EmptyStateSite {
  file: string
  line: number
  /** Prop names found at the top level of the opening tag, in source order. */
  props: string[]
  hasDescription: boolean
  hasAction: boolean
  hasSecondaryAction: boolean
  /** A description that is present but resolves to the empty string. */
  emptyDescription: boolean
}

export interface Census {
  sites: EmptyStateSite[]
  /** Opening tags whose end could not be found — a parse failure, never a pass. */
  unparsed: { file: string; line: number }[]
}

/** Walk a directory tree, returning every file whose name ends with one of `exts`. */
export function walk(dir: string, exts = ['.tsx']): string[] {
  const out: string[] = []
  for (const name of readdirSync(dir).sort()) {
    const path = join(dir, name)
    if (statSync(path).isDirectory()) {
      if (name === 'node_modules' || name === '__shots__') continue
      out.push(...walk(path, exts))
    } else if (exts.some((e) => name.endsWith(e))) {
      out.push(path)
    }
  }
  return out
}

/**
 * Find the index just past the `>` that closes the JSX opening tag starting at
 * `from` (which must point at the `<`). Returns -1 when the tag does not close —
 * the caller reports that as `unparsed` rather than assuming anything about it.
 *
 * It tracks the three nestings that can legally contain a `>`: braces (a JSX
 * expression), and single/double/backtick quotes. It does NOT try to be a parser:
 * a `>` inside a `//` comment inside a JSX expression would end the tag early, and
 * the mutant battery pins that as a KNOWN limit rather than a claim of correctness.
 */
export function endOfOpeningTag(src: string, from: number): number {
  let depth = 0
  let quote: string | null = null
  for (let i = from; i < src.length; i++) {
    const c = src[i]
    if (quote) {
      if (c === '\\') i++
      else if (c === quote) quote = null
      continue
    }
    if (c === '"' || c === "'" || c === '`') {
      quote = c
      continue
    }
    if (c === '{') depth++
    else if (c === '}') depth--
    else if (c === '>' && depth === 0) return i + 1
  }
  return -1
}

/** The prop names written at the top level of an opening tag's attribute list. */
export function topLevelProps(attrs: string): string[] {
  const names: string[] = []
  let depth = 0
  let quote: string | null = null
  let word = ''
  for (let i = 0; i < attrs.length; i++) {
    const c = attrs[i]
    if (quote) {
      if (c === '\\') i++
      else if (c === quote) quote = null
      continue
    }
    if (c === '"' || c === "'" || c === '`') {
      quote = c
      word = ''
      continue
    }
    if (c === '{') {
      depth++
      word = ''
      continue
    }
    if (c === '}') {
      depth--
      word = ''
      continue
    }
    if (depth > 0) continue
    if (/[A-Za-z0-9_-]/.test(c)) {
      word += c
      continue
    }
    if (c === '=' && word) names.push(word)
    word = ''
  }
  return names
}

/**
 * Does the `description` prop resolve to the empty string? Only the two forms a
 * human writes are recognised — `description=""` and `description={''}`. Anything
 * computed is left alone: guessing at an expression would produce a finding nobody
 * can act on, which is worse than not looking.
 */
export function describesNothing(attrs: string): boolean {
  return /\bdescription=(""|''|\{\s*(''|""|``)\s*\})/.test(attrs)
}

/** Census every `<EmptyState …>` under `root` (a directory of .tsx sources). */
export function censusEmptyStates(root: string): Census {
  const sites: EmptyStateSite[] = []
  const unparsed: { file: string; line: number }[] = []
  for (const file of walk(root)) {
    const src = stripComments(readFileSync(file, 'utf8'))
    // `[\s/>]` after the name so `<EmptyStateProps` and `<EmptyStateRow` never match.
    const re = /<EmptyState(?=[\s/>])/g
    let m: RegExpExecArray | null
    while ((m = re.exec(src)) !== null) {
      const line = src.slice(0, m.index).split('\n').length
      const end = endOfOpeningTag(src, m.index)
      if (end === -1) {
        unparsed.push({ file, line })
        continue
      }
      const attrs = src.slice(m.index + '<EmptyState'.length, end - 1)
      const props = topLevelProps(attrs)
      sites.push({
        file,
        line,
        props,
        hasDescription: props.includes('description'),
        hasAction: props.includes('action'),
        hasSecondaryAction: props.includes('secondaryAction'),
        emptyDescription: describesNothing(attrs),
      })
    }
  }
  return { sites, unparsed }
}

/** The one number the oracle names: empty states that say nothing useful. */
export function bareStates(census: Census): EmptyStateSite[] {
  return census.sites.filter((s) => !s.hasDescription || s.emptyDescription)
}
