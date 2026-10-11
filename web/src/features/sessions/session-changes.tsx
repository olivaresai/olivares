// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// WHAT THE SESSION CHANGED, beside its conversation (TARGET §2, the Conductor concept): the
// files in the session's folder that changed since it started, newest first, and the text of
// one of them on click. The engine reads the folder without running anything in it
// (modules/sessions/run_changes.go).
import { useQuery } from '@tanstack/react-query'
import { ArrowLeft, FileText } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { agentOpsApi, agentOpsKeys } from '@/features/agentops/api'
import type { RunDTO } from '@/features/agentops/types'
import { HeadDiff } from '@/features/shared/head-diff'
import { headUnavailable } from '@/features/shared/head-unavailable'
import { RelTimeLabel } from '@/features/shared'
import { useAuth } from '@/lib/auth/context'
import { formatBytes } from '@/lib/format'
import './i18n'

/** While the agent works the list follows it; a finished session does not change. */
const WORKING_REFRESH_MS = 5_000

export function SessionChanges({ run }: { run: RunDTO }) {
  const { t } = useTranslation('sessions')
  const { activeTenant } = useAuth()
  const [open, setOpen] = useState<string | null>(null)
  const working = run.state === 'running' || run.state === 'idle'
  const changes = useQuery({
    queryKey: agentOpsKeys.runChanges(activeTenant, run.run_ref),
    queryFn: ({ signal }) => agentOpsApi.changes(run.run_ref, { signal }),
    refetchInterval: working ? WORKING_REFRESH_MS : false,
  })
  const file = useQuery({
    queryKey: [
      ...agentOpsKeys.runChanges(activeTenant, run.run_ref),
      'file',
      open,
    ],
    queryFn: ({ signal }) =>
      agentOpsApi.changedFile(run.run_ref, open!, { signal }),
    enabled: open !== null,
  })
  // What git HEAD holds for the open file. A 404 (no repository, a file the agent
  // created) is an answer: the file shows without a Changes view. Any other failure is
  // said so under the file. Nothing is asked for a binary or cut-off file, which cannot
  // be compared, and a focus change does not ask again.
  const head = useQuery({
    queryKey: [
      ...agentOpsKeys.runChanges(activeTenant, run.run_ref),
      'file',
      open,
      'head',
    ],
    queryFn: ({ signal }) =>
      agentOpsApi.changedFile(run.run_ref, open!, { signal, rev: 'HEAD' }),
    enabled:
      open !== null &&
      file.data !== undefined &&
      !file.data.binary &&
      !file.data.truncated,
    refetchOnWindowFocus: false,
  })
  const data = changes.data
  // The folder Olivares created for the run is named by the run's id: no name to show.
  const title =
    data?.folder && data.folder !== run.run_ref
      ? t('changes.title', { folder: data.folder })
      : t('changes.titleNoFolder')

  return (
    <section data-testid="session-changes" className="flex flex-col gap-2">
      <div>
        <h3 className="text-caption font-medium text-foreground">{title}</h3>
        <p className="text-caption text-muted-foreground">
          {t('changes.hint')}
        </p>
      </div>
      {open !== null ? (
        <div className="flex min-w-0 flex-col gap-2">
          <Button
            variant="ghost"
            size="sm"
            className="self-start"
            onClick={() => setOpen(null)}
          >
            <ArrowLeft className="size-3.5" />
            {t('changes.back')}
          </Button>
          <p className="break-all font-mono text-caption text-foreground">
            {open}
          </p>
          {file.isPending ? (
            <p className="text-caption text-muted-foreground">
              {t('changes.loading')}
            </p>
          ) : file.isError ? (
            <p className="text-caption text-danger">{t('changes.error')}</p>
          ) : file.data.binary ? (
            <p className="text-caption text-muted-foreground">
              {t('changes.binary', { size: formatBytes(file.data.size) })}
            </p>
          ) : (
            <>
              <HeadDiff
                unavailable={headUnavailable(head.error)}
                committed={
                  head.data &&
                  !head.data.binary &&
                  !head.data.truncated &&
                  !file.data.truncated
                    ? head.data.text
                    : undefined
                }
                current={file.data.text}
              >
                <pre
                  data-testid="changes-file"
                  className="max-h-96 overflow-auto whitespace-pre-wrap break-all rounded-md border border-border bg-muted p-2 font-mono text-caption text-foreground"
                >
                  {file.data.text}
                </pre>
              </HeadDiff>
              {file.data.truncated ? (
                <p className="text-caption text-muted-foreground">
                  {t('changes.fileTruncated')}
                </p>
              ) : null}
            </>
          )}
        </div>
      ) : changes.isPending ? (
        <p className="text-caption text-muted-foreground">
          {t('changes.loading')}
        </p>
      ) : changes.isError ? (
        <p className="text-caption text-danger">{t('changes.error')}</p>
      ) : !data!.folder ? (
        <p className="text-caption text-muted-foreground">
          {t('changes.none')}
        </p>
      ) : data!.files.length === 0 ? (
        <p className="text-caption text-muted-foreground">
          {t('changes.empty')}
        </p>
      ) : (
        <ul className="flex flex-col" data-testid="changes-list">
          {data!.files.map((f) => (
            <li key={f.path}>
              <button
                type="button"
                onClick={() => setOpen(f.path)}
                className="flex w-full min-w-0 items-center gap-2 rounded-md px-1.5 py-1 text-left outline-none hover:bg-muted focus-visible:ring-2 focus-visible:ring-ring"
              >
                <FileText
                  className="size-3.5 shrink-0 text-muted-foreground"
                  aria-hidden
                />
                <span className="min-w-0 flex-1 truncate font-mono text-caption text-foreground">
                  {f.path}
                </span>
                <RelTimeLabel
                  ts={f.modified_at}
                  className="shrink-0 text-caption text-muted-foreground"
                />
              </button>
            </li>
          ))}
          {data!.truncated ? (
            <li className="px-1.5 pt-1 text-caption text-muted-foreground">
              {t('changes.truncated', { count: data!.files.length })}
            </li>
          ) : null}
        </ul>
      )}
    </section>
  )
}
