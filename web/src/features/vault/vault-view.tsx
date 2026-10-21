// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The vault: a person's own logins, notes and authenticator keys, sealed on this server
// where only they can read them back. The page is a search box and a list. Add, find,
// copy and delete are each one action: a row copies its password or code in one click
// (the clipboard is cleared 30 seconds later), and opening an item reveals it.
import { useQuery } from '@tanstack/react-query'
import {
  Copy,
  FileText,
  KeyRound,
  LockKeyhole,
  MoreHorizontal,
  Plus,
  Search,
  ShieldCheck,
  Trash2,
} from 'lucide-react'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { QueryErrorState } from '@/components/layout/query-error-state'
import { Button } from '@/components/ui/button'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import { Dialog, DialogContent } from '@/components/ui/dialog'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { EmptyState } from '@/components/ui/empty-state'
import { Input } from '@/components/ui/input'
import { PageHeader } from '@/components/ui/page-header'
import { Spinner } from '@/components/ui/spinner'
import { useAuthBoundary } from '@/features/agentops/auth-boundary'
import { RelTimeLabel } from '@/features/shared'
import { useAuth } from '@/lib/auth/context'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { vaultApi, vaultKeys } from './api'
import { CLIPBOARD_CLEAR_MS, copySecret } from './clipboard'
import { ItemDetail } from './item-detail'
import { ItemForm } from './item-form'
import {
  findRows,
  parseItem,
  primaryValue,
  vaultRows,
  type VaultKind,
  type VaultRow,
} from './model'
import './i18n'

type Copied = 'password' | 'code' | 'note'

const KIND_ICON: Record<VaultKind, typeof KeyRound> = {
  login: KeyRound,
  note: FileText,
  totp: ShieldCheck,
}

export function VaultView() {
  const boundary = useAuthBoundary()
  // Whatever was open belonged to the previous person or organization: a new boundary
  // mounts a fresh page, with nothing open and nothing typed.
  return <VaultBody key={boundary.key} />
}

function VaultBody() {
  const { t } = useTranslation(['vault', 'common'])
  const { principal } = useAuth()
  const boundary = useAuthBoundary()
  const userId = principal?.user_id ?? ''
  const [query, setQuery] = useState('')
  const [adding, setAdding] = useState(false)
  const [opened, setOpened] = useState<VaultRow | null>(null)
  const [deleting, setDeleting] = useState<VaultRow | null>(null)

  const list = useQuery({
    queryKey: vaultKeys.list(boundary.tenant, boundary.epoch),
    queryFn: ({ signal }) =>
      vaultApi.list({ signal, tenant: boundary.tenant ?? undefined }),
    enabled: !!boundary.tenant && !!userId,
  })
  const rows = useMemo(
    () => vaultRows(list.data?.secrets ?? [], userId),
    [list.data, userId],
  )
  const shown = useMemo(() => findRows(rows, query), [rows, query])
  const taken = useMemo(() => new Set(rows.map((r) => r.label)), [rows])

  // The copy happens inside the request, so the secret is never kept as the answer.
  const copy = usePrivilegedMutation<{ row: VaultRow; what: Copied }, Copied>({
    mutationFn: async ({ row, what }, authority) => {
      const opts = { ...authority, tenant: boundary.tenant ?? undefined }
      let text: string
      if (what === 'code') {
        text = (await vaultApi.code(row.name, opts)).code
      } else {
        const { value } = await vaultApi.reveal(row.name, opts)
        const item = parseItem(value)
        text = item ? primaryValue(item) : value
      }
      if (text === '') throw new Error(t('vault:nothingToCopy'))
      if (!(await copySecret(text))) throw new Error(t('vault:copyBlocked'))
      return what
    },
    successMessage: (what) =>
      t('vault:rowCopied', {
        what: t(`vault:what.${what}`),
        seconds: CLIPBOARD_CLEAR_MS / 1000,
      }),
  })

  const remove = usePrivilegedMutation<VaultRow, void>({
    mutationFn: (row, authority) =>
      vaultApi.remove(row.name, {
        ...authority,
        tenant: boundary.tenant ?? undefined,
      }),
    invalidateKeys: () => [vaultKeys.list(boundary.tenant, boundary.epoch)],
    successMessage: t('vault:deleted'),
    onDone: () => setDeleting(null),
  })

  const sealerMissing = list.data && !list.data.sealer_available

  return (
    <div className="flex h-full flex-col gap-4">
      <PageHeader
        icon={LockKeyhole}
        title={t('vault:title')}
        description={t('vault:subtitle')}
        primaryAction={
          // Adding waits for the list: without it the form cannot tell that a name is taken,
          // and saving over an existing item would replace it.
          <Button
            variant="primary"
            disabled={!list.isSuccess}
            onClick={() => setAdding(true)}
          >
            <Plus />
            {t('vault:add')}
          </Button>
        }
      />
      {sealerMissing && (
        <p className="text-body text-warning" role="status">
          {t('vault:sealerUnavailable')}
        </p>
      )}
      {rows.length > 0 && (
        <div className="relative max-w-md">
          <Search
            className="pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2 text-muted-foreground"
            aria-hidden
          />
          <Input
            type="search"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            aria-label={t('vault:search')}
            placeholder={t('vault:search')}
            className="pl-9"
            autoComplete="off"
          />
        </div>
      )}
      {!userId ? (
        <p className="text-body text-muted-foreground" role="status">
          {t('vault:needPerson')}
        </p>
      ) : !boundary.tenant ? (
        <p className="text-body text-muted-foreground" role="status">
          {t('vault:needOrganization')}
        </p>
      ) : list.isLoading ? (
        <div className="flex justify-center py-8">
          <Spinner />
        </div>
      ) : list.isError ? (
        <QueryErrorState error={list.error} retry={() => void list.refetch()} />
      ) : rows.length === 0 ? (
        <EmptyState
          icon={<LockKeyhole />}
          title={t('vault:empty.title')}
          description={t('vault:empty.description')}
          action={
            <Button
              variant="primary"
              disabled={!list.isSuccess}
              onClick={() => setAdding(true)}
            >
              <Plus />
              {t('vault:add')}
            </Button>
          }
        />
      ) : shown.length === 0 ? (
        <p className="text-body text-muted-foreground" role="status">
          {t('vault:noMatch', { query })}
        </p>
      ) : (
        <ul className="flex flex-col gap-2" aria-label={t('vault:title')}>
          {shown.map((row) => (
            <ItemRow
              key={row.name}
              row={row}
              busy={copy.isPending}
              onOpen={() => setOpened(row)}
              onCopy={(what) => copy.mutate({ row, what })}
              onDelete={() => setDeleting(row)}
            />
          ))}
        </ul>
      )}

      <Dialog open={adding} onOpenChange={setAdding}>
        <DialogContent className="max-h-[85vh] max-w-lg overflow-y-auto">
          {adding && (
            <ItemForm
              userId={userId}
              taken={taken}
              onCancel={() => setAdding(false)}
              onSaved={() => setAdding(false)}
            />
          )}
        </DialogContent>
      </Dialog>

      <Dialog
        open={opened !== null}
        onOpenChange={(o) => !o && setOpened(null)}
      >
        <DialogContent className="max-h-[85vh] max-w-lg overflow-y-auto">
          {opened && (
            <ItemDetail
              key={`${boundary.key}:${opened.name}`}
              row={opened}
              userId={userId}
              taken={taken}
              onClose={() => setOpened(null)}
            />
          )}
        </DialogContent>
      </Dialog>

      <ConfirmDialog
        open={deleting !== null}
        onOpenChange={(o) => !o && setDeleting(null)}
        title={t('vault:delete.title', { label: deleting?.label ?? '' })}
        description={t('vault:delete.body')}
        confirmLabel={t('vault:delete.confirm')}
        tone="danger"
        pending={remove.isPending}
        onConfirm={() => deleting && remove.mutate(deleting)}
      />
    </div>
  )
}

function ItemRow({
  row,
  busy,
  onOpen,
  onCopy,
  onDelete,
}: {
  row: VaultRow
  busy: boolean
  onOpen: () => void
  onCopy: (what: Copied) => void
  onDelete: () => void
}) {
  const { t } = useTranslation('vault')
  const Icon = KIND_ICON[row.kind]
  const copyButton = (what: Copied, key: string) => (
    <Button
      variant="ghost"
      size="icon"
      disabled={busy}
      aria-label={t(key, { label: row.label })}
      title={t(key, { label: row.label })}
      onClick={() => onCopy(what)}
    >
      <Copy />
    </Button>
  )
  return (
    <li className="flex items-center gap-2 rounded-lg border border-border px-3 py-1.5">
      <button
        type="button"
        className="flex min-w-0 flex-1 items-center gap-3 rounded-md py-1 text-left outline-none focus-visible:ring-2 focus-visible:ring-ring"
        aria-label={t('row.open', { label: row.label })}
        onClick={onOpen}
      >
        <Icon className="size-4 shrink-0 text-muted-foreground" aria-hidden />
        <span className="min-w-0">
          <span className="block truncate font-mono text-body text-foreground">
            {row.label}
          </span>
          <span className="block truncate text-caption text-muted-foreground">
            {t(`kind.${row.kind}`)}
            {row.hasCode && row.kind !== 'totp' ? ` · ${t('kind.code')}` : ''}
            {row.description ? ` · ${row.description}` : ''}
            {' · '}
            <RelTimeLabel ts={row.updatedAt} />
          </span>
        </span>
      </button>
      <div className="flex shrink-0 items-center">
        {row.kind === 'login' && copyButton('password', 'row.copyPassword')}
        {row.kind === 'note' && copyButton('note', 'row.copyNote')}
        {row.hasCode && copyButton('code', 'row.copyCode')}
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button
              variant="ghost"
              size="icon"
              aria-label={t('row.menu', { label: row.label })}
            >
              <MoreHorizontal />
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end">
            <DropdownMenuItem onSelect={onOpen}>
              {t('row.openItem')}
            </DropdownMenuItem>
            <DropdownMenuItem onSelect={onDelete}>
              <Trash2 />
              {t('row.delete')}
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </div>
    </li>
  )
}
