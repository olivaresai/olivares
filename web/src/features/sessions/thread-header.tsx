// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE HEAD OF A THREAD: ONE 48 px BAR, A WINDOW TITLE.
//
// The session's name, and under it one quiet line of where it works and with what (the
// folder, the branch, the tool, the model); on the right the state in words beside the
// actions that act on it, what it has spent, the lifecycle controls as icons (Stop only
// while a turn runs, End session), the Context toggle and a `⋯` menu with the rest (Full
// controls, Copy reference, Open in the editor, In a terminal, and the pin). It used to be
// a 280 px block that said the state five times.
//
// ⛔ WHAT THE LINE DOES NOT SAY IS NOT LOST: the badges (who manages the session, its mode,
//    the live stream) and the working folder's path are the Context pane's, in plain
//    words (`session-overview.tsx`). Paths, ids and sandbox names are never in a header.
import {
  ArrowUpRight,
  ExternalLink,
  Folder,
  GitBranch,
  MoreHorizontal,
  Pin,
  PinOff,
} from 'lucide-react'
import { useState, type ReactNode } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useAuth } from '@/lib/auth/context'
import { agentOpsApi, agentOpsKeys } from '@/features/agentops/api'
import { useAuthBoundary } from '@/features/agentops/auth-boundary'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { CodeLine } from '@/components/ui/code-line'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { Kbd } from '@/components/ui/kbd'
import { PathText } from '@/components/ui/path-text'
import { toast } from '@/components/ui/toaster'
import { AtLimit, useAccountUsageOf } from '@/features/agentops/account-usage'
import { toolName } from '@/features/agentops/engine-tools'
import { modelName } from '@/features/agentops/model-name'
import { ToolMark } from '@/features/agentops/tool-mark'
import { AccountBadge } from '@/features/agent-tools/account-marks'
import type { RunDTO } from '@/features/agentops/types'
import { formatMicroUsd } from '@/lib/format'
import { cn } from '@/lib/utils'
import { useEditorLink } from '@/features/shared/open-in-editor'
import { folderName, runFolder } from './folder'
import type { Capability, UnifiedSession } from './provenance'
import { hostIsolationUnavailable } from './provenance'
import { IconTip } from './icon-tip'
import { RunActions } from './run-actions'
import { SessionGit } from './session-git'
import { SessionMarkItems } from './session-mark-items'
import { LimitNotice } from './session-limit'
import { useUsagePath } from './session-meter'
import { StateDot } from './session-state-dot'
import { StateWord } from './session-state-word'
import { useSessionMarks } from './use-session-marks'
import type { LiveDTO } from './types'
import './i18n'

/** The lifecycle actions the header offers; the rest stay in the full controls. */
const HEADER_ACTIONS = ['interrupt', 'stop', 'resume'] as const

/** Copies a reference; true only once the clipboard took it (a denied one says nothing). */
async function copyReference(reference: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(reference)
    return true
  } catch {
    // An insecure context or a denied permission: the reference is still on screen in the
    // Context pane, selectable. A toast about a clipboard helps nobody.
    return false
  }
}

/** The three commands that follow the session from a terminal. */
function terminalCommands(run: RunDTO): string[] {
  const live =
    run.state === 'running' || run.state === 'idle' || run.state === 'pending'
  return [
    `olivares session follow ${run.run_ref}`,
    `olivares session follow ${run.run_ref} -o json`,
    ...(hostIsolationUnavailable(run)
      ? []
      : [
          `olivares session send ${run.run_ref} "…"`,
          `olivares session ${live ? 'stop' : 'resume'} ${run.run_ref}`,
        ]),
  ]
}

export function ThreadHeader({
  session,
  run,
  live,
  caps,
  title,
  titleHint,
  titleMono,
  address,
  reference,
  pinned,
  onTogglePin,
  onOpenDetail,
  onOpenContext,
  back,
  contextToggle,
  contextPaneButton,
  stacked = false,
}: {
  session: UnifiedSession
  run?: RunDTO
  live?: LiveDTO
  caps: Capability[]
  title: string
  /** The whole name, its tail and its reference, for the hover. */
  titleHint: string
  /** An unnamed session is told by the tail of its reference, in the machine face. */
  titleMono: boolean
  address: string
  /** The session's own reference, which Copy reference writes. */
  reference: string | null
  pinned: boolean
  onTogglePin: ((address: string) => void) | null
  /** The full session controls — attach, drive, stop, governance — live in the card.
   * Absent where no card is on hand (the Estate sheet): the menu leaves the item out. */
  onOpenDetail?: () => void
  /** Brings the Context pane forward where it is a pane of its own (a phone). */
  onOpenContext?: () => void
  /** The way back to the list, painted below 761 px only. */
  back?: ReactNode
  /** The Context icon toggle, painted from `xl` up. */
  contextToggle?: ReactNode
  /** The Context icon that opens the pane in front, painted between 761 px and `xl`. */
  contextPaneButton?: ReactNode
  /**
   * The sheet's form: the name and the controls on the first line and every fact
   * on a second one, at any width. A sheet is narrower than the viewport the one-line
   * form's breakpoints read, so there the facts would crowd the name out.
   */
  stacked?: boolean
}) {
  const { t } = useTranslation('sessions')
  const [terminalOpen, setTerminalOpen] = useState(false)
  const { activeTenant, can } = useAuth()
  const boundary = useAuthBoundary()
  const canReadWorkspaces = can('sessions:workspace:read')
  const workspaceQuery = useQuery({
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
    enabled: canReadWorkspaces && !!activeTenant && !!run?.workspace_ref,
  })
  const workspace = canReadWorkspaces ? workspaceQuery.data : undefined
  const registeredName =
    workspace?.root_path === run?.workspace_path &&
    workspace?.name !== workspace?.root_path
      ? workspace?.name
      : undefined
  // A registration may name a managed storage directory. Never paint its raw
  // breadcrumb while the name is still being resolved or cannot be read.
  const folder =
    registeredName || (!run?.workspace_ref || workspace ? runFolder(run) : null)
  const parentPath =
    run?.workspace_path?.replace(/[/\\]+[^/\\]+[/\\]*$/, '') ?? ''
  const parent = folderName(parentPath)
  const driver = run?.provider_driver || live?.provider || live?.engine
  // THE SESSION, THEN ITS TOOL AND MODEL. Two panes beside each other must say which
  // session each is. The effort stays the composer's, and a model other than the one
  // asked for is the narrative's warning.
  const tool = driver ? toolName(driver) : null
  const modelRef = run?.usage_model_ref || run?.model_ref
  const editorLink = useEditorLink(
    parentPath || '/',
    folderName(run?.workspace_path) ?? '',
  )
  const accountUsage = useAccountUsageOf(
    live?.provider_profile_ref || run?.provider_profile_ref,
  )
  const marks = useSessionMarks()
  // Money only where Usage opens: the Sessions page's one question.
  const tellsCost = useUsagePath() !== null

  // THE QUIET LINE UNDER THE NAME gives way by the width of ITS PANE, not the window's (a
  // container query): two panes beside each other at 1280 px are 340 px each. The facts
  // keep the drawing's order and give way from its end, the model first, then the tool, so
  // a dot always stands between two facts that show; the folder and the branch stay and
  // are cut. The stacked form shows them all and lets them wrap.
  const stackedFacts = stacked || !!run?.limit?.resets_at
  const facts = (
    <span
      data-slot="thread-facts"
      className={cn(
        'flex min-w-0 items-center text-overline font-normal text-text-2',
        "[&>*+*]:before:mx-1.5 [&>*+*]:before:font-sans [&>*+*]:before:text-text-3 [&>*+*]:before:content-['·']",
        stackedFacts
          ? 'flex-wrap gap-y-1'
          : 'overflow-hidden whitespace-nowrap',
      )}
    >
      {folder ? (
        <span
          data-testid="narrative-folder"
          title={registeredName || run?.workspace_path}
          className="min-w-0 truncate"
        >
          <Folder
            className="me-1 inline size-[13px] align-[-2px] text-text-3"
            aria-hidden="true"
          />
          {registeredName || (parent ? `${parent} / ${folder}` : folder)}
        </span>
      ) : null}
      {run?.worktree_branch ? (
        <span
          data-testid="thread-branch"
          title={run.worktree_branch}
          className="flex min-w-0 items-center font-mono text-mono-s"
        >
          <GitBranch
            className="me-1 inline size-[13px] align-[-2px] text-text-3"
            aria-hidden="true"
          />
          <PathText
            value={run.worktree_branch}
            copy={false}
            tone="muted"
            size="small"
          />
        </span>
      ) : null}
      {tool ? (
        <span
          className={cn(
            'inline-flex min-w-0 items-center gap-1',
            !stackedFacts && 'hidden @min-[26rem]/thread:inline-flex',
          )}
        >
          <ToolMark driver={driver!} />
          <span data-testid="thread-tool" className="min-w-0 truncate">
            {tool}
          </span>
        </span>
      ) : null}
      {modelRef ? (
        <span
          data-testid="thread-model"
          className={cn(
            'min-w-0 truncate',
            !stackedFacts && 'hidden @min-[48rem]/thread:inline',
          )}
        >
          {modelName(modelRef)}
        </span>
      ) : null}
    </span>
  )

  return (
    <header
      data-slot="thread-header"
      className={cn(
        '@container/thread flex shrink-0 items-center border-b border-line px-3',
        stackedFacts ? 'min-h-12 flex-wrap gap-x-2 gap-y-1 py-2' : 'h-12 gap-2',
      )}
    >
      {back}
      <div
        data-slot="thread-title"
        className="flex min-w-0 flex-1 flex-col justify-center"
      >
        <div className="flex min-w-0 items-center gap-2">
          <h2
            title={titleHint}
            className={cn(
              // THE TITLE takes the room the controls leave and is cut there: a width
              // of its own would push it under them in a narrow pane.
              'truncate text-body-l font-bold text-text',
              titleMono && 'font-mono',
            )}
          >
            {title}
          </h2>
          {/* Beside the name, not among the facts: those give way in a narrow pane, and
              which account a thread runs under is what tells two of the same tool
              apart there too. */}
          <AccountBadge
            profileRef={session.profileRef}
            className="max-w-20 shrink-0 min-[761px]:max-w-32"
          />
        </div>
        <div
          className={cn(
            'flex min-w-0 items-center gap-x-2',
            stackedFacts && 'flex-wrap gap-y-1',
          )}
        >
          {facts}
          {/* The account's quota is not the session's: it is the status bar's, and
              Details' "Account usage" (`session-facts.tsx`). Here only a reached limit is
              said, as it stops the turn. */}
          <AtLimit usage={accountUsage} className="min-w-0 truncate" />
          {run ? <LimitNotice run={run} caps={caps} /> : null}
        </div>
      </div>
      <span className="ml-auto flex shrink-0 items-center gap-0.5">
        {folder && run?.workspace_path ? (
          <span
            className={cn(
              'me-1 shrink-0 items-center gap-1',
              !stackedFacts ? 'hidden @min-[40rem]/thread:flex' : 'flex',
            )}
          >
            <SessionGit run={run} variant="init" />
          </span>
        ) : null}
        {/* THE STATE IN WORDS, beside the actions that act on it; a narrow pane keeps its
            dot, named by the word. */}
        <span
          data-slot="thread-state"
          className="me-1.5 hidden items-center gap-1.5 @min-[40rem]/thread:inline-flex"
        >
          <StateWord session={session} />
        </span>
        <StateDot
          session={session}
          className="me-1 @min-[40rem]/thread:hidden"
        />
        {tellsCost && run?.cost_micro_usd ? (
          <span
            data-testid="thread-cost"
            className="me-1 hidden text-caption tabular-nums text-text-2 @min-[40rem]/thread:inline"
          >
            {formatMicroUsd(run.cost_micro_usd)}
          </span>
        ) : null}
        <RunActions
          compact
          run={run}
          caps={caps}
          only={HEADER_ACTIONS}
          onClose={() => {}}
        />
        {contextToggle}
        {contextPaneButton}
        <DropdownMenu>
          <IconTip label={t('narrative.menu', { name: title })}>
            <DropdownMenuTrigger asChild>
              <Button
                variant="ghost"
                size="icon"
                aria-label={t('narrative.menu', { name: title })}
                data-testid="narrative-menu"
              >
                <MoreHorizontal />
              </Button>
            </DropdownMenuTrigger>
          </IconTip>
          <DropdownMenuContent align="end">
            {/* THE MENU PATH, equal to the `p` key the list listens for — and it lives HERE
                because a focusable control inside a `role="option"` row is
                children-presentational in ARIA. The item NAMES what it will do and shows
                the key that does the same thing. */}
            {onTogglePin ? (
              <DropdownMenuItem onClick={() => onTogglePin(address)}>
                {pinned ? <PinOff /> : <Pin />}
                {pinned ? t('rail.unpin') : t('rail.pin')}
                <span className="ml-auto pl-3">
                  <Kbd>p</Kbd>
                </span>
              </DropdownMenuItem>
            ) : null}
            {/* The person's own unread, snooze and done need a run the engine
                still lists; an observed or released session is offered only the way back. */}
            <SessionMarkItems session={session} marks={marks} />
            {onOpenDetail ? (
              <DropdownMenuItem
                onSelect={onOpenDetail}
                data-testid="narrative-open-detail"
              >
                <ArrowUpRight />
                {t('narrative.openDetail')}
              </DropdownMenuItem>
            ) : null}
            {reference ? (
              <DropdownMenuItem
                onSelect={() => {
                  void copyReference(reference).then((ok) => {
                    if (ok) toast.success(t('context.copied'))
                  })
                }}
              >
                {t('thread.copyReference')}
              </DropdownMenuItem>
            ) : null}
            {run?.workspace_path && editorLink ? (
              <DropdownMenuItem asChild>
                <a href={editorLink.href}>
                  <ExternalLink />
                  {editorLink.label}
                </a>
              </DropdownMenuItem>
            ) : null}
            {run ? (
              <DropdownMenuItem onSelect={() => setTerminalOpen(true)}>
                {t('narrative.terminal')}
              </DropdownMenuItem>
            ) : null}
            {onOpenContext ? (
              <DropdownMenuItem
                onSelect={onOpenContext}
                className="min-[761px]:hidden"
              >
                {t('surface.pane.context')}
              </DropdownMenuItem>
            ) : null}
          </DropdownMenuContent>
        </DropdownMenu>
      </span>

      {/* THE SAME IN A TERMINAL (Pomerium concept): the exact olivares commands for this
          session, each with Copy. The id is used because a name can hold spaces. `-o json`
          prints every frame as it came, the protocol the conversation does not draw. */}
      {run ? (
        <Dialog open={terminalOpen} onOpenChange={setTerminalOpen}>
          <DialogContent data-testid="narrative-cli">
            <DialogHeader>
              <DialogTitle>{t('narrative.terminal')}</DialogTitle>
              <DialogDescription>{t('thread.terminalHint')}</DialogDescription>
            </DialogHeader>
            <div className="flex flex-col gap-1.5">
              {terminalCommands(run).map((command) => (
                <CodeLine key={command} command={command} />
              ))}
            </div>
          </DialogContent>
        </Dialog>
      ) : null}
    </header>
  )
}
