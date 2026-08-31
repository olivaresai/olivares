// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// alerting / notify administration. The engine routes findings to destinations
// and records deliveries, but the console never exposed it. This surfaces route CRUD
// (create/edit editor-tier, delete/test admin-tier), a live route probe, the
// provisioned destinations (name-only — the secret lives in the transport), and the
// read-only delivery log.
//
// Adds the third tab: the durable OUTBOX, whose ?status=dead view is the
// dead-letter queue, and the per-row requeue. This file used to state here that "there
// is no per-delivery redeliver in notify's API, so the log is honestly read-only" —
// FALSE since mounted POST /outbox/{id}/redeliver (modules/notify/api.go:47).
// The sentence was not merely stale: modules/notify/outbox.go dead-letters a row so it
// "surfaces in the DLQ for the operator", and there was no operator who could see it.
// What survives of the old sentence is the part that is still true and is a DIFFERENT
// object: the delivery LEDGER (GET /deliveries) is append-only and has no retry; the
// outbox is the state machine, and that is what is requeued.
import './i18n'
import { useEffect, useMemo, useState } from 'react'
import {
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
} from '@tanstack/react-query'
import {
  CircleCheck,
  CircleOff,
  History,
  Pencil,
  Plus,
  RefreshCw,
  Send,
  Siren,
  Trash2,
} from 'lucide-react'
import { useTranslation } from 'react-i18next'
import {
  BulkActionBar,
  type BulkAction,
} from '@/components/data/bulk-action-bar'
import { DataTable, type TableColumn } from '@/components/data/data-table'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
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
import { ErrorState, ForbiddenState } from '@/components/ui/error-state'
import { Field } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { PageHeader } from '@/components/ui/page-header'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Spinner } from '@/components/ui/spinner'
import { Switch } from '@/components/ui/switch'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { toast } from '@/components/ui/toaster'
import { useAuth } from '@/lib/auth/context'
import { useCommandStore } from '@/stores/command'
import { ApiError } from '@/lib/api/errors'
import {
  useFailedActionReporter,
  usePrivilegedMutation,
} from '@/lib/hooks/use-privileged-mutation'
import { RecordingNotice } from '@/features/recordings/recording-notice'
import { RevisionsSheet } from '@/features/shared/revisions-sheet'
import {
  isRedeliverable,
  notifyApi,
  notifyKeys,
  OUTBOX_STATUSES,
  type NotifyOutboxEntry,
  type NotifyRedeliverResult,
  type NotifyRoute,
  type NotifyEvaluateResult,
  type NotifySeverity,
} from './api'

const SEVERITIES: NotifySeverity[] = [
  '',
  'info',
  'low',
  'medium',
  'high',
  'critical',
]
const DELIVERY_STATUSES = [
  'claimed',
  'delivered',
  // 'rejected' is the destination having READ the payload and refused it, which is
  // distinct from 'failed' (we could not reach it): the first will never succeed on
  // a retry and the second usually will. Omitting it here hid a whole class of
  // delivery from the operator's filter.
  'rejected',
  'failed',
  'no_dispatcher',
  'unknown_destination',
]

const toList = (s: string) =>
  s
    .split(',')
    .map((x) => x.trim())
    .filter(Boolean)
const fromList = (a?: string[]) => (a ?? []).join(', ')

export function AlertingView() {
  const { t } = useTranslation(['alerting', 'common'])
  const { can } = useAuth()
  if (!can('notify:route:read')) return <ForbiddenState />
  return (
    <div className="flex flex-col gap-6 p-6">
      <PageHeader
        icon={Siren}
        title={t('title')}
        description={t('description')}
      />
      {/* AC-8: notify's routes and the outbox requeue are privileged, audited surfaces,
          and a tenant may add this namespace to its recorded set. Without this the engine
          answers 403 `recording_consent_required` and the console had no way for the
          operator to answer it — it told them to obtain a permission they already held. */}
      <RecordingNotice namespace="notify" />
      <Tabs defaultValue="routes">
        <TabsList>
          <TabsTrigger value="routes">{t('tabs.routes')}</TabsTrigger>
          <TabsTrigger value="deliveries">{t('tabs.deliveries')}</TabsTrigger>
          <TabsTrigger value="outbox">{t('tabs.outbox')}</TabsTrigger>
        </TabsList>
        <TabsContent value="routes" className="pt-4">
          <RoutesTab />
        </TabsContent>
        <TabsContent value="deliveries" className="pt-4">
          <DeliveriesTab />
        </TabsContent>
        <TabsContent value="outbox" className="pt-4">
          <DeadLettersTab />
        </TabsContent>
      </Tabs>
    </div>
  )
}

// --- routes ------------------------------------------------------------------

function RoutesTab() {
  const report = useFailedActionReporter()
  const { t } = useTranslation(['alerting', 'common'])
  const { can, activeTenant } = useAuth()
  const canWrite = can('notify:route:write')
  const canAdmin = can('notify:route:admin')
  const qc = useQueryClient()

  const [editing, setEditing] = useState<NotifyRoute | null>(null)
  const [creating, setCreating] = useState(false)
  // ⌘K palette action: "new alert route" navigated here — consume once.
  useEffect(() => {
    if (
      useCommandStore.getState().consumeAction('alerting') === 'createRoute'
    ) {
      setCreating(true)
    }
  }, [])
  const [deleting, setDeleting] = useState<NotifyRoute | null>(null)
  const [historyRoute, setHistoryRoute] = useState<NotifyRoute | null>(null)
  const [selectedIds, setSelectedIds] = useState<Set<string>>(new Set())

  const routesQ = useQuery({
    queryKey: notifyKeys.routes(activeTenant),
    queryFn: () => notifyApi.listRoutes(),
  })

  const del = useMutation({
    mutationFn: (id: string) => notifyApi.deleteRoute(id),
    onSuccess: () => {
      toast.success(t('routes.deleted'))
      void qc.invalidateQueries({ queryKey: notifyKeys.routes(activeTenant) })
      setDeleting(null)
    },
    onError: (e: unknown) => {
      // La LIMPIEZA se queda aquí y el REPORTE se delega: `report` distingue el 403 de
      // ceremonia del de rol, que es lo que este `onError` colapsaba. `isForbidden` es
      // cierto para los DOS (lib/api/errors.ts:59), así que el cierre del diálogo cubre
      // igual las dos negativas, como antes.
      if (e instanceof ApiError && e.isForbidden) setDeleting(null)
      report(e)
    },
  })

  const test = useMutation({
    mutationFn: (id: string) => notifyApi.testRoute(id),
    onSuccess: (res) => {
      // A route test that the destination REFUSED is not a success, even though the
      // request itself completed. Reporting it with a success toast told an operator
      // their route worked while the destination had rejected the payload.
      if (res.status !== 'delivered') {
        toast.error(t('routes.tested', { destination: res.destination }), {
          description: res.detail || res.status,
        })
        return
      }
      toast.success(t('routes.tested', { destination: res.destination }), {
        description: res.detail || res.status,
      })
    },
    onError: (e: unknown) => {
      report(e)
    },
  })

  const routes = routesQ.data?.items ?? []
  const setEnabled = async (id: string, enabled: boolean) => {
    // Notify exposes an audited full PUT. Fetch the latest route first so bulk
    // enable/disable preserves predicates and delivery-window configuration.
    const current = await notifyApi.getRoute(id)
    await notifyApi.updateRoute(id, routeWithEnabled(current, enabled))
    await qc.invalidateQueries({ queryKey: notifyKeys.routes(activeTenant) })
  }
  const bulkActions: BulkAction[] = [
    {
      id: 'enable',
      label: t('routes.bulk.enable'),
      icon: CircleCheck,
      run: (id) => setEnabled(id, true),
    },
    {
      id: 'disable',
      label: t('routes.bulk.disable'),
      icon: CircleOff,
      run: (id) => setEnabled(id, false),
    },
  ]
  const columns: TableColumn<NotifyRoute>[] = [
    {
      accessorKey: 'name',
      header: t('routes.colName'),
      cell: ({ row }) => (
        <span className="font-medium text-foreground">{row.original.name}</span>
      ),
    },
    {
      accessorKey: 'destination',
      header: t('routes.colDest'),
      cell: ({ row }) => (
        <span className="font-mono text-muted-foreground">
          {row.original.destination}
        </span>
      ),
    },
    {
      accessorKey: 'min_severity',
      header: t('routes.colSeverity'),
      cell: ({ row }) =>
        row.original.min_severity
          ? t(`severities.${row.original.min_severity}`, {
              defaultValue: row.original.min_severity,
            })
          : t('severities.any'),
    },
    {
      accessorKey: 'enabled',
      header: t('routes.colStatus'),
      cell: ({ row }) =>
        row.original.enabled === false ? (
          <Badge variant="neutral">{t('routes.disabled')}</Badge>
        ) : (
          <Badge variant="success">{t('routes.enabled')}</Badge>
        ),
    },
    {
      id: 'actions',
      header: () => <span className="sr-only">{t('routes.colActions')}</span>,
      enableSorting: false,
      cell: ({ row }) => {
        const route = row.original
        return (
          <div className="flex justify-end gap-1">
            {route.id ? (
              <Button
                variant="ghost"
                size="icon-sm"
                aria-label={t('history.action', { name: route.name })}
                onClick={() => setHistoryRoute(route)}
              >
                <History />
              </Button>
            ) : null}
            {canAdmin && route.id ? (
              <Button
                variant="ghost"
                size="icon-sm"
                aria-label={t('routes.testAction', { name: route.name })}
                disabled={test.isPending}
                onClick={() => test.mutate(route.id!)}
              >
                <Send />
              </Button>
            ) : null}
            {canWrite ? (
              <Button
                variant="ghost"
                size="icon-sm"
                aria-label={t('routes.editAction', { name: route.name })}
                onClick={() => setEditing(route)}
              >
                <Pencil />
              </Button>
            ) : null}
            {canAdmin ? (
              <Button
                variant="ghost"
                size="icon-sm"
                aria-label={t('routes.deleteAction', { name: route.name })}
                onClick={() => setDeleting(route)}
              >
                <Trash2 />
              </Button>
            ) : null}
          </div>
        )
      },
    },
  ]

  return (
    <div className="flex flex-col gap-4">
      <div className="flex items-center justify-between gap-2">
        <p className="text-sm text-muted-foreground">{t('routes.intro')}</p>
        {canWrite ? (
          <Button variant="primary" size="sm" onClick={() => setCreating(true)}>
            <Plus />
            {t('routes.add')}
          </Button>
        ) : null}
      </div>
      {canWrite ? (
        <BulkActionBar
          selectedIds={[...selectedIds]}
          onClear={() => setSelectedIds(new Set())}
          actions={bulkActions}
        />
      ) : null}
      <DataTable
        columns={columns}
        data={routes}
        isLoading={routesQ.isLoading}
        error={routesQ.error}
        onRetry={() => void routesQ.refetch()}
        empty={
          <EmptyState
            title={t('routes.empty')}
            description={t('routes.emptyHint')}
          />
        }
        getRowId={(route) => route.id ?? route.name}
        selectable={canWrite}
        selectedIds={selectedIds}
        onSelectedIdsChange={setSelectedIds}
        label={t('tabs.routes')}
      />

      <TestSignalPanel />

      {creating ? <RouteDialog onClose={() => setCreating(false)} /> : null}
      {editing ? (
        <RouteDialog
          key={editing.id ?? editing.name}
          route={editing}
          onClose={() => setEditing(null)}
        />
      ) : null}
      <Dialog
        open={deleting != null}
        onOpenChange={(v) => {
          if (!v) setDeleting(null)
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t('routes.deleteDialog.title')}</DialogTitle>
            <DialogDescription>
              {t('routes.deleteDialog.description', {
                name: deleting?.name ?? '',
              })}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button
              variant="secondary"
              onClick={() => setDeleting(null)}
              disabled={del.isPending}
            >
              {t('common:actions.cancel')}
            </Button>
            <Button
              variant="destructive-solid"
              onClick={() => deleting?.id && del.mutate(deleting.id)}
              disabled={del.isPending}
            >
              {t('routes.deleteDialog.confirm')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
      {historyRoute?.id ? (
        <RevisionsSheet
          open
          onOpenChange={(open) => {
            if (!open) setHistoryRoute(null)
          }}
          queryKey={notifyKeys.routeRevisions(activeTenant, historyRoute.id)}
          listRevisions={(params) =>
            notifyApi.routeRevisions(historyRoute.id!, params)
          }
          restoreRevision={(revisionId) =>
            notifyApi.restoreRoute(historyRoute.id!, revisionId)
          }
          invalidateKeys={[
            notifyKeys.routes(activeTenant),
            notifyKeys.route(activeTenant, historyRoute.id),
          ]}
          canWrite={canWrite}
          labels={{
            title: t('history.title', { name: historyRoute.name }),
            description: t('history.description'),
            empty: t('history.empty'),
            loading: t('common:states.loading'),
            loadMore: t('history.loadMore'),
            compareTitle: t('history.compareTitle'),
            selectTwo: t('history.selectTwo'),
            selectRevision: (operation, actor) =>
              t('history.selectRevision', { op: operation, actor }),
            originalLabel: t('history.originalLabel'),
            modifiedLabel: t('history.modifiedLabel'),
            restore: t('history.restore'),
            restoreTitle: t('history.restoreTitle'),
            restoreDescription: t('history.restoreDescription', {
              name: historyRoute.name,
            }),
            restoreConfirm: t('history.restoreConfirm'),
            restoreSuccess: t('history.restored'),
            operations: {
              create: t('history.operations.create'),
              update: t('history.operations.update'),
              delete: t('history.operations.delete'),
              restore: t('history.operations.restore'),
            },
          }}
        />
      ) : null}
    </div>
  )
}

function routeWithEnabled(route: NotifyRoute, enabled: boolean): NotifyRoute {
  return {
    name: route.name,
    destination: route.destination,
    enabled,
    match_types: route.match_types,
    match_kinds: route.match_kinds,
    min_severity: route.min_severity,
    match_sources: route.match_sources,
    match_subject_kinds: route.match_subject_kinds,
    dedup_window_seconds: route.dedup_window_seconds,
    throttle_window_seconds: route.throttle_window_seconds,
    priority: route.priority,
  }
}

function RouteDialog({
  route,
  onClose,
}: {
  route?: NotifyRoute
  onClose: () => void
}) {
  const { t } = useTranslation(['alerting', 'common'])
  const { activeTenant } = useAuth()
  const isEdit = route != null
  const [name, setName] = useState(route?.name ?? '')
  const [destination, setDestination] = useState(route?.destination ?? '')
  const [enabled, setEnabled] = useState(route?.enabled ?? true)
  const [minSeverity, setMinSeverity] = useState<string>(
    route?.min_severity ?? '',
  )
  const [matchTypes, setMatchTypes] = useState<string[]>(
    route?.match_types ?? [],
  )
  const [matchKinds, setMatchKinds] = useState(fromList(route?.match_kinds))
  const [matchSources, setMatchSources] = useState(
    fromList(route?.match_sources),
  )
  const [matchSubjectKinds, setMatchSubjectKinds] = useState(
    fromList(route?.match_subject_kinds),
  )
  const [dedup, setDedup] = useState(String(route?.dedup_window_seconds ?? 0))
  const [throttle, setThrottle] = useState(
    String(route?.throttle_window_seconds ?? 0),
  )
  const [priority, setPriority] = useState(String(route?.priority ?? 0))

  const destinationsQ = useQuery({
    queryKey: notifyKeys.destinations(activeTenant),
    queryFn: () => notifyApi.listDestinations(),
  })
  const matchTypesQ = useQuery({
    queryKey: notifyKeys.matchTypes(activeTenant),
    queryFn: () => notifyApi.listMatchTypes(),
  })
  const destinations = destinationsQ.data?.destinations ?? []
  // Keep the route's current destination selectable even if it is no longer
  // provisioned, so an edit never silently drops it.
  const destOptions =
    destination && !destinations.includes(destination)
      ? [destination, ...destinations]
      : destinations

  const save = usePrivilegedMutation({
    mutationFn: () => {
      const body: NotifyRoute = {
        name: name.trim(),
        destination,
        enabled,
        min_severity: minSeverity || undefined,
        match_types: matchTypes,
        match_kinds: toList(matchKinds),
        match_sources: toList(matchSources),
        match_subject_kinds: toList(matchSubjectKinds),
        dedup_window_seconds: Number(dedup) || 0,
        throttle_window_seconds: Number(throttle) || 0,
        priority: Number(priority) || 0,
      }
      return isEdit
        ? notifyApi.updateRoute(route.id!, body)
        : notifyApi.createRoute(body)
    },
    invalidateKeys: [notifyKeys.routes(activeTenant)],
    successMessage: isEdit ? t('routes.updated') : t('routes.created'),
    onDone: onClose,
  })

  const valid = name.trim().length > 0 && destination.length > 0

  return (
    <Dialog
      open
      onOpenChange={(o) => {
        if (!o && !save.isPending) onClose()
      }}
    >
      {/* The route form is taller than a 720 px laptop viewport once the live
          match-type catalog is present. Bound the surface and let the dialog
          scroll; otherwise its footer is painted below the viewport and neither
          Cancel nor Save can be reached with a pointer. */}
      <DialogContent className="max-h-[calc(100vh-2rem)] max-w-lg overflow-y-auto">
        <DialogHeader>
          <DialogTitle>
            {isEdit ? t('routes.dialog.editTitle') : t('routes.dialog.title')}
          </DialogTitle>
          <DialogDescription>{t('routes.dialog.subtitle')}</DialogDescription>
        </DialogHeader>
        <form
          className="flex flex-col gap-3"
          onSubmit={(e) => {
            e.preventDefault()
            if (valid) save.mutate()
          }}
        >
          <Field label={t('routes.dialog.name')} required>
            {({ id }) => (
              <Input
                id={id}
                value={name}
                disabled={isEdit}
                onChange={(e) => setName(e.target.value)}
              />
            )}
          </Field>
          <div className="grid grid-cols-2 gap-3">
            <Field
              label={t('routes.dialog.destination')}
              description={t('routes.dialog.destinationHint')}
              required
            >
              <Select value={destination} onValueChange={setDestination}>
                <SelectTrigger aria-label={t('routes.dialog.destination')}>
                  <SelectValue
                    placeholder={t('routes.dialog.destinationPlaceholder')}
                  />
                </SelectTrigger>
                <SelectContent>
                  {destOptions.map((d) => (
                    <SelectItem key={d} value={d}>
                      {d}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>
            <Field label={t('routes.dialog.minSeverity')}>
              <Select
                value={minSeverity || 'any'}
                onValueChange={(v) => setMinSeverity(v === 'any' ? '' : v)}
              >
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {SEVERITIES.map((s) => (
                    <SelectItem key={s || 'any'} value={s || 'any'}>
                      {s ? t(`severities.${s}`) : t('severities.any')}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>
          </div>
          <Field
            label={t('routes.dialog.matchTypes')}
            description={t('routes.dialog.matchTypesHint')}
          >
            <div className="flex max-h-40 flex-col gap-2 overflow-y-auto rounded-md border border-border p-2">
              {matchTypesQ.data?.match_types.map((item) => (
                <label
                  key={item.type}
                  className="flex items-start gap-2 text-sm"
                >
                  <Checkbox
                    checked={matchTypes.includes(item.type)}
                    onCheckedChange={(checked) =>
                      setMatchTypes((current) =>
                        checked
                          ? [...current, item.type]
                          : current.filter((value) => value !== item.type),
                      )
                    }
                  />
                  <span>
                    <span className="block font-mono text-xs">{item.type}</span>
                    <span className="block text-xs text-muted-foreground">
                      {item.description}
                    </span>
                  </span>
                </label>
              ))}
            </div>
          </Field>
          <div className="grid grid-cols-2 gap-3">
            <Field
              label={t('routes.dialog.matchKinds')}
              description={t('routes.dialog.listHint')}
            >
              {({ id }) => (
                <Input
                  id={id}
                  value={matchKinds}
                  onChange={(e) => setMatchKinds(e.target.value)}
                />
              )}
            </Field>
            <Field
              label={t('routes.dialog.matchSources')}
              description={t('routes.dialog.listHint')}
            >
              {({ id }) => (
                <Input
                  id={id}
                  value={matchSources}
                  onChange={(e) => setMatchSources(e.target.value)}
                />
              )}
            </Field>
          </div>
          <Field
            label={t('routes.dialog.matchSubjectKinds')}
            description={t('routes.dialog.listHint')}
          >
            {({ id }) => (
              <Input
                id={id}
                value={matchSubjectKinds}
                onChange={(e) => setMatchSubjectKinds(e.target.value)}
              />
            )}
          </Field>
          <div className="grid grid-cols-3 gap-3">
            <Field label={t('routes.dialog.dedup')}>
              {({ id }) => (
                <Input
                  id={id}
                  type="number"
                  min="0"
                  value={dedup}
                  onChange={(e) => setDedup(e.target.value)}
                />
              )}
            </Field>
            <Field label={t('routes.dialog.throttle')}>
              {({ id }) => (
                <Input
                  id={id}
                  type="number"
                  min="0"
                  value={throttle}
                  onChange={(e) => setThrottle(e.target.value)}
                />
              )}
            </Field>
            <Field label={t('routes.dialog.priority')}>
              {({ id }) => (
                <Input
                  id={id}
                  type="number"
                  value={priority}
                  onChange={(e) => setPriority(e.target.value)}
                />
              )}
            </Field>
          </div>
          <div className="flex items-center justify-between gap-4 rounded-md border p-3">
            <span className="min-w-0">
              <span className="text-sm font-medium text-foreground">
                {t('routes.dialog.enabled')}
              </span>
              <span className="block text-xs text-muted-foreground">
                {t('routes.dialog.enabledHint')}
              </span>
            </span>
            <Switch
              checked={enabled}
              onCheckedChange={setEnabled}
              aria-label={t('routes.dialog.enabled')}
            />
          </div>
          <DialogFooter>
            <Button type="button" variant="ghost" onClick={onClose}>
              {t('common:actions.cancel')}
            </Button>
            <Button
              type="submit"
              variant="primary"
              disabled={!valid || save.isPending}
            >
              {save.isPending && <Spinner size="sm" aria-hidden />}
              {isEdit ? t('routes.dialog.save') : t('routes.dialog.create')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

function TestSignalPanel() {
  const { t } = useTranslation(['alerting', 'common'])
  const { activeTenant } = useAuth()
  const [eventType, setEventType] = useState('')
  const [kind, setKind] = useState('')
  const [severity, setSeverity] = useState<Exclude<NotifySeverity, ''>>('info')
  const [source, setSource] = useState('')
  const [subjectKind, setSubjectKind] = useState('')
  const [result, setResult] = useState<NotifyEvaluateResult | null>(null)
  const matchTypesQ = useQuery({
    queryKey: notifyKeys.matchTypes(activeTenant),
    queryFn: () => notifyApi.listMatchTypes(),
  })
  const evaluate = useMutation({
    mutationFn: () =>
      notifyApi.evaluateRoutes({
        event_type: eventType,
        kind,
        severity,
        source,
        subject_kind: subjectKind,
      }),
    onSuccess: setResult,
    onError: (e: unknown) => toast.error(String((e as Error).message ?? e)),
  })

  return (
    <section className="rounded-lg border border-border p-4">
      {/* ⛔ h2, NO h3. Es el ÚNICO subtítulo con etiqueta de encabezado de toda la página —el
          informe del arnés recoge exactamente dos: el h1 «Alerting» y éste—, así que el nivel 3
          producía un salto 1→3. Y llevaba sin detectarse porque /alerting caía al error boundary,
          que puntúa h1=1 skips=0 igual que una vista limpia. El peso visual lo da `font-medium`,
          que no se toca. */}
      <h2 className="font-medium text-foreground">{t('evaluate.title')}</h2>
      <p className="mt-1 text-sm text-muted-foreground">
        {t('evaluate.caption')}
      </p>
      <form
        className="mt-4 grid grid-cols-2 gap-3 lg:grid-cols-5"
        onSubmit={(e) => {
          e.preventDefault()
          if (eventType) evaluate.mutate()
        }}
      >
        <Field label={t('evaluate.eventType')} required>
          <Select value={eventType} onValueChange={setEventType}>
            <SelectTrigger>
              <SelectValue placeholder={t('evaluate.selectType')} />
            </SelectTrigger>
            <SelectContent>
              {matchTypesQ.data?.match_types.map((item) => (
                <SelectItem key={item.type} value={item.type}>
                  {item.type}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>
        <Field label={t('evaluate.kind')}>
          {({ id }) => (
            <Input
              id={id}
              value={kind}
              onChange={(e) => setKind(e.target.value)}
            />
          )}
        </Field>
        <Field label={t('evaluate.severity')}>
          <Select
            value={severity}
            onValueChange={(v) => setSeverity(v as Exclude<NotifySeverity, ''>)}
          >
            <SelectTrigger>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {SEVERITIES.filter(Boolean).map((value) => (
                <SelectItem key={value} value={value}>
                  {t(`severities.${value}`)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>
        <Field label={t('evaluate.source')}>
          {({ id }) => (
            <Input
              id={id}
              value={source}
              onChange={(e) => setSource(e.target.value)}
            />
          )}
        </Field>
        <Field label={t('evaluate.subjectKind')}>
          {({ id }) => (
            <Input
              id={id}
              value={subjectKind}
              onChange={(e) => setSubjectKind(e.target.value)}
            />
          )}
        </Field>
        <Button
          type="submit"
          variant="primary"
          disabled={!eventType || evaluate.isPending}
        >
          {t('evaluate.submit')}
        </Button>
      </form>
      {result ? (
        <div className="mt-4">
          <p className="text-sm font-medium">
            {t('evaluate.matchedCount', { count: result.matched_count })}
          </p>
          <table className="mt-2 w-full text-sm">
            <thead>
              <tr className="border-b text-left">
                <th>{t('evaluate.name')}</th>
                <th>{t('evaluate.enabled')}</th>
                <th>{t('evaluate.matched')}</th>
                <th>{t('evaluate.mismatches')}</th>
              </tr>
            </thead>
            <tbody>
              {result.items.map((verdict) => (
                <tr key={verdict.id} className="border-b last:border-0">
                  <td className="py-2">{verdict.name}</td>
                  <td>
                    <Badge variant={verdict.enabled ? 'success' : 'neutral'}>
                      {t(
                        verdict.enabled ? 'routes.enabled' : 'routes.disabled',
                      )}
                    </Badge>
                  </td>
                  <td>
                    <Badge variant={verdict.matched ? 'success' : 'neutral'}>
                      {t(verdict.matched ? 'evaluate.yes' : 'evaluate.no')}
                    </Badge>
                  </td>
                  <td className="flex flex-wrap gap-1 py-2">
                    {verdict.mismatches.map((mismatch) => (
                      <Badge key={mismatch} variant="neutral">
                        {t(`evaluate.dimensions.${mismatch}`)}
                      </Badge>
                    ))}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : null}
    </section>
  )
}

// --- deliveries --------------------------------------------------------------

function DeliveriesTab() {
  const { t } = useTranslation(['alerting', 'common'])
  const { can } = useAuth()
  const [status, setStatus] = useState<string>('all')

  if (!can('notify:delivery:read')) {
    return <EmptyState title={t('deliveries.noRead')} />
  }

  const params = status === 'all' ? undefined : { status }

  return (
    <DeliveriesList status={status} setStatus={setStatus} params={params} />
  )
}

function DeliveriesList({
  status,
  setStatus,
  params,
}: {
  status: string
  setStatus: (v: string) => void
  params: { status: string } | undefined
}) {
  const { t } = useTranslation(['alerting', 'common'])
  const { activeTenant } = useAuth()
  // Cursor pagination over the append-only delivery ledger (notify listQuery):
  // each page carries the cursor for the next; the status filter is part of the
  // key so changing it restarts the walk.
  const deliveriesQ = useInfiniteQuery({
    queryKey: notifyKeys.deliveries(activeTenant, params ?? null),
    queryFn: ({ pageParam }) =>
      notifyApi.listDeliveries(
        pageParam ? { ...(params ?? {}), cursor: pageParam } : params,
      ),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => (last.has_more ? last.cursor : undefined),
  })
  const items = useMemo(
    () => deliveriesQ.data?.pages.flatMap((p) => p.items) ?? [],
    [deliveriesQ.data],
  )

  return (
    <div className="flex flex-col gap-4">
      <div className="flex items-center justify-between gap-2">
        <p className="text-sm text-muted-foreground">{t('deliveries.intro')}</p>
        <div className="w-48">
          <Select value={status} onValueChange={setStatus}>
            <SelectTrigger aria-label={t('deliveries.filterStatus')}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">{t('deliveries.allStatuses')}</SelectItem>
              {DELIVERY_STATUSES.map((s) => (
                <SelectItem key={s} value={s}>
                  {t(`deliveries.statuses.${s}`, { defaultValue: s })}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
      </div>
      {deliveriesQ.isLoading ? (
        <div role="status" className="flex justify-center py-8">
          <span className="sr-only">{t('common:states.loading')}</span>
          <Spinner />
        </div>
      ) : deliveriesQ.isError ? (
        <ErrorState retry={() => void deliveriesQ.refetch()} />
      ) : items.length === 0 ? (
        <EmptyState title={t('deliveries.empty')} />
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b text-left text-xs uppercase tracking-wider text-muted-foreground">
                <th className="py-2 pr-4 font-medium">
                  {t('deliveries.colTime')}
                </th>
                <th className="py-2 pr-4 font-medium">
                  {t('deliveries.colDest')}
                </th>
                <th className="py-2 pr-4 font-medium">
                  {t('deliveries.colKind')}
                </th>
                <th className="py-2 pr-4 font-medium">
                  {t('deliveries.colStatus')}
                </th>
                <th className="py-2 pr-4 font-medium">
                  {t('deliveries.colDetail')}
                </th>
              </tr>
            </thead>
            <tbody>
              {items.map((d) => (
                <tr key={d.id} className="border-b last:border-0 align-top">
                  <td className="py-2 pr-4 font-mono text-xs text-muted-foreground">
                    {d.occurred_at}
                  </td>
                  <td className="py-2 pr-4 font-mono">{d.destination}</td>
                  <td className="py-2 pr-4">
                    {d.finding_kind || d.event_type}
                    {d.severity ? (
                      <span className="ml-1 text-xs text-muted-foreground">
                        ({d.severity})
                      </span>
                    ) : null}
                  </td>
                  <td className="py-2 pr-4">
                    <Badge
                      variant={
                        d.status === 'delivered'
                          ? 'success'
                          : d.status === 'rejected' ||
                              d.status === 'failed' ||
                              d.status === 'unknown_destination' ||
                              d.status === 'no_dispatcher'
                            ? 'danger'
                            : 'neutral'
                      }
                    >
                      {t(`deliveries.statuses.${d.status}`, {
                        defaultValue: d.status,
                      })}
                    </Badge>
                  </td>
                  <td className="py-2 pr-4 text-muted-foreground">
                    {d.detail ?? ''}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          {deliveriesQ.hasNextPage ? (
            <div className="flex justify-center pt-2">
              <Button
                variant="ghost"
                size="sm"
                onClick={() => void deliveriesQ.fetchNextPage()}
                disabled={deliveriesQ.isFetchingNextPage}
              >
                {t('deliveries.loadMore')}
              </Button>
            </div>
          ) : null}
        </div>
      )}
    </div>
  )
}

// --- dead letters / the durable outbox --------------------------------

/**
 * DeadLettersTab — the operator's view of notify's durable outbox
 * (GET /v1/m/notify/outbox) and the one action it offers: requeue a row
 * (POST /outbox/{id}/redeliver). Both at modules/notify/api.go:46-47.
 *
 * TWO ROUTES, TWO PERMISSIONS, AND THEY ARE NOT THE SAME ONE. The engine gates the
 * list with notify:delivery:read and the requeue with notify:route:admin — a requeue
 * re-triggers an EXTERNAL delivery, which is why it sits at admin tier and is audited
 * (the reason is written at api.go:44-45). So the tab renders for a reader and the
 * button only for an admin.
 *
 * ⛔ WHAT THIS GATE DOES NOT PROMISE, AND AN EARLIER VERSION OF THIS COMMENT DID.
 * It said "nobody is shown a control the engine will refuse". That is FALSE, and it is
 * the same species of over-claim this session exists to retire. can() is pure membership
 * of the flat set /v1/auth/whoami hands over (#578, enforced by task lint:console-perms),
 * and that set is a STATIC PROJECTION whose own contract states it cannot represent
 * authored forbids or the deny overlay (core/auth/effective.go:30-48). The live decision
 * is (RBAC OR grant) AND NOT forbid AND NOT deny-overlay (core/auth/authorizer.go:199-247),
 * re-evaluated per request. Both directions are therefore reachable:
 *   - OVER-OFFER: a forbid or overlay veto lands after the button rendered → 403. That is
 *     why the 403 path below is a real outcome, not merely a race-condition net.
 *   - UNDER-OFFER: a scoped grant can authorize the engine while whoami omits it, hiding
 *     the tab from someone the engine would have served.
 * Closing either would mean re-implementing the authorizer client-side, which is exactly
 * the defect #578 removed. The engine stays the authority; this gate is a courtesy.
 *
 * WHY A TAB AND NOT A NAV ENTRY. The outbox belongs to modules/notify, the module this
 * view already administers, and its read permission is the very one already gating the
 * Deliveries tab. The precedent copied here — eventing's DeadLettersTab
 * (web/src/features/eventing/eventing-view.tsx:950-1002) — is itself a tab of its
 * module's view. So features/registry.tsx gains no entry; /alerting gains a tab.
 *
 * ON THE VIEW-LEVEL GATE. AlertingView returns ForbiddenState unless
 * can('notify:route:read'), so a principal holding delivery:read WITHOUT route:read could
 * not reach this tab. Under a BUILT-IN ROLE that cannot happen: module permissions are
 * granted by VERB tier (core/auth/permission.go:299-301) and no notify permission is in
 * the privileged-read set that is raised above it (:189-215), so the two :read permissions
 * always arrive together. A custom role or a direct scoped grant CAN separate them, which
 * is the under-offer above. Left as it is rather than widened: the whole view's gate is
 * not this session's to change.
 */
function DeadLettersTab() {
  const { t } = useTranslation(['alerting', 'common'])
  const { can } = useAuth()

  if (!can('notify:delivery:read')) {
    return <ForbiddenState description={t('outbox.noRead')} />
  }
  return <OutboxList canRedeliver={can('notify:route:admin')} />
}

/**
 * The THIRD ANSWER. A requeue that failed is neither a delivered notification nor
 * "nothing to see": it is its own outcome, and the screen has to say which one — a
 * toast is gone in seconds and the operator's next question is "so is it queued?".
 *
 * Read from the STATUS, never by matching the engine's prose: a message is not a
 * contract, and it is neither stable nor translated.
 *   409 — in flight; only a terminal row can be requeued (outbox_api.go:111-117,137-139)
 *   404 — no longer in this tenant's outbox (outbox_api.go:131-133)
 *   403 — NOT ONE THING. Three of them arrive here and only one is «you may not»:
 *        code=recording_consent_required → the acknowledgement
 *        code=step_up_required           → the ceremony (errors.ts:71-79 says to branch
 *                                          on this BEFORE isForbidden, which is only a status)
 *        anything else 403               → the role boundary, calm and not a failure
 *   ---- anything else carries the engine's own message
 */
type RedeliverFailureKind =
  'inFlight' | 'gone' | 'consent' | 'stepUp' | 'forbidden' | 'failed'

function redeliverFailureKind(err: unknown): RedeliverFailureKind {
  if (err instanceof ApiError) {
    if (err.status === 409) return 'inFlight'
    if (err.status === 404) return 'gone'
    // ⚠ A 403 IS NOT ONE THING, and reading only the status told the operator to go and
    // obtain a permission they already held. The engine mints a DISTINCT code for the
    // recording-consent boundary precisely so a console can route to the acknowledgement
    // instead of a generic authz denial (core/api/errors.go:211-215), and any tenant may
    // add notify to its recorded set (modules/recording accepts any mounted namespace).
    // The CODE is the machine-readable contract; the message is prose.
    if (err.code === 'recording_consent_required') return 'consent'
    // ⛔ ANTES que isForbidden, y no es estilo: `isForbidden` es SÓLO el status 403
    // (errors.ts:59), así que un step_up_required lo satisface también. Leerlo primero
    // clasificaba como «no tienes permiso» una negativa cuyo remedio es la ceremonia
    // —que `usePrivilegedMutation` ya abre—, y el operador veía las dos cosas a la vez.
    if (err.isStepUpRequired) return 'stepUp'
    if (err.isForbidden) return 'forbidden'
  }
  return 'failed'
}

/** A refusal is not a breakage. A permission or consent boundary is announced calmly
 * (role="status", muted) the way ForbiddenState is; only a real failure is red. */
function isCalmRefusal(kind: RedeliverFailureKind): boolean {
  return kind === 'forbidden' || kind === 'consent' || kind === 'stepUp'
}

function RedeliverFailure({
  kind,
  destination,
  detail,
}: {
  kind: RedeliverFailureKind
  destination: string
  detail?: string
}) {
  const { t } = useTranslation('alerting')
  // LITERAL keys, deliberately. check-i18n-usage.mjs resolves only string-literal
  // t() arguments, so a computed t(key) would be invisible to the gate — and an
  // unresolved key renders its raw dotted path to an operator mid-incident.
  const message =
    kind === 'inFlight'
      ? t('outbox.redeliver.inFlight', { destination })
      : kind === 'gone'
        ? t('outbox.redeliver.gone', { destination })
        : kind === 'consent'
          ? t('outbox.redeliver.consent', { destination })
          : kind === 'stepUp'
            ? t('outbox.redeliver.stepUp', { destination })
            : kind === 'forbidden'
              ? t('outbox.redeliver.forbidden', { destination })
              : t('outbox.redeliver.failed', { destination })
  const calm = isCalmRefusal(kind)
  return (
    <div
      // A boundary is announced, not alarmed: role="status" and muted, matching
      // ForbiddenState (components/ui/error-state.tsx) — this box used to paint EVERY
      // refusal red, including the one the design system calls calm.
      role={calm ? 'status' : 'alert'}
      className={
        calm
          ? 'rounded-md border border-border bg-muted px-3 py-2 text-sm text-foreground'
          : 'rounded-md border border-danger/40 bg-danger-soft px-3 py-2 text-sm text-foreground'
      }
    >
      <span className="font-medium">{message}</span>
      {detail ? (
        <span className="block text-xs text-muted-foreground">{detail}</span>
      ) : null}
    </div>
  )
}

function outboxStatusVariant(status: string): 'success' | 'danger' | 'neutral' {
  if (status === 'delivered') return 'success'
  if (status === 'dead') return 'danger'
  return 'neutral'
}

function OutboxList({ canRedeliver }: { canRedeliver: boolean }) {
  const { t } = useTranslation(['alerting', 'common'])
  const { activeTenant } = useAuth()
  // Opens on the DEAD-LETTER view, which is the reason this screen exists
  // (modules/notify/api.go:44). The other three statuses share the filter because a
  // row stuck in `delivering` is the other thing an operator comes here hunting for.
  const [status, setStatus] = useState<string>('dead')
  const [confirming, setConfirming] = useState<NotifyOutboxEntry | null>(null)
  const [failure, setFailure] = useState<{
    destination: string
    kind: RedeliverFailureKind
    detail?: string
  } | null>(null)

  const params = useMemo(() => ({ status }), [status])
  const outboxQ = useInfiniteQuery({
    queryKey: notifyKeys.outbox(activeTenant, params),
    queryFn: ({ pageParam }) =>
      notifyApi.listOutbox(
        pageParam ? { ...params, cursor: pageParam } : params,
      ),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => (last.has_more ? last.cursor : undefined),
  })
  const items = useMemo(
    () => outboxQ.data?.pages.flatMap((p) => p.items) ?? [],
    [outboxQ.data],
  )

  const redeliver = usePrivilegedMutation<
    NotifyOutboxEntry,
    NotifyRedeliverResult
  >({
    mutationFn: (row) => notifyApi.redeliverOutbox(row.id),
    // The PREFIX key: a requeue moves the row out of `dead` and into `queued`, two
    // lists whose keys differ in their params element, so invalidating the selected
    // filter alone would leave the other stale.
    invalidateKeys: [notifyKeys.outboxAll(activeTenant)],
    // 200 MEANS QUEUED, NOT DELIVERED. outbox_api.go:141 answers
    // {"id":…,"status":"queued"} and the send is made later by the pump — it can fail
    // exactly as it failed before. Saying "redelivered" would report an outcome the
    // engine has not reached, so the message reports what the engine actually said and
    // falls back to naming the status verbatim if it ever says something else.
    successMessage: (res, row) =>
      res.status === 'queued'
        ? t('outbox.redeliver.queued', { destination: row.destination })
        : t('outbox.redeliver.accepted', {
            destination: row.destination,
            status: res.status,
          }),
    successDescription: t('outbox.redeliver.queuedHint'),
    onDone: () => {
      setFailure(null)
      setConfirming(null)
    },
  })

  const requeue = (row: NotifyOutboxEntry) => {
    setFailure(null)
    redeliver.mutate(row, {
      onError: (err) => {
        setFailure({
          destination: row.destination,
          kind: redeliverFailureKind(err),
          detail: err instanceof ApiError ? err.message : undefined,
        })
        // Close on failure too: the outcome now lives in the alert region above the
        // table, which survives the row disappearing (a 404 means it already has).
        setConfirming(null)
      },
    })
  }

  const columns: TableColumn<NotifyOutboxEntry>[] = [
    {
      accessorKey: 'occurred_at',
      header: t('outbox.colTime'),
      cell: ({ row }) => (
        <span className="font-mono text-xs text-muted-foreground">
          {row.original.occurred_at}
        </span>
      ),
    },
    {
      accessorKey: 'destination',
      header: t('outbox.colDest'),
      cell: ({ row }) => (
        <span className="font-mono">{row.original.destination}</span>
      ),
    },
    {
      accessorKey: 'finding_kind',
      header: t('outbox.colKind'),
      cell: ({ row }) => {
        const entry = row.original
        return (
          <span>
            {entry.finding_kind || entry.event_type}
            {entry.severity ? (
              <span className="ml-1 text-xs text-muted-foreground">
                ({entry.severity})
              </span>
            ) : null}
            {entry.title ? (
              <span className="block text-xs text-muted-foreground">
                {entry.title}
              </span>
            ) : null}
          </span>
        )
      },
    },
    {
      accessorKey: 'attempts',
      header: t('outbox.colAttempts'),
      cell: ({ row }) => (
        <span className="font-mono text-xs">{row.original.attempts}</span>
      ),
    },
    {
      accessorKey: 'status',
      header: t('outbox.colStatus'),
      cell: ({ row }) => (
        <Badge variant={outboxStatusVariant(row.original.status)}>
          {t(`outbox.statuses.${row.original.status}`, {
            defaultValue: row.original.status,
          })}
        </Badge>
      ),
    },
    {
      accessorKey: 'last_detail',
      header: t('outbox.colDetail'),
      cell: ({ row }) => (
        <span className="text-muted-foreground">
          {row.original.last_detail ?? ''}
        </span>
      ),
    },
  ]
  if (canRedeliver) {
    columns.push({
      id: 'actions',
      header: () => <span className="sr-only">{t('outbox.colActions')}</span>,
      enableSorting: false,
      cell: ({ row }) => {
        const entry = row.original
        // A queued/delivering row is in flight and the engine answers 409, so the
        // action is not offered for it. This mirrors the engine's rule; it does not
        // replace it — the 409 path above still handles the race between this render
        // and the click.
        if (!isRedeliverable(entry.status)) return null
        return (
          <div className="flex justify-end">
            <Button
              variant="ghost"
              size="icon-sm"
              aria-label={t('outbox.redeliverAction', {
                destination: entry.destination,
              })}
              disabled={redeliver.isPending}
              onClick={() => {
                setFailure(null)
                setConfirming(entry)
              }}
            >
              <RefreshCw />
            </Button>
          </div>
        )
      },
    })
  }

  return (
    <div className="flex flex-col gap-4">
      <div className="flex items-center justify-between gap-2">
        <p className="text-sm text-muted-foreground">{t('outbox.intro')}</p>
        <div className="w-48">
          <Select value={status} onValueChange={setStatus}>
            <SelectTrigger aria-label={t('outbox.filterStatus')}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {OUTBOX_STATUSES.map((s) => (
                <SelectItem key={s} value={s}>
                  {t(`outbox.statuses.${s}`, { defaultValue: s })}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
      </div>

      {failure ? (
        <RedeliverFailure
          kind={failure.kind}
          destination={failure.destination}
          detail={failure.detail}
        />
      ) : null}

      <DataTable
        columns={columns}
        data={items}
        isLoading={outboxQ.isLoading}
        error={outboxQ.error}
        onRetry={() => void outboxQ.refetch()}
        // An empty DLQ and an unreadable one are DIFFERENT answers, and DataTable is
        // what keeps them apart: `error` renders its own state (a 403 calmly), and
        // this element is reached only when the engine really answered with no rows.
        empty={
          status === 'dead' ? (
            <EmptyState
              title={t('outbox.emptyDead')}
              description={t('outbox.emptyDeadHint')}
            />
          ) : (
            <EmptyState
              title={t('outbox.empty', {
                status: t(`outbox.statuses.${status}`, {
                  defaultValue: status,
                }),
              })}
              description={t('outbox.emptyHint')}
            />
          )
        }
        getRowId={(entry) => entry.id}
        hasMore={outboxQ.hasNextPage}
        onLoadMore={() => void outboxQ.fetchNextPage()}
        isFetchingMore={outboxQ.isFetchingNextPage}
        label={t('tabs.outbox')}
      />

      <ConfirmDialog
        open={confirming != null}
        onOpenChange={(open) => {
          if (!open && !redeliver.isPending) setConfirming(null)
        }}
        title={t('outbox.confirm.title')}
        description={t('outbox.confirm.description', {
          destination: confirming?.destination ?? '',
        })}
        confirmLabel={t('outbox.confirm.confirm')}
        pending={redeliver.isPending}
        onConfirm={() => {
          if (confirming) requeue(confirming)
        }}
      >
        {t('outbox.confirm.body', {
          attempts: confirming?.attempts ?? 0,
          detail: confirming?.last_detail || t('outbox.confirm.noDetail'),
        })}
      </ConfirmDialog>
    </div>
  )
}

export default AlertingView
