// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Security presentational pieces — PURE: they take the module's data as props and
// render it (findings table, guardrail verdict, enforcement posture, anomaly queue,
// integrity panels, evidence timeline). No fetching, no auth — so they are trivially
// testable with fixtures and reused by the container. They encode the honesty rules:
// a `detail_hash` is a FINGERPRINT (HashChip), never a payload; verdicts are detective
// unless governed; an `approximate` anomaly is never titled a firm violation; an
// unavailable checkpoint key reads calm, not as a failure.
import { useMemo } from 'react'
import type { TFunction } from 'i18next'
import { Activity, ShieldCheck } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { DataTable, type TableColumn } from '@/components/data/data-table'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { EmptyState } from '@/components/ui/empty-state'
import {
  CaveatNotice,
  EvidenceTimeline,
  HashChip,
  IntegrityBadge,
  MetricStat,
  SeamBadge,
  SectionCard,
  SeverityBadge,
  StatGrid,
  VerdictBadge,
} from '@/features/_intel'
import { formatDateTime, formatInt, humanize } from '@/lib/format'
import type {
  Anomaly,
  CaseIntegrity,
  Detection,
  EnforcementEntry,
  Finding,
  FindingStatus,
  InspectResult,
  IntegrityVerify,
  SafetyPostureProvider,
  TimelineEntry,
} from './types'

const FINDING_STATUS_VARIANT: Record<
  FindingStatus,
  'neutral' | 'warning' | 'success' | 'outline'
> = {
  open: 'warning',
  triaged: 'neutral',
  resolved: 'success',
  dismissed: 'outline',
}

// --- 1a. safety posture surfaces --------------------------------------

/** SafetyPostureSurfaces renders the per-provider-surface roll-up of the read-first
 *  AI-safety posture: one card per surface (openai.moderation, bedrock.guardrail,
 *  azure.rai_policy…) with its finding count, open count and severity breakdown. It is
 *  pure presentation over the GET /safety-posture roll-up. */
export function SafetyPostureSurfaces({
  providers,
}: {
  providers: SafetyPostureProvider[]
}) {
  const { t } = useTranslation('security')
  if (providers.length === 0) return null
  return (
    <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
      {providers.map((p) => (
        <div
          key={p.subject_kind}
          className="flex flex-col gap-2 rounded-lg border border-border bg-surface p-4"
        >
          <div className="flex items-center justify-between gap-2">
            <h3 className="truncate font-mono text-sm text-foreground">
              {p.subject_kind}
            </h3>
            <Badge variant="neutral">{formatInt(p.total)}</Badge>
          </div>
          <div className="flex flex-wrap items-center gap-2">
            {Object.keys(p.by_severity)
              .sort()
              .map((sev) => (
                <span key={sev} className="flex items-center gap-1">
                  <SeverityBadge severity={sev} />
                  <span className="text-xs text-muted-foreground">
                    {formatInt(p.by_severity[sev])}
                  </span>
                </span>
              ))}
          </div>
          <span className="text-xs text-muted-foreground">
            {t('safetyPosture.columns.open')}: {formatInt(p.open)}
          </span>
        </div>
      ))}
    </div>
  )
}

// subjectKindLabel turns a finding's subject kind into something an operator can read,
// falling back to the raw value for any kind this console has never heard of.
//
// THE DOTS ARE FLATTENED, and that is the point. `subject_kind` is an unconstrained
// producer string, and i18next treats `.` as a key separator — so interpolating it
// directly made `local.residency` resolve through a NESTED locale object, and the bare
// kind `local` then resolved to that object's PARENT and rendered i18next's own error
// text ("returned an object instead of string") straight into the table. Measured with
// this repo's i18next during the contrast. One flat key per kind cannot nest, so
// the class is gone rather than patched.
//
// A kind that literally contains `local_residency` would map to the same key; both
// would render the same label, so the collision is harmless.
function subjectKindLabel(kind: string, t: TFunction): string {
  return t(`findings.subjectKinds.${kind.replace(/\./g, '_')}`, {
    defaultValue: kind,
  })
}

// --- 1. findings table -------------------------------------------------------

export function FindingsTable({
  findings,
  canTriage = false,
  onTriage,
}: {
  findings: Finding[]
  /** When true, an inline triage control is shown (gated by the container). */
  canTriage?: boolean
  onTriage?: (finding: Finding) => void
}) {
  const { t, i18n } = useTranslation('security')
  const columns = useMemo<TableColumn<Finding>[]>(() => {
    const base: TableColumn<Finding>[] = [
      {
        accessorKey: 'severity',
        header: t('findings.columns.severity'),
        cell: ({ row }) => <SeverityBadge severity={row.original.severity} />,
      },
      {
        accessorKey: 'kind',
        header: t('findings.columns.kind'),
        cell: ({ row }) => (
          <Badge variant="outline">
            {t(`findings.kind.${row.original.kind}`, {
              defaultValue: humanize(row.original.kind),
            })}
          </Badge>
        ),
      },
      {
        accessorKey: 'source',
        header: t('findings.columns.source'),
        // ⛔ LA COLUMNA QUE CRECÍA CON EL DATO. Medido a 1440 con hallazgos de kill
        // switch: `governance.killswitch` la llevó a 175 px, la tabla a 1133 dentro
        // de una tarjeta de 1116, y ACTIONS quedó fuera —«ACTIO» y TRES de seis
        // botones «Triage» cortados—. El origen es un identificador: se lee entero
        // en el `title` y no necesita empujar a la columna que se ACCIONA.
        // ⛔ Y NO 150: `data-table.tsx:643-645` aplica la anchura sólo si
        // `getSize() !== 150`, porque 150 es el DEFECTO de TanStack y ahí hace de
        // centinela de «sin anchura». Pedir exactamente 150 es indistinguible de no
        // pedir nada — lo comprobé pidiéndolo: la columna se quedó en sus 175 px y
        // la tabla en 1133, idéntica byte a byte a antes del cambio.
        size: 148,
        cell: ({ row }) => (
          <span
            className="block max-w-[124px] truncate font-mono text-xs text-muted-foreground"
            title={row.original.source}
          >
            {row.original.source}
          </span>
        ),
      },
      {
        accessorKey: 'title',
        header: t('findings.columns.title'),
        // ⛔ LA ELIPSIS YA ESTABA Y NO PODÍA DISPARARSE. Medido en Chrome sobre el
        // `dist`: los dos `<p>` llevan `truncate` —`getComputedStyle` confirma
        // `text-overflow: ellipsis`, `overflow: hidden`, `white-space: nowrap`— pero
        // NADA acotaba su ancho, así que con un título largo el `<p>` crecía a
        // 1136 px, `scrollWidth == clientWidth` y **no había desbordamiento que
        // truncar**. La columna estiraba la tabla a 1189 px dentro de un contenedor
        // de 1116, y el corte que se ve en la captura lo hacía el BORDE de la tabla,
        // no la celda: por eso salía sin puntos suspensivos.
        //
        // `truncate` sólo produce elipsis cuando algo limita la anchura. Esto la
        // limita, y con eso la maquinaria que ya existía empieza a funcionar.
        size: 420,
        cell: ({ row }) => (
          <div className="min-w-0 max-w-[420px]">
            <p className="truncate text-sm text-foreground">
              {row.original.title}
            </p>
            <p className="truncate text-xs text-muted-foreground">
              {/*the subject KIND is painted, not dumped. A connector may report
                  a posture about something the raw identifier does not explain: the
                  local connector emits `local.residency` per model held in memory
                  right now (connectors/local/local.go:34), and a row reading
                  "local.residency: llama3" told the operator nothing. Unknown kinds
                  still fall back to the raw value, so a connector this console has
                  never heard of degrades to what it always showed — never to blank.
                  The raw kind stays reachable in the title, because it is what the
                  subject_kind filter takes. */}
              <span
                className="font-mono"
                title={`${row.original.subject_kind}: ${row.original.subject_ref}`}
              >
                {subjectKindLabel(row.original.subject_kind, t)}:{' '}
                {row.original.subject_ref}
              </span>
              {' · '}
              {formatDateTime(row.original.occurred_at, i18n.language)}
            </p>
          </div>
        ),
      },
      {
        accessorKey: 'detail_hash',
        header: t('findings.columns.evidence'),
        // La huella ya se pinta recortada (`head…tail`), así que su columna no
        // necesita más de esto — y lo que sobra se lo queda ACTIONS.
        size: 120,
        // The evidence is a FINGERPRINT — there is no payload to expand (docs/SECURITY-HARDENING.md).
        cell: ({ row }) => (
          <HashChip
            hash={row.original.detail_hash}
            label={t('findings.fingerprint')}
          />
        ),
      },
      {
        accessorKey: 'status',
        header: t('findings.columns.status'),
        cell: ({ row }) => (
          <Badge
            variant={FINDING_STATUS_VARIANT[row.original.status] ?? 'neutral'}
          >
            {t(`findings.status.${row.original.status}`, {
              defaultValue: humanize(row.original.status),
            })}
          </Badge>
        ),
      },
    ]
    if (canTriage) {
      base.push({
        id: 'triage',
        header: t('findings.columns.actions'),
        cell: ({ row }) => (
          <Button
            variant="outline"
            size="sm"
            onClick={() => onTriage?.(row.original)}
          >
            {t('findings.triage.action')}
          </Button>
        ),
      })
    }
    return base
  }, [t, i18n.language, canTriage, onTriage])

  return (
    <DataTable<Finding>
      columns={columns}
      data={findings}
      getRowId={(r) => r.id}
      searchable
      searchPlaceholder={t('findings.search')}
      empty={
        <EmptyState
          title={t('empty.findings.title')}
          description={t('empty.findings.description')}
        />
      }
    />
  )
}

// --- 2. guardrail inspect verdict --------------------------------------------

function DetectionRow({ detection }: { detection: Detection }) {
  const { t } = useTranslation('security')
  return (
    <div className="flex flex-col gap-1.5 rounded-md border border-border bg-surface p-3">
      <div className="flex flex-wrap items-center gap-2">
        <SeverityBadge severity={detection.severity} />
        <Badge variant="outline">
          {t(`guardrails.class.${detection.class}`, {
            defaultValue: humanize(detection.class),
          })}
        </Badge>
        {detection.enforced ? (
          <Badge variant="danger">{t('guardrails.wouldBlock')}</Badge>
        ) : (
          <Badge variant="neutral">{t('guardrails.detectOnly')}</Badge>
        )}
      </div>
      <p className="text-sm font-medium text-foreground">{detection.title}</p>
      <p className="font-mono text-xs text-muted-foreground">
        {detection.rule}
      </p>
      {detection.excerpt ? (
        // Already redacted — a label/placeholder, never the secret (docs/SECURITY-HARDENING.md).
        <p className="rounded bg-muted px-2 py-1 font-mono text-xs text-muted-foreground">
          {detection.excerpt}
        </p>
      ) : null}
      <div className="flex flex-wrap gap-1.5">
        {detection.owasp ? (
          <Badge variant="info">{`OWASP ${detection.owasp}`}</Badge>
        ) : null}
        {detection.atlas ? (
          <Badge variant="info">{`ATLAS ${detection.atlas}`}</Badge>
        ) : null}
      </div>
    </div>
  )
}

export function GuardrailVerdict({ result }: { result: InspectResult }) {
  const { t } = useTranslation('security')
  const enforced = result.enforcement === 'enforced'
  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center gap-2">
        <VerdictBadge verdict={result.verdict} />
        <Badge variant={enforced ? 'accent' : 'neutral'}>
          {t(`guardrails.enforcement.${result.enforcement}`, {
            defaultValue: humanize(result.enforcement),
          })}
        </Badge>
        {result.finding_ids.length > 0 ? (
          <span className="text-xs text-muted-foreground">
            {t('guardrails.findingsRecorded', {
              count: result.finding_ids.length,
            })}
          </span>
        ) : null}
      </div>
      {/* Detective plane is the default — say so unless governance enabled blocking. */}
      {!enforced ? (
        <CaveatNotice>{t('guardrails.detectiveNote')}</CaveatNotice>
      ) : null}
      {result.detections.length === 0 ? (
        <p className="text-sm text-muted-foreground">{t('guardrails.clean')}</p>
      ) : (
        <div className="flex flex-col gap-2">
          {result.detections.map((d, i) => (
            <DetectionRow key={`${d.class}-${d.rule}-${i}`} detection={d} />
          ))}
        </div>
      )}
    </div>
  )
}

// --- 3. enforcement posture --------------------------------------------------

export function EnforcementTable({
  entries,
  canAdmin = false,
  onToggle,
}: {
  entries: EnforcementEntry[]
  canAdmin?: boolean
  onToggle?: (entry: EnforcementEntry) => void
}) {
  const { t, i18n } = useTranslation('security')
  // An empty posture is the safe default: fully detective.
  if (entries.length === 0) {
    return (
      <div className="flex flex-col gap-3">
        <CaveatNotice tone="info">
          {t('enforcement.fullyDetective')}
        </CaveatNotice>
      </div>
    )
  }

  const columns: TableColumn<EnforcementEntry>[] = [
    {
      accessorKey: 'class',
      header: t('enforcement.columns.class'),
      cell: ({ row }) => (
        <span className="font-mono text-sm text-foreground">
          {row.original.class === '*'
            ? t('enforcement.wildcard')
            : t(`guardrails.class.${row.original.class}`, {
                defaultValue: humanize(row.original.class),
              })}
        </span>
      ),
    },
    {
      accessorKey: 'enabled',
      header: t('enforcement.columns.posture'),
      cell: ({ row }) =>
        row.original.enabled ? (
          <Badge variant="accent">{t('enforcement.enforced')}</Badge>
        ) : (
          <Badge variant="neutral">{t('enforcement.detective')}</Badge>
        ),
    },
    {
      accessorKey: 'min_severity',
      header: t('enforcement.columns.minSeverity'),
      cell: ({ row }) => <SeverityBadge severity={row.original.min_severity} />,
    },
    {
      accessorKey: 'governed',
      header: t('enforcement.columns.governance'),
      cell: ({ row }) =>
        !row.original.enabled ? (
          <span className="text-xs text-muted-foreground">—</span>
        ) : row.original.governed ? (
          <Badge variant="success">{t('enforcement.governed')}</Badge>
        ) : (
          // Enabled WITHOUT governance — visible warning, never hidden (docs/SECURITY-HARDENING.md).
          <Badge variant="warning">{t('enforcement.ungoverned')}</Badge>
        ),
    },
    {
      accessorKey: 'updated_at',
      header: t('enforcement.columns.updated'),
      cell: ({ row }) => (
        <span className="text-xs text-muted-foreground">
          {formatDateTime(row.original.updated_at, i18n.language)}
        </span>
      ),
    },
  ]

  if (canAdmin) {
    columns.push({
      id: 'admin',
      header: t('enforcement.columns.actions'),
      cell: ({ row }) => (
        <Button
          variant="outline"
          size="sm"
          onClick={() => onToggle?.(row.original)}
        >
          {t('enforcement.change')}
        </Button>
      ),
    })
  }

  return (
    <div className="flex flex-col gap-3">
      <CaveatNotice tone="info">
        {t('enforcement.defaultDetectiveNote')}
      </CaveatNotice>
      <DataTable<EnforcementEntry>
        columns={columns}
        data={entries}
        getRowId={(r) => r.class}
        empty={
          <EmptyState
            title={t('empty.enforcement.title')}
            description={t('empty.enforcement.description')}
          />
        }
      />
    </div>
  )
}

// --- 4. anomalies (priority-ordered) -----------------------------------------

export function AnomalyCard({ anomaly }: { anomaly: Anomaly }) {
  const { t, i18n } = useTranslation('security')
  // `approximate` confidence = unreconciled drift — never titled a firm violation.
  const approximate = anomaly.confidence === 'approximate'
  return (
    <div className="flex flex-col gap-2 rounded-lg border border-border bg-surface p-4 shadow-sm">
      <div className="flex items-start justify-between gap-3">
        <div className="flex min-w-0 flex-col gap-1">
          <div className="flex flex-wrap items-center gap-2">
            <SeverityBadge severity={anomaly.severity} />
            <Badge variant="outline">
              {t(`anomalies.kind.${anomaly.kind}`, {
                defaultValue: humanize(anomaly.kind),
              })}
            </Badge>
            {approximate ? (
              <Badge variant="warning">{t('anomalies.approximate')}</Badge>
            ) : null}
          </div>
          <p className="text-sm font-medium text-foreground">
            {approximate
              ? t('anomalies.suspectedTitle', { title: anomaly.title })
              : anomaly.title}
          </p>
          <p className="text-xs text-muted-foreground">
            <span className="font-mono">
              {anomaly.subject_kind}: {anomaly.subject_ref}
            </span>
            {' · '}
            {formatDateTime(anomaly.occurred_at, i18n.language)}
          </p>
          {approximate ? (
            <p className="text-xs text-muted-foreground">
              {t('anomalies.unreconciledNote')}
            </p>
          ) : null}
        </div>
        <div className="shrink-0 text-right">
          <div className="text-xs text-muted-foreground">
            {t('anomalies.priority')}
          </div>
          <div className="font-display text-lg font-semibold tabular-nums text-foreground">
            {formatInt(anomaly.priority, i18n.language)}
          </div>
        </div>
      </div>
    </div>
  )
}

export function AnomalyList({ anomalies }: { anomalies: Anomaly[] }) {
  const { t } = useTranslation('security')
  return (
    <div className="flex flex-col gap-3">
      <p className="text-xs text-muted-foreground">
        {t('anomalies.orderNote')}
      </p>
      {/* Respect the backend order (priority desc) — do not re-sort here. */}
      {anomalies.map((a, i) => (
        <AnomalyCard key={`${a.kind}-${a.subject_ref}-${i}`} anomaly={a} />
      ))}
    </div>
  )
}

// --- 6. integrity panel ------------------------------------------------------

/** The two-verdict integrity panel (chain + checkpoints). Drives both the global
 *  /integrity/verify view and the per-case integrity object. */
export function IntegrityPanel({ integrity }: { integrity: IntegrityVerify }) {
  const { t } = useTranslation('security')
  return (
    <div className="flex flex-col gap-4">
      <StatGrid>
        <MetricStat
          icon={<ShieldCheck />}
          label={t('integrity.chain')}
          value={
            // ⛔ UN LEDGER VACÍO NO ES UNA CADENA ROTA, y salía ROJO. `Verify`
            // (`core/internal/store/sqlstore/audit.go:623-629`) deja `OK: false` a propósito
            // cuando `Checked == 0`, y lo razona: «An empty range proves nothing. In particular,
            // post-erasure and compliance callers must not be able to turn an absent ledger into
            // "verified" evidence through vacuous truth». El motor tiene razón al decir `false`;
            // lo que no se puede es pintarlo como FALLO.
            //
            // Sin esta rama, una instalación recién levantada abre la vista de seguridad y lo
            // primero que lee es que su cadena de evidencia está rota. Es exactamente el rojo que
            // la propia insignia documenta como «the red that teaches operators to ignore red» —
            // y la salida para evitarlo (`pending`) ya existía y ya se usaba para los
            // checkpoints, dos tarjetas más abajo. Sólo a la cadena no se le había aplicado.
            //
            // Se clasifica por `chain_checked`, el hecho ESTRUCTURAL, no por
            // `chain_reason === 'no-events'`: una razón es prosa y cambia; el contador no.
            <IntegrityBadge
              ok={integrity.chain_ok}
              pending={integrity.chain_checked === 0}
              reason={integrity.chain_reason}
            />
          }
          caption={
            integrity.chain_checked === 0
              ? t('integrity.chainEmptyNote')
              : t('integrity.chainChecked', {
                  count: integrity.chain_checked,
                })
          }
          tone={
            integrity.chain_ok || integrity.chain_checked === 0
              ? 'default'
              : 'danger'
          }
        />
        <MetricStat
          icon={<ShieldCheck />}
          label={t('integrity.checkpoints')}
          value={
            // Three calm-vs-loud answers, none of them derivable from
            // checkpoints_ok alone (it is false for two of them):
            //   checkpoints_verified=false => "unavailable" (no key wired)
            //   checkpoint_status=pending  => nothing attested YET (young ledger)
            //   otherwise                  => the engine's verdict, red if bad.
            integrity.checkpoints_verified ? (
              <IntegrityBadge
                ok={integrity.checkpoints_ok}
                pending={integrity.checkpoint_status === 'pending'}
                reason={integrity.checkpoint_reason}
              />
            ) : (
              <IntegrityBadge unavailable />
            )
          }
          caption={
            integrity.checkpoints_verified
              ? integrity.checkpoint_status === 'pending'
                ? t('integrity.checkpointsPendingNote')
                : t('integrity.checkpointsCount', {
                    count: integrity.checkpoints,
                  })
              : t('integrity.signingUnavailable')
          }
        />
        <MetricStat
          icon={<Activity />}
          label={t('integrity.attestedSeq')}
          value={formatInt(integrity.attested_seq)}
          caption={t('integrity.headSeq', {
            seq: formatInt(integrity.head_seq),
          })}
        />
      </StatGrid>
      {!integrity.checkpoints_verified ? (
        <SeamBadge label={t('integrity.signingSeam')} />
      ) : null}
      <CaveatNotice tone="info">{t('integrity.reverifyNote')}</CaveatNotice>
    </div>
  )
}

// --- 5. case integrity (the frozen-at-open subset) ---------------------------

export function CaseIntegrityPanel({
  integrity,
}: {
  integrity: CaseIntegrity
}) {
  const { t } = useTranslation('security')
  return (
    <div className="flex flex-wrap items-center gap-3 rounded-lg border border-border bg-muted px-4 py-3">
      <div className="flex flex-col gap-1">
        <span className="text-xs text-muted-foreground">
          {t('integrity.chain')}
        </span>
        <IntegrityBadge
          ok={integrity.chain_ok}
          reason={integrity.chain_reason}
        />
      </div>
      <div className="flex flex-col gap-1">
        <span className="text-xs text-muted-foreground">
          {t('integrity.checkpoints')}
        </span>
        {/* Same three answers as IntegrityPanel: a case opened on a ledger that
            has not been attested yet is "pending", never a red failure. */}
        {integrity.checkpoints_verified ? (
          <IntegrityBadge
            ok={integrity.checkpoints_ok}
            pending={integrity.checkpoint_status === 'pending'}
          />
        ) : (
          <IntegrityBadge unavailable />
        )}
      </div>
      <div className="flex flex-col gap-1">
        <span className="text-xs text-muted-foreground">
          {t('integrity.attestedSeq')}
        </span>
        <span className="font-mono text-sm tabular-nums text-foreground">
          {formatInt(integrity.attested_seq)} / {formatInt(integrity.head_seq)}
        </span>
      </div>
    </div>
  )
}

// --- 5. forensic timeline ----------------------------------------------------

export function ForensicTimeline({ events }: { events: TimelineEntry[] }) {
  const { t } = useTranslation('security')
  return (
    <SectionCard
      title={t('forensics.timeline')}
      description={t('forensics.timelineNote')}
    >
      {/* Each event carries seq · hash · prev_hash so a reader can re-verify. */}
      <EvidenceTimeline events={events} />
    </SectionCard>
  )
}
