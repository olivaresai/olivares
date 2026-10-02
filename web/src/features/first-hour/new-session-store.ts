// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { create } from 'zustand'

/** Whether the one New session dialog is open (the sidebar button, the phone bar,
 * the N key, Home and Sessions all open the same dialog). */
export const useNewSessionDialog = create<{
  open: boolean
  setOpen: (open: boolean) => void
}>((set) => ({ open: false, setOpen: (open) => set({ open }) }))
