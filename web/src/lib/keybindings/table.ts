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
// ⛔ AND THE `g`-SEQUENCES ARE NOT CHORDS, so they are not rules of `KEYBINDINGS`. They
//    are a LEADER SEQUENCE — two keystrokes with a timeout — and each names a registry id,
//    not a path: `shortcuts.tsx` reads the destination from the registry at render time.
//    They are DECLARED here, in `NAV_SEQUENCES`, so every key the shell answers is declared
//    in this one file. The documentation page shows both, from their own sources, and
//    says which is which.
import type { KeyRule } from './model'
import { COST_VIEW } from '@/features/registry'

/** The context keys this table uses. Anything else resolves false. */
export const KEY_CONTEXTS = [
  /** The work rail has keyboard focus. */
  'railFocused',
  /** The shell launcher's field has focus. */
  'launcherFocused',
  /** The sessions work surface is the routed view. */
  'workSurface',
  /** No field has focus, so a bare letter is a command and not text. */
  'outsideField',
  /** The source diff view is on screen and the event is outside a field. */
  'sourceDiff',
] as const

export type KeyContextName = (typeof KEY_CONTEXTS)[number]

/** Which group a command belongs to on the documentation page. */
export const KEY_GROUPS = ['general', 'launcher', 'surface'] as const
export type KeyGroup = (typeof KEY_GROUPS)[number]

/** A command's group, for the one page that documents the table. */
export const COMMAND_GROUP: Readonly<Record<string, KeyGroup>> = {
  'palette.open': 'general',
  'session.new': 'general',
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
  'sourceDiff.nextFile': 'surface',
  'sourceDiff.previousFile': 'surface',
  'sourceDiff.nextHunk': 'surface',
  'sourceDiff.previousHunk': 'surface',
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
  // New session (redesign §3.7.13): a bare letter, so only outside a field. `Shift+N`
  // (with another account) waits for the composer's account picker.
  { command: 'session.new', keys: 'n', when: 'outsideField' },

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

  // ── source diff, only while that view is listening and the target is not a field
  { command: 'sourceDiff.nextFile', keys: 'Alt+ArrowDown', when: 'sourceDiff' },
  {
    command: 'sourceDiff.previousFile',
    keys: 'Alt+ArrowUp',
    when: 'sourceDiff',
  },
  {
    command: 'sourceDiff.nextHunk',
    keys: 'Alt+ArrowRight',
    when: 'sourceDiff',
  },
  {
    command: 'sourceDiff.previousHunk',
    keys: 'Alt+ArrowLeft',
    when: 'sourceDiff',
  },
]

/** The key that arms a navigation sequence, and how long it stays armed. */
export const NAV_LEADER = 'g'
export const NAV_SEQUENCE_TIMEOUT_MS = 1200

/**
 * THE `g`-SEQUENCES: letter → registry id. A sequence only fires (and only shows on the
 * documentation page) when the principal may open that view — the sidebar's rule.
 *
 * The five journeys come first, on the letters of redesign §3.7.13: Home, Sessions,
 * Workspaces, AI tools (T), Deploy (D). `d` named Dashboards before the journeys; it moved
 * to `b` so that no destination lost its sequence.
 */
export const NAV_SEQUENCES: ReadonlyArray<{ key: string; featureId: string }> =
  [
    { key: 'h', featureId: 'home' },
    { key: 's', featureId: 'sessions' },
    { key: 'w', featureId: 'workspaceDashboard' },
    { key: 't', featureId: 'providers' },
    { key: 'd', featureId: 'deploy' },
    { key: 'i', featureId: 'inventory' },
    { key: 'a', featureId: 'automations' },
    { key: 'e', featureId: 'eventing' },
    { key: 'n', featureId: 'alerting' },
    { key: 'o', featureId: 'orchestration' },
    { key: 'c', featureId: 'console' },
    { key: 'p', featureId: 'permissions' },
    { key: 'm', featureId: 'models' },
    { key: 'f', featureId: COST_VIEW.id },
    { key: 'b', featureId: 'dashboards' },
    { key: 'u', featureId: 'audit' },
    { key: 'k', featureId: 'knowledge' },
  ]
