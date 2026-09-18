// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// KEYBINDINGS AS DATA — a declared table, a pure resolver, and a report.
//
// A review named the whole contract in one row: *"command IDs, `when` conditions,
// documented precedence, invalid rules ignored rather than fatal"*. Until now the console had no keybinding surface at all: `⌘K` lived in
// one `useEffect`, `?` and the `g`-sequences in another, and `p` would have been a third
// — three hand-written switch statements that no page could document and no test could
// enumerate.
//
// ⛔ WHAT IT DELIBERATELY IS NOT. A keybindings file on the operator's own machine was
//    rejected: this console is served by a multi-tenant engine and has no privileged
//    host to write to, and bindings belong to the principal rather than to a filesystem. So this table is CODE, shipped with the
//    console and identical for everyone — and the one page that documents it READS it,
//    so the documentation cannot drift from the behaviour.
//
// ⛔ AND THE THREE RULES ARE THE REFERENCE'S, because they are the small testable part
//    worth taking:
//      1. LAST MATCHING RULE WINS. Order is precedence, and it is written down.
//      2. AN UNKNOWN `when` KEY IS FALSE. A rule whose condition nobody supplies never
//         fires — it cannot be "accidentally global".
//      3. AN INVALID RULE IS IGNORED AND REPORTED, NEVER FATAL. One typo must not take
//         the keyboard away from every other binding.

/** One declared binding. `keys` is a chord; `when` is a boolean context key. */
export interface KeyRule {
  /** The command this fires — a stable id the UI and the docs both name. */
  command: string
  /** `Mod+K`, `Ctrl+Enter`, `ArrowDown`, `p`, `/`. See `parseChord`. */
  keys: string
  /**
   * The context in which it applies. Absent = everywhere the handler runs.
   * A key nobody supplies resolves FALSE, so a `when` can only narrow.
   */
  when?: string
}

/** A chord, decomposed. `mod` is Meta OR Control — one rule for both platforms. */
export interface Chord {
  key: string
  mod: boolean
  shift: boolean
  alt: boolean
}

/** The context a handler is in: which boolean keys are true right now. */
export type KeyContext = Readonly<Record<string, boolean>>

const MODIFIERS = new Set(['mod', 'shift', 'alt'])

/**
 * Parse `Mod+Shift+K` into a chord, or null when the text is not a chord.
 *
 * Null is the "invalid rule" signal the audit reports and the resolver skips. It is
 * never an exception: a bad rule is ordinary data, not an exceptional condition.
 */
export function parseChord(keys: string): Chord | null {
  const parts = keys
    .split('+')
    .map((p) => p.trim())
    .filter((p) => p.length > 0)
  if (parts.length === 0) return null
  // `Space` is spelled out because a literal space cannot survive splitting on `+`
  // and trimming — and the listbox pattern binds it beside Enter.
  const raw = parts[parts.length - 1]
  const key = raw.toLowerCase() === 'space' ? ' ' : raw
  if (key.length === 0) return null
  // The final segment must be a KEY, not a modifier: `Mod+Shift` binds nothing.
  if (MODIFIERS.has(raw.toLowerCase())) return null
  const chord: Chord = { key, mod: false, shift: false, alt: false }
  for (const part of parts.slice(0, -1)) {
    const m = part.toLowerCase()
    if (m === 'mod') chord.mod = true
    else if (m === 'shift') chord.shift = true
    else if (m === 'alt') chord.alt = true
    else return null // an unknown modifier is an invalid rule, not a silent one
  }
  return chord
}

/** The parts of a keyboard event this matches on — so a test needs no real event. */
export interface KeyEventLike {
  key: string
  metaKey?: boolean
  ctrlKey?: boolean
  shiftKey?: boolean
  altKey?: boolean
}

/**
 * Does this event produce this chord?
 *
 * ⛔ SHIFT IS ONLY COMPARED FOR NAMED KEYS, and the reason is that a printable
 *    character already carries it: `?` is typed with Shift on most layouts, so demanding
 *    `shiftKey === false` for a `?` rule would make it unreachable, and demanding
 *    `true` would break the layouts where it is not. For `Enter`, `ArrowDown` and the
 *    rest, Shift is a real distinction and is compared exactly.
 */
export function chordMatches(chord: Chord, event: KeyEventLike): boolean {
  const printable = chord.key.length === 1
  const key = printable ? event.key.toLowerCase() : event.key
  const want = printable ? chord.key.toLowerCase() : chord.key
  if (key !== want) return false
  const mod = !!event.metaKey || !!event.ctrlKey
  if (mod !== chord.mod) return false
  if (!!event.altKey !== chord.alt) return false
  if (!printable && !!event.shiftKey !== chord.shift) return false
  if (printable && chord.shift && !event.shiftKey) return false
  return true
}

/** A rule the table declares that cannot be honoured, and why. */
export interface KeyProblem {
  rule: KeyRule
  reason: 'unparsable-chord' | 'no-command'
}

export interface KeyAudit {
  /** Rules that can fire, with their chord already parsed. */
  valid: { rule: KeyRule; chord: Chord }[]
  /** Rules that cannot, reported rather than thrown. */
  problems: KeyProblem[]
}

/** Split a table into what can fire and what cannot. Pure; the resolver uses it. */
export function auditKeybindings(rules: readonly KeyRule[]): KeyAudit {
  const valid: KeyAudit['valid'] = []
  const problems: KeyProblem[] = []
  for (const rule of rules) {
    if (!rule.command.trim()) {
      problems.push({ rule, reason: 'no-command' })
      continue
    }
    const chord = parseChord(rule.keys)
    if (!chord) {
      problems.push({ rule, reason: 'unparsable-chord' })
      continue
    }
    valid.push({ rule, chord })
  }
  return { valid, problems }
}

/**
 * Which command this event fires in this context, or null.
 *
 * LAST MATCHING RULE WINS: a later entry overrides an earlier one, so a narrower rule
 * is written below the general one it refines. That is the precedence, it is the only
 * precedence, and it is what the documentation page states.
 */
export function resolveBinding(
  rules: readonly KeyRule[],
  event: KeyEventLike,
  context: KeyContext = {},
): string | null {
  let won: string | null = null
  for (const { rule, chord } of auditKeybindings(rules).valid) {
    // An unknown context key is FALSE: a rule nobody supplies a context for never
    // fires, which is what stops a `when` from being accidentally global.
    if (rule.when !== undefined && context[rule.when] !== true) continue
    if (!chordMatches(chord, event)) continue
    won = rule.command
  }
  return won
}

/**
 * True when the event came from somewhere typing is expected.
 *
 * It lives beside the resolver rather than in one component because every keyboard
 * surface needs the same answer, and two copies of "is the operator typing" is how one
 * of them ends up eating a keystroke from a form.
 */
export function isTypingTarget(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) return false
  const tag = target.tagName
  return (
    tag === 'INPUT' ||
    tag === 'TEXTAREA' ||
    tag === 'SELECT' ||
    target.isContentEditable
  )
}
