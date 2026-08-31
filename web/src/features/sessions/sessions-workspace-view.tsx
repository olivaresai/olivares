// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQuery } from '@tanstack/react-query'
import {
  Activity,
  Eye,
  HelpCircle,
  Plus,
  RefreshCw,
  Terminal,
} from 'lucide-react'
import { useCallback, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { DataTable, type TableColumn } from '@/components/data/data-table'
import { Button } from '@/components/ui/button'
import { Card } from '@/components/ui/card'
import { EmptyState } from '@/components/ui/empty-state'
import { PageHeader } from '@/components/ui/page-header'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Skeleton } from '@/components/ui/skeleton'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { agentOpsApi, agentOpsKeys } from '@/features/agentops/api'
import { RunCreateDialog } from '@/features/agentops/run-create-dialog'
import { RunStateBadge } from '@/features/agentops/run-state-badge'
import type { RunState } from '@/features/agentops/types'
import { WorkspacesPanel } from '@/features/agentops/workspaces-panel'
import { LiveDot, RelTimeLabel, useLiveStream } from '@/features/shared'
import { useAuth } from '@/lib/auth/context'
import { formatInt, formatMicroUsd, formatTokens } from '@/lib/format'
import { useWorkspaceFilter } from '@/lib/hooks/use-workspace-filter'
import { cn } from '@/lib/utils'
import { sessionsApi, sessionsKeys } from './api'
import { CcStateBadge } from './cc-state-badge'
import {
  mergeSessions,
  primaryRun,
  sessionLabel,
  sessionSearchKey,
  type Provenance,
  type UnifiedSession,
} from './provenance'
import { SessionCard, type SessionTarget } from './session-card'
import type { CcState, LiveDTO } from './types'
import './i18n'

const LIVE_LIMIT = 200
const RUNS_LIMIT = 200
const ALL = '__all__'
const MAX_OVERRIDES = 500

// ONE state facet over BOTH halves. The observed states are the engine's derived
// cc_state; the run states are the runtime's stored lifecycle. They are prefixed
// because they are different vocabularies that share words ("idle" means "quiet within
// tolerance" on one side and "no recent activity, process alive" on the other), and
// collapsing them would filter for a state the operator did not choose.
//
// The run half is here because `/agentops` HAD it (origin/main
// web/src/features/agentops/agentops-view.tsx:36-45) and a unified screen that could no
// longer isolate failed or stopped runs would have taken a function away.
const OBSERVED_STATES: CcState[] = ['active', 'idle', 'ended', 'silent_evasion']
const RUN_STATES: RunState[] = [
  'pending',
  'running',
  'idle',
  'stopped',
  'failed',
  'cleaned',
]

/** Which entrance opened this view. Both land on the SAME room and the SAME card;
 * the entrance only decides which source the list opens focused on, and whether the
 * operate affordances (launch, workspaces) are offered. */
export type SessionsEntrance = 'observe' | 'operate'

/**
 * SessionsWorkspaceView — ONE destination for sessions, whichever way they reached
 * the plane.
 *
 * Before this, a session lived in one of two screens in two different nav sections:
 * observed sessions under Visibility (`/sessions`) and launched runs under Management
 * (`/agentops`, labelled "Claude Code"), with a detail card on only one of the two.
 * The operator had to know whether a session had been DISCOVERED or LAUNCHED to pick
 * a section — which is precisely what they came to the screen to find out.
 *
 * Both routes now mount this view, and both keep their own path, permission and nav
 * entry. They are NOT redirects, and that is a measured decision, not a preference:
 * `RequirePermission` (components/layout/require-permission.tsx:25) blocks a route on
 * the ONE permission its registry entry declares, so sending `/agentops` to
 * `/sessions` would hand an operator holding only `sessions:run:read` a Forbidden
 * page instead of their own runs. Two doors into one room removes the split without
 * taking anything away — and the canon's first hard rule is that the answer to a
 * problem is never to remove a function.
 */
export function SessionsWorkspaceView({
  entrance = 'observe',
}: {
  entrance?: SessionsEntrance
}) {
  const { activeTenant } = useAuth()
  // Remount on a tenant switch so no row, override or open card can outlive it.
  return <Inner key={activeTenant ?? 'none'} entrance={entrance} />
}

function Inner({ entrance }: { entrance: SessionsEntrance }) {
  const { t, i18n } = useTranslation('sessions')
  const lang = i18n.language
  const { activeTenant, can } = useAuth()
  const { workspaceId, queryKey: wsKey } = useWorkspaceFilter()

  const canLiveRead = can('sessions:live:read')
  const canRunRead = can('sessions:run:read')
  const canRunWrite = can('sessions:run:write')

  const [tab, setTab] = useState('sessions')
  const [state, setState] = useState<string>(ALL)
  const [source, setSource] = useState<string>(ALL)
  const [target, setTarget] = useState<SessionTarget | null>(null)
  const [createOpen, setCreateOpen] = useState(false)
  const [overrides, setOverrides] = useState<Record<string, LiveDTO>>({})

  const liveQuery = useQuery({
    queryKey: [
      ...sessionsKeys.live(activeTenant, { limit: LIVE_LIMIT }),
      wsKey,
    ],
    queryFn: () =>
      sessionsApi.live({ workspace_id: workspaceId, limit: LIVE_LIMIT }),
    enabled: canLiveRead,
  })

  const runsQuery = useQuery({
    queryKey: agentOpsKeys.runs(activeTenant, { limit: RUNS_LIMIT }),
    queryFn: () => agentOpsApi.listRuns({ limit: RUNS_LIMIT }),
    enabled: canRunRead,
    refetchInterval: 8_000,
  })

  /**
   * ⛔ REFRESCAR SÓLO LO QUE SE PUEDE LEER. Las dos consultas ya están guardadas con `enabled`,
   *    pero el botón llamaba a `refetch()` de LAS DOS sin mirar el permiso, y `refetch` es una
   *    orden explícita: pide al motor una lectura que este operador no tiene derecho a hacer, y
   *    la respuesta es un 403 que nadie enseña. La guarda de `enabled` protege la carga
   *    automática y no ésta.
   */
  const refrescarLegibles = useCallback(() => {
    if (canLiveRead) void liveQuery.refetch()
    if (canRunRead) void runsQuery.refetch()
  }, [canLiveRead, canRunRead, liveQuery, runsQuery])

  /** Ninguna fuente legible ⇒ el botón no tiene nada que refrescar y no se pinta. */
  const hayFuenteLegible = canLiveRead || canRunRead

  const onSnapshot = useCallback((snap: LiveDTO) => {
    if (!snap?.session_ref) return
    setOverrides((prev) => {
      const next = { ...prev, [snap.session_ref]: snap }
      const keys = Object.keys(next)
      if (keys.length > MAX_OVERRIDES) {
        keys.sort(
          (a, b) =>
            (Date.parse(next[a]!.last_event_at) || 0) -
            (Date.parse(next[b]!.last_event_at) || 0),
        )
        for (const k of keys.slice(0, keys.length - MAX_OVERRIDES))
          delete next[k]
      }
      return next
    })
  }, [])

  const { status: streamStatus } = useLiveStream<LiveDTO>({
    path: '/v1/m/sessions/stream',
    events: ['session'],
    enabled: canLiveRead,
    onSnapshot,
  })

  const live = useMemo(() => {
    const base = liveQuery.data?.items ?? []
    const seen = new Set<string>()
    const merged: LiveDTO[] = base.map((row) => {
      seen.add(row.session_ref)
      return overrides[row.session_ref] ?? row
    })
    const extras: LiveDTO[] = []
    for (const [ref, o] of Object.entries(overrides)) {
      if (!seen.has(ref) && o.cc_state !== 'ended') extras.push(o)
    }
    return [...extras, ...merged]
  }, [liveQuery.data, overrides])

  const sessions = useMemo(
    () => mergeSessions(live, runsQuery.data?.items ?? []),
    [live, runsQuery.data],
  )

  const rows = useMemo(
    () =>
      sessions.filter((s) => {
        if (source !== ALL && s.provenance !== (source as Provenance))
          return false
        if (state !== ALL) {
          const [half, value] = state.split(':')
          if (half === 'obs' && s.live?.cc_state !== value) return false
          if (half === 'run' && !s.runs.some((r) => r.state === value))
            return false
        }
        return true
      }),
    [sessions, state, source],
  )

  const counts = useMemo(() => {
    let launched = 0
    let discovered = 0
    let attention = 0
    for (const s of sessions) {
      if (s.provenance === 'launched') launched++
      else discovered++
      if (s.live?.cc_state === 'silent_evasion' || s.live?.unclaimed)
        attention++
      else if (s.runs.some((r) => r.state === 'failed')) attention++
    }
    return { total: sessions.length, launched, discovered, attention }
  }, [sessions])

  // A page that reports has_more is a page, not the estate. Provenance in THIS TABLE
  // is joined over what is loaded, so a run older than the run page would leave its
  // session reading "discovered" here. Say so rather than let the column imply a
  // completeness it does not have — the card resolves each session against the
  // engine, which is where the answer has to be right.
  const truncated =
    (liveQuery.data?.has_more ?? false) || (runsQuery.data?.has_more ?? false)

  // THE RUN HALF WAS NOT READ. Missing permission or a failed lookup both mean the
  // same thing here: a row with no runs is a row nothing could be checked against, and
  // rendering it "Discovered" would state a fact this list does not hold (contrast
  // finding, 2026-08-10 — a notice beside the table does not unsay the column).
  const originUnknown = !canRunRead || runsQuery.isError

  // An error blanks the table ONLY when it leaves nothing to show. A failed run lookup
  // while the observed half answered must not throw away the rows that DID arrive: it
  // becomes the "origin not read" column and the notice above, which is the honest
  // reading — half the picture, said out loud, beats an error page over data we have.

  const columns = useMemo<TableColumn<UnifiedSession>[]>(
    () => [
      {
        id: 'session',
        // Searched by label AND by every reference: naming a row after the run an
        // operator typed must not make it unfindable by the session id the ledger,
        // the API and any saved deep link use.
        accessorFn: (s) => sessionSearchKey(s),
        header: t('cols.session'),
        cell: ({ row }) => {
          const label = sessionLabel(row.original)
          const ref = row.original.sessionRef
          return (
            <span className="flex flex-col">
              <span className="font-mono text-xs font-medium text-foreground">
                {label}
              </span>
              {ref && ref !== label && (
                <span className="font-mono text-[11px] text-muted-foreground">
                  {ref}
                </span>
              )}
            </span>
          )
        },
      },
      {
        id: 'provenance',
        accessorFn: (s) =>
          originUnknown && s.provenance === 'discovered'
            ? 'unknown'
            : s.provenance,
        header: t('cols.provenance'),
        cell: ({ row }) => (
          <ProvenanceChip
            provenance={row.original.provenance}
            unknown={originUnknown}
          />
        ),
      },
      {
        id: 'state',
        accessorFn: (s) => s.live?.cc_state ?? primaryRun(s.runs)?.state ?? '',
        header: t('cols.state'),
        cell: ({ row }) => {
          const s = row.original
          const run = primaryRun(s.runs)
          return (
            <span className="flex flex-wrap items-center gap-1">
              {s.live && <CcStateBadge state={s.live.cc_state} />}
              {run && <RunStateBadge state={run.state} />}
              {!s.live && !run && (
                <span className="text-muted-foreground">—</span>
              )}
            </span>
          )
        },
      },
      {
        id: 'control',
        accessorFn: (s) => s.control,
        header: t('cols.control'),
        cell: ({ row }) => (
          <span className="text-xs text-muted-foreground">
            {t(`card.control.${row.original.control}`)}
          </span>
        ),
      },
      {
        id: 'action',
        accessorFn: (s) => s.live?.current_action ?? '',
        header: t('cols.action'),
        enableSorting: false,
        cell: ({ row }) => {
          const v = row.original.live?.current_action
          return v ? (
            <span className="truncate text-foreground" title={v}>
              {v}
            </span>
          ) : (
            <span className="text-muted-foreground">—</span>
          )
        },
      },
      {
        id: 'model',
        accessorFn: (s) =>
          s.live?.model_ref ?? primaryRun(s.runs)?.model_ref ?? '',
        header: t('cols.model'),
        cell: ({ getValue }) => {
          const v = getValue<string>()
          return v ? (
            <span className="font-mono text-xs text-muted-foreground">{v}</span>
          ) : (
            <span className="text-muted-foreground">—</span>
          )
        },
      },
      {
        id: 'tokens',
        accessorFn: (s) =>
          s.live ? s.live.input_tokens + s.live.output_tokens : -1,
        header: t('cols.tokens'),
        cell: ({ row }) => {
          const l = row.original.live
          return l ? (
            <span className="font-mono text-xs tabular-nums text-muted-foreground">
              {formatTokens(l.input_tokens, lang)} /{' '}
              {formatTokens(l.output_tokens, lang)}
            </span>
          ) : (
            <span className="text-muted-foreground">—</span>
          )
        },
      },
      {
        id: 'cost',
        accessorFn: (s) => s.live?.cost_micro_usd ?? -1,
        header: t('cols.cost'),
        cell: ({ row }) => {
          const l = row.original.live
          return l ? (
            <span className="font-mono text-xs tabular-nums text-foreground">
              {formatMicroUsd(l.cost_micro_usd, { locale: lang })}
            </span>
          ) : (
            <span className="text-muted-foreground">—</span>
          )
        },
      },
      {
        id: 'lastSeen',
        accessorFn: (s) => s.lastActivityMs,
        header: t('cols.lastSeen'),
        cell: ({ row }) =>
          row.original.lastActivityMs > 0 ? (
            <RelTimeLabel
              ts={new Date(row.original.lastActivityMs).toISOString()}
            />
          ) : (
            <span className="text-muted-foreground">—</span>
          ),
      },
    ],
    [t, lang, originUnknown],
  )

  const showWorkspaces = canRunRead
  const title = entrance === 'operate' ? t('operateTitle') : t('title')
  const subtitle =
    entrance === 'operate' ? t('operateSubtitle') : t('unifiedSubtitle')

  return (
    <div className="flex h-full flex-col gap-4">
      <PageHeader
        icon={entrance === 'operate' ? Terminal : Activity}
        title={title}
        description={subtitle}
        actions={
          <div className="flex items-center gap-2">
            {canLiveRead && <LiveDot status={streamStatus} />}
            {hayFuenteLegible && (
              <Button
                variant="ghost"
                size="sm"
                onClick={refrescarLegibles}
                disabled={
                  (canLiveRead && liveQuery.isFetching) ||
                  (canRunRead && runsQuery.isFetching)
                }
              >
                <RefreshCw
                  className={cn(
                    'size-3.5',
                    ((canLiveRead && liveQuery.isFetching) ||
                      (canRunRead && runsQuery.isFetching)) &&
                      'animate-spin',
                  )}
                />
                {t('refresh')}
              </Button>
            )}
            {canRunWrite && (
              <Button
                variant="primary"
                size="sm"
                onClick={() => setCreateOpen(true)}
              >
                <Plus className="size-3.5" />
                {t('launch')}
              </Button>
            )}
          </div>
        }
      />

      <Tabs value={tab} onValueChange={setTab}>
        <TabsList>
          <TabsTrigger value="sessions">{t('tabs.sessions')}</TabsTrigger>
          {showWorkspaces && (
            <TabsTrigger value="workspaces">{t('tabs.workspaces')}</TabsTrigger>
          )}
        </TabsList>

        <TabsContent value="sessions" className="mt-4 flex flex-col gap-4">
          <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
            <Tile
              label={t('summary.total')}
              value={counts.total}
              locale={lang}
              loading={liveQuery.isLoading || runsQuery.isLoading}
            />
            <Tile
              label={t('summary.launched')}
              value={counts.launched}
              locale={lang}
              loading={runsQuery.isLoading}
            />
            <Tile
              label={t('summary.discovered')}
              value={counts.discovered}
              locale={lang}
              loading={liveQuery.isLoading}
            />
            <Tile
              label={t('summary.attention')}
              value={counts.attention}
              tone={counts.attention > 0 ? 'danger' : undefined}
              locale={lang}
              loading={liveQuery.isLoading || runsQuery.isLoading}
            />
          </div>

          {!canLiveRead && (
            <p className="rounded-md border border-border bg-muted px-2.5 py-2 text-xs text-muted-foreground">
              {t('partial.noLiveRead')}
            </p>
          )}
          {!canRunRead && (
            <p className="rounded-md border border-border bg-muted px-2.5 py-2 text-xs text-muted-foreground">
              {t('partial.noRunRead')}
            </p>
          )}
          {canRunRead && runsQuery.isError && (
            <p className="rounded-md border border-warning-line bg-warning-soft px-2.5 py-2 text-xs text-warning">
              {t('partial.runLookupFailed')}
            </p>
          )}
          {canLiveRead && liveQuery.isError && (
            <p className="rounded-md border border-warning-line bg-warning-soft px-2.5 py-2 text-xs text-warning">
              {t('partial.liveLookupFailed')}
            </p>
          )}
          {truncated && (
            <p className="rounded-md border border-warning-line bg-warning-soft px-2.5 py-2 text-xs text-warning">
              {t('partial.truncated')}
            </p>
          )}

          <div className="flex flex-wrap items-center justify-end gap-2">
            <Select value={source} onValueChange={setSource}>
              <SelectTrigger
                className="h-7 w-auto min-w-[9rem] text-xs"
                aria-label={t('allSources')}
              >
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={ALL}>{t('allSources')}</SelectItem>
                <SelectItem value="launched">
                  {t('card.provenance.launched')}
                </SelectItem>
                <SelectItem value="discovered">
                  {t('card.provenance.discovered')}
                </SelectItem>
              </SelectContent>
            </Select>
            <Select value={state} onValueChange={setState}>
              <SelectTrigger
                className="h-7 w-auto min-w-[11rem] text-xs"
                aria-label={t('allStates')}
              >
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={ALL}>{t('allStates')}</SelectItem>
                {OBSERVED_STATES.map((s) => (
                  <SelectItem key={`obs:${s}`} value={`obs:${s}`}>
                    {t('facet.observed', { state: t(`state.${s}`) })}
                  </SelectItem>
                ))}
                {RUN_STATES.map((s) => (
                  <SelectItem key={`run:${s}`} value={`run:${s}`}>
                    {t('facet.run', { state: t(`runState.${s}`) })}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>

          <DataTable
            columns={columns}
            data={rows}
            isLoading={liveQuery.isLoading || runsQuery.isLoading}
            error={
              (!canLiveRead || liveQuery.isError) &&
              (!canRunRead || runsQuery.isError)
                ? (liveQuery.error ?? runsQuery.error)
                : undefined
            }
            onRetry={refrescarLegibles}
            getRowId={(s) => s.key}
            onRowClick={(s) =>
              setTarget({
                sessionRef: s.sessionRef,
                runRef: s.sessionRef ? undefined : primaryRun(s.runs)?.run_ref,
              })
            }
            searchable
            searchPlaceholder={t('search')}
            stickyHeader
            label={t('title')}
            empty={
              <EmptyState
                icon={<Activity />}
                title={t('empty.title')}
                description={t('empty.description')}
              />
            }
          />
        </TabsContent>

        {showWorkspaces && (
          <TabsContent value="workspaces" className="mt-4">
            <WorkspacesPanel />
          </TabsContent>
        )}
      </Tabs>

      <RunCreateDialog open={createOpen} onOpenChange={setCreateOpen} />
      <SessionCard target={target} onClose={() => setTarget(null)} />
    </div>
  )
}

function ProvenanceChip({
  provenance,
  unknown,
}: {
  provenance: Provenance
  unknown?: boolean
}) {
  const { t } = useTranslation('sessions')
  const launched = provenance === 'launched'
  // A linked run outranks not having looked: with one in hand the origin is known.
  const notKnown = !!unknown && !launched
  const key = notKnown ? 'unknown' : provenance
  return (
    <span
      title={t(`card.provenance.${key}Explain`)}
      className={cn(
        'inline-flex items-center gap-1 rounded-sm border px-1.5 py-0.5 text-[10px] font-medium',
        launched
          ? 'border-accent-line bg-accent-soft text-accent-text'
          : notKnown
            ? 'border-warning-line bg-warning-soft text-warning'
            : 'border-border bg-muted text-muted-foreground',
      )}
    >
      {launched ? (
        <Terminal className="size-3" />
      ) : notKnown ? (
        <HelpCircle className="size-3" />
      ) : (
        <Eye className="size-3" />
      )}
      {t(`card.provenance.${key}`)}
    </span>
  )
}

function Tile({
  label,
  value,
  tone,
  locale,
  loading,
}: {
  label: string
  value: number
  tone?: 'danger'
  locale?: string
  loading?: boolean
}) {
  return (
    <Card className="p-3">
      <div className="text-xs text-muted-foreground">{label}</div>
      {loading ? (
        <Skeleton className="mt-1 h-7 w-12" />
      ) : (
        <div
          className={cn(
            'font-display text-2xl font-semibold tabular-nums',
            tone === 'danger' ? 'text-danger' : 'text-foreground',
          )}
        >
          {formatInt(value, locale)}
        </div>
      )}
    </Card>
  )
}
