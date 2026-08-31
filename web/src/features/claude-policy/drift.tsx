// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Drift overview (REAL). The PERMITTED-policy-vs-OBSERVED-config Findings the
// read-only connectors emit, read from /v1/m/security/findings. This is a
// PRIVILEGED, self-audited read (docs/SECURITY-HARDENING.md). Evidence is a redacted fingerprint
// (detail_hash) — never a payload (docs/SECURITY-HARDENING.md).
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { EmptyState } from '@/components/ui/empty-state'
import { StatusBadge } from '@/components/data/badges'
import {
  AsyncSection,
  SectionCard,
  SelfAuditNotice,
  ListTruncationBadge,
} from '@/features/_intel'
import { RelTimeLabel } from '@/features/shared'
import { useAuth } from '@/lib/auth/context'
import { claudePolicyApi, claudePolicyKeys } from './api'
import type { PolicyDriftFinding } from './types'

const SEVERITY_VARIANT: Record<
  string,
  'danger' | 'warning' | 'info' | 'outline'
> = {
  critical: 'danger',
  high: 'danger',
  medium: 'warning',
  low: 'info',
  info: 'outline',
}

/** A compact, redacted list of drift Findings. Reused by the publish→drift panel. */
export function DriftFindingList({
  findings,
}: {
  findings: PolicyDriftFinding[]
}) {
  const { t } = useTranslation('claudePolicy')
  return (
    <ul className="flex flex-col gap-2" aria-label={t('drift.listLabel')}>
      {findings.map((f) => (
        <li
          key={f.id}
          className="flex flex-col gap-1 rounded-md border border-border bg-surface px-3 py-2"
        >
          <div className="flex flex-wrap items-center gap-2">
            <Badge
              variant={SEVERITY_VARIANT[f.severity?.toLowerCase()] ?? 'outline'}
            >
              {f.severity ?? 'info'}
            </Badge>
            <Badge variant="outline" className="font-mono text-[0.65rem]">
              {f.kind}
            </Badge>
            {f.status && <StatusBadge status={f.status} />}
            <span className="text-sm text-foreground">
              {f.title ?? f.subject_ref}
            </span>
          </div>
          <div className="flex flex-wrap items-center gap-x-3 gap-y-0.5 text-xs text-muted-foreground">
            {f.subject_kind && (
              <span>
                {t('drift.subject')}:{' '}
                <code className="font-mono">{f.subject_kind}</code>
                {f.subject_ref ? `/${f.subject_ref}` : ''}
              </span>
            )}
            {f.detail_hash && (
              <span title={f.detail_hash}>
                {t('drift.fingerprint')}:{' '}
                <code className="font-mono">{f.detail_hash.slice(0, 16)}…</code>
              </span>
            )}
            {f.occurred_at && <RelTimeLabel ts={f.occurred_at} />}
          </div>
        </li>
      ))}
    </ul>
  )
}

/** The "Drift & posture" tab: the PERMITTED-vs-OBSERVED verification emits.*/
export function DriftView({ active }: { active: boolean }) {
  const { t } = useTranslation('claudePolicy')
  const { activeTenant, can } = useAuth()
  const canRead = can('governance:claude-policy:read')

  const query = useQuery({
    queryKey: claudePolicyKeys.drift(activeTenant),
    queryFn: () => claudePolicyApi.listDrift(),
    enabled: canRead && active,
  })

  return (
    <div className="flex flex-col gap-3">
      <SelfAuditNotice />
      <SectionCard title={t('drift.title')} description={t('drift.subtitle')}>
        <ListTruncationBadge
          query={query}
          label={t('drift.truncated', { n: query.data?.items?.length ?? 0 })}
          hint={t('drift.truncatedHint')}
        />
        <AsyncSection query={query}>
          {(data) => {
            const items = data.items ?? []
            if (items.length === 0) {
              return (
                <EmptyState
                  title={t('drift.emptyTitle')}
                  description={t('drift.emptyBody')}
                />
              )
            }
            return <DriftFindingList findings={items} />
          }}
        </AsyncSection>
      </SectionCard>
    </div>
  )
}
