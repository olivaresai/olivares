// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Compliance presentational pieces — PURE (data in, UI out). They encode the product's
// honesty rules (docs/SECURITY-HARDENING.md): NEVER "compliant"/"certified" — only control status and
// evidence; `by_design` (a design guarantee, no telemetry) renders with a DISTINCT
// color and copy from `satisfied` (operational evidence) via ControlStatusBadge; every
// reporting payload's `disclaimer` and any control `note` are ALWAYS rendered; an
// evidence package shows its tamper-evidence integrity badge; `unmapped`/`gap` are
// honest VALUE for the auditor, not load errors. No payloads/secrets are rendered.
import { useMemo } from 'react'
import { Download, Lock, LockOpen, ShieldCheck } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { DataTable, type TableColumn } from '@/components/data/data-table'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { EmptyState } from '@/components/ui/empty-state'
import {
  CaveatNotice,
  HashChip,
  IntegrityBadge,
  RiskTierBadge,
  SectionCard,
} from '@/features/_intel'
import { formatDateTime, humanize } from '@/lib/format'
import {
  type EvidenceExportFormat,
  type EvidencePackage,
  type ResidencyAttestation,
  type RiskClassification,
} from './types'

// --- evidence package card ---------------------------------------------------

const EXPORT_FORMATS: EvidenceExportFormat[] = ['json', 'csv']

export function EvidenceCard({
  pkg,
  canExport,
  exportBusy,
  onExport,
  formats = EXPORT_FORMATS,
}: {
  pkg: EvidencePackage
  formats?: readonly EvidenceExportFormat[]
  /** RBAC: export is gated on compliance:framework:read (self-audited server-side). */
  canExport?: boolean
  /** The format currently being fetched (disables that button), if any. */
  exportBusy?: EvidenceExportFormat | null
  onExport?: (id: string, format: EvidenceExportFormat) => void
}) {
  const { t, i18n } = useTranslation('compliance')
  return (
    <SectionCard
      className={pkg.integrity_ok ? undefined : 'border-danger-line'}
    >
      <div className="flex flex-col gap-3">
        <div className="flex flex-wrap items-start justify-between gap-2">
          <div className="min-w-0">
            <div className="flex items-center gap-2">
              <ShieldCheck
                className="size-4 text-muted-foreground"
                aria-hidden
              />
              <span className="text-body font-medium text-foreground">
                {pkg.framework} · {pkg.framework_version}
              </span>
            </div>
            <p className="text-caption text-muted-foreground">
              {t('evidence.generatedAt')}:{' '}
              {formatDateTime(pkg.generated_at, i18n.language)} ·{' '}
              {t('evidence.generatedBy')}:{' '}
              <span className="font-mono">{pkg.generated_by}</span>
            </p>
          </div>
          <IntegrityBadge ok={pkg.integrity_ok} reason={pkg.integrity_reason} />
        </div>

        <div className="flex flex-wrap items-center gap-2">
          <HashChip
            hash={pkg.ledger_hash}
            label={`${t('evidence.ledgerSeq')} ${pkg.ledger_seq}`}
          />
          <HashChip hash={pkg.manifest_hash} label={t('evidence.manifest')} />
          <span className="font-mono text-caption text-muted-foreground">
            {t('evidence.checked', { count: pkg.integrity_checked })}
          </span>
        </div>

        {pkg.scope_note ? (
          <p className="text-caption text-muted-foreground">
            {t('evidence.scope')}:{' '}
            <span className="text-foreground">{pkg.scope_note}</span>
          </p>
        ) : null}

        {canExport ? (
          <div className="flex flex-col gap-2 border-t border-border pt-3">
            <div className="flex flex-wrap items-center gap-2">
              <span className="text-caption font-medium tracking-wide text-muted-foreground uppercase">
                {t('export.label')}
              </span>
              {formats.map((fmt) => (
                <Button
                  key={fmt}
                  variant="secondary"
                  size="sm"
                  disabled={exportBusy === fmt}
                  onClick={() => onExport?.(pkg.id, fmt)}
                >
                  <Download aria-hidden />
                  {t(`export.format.${fmt}`)}
                </Button>
              ))}
            </div>
          </div>
        ) : null}
      </div>
    </SectionCard>
  )
}

// --- agent risk register -----------------------------------------------------

function NistFunctions({ functions }: { functions: string[] }) {
  return (
    <div className="flex flex-wrap gap-1">
      {functions.map((fn) => (
        <Badge key={fn} variant="neutral" className="font-mono text-[11px]">
          {fn}
        </Badge>
      ))}
    </div>
  )
}

function RiskSignalsCell({ row }: { row: RiskClassification }) {
  const { t } = useTranslation('compliance')
  const s = row.signals
  const state = s.declared_autonomy?.state
  const declarationState =
    state &&
    ['declared', 'none_declared', 'partial', 'unavailable'].includes(state)
      ? state
      : 'unknown'
  return (
    <div className="space-y-2">
      <div>
        <p className="mb-1 text-caption text-muted-foreground">
          {t('risk.signals.observed')}
        </p>
        <div className="flex flex-wrap gap-1">
          <Badge variant={s.high_severity_findings > 0 ? 'danger' : 'neutral'}>
            {t('risk.signals.findings', { count: s.high_severity_findings })}
          </Badge>
          <Badge variant="neutral">
            {t('risk.signals.rwEdges', { count: s.rw_edges })}
          </Badge>
          <Badge variant="neutral">
            {t('risk.signals.resources', { count: s.distinct_resources })}
          </Badge>
        </div>
      </div>
      <div>
        <p className="mb-1 text-caption text-muted-foreground">
          {t(
            declarationState === 'unknown' || declarationState === 'unavailable'
              ? 'risk.signals.evidenceTitle'
              : 'risk.signals.declared',
          )}
        </p>
        <div className="flex flex-wrap gap-1">
          {s.autonomous ? (
            <Badge variant="warning">{t('risk.signals.autonomous')}</Badge>
          ) : null}
          {s.scheduled ? (
            <Badge variant="outline">{t('risk.signals.scheduled')}</Badge>
          ) : null}
        </div>
        <p className="max-w-64 whitespace-normal text-caption text-muted-foreground">
          {t(`risk.signals.declaration.${declarationState}`)}
        </p>
      </div>
    </div>
  )
}

const RISK_STATE_VARIANT: Record<string, 'neutral' | 'success' | 'warning'> = {
  suggested: 'neutral',
  approved: 'success',
  overridden: 'warning',
}

export function RiskTable({
  rows,
  canReview,
  onReview,
}: {
  rows: RiskClassification[]
  canReview?: boolean
  onReview?: (row: RiskClassification) => void
}) {
  const { t } = useTranslation('compliance')
  const columns = useMemo<TableColumn<RiskClassification>[]>(() => {
    const cols: TableColumn<RiskClassification>[] = [
      {
        accessorKey: 'agent_id',
        header: t('risk.columns.agent'),
        cell: ({ row }) => (
          <div className="flex flex-col">
            <span className="font-mono text-caption text-foreground">
              {row.original.agent_id || row.original.subject_ref}
            </span>
            <span className="text-[11px] text-muted-foreground">
              {humanize(row.original.subject_kind)}
            </span>
          </div>
        ),
      },
      {
        accessorKey: 'tier',
        header: t('risk.columns.tier'),
        cell: ({ row }) => <RiskTierBadge tier={row.original.tier} />,
      },
      {
        accessorKey: 'suggested_tier',
        header: t('risk.columns.suggested'),
        cell: ({ row }) => (
          <span className="text-caption text-muted-foreground">
            {t(`tiers.${row.original.suggested_tier}`, {
              defaultValue: humanize(row.original.suggested_tier),
            })}
          </span>
        ),
      },
      {
        accessorKey: 'state',
        header: t('risk.columns.state'),
        cell: ({ row }) => (
          <Badge variant={RISK_STATE_VARIANT[row.original.state] ?? 'neutral'}>
            {t(`risk.state.${row.original.state}`, {
              defaultValue: humanize(row.original.state),
            })}
          </Badge>
        ),
      },
      {
        id: 'nist',
        header: t('risk.columns.nist'),
        cell: ({ row }) => (
          <NistFunctions functions={row.original.nist_functions} />
        ),
      },
      {
        id: 'signals',
        header: t('risk.columns.signals'),
        cell: ({ row }) => <RiskSignalsCell row={row.original} />,
      },
    ]
    if (canReview) {
      cols.push({
        id: 'review',
        header: t('risk.columns.review'),
        cell: ({ row }) => (
          <Button
            variant="secondary"
            size="sm"
            onClick={() => onReview?.(row.original)}
          >
            {t('risk.reviewAction')}
          </Button>
        ),
      })
    }
    return cols
  }, [t, canReview, onReview])

  return (
    <DataTable<RiskClassification>
      columns={columns}
      data={rows}
      getRowId={(r) => r.id}
      empty={
        <EmptyState
          title={t('empty.risk.title')}
          description={t('empty.risk.description')}
        />
      }
    />
  )
}

// --- data residency ----------------------------------------------------------

export function ResidencyCard({ region }: { region: ResidencyAttestation }) {
  const { t, i18n } = useTranslation('compliance')
  const hasViolations = region.violations_observed > 0
  return (
    <SectionCard className={hasViolations ? 'border-danger-line' : undefined}>
      <div className="flex flex-col gap-3">
        <div className="flex items-start justify-between gap-2">
          <div className="min-w-0">
            <div className="font-mono text-body font-medium text-foreground">
              {region.region}
            </div>
            <div className="text-caption text-muted-foreground">
              {region.perimeter}
            </div>
          </div>
          <Badge variant={hasViolations ? 'danger' : 'success'}>
            {hasViolations
              ? t('residency.violations', { count: region.violations_observed })
              : t('residency.noViolations')}
          </Badge>
        </div>

        <div className="flex flex-wrap gap-2">
          <Badge variant={region.self_hosted ? 'success' : 'warning'}>
            {region.self_hosted
              ? t('residency.inPerimeter')
              : t('residency.outOfPerimeter')}
          </Badge>
          <Badge variant={region.encryption_at_rest ? 'success' : 'neutral'}>
            {region.encryption_at_rest ? (
              <Lock className="size-3" aria-hidden />
            ) : (
              <LockOpen className="size-3" aria-hidden />
            )}
            {region.encryption_at_rest
              ? t('residency.encryptionOn')
              : t('residency.encryptionOff')}
          </Badge>
        </div>

        {region.data_classes.length > 0 ? (
          <p className="text-caption text-muted-foreground">
            {t('residency.dataClasses')}:{' '}
            <span className="font-mono text-foreground">
              {region.data_classes.join(', ')}
            </span>
          </p>
        ) : null}

        {region.note ? (
          <CaveatNotice tone="warning">{region.note}</CaveatNotice>
        ) : null}

        <p className="text-caption text-muted-foreground">
          {t('residency.lastChecked')}:{' '}
          {formatDateTime(region.last_checked, i18n.language)} ·{' '}
          {t('residency.attestedBy')}:{' '}
          <span className="font-mono">{region.attested_by}</span>
        </p>
      </div>
    </SectionCard>
  )
}
