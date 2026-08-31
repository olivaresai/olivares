// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useTranslation } from 'react-i18next'
import { cn } from '@/lib/utils'
import { Badge, type BadgeVariant } from '@/components/ui/badge'
import type { AccessConfidence, AccessMode } from '@/lib/api/types'

/**
 * Product-domain badges that map engine values to the design system. Kept in the
 * data layer (not ui/) so they can localize their labels. They are the canonical
 * way the catalog/graph views render status, R/RW access mode, and the
 * access-graph CONFIDENCE — the last is contract-critical: `attributed` reads as
 * firm (solid teal, filled dot) and `approximate` as quieter and cooler (dashed
 * slate, hollow dot), never alarming (UI-CONTRACT-ACCESS-MAP).
 */
const STATUS_VARIANT: Record<string, BadgeVariant> = {
  active: 'success',
  healthy: 'success',
  running: 'success',
  ok: 'success',
  enabled: 'success',
  approved: 'success',
  pending: 'info',
  queued: 'info',
  provisioning: 'info',
  draft: 'info',
  degraded: 'warning',
  warning: 'warning',
  deprecated: 'warning',
  unused: 'warning',
  error: 'danger',
  failed: 'danger',
  revoked: 'danger',
  down: 'danger',
  unexpected: 'danger',
  inactive: 'neutral',
  idle: 'neutral',
  unknown: 'neutral',
  disabled: 'neutral',
}

function humanize(s: string): string {
  if (!s) return '—'
  return s.charAt(0).toUpperCase() + s.slice(1).replace(/[_-]+/g, ' ')
}

export function StatusBadge({
  status,
  className,
}: {
  status: string
  className?: string
}) {
  const { t } = useTranslation('common')
  const key = (status ?? '').toLowerCase()
  const variant = STATUS_VARIANT[key] ?? 'neutral'
  return (
    <Badge variant={variant} className={className}>
      {t(`status.${key}`, { defaultValue: humanize(status) })}
    </Badge>
  )
}

export function AccessModeBadge({
  mode,
  className,
}: {
  mode: AccessMode
  className?: string
}) {
  const { t } = useTranslation('common')
  const m = String(mode).toLowerCase()
  const isWrite = m === 'readwrite' || m === 'rw' || m === 'write'
  // Write carries risk — the one place copper means "write" (per the brand contract).
  const variant: BadgeVariant = isWrite
    ? 'accent'
    : m === 'read'
      ? 'info'
      : 'neutral'
  const labelKey = m === 'rw' ? 'readwrite' : m
  return (
    <Badge variant={variant} className={className}>
      {t(`accessMode.${labelKey}`, { defaultValue: humanize(m) })}
    </Badge>
  )
}

export function ConfidenceBadge({
  confidence,
  className,
}: {
  confidence: AccessConfidence
  className?: string
}) {
  const { t } = useTranslation('common')
  const c = String(confidence).toLowerCase()

  if (c === 'approximate') {
    return (
      <span
        className={cn(
          'inline-flex items-center gap-1.5 rounded-sm border border-dashed border-confidence-approximate/60 px-1.5 py-0.5 text-xs font-medium text-confidence-approximate',
          className,
        )}
        title={t('confidence.approximateHint')}
      >
        <span
          className="size-1.5 rounded-full border border-current"
          aria-hidden
        />
        {t('confidence.approximate')}
      </span>
    )
  }
  // `attributed` (and any firm value) — solid, filled, confident.
  return (
    <span
      className={cn(
        'inline-flex items-center gap-1.5 rounded-sm border border-confidence-attributed/40 px-1.5 py-0.5 text-xs font-medium text-confidence-attributed',
        className,
      )}
      title={t('confidence.attributedHint')}
    >
      <span className="size-1.5 rounded-full bg-current" aria-hidden />
      {t('confidence.attributed')}
    </span>
  )
}
