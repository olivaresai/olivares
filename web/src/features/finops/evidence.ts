// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type {
  Alert,
  BudgetStatus,
  AmountClass,
  AmountComponents,
} from './types'

// A presentation bound, not a ledger limit. Oversized or noncanonical wire values
// are unavailable here; they never fall back to the lossy legacy number.
export function decimalMicro(value: unknown): value is string {
  return (
    typeof value === 'string' &&
    value.length <= 128 &&
    /^(0|-?[1-9][0-9]*)$/.test(value)
  )
}
export function formatEvidenceMoney(
  value: unknown,
  locale = 'en',
): string | null {
  if (!decimalMicro(value)) return null
  const negative = value.startsWith('-')
  const digits = negative ? value.slice(1) : value
  const padded = digits.padStart(7, '0')
  const whole = BigInt(padded.slice(0, -6))
  const fraction = padded.slice(-6).replace(/0+$/, '').padEnd(2, '0')
  // BigInt reaches Intl directly. For negative sub-dollar amounts, retain the
  // locale's sign position with a -1 template and replace only its integer part.
  const template = negative ? -(whole || 1n) : whole
  return new Intl.NumberFormat(locale, {
    style: 'currency',
    currency: 'USD',
    minimumFractionDigits: 6,
    maximumFractionDigits: 6,
  })
    .formatToParts(template)
    .map((part) => {
      if (part.type === 'fraction') return fraction
      if (part.type === 'integer' && negative && whole === 0n) return '0'
      return part.value
    })
    .join('')
}
export interface PresentedAmount {
  class: AmountClass | 'historical'
  value: string | null
  cause?: string
}
const unknown = (cause = 'malformed_evidence'): PresentedAmount => ({
  class: 'unknown',
  value: null,
  cause,
})
function classified(
  kind: unknown,
  value: unknown,
  currency: unknown,
): PresentedAmount {
  if (currency !== 'USD') return unknown()
  if (kind === 'unknown' && value === null) return unknown('incomplete')
  if ((kind === 'exact' || kind === 'lower_bound') && decimalMicro(value))
    return { class: kind, value }
  return unknown()
}
function componentShape(components: AmountComponents | undefined): boolean {
  if (!components) return false
  return [
    components.cost,
    components.static_reservation,
    components.dynamic_reservation,
  ].every(
    (c) =>
      c &&
      (c.state === 'known'
        ? decimalMicro(c.value_micro_usd)
        : ['unknown', 'nonnegative_unknown', 'indeterminate'].includes(
            c.state,
          ) && c.value_micro_usd === null),
  )
}
export function budgetAmount(status: BudgetStatus): PresentedAmount {
  const a = status.amount
  if (!a) return unknown('unavailable')
  if (
    !componentShape(a.components) ||
    !a.over_limit ||
    !['proven', 'not_reached', 'unproven'].includes(a.over_limit.result) ||
    !a.legacy_fields ||
    a.forecast_certified !== false
  )
    return unknown()
  if (
    a.state !== (a.class === 'exact' ? 'complete' : 'incomplete') ||
    (a.remaining_micro_usd !== null && !decimalMicro(a.remaining_micro_usd)) ||
    (a.thresholds !== null && !Array.isArray(a.thresholds))
  )
    return unknown()
  if (
    a.class === 'exact' &&
    [
      a.components.cost,
      a.components.static_reservation,
      a.components.dynamic_reservation,
    ].some((c) => c.state !== 'known')
  )
    return unknown()
  if (
    a.class === 'lower_bound' &&
    (a.components.cost.state !== 'known' ||
      a.components.static_reservation.state !== 'known' ||
      a.components.dynamic_reservation.state !== 'nonnegative_unknown')
  )
    return unknown()
  const result = classified(a.class, a.effective_micro_usd, a.currency)
  return result.class === 'unknown' &&
    Array.isArray(a.causes) &&
    a.causes.length
    ? { ...result, cause: a.causes.slice(0, 36).join(', ') }
    : result
}
export function alertAmount(
  alert: Alert,
  tenant?: string | null,
): PresentedAmount {
  const evidence = alert.amount_evidence
  if (!evidence) return unknown('unavailable')
  if (evidence.state === 'unknown') {
    if (
      evidence.cause === 'legacy_unversioned' &&
      !Object.hasOwn(evidence, 'envelope') &&
      !Object.hasOwn(evidence, 'evidence_hash') &&
      alert.legacy_value_kind === 'unverified'
    ) {
      return {
        class: 'historical',
        value: Number.isSafeInteger(alert.spend_micro_usd)
          ? String(alert.spend_micro_usd)
          : null,
        cause: evidence.cause,
      }
    }
    return unknown(evidence.cause)
  }
  const e = evidence.envelope
  if (
    evidence.state !== 'valid' ||
    !e ||
    e.schema_version !== 1 ||
    e.digest_version !== 1 ||
    !/^[0-9a-f]{64}$/.test(evidence.evidence_hash ?? '') ||
    !e.alert_id ||
    e.alert_id !== alert.id ||
    e.budget_id !== alert.budget_id ||
    !e.tenant_id ||
    (tenant != null && e.tenant_id !== tenant) ||
    !e.policy ||
    e.policy.id !== e.budget_id ||
    !e.amount ||
    !componentShape(e.components) ||
    e.decision?.result !== 'proven' ||
    !e.context ||
    !e.legacy ||
    !decimalMicro(e.policy.limit_micro_usd) ||
    e.policy.currency !== 'USD' ||
    !['exact', 'lower_bound', 'unavailable'].includes(e.legacy.value_kind) ||
    ![
      e.context.evaluated_at,
      e.context.sample_occurred_at,
      e.context.window_bounds,
      e.context.provenance_filter,
      e.context.read_consistency,
      e.decision.threshold,
      e.decision.target_numerator,
      e.decision.target_denominator,
    ].every((v) => typeof v === 'string' && v.length > 0)
  )
    return unknown()
  // The authenticated server verifies digest/arithmetic. This consumer rejects
  // unsupported and broken shapes; it does not create a second evidence verifier.
  return classified(e.amount.class, e.amount.value_micro_usd, e.amount.currency)
}
export function budgetPercent(status: BudgetStatus): number | null {
  const a = budgetAmount(status)
  const limit = status.amount?.over_limit?.target_micro_usd
  if (
    a.class !== 'exact' ||
    !decimalMicro(a.value) ||
    !decimalMicro(limit) ||
    BigInt(limit) <= 0n
  )
    return null
  const pct = (BigInt(a.value) * 100n) / BigInt(limit)
  return Number(pct < 0n ? 0n : pct > 100n ? 100n : pct)
}
