// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE FILES CHANGED IN A SESSION'S FOLDER SINCE IT STARTED, to take away. The list is
// every file changed there since the run began, by the session or anyone else, as far
// as the engine looks: the run's outputs read where the engine has it (the changes
// less its Markdown copies of attached documents), else GET /runs/{ref}/changes. A
// file of a registered folder is read from it like any other read (files/raw: the
// engine's jail, size limit and DLP); one of a folder or worktree of the session's own
// only through the run's own file read, never from the registered folder, whose copy
// may be another version; both are saved through the console's one download helper.
// Download all is the archive the engine builds of the listed files.
import { useMutation, useQuery } from '@tanstack/react-query'
import { Download } from 'lucide-react'
import { useState } from 'react'
import { Trans, useTranslation } from 'react-i18next'
import { QueryErrorState } from '@/components/layout/query-error-state'
import { BoxedList, BoxedListRow } from '@/components/ui/boxed-list'
import { Button } from '@/components/ui/button'
import { EmptyState } from '@/components/ui/empty-state'
import { PathText } from '@/components/ui/path-text'
import { Skeleton } from '@/components/ui/skeleton'
import { toast } from '@/components/ui/toaster'
import { agentOpsApi, agentOpsKeys } from '@/features/agentops/api'
import type { RunDTO } from '@/features/agentops/types'
import { downloadFile } from '@/features/agentops/workspace-file-viewer'
import { ArchiveButton } from '@/features/agentops/workspace-transfer'
import { labelsToast } from '@/features/agentops/workspace-upload'
import { apiFetchRaw, http } from '@/lib/api/client'
import { downloadBlob } from '@/lib/api/download'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import { formatBytes } from '@/lib/format'
import './i18n'

const RUNS = '/v1/m/sessions/runs'

/** GET /runs/{ref}/outputs, on an engine that has it (not on main yet, so typed here):
 * the files changed since the run started, less the engine's Markdown copies of the
 * documents attached to it. Its authorization is the changes read's. */
interface RunOutputsDTO {
  files: { name: string; size: number; modified_at: string }[]
  truncated: boolean
}

/** The list: each file by its path in the session's folder, and whether it was cut. */
interface ResultsRead {
  files: { path: string; size: number }[]
  truncated: boolean
}

/** The core's code for a path no route is registered at: an engine without the read.
 * The core gives it too for a run the caller may not see, so it is never remembered:
 * the next read asks again. Any other code on a 404 is the engine's answer about the
 * run or the file. */
const NO_ROUTE = 'not_found'

/** The session's files: the outputs read where the engine has it, else the changes
 * read. A session outside a registered folder gets null there: nothing could be
 * downloaded from it, as before the outputs read. */
async function readResults(
  runRef: string,
  ownPlace: boolean,
  signal: AbortSignal,
): Promise<ResultsRead | null> {
  try {
    const out = await http.get<RunOutputsDTO>(
      `${RUNS}/${encodeURIComponent(runRef)}/outputs`,
      { signal },
    )
    const files = out.files.map((f) => ({ path: f.name, size: f.size }))
    return { files, truncated: out.truncated }
  } catch (err) {
    const missingRoute =
      err instanceof ApiError && err.status === 404 && err.code === NO_ROUTE
    if (!missingRoute) throw err
  }
  if (ownPlace) return null
  const changes = await agentOpsApi.changes(runRef, { signal })
  return { files: changes.files, truncated: changes.truncated }
}

/** Saves one file of a session in a folder or worktree of its own through the run's
 * own read: the exact bytes, under the file's last name. Its 409 (the engine serves
 * the file from a registered folder) is a refusal here: this session works
 * elsewhere, so the registered folder's copy is never read. */
async function saveOwnFile(runRef: string, path: string): Promise<void> {
  // `path` is the name the outputs read gave, sent back unchanged (the query
  // encodes it), never a path built here.
  const res = await apiFetchRaw(
    `${RUNS}/${encodeURIComponent(runRef)}/outputs/file`,
    { query: { path } },
  )
  downloadBlob(await res.blob(), path.split('/').pop() || 'file')
}

export function SessionResults({
  run,
}: {
  run: Pick<RunDTO, 'run_ref' | 'workspace_ref' | 'worktree_branch'>
}) {
  // The folder reads reach the registered folder only: not a folder of its own the
  // engine made for the run (no folder of record) nor a worktree of its own. Those
  // are read through the run's own reads, where the engine has them.
  return (
    <Results
      runRef={run.run_ref}
      folderRef={run.workspace_ref ?? ''}
      ownPlace={!!run.worktree_branch || !run.workspace_ref}
    />
  )
}

function Results({
  runRef,
  folderRef,
  ownPlace,
}: {
  runRef: string
  folderRef: string
  ownPlace: boolean
}) {
  const { t } = useTranslation('sessions')
  const agentops = useTranslation('agentops').t
  const { can, activeTenant } = useAuth()
  // The file a Download found gone, said under the list until the next Download.
  const [gone, setGone] = useState<string | null>(null)
  // The file a Download saved, said politely under the list until the next Download.
  const [saved, setSaved] = useState<string | null>(null)
  // A sentence that names a file: the page's words, the file's whole path in it, in the
  // sentence's own colour (a toast is drawn on the inverse surface).
  const named = (key: string, path: string) => (
    <Trans
      i18nKey={key}
      ns="sessions"
      components={{
        path: <PathText value={path} copy={false} wrap tone="inherit" />,
      }}
    />
  )
  // A download refused by name, as a toast.
  const refuse = (key: string, path: string) => toast.error(named(key, path))
  // Under the changes read's key, so whatever reads that again reads this too.
  // shortcut: without the outputs read it is every file changed since 2 s before
  // the run started, so a document attached at Start, and the engine's Markdown
  // copy of it, are listed too, and the words say so.
  const read = useQuery({
    queryKey: [...agentOpsKeys.runChanges(activeTenant, runRef), 'results'],
    queryFn: ({ signal }) => readResults(runRef, ownPlace, signal),
  })
  const download = useMutation({
    // Whether the file was saved.
    mutationFn: async (path: string): Promise<boolean> => {
      // A folder or worktree of the session's own is read only through the run's
      // own read: the registered folder's checkout may hold another version.
      if (ownPlace) {
        await saveOwnFile(runRef, path)
        return true
      }
      const file = await agentOpsApi.readFile(folderRef, path)
      // A read cut at the folder's limit is not the file: nothing is saved.
      if (file.truncated) {
        refuse('results.tooLarge', path)
        return false
      }
      downloadFile(file)
      // The data-protection labels the read found, as Download all reports them.
      const classes = (file.sensitivity ?? []).map((hit) => hit.class)
      const labeled = labelsToast([...new Set(classes)], agentops)
      if (labeled) toast.warning(labeled)
      return true
    },
    onMutate: () => {
      setGone(null)
      setSaved(null)
    },
    onSuccess: (done, path) => setSaved(done ? path : null),
    onError: (err, path) => {
      if (!(err instanceof ApiError)) {
        toast.error(t('results.downloadFailed'))
      } else if (err.status === 404 && ownPlace && err.code === NO_ROUTE) {
        // No run file read on this engine: nothing is saved, as before it, and
        // the next Download asks again.
        toast.error(t('results.noOwnDownload'))
      } else if (err.status === 404) {
        // Any other 404 (module_error, output_not_found): the file left the
        // folder since the list was read. Said under the list, read again.
        setGone(path)
        void read.refetch()
      } else if (err.status === 400) {
        // A path the engine does not take as inside the folder: the page's words.
        refuse('results.outsideFolder', path)
      } else if (err.status === 409) {
        // Only the run's own read answers 409, for a registered folder.
        refuse('results.refused', path)
      } else {
        toast.error(err.message)
      }
    },
  })
  // A session of its own place shows nothing while its outputs read is pending, nor
  // on an engine without it; a read that failed says so, as for any session.
  if (ownPlace && (read.isPending || read.data === null)) return null
  const mayDownload = ownPlace || can('sessions:workspace:read')
  const files = read.data?.files ?? []
  // Cut short: more changed files than listed, or more entries than the engine looks
  // through. Download all then saves only the listed ones, and its name says so.
  const truncated = read.data?.truncated === true
  return (
    <section aria-labelledby={`results-${runRef}`} className="mt-4">
      <h3
        id={`results-${runRef}`}
        className="mb-2 text-body font-medium text-text"
      >
        {t('results.title')}
      </h3>
      {read.isPending ? (
        <div role="status" className="flex flex-col gap-2">
          <span className="sr-only">{t('results.loading')}</span>
          <Skeleton className="h-[52px] w-full" />
        </div>
      ) : read.isError ? (
        <QueryErrorState
          error={read.error}
          retry={() => void read.refetch()}
          subject={t('results.subject')}
        />
      ) : files.length === 0 ? (
        <EmptyState
          quiet
          title={t('results.emptyTitle')}
          description={t('results.emptyBody')}
        />
      ) : (
        <div className="flex flex-col gap-2">
          <BoxedList role="list" aria-label={t('results.title')}>
            {files.map((f) => (
              <BoxedListRow key={f.path} role="listitem">
                {/* The engine's path is shown as text, never used to build one here. */}
                <PathText value={f.path} copy={false} className="flex-1" />
                <span className="shrink-0 text-caption text-text-3">
                  {formatBytes(f.size)}
                </span>
                {mayDownload ? (
                  <Button
                    type="button"
                    variant="secondary"
                    size="sm"
                    aria-label={t('results.downloadName', { name: f.path })}
                    disabled={download.isPending}
                    onClick={() => download.mutate(f.path)}
                  >
                    <Download aria-hidden />
                    {t('results.download')}
                  </Button>
                ) : null}
              </BoxedListRow>
            ))}
          </BoxedList>
          {truncated ? (
            <p className="text-caption text-text-2">{t('results.truncated')}</p>
          ) : null}
          {!mayDownload ? (
            <p className="text-caption text-text-2">
              {t('results.noDownload')}
            </p>
          ) : ownPlace ? null : (
            // The archive is the registered folder's, so only there.
            <div>
              <ArchiveButton
                wref={folderRef}
                paths={files.map((f) => f.path)}
                label={
                  truncated
                    ? t('results.downloadListed', { count: files.length })
                    : t('results.downloadAll')
                }
              />
            </div>
          )}
        </div>
      )}
      {gone ? (
        <p role="alert" className="mt-2 text-caption text-warning">
          {named('results.gone', gone)}
        </p>
      ) : null}
      {saved ? (
        <p role="status" className="mt-2 text-caption text-text-2">
          {named('results.downloaded', saved)}
        </p>
      ) : null}
    </section>
  )
}
