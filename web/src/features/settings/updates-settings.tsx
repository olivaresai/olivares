// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Settings > Updates: the running version, the newest signed release in this instance's
// channel with its notes, and the one action that installs it. The engine runs
// `olivares upgrade` (signed, verified, atomic) and restarts itself on the new binary, so
// this screen asks, applies, waits for the engine to come back and reads the result.
import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import { Spinner } from '@/components/ui/spinner'
import { waitForEngineRestart } from '@/features/console/engine-restart'
import { http, queryKeys } from '@/lib/api'
import { ApiError, isOpenCoreSeam } from '@/lib/api/errors'
import { formatDate } from '@/lib/format'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'

/** GET /v1/console/updates and POST /v1/console/updates/apply (core/api/updates.go). */
export interface EngineUpdate {
  edition: string
  /** The version the engine runs now; after an apply it stays the old one until the engine restarts. */
  version: string
  channel?: string
  /** The newest release in the channel, when the check reached it. */
  available?: string
  update_available: boolean
  released_at?: string
  notes?: string
  security?: boolean
  advisories?: string[]
  /** Why the channel could not be checked, in the words `olivares upgrade` uses. */
  check_error?: string
  /** A newer binary than the running one is already installed; the engine runs the old version until it restarts. */
  restart_required?: boolean
  restarting?: boolean
  /** Sessions the engine runs now; the restart that applies an update stops them. */
  running_sessions: number
}

export const updatesApi = {
  get: () => http.get<EngineUpdate>('/v1/console/updates'),
  apply: () => http.post<EngineUpdate>('/v1/console/updates/apply', {}),
}

const updatesKey = ['settings', 'updates'] as const

/** The version the engine answers with now, or undefined while it does not answer. */
async function engineVersion(): Promise<string | undefined> {
  try {
    const info = await http.get<{ version?: string }>('/v1/server-info', {
      anonymous: true,
    })
    return info.version
  } catch {
    return undefined
  }
}

/**
 * An apply whose answer never arrived (a dropped connection, or a proxy that gave up
 * on a long request): the engine keeps installing and restarts, so the page waits for
 * it instead of calling the update refused. The engine's own refusals carry `update_*`.
 */
function answerLost(err: unknown): boolean {
  if (!(err instanceof ApiError)) return true
  return err.status >= 502 && !err.code.startsWith('update_')
}

type Phase = 'idle' | 'waiting' | 'stuck'

/** Mounted for a system administrator only; the engine checks system:admin and the step-up itself. */
export function UpdatesSettings() {
  const { t } = useTranslation('settings')
  const queryClient = useQueryClient()
  const query = useQuery({
    queryKey: updatesKey,
    queryFn: () => updatesApi.get(),
    retry: false,
    refetchOnWindowFocus: false,
  })
  const [confirming, setConfirming] = useState(false)
  const [phase, setPhase] = useState<Phase>('idle')
  const [refusal, setRefusal] = useState<string | null>(null)

  // The engine is back when it answers on a version other than the one it ran.
  const waitForNewVersion = (from: string) => {
    setPhase('waiting')
    void waitForEngineRestart({
      probe: async () => {
        const now = await engineVersion()
        return now !== undefined && now !== from
      },
    }).then((ready) => {
      setPhase(ready ? 'idle' : 'stuck')
      void queryClient.invalidateQueries({ queryKey: updatesKey })
      void queryClient.invalidateQueries({ queryKey: queryKeys.serverInfo })
    })
  }

  const apply = usePrivilegedMutation<void, EngineUpdate>({
    mutationFn: () => updatesApi.apply(),
    successMessage: (status) =>
      status.restarting ? t('updates.applied') : t('updates.alreadyCurrent'),
    stepUpAction: 'updates',
    onError: (err) => {
      if (answerLost(err)) {
        waitForNewVersion(query.data?.version ?? '')
        return true
      }
      setRefusal(err instanceof Error ? err.message : t('updates.failed'))
      void queryClient.invalidateQueries({ queryKey: updatesKey })
      return true
    },
    onDone: (status) => {
      if (!status.restarting) {
        void queryClient.invalidateQueries({ queryKey: updatesKey })
        return
      }
      // No read until the engine is back: the page keeps showing what it knew, with the
      // install disabled, and the answer to the apply (running version old, newest new)
      // would read as "up to date" if it were shown as a status.
      waitForNewVersion(status.version)
    },
  })

  const data = query.data
  const waiting = phase === 'waiting'
  const busy = apply.isPending || waiting
  const status = data?.check_error
    ? 'unchecked'
    : data?.update_available
      ? 'available'
      : 'current'

  return (
    <section
      className="flex flex-col gap-3"
      aria-label={t('updates.title')}
      data-slot="updates-settings"
    >
      <div className="flex flex-wrap items-start justify-end gap-2">
        <Button
          size="sm"
          disabled={busy || query.isFetching}
          onClick={() => {
            setRefusal(null)
            void query.refetch()
          }}
        >
          {query.isFetching ? <Spinner size="sm" aria-hidden /> : null}
          {t('updates.check')}
        </Button>
        {data?.update_available && !data.check_error ? (
          <Button
            variant="primary"
            size="sm"
            disabled={busy}
            onClick={() => setConfirming(true)}
          >
            {apply.isPending ? <Spinner size="sm" aria-hidden /> : null}
            {t('updates.install', { version: data.available })}
          </Button>
        ) : null}
      </div>

      {waiting ? (
        <p
          role="status"
          className="flex items-center gap-2 text-caption text-text-2"
          data-slot="engine-restarting"
        >
          <Spinner size="sm" aria-hidden />
          {t('updates.restarting')}
        </p>
      ) : null}
      {phase === 'stuck' ? (
        <p role="status" className="text-caption text-warning">
          {t('updates.stillRestarting')}
        </p>
      ) : null}
      {refusal ? (
        <p role="alert" className="text-caption text-danger">
          {refusal}
        </p>
      ) : null}

      {query.isLoading ? (
        <div className="flex justify-center py-6">
          <Spinner />
        </div>
      ) : query.isError ? (
        <p className="text-body text-text-2">
          {t(
            isOpenCoreSeam(query.error)
              ? 'updates.unavailable'
              : 'updates.loadFailed',
          )}
        </p>
      ) : data ? (
        <>
          <div className="flex flex-wrap items-center gap-2">
            <Badge
              variant={
                status === 'available'
                  ? 'accent'
                  : status === 'unchecked'
                    ? 'warning'
                    : 'success'
              }
            >
              {t(`updates.status.${status}`)}
            </Badge>
            {data.security ? (
              <Badge variant="warning">{t('updates.security')}</Badge>
            ) : null}
          </div>
          {data.check_error ? (
            <p className="text-caption text-text-2">{data.check_error}</p>
          ) : null}
          {data.restart_required ? (
            <p role="status" className="text-caption text-warning">
              {t('updates.restartRequired')}
            </p>
          ) : null}
          <ul
            aria-label={t('updates.release')}
            className="divide-y divide-line"
          >
            <Fact label={t('updates.running')} value={data.version} />
            <Fact
              label={t('edition.label')}
              value={t(`edition.editions.${data.edition}`, {
                defaultValue: data.edition,
              })}
            />
            {data.channel ? (
              <Fact label={t('updates.channel')} value={data.channel} />
            ) : null}
            {data.available ? (
              <Fact
                label={t('updates.newest')}
                value={data.available}
                note={formatDate(data.released_at)}
              />
            ) : null}
          </ul>
          {data.advisories?.length ? (
            <div className="flex flex-col gap-1">
              <h3 className="text-caption font-medium text-text">
                {t('updates.advisories')}
              </h3>
              <ul className="list-disc pl-5 text-caption text-text-2">
                {data.advisories.map((a) => (
                  <li key={a}>{a}</li>
                ))}
              </ul>
            </div>
          ) : null}
          {data.notes ? (
            <div className="flex flex-col gap-1">
              <h3 className="text-caption font-medium text-text">
                {t('updates.notes')}
              </h3>
              <p className="whitespace-pre-wrap text-body text-text-2">
                {data.notes}
              </p>
            </div>
          ) : null}
        </>
      ) : null}
      <ConfirmDialog
        open={confirming}
        onOpenChange={setConfirming}
        title={t('updates.confirm.title', { version: data?.available })}
        description={
          data?.running_sessions
            ? t('updates.confirm.stop', { count: data.running_sessions })
            : t('updates.confirm.none')
        }
        confirmLabel={t('updates.confirm.confirm')}
        onConfirm={() => {
          setConfirming(false)
          setRefusal(null)
          apply.mutate()
        }}
      />
    </section>
  )
}

function Fact({
  label,
  value,
  note,
}: {
  label: string
  value: string
  note?: string
}) {
  return (
    <li className="flex items-center justify-between gap-4 py-2">
      <span className="text-body text-text-2">{label}</span>
      <span className="min-w-0 truncate text-body text-text">
        {value}
        {note ? <span className="ml-2 text-text-3">{note}</span> : null}
      </span>
    </li>
  )
}
