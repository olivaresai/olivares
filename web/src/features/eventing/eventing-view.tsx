// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Eventing (module: webhooks & event subscriptions) — the container. Tabs over
// subscriptions / events / deliveries / dead-letters. The subscription editor
// (react-hook-form + zod) configures the webhook endpoint; creation returns a
// one-time HMAC secret shown in a reveal modal (never persisted client-side).
import { useEffect, useMemo, useState } from 'react'
import { zodResolver } from '@hookform/resolvers/zod'
import {
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
} from '@tanstack/react-query'
import { Bell, CircleCheck, CircleOff, Plus } from 'lucide-react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { z } from 'zod'
import {
  BulkActionBar,
  type BulkAction,
} from '@/components/data/bulk-action-bar'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
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
import { Switch } from '@/components/ui/switch'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { Textarea } from '@/components/ui/textarea'
import { toast } from '@/components/ui/toaster'
import { useAuth } from '@/lib/auth/context'
import { useFailedActionReporter } from '@/lib/hooks/use-privileged-mutation'
import { useCommandStore } from '@/stores/command'
import { AsyncSection, IntelPage, SectionCard } from '@/features/_intel'
import { RevisionsSheet } from '@/features/shared/revisions-sheet'
import { eventingApi, eventingKeys } from './api'
import { EgressCompatReport, EgressPolicyPanel } from './egress-policy-panel'
import {
  DeliveryRow,
  EventRow,
  SecretReveal,
  SubscriptionCard,
} from './components'
import type {
  AuthType,
  DeliveryOrigin,
  ReplayResult,
  Subscription,
  SubscriptionInput,
} from './types'
import './i18n'

const AUTH_TYPES: AuthType[] = ['none', 'bearer', 'basic', 'header']
const ROLES = ['viewer', 'editor', 'admin', 'owner']
const DELIVERY_STATUSES = [
  'queued',
  'delivering',
  'delivered',
  'dead',
  'denied',
]

// Closed sets mirroring the module's authoring validation. SINK_FORMATS is the
// sdk/siemwire catalog's eventing surface, in catalog order, and is pinned
// against the beta OpenAPI snapshot's sink_format enum (sink-format.test.ts) —
// the engine renders that enum from the same catalog the module validates
// against, so this mirror cannot drift silently. SINK_KINDS mirrors
// modules/eventing/sink.go sinkKinds, which has no engine-published artifact
// yet. '' = the unchanged generic HMAC webhook.
const SINK_KINDS = [
  'https',
  'splunk_hec',
  'sentinel_dcr',
  'datadog',
  'newrelic',
] as const
export const SINK_FORMATS = [
  'ocsf',
  'cef',
  'leef',
  'syslog',
  'otlp',
  'otlp_envelope',
  'json',
] as const

// Vendor sinks carry a tower credential (sink.go credRequiredFor); the generic
// https sink authenticates via the engine HMAC and needs none.
function sinkCredRequired(kind: string): boolean {
  return kind !== '' && kind !== 'https'
}

/** Page size for the cursor/seq-paginated lists (events, deliveries, DLQ). */
const PAGE = 50

// Radix Select rejects an empty-string item value, so the "no filter" option
// carries this sentinel and is mapped back to undefined at the query edge.
const ALL = 'all'

export function EventingView() {
  const { t } = useTranslation('eventing')
  const { activeTenant, can } = useAuth()

  const canRead = can('eventing:subscription:read')
  const canWrite = can('eventing:subscription:write')
  // Replay, test, delete and redeliver are ADMIN-tier server-side
  // (eventing.go: permSubAdmin) — the UI gate must mirror the API tier, not
  // lump every action under write.
  const canAdmin = can('eventing:subscription:admin')
  const [createOpen, setCreateOpen] = useState(false)
  // ⌘K palette action: "new subscription" navigated here — consume once.
  useEffect(() => {
    if (
      useCommandStore.getState().consumeAction('eventing') ===
      'createSubscription'
    ) {
      setCreateOpen(true)
    }
  }, [])
  const [editSub, setEditSub] = useState<Subscription | null>(null)
  const [revealSecret, setRevealSecret] = useState<string | null>(null)
  const [tab, setTab] = useState('subscriptions')
  const [deliveryOrigin, setDeliveryOrigin] = useState<string>(ALL)

  const subsQ = useQuery({
    queryKey: eventingKeys.subscriptions(activeTenant),
    queryFn: () => eventingApi.subscriptions(),
  })

  return (
    <IntelPage icon={Bell} title={t('title')} description={t('description')}>
      <Tabs value={tab} onValueChange={setTab}>
        <TabsList>
          <TabsTrigger value="subscriptions">
            {t('tabs.subscriptions')}
          </TabsTrigger>
          <TabsTrigger value="events">{t('tabs.events')}</TabsTrigger>
          <TabsTrigger value="deliveries">{t('tabs.deliveries')}</TabsTrigger>
          <TabsTrigger value="deadLetters">{t('tabs.deadLetters')}</TabsTrigger>
        </TabsList>

        <TabsContent value="subscriptions" className="flex flex-col gap-4">
          {/* ⛔ VA ARRIBA Y EN ESTA PESTAÑA, no en una suya. El motor dice para quién existe:
              «an author whose destination was refused could not tell an operator's rule from a
              typo» (modules/eventing/egressapi.go:20-22). Ese autor está AQUÍ, mirando por qué
              su suscripción no entrega — no en una pestaña que abriría sólo si ya sospechara
              que el problema es la política, que es justo lo que no puede saber. */}
          <EgressPolicyPanel canAdmin={canAdmin} />
          <EgressCompatReport canAdmin={canAdmin} />
          <SubscriptionsTab
            subsQ={subsQ}
            canRead={canRead}
            canWrite={canWrite}
            canAdmin={canAdmin}
            onNew={() => setCreateOpen(true)}
            onEdit={setEditSub}
            onSecretRevealed={setRevealSecret}
            onViewReplay={() => {
              setDeliveryOrigin('replay')
              setTab('deliveries')
            }}
          />
        </TabsContent>

        <TabsContent value="events" className="flex flex-col gap-4">
          <EventsTab />
        </TabsContent>

        <TabsContent value="deliveries" className="flex flex-col gap-4">
          <DeliveriesTab
            originFilter={deliveryOrigin}
            onOriginFilterChange={setDeliveryOrigin}
          />
        </TabsContent>

        <TabsContent value="deadLetters" className="flex flex-col gap-4">
          <DeadLettersTab canAdmin={canAdmin} />
        </TabsContent>
      </Tabs>

      {/* Create dialog */}
      {canWrite ? (
        <SubscriptionDialog
          open={createOpen}
          onOpenChange={setCreateOpen}
          onSecretRevealed={setRevealSecret}
        />
      ) : null}

      {/* Edit dialog */}
      {canWrite && editSub ? (
        <SubscriptionDialog
          open={editSub !== null}
          onOpenChange={(v) => {
            if (!v) setEditSub(null)
          }}
          existing={editSub}
        />
      ) : null}

      {/* Secret reveal dialog */}
      <Dialog
        open={revealSecret !== null}
        onOpenChange={(v) => {
          if (!v) setRevealSecret(null)
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t('secret.title')}</DialogTitle>
          </DialogHeader>
          {revealSecret ? (
            <SecretReveal
              secret={revealSecret}
              onDone={() => setRevealSecret(null)}
            />
          ) : null}
        </DialogContent>
      </Dialog>
    </IntelPage>
  )
}

// --- subscriptions tab -------------------------------------------------------

function SubscriptionsTab({
  subsQ,
  canRead,
  canWrite,
  canAdmin,
  onNew,
  onEdit,
  onSecretRevealed,
  onViewReplay,
}: {
  subsQ: ReturnType<
    typeof useQuery<Awaited<ReturnType<typeof eventingApi.subscriptions>>>
  >
  canRead: boolean
  canWrite: boolean
  canAdmin: boolean
  onNew: () => void
  onEdit: (sub: Subscription) => void
  onSecretRevealed: (secret: string) => void
  onViewReplay: () => void
}) {
  const { t } = useTranslation(['eventing', 'common'])
  const { activeTenant } = useAuth()
  const qc = useQueryClient()
  const report = useFailedActionReporter()
  const [testingId, setTestingId] = useState<string | null>(null)
  // Rotate-auth needs the NEW credential from the operator (the endpoint 400s
  // on an empty body — the engine never invents a downstream credential), so
  // the action opens a dialog instead of firing a bodyless POST.
  const [rotateAuthSub, setRotateAuthSub] = useState<Subscription | null>(null)
  const [replaySub, setReplaySub] = useState<Subscription | null>(null)
  const [historySub, setHistorySub] = useState<Subscription | null>(null)
  const [selectedIds, setSelectedIds] = useState<Set<string>>(new Set())

  const testMut = useMutation({
    mutationFn: (id: string) => eventingApi.testSubscription(id),
    onMutate: (id) => setTestingId(id),
    onSuccess: (res) => {
      if (res.delivered) toast.success(t('subscriptions.testSuccess'))
      else toast.error(t('subscriptions.testFailed') + `: ${res.outcome}`)
      setTestingId(null)
    },
    onError: (e: unknown) => {
      report(e)
      setTestingId(null)
    },
  })

  const deleteMut = useMutation({
    mutationFn: (id: string) => eventingApi.deleteSubscription(id),
    onSuccess: () => {
      toast.success(t('subscriptions.deleted'))
      void qc.invalidateQueries({
        queryKey: eventingKeys.subscriptions(activeTenant),
      })
    },
    onError: (e: unknown) => report(e),
  })

  const rotateSecretMut = useMutation({
    mutationFn: (id: string) => eventingApi.rotateSecret(id),
    onSuccess: (res) => {
      toast.success(t('subscriptions.rotated'))
      onSecretRevealed(res.secret)
    },
    onError: (e: unknown) => report(e),
  })

  const setEnabled = async (id: string, enabled: boolean) => {
    // The eventing contract exposes an audited full PUT, not PATCH. Refresh the
    // item immediately before changing it so a bulk run never overwrites a
    // concurrent edit with stale card data or drops a stored SIEM profile.
    const current = await eventingApi.subscription(id)
    await eventingApi.updateSubscription(
      id,
      subscriptionInputWithEnabled(current, enabled),
    )
    await qc.invalidateQueries({
      queryKey: eventingKeys.subscriptions(activeTenant),
    })
  }
  const bulkActions: BulkAction[] = [
    {
      id: 'enable',
      label: t('subscriptions.bulk.enable'),
      icon: CircleCheck,
      run: (id) => setEnabled(id, true),
    },
    {
      id: 'disable',
      label: t('subscriptions.bulk.disable'),
      icon: CircleOff,
      run: (id) => setEnabled(id, false),
    },
  ]

  return (
    <SectionCard
      title={t('subscriptions.title')}
      description={t('subscriptions.description')}
      actions={
        canWrite ? (
          <Button variant="primary" size="sm" onClick={onNew}>
            <Plus />
            {t('subscriptions.new')}
          </Button>
        ) : null
      }
    >
      <AsyncSection query={subsQ} skeletonHeight={240}>
        {(list) => {
          const visibleIds = list.items.map((sub) => sub.id)
          const selectedVisible = visibleIds.filter((id) =>
            selectedIds.has(id),
          ).length
          const allVisible =
            visibleIds.length > 0 && selectedVisible === visibleIds.length
          return list.items.length === 0 ? (
            <EmptyState
              title={t('subscriptions.empty')}
              description={t('subscriptions.emptyHint')}
            />
          ) : (
            <div className="flex flex-col gap-3">
              {canWrite ? (
                <div className="flex items-center gap-2 px-1 text-sm text-muted-foreground">
                  <Checkbox
                    checked={
                      allVisible
                        ? true
                        : selectedVisible > 0
                          ? 'indeterminate'
                          : false
                    }
                    aria-label={t('common:table.selectAllVisible')}
                    onCheckedChange={(checked) => {
                      const next = new Set(selectedIds)
                      for (const id of visibleIds) {
                        if (checked === true) next.add(id)
                        else next.delete(id)
                      }
                      setSelectedIds(next)
                    }}
                  />
                  <span>{t('common:table.selectAllVisible')}</span>
                </div>
              ) : null}
              {canWrite ? (
                <BulkActionBar
                  selectedIds={[...selectedIds]}
                  onClear={() => setSelectedIds(new Set())}
                  actions={bulkActions}
                />
              ) : null}
              {list.items.map((sub) => (
                <div key={sub.id} className="flex items-start gap-2">
                  {canWrite ? (
                    <Checkbox
                      className="mt-4"
                      checked={selectedIds.has(sub.id)}
                      aria-label={t('common:table.selectRow', { id: sub.id })}
                      onCheckedChange={() => {
                        const next = new Set(selectedIds)
                        if (next.has(sub.id)) next.delete(sub.id)
                        else next.add(sub.id)
                        setSelectedIds(next)
                      }}
                    />
                  ) : null}
                  <div className="min-w-0 flex-1">
                    <SubscriptionCard
                      sub={sub}
                      canRead={canRead}
                      canWrite={canWrite}
                      canAdmin={canAdmin}
                      onEdit={onEdit}
                      onTest={(id) => testMut.mutate(id)}
                      onDelete={(id) => deleteMut.mutate(id)}
                      onRotateSecret={(id) => rotateSecretMut.mutate(id)}
                      onRotateAuth={() => setRotateAuthSub(sub)}
                      onReplay={setReplaySub}
                      onHistory={setHistorySub}
                      testPending={testingId === sub.id}
                    />
                  </div>
                </div>
              ))}
            </div>
          )
        }}
      </AsyncSection>
      {rotateAuthSub ? (
        <RotateAuthDialog
          sub={rotateAuthSub}
          onClose={() => setRotateAuthSub(null)}
        />
      ) : null}
      {replaySub ? (
        <ReplayDialog
          sub={replaySub}
          onClose={() => setReplaySub(null)}
          onViewDeliveries={() => {
            setReplaySub(null)
            onViewReplay()
          }}
        />
      ) : null}
      {historySub ? (
        <RevisionsSheet
          open
          onOpenChange={(open) => {
            if (!open) setHistorySub(null)
          }}
          queryKey={eventingKeys.subscriptionRevisions(
            activeTenant,
            historySub.id,
          )}
          listRevisions={(params) =>
            eventingApi.subscriptionRevisions(historySub.id, params)
          }
          restoreRevision={(revisionId) =>
            eventingApi.restoreSubscription(historySub.id, revisionId)
          }
          invalidateKeys={[
            eventingKeys.subscriptions(activeTenant),
            eventingKeys.subscription(activeTenant, historySub.id),
          ]}
          canWrite={canWrite}
          labels={{
            title: t('history.title', { name: historySub.name }),
            description: t('history.description'),
            caption: t('history.scopeCaption'),
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
              name: historySub.name,
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
    </SectionCard>
  )
}

function ReplayDialog({
  sub,
  onClose,
  onViewDeliveries,
}: {
  sub: Subscription
  onClose: () => void
  onViewDeliveries: () => void
}) {
  const { t } = useTranslation(['eventing', 'common'])
  const { activeTenant } = useAuth()
  const qc = useQueryClient()
  const report = useFailedActionReporter()
  const [fromSeq, setFromSeq] = useState('1')
  const [toSeq, setToSeq] = useState('')
  const [total, setTotal] = useState(0)
  const [result, setResult] = useState<ReplayResult | null>(null)

  const mut = useMutation({
    mutationFn: () =>
      eventingApi.replayEvents(sub.id, {
        from_seq: Number(fromSeq),
        ...(toSeq ? { to_seq: Number(toSeq) } : {}),
      }),
    onSuccess: (res) => {
      setTotal((current) => current + res.replayed)
      setResult(res)
      if (res.has_more) setFromSeq(String(res.next_seq))
      toast.success(t('replay.completed', { count: res.replayed }))
      void qc.invalidateQueries({
        queryKey: eventingKeys.deliveries(activeTenant),
      })
    },
    onError: (e: unknown) => report(e),
  })
  const valid =
    Number(fromSeq) >= 1 && (!toSeq || Number(toSeq) >= Number(fromSeq))

  return (
    <Dialog open onOpenChange={(v) => !v && !mut.isPending && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t('replay.title', { name: sub.name })}</DialogTitle>
          <DialogDescription>{t('replay.description')}</DialogDescription>
        </DialogHeader>
        <form
          className="flex flex-col gap-3"
          onSubmit={(e) => {
            e.preventDefault()
            if (valid && !mut.isPending) mut.mutate()
          }}
        >
          <div className="grid grid-cols-2 gap-3">
            <Field label={t('replay.fromSeq')} required>
              {({ id }) => (
                <Input
                  id={id}
                  type="number"
                  min="1"
                  value={fromSeq}
                  onChange={(e) => setFromSeq(e.target.value)}
                />
              )}
            </Field>
            <Field label={t('replay.toSeq')}>
              {({ id }) => (
                <Input
                  id={id}
                  type="number"
                  min="1"
                  value={toSeq}
                  onChange={(e) => setToSeq(e.target.value)}
                />
              )}
            </Field>
          </div>
          {result ? (
            <p className="text-sm text-muted-foreground">
              {t('replay.total', { count: total })}
            </p>
          ) : null}
          <DialogFooter>
            {result && !result.has_more ? (
              <Button
                type="button"
                variant="secondary"
                onClick={onViewDeliveries}
              >
                {t('replay.viewDeliveries')}
              </Button>
            ) : null}
            <Button
              type="button"
              variant="ghost"
              onClick={onClose}
              disabled={mut.isPending}
            >
              {t('common:actions.cancel')}
            </Button>
            <Button
              type="submit"
              variant="primary"
              disabled={!valid || mut.isPending}
            >
              {result?.has_more ? t('replay.continue') : t('replay.submit')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

// --- rotate-auth dialog ------------------------------------------------------

// Collects the NEW auth credential and posts it to rotate-auth. The value is a
// secret: masked while typed, sent once, never displayed again (the card shows
// the server-derived auth_value_hint only).
function RotateAuthDialog({
  sub,
  onClose,
}: {
  sub: Subscription
  onClose: () => void
}) {
  const { t } = useTranslation(['eventing', 'common'])
  const { activeTenant } = useAuth()
  const qc = useQueryClient()
  const report = useFailedActionReporter()
  const [authValue, setAuthValue] = useState('')

  const mut = useMutation({
    mutationFn: () => eventingApi.rotateAuth(sub.id, { auth_value: authValue }),
    onSuccess: () => {
      toast.success(t('subscriptions.authRotated'))
      void qc.invalidateQueries({
        queryKey: eventingKeys.subscriptions(activeTenant),
      })
      onClose()
    },
    onError: (e: unknown) => report(e),
  })

  return (
    <Dialog
      open
      onOpenChange={(v) => {
        if (!v && !mut.isPending) onClose()
      }}
    >
      <DialogContent>
        <DialogHeader>
          <DialogTitle>
            {t('rotateAuthDialog.title', { name: sub.name })}
          </DialogTitle>
          <DialogDescription>
            {t('rotateAuthDialog.description')}
          </DialogDescription>
        </DialogHeader>
        <form
          className="flex flex-col gap-3"
          onSubmit={(e) => {
            e.preventDefault()
            if (authValue.trim() && !mut.isPending) mut.mutate()
          }}
        >
          <Field
            label={t('rotateAuthDialog.value')}
            description={t('rotateAuthDialog.valueHint')}
            required
          >
            {({ id }) => (
              <Input
                id={id}
                type="password"
                autoComplete="off"
                value={authValue}
                onChange={(e) => setAuthValue(e.target.value)}
              />
            )}
          </Field>
          <DialogFooter>
            <Button
              type="button"
              variant="ghost"
              onClick={onClose}
              disabled={mut.isPending}
            >
              {t('common:actions.cancel')}
            </Button>
            <Button
              type="submit"
              variant="primary"
              disabled={!authValue.trim() || mut.isPending}
            >
              {t('rotateAuthDialog.submit')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

// --- events tab --------------------------------------------------------------

function EventsTab() {
  const { t } = useTranslation('eventing')
  const { activeTenant } = useAuth()
  const [typeFilter, setTypeFilter] = useState<string>(ALL)

  const eventTypesQ = useQuery({
    queryKey: eventingKeys.eventTypes(activeTenant),
    queryFn: () => eventingApi.eventTypes(),
  })

  // Seq-keyset pagination: each page ends with next_seq, the cursor for the
  // following page (modules/eventing/replay.go handleListEvents). The filter is
  // part of the key so changing it restarts the walk from the head.
  const eventsQ = useInfiniteQuery({
    queryKey: [...eventingKeys.events(activeTenant), typeFilter],
    queryFn: ({ pageParam }) =>
      eventingApi.events({
        type: typeFilter === ALL ? undefined : typeFilter,
        since_seq: pageParam,
        limit: PAGE,
      }),
    initialPageParam: undefined as number | undefined,
    getNextPageParam: (last) => (last.has_more ? last.next_seq : undefined),
  })

  const events = useMemo(
    () => eventsQ.data?.pages.flatMap((p) => p.items) ?? [],
    [eventsQ.data],
  )

  return (
    <SectionCard
      title={t('events.title')}
      description={t('events.description')}
      actions={
        <Select value={typeFilter} onValueChange={setTypeFilter}>
          <SelectTrigger className="w-56" aria-label={t('events.typeFilter')}>
            <SelectValue placeholder={t('events.typeFilter')} />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={ALL}>{t('events.allTypes')}</SelectItem>
            {eventTypesQ.data?.event_types.map((et) => (
              <SelectItem key={et.type} value={et.type}>
                {et.type}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      }
    >
      <AsyncSection query={eventsQ} skeletonHeight={240}>
        {() =>
          events.length === 0 ? (
            <EmptyState title={t('events.empty')} />
          ) : (
            <div className="flex flex-col gap-2">
              {events.map((evt) => (
                <EventRow key={evt.id} event={evt} />
              ))}
              {eventsQ.hasNextPage ? (
                <Button
                  variant="ghost"
                  size="sm"
                  className="self-center"
                  onClick={() => void eventsQ.fetchNextPage()}
                  disabled={eventsQ.isFetchingNextPage}
                >
                  {t('events.loadMore')}
                </Button>
              ) : (
                <p className="text-center text-xs text-muted-foreground">
                  {t('events.noMore')}
                </p>
              )}
            </div>
          )
        }
      </AsyncSection>
    </SectionCard>
  )
}

// --- deliveries tab ----------------------------------------------------------

function DeliveriesTab({
  originFilter,
  onOriginFilterChange,
}: {
  originFilter: string
  onOriginFilterChange: (value: string) => void
}) {
  const { t } = useTranslation('eventing')
  const { activeTenant } = useAuth()
  const [subFilter, setSubFilter] = useState<string>(ALL)
  const [statusFilter, setStatusFilter] = useState<string>(ALL)
  const [typeFilter, setTypeFilter] = useState<string>(ALL)

  // The filter selects are fed from the REAL rosters: the tenant's
  // subscriptions and the event-type catalog (both already served).
  const subsQ = useQuery({
    queryKey: eventingKeys.subscriptions(activeTenant),
    queryFn: () => eventingApi.subscriptions(),
  })
  const eventTypesQ = useQuery({
    queryKey: eventingKeys.eventTypes(activeTenant),
    queryFn: () => eventingApi.eventTypes(),
  })

  // Cursor pagination (modules/eventing listQuery): each page carries the
  // cursor for the next; filters are part of the key so changing one restarts.
  const deliveriesQ = useInfiniteQuery({
    queryKey: [
      ...eventingKeys.deliveries(activeTenant),
      subFilter,
      statusFilter,
      typeFilter,
      originFilter,
    ],
    queryFn: ({ pageParam }) =>
      eventingApi.deliveries({
        subscription: subFilter === ALL ? undefined : subFilter,
        status: statusFilter === ALL ? undefined : statusFilter,
        event_type: typeFilter === ALL ? undefined : typeFilter,
        origin:
          originFilter === ALL ? undefined : (originFilter as DeliveryOrigin),
        cursor: pageParam,
        limit: PAGE,
      }),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => (last.has_more ? last.cursor : undefined),
  })

  const deliveries = useMemo(
    () => deliveriesQ.data?.pages.flatMap((p) => p.items) ?? [],
    [deliveriesQ.data],
  )

  return (
    <SectionCard
      title={t('deliveries.title')}
      description={t('deliveries.description')}
      actions={
        <div className="flex gap-2">
          <Select value={subFilter} onValueChange={setSubFilter}>
            <SelectTrigger
              className="w-44"
              aria-label={t('deliveries.subscriptionFilter')}
            >
              <SelectValue placeholder={t('deliveries.subscriptionFilter')} />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={ALL}>
                {t('deliveries.allSubscriptions')}
              </SelectItem>
              {subsQ.data?.items.map((s) => (
                <SelectItem key={s.id} value={s.id}>
                  {s.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <Select value={statusFilter} onValueChange={setStatusFilter}>
            <SelectTrigger
              className="w-40"
              aria-label={t('deliveries.statusFilter')}
            >
              <SelectValue placeholder={t('deliveries.statusFilter')} />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={ALL}>{t('deliveries.allStatuses')}</SelectItem>
              {DELIVERY_STATUSES.map((s) => (
                <SelectItem key={s} value={s}>
                  {t(`deliveries.statuses.${s}`)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <Select value={typeFilter} onValueChange={setTypeFilter}>
            <SelectTrigger
              className="w-44"
              aria-label={t('deliveries.typeFilter')}
            >
              <SelectValue placeholder={t('deliveries.typeFilter')} />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={ALL}>{t('deliveries.allTypes')}</SelectItem>
              {eventTypesQ.data?.event_types.map((et) => (
                <SelectItem key={et.type} value={et.type}>
                  {et.type}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <Select value={originFilter} onValueChange={onOriginFilterChange}>
            <SelectTrigger
              className="w-36"
              aria-label={t('deliveries.originFilter')}
            >
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={ALL}>{t('deliveries.allOrigins')}</SelectItem>
              <SelectItem value="live">
                {t('deliveries.origins.live')}
              </SelectItem>
              <SelectItem value="replay">
                {t('deliveries.origins.replay')}
              </SelectItem>
            </SelectContent>
          </Select>
        </div>
      }
    >
      <AsyncSection query={deliveriesQ} skeletonHeight={240}>
        {() =>
          deliveries.length === 0 ? (
            <EmptyState title={t('deliveries.empty')} />
          ) : (
            <div className="flex flex-col gap-2">
              {deliveries.map((d) => (
                <DeliveryRow
                  key={d.id}
                  delivery={d}
                  subscriptionName={
                    subsQ.data?.items.find((sub) => sub.id === d.subscription)
                      ?.name
                  }
                />
              ))}
              {deliveriesQ.hasNextPage ? (
                <Button
                  variant="ghost"
                  size="sm"
                  className="self-center"
                  onClick={() => void deliveriesQ.fetchNextPage()}
                  disabled={deliveriesQ.isFetchingNextPage}
                >
                  {t('deliveries.loadMore')}
                </Button>
              ) : null}
            </div>
          )
        }
      </AsyncSection>
    </SectionCard>
  )
}

// --- dead-letters tab --------------------------------------------------------

function DeadLettersTab({ canAdmin }: { canAdmin: boolean }) {
  const { t } = useTranslation('eventing')
  const { activeTenant } = useAuth()
  const qc = useQueryClient()
  const report = useFailedActionReporter()
  const subsQ = useQuery({
    queryKey: eventingKeys.subscriptions(activeTenant),
    queryFn: () => eventingApi.subscriptions(),
  })

  const dlQ = useInfiniteQuery({
    queryKey: eventingKeys.deadLetters(activeTenant),
    queryFn: ({ pageParam }) =>
      eventingApi.deadLetters({ cursor: pageParam, limit: PAGE }),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => (last.has_more ? last.cursor : undefined),
  })

  const deadLetters = useMemo(
    () => dlQ.data?.pages.flatMap((p) => p.items) ?? [],
    [dlQ.data],
  )

  const [redeliverId, setRedeliverId] = useState<string | null>(null)

  const redeliverMut = useMutation({
    mutationFn: (id: string) => eventingApi.redeliver(id),
    onMutate: (id) => setRedeliverId(id),
    onSuccess: () => {
      toast.success(t('deadLetters.redelivered'))
      void qc.invalidateQueries({
        queryKey: eventingKeys.deadLetters(activeTenant),
      })
      void qc.invalidateQueries({
        queryKey: eventingKeys.deliveries(activeTenant),
      })
      setRedeliverId(null)
    },
    onError: (e: unknown) => {
      report(e)
      setRedeliverId(null)
    },
  })

  return (
    <SectionCard
      title={t('deadLetters.title')}
      description={t('deadLetters.description')}
    >
      <AsyncSection query={dlQ} skeletonHeight={200}>
        {() =>
          deadLetters.length === 0 ? (
            <EmptyState
              title={t('deadLetters.empty')}
              description={t('deadLetters.emptyHint')}
            />
          ) : (
            <div className="flex flex-col gap-2">
              {deadLetters.map((d) => (
                <DeliveryRow
                  key={d.id}
                  delivery={d}
                  subscriptionName={
                    subsQ.data?.items.find((sub) => sub.id === d.subscription)
                      ?.name
                  }
                  showRedeliver={canAdmin}
                  onRedeliver={(id) => redeliverMut.mutate(id)}
                  redeliverPending={redeliverId === d.id}
                />
              ))}
              {dlQ.hasNextPage ? (
                <Button
                  variant="ghost"
                  size="sm"
                  className="self-center"
                  onClick={() => void dlQ.fetchNextPage()}
                  disabled={dlQ.isFetchingNextPage}
                >
                  {t('deadLetters.loadMore')}
                </Button>
              ) : null}
            </div>
          )
        }
      </AsyncSection>
    </SectionCard>
  )
}

// --- subscription create/edit dialog -----------------------------------------

export const subscriptionSchema = z.object({
  name: z.string().min(1),
  endpoint: z.string().url(),
  event_types: z.array(z.string()).min(1),
  match_sources: z.string(),
  role: z.string(),
  auth_type: z.enum(['none', 'bearer', 'basic', 'header']),
  auth_value: z.string(),
  auth_header_name: z.string(),
  max_attempts: z.coerce
    .number()
    .int()
    .refine((v) => v === 0 || (v >= 1 && v <= 20), {
      message: 'dialog.errors.maxAttempts',
    }),
  initial_interval_seconds: z.coerce
    .number()
    .int()
    .refine((v) => v === 0 || (v >= 5 && v <= 3600), {
      message: 'dialog.errors.initialInterval',
    }),
  description: z.string(),
  enabled: z.boolean(),
  // SIEM sink profile. '' = generic webhook. The cred is write-only:
  // required at creation for vendor sinks, empty on edit = keep the sealed one.
  sink_kind: z.string(),
  sink_format: z.string(),
  sink_cred: z.string(),
})
type SubFormInput = z.input<typeof subscriptionSchema>
type SubForm = z.output<typeof subscriptionSchema>

// The sink fields of the wire payload. On EDIT the existing profile is
// PRESERVED: kind/format prefill from the subscription, an empty cred means
// "keep the sealed one" (modules/eventing/subscription.go update path), and the
// stored non-secret sink_opts are re-sent verbatim while the kind is unchanged
// (omitting sink_kind would make the backend DELETE the profile).
function sinkPayload(
  values: SubForm,
  existing?: Subscription,
): Pick<
  SubscriptionInput,
  'sink_kind' | 'sink_format' | 'sink_cred' | 'sink_opts'
> {
  if (!values.sink_kind) return {}
  const keepOpts =
    existing && existing.sink_kind === values.sink_kind
      ? existing.sink_opts
      : undefined
  return {
    sink_kind: values.sink_kind,
    sink_format: values.sink_format || undefined,
    sink_cred: values.sink_cred || undefined,
    sink_opts:
      keepOpts && Object.keys(keepOpts).length > 0 ? keepOpts : undefined,
  }
}

function storedSinkPayload(
  current: Subscription,
): Pick<SubscriptionInput, 'sink_kind' | 'sink_format' | 'sink_opts'> {
  if (!current.sink_kind) return {}
  return {
    sink_kind: current.sink_kind,
    sink_format: current.sink_format || undefined,
    sink_opts:
      current.sink_opts && Object.keys(current.sink_opts).length > 0
        ? current.sink_opts
        : undefined,
  }
}

function subscriptionInputWithEnabled(
  current: Subscription,
  enabled: boolean,
): SubscriptionInput {
  return {
    name: current.name,
    enabled,
    event_types: current.event_types,
    match_sources: current.match_sources,
    endpoint: current.endpoint,
    role: current.role,
    description: current.description,
    auth_type: current.auth_type,
    auth_header_name: current.auth_header_name,
    max_attempts: current.max_attempts,
    initial_interval_seconds: current.initial_interval_seconds,
    ...storedSinkPayload(current),
  }
}

function csv(s: string): string[] {
  return s
    .split(',')
    .map((x) => x.trim())
    .filter(Boolean)
}

function SubscriptionDialog({
  open,
  onOpenChange,
  existing,
  onSecretRevealed,
}: {
  open: boolean
  onOpenChange: (v: boolean) => void
  existing?: Subscription
  onSecretRevealed?: (secret: string) => void
}) {
  const { t } = useTranslation(['eventing', 'common'])
  const { activeTenant } = useAuth()
  const qc = useQueryClient()
  const report = useFailedActionReporter()
  const isEdit = !!existing

  const eventTypesQ = useQuery({
    queryKey: eventingKeys.eventTypes(activeTenant),
    queryFn: () => eventingApi.eventTypes(),
  })

  const form = useForm<SubFormInput, unknown, SubForm>({
    resolver: zodResolver(subscriptionSchema),
    defaultValues: {
      name: existing?.name ?? '',
      endpoint: existing?.endpoint ?? '',
      event_types: existing?.event_types ?? [],
      match_sources: existing?.match_sources?.join(', ') ?? '',
      role: existing?.role ?? 'viewer',
      auth_type: existing?.auth_type ?? 'none',
      auth_value: '',
      auth_header_name: existing?.auth_header_name ?? '',
      max_attempts: existing?.max_attempts ?? 0,
      initial_interval_seconds: existing?.initial_interval_seconds ?? 0,
      description: existing?.description ?? '',
      enabled: existing?.enabled ?? true,
      sink_kind: existing?.sink_kind ?? '',
      sink_format: existing?.sink_format ?? '',
      sink_cred: '',
    },
  })

  const authType = form.watch('auth_type')
  const sinkKind = form.watch('sink_kind')
  const selectedEventTypes = form.watch('event_types')
  const existingId = existing?.id

  const createMut = useMutation({
    mutationFn: (values: SubForm) =>
      eventingApi.createSubscription({
        name: values.name,
        endpoint: values.endpoint,
        event_types: values.event_types,
        match_sources: csv(values.match_sources),
        role: values.role || undefined,
        auth_type: values.auth_type,
        auth_value: values.auth_value || undefined,
        auth_header_name: values.auth_header_name || undefined,
        max_attempts: values.max_attempts || undefined,
        initial_interval_seconds: values.initial_interval_seconds || undefined,
        description: values.description || undefined,
        enabled: values.enabled,
        ...sinkPayload(values),
      }),
    onSuccess: (res) => {
      toast.success(t('dialog.created'))
      void qc.invalidateQueries({
        queryKey: eventingKeys.subscriptions(activeTenant),
      })
      onOpenChange(false)
      form.reset()
      if (onSecretRevealed && res.secret) onSecretRevealed(res.secret)
    },
    onError: (e: unknown) => report(e),
  })

  const updateMut = useMutation({
    mutationFn: async (values: SubForm) => {
      if (!existingId) throw new Error('subscription id is required')

      // Refresh immediately before PUT. The list card is only a snapshot: if a
      // second operator added/changed a SIEM profile after this dialog opened,
      // re-sending the stale list fields would destroy that profile. A failed
      // refresh aborts the edit (fail closed) before any update request.
      const current = await eventingApi.subscription(existingId)
      const sinkUntouched =
        values.sink_kind === (existing?.sink_kind ?? '') &&
        values.sink_format === (existing?.sink_format ?? '') &&
        values.sink_cred.trim() === ''
      const sink = sinkUntouched
        ? storedSinkPayload(current)
        : sinkPayload(values, current)

      return eventingApi.updateSubscription(existingId, {
        name: values.name,
        endpoint: values.endpoint,
        event_types: values.event_types,
        match_sources: csv(values.match_sources),
        role: values.role || undefined,
        auth_type: values.auth_type,
        auth_value: values.auth_value || undefined,
        auth_header_name: values.auth_header_name || undefined,
        max_attempts: values.max_attempts || undefined,
        initial_interval_seconds: values.initial_interval_seconds || undefined,
        description: values.description || undefined,
        enabled: values.enabled,
        ...sink,
      })
    },
    onSuccess: () => {
      toast.success(t('dialog.updated'))
      void qc.invalidateQueries({
        queryKey: eventingKeys.subscriptions(activeTenant),
      })
      onOpenChange(false)
    },
    onError: (e: unknown) => report(e),
  })

  const pending = createMut.isPending || updateMut.isPending

  const handleToggleEventType = (type: string) => {
    const current = form.getValues('event_types')
    if (current.includes(type)) {
      form.setValue(
        'event_types',
        current.filter((t) => t !== type),
        { shouldValidate: true },
      )
    } else {
      form.setValue('event_types', [...current, type], {
        shouldValidate: true,
      })
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[85vh] overflow-y-auto">
        <DialogHeader>
          <DialogTitle>
            {isEdit ? t('dialog.editTitle') : t('dialog.createTitle')}
          </DialogTitle>
          <DialogDescription>{t('dialog.description')}</DialogDescription>
        </DialogHeader>
        <form
          className="flex flex-col gap-3"
          onSubmit={form.handleSubmit((v) => {
            // Vendor sinks need their tower credential at creation (the module
            // rejects the create otherwise); on edit empty = keep the sealed one.
            if (
              !isEdit &&
              sinkCredRequired(v.sink_kind) &&
              !v.sink_cred.trim()
            ) {
              form.setError('sink_cred', {
                type: 'required',
                message: t('dialog.sinkCredRequired'),
              })
              return
            }
            if (isEdit) updateMut.mutate(v)
            else createMut.mutate(v)
          })}
        >
          <Field
            label={t('dialog.name')}
            required
            error={form.formState.errors.name?.message}
          >
            {({ id }) => <Input id={id} {...form.register('name')} />}
          </Field>

          <Field
            label={t('dialog.endpoint')}
            required
            error={form.formState.errors.endpoint?.message}
          >
            {({ id }) => (
              <Input
                id={id}
                type="url"
                placeholder="https://"
                {...form.register('endpoint')}
              />
            )}
          </Field>

          <Field
            label={t('dialog.eventTypes')}
            description={t('dialog.eventTypesHint')}
            error={form.formState.errors.event_types?.message}
          >
            <div className="flex max-h-40 flex-col gap-1 overflow-y-auto rounded-md border border-border p-2">
              {eventTypesQ.data?.event_types.map((et) => (
                <label
                  key={et.type}
                  className="flex items-center gap-2 rounded px-1 py-0.5 text-sm hover:bg-muted"
                >
                  <Checkbox
                    checked={selectedEventTypes.includes(et.type)}
                    onCheckedChange={() => handleToggleEventType(et.type)}
                  />
                  <span className="font-mono text-xs">{et.type}</span>
                  {et.stability !== 'stable' ? (
                    <Badge variant="warning">{et.stability}</Badge>
                  ) : null}
                </label>
              )) ?? <p className="text-xs text-muted-foreground">Loading...</p>}
            </div>
          </Field>

          <Field
            label={t('dialog.matchSources')}
            description={t('dialog.matchSourcesHint')}
          >
            {({ id }) => <Input id={id} {...form.register('match_sources')} />}
          </Field>

          <div className="grid grid-cols-2 gap-3">
            <Field label={t('dialog.role')}>
              <Select
                value={form.watch('role')}
                onValueChange={(v) => form.setValue('role', v)}
              >
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {ROLES.map((r) => (
                    <SelectItem key={r} value={r}>
                      {r}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>

            <Field label={t('dialog.authType')}>
              <Select
                value={form.watch('auth_type')}
                onValueChange={(v) => form.setValue('auth_type', v as AuthType)}
              >
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {AUTH_TYPES.map((a) => (
                    <SelectItem key={a} value={a}>
                      {t(`subscriptions.authTypes.${a}`)}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>
          </div>

          {authType !== 'none' ? (
            <>
              <Field
                label={t('dialog.authValue')}
                description={t('dialog.authValueHint')}
              >
                {({ id }) => (
                  <Input
                    id={id}
                    type="password"
                    {...form.register('auth_value')}
                  />
                )}
              </Field>
              {authType === 'header' ? (
                <Field
                  label={t('dialog.authHeaderName')}
                  description={t('dialog.authHeaderNameHint')}
                >
                  {({ id }) => (
                    <Input id={id} {...form.register('auth_header_name')} />
                  )}
                </Field>
              ) : null}
            </>
          ) : null}

          {/* SIEM sink profile: shown for every subscription so an
              existing profile is VISIBLE and PRESERVED on edit — before this
              section existed, edit silently omitted sink_* and the backend
              deleted the profile (revert-to-generic-webhook semantics). */}
          <Field
            label={t('dialog.sinkKind')}
            description={t('dialog.sinkKindHint')}
          >
            <Select
              value={sinkKind || 'none'}
              onValueChange={(v) =>
                form.setValue('sink_kind', v === 'none' ? '' : v)
              }
            >
              <SelectTrigger>
                <SelectValue placeholder={t('dialog.sinkNone')} />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="none">{t('dialog.sinkNone')}</SelectItem>
                {SINK_KINDS.map((k) => (
                  <SelectItem key={k} value={k}>
                    {t(`dialog.sinkKinds.${k}`)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>

          {sinkKind ? (
            <>
              <Field
                label={t('dialog.sinkFormat')}
                description={t('dialog.sinkFormatHint')}
              >
                <Select
                  value={form.watch('sink_format') || 'default'}
                  onValueChange={(v) =>
                    form.setValue('sink_format', v === 'default' ? '' : v)
                  }
                >
                  <SelectTrigger>
                    <SelectValue placeholder={t('dialog.sinkFormatDefault')} />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="default">
                      {t('dialog.sinkFormatDefault')}
                    </SelectItem>
                    {SINK_FORMATS.map((f) => (
                      <SelectItem key={f} value={f}>
                        {f}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </Field>
              {sinkCredRequired(sinkKind) ? (
                <Field
                  label={t('dialog.sinkCred')}
                  description={
                    isEdit && existing?.sink_cred_hint
                      ? t('dialog.sinkCredKeepHint', {
                          hint: existing.sink_cred_hint,
                        })
                      : t('dialog.sinkCredHint')
                  }
                  required={!isEdit}
                  error={form.formState.errors.sink_cred?.message}
                >
                  {({ id }) => (
                    <Input
                      id={id}
                      type="password"
                      autoComplete="off"
                      {...form.register('sink_cred')}
                    />
                  )}
                </Field>
              ) : null}
            </>
          ) : null}

          <div className="grid grid-cols-2 gap-3">
            <Field
              label={t('dialog.maxAttempts')}
              description={t('dialog.maxAttemptsHint')}
              error={
                form.formState.errors.max_attempts?.message
                  ? t(form.formState.errors.max_attempts.message)
                  : undefined
              }
            >
              {({ id }) => (
                <Input
                  id={id}
                  type="number"
                  min="0"
                  max="20"
                  {...form.register('max_attempts')}
                />
              )}
            </Field>
            <Field
              label={t('dialog.initialInterval')}
              description={t('dialog.initialIntervalHint')}
              error={
                form.formState.errors.initial_interval_seconds?.message
                  ? t(form.formState.errors.initial_interval_seconds.message)
                  : undefined
              }
            >
              {({ id }) => (
                <Input
                  id={id}
                  type="number"
                  min="0"
                  max="3600"
                  {...form.register('initial_interval_seconds')}
                />
              )}
            </Field>
          </div>

          <Field label={t('dialog.description')}>
            {({ id }) => (
              <Textarea id={id} rows={2} {...form.register('description')} />
            )}
          </Field>

          <label className="flex items-center justify-between gap-2 rounded-md border border-border px-3 py-2">
            <span className="text-sm text-foreground">
              {t('dialog.enabled')}
            </span>
            <Switch
              checked={form.watch('enabled')}
              onCheckedChange={(v) => form.setValue('enabled', v)}
            />
          </label>

          <DialogFooter>
            <Button
              type="button"
              variant="ghost"
              onClick={() => onOpenChange(false)}
            >
              {t('common:actions.cancel')}
            </Button>
            <Button type="submit" variant="primary" disabled={pending}>
              {isEdit ? t('dialog.update') : t('dialog.create')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

export default EventingView
