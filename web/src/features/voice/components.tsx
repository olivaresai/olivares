// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Voice presentational pieces — PURE (data in, UI out). Contract-critical honesty:
// the transcript is rendered ONLY as a HashChip (fingerprint of an external locator,
// never text/audio); latency is shown as BOTH avg and max (never a fabricated
// p50/p95); an ungoverned open is FLAGGED, not hidden. No fetching, no auth — so they
// are trivially testable with fixtures and reused by the container.
import { useMemo } from 'react'
import { AudioLines, Gauge, ShieldCheck } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { DataTable, type TableColumn } from '@/components/data/data-table'
import { StatusBadge } from '@/components/data/badges'
import { Badge, type BadgeVariant } from '@/components/ui/badge'
import { EmptyState } from '@/components/ui/empty-state'
import { CategoryBarChart } from '@/components/charts'
import { AccessibleChart } from '@/components/data/accessible-chart'
import { HashChip, MetricStat, SectionCard, StatGrid } from '@/features/_intel'
import {
  formatDuration,
  formatInt,
  formatLatency,
  formatPercent,
  formatRelativeTime,
} from '@/lib/format'
import type {
  SessionState,
  VoiceDecision,
  VoicePolicy,
  VoiceSession,
} from './types'

// --- headline stats ----------------------------------------------------------

/** Live-session count, avg latency across sessions, and governed share. All three
 *  are computed from the rows ALREADY returned — no fabricated aggregate. */
export function VoiceStats({ sessions }: { sessions: VoiceSession[] }) {
  const { t } = useTranslation('voice')
  const live = sessions.filter((s) => s.state === 'live').length
  const governed = sessions.filter((s) => s.governed).length
  const avgLatency =
    sessions.length > 0
      ? Math.round(
          sessions.reduce((sum, s) => sum + s.latency_avg_ms, 0) /
            sessions.length,
        )
      : null
  const governedPct =
    sessions.length > 0 ? (governed / sessions.length) * 100 : null
  const allGoverned = governed === sessions.length

  return (
    <StatGrid>
      <MetricStat
        icon={<AudioLines />}
        label={t('stats.live')}
        value={formatInt(live)}
        caption={t('stats.liveCaption', { total: formatInt(sessions.length) })}
        tone={live > 0 ? 'success' : 'default'}
      />
      <MetricStat
        icon={<Gauge />}
        label={t('stats.avgLatency')}
        value={formatLatency(avgLatency)}
        caption={t('stats.avgLatencyCaption')}
      />
      <MetricStat
        icon={<ShieldCheck />}
        label={t('stats.governed')}
        value={governedPct === null ? '—' : formatPercent(governedPct)}
        caption={t('stats.governedCaption', {
          governed: formatInt(governed),
          total: formatInt(sessions.length),
        })}
        tone={allGoverned ? 'success' : 'warning'}
      />
    </StatGrid>
  )
}

// --- governed flag -----------------------------------------------------------

/** Governed = the open was ungoverned is flagged (anti-evasion), never
 *  hidden. The deeper findings (voice_ungoverned_open) live in the security plane. */
function GovernedBadge({ governed }: { governed: boolean }) {
  const { t } = useTranslation('voice')
  return governed ? (
    <Badge variant="success">{t('sessions.governed')}</Badge>
  ) : (
    <Badge variant="warning">{t('sessions.ungoverned')}</Badge>
  )
}

const STATE_VARIANT: Record<SessionState, string> = {
  live: 'running',
  idle: 'idle',
  ended: 'inactive',
}

// --- sessions table ----------------------------------------------------------

export function SessionsTable({
  sessions,
  onRowClick,
}: {
  sessions: VoiceSession[]
  onRowClick?: (s: VoiceSession) => void
}) {
  const { t, i18n } = useTranslation('voice')
  const columns = useMemo<TableColumn<VoiceSession>[]>(
    () => [
      {
        accessorKey: 'state',
        header: t('sessions.columns.state'),
        cell: ({ row }) => (
          <StatusBadge status={STATE_VARIANT[row.original.state]} />
        ),
      },
      {
        accessorKey: 'agent_ref',
        header: t('sessions.columns.agent'),
        cell: ({ row }) => (
          <div className="flex flex-col">
            <span className="font-mono text-xs text-foreground">
              {row.original.agent_ref}
            </span>
            <span className="text-[11px] text-muted-foreground">
              {row.original.language_code}
            </span>
          </div>
        ),
      },
      {
        id: 'model',
        header: t('sessions.columns.model'),
        cell: ({ row }) => (
          <span className="font-mono text-xs text-muted-foreground">
            {row.original.provider_ref} / {row.original.model_ref}
          </span>
        ),
      },
      {
        accessorKey: 'turn_count',
        header: t('sessions.columns.turns'),
        cell: ({ row }) => (
          <span className="font-mono text-xs tabular-nums text-muted-foreground">
            {formatInt(row.original.turn_count, i18n.language)}{' '}
            <span className="text-[11px]">
              (
              {t('sessions.turnsBreakdown', {
                user: formatInt(row.original.user_turns, i18n.language),
                agent: formatInt(row.original.agent_turns, i18n.language),
              })}
              )
            </span>
          </span>
        ),
      },
      {
        accessorKey: 'duration_ms',
        header: t('sessions.columns.duration'),
        cell: ({ row }) => (
          <span className="font-mono text-xs tabular-nums text-muted-foreground">
            {formatDuration(row.original.duration_ms)}
          </span>
        ),
      },
      {
        // Honest latency: BOTH avg and max — never a fabricated p50/p95.
        id: 'latency',
        header: t('sessions.columns.latency'),
        cell: ({ row }) => (
          <div className="flex flex-col font-mono text-xs tabular-nums">
            <span className="text-foreground">
              {t('sessions.latencyAvg', {
                value: formatLatency(row.original.latency_avg_ms),
              })}
            </span>
            <span className="text-muted-foreground">
              {t('sessions.latencyMax', {
                value: formatLatency(row.original.latency_max_ms),
              })}
            </span>
          </div>
        ),
      },
      {
        accessorKey: 'governed',
        header: t('sessions.columns.governed'),
        cell: ({ row }) => <GovernedBadge governed={row.original.governed} />,
      },
      {
        // The transcript is ONLY ever a fingerprint — never text/audio (the HashChip
        // tooltip already says so). It proves a transcript exists externally.
        accessorKey: 'transcript_ref_hash',
        header: t('sessions.columns.transcript'),
        cell: ({ row }) => (
          <HashChip
            hash={row.original.transcript_ref_hash}
            label={t('sessions.transcriptLabel')}
          />
        ),
      },
      {
        accessorKey: 'last_event_at',
        header: t('sessions.columns.lastEvent'),
        cell: ({ row }) => (
          <span className="text-xs text-muted-foreground">
            {formatRelativeTime(row.original.last_event_at, i18n.language)}
          </span>
        ),
      },
    ],
    [t, i18n.language],
  )
  return (
    <DataTable<VoiceSession>
      columns={columns}
      data={sessions}
      getRowId={(r) => r.id}
      onRowClick={onRowClick}
      searchable
      empty={
        <EmptyState
          title={t('empty.sessions.title')}
          description={t('empty.sessions.description')}
        />
      }
    />
  )
}

// --- latency breakdown (honest: avg + max, no p50/p95) -----------------------

/** A ranked bar of per-session AVERAGE latency. The contract gives avg and max only,
 *  so the chart shows avg and the table beside it carries the max — no invented
 *  percentile. */
export function LatencyByCategory({ sessions }: { sessions: VoiceSession[] }) {
  const { t } = useTranslation('voice')
  const data = sessions
    .slice()
    .sort((a, b) => b.latency_avg_ms - a.latency_avg_ms)
    .slice(0, 8)
    .map((s) => ({ key: s.session_ref, latency_avg_ms: s.latency_avg_ms }))
  // Descending sort puts the slowest session first — name it in the SR summary.
  const summary =
    data.length > 0
      ? t('latency.summary', {
          count: data.length,
          session: data[0].key,
          latency: formatLatency(data[0].latency_avg_ms),
        })
      : t('latency.summaryEmpty')
  const columns = useMemo<TableColumn<(typeof data)[number]>[]>(
    () => [
      {
        accessorKey: 'key',
        header: t('latency.colSession'),
        cell: ({ row }) => (
          <span className="font-mono text-xs">{row.original.key}</span>
        ),
      },
      {
        accessorKey: 'latency_avg_ms',
        header: t('latency.colLatency'),
        cell: ({ row }) => formatLatency(row.original.latency_avg_ms),
      },
    ],
    [t],
  )

  return (
    <SectionCard
      title={t('latency.title')}
      description={t('latency.description')}
    >
      <AccessibleChart
        title={t('latency.title')}
        summary={summary}
        columns={columns}
        data={data}
        getRowId={(d) => d.key}
        empty={
          <EmptyState
            title={t('empty.latencyByCategoryChart.title')}
            description={t('empty.latencyByCategoryChart.description')}
          />
        }
      >
        <CategoryBarChart
          data={data}
          categoryKey="key"
          valueKey="latency_avg_ms"
          valueFormatter={(v) => formatLatency(v)}
          height={Math.max(160, data.length * 30 + 24)}
        />
      </AccessibleChart>
    </SectionCard>
  )
}

// --- policies table ----------------------------------------------------------

export function PoliciesTable({
  policies,
  onRowClick,
}: {
  policies: VoicePolicy[]
  onRowClick?: (p: VoicePolicy) => void
}) {
  const { t, i18n } = useTranslation('voice')
  const allLabel = t('policies.all')
  const columns = useMemo<TableColumn<VoicePolicy>[]>(
    () => [
      {
        accessorKey: 'agent_ref',
        header: t('policies.columns.agent'),
        cell: ({ row }) =>
          row.original.agent_ref === '*' ? (
            <Badge variant="accent">{allLabel}</Badge>
          ) : (
            <span className="font-mono text-xs text-foreground">
              {row.original.agent_ref}
            </span>
          ),
      },
      {
        accessorKey: 'allowed_model_ref',
        header: t('policies.columns.model'),
        cell: ({ row }) => (
          <span className="font-mono text-xs text-muted-foreground">
            {row.original.allowed_model_ref === '*'
              ? allLabel
              : row.original.allowed_model_ref}
          </span>
        ),
      },
      {
        accessorKey: 'allowed_provider_ref',
        header: t('policies.columns.provider'),
        cell: ({ row }) => (
          <span className="font-mono text-xs text-muted-foreground">
            {row.original.allowed_provider_ref === '*'
              ? allLabel
              : row.original.allowed_provider_ref}
          </span>
        ),
      },
      {
        accessorKey: 'max_session_minutes',
        header: t('policies.columns.maxMinutes'),
        cell: ({ row }) => (
          <span className="font-mono text-xs tabular-nums text-muted-foreground">
            {t('policies.minutes', {
              value: formatInt(row.original.max_session_minutes, i18n.language),
            })}
          </span>
        ),
      },
      {
        accessorKey: 'max_latency_ms',
        header: t('policies.columns.maxLatency'),
        cell: ({ row }) => (
          <span className="font-mono text-xs tabular-nums text-muted-foreground">
            {formatLatency(row.original.max_latency_ms)}
          </span>
        ),
      },
      {
        accessorKey: 'set_by',
        header: t('policies.columns.setBy'),
        cell: ({ row }) => (
          <span className="font-mono text-xs text-muted-foreground">
            {row.original.set_by}
          </span>
        ),
      },
      {
        accessorKey: 'updated_at',
        header: t('policies.columns.updated'),
        cell: ({ row }) => (
          <span className="text-xs text-muted-foreground">
            {formatRelativeTime(row.original.updated_at, i18n.language)}
          </span>
        ),
      },
    ],
    [t, i18n.language, allLabel],
  )
  return (
    <DataTable<VoicePolicy>
      columns={columns}
      data={policies}
      getRowId={(r) => r.id}
      onRowClick={onRowClick}
      empty={
        <EmptyState
          title={t('empty.policies.title')}
          description={t('empty.policies.description')}
        />
      }
    />
  )
}

// --- decision ledger ---------------------------------------------------------

// ⛔ EL VARIANTE NO SE DELEGA EN `StatusBadge`, Y NO ES ESTILO. `StatusBadge` cae a
//    `neutral` para toda clave que no conozca (`components/data/badges.tsx`), y de los
//    SIETE `op_status` que escribe el motor sólo `failed` está en su mapa. Es decir:
//    `blocked`, `budget_blocked` y `budget_throttled` —las tres denegaciones— saldrían
//    en GRIS NEUTRO, con el mismo peso visual que «idle». Una denegación pintada como
//    estado tranquilo es peor que no pintarla: afirma calma sobre un rechazo.
//
//    Los siete son `modules/voice/policies.go:30-39`. Si el motor añade un octavo, la
//    fila NO se queda en blanco ni se inventa un color: cae a `neutral` y muestra el
//    literal crudo, que es lo único cierto que se puede decir de un valor desconocido.
const OP_STATUS_VARIANT: Record<string, BadgeVariant> = {
  blocked: 'danger',
  budget_blocked: 'danger',
  failed: 'danger',
  budget_throttled: 'warning',
  declared_not_opened: 'warning',
  requested: 'info',
  dispatched: 'success',
}

export function OpStatusBadge({ status }: { status: string }) {
  const { t } = useTranslation('voice')
  const key = (status ?? '').toLowerCase()
  const variant = OP_STATUS_VARIANT[key] ?? 'neutral'
  return (
    <Badge variant={variant}>
      {t(`ledger.opStatus.${key}`, { defaultValue: status || '—' })}
    </Badge>
  )
}

export function DecisionsTable({ decisions }: { decisions: VoiceDecision[] }) {
  const { t, i18n } = useTranslation('voice')
  const columns = useMemo<TableColumn<VoiceDecision>[]>(
    () => [
      {
        accessorKey: 'occurred_at',
        header: t('ledger.columns.when'),
        cell: ({ row }) => (
          <span className="text-xs text-muted-foreground">
            {formatRelativeTime(row.original.occurred_at, i18n.language)}
          </span>
        ),
      },
      {
        id: 'outcome',
        header: t('ledger.columns.outcome'),
        cell: ({ row }) => (
          <div className="flex flex-col gap-1">
            <OpStatusBadge status={row.original.op_status} />
            <span className="text-[11px] text-muted-foreground">
              {t(`ledger.op.${row.original.op}`, {
                defaultValue: row.original.op,
              })}
            </span>
          </div>
        ),
      },
      {
        accessorKey: 'session_ref',
        header: t('ledger.columns.session'),
        // ⛔ AQUÍ NO SE DICE «esta sesión no existe», y la tentación es fuerte porque es
        //    justo el caso que motiva esta pestaña. Cruzarlo contra la tabla de sesiones
        //    de la otra pestaña daría un falso «no existe» en cuanto ESA lista venga
        //    recortada, que es su estado por defecto. Ausente + incompleto = no lo sé, y
        //    no lo sé no se pinta como un hecho.
        cell: ({ row }) => (
          <span className="font-mono text-xs text-foreground">
            {row.original.session_ref || '—'}
          </span>
        ),
      },
      {
        accessorKey: 'agent_ref',
        header: t('ledger.columns.agent'),
        cell: ({ row }) => (
          <div className="flex flex-col">
            <span className="font-mono text-xs text-foreground">
              {row.original.agent_ref || '—'}
            </span>
            <span className="text-[11px] text-muted-foreground">
              {row.original.requested_provider_ref} /{' '}
              {row.original.requested_model_ref}
            </span>
          </div>
        ),
      },
      {
        accessorKey: 'actor',
        header: t('ledger.columns.actor'),
        cell: ({ row }) => (
          <div className="flex flex-col">
            <span className="font-mono text-xs text-muted-foreground">
              {row.original.actor}
            </span>
            <span className="text-[11px] text-muted-foreground">
              {row.original.actor_kind}
            </span>
          </div>
        ),
      },
      {
        accessorKey: 'result',
        header: t('ledger.columns.reason'),
        // El texto es el que REDACTA el motor (`result`), no una glosa de la consola: es
        // lo que distingue «no hay política» de «tope de presupuesto» de «kill switch».
        cell: ({ row }) => (
          <span className="text-xs text-muted-foreground">
            {row.original.result || '—'}
          </span>
        ),
      },
    ],
    [t, i18n.language],
  )
  return (
    <DataTable<VoiceDecision>
      columns={columns}
      data={decisions}
      getRowId={(r) => r.id}
      empty={
        <EmptyState
          title={t('ledger.empty')}
          description={t('ledger.emptyHint')}
        />
      }
    />
  )
}
