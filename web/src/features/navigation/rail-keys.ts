// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE NAVIGATION RAIL FROM A KEYBOARD — the movement, as a pure function.
//
// ⛔ THE DEFECT THIS CLOSES, measured on 2026-09-18 against a live engine at 1600 px with
//    the demo estate: the sidebar held **85 tab stops**. An operator arriving with the
//    keyboard pressed Tab eighty-five times before reaching the page. The skip link helps
//    the ones who know it exists; it does not make the rail usable.
//
// ⇒ SO THE RAIL IS ONE TAB STOP with a roving tabindex, which is what `DataTable` already
//   does for a grid of ten thousand rows and what the work rail does for sessions — and the same
//   sentence applies: moving focus is not choosing. Arrows move, Enter opens, and Enter on
//   a focused LINK is the browser's own behaviour, not a handler of ours.
//
// ⛔ AND IT IS A FUNCTION, NOT A `useEffect`. The rules below are the whole behaviour;
//    `rail-keys.test.ts` drives every one of them without a DOM, and the component only
//    has to turn an action into `focus()`, a click or a preference write. A keyboard
//    contract written inline in a component is a contract nobody can enumerate — which is
//    the reason the console's chords live in a declared table in the first place.

/** One row of the rail, in DOM order. */
export interface RailRow {
  /** An area header. `open` is what its disclosure button reports right now. */
  readonly area?: { readonly id: string; readonly open: boolean }
  /** The personal-navigation id this row can pin, when it has one. */
  readonly pinId?: string
  /** 0 for a top-level row, 1 for a leaf inside an open area. */
  readonly depth: number
}

/** What the rail must do. `none` means the key was not ours — never swallowed. */
export type RailAction =
  | { readonly kind: 'none' }
  | { readonly kind: 'focus'; readonly index: number }
  | { readonly kind: 'expand'; readonly areaId: string }
  | { readonly kind: 'fold'; readonly areaId: string }
  | { readonly kind: 'pin'; readonly index: number }

const NONE: RailAction = { kind: 'none' }

/** The index of the area row that owns a leaf — the nearest depth-0 row above it. */
export function parentIndex(rows: readonly RailRow[], index: number): number {
  for (let i = index - 1; i >= 0; i--) if (rows[i].depth === 0) return i
  return -1
}

/**
 * What one keystroke does to the rail.
 *
 * Movement CLAMPS rather than wraps. A wrapping rail sends an operator who holds ArrowDown
 * back to the top without saying so; the APG disclosure/tree patterns clamp, and Home/End
 * are the way to reach an end on purpose.
 */
export function railKey(
  key: string,
  rows: readonly RailRow[],
  active: number,
): RailAction {
  if (rows.length === 0) return NONE
  const at = Math.min(Math.max(active, 0), rows.length - 1)
  const row = rows[at]
  switch (key) {
    case 'ArrowDown':
      return at + 1 < rows.length ? { kind: 'focus', index: at + 1 } : NONE
    case 'ArrowUp':
      return at > 0 ? { kind: 'focus', index: at - 1 } : NONE
    case 'Home':
      return at === 0 ? NONE : { kind: 'focus', index: 0 }
    case 'End':
      return at === rows.length - 1
        ? NONE
        : { kind: 'focus', index: rows.length - 1 }
    case 'ArrowRight':
      // A closed area opens; an open one hands focus to its first leaf. A leaf has
      // nothing to the right, and inventing a move there would teach a rule that is
      // true on one row and false on the next.
      if (!row.area) return NONE
      if (!row.area.open) return { kind: 'expand', areaId: row.area.id }
      return at + 1 < rows.length && rows[at + 1].depth > 0
        ? { kind: 'focus', index: at + 1 }
        : NONE
    case 'ArrowLeft':
      // An open area folds; a leaf goes back to the area that holds it. Folding from a
      // leaf would move focus and change state in one keystroke, and the operator would
      // not see which of the two they asked for.
      if (row.area?.open) return { kind: 'fold', areaId: row.area.id }
      if (row.depth > 0) {
        const parent = parentIndex(rows, at)
        return parent >= 0 ? { kind: 'focus', index: parent } : NONE
      }
      return NONE
    case 'p':
    case 'P':
      // A row with nothing to pin is not an error and not a beep: `p` on the Overview
      // link or a section label simply is not this rail's key.
      return row.pinId ? { kind: 'pin', index: at } : NONE
    default:
      // Enter and Space are deliberately absent. Every row is a LINK or a BUTTON, and
      // the browser already activates a focused one — handling them here would be a
      // second, worse copy of behaviour we get for free, and it would break
      // modified-click and middle-click on the way.
      return NONE
  }
}

/**
 * The rail's own keys, for the page that documents the keyboard.
 *
 * ⛔ THEY ARE NOT IN `lib/keybindings/table.ts`, and the reason is that table's own rule
 *    read honestly: that table is what `resolveBinding` answers for, and the shell resolves
 *    none of these — `railKey` does, inside a composite widget, exactly as `DataTable`
 *    owns the arrow keys of a grid. A row in the table that the shell does not fire would
 *    be a lie of the kind the `?` page exists to prevent.
 *
 * ⇒ So they are declared HERE, beside the function that implements them, and the page
 *   renders this list. The documentation still cannot drift from the behaviour; it just
 *   reads the right source.
 */
export const RAIL_KEYS: ReadonlyArray<{ command: string; keys: string }> = [
  { command: 'navRail.next', keys: 'ArrowDown' },
  { command: 'navRail.previous', keys: 'ArrowUp' },
  { command: 'navRail.expand', keys: 'ArrowRight' },
  { command: 'navRail.fold', keys: 'ArrowLeft' },
  { command: 'navRail.first', keys: 'Home' },
  { command: 'navRail.last', keys: 'End' },
  { command: 'navRail.pin', keys: 'p' },
]
