// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE STATUS BAR (#3161): one thin row under the work that says how things stand, the
// way the editors people already use do. Left to right: the engine (a dot and a word), the
// sessions open, each tool's 5-hour and weekly use as the tool reported it, the agent
// stops while one is active, what Business adds, then Stop all while a session is open and
// the version.
//
// ⛔ STOP ALL IS A SAFETY CONTROL, NOT CHROME. It left the top bar, where it sat on every
//    page whether anything ran or not; it shows here only while a session is open and the
//    person may engage the stop, and its click opens the kill switch's own confirmation,
//    which says how many sessions stop. The palette and the kill switch page keep it
//    within two actions whatever this bar shows.
//
// ⛔ THE VERSION IS THE DEPLOYMENT IDENTITY THE RELEASE GATE READS AT EVERY WIDTH
//    (e2e-release/surface.ts): no width class hides it, and it is absent, never a dash,
//    until the server says it.
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
import { agentToolsApi, agentToolsKeys } from '@/features/agent-tools/api'
import { toolName } from '@/features/agentops/tool-names'
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
import { useServerInfo } from '@/lib/hooks/use-server-info'
import { cn } from '@/lib/utils'
import { useTenantStore } from '@/stores/tenant'
import { EngineStatus } from './engine-status'
import { KillSwitchStatus } from './killswitch-status'
import { useKillSwitchState } from './use-killswitch-state'
import { useSessionRail } from './use-session-rail'

/** The metadata step (#3135): counters and secondary facts, never content. */
const META = 'text-[11px] leading-4'

/** The usage windows the bar names, by the label the engine gives them. */
const WINDOWS = { '5-hour': 'fiveHour', Weekly: 'week' } as const

/** While the engine is still asking a tool, how soon to ask the engine again. */
const REFRESHING_POLL_MS = 10_000

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

/** Each tool's 5-hour and weekly use, as its default login reported it. A tool that
 * reported nothing shows nothing: unknown is never 0%. */
function ProviderUsage() {
  const { t } = useTranslation('nav')
  const { isSuperadmin } = useAuth()
  const tenant = useTenantStore((s) => s.activeTenant)
  // ⛔ EVERY READ OF THE PROVIDERS IS AUDITED (agenttoolsapi handleProviders), so the bar
  //    reads once per page load, asks again only while the engine says a tool is still
  //    answering, and never on a timer or a focus: a status bar must not write the
  //    signed ledger every few minutes for every open tab.
  const providers = useQuery({
    queryKey: [...agentToolsKeys.all, 'providers', tenant, 'background'],
    // The last answer the engine has: the bar never waits on a tool.
    queryFn: ({ signal }) => agentToolsApi.providers(tenant, signal, true),
    // A tenant-scoped read: none before an organization is chosen (the shell sits
    // outside TenantGate, and the engine answers "tenant required").
    enabled: !!isSuperadmin && !!tenant,
    staleTime: Infinity,
    refetchOnWindowFocus: false,
    refetchInterval: (query) =>
      query.state.data?.refreshing ? REFRESHING_POLL_MS : false,
  })
  const tools = (providers.data?.providers ?? [])
    .filter((p) => p.default && p.state === 'ready')
    .map((p) => ({
      driver: p.driver,
      windows: p.limits.flatMap((limit) => {
        const key = WINDOWS[limit.label as keyof typeof WINDOWS]
        return key ? [{ key, percent: Math.round(limit.percent) }] : []
      }),
    }))
    .filter((tool) => tool.windows.length > 0)
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
            <span
              key={w.key}
              className={cn(
                w.percent >= 100
                  ? 'text-bad'
                  : w.percent >= 80
                    ? 'text-warn'
                    : undefined,
              )}
            >
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

/** Stop all, offered while a session is open to whoever may engage the stop. */
function StopAll({ running }: { running: number }) {
  const { t } = useTranslation('nav')
  const { listed } = useViewAccess()
  const { authorized } = useCommandActionAuthority()
  const { posture } = useKillSwitchState()
  const [open, setOpen] = useState(false)
  const [asked, setAsked] = useState(false)
  const view = viewById('killswitch')
  const action = commandActionOf('killswitch', 'engage')
  const offered =
    running > 0 &&
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
  const version = useServerInfo().data?.version
  const { running } = useSessionRail().sessionCounts
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
      {/* What can give way on a narrow screen is on the left; Stop all and the version
          on the right never do. */}
      <span className="flex min-w-0 flex-1 items-center gap-3 overflow-hidden">
        <EngineStatus
          className={cn(META, 'flex-none gap-1.5 rounded-none p-0')}
        />
        {running ? (
          <Link
            to={'/sessions' as never}
            className="shrink-0 rounded-ctl tabular-nums outline-none hover:text-text focus-visible:ring-2 focus-visible:ring-focus"
          >
            {t('shell.status.running', { count: running })}
          </Link>
        ) : null}
        <ProviderUsage />
        <KillSwitchStatus />
        {additions.map(({ id, Component }) => (
          <Component key={id} />
        ))}
      </span>
      <span className="flex shrink-0 items-center gap-3">
        <StopAll running={running} />
        {version ? (
          <p
            data-testid="deployment-identity"
            className="flex shrink-0 tabular-nums text-text-3"
          >
            <span className="sr-only">{t('shell.status.version')} </span>
            <span>{version}</span>
          </p>
        ) : null}
      </span>
    </footer>
  )
}
