// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE SESSION'S MODE AND WHAT IT HAS SPENT, on the session header (#427).
//
// ⛔ EVERY FIGURE IS ONE THE ENGINE SENT, AS THE TOOL SAID IT.
//    - The mode is `tool_mode`, the tool's own word (Claude Code's permission mode,
//      Codex's approval policy and sandbox). It is shown in the console's own words
//      for a permission, or in a person's words for a known tool word (see
//      SessionMode); only an absent `tool_mode` shows nothing.
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
import { EDITS_AND_COMMANDS_TEMPLATE } from '@/features/agentops/session-launch'
import type { PermissionChoice } from '@/features/agentops/session-launch'
import type { RunDTO } from '@/features/agentops/types'
import { toolName } from '@/features/agentops/tool-names'
import { useAuth } from '@/lib/auth/context'
import { COST_VIEW } from '@/features/registry'
import { useViewAccess } from '@/features/navigation/authorization'
import {
  formatDateTime,
  formatMicroUsd,
  formatPercent,
  formatRelativeTime,
  formatTokens,
} from '@/lib/format'
import { templatesApi, templatesKeys } from '@/features/workspace-templates/api'
import { useTenantStore } from '@/stores/tenant'
import '@/features/first-hour/i18n'
import type { SessionUsage } from './session-usage'
import './i18n'

/** The permission names New session offers, by the permission mode each one launches
 * under (agentops/session-launch.ts: "Edit files and run commands" is the built-in
 * allowlist template, which runs under dontAsk; a custom template may run under it too,
 * so that wording needs the template read, see SessionMode). */
const PERMISSION_WORDS: Record<string, PermissionChoice> = {
  dontAsk: 'editsAndCommands',
  acceptEdits: 'editsOnly',
  plan: 'readOnly',
  default: 'ask',
}

/** Codex joins its approval policy and sandbox with this ("on-request · workspaceWrite"). */
const CODEX_MODE_JOINER = ' · '

/** The tool's own words a person is told in plain words (sessions meter.toolWord): Claude
 * Code's bypassPermissions and Codex's approval policies (driver_codex.go toolMode). Codex's
 * sandbox word is never said: Olivares confines the session from outside, so that sandbox
 * is not the session's boundary. */
const TOOL_WORDS: Record<string, string> = {
  bypassPermissions: 'bypassPermissions',
  untrusted: 'untrusted',
  'on-failure': 'onFailure',
  'on-request': 'onRequest',
  never: 'never',
  granular: 'granular',
}

/** The plain-words key of a run's mode, or undefined to show the tool's word as it said it. */
function toolWord(toolMode: string): string | undefined {
  const word = toolMode.split(CODEX_MODE_JOINER)[0]
  return Object.hasOwn(TOOL_WORDS, word) ? TOOL_WORDS[word] : undefined
}

/**
 * The New session permission a run's mode is worded as, or undefined to show the tool's
 * word as it said it. A tool's own single word (Claude Code's permission mode) is worded
 * only from itself, so a session running wider than it was launched is never worded as
 * the launch asked. Codex's composite word names its private sandbox, which Olivares
 * confines from outside, so the permission Olivares enforced for the launch is the one
 * the session has.
 */
function permissionChoice(
  toolMode: string,
  launchMode: string,
): PermissionChoice | undefined {
  const word = toolMode.includes(CODEX_MODE_JOINER) ? launchMode : toolMode
  return Object.hasOwn(PERMISSION_WORDS, word)
    ? PERMISSION_WORDS[word]
    : undefined
}

/**
 * The session's permission in the words New session offers, with the tool's own word for
 * it one hover away. A tool's mode that is one of those permission modes (Claude Code's)
 * is worded as the console words it; Codex's approval policy and sandbox are the tool's
 * private names, so the permission the launch asked for is worded instead. Without a
 * permission to word, a known tool word is said plainly (TOOL_WORDS); any other mode stays
 * as the tool said it, never guessed.
 */
export function SessionMode({ run }: { run: RunDTO | null | undefined }) {
  const { t } = useTranslation(['sessions', 'firstHour'])
  const tenant = useTenantStore((s) => s.activeTenant)
  const toolMode = run?.tool_mode ?? ''
  const choice = run
    ? permissionChoice(toolMode, run.permission_mode)
    : undefined
  // dontAsk is the built-in "Edit files and run commands" allowlist, or any template a
  // person wrote under that mode: the wording is the built-in's, so the run's template
  // is read before the chip claims it.
  const templateId =
    choice === 'editsAndCommands' ? run?.template_id : undefined
  const template = useQuery({
    queryKey: templatesKeys.detail(tenant, templateId ?? ''),
    queryFn: () => templatesApi.get(templateId ?? ''),
    enabled: !!templateId,
    staleTime: 60_000,
  })
  if (!run || !toolMode) return null
  const tool = run.provider_driver ? toolName(run.provider_driver) : ''
  const builtin =
    template.data?.builtin === true &&
    template.data.name === EDITS_AND_COMMANDS_TEMPLATE
  // Not worded until the template answers: the chip never flashes the tool's private
  // names, and a template that cannot be read keeps the tool's word, never a guess.
  if (templateId && template.isPending) return null
  const worded =
    choice && (choice !== 'editsAndCommands' || builtin) ? choice : undefined
  const plain = worded ? undefined : toolWord(toolMode)
  return (
    <Badge
      variant="outline"
      title={t('sessions:meter.modeTitle', {
        tool: tool || t('sessions:meter.theTool'),
        mode: toolMode,
      })}
      data-testid="narrative-mode"
    >
      {worded
        ? t(`firstHour:permission.${worded}`)
        : t('sessions:meter.mode', {
            mode: plain ? t(`sessions:meter.toolWord.${plain}`) : toolMode,
          })}
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
  const access = useViewAccess()
  const lang = i18n.language
  const amount =
    usage?.costMicroUsd != null
      ? formatMicroUsd(usage.costMicroUsd, { locale: lang })
      : t('meter.costUnknown')
  const costText = usage?.costPartial
    ? t('meter.costPartial', { cost: amount })
    : amount
  const cost = (
    <span
      className={usage?.costMicroUsd != null ? 'text-foreground' : undefined}
      title={usage?.costMicroUsd != null ? t('meter.costTitle') : undefined}
    >
      {costText}
    </span>
  )
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
          {access.navigable(COST_VIEW) ? (
            <a
              href={COST_VIEW.path}
              aria-label={`${t('meter.viewCosts')}: ${costText}`}
              className="rounded-sm underline decoration-text-3 underline-offset-2 hover:decoration-current focus-visible:outline-2 focus-visible:outline-ring"
            >
              {cost}
            </a>
          ) : (
            cost
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
