// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import {
  Activity,
  Boxes,
  Disc3,
  Eye,
  HelpCircle,
  Network,
  Play,
  Radio,
  ShieldCheck,
  Square,
  Terminal,
  Trash2,
} from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { AccessModeBadge } from '@/components/data/badges'
import { Button } from '@/components/ui/button'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import { KvList, KvRow } from '@/components/ui/kv'
import { Separator } from '@/components/ui/separator'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { Spinner } from '@/components/ui/spinner'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { toast } from '@/components/ui/toaster'
import { agentOpsApi, agentOpsKeys } from '@/features/agentops/api'
import { GovernancePanel } from '@/features/agentops/governance-panel'
import { LiveConsole } from '@/features/agentops/live-console'
import { EventsPanel, RunInfo } from '@/features/agentops/run-detail'
import { RunStateBadge } from '@/features/agentops/run-state-badge'
import type { RunDTO } from '@/features/agentops/types'
import {
  LiveDot,
  RelTimeLabel,
  humanDurationSeconds,
  useLiveStream,
} from '@/features/shared'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import { formatInt, formatMicroUsd, formatTokens } from '@/lib/format'
import { cn } from '@/lib/utils'
import { sessionsApi, sessionsKeys } from './api'
import { CcStateBadge } from './cc-state-badge'
import {
  capabilities,
  controlLevel,
  isLiveRun,
  mergeSessions,
  primaryRun,
  sessionLabel,
  type Capability,
  type ControlLevel,
  type UnifiedSession,
} from './provenance'
import { SessionTimeline } from './timeline'
import type { LiveDTO } from './types'
import './i18n'

/** What the caller clicked. Either half identifies the session; the card resolves
 * the other from the engine rather than from whatever list happened to be loaded. */
export interface SessionTarget {
  sessionRef?: string
  runRef?: string
}

/**
 * SessionCard — ONE card for one session, whether it was discovered or launched
 *. Both console entrances (`/sessions` under Visibility and `/agentops` under
 * Management) open this same card, and it always answers the two questions the split
 * screens could not:
 *
 *  · PROVENANCE — did Olivares start this session, or does it only observe it? It is
 *    resolved against the ENGINE (`GET /runs?claude_session_id=…`, an exact store
 *    lookup), never by joining whatever page a list had loaded: a page-bound join
 *    reports "discovered" for a session whose run has scrolled off, which is the one
 *    wrong answer that looks like a right one.
 *
 *  · CONTROL — what can actually be done with it, split into what the PLANE can do
 *    (reach) and what YOU can do (RBAC), because "you can't" and "nobody can" are
 *    different sentences and the operator needs to know which one they are reading.
 *
 * Each half degrades honestly on its own permission: a caller without
 * sessions:run:read is told the operate half was not read, never that there is none.
 */
export function SessionCard({
  target,
  onClose,
}: {
  target: SessionTarget | null
  onClose: () => void
}) {
  const open = target !== null
  return (
    <Sheet
      open={open}
      onOpenChange={(o) => {
        if (!o) onClose()
      }}
    >
      <SheetContent className="flex w-full flex-col gap-4 overflow-y-auto sm:max-w-3xl">
        {target && <CardBody target={target} onClose={onClose} />}
      </SheetContent>
    </Sheet>
  )
}

function CardBody({
  target,
  onClose,
}: {
  target: SessionTarget
  onClose: () => void
}) {
  const { t, i18n } = useTranslation('sessions')
  const lang = i18n.language
  const { activeTenant, can } = useAuth()
  const [tab, setTab] = useState('overview')
  const [selectedRun, setSelectedRun] = useState<string | null>(null)

  const canLiveRead = can('sessions:live:read')
  const canRunRead = can('sessions:run:read')
  const grants = {
    liveRead: canLiveRead,
    runRead: canRunRead,
    runWrite: can('sessions:run:write'),
    runAdmin: can('sessions:run:admin'),
  }

  // Opened from a run row: the run itself announces the observed session it drives.
  const seedRunQuery = useQuery({
    queryKey: agentOpsKeys.run(activeTenant, target.runRef ?? ''),
    queryFn: () => agentOpsApi.getRun(target.runRef as string),
    enabled: !!target.runRef && canRunRead,
    refetchInterval: 5_000,
  })
  const sessionRef =
    target.sessionRef || seedRunQuery.data?.claude_session_id || ''

  const liveQuery = useQuery({
    queryKey: sessionsKeys.liveOne(activeTenant, sessionRef),
    queryFn: () => sessionsApi.liveOne(sessionRef),
    enabled: !!sessionRef && canLiveRead,
    // A session with no telemetry yet is a 404, and that is an ANSWER (the launched
    // half exists, the observed half has not started) — not an error to retry at.
    retry: false,
  })

  // THE PROVENANCE LOOKUP, and it is the engine's answer, not a page join.
  const runsQuery = useQuery({
    queryKey: agentOpsKeys.runs(activeTenant, {
      claude_session_id: sessionRef,
    }),
    queryFn: () => agentOpsApi.listRuns({ claude_session_id: sessionRef }),
    enabled: !!sessionRef && canRunRead,
    refetchInterval: 8_000,
  })

  // Freshness for the observed half while the card is open.
  const [override, setOverride] = useState<LiveDTO | null>(null)
  const { status: streamStatus } = useLiveStream<LiveDTO>({
    path: '/v1/m/sessions/stream',
    events: ['session'],
    query: sessionRef ? { ref: sessionRef } : undefined,
    enabled: !!sessionRef && canLiveRead,
    onSnapshot: (snap) => {
      if (snap.session_ref === sessionRef) setOverride(snap)
    },
  })

  const live =
    override && override.session_ref === sessionRef
      ? override
      : (liveQuery.data ?? undefined)

  // Every run the engine linked to this session, plus the seed run when the card was
  // opened from a run that has not announced a session id (so it is never dropped).
  const linked = runsQuery.data?.items ?? []
  const seed = seedRunQuery.data
  const runs: RunDTO[] =
    seed && !linked.some((r) => r.run_ref === seed.run_ref)
      ? [seed, ...linked]
      : linked

  const merged = mergeSessions(live ? [live] : [], runs)
  const session: UnifiedSession = merged[0] ?? {
    key: sessionRef ? `sess:${sessionRef}` : `run:${target.runRef ?? ''}`,
    sessionRef: sessionRef || undefined,
    runs: [],
    live,
    provenance: 'discovered',
    control: 'observe',
    lastActivityMs: 0,
  }
  // The operator can act on ANY of the runs driving this session, not only the one
  // the card opens on: before each run was its own selectable row, and folding
  // them into one card must not take that away (contrast finding, 2026-08-10).
  const defaultRun = primaryRun(session.runs)
  const run = session.runs.find((r) => r.run_ref === selectedRun) ?? defaultRun
  const caps = capabilities({ ...session, runs: run ? [run] : [] }, grants)
  // The level describes the run the card is ACTING ON, not the session's strongest.
  // Seen on screen while switching runs: the header kept saying "Full control" (the
  // bridged run's reach) while the capability list correctly described the relayed one
  // it had switched to — the same contradiction the contrast found between
  // controlLevel and capabilities, surfacing again through the picker. The list still
  // reports the SESSION's reach, which is the right unit there.
  const control = controlLevel(run ? [run] : session.runs)

  // The operate half was NOT READ — which is NOT the same as "there is none", and the
  // difference is the whole point of this card. Three ways to not know: no permission,
  // a failed lookup, or a failed seed read. All three must say "not read"; only an
  // answered lookup that came back empty may say "discovered".
  const operateUnknown =
    !canRunRead ||
    (!!sessionRef && runsQuery.isError) ||
    (!!target.runRef && seedRunQuery.isError)
  // Same rule on the observed half: a 404 is an ANSWER (nothing observed), any other
  // failure is not.
  const observeFailed =
    liveQuery.isError &&
    !(liveQuery.error instanceof ApiError && liveQuery.error.status === 404)
  const observeUnknown = (!canLiveRead || observeFailed) && !!sessionRef

  const loading =
    (!!target.runRef && seedRunQuery.isLoading) ||
    (!!sessionRef && canRunRead && runsQuery.isLoading)

  if (loading) {
    return (
      <div className="flex items-center justify-center p-12">
        <Spinner />
      </div>
    )
  }

  return (
    <>
      <SheetHeader>
        <SheetTitle className="flex items-center gap-2">
          {session.provenance === 'launched' ? (
            <Terminal className="size-4 text-accent-text" />
          ) : (
            <Activity className="size-4 text-accent-text" />
          )}
          <span className="truncate font-mono text-base">
            {sessionLabel(session)}
          </span>
        </SheetTitle>
        <SheetDescription className="flex flex-wrap items-center gap-2">
          {live && <CcStateBadge state={live.cc_state} />}
          {run && <RunStateBadge state={run.state} />}
          {live && <LiveDot status={streamStatus} />}
        </SheetDescription>
      </SheetHeader>

      <ProvenanceBlock
        session={session}
        operateUnknown={operateUnknown}
        live={live}
        activeRun={run?.run_ref}
        onSelectRun={setSelectedRun}
      />

      <ControlBlock
        control={control}
        caps={caps}
        operateUnknown={operateUnknown}
      />

      <RunActions run={run} caps={caps} onClose={onClose} />

      <Tabs value={tab} onValueChange={setTab}>
        <TabsList>
          <TabsTrigger value="overview">{t('card.tabs.overview')}</TabsTrigger>
          {run && <TabsTrigger value="live">{t('card.tabs.live')}</TabsTrigger>}
          {run && (
            <TabsTrigger value="governance">
              {t('card.tabs.governance')}
            </TabsTrigger>
          )}
          {run && (
            <TabsTrigger value="lifecycle">
              {t('card.tabs.lifecycle')}
            </TabsTrigger>
          )}
          <TabsTrigger value="details">{t('card.tabs.details')}</TabsTrigger>
        </TabsList>

        <TabsContent value="overview" className="mt-3 flex flex-col gap-4">
          {live ? (
            <Observed live={live} lang={lang} />
          ) : observeUnknown ? (
            <NotRead text={t('card.observedNotRead')} />
          ) : (
            <p className="text-sm text-muted-foreground">
              {t('card.noObservationYet')}
            </p>
          )}
          {session.sessionRef && live && (
            <>
              <Separator />
              <SessionTimeline sessionRef={session.sessionRef} />
            </>
          )}
        </TabsContent>

        <TabsContent value="live" className="mt-3">
          {run ? (
            isLiveRun(run) && run.transport === 'stream-json' ? (
              <LiveConsole run={run} />
            ) : (
              <p className="text-sm text-muted-foreground">
                {run.transport === 'stream-json'
                  ? t('card.cap.reason.state')
                  : t('card.relayedIO')}
              </p>
            )
          ) : null}
        </TabsContent>

        <TabsContent value="governance" className="mt-3">
          {run && (
            <GovernancePanel
              run={run}
              onViewEvidence={() => setTab('lifecycle')}
            />
          )}
        </TabsContent>

        <TabsContent value="lifecycle" className="mt-3">
          {run && <EventsPanel runRef={run.run_ref} />}
        </TabsContent>

        <TabsContent value="details" className="mt-3 flex flex-col gap-4">
          <KvList>
            <KvRow label={t('card.sessionRef')} mono align="start">
              {session.sessionRef || t('card.none')}
            </KvRow>
            <KvRow label={t('card.engine')}>
              {live?.engine || t('card.notDeclared')}
            </KvRow>
            <KvRow label={t('card.posture')}>
              {live?.posture
                ? t(`card.postureValue.${live.posture}`, {
                    defaultValue: live.posture,
                  })
                : t('card.notDeclared')}
            </KvRow>
          </KvList>
          {run && <RunInfo run={run} />}
        </TabsContent>
      </Tabs>

      {/* The observed detail sheet carried these two and the unified card must not
          drop them: from a session, the next question is usually what it can reach.
          Feature routes are generated from the registry, so their paths are not in the
          static route union — the shell uses the same `as never` escape hatch. */}
      <div className="flex flex-wrap gap-2">
        <Button variant="secondary" size="sm" asChild>
          <Link to={'/access-map' as never}>
            <Network className="size-3.5" />
            {t('detail.viewAccess')}
          </Link>
        </Button>
        <Button variant="secondary" size="sm" asChild>
          <Link to={'/inventory' as never}>
            <Boxes className="size-3.5" />
            {t('detail.viewInventory')}
          </Link>
        </Button>
      </div>

      <p className="flex items-start gap-1.5 text-[11px] leading-snug text-muted-foreground">
        <ShieldCheck className="mt-0.5 size-3 shrink-0" />
        {t('detail.minimalData')}
      </p>
    </>
  )
}

/** PROVENANCE — a fact about the session, stated in a sentence, with its evidence. */
function ProvenanceBlock({
  session,
  operateUnknown,
  live,
  activeRun,
  onSelectRun,
}: {
  session: UnifiedSession
  operateUnknown: boolean
  live?: LiveDTO
  activeRun?: string
  onSelectRun: (ref: string) => void
}) {
  const { t } = useTranslation('sessions')
  const launched = session.provenance === 'launched'
  // A run the plane DID find outranks not having looked: with a linked run in hand the
  // provenance is known whatever else failed. Only "no runs AND could not look" is
  // unknown — and it must not be dressed as "discovered".
  const unknown = operateUnknown && !launched
  const badge = unknown ? 'unknown' : session.provenance
  return (
    <section className="rounded-md border border-border bg-surface p-3">
      <div className="flex flex-wrap items-center gap-2">
        <span
          className={cn(
            'inline-flex items-center gap-1.5 rounded-sm border px-1.5 py-0.5 text-[11px] font-medium',
            launched
              ? 'border-accent-line bg-accent-soft text-accent-text'
              : unknown
                ? 'border-warning-line bg-warning-soft text-warning'
                : 'border-border bg-muted text-muted-foreground',
          )}
        >
          {launched ? (
            <Terminal className="size-3" />
          ) : unknown ? (
            <HelpCircle className="size-3" />
          ) : (
            <Eye className="size-3" />
          )}
          {t(`card.provenance.${badge}`)}
        </span>
        {session.runs.length > 1 && (
          <span className="text-[11px] text-muted-foreground">
            {t('card.provenance.multipleRuns', { n: session.runs.length })}
          </span>
        )}
      </div>
      <p className="mt-1.5 text-sm text-muted-foreground">
        {unknown
          ? t('card.provenance.unknownExplain')
          : t(`card.provenance.${session.provenance}Explain`)}
      </p>
      {session.runs.length > 0 && (
        <ul className="mt-2 flex flex-col gap-1">
          {session.runs.map((r) => {
            const active = r.run_ref === activeRun
            const many = session.runs.length > 1
            return (
              <li key={r.run_ref}>
                <button
                  type="button"
                  onClick={() => onSelectRun(r.run_ref)}
                  aria-pressed={many ? active : undefined}
                  disabled={!many}
                  className={cn(
                    'flex w-full flex-wrap items-center gap-x-2 gap-y-0.5 rounded-sm px-1 py-0.5 text-left text-xs',
                    many && 'hover:bg-muted',
                    many && active && 'bg-muted',
                  )}
                >
                  <span className="font-mono text-foreground">
                    {r.name || r.run_ref}
                  </span>
                  <RunStateBadge state={r.state} />
                  <span className="text-muted-foreground">
                    {t(`card.transport.${r.transport}`, {
                      defaultValue: r.transport,
                    })}
                  </span>
                  {r.created_at && (
                    <span className="text-muted-foreground">
                      <RelTimeLabel ts={r.created_at} />
                    </span>
                  )}
                  {many && active && (
                    <span className="text-[10px] uppercase tracking-wide text-accent-text">
                      {t('card.activeRun')}
                    </span>
                  )}
                </button>
              </li>
            )
          })}
        </ul>
      )}
      {session.runs.length > 1 && (
        <p className="mt-1 text-[11px] text-muted-foreground">
          {t('card.pickRun')}
        </p>
      )}
      {live?.unclaimed && (
        <p className="mt-2 rounded-md border border-warning-line bg-warning-soft px-2.5 py-1.5 text-xs text-warning">
          {t('card.unclaimed')}
        </p>
      )}
    </section>
  )
}

const CAP_ICON = {
  watch: Eye,
  attach: Radio,
  drive: Terminal,
  stop: Square,
  resume: Play,
  cleanup: Disc3,
  delete: Trash2,
} as const

/** CONTROL — the plane's reach, then the caller's, each with the reason it stops. */
function ControlBlock({
  control,
  caps,
  operateUnknown,
}: {
  control: ControlLevel
  caps: Capability[]
  operateUnknown: boolean
}) {
  const { t } = useTranslation('sessions')
  // When every unavailable capability is blocked by the SAME thing — the usual case
  // for a discovered session, where nothing is operable because there is no process —
  // say it once. Repeating one sentence six times reads as six findings.
  const reasons = new Set(
    caps.filter((c) => !c.available && c.reason).map((c) => c.reason as string),
  )
  const shared = reasons.size === 1 ? [...reasons][0] : undefined
  return (
    <section className="rounded-md border border-border bg-surface p-3">
      <div className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
        {t('card.control.title')}
      </div>
      <p className="mt-1 text-sm text-foreground">
        {t(`card.control.${control}`)}
      </p>
      <p className="mt-0.5 text-sm text-muted-foreground">
        {operateUnknown
          ? t('card.control.unknownExplain')
          : t(`card.control.${control}Explain`)}
      </p>
      <ul className="mt-2 grid grid-cols-1 gap-1 sm:grid-cols-2">
        {caps.map((c) => {
          const Icon = CAP_ICON[c.id]
          return (
            <li
              key={c.id}
              className={cn(
                'flex items-start gap-1.5 text-xs',
                c.available ? 'text-foreground' : 'text-muted-foreground',
              )}
            >
              <Icon
                className={cn(
                  'mt-0.5 size-3 shrink-0',
                  c.available ? 'text-success' : 'opacity-50',
                )}
              />
              <span>
                {t(`card.cap.${c.id}`)}
                {!c.available && c.reason && !shared && (
                  <span className="ml-1 opacity-80">
                    — {t(`card.cap.reason.${c.reason}`)}
                  </span>
                )}
              </span>
            </li>
          )
        })}
      </ul>
      {shared && (
        <p className="mt-1.5 text-xs text-muted-foreground">
          {t('card.cap.sharedReason', {
            reason: t(`card.cap.reason.${shared}`),
          })}
        </p>
      )}
    </section>
  )
}

/** The real lifecycle controls, driven by the SAME capability model shown above, so a
 * button can never appear that the block just said was unavailable. */
function RunActions({
  run,
  caps,
  onClose,
}: {
  run?: RunDTO
  caps: Capability[]
  onClose: () => void
}) {
  const { t } = useTranslation('sessions')
  const { activeTenant } = useAuth()
  const qc = useQueryClient()
  const [confirm, setConfirm] = useState<null | 'cleanup' | 'delete'>(null)

  const invalidate = () => {
    void qc.invalidateQueries({ queryKey: agentOpsKeys.all(activeTenant) })
    void qc.invalidateQueries({ queryKey: sessionsKeys.all(activeTenant) })
  }
  const onErr = (err: unknown) =>
    toast.error(err instanceof ApiError ? err.message : t('card.actionFailed'))

  const stop = useMutation({
    mutationFn: () => agentOpsApi.stop(run?.run_ref as string),
    onSuccess: invalidate,
    onError: onErr,
  })
  const resume = useMutation({
    mutationFn: () => agentOpsApi.resume(run?.run_ref as string),
    onSuccess: invalidate,
    onError: onErr,
  })
  const cleanup = useMutation({
    mutationFn: () => agentOpsApi.cleanup(run?.run_ref as string),
    onSuccess: () => {
      setConfirm(null)
      invalidate()
    },
    onError: onErr,
  })
  const del = useMutation({
    mutationFn: () => agentOpsApi.deleteRun(run?.run_ref as string),
    onSuccess: () => {
      setConfirm(null)
      invalidate()
      onClose()
    },
    onError: onErr,
  })

  const allow = (id: Capability['id']) =>
    caps.some((c) => c.id === id && c.available)
  if (!run) return null
  if (
    !allow('stop') &&
    !allow('resume') &&
    !allow('cleanup') &&
    !allow('delete')
  )
    return null

  return (
    <div className="flex flex-wrap items-center gap-2">
      {allow('stop') && (
        <Button
          variant="secondary"
          size="sm"
          onClick={() => stop.mutate()}
          disabled={stop.isPending}
        >
          <Square className="size-3.5" />
          {t('card.actions.stop')}
        </Button>
      )}
      {allow('resume') && (
        <Button
          variant="secondary"
          size="sm"
          onClick={() => resume.mutate()}
          disabled={resume.isPending}
        >
          <Play className="size-3.5" />
          {t('card.actions.resume')}
        </Button>
      )}
      {allow('cleanup') && (
        <Button
          variant="secondary"
          size="sm"
          onClick={() => setConfirm('cleanup')}
        >
          <Disc3 className="size-3.5" />
          {t('card.actions.cleanup')}
        </Button>
      )}
      {allow('delete') && (
        <Button
          variant="destructive"
          size="sm"
          onClick={() => setConfirm('delete')}
        >
          <Trash2 className="size-3.5" />
          {t('card.actions.delete')}
        </Button>
      )}
      <ConfirmDialog
        open={confirm === 'cleanup'}
        onOpenChange={(o) => !o && setConfirm(null)}
        title={t('card.actions.cleanup')}
        description={t('card.actions.cleanupHint')}
        confirmLabel={t('card.actions.cleanup')}
        pending={cleanup.isPending}
        onConfirm={() => cleanup.mutate()}
      />
      <ConfirmDialog
        open={confirm === 'delete'}
        onOpenChange={(o) => !o && setConfirm(null)}
        tone="danger"
        title={t('card.actions.delete')}
        description={t('card.actions.deleteHint')}
        confirmLabel={t('card.actions.delete')}
        pending={del.isPending}
        onConfirm={() => del.mutate()}
      />
    </div>
  )
}

/** The observed half: objective, running summary and live telemetry. */
function Observed({ live, lang }: { live: LiveDTO; lang: string }) {
  const { t } = useTranslation('sessions')
  return (
    <div className="flex flex-col gap-3">
      {live.cc_state === 'silent_evasion' && (
        <div className="rounded-md border border-danger-line bg-danger-soft px-2.5 py-2 text-xs text-danger">
          {t('evasionBanner')}
        </div>
      )}
      <div className="flex flex-col gap-2">
        <Field
          label={t('detail.goal')}
          value={live.goal}
          fallback={t('detail.noGoal')}
        />
        <Field
          label={t('detail.summary')}
          value={live.summary}
          fallback={t('detail.noSummary')}
        />
      </div>
      <KvList>
        <KvRow label={t('detail.agent')} mono align="start">
          {live.agent_ref || '—'}
        </KvRow>
        <KvRow label={t('detail.model')} mono align="start">
          {live.model_ref || '—'}
        </KvRow>
        <KvRow label={t('detail.action')} align="start">
          {live.current_action || '—'}
        </KvRow>
        <KvRow label={t('detail.resource')} mono align="start">
          {live.current_resource || '—'}
        </KvRow>
        <KvRow label={t('detail.mode')} align="start">
          {live.current_mode ? (
            <AccessModeBadge mode={live.current_mode} />
          ) : (
            '—'
          )}
        </KvRow>
        <KvRow label={t('detail.inputTokens')} mono>
          {formatTokens(live.input_tokens, lang)}
        </KvRow>
        <KvRow label={t('detail.outputTokens')} mono>
          {formatTokens(live.output_tokens, lang)}
        </KvRow>
        <KvRow label={t('detail.cost')} mono>
          {formatMicroUsd(live.cost_micro_usd, { locale: lang })}
        </KvRow>
        <KvRow label={t('detail.events')} mono>
          {formatInt(live.event_count, lang)}
        </KvRow>
        <KvRow label={t('detail.toolCalls')} mono>
          {formatInt(live.tool_call_count, lang)}
        </KvRow>
        <KvRow label={t('detail.duration')} mono>
          {humanDurationSeconds(live.duration_seconds)}
        </KvRow>
        <KvRow label={t('detail.firstSeen')}>
          <RelTimeLabel ts={live.first_event_at} />
        </KvRow>
        <KvRow label={t('detail.lastSeen')}>
          <RelTimeLabel ts={live.last_event_at} />
        </KvRow>
      </KvList>
    </div>
  )
}

function NotRead({ text }: { text: string }) {
  return (
    <p className="rounded-md border border-border bg-muted px-2.5 py-2 text-xs text-muted-foreground">
      {text}
    </p>
  )
}

function Field({
  label,
  value,
  fallback,
}: {
  label: string
  value?: string
  fallback: string
}) {
  return (
    <div>
      <div className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
        {label}
      </div>
      {value ? (
        <p className="mt-0.5 text-sm text-foreground">{value}</p>
      ) : (
        <p className="mt-0.5 text-sm italic text-muted-foreground">
          {fallback}
        </p>
      )}
    </div>
  )
}
