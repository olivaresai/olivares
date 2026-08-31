// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { Activity, Gauge, HeartPulse, Lock } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { KvList, KvRow } from '@/components/ui/kv'
import { Separator } from '@/components/ui/separator'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { RelTimeLabel } from '@/features/shared'
import { humanDurationSeconds, ppmToPercent } from '@/features/shared'
import { formatLatency } from '@/lib/format'
import { HealthStateBadge } from './health-state-badge'
import type { StatusDTO } from './types'

/**
 * HealthDetailSheet — the per-subject health detail. It mirrors the status row's
 * encoding (state badge, SLA breach, alerting/not-alerting) and exposes the check's
 * declared cadence and SLA target as honest metadata. Cross-links jump to the SLA
 * and timeline tabs for the same subject. Everything is metadata: no error text, no
 * payloads (minimal-data, docs/SECURITY-HARDENING.md).
 */
export function HealthDetailSheet({
  status,
  onClose,
  onViewSla,
  onViewTimeline,
}: {
  status: StatusDTO | null
  onClose: () => void
  onViewSla: (s: StatusDTO) => void
  onViewTimeline: (s: StatusDTO) => void
}) {
  const { t } = useTranslation('health')
  const open = status !== null
  const alerting = status?.desired_status === 'active'

  return (
    <Sheet open={open} onOpenChange={(o) => !o && onClose()}>
      <SheetContent className="w-full sm:max-w-md">
        {status && (
          <>
            <SheetHeader>
              <SheetTitle className="flex items-center gap-2">
                <HeartPulse className="size-4 text-accent-text" />
                <span className="truncate">
                  {status.name || status.subject_ref}
                </span>
              </SheetTitle>
              <SheetDescription className="flex flex-wrap items-center gap-2">
                <Badge variant="outline">
                  {t(`subjectKind.${status.subject_kind}`, {
                    defaultValue: status.subject_kind,
                  })}
                </Badge>
                <HealthStateBadge state={status.state} />
                {status.sla_breach_open && (
                  <Badge variant="danger" title={t('status.slaBreachHint')}>
                    {t('status.slaBreach')}
                  </Badge>
                )}
                {!alerting && (
                  <Badge
                    variant="neutral"
                    title={t(`desiredHint.${status.desired_status}`, {
                      defaultValue: '',
                    })}
                  >
                    {t('status.notAlerting')}
                  </Badge>
                )}
              </SheetDescription>
            </SheetHeader>

            <div className="overflow-y-auto">
              <KvList>
                <KvRow label={t('detail.ref')} mono align="start">
                  {status.subject_ref}
                </KvRow>
                <KvRow label={t('detail.id')} mono align="start">
                  {status.id}
                </KvRow>
                {status.name && (
                  <KvRow label={t('detail.name')} align="start">
                    {status.name}
                  </KvRow>
                )}
                <KvRow label={t('detail.desired')}>
                  {t(`desired.${status.desired_status}`, {
                    defaultValue: status.desired_status,
                  })}
                </KvRow>
                <KvRow label={t('detail.cadence')}>
                  {humanDurationSeconds(status.expected_interval_seconds)}
                </KvRow>
                <KvRow label={t('detail.grace')} mono>
                  ×{status.grace_factor}
                </KvRow>
                <KvRow label={t('detail.slaTarget')} mono>
                  {status.sla_target_ppm > 0
                    ? ppmToPercent(status.sla_target_ppm)
                    : '—'}
                </KvRow>
                <KvRow label={t('detail.slaBreach')}>
                  {status.sla_breach_open ? (
                    <Badge variant="danger">{t('detail.yes')}</Badge>
                  ) : (
                    <Badge variant="neutral">{t('detail.no')}</Badge>
                  )}
                </KvRow>
                <KvRow label={t('detail.latency')} mono>
                  {status.last_latency_ms >= 0
                    ? formatLatency(status.last_latency_ms)
                    : '—'}
                </KvRow>
                <KvRow label={t('detail.lastSeen')}>
                  <RelTimeLabel ts={status.last_seen_at} />
                </KvRow>
                <KvRow label={t('detail.lastChecked')}>
                  <RelTimeLabel ts={status.last_checked_at} />
                </KvRow>
                {status.owner_actor && (
                  <KvRow label={t('detail.owner')} mono align="start">
                    {status.owner_actor}
                  </KvRow>
                )}
                {/* last_detail_hash is an opaque hash (docs/SECURITY-HARDENING.md) — not shown. */}
              </KvList>

              <Separator className="my-3" />
              <div className="flex flex-wrap gap-2">
                <Button
                  variant="secondary"
                  size="sm"
                  onClick={() => onViewSla(status)}
                >
                  <Gauge className="size-3.5" />
                  {t('detail.viewSla')}
                </Button>
                <Button
                  variant="secondary"
                  size="sm"
                  onClick={() => onViewTimeline(status)}
                >
                  <Activity className="size-3.5" />
                  {t('detail.viewTimeline')}
                </Button>
              </div>

              <Separator className="my-3" />
              <p className="flex items-start gap-1.5 text-[11px] leading-snug text-muted-foreground">
                <Lock className="mt-0.5 size-3 shrink-0" />
                {t('detail.minimalData')}
              </p>
            </div>
          </>
        )}
      </SheetContent>
    </Sheet>
  )
}
