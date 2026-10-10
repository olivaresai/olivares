// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// WHAT THE SESSION'S WORKTREE BRANCH HOLDS, against where it began (K4.A2): the paths
// its commits changed since the branch left the workspace's current commit, and for one
// path its text at that base beside its text at the branch tip, in the existing diff
// view. It is what a person reviews after opening the work a handoff names. Committed
// work only: uncommitted edits are what session-changes.tsx shows. The engine reads git's
// objects and runs nothing in the session's folder (modules/sessions/run_worktree_diff.go).
import { useQuery } from '@tanstack/react-query'
import { ArrowLeft, FileText } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { CodeDiff } from '@/components/ui/code-diff'
import { agentOpsApi, agentOpsKeys } from '@/features/agentops/api'
import type { RunDTO } from '@/features/agentops/types'
import { useAuth } from '@/lib/auth/context'
import './i18n'

/** While the agent works its commits arrive; a finished session does not change. */
const WORKING_REFRESH_MS = 5_000
/** A commit id is shown by its first twelve digits, as git's short form. */
const SHORT_ID = 12

export function SessionBranchChanges({ run }: { run: RunDTO }) {
  const { t } = useTranslation('sessions')
  const { activeTenant } = useAuth()
  const [open, setOpen] = useState<string | null>(null)
  const working = run.state === 'running' || run.state === 'idle'
  const diff = useQuery({
    queryKey: agentOpsKeys.runWorktreeDiff(activeTenant, run.run_ref),
    queryFn: ({ signal }) => agentOpsApi.worktreeDiff(run.run_ref, { signal }),
    refetchInterval: working ? WORKING_REFRESH_MS : false,
  })
  const file = useQuery({
    queryKey: agentOpsKeys.runWorktreeDiffFile(activeTenant, run.run_ref, open),
    queryFn: ({ signal }) =>
      agentOpsApi.worktreeDiffFile(run.run_ref, open!, { signal }),
    enabled: open !== null,
    // The header's range follows the branch, so the open file does too.
    refetchInterval: working ? WORKING_REFRESH_MS : false,
  })
  const data = diff.data

  return (
    <section
      data-testid="session-branch-changes"
      className="flex flex-col gap-2"
    >
      <div>
        <h3 className="text-caption font-medium text-foreground">
          {t('branchChanges.title')}
        </h3>
        <p className="text-caption text-muted-foreground">
          {t('branchChanges.hint')}
        </p>
        {data ? (
          <p
            data-testid="branch-changes-range"
            className="break-all font-mono text-caption text-muted-foreground"
          >
            {t('branchChanges.range', {
              base: data.base.slice(0, SHORT_ID),
              head: data.head.slice(0, SHORT_ID),
            })}
          </p>
        ) : null}
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
            {t('branchChanges.back')}
          </Button>
          <p className="break-all font-mono text-caption text-foreground">
            {open}
          </p>
          {file.isPending ? (
            <p className="text-caption text-muted-foreground">
              {t('branchChanges.loading')}
            </p>
          ) : file.isError ? (
            <p className="text-caption text-danger">
              {t('branchChanges.error')}
            </p>
          ) : file.data.binary ? (
            <p className="text-caption text-muted-foreground">
              {t('branchChanges.binary')}
            </p>
          ) : (
            <>
              <CodeDiff
                original={file.data.original}
                modified={file.data.modified}
                language="text"
                originalLabel={t('branchChanges.original')}
                modifiedLabel={t('branchChanges.modified')}
              />
              {file.data.truncated ? (
                <p className="text-caption text-muted-foreground">
                  {t('branchChanges.fileTruncated')}
                </p>
              ) : null}
            </>
          )}
        </div>
      ) : diff.isPending ? (
        <p className="text-caption text-muted-foreground">
          {t('branchChanges.loading')}
        </p>
      ) : diff.isError ? (
        <p className="text-caption text-danger">{t('branchChanges.error')}</p>
      ) : data!.files.length === 0 ? (
        <p className="text-caption text-muted-foreground">
          {t('branchChanges.empty')}
        </p>
      ) : (
        <ul className="flex flex-col" data-testid="branch-changes-list">
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
                <Badge variant="neutral">
                  {t(`branchChanges.status.${f.status}`)}
                </Badge>
              </button>
            </li>
          ))}
          {data!.truncated ? (
            <li className="px-1.5 pt-1 text-caption text-muted-foreground">
              {t('branchChanges.truncated', { count: data!.files.length })}
            </li>
          ) : null}
        </ul>
      )}
    </section>
  )
}
