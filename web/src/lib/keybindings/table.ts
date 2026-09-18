// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE CONSOLE'S KEYBINDINGS, DECLARED.
//
// ⛔ ORDER IS PRECEDENCE. `resolveBinding` takes the LAST matching rule, so a rule that
//    narrows another is written BELOW it. Nothing else decides; there is no specificity
//    score and no per-rule weight to reason about.
//
// ⛔ EVERY ROW HAS A `when` EXCEPT THE SHELL-WIDE ONES, and a `when` nobody supplies is
//    false. That is what keeps `p` from pinning while an operator types a session name,
//    and `Enter` from starting a launch while they are walking the rail.
//
// ⛔ AND THE `g`-SEQUENCES ARE NOT HERE, ON PURPOSE. They are a LEADER SEQUENCE — two
//    keystrokes with a timeout, and a destination read from the feature registry at
//    render time (`shortcuts.tsx`, `NAV_SHORTCUTS`) — not a chord. Modelling them as
//    fifteen chords would be a second, less true copy of a list the registry already
//    owns. The documentation page shows both, from their own sources, and says which is
//    which.
import type { KeyRule } from './model'

/** The context keys this table uses. Anything else resolves false. */
export const KEY_CONTEXTS = [
  /** The work rail has keyboard focus. */
  'railFocused',
  /** The shell launcher's field has focus. */
  'launcherFocused',
  /** The sessions work surface is the routed view. */
  'workSurface',
] as const

export type KeyContextName = (typeof KEY_CONTEXTS)[number]

/** Which group a command belongs to on the documentation page. */
export const KEY_GROUPS = ['general', 'launcher', 'surface'] as const
export type KeyGroup = (typeof KEY_GROUPS)[number]

/** A command's group, for the one page that documents the table. */
export const COMMAND_GROUP: Readonly<Record<string, KeyGroup>> = {
  'palette.open': 'general',
  'help.toggle': 'general',
  'nav.toggleRail': 'general',
  'launcher.focus': 'launcher',
  'launcher.start': 'launcher',
  'launcher.startBackground': 'launcher',
  'surface.nextPane': 'surface',
  'surface.previousPane': 'surface',
  'rail.next': 'surface',
  'rail.previous': 'surface',
  'rail.first': 'surface',
  'rail.last': 'surface',
  'rail.open': 'surface',
  'rail.pin': 'surface',
}

export const KEYBINDINGS: readonly KeyRule[] = [
  // ── shell-wide ────────────────────────────────────────────────────────────
  { command: 'palette.open', keys: 'Mod+k' },
  { command: 'help.toggle', keys: '?' },
  { command: 'launcher.focus', keys: '/' },
  // The rail folds to an icon rail and back. `Mod+b` rather than a bare letter
  // because this one has to work while the operator is typing — it is how you get the
  // width back mid-task — and the guard above only lets a MODIFIED chord through a field.
  { command: 'nav.toggleRail', keys: 'Mod+b' },

  // ── the launcher, only while its field has focus ──────────────────────────
  { command: 'launcher.start', keys: 'Enter', when: 'launcherFocused' },
  {
    command: 'launcher.startBackground',
    keys: 'Mod+Enter',
    when: 'launcherFocused',
  },

  // ── the work surface ──────────────────────────────────────────────────────
  { command: 'surface.nextPane', keys: ']', when: 'workSurface' },
  { command: 'surface.previousPane', keys: '[', when: 'workSurface' },

  // ── the rail, only while it has focus ─────────────────────────────────────
  { command: 'rail.next', keys: 'ArrowDown', when: 'railFocused' },
  { command: 'rail.previous', keys: 'ArrowUp', when: 'railFocused' },
  { command: 'rail.first', keys: 'Home', when: 'railFocused' },
  { command: 'rail.last', keys: 'End', when: 'railFocused' },
  { command: 'rail.open', keys: 'Enter', when: 'railFocused' },
  { command: 'rail.open', keys: 'Space', when: 'railFocused' },
  { command: 'rail.pin', keys: 'p', when: 'railFocused' },
]
