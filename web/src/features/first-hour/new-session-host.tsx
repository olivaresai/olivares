// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { NewSessionDialog } from './first-hour'
import { useNewSessionDialog } from './new-session-store'

/** The New session dialog, mounted once in the authenticated shell. */
export function NewSessionHost() {
  const open = useNewSessionDialog((s) => s.open)
  const setOpen = useNewSessionDialog((s) => s.setOpen)
  return <NewSessionDialog open={open} onOpenChange={setOpen} />
}
