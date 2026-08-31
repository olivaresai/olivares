// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Bookmark, BookmarkPlus, Trash2 } from 'lucide-react'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
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
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { Field } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Spinner } from '@/components/ui/spinner'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import { roleRank } from '@/lib/auth/rbac'
import { savedViewsApi, savedViewsKeys } from './api'
import type { SavedView } from './types'
import './i18n'

export interface SavedViewsMenuProps {
  featureId: string
  params: Record<string, string | undefined>
  onApply: (params: Record<string, string>) => void
}

const EMPTY_SAVED_VIEWS: SavedView[] = []

/** Keep only non-empty string URL values. The module intentionally permits any
 * JSON object, so values read back from the server remain untrusted data. */
function stringParams(params: Record<string, unknown>): Record<string, string> {
  return Object.fromEntries(
    Object.entries(params).filter(
      (entry): entry is [string, string] =>
        typeof entry[1] === 'string' && entry[1] !== '',
    ),
  )
}

export function SavedViewsMenu({
  featureId,
  params,
  onApply,
}: SavedViewsMenuProps) {
  const { t } = useTranslation('saved-views')
  const { activeTenant, activeRole, confinedWorkspace, isSuperadmin, can } =
    useAuth()
  const queryClient = useQueryClient()
  const canRead = can('consoleviews:view:read')
  const canWrite = can('consoleviews:view:write')
  // Delete-any is a ROLE power, so it is asked as one. The server's rule is
  // `Superadmin || ((role == admin || role == owner) && !confined)`
  // (modules/consoleviews/consoleviews.go, handleDelete) — there is no
  // `consoleviews:view:admin` permission and there never was.
  //
  // Asking can() for the invented name used to work by accident: the console read the
  // trailing verb, resolved 'admin' to the admin/owner tier and happened to land on the
  // right answer. That accident is gone now that the mirror consults the engine's
  // declared sets, and it deserved to be — the same accident is what silently opened
  // `voice:write` to editors against a route requiring admin. A UI that asks a question
  // the server does not answer is right only until someone changes the spelling.
  //
  // The confinement clause is now expressible, and this is the whole of Q4-F2. The
  // KNOWN LIMIT that used to be recorded here — "`Whoami.grants` carries only tenant and
  // role, so no client-side expression can narrow it", which left a workspace-confined
  // admin looking at a Delete button the server refuses — ended when the grant started
  // carrying `confined_workspace`. The line below is now the server's rule term for
  // term, and it is deliberately NOT asked through can(): delete-any is a ROLE gate the
  // handler applies itself, so no permission set could ever answer it.
  const canDeleteAny =
    isSuperadmin ||
    (roleRank(activeRole ?? undefined) >= roleRank('admin') && !confinedWorkspace)
  const [saveOpen, setSaveOpen] = useState(false)
  const [deleteTarget, setDeleteTarget] = useState<SavedView | null>(null)

  const viewsQ = useQuery({
    queryKey: savedViewsKeys.list(activeTenant, featureId),
    queryFn: () => savedViewsApi.list(featureId),
    enabled: canRead,
  })

  const views = viewsQ.data?.items ?? EMPTY_SAVED_VIEWS
  const mine = useMemo(() => views.filter((view) => view.mine), [views])
  const shared = useMemo(() => views.filter((view) => !view.mine), [views])

  const deleteM = useMutation({
    mutationFn: (id: string) => savedViewsApi.delete(id),
    onSuccess: async () => {
      await queryClient.invalidateQueries({
        queryKey: savedViewsKeys.list(activeTenant, featureId),
      })
      setDeleteTarget(null)
    },
  })

  if (!canRead) return null

  function renderViews(items: SavedView[]) {
    return items.map((view) => (
      <div key={view.id}>
        <DropdownMenuItem onSelect={() => onApply(stringParams(view.params))}>
          <Bookmark aria-hidden />
          <span className="min-w-0 truncate">{view.name}</span>
        </DropdownMenuItem>
        {(view.mine || canDeleteAny) && (
          <DropdownMenuItem
            variant="destructive"
            inset
            aria-label={t('menu.deleteAria', { name: view.name })}
            onSelect={() => {
              deleteM.reset()
              setDeleteTarget(view)
            }}
          >
            <Trash2 aria-hidden />
            {t('menu.delete')}
          </DropdownMenuItem>
        )}
      </div>
    ))
  }

  return (
    <>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button variant="secondary" size="sm">
            <Bookmark aria-hidden />
            {t('menu.trigger')}
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end" className="min-w-56">
          {viewsQ.isLoading ? (
            <DropdownMenuItem disabled>{t('menu.loading')}</DropdownMenuItem>
          ) : viewsQ.isError ? (
            <DropdownMenuItem disabled>{t('menu.loadFailed')}</DropdownMenuItem>
          ) : views.length === 0 ? (
            <DropdownMenuItem disabled>{t('menu.empty')}</DropdownMenuItem>
          ) : (
            <>
              {mine.length > 0 && (
                <>
                  <DropdownMenuLabel>{t('menu.myViews')}</DropdownMenuLabel>
                  {renderViews(mine)}
                </>
              )}
              {shared.length > 0 && (
                <>
                  {mine.length > 0 && <DropdownMenuSeparator />}
                  <DropdownMenuLabel>{t('menu.shared')}</DropdownMenuLabel>
                  {renderViews(shared)}
                </>
              )}
            </>
          )}
          {canWrite && (
            <>
              <DropdownMenuSeparator />
              <DropdownMenuItem onSelect={() => setSaveOpen(true)}>
                <BookmarkPlus aria-hidden />
                {t('menu.save')}
              </DropdownMenuItem>
            </>
          )}
        </DropdownMenuContent>
      </DropdownMenu>

      {saveOpen && (
        <SaveViewDialog
          featureId={featureId}
          params={params}
          onClose={() => setSaveOpen(false)}
        />
      )}

      <ConfirmDialog
        open={deleteTarget !== null}
        onOpenChange={(open) => {
          if (!open) setDeleteTarget(null)
        }}
        title={t('delete.title')}
        description={
          deleteTarget
            ? t('delete.description', { name: deleteTarget.name })
            : undefined
        }
        confirmLabel={t('delete.confirm')}
        cancelLabel={t('delete.cancel')}
        tone="danger"
        pending={deleteM.isPending}
        onConfirm={() => {
          if (deleteTarget) deleteM.mutate(deleteTarget.id)
        }}
      >
        {deleteM.isError && (
          <p className="font-medium text-danger">
            {deleteM.error instanceof Error
              ? deleteM.error.message
              : t('delete.failed')}
          </p>
        )}
      </ConfirmDialog>
    </>
  )
}

function SaveViewDialog({
  featureId,
  params,
  onClose,
}: {
  featureId: string
  params: Record<string, string | undefined>
  onClose: () => void
}) {
  const { t } = useTranslation('saved-views')
  const { activeTenant } = useAuth()
  const queryClient = useQueryClient()
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [shared, setShared] = useState(false)

  const saveM = useMutation({
    mutationFn: () =>
      savedViewsApi.create({
        feature_id: featureId,
        name: name.trim(),
        ...(description.trim() ? { description: description.trim() } : {}),
        params: stringParams(params),
        shared,
      }),
    onSuccess: async () => {
      await queryClient.invalidateQueries({
        queryKey: savedViewsKeys.list(activeTenant, featureId),
      })
      onClose()
    },
  })

  const inlineError =
    saveM.error instanceof ApiError &&
    (saveM.error.status === 409 || saveM.error.status === 422)
      ? saveM.error.message
      : saveM.isError
        ? t('save.failed')
        : null

  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open && !saveM.isPending) onClose()
      }}
    >
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t('save.title')}</DialogTitle>
          <DialogDescription>{t('save.description')}</DialogDescription>
        </DialogHeader>
        <form
          className="flex flex-col gap-4"
          onSubmit={(event) => {
            event.preventDefault()
            if (name.trim()) saveM.mutate()
          }}
        >
          <Field label={t('save.name')} required>
            {({ id }) => (
              <Input
                id={id}
                value={name}
                maxLength={120}
                onChange={(event) => setName(event.target.value)}
                autoFocus
              />
            )}
          </Field>
          <Field label={t('save.viewDescription')}>
            {({ id }) => (
              <Textarea
                id={id}
                value={description}
                maxLength={500}
                onChange={(event) => setDescription(event.target.value)}
              />
            )}
          </Field>
          <div className="flex items-center justify-between gap-4 rounded-md border border-border p-3">
            <span className="min-w-0">
              <span className="block text-sm font-medium text-foreground">
                {t('save.share')}
              </span>
              <span className="block text-xs text-muted-foreground">
                {t('save.shareHint')}
              </span>
            </span>
            <Switch
              checked={shared}
              onCheckedChange={setShared}
              aria-label={t('save.share')}
            />
          </div>
          {inlineError && (
            <p role="alert" className="text-sm font-medium text-danger">
              {inlineError}
            </p>
          )}
          <DialogFooter>
            <Button
              type="button"
              variant="ghost"
              onClick={onClose}
              disabled={saveM.isPending}
            >
              {t('save.cancel')}
            </Button>
            <Button
              type="submit"
              variant="primary"
              disabled={!name.trim() || saveM.isPending}
            >
              {saveM.isPending && <Spinner size="sm" aria-hidden />}
              {t('save.submit')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
