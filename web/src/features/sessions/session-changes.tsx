// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// WHAT THE SESSION CHANGED, beside its conversation (TARGET §2, the Conductor concept): the
// files in the session's folder that changed since it started, newest first, and the text of
// one of them on click. The engine reads the folder without running anything in it
// (modules/sessions/run_changes.go).
import { useQuery } from '@tanstack/react-query'
import { ArrowLeft } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { FileTypeIcon } from '@/components/ui/file-type-icon'
import { PathText } from '@/components/ui/path-text'
import { agentOpsApi, agentOpsKeys } from '@/features/agentops/api'
import type { RunDTO } from '@/features/agentops/types'
import { CodeDiff } from '@/components/ui/code-diff'
import { headUnavailable } from '@/features/shared/head-unavailable'
import { OpenInEditor } from '@/features/shared/open-in-editor'
import { RelTimeLabel } from '@/features/shared'
import { useAuth } from '@/lib/auth/context'
import { firstChangedLine } from '@/lib/editor-links'
import { formatBytes } from '@/lib/format'
import { TurnCheckpoints } from './turn-checkpoints'
import './i18n'

/** While the agent works the list follows it; a finished session does not change. */
const WORKING_REFRESH_MS = 5_000

export function SessionChanges({ run }: { run: RunDTO }) {
  const { t } = useTranslation('sessions')
  const { activeTenant } = useAuth()
  const [open, setOpen] = useState<string | null>(null)
  const [scope, setScope] = useState('session')
  const working = run.state === 'running' || run.state === 'idle'
  const changes = useQuery({
    queryKey: agentOpsKeys.runChanges(activeTenant, run.run_ref),
    queryFn: ({ signal }) => agentOpsApi.changes(run.run_ref, { signal }),
    refetchInterval: working ? WORKING_REFRESH_MS : false,
  })
  const checkpoints = useQuery({
    queryKey: agentOpsKeys.runCheckpoints(activeTenant, run.run_ref),
    queryFn: ({ signal }) =>
      agentOpsApi.runCheckpoints(run.run_ref, { signal }),
    refetchInterval: working ? WORKING_REFRESH_MS : false,
  })
  const turns = checkpoints.data?.available ? checkpoints.data.turns : []
  const turn =
    scope === 'latest'
      ? turns.at(-1)
      : turns.find((t) => String(t.seq) === scope)
  const baseline = scope === 'session' ? turns[0] : turn
  const tip = scope === 'session' ? turns.at(-1) : turn
  const scopedFiles = turn
    ? turn.files
    : scope === 'session' && turns.length
      ? [
          ...new Map(
            turns.flatMap((t) => t.files).map((f) => [f.path, f]),
          ).values(),
        ]
      : undefined
  const file = useQuery({
    queryKey: [
      ...agentOpsKeys.runChanges(activeTenant, run.run_ref),
      'file',
      scope,
      baseline?.seq,
      tip?.seq,
      open,
    ],
    queryFn: ({ signal }) =>
      agentOpsApi.changedFile(run.run_ref, open!, {
        signal,
        ...(tip
          ? {
              turn: tip.seq,
              side:
                scope === 'session' ? ('current' as const) : ('end' as const),
            }
          : {}),
      }),
    enabled: open !== null,
    refetchInterval: working ? WORKING_REFRESH_MS : false,
  })
  // Compare the selected snapshot range; older sessions without checkpoints use HEAD.
  // Binary and cut-off files cannot be compared.
  const head = useQuery({
    queryKey: [
      ...agentOpsKeys.runChanges(activeTenant, run.run_ref),
      'file',
      scope,
      baseline?.seq,
      tip?.seq,
      open,
      'head',
    ],
    queryFn: ({ signal }) =>
      agentOpsApi.changedFile(run.run_ref, open!, {
        signal,
        ...(baseline
          ? { turn: baseline.seq, side: 'start' as const }
          : { rev: 'HEAD' as const }),
      }),
    enabled:
      open !== null &&
      file.data !== undefined &&
      !file.data.binary &&
      !file.data.truncated,
    refetchOnWindowFocus: false,
  })
  const data = changes.data
  const files = scopedFiles ?? data?.files ?? []
  // The diff and editor link share the same baseline.
  const committed =
    file.data &&
    !file.data.truncated &&
    head.data &&
    !head.data.binary &&
    !head.data.truncated
      ? head.data.text
      : undefined
  const firstChange =
    file.data && !file.data.binary
      ? firstChangedLine(committed, file.data.text)
      : undefined
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
      <select
        aria-label={t('changes.scope')}
        value={scope}
        onChange={(event) => {
          setScope(event.target.value)
          setOpen(null)
        }}
        className="min-w-0 rounded-md border border-border bg-background p-1 text-caption"
      >
        <option value="session">{t('changes.session')}</option>
        <option value="latest" disabled={!turns.length}>
          {t('changes.latest')}
        </option>
        {[...turns].reverse().map((turn) => (
          <option key={turn.seq} value={turn.seq}>
            {t('checkpoints.turn', { n: turn.seq })}
          </option>
        ))}
      </select>
      {checkpoints.isError ? (
        <p role="status" className="text-caption text-danger">
          {t('changes.error')}
        </p>
      ) : null}
      {open === null && scope === 'session' ? (
        <TurnCheckpoints run={run} />
      ) : null}
      {open !== null ? (
        <div className="flex min-w-0 flex-col gap-2">
          <div className="flex flex-wrap items-center justify-between gap-x-2">
            <Button variant="ghost" size="sm" onClick={() => setOpen(null)}>
              <ArrowLeft className="size-3.5" />
              {t('changes.back')}
            </Button>
            {run.workspace_path ? (
              <OpenInEditor
                root={run.workspace_path}
                file={open}
                line={firstChange}
              />
            ) : null}
          </div>
          <PathText value={open} wrap />
          {file.isPending ? (
            <p className="text-caption text-muted-foreground">
              {t('changes.loading')}
            </p>
          ) : file.isError ? (
            <p className="text-caption text-danger">{t('changes.error')}</p>
          ) : file.data.binary || head.data?.binary ? (
            <p className="text-caption text-muted-foreground">
              {tip
                ? t('branchChanges.binary')
                : t('changes.binary', { size: formatBytes(file.data.size) })}
            </p>
          ) : (
            <>
              {committed !== undefined ? (
                <CodeDiff
                  original={committed}
                  modified={file.data.text}
                  language="text"
                  originalLabel={baseline ? t('changes.before') : 'HEAD'}
                  modifiedLabel={t('changes.after')}
                  height="24rem"
                />
              ) : (
                <>
                  <pre
                    data-testid="changes-file"
                    className="max-h-96 overflow-auto whitespace-pre-wrap break-all rounded-md border border-border bg-muted p-2 font-mono text-caption text-foreground"
                  >
                    {file.data.text}
                  </pre>
                  {(baseline ? head.isError : headUnavailable(head.error)) ? (
                    <p role="status" className="text-caption text-danger">
                      {t('changes.error')}
                    </p>
                  ) : null}
                </>
              )}
              {file.data.truncated || head.data?.truncated ? (
                <p className="text-caption text-muted-foreground">
                  {t('changes.fileTruncated')}
                </p>
              ) : null}
            </>
          )}
        </div>
      ) : changes.isPending || checkpoints.isPending ? (
        <p className="text-caption text-muted-foreground">
          {t('changes.loading')}
        </p>
      ) : changes.isError ? (
        <p className="text-caption text-danger">{t('changes.error')}</p>
      ) : !data?.folder ? (
        <p className="text-caption text-muted-foreground">
          {t('changes.none')}
        </p>
      ) : files.length === 0 ? (
        <p className="text-caption text-muted-foreground">
          {t('changes.empty')}
        </p>
      ) : (
        <ul className="flex flex-col" data-testid="changes-list">
          {files.map((f) => (
            <li key={f.path}>
              <button
                type="button"
                onClick={() => setOpen(f.path)}
                className="flex w-full min-w-0 items-center gap-2 rounded-md px-1.5 py-1 text-left outline-none hover:bg-muted focus-visible:ring-2 focus-visible:ring-ring"
              >
                <FileTypeIcon
                  path={f.path}
                  className="size-3.5 shrink-0 text-muted-foreground"
                />
                <PathText value={f.path} copy={false} className="flex-1" />
                {'modified_at' in f ? (
                  <RelTimeLabel
                    ts={f.modified_at}
                    className="shrink-0 text-caption text-muted-foreground"
                  />
                ) : null}
              </button>
            </li>
          ))}
          {(turn?.truncated ??
          (turns.some((t) => t.truncated) || data?.truncated)) ? (
            <li className="px-1.5 pt-1 text-caption text-muted-foreground">
              {t('changes.truncated', { count: files.length })}
            </li>
          ) : null}
        </ul>
      )}
    </section>
  )
}
