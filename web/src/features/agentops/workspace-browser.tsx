// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import {
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
} from '@tanstack/react-query'
import {
  ChevronRight,
  FilePlus,
  Folder,
  FolderPlus,
  Home,
  RefreshCw,
} from 'lucide-react'
import { FileTypeIcon } from '@/components/ui/file-type-icon'
import { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Spinner } from '@/components/ui/spinner'
import { toast } from '@/components/ui/toaster'
import { useAuth } from '@/lib/auth/context'
import { ApiError } from '@/lib/api/errors'
import {
  useUrlState,
  type UrlState,
  type UrlStatePatch,
} from '@/lib/hooks/use-url-state'
import { cn } from '@/lib/utils'
import { agentOpsApi, agentOpsKeys } from './api'
import type { FileEntry, WorkspaceDTO } from './types'
import { FileViewer } from './workspace-file-viewer'
import { ArchiveButton, UploadButton } from './workspace-transfer'
import {
  MAX_UPLOAD_BYTES,
  uploadRefusal,
  useWorkspaceUpload,
} from './workspace-upload'
import './i18n'

const PAGE = 200

/**
 * WorkspaceBrowser — a governed Finder over one workspace: navigate dir-by-dir (the
 * API lists ONE level + keyset pagination, so the client walks the tree), read with
 * DLP sensitivity labels, preview documents and images, and edit text in CodeMirror.
 * Every access is jailed server-side; HTML runs only in a fully sandboxed iframe.
 */
export function WorkspaceBrowser({ workspace }: { workspace: WorkspaceDTO }) {
  const { activeTenant } = useAuth()
  const scope = `${activeTenant}:${workspace.workspace_ref}:${workspace.root_path}`
  const [location, patchLocation] = useUrlState(['workspace', 'path', 'file'], {
    history: 'push',
  })
  // The scope the URL's folder and file belong to. A link opened on this scope keeps
  // them; after the scope changes they are the old scope's, so they are dropped from
  // the URL and hidden until that lands.
  const [ownedScope, setOwnedScope] = useState(scope)
  const locationMatches =
    location.workspace === undefined ||
    location.workspace === workspace.workspace_ref
  if (
    ownedScope !== scope &&
    location.path === undefined &&
    location.file === undefined
  ) {
    setOwnedScope(scope)
  }
  const stale = ownedScope !== scope || !locationMatches
  useEffect(() => {
    if (!stale) return
    patchLocation(
      {
        ...(locationMatches ? {} : { workspace: undefined }),
        path: undefined,
        file: undefined,
      },
      { history: 'replace' },
    )
  }, [stale, locationMatches, patchLocation])
  return (
    <Browser
      key={scope}
      workspace={workspace}
      location={stale ? {} : location}
      onLocation={patchLocation}
    />
  )
}

function Browser({
  workspace,
  location,
  onLocation,
}: {
  workspace: WorkspaceDTO
  location: UrlState
  onLocation: UrlStatePatch
}) {
  const { t } = useTranslation(['agentops', 'common'])
  const { activeTenant, can } = useAuth()
  const qc = useQueryClient()
  const wref = workspace.workspace_ref
  const writable =
    workspace.mount_mode === 'rw' && can('sessions:workspace:write')

  const dir = location.path ?? '' // '' = root
  const selected = location.file ?? null
  const [draft, setDraft] = useState<{ path: string; value: string } | null>(
    null,
  )
  const editing = draft?.path === selected
  const editValue = editing ? draft.value : ''
  const invalidateDir = () =>
    qc.invalidateQueries({
      queryKey: agentOpsKeys.files(activeTenant, wref, dir),
    })

  const refusal = uploadRefusal(
    workspace.mount_mode,
    can('sessions:workspace:write'),
    (key) => t(key),
  )
  // The engine refuses a write past 64 MiB whatever limit the folder registered.
  const uploadLimit = Math.min(
    workspace.max_read_bytes || MAX_UPLOAD_BYTES,
    MAX_UPLOAD_BYTES,
  )
  const { upload, pending: uploading } = useWorkspaceUpload({
    wref,
    dir,
    maxBytes: uploadLimit,
    refusal,
    onDone: invalidateDir,
  })
  const [dropping, setDropping] = useState(false)

  const filesQuery = useInfiniteQuery({
    queryKey: agentOpsKeys.files(activeTenant, wref, dir),
    queryFn: ({ pageParam }) =>
      agentOpsApi.listFiles(wref, {
        path: dir || undefined,
        limit: PAGE,
        cursor: pageParam,
      }),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => (last.has_more ? last.cursor : undefined),
  })
  const entries = useMemo(
    () => filesQuery.data?.pages.flatMap((p) => p.entries) ?? [],
    [filesQuery.data],
  )

  // The ticked entries belong to the folder they were ticked in: leaving it drops them (also
  // when the same folder is opened again), and only an entry the list still has counts.
  const [ticked, setTicked] = useState<{ dir: string; paths: string[] }>({
    dir,
    paths: [],
  })
  if (ticked.dir !== dir) setTicked({ dir, paths: [] })
  const checked = ticked.paths.filter((p) => entries.some((e) => e.path === p))
  const toggle = (path: string) =>
    setTicked({
      dir,
      paths: checked.includes(path)
        ? checked.filter((p) => p !== path)
        : [...checked, path],
    })

  const fileQuery = useQuery({
    queryKey: agentOpsKeys.file(activeTenant, wref, selected ?? ''),
    queryFn: () => agentOpsApi.readFile(wref, selected as string),
    enabled: !!selected,
  })
  const file = fileQuery.data

  const openEntry = (e: FileEntry) => {
    setDraft(null)
    if (e.type === 'dir') onLocation({ path: e.path, file: undefined })
    else onLocation({ file: e.path })
  }

  const crumbs = dir ? dir.split('/') : []

  const save = useMutation({
    mutationFn: (submitted: { path: string; value: string }) =>
      agentOpsApi.writeFile(wref, submitted.path, submitted.value),
    onSuccess: (_result, submitted) => {
      toast.success(t('browser.saved'))
      setDraft((current) => (current === submitted ? null : current))
      void qc.invalidateQueries({
        queryKey: agentOpsKeys.file(activeTenant, wref, submitted.path),
      })
      void invalidateDir()
    },
    onError: errToast(t),
  })

  // TWO COLUMNS BY ITS OWN WIDTH, NOT THE WINDOW'S (a container query): in the session's
  // side pane (280 to 640 px) the list and the file stack in one column; on the Workspaces
  // page they stand side by side. The file's column never grows past the space it has, so
  // a long line wraps or scrolls inside the editor instead of pushing the pane sideways.
  return (
    // STACKED (the side pane), the two fill the pane's height: the list on top, the file
    // under it, and the list's bottom edge is the browser's own splitter (resize-y).
    <div className="@container/files flex min-h-0 flex-1 flex-col">
      <div className="flex min-h-0 flex-1 flex-col gap-3 @min-[40rem]/files:grid @min-[40rem]/files:grid-cols-[minmax(16rem,22rem)_minmax(0,1fr)]">
        {/* Left: breadcrumb + toolbar + list */}
        <div
          data-testid="files-list-pane"
          className={cn(
            'flex min-w-0 flex-col gap-2 overflow-hidden rounded-md border border-border',
            selected
              ? '@max-[40rem]/files:h-[45%] @max-[40rem]/files:max-h-[85%] @max-[40rem]/files:min-h-32 @max-[40rem]/files:shrink-0 @max-[40rem]/files:resize-y'
              : '@max-[40rem]/files:min-h-0 @max-[40rem]/files:flex-1',
          )}
        >
          <div className="flex flex-wrap items-center gap-1 border-b border-border px-2 py-1.5 text-caption">
            <button
              type="button"
              onClick={() => onLocation({ path: undefined, file: undefined })}
              className="inline-flex items-center gap-1 rounded px-1 py-0.5 text-muted-foreground hover:bg-muted hover:text-foreground"
            >
              <Home className="size-3" />
              {t('browser.root')}
            </button>
            {crumbs.map((c, i) => {
              const target = crumbs.slice(0, i + 1).join('/')
              return (
                <span key={target} className="inline-flex items-center gap-1">
                  <ChevronRight className="size-3 text-muted-foreground" />
                  <button
                    type="button"
                    onClick={() =>
                      onLocation({ path: target, file: undefined })
                    }
                    className="rounded px-1 py-0.5 font-mono text-foreground hover:bg-muted"
                  >
                    {c}
                  </button>
                </span>
              )
            })}
          </div>

          <div className="flex flex-wrap items-center gap-1 px-2">
            <Button
              variant="ghost"
              size="sm"
              onClick={() => void filesQuery.refetch()}
            >
              <RefreshCw
                className={cn(
                  'size-3.5',
                  filesQuery.isFetching && 'animate-spin',
                )}
              />
              {t('browser.refresh')}
            </Button>
            {writable && (
              <BrowserCreateActions
                wref={wref}
                dir={dir}
                onDone={invalidateDir}
              />
            )}
            {(checked.length > 0 || dir !== '') && (
              <ArchiveButton
                wref={wref}
                paths={checked.length > 0 ? checked : [dir]}
                label={
                  checked.length > 0
                    ? t('browser.downloadSelected', { n: checked.length })
                    : t('browser.downloadFolder')
                }
              />
            )}
            <UploadButton
              refusal={refusal}
              maxBytes={uploadLimit}
              pending={uploading}
              onFiles={upload}
            />
          </div>

          {/* Drop files anywhere on the list to upload them into the folder shown. */}
          <div
            onDragOver={(e) => {
              e.preventDefault()
              setDropping(true)
            }}
            onDragLeave={() => setDropping(false)}
            onDrop={(e) => {
              e.preventDefault()
              setDropping(false)
              upload(Array.from(e.dataTransfer.files))
            }}
            className={cn(
              'flex min-h-0 flex-1 flex-col rounded-b-md',
              dropping && 'outline-2 -outline-offset-2 outline-accent-text',
            )}
          >
            <FileList
              entries={entries}
              loading={filesQuery.isLoading}
              error={filesQuery.error}
              onRetry={() => void filesQuery.refetch()}
              selected={selected}
              onOpen={openEntry}
              checked={checked}
              onToggle={toggle}
              hasMore={!!filesQuery.hasNextPage}
              onLoadMore={() => void filesQuery.fetchNextPage()}
              loadingMore={filesQuery.isFetchingNextPage}
            />
          </div>
        </div>

        {/* Right: viewer / editor. Its toolbar keeps one row by its width (workspace-file-viewer). */}
        <div
          className={cn(
            '@container/viewer flex min-h-0 min-w-0 flex-1 flex-col gap-2 overflow-auto rounded-md border border-border p-3',
            // Stacked, an empty viewer gives its height to the list.
            !selected && '@max-[40rem]/files:hidden',
          )}
        >
          {!selected ? (
            <p className="p-6 text-center text-body text-muted-foreground">
              {t('browser.pickFile')}
            </p>
          ) : fileQuery.isLoading ? (
            <div className="flex items-center justify-center p-8">
              <Spinner />
            </div>
          ) : fileQuery.error ? (
            <div role="alert" className="p-4">
              <p className="text-body text-danger">
                {fileQuery.error instanceof ApiError
                  ? fileQuery.error.message
                  : String(fileQuery.error)}
              </p>
              <Button
                variant="secondary"
                size="sm"
                className="mt-2"
                onClick={() => void fileQuery.refetch()}
              >
                {t('common:actions.retry')}
              </Button>
            </div>
          ) : file ? (
            <FileViewer
              key={selected}
              workspace={workspace}
              path={selected}
              file={file}
              writable={writable}
              editing={editing}
              editValue={editValue}
              onEdit={() => setDraft({ path: selected, value: file.content })}
              onCancel={() => setDraft(null)}
              onChange={(value) =>
                setDraft((current) =>
                  current ? { ...current, value } : current,
                )
              }
              onSave={() => {
                if (draft) save.mutate(draft)
              }}
              saving={save.isPending}
              onDeleted={() => {
                onLocation({ file: undefined })
                void invalidateDir()
              }}
              onMoved={(to) => {
                onLocation({ file: to })
                void invalidateDir()
              }}
            />
          ) : null}
        </div>
      </div>
    </div>
  )
}

// --- File list -------------------------------------------------------------------

function FileList({
  entries,
  loading,
  error,
  onRetry,
  selected,
  onOpen,
  checked,
  onToggle,
  hasMore,
  onLoadMore,
  loadingMore,
}: {
  entries: FileEntry[]
  loading: boolean
  error: unknown
  onRetry: () => void
  selected: string | null
  onOpen: (e: FileEntry) => void
  checked: string[]
  onToggle: (path: string) => void
  hasMore: boolean
  onLoadMore: () => void
  loadingMore: boolean
}) {
  const { t } = useTranslation('agentops')
  if (loading)
    return (
      <div className="flex items-center justify-center p-6">
        <Spinner />
      </div>
    )
  if (error)
    return (
      <div role="alert" className="p-4">
        <p className="text-body text-danger">
          {error instanceof ApiError ? error.message : String(error)}
        </p>
        <Button
          variant="secondary"
          size="sm"
          onClick={onRetry}
          className="mt-2"
        >
          {t('refresh')}
        </Button>
      </div>
    )
  if (entries.length === 0)
    return (
      <p className="p-6 text-center text-body text-muted-foreground">
        {t('browser.empty')}
      </p>
    )
  return (
    <ul className="min-h-0 flex-1 overflow-auto pb-2 @min-[40rem]/files:max-h-[28rem]">
      {entries.map((e) => (
        <li
          key={e.path}
          className={cn(
            'flex items-center gap-2 pl-2 hover:bg-muted',
            selected === e.path && 'bg-muted',
          )}
        >
          <Checkbox
            checked={checked.includes(e.path)}
            onCheckedChange={() => onToggle(e.path)}
            aria-label={t('browser.select', { name: e.name })}
          />
          <button
            type="button"
            onClick={() => onOpen(e)}
            className="flex min-w-0 flex-1 items-center gap-2 py-1.5 pr-2 text-left text-body"
          >
            {e.type === 'dir' ? (
              <Folder className="size-4 shrink-0 text-accent-text" />
            ) : (
              <FileTypeIcon
                path={e.name}
                className="size-4 shrink-0 text-muted-foreground"
              />
            )}
            <span className="truncate font-mono text-caption text-foreground">
              {e.name}
            </span>
            {e.is_symlink && (
              <span className="ml-auto text-[10px] uppercase text-muted-foreground">
                link
              </span>
            )}
          </button>
        </li>
      ))}
      {hasMore && (
        <li className="px-2 pt-1">
          <Button
            variant="ghost"
            size="sm"
            onClick={onLoadMore}
            disabled={loadingMore}
          >
            {loadingMore && <Spinner className="size-3.5" />}
            {t('browser.loadMore')}
          </Button>
        </li>
      )}
    </ul>
  )
}

// --- Create actions (new folder / new file) --------------------------------------

function BrowserCreateActions({
  wref,
  dir,
  onDone,
}: {
  wref: string
  dir: string
  onDone: () => void
}) {
  const { t } = useTranslation('agentops')
  const [mode, setMode] = useState<'folder' | 'file' | null>(null)
  const [name, setName] = useState('')

  const join = (n: string) => (dir ? `${dir}/${n}` : n)
  const create = useMutation({
    mutationFn: async () => {
      const target = join(name.trim())
      if (mode === 'folder') await agentOpsApi.mkdir(wref, target)
      else await agentOpsApi.writeFile(wref, target, '')
    },
    onSuccess: () => {
      toast.success(t('browser.created'))
      setMode(null)
      setName('')
      onDone()
    },
    onError: errToast(t),
  })

  return (
    <>
      <Button variant="ghost" size="sm" onClick={() => setMode('folder')}>
        <FolderPlus className="size-3.5" />
        {t('browser.newFolder')}
      </Button>
      <Button variant="ghost" size="sm" onClick={() => setMode('file')}>
        <FilePlus className="size-3.5" />
        {t('browser.newFile')}
      </Button>
      <Dialog open={mode !== null} onOpenChange={(o) => !o && setMode(null)}>
        <DialogContent className="max-w-md">
          <DialogHeader>
            <DialogTitle>
              {mode === 'folder'
                ? t('browser.newFolderTitle')
                : t('browser.newFileTitle')}
            </DialogTitle>
          </DialogHeader>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="new-name">
              {mode === 'folder'
                ? t('browser.newFolderLabel')
                : t('browser.newFileLabel')}
            </Label>
            <Input
              id="new-name"
              value={name}
              onChange={(e) => setName(e.target.value)}
              mono
              autoFocus
            />
          </div>
          <DialogFooter>
            <Button
              variant="secondary"
              onClick={() => setMode(null)}
              disabled={create.isPending}
            >
              {t('browser.cancel')}
            </Button>
            <Button
              variant="primary"
              onClick={() => create.mutate()}
              disabled={create.isPending || !name.trim()}
            >
              {create.isPending && <Spinner className="size-3.5" />}
              {t('browser.create')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  )
}

function errToast(t: (k: string) => string) {
  return (err: unknown) =>
    toast.error(err instanceof ApiError ? err.message : t('browser.title'))
}

export { SensitivityBadges } from './workspace-file-viewer'
