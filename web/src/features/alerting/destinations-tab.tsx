// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Destinations tab: add, test and remove the Slack and Microsoft Teams destinations a
// route can send to, without editing OLIVARES_NOTIFY_CONFIG. The webhook URL is the
// secret: it is typed into a masked field, sealed by the engine, and never shown again
// (the list names only the host it points at). Destinations from the operator file are
// listed read-only; every other kind (Jira, PagerDuty, SIEM) stays in that file.
// The engine owns the rules (vendor hosts only, superadmin, AAL3 step-up); this view
// mirrors only what it needs to avoid offering an action the engine refuses.
import './i18n'
import { useState } from 'react'
import { useQuery, type QueryKey } from '@tanstack/react-query'
import { Pencil, Plus, Send, Trash2 } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { DataTable, type TableColumn } from '@/components/data/data-table'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { EmptyState } from '@/components/ui/empty-state'
import { Field } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { toast } from '@/components/ui/toaster'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import {
  notifyApi,
  notifyKeys,
  type NotifyDestinationInput,
  type NotifyDestinationKind,
  type NotifyDestinationRow,
} from './api'

// The engine's name rule (cmd/olivares/notifydestinations.go notifyDestinationName),
// mirrored so Save stays disabled for a name the engine would refuse.
const NAME_RE = /^[a-z0-9][a-z0-9._-]{0,62}$/

export function DestinationsTab() {
  const { t } = useTranslation(['alerting', 'common'])
  const { can, activeTenant } = useAuth()
  const isAdmin = can('system:admin')
  const [editing, setEditing] = useState<NotifyDestinationRow | 'new' | null>(
    null,
  )
  const [deleting, setDeleting] = useState<NotifyDestinationRow | null>(null)

  const listQ = useQuery({
    queryKey: notifyKeys.consoleDestinations(),
    queryFn: () => notifyApi.listConsoleDestinations(),
    enabled: isAdmin,
  })
  const kinds = listQ.data?.kinds ?? []
  const refresh = (): QueryKey[] => [
    notifyKeys.consoleDestinations(),
    // The route editor's selector lists the same destinations by name.
    notifyKeys.destinations(activeTenant),
  ]

  const testSaved = usePrivilegedMutation<NotifyDestinationRow>({
    stepUpAction: 'console',
    mutationFn: (row, { signal, dispatchGuard }) =>
      notifyApi.testConsoleDestination(
        { name: row.name, kind: row.kind },
        { signal, dispatchGuard },
      ),
    successMessage: (_data, row) =>
      t('destinations.tested', { name: row.name }),
    onError: (err) => reportTestFailure(err, t('destinations.testFailed')),
  })

  const remove = usePrivilegedMutation<string>({
    stepUpAction: 'console',
    mutationFn: (name, { signal, dispatchGuard }) =>
      notifyApi.deleteConsoleDestination(name, { signal, dispatchGuard }),
    invalidateKeys: refresh,
    successMessage: t('destinations.deleted'),
    onDone: () => setDeleting(null),
  })

  if (!isAdmin) {
    return (
      <EmptyState
        title={t('destinations.adminOnly')}
        description={t('destinations.adminOnlyHint')}
      />
    )
  }

  const kindTitle = (kind: string) =>
    kinds.find((k) => k.kind === kind)?.title ?? kind
  const columns: TableColumn<NotifyDestinationRow>[] = [
    {
      accessorKey: 'name',
      header: t('destinations.colName'),
      cell: ({ row }) => (
        <span className="font-medium text-foreground">{row.original.name}</span>
      ),
    },
    {
      accessorKey: 'kind',
      header: t('destinations.colKind'),
      cell: ({ row }) => kindTitle(row.original.kind),
    },
    {
      accessorKey: 'webhook_host',
      header: t('destinations.colHost'),
      cell: ({ row }) => (
        <span className="font-mono text-muted-foreground">
          {row.original.webhook_host ?? '—'}
        </span>
      ),
    },
    {
      accessorKey: 'source',
      header: t('destinations.colSource'),
      cell: ({ row }) => (
        <Badge variant="neutral">
          {row.original.source === 'file'
            ? t('destinations.sourceFile')
            : t('destinations.sourceConsole')}
        </Badge>
      ),
    },
    {
      accessorKey: 'live',
      header: t('destinations.colStatus'),
      cell: ({ row }) =>
        row.original.live ? (
          <Badge variant="success">{t('destinations.open')}</Badge>
        ) : (
          <Badge variant="warning">{t('destinations.notOpen')}</Badge>
        ),
    },
    {
      id: 'actions',
      header: () => (
        <span className="sr-only">{t('destinations.colActions')}</span>
      ),
      enableSorting: false,
      cell: ({ row }) => {
        const dest = row.original
        // A file destination is provisioned outside the console: nothing here edits it.
        if (dest.source !== 'console') return null
        return (
          <div className="flex justify-end gap-1">
            <Button
              variant="ghost"
              size="icon-sm"
              aria-label={t('destinations.testAction', { name: dest.name })}
              disabled={testSaved.isPending}
              onClick={() => testSaved.mutate(dest)}
            >
              <Send />
            </Button>
            <Button
              variant="ghost"
              size="icon-sm"
              aria-label={t('destinations.editAction', { name: dest.name })}
              onClick={() => setEditing(dest)}
            >
              <Pencil />
            </Button>
            <Button
              variant="ghost"
              size="icon-sm"
              aria-label={t('destinations.deleteAction', { name: dest.name })}
              onClick={() => setDeleting(dest)}
            >
              <Trash2 />
            </Button>
          </div>
        )
      },
    },
  ]

  const addButton = (
    <Button variant="primary" size="sm" onClick={() => setEditing('new')}>
      <Plus />
      {t('destinations.add')}
    </Button>
  )

  return (
    <div className="flex flex-col gap-4">
      <div className="flex items-start justify-between gap-4">
        <p className="text-body text-muted-foreground">
          {t('destinations.intro')}
        </p>
        {addButton}
      </div>
      {listQ.data && !listQ.data.sealer_available ? (
        <p role="alert" className="text-body text-warning">
          {t('destinations.noSealer')}
        </p>
      ) : null}
      <DataTable
        columns={columns}
        data={listQ.data?.destinations ?? []}
        isLoading={listQ.isLoading}
        error={listQ.error}
        onRetry={() => void listQ.refetch()}
        empty={
          <EmptyState
            title={t('destinations.empty')}
            description={t('destinations.emptyHint')}
            action={addButton}
          />
        }
        getRowId={(d) => d.name}
        label={t('tabs.destinations')}
      />
      <p className="text-caption text-muted-foreground">
        {t('destinations.fileNote')}
      </p>
      {editing ? (
        <DestinationDialog
          key={editing === 'new' ? 'new' : editing.name}
          destination={editing === 'new' ? undefined : editing}
          kinds={kinds}
          refresh={refresh}
          onClose={() => setEditing(null)}
        />
      ) : null}
      <Dialog
        open={deleting != null}
        onOpenChange={(open) => {
          if (!open && !remove.isPending) setDeleting(null)
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t('destinations.deleteDialog.title')}</DialogTitle>
            <DialogDescription>
              {t('destinations.deleteDialog.description', {
                name: deleting?.name ?? '',
              })}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button
              variant="secondary"
              onClick={() => setDeleting(null)}
              disabled={remove.isPending}
            >
              {t('common:actions.cancel')}
            </Button>
            <Button
              variant="destructive-solid"
              onClick={() => deleting && remove.mutate(deleting.name)}
              disabled={remove.isPending}
            >
              {t('destinations.deleteDialog.confirm')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}

/** A refused test message is the destination's answer, not a console failure: say so
 * plainly instead of the generic error toast. Returns true when it handled the error. */
function reportTestFailure(err: unknown, message: string): boolean {
  if (
    err instanceof ApiError &&
    err.code === 'notify_destination_test_failed'
  ) {
    toast.error(message)
    return true
  }
  return false
}

function DestinationDialog({
  destination,
  kinds,
  refresh,
  onClose,
}: {
  destination?: NotifyDestinationRow
  kinds: NotifyDestinationKind[]
  refresh: () => QueryKey[]
  onClose: () => void
}) {
  const { t } = useTranslation(['alerting', 'common'])
  const isEdit = destination != null
  // The kind list may arrive after the dialog opens, so the default is derived, not stored.
  const [chosenKind, setKind] = useState(destination?.kind ?? '')
  const kind = chosenKind || kinds[0]?.kind || ''
  const [name, setName] = useState(destination?.name ?? '')
  const [url, setUrl] = useState('')

  const input = (): NotifyDestinationInput => ({
    name: name.trim(),
    kind,
    // Blank keeps the stored URL on an update, and tests it.
    ...(url.trim() ? { webhook_url: url.trim() } : {}),
  })
  const save = usePrivilegedMutation<
    NotifyDestinationInput,
    NotifyDestinationRow
  >({
    stepUpAction: 'console',
    mutationFn: (body, { signal, dispatchGuard }) =>
      notifyApi.putConsoleDestination(body, { signal, dispatchGuard }),
    invalidateKeys: refresh,
    successMessage: (res) =>
      res.live ? t('destinations.saved') : t('destinations.savedNotOpen'),
    successIntent: (res) => (res.live ? 'success' : 'warning'),
    onDone: onClose,
  })
  const test = usePrivilegedMutation<NotifyDestinationInput>({
    stepUpAction: 'console',
    mutationFn: (body, { signal, dispatchGuard }) =>
      notifyApi.testConsoleDestination(body, { signal, dispatchGuard }),
    successMessage: t('destinations.testSent'),
    onError: (err) => reportTestFailure(err, t('destinations.testFailed')),
  })

  const busy = save.isPending || test.isPending
  const hasUrl = url.trim() !== ''
  const valid = NAME_RE.test(name.trim()) && kind !== '' && (isEdit || hasUrl)
  const canTest = kind !== '' && (hasUrl || isEdit)

  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open && !busy) onClose()
      }}
    >
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>
            {isEdit
              ? t('destinations.dialog.editTitle')
              : t('destinations.dialog.title')}
          </DialogTitle>
          <DialogDescription>
            {t('destinations.dialog.subtitle')}
          </DialogDescription>
        </DialogHeader>
        <form
          className="flex flex-col gap-3"
          onSubmit={(e) => {
            e.preventDefault()
            if (valid && !busy) save.mutate(input())
          }}
        >
          <Field label={t('destinations.dialog.kind')} required>
            <Select value={kind} onValueChange={setKind} disabled={isEdit}>
              <SelectTrigger aria-label={t('destinations.dialog.kind')}>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {kinds.map((k) => (
                  <SelectItem key={k.kind} value={k.kind}>
                    {k.title}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>
          <Field
            label={t('destinations.dialog.name')}
            description={t('destinations.dialog.nameHint')}
            required
          >
            {({ id }) => (
              <Input
                id={id}
                value={name}
                disabled={isEdit}
                autoComplete="off"
                onChange={(e) => setName(e.target.value)}
              />
            )}
          </Field>
          <Field
            label={t('destinations.dialog.url')}
            description={
              isEdit
                ? t('destinations.dialog.urlKeep', {
                    host: destination?.webhook_host ?? '',
                  })
                : t(`destinations.dialog.urlHint.${kind}`, {
                    defaultValue: t('destinations.dialog.urlHint.default'),
                  })
            }
            required={!isEdit}
          >
            {({ id }) => (
              <Input
                id={id}
                type="password"
                value={url}
                autoComplete="off"
                spellCheck={false}
                placeholder={
                  isEdit ? t('destinations.dialog.urlStored') : 'https://'
                }
                onChange={(e) => setUrl(e.target.value)}
              />
            )}
          </Field>
          <DialogFooter>
            <Button
              type="button"
              variant="secondary"
              disabled={busy || !canTest}
              onClick={() => test.mutate(input())}
            >
              <Send />
              {t('destinations.dialog.test')}
            </Button>
            <Button
              type="button"
              variant="secondary"
              onClick={onClose}
              disabled={busy}
            >
              {t('common:actions.cancel')}
            </Button>
            <Button type="submit" variant="primary" disabled={!valid || busy}>
              {t('common:actions.save')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
