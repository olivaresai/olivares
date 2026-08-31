// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// AuditView — the Audit / Evidence Explorer. The tamper-evident ledger an
// auditor expects to see, made visible: the keyset-paginated evidence chain, a
// one-click server-side chain+checkpoint verification, a WORM/SIEM export of the
// engine's verbatim bytes, and the Ed25519 key for offline verification. A superadmin
// can also read the system-tenant auth-partition chain (list-only).
//
// HONESTY (ARCHITECTURE.md, docs/SECURITY-HARDENING.md): the web RENDERS the engine's events and verdicts.
// It never recomputes, repairs, or fabricates the chain; reading the ledger is itself
// audited (the engine appends audit.read / audit.export), surfaced via SelfAuditNotice.
import { useInfiniteQuery, useMutation, useQuery } from '@tanstack/react-query'
import {
  FileCheck2,
  FileDown,
  Filter,
  KeyRound,
  ShieldCheck,
  X,
} from 'lucide-react'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { DataTable, type TableColumn } from '@/components/data/data-table'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import { PageHeader } from '@/components/ui/page-header'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Skeleton } from '@/components/ui/skeleton'
import { toast } from '@/components/ui/toaster'
import {
  CaveatNotice,
  HashChip,
  IntegrityBadge,
  SelfAuditNotice,
} from '@/features/_intel'
import { SavedViewsMenu } from '@/features/saved-views'
import { isoMinuteBound } from '@/features/shared'
import { RelTimeLabel } from '@/features/shared'
import { auditApi } from '@/lib/api/endpoints'
import { useFailedActionReporter } from '@/lib/hooks/use-privileged-mutation'
import { ApiError } from '@/lib/api/errors'
import { queryKeys } from '@/lib/api/query'
import type { AuditEventDTO } from '@/lib/api/types'
import { useAuth } from '@/lib/auth/context'
import { formatInt } from '@/lib/format'
import { useUrlState } from '@/lib/hooks/use-url-state'
import { AuditEventSheet } from './audit-detail'
import { downloadBlob, exportFilename, fetchAuditExport } from './export'
import {
  EXPORT_FORMATS,
  type AuditExportOptions,
  type AuditFilters,
  type ExportFormat,
  type LedgerScope,
} from './types'
import './i18n'

const PAGE = 100
const FILTER_KEYS = [
  'q',
  'actor',
  'action',
  'target_kind',
  'target_id',
  'since',
  'until',
] as const
const URL_KEYS = [...FILTER_KEYS, 'scope'] as const
type FilterKey = (typeof FILTER_KEYS)[number]

/** Keyset next-page cursor: the chain is gap-free, so the next page starts one past
 * the last event's seq (core/api/handlers_audit.go). Undefined ends the scroll. */
function nextFrom(last: { items: AuditEventDTO[]; has_more: boolean }) {
  if (!last.has_more || last.items.length === 0) return undefined
  return last.items[last.items.length - 1].seq + 1
}

/** Sparse filtered pages must continue from the last EXAMINED event supplied by
 * the server. Falling back to the last matching item would skip or rescan data. */
function filteredNextFrom(last: { has_more: boolean; next_from?: number }) {
  return last.has_more ? last.next_from : undefined
}

/** Validate URL/saved-view values before they can select an endpoint or enter a
 * request. Unknown keys and non-string values are ignored. */
function sanitizeParams(
  input: Record<string, unknown>,
): Record<string, string> {
  const clean: Record<string, string> = {}
  for (const key of FILTER_KEYS) {
    const value = input[key]
    if (typeof value !== 'string' || value === '') continue
    if (key === 'since' || key === 'until') {
      // The SHARED bound parser, not a local NaN check. `new Date` accepts
      // 2026-02-30 and answers with the 2nd of March, so an impossible day was
      // reaching the ledger query silently — the same defect observability had,
      // in a third copy, in the very view the rest of this wave copies from.
      //
      // Minute precision, because that is what this view's control shows
      // (localDateTimeValue). Preserving nanosecond precision read from a URL
      // while rendering minutes is the same divergence one unit down: two links
      // identical on screen, two different windows behind them.
      const bound = isoMinuteBound(value)
      if (bound === undefined) continue
      clean[key] = bound
    } else {
      clean[key] = value
    }
  }
  if (input.scope === 'tenant' || input.scope === 'system') {
    clean.scope = input.scope
  }
  return clean
}

function filtersFromParams(params: Record<string, string>): AuditFilters {
  const filters: AuditFilters = {}
  for (const key of FILTER_KEYS) {
    if (params[key]) filters[key] = params[key]
  }
  return filters
}

function localDateTimeValue(value?: string): string {
  if (!value) return ''
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return ''
  const pad = (part: number) => String(part).padStart(2, '0')
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(
    date.getDate(),
  )}T${pad(date.getHours())}:${pad(date.getMinutes())}`
}

function rfc3339FromLocal(value: string): string | undefined {
  if (!value) return undefined
  const parsed = new Date(value)
  if (Number.isNaN(parsed.getTime())) return undefined
  // Core /v1/audit accepts RFC3339/RFC3339Nano. Unlike modules/* timestamp
  // inputs, using Date.toISOString() here is explicitly valid and preserves UTC.
  return parsed.toISOString()
}

export function AuditView() {
  const { t, i18n } = useTranslation('audit')
  const lang = i18n.language
  const { activeTenant, isSuperadmin } = useAuth()

  const [urlState, patchUrlState] = useUrlState(URL_KEYS)
  const validatedParams = useMemo(() => sanitizeParams(urlState), [urlState])
  const scope: LedgerScope =
    isSuperadmin && validatedParams.scope === 'system' ? 'system' : 'tenant'
  const filters = useMemo(
    () => filtersFromParams(validatedParams),
    [validatedParams],
  )
  const filtersActive = Object.keys(filters).length > 0
  const [selected, setSelected] = useState<AuditEventDTO | null>(null)
  const isSystem = scope === 'system'
  const listParams = useMemo(() => ({ limit: PAGE, ...filters }), [filters])

  const listQuery = useInfiniteQuery({
    queryKey: isSystem
      ? queryKeys.audit.systemList(activeTenant, listParams)
      : queryKeys.audit.list(activeTenant, listParams),
    queryFn: ({ pageParam }) =>
      (isSystem ? auditApi.systemList : auditApi.list)({
        from: pageParam,
        ...listParams,
      }),
    initialPageParam: 1,
    getNextPageParam: (last) =>
      filtersActive ? filteredNextFrom(last) : nextFrom(last),
  })

  const rows = useMemo(
    () => listQuery.data?.pages.flatMap((p) => p.items) ?? [],
    [listQuery.data],
  )
  const lastPage = listQuery.data?.pages[listQuery.data.pages.length - 1]
  const scannedThrough =
    filtersActive &&
    lastPage?.scan_complete === false &&
    lastPage.next_from !== undefined
      ? lastPage.next_from - 1
      : undefined
  const savedViewParams = useMemo(
    () => ({
      ...filters,
      scope: scope === 'system' ? 'system' : undefined,
    }),
    [filters, scope],
  )

  function applySavedView(params: Record<string, string>) {
    const clean = sanitizeParams(params)
    const patch: Record<string, string | undefined> = {}
    for (const key of FILTER_KEYS) patch[key] = clean[key]
    patch.scope =
      isSuperadmin && clean.scope === 'system' ? 'system' : undefined
    patchUrlState(patch)
  }

  const columns = useMemo<TableColumn<AuditEventDTO>[]>(
    () => [
      {
        accessorKey: 'seq',
        header: t('cols.seq'),
        cell: ({ getValue }) => (
          <span className="font-mono text-xs tabular-nums text-muted-foreground">
            {formatInt(getValue<number>(), lang)}
          </span>
        ),
      },
      {
        accessorKey: 'occurred_at',
        header: t('cols.occurred'),
        // MEDIDO con datos reales: esta columna se quedaba en 78 px y «6 minutes
        // ago» salía en TRES líneas. 120 px es lo que ese texto ocupa entero; el
        // `nowrap` de abajo impide que vuelva a repartirse si el reparto cambia.
        size: 120,
        // ⛔ «6 / minutes / ago» en TRES lineas: sin politica, una hora relativa se
        // reparte por sus espacios en cuanto la columna se estrecha. Una marca de
        // tiempo es atomica y no rompe nunca.
        cell: ({ getValue }) => (
          <span className="whitespace-nowrap">
            <RelTimeLabel ts={getValue<string>()} />
          </span>
        ),
      },
      {
        accessorKey: 'action',
        header: t('cols.action'),
        cell: ({ getValue }) => (
          <span className="font-mono text-xs font-medium text-foreground">
            {getValue<string>()}
          </span>
        ),
      },
      {
        id: 'actor',
        accessorFn: (e) => `${e.actor} ${e.actor_kind}`,
        header: t('cols.actor'),
        cell: ({ row }) => {
          const e = row.original
          return (
            <div className="flex min-w-0 items-center gap-1.5">
              <span className="truncate font-mono text-xs text-foreground">
                {e.actor || t('detail.actorSystem')}
              </span>
              <Badge variant="neutral">
                {t(`actorKind.${e.actor_kind}`, {
                  defaultValue: e.actor_kind || '—',
                })}
              </Badge>
            </div>
          )
        },
      },
      {
        id: 'target',
        accessorFn: (e) =>
          e.target_id ? `${e.target_kind ?? ''} ${e.target_id}` : '',
        header: t('cols.target'),
        enableSorting: false,
        cell: ({ row }) => {
          const e = row.original
          if (!e.target_id && !e.target_kind)
            return <span className="text-muted-foreground">—</span>
          return (
            <span className="truncate font-mono text-xs text-muted-foreground">
              {e.target_kind ? `${e.target_kind}:` : ''}
              {e.target_id || '—'}
            </span>
          )
        },
      },
      {
        id: 'hash',
        accessorKey: 'hash',
        header: t('cols.hash'),
        enableSorting: false,
        cell: ({ row }) => (
          <div className="flex items-center gap-1.5">
            <HashChip hash={row.original.hash} />
            {row.original.sig ? (
              <Badge variant="success" title={t('detail.signed')}>
                <FileCheck2 className="size-3" aria-hidden />
                <span className="sr-only">{t('detail.signed')}</span>
              </Badge>
            ) : null}
          </div>
        ),
      },
    ],
    [t, lang],
  )

  return (
    <div className="flex h-full flex-col gap-4">
      <PageHeader
        icon={FileCheck2}
        title={t('title')}
        description={t(isSystem ? 'subtitleSystem' : 'subtitle')}
        actions={
          isSuperadmin ? (
            <Select
              value={scope}
              onValueChange={(value) =>
                patchUrlState({
                  scope: value === 'system' ? 'system' : undefined,
                })
              }
            >
              <SelectTrigger
                className="h-8 w-auto min-w-[11rem] text-xs"
                aria-label={t('scope.label')}
              >
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="tenant">{t('scope.tenant')}</SelectItem>
                <SelectItem value="system">{t('scope.system')}</SelectItem>
              </SelectContent>
            </Select>
          ) : undefined
        }
      />

      {/* Reading the ledger is itself recorded in the ledger. */}
      <SelfAuditNotice />

      <AuditFilterBar
        filters={filters}
        onChange={(key, value) => patchUrlState({ [key]: value })}
        onClearAll={() =>
          patchUrlState(
            Object.fromEntries(FILTER_KEYS.map((key) => [key, undefined])),
          )
        }
      />

      {isSystem ? (
        <CaveatNotice>{t('scope.systemHint')}</CaveatNotice>
      ) : (
        <EvidenceControls filters={filters} />
      )}

      {scannedThrough !== undefined && (
        <div
          role="status"
          className="rounded-md border border-info-line bg-info-soft px-3 py-2 text-sm text-info"
        >
          {t('scan.incomplete', {
            seq: formatInt(scannedThrough, lang),
          })}
        </div>
      )}

      <DataTable
        columns={columns}
        data={rows}
        isLoading={listQuery.isLoading}
        error={listQuery.error}
        onRetry={() => void listQuery.refetch()}
        getRowId={(r) => r.id}
        onRowClick={(r) => setSelected(r)}
        searchable
        searchPlaceholder={t('searchLoaded')}
        toolbar={
          <SavedViewsMenu
            featureId="audit"
            params={savedViewParams}
            onApply={applySavedView}
          />
        }
        stickyHeader
        hasMore={listQuery.hasNextPage}
        onLoadMore={() => void listQuery.fetchNextPage()}
        isFetchingMore={listQuery.isFetchingNextPage}
        label={t('title')}
        empty={<EmptyHint />}
      />

      <AuditEventSheet
        event={selected}
        open={selected !== null}
        onOpenChange={(o) => {
          if (!o) setSelected(null)
        }}
      />
    </div>
  )
}

function AuditFilterBar({
  filters,
  onChange,
  onClearAll,
}: {
  filters: AuditFilters
  onChange: (key: FilterKey, value: string | undefined) => void
  onClearAll: () => void
}) {
  const { t } = useTranslation('audit')
  const active = FILTER_KEYS.flatMap((key) =>
    filters[key] ? [[key, filters[key]] as const] : [],
  )

  const textFields: Array<{
    key: Exclude<FilterKey, 'since' | 'until'>
    label: string
    placeholder: string
  }> = [
    {
      key: 'q',
      label: t('filters.q'),
      placeholder: t('filters.qPlaceholder'),
    },
    {
      key: 'actor',
      label: t('filters.actor'),
      placeholder: t('filters.actorPlaceholder'),
    },
    {
      key: 'action',
      label: t('filters.action'),
      placeholder: t('filters.actionPlaceholder'),
    },
    {
      key: 'target_kind',
      label: t('filters.targetKind'),
      placeholder: t('filters.targetKindPlaceholder'),
    },
    {
      key: 'target_id',
      label: t('filters.targetId'),
      placeholder: t('filters.targetIdPlaceholder'),
    },
  ]

  const labelFor = (key: FilterKey) =>
    t(
      key === 'target_kind'
        ? 'filters.targetKind'
        : key === 'target_id'
          ? 'filters.targetId'
          : `filters.${key}`,
    )

  return (
    <section
      aria-label={t('filters.title')}
      className="rounded-lg border border-border bg-surface p-3"
    >
      <div className="mb-3 flex items-center gap-2 text-sm font-medium text-foreground">
        <Filter className="size-4 text-muted-foreground" aria-hidden />
        {t('filters.title')}
      </div>
      <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
        {textFields.map((field) => (
          <label key={field.key} className="flex min-w-0 flex-col gap-1">
            <span className="text-xs font-medium text-muted-foreground">
              {field.label}
            </span>
            <Input
              value={filters[field.key] ?? ''}
              onChange={(event) =>
                onChange(field.key, event.currentTarget.value || undefined)
              }
              placeholder={field.placeholder}
              mono={field.key !== 'q'}
            />
          </label>
        ))}
        {(['since', 'until'] as const).map((key) => (
          <label key={key} className="flex min-w-0 flex-col gap-1">
            <span className="text-xs font-medium text-muted-foreground">
              {t(`filters.${key}`)}
            </span>
            <Input
              type="datetime-local"
              value={localDateTimeValue(filters[key])}
              onChange={(event) =>
                onChange(key, rfc3339FromLocal(event.currentTarget.value))
              }
            />
          </label>
        ))}
      </div>

      {active.length > 0 && (
        <div className="mt-3 flex flex-wrap items-center gap-2">
          {active.map(([key, value]) => (
            <Badge key={key} variant="accent" className="gap-1.5">
              <span className="max-w-64 truncate">
                {labelFor(key)}: {value}
              </span>
              <button
                type="button"
                className="-my-1.5 -mr-1.5 rounded-sm p-1.5 text-accent-soft-foreground outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring"
                aria-label={t('filters.clearOne', {
                  filter: labelFor(key),
                })}
                onClick={() => onChange(key, undefined)}
              >
                <X className="size-3" aria-hidden />
              </button>
            </Badge>
          ))}
          <Button variant="ghost" size="sm" onClick={onClearAll}>
            {t('filters.clearAll')}
          </Button>
        </div>
      )}
    </section>
  )
}

/** The auditor's control card: verify the chain, export to WORM/SIEM, and grab the
 * Ed25519 key for offline verification. All three operate on the TENANT ledger (the
 * engine exposes no verify/export route for the system chain). */
function EvidenceControls({ filters }: { filters: AuditFilters }) {
  const report = useFailedActionReporter()
  const { t, i18n } = useTranslation('audit')
  const lang = i18n.language
  const { activeTenant } = useAuth()

  const [verifyOn, setVerifyOn] = useState(false)
  const [keyOn, setKeyOn] = useState(false)
  const [format, setFormat] = useState<ExportFormat>('cef')
  const [fromSeq, setFromSeq] = useState('')
  const [toSeq, setToSeq] = useState('')
  const [useCurrentOverride, setUseCurrentOverride] = useState<boolean | null>(
    null,
  )
  const hasCurrentContext =
    Object.keys(filters).length > 0 || fromSeq !== '' || toSeq !== ''
  const useCurrent = useCurrentOverride ?? hasCurrentContext

  const verifyQuery = useQuery({
    queryKey: queryKeys.audit.verify(activeTenant),
    queryFn: () => auditApi.verify(),
    enabled: verifyOn,
  })

  const pubkeyQuery = useQuery({
    queryKey: queryKeys.audit.pubkey(activeTenant),
    queryFn: () => auditApi.pubkey(),
    enabled: keyOn,
  })

  const exportM = useMutation({
    mutationFn: () => {
      if (!useCurrent) return fetchAuditExport(format)
      const options: AuditExportOptions = { ...filters }
      const from = Number(fromSeq)
      const to = Number(toSeq)
      if (fromSeq !== '' && Number.isSafeInteger(from) && from >= 1) {
        options.from = from
      }
      if (toSeq !== '' && Number.isSafeInteger(to) && to >= 1) {
        options.to = to
      }
      return fetchAuditExport(format, options)
    },
    onSuccess: (blob) => {
      downloadBlob(blob, exportFilename(format))
      toast.success(t('export.done'))
    },
    onError: (e: unknown) => {
      // ⛔ ASEGURAMIENTO ANTES QUE ROL. Aquí NO se sustituye la rama de rol por `report`:
      // esta pantalla tiene copy propia (`export.forbidden`) y perderla sería cambiar un
      // mensaje exacto por uno genérico. Lo que faltaba es la rama de ceremonia delante,
      // porque `isForbidden` es sólo el status y un step_up_required lo satisface también.
      if (e instanceof ApiError && e.isStepUpRequired) {
        report(e)
        return
      }
      if (e instanceof ApiError && e.isForbidden) {
        toast.warning(t('export.forbidden'))
        return
      }
      toast.error(
        t('export.failed'),
        e instanceof Error && e.message
          ? { description: e.message }
          : undefined,
      )
    },
  })

  const v = verifyQuery.data

  return (
    <section className="rounded-lg border border-border bg-surface p-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex flex-wrap items-center gap-2">
          <Button
            variant="secondary"
            size="sm"
            onClick={() => {
              if (verifyOn) void verifyQuery.refetch()
              else setVerifyOn(true)
            }}
            disabled={verifyQuery.isFetching}
          >
            <ShieldCheck className="size-3.5" />
            {t('verify.action')}
          </Button>
          <Button
            variant="ghost"
            size="sm"
            onClick={() => setKeyOn((on) => !on)}
          >
            <KeyRound className="size-3.5" />
            {t('pubkey.action')}
          </Button>
        </div>

        <div className="flex flex-wrap items-end gap-2">
          <label className="flex flex-col gap-1">
            <span className="text-xs text-muted-foreground">
              {t('export.fromSeq')}
            </span>
            <Input
              type="number"
              min={1}
              step={1}
              value={fromSeq}
              onChange={(event) => setFromSeq(event.currentTarget.value)}
              className="w-24"
              placeholder={t('export.optional')}
            />
          </label>
          <label className="flex flex-col gap-1">
            <span className="text-xs text-muted-foreground">
              {t('export.toSeq')}
            </span>
            <Input
              type="number"
              min={1}
              step={1}
              value={toSeq}
              onChange={(event) => setToSeq(event.currentTarget.value)}
              className="w-24"
              placeholder={t('export.optional')}
            />
          </label>
          <Select
            value={format}
            onValueChange={(f) => setFormat(f as ExportFormat)}
          >
            <SelectTrigger
              className="h-8 w-auto min-w-[7rem] text-xs"
              aria-label={t('export.formatLabel')}
            >
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {EXPORT_FORMATS.map((f) => (
                <SelectItem key={f} value={f}>
                  {t(`export.format.${f}`)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <Button
            variant="secondary"
            size="sm"
            onClick={() => exportM.mutate()}
            disabled={exportM.isPending}
          >
            <FileDown className="size-3.5" />
            {t('export.action')}
          </Button>
        </div>
      </div>
      <label className="mt-3 flex items-center gap-2 text-xs text-muted-foreground">
        <Checkbox
          checked={useCurrent}
          onCheckedChange={(checked) => setUseCurrentOverride(checked === true)}
        />
        {t('export.useCurrent')}
      </label>

      {/* Server-side chain + checkpoint verdict — rendered, never computed. */}
      {verifyOn && (
        <div
          className="mt-3 border-t border-border pt-3"
          role="status"
          aria-live="polite"
          aria-atomic="true"
        >
          {verifyQuery.isFetching && !v ? (
            <Skeleton className="h-9 w-full" />
          ) : verifyQuery.error ? (
            // A request-level failure is "verdict unavailable", NOT a confirmed
            // break — render it muted (never the danger-red reserved for a real
            // tamper finding). The Verify button stays the retry affordance.
            <p className="text-xs text-muted-foreground">
              {t('verify.failed')}
            </p>
          ) : v ? (
            <div className="flex flex-col gap-2">
              <div className="flex flex-wrap items-center gap-1.5">
                <IntegrityBadge ok={v.ok} />
                <Badge variant={v.chain.ok ? 'success' : 'danger'}>
                  {t('verify.chain', {
                    checked: formatInt(v.chain.checked, lang),
                  })}
                </Badge>
                {/* Zero signed checkpoints is "no attestation coverage", NOT a
                    healthy green pass — the structural chain can be intact while
                    nothing has been signed yet. Render it neutral, never success. */}
                {v.checkpoints.status === 'pending' ? (
                  <Badge variant="neutral">{t('verify.noCheckpoints')}</Badge>
                ) : (
                  <Badge variant={v.checkpoints.ok ? 'success' : 'danger'}>
                    {t('verify.checkpoints', {
                      count: formatInt(v.checkpoints.count, lang),
                    })}
                  </Badge>
                )}
                {v.checkpoints.latest_attested_seq > 0 && (
                  <Badge variant="neutral">
                    {t('verify.attested', {
                      seq: formatInt(v.checkpoints.latest_attested_seq, lang),
                    })}
                  </Badge>
                )}
              </div>
              {!v.chain.ok && (
                <p className="text-xs font-medium text-danger">
                  {t('verify.chainBreak', {
                    seq: formatInt(v.chain.break_at, lang),
                  })}
                  {v.chain.reason ? ` — ${v.chain.reason}` : ''}
                </p>
              )}
              {/* The loud line belongs to a checkpoint that EXISTS and does not
                  verify. A ledger that has simply not been attested yet reports
                  `pending` and gets the neutral badge above — printing
                  "Checkpoint signature failed at seq 0" on a healthy first-boot
                  install is how an operator learns to ignore this red. */}
              {v.checkpoints.status === 'failed' && (
                <p className="text-xs font-medium text-danger">
                  {t('verify.checkpointBreak', {
                    seq: formatInt(v.checkpoints.first_bad_seq, lang),
                  })}
                  {v.checkpoints.reason ? ` — ${v.checkpoints.reason}` : ''}
                </p>
              )}
            </div>
          ) : null}
        </div>
      )}

      {/* Ed25519 checkpoint key — for an external party to verify an export offline. */}
      {keyOn && (
        <div className="mt-3 border-t border-border pt-3">
          {pubkeyQuery.isFetching && !pubkeyQuery.data ? (
            <Skeleton className="h-9 w-full" />
          ) : pubkeyQuery.error ? (
            <p className="text-xs font-medium text-danger">
              {t('pubkey.failed')}
            </p>
          ) : pubkeyQuery.data ? (
            <div className="flex flex-wrap items-center gap-2">
              <span className="text-xs text-muted-foreground">
                {t('pubkey.label')}
              </span>
              <Badge variant="outline" className="font-mono uppercase">
                {pubkeyQuery.data.algorithm}
              </Badge>
              <HashChip
                hash={pubkeyQuery.data.public_key}
                label={t('pubkey.key')}
                head={12}
                tail={8}
              />
            </div>
          ) : null}
          <p className="mt-1.5 text-xs text-muted-foreground">
            {t('pubkey.hint')}
          </p>
        </div>
      )}
    </section>
  )
}

function EmptyHint() {
  const { t } = useTranslation('audit')
  return (
    <div className="px-6 py-12 text-center">
      <p className="text-sm font-medium text-foreground">{t('empty.title')}</p>
      <p className="mx-auto mt-1 max-w-sm text-sm text-muted-foreground">
        {t('empty.description')}
      </p>
    </div>
  )
}
