// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE HEAD OF A THREAD: ONE 48 px LINE.
//
// The state as a dot, the session's name, the folder it works in, the tool and the model,
// what it has spent; on the right the lifecycle controls as icons (Interrupt only while a
// turn runs, Stop), the Context toggle and a `⋯` menu with the rest (Full controls, Copy
// reference, In a terminal, and the pin). It used to be a 280 px block that said the state
// five times.
//
// ⛔ WHAT THE LINE DOES NOT SAY IS NOT LOST: the badges (who manages the session, its mode,
//    the live stream) and the working folder's path are the Context pane's, in plain
//    words (`session-overview.tsx`). Paths, ids and sandbox names are never in a header.
import { ArrowUpRight, MoreHorizontal, Pin, PinOff } from 'lucide-react'
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
import { IconTip } from './icon-tip'
import { RunActions } from './run-actions'
import { StateDot } from './session-state-dot'
import { SessionMeter } from './session-meter'
import { sessionUsage } from './session-usage'
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
    `olivares session send ${run.run_ref} "…"`,
    `olivares session ${live ? 'stop' : 'resume'} ${run.run_ref}`,
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
  /** The full session controls — attach, drive, stop, governance — live in the card. */
  onOpenDetail: () => void
  /** Brings the Context pane forward where it is a pane of its own (a phone). */
  onOpenContext?: () => void
  /** The way back to the list, painted below 761 px only. */
  back?: ReactNode
  /** The Context icon toggle, painted from `xl` up. */
  contextToggle?: ReactNode
  /** The Context icon that opens the pane in front, painted between 761 px and `xl`. */
  contextPaneButton?: ReactNode
}) {
  const { t } = useTranslation('sessions')
  const [terminalOpen, setTerminalOpen] = useState(false)
  const folder = runFolder(run)
  const driver = run?.provider_driver || live?.provider || live?.engine
  const tool = driver ? toolName(driver) : null
  const model = run?.model_ref || run?.usage_model_ref || live?.model_ref
  const toolModel = [tool, model].filter(Boolean).join(' · ')
  const usage = sessionUsage(run, live)

  return (
    <header
      data-slot="thread-header"
      className="flex h-12 shrink-0 items-center gap-2 border-b border-line px-3"
    >
      {back}
      <StateDot session={session} />
      <h2
        title={titleHint}
        className={cn(
          'min-w-0 truncate text-body-l font-semibold text-text',
          titleMono && 'font-mono',
        )}
      >
        {title}
      </h2>
      {folder ? (
        <span
          data-testid="narrative-folder"
          title={run?.workspace_path}
          className="hidden max-w-40 shrink-0 truncate rounded-[6px] bg-muted px-1.5 py-0.5 text-overline font-normal text-text-2 min-[761px]:inline-block"
        >
          {folder}
        </span>
      ) : null}
      {toolModel ? (
        <span
          data-testid="thread-tool-model"
          title={toolModel}
          className="hidden min-w-0 shrink-0 truncate text-caption text-text-2 min-[761px]:inline"
        >
          {toolModel}
        </span>
      ) : null}
      <span className="hidden min-w-0 flex-1 items-center gap-2 truncate min-[1100px]:flex">
        <SessionMeter run={run} usage={usage} />
      </span>
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
            <DropdownMenuItem
              onSelect={onOpenDetail}
              data-testid="narrative-open-detail"
            >
              <ArrowUpRight />
              {t('narrative.openDetail')}
            </DropdownMenuItem>
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
