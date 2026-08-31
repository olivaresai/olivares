// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { Link } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import {
  Coins,
  Disc3,
  ExternalLink,
  OctagonAlert,
  ScrollText,
  ShieldCheck,
  ShieldX,
  UserCheck,
  type LucideIcon,
} from 'lucide-react'
import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { governanceApi } from '@/features/governance/api'
import { killswitchApi, killswitchKeys } from '@/features/killswitch/api'
import { useAuth } from '@/lib/auth/context'
import { cn } from '@/lib/utils'
import type { RunDTO } from './types'
import './i18n'

type Tone = 'ok' | 'warn' | 'danger' | 'muted'

/**
 * GovernancePanel — the per-session governance posture (the FASE V differentiator made
 * visible). PEP / recording / CRITICAL come from the run's persisted launch facts; the
 * LIVE kill-switch and HITL approval status are composed from the existing governance
 * read APIs, keyed by the run's agent_ref / approval_ref. Each live signal is gated on
 * its own read permission and DEGRADES HONESTLY ("no permission" / a deep-link) rather
 * than faking a value — never a green check it cannot prove.
 */
export function GovernancePanel({
  run,
  onViewEvidence,
}: {
  run: RunDTO
  onViewEvidence?: () => void
}) {
  const { t } = useTranslation('agentops')
  const { activeTenant, can } = useAuth()
  const isRemote = run.transport === 'remote-control'

  const canKill = can('governance:killswitch:read')
  const ksQuery = useQuery({
    queryKey: killswitchKeys.state(activeTenant),
    queryFn: () => killswitchApi.state(),
    enabled: canKill,
    refetchInterval: 15_000,
  })

  const canApproval = can('governance:approval:read')
  const approvalQuery = useQuery({
    queryKey: ['agentops', activeTenant, 'approval', run.approval_ref] as const,
    queryFn: () => governanceApi.getApproval(run.approval_ref as string),
    enabled: canApproval && !!run.approval_ref,
    refetchInterval: 15_000,
  })

  // --- PEP (inline tool-call governance) ----------------------------------------
  const pep: { text: string; tone: Tone } = isRemote
    ? { text: t('gov.pepRemote'), tone: 'muted' }
    : run.pep_provisioned
      ? { text: t('gov.pepProvisioned'), tone: 'ok' }
      : { text: t('gov.pepDenyClosed'), tone: 'warn' }

  // --- Kill switch (live) -------------------------------------------------------
  // Compute {text,tone} TOGETHER so a degraded read never renders a green "clear" it
  // cannot prove. Deny-closed: an UNREADABLE stop state is treated as
  // not-provably-clear (danger), never as "ok" — the kill-switch is the safety control.
  const ks = ksQuery.data
  const agentStopped =
    !!run.agent_ref &&
    !!ks?.active?.some(
      (s) => s.scope_kind === 'agent' && s.scope_ref === run.agent_ref,
    )
  const killEngaged = !!ks?.estate_stopped || agentStopped
  const killScope = ks?.estate_stopped
    ? t('gov.killScopeEstate')
    : agentStopped
      ? t('gov.killScopeAgent', { ref: run.agent_ref })
      : ''
  const kill: { text: string; tone: Tone } = !canKill
    ? { text: t('gov.noPermission'), tone: 'muted' }
    : ksQuery.isLoading
      ? { text: t('gov.loading'), tone: 'muted' }
      : ksQuery.isError
        ? { text: t('gov.killUnreadable'), tone: 'danger' }
        : killEngaged
          ? { text: `${t('gov.killEngaged')} · ${killScope}`, tone: 'danger' }
          : { text: t('gov.killClear'), tone: 'ok' }

  // --- HITL approval (live) -----------------------------------------------------
  const approvalStatus = approvalQuery.data?.status
  const hitl: { text: string; tone: Tone } = !run.approval_ref
    ? { text: t('gov.hitlNone'), tone: 'muted' }
    : !canApproval
      ? { text: t('gov.noPermission'), tone: 'muted' }
      : approvalQuery.isLoading
        ? { text: t('gov.loading'), tone: 'muted' }
        : hitlFromStatus(approvalStatus, t)

  return (
    <div className="flex flex-col gap-2">
      <div>
        <h3 className="text-sm font-semibold text-foreground">
          {t('gov.title')}
        </h3>
        <p className="text-xs text-muted-foreground">{t('gov.subtitle')}</p>
      </div>

      <div className="divide-y divide-border rounded-md border border-border">
        <GovRow
          icon={pep.tone === 'ok' ? ShieldCheck : ShieldX}
          label={t('gov.pep')}
          tone={pep.tone}
        >
          {pep.text}
        </GovRow>

        <GovRow
          icon={OctagonAlert}
          label={t('gov.killSwitch')}
          tone={kill.tone}
          action={
            canKill ? (
              <DeepLink to="/killswitch" label={t('gov.viewKillswitch')} />
            ) : undefined
          }
        >
          {kill.text}
        </GovRow>

        <GovRow
          icon={UserCheck}
          label={t('gov.hitl')}
          tone={hitl.tone}
          action={
            run.approval_ref ? (
              <DeepLink to="/permissions" label={t('gov.viewApproval')} />
            ) : undefined
          }
        >
          {hitl.text}
        </GovRow>

        <GovRow
          icon={Disc3}
          label={t('gov.recording')}
          tone={run.record_io ? 'ok' : 'muted'}
          action={
            onViewEvidence ? (
              <button
                type="button"
                onClick={onViewEvidence}
                className="inline-flex items-center gap-1 text-xs font-medium text-accent-text hover:underline"
              >
                <ScrollText className="size-3" />
                {t('gov.viewEvidence')}
              </button>
            ) : undefined
          }
        >
          {run.record_io ? t('gov.recordingOn') : t('gov.recordingOff')}
        </GovRow>

        <GovRow
          icon={Coins}
          label={t('gov.budget')}
          /* Informational, NOT an affirmative green: a persisted run passed the launch
             budget gate (a firm cap denies → no row), but live remaining lives in the
             gateway/FinOps — so this is muted, never a health check we cannot prove. */
          tone="muted"
          action={
            /* Gated on the DESTINATION's own route gate, not on the noun in the label:
               this offers navigation to /finops, and the registry gates that route on
               finops:spend:read. Naming finops:budget:read here — which the budget
               wording invites — would show the link to a principal the destination then
               answers with the Forbidden state. The /finops view loads spend AND budget
               data, so no single permission covers it; that is the wider gate-vs-routes
               gap recorded in sessions-consola-motor-INFORME.md §4.ter. */
            can('finops:spend:read') ? (
              <DeepLink to="/finops" label={t('gov.viewBudget')} />
            ) : undefined
          }
        >
          {t('gov.budgetOk')}
        </GovRow>
      </div>
    </div>
  )
}

function hitlFromStatus(
  status: string | undefined,
  t: (k: string) => string,
): { text: string; tone: Tone } {
  switch (status) {
    case 'approved':
      return { text: t('gov.hitlApproved'), tone: 'ok' }
    case 'break_glass':
      return { text: t('gov.hitlBreakGlass'), tone: 'warn' }
    case 'pending':
      return { text: t('gov.hitlPending'), tone: 'warn' }
    case 'rejected':
    case 'canceled':
    case 'expired':
      return { text: t('gov.hitlDenied'), tone: 'danger' }
    default:
      return { text: status ?? t('gov.unknown'), tone: 'muted' }
  }
}

const TONE_TEXT: Record<Tone, string> = {
  ok: 'text-success',
  warn: 'text-warning',
  danger: 'text-danger',
  muted: 'text-muted-foreground',
}

function GovRow({
  icon: Icon,
  label,
  tone,
  action,
  children,
}: {
  icon: LucideIcon
  label: string
  tone: Tone
  action?: ReactNode
  children: ReactNode
}) {
  return (
    <div className="flex items-start gap-3 px-3 py-2.5">
      <Icon
        className={cn('mt-0.5 size-4 shrink-0', TONE_TEXT[tone])}
        aria-hidden
      />
      <div className="min-w-0 flex-1">
        <div className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
          {label}
        </div>
        <div className={cn('text-sm', TONE_TEXT[tone])}>{children}</div>
      </div>
      {action && <div className="shrink-0 self-center">{action}</div>}
    </div>
  )
}

/** A tiny in-app deep link (registry routes aren't in the static route union → `as never`). */
function DeepLink({ to, label }: { to: string; label: string }) {
  return (
    <Link
      to={to as never}
      className="inline-flex items-center gap-1 text-xs font-medium text-accent-text hover:underline"
    >
      <ExternalLink className="size-3" />
      {label}
    </Link>
  )
}
