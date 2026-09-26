// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { Link } from '@tanstack/react-router'
import { useCallback, useMemo, useRef, useState } from 'react'
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
import { useCoalescedRefresh } from './coalesce'
import { useOwnerLabel } from './owner-label'
import { useAuth } from '@/lib/auth/context'
import { useUrlState } from '@/lib/hooks/use-url-state'
import {
  getWorkItem,
  listWorkItems,
  workKeys,
  type ListWorkParams,
} from './api'
import { DecisionsPanel } from './decisions-panel'
import { ItemDetailSheet, type WorkDetailTab } from './item-detail'
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

/**
 * Address-bar keys this room owns. A reload, a pasted link and Back/Forward all
 * re-read the engine; nothing of the snapshot is kept only in React state.
 * `item` is the WorkItem id, `detail` the sheet tab, `tab` the page tab, and the
 * rest are the store-wide filters.
 */
const WORK_URL_KEYS = [
  'item',
  'tab',
  'status',
  'priority',
  'archived',
  'detail',
] as const

/** The engine's WorkItem id is a UUID (model.ParseID). Anything else in the
 * address is untrusted input, not a fetch. */
const ITEM_ID =
  /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i

const DETAIL_TABS: readonly WorkDetailTab[] = [
  'overview',
  'acceptance',
  'dependencies',
  'lease',
  'events',
]

function decodeItemId(raw: string | undefined): string | null {
  if (!raw) return null
  return ITEM_ID.test(raw) ? raw : null
}

function decodeDetailTab(raw: string | undefined): WorkDetailTab {
  return DETAIL_TABS.includes(raw as WorkDetailTab)
    ? (raw as WorkDetailTab)
    : 'overview'
}

function decodeArchived(raw: string | undefined): ArchivedFilter {
  return raw === 'true' || raw === 'false' || raw === 'any' ? raw : 'any'
}

export function WorkView() {
  const etiquetaDuenno = useOwnerLabel()
  const { t } = useTranslation('work')
  const { activeTenant, can } = useAuth()
  // Quién puede EMPEZAR trabajo: lanzar una sesión es lo que hace que aparezcan
  // unidades aquí, y es el permiso que el compositor del turno ya comprueba.
  const canStartWork = can('sessions:run:write')
  const qc = useQueryClient()

  const [url, patchUrl] = useUrlState(WORK_URL_KEYS)
  const status = url.status ?? ''
  const priority = url.priority ?? ''
  const archived = decodeArchived(url.archived)
  const openItem = decodeItemId(url.item)
  const invalidItem = Boolean(url.item && !openItem)
  const canReadDecisions = can('sessions:decision:read')
  const pageTab =
    url.tab === 'decisions' && canReadDecisions ? 'decisions' : 'items'
  const detailTab = decodeDetailTab(url.detail)
  /** Whether the reader narrowed the list themselves — the three controls above it. */
  const hayFiltro = status !== '' || priority !== '' || archived !== 'any'
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
  // ⛔ ONE BURST IS A BOUNDED NUMBER OF READS, AND IT USED TO BE ONE PER EVENT. The list
  //    query carries an abort signal, and `invalidateQueries` cancels a fetch still in
  //    flight before it starts the next one, so invalidating on every frame turned a
  //    burst of N events into N-1 aborted reads and one that answered: about sixty
  //    aborted reads on a single visit to /work. The key stays as wide as it is — the
  //    open item and the decisions must refresh too, and dropping the signal would only
  //    turn aborted reads into discarded ones. What changes is WHEN: the throttle in
  //    `coalesce.ts` is LEADING-plus-trailing, so the first event of a quiet period
  //    refreshes at once — the reason the stream is subscribed at all — and everything
  //    arriving inside the window collapses into one more refresh at its end, which
  //    reads the state AFTER the burst. A stream that never goes quiet costs two reads
  //    per window instead of one per event.
  //
  // ⛔ `activeTenant` VA EN LAS DEPENDENCIAS. Sin él la retrollamada se queda con el inquilino
  // del primer render: tras cambiar de inquilino, cada evento del flujo invalidaría la clave del
  // ANTERIOR y la lista que el operador está mirando no se refrescaría nunca. Lo señaló
  // `react-hooks/exhaustive-deps` en el mismo cambio que metió la variable.
  const refresh = useCallback(() => {
    void qc.invalidateQueries({ queryKey: workKeys.all(activeTenant) })
  }, [qc, activeTenant])
  // A window still open when the view unmounts, or when the tenant changes under it, is
  // abandoned instead of flushed: the key it would invalidate is no longer the one on
  // screen, and the new tenant's list is a new key that reads fresh on its own. The hook
  // keys that on the identity of `refresh`, which carries exactly that tenant.
  const onEvent = useCoalescedRefresh(refresh)
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
        <>
          {invalidItem ? (
            <p
              role="status"
              className="text-body text-muted-foreground"
              data-slot="work-url-invalid-item"
            >
              {t('url.invalidItem')}
            </p>
          ) : null}
          {/* The stream told us it could not look. That is NOT a disconnect and must not
             be shown as one: the list on screen may be stale in ways nothing else will
             reveal. */}
          {streamUnavailable ? (
            <UnavailableNotice code={streamUnavailable}>
              <p className="text-caption">{t('stream.unavailableBody')}</p>
            </UnavailableNotice>
          ) : null}
        </>
      }
    >
      <Tabs
        value={pageTab}
        onValueChange={(v) =>
          patchUrl({ tab: v === 'decisions' ? 'decisions' : undefined })
        }
      >
        <TabsList>
          <TabsTrigger value="items">{t('tabs.items')}</TabsTrigger>
          {canReadDecisions ? (
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
                  onValueChange={(v) =>
                    patchUrl({ status: v === 'all' ? undefined : v })
                  }
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
                  onValueChange={(v) =>
                    patchUrl({ priority: v === 'all' ? undefined : v })
                  }
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
                  onValueChange={(v) =>
                    patchUrl({
                      archived: v === 'any' ? undefined : v,
                    })
                  }
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
                  /* ⛔ «NADA COINCIDE CON ESTOS FILTROS» ES FALSO CUANDO NO HAY
                     FILTROS. Medido a 1440 sobre el motor sembrado: la ruta abre sin
                     filtro alguno y afirmaba que la lista vacía era el resultado de
                     uno. Son dos estados distintos y sólo uno tiene siguiente acción:
                     con filtro puesto, quitarlo; sin filtro, no hay nada que pulsar y
                     la frase dice de dónde vendrán las unidades. */
                  <EmptyState
                    icon={<Inbox />}
                    title={t(
                      hayFiltro ? 'items.empty.title' : 'items.empty.allTitle',
                    )}
                    description={t(
                      hayFiltro ? 'items.empty.body' : 'items.empty.allBody',
                    )}
                    /* Con filtro, la siguiente acción es quitarlo. SIN filtro, la
                       siguiente acción es EMPEZAR trabajo: las unidades aparecen aquí
                       según las registran las sesiones, así que la puerta es donde se
                       lanza una — y sólo se ofrece a quien puede lanzarla, que no se
                       encuentre con un 403 al otro lado. */
                    action={
                      hayFiltro ? (
                        <Button
                          variant="secondary"
                          size="sm"
                          onClick={() =>
                            patchUrl({
                              status: undefined,
                              priority: undefined,
                              archived: undefined,
                            })
                          }
                        >
                          {t('filters.clear')}
                        </Button>
                      ) : canStartWork ? (
                        <Button variant="primary" size="sm" asChild>
                          <Link to={'/sessions' as never}>
                            {t('items.empty.start')}
                          </Link>
                        </Button>
                      ) : null
                    }
                  />
                ) : (
                  <>
                    <ul
                      className="flex flex-col divide-y divide-border"
                      data-slot="work-list"
                    >
                      {page.items.map((item) => (
                        <li key={item.id}>
                          <button
                            type="button"
                            data-slot="work-item-row"
                            aria-current={
                              openItem === item.id ? 'true' : undefined
                            }
                            onClick={() => patchUrl({ item: item.id })}
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
        detailTab={detailTab}
        onDetailTabChange={(tab) =>
          patchUrl({ detail: tab === 'overview' ? undefined : tab })
        }
        onOpenChange={(open) => {
          if (!open) patchUrl({ item: undefined, detail: undefined })
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
