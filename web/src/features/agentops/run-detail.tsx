// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  Disc3,
  Play,
  ShieldCheck,
  Square,
  Terminal,
  Trash2,
} from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import { KvList, KvRow } from '@/components/ui/kv'
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
import { RelTimeLabel } from '@/features/shared'
import { useAuth } from '@/lib/auth/context'
import { ApiError } from '@/lib/api/errors'
import { cn } from '@/lib/utils'
import { agentOpsApi, agentOpsKeys } from './api'
import { GovernancePanel } from './governance-panel'
import { LiveConsole } from './live-console'
import { RunStateBadge } from './run-state-badge'
import type { RunDTO, RunEventDTO } from './types'
import './i18n'

/**
 * RunDetailSheet — one operated session's full surface: a live attach console, the
 * governance posture, the anchored lifecycle ledger, and its details — plus the
 * lifecycle controls (stop / resume / clean up / delete), each gated by RBAC and
 * surfacing the backend's honest denial (e.g. a kill-switch stop on resume) verbatim.
 */
export function RunDetailSheet({
  runRef,
  onClose,
}: {
  runRef: string | null
  onClose: () => void
}) {
  const { t } = useTranslation('agentops')
  const { activeTenant, can } = useAuth()
  const qc = useQueryClient()
  const open = runRef !== null
  const [tab, setTab] = useState('live')

  const runQuery = useQuery({
    queryKey: agentOpsKeys.run(activeTenant, runRef ?? ''),
    queryFn: () => agentOpsApi.getRun(runRef as string),
    enabled: open && !!runRef,
    refetchInterval: 5_000,
  })
  const run = runQuery.data

  return (
    <Sheet
      open={open}
      onOpenChange={(o) => {
        if (!o) onClose()
      }}
    >
      <SheetContent className="flex w-full flex-col gap-4 overflow-y-auto sm:max-w-3xl">
        {run ? (
          <>
            <SheetHeader>
              <SheetTitle className="flex items-center gap-2">
                <Terminal className="size-4 text-accent-text" />
                <span className="truncate font-mono text-base">
                  {run.name || run.run_ref}
                </span>
              </SheetTitle>
              <SheetDescription className="flex flex-wrap items-center gap-2">
                <RunStateBadge state={run.state} />
                <GovBadges run={run} />
              </SheetDescription>
            </SheetHeader>

            <RunActions run={run} />

            <Tabs value={tab} onValueChange={setTab}>
              <TabsList>
                <TabsTrigger value="live">{t('detail.live')}</TabsTrigger>
                <TabsTrigger value="governance">
                  {t('detail.governance')}
                </TabsTrigger>
                <TabsTrigger value="events">{t('detail.events')}</TabsTrigger>
                <TabsTrigger value="info">{t('detail.info')}</TabsTrigger>
              </TabsList>

              <TabsContent value="live" className="mt-3">
                <LiveConsole run={run} />
              </TabsContent>
              <TabsContent value="governance" className="mt-3">
                <GovernancePanel
                  run={run}
                  onViewEvidence={() => setTab('events')}
                />
              </TabsContent>
              <TabsContent value="events" className="mt-3">
                {open && runRef && <EventsPanel runRef={runRef} />}
              </TabsContent>
              <TabsContent value="info" className="mt-3">
                <RunInfo run={run} />
              </TabsContent>
            </Tabs>

            <p className="flex items-start gap-1.5 text-[11px] leading-snug text-muted-foreground">
              <ShieldCheck className="mt-0.5 size-3 shrink-0" />
              {t('detail.minimalData')}
            </p>
          </>
        ) : runQuery.isLoading ? (
          <div className="flex items-center justify-center p-12">
            <Spinner />
          </div>
        ) : runQuery.error ? (
          <p role="alert" className="p-6 text-sm text-danger">
            {runQuery.error instanceof ApiError
              ? runQuery.error.message
              : String(runQuery.error)}
          </p>
        ) : null}
      </SheetContent>
    </Sheet>
  )

  function RunActions({ run }: { run: RunDTO }) {
    const canWrite = can('sessions:run:write')
    const canAdmin = can('sessions:run:admin')
    const [confirm, setConfirm] = useState<null | 'cleanup' | 'delete'>(null)

    const invalidate = () =>
      qc.invalidateQueries({ queryKey: agentOpsKeys.all(activeTenant) })
    const onErr = (err: unknown) =>
      toast.error(err instanceof ApiError ? err.message : t('title'))

    const stop = useMutation({
      mutationFn: () => agentOpsApi.stop(run.run_ref),
      onSuccess: () => invalidate(),
      onError: onErr,
    })
    const resume = useMutation({
      mutationFn: () => agentOpsApi.resume(run.run_ref),
      onSuccess: () => invalidate(),
      onError: onErr,
    })
    const cleanup = useMutation({
      mutationFn: () => agentOpsApi.cleanup(run.run_ref),
      onSuccess: () => {
        setConfirm(null)
        invalidate()
      },
      onError: onErr,
    })
    const del = useMutation({
      mutationFn: () => agentOpsApi.deleteRun(run.run_ref),
      onSuccess: () => {
        setConfirm(null)
        invalidate()
        onClose()
      },
      onError: onErr,
    })

    const stoppable = ['pending', 'running', 'idle'].includes(run.state)
    const resumable =
      ['stopped', 'failed'].includes(run.state) &&
      (run.transport !== 'stream-json' || !!run.claude_session_id)
    const cleanable = ['stopped', 'failed'].includes(run.state)
    const deletable = run.state === 'cleaned'

    if (!canWrite && !canAdmin) return null

    return (
      <div className="flex flex-wrap items-center gap-2">
        {canWrite && stoppable && (
          <Button
            variant="secondary"
            size="sm"
            onClick={() => stop.mutate()}
            disabled={stop.isPending}
          >
            <Square className="size-3.5" />
            {t('actions.stop')}
          </Button>
        )}
        {canWrite && resumable && (
          <Button
            variant="secondary"
            size="sm"
            onClick={() => resume.mutate()}
            disabled={resume.isPending}
          >
            <Play className="size-3.5" />
            {t('actions.resume')}
          </Button>
        )}
        {canAdmin && cleanable && (
          <Button
            variant="secondary"
            size="sm"
            onClick={() => setConfirm('cleanup')}
          >
            <Disc3 className="size-3.5" />
            {t('actions.cleanup')}
          </Button>
        )}
        {canAdmin && deletable && (
          <Button
            variant="destructive"
            size="sm"
            onClick={() => setConfirm('delete')}
          >
            <Trash2 className="size-3.5" />
            {t('actions.delete')}
          </Button>
        )}

        <ConfirmDialog
          open={confirm === 'cleanup'}
          onOpenChange={(o) => !o && setConfirm(null)}
          title={t('actions.cleanup')}
          description={t('detail.minimalData')}
          confirmLabel={t('actions.cleanup')}
          pending={cleanup.isPending}
          onConfirm={() => cleanup.mutate()}
        />
        <ConfirmDialog
          open={confirm === 'delete'}
          onOpenChange={(o) => !o && setConfirm(null)}
          tone="danger"
          title={t('actions.delete')}
          confirmLabel={t('actions.delete')}
          pending={del.isPending}
          onConfirm={() => del.mutate()}
        />
      </div>
    )
  }
}

function GovBadges({ run }: { run: RunDTO }) {
  const { t } = useTranslation('agentops')
  return (
    <span className="flex flex-wrap items-center gap-1">
      {run.transport !== 'remote-control' && (
        <Chip
          tone={run.pep_provisioned ? 'ok' : 'warn'}
          title={
            run.pep_provisioned
              ? t('badge.governedHint')
              : t('badge.denyClosedHint')
          }
        >
          {run.pep_provisioned ? t('badge.governed') : t('badge.denyClosed')}
        </Chip>
      )}
      {run.record_io && (
        <Chip tone="ok" title={t('badge.recordingHint')}>
          {t('badge.recording')}
        </Chip>
      )}
      {run.critical && (
        <Chip tone="danger" title={t('badge.criticalHint')}>
          {t('badge.critical')}
        </Chip>
      )}
    </span>
  )
}

function Chip({
  tone,
  title,
  children,
}: {
  tone: 'ok' | 'warn' | 'danger'
  title?: string
  children: React.ReactNode
}) {
  const cls =
    tone === 'ok'
      ? 'border-success-line bg-success-soft text-success'
      : tone === 'warn'
        ? 'border-warning-line bg-warning-soft text-warning'
        : 'border-danger-line bg-danger-soft text-danger'
  return (
    <span
      title={title}
      className={cn(
        'inline-flex items-center rounded-sm border px-1.5 py-0.5 text-[11px] font-medium',
        cls,
      )}
    >
      {children}
    </span>
  )
}

// Exported for the unified session card, which renders the SAME lifecycle
// ledger and the SAME run details as this sheet — one session, one card.
export function EventsPanel({ runRef }: { runRef: string }) {
  const { t } = useTranslation('agentops')
  const { activeTenant } = useAuth()
  const q = useQuery({
    queryKey: agentOpsKeys.runEvents(activeTenant, runRef, { limit: 200 }),
    queryFn: () => agentOpsApi.runEvents(runRef, { limit: 200 }),
  })
  const events = q.data?.items ?? []

  return (
    <div className="flex flex-col gap-2">
      <p className="text-xs text-muted-foreground">{t('events.subtitle')}</p>
      {q.isLoading ? (
        <div className="flex justify-center p-6">
          <Spinner />
        </div>
      ) : events.length === 0 ? (
        <p className="p-4 text-sm text-muted-foreground">{t('events.empty')}</p>
      ) : (
        <ol className="divide-y divide-border rounded-md border border-border">
          {events.map((e) => (
            <EventRow key={e.seq} e={e} />
          ))}
        </ol>
      )}
    </div>
  )
}

function EventRow({ e }: { e: RunEventDTO }) {
  const { t } = useTranslation('agentops')
  return (
    <li className="flex flex-col gap-0.5 px-3 py-2">
      <div className="flex items-center justify-between gap-2">
        <span className="font-mono text-xs font-medium text-foreground">
          #{e.seq} · {e.event}
        </span>
        <RelTimeLabel ts={e.at} />
      </div>
      <div className="flex flex-wrap items-center gap-x-3 gap-y-0.5 text-xs text-muted-foreground">
        {(e.from_state || e.to_state) && (
          <span>
            {e.from_state || '—'} → {e.to_state || '—'}
          </span>
        )}
        {e.actor && <span className="font-mono">{e.actor}</span>}
        {e.detail && <span className="italic">{e.detail}</span>}
      </div>
      <div className="flex flex-wrap items-center gap-x-3 text-[11px] text-muted-foreground">
        <span className="truncate font-mono" title={e.payload_hash}>
          {t('events.payloadHash')}: {e.payload_hash.slice(0, 16)}…
        </span>
        <span className="font-mono">
          {t('events.auditSeq')}: {e.audit_seq}
        </span>
      </div>
    </li>
  )
}

export function RunInfo({ run }: { run: RunDTO }) {
  const { t } = useTranslation('agentops')
  const none = t('info.none')
  return (
    <KvList>
      <KvRow label={t('info.runRef')} mono align="start">
        {run.run_ref}
      </KvRow>
      <KvRow label={t('info.agent')} mono align="start">
        {run.agent_ref || none}
      </KvRow>
      <KvRow label={t('info.transport')}>
        {t(`transport.${run.transport}`, { defaultValue: run.transport })}
      </KvRow>
      <KvRow label={t('info.isolation')}>
        {t(`isolation.${run.isolation}`, { defaultValue: run.isolation })}
      </KvRow>
      <KvRow label={t('info.permissionMode')} mono>
        {run.permission_mode}
      </KvRow>
      <KvRow label={t('info.effort')} mono>
        {run.effort || none}
      </KvRow>
      <KvRow label={t('info.model')} mono align="start">
        {run.model_ref || none}
      </KvRow>
      <KvRow label={t('info.workspace')} mono align="start">
        {run.workspace_ref || none}
      </KvRow>
      <KvRow label={t('info.claudeSessionId')} mono align="start">
        {run.claude_session_id || none}
      </KvRow>
      <KvRow label={t('info.credentialId')} mono align="start">
        {run.credential_id || none}
      </KvRow>
      <KvRow label={t('info.pid')} mono>
        {run.pid ?? none}
      </KvRow>
      <KvRow label={t('info.exitCode')} mono>
        {run.exit_code ?? none}
      </KvRow>
      <KvRow label={t('info.reason')} align="start">
        {run.reason || none}
      </KvRow>
      <KvRow label={t('info.created')}>
        {run.created_at ? <RelTimeLabel ts={run.created_at} /> : none}
      </KvRow>
      <KvRow label={t('info.started')}>
        {run.started_at ? <RelTimeLabel ts={run.started_at} /> : none}
      </KvRow>
      <KvRow label={t('info.lastActivity')}>
        {run.last_activity_at ? (
          <RelTimeLabel ts={run.last_activity_at} />
        ) : (
          none
        )}
      </KvRow>
      <KvRow label={t('info.stopped')}>
        {run.stopped_at ? <RelTimeLabel ts={run.stopped_at} /> : none}
      </KvRow>
    </KvList>
  )
}
