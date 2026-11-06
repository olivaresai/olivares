// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { create } from 'zustand'

/** Open state of the Ask Olivares panel, shared so the shortcut and the ⌘K palette drive the one panel. */
interface AskState {
  open: boolean
  setOpen: (open: boolean) => void
  toggle: () => void
}

export const useAskStore = create<AskState>((set) => ({
  open: false,
  setOpen: (open) => set({ open }),
  toggle: () => set((s) => ({ open: !s.open })),
}))
