// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { create } from 'zustand'
import { sameContext, type CapabilityContext } from '@/lib/auth/capabilities'

/**
 * ONE QUEUED VERB, BOUND TO THE AUTHORITY THAT CHOSE IT.
 *
 * ⛔ THE COMMAND CARRIES NO AUTHORITY AND NOTHING SECRET. `context` is the same
 *    `CapabilityContext` the capability layer already builds — principal class, actor,
 *    tenant, credential generation, workspace and the local movement counter — and it is
 *    a TICKET TO BE MATCHED, never a permit to be honoured. It authorizes nothing on its
 *    own: the target view re-asks `can()` before it opens anything, and the engine decides
 *    the write regardless. There is no token, no session id and no payload here, by
 *    construction of `CapabilityContext` itself.
 *
 *    What the binding buys is the one thing a bare `{featureId, action}` could not say:
 *    WHICH authority selected the verb. A command chosen as tenant A must not open a form
 *    after a switch to B — and, because `lifetime` separates A → B → A from A, not after a
 *    round trip back to A either, where every other field matches again.
 */
export interface PendingCommand {
  readonly featureId: string
  readonly action: string
  readonly context: CapabilityContext
}

/** Open state for the ⌘K command palette — shared so the topbar search button and
 * the global keyboard shortcut both drive the one dialog. Also carries the
 * pending palette action: a verb selected in ⌘K (e.g. "new subscription") that the
 * target feature's root observes and consumes once, on the arrival it was chosen for,
 * whether or not that feature was already on screen. */
interface CommandState {
  open: boolean
  pendingAction: PendingCommand | null
  /**
   * The control that had focus when the palette opened (the topbar search button, a
   * sidebar link, whatever the operator was on when pressing ⌘K), so closing the palette
   * can hand focus BACK to it. Measured on the built console (console-navigation-n1,
   * 2026-09-06, Chromium 147): after Escape the dialog primitive left focus on <body> on
   * both the keyboard and the pointer path, so the palette restores it itself. Not
   * persisted; a DOM node lives only as long as this page.
   */
  opener: HTMLElement | null
  setOpen: (open: boolean) => void
  toggle: () => void
  /** Returns the opener once and forgets it. */
  takeOpener: () => HTMLElement | null
  /**
   * Queue the verb selected in the palette, bound to the context it was selected in.
   *
   * A null context is NOT queued. An unestablished identity cannot be matched against
   * anything later, so a command tagged with one would be either unusable or — if the
   * match were relaxed to compensate — consumable by whoever arrived next.
   */
  setPendingAction: (
    featureId: string,
    action: string,
    context: CapabilityContext | null,
  ) => void
  /**
   * Take the command for `featureId`, if the one queued is for this feature.
   *
   * ⛔ A MATCHING FEATURE CONSUMES, WHATEVER THE ANSWER. Clearing only on a successful
   *    match would leave a retired command in the store — and a retired command is exactly
   *    the one that must not survive to meet a later, unrelated arrival. So the store
   *    clears, and ANSWERS null when the live context is not the one that queued it.
   *
   * ⛔ ANOTHER FEATURE'S COMMAND IS NOT TOUCHED. "Consume once" is per command, not per
   *    visit: a view that is not the target neither runs it nor discards it.
   *
   * The caller still re-checks its own authority on the id it receives; this answers only
   * "was this verb chosen, here, by the authority that is live now".
   */
  consumeAction: (
    featureId: string,
    now: CapabilityContext | null,
  ) => string | null
}

/** The element to give focus back to: whatever is focused now, unless nothing is. */
function currentOpener(): HTMLElement | null {
  if (typeof document === 'undefined') return null
  const el = document.activeElement
  return el instanceof HTMLElement && el !== document.body ? el : null
}

export const useCommandStore = create<CommandState>((set, get) => ({
  open: false,
  pendingAction: null,
  opener: null,
  setOpen: (open) =>
    set((s) => ({
      open,
      opener: open && !s.open ? currentOpener() : s.opener,
    })),
  toggle: () =>
    set((s) => ({
      open: !s.open,
      opener: !s.open ? currentOpener() : s.opener,
    })),
  takeOpener: () => {
    const el = get().opener
    if (el) set({ opener: null })
    return el
  },
  setPendingAction: (featureId, action, context) => {
    if (!context) return
    set({ pendingAction: { featureId, action, context } })
  },
  consumeAction: (featureId, now) => {
    const pending = get().pendingAction
    if (!pending || pending.featureId !== featureId) return null
    set({ pendingAction: null })
    return sameContext(pending.context, now) ? pending.action : null
  },
}))
