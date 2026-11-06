// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE STATUS BAR (#3161): one thin row under the work that says how things stand, the
// way the editors people already use do. Left to right: the engine (a dot and a word), the
// sessions open, each tool's 5-hour and weekly use as the tool reported it, the agent
// stops while one is active, what Business adds, then Stop all while a session works or waits
// for a person. No version: Settings › About and the account menu carry it.
//
// ⛔ STOP ALL IS A SAFETY CONTROL, NOT CHROME. It left the top bar, where it sat on every
//    page whether anything ran or not; it shows here only while a session works or waits
//    for a person, snoozed or archived too (an idle session left open does not keep it on
//    screen), and the person may engage the stop. Its click opens the kill switch's own
//    confirmation, which says how many sessions stop: every one alive, the idle ones too.
//    The palette and the kill switch page keep it within two actions whatever this bar
//    shows.
import { useQuery } from '@tanstack/react-query'
import { Link, useNavigate } from '@tanstack/react-router'
import { OctagonX } from 'lucide-react'
import {
  type ComponentProps,
  type ComponentType,
  lazy,
  Suspense,
  useEffect,
  useState,
} from 'react'
import { useTranslation } from 'react-i18next'
import {
  USAGE_WINDOWS,
  useBackgroundProviders,
  windowTone,
} from '@/features/agent-tools/use-background-providers'
import { engineToolsQuery } from '@/features/agentops/api'
import { toolName } from '@/features/agentops/engine-tools'
import { PanelSlot } from '@/components/ui/trust-chrome'
import type * as EngageCard from '@/features/killswitch/engage-card'
import { PANEL_EXTENSIONS } from '@/features/extensions'
import { useOfferedPanels } from '@/features/panels'
import { useViewAccess } from '@/features/navigation/authorization'
import {
  commandActionOf,
  useCommandActionAuthority,
} from '@/features/navigation/command-actions'
import { viewById } from '@/features/navigation/model'
import { useAuth } from '@/lib/auth/context'
import { cn } from '@/lib/utils'
import { EngineStatus } from './engine-status'
import { KillSwitchStatus } from './killswitch-status'
import { useKillSwitchState } from './use-killswitch-state'
import { useSessionRail } from './use-session-rail'

/** The metadata step (#3135): counters and secondary facts, never content. */
const META = 'text-metadata'

const loadConfirm = () => import('@/features/killswitch/engage-card')

type ConfirmProps = ComponentProps<typeof EngageCard.StopAllConfirm>

/** The kill switch's confirmation, loaded once Stop all is offered: the shell does not
 * carry the kill switch's dictionary until a session is open. A chunk that cannot load (a
 * tab left open across an upgrade) opens the kill switch page instead, whose own Stop
 * all is there: an emergency control never does nothing. */
const StopAllConfirm = lazy<ComponentType<ConfirmProps>>(() =>
  loadConfirm().then(
    (m) => ({ default: m.StopAllConfirm }),
    () => ({ default: OpenKillSwitch }),
  ),
)

function OpenKillSwitch() {
  const navigate = useNavigate()
  useEffect(() => {
    void navigate({ to: '/killswitch' as never })
  }, [navigate])
  return null
}

/** Each tool's 5-hour and weekly use, as its default login reported it, else as the
 * first of its logins that reported any (the account quota the session header no longer
 * repeats). A tool that reported nothing shows nothing: unknown is never 0%. */
function ProviderUsage() {
  const { t } = useTranslation('nav')
  const providers = useBackgroundProviders()
  const reported = (providers.data?.providers ?? [])
    .filter((p) => p.state === 'ready')
    .map((p) => ({
      driver: p.driver,
      isDefault: p.default,
      windows: p.limits.flatMap((limit) => {
        const key = USAGE_WINDOWS[limit.label as keyof typeof USAGE_WINDOWS]
        return key ? [{ key, percent: Math.round(limit.percent) }] : []
      }),
    }))
    .filter((login) => login.windows.length > 0)
  const tools = [...new Set(reported.map((login) => login.driver))].map(
    (driver) =>
      reported.find((l) => l.driver === driver && l.isDefault) ??
      reported.find((l) => l.driver === driver)!,
  )
  if (tools.length === 0) return null
  return (
    <>
      {tools.map((tool) => (
        <span
          key={tool.driver}
          role="group"
          aria-label={t('shell.status.usage', { tool: toolName(tool.driver) })}
          title={toolName(tool.driver)}
          className="flex shrink-0 items-center gap-1.5 tabular-nums max-[760px]:hidden"
        >
          <span className="text-text-3">{toolName(tool.driver)}</span>
          {tool.windows.map((w, i) => (
            <span key={w.key} className={windowTone(w.percent)}>
              {i > 0 ? (
                <span aria-hidden className="mr-1.5 text-text-3">
                  ·
                </span>
              ) : null}
              {w.percent}% {t(`shell.status.${w.key}`)}
            </span>
          ))}
        </span>
      ))}
    </>
  )
}

/** The engine's tool table could not be read (engine-tools.ts): tools are named by their
 * keys and none is offered until it is. Says so, with the engine's reason on the hover,
 * and asks again. */
function ToolsUnreadable() {
  const { t } = useTranslation('nav')
  const { status, activeTenant } = useAuth()
  const tools = useQuery(
    engineToolsQuery(status === 'authenticated' ? activeTenant : null),
  )
  if (!tools.isError) return null
  return (
    <button
      type="button"
      title={tools.error.message}
      onClick={() => void tools.refetch()}
      className="shrink-0 rounded-ctl text-bad outline-none hover:text-text focus-visible:ring-2 focus-visible:ring-focus"
    >
      {t('shell.status.toolsUnreadable')}
    </button>
  )
}

/** Stop all, offered while a session works or waits for a person to whoever may engage the
 * stop; its confirmation counts every session it ends (`running`). */
function StopAll({ active, running }: { active: number; running: number }) {
  const { t } = useTranslation('nav')
  const { listed } = useViewAccess()
  const { authorized } = useCommandActionAuthority()
  const { posture } = useKillSwitchState()
  const [open, setOpen] = useState(false)
  const [asked, setAsked] = useState(false)
  const view = viewById('killswitch')
  const action = commandActionOf('killswitch', 'engage')
  const offered =
    active > 0 &&
    !!view &&
    !!action &&
    listed(view) &&
    authorized(action) &&
    !posture?.estate
  // The confirmation is fetched as soon as it may be asked for, not on the click.
  useEffect(() => {
    if (offered) void loadConfirm().catch(() => undefined)
  }, [offered])
  // A confirmation already open stays open when the last session ends under it.
  if (!offered && !open) return null
  return (
    <>
      {offered ? (
        <button
          type="button"
          aria-haspopup="dialog"
          onClick={() => {
            setAsked(true)
            setOpen(true)
          }}
          className={cn(
            META,
            'inline-flex h-5 shrink-0 items-center gap-1 rounded-ctl px-1.5 font-medium text-text-2 outline-none hover:bg-bad-soft hover:text-bad focus-visible:ring-2 focus-visible:ring-focus',
          )}
        >
          <OctagonX aria-hidden className="size-3 text-bad" />
          {t('shell.stopAll')}
        </button>
      ) : null}
      {asked ? (
        <Suspense fallback={null}>
          <StopAllConfirm
            open={open}
            onOpenChange={setOpen}
            running={running}
          />
        </Suspense>
      ) : null}
    </>
  )
}

export function StatusBar() {
  const { t } = useTranslation('nav')
  // The total the list shows; Stop all shows while one works or waits for a person, and
  // counts every session it would end, idle and archived too.
  const { active, running, open } = useSessionRail().sessionCounts
  const additions = useOfferedPanels(PANEL_EXTENSIONS.statusBarItems ?? [])
  return (
    <footer
      aria-label={t('shell.status.label')}
      data-slot="status-bar"
      className={cn(
        META,
        'flex h-6 min-h-6 shrink-0 items-center gap-3 border-t border-line bg-canvas px-3 text-text-2 print:hidden',
      )}
    >
      {/* What can give way on a narrow screen is on the left; Stop all on the right
          never does. */}
      <span className="flex min-w-0 flex-1 items-center gap-3 overflow-hidden">
        <EngineStatus
          className={cn(META, 'flex-none gap-1.5 rounded-none p-0')}
        />
        {open ? (
          <Link
            to={'/sessions' as never}
            className="shrink-0 rounded-ctl tabular-nums outline-none hover:text-text focus-visible:ring-2 focus-visible:ring-focus"
          >
            {t('shell.status.running', { count: open })}
          </Link>
        ) : null}
        <ToolsUnreadable />
        <ProviderUsage />
        <KillSwitchStatus />
        {additions.map(({ id, Component }) => (
          <PanelSlot key={id} className="flex items-center">
            <Component />
          </PanelSlot>
        ))}
      </span>
      <span className="flex shrink-0 items-center gap-3">
        <StopAll active={active} running={running} />
      </span>
    </footer>
  )
}
