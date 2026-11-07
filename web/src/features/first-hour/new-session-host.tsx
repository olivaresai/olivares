// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useNavigate, useRouterState } from '@tanstack/react-router'
import { lazy, Suspense, useEffect, useRef } from 'react'
import { sessionTools } from './api'
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
  const search = useRouterState({ select: (s) => s.location.searchStr })
  const navigate = useNavigate()
  const consumed = useRef('')
  const draft = useNewSessionDialog((s) => s.draft)
  useEffect(() => {
    const params = new URLSearchParams(search)
    if (params.get('newSession') !== '1') {
      consumed.current = ''
      return
    }
    if (consumed.current === search) return
    consumed.current = search
    const tool = params.get('tool') ?? ''
    useNewSessionDialog.setState({
      open: true,
      advanced: null,
      draft: {
        tool: sessionTools().find((d) => d === tool),
        workspace: params.get('workspace') ?? undefined,
        prompt: params.get('prompt') ?? undefined,
      },
    })
    // The draft belongs to this sign-in and organization, not the next one.
    void navigate({
      replace: true,
      search: ((prev: Record<string, unknown>) => {
        const next = { ...prev }
        for (const key of ['newSession', 'tool', 'workspace', 'prompt'])
          delete next[key]
        return next
      }) as never,
    })
  }, [search, navigate])
  const open = useNewSessionDialog((s) => s.open)
  const setOpen = useNewSessionDialog((s) => s.setOpen)
  const advance = useNewSessionDialog((s) => s.advance)
  const advanced = useNewSessionDialog((s) => s.advanced)
  const closeAdvanced = useNewSessionDialog((s) => s.closeAdvanced)
  return (
    <>
      <NewSessionDialog
        key={JSON.stringify(draft)}
        open={open}
        initialDraft={draft ?? undefined}
        onOpenChange={setOpen}
        onAdvanced={advance}
      />
      {advanced && (
        <Suspense fallback={null}>
          <RunCreateDialog
            // Another template or handoff starts a draft of its own.
            key={[
              advanced.message,
              advanced.workspaceRef,
              advanced.templateId,
              advanced.worktreeFrom,
              advanced.driver,
              advanced.profileRef,
              advanced.model,
              advanced.effort,
              String(advanced.fastMode),
              advanced.serviceTier,
              advanced.runOn,
            ]
              .map((part) => part ?? '')
              .join('\n')}
            open={advanced.open}
            onOpenChange={(o) => (o ? undefined : closeAdvanced())}
            initialMessage={advanced.message}
            initialWorkspaceRef={advanced.workspaceRef}
            initialTemplateId={advanced.templateId}
            initialWorktreeFrom={advanced.worktreeFrom}
            initialDriver={advanced.driver}
            initialProfileRef={advanced.profileRef}
            initialModel={advanced.model}
            initialEffort={advanced.effort}
            initialFastMode={advanced.fastMode}
            initialServiceTier={advanced.serviceTier}
            initialRunOn={advanced.runOn}
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
