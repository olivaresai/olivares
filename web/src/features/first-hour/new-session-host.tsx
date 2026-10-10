// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { lazy, Suspense } from 'react'
import { NewSessionDialog } from './first-hour'
import { useNewSessionDialog } from './new-session-store'

// Loaded once it is first asked for, not with the shell.
const RunCreateDialog = lazy(() =>
  import('@/features/agentops/run-create-dialog').then((m) => ({
    default: m.RunCreateDialog,
  })),
)

/** The New session form and the advanced launch, mounted once in the authenticated shell. */
export function NewSessionHost() {
  const open = useNewSessionDialog((s) => s.open)
  const setOpen = useNewSessionDialog((s) => s.setOpen)
  const advance = useNewSessionDialog((s) => s.advance)
  const advanced = useNewSessionDialog((s) => s.advanced)
  const closeAdvanced = useNewSessionDialog((s) => s.closeAdvanced)
  return (
    <>
      <NewSessionDialog
        open={open}
        onOpenChange={setOpen}
        onAdvanced={advance}
      />
      {advanced && (
        <Suspense fallback={null}>
          <RunCreateDialog
            // Another template or handoff starts a draft of its own.
            key={`${advanced.templateId ?? ''}\n${advanced.worktreeFrom ?? ''}`}
            open={advanced.open}
            onOpenChange={(o) => (o ? undefined : closeAdvanced())}
            initialTemplateId={advanced.templateId}
            initialWorktreeFrom={advanced.worktreeFrom}
            onCloseAutoFocus={(event) => {
              const { opener } = useNewSessionDialog.getState()
              if (!opener?.isConnected) return
              event.preventDefault()
              opener.focus()
            }}
          />
        </Suspense>
      )}
    </>
  )
}
