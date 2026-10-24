// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Settings > Updates: the running version, the newest signed release in this instance's
// channel with its notes, and the one action that installs it. The engine runs
// `olivares upgrade` (signed, verified, atomic) and restarts itself on the new binary, so
// this screen asks, applies, waits for the engine to come back and reads the result.
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import { Spinner } from '@/components/ui/spinner'
import { isOpenCoreSeam } from '@/lib/api/errors'
import { formatDate } from '@/lib/format'
import { EditionSwitchView } from './edition-switch'
import { UpdateProgress } from './update-progress'
import { useEngineUpdate } from './use-engine-update'

export { updatesApi, type EngineUpdate } from './use-engine-update'

/** Mounted for a system administrator only; the engine checks system:admin and the step-up itself. */
export function UpdatesSettings() {
  const { t } = useTranslation('settings')
  const update = useEngineUpdate()
  const { query, apply, phase, refusal, clearRefusal, busy } = update
  const [confirming, setConfirming] = useState(false)

  const data = query.data
  // The engine says console_action when its Install is the licensed switch (EditionSwitchView has
  // that button); without it Install stays the public-channel update, with the step beside it.
  const installIsTheSwitch = data?.edition_switch?.console_action === true
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
            clearRefusal()
            void query.refetch()
          }}
        >
          {query.isFetching ? <Spinner size="sm" aria-hidden /> : null}
          {t('updates.check')}
        </Button>
        {data?.update_available && !data.check_error && !installIsTheSwitch ? (
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

      <UpdateProgress phase={phase} refusal={refusal} />

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
          {data.edition_switch ? (
            <EditionSwitchView update={update} progress={false} />
          ) : null}
          {data.check_error && !data.edition_switch ? (
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
          clearRefusal()
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
