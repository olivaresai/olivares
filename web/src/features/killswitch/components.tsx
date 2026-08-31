// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Small presentational pieces for the kill-switch console: lifecycle / scope /
// source chips over the stop DTO and the guardian-trail status chip. Pure render
// over engine values — no derivation beyond presentedStopState (status +
// reviewed flag → presented lifecycle).
import { useTranslation } from 'react-i18next'
import { Badge, type BadgeVariant } from '@/components/ui/badge'
import './i18n'
import { presentedStopState } from './types'
import type { GuardianActionStatus, KillSwitchDTO } from './types'

/** Presented stop lifecycle chip: active (danger — the estate/agent is stopped) →
 *  post-review due (warning — re-enabled but the incident loop is open) →
 *  reviewed (neutral — closed). */
export function StopStateBadge({ stop }: { stop: KillSwitchDTO }) {
  const { t } = useTranslation('killswitch')
  const state = presentedStopState(stop)
  const variant: BadgeVariant =
    state === 'active'
      ? 'danger'
      : state === 'review_due'
        ? 'warning'
        : 'success'
  return <Badge variant={variant}>{t(`status.${state}`)}</Badge>
}

/** Scope chip + the agent ref (external id preferred — the operator-facing name). */
export function StopScopeCell({ stop }: { stop: KillSwitchDTO }) {
  const { t } = useTranslation('killswitch')
  const isEstate = stop.scope_kind === 'estate'
  const ref = stop.agent_external_id || stop.scope_ref || stop.agent_id
  return (
    <span className="flex items-center gap-1.5">
      <Badge variant={isEstate ? 'danger' : 'neutral'}>
        {t(`scope.${isEstate ? 'estate' : 'agent'}`)}
      </Badge>
      {!isEstate && ref && (
        <span className="truncate font-mono text-xs text-muted-foreground">
          {ref}
        </span>
      )}
    </span>
  )
}

/** Source chip: operator engage vs guardian-rule engage (with the rule ref). */
export function StopSourceCell({ stop }: { stop: KillSwitchDTO }) {
  const { t } = useTranslation('killswitch')
  const isGuardian = stop.source === 'guardian'
  return (
    <span className="flex items-center gap-1.5">
      <Badge variant={isGuardian ? 'info' : 'outline'}>
        {t(`source.${isGuardian ? 'guardian' : 'operator'}`)}
      </Badge>
      {isGuardian && stop.rule_ref && (
        <span
          className="truncate font-mono text-xs text-muted-foreground"
          title={t('stops.byRule')}
        >
          {stop.rule_ref}
        </span>
      )}
    </span>
  )
}

const GUARDIAN_STATUS_VARIANT: Record<string, BadgeVariant> = {
  pending: 'info',
  executed: 'success',
  rejected: 'danger',
  expired: 'neutral',
  failed: 'danger',
}

/** Guardian containment-trail status chip (pending/executed/rejected/expired/failed). */
export function GuardianStatusBadge({
  status,
}: {
  status: GuardianActionStatus
}) {
  const { t } = useTranslation('killswitch')
  const key = (status ?? '').toLowerCase()
  return (
    <Badge variant={GUARDIAN_STATUS_VARIANT[key] ?? 'neutral'}>
      {t(`guardian.status.${key}`, { defaultValue: status })}
    </Badge>
  )
}
