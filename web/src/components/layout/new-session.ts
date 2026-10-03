// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// NEW SESSION, ONE IMPLEMENTATION for the sidebar button, the phone bar and the `N` key:
// it opens the one New session dialog (tool, folder, first message), the same dialog
// Home and Sessions open (features/first-hour).
import { useNewSessionDialog } from '@/features/first-hour/new-session-store'

export function useNewSession(): () => void {
  const setOpen = useNewSessionDialog((s) => s.setOpen)
  return () => setOpen(true)
}
