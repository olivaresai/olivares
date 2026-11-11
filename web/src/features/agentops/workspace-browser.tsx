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
import {
  useEffect,
  useId,
  useMemo,
  useRef,
  useState,
  type KeyboardEvent,
} from 'react'
import { useTranslation } from 'react-i18next'
import { QueryErrorState } from '@/components/layout/query-error-state'
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
import { StateBlock } from '@/components/ui/state-block'
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
import { gitStatusWords } from './git-status-words'
import type { FileEntry, WorkspaceDTO } from './types'
import { FileViewer, NameTaken, pathTaken } from './workspace-file-viewer'
import { ArchiveButton, UploadButton } from './workspace-transfer'
import {
  MAX_UPLOAD_BYTES,
  uploadRefusal,
  useWorkspaceUpload,
} from './workspace-upload'
import './i18n'

const PAGE = 200
/** An edit in progress: the text, and the version of the file it started from. */
type Draft = { path: string; value: string; version?: string }
/** How many pages of one folder a deep link walks to reveal its file. */
const REVEAL_PAGES = 10

/**
 * The two places the browser lives. `pane` follows its own width (a container query): in the
 * session's side pane (280 to 640 px) the tree and the file stack, and the tree's bottom edge
 * is the browser's own splitter. `page` follows the window, because the columns of /workspace
 * stack by the layout's breakpoint and the stacked columns scroll together.
 */
const LAYOUTS = {
  pane: {
    root: 'flex min-h-0 flex-1 flex-col',
    grid: 'flex min-h-0 flex-1 flex-col gap-3 @min-[40rem]/files:grid @min-[40rem]/files:grid-cols-[minmax(16rem,22rem)_minmax(0,1fr)]',
    list: 'gap-2 rounded-md border border-border',
    listWithFile:
      '@max-[40rem]/files:h-[45%] @max-[40rem]/files:max-h-[85%] @max-[40rem]/files:min-h-32 @max-[40rem]/files:shrink-0 @max-[40rem]/files:resize-y',
    listAlone: '@max-[40rem]/files:min-h-0 @max-[40rem]/files:flex-1',
    tree: '@min-[40rem]/files:max-h-[28rem]',
    viewer: 'min-h-0 flex-1 rounded-md border border-border p-3',
    viewerIdle: '@max-[40rem]/files:order-first @max-[40rem]/files:flex-none',
    idleHint: '@max-[40rem]/files:hidden',
  },
  page: {
    root: 'flex flex-col min-[1181px]:min-h-0 min-[1181px]:flex-1',
    grid: 'flex flex-col min-[1181px]:grid min-[1181px]:min-h-0 min-[1181px]:flex-1 min-[1181px]:grid-cols-[minmax(16rem,20rem)_minmax(0,1fr)]',
    list: 'gap-2 border-b border-line pt-2 max-[1180px]:max-h-96 min-[1181px]:border-r min-[1181px]:border-b-0',
    listWithFile: '',
    listAlone: '',
    tree: '',
    viewer:
      'p-4 max-[1180px]:min-h-[28rem] min-[1181px]:min-h-0 min-[1181px]:flex-1',
    viewerIdle: '',
    idleHint: '',
  },
}

/** Every folder from the root down to `dir`, `dir` included. */
function foldersTo(dir: string): string[] {
  const parts = dir ? dir.split('/') : []
  return parts.map((_, i) => parts.slice(0, i + 1).join('/'))
}

const parentOf = (path: string) => path.split('/').slice(0, -1).join('/')

function useFilesQuery(wref: string, dir: string) {
  const { activeTenant } = useAuth()
  return useInfiniteQuery({
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
}

/**
 * WorkspaceBrowser — a governed file browser over one workspace: a tree that lists ONE level
 * at a time with keyset pagination (the API has no recursive read, so the client walks it),
 * read with DLP sensitivity labels, preview documents and images, and edit text in CodeMirror.
 * Every access is jailed server-side; HTML runs only in a fully sandboxed iframe.
 */
export function WorkspaceBrowser({
  workspace,
  layout = 'pane',
  changes,
}: {
  workspace: WorkspaceDTO
  layout?: keyof typeof LAYOUTS
  /** Git's two-letter code of each changed path, when the folder is a session's worktree. */
  changes?: ReadonlyMap<string, string>
}) {
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
      layout={layout}
      changes={changes}
      location={stale ? {} : location}
      onLocation={patchLocation}
    />
  )
}

function Browser({
  workspace,
  layout,
  changes,
  location,
  onLocation,
}: {
  workspace: WorkspaceDTO
  layout: keyof typeof LAYOUTS
  changes?: ReadonlyMap<string, string>
  location: UrlState
  onLocation: UrlStatePatch
}) {
  const { t } = useTranslation(['agentops', 'common'])
  const { activeTenant, can } = useAuth()
  const qc = useQueryClient()
  const wref = workspace.workspace_ref
  const writable =
    workspace.mount_mode === 'rw' && can('sessions:workspace:write')
  const shape = LAYOUTS[layout]

  const dir = location.path ?? '' // '' = root
  const selected = location.file ?? null
  const [draft, setDraft] = useState<Draft | null>(null)
  // A save the server refused for the file's version: the draft stays, Save waits.
  const [stale, setStale] =
    useState<Parameters<typeof FileViewer>[0]['stale']>(undefined)
  const editing = draft?.path === selected
  const editValue = editing ? draft.value : ''
  // Every open level, not only the folder shown: a file may be deleted or moved from any.
  const invalidateDir = () =>
    qc.invalidateQueries({
      queryKey: agentOpsKeys.files(activeTenant, wref, '').slice(0, -1),
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

  const filesQuery = useFilesQuery(wref, dir)
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

  // Open folders: what the person opened, plus every folder on the way to what the address
  // names. Closing one stays closed until the address names something inside it again.
  const [open, setOpen] = useState(
    () => new Set([...foldersTo(dir), ...foldersTo(parentOf(selected ?? ''))]),
  )
  const addressed = `${dir}\n${selected ?? ''}`
  const [revealed, setRevealed] = useState(addressed)
  if (revealed !== addressed) {
    setRevealed(addressed)
    setOpen(
      (prev) =>
        new Set([
          ...prev,
          ...foldersTo(dir),
          ...foldersTo(parentOf(selected ?? '')),
        ]),
    )
  }
  const setFolderOpen = (path: string, value: boolean) =>
    setOpen((prev) => {
      const next = new Set(prev)
      if (value) next.add(path)
      else next.delete(path)
      return next
    })

  const fileQuery = useQuery({
    queryKey: agentOpsKeys.file(activeTenant, wref, selected ?? ''),
    queryFn: () => agentOpsApi.readFile(wref, selected as string),
    enabled: !!selected,
  })
  const file = fileQuery.data

  const openEntry = (e: Pick<FileEntry, 'path' | 'type'>) => {
    if (e.type === 'dir') {
      const opening = !open.has(e.path)
      setFolderOpen(e.path, opening)
      if (opening) onLocation({ path: e.path })
      return
    }
    setDraft(null)
    onLocation({ file: e.path })
  }

  const showFolder = (path: string) => {
    setDraft(null)
    onLocation({ path: path || undefined, file: undefined })
  }

  const save = useMutation({
    mutationFn: (submitted: Draft) =>
      agentOpsApi.writeFile(wref, submitted.path, submitted.value, {
        version: submitted.version,
      }),
    onSuccess: (result, submitted) => {
      toast.success(t('browser.saved'))
      // Text typed while the save ran now starts from the version this write made.
      setDraft((current) =>
        current === submitted
          ? null
          : current?.path === submitted.path &&
              current.version === submitted.version
            ? { ...current, version: result.etag ?? current.version }
            : current,
      )
      // So does the next edit, before the read of the file lands again.
      if (result.etag)
        qc.setQueryData<typeof file>(
          agentOpsKeys.file(activeTenant, wref, submitted.path),
          (read) =>
            read && { ...read, content: submitted.value, etag: result.etag },
        )
      void qc.invalidateQueries({
        queryKey: agentOpsKeys.file(activeTenant, wref, submitted.path),
      })
      void invalidateDir()
    },
    onError: (err, submitted) => {
      if (err instanceof ApiError && err.status === 428)
        return setStale('reopen')
      if (!(err instanceof ApiError) || err.status !== 412)
        return errToast(t)(err)
      // Changed or deleted since it was read: the file system says which.
      void pathTaken(wref, submitted.path).then(
        (there) => setStale(there ? 'changed' : 'gone'),
        () => setStale('changed'),
      )
    },
  })
  const discardDraft = () => {
    setDraft(null)
    setStale(undefined)
    void fileQuery.refetch()
  }

  // The file's column never grows past the space it has, so a long line wraps or scrolls
  // inside the editor instead of pushing the pane sideways.
  return (
    <div className={cn('@container/files', shape.root)}>
      <div className={shape.grid}>
        {/* Left: toolbar + tree */}
        <div
          data-testid="files-list-pane"
          className={cn(
            'flex min-w-0 flex-col overflow-hidden',
            shape.list,
            selected ? shape.listWithFile : shape.listAlone,
          )}
        >
          <div className="flex flex-wrap items-center gap-1 px-2 pt-1.5">
            <Button
              variant="ghost"
              size="sm"
              onClick={() => void invalidateDir()}
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

          {/* Drop files anywhere on the tree to upload them into the folder shown. */}
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
            <FileTree
              wref={wref}
              changes={changes}
              className={shape.tree}
              selected={selected}
              dir={dir}
              open={open}
              onFolderOpen={setFolderOpen}
              onOpen={openEntry}
              checked={checked}
              onToggle={toggle}
            />
          </div>
        </div>

        {/* Right: breadcrumb + viewer / editor. Its toolbar keeps one row by its width (workspace-file-viewer). */}
        <div
          className={cn(
            '@container/viewer flex min-w-0 flex-col gap-2 overflow-auto',
            shape.viewer,
            !selected && shape.viewerIdle,
          )}
        >
          <Breadcrumb dir={dir} file={selected} onFolder={showFolder} />
          {!selected ? (
            <p
              className={cn(
                'p-6 text-center text-body text-muted-foreground',
                shape.idleHint,
              )}
            >
              {t('browser.pickFile')}
            </p>
          ) : fileQuery.isLoading ? (
            <StateBlock state="loading" />
          ) : fileQuery.error ? (
            <QueryErrorState
              error={fileQuery.error}
              description={errorText(fileQuery.error)}
              retry={() => void fileQuery.refetch()}
            />
          ) : file ? (
            <FileViewer
              key={selected}
              workspace={workspace}
              path={selected}
              file={file}
              writable={writable}
              editing={editing}
              editValue={editValue}
              onEdit={() => {
                setStale(undefined)
                setDraft({
                  path: selected,
                  value: file.content,
                  version: file.etag,
                })
              }}
              onCancel={() => {
                setDraft(null)
                setStale(undefined)
              }}
              onChange={(value) =>
                setDraft((current) =>
                  current ? { ...current, value } : current,
                )
              }
              onSave={() => {
                if (draft) save.mutate(draft)
              }}
              saving={save.isPending}
              stale={stale}
              onDiscard={discardDraft}
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

// --- Breadcrumb ------------------------------------------------------------------

function Breadcrumb({
  dir,
  file,
  onFolder,
}: {
  dir: string
  file: string | null
  onFolder: (path: string) => void
}) {
  const { t } = useTranslation('agentops')
  const folders = foldersTo(file ? parentOf(file) : dir)
  return (
    <nav
      aria-label={t('browser.title')}
      className="flex min-w-0 flex-wrap items-center gap-1 text-caption"
    >
      <button
        type="button"
        onClick={() => onFolder('')}
        className="inline-flex items-center gap-1 rounded px-1 py-0.5 text-muted-foreground hover:bg-muted hover:text-foreground"
      >
        <Home className="size-3" aria-hidden />
        {t('browser.root')}
      </button>
      {folders.map((path) => (
        <span key={path} className="inline-flex min-w-0 items-center gap-1">
          <ChevronRight
            className="size-3 shrink-0 text-muted-foreground"
            aria-hidden
          />
          <button
            type="button"
            onClick={() => onFolder(path)}
            className="min-w-0 truncate rounded px-1 py-0.5 font-mono text-foreground hover:bg-muted"
          >
            {path.split('/').pop()}
          </button>
        </span>
      ))}
      {file && (
        <span className="inline-flex min-w-0 items-center gap-1">
          <ChevronRight
            className="size-3 shrink-0 text-muted-foreground"
            aria-hidden
          />
          <span
            aria-current="page"
            className="min-w-0 truncate px-1 py-0.5 font-mono font-medium text-foreground"
          >
            {file.split('/').pop()}
          </span>
        </span>
      )}
    </nav>
  )
}

// --- File tree -------------------------------------------------------------------

interface TreeShared {
  wref: string
  changes?: ReadonlyMap<string, string>
  selected: string | null
  dir: string
  open: Set<string>
  active: string | null
  rowId: (path: string) => string
  onActive: (path: string) => void
  onOpen: (e: Pick<FileEntry, 'path' | 'type'>) => void
  checked: string[]
  onToggle: (path: string) => void
}

/**
 * An ARIA tree: one tab stop, `aria-activedescendant` names the row the arrow keys are on, and
 * the rows are the DOM order of what is open (a closed folder mounts no children). A checkbox
 * shows only on the rows of the folder shown, whose ticks the toolbar downloads; Space ticks.
 */
function FileTree({
  wref,
  changes,
  className,
  selected,
  dir,
  open,
  onFolderOpen,
  onOpen,
  checked,
  onToggle,
}: {
  wref: string
  changes?: ReadonlyMap<string, string>
  className: string
  selected: string | null
  dir: string
  open: Set<string>
  onFolderOpen: (path: string, value: boolean) => void
  onOpen: (e: Pick<FileEntry, 'path' | 'type'>) => void
  checked: string[]
  onToggle: (path: string) => void
}) {
  const { t } = useTranslation('agentops')
  const treeId = useId()
  const rootQuery = useFilesQuery(wref, '')
  const [active, setActive] = useState<string | null>(selected)
  const rowId = (path: string) => `${treeId}${encodeURIComponent(path)}`
  const shared: TreeShared = {
    wref,
    changes,
    selected,
    dir,
    open,
    active,
    rowId,
    onActive: setActive,
    onOpen,
    checked,
    onToggle,
  }

  if (rootQuery.isLoading) return <StateBlock state="loading" />
  if (rootQuery.error)
    return (
      <QueryErrorState
        error={rootQuery.error}
        description={errorText(rootQuery.error)}
        retry={() => void rootQuery.refetch()}
      />
    )
  const entries = rootQuery.data?.pages.flatMap((p) => p.entries) ?? []
  if (entries.length === 0)
    return (
      <p className="p-6 text-center text-body text-muted-foreground">
        {t('browser.empty')}
      </p>
    )

  const onKeyDown = (e: KeyboardEvent<HTMLUListElement>) => {
    const rows = Array.from(
      e.currentTarget.querySelectorAll<HTMLElement>('[role="treeitem"]'),
    )
    const at = rows.findIndex((row) => row.dataset.path === active)
    const row = rows[at]
    const go = (target: HTMLElement | undefined) => {
      if (!target) return
      setActive(target.dataset.path ?? null)
      target.scrollIntoView({ block: 'nearest' })
    }
    const path = row?.dataset.path ?? ''
    const isFolder = row?.dataset.type === 'dir'
    const isOpen = isFolder && open.has(path)
    switch (e.key) {
      case 'ArrowDown':
        go(rows[at + 1])
        break
      case 'ArrowUp':
        go(rows[at - 1])
        break
      case 'Home':
        go(rows[0])
        break
      case 'End':
        go(rows[rows.length - 1])
        break
      case 'ArrowRight':
        if (isFolder && !isOpen) onFolderOpen(path, true)
        else if (isOpen && rows[at + 1]?.dataset.path?.startsWith(`${path}/`))
          go(rows[at + 1])
        break
      case 'ArrowLeft':
        if (isOpen) onFolderOpen(path, false)
        else if (row) go(rows.find((r) => r.dataset.path === parentOf(path)))
        break
      case 'Enter':
        if (row) onOpen({ path, type: row.dataset.type ?? 'file' })
        break
      case ' ':
        if (row?.dataset.tickable) onToggle(path)
        break
      default:
        return
    }
    e.preventDefault()
  }

  return (
    <ul
      role="tree"
      tabIndex={0}
      aria-label={t('browser.title')}
      aria-activedescendant={active ? rowId(active) : undefined}
      onKeyDown={onKeyDown}
      onFocus={(e) => {
        if (active || e.target !== e.currentTarget) return
        const first =
          e.currentTarget.querySelector<HTMLElement>('[role="treeitem"]')
        setActive(selected ?? first?.dataset.path ?? null)
      }}
      className={cn(
        'group/tree min-h-0 flex-1 overflow-auto pb-2 outline-none',
        className,
      )}
    >
      <TreeRows entries={entries} level={1} hasMore={false} shared={shared} />
      <LevelFooter dir="" query={rootQuery} entries={entries} shared={shared} />
    </ul>
  )
}

type FilesQuery = ReturnType<typeof useFilesQuery>

/** Load more, and the walk that reveals a file the address names in a later page. */
function LevelFooter({
  dir,
  query,
  entries,
  shared,
}: {
  dir: string
  query: FilesQuery
  entries: FileEntry[]
  shared: TreeShared
}) {
  const { t } = useTranslation('agentops')
  const { hasNextPage, isFetchingNextPage, fetchNextPage, data } = query
  const prefix = dir ? `${dir}/` : ''
  const unseen = [shared.selected, shared.dir].some(
    (target) =>
      !!target &&
      target.startsWith(prefix) &&
      !entries.some(
        (e) => target === e.path || target.startsWith(`${e.path}/`),
      ),
  )
  const pages = data?.pages.length ?? 0
  // shortcut: a target past REVEAL_PAGES stays open in the preview but off the tree until
  // Load more reaches it; raise the limit if real folders run past 2,000 entries.
  const walk = unseen && !!hasNextPage && pages < REVEAL_PAGES
  useEffect(() => {
    if (walk && !isFetchingNextPage) void fetchNextPage()
  }, [walk, isFetchingNextPage, fetchNextPage])
  if (!hasNextPage) return null
  return (
    <li role="none" className="px-2 pt-1">
      <Button
        variant="ghost"
        size="sm"
        onClick={() => void fetchNextPage()}
        disabled={isFetchingNextPage}
      >
        {isFetchingNextPage && <Spinner size="sm" />}
        {t('browser.loadMore')}
      </Button>
    </li>
  )
}

/** One open folder's level, listed on demand when the folder opens. */
function ChildLevel({
  dir,
  level,
  shared,
}: {
  dir: string
  level: number
  shared: TreeShared
}) {
  const { t } = useTranslation(['agentops', 'common'])
  const query = useFilesQuery(shared.wref, dir)
  const entries = query.data?.pages.flatMap((p) => p.entries) ?? []
  const inset = { paddingInlineStart: `${level * 16}px` }
  if (query.isLoading)
    return (
      <li role="none" style={inset} className="py-1">
        <Spinner className="size-3.5" />
      </li>
    )
  if (query.error)
    return (
      <li role="none" style={inset} className="py-1">
        <QueryErrorState
          error={query.error}
          description={errorText(query.error)}
          retry={() => void query.refetch()}
        />
      </li>
    )
  if (entries.length === 0)
    return (
      <li
        role="none"
        style={inset}
        className="py-1 text-caption text-muted-foreground"
      >
        {t('browser.empty')}
      </li>
    )
  return (
    <>
      <TreeRows
        entries={entries}
        level={level}
        hasMore={!!query.hasNextPage}
        shared={shared}
      />
      <LevelFooter dir={dir} query={query} entries={entries} shared={shared} />
    </>
  )
}

function TreeRows({
  entries,
  level,
  hasMore,
  shared,
}: {
  entries: FileEntry[]
  level: number
  hasMore: boolean
  shared: TreeShared
}) {
  return (
    <>
      {entries.map((entry, index) => (
        <TreeRow
          key={entry.path}
          entry={entry}
          level={level}
          position={index + 1}
          // -1 is ARIA's "not known": more of this level is on the server.
          size={hasMore ? -1 : entries.length}
          shared={shared}
        />
      ))}
    </>
  )
}

function TreeRow({
  entry,
  level,
  position,
  size,
  shared,
}: {
  entry: FileEntry
  level: number
  position: number
  size: number
  shared: TreeShared
}) {
  const { t } = useTranslation('agentops')
  const { selected, dir, open, active, rowId, onActive, onOpen } = shared
  const isFolder = entry.type === 'dir'
  const isOpen = isFolder && open.has(entry.path)
  const isSelected = selected === entry.path
  const tickable = parentOf(entry.path) === dir
  const rowRef = useRef<HTMLDivElement>(null)
  useEffect(() => {
    if (isSelected) rowRef.current?.scrollIntoView({ block: 'nearest' })
  }, [isSelected])
  return (
    <li
      role="treeitem"
      id={rowId(entry.path)}
      aria-label={entry.name}
      aria-level={level}
      aria-posinset={position}
      aria-setsize={size}
      aria-selected={isSelected}
      aria-expanded={isFolder ? isOpen : undefined}
      data-path={entry.path}
      data-type={entry.type}
      data-tickable={tickable || undefined}
    >
      <div
        ref={rowRef}
        data-active={active === entry.path || undefined}
        style={{ paddingInlineStart: `${(level - 1) * 16 + 8}px` }}
        onClick={() => {
          onActive(entry.path)
          onOpen(entry)
        }}
        className={cn(
          'group/row flex cursor-pointer items-center gap-1.5 py-1 pr-2 hover:bg-muted max-md:min-h-11',
          'group-focus-visible/tree:data-[active]:outline-2 group-focus-visible/tree:data-[active]:-outline-offset-2 group-focus-visible/tree:data-[active]:outline-focus',
          isSelected && 'bg-muted font-medium',
        )}
      >
        {tickable ? (
          <span
            className="flex shrink-0 opacity-0 group-hover/row:opacity-100 has-[[data-state=checked]]:opacity-100 pointer-coarse:opacity-100"
            onClick={(e) => e.stopPropagation()}
          >
            <Checkbox
              tabIndex={-1}
              checked={shared.checked.includes(entry.path)}
              onCheckedChange={() => shared.onToggle(entry.path)}
              aria-label={t('browser.select', { name: entry.name })}
            />
          </span>
        ) : null}
        {isFolder ? (
          <ChevronRight
            aria-hidden
            className={cn(
              'size-3.5 shrink-0 text-muted-foreground',
              isOpen && 'rotate-90',
            )}
          />
        ) : (
          <span aria-hidden className="size-3.5 shrink-0" />
        )}
        {isFolder ? (
          <Folder className="size-4 shrink-0 text-accent-text" aria-hidden />
        ) : (
          <FileTypeIcon
            path={entry.name}
            className="size-4 shrink-0 text-muted-foreground"
          />
        )}
        <span className="truncate font-mono text-caption text-foreground">
          {entry.name}
        </span>
        {entry.is_symlink && (
          <span className="ms-auto shrink-0 text-caption text-muted-foreground">
            {t('browser.symlink')}
          </span>
        )}
        <GitLetter code={shared.changes?.get(entry.path)} />
      </div>
      {isOpen && (
        <ul role="group">
          <ChildLevel dir={entry.path} level={level + 1} shared={shared} />
        </ul>
      )}
    </li>
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
  const [taken, setTaken] = useState<string | null>(null)
  const field = useRef<HTMLInputElement>(null)

  const join = (n: string) => (dir ? `${dir}/${n}` : n)
  const create = useMutation({
    mutationFn: async () => {
      const target = join(name.trim())
      // The check says it in words; the write is create-only, so a name another writer
      // made meanwhile is refused by the engine too (onError).
      if (await pathTaken(wref, target)) return name.trim()
      if (mode === 'folder') await agentOpsApi.mkdir(wref, target)
      else await agentOpsApi.writeFile(wref, target, '', { create: true })
      return null
    },
    onSuccess: (existing) => (existing ? refuse(existing) : created()),
    onError: (err) => {
      // The name appeared after the check: the engine's create-only write refused it.
      if (err instanceof ApiError && err.status === 409) refuse(name.trim())
      else errToast(t)(err)
    },
  })
  function refuse(existing: string) {
    setTaken(existing)
    field.current?.focus()
  }
  function created() {
    toast.success(t('browser.created'))
    setMode(null)
    setName('')
    onDone()
  }

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
              ref={field}
              id="new-name"
              aria-invalid={taken ? true : undefined}
              aria-describedby={taken ? 'new-name-taken' : undefined}
              value={name}
              onChange={(e) => {
                setName(e.target.value)
                setTaken(null)
              }}
              mono
              autoFocus
            />
            {taken && <NameTaken id="new-name-taken" name={taken} />}
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

/** A changed file's two-letter git code, marked by its letters and named by its words. */
function GitLetter({ code }: { code?: string }) {
  const { t } = useTranslation('agentops')
  if (!code) return null
  const words = gitStatusWords(code, t)
  return (
    <span
      role="img"
      aria-label={words}
      title={words}
      className="ms-auto shrink-0 font-mono text-caption text-text-2"
    >
      {code.trim()}
    </span>
  )
}

const errorText = (err: unknown) =>
  err instanceof ApiError ? err.message : String(err)

function errToast(t: (k: string) => string) {
  return (err: unknown) => {
    // A write the engine asks a precondition for (428) is the console's own gap: its remedy.
    if (err instanceof ApiError && err.status === 428)
      return void toast.warning(t('browser.reopenToSave'))
    toast.error(err instanceof ApiError ? err.message : t('browser.title'))
  }
}

export { SensitivityBadges } from './workspace-file-viewer'
