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
import { useMemo, useState } from 'react'
import {
  Boxes,
  ChevronDown,
  ChevronRight,
  Download,
  Layers,
  Lock,
  LockOpen,
  ScrollText,
  ShieldCheck,
} from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { DataTable, type TableColumn } from '@/components/data/data-table'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { EmptyState } from '@/components/ui/empty-state'
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { StatusBar, useChartTheme } from '@/components/charts'
import {
  CaveatNotice,
  ControlStatusBadge,
  DisclaimerNote,
  HashChip,
  IntegrityBadge,
  RiskTierBadge,
  SectionCard,
} from '@/features/_intel'
import { cn } from '@/lib/utils'
import { formatDateTime, formatInt, humanize } from '@/lib/format'
import {
  frameworkGroup,
  isInDevelopmentFramework,
  type ControlAssessment,
  type ControlCapability,
  type EvidenceExportFormat,
  type EvidencePackage,
  type FrameworkRollup,
  type OscalExport,
  type ResidencyAttestation,
  type RiskClassification,
  type StatusSummary,
} from './types'

/** Capabilities whose honesty hint is load-bearing for an auditor. The copy lives
 *  in i18n (posture.capabilityHint.*); this set decides WHICH capabilities get the
 *  prominent caveat. resource_accounting = compute/cost accounting ONLY; external_activity
 *  = audit/eDiscovery evidence, NOT a security alert; supplier_gpai_posture = a CLAIM
 *  unless operator-verified. */
const CAPABILITY_HINT_KEYS = new Set([
  'resource_accounting',
  'external_activity',
  'supplier_gpai_posture',
])

// --- status-bar segments (shared colors) -------------------------------------

/** The 5 status categories with DISTINCT colors (docs/SECURITY-HARDENING.md): satisfied=success,
 *  by_design=info, partial=warning, gap=danger, unmapped=slate/neutral. */
function useStatusSegments() {
  const { t } = useTranslation('compliance')
  const theme = useChartTheme()
  return (summary: StatusSummary) => [
    {
      key: 'satisfied',
      label: t('status.satisfied'),
      value: summary.satisfied,
      color: theme.success,
    },
    {
      key: 'by_design',
      label: t('status.by_design'),
      value: summary.by_design,
      color: theme.info,
    },
    {
      key: 'partial',
      label: t('status.partial'),
      value: summary.partial,
      color: theme.warning,
    },
    {
      key: 'gap',
      label: t('status.gap'),
      value: summary.gap,
      color: theme.danger,
    },
    {
      key: 'unmapped',
      label: t('status.unmapped'),
      value: summary.unmapped,
      color: theme.slate,
    },
  ]
}

// --- cross-framework roll-up (dashboard header) ------------------------------

function FrameworkRollupCard({
  f,
  active,
  onSelect,
}: {
  f: FrameworkRollup
  active: boolean
  onSelect?: (framework: string) => void
}) {
  const { t } = useTranslation('compliance')
  const segmentsFor = useStatusSegments()
  const inDev = isInDevelopmentFramework(f.framework)
  return (
    <button
      type="button"
      onClick={() => onSelect?.(f.framework)}
      className={cn(
        'flex flex-col gap-3 rounded-lg border bg-surface p-4 text-left outline-none transition-colors',
        'hover:border-border-strong focus-visible:ring-2 focus-visible:ring-ring',
        active ? 'border-accent-text' : 'border-border',
      )}
      aria-pressed={active}
    >
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          {/* ⛔ SEIS titulos de marco salian con «…». Un marco se identifica POR SU
              NOMBRE: truncarlo esconde justo lo que distingue «EU AI Act» de «EU AI
              Act — GPAI». El informe lo dice: wrap permitido en titulos. */}
          <div className="text-sm font-medium text-foreground">{f.name}</div>
          <div className="font-mono text-xs text-muted-foreground">
            {f.version}
          </div>
        </div>
        <div className="flex shrink-0 flex-col items-end gap-1">
          <Badge variant="outline">
            {t('status.ofTotal', {
              satisfied: f.summary.satisfied,
              total: f.summary.total,
            })}
          </Badge>
          {inDev ? (
            <Badge variant="warning">{t('groups.inDevelopment')}</Badge>
          ) : null}
          {frameworkGroup(f.framework) === 'crosswalk' ? (
            // Per-card honesty: a design-toward crosswalk is never a conformance
            // standard — flag it on the card itself, not only the group header.
            <Badge variant="outline">{t('groups.designTowardTag')}</Badge>
          ) : null}
        </div>
      </div>
      <StatusBar
        segments={segmentsFor(f.summary)}
        valueFormatter={(v) => formatInt(v)}
      />
    </button>
  )
}

/** Cross-framework roll-up, GROUPED into regulatory frameworks/standards vs the
 *  design-toward crosswalks (threat models / guidance / overlays). The grouping is a
 *  fixed client-side PRESENTATION split of the framework ids (types.ts frameworkGroup)
 *  — it derives nothing from per-tenant data, so it is not a recompute. The crosswalk
 *  group carries an explicit no-conformance-claim caveat; each non-final framework's
 *  own design-toward disclaimer is rendered prominently when its controls are opened. */
export function FrameworkRollupList({
  frameworks,
  selected,
  onSelect,
}: {
  frameworks: FrameworkRollup[]
  selected?: string
  onSelect?: (framework: string) => void
}) {
  const { t } = useTranslation('compliance')
  const { regulatory, crosswalk } = useMemo(() => {
    const reg: FrameworkRollup[] = []
    const cross: FrameworkRollup[] = []
    for (const f of frameworks) {
      if (frameworkGroup(f.framework) === 'crosswalk') cross.push(f)
      else reg.push(f)
    }
    return { regulatory: reg, crosswalk: cross }
  }, [frameworks])

  const renderGroup = (items: FrameworkRollup[]) => (
    <div className="grid gap-3 md:grid-cols-2">
      {items.map((f) => (
        <FrameworkRollupCard
          key={f.framework}
          f={f}
          active={f.framework === selected}
          onSelect={onSelect}
        />
      ))}
    </div>
  )

  return (
    <div className="flex flex-col gap-5">
      {regulatory.length > 0 ? (
        <section className="flex flex-col gap-3">
          <div className="flex items-center gap-2">
            <span className="text-xs font-medium tracking-wide text-muted-foreground uppercase">
              {t('groups.regulatory')}
            </span>
            <Badge variant="neutral">{formatInt(regulatory.length)}</Badge>
          </div>
          {renderGroup(regulatory)}
        </section>
      ) : null}

      {crosswalk.length > 0 ? (
        <section className="flex flex-col gap-3">
          <div className="flex items-center gap-2">
            <span className="text-xs font-medium tracking-wide text-muted-foreground uppercase">
              {t('groups.crosswalk')}
            </span>
            <Badge variant="outline">{formatInt(crosswalk.length)}</Badge>
          </div>
          <CaveatNotice tone="info">{t('groups.crosswalkHint')}</CaveatNotice>
          {renderGroup(crosswalk)}
        </section>
      ) : null}
    </div>
  )
}

/** A PROMINENT design-toward / no-conformance-claim banner for a framework's own
 *  disclaimer. Rendered for the crosswalks (and any in-development framework) so
 *  the no-conformance-claim wording is impossible to miss — not just a muted footer.
 *  The text is the framework's OWN disclaimer from the engine (never invented). */
export function FrameworkDisclaimerBanner({
  framework,
  disclaimer,
}: {
  framework: string
  disclaimer: string
}) {
  const { t } = useTranslation('compliance')
  const isCrosswalk = frameworkGroup(framework) === 'crosswalk'
  const inDev = isInDevelopmentFramework(framework)
  if (!isCrosswalk && !inDev) return null
  return (
    <CaveatNotice tone="warning">
      <span className="font-medium text-warning">
        {inDev
          ? t('groups.inDevelopmentBanner')
          : t('groups.designTowardBanner')}
      </span>{' '}
      <span className="text-muted-foreground">{disclaimer}</span>
    </CaveatNotice>
  )
}

// --- capability row (operational vs architectural) ---------------------------

function CapabilityRow({ cap }: { cap: ControlCapability }) {
  const { t } = useTranslation('compliance')
  const isArchitectural = cap.class === 'architectural'
  const Icon = isArchitectural ? Layers : Boxes
  const stateVariant =
    cap.state === 'present'
      ? 'success'
      : cap.state === 'absent'
        ? 'danger'
        : 'neutral'
  return (
    <li className="flex flex-col gap-1 rounded-md border border-border bg-muted/40 p-3">
      <div className="flex flex-wrap items-center gap-2">
        <Tooltip>
          <TooltipTrigger asChild>
            <span className="inline-flex items-center gap-1.5 text-xs font-medium text-foreground">
              <Icon
                className={cn(
                  'size-3.5',
                  isArchitectural ? 'text-info' : 'text-accent-text',
                )}
                aria-hidden
              />
              {humanize(cap.key)}
            </span>
          </TooltipTrigger>
          <TooltipContent>
            {isArchitectural
              ? t('posture.class.architecturalHint')
              : t('posture.class.operationalHint')}
          </TooltipContent>
        </Tooltip>
        <Badge variant="outline">
          {isArchitectural
            ? t('posture.class.architectural')
            : t('posture.class.operational')}
        </Badge>
        <Badge variant={stateVariant}>
          {t(`posture.capabilityState.${cap.state}`)}
        </Badge>
        {cap.count !== undefined ? (
          <span className="font-mono text-xs text-muted-foreground">
            {t('posture.count')}: {formatInt(cap.count)}
          </span>
        ) : null}
      </div>
      {cap.detail ? (
        <p className="text-xs leading-relaxed text-muted-foreground">
          {cap.detail}
        </p>
      ) : null}
      {/* Capabilities carry a load-bearing honesty hint (compute-accounting-only /
          audit-evidence-not-alert / claim-vs-verified) — ALWAYS shown. */}
      {CAPABILITY_HINT_KEYS.has(cap.key) ? (
        <CaveatNotice tone="info">
          {t(`posture.capabilityHint.${cap.key}`)}
        </CaveatNotice>
      ) : null}
      {cap.refs && cap.refs.length > 0 ? (
        <ul className="flex flex-wrap gap-2 pt-0.5">
          {cap.refs.map((ref, i) => (
            <li key={`${ref.kind}-${i}`}>
              <Badge variant="neutral" className="font-mono">
                {ref.kind}: {ref.detail}
              </Badge>
            </li>
          ))}
        </ul>
      ) : null}
    </li>
  )
}

// --- control row (expandable) ------------------------------------------------

export function ControlRow({ control }: { control: ControlAssessment }) {
  const { t } = useTranslation('compliance')
  const [open, setOpen] = useState(false)
  const hasCaps = control.capabilities.length > 0
  return (
    <div className="flex flex-col gap-2 rounded-lg border border-border bg-surface p-4 shadow-sm">
      <div className="flex items-start justify-between gap-3">
        <button
          type="button"
          onClick={() => setOpen((v) => !v)}
          className="flex min-w-0 items-start gap-2 text-left outline-none focus-visible:ring-2 focus-visible:ring-ring"
          aria-expanded={open}
          aria-label={open ? t('posture.collapse') : t('posture.expand')}
        >
          {open ? (
            <ChevronDown
              className="mt-0.5 size-4 shrink-0 text-muted-foreground"
              aria-hidden
            />
          ) : (
            <ChevronRight
              className="mt-0.5 size-4 shrink-0 text-muted-foreground"
              aria-hidden
            />
          )}
          <span className="min-w-0">
            <span className="flex items-center gap-2">
              <span className="font-mono text-xs text-muted-foreground">
                {control.control_id}
              </span>
              <span className="text-sm font-medium text-foreground">
                {control.title}
              </span>
            </span>
            <span className="mt-1 block text-xs leading-relaxed text-muted-foreground">
              {control.requirement}
            </span>
          </span>
        </button>
        <ControlStatusBadge status={control.status} />
      </div>

      {/* The honest coverage caveat is ALWAYS rendered when present. */}
      {control.note ? (
        <CaveatNotice tone="warning">{control.note}</CaveatNotice>
      ) : null}

      {open ? (
        <div className="mt-1 flex flex-col gap-2 pl-6">
          <p className="text-xs text-muted-foreground">
            <span className="font-medium text-foreground">
              {t('posture.criterion')}:
            </span>{' '}
            {control.criterion}
          </p>
          <span className="text-xs font-medium tracking-wide text-muted-foreground uppercase">
            {t('posture.capabilities')}
          </span>
          {hasCaps ? (
            <ul className="flex flex-col gap-2">
              {control.capabilities.map((cap) => (
                <CapabilityRow key={cap.key} cap={cap} />
              ))}
            </ul>
          ) : (
            <p className="text-xs text-muted-foreground">
              {t('posture.noCapabilities')}
            </p>
          )}
        </div>
      ) : null}
    </div>
  )
}

export function ControlList({ controls }: { controls: ControlAssessment[] }) {
  return (
    <div className="flex flex-col gap-3">
      {controls.map((c) => (
        <ControlRow key={c.control_id} control={c} />
      ))}
    </div>
  )
}

// --- gaps ("what to fix") ----------------------------------------------------

/** Severity order for the gap list: the loudest first. `by_design` is included by
 *  the contract but is the calmest — it sorts last among the open items. */
const GAP_ORDER: Record<string, number> = {
  gap: 0,
  unmapped: 1,
  partial: 2,
  by_design: 3,
}

export function GapList({ gaps }: { gaps: ControlAssessment[] }) {
  const { t } = useTranslation('compliance')
  const ordered = useMemo(
    () =>
      [...gaps].sort(
        (a, b) => (GAP_ORDER[a.status] ?? 9) - (GAP_ORDER[b.status] ?? 9),
      ),
    [gaps],
  )
  return (
    <div className="flex flex-col gap-3">
      {ordered.map((g) => (
        <div
          key={g.control_id}
          className="flex flex-col gap-2 rounded-lg border border-border bg-surface p-4 shadow-sm"
        >
          <div className="flex items-start justify-between gap-3">
            <div className="min-w-0">
              <div className="flex items-center gap-2">
                <span className="font-mono text-xs text-muted-foreground">
                  {g.control_id}
                </span>
                <span className="text-sm font-medium text-foreground">
                  {g.title}
                </span>
              </div>
              <p className="mt-1 text-xs leading-relaxed text-muted-foreground">
                {g.criterion}
              </p>
            </div>
            <ControlStatusBadge status={g.status} />
          </div>
          {g.note ? <CaveatNotice tone="warning">{g.note}</CaveatNotice> : null}
          <div className="flex flex-col gap-1">
            <span className="text-xs font-medium tracking-wide text-muted-foreground uppercase">
              {t('gaps.missing')}
            </span>
            {g.missing_capabilities && g.missing_capabilities.length > 0 ? (
              <ul className="flex flex-wrap gap-2">
                {g.missing_capabilities.map((key) => (
                  <li key={key}>
                    <Badge variant="warning" className="font-mono">
                      {humanize(key)}
                    </Badge>
                  </li>
                ))}
              </ul>
            ) : (
              <p className="text-xs text-muted-foreground">
                {t('gaps.noMissing')}
              </p>
            )}
          </div>
        </div>
      ))}
    </div>
  )
}

// --- evidence package card ---------------------------------------------------

const EXPORT_FORMATS: EvidenceExportFormat[] = ['json', 'csv', 'oscal']

export function EvidenceCard({
  pkg,
  canExport,
  exportBusy,
  onExport,
}: {
  pkg: EvidencePackage
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
              <span className="text-sm font-medium text-foreground">
                {pkg.framework} · {pkg.framework_version}
              </span>
            </div>
            <p className="text-xs text-muted-foreground">
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
          <span className="font-mono text-xs text-muted-foreground">
            {t('evidence.checked', { count: pkg.integrity_checked })}
          </span>
        </div>

        {pkg.scope_note ? (
          <p className="text-xs text-muted-foreground">
            {t('evidence.scope')}:{' '}
            <span className="text-foreground">{pkg.scope_note}</span>
          </p>
        ) : null}

        {canExport ? (
          <div className="flex flex-col gap-2 border-t border-border pt-3">
            <div className="flex flex-wrap items-center gap-2">
              <span className="text-xs font-medium tracking-wide text-muted-foreground uppercase">
                {t('export.label')}
              </span>
              {EXPORT_FORMATS.map((fmt) => (
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
            {/* OSCAL honesty: the OSCAL finding status is a 2-value enum; a by_design
                control rides as not-satisfied with the real status in reason. */}
            <CaveatNotice tone="info">{t('export.oscalHint')}</CaveatNotice>
          </div>
        ) : null}
      </div>
    </SectionCard>
  )
}

// --- OSCAL export preview (honest finding status) ----------------------------

/** OSCAL maps the product's 5-way status onto a 2-value finding enum
 *  {satisfied, not-satisfied} (oscal.go). This preview shows BOTH: the OSCAL state AND
 *  the real product status from `status.reason`, so a by_design control reads
 *  "not-satisfied (by_design)" and is NEVER laundered to "satisfied" (docs/SECURITY-HARDENING.md).*/
export function OscalFindingsPreview({ oscal }: { oscal: OscalExport }) {
  const { t } = useTranslation('compliance')
  const findings = oscal['assessment-results'].results.flatMap(
    (r) => r.findings,
  )
  return (
    <div className="flex flex-col gap-2">
      <div className="flex items-center gap-2">
        <ScrollText className="size-4 text-muted-foreground" aria-hidden />
        <span className="text-sm font-medium text-foreground">
          {t('export.oscalPreview', { version: oscal.oscal_version })}
        </span>
      </div>
      <ul className="flex flex-col gap-1.5">
        {findings.map((f) => {
          const laundered =
            f.target.status.state === 'satisfied' &&
            f.target.status.reason !== 'satisfied'
          return (
            <li
              key={f.uuid}
              className="flex flex-wrap items-center justify-between gap-2 rounded-md border border-border bg-muted/40 px-3 py-2"
            >
              <span className="font-mono text-xs text-foreground">
                {f.target['target-id']}
              </span>
              <span className="flex items-center gap-2">
                <Badge
                  variant={
                    f.target.status.state === 'satisfied'
                      ? 'success'
                      : 'neutral'
                  }
                  className="font-mono"
                >
                  {f.target.status.state}
                </Badge>
                {/* The real product status — visually distinct, never collapsed. */}
                <ControlStatusBadge status={f.target.status.reason} />
                {laundered ? (
                  <Badge variant="danger">{t('export.laundered')}</Badge>
                ) : null}
              </span>
            </li>
          )
        })}
      </ul>
      <DisclaimerNote text={oscal.disclaimer} />
    </div>
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
  return (
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
      {s.autonomous ? (
        <Badge variant="warning">{t('risk.signals.autonomous')}</Badge>
      ) : null}
      {s.scheduled ? (
        <Badge variant="outline">{t('risk.signals.scheduled')}</Badge>
      ) : null}
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
            <span className="font-mono text-xs text-foreground">
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
          <span className="text-xs text-muted-foreground">
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
            <div className="font-mono text-sm font-medium text-foreground">
              {region.region}
            </div>
            <div className="text-xs text-muted-foreground">
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
          <p className="text-xs text-muted-foreground">
            {t('residency.dataClasses')}:{' '}
            <span className="font-mono text-foreground">
              {region.data_classes.join(', ')}
            </span>
          </p>
        ) : null}

        {region.note ? (
          <CaveatNotice tone="warning">{region.note}</CaveatNotice>
        ) : null}

        <p className="text-xs text-muted-foreground">
          {t('residency.lastChecked')}:{' '}
          {formatDateTime(region.last_checked, i18n.language)} ·{' '}
          {t('residency.attestedBy')}:{' '}
          <span className="font-mono">{region.attested_by}</span>
        </p>
      </div>
    </SectionCard>
  )
}
