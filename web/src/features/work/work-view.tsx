// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { ClipboardList, Inbox } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { EmptyState } from '@/components/ui/empty-state'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { IntelPage, ListTruncationBadge, SectionCard } from '@/features/_intel'
import { LiveDot } from '@/features/shared'
import {
  HandoffOfferHost,
  type HandoffOfferTarget,
  type HandoffWorkItemView,
} from '@/features/communications'
import { useOwnerLabel } from './owner-label'
import { useAuth } from '@/lib/auth/context'
import {
  getWorkItem,
  listWorkItems,
  workKeys,
  type ListWorkParams,
} from './api'
import { DecisionsPanel } from './decisions-panel'
import { ItemDetailSheet } from './item-detail'
import { StatusBadge } from './status-badge'
import { useWorkStream } from './stream'
import { WorkSection } from './work-section'
import { UnavailableNotice } from './verdict'
import type { WorkStatus } from './types'
import './i18n'

/**
 * THE WORK COCKPIT — the console surface over the K1 cross-session work kernel.
 *
 * It renders the engine's governed API and adds no logic of its own (ARCHITECTURE.md). What
 * it does add is REFUSALS: several places where the obvious UI would assert something
 * the kernel declines to assert. Each is commented where it lives; the two on this
 * screen are the archived tri-state below and the live stream's unavailable notice.
 *
 * PERMISSIONS ARE PURE SET MEMBERSHIP (#578). can() is a lookup in the effective set
 * /v1/auth/whoami serves — no verb arithmetic, no "admin implies write", no exceptions
 * wired in for this feature. Measured on the wire for all six of this cockpit's
 * permissions by cmd/olivares/work_console_whoami_reach_test.go.
 */
const STATUSES: WorkStatus[] = [
  'draft',
  'ready',
  'active',
  'blocked',
  'review',
  'completed',
  'failed',
  'canceled',
]

/** The archived filter's THREE states, in the engine's own terms. `any` is not a
 * convenience default — it is the engine's behaviour when the key is absent. */
type ArchivedFilter = 'any' | 'false' | 'true'

/** How long the stream must stay quiet before one burst of events becomes one read. */
const WORK_REFRESH_QUIET_MS = 150
/** The longest a stream that never goes quiet may keep the list from refreshing. */
const WORK_REFRESH_CEILING_MS = 1000

/** The two timers of a coalesced refresh: the quiet window and its ceiling. */
type PendingRefresh = { quiet: number | null; ceiling: number | null }

function clearPendingRefresh(timers: PendingRefresh) {
  if (timers.quiet !== null) window.clearTimeout(timers.quiet)
  if (timers.ceiling !== null) window.clearTimeout(timers.ceiling)
  timers.quiet = null
  timers.ceiling = null
}

export function WorkView() {
  const etiquetaDuenno = useOwnerLabel()
  const { t } = useTranslation('work')
  const { activeTenant, can } = useAuth()
  const qc = useQueryClient()

  const [status, setStatus] = useState<string>('')
  const [priority, setPriority] = useState<string>('')
  const [archived, setArchived] = useState<ArchivedFilter>('any')
  const [openItem, setOpenItem] = useState<string | null>(null)
  const [streamUnavailable, setStreamUnavailable] = useState<string | null>(
    null,
  )
  /**
   * K3 I3 — the offer TARGET, and why it carries an invocation counter rather than
   * just an id. Reopening the same item has to be a NEW explicit editor action: with
   * an id alone, closing the offer dialog and pressing the button again would look
   * identical to the state that is already set, so nothing would reopen. The counter
   * makes "again" expressible.
   */
  const [offerTarget, setOfferTarget] = useState<HandoffOfferTarget | null>(
    null,
  )
  const offerInvocation = useRef(0)
  /** The item-sheet control that opened the offer dialog, captured at the click. */
  const offerOpener = useRef<HTMLElement | null>(null)
  /** A visible control of this view, used when the opener is gone on close. */
  const refreshRef = useRef<HTMLButtonElement | null>(null)

  const params: ListWorkParams = useMemo(
    () => ({
      status: status || undefined,
      priority: priority || undefined,
      /**
       * TRI-STATE, NOT A BOOLEAN WITH A DEFAULT. The engine maps archived=false to
       * `archived_at IS NULL`, archived=true to `IS NOT NULL`, and ABSENT to neither.
       *
       * Defaulting to `false` — which is what almost every list UI does — would
       * quietly redefine the list the operator believes they are reading: archived
       * work would vanish behind a filter nobody chose and the count would not match
       * the store. So 'any' sends nothing at all, and it is the default.
       */
      archived: archived === 'any' ? undefined : archived === 'true',
      limit: 100,
    }),
    [status, priority, archived],
  )

  const query = useQuery({
    queryKey: workKeys.items(activeTenant, params),
    queryFn: ({ signal }) =>
      listWorkItems(params, { tenant: activeTenant }, signal),
  })

  // The durable event stream keeps the list honest without polling. Any work event
  // invalidates the whole work key — the list, the open item with its lease and events,
  // the decisions — and the stream's own cursor handles resume (stream.ts).
  //
  // ⛔ ONE BURST IS ONE READ. The list query carries an abort signal, and
  //    `invalidateQueries` cancels a fetch still in flight before it starts the next one,
  //    so invalidating on every frame turned a burst of N events into N-1 aborted reads
  //    and one that answered: about sixty aborted reads per visit to /work, measured.
  //    The key stays as wide as it is — the open item and the decisions must refresh too,
  //    and dropping the signal would only turn aborted reads into discarded ones. What
  //    changes is WHEN: the invalidation fires on the trailing edge of a quiet window, so
  //    the read sees the state AFTER the burst and never one from its middle, and a
  //    stream that never goes quiet still refreshes at the ceiling.
  //
  // ⛔ `activeTenant` VA EN LAS DEPENDENCIAS. Sin él la retrollamada se queda con el inquilino
  // del primer render: tras cambiar de inquilino, cada evento del flujo invalidaría la clave del
  // ANTERIOR y la lista que el operador está mirando no se refrescaría nunca. Lo señaló
  // `react-hooks/exhaustive-deps` en el mismo cambio que metió la variable.
  const pending = useRef<{ quiet: number | null; ceiling: number | null }>({
    quiet: null,
    ceiling: null,
  })
  const refresh = useCallback(() => {
    clearPendingRefresh(pending.current)
    void qc.invalidateQueries({ queryKey: workKeys.all(activeTenant) })
  }, [qc, activeTenant])
  const onEvent = useCallback(() => {
    const timers = pending.current
    if (timers.quiet !== null) window.clearTimeout(timers.quiet)
    timers.quiet = window.setTimeout(refresh, WORK_REFRESH_QUIET_MS)
    if (timers.ceiling === null)
      timers.ceiling = window.setTimeout(refresh, WORK_REFRESH_CEILING_MS)
  }, [refresh])
  // A refresh still pending when the view unmounts, or when the tenant changes under it,
  // is dropped: the key it would invalidate is no longer the one on screen, and the new
  // tenant's list is a new key that reads fresh on its own.
  useEffect(() => {
    const timers = pending.current
    return () => clearPendingRefresh(timers)
  }, [refresh])
  const { status: streamStatus } = useWorkStream({
    enabled: can('sessions:work:read'),
    onEvent,
    onUnavailable: setStreamUnavailable,
  })

  /**
   * The work-side adapter: one fresh, uncached `GET /work-items/{id}` under the
   * explicitly captured tenant, projected down to what communications may see.
   *
   * The tenant is a closure value, not a lookup at call time, so an operation begun
   * in one tenant cannot read an item in the next. A missing ETag is returned as
   * missing rather than rebuilt from `item.version`: without a validator there is
   * no precondition to offer under, and the host refuses to confirm.
   */
  const readItemForHandoff = useCallback(
    async (id: string, signal: AbortSignal): Promise<HandoffWorkItemView> => {
      const { snapshot, etag } = await getWorkItem(
        id,
        { tenant: activeTenant },
        signal,
      )
      return {
        item: {
          id: snapshot.item.id,
          workspace_id: snapshot.item.workspace_id,
          title: snapshot.item.title,
          status: snapshot.item.status,
          owner_kind: snapshot.item.owner_kind,
          owner_ref: snapshot.item.owner_ref,
          owner_epoch: snapshot.item.owner_epoch,
        },
        etag,
      }
    },
    [activeTenant],
  )

  return (
    <IntelPage
      icon={ClipboardList}
      title={t('title')}
      description={t('subtitle')}
      actions={
        <div className="flex items-center gap-3">
          <LiveDot status={streamStatus} />
          <Button
            ref={refreshRef}
            variant="outline"
            size="sm"
            onClick={() => void query.refetch()}
          >
            {t('common.refresh')}
          </Button>
        </div>
      }
      notices={
        /* The stream told us it could not look. That is NOT a disconnect and must not
           be shown as one: the list on screen may be stale in ways nothing else will
           reveal. */
        streamUnavailable ? (
          <UnavailableNotice code={streamUnavailable}>
            <p className="text-caption">{t('stream.unavailableBody')}</p>
          </UnavailableNotice>
        ) : null
      }
    >
      <Tabs defaultValue="items">
        <TabsList>
          <TabsTrigger value="items">{t('tabs.items')}</TabsTrigger>
          {can('sessions:decision:read') ? (
            <TabsTrigger value="decisions">{t('tabs.decisions')}</TabsTrigger>
          ) : null}
        </TabsList>

        <TabsContent value="items" className="mt-4">
          <SectionCard
            title={t('items.title')}
            description={t('items.subtitle')}
            actions={
              /* ⛔ LOS TRES FILTROS LLEVAN `aria-label`, y no es decoración: axe los marcaba
                 como `button-name` — el ÚNICO bloqueante axe de las 56 rutas × 2 temas. El
                 `placeholder` del `SelectValue` NO es un nombre accesible: desaparece en cuanto
                 hay valor elegido, así que con un filtro puesto el lector de pantalla anuncia
                 tres botones sin nombre. El tercero ni siquiera tenía placeholder. */
              <div className="flex flex-wrap gap-2">
                <Select
                  value={status || 'all'}
                  onValueChange={(v) => setStatus(v === 'all' ? '' : v)}
                >
                  <SelectTrigger
                    className="w-40"
                    aria-label={t('filters.status')}
                  >
                    <SelectValue placeholder={t('filters.status')} />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="all">
                      {t('filters.allStatus')}
                    </SelectItem>
                    {STATUSES.map((s) => (
                      <SelectItem key={s} value={s}>
                        {t(`status.${s}`)}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>

                <Select
                  value={priority || 'all'}
                  onValueChange={(v) => setPriority(v === 'all' ? '' : v)}
                >
                  <SelectTrigger
                    className="w-32"
                    aria-label={t('filters.priority')}
                  >
                    <SelectValue placeholder={t('filters.priority')} />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="all">
                      {t('filters.allPriority')}
                    </SelectItem>
                    {(['p0', 'p1', 'p2', 'p3'] as const).map((p) => (
                      <SelectItem key={p} value={p}>
                        {p}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>

                {/* All THREE archived states are offered, and the labels say what each
                    one means rather than reading as an on/off switch. */}
                <Select
                  value={archived}
                  onValueChange={(v) => setArchived(v as ArchivedFilter)}
                >
                  <SelectTrigger
                    className="w-44"
                    aria-label={t('filters.archived.label')}
                  >
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="any">
                      {t('filters.archived.any')}
                    </SelectItem>
                    <SelectItem value="false">
                      {t('filters.archived.false')}
                    </SelectItem>
                    <SelectItem value="true">
                      {t('filters.archived.true')}
                    </SelectItem>
                  </SelectContent>
                </Select>
              </div>
            }
          >
            <WorkSection query={query}>
              {(page) =>
                page.items.length === 0 ? (
                  <EmptyState
                    icon={<Inbox />}
                    title={t('items.empty.title')}
                    description={t('items.empty.body')}
                  />
                ) : (
                  <>
                    <ul className="flex flex-col divide-y divide-border">
                      {page.items.map((item) => (
                        <li key={item.id}>
                          <button
                            type="button"
                            onClick={() => setOpenItem(item.id)}
                            className="flex w-full items-start justify-between gap-4 py-3 text-left hover:bg-muted/40"
                          >
                            <div className="min-w-0 space-y-1">
                              <p className="truncate text-body font-medium">
                                {item.title}
                              </p>
                              <p className="font-mono text-caption text-muted-foreground">
                                {item.work_kind} ·{' '}
                                {etiquetaDuenno(
                                  item.owner_kind,
                                  item.owner_ref,
                                )}
                              </p>
                            </div>
                            <StatusBadge item={item} />
                          </button>
                        </li>
                      ))}
                    </ul>
                    {/* ⛔ EL AVISO VA CON LA LISTA, no en otra parte de la pantalla. `/v1/m/sessions`
                        pagina por KEYSET: `queryLimit` (modules/sessions/work_api.go:798-807) sirve
                        CIEN por omision y rechaza con 400 por encima de 200, asi que aqui NO existe
                        un techo que complete la lista — subir el numero solo agranda la primera
                        pagina. Lo unico honesto es decir que viene recortada.
                        La cifra la compone el llamante a proposito: es la CARGADA (`items.length`),
                        no el limite que se pidio; interpolar la constante convertiria el aviso en
                        una medida inventada. */}
                    <ListTruncationBadge
                      query={query}
                      label={t('truncation.label', {
                        n: query.data?.items?.length,
                      })}
                      hint={t('truncation.hint')}
                    />
                  </>
                )
              }
            </WorkSection>
          </SectionCard>
        </TabsContent>

        <TabsContent value="decisions" className="mt-4">
          <DecisionsPanel />
        </TabsContent>
      </Tabs>

      <ItemDetailSheet
        itemId={openItem}
        onOpenChange={(open) => {
          if (!open) setOpenItem(null)
        }}
        onOffer={(itemId) => {
          offerInvocation.current += 1
          // Captured at the gesture: this control lives inside the item sheet,
          // where a focusin observer cannot see it.
          offerOpener.current =
            document.activeElement instanceof HTMLElement
              ? document.activeElement
              : null
          setOfferTarget({ itemId, invocation: offerInvocation.current })
        }}
      />
      {/* Mounted beside the item sheet and keyed by the communications scope, so
          an unresolved offer survives closing the sheet and dies with the context. */}
      <HandoffOfferHost
        target={offerTarget}
        readItem={readItemForHandoff}
        getOpener={() => offerOpener.current}
        getFallbackFocus={() => refreshRef.current}
        onTargetConsumed={() => setOfferTarget(null)}
      />
    </IntelPage>
  )
}
