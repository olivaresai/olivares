// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE TERMINAL DRAWER OF A SESSION, under its conversation.
//
// A session works in a folder; the drawer holds that folder's terminals (session-terminal.tsx)
// at the bottom of the session's column, shut or open, and the terminals outlive both: they
// are kept by the terminal store, not by the drawer. The host also lends the conversation
// two acts, each one a person's click: run a code block's command in a terminal, and carry
// selected terminal output to the message being written.
//
// ⛔ A SHUT DRAWER STARTS NOTHING. Opening it (its toggle or Mod+J) is the person asking for
//    a shell: one starts when the folder has none, and the screen takes the keys. Closing
//    the last terminal shuts the drawer.
import { useQuery } from '@tanstack/react-query'
import {
  ChevronDown,
  ChevronUp,
  Maximize2,
  Minimize2,
  Terminal as TerminalIcon,
} from 'lucide-react'
import {
  useEffect,
  useMemo,
  useRef,
  useSyncExternalStore,
  type ReactNode,
} from 'react'
import { useTranslation } from 'react-i18next'
import { create } from 'zustand'
import { Button } from '@/components/ui/button'
import { agentOpsApi, agentOpsKeys } from '@/features/agentops/api'
import { useAuthBoundary } from '@/features/agentops/auth-boundary'
import type { RunDTO } from '@/features/agentops/types'
import { useAuth } from '@/lib/auth/context'
import { cn } from '@/lib/utils'
import {
  readSessionDraft,
  sessionDraftKey,
  writeSessionDraft,
} from '@/lib/session-drafts'
import { RunInTerminalContext, type RunInTerminal } from './run-in-terminal'
import { useRunFolder } from './session-context-pane'
import { TerminalPanel, useTerminalGroup } from './session-terminal'
import './i18n'

interface DrawerState {
  open: Record<string, boolean>
  maximized: Record<string, boolean>
  setOpen: (run: string, open: boolean) => void
  toggle: (run: string) => void
  toggleMaximized: (run: string) => void
}

/** Whether the drawer of a session (by its run) is open, and whether it takes the column. */
export const useTerminalDrawer = create<DrawerState>()((set) => ({
  open: {},
  maximized: {},
  setOpen: (run, open) => set((s) => ({ open: { ...s.open, [run]: open } })),
  toggle: (run) => set((s) => ({ open: { ...s.open, [run]: !s.open[run] } })),
  toggleMaximized: (run) =>
    set((s) => ({ maximized: { ...s.maximized, [run]: !s.maximized[run] } })),
}))

/** A code fence long enough that the output cannot close it. */
export function fenced(text: string): string {
  let fence = '```'
  while (text.includes(fence)) fence += '`'
  return `${fence}\n${text.replace(/\n+$/, '')}\n${fence}`
}

/**
 * The session's folder for a terminal, as the Files tab resolves it: a worktree's own
 * registration, not the reference the session was launched with.
 */
function useTerminalFolder(run: RunDTO | undefined): string | null {
  const { activeTenant, can } = useAuth()
  const boundary = useAuthBoundary()
  const canRead = can('sessions:workspace:read')
  const query = useQuery({
    queryKey: [
      ...agentOpsKeys.boundaryScope(activeTenant, boundary.epoch),
      'workspace',
      run?.workspace_ref,
    ],
    queryFn: ({ signal }) =>
      agentOpsApi.getWorkspace(run!.workspace_ref!, {
        tenant: activeTenant!,
        signal,
      }),
    enabled: canRead && !!activeTenant && !!run?.workspace_ref,
  })
  const folder = useRunFolder({
    run,
    workspace: canRead ? query.data : undefined,
    canRead,
    loading: query.isPending && query.fetchStatus !== 'idle',
    failed: query.isError,
    onRetry: () => void query.refetch(),
  })
  return folder.actual?.workspace_ref ?? null
}

/**
 * Wraps a session's column. Where the person may use a terminal in the session's folder,
 * it gives the conversation `useRunInTerminal` and puts the drawer under it.
 */
export function TerminalHost({
  run,
  children,
}: {
  run: RunDTO | undefined
  children: ReactNode
}) {
  const { can } = useAuth()
  const workspaceRef = useTerminalFolder(run)
  const allowed = !!run?.workspace_ref && can('sessions:workspace:write')
  if (!run || !allowed || !workspaceRef) return <>{children}</>
  return (
    <HostWithFolder run={run} workspaceRef={workspaceRef}>
      {children}
    </HostWithFolder>
  )
}

function HostWithFolder({
  run,
  workspaceRef,
  children,
}: {
  run: RunDTO
  workspaceRef: string
  children: ReactNode
}) {
  const group = useTerminalGroup(workspaceRef)
  const runRef = run.run_ref
  const api = useMemo<RunInTerminal>(
    () => ({
      run: (command) => {
        useTerminalDrawer.getState().setOpen(runRef, true)
        group.run(command)
      },
    }),
    [group, runRef],
  )
  return (
    <RunInTerminalContext.Provider value={api}>
      {children}
      <SessionTerminalDrawer run={run} workspaceRef={workspaceRef} />
    </RunInTerminalContext.Provider>
  )
}

function SessionTerminalDrawer({
  run,
  workspaceRef,
}: {
  run: RunDTO
  workspaceRef: string
}) {
  const { t } = useTranslation('sessions')
  const { activeTenant, principal } = useAuth()
  const group = useTerminalGroup(workspaceRef)
  const groupView = useSyncExternalStore(group.subscribe, group.getView)
  const open = useTerminalDrawer((s) => s.open[run.run_ref] ?? false)
  const maximized = useTerminalDrawer((s) => s.maximized[run.run_ref] ?? false)
  const { toggle, setOpen, toggleMaximized } = useTerminalDrawer.getState()
  const count = groupView.items.length

  // Opening the drawer asks for a shell: one starts when there is none.
  const wasOpen = useRef(false)
  useEffect(() => {
    if (open && !wasOpen.current && count === 0) group.create()
    wasOpen.current = open
  }, [open, count, group])
  // Closing the last terminal shuts the drawer; a refused start stays to say why.
  const hadTerminals = useRef(count > 0)
  useEffect(() => {
    if (hadTerminals.current && count === 0 && !groupView.problem)
      setOpen(run.run_ref, false)
    hadTerminals.current = count > 0
  }, [count, groupView.problem, run.run_ref, setOpen])
  const draftKey = sessionDraftKey(activeTenant, principal, run.run_ref)

  // Selected output goes into the message being written, where the person reads it, edits
  // it and sends it or not. Nothing reaches the agent from here.
  const sendToAgent = draftKey
    ? (text: string, title: string) => {
        const block = `${t('terminal.contextLabel', { name: title })}\n${fenced(text)}`
        const draft = readSessionDraft(draftKey)
        writeSessionDraft(
          draftKey,
          draft ? `${draft}\n\n${block}\n` : `${block}\n`,
        )
      }
    : undefined

  const toggleButton = (
    <Button
      variant="ghost"
      size="sm"
      className="shrink-0 gap-2"
      aria-expanded={open}
      onClick={() => toggle(run.run_ref)}
    >
      <TerminalIcon aria-hidden />
      <span>{t('terminal.title')}</span>
      {!open && count > 0 ? (
        <span className="text-caption text-muted-foreground">{count}</span>
      ) : null}
      {open ? <ChevronDown aria-hidden /> : <ChevronUp aria-hidden />}
    </Button>
  )
  const maximizeButton = (
    <Button
      variant="ghost"
      size="icon"
      aria-label={maximized ? t('terminal.restore') : t('terminal.maximize')}
      title={maximized ? t('terminal.restore') : t('terminal.maximize')}
      aria-pressed={maximized}
      onClick={() => toggleMaximized(run.run_ref)}
    >
      {maximized ? <Minimize2 aria-hidden /> : <Maximize2 aria-hidden />}
    </Button>
  )

  return (
    <section
      aria-label={t('terminal.drawer')}
      data-testid="terminal-drawer"
      className={cn(
        'flex min-h-0 shrink-0 flex-col border-t border-line',
        // Maximized, it takes the column; the conversation keeps a sliver.
        open && maximized && 'shrink flex-[100_1_0%]',
      )}
    >
      {open ? (
        <div
          className={cn(
            'flex min-h-0 flex-col pb-2',
            maximized ? 'flex-1' : 'h-[min(24rem,50dvh)]',
          )}
        >
          <TerminalPanel
            group={group}
            onSendToAgent={sendToAgent}
            lead={toggleButton}
            trail={maximizeButton}
          />
        </div>
      ) : (
        <div className="flex h-9 items-center px-1">{toggleButton}</div>
      )}
    </section>
  )
}
