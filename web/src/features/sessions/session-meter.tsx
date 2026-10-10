// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE SESSION'S MODE AND WHAT IT HAS SPENT, on the session header (#427).
//
// ⛔ EVERY FIGURE IS ONE THE ENGINE SENT, AS THE TOOL SAID IT.
//    - The mode is `tool_mode`: the tool's own word (Claude Code's permission mode,
//      Codex's approval policy and sandbox), never `permission_mode`, which is only
//      what the launch asked for. Absent, nothing is shown.
//    - The usage is the run's own counters (an observed session's, its live row's).
//      Absent tokens are unknown, and a cost the tool did not report is said to be
//      unknown, never printed as $0.
//    - The plan window is the AI tools instance's own usage windows (GET
//      /agenttools/providers), only for a run on the organization's own login of
//      its tool, and only for a viewer who may read that page.
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { agentToolsApi, agentToolsKeys } from '@/features/agent-tools/api'
import type { RunDTO } from '@/features/agentops/types'
import { toolName } from '@/features/agentops/tool-names'
import { useAuth } from '@/lib/auth/context'
import {
  formatDateTime,
  formatMicroUsd,
  formatPercent,
  formatRelativeTime,
  formatTokens,
} from '@/lib/format'
import { useTenantStore } from '@/stores/tenant'
import type { SessionUsage } from './session-usage'
import './i18n'

/**
 * The tool's own mode, as it reported it. It is a fact for the Context pane: the thread's
 * header is one line and does not carry it.
 */
export function SessionMode({ run }: { run: RunDTO | null | undefined }) {
  const { t } = useTranslation('sessions')
  const tool = run?.provider_driver ? toolName(run.provider_driver) : ''
  if (!run?.tool_mode) return null
  return (
    <Badge
      variant="outline"
      title={t('meter.modeTitle', { tool: tool || t('meter.theTool') })}
      data-testid="narrative-mode"
    >
      {t('meter.mode', { mode: run.tool_mode })}
    </Badge>
  )
}

/** What the session has spent and the plan window of its login, for the thread's header. */
export function SessionMeter({
  run,
  usage,
  where = 'header',
}: {
  run: RunDTO | null | undefined
  usage: SessionUsage | null
  /**
   * The thread's header paints usage and the plan window beside the name; the Context pane
   * paints the usage alone, so it is still there where the header has no room.
   */
  where?: 'header' | 'context'
}) {
  const { t, i18n } = useTranslation('sessions')
  const lang = i18n.language
  return (
    <>
      {usage ? (
        <span
          className="shrink-0 text-overline font-normal tabular-nums text-text-3"
          aria-label={t('meter.usageLabel')}
          data-testid={where === 'header' ? 'narrative-usage' : 'context-usage'}
        >
          {t('meter.tokens', {
            input: formatTokens(usage.input, lang),
            output: formatTokens(usage.output, lang),
          })}
          {' · '}
          {usage.costMicroUsd != null ? (
            <span className="text-foreground" title={t('meter.costTitle')}>
              {formatMicroUsd(usage.costMicroUsd, { locale: lang })}
            </span>
          ) : (
            <span>{t('meter.costUnknown')}</span>
          )}
        </span>
      ) : null}
      {where === 'header' && run?.provider_instance ? (
        <PlanWindow instance={run.provider_instance} />
      ) : null}
    </>
  )
}

/** The plan's usage windows of the tool login this session spends. */
function PlanWindow({ instance }: { instance: string }) {
  const { t } = useTranslation('sessions')
  const { isSuperadmin } = useAuth()
  const tenant = useTenantStore((s) => s.activeTenant)
  const providers = useQuery({
    queryKey: [...agentToolsKeys.all, 'providers', tenant],
    queryFn: ({ signal }) => agentToolsApi.providers(tenant, signal),
    // The providers page reads the same snapshot; the engine probes at most once a minute.
    enabled: !!isSuperadmin,
    staleTime: 60_000,
  })
  if (!isSuperadmin || providers.isPending) return null
  const snap = providers.isError
    ? undefined
    : providers.data.providers.find((p) => p.instance === instance)
  // A read that failed, an instance the snapshot does not list, and a login the
  // tool reports as not ready all leave the window unknown: said, not left blank.
  if (!snap || snap.state !== 'ready')
    return (
      <span
        className="text-caption text-muted-foreground"
        title={
          providers.error instanceof Error
            ? providers.error.message
            : (snap?.error ?? snap?.state)
        }
        data-testid="narrative-plan-error"
      >
        {t('meter.planNotRead')}
      </span>
    )
  if (snap.limits.length === 0) return null
  return (
    <span
      className="flex flex-wrap items-center gap-2 text-caption text-muted-foreground"
      aria-label={t('meter.planLabel')}
      data-testid="narrative-plan"
    >
      {snap.limits.map((limit, i) => (
        <span
          key={`${limit.label}-${i}`}
          title={
            limit.resets_at
              ? t('meter.resets', {
                  when: formatRelativeTime(limit.resets_at),
                  at: formatDateTime(limit.resets_at),
                })
              : undefined
          }
        >
          {limit.label}{' '}
          <span className="font-mono tabular-nums text-foreground">
            {formatPercent(limit.percent)}
          </span>
        </span>
      ))}
      {snap.stale ? (
        <span
          title={t('meter.readAt', { at: formatDateTime(snap.checked_at) })}
        >
          {t('meter.stale')}
        </span>
      ) : null}
    </span>
  )
}
