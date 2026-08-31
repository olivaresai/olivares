// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useInfiniteQuery } from '@tanstack/react-query'
import { FolderTree, HardDrive } from 'lucide-react'
import { useMemo, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { DataTable, type TableColumn } from '@/components/data/data-table'
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
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { Spinner } from '@/components/ui/spinner'
import { useAuth } from '@/lib/auth/context'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { cn } from '@/lib/utils'
import { agentOpsApi, agentOpsKeys } from './api'
import { WorkspaceBrowser } from './workspace-browser'
import type { CreateWorkspaceRequest, WorkspaceDTO } from './types'
import './i18n'

const PAGE = 100
const DEFAULT_MAX_READ = 5 * 1024 * 1024

/** WorkspacesPanel — register host roots, browse their governed file trees, deregister. */
export function WorkspacesPanel() {
  const { t } = useTranslation('agentops')
  const { activeTenant, can } = useAuth()
  const canAdmin = can('sessions:workspace:admin')

  const [createOpen, setCreateOpen] = useState(false)
  const [browse, setBrowse] = useState<WorkspaceDTO | null>(null)
  const [confirmDel, setConfirmDel] = useState<WorkspaceDTO | null>(null)

  const query = useInfiniteQuery({
    queryKey: agentOpsKeys.workspaces(activeTenant, { limit: PAGE }),
    queryFn: ({ pageParam }) =>
      agentOpsApi.listWorkspaces({ limit: PAGE, cursor: pageParam }),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => (last.has_more ? last.cursor : undefined),
  })
  const rows = useMemo(
    () => query.data?.pages.flatMap((p) => p.items) ?? [],
    [query.data],
  )

  // Borrar NO está gateado por AAL3 hoy (handlers_scoping.go no llama `requireAAL3` en
  // `handleDeleteWorkspace`), así que esto NO arregla un fallo alcanzable: pasa a la política
  // común por consistencia con la creación de al lado y para que un 403 de ROL se cuente como
  // frontera de permiso —advertencia calmada— en vez de como error rojo.
  const del = usePrivilegedMutation<WorkspaceDTO, { deleted: boolean }>({
    mutationFn: (w) => agentOpsApi.deleteWorkspace(w.workspace_ref),
    invalidateKeys: () => [agentOpsKeys.workspaces(activeTenant)],
    successMessage: t('workspaces.deleted'),
    onDone: () => setConfirmDel(null),
  })

  const columns = useMemo<TableColumn<WorkspaceDTO>[]>(() => {
    const base: TableColumn<WorkspaceDTO>[] = [
      {
        id: 'name',
        header: t('workspaces.cols.name'),
        accessorFn: (w) => w.name || w.workspace_ref,
        cell: ({ row }) => (
          <span className="font-medium text-foreground">
            {row.original.name || row.original.workspace_ref}
          </span>
        ),
      },
      {
        accessorKey: 'root_path',
        header: t('workspaces.cols.rootPath'),
        cell: ({ getValue }) => (
          <span className="font-mono text-xs text-muted-foreground">
            {getValue<string>()}
          </span>
        ),
      },
      {
        accessorKey: 'mount_mode',
        header: t('workspaces.cols.mount'),
        cell: ({ getValue }) => {
          const m = getValue<string>()
          return (
            <Pill tone={m === 'rw' ? 'accent' : 'muted'}>
              {t(`workspaces.mount.${m}`, { defaultValue: m })}
            </Pill>
          )
        },
      },
      {
        accessorKey: 'dlp_mode',
        header: t('workspaces.cols.dlp'),
        cell: ({ getValue }) => {
          const d = getValue<string>()
          return (
            <Pill
              tone={d === 'deny' ? 'danger' : d === 'off' ? 'muted' : 'warn'}
            >
              {t(`workspaces.dlp.${d}`, { defaultValue: d })}
            </Pill>
          )
        },
      },
      {
        accessorKey: 'state',
        header: t('workspaces.cols.state'),
        cell: ({ getValue }) => {
          const s = getValue<string>()
          return (
            <Pill tone={s === 'active' ? 'success' : 'muted'}>
              {t(`workspaces.wsState.${s}`, { defaultValue: s })}
            </Pill>
          )
        },
      },
      {
        id: 'actions',
        header: '',
        enableSorting: false,
        cell: ({ row }) => (
          <div className="flex items-center justify-end gap-1">
            <Button
              variant="ghost"
              size="sm"
              onClick={() => setBrowse(row.original)}
            >
              <FolderTree className="size-3.5" />
              {t('workspaces.open')}
            </Button>
            {canAdmin && (
              <Button
                variant="destructive"
                size="sm"
                onClick={() => setConfirmDel(row.original)}
              >
                {t('workspaces.deleteConfirm')}
              </Button>
            )}
          </div>
        ),
      },
    ]
    return base
  }, [t, canAdmin])

  return (
    <div className="flex flex-col gap-3">
      <div className="flex items-center justify-between">
        <p className="text-sm text-muted-foreground">
          {t('workspaces.subtitle')}
        </p>
        {canAdmin && (
          <Button
            variant="primary"
            size="sm"
            onClick={() => setCreateOpen(true)}
          >
            <HardDrive className="size-3.5" />
            {t('workspaces.register')}
          </Button>
        )}
      </div>

      <DataTable
        columns={columns}
        data={rows}
        isLoading={query.isLoading}
        error={query.error}
        onRetry={() => void query.refetch()}
        getRowId={(w) => w.workspace_ref}
        searchable
        searchPlaceholder={t('search')}
        stickyHeader
        hasMore={!!query.hasNextPage}
        onLoadMore={() => void query.fetchNextPage()}
        isFetchingMore={query.isFetchingNextPage}
        label={t('workspaces.title')}
        empty={
          <EmptyState
            icon={<HardDrive />}
            title={t('workspaces.empty.title')}
            description={t('workspaces.empty.description')}
          />
        }
      />

      {canAdmin && (
        <WorkspaceCreateDialog open={createOpen} onOpenChange={setCreateOpen} />
      )}

      <ConfirmDialog
        open={confirmDel !== null}
        onOpenChange={(o) => !o && setConfirmDel(null)}
        tone="danger"
        title={t('workspaces.deleteTitle')}
        description={t('workspaces.deleteBody')}
        confirmLabel={t('workspaces.deleteConfirm')}
        pending={del.isPending}
        onConfirm={() => confirmDel && del.mutate(confirmDel)}
      />

      <Sheet open={browse !== null} onOpenChange={(o) => !o && setBrowse(null)}>
        <SheetContent className="flex w-full flex-col gap-3 overflow-y-auto sm:max-w-5xl">
          {browse && (
            <>
              <SheetHeader>
                <SheetTitle className="flex items-center gap-2">
                  <FolderTree className="size-4 text-accent-text" />
                  <span className="truncate">
                    {browse.name || browse.workspace_ref}
                  </span>
                </SheetTitle>
                <SheetDescription className="font-mono text-xs">
                  {browse.root_path}
                </SheetDescription>
              </SheetHeader>
              <WorkspaceBrowser workspace={browse} />
            </>
          )}
        </SheetContent>
      </Sheet>
    </div>
  )
}

function Pill({
  tone,
  children,
}: {
  tone: 'accent' | 'success' | 'warn' | 'danger' | 'muted'
  children: ReactNode
}) {
  const cls = {
    accent: 'border-accent-line bg-accent-soft text-accent-text',
    success: 'border-success-line bg-success-soft text-success',
    warn: 'border-warning-line bg-warning-soft text-warning',
    danger: 'border-danger-line bg-danger-soft text-danger',
    muted: 'border-border bg-muted text-muted-foreground',
  }[tone]
  return (
    <span
      className={cn(
        'inline-flex items-center rounded-sm border px-1.5 py-0.5 text-[11px] font-medium',
        cls,
      )}
    >
      {children}
    </span>
  )
}

function WorkspaceCreateDialog({
  open,
  onOpenChange,
}: {
  open: boolean
  onOpenChange: (o: boolean) => void
}) {
  const { t } = useTranslation('agentops')
  const { activeTenant } = useAuth()

  const [name, setName] = useState('')
  const [rootPath, setRootPath] = useState('')
  const [mountMode, setMountMode] = useState<'rw' | 'ro'>('rw')
  const [containerTarget, setContainerTarget] = useState('')
  const [allowSubpaths, setAllowSubpaths] = useState('')
  const [maxRead, setMaxRead] = useState(String(DEFAULT_MAX_READ))
  const [dlpMode, setDlpMode] = useState<'label' | 'deny' | 'off'>('label')

  const reset = () => {
    setName('')
    setRootPath('')
    setMountMode('rw')
    setContainerTarget('')
    setAllowSubpaths('')
    setMaxRead(String(DEFAULT_MAX_READ))
    setDlpMode('label')
  }

  // ⛔ AQUÍ SÍ HABÍA UN FALLO ALCANZABLE HOY, y trazado de punta a punta:
  //   workspaces-panel → agentOpsApi.createWorkspace → POST /workspaces →
  //   handleCreateWorkspace (core/api/handlers_scoping.go:143) → s.requireAAL3(...) (:151).
  // Con la sesión por debajo de AAL3 el motor contesta 403 con `code: step_up_required`, y este
  // `onError` lo pintaba como `toast.error(err.message)`: un error ROJO diciendo «assurance
  // level too low», sin ceremonia y sin salida. Ni este componente ni sus dos ancestros
  // (agentops/index.tsx, an internal design note (not shipped)) envuelven nada en
  // `RequireAssurance`, así que no había otra red debajo.
  //
  // `usePrivilegedMutation` es la política común: abre la ceremonia con el código, reanuda la
  // llamada al elevar (con presupuesto de UN reintento) y deja el 403 de ROL como advertencia
  // calmada en vez de error rojo.
  const create = usePrivilegedMutation<void, WorkspaceDTO>({
    mutationFn: () => {
      const body: CreateWorkspaceRequest = {
        name: name.trim(),
        root_path: rootPath.trim(),
        mount_mode: mountMode,
        container_target: containerTarget.trim(),
        allow_subpaths: allowSubpaths
          .split(',')
          .map((s) => s.trim())
          .filter(Boolean),
        max_read_bytes: Number(maxRead) || DEFAULT_MAX_READ,
        dlp_mode: dlpMode,
      }
      return agentOpsApi.createWorkspace(body)
    },
    invalidateKeys: () => [agentOpsKeys.workspaces(activeTenant)],
    successMessage: t('workspaces.create.success'),
    onDone: () => {
      reset()
      onOpenChange(false)
    },
  })

  return (
    <Dialog
      open={open}
      onOpenChange={(o) => (create.isPending ? undefined : onOpenChange(o))}
    >
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>{t('workspaces.create.title')}</DialogTitle>
          <DialogDescription>
            {t('workspaces.create.description')}
          </DialogDescription>
        </DialogHeader>
        <form
          onSubmit={(e) => {
            e.preventDefault()
            if (!create.isPending) create.mutate()
          }}
          className="flex flex-col gap-3"
        >
          <Field label={t('workspaces.create.name')}>
            <Input
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder={t('workspaces.create.namePlaceholder')}
            />
          </Field>
          <Field
            label={t('workspaces.create.rootPath')}
            description={t('workspaces.create.rootPathHint')}
          >
            <Input
              value={rootPath}
              onChange={(e) => setRootPath(e.target.value)}
              placeholder={t('workspaces.create.rootPathPlaceholder')}
              mono
              required
            />
          </Field>
          <div className="grid grid-cols-2 gap-3">
            <Field label={t('workspaces.create.mountMode')}>
              <Select
                value={mountMode}
                onValueChange={(v) => setMountMode(v as 'rw' | 'ro')}
              >
                <SelectTrigger aria-label={t('workspaces.create.mountMode')}>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="rw">{t('workspaces.mount.rw')}</SelectItem>
                  <SelectItem value="ro">{t('workspaces.mount.ro')}</SelectItem>
                </SelectContent>
              </Select>
            </Field>
            <Field label={t('workspaces.create.dlpMode')}>
              <Select
                value={dlpMode}
                onValueChange={(v) => setDlpMode(v as 'label' | 'deny' | 'off')}
              >
                <SelectTrigger aria-label={t('workspaces.create.dlpMode')}>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="label">
                    {t('workspaces.dlp.label')}
                  </SelectItem>
                  <SelectItem value="deny">
                    {t('workspaces.dlp.deny')}
                  </SelectItem>
                  <SelectItem value="off">{t('workspaces.dlp.off')}</SelectItem>
                </SelectContent>
              </Select>
            </Field>
          </div>
          <div className="grid grid-cols-2 gap-3">
            <Field label={t('workspaces.create.containerTarget')}>
              <Input
                value={containerTarget}
                onChange={(e) => setContainerTarget(e.target.value)}
                placeholder={t('workspaces.create.containerTargetPlaceholder')}
                mono
              />
            </Field>
            <Field label={t('workspaces.create.maxReadBytes')}>
              <Input
                type="number"
                value={maxRead}
                onChange={(e) => setMaxRead(e.target.value)}
                mono
              />
            </Field>
          </div>
          <Field
            label={t('workspaces.create.allowSubpaths')}
            description={t('workspaces.create.allowSubpathsHint')}
          >
            <Input
              value={allowSubpaths}
              onChange={(e) => setAllowSubpaths(e.target.value)}
              placeholder={t('workspaces.create.allowSubpathsPlaceholder')}
              mono
            />
          </Field>
          <DialogFooter>
            <Button
              type="button"
              variant="secondary"
              onClick={() => onOpenChange(false)}
              disabled={create.isPending}
            >
              {t('browser.cancel')}
            </Button>
            <Button
              type="submit"
              variant="primary"
              disabled={create.isPending || !rootPath.trim()}
            >
              {create.isPending && <Spinner className="size-3.5" />}
              {create.isPending
                ? t('workspaces.create.submitting')
                : t('workspaces.create.submit')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
