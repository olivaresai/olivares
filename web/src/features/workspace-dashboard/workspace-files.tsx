// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useMemo, useState } from 'react'
import { useInfiniteQuery, useQueries, useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { Folder, FolderOpen, FolderPlus, GitBranch, Info } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { ConsolePage } from '@/components/layout/page-frames'
import { QueryErrorState } from '@/components/layout/query-error-state'
import { Button } from '@/components/ui/button'
import { PageSecondaryActions } from '@/components/ui/page-actions'
import { PageHeader } from '@/components/ui/page-header'
import { PathText } from '@/components/ui/path-text'
import { Spinner } from '@/components/ui/spinner'
import { StateBlock } from '@/components/ui/state-block'
import { agentOpsApi, agentOpsKeys } from '@/features/agentops/api'
import { useAuthBoundary } from '@/features/agentops/auth-boundary'
import type { RunDTO, WorkspaceDTO } from '@/features/agentops/types'
import { WorkspaceBrowser } from '@/features/agentops/workspace-browser'
import { useViewAccess } from '@/features/navigation/authorization'
import { viewById } from '@/features/navigation/model'
import { TAB_PARAM } from '@/features/sessions/session-address'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import { useTabParam } from '@/lib/hooks/use-tab-param'
import { useUrlState } from '@/lib/hooks/use-url-state'
import { cn } from '@/lib/utils'
import { useModuleOn } from '@/stores/modules'
import { folderName } from './folder-name'
import { FolderDetailsSheet } from './session-folders'
import { WorkspaceDashboardView } from './workspace-dashboard-view'
import './i18n'

const FOLDER_PAGE = 100
/** The sessions read to find worktrees: the list is newest first. */
const RUN_PAGE = 200
/** Each git state read runs git in the session's boundary, so only these many rows ask. */
const STATUS_ROWS = 12

/** Where folders are registered; the door whose permission that tab needs. */
const FOLDERS_VIEW = viewById('agentops')

interface Worktree {
  branch: string
  run: string
  /** The folder registered at this worktree's path, when there is one to browse. */
  folder?: WorkspaceDTO
}

/** One row per branch, newest session first. A cleaned session's worktree is gone. */
function worktreesByFolder(runs: RunDTO[], folders: WorkspaceDTO[]) {
  const byFolder = new Map<string, Worktree[]>()
  for (const run of runs) {
    if (!run.worktree_branch || !run.workspace_ref || run.state === 'cleaned')
      continue
    const rows = byFolder.get(run.workspace_ref) ?? []
    if (rows.some((w) => w.branch === run.worktree_branch)) continue
    rows.push({
      branch: run.worktree_branch,
      run: run.run_ref,
      folder: folders.find((f) => f.root_path === run.workspace_path),
    })
    byFolder.set(run.workspace_ref, rows)
  }
  return byFolder
}

/**
 * /workspace: the files of the folders sessions work in, and behind `?tab=overview` the
 * organization workspace's overview. Whoever cannot read session folders gets the overview,
 * which is what this address has always shown them.
 */
export function WorkspaceView() {
  const { t } = useTranslation('workspaceDashboard')
  const { activeTenant, can } = useAuth()
  const sessionsOn = useModuleOn('sessions')
  const [tab, setTab] = useTabParam(['files', 'overview'])
  const showsFiles =
    !!activeTenant && sessionsOn && can('sessions:workspace:read')

  if (tab === 'overview' || !activeTenant || !showsFiles)
    return (
      <ConsolePage>
        {showsFiles && (
          <PageSecondaryActions>
            <Button
              variant="secondary"
              size="sm"
              onClick={() => setTab('files')}
            >
              {t('files.backToFiles')}
            </Button>
          </PageSecondaryActions>
        )}
        <WorkspaceDashboardView />
      </ConsolePage>
    )
  return (
    <WorkspaceFiles
      tenant={activeTenant}
      onOverview={() => setTab('overview')}
    />
  )
}

function WorkspaceFiles({
  tenant,
  onOverview,
}: {
  tenant: string
  onOverview: () => void
}) {
  const boundary = useAuthBoundary()
  return (
    <FilesPage
      key={boundary.epoch}
      tenant={tenant}
      epoch={boundary.epoch}
      onOverview={onOverview}
    />
  )
}

function FilesPage({
  tenant,
  epoch,
  onOverview,
}: {
  tenant: string
  epoch: number
  onOverview: () => void
}) {
  const { t } = useTranslation(['workspaceDashboard', 'agentops'])
  const { can } = useAuth()
  const { navigable } = useViewAccess()
  const scope = agentOpsKeys.boundaryScope(tenant, epoch)
  const [location, patchLocation] = useUrlState(['workspace', 'path', 'file'], {
    history: 'push',
  })
  const [details, setDetails] = useState<WorkspaceDTO | null>(null)
  const addFolder =
    can('sessions:workspace:admin') &&
    FOLDERS_VIEW !== undefined &&
    navigable(FOLDERS_VIEW)

  const foldersQuery = useInfiniteQuery({
    queryKey: [...scope, 'workspaces', 'files-page', tenant],
    queryFn: ({ pageParam, signal }) =>
      agentOpsApi.listWorkspaces(
        { state: 'active', limit: FOLDER_PAGE, cursor: pageParam },
        { tenant, signal },
      ),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => (last.has_more ? last.cursor : undefined),
  })
  const all = useMemo(
    () => foldersQuery.data?.pages.flatMap((p) => p.items) ?? [],
    [foldersQuery.data],
  )
  const runsQuery = useQuery({
    queryKey: [...scope, 'runs', 'worktrees', RUN_PAGE, tenant],
    queryFn: ({ signal }) =>
      agentOpsApi.listRuns({ limit: RUN_PAGE }, { tenant, signal }),
    enabled: can('sessions:run:read'),
    staleTime: 30_000,
  })
  const worktrees = useMemo(
    () => worktreesByFolder(runsQuery.data?.items ?? [], all),
    [runsQuery.data, all],
  )
  // A worktree registered as a folder is listed under its repository, not beside it.
  const nested = new Set(
    [...worktrees.values()]
      .flat()
      .flatMap((w) => w.folder?.workspace_ref ?? []),
  )
  const folders = all.filter((f) => !nested.has(f.workspace_ref))

  const wanted = location.workspace
  const listed = all.find((f) => f.workspace_ref === wanted)
  const lookup = useQuery({
    queryKey: [...scope, 'workspace', wanted ?? '', tenant],
    queryFn: ({ signal }) =>
      agentOpsApi.getWorkspace(wanted as string, { tenant, signal }),
    enabled: !!wanted && !listed && foldersQuery.isSuccess,
    retry: false,
  })
  const current = wanted ? (listed ?? lookup.data) : folders[0]
  const repository =
    current &&
    (folders.find((f) =>
      (worktrees.get(f.workspace_ref) ?? []).some(
        (w) => w.folder?.workspace_ref === current.workspace_ref,
      ),
    ) ??
      current)
  const shownWorktrees = repository
    ? (worktrees.get(repository.workspace_ref) ?? [])
    : []

  const statuses = useQueries({
    queries: shownWorktrees.slice(0, STATUS_ROWS).map((w) => ({
      queryKey: agentOpsKeys.runGit(tenant, epoch, w.run),
      queryFn: ({ signal }: { signal: AbortSignal }) =>
        agentOpsApi.gitStatus(w.run, { signal }),
      retry: false,
      staleTime: 30_000,
    })),
  })
  const changesOf = (index: number) => statuses[index]?.data?.files.length
  // The folder shown is a session's worktree: its git status marks the files it changed.
  const shownStatus =
    statuses[
      shownWorktrees.findIndex(
        (w) => w.folder?.workspace_ref === current?.workspace_ref,
      )
    ]?.data
  const changes = useMemo(
    () =>
      new Map(
        (shownStatus?.files ?? []).map(
          (f) => [f.path.replace(/\/$/, ''), f.index + f.worktree] as const,
        ),
      ),
    [shownStatus],
  )

  const select = (ref: string) =>
    patchLocation({
      workspace: ref,
      path: undefined,
      file: undefined,
    })
  const showFolders = () =>
    patchLocation({
      workspace: undefined,
      path: undefined,
      file: undefined,
    })

  const addLink = addFolder ? (
    <Button asChild variant="primary" size="sm">
      <Link
        to={'/agentops' as never}
        search={{ [TAB_PARAM]: 'workspaces' } as never}
      >
        <FolderPlus className="size-3.5" aria-hidden />
        {t('files.addFolder')}
      </Link>
    </Button>
  ) : undefined

  const header = (
    <PageHeader
      title={t('files.title')}
      titleHidden
      actions={
        <Button variant="secondary" size="sm" onClick={onOverview}>
          {t('files.overview')}
        </Button>
      }
      primaryAction={addLink}
    />
  )

  if (foldersQuery.isPending)
    return (
      <>
        {header}
        <StateBlock state="loading" label={t('files.loading')} />
      </>
    )
  if (foldersQuery.isError)
    return (
      <>
        {header}
        <QueryErrorState
          error={foldersQuery.error}
          description={t('files.loadFailed')}
          retry={() => void foldersQuery.refetch()}
        />
      </>
    )
  if (all.length === 0 && !wanted)
    return (
      <>
        {header}
        <StateBlock
          state="empty"
          title={t('files.emptyTitle')}
          description={t('files.emptyDescription')}
          action={addLink}
        />
      </>
    )

  const refused =
    lookup.error instanceof ApiError &&
    (lookup.error.isForbidden || lookup.error.isNotFound)

  return (
    <>
      {header}
      <div
        data-testid="workspace-files"
        className="flex min-h-0 min-w-0 flex-1 flex-col overflow-x-hidden overflow-y-auto min-[1181px]:flex-row min-[1181px]:overflow-hidden"
      >
        <nav
          aria-label={t('files.folders')}
          className="flex max-h-64 shrink-0 flex-col overflow-y-auto border-b border-line p-2 min-[1181px]:max-h-none min-[1181px]:w-70 min-[1181px]:border-r min-[1181px]:border-b-0"
        >
          <div className="flex items-center justify-between px-2 py-1.5 text-caption font-semibold text-text">
            <span>{t('files.folders')}</span>
            <span className="font-normal text-text-3 tabular-nums">
              {folders.length}
            </span>
          </div>
          <ul className="flex flex-col gap-0.5">
            {folders.map((f) => {
              const open = f.workspace_ref === repository?.workspace_ref
              const count = worktrees.get(f.workspace_ref)?.length ?? 0
              const Icon = open ? FolderOpen : Folder
              return (
                <li key={f.workspace_ref}>
                  <button
                    type="button"
                    aria-current={
                      f.workspace_ref === current?.workspace_ref
                        ? 'true'
                        : undefined
                    }
                    onClick={() => select(f.workspace_ref)}
                    className={cn(
                      'flex w-full min-w-0 items-center gap-2 rounded-ctl px-2 py-1.5 text-start text-body text-text-2 hover:bg-muted max-md:min-h-11',
                      open && 'bg-muted font-medium text-text',
                    )}
                  >
                    <Icon className="size-4 shrink-0" aria-hidden />
                    <span className="min-w-0 flex-1 truncate">
                      {folderName(f)}
                    </span>
                    {count > 0 && (
                      <span
                        title={t('files.worktreeCount', { n: count })}
                        className="shrink-0 text-caption text-text-3 tabular-nums"
                      >
                        <span aria-hidden>{count}</span>
                        <span className="sr-only">
                          {t('files.worktreeCount', { n: count })}
                        </span>
                      </span>
                    )}
                  </button>
                  {open && shownWorktrees.length > 0 && (
                    <ul className="ms-4 mt-0.5 flex flex-col gap-0.5 border-s border-line ps-2">
                      {shownWorktrees.map((w, index) => (
                        <WorktreeRow
                          key={w.branch}
                          worktree={w}
                          changes={changesOf(index)}
                          current={
                            !!w.folder &&
                            w.folder.workspace_ref === current?.workspace_ref
                          }
                          onSelect={select}
                        />
                      ))}
                    </ul>
                  )}
                </li>
              )
            })}
          </ul>
          {foldersQuery.hasNextPage && (
            <Button
              variant="ghost"
              size="sm"
              className="mt-1 self-start"
              disabled={foldersQuery.isFetchingNextPage}
              onClick={() => void foldersQuery.fetchNextPage()}
            >
              {foldersQuery.isFetchingNextPage && <Spinner size="sm" />}
              {t('agentops:browser.loadMore')}
            </Button>
          )}
        </nav>

        <div className="flex min-w-0 flex-col min-[1181px]:min-h-0 min-[1181px]:flex-1">
          {current ? (
            <>
              <div className="flex min-w-0 items-center gap-2 border-b border-line px-4 py-2">
                <div className="min-w-0 flex-1">
                  <PathText value={current.root_path} copy={false} />
                </div>
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() => setDetails(current)}
                >
                  <Info className="size-3.5" aria-hidden />
                  {t('files.details')}
                </Button>
              </div>
              {shownStatus?.truncated && (
                <p className="border-b border-line px-4 py-1.5 text-caption text-text-2">
                  {t('files.gitCut')}
                </p>
              )}
              <WorkspaceBrowser
                workspace={current}
                layout="page"
                changes={changes}
              />
            </>
          ) : refused ? (
            <StateBlock
              state="no-access"
              title={t('files.noAccessTitle')}
              description={t('files.noAccessDescription')}
              action={
                <Button variant="secondary" size="sm" onClick={showFolders}>
                  {t('files.showFolders')}
                </Button>
              }
            />
          ) : lookup.isError ? (
            <QueryErrorState
              error={lookup.error}
              description={t('files.loadFailed')}
              retry={() => void lookup.refetch()}
            />
          ) : (
            <StateBlock state="loading" label={t('files.loading')} />
          )}
        </div>
      </div>
      <FolderDetailsSheet
        tenant={tenant}
        folder={details}
        onClose={() => setDetails(null)}
      />
    </>
  )
}

function WorktreeRow({
  worktree,
  changes,
  current,
  onSelect,
}: {
  worktree: Worktree
  changes: number | undefined
  current: boolean
  onSelect: (ref: string) => void
}) {
  const { t } = useTranslation('workspaceDashboard')
  const body = (
    <>
      <GitBranch className="size-3.5 shrink-0" aria-hidden />
      <PathText
        value={worktree.branch}
        copy={false}
        size="small"
        className="min-w-0 flex-1"
      />
      {changes !== undefined && <GitState changes={changes} />}
    </>
  )
  const row =
    'flex w-full min-w-0 items-center gap-2 rounded-ctl px-2 py-1 max-md:min-h-11'
  const registered = worktree.folder
  return (
    <li>
      {registered ? (
        <button
          type="button"
          aria-current={current ? 'true' : undefined}
          onClick={() => onSelect(registered.workspace_ref)}
          className={cn(
            row,
            'text-start text-text-2 hover:bg-muted',
            current && 'bg-muted text-text',
          )}
        >
          {body}
        </button>
      ) : (
        <div title={t('files.worktreeHint')} className={cn(row, 'text-text-3')}>
          {body}
        </div>
      )}
    </li>
  )
}

/** A dot never speaks alone: its word is there for a screen reader and on hover. */
function GitState({ changes }: { changes: number }) {
  const { t } = useTranslation('workspaceDashboard')
  const word =
    changes > 0 ? t('files.changes', { n: changes }) : t('files.clean')
  return (
    <span title={word} className="flex shrink-0 items-center">
      <span
        aria-hidden
        className={cn(
          'size-[7px] rounded-full',
          changes > 0 ? 'bg-warn' : 'bg-ok',
        )}
      />
      <span className="sr-only">{word}</span>
    </span>
  )
}
