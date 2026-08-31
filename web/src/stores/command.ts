// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { create } from 'zustand'

/** Open state for the ⌘K command palette — shared so the topbar search button and
 * the global keyboard shortcut both drive the one dialog. Also carries the
 * pending palette action: a verb selected in ⌘K (e.g. "new subscription")
 * that the target view consumes once on mount/arrival to open its dialog. */
interface CommandState {
  open: boolean
  pendingAction: { featureId: string; action: string } | null
  setOpen: (open: boolean) => void
  toggle: () => void
  setPendingAction: (featureId: string, action: string) => void
  /** Returns and clears the pending action if it targets featureId. */
  consumeAction: (featureId: string) => string | null
}

export const useCommandStore = create<CommandState>((set, get) => ({
  open: false,
  pendingAction: null,
  setOpen: (open) => set({ open }),
  toggle: () => set((s) => ({ open: !s.open })),
  setPendingAction: (featureId, action) =>
    set({ pendingAction: { featureId, action } }),
  consumeAction: (featureId) => {
    const pending = get().pendingAction
    if (!pending || pending.featureId !== featureId) return null
    set({ pendingAction: null })
    return pending.action
  },
}))
