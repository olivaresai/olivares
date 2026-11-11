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
//    so the documentation cannot drift from the behaviour. A person changes a row's
//    keys in Settings › Keyboard; the edit is their preference on the server row, and
//    `resolveBinding` and every printed hint read the table WITH it (`withOverrides`).
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

/**
 * The Mod key as this platform names it: ⌘ on Apple systems, Ctrl on Linux and Windows,
 * which have no ⌘ key. Every hint prints Mod through here.
 */
export function modKeyName(): '⌘' | 'Ctrl' {
  const nav = globalThis.navigator as
    (Navigator & { userAgentData?: { platform?: string } }) | undefined
  const platform = nav?.userAgentData?.platform || nav?.platform || ''
  return /mac|iphone|ipad|ipod/i.test(platform) ? '⌘' : 'Ctrl'
}

/** A chord as the keys a person presses here: `Mod+k` is Ctrl, k (⌘, k on a Mac). */
export function chordKeys(keys: string): string[] {
  const mod = modKeyName()
  return keys
    .split('+')
    .map((p) => p.trim())
    .filter(Boolean)
    .map((p) => (p.toLowerCase() === 'mod' ? mod : p))
}

/** A chord as one short hint: ⌘K on a Mac, Ctrl+K elsewhere; glyphs join, words take a +. */
export function chordHint(keys: string): string {
  const parts = chordKeys(keys).map((p) =>
    p.length === 1 ? p.toUpperCase() : p,
  )
  return parts.join(parts.every((p) => p.length === 1) ? '' : '+')
}

/** A chord as `aria-keyshortcuts` names it: Mod as Control and as Meta, a letter as its
 * capital, the space bar as Space; nothing when the keys are not a chord. */
export function ariaKeys(keys: string): string | undefined {
  const chord = parseChord(keys)
  if (!chord) return undefined
  const named = chord.key === ' ' ? 'Space' : chord.key
  const key = named.length === 1 ? named.toUpperCase() : named
  const rest = `${chord.alt ? 'Alt+' : ''}${chord.shift ? 'Shift+' : ''}${key}`
  return chord.mod ? `Control+${rest} Meta+${rest}` : rest
}

/** The parts of a keyboard event this matches on — so a test needs no real event. */
export interface KeyEventLike {
  key: string
  /** The physical key (`KeyO`): what an Alt chord names, since Option retypes `key` on macOS. */
  code?: string
  metaKey?: boolean
  ctrlKey?: boolean
  shiftKey?: boolean
  altKey?: boolean
}

/**
 * Does this event produce this chord?
 *
 * ⛔ SHIFT IS ONLY COMPARED FOR NAMED KEYS AND LETTERS, and the reason is that any
 *    other character already carries it: `?` is typed with Shift on most layouts, so
 *    demanding `shiftKey === false` for a `?` rule would make it unreachable, and
 *    demanding `true` would break the layouts where it is not. For `Enter`, `ArrowDown`
 *    and the rest, and for a letter (Shift+P types a capital, so it is not `p`), Shift
 *    is a real distinction and is compared exactly, as a person's edit writes it.
 */
export function chordMatches(chord: Chord, event: KeyEventLike): boolean {
  const printable = chord.key.length === 1
  const key = printable ? event.key.toLowerCase() : event.key
  const want = printable ? chord.key.toLowerCase() : chord.key
  // Option+Shift+O types "Ø" on a Mac, so an Alt+letter chord also matches its physical
  // key, but only when the layout typed no letter: AZERTY's Alt+Q is not Alt+A.
  const physical =
    chord.alt && /^[a-z]$/i.test(chord.key) && !/^[a-z]$/i.test(event.key)
      ? event.code === `Key${chord.key.toUpperCase()}`
      : false
  if (key !== want && !physical) return false
  const mod = !!event.metaKey || !!event.ctrlKey
  if (mod !== chord.mod) return false
  if (!!event.altKey !== chord.alt) return false
  // A letter is compared without its case (Caps Lock types the capital alone), so its
  // Shift is read here.
  const exactShift = !printable || /^[a-z]$/i.test(chord.key)
  if (exactShift && !!event.shiftKey !== chord.shift) return false
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

/** One row of Settings › Keyboard: a command in its context. */
export function ruleId(rule: KeyRule): string {
  return `${rule.command}|${rule.when ?? ''}`
}

/**
 * The table as a person uses it: each row they changed takes the one chord they chose,
 * in the place of all the chords it had, and in its own place, so the precedence holds.
 * An edit that is not a chord changes nothing.
 */
export function withOverrides(
  rules: readonly KeyRule[],
  overrides: Readonly<Record<string, string>>,
): readonly KeyRule[] {
  const edited = new Set<string>()
  return rules.flatMap((rule) => {
    const id = ruleId(rule)
    const keys = Object.hasOwn(overrides, id) ? overrides[id] : undefined
    if (keys === undefined || !parseChord(keys)) return [rule]
    if (edited.has(id)) return []
    edited.add(id)
    return [{ ...rule, keys }]
  })
}

/** Why an edit of a row's keys cannot work, for Settings › Keyboard to say. */
export type KeyEditRefusal =
  | { ok: false; reason: 'unknownRow' }
  | { ok: false; reason: 'notAChord' }
  | { ok: false; reason: 'needsModifier' }
  | { ok: false; reason: 'needsAltOrShift' }
  | { ok: false; reason: 'taken'; by: KeyRule }
  | { ok: false; reason: 'leader' }
  | { ok: false; reason: 'tooLarge' }
export type KeyEditResult = { ok: true } | KeyEditRefusal

/** Longer than any chord a person presses: an edit past it is not keys. */
const KEY_EDIT_MAX = 32
/** The contexts in which a key never reaches a field, so one key alone may act there.
 * Any other context, or none, is one where a person may be typing. */
const OUTSIDE_A_FIELD: ReadonlySet<string> = new Set([
  'outsideField',
  'outsideList',
  'railFocused',
  'paletteRecent',
  'approvalQueue',
  'accessMap',
  'sourceDiff',
])
/** The rows that decide a request: never on one key alone. */
const DECIDES: ReadonlySet<string> = new Set([
  'approvals.approve',
  'approvals.deny',
  'surface.approve',
])

const sameChord = (a: Chord | null, b: Chord) =>
  !!a &&
  a.key.toLowerCase() === b.key.toLowerCase() &&
  a.mod === b.mod &&
  a.shift === b.shift &&
  a.alt === b.alt

/** Whether the table gives the row `id` these keys. */
const declares = (rules: readonly KeyRule[], id: string, chord: Chord) =>
  rules.some((r) => ruleId(r) === id && sameChord(parseChord(r.keys), chord))

/**
 * Whether a person may give the row `id` these keys, or with null give it back the
 * table's, with their other edits: a row of the table, a chord, Mod or Alt where one
 * key alone could fire while they type or decide a request (and Alt, or Mod with
 * Shift, where it decides one), not the `leader` that starts the go-to keys (the
 * table's `g`), and keys no other row answers (both would act, or the last rule that
 * matches would win without a word).
 */
export function checkKeyEdit(
  rules: readonly KeyRule[],
  edits: Readonly<Record<string, string>>,
  id: string,
  keys: string | null,
  leader: string,
): KeyEditResult {
  const own = rules.filter((r) => ruleId(r) === id)
  if (own.length === 0) return { ok: false, reason: 'unknownRow' }
  const rule = own[0]
  let chords = own.flatMap((r) => parseChord(r.keys) ?? [])
  if (keys !== null) {
    const chord = keys.length <= KEY_EDIT_MAX ? parseChord(keys) : null
    if (!chord) return { ok: false, reason: 'notAChord' }
    const typing = !rule.when || !OUTSIDE_A_FIELD.has(rule.when)
    if ((typing || DECIDES.has(rule.command)) && !chord.mod && !chord.alt)
      return { ok: false, reason: 'needsModifier' }
    // Mod alone is pressed while typing too (Ctrl+A selects a field's words): a key
    // that decides a request takes Alt, or Mod with Shift.
    if (DECIDES.has(rule.command) && !chord.alt && !chord.shift)
      return { ok: false, reason: 'needsAltOrShift' }
    // One `g` arms the go-to keys before any row reads it.
    const bare = !chord.mod && !chord.alt && !chord.shift
    if (bare && chord.key.toLowerCase() === leader)
      return { ok: false, reason: 'leader' }
    chords = [chord]
  }
  // The other rows as the person has them; this row's own edit is the one being made.
  const { [id]: _edit, ...others } = edits
  const table = withOverrides(rules, others)
  // In any context: each handler supplies its own, and several hold at once (a list
  // outside a field, and the approvals queue). Keys the table gives both rows are its
  // own precedence, not a clash.
  for (const chord of chords) {
    const by = table.find(
      (other) =>
        ruleId(other) !== id &&
        sameChord(parseChord(other.keys), chord) &&
        !(declares(rules, id, chord) && declares(rules, ruleId(other), chord)),
    )
    if (by) return { ok: false, reason: 'taken', by }
  }
  return { ok: true }
}

/**
 * The edits of a stored value that pass `checkKeyEdit`, each with all the others, in
 * table order, so the order they were saved in decides nothing. Of two edits that
 * clash, the row earlier in the table keeps its own. A row that loses its edit takes
 * the table's keys back, which can clash with an edit kept: so again, until none drops.
 */
export function validKeyEdits(
  rules: readonly KeyRule[],
  stored: unknown,
  leader: string,
): Record<string, string> {
  const saved: Record<string, unknown> =
    stored !== null && typeof stored === 'object'
      ? (stored as Record<string, unknown>)
      : {}
  const edits: Record<string, string> = {}
  for (const id of new Set(rules.map(ruleId))) {
    const keys = saved[id]
    if (typeof keys === 'string') edits[id] = keys
  }
  const order = Object.keys(edits)
  let dropped = true
  while (dropped) {
    dropped = false
    for (const id of order) {
      if (!Object.hasOwn(edits, id)) continue
      const result = checkKeyEdit(rules, edits, id, edits[id], leader)
      if (result.ok) continue
      const by = result.reason === 'taken' ? ruleId(result.by) : ''
      const later =
        Object.hasOwn(edits, by) && order.indexOf(by) > order.indexOf(id)
      delete edits[later ? by : id]
      dropped = true
    }
  }
  return edits
}

/** The person's own keys the resolver applies, checked against the table: handed over by
 * lib/keybindings/effective.ts once it loads and on every change of the settings. */
let personalKeys: Readonly<Record<string, string>> = {}
export function setPersonalKeys(edits: Readonly<Record<string, string>>): void {
  personalKeys = edits
}

/**
 * Which command this event fires in this context, or null.
 *
 * LAST MATCHING RULE WINS: a later entry overrides an earlier one, so a narrower rule
 * is written below the general one it refines. That is the precedence, it is the only
 * precedence, and it is what the documentation page states.
 *
 * The rules are read with the person's own keys from Settings › Keyboard, so every
 * handler fires on the keys the person chose and the hints print.
 */
export function resolveBinding(
  rules: readonly KeyRule[],
  event: KeyEventLike,
  context: KeyContext = {},
): string | null {
  let won: string | null = null
  const edited = withOverrides(rules, personalKeys)
  for (const { rule, chord } of auditKeybindings(edited).valid) {
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
 *
 * An open menu counts: its letters are typeahead ("Ren" finds Rename), and a shell
 * key read from them (`n`, a new session) took the menu's choice away.
 */
export function isTypingTarget(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) return false
  const tag = target.tagName
  return (
    tag === 'INPUT' ||
    tag === 'TEXTAREA' ||
    tag === 'SELECT' ||
    target.isContentEditable ||
    target.closest('[role="menu"]') !== null
  )
}

/** True while any Radix dialog or sheet is open: a bare key then belongs to it, never to
 * the page behind it. Beside `isTypingTarget` for the same reason: one answer for every
 * keyboard surface. */
export function modalIsOpen(): boolean {
  return !!document.querySelector(
    '[role="dialog"][data-state="open"], [role="alertdialog"][data-state="open"]',
  )
}

/**
 * THE `g` LEADER, ARMED: one value for the whole console, not a ref inside the shell.
 * A page that answers a bare key asks it too, so the second half of `g a` is never read
 * as `a`, whichever window listener runs first (the shell re-adds its listener when it
 * re-renders, so registration order is not a guarantee;).
 */
let navLeaderUntil = 0
export const navLeader = {
  arm(until: number) {
    navLeaderUntil = until
  },
  disarm() {
    navLeaderUntil = 0
  },
  armed(now: number = Date.now()): boolean {
    return navLeaderUntil >= now
  },
}

/**
 * True when the event target is inside a list surface the shell will not move.
 *
 * The shell's bare `j`/`k` row movement applies OUTSIDE such a surface.
 * A grid answers those letters itself; a listbox, tree or tablist is included
 * deliberately because a dead letter beats stolen focus — re-dispatching an arrow
 * into a surface whose own handling does not expect it can yank the operator's
 * position or open the wrong thing. It lives beside the resolver for the same
 * reason `isTypingTarget` does: every keyboard surface needs one answer.
 */
export function isInsideList(target: EventTarget | null): boolean {
  if (!(target instanceof Element)) return false
  return (
    target.closest(
      '[role="grid"], [role="listbox"], [role="tree"], [role="tablist"], [data-list-keys]',
    ) !== null
  )
}
