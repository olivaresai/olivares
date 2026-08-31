// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Domain badges shared across the intelligence views. They map the engine's ordered
// vocabularies (severity, outcome, guardrail verdict, ledger integrity, red-team
// consent, EU AI Act risk tier, compliance control status) onto the design system's
// semantic tokens — and they encode the product's honesty rules: `critical`/`high`
// read loud; `by_design` is NOT `satisfied` (distinct color + copy); a `registered`
// red-team target is NOT consent; an unavailable signing key is NOT a failure.
import {
  AlertTriangle,
  ShieldAlert,
  ShieldCheck,
  ShieldQuestion,
} from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Badge, type BadgeVariant } from '@/components/ui/badge'
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { cn } from '@/lib/utils'
import { humanize } from '@/lib/format'
// The `intel` namespace travels with the modules that translate: these are deep-
// imported across features (`@/features/_intel/notices`), where the barrel — and so
// the registration — is never in the chunk.
import './i18n'

function lc(s: string | null | undefined): string {
  return (s ?? '').toLowerCase()
}

// --- severity ----------------------------------------------------------------

const SEVERITY_VARIANT: Record<string, BadgeVariant> = {
  info: 'neutral',
  low: 'info',
  medium: 'warning',
  high: 'danger',
  critical: 'danger',
}

/** Ordered finding/recommendation severity. `critical` is emphasized so it never
 *  blends into `high` (docs/SECURITY-HARDENING.md — the loud cases must read loud).*/
export function SeverityBadge({
  severity,
  className,
}: {
  severity: string
  className?: string
}) {
  const { t } = useTranslation('intel')
  const key = lc(severity)
  const variant = SEVERITY_VARIANT[key] ?? 'neutral'
  const emphatic = key === 'critical'
  return (
    <Badge
      variant={variant}
      className={cn(
        emphatic && 'font-semibold uppercase tracking-wide',
        className,
      )}
    >
      {emphatic ? <AlertTriangle className="size-3" /> : null}
      {t(`severity.${key}`, { defaultValue: humanize(severity) })}
    </Badge>
  )
}

// --- outcome (eval / red-team per-probe result) ------------------------------

const OUTCOME_VARIANT: Record<string, BadgeVariant> = {
  pass: 'success',
  blocked: 'success',
  refused: 'success',
  fail: 'danger',
  complied: 'danger',
  leaked: 'danger',
  error: 'warning',
  skipped: 'outline',
}

export function OutcomeBadge({
  outcome,
  className,
}: {
  outcome: string
  className?: string
}) {
  const { t } = useTranslation('intel')
  const key = lc(outcome)
  return (
    <Badge variant={OUTCOME_VARIANT[key] ?? 'neutral'} className={className}>
      {t(`outcome.${key}`, { defaultValue: humanize(outcome) })}
    </Badge>
  )
}

// --- guardrail verdict -------------------------------------------------------

const VERDICT_VARIANT: Record<string, BadgeVariant> = {
  allow: 'success',
  flag: 'warning',
  block: 'danger',
}

/** Guardrail verdict. `block` is only reachable under governed enforcement; the
 *  default plane is detective (`allow`/`flag`) — this badge just colors the value. */
export function VerdictBadge({
  verdict,
  className,
}: {
  verdict: string
  className?: string
}) {
  const { t } = useTranslation('intel')
  const key = lc(verdict)
  return (
    <Badge variant={VERDICT_VARIANT[key] ?? 'neutral'} className={className}>
      {t(`verdict.${key}`, { defaultValue: humanize(verdict) })}
    </Badge>
  )
}

// --- ledger integrity --------------------------------------------------------

/** The forensic guarantee, as a verdict — not decoration. `unavailable` (no
 *  checkpoint key wired) is a calm neutral, NOT a red failure (docs/SECURITY-HARDENING.md), and
 *  neither is `pending` (the ledger is healthy, nothing has been attested YET —
 *  a first-boot install before the checkpoint scheduler fires). Both take
 *  precedence over `ok`, because `ok` is false in both and the red they would
 *  otherwise produce is the red that teaches operators to ignore red. */
export function IntegrityBadge({
  ok,
  unavailable,
  pending,
  reason,
  className,
}: {
  ok?: boolean
  unavailable?: boolean
  pending?: boolean
  reason?: string
  className?: string
}) {
  const { t } = useTranslation('intel')
  if (unavailable) {
    return (
      <Badge variant="neutral" className={className}>
        <ShieldQuestion className="size-3" />
        {t('integrity.unavailable')}
      </Badge>
    )
  }
  if (pending) {
    return (
      <Badge variant="neutral" className={className}>
        <ShieldQuestion className="size-3" />
        {t('integrity.pending')}
      </Badge>
    )
  }
  if (ok === false) {
    const badge = (
      <Badge variant="danger" className={cn('font-semibold', className)}>
        <ShieldAlert className="size-3" />
        {t('integrity.failed')}
      </Badge>
    )
    return reason ? (
      <Tooltip>
        <TooltipTrigger asChild>
          <span>{badge}</span>
        </TooltipTrigger>
        <TooltipContent>{reason}</TooltipContent>
      </Tooltip>
    ) : (
      badge
    )
  }
  return (
    <Badge variant="success" className={className}>
      <ShieldCheck className="size-3" />
      {t('integrity.verified')}
    </Badge>
  )
}

// --- red-team consent --------------------------------------------------------

const CONSENT_VARIANT: Record<string, BadgeVariant> = {
  registered: 'warning',
  authorized: 'success',
  revoked: 'danger',
}

/** The double-use boundary in the UI: only `authorized` enables a run. */
export function ConsentBadge({
  status,
  className,
}: {
  status: string
  className?: string
}) {
  const { t } = useTranslation('intel')
  const key = lc(status)
  return (
    <Badge variant={CONSENT_VARIANT[key] ?? 'neutral'} className={className}>
      {t(`consent.${key}`, { defaultValue: humanize(status) })}
    </Badge>
  )
}

// --- EU AI Act risk tier -----------------------------------------------------

const RISK_VARIANT: Record<string, BadgeVariant> = {
  unacceptable: 'danger',
  high: 'danger',
  limited: 'warning',
  minimal: 'success',
}

/** EU AI Act effective tier. `unacceptable` only appears after human review — it is
 *  emphasized; the heuristic never assigns it (docs/SECURITY-HARDENING.md contract).*/
export function RiskTierBadge({
  tier,
  className,
}: {
  tier: string
  className?: string
}) {
  const { t } = useTranslation('intel')
  const key = lc(tier)
  const emphatic = key === 'unacceptable'
  return (
    <Badge
      variant={RISK_VARIANT[key] ?? 'neutral'}
      className={cn(
        emphatic && 'font-semibold uppercase tracking-wide',
        className,
      )}
    >
      {emphatic ? <AlertTriangle className="size-3" /> : null}
      {t(`risk.${key}`, { defaultValue: humanize(tier) })}
    </Badge>
  )
}

// --- compliance control status ----------------------------------------------

const CONTROL_VARIANT: Record<string, BadgeVariant> = {
  satisfied: 'success',
  by_design: 'info',
  partial: 'warning',
  gap: 'danger',
  unmapped: 'neutral',
}

/** Compliance control status. NEVER "compliant"/"certified" — control status only.
 *  `by_design` (design guarantee, no telemetry) is a distinct color from
 *  `satisfied` (operational evidence) — never collapse them (docs/SECURITY-HARDENING.md).*/
export function ControlStatusBadge({
  status,
  className,
}: {
  status: string
  className?: string
}) {
  const { t } = useTranslation('intel')
  const key = lc(status)
  return (
    <Badge variant={CONTROL_VARIANT[key] ?? 'neutral'} className={className}>
      {t(`control.${key}`, { defaultValue: humanize(status) })}
    </Badge>
  )
}

/**
 * ⛔ EL VEREDICTO DE UNA PUERTA DE APROBACIÓN, COMPARTIDO, y vive aquí porque lo pintan TRES
 *    superficies con el mismo campo `gate_status` y sólo una lo hacía bien.
 *
 *    `modules/deploy/ports.go` y `modules/orchestration/ports.go` declaran el MISMO vocabulario:
 *    `approved · pending · rejected · expired · no_gate · not_required`. Deploy ya lo pintaba con
 *    variantes; la línea de decisiones de orquestación lo escribía como **texto gris crudo** —
 *    `rejected` y `approved` con el mismo peso y el mismo color, al lado de un badge de operación
 *    que SÍ va coloreado, de modo que **una puerta rechazada era lo más silencioso de la fila**—;
 *    y el panel de ejecución de workflows lo forzaba a `warning` SIEMPRE, así que una puerta
 *    aprobada se leía como una advertencia.
 *
 * ⚠ Y SIGUE SIENDO SÓLO VISUAL: la autorización la impone el servidor, deny-by-default (un 403).
 *    La consola no infiere permiso de este campo — el `title` lo dice.
 */
const GATE_VARIANT: Record<string, BadgeVariant> = {
  approved: 'success',
  pending: 'info',
  rejected: 'danger',
  expired: 'warning',
  no_gate: 'warning',
  not_required: 'neutral',
}

export function GateBadge({ gate }: { gate?: string }) {
  const { t } = useTranslation('intel')
  if (!gate) return null
  const key = String(gate)
  return (
    <Badge
      variant={GATE_VARIANT[key] ?? 'neutral'}
      title={t(`gate.${key}Hint`, { defaultValue: t('gate.displayNotice') })}
    >
      {t(`gate.${key}`, { defaultValue: key })}
    </Badge>
  )
}
