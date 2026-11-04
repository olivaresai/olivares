// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { create } from 'zustand'

/** One action the palette offers for the selected object. */
export interface ObjectAction {
  /** Stable id, unique within the selection — also the palette item's `object:<id>` value. */
  id: string
  /** What the item says. */
  label: string
  /** The key that runs it from the object's own surface, shown as a hint. */
  shortcut?: string
  /** Runs when the palette item is chosen. */
  run: () => void
}

/** The object the operator is on: its name and the actions its surface offers. */
export interface ObjectSelection {
  label: string
  actions: ObjectAction[]
}

interface ObjectActionsState {
  /** The current selection, or null when nothing is selected. */
  current: ObjectSelection | null
  set: (selection: ObjectSelection | null) => void
}

/**
 * The selected object's actions, shared between the surface that owns the object and
 * the palette that lists them first. Transient by design: it mirrors
 * what is on screen, so it is never persisted.
 */
export const useObjectActionsStore = create<ObjectActionsState>((set) => ({
  current: null,
  set: (selection) => set({ current: selection }),
}))
