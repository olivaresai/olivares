// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE SESSION'S MODE AND WHAT IT HAS SPENT, in Details. The header names the session
// and its state; each tool's plan windows are the status bar's.
//
// ⛔ EVERY FIGURE IS ONE THE ENGINE SENT, AS THE TOOL SAID IT.
//    - The mode is `tool_mode`, the tool's own word (Claude Code's permission mode,
//      Codex's approval policy and sandbox). It is shown in the console's own words
//      for a permission, or in a person's words for a known tool word (see
//      SessionMode); only an absent `tool_mode` shows nothing.
//    - The usage is the run's own counters (an observed session's, its live row's).
//      Absent tokens are unknown, and a cost the tool did not report is said to be
//      unknown, never printed as $0.
import { useQuery } from '@tanstack/react-query'
import { AppLink } from '@/components/ui/app-link'
import { Trans, useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import {
  EDITS_AND_COMMANDS_TEMPLATE,
  PERMISSION_CHOICES,
  PERMISSION_MODE,
} from '@/features/agentops/session-launch'
import type { PermissionChoice } from '@/features/agentops/session-launch'
import type { RunDTO } from '@/features/agentops/types'
import { toolName } from '@/features/agentops/engine-tools'
import { COST_VIEW } from '@/features/registry'
import { useViewAccess } from '@/features/navigation/authorization'
import { viewById } from '@/features/navigation/model'
import { formatMicroUsd, formatTokens } from '@/lib/format'
import { templatesApi, templatesKeys } from '@/features/workspace-templates/api'
import { useTenantStore } from '@/stores/tenant'
import '@/features/first-hour/i18n'
import type { SessionUsage } from './session-usage'
import './i18n'

/** The permission names New session offers, by the permission mode each one launches
 * under (agentops/session-launch.ts: "Edit files and run commands" is the built-in
 * allowlist template, which runs under dontAsk; a custom template may run under it too,
 * so that wording needs the template read, see SessionMode). */
const PERMISSION_WORDS = Object.fromEntries(
  PERMISSION_CHOICES.map((choice) => [
    PERMISSION_MODE[choice] || 'dontAsk',
    choice,
  ]),
) as Record<string, PermissionChoice>

/** Codex joins its approval policy and sandbox with this ("on-request · workspaceWrite"). */
const CODEX_MODE_JOINER = ' · '

/** The tool's own words a person is told in plain words (sessions meter.toolWord): Codex's
 * approval policies (driver_codex.go toolMode). Codex's sandbox word is never said:
 * Olivares confines the session from outside, so that sandbox is not the session's
 * boundary. */
const TOOL_WORDS: Record<string, string> = {
  untrusted: 'untrusted',
  'on-failure': 'onFailure',
  'on-request': 'onRequest',
  never: 'unasked',
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

/** The run's launch template, when its mode is worded from it: dontAsk is the built-in
 * "Edit files and run commands" allowlist, or any template a person wrote under that mode.
 * One query for the Context pane's chip and the composer's access picker. */
function useLaunchTemplate(run: RunDTO | null | undefined) {
  const tenant = useTenantStore((s) => s.activeTenant)
  const choice = run
    ? permissionChoice(
        run.tool_mode || run.permission_mode,
        run.permission_mode,
      )
    : undefined
  const templateId =
    choice === 'editsAndCommands' ? run?.template_id : undefined
  const template = useQuery({
    queryKey: templatesKeys.detail(tenant, templateId ?? ''),
    queryFn: () => templatesApi.get(templateId ?? ''),
    enabled: !!templateId,
    staleTime: 60_000,
  })
  const builtin =
    template.data?.builtin === true &&
    template.data.name === EDITS_AND_COMMANDS_TEMPLATE
  return { choice, templateId, template, builtin }
}

/**
 * The session's access in a few words, for the composer's access picker, as
 * SessionMode words it: the permission New session offers, the run's template read first;
 * a template a person wrote, by its name; else the tool's word said plainly; else the
 * tool's word as it said it. Before a report, use the launch permission; unknown access
 * has no picker label.
 */
export function useAccessWords(run: RunDTO): string {
  const { t } = useTranslation(['sessions', 'firstHour'])
  const { choice, templateId, template, builtin } = useLaunchTemplate(run)
  const toolMode = run.tool_mode ?? ''
  // Nothing until the template answers, as SessionMode: the first frames never show the
  // tool's private name for the mode ("dontAsk").
  if (templateId && template.isPending) return ''
  if (choice && (choice !== 'editsAndCommands' || builtin))
    return t(`firstHour:permission.${choice}`)
  if (templateId && template.data?.name) return template.data.name
  const plain = toolWord(toolMode)
  if (plain === 'unasked') return t('sessions:meter.unaskedShort')
  if (plain) return t(`sessions:meter.toolWord.${plain}`)
  // Never expose an internal launch name when no readable permission is known.
  return toolMode
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
  const access = useViewAccess()
  const toolMode = run?.tool_mode ?? ''
  // The wording is the built-in's, so the run's template is read before the chip claims it.
  const { choice, templateId, template, builtin } = useLaunchTemplate(run)
  if (!run || !toolMode) return null
  const tool = run.provider_driver ? toolName(run.provider_driver) : ''
  // Not worded until the template answers: the chip never flashes the tool's private
  // names, and a template that cannot be read keeps the tool's word, never a guess.
  if (templateId && template.isPending) return null
  const worded =
    choice && (choice !== 'editsAndCommands' || builtin) ? choice : undefined
  const plain = worded ? undefined : toolWord(toolMode)
  // A tool that never asks still runs inside Olivares' confinement and policy: the words
  // say what the person gets, not an alarm, and name the policy that holds it.
  const policyView =
    run.provider_driver === 'claude' ? viewById('claudePolicy') : undefined
  const policy =
    policyView && access.navigable(policyView) ? (
      <AppLink
        href={policyView.path}
        className="rounded-sm underline decoration-text-3 underline-offset-2 hover:decoration-current focus-visible:outline-2 focus-visible:outline-ring"
      />
    ) : (
      <span />
    )
  return (
    <Badge
      variant="outline"
      // A mode is a sentence ("Runs without asking: confined by Olivares policy"): it
      // wraps in the Context pane instead of pushing it sideways.
      className="max-w-full whitespace-normal leading-snug"
      title={t('sessions:meter.modeTitle', {
        tool: tool || t('sessions:meter.theTool'),
        mode: toolMode,
      })}
      data-testid="narrative-mode"
    >
      {worded === 'full' ? (
        // the regression: Full says what the person gets and the boundary that holds it.
        <Trans t={t} i18nKey="sessions:meter.full" components={{ policy }} />
      ) : worded ? (
        // The name the person chose it by, then what it does (the same words Settings lists).
        t('sessions:meter.permission', {
          name: t(`firstHour:permission.${worded}`),
          what: t(`firstHour:permissionSummary.${worded}`),
        })
      ) : plain === 'unasked' ? (
        <Trans t={t} i18nKey="sessions:meter.unasked" components={{ policy }} />
      ) : (
        t('sessions:meter.mode', {
          mode: plain ? t(`sessions:meter.toolWord.${plain}`) : toolMode,
        })
      )}
    </Badge>
  )
}

/** The Usage place's path where it opens for the person, otherwise null. The Sessions
 * page tells money only where Usage opens: a member sees the tokens and no cost. */
export function useUsagePath(): string | null {
  const { navigable } = useViewAccess()
  const usageView = viewById('usage')
  return usageView && navigable(usageView) ? usageView.path : null
}

/** What the session has spent, in Details: the header names the session and its state,
 * and the status bar says each tool's plan windows. The cost, or that none was reported,
 * is told only where Usage opens for the person: a member sees the tokens and no money. */
export function SessionMeter({ usage }: { usage: SessionUsage | null }) {
  const { t, i18n } = useTranslation('sessions')
  const access = useViewAccess()
  const lang = i18n.language
  const tellsCost = useUsagePath() !== null
  const tokens = usage?.input != null && usage.output != null
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
      {usage && (tokens || tellsCost) ? (
        <span
          className="shrink-0 text-overline font-normal tabular-nums text-text-3"
          aria-label={t('meter.usageLabel')}
          data-testid="context-usage"
        >
          {usage.input != null && usage.output != null ? (
            <>
              {t('meter.tokens', {
                input: formatTokens(usage.input, lang),
                output: formatTokens(usage.output, lang),
              })}
              {tellsCost ? ' · ' : null}
            </>
          ) : null}
          {!tellsCost ? null : access.navigable(COST_VIEW) ? (
            <AppLink
              href={COST_VIEW.path}
              aria-label={`${t('meter.viewCosts')}: ${costText}`}
              className="rounded-sm underline decoration-text-3 underline-offset-2 hover:decoration-current focus-visible:outline-2 focus-visible:outline-ring"
            >
              {cost}
            </AppLink>
          ) : (
            cost
          )}
        </span>
      ) : null}
    </>
  )
}
