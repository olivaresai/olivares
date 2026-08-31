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
  Download,
  File as FileIcon,
  FilePlus,
  Folder,
  FolderPlus,
  Home,
  Lock,
  Pencil,
  RefreshCw,
  Save,
  Trash2,
} from 'lucide-react'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { CodeEditor } from '@/components/ui/code-editor'
import type { CodeLanguage } from '@/components/ui/code-languages'
import { Button } from '@/components/ui/button'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Spinner } from '@/components/ui/spinner'
import { Switch } from '@/components/ui/switch'
import { toast } from '@/components/ui/toaster'
import { useAuth } from '@/lib/auth/context'
import { ApiError } from '@/lib/api/errors'
import { cn } from '@/lib/utils'
import { agentOpsApi, agentOpsKeys } from './api'
import type { FileEntry, SensitivityHit, WorkspaceDTO } from './types'
import './i18n'

const PAGE = 200

/**
 * WorkspaceBrowser — a governed Finder over one workspace: navigate dir-by-dir (the
 * API lists ONE level + keyset pagination, so the client walks the tree), read with
 * DLP sensitivity labels, and — when the workspace mounts read-write — edit via
 * CodeMirror, create, move/rename and delete. Every access is jailed server-side; the
 * UI never renders content through an HTML sink (the editor is text-only).
 */
export function WorkspaceBrowser({ workspace }: { workspace: WorkspaceDTO }) {
  const { t } = useTranslation('agentops')
  const { activeTenant, can } = useAuth()
  const qc = useQueryClient()
  const wref = workspace.workspace_ref
  const writable =
    workspace.mount_mode === 'rw' && can('sessions:workspace:write')

  const [dir, setDir] = useState('') // '' = root
  const [selected, setSelected] = useState<string | null>(null)
  const [editing, setEditing] = useState(false)
  const [editValue, setEditValue] = useState('')

  const invalidateDir = () =>
    qc.invalidateQueries({
      queryKey: agentOpsKeys.files(activeTenant, wref, dir),
    })

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

  const fileQuery = useQuery({
    queryKey: agentOpsKeys.file(activeTenant, wref, selected ?? ''),
    queryFn: () => agentOpsApi.readFile(wref, selected as string),
    enabled: !!selected,
  })
  const file = fileQuery.data

  const openEntry = (e: FileEntry) => {
    if (e.type === 'dir') {
      setDir(e.path)
      setSelected(null)
      setEditing(false)
    } else {
      setSelected(e.path)
      setEditing(false)
    }
  }

  const crumbs = dir ? dir.split('/') : []

  const save = useMutation({
    mutationFn: () =>
      agentOpsApi.writeFile(wref, selected as string, editValue),
    onSuccess: () => {
      toast.success(t('browser.saved'))
      setEditing(false)
      void qc.invalidateQueries({
        queryKey: agentOpsKeys.file(activeTenant, wref, selected ?? ''),
      })
      void invalidateDir()
    },
    onError: errToast(t),
  })

  return (
    <div className="grid grid-cols-1 gap-3 lg:grid-cols-[minmax(16rem,22rem)_1fr]">
      {/* Left: breadcrumb + toolbar + list */}
      <div className="flex min-w-0 flex-col gap-2 rounded-md border border-border">
        <div className="flex flex-wrap items-center gap-1 border-b border-border px-2 py-1.5 text-xs">
          <button
            type="button"
            onClick={() => {
              setDir('')
              setSelected(null)
            }}
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
                  onClick={() => {
                    setDir(target)
                    setSelected(null)
                  }}
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
        </div>

        <FileList
          entries={entries}
          loading={filesQuery.isLoading}
          error={filesQuery.error}
          onRetry={() => void filesQuery.refetch()}
          selected={selected}
          onOpen={openEntry}
          hasMore={!!filesQuery.hasNextPage}
          onLoadMore={() => void filesQuery.fetchNextPage()}
          loadingMore={filesQuery.isFetchingNextPage}
        />
      </div>

      {/* Right: viewer / editor */}
      <div className="flex min-w-0 flex-col gap-2 rounded-md border border-border p-3">
        {!selected ? (
          <p className="p-6 text-center text-sm text-muted-foreground">
            {t('browser.pickFile')}
          </p>
        ) : fileQuery.isLoading ? (
          <div className="flex items-center justify-center p-8">
            <Spinner />
          </div>
        ) : fileQuery.error ? (
          <p role="alert" className="p-4 text-sm text-danger">
            {fileQuery.error instanceof ApiError
              ? fileQuery.error.message
              : String(fileQuery.error)}
          </p>
        ) : file ? (
          <FileViewer
            workspace={workspace}
            path={selected}
            file={file}
            writable={writable}
            editing={editing}
            editValue={editValue}
            onEdit={() => {
              setEditValue(file.content)
              setEditing(true)
            }}
            onCancel={() => setEditing(false)}
            onChange={setEditValue}
            onSave={() => save.mutate()}
            saving={save.isPending}
            onDeleted={() => {
              setSelected(null)
              void invalidateDir()
            }}
            onMoved={(to) => {
              setSelected(to)
              void invalidateDir()
            }}
          />
        ) : null}
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
        <p className="text-sm text-danger">
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
      <p className="p-6 text-center text-sm text-muted-foreground">
        {t('browser.empty')}
      </p>
    )
  return (
    <ul className="max-h-[28rem] overflow-auto pb-2">
      {entries.map((e) => (
        <li key={e.path}>
          <button
            type="button"
            onClick={() => onOpen(e)}
            className={cn(
              'flex w-full items-center gap-2 px-2 py-1.5 text-left text-sm hover:bg-muted',
              selected === e.path && 'bg-muted',
            )}
          >
            {e.type === 'dir' ? (
              <Folder className="size-4 shrink-0 text-accent-text" />
            ) : (
              <FileIcon className="size-4 shrink-0 text-muted-foreground" />
            )}
            <span className="truncate font-mono text-xs text-foreground">
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

// --- File viewer / editor --------------------------------------------------------

function FileViewer({
  workspace,
  path,
  file,
  writable,
  editing,
  editValue,
  onEdit,
  onCancel,
  onChange,
  onSave,
  saving,
  onDeleted,
  onMoved,
}: {
  workspace: WorkspaceDTO
  path: string
  file: import('./types').FileReadResponse
  writable: boolean
  editing: boolean
  editValue: string
  onEdit: () => void
  onCancel: () => void
  onChange: (v: string) => void
  onSave: () => void
  saving: boolean
  onDeleted: () => void
  onMoved: (to: string) => void
}) {
  const { t } = useTranslation('agentops')
  const { activeTenant } = useAuth()
  const qc = useQueryClient()
  const isBinary = file.encoding !== 'utf-8'
  const lang: CodeLanguage = path.toLowerCase().endsWith('.json')
    ? 'json'
    : 'text'

  const [confirmDel, setConfirmDel] = useState(false)
  const [recursive, setRecursive] = useState(false)
  const [moveOpen, setMoveOpen] = useState(false)
  const [moveTo, setMoveTo] = useState(path)

  const del = useMutation({
    mutationFn: () =>
      agentOpsApi.deleteFile(workspace.workspace_ref, path, recursive),
    onSuccess: () => {
      toast.success(t('browser.deletedFile'))
      setConfirmDel(false)
      onDeleted()
    },
    onError: errToast(t),
  })
  const move = useMutation({
    mutationFn: () =>
      agentOpsApi.moveFile(workspace.workspace_ref, path, moveTo.trim()),
    onSuccess: () => {
      toast.success(t('browser.moved'))
      setMoveOpen(false)
      void qc.invalidateQueries({ queryKey: agentOpsKeys.all(activeTenant) })
      onMoved(moveTo.trim())
    },
    onError: errToast(t),
  })

  return (
    <>
      <div className="flex flex-wrap items-center justify-between gap-2">
        <span className="truncate font-mono text-sm font-medium text-foreground">
          {path}
        </span>
        <div className="flex items-center gap-1">
          <Button variant="ghost" size="sm" onClick={() => downloadFile(file)}>
            <Download className="size-3.5" />
            {t('browser.download')}
          </Button>
          {writable && !isBinary && !editing && (
            <Button variant="ghost" size="sm" onClick={onEdit}>
              <Pencil className="size-3.5" />
              {t('browser.edit')}
            </Button>
          )}
          {writable && (
            <Button variant="ghost" size="sm" onClick={() => setMoveOpen(true)}>
              {t('browser.move')}
            </Button>
          )}
          {writable && (
            <Button
              variant="destructive"
              size="sm"
              onClick={() => setConfirmDel(true)}
            >
              <Trash2 className="size-3.5" />
              {t('browser.delete')}
            </Button>
          )}
        </div>
      </div>

      <SensitivityBadges hits={file.sensitivity} />
      {file.truncated && (
        <p className="text-xs text-warning">{t('browser.truncated')}</p>
      )}

      {isBinary ? (
        <p className="rounded-md border border-border bg-muted p-4 text-sm text-muted-foreground">
          {t('browser.binary')}
        </p>
      ) : (
        <CodeEditor
          value={editing ? editValue : file.content}
          onChange={editing ? onChange : undefined}
          language={lang}
          readOnly={!editing}
          ariaLabel={t('browser.editorAria')}
          height="26rem"
        />
      )}

      {editing && (
        <div className="flex items-center justify-end gap-2">
          <Button
            variant="secondary"
            size="sm"
            onClick={onCancel}
            disabled={saving}
          >
            {t('browser.cancel')}
          </Button>
          <Button
            variant="primary"
            size="sm"
            onClick={onSave}
            disabled={saving}
          >
            {saving ? (
              <Spinner className="size-3.5" />
            ) : (
              <Save className="size-3.5" />
            )}
            {saving ? t('browser.saving') : t('browser.save')}
          </Button>
        </div>
      )}
      {!writable && (
        <p className="flex items-center gap-1.5 text-xs text-muted-foreground">
          <Lock className="size-3" />
          {t('browser.readOnlyWorkspace')}
        </p>
      )}

      <ConfirmDialog
        open={confirmDel}
        onOpenChange={setConfirmDel}
        tone="danger"
        title={t('browser.deleteTitle', { name: path })}
        description={t('browser.deleteBody')}
        confirmLabel={t('browser.delete')}
        pending={del.isPending}
        onConfirm={() => del.mutate()}
      >
        <label className="flex items-center gap-2">
          <Switch checked={recursive} onCheckedChange={setRecursive} />
          {t('browser.deleteRecursive')}
        </label>
      </ConfirmDialog>

      <Dialog open={moveOpen} onOpenChange={setMoveOpen}>
        <DialogContent className="max-w-md">
          <DialogHeader>
            <DialogTitle>{t('browser.moveTitle')}</DialogTitle>
            <DialogDescription>
              {t('browser.moveFromLabel')}: {path}
            </DialogDescription>
          </DialogHeader>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="move-to">{t('browser.moveToLabel')}</Label>
            <Input
              id="move-to"
              value={moveTo}
              onChange={(e) => setMoveTo(e.target.value)}
              mono
            />
          </div>
          <DialogFooter>
            <Button
              variant="secondary"
              onClick={() => setMoveOpen(false)}
              disabled={move.isPending}
            >
              {t('browser.cancel')}
            </Button>
            <Button
              variant="primary"
              onClick={() => move.mutate()}
              disabled={
                move.isPending || !moveTo.trim() || moveTo.trim() === path
              }
            >
              {move.isPending && <Spinner className="size-3.5" />}
              {t('browser.move')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
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

// --- helpers ---------------------------------------------------------------------

function SensitivityBadges({
  hits,
  className,
}: {
  hits?: SensitivityHit[]
  className?: string
}) {
  if (!hits || hits.length === 0) return null
  const sev = (s?: string) => {
    switch ((s ?? '').toLowerCase()) {
      case 'high':
        return 'border-danger-line bg-danger-soft text-danger'
      case 'medium':
        return 'border-warning-line bg-warning-soft text-warning'
      default:
        return 'border-border bg-muted text-muted-foreground'
    }
  }
  return (
    <div className={cn('flex flex-wrap items-center gap-1', className)}>
      {hits.map((h, i) => (
        <span
          key={`${h.class}-${i}`}
          title={[h.rule, h.severity].filter(Boolean).join(' · ')}
          className={cn(
            'inline-flex items-center gap-1 rounded-sm border px-1.5 py-0.5 text-[11px] font-medium',
            sev(h.severity),
          )}
        >
          {h.class}
          {h.count ? ` ×${h.count}` : ''}
        </span>
      ))}
    </div>
  )
}

function downloadFile(file: import('./types').FileReadResponse): void {
  let blob: Blob
  if (file.encoding === 'base64') {
    const bin = atob(file.content)
    const bytes = new Uint8Array(bin.length)
    for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i)
    blob = new Blob([bytes], { type: 'application/octet-stream' })
  } else {
    blob = new Blob([file.content], { type: 'text/plain;charset=utf-8' })
  }
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = file.path.split('/').pop() || 'file'
  a.click()
  URL.revokeObjectURL(url)
}

function errToast(t: (k: string) => string) {
  return (err: unknown) =>
    toast.error(err instanceof ApiError ? err.message : t('browser.title'))
}

export { SensitivityBadges }
