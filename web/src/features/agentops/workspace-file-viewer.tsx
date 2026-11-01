// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Download, Lock, Pencil, Save, Trash2 } from 'lucide-react'
import { useMemo, useState } from 'react'
import { Trans, useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { CodeEditor } from '@/components/ui/code-editor'
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
import { cn } from '@/lib/utils'
import { agentOpsApi, agentOpsKeys } from './api'
import type { FileReadResponse, SensitivityHit, WorkspaceDTO } from './types'

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
}) {
  const { t } = useTranslation('agentops')
  const { activeTenant } = useAuth()
  const qc = useQueryClient()
  const isBinary = file.encoding !== 'utf-8'
  const image = file.truncated ? undefined : imageType(path)
  const isMarkdown = path.toLowerCase().endsWith('.md')
  const isHTML = path.toLowerCase().endsWith('.html')
  const hasRenderedView = !isBinary && !image && (isMarkdown || isHTML)
  const source = useMemo(
    () => (image ? imageSource(file, image) : undefined),
    [file, image],
  )
  const lang: CodeLanguage = path.toLowerCase().endsWith('.json')
    ? 'json'
    : 'text'

  const [confirmDel, setConfirmDel] = useState(false)
  const [recursive, setRecursive] = useState(false)
  const [moveOpen, setMoveOpen] = useState(false)
  const [moveTo, setMoveTo] = useState(path)
  const [showSource, setShowSource] = useState(false)

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
    enabled: !isBinary && !image && !file.truncated,
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
        <PathText value={path} />
        <div className="flex flex-wrap items-center gap-1">
          <OpenInEditor
            root={workspace.root_path}
            file={path}
            line={firstChange}
          />
          {hasRenderedView && !editing && (
            <Button
              variant="ghost"
              size="sm"
              onClick={() => setShowSource((current) => !current)}
            >
              {showSource ? t('browser.rendered') : t('browser.source')}
            </Button>
          )}
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
        <p className="text-caption text-warning">{t('browser.truncated')}</p>
      )}

      {image && !editing ? (
        <img
          src={source}
          alt={path}
          loading="lazy"
          decoding="async"
          className="max-h-[26rem] max-w-full self-start rounded-md border border-border bg-muted object-contain"
        />
      ) : isBinary ? (
        <p className="rounded-md border border-border bg-muted p-4 text-body text-muted-foreground">
          {t('browser.binary')}
        </p>
      ) : hasRenderedView && !showSource && !editing ? (
        isMarkdown ? (
          <article className="max-w-[72ch] break-words text-body leading-relaxed text-text">
            <MarkdownText text={file.content} headingOffset={1} />
          </article>
        ) : (
          // ponytail: the console's default Trusted Types policy sanitizes srcDoc, so the
          // page shows its structure without <style>, style= or <head>; a faithful preview
          // needs a sandboxed same-origin route like the session preview's, add it when
          // styled reports matter.
          <iframe
            title={t('browser.htmlPreview', { name: path })}
            sandbox=""
            srcDoc={file.content}
            // eslint-disable-next-line olivares/no-raw-palette -- a document is written for a white page, in every theme
            className="h-[26rem] w-full rounded-md border border-border bg-white"
          />
        )
      ) : (
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
            height="26rem"
          />
        </HeadDiff>
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

function downloadFile(file: FileReadResponse): void {
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
  return (err: unknown) =>
    toast.error(err instanceof ApiError ? err.message : t('browser.title'))
}
