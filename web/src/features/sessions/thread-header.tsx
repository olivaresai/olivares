// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE HEAD OF A THREAD: ONE 48 px LINE.
//
// The state as a dot, the session's name, the folder it works in, the tool, the model and
// its effort, what it has spent; on the right the lifecycle controls as icons (Interrupt only while a
// turn runs, Stop), the Context toggle and a `⋯` menu with the rest (Full controls, Copy
// reference, In a terminal, and the pin). It used to be a 280 px block that said the state
// five times.
//
// ⛔ WHAT THE LINE DOES NOT SAY IS NOT LOST: the badges (who manages the session, its mode,
//    the live stream) and the working folder's path are the Context pane's, in plain
//    words (`session-overview.tsx`). Paths, ids and sandbox names are never in a header.
import {
  AlarmClock,
  AlarmClockOff,
  ArrowUpRight,
  CircleCheck,
  CircleDashed,
  MoreHorizontal,
  Pin,
  PinOff,
} from 'lucide-react'
import { useState, type ReactNode } from 'react'
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
import { toast } from '@/components/ui/toaster'
import { toolName } from '@/features/agentops/tool-names'
import type { RunDTO } from '@/features/agentops/types'
import { cn } from '@/lib/utils'
import { runFolder } from './folder'
import type { Capability, UnifiedSession } from './provenance'
import { hostIsolationUnavailable } from './provenance'
import { IconTip } from './icon-tip'
import { RunActions } from './run-actions'
import { isMarkedDone, isSnoozed } from './session-groups'
import { LimitNotice } from './session-limit'
import { StateDot } from './session-state-dot'
import { SessionMeter } from './session-meter'
import { sessionUsage } from './session-usage'
import { markableRun, useSessionMarks } from './use-session-marks'
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

const HOUR_MS = 3_600_000

/** The moments a snooze offers, from now (#2480). The CLI takes any duration or time. */
const SNOOZE_FOR = [
  { label: 'rail.snoozeHour', ms: HOUR_MS },
  { label: 'rail.snoozeDay', ms: 24 * HOUR_MS },
  { label: 'rail.snoozeWeek', ms: 7 * 24 * HOUR_MS },
] as const

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
   * The sheet's form (#2549): the name and the controls on the first line and every fact
   * on a second one, at any width. A sheet is narrower than the viewport the one-line
   * form's breakpoints read, so there the facts would crowd the name out.
   */
  stacked?: boolean
}) {
  const { t } = useTranslation('sessions')
  const [terminalOpen, setTerminalOpen] = useState(false)
  const folder = runFolder(run)
  const driver = run?.provider_driver || live?.provider || live?.engine
  // THE SESSION, THEN ITS TOOL (dev#3120). Two panes beside each other must say which
  // session each is, so the header is the name and the tool; the model and the effort
  // are said once per pane, by the composer's picker, and a model other than the one
  // asked for is the narrative's warning.
  const tool = driver ? toolName(driver) : null
  const usage = sessionUsage(session.runs, live)
  const marks = useSessionMarks()
  // The person's own snooze and done (#2480) need a run the engine still lists; an observed
  // session or a released one is not offered them.
  const markable = !!markableRun(session)
  const snoozed = isSnoozed(session)
  const markedDone = isMarkedDone(session)

  // The one-line form gives way by the width of ITS PANE, not the window's (a container
  // query): two panes beside each other at 1280 px are 340 px each, and there the name
  // and the controls stay while the usage, the folder and then the tool step aside
  // (dev#3120). The stacked form shows them all, on their own line.
  const facts = (
    <>
      {tool ? (
        <span
          data-testid="thread-tool"
          className={cn(
            'min-w-0 truncate text-caption text-text-2',
            !stacked && 'hidden @min-[26rem]/thread:inline',
          )}
        >
          {tool}
        </span>
      ) : null}
      {folder ? (
        <span
          data-testid="narrative-folder"
          title={run?.workspace_path}
          className={cn(
            'max-w-40 shrink-0 truncate rounded-[6px] bg-muted px-1.5 py-0.5 text-overline font-normal text-text-2',
            !stacked && 'hidden @min-[40rem]/thread:inline-block',
          )}
        >
          {folder}
        </span>
      ) : null}
      {run ? <LimitNotice run={run} caps={caps} /> : null}
      <span
        className={cn(
          'min-w-0 items-center gap-2 truncate',
          stacked ? 'flex' : 'hidden flex-1 @min-[48rem]/thread:flex',
        )}
      >
        <SessionMeter run={run} usage={usage} />
      </span>
    </>
  )

  return (
    <header
      data-slot="thread-header"
      className={cn(
        '@container/thread flex shrink-0 items-center border-b border-line px-3',
        stacked ? 'min-h-12 flex-wrap gap-x-2 gap-y-1 py-2' : 'h-12 gap-2',
      )}
    >
      {back}
      <StateDot session={session} />
      <h2
        title={titleHint}
        className={cn(
          'min-w-0 truncate text-body-l font-semibold text-text',
          // A zero basis keeps a long name on the controls' line, cut, instead of
          // wrapping the controls under it.
          stacked && 'flex-1 basis-0',
          titleMono && 'font-mono',
        )}
      >
        {title}
      </h2>
      {stacked ? (
        <div
          data-slot="thread-facts"
          className="order-last flex min-w-0 basis-full flex-wrap items-center gap-x-2 gap-y-1"
        >
          {facts}
        </div>
      ) : (
        facts
      )}
      <span className="ml-auto flex shrink-0 items-center gap-0.5">
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
            {snoozed ? (
              <DropdownMenuItem onSelect={() => marks.unsnooze(session)}>
                <AlarmClockOff />
                {t('rail.unsnooze')}
              </DropdownMenuItem>
            ) : markable ? (
              // Three plain items, not a submenu: no hover to hold, the same on a touch
              // screen, and each one names the whole action.
              SNOOZE_FOR.map(({ label, ms }) => (
                <DropdownMenuItem
                  key={label}
                  onSelect={() => marks.snooze(session, ms)}
                >
                  <AlarmClock />
                  {t(label)}
                </DropdownMenuItem>
              ))
            ) : null}
            {markedDone || markable ? (
              <DropdownMenuItem
                onSelect={() => marks.setDone(session, !markedDone)}
              >
                {markedDone ? <CircleDashed /> : <CircleCheck />}
                {markedDone ? t('rail.markNotDone') : t('rail.markDone')}
              </DropdownMenuItem>
            ) : null}
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
