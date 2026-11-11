// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  Code,
  Download,
  Eye,
  FolderInput,
  Lock,
  MoreHorizontal,
  Pencil,
  Save,
  Trash2,
} from 'lucide-react'
import { useMemo, useState, useRef } from 'react'
import { Trans, useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { CodeEditor } from '@/components/ui/code-editor'
import { PassageNotes } from './passage-notes'
import type { CodeLanguage } from '@/components/ui/code-languages'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { PathText } from '@/components/ui/path-text'
import { Spinner } from '@/components/ui/spinner'
import { Switch } from '@/components/ui/switch'
import { toast } from '@/components/ui/toaster'
import { MarkdownText } from '@/features/sessions/markdown-text'
import { HeadDiff } from '@/features/shared/head-diff'
import { headUnavailable } from '@/features/shared/head-unavailable'
import { OpenInEditor } from '@/features/shared/open-in-editor'
import { downloadBlob } from '@/lib/api/download'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import { firstChangedLine } from '@/lib/editor-links'
import { formatBytes } from '@/lib/format'
import { cn } from '@/lib/utils'
import { agentOpsApi, agentOpsKeys } from './api'
import type { FileReadResponse, SensitivityHit, WorkspaceDTO } from './types'

/** An action's name in the file toolbar: shown where the viewer has room for it (44rem),
 * else read only by a screen reader, beside the icon. */
const TOOL_LABEL = '@max-[44rem]/viewer:sr-only'
/** The actions that move under More where the viewer is narrower than that. */
const WIDE_ONLY = '@max-[44rem]/viewer:hidden'
/** Fills a tall pane; in a column with no height of its own, the basis keeps it at 26rem. */
const FILL = 'min-h-0 flex-[1_1_26rem]'

const IMAGE_TYPES: Record<string, string> = {
  '.png': 'image/png',
  '.jpg': 'image/jpeg',
  '.jpeg': 'image/jpeg',
  '.gif': 'image/gif',
  '.webp': 'image/webp',
  '.svg': 'image/svg+xml',
}

function imageType(path: string): string | undefined {
  const dot = path.lastIndexOf('.')
  return dot < 0 ? undefined : IMAGE_TYPES[path.slice(dot).toLowerCase()]
}

function imageSource(file: FileReadResponse, type: string): string {
  if (file.encoding === 'base64') return `data:${type};base64,${file.content}`
  return `data:${type};charset=utf-8,${encodeURIComponent(file.content)}`
}

/**
 * Whether the folder already holds `path`, asked of its file system now (`files/stat`, the
 * file system's own name rules), never of a listing that can be stale or cut. The write and
 * move routes replace what they find, so a name that exists is never written to: a 404 is
 * free, and any other failure is no answer at all.
 */
export async function pathTaken(wref: string, path: string): Promise<boolean> {
  try {
    await agentOpsApi.statFile(wref, path)
    return true
  } catch (err) {
    if (err instanceof ApiError && err.isNotFound) return false
    throw err
  }
}

/** The lines of a text, a last newline ending a line rather than starting one. */
function lineCount(text: string): number {
  if (text === '') return 0
  return text.split('\n').length - (text.endsWith('\n') ? 1 : 0)
}

/** Why a save waits: the file changed, the engine wants it opened again, or it is gone. */
export type Stale = 'changed' | 'reopen' | 'gone'

export function FileViewer({
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
  stale,
  onDiscard,
}: {
  workspace: WorkspaceDTO
  path: string
  file: FileReadResponse
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
  /** Why a save was refused for the file's version, while it waits. */
  stale?: Stale
  /** Drops the draft and reads the server's version again. */
  onDiscard?: () => void
}) {
  const { t } = useTranslation('agentops')
  const { activeTenant } = useAuth()
  const qc = useQueryClient()
  const isBinary = file.encoding !== 'utf-8'
  const image = file.truncated ? undefined : imageType(path)
  const isPDF = /\.pdf$/i.test(path)
  // A cut file saved under its name would look whole, and a cut file saved from the editor
  // would replace the file on disk with its first part: only a whole file is offered or edited.
  const whole = !file.truncated
  const readLimit = formatBytes(workspace.max_read_bytes)
  const isMarkdown = /\.(md|markdown)$/i.test(path)
  const isHTML = /\.html?$/i.test(path)
  const hasRenderedView = !isBinary && !image && (isMarkdown || isHTML)
  const source = useMemo(
    () => (image ? imageSource(file, image) : undefined),
    [file, image],
  )
  const lang: CodeLanguage = /\.json$/i.test(path)
    ? 'json'
    : isMarkdown
      ? 'markdown'
      : 'text'

  const [confirmDel, setConfirmDel] = useState(false)
  const [confirmReload, setConfirmReload] = useState(false)
  const [recursive, setRecursive] = useState(false)
  const [moveOpen, setMoveOpen] = useState(false)
  const [moveTo, setMoveTo] = useState(path)
  const [moveTaken, setMoveTaken] = useState<string | null>(null)
  const moveField = useRef<HTMLInputElement>(null)
  const [showSource, setShowSource] = useState(false)
  // The rendered Markdown: notes are taken from a passage selected in it.
  const rendered = useRef<HTMLElement | null>(null)

  // What git HEAD holds for this file. A 404 (no repository, a file never committed) is
  // an answer: the file shows without a Changes view. Any other failure is said so under
  // the file. Each read is an audited, policy-checked read, so a cut-off file does not
  // ask and a focus change does not ask again.
  const head = useQuery({
    queryKey: agentOpsKeys.fileHead(
      activeTenant,
      workspace.workspace_ref,
      path,
    ),
    queryFn: () => agentOpsApi.readFile(workspace.workspace_ref, path, 'HEAD'),
    enabled: !isBinary && !image && !isPDF && !file.truncated,
    refetchOnWindowFocus: false,
  })
  const committed =
    head.data &&
    head.data.encoding === 'utf-8' &&
    !head.data.truncated &&
    !file.truncated
      ? head.data.content
      : undefined
  // The editor link lands on the first line the file changed since HEAD.
  const firstChange = firstChangedLine(committed, file.content)

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
    mutationFn: async () => {
      const to = moveTo.trim()
      if (await pathTaken(workspace.workspace_ref, to)) return to
      await agentOpsApi.moveFile(workspace.workspace_ref, path, to)
      return null
    },
    onSuccess: (taken) => {
      if (taken) {
        setMoveTaken(taken)
        return moveField.current?.focus()
      }
      toast.success(t('browser.moved'))
      setMoveOpen(false)
      void qc.invalidateQueries({ queryKey: agentOpsKeys.all(activeTenant) })
      onMoved(moveTo.trim())
    },
    onError: (err) => {
      // The target appeared after the check: the engine refuses to replace it.
      if (err instanceof ApiError && err.status === 409) {
        setMoveTaken(moveTo.trim())
        return void moveField.current?.focus()
      }
      errToast(t)(err)
    },
  })

  return (
    <>
      {/* ONE TOOLBAR ROW: the file's path, then its actions. Where the viewer is narrow
          (the session's side pane) Open and Edit are their icons, their names kept for a
          screen reader and shown on hover, and Download, Move and Delete wait under More,
          so the file's name keeps its room. */}
      <div className="flex min-w-0 items-center gap-2">
        <PathText value={path} className="min-w-0 flex-1" />
        {!isBinary && !file.truncated && (
          <span className="shrink-0 text-caption text-text-2 tabular-nums">
            {t('browser.lineCount', { n: lineCount(file.content) })}
          </span>
        )}
        <div className="flex shrink-0 items-center gap-1">
          <OpenInEditor
            root={workspace.root_path}
            file={path}
            line={firstChange}
            labelClassName={TOOL_LABEL}
          />
          {hasRenderedView && !editing && (
            <Button
              variant="ghost"
              size="sm"
              title={showSource ? t('browser.rendered') : t('browser.source')}
              onClick={() => setShowSource((current) => !current)}
            >
              {showSource ? (
                <Eye className="size-3.5" aria-hidden="true" />
              ) : (
                <Code className="size-3.5" aria-hidden="true" />
              )}
              <span className={TOOL_LABEL}>
                {showSource ? t('browser.rendered') : t('browser.source')}
              </span>
            </Button>
          )}
          {whole && (
            <Button
              variant="ghost"
              size="sm"
              className={WIDE_ONLY}
              onClick={() => downloadFile(file)}
            >
              <Download className="size-3.5" aria-hidden="true" />
              {t('browser.download')}
            </Button>
          )}
          {writable && !isBinary && !editing && whole && (
            <Button
              variant="ghost"
              size="sm"
              title={t('browser.edit')}
              onClick={onEdit}
            >
              <Pencil className="size-3.5" aria-hidden="true" />
              <span className={TOOL_LABEL}>{t('browser.edit')}</span>
            </Button>
          )}
          {writable && (
            <Button
              variant="ghost"
              size="sm"
              className={WIDE_ONLY}
              onClick={() => setMoveOpen(true)}
            >
              <FolderInput className="size-3.5" aria-hidden="true" />
              {t('browser.move')}
            </Button>
          )}
          {writable && (
            <Button
              variant="destructive"
              size="sm"
              className={WIDE_ONLY}
              onClick={() => setConfirmDel(true)}
            >
              <Trash2 className="size-3.5" aria-hidden="true" />
              {t('browser.delete')}
            </Button>
          )}
          {(whole || writable) && (
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button
                  variant="ghost"
                  size="sm"
                  className="@min-[44rem]/viewer:hidden"
                  aria-label={t('browser.moreActions')}
                  title={t('browser.moreActions')}
                >
                  <MoreHorizontal className="size-3.5" aria-hidden="true" />
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end">
                {whole && (
                  <DropdownMenuItem onSelect={() => downloadFile(file)}>
                    <Download aria-hidden="true" />
                    {t('browser.download')}
                  </DropdownMenuItem>
                )}
                {writable && (
                  <DropdownMenuItem onSelect={() => setMoveOpen(true)}>
                    <FolderInput aria-hidden="true" />
                    {t('browser.move')}
                  </DropdownMenuItem>
                )}
                {writable && (
                  <DropdownMenuItem
                    variant="destructive"
                    onSelect={() => setConfirmDel(true)}
                  >
                    <Trash2 aria-hidden="true" />
                    {t('browser.delete')}
                  </DropdownMenuItem>
                )}
              </DropdownMenuContent>
            </DropdownMenu>
          )}
        </div>
      </div>

      <SensitivityBadges hits={file.sensitivity} />
      {!whole && !isPDF && (
        <p className="text-caption text-warning">
          {t('browser.truncated', { limit: readLimit })}
        </p>
      )}

      {image && !editing ? (
        <img
          src={source}
          alt={path}
          loading="lazy"
          decoding="async"
          className="max-h-[26rem] max-w-full self-start rounded-md border border-border bg-muted object-contain"
        />
      ) : isPDF && !editing ? (
        // A PDF is offered to save, never framed: a frame that could show it would run in
        // the console's origin. Nothing opens until the person asks.
        <div className="flex flex-col items-start gap-2 rounded-md border border-border bg-muted p-4">
          <PathText value={path} copy={false} />
          <span className="text-caption text-text-2 tabular-nums">
            {formatBytes(file.size)}
          </span>
          {whole ? (
            <>
              <p className="text-body text-text-2">
                {t('browser.pdfNoPreview')}
              </p>
              <Button
                variant="secondary"
                size="sm"
                onClick={() => downloadFile(file)}
              >
                <Download className="size-3.5" aria-hidden="true" />
                {t('browser.download')}
              </Button>
            </>
          ) : (
            <p className="text-body text-warning">
              {t('browser.truncated', { limit: readLimit })}
            </p>
          )}
        </div>
      ) : isBinary ? (
        <p className="rounded-md border border-border bg-muted p-4 text-body text-muted-foreground">
          {t('browser.binary')}
        </p>
      ) : hasRenderedView && !showSource && !editing ? (
        isMarkdown ? (
          <>
            <article
              ref={rendered}
              className="max-w-[72ch] break-words text-body leading-relaxed text-text"
            >
              <MarkdownText text={file.content} headingOffset={1} />
            </article>
            <PassageNotes
              workspace={workspace}
              path={path}
              container={rendered}
            />
          </>
        ) : (
          // ponytail: the console's default Trusted Types policy sanitizes srcDoc, so the
          // page shows its structure without <style>, style= or <head>; a faithful preview
          // needs a frame the console's CSP allows from a blob: URL, add it when the CSP does.
          <iframe
            title={t('browser.htmlPreview', { name: path })}
            sandbox=""
            srcDoc={file.content}
            // eslint-disable-next-line olivares/no-raw-palette -- a document is written for a white page, in every theme
            className={`${FILL} w-full rounded-md border border-border bg-white`}
          />
        )
      ) : (
        // The file view and its bar share the height; HeadDiff's root is the only child.
        <div className={`${FILL} flex flex-col [&>*]:min-h-0 [&>*]:flex-1`}>
          <HeadDiff
            key={path}
            committed={editing ? undefined : committed}
            current={file.content}
            language={lang}
            height="26rem"
            unavailable={!editing && headUnavailable(head.error)}
          >
            <CodeEditor
              value={editing ? editValue : file.content}
              onChange={editing ? onChange : undefined}
              language={lang}
              readOnly={!editing}
              ariaLabel={t('browser.editorAria')}
              height="none"
              className="min-h-0 flex-1"
            />
          </HeadDiff>
        </div>
      )}

      {editing && stale && (
        <div
          role="alert"
          className="flex flex-col gap-2 rounded-md border border-warning-line bg-warning-soft p-3 text-body text-text"
        >
          <p>
            {stale === 'gone'
              ? t('browser.staleGone')
              : stale === 'reopen'
                ? t('browser.staleReopen')
                : t('browser.stale')}
          </p>
          <div className="flex flex-wrap gap-2">
            <Button
              variant="secondary"
              size="sm"
              onClick={() => void navigator.clipboard?.writeText(editValue)}
            >
              {t('browser.staleCopy')}
            </Button>
            {stale !== 'gone' && (
              <Button
                variant="secondary"
                size="sm"
                onClick={() => setConfirmReload(true)}
              >
                {t('browser.staleReload')}
              </Button>
            )}
          </div>
        </div>
      )}
      <ConfirmDialog
        open={confirmReload}
        onOpenChange={setConfirmReload}
        tone="danger"
        title={t('browser.staleReloadTitle')}
        description={t('browser.staleReloadBody')}
        confirmLabel={t('browser.staleDiscard')}
        onConfirm={() => {
          setConfirmReload(false)
          onDiscard?.()
        }}
      />
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
          {whole && (
            <Button
              variant="primary"
              size="sm"
              onClick={onSave}
              disabled={saving || !!stale}
            >
              {saving ? <Spinner size="sm" /> : <Save className="size-3.5" />}
              {saving ? t('browser.saving') : t('browser.save')}
            </Button>
          )}
        </div>
      )}
      {!writable && (
        <p className="flex items-center gap-1.5 text-caption text-muted-foreground">
          <Lock className="size-3" />
          {t('browser.readOnlyWorkspace')}
        </p>
      )}

      <ConfirmDialog
        open={confirmDel}
        onOpenChange={setConfirmDel}
        tone="danger"
        title={
          <Trans
            i18nKey="browser.deleteTitle"
            ns="agentops"
            components={{ path: <PathText value={path} copy={false} wrap /> }}
          />
        }
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
              {t('browser.moveFromLabel')}:{' '}
              <PathText value={path} copy={false} wrap />
            </DialogDescription>
          </DialogHeader>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="move-to">{t('browser.moveToLabel')}</Label>
            <Input
              ref={moveField}
              id="move-to"
              aria-invalid={moveTaken ? true : undefined}
              aria-describedby={moveTaken ? 'move-to-taken' : undefined}
              value={moveTo}
              onChange={(e) => {
                setMoveTo(e.target.value)
                setMoveTaken(null)
              }}
              mono
            />
            {moveTaken && <NameTaken id="move-to-taken" name={moveTaken} />}
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
              {move.isPending && <Spinner size="sm" />}
              {t('browser.move')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  )
}

/** The one sentence a create or a move says when its name is already taken. */
export function NameTaken({ id, name }: { id: string; name: string }) {
  return (
    <p id={id} role="alert" className="text-caption text-danger">
      <Trans
        i18nKey="browser.exists"
        ns="agentops"
        components={{ name: <PathText value={name} copy={false} /> }}
      />
    </p>
  )
}

export function SensitivityBadges({
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

export function downloadFile(file: FileReadResponse): void {
  let blob: Blob
  if (file.encoding === 'base64') {
    const bin = atob(file.content)
    const bytes = new Uint8Array(bin.length)
    for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i)
    blob = new Blob([bytes], { type: 'application/octet-stream' })
  } else {
    blob = new Blob([file.content], { type: 'text/plain;charset=utf-8' })
  }
  downloadBlob(blob, file.path.split('/').pop() || 'file')
}

function errToast(t: (k: string) => string) {
  return (err: unknown) => {
    // A write the engine asks a precondition for (428) is the console's own gap: its remedy.
    if (err instanceof ApiError && err.status === 428)
      return void toast.warning(t('browser.reopenToSave'))
    toast.error(err instanceof ApiError ? err.message : t('browser.title'))
  }
}
