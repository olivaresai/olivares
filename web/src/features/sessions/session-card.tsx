// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useMutation, useQueryClient } from '@tanstack/react-query'
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
import { LiveDot, RelTimeLabel, humanDurationSeconds } from '@/features/shared'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import { formatInt, formatMicroUsd, formatTokens } from '@/lib/format'
import { cn } from '@/lib/utils'
import { sessionsKeys } from './api'
import { AttributionChip } from './attribution-chip'
import { CcStateBadge } from './cc-state-badge'
import {
  capabilities,
  controlLevel,
  isLiveRun,
  isScopedRow,
  primaryRun,
  sessionLabel,
  type Capability,
  type ControlLevel,
  type UnifiedSession,
} from './provenance'
import type { SessionTarget } from './session-target'
import type { SessionResolution } from './use-session-resolution'
import { SessionTimeline } from './timeline'
import type { Attribution, LiveDTO } from './types'
import './i18n'

export type { SessionTarget } from './session-target'

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
  open: wanted,
  resolution,
  onClose,
  onNavigate,
}: {
  /**
   * Does the operator want the sheet? The work surface separated the two questions this component
   * used to answer with one field: a session being SELECTED (the work surface reads it
   * in three panes) and the operator asking for the full controls. Opening on selection
   * would put a modal over the surface the moment anything was chosen.
   */
  open: boolean
  /**
   * The session, ALREADY RESOLVED by the surface that owns it. The card used
   * to take a target and resolve it itself; the work surface needs the same answer
   * for its panes, and the resolution carries an SSE subscription — so it is done
   * once, above, and handed down. `resolution.target === null` means nothing is open.
   */
  resolution: SessionResolution
  onClose: () => void
  /** Open another row from inside the card (B2: the observation rows that share a
   * managed run's profile and id are separate rows, reachable from it). */
  onNavigate?: (target: SessionTarget) => void
}) {
  const target = resolution.target
  const open = wanted && target !== null
  return (
    <Sheet
      open={open}
      onOpenChange={(o) => {
        if (!o) onClose()
      }}
    >
      <SheetContent className="flex w-full flex-col gap-4 overflow-y-auto sm:max-w-3xl">
        {target && (
          <CardBody
            key={target.liveRef ?? target.sessionRef ?? target.runRef ?? ''}
            resolution={resolution}
            onClose={onClose}
            onNavigate={onNavigate}
          />
        )}
      </SheetContent>
    </Sheet>
  )
}

function CardBody({
  resolution,
  onClose,
  onNavigate,
}: {
  resolution: SessionResolution
  onClose: () => void
  onNavigate?: (target: SessionTarget) => void
}) {
  const { t, i18n } = useTranslation('sessions')
  const lang = i18n.language
  const [tab, setTab] = useState('overview')
  const [selectedRun, setSelectedRun] = useState<string | null>(null)

  const {
    session,
    live,
    related,
    streamStatus,
    operateUnknown,
    observeUnknown,
    loading,
    grants,
  } = resolution
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
          <span className="truncate font-mono text-body">
            {sessionLabel(session)}
          </span>
        </SheetTitle>
        <SheetDescription className="flex flex-wrap items-center gap-2">
          {live && <CcStateBadge state={live.cc_state} />}
          {run && <RunStateBadge state={run.state} />}
          {live && isScopedRow(live) && (
            <AttributionChip attribution={live.attribution} />
          )}
          {live && <LiveDot status={streamStatus} />}
        </SheetDescription>
      </SheetHeader>

      <ProvenanceBlock
        session={session}
        operateUnknown={operateUnknown}
        live={live}
        attribution={live?.attribution}
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
            <p className="text-body text-muted-foreground">
              {t('card.noObservationYet')}
            </p>
          )}
          {live?.attribution === 'managed' && related.length > 0 && (
            <RelatedObservations
              rows={related}
              lang={lang}
              onNavigate={onNavigate}
            />
          )}
          {live && (
            <>
              <Separator />
              <SessionTimeline
                liveRef={live.live_ref || undefined}
                sessionRef={live.session_ref}
              />
            </>
          )}
        </TabsContent>

        <TabsContent value="live" className="mt-3">
          {run ? (
            isLiveRun(run) && run.transport === 'stream-json' ? (
              <LiveConsole run={run} />
            ) : (
              <p className="text-body text-muted-foreground">
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
            <KvRow label={t('card.attributionTitle')}>
              {live
                ? t(`card.attribution.${live.attribution}`, {
                    defaultValue: live.attribution,
                  })
                : t('card.none')}
            </KvRow>
            <KvRow label={t('card.liveRef')} mono align="start">
              {live?.live_ref || t('card.none')}
            </KvRow>
            <KvRow label={t('card.profile')} mono align="start">
              {live?.provider_profile_ref ||
                run?.provider_profile_ref ||
                t('card.none')}
            </KvRow>
            <KvRow label={t('card.provider')}>
              {live?.provider || run?.provider_driver || t('card.notDeclared')}
            </KvRow>
            <KvRow label={t('card.environment')} mono align="start">
              {live?.environment_ref ||
                run?.provider_environment_ref ||
                t('card.none')}
            </KvRow>
            {live?.source_binding_ref && (
              <KvRow label={t('card.binding')} mono align="start">
                {live.source_binding_ref}
              </KvRow>
            )}
            {live?.canonical_sid && (
              <KvRow label={t('card.canonicalSid')} mono align="start">
                {live.canonical_sid}
              </KvRow>
            )}
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
  attribution,
  activeRun,
  onSelectRun,
}: {
  session: UnifiedSession
  operateUnknown: boolean
  live?: LiveDTO
  attribution?: Attribution
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
      <p className="mt-1.5 text-body text-muted-foreground">
        {unknown
          ? t('card.provenance.unknownExplain')
          : t(`card.provenance.${session.provenance}Explain`)}
      </p>
      {attribution && attribution !== 'legacy' && (
        <p className="mt-1 text-caption text-muted-foreground">
          {t(`card.attributionExplain.${attribution}`, { defaultValue: '' })}
        </p>
      )}
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
                    'flex w-full flex-wrap items-center gap-x-2 gap-y-0.5 rounded-sm px-1 py-0.5 text-left text-caption',
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
        <p className="mt-2 rounded-md border border-warning-line bg-warning-soft px-2.5 py-1.5 text-caption text-warning">
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
      <div className="text-caption font-semibold uppercase tracking-wide text-muted-foreground">
        {t('card.control.title')}
      </div>
      <p className="mt-1 text-body text-foreground">
        {t(`card.control.${control}`)}
      </p>
      <p className="mt-0.5 text-body text-muted-foreground">
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
                'flex items-start gap-1.5 text-caption',
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
        <p className="mt-1.5 text-caption text-muted-foreground">
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

/** B2: observation rows that share a managed run's profile and provider id. Read
 * beside the run, opened as their own rows — never folded into the run's evidence. */
function RelatedObservations({
  rows,
  lang,
  onNavigate,
}: {
  rows: LiveDTO[]
  lang: string
  onNavigate?: (target: SessionTarget) => void
}) {
  const { t } = useTranslation('sessions')
  return (
    <section
      className="rounded-md border border-border bg-surface p-3"
      data-testid="related-observations"
    >
      <div className="text-caption font-semibold uppercase tracking-wide text-muted-foreground">
        {t('card.related.title')}
      </div>
      <p className="mt-1 text-caption text-muted-foreground">
        {t('card.related.explain')}
      </p>
      <ul className="mt-2 flex flex-col gap-1">
        {rows.map((r) => (
          <li
            key={r.live_ref}
            className="flex flex-wrap items-center gap-x-2 gap-y-0.5 text-caption"
          >
            <AttributionChip attribution={r.attribution} />
            {r.source_binding_ref && (
              <span className="font-mono text-muted-foreground">
                {r.source_binding_ref}
              </span>
            )}
            <span className="font-mono tabular-nums text-muted-foreground">
              {formatTokens(r.input_tokens, lang)} /{' '}
              {formatTokens(r.output_tokens, lang)}
            </span>
            <span className="font-mono tabular-nums text-foreground">
              {formatMicroUsd(r.cost_micro_usd, { locale: lang })}
            </span>
            <RelTimeLabel
              ts={r.last_event_at}
              className="text-muted-foreground"
            />
            {onNavigate && (
              <Button
                variant="ghost"
                size="sm"
                onClick={() => onNavigate({ liveRef: r.live_ref })}
              >
                {t('card.related.open')}
              </Button>
            )}
          </li>
        ))}
      </ul>
    </section>
  )
}

/** The observed half: objective, running summary and live telemetry. */
function Observed({ live, lang }: { live: LiveDTO; lang: string }) {
  const { t } = useTranslation('sessions')
  return (
    <div className="flex flex-col gap-3">
      {live.cc_state === 'silent_evasion' && (
        <div className="rounded-md border border-danger-line bg-danger-soft px-2.5 py-2 text-caption text-danger">
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
    <p className="rounded-md border border-border bg-muted px-2.5 py-2 text-caption text-muted-foreground">
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
      <div className="text-caption font-semibold uppercase tracking-wide text-muted-foreground">
        {label}
      </div>
      {value ? (
        <p className="mt-0.5 text-body text-foreground">{value}</p>
      ) : (
        <p className="mt-0.5 text-body italic text-muted-foreground">
          {fallback}
        </p>
      )}
    </div>
  )
}
