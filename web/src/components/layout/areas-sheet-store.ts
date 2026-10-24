// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { create } from 'zustand'

/**
 * Whether All areas is open, and the control that opened it: closing gives focus back to
 * it. One store, because three controls open the one sheet (the account menu, the palette
 * and the phone bar's More).
 */
export const useAreasSheet = create<{
  open: boolean
  opener: HTMLElement | null
  show: (opener?: HTMLElement | null) => void
  setOpen: (open: boolean) => void
}>((set) => ({
  open: false,
  opener: null,
  // Safari does not focus a button on click, so the active element can be <body>: that is
  // no opener, and focus then stays with the sheet primitive's own restore.
  show: (opener) => {
    const active =
      opener ??
      (document.activeElement instanceof HTMLElement
        ? document.activeElement
        : null)
    set({ open: true, opener: active === document.body ? null : active })
  },
  setOpen: (open) => set({ open }),
}))

/** Give focus back to the control that opened All areas; false when it is gone. The sheet
 * primitive's own restore left focus on <body> on the built console (measured 2026-09-06,
 * keyboard and pointer paths). */
export function restoreOpener(): boolean {
  const el = useAreasSheet.getState().opener
  if (!el?.isConnected) return false
  el.focus()
  return true
}
