// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useId } from 'react'
import { useMutation, useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { CodeLine } from '@/components/ui/code-line'
import { CopyButton } from '@/components/ui/copy-button'
import { ApiError } from '@/lib/api/errors'
import { formatShortDateTime } from '@/lib/format'
import {
  workspaceDashboardApi as api,
  workspaceDashboardKeys as keys,
} from './api'
import './i18n'

/** The file a person saves the key to; the commands name it, never the key. */
const KEY_FILE = 'olivares-folder.key'

/** The server answers 404 where it does not serve SFTP and 403 where the person may not read
 * and write the folder: in both the card is not there. */
const absent = (error: unknown) =>
  error instanceof ApiError && (error.status === 404 || error.status === 403)

/** An IPv6 host needs its brackets in a user@host:path. */
const hostOf = (hostname: string) =>
  hostname.includes(':') ? `[${hostname}]` : hostname

/**
 * Copy files in and out of a session folder with sftp or scp. It renders nothing until the
 * server says it serves SFTP for the folder, so a host without the route shows no card. Connect
 * asks for a key that opens connections for a few minutes; the key lives only in this
 * component's state, shown once, and is gone when the folder closes.
 */
export function FolderConnect({
  tenant,
  folderRef,
}: {
  tenant: string
  folderRef: string
}) {
  const { t } = useTranslation('workspaceDashboard')
  const headingId = useId()
  const keyId = useId()
  const status = useQuery({
    queryKey: keys.folderSftp(tenant, folderRef),
    queryFn: ({ signal }) => api.folderSftp(folderRef, { tenant, signal }),
    staleTime: 60_000,
    retry: false,
  })
  const connect = useMutation({
    mutationFn: () => api.folderSftpConnect(folderRef, { tenant }),
  })

  if (status.isError && !absent(status.error)) {
    return (
      <p role="alert" className="text-body text-text-2">
        {t('connect.statusFailed')}
      </p>
    )
  }
  if (!status.data) return null

  const cred = connect.data
  const host = hostOf(window.location.hostname)
  const target = `${status.data.user}@${host}`
  const port = status.data.port
  const stepUp =
    connect.error instanceof ApiError &&
    connect.error.code === 'step_up_required'

  return (
    <section
      aria-labelledby={headingId}
      className="flex flex-col gap-3 border-t border-line pt-4"
    >
      <h3
        id={headingId}
        className="text-overline text-muted-foreground uppercase"
      >
        {t('connect.title')}
      </h3>
      <p className="text-body text-text-2">{t('connect.intro')}</p>
      {!cred ? (
        <div className="flex flex-col items-start gap-2">
          <Button
            size="sm"
            disabled={connect.isPending}
            onClick={() => connect.mutate()}
          >
            {connect.isPending ? t('connect.connecting') : t('connect.button')}
          </Button>
          {connect.isError && (
            <p role="alert" className="text-body text-text-2">
              {stepUp ? t('connect.stepUp') : t('connect.failed')}
            </p>
          )}
        </div>
      ) : (
        <div className="flex flex-col gap-3">
          {cred.read_only && (
            <p className="text-body text-text-2">{t('connect.readOnly')}</p>
          )}
          <div className="flex flex-col gap-1.5">
            <div className="flex items-center justify-between gap-2">
              <label htmlFor={keyId} className="text-overline text-text-3">
                {t('connect.privateKey')}
              </label>
              <CopyButton
                text={() => cred.private_key}
                label={t('connect.copyKey')}
                copiedLabel={t('connect.copied')}
                failedLabel={t('connect.copyFailed')}
              />
            </div>
            <textarea
              id={keyId}
              readOnly
              rows={5}
              spellCheck={false}
              value={cred.private_key}
              onFocus={(e) => e.currentTarget.select()}
              className="w-full rounded-ctl border border-ctl-border bg-background px-3 py-2 font-mono text-mono-s outline-none focus-visible:ring-2 focus-visible:ring-focus"
            />
            <p className="text-caption text-text-2">
              {t('connect.keyHint', { file: KEY_FILE })}
            </p>
          </div>
          <CodeLine
            label={t('connect.sftp')}
            command={`sftp -i ${KEY_FILE} -P ${port} ${target}`}
          />
          <CodeLine
            label={t('connect.scp')}
            command={`scp -i ${KEY_FILE} -P ${port} ./file ${target}:`}
          />
          <p className="text-body text-text-2">
            {t('connect.hostKey')}{' '}
            <code className="font-mono text-mono-s break-all">
              {cred.host_key}
            </code>
          </p>
          <p className="text-caption text-text-2">
            {t('connect.expires', {
              time: formatShortDateTime(cred.expires_at),
            })}
          </p>
        </div>
      )}
    </section>
  )
}
