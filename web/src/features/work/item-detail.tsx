// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { useOwnerLabel } from './owner-label'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { useAuth } from '@/lib/auth/context'
import {
  acceptanceEvaluateBody,
  buildIntent,
  getLease,
  getWorkItem,
  listDependencies,
  listWorkEvents,
  workKeys,
  type WorkCommandName,
  type WorkIntent,
} from './api'
import { ApplyFlow } from './apply-flow'
import { StatusBadge } from './status-badge'
import { ListTruncationBadge } from '@/features/_intel'
import { WorkSection } from './work-section'
import type { AcceptanceCriterion, WorkItem, WorkSnapshot } from './types'

/** The FSM transitions the engine accepts, mirroring validRouteBodyCommand
 * (work_api.go:263-270). `item.archive` and `item.fail` additionally require admin, and
 * the engine re-authorizes them per resource (work_api.go:185-193). */
const TRANSITIONS = [
  'item.ready',
  'item.block',
  'item.unblock',
  'item.submit',
  'item.complete',
  'item.cancel',
] as const
const ADMIN_TRANSITIONS = ['item.fail', 'item.archive'] as const

export function ItemDetailSheet({
  itemId,
  onOpenChange,
}: {
  itemId: string | null
  onOpenChange: (open: boolean) => void
}) {
  const { t } = useTranslation('work')
  const { activeTenant } = useAuth()
  const queryClient = useQueryClient()
  const [intent, setIntent] = useState<WorkIntent | null>(null)
  // Which verdict is being recorded, so the dialog knows WHICH evidence the engine will
  // demand (passed needs a hash, failed only a ref, waived neither).
  const [acceptanceState, setAcceptanceState] = useState<
    'passed' | 'failed' | 'waived' | undefined
  >(undefined)

  const query = useQuery({
    queryKey: workKeys.item(activeTenant, itemId ?? ''),
    queryFn: ({ signal }) =>
      getWorkItem(itemId as string, { tenant: activeTenant }, signal),
    enabled: !!itemId,
  })

  const refreshIntentTenant = (operation: WorkIntent | null) => {
    if (!operation) return
    void queryClient.invalidateQueries({
      queryKey: workKeys.all(operation.tenant),
    })
    if (activeTenant === operation.tenant) void query.refetch()
  }

  return (
    <Sheet open={!!itemId} onOpenChange={onOpenChange}>
      <SheetContent className="max-w-2xl overflow-y-auto">
        <WorkSection query={query}>
          {({ snapshot, etag }) => (
            <>
              <SheetHeader>
                <SheetTitle className="flex items-center gap-2">
                  {snapshot.item.title}
                  <StatusBadge item={snapshot.item} />
                </SheetTitle>
                <SheetDescription>
                  {t('detail.subtitle', {
                    kind: snapshot.item.work_kind,
                    version: snapshot.item.version,
                  })}
                </SheetDescription>
              </SheetHeader>

              <Tabs defaultValue="overview" className="mt-4">
                <TabsList>
                  <TabsTrigger value="overview">
                    {t('detail.tabs.overview')}
                  </TabsTrigger>
                  <TabsTrigger value="acceptance">
                    {t('detail.tabs.acceptance')}
                  </TabsTrigger>
                  <TabsTrigger value="dependencies">
                    {t('detail.tabs.dependencies')}
                  </TabsTrigger>
                  <TabsTrigger value="lease">
                    {t('detail.tabs.lease')}
                  </TabsTrigger>
                  <TabsTrigger value="events">
                    {t('detail.tabs.events')}
                  </TabsTrigger>
                </TabsList>

                <TabsContent value="overview">
                  <OverviewTab
                    item={snapshot.item}
                    etag={etag}
                    onIntent={setIntent}
                  />
                </TabsContent>
                <TabsContent value="acceptance">
                  <AcceptanceTab
                    snapshot={snapshot}
                    etag={etag}
                    onIntent={setIntent}
                    onAcceptanceState={setAcceptanceState}
                  />
                </TabsContent>
                <TabsContent value="dependencies">
                  <DependenciesTab itemId={snapshot.item.id} />
                </TabsContent>
                <TabsContent value="lease">
                  <LeaseTab
                    itemId={snapshot.item.id}
                    etag={etag}
                    onIntent={setIntent}
                  />
                </TabsContent>
                <TabsContent value="events">
                  <EventsTab itemId={snapshot.item.id} />
                </TabsContent>
              </Tabs>

              <ApplyFlow
                open={intent !== null}
                onOpenChange={(open) => {
                  if (!open) {
                    setIntent(null)
                    setAcceptanceState(undefined)
                  }
                }}
                intent={intent}
                acceptanceState={acceptanceState}
                title={t('detail.applyTitle')}
                onApplied={() => refreshIntentTenant(intent)}
                // A version conflict is resolved by RE-READING, which is exactly this.
                onReread={() => refreshIntentTenant(intent)}
              />
            </>
          )}
        </WorkSection>
      </SheetContent>
    </Sheet>
  )
}

function OverviewTab({
  item,
  etag,
  onIntent,
}: {
  item: WorkItem
  etag: string | null
  onIntent: (i: WorkIntent) => void
}) {
  const { t } = useTranslation('work')
  const etiquetaDuenno = useOwnerLabel()
  const { activeTenant, can } = useAuth()
  const canWrite = can('sessions:work:write')
  const canAdmin = can('sessions:work:admin')

  return (
    <div className="flex flex-col gap-4 py-4">
      {item.brief_md ? (
        <p className="whitespace-pre-wrap text-sm">{item.brief_md}</p>
      ) : null}

      <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5 text-sm">
        <dt className="text-muted-foreground">{t('detail.owner')}</dt>
        <dd className="font-mono text-xs">
          {etiquetaDuenno(item.owner_kind, item.owner_ref)}
        </dd>
        <dt className="text-muted-foreground">{t('detail.provenance')}</dt>
        <dd className="font-mono text-xs">
          {item.provenance_kind}:{item.provenance_ref}
        </dd>
        <dt className="text-muted-foreground">{t('detail.priority')}</dt>
        <dd>
          <Badge variant="outline">{item.priority}</Badge>
        </dd>
        {item.blocked_code ? (
          <>
            <dt className="text-muted-foreground">{t('detail.blocked')}</dt>
            <dd className="text-xs">
              {item.blocked_code}
              {item.blocked_reason ? ` — ${item.blocked_reason}` : ''}
            </dd>
          </>
        ) : null}
        {item.terminal_code ? (
          <>
            <dt className="text-muted-foreground">{t('detail.terminal')}</dt>
            <dd className="text-xs">
              {item.terminal_code}
              {item.terminal_reason ? ` — ${item.terminal_reason}` : ''}
            </dd>
          </>
        ) : null}
        <dt className="text-muted-foreground">{t('detail.etag')}</dt>
        <dd className="font-mono text-xs">{etag ?? '—'}</dd>
      </dl>

      {canWrite ? (
        <div className="flex flex-wrap gap-2 border-t border-border pt-4">
          {TRANSITIONS.map((cmd) => (
            <Button
              key={cmd}
              variant="outline"
              size="sm"
              onClick={() =>
                onIntent(
                  buildIntent({
                    tenant: activeTenant,
                    command: cmd,
                    itemId: item.id,
                    // The ETag comes from the server's header, never rebuilt from
                    // `version`: see getWorkItem.
                    etag,
                  }),
                )
              }
            >
              {t(`transition.${cmd}`)}
            </Button>
          ))}
          {canAdmin
            ? ADMIN_TRANSITIONS.map((cmd) => (
                <Button
                  key={cmd}
                  variant="outline"
                  size="sm"
                  onClick={() =>
                    onIntent(
                      buildIntent({
                        tenant: activeTenant,
                        command: cmd,
                        itemId: item.id,
                        etag,
                      }),
                    )
                  }
                >
                  {t(`transition.${cmd}`)}
                </Button>
              ))
            : null}
        </div>
      ) : null}
    </div>
  )
}

/**
 * ACCEPTANCE CRITERIA — where the console must respect a rule it cannot see in the data.
 *
 * The contract: "acceptance.update permite corregir statement, ordinal y obligatoriedad
 * SÓLO mientras el item está en draft. La clave del criterio es identidad durable e
 * inmutable; una evaluación posterior usa acceptance.evaluate."
 *
 * Two consequences, both structural here rather than advisory:
 *
 *  1. EDIT IS OFFERED ONLY IN DRAFT. Outside draft the engine refuses acceptance.update,
 *    so offering the button would produce a 409 the operator cannot act on — and, worse,
 *    would teach them that criteria are editable mid-flight. Outside draft the only
 *    offer is EVALUATE.
 *  2. THE KEY IS NEVER EDITABLE. `criterion_key` is durable, immutable identity. It is
 *    rendered as identity — monospace, alongside the id — and there is no input bound to
 *    it anywhere in this feature. Treating it as reassignable would silently re-point
 *    evidence at a different criterion.
 */
function AcceptanceTab({
  snapshot,
  etag,
  onIntent,
  onAcceptanceState,
}: {
  snapshot: WorkSnapshot
  etag: string | null
  onIntent: (i: WorkIntent) => void
  onAcceptanceState: (s: 'passed' | 'failed' | 'waived') => void
}) {
  const { t } = useTranslation('work')
  const { activeTenant, can } = useAuth()
  const canWrite = can('sessions:work:write')
  const isDraft = snapshot.item.status === 'draft'
  const criteria = snapshot.acceptance ?? []

  return (
    <div className="flex flex-col gap-3 py-4">
      <p className="text-xs text-muted-foreground">
        {isDraft ? t('acceptance.draftNotice') : t('acceptance.lockedNotice')}
      </p>

      {criteria.length === 0 ? (
        <p className="text-sm text-muted-foreground">{t('acceptance.none')}</p>
      ) : (
        <ul className="flex flex-col divide-y divide-border">
          {criteria.map((c) => (
            <AcceptanceRow
              key={c.id}
              criterion={c}
              isDraft={isDraft}
              canWrite={canWrite}
              onEvaluate={(state) => {
                onAcceptanceState(state)
                onIntent(
                  buildIntent({
                    tenant: activeTenant,
                    command: 'acceptance.evaluate',
                    itemId: snapshot.item.id,
                    criterionId: c.id,
                    // The engine wants an acceptance ARRAY, not a bare state
                    // (work_state.go:318-321). The evidence fields are collected by
                    // the dialog and folded into this same element.
                    body: acceptanceEvaluateBody(state, '', '', ''),
                    etag,
                  }),
                )
              }}
            />
          ))}
        </ul>
      )}
    </div>
  )
}

function AcceptanceRow({
  criterion,
  isDraft,
  canWrite,
  onEvaluate,
}: {
  criterion: AcceptanceCriterion
  isDraft: boolean
  canWrite: boolean
  onEvaluate: (state: 'passed' | 'failed' | 'waived') => void
}) {
  const { t } = useTranslation('work')
  const stateVariant =
    criterion.state === 'passed'
      ? 'success'
      : criterion.state === 'failed'
        ? 'danger'
        : criterion.state === 'waived'
          ? 'warning'
          : 'neutral'

  return (
    <li className="flex items-start justify-between gap-4 py-3">
      <div className="min-w-0 space-y-1">
        <div className="flex flex-wrap items-center gap-2">
          {/* Identity, rendered as identity. Never an input. */}
          <code className="font-mono text-xs">{criterion.criterion_key}</code>
          <Badge variant={stateVariant}>
            {t(`acceptance.state.${criterion.state}`)}
          </Badge>
          {criterion.required ? (
            <Badge variant="outline">{t('acceptance.required')}</Badge>
          ) : null}
        </div>
        <p className="text-sm">{criterion.statement}</p>
        {criterion.evidence_ref ? (
          <p className="font-mono text-xs text-muted-foreground">
            {criterion.evidence_ref}
          </p>
        ) : null}
      </div>

      {canWrite && !isDraft ? (
        <div className="flex shrink-0 gap-1">
          {(['passed', 'failed', 'waived'] as const).map((s) => (
            <Button
              key={s}
              variant="outline"
              size="sm"
              onClick={() => onEvaluate(s)}
            >
              {t(`acceptance.state.${s}`)}
            </Button>
          ))}
        </div>
      ) : null}
      {/* In draft the engine allows acceptance.update, but editing statement/ordinal is
          an authoring flow rather than an operational one, and this cockpit does not
          author criteria. Stating that is better than an "edit" button that only works
          in one status — see the session note for the residue. */}
    </li>
  )
}

function DependenciesTab({ itemId }: { itemId: string }) {
  const { t } = useTranslation('work')
  const { activeTenant } = useAuth()
  const query = useQuery({
    queryKey: workKeys.dependencies(activeTenant, itemId),
    queryFn: ({ signal }) =>
      listDependencies(itemId, { tenant: activeTenant }, signal),
  })
  return (
    <div className="py-4">
      <WorkSection query={query}>
        {(page) =>
          page.items.length === 0 ? (
            <p className="text-sm text-muted-foreground">
              {t('dependencies.none')}
            </p>
          ) : (
            <>
            <ul className="flex flex-col divide-y divide-border">
              {page.items.map((d) => (
                <li key={d.id} className="py-2 font-mono text-xs">
                  {d.depends_on_id}
                </li>
              ))}
            </ul>
              {/* Keyset: `queryLimit` sirve CIEN por omision y rechaza 400 por encima de 200
                  (modules/sessions/work_api.go:798-807), asi que no hay techo que complete la
                  lista. La cifra es la CARGADA, no el limite pedido. */}
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
    </div>
  )
}

/**
 * The lease tab (C07-01). The console used to paint the derived `leased` boolean and offer
 * nothing else; these are the eight routes the engine registers at
 * `modules/sessions/work_api.go:42-49`, with the THREE permission tiers it declares.
 *
 * ⛔ THE PERMISSION STRINGS ARE THE ENGINE'S OWN (`modules/sessions/api.go:31-33`), not
 * invented here. `lint:console-perms` states why that matters and it is not a style rule: a
 * permission the engine declares NOWHERE can be in no effective set, so the action would be
 * hidden for every role, for ever, with NO 403 to notice it by. A wrong string does not
 * produce an error — it produces a mute screen.
 *
 * ⛔ AND THE LIVENESS IS RENDERED WITH THREE OUTCOMES, NEVER TWO. `NO_HE_PODIDO_MIRAR` is not
 * "not live": it is the engine saying it could not look, and collapsing it into the negative
 * is the single defect this repository has spent the most closing. `live` is only consulted
 * when the verdict actually is an observation.
 */
export function LeaseTab({
  itemId,
  etag,
  onIntent,
}: {
  itemId: string
  etag: string | null
  onIntent: (i: WorkIntent) => void
}) {
  const { t } = useTranslation('work')
  const { activeTenant, can } = useAuth()
  const canRead = can('sessions:lease:read')
  const canWrite = can('sessions:lease:write')
  const canAdmin = can('sessions:lease:admin')

  // ⛔ LA LECTURA TAMBIÉN ESTÁ GATEADA. El motor registra los dos GET con
  // `sessions:lease:read` (`work_api.go:42-43`), así que pedirlos sin el permiso no es «enseñar
  // de más»: es provocar un 403 que la pantalla tendría que explicar, y a un rol que no debía
  // ver ni la pregunta. Lo señaló el contraste `sol max` del 2026-08-16 — es la mitad que se
  // olvida cuando uno gatea sólo los botones.
  const query = useQuery({
    queryKey: workKeys.lease(activeTenant, itemId),
    queryFn: ({ signal }) => getLease(itemId, { tenant: activeTenant }, signal),
    enabled: canRead,
  })

  /**
   * ⛔ THE BUTTONS RAISE AN INTENT; THEY DO NOT POST. Measured against a live engine on
   * 2026-08-16: a direct POST to .../lease/acquire answers **400 mode_required**, because the
   * lease commands run through handleWorkMutation like every other work mutation
   * (work_api.go:110-131) and therefore need ?mode=, If-Plan-Hash and an Idempotency-Key.
   *
   * The first version of this tab posted directly and its 34 unit cells stayed green, because
   * a mocked fetch answers whatever it is told. Only opening the screen against the engine
   * showed six buttons that did nothing (canon §1.10). Going through the intent also gives
   * them what the page itself promises three lines above: the plan, shown before it is applied.
   */
  const action = (
    command: WorkCommandName,
    label: string,
    allowed: boolean,
  ) => (
    <Button
      key={command}
      size="sm"
      variant="outline"
      disabled={!allowed}
      onClick={() =>
        onIntent(
          // The ETag comes from the item read, never rebuilt from `lease.version`: the engine
          // returns the PARENT item's version on the lease read on purpose (work_api.go:643-645).
          // ⛔ EL ETag DEL LEASE, NO EL DEL ÍTEM. Los dos son la versión del WorkItem padre —el
          // motor lo devuelve así a propósito (`work_api.go:643-645`)— pero el del lease es la
          // lectura que el operador está MIRANDO, y por tanto la más fresca. Con el del ítem, una
          // escritura ajena entre ambas lecturas se descubre como un 412 en el apply en vez de
          // como una divergencia visible. Señalado por el contraste.
          buildIntent({
            tenant: activeTenant,
            command,
            itemId,
            etag: query.data?.etag ?? etag,
            // El titular que la pantalla YA muestra. `acquire` no lo lleva a propósito: sobre un
            // lease vacante no hay titular que heredar, y sembrarlo con el del lease anterior
            // sería afirmar algo que no consta.
            body:
              command !== 'lease.acquire' && query.data?.lease.holder_sid
                ? { holder_sid: query.data.lease.holder_sid }
                : undefined,
          }),
        )
      }
    >
      {label}
    </Button>
  )

  if (!canRead) {
    // Una tabla vacía y «no tienes permiso» se ven igual, y sólo una de las dos es cierta.
    return (
      <p className="py-4 text-sm text-muted-foreground">{t('lease.noRead')}</p>
    )
  }

  return (
    <div className="py-4">
      <WorkSection query={query}>
        {({ lease }) => (
          <div className="flex flex-col gap-4">
            <dl className="grid grid-cols-2 gap-x-6 gap-y-2 text-sm">
              <dt className="text-muted-foreground">{t('lease.state')}</dt>
              <dd className="font-mono text-xs">{lease.state}</dd>
              <dt className="text-muted-foreground">{t('lease.holder')}</dt>
              <dd className="font-mono text-xs">{lease.holder_sid || '—'}</dd>
              <dt className="text-muted-foreground">{t('lease.fence')}</dt>
              <dd className="font-mono text-xs">{lease.fence}</dd>
              <dt className="text-muted-foreground">{t('lease.expires')}</dt>
              <dd className="font-mono text-xs">{lease.expires_at || '—'}</dd>
              <dt className="text-muted-foreground">{t('lease.renewals')}</dt>
              <dd className="font-mono text-xs">{lease.renewal_count}</dd>
              <dt className="text-muted-foreground">{t('lease.liveness')}</dt>
              <dd>
                {/* THREE outcomes. The third is its own answer, not the negative one. */}
                {lease.liveness_verdict === 'NO_HE_PODIDO_MIRAR' ? (
                  <Badge variant="warning">{t('lease.unknown')}</Badge>
                ) : lease.live ? (
                  <Badge variant="success">{t('lease.live')}</Badge>
                ) : (
                  <Badge variant="neutral">{t('lease.notLive')}</Badge>
                )}
              </dd>
            </dl>

            <div className="flex flex-wrap gap-2">
              {action('lease.acquire', t('lease.actions.acquire'), canWrite)}
              {action('lease.renew', t('lease.actions.renew'), canWrite)}
              {action('lease.release', t('lease.actions.release'), canWrite)}
              {action('lease.takeover', t('lease.actions.takeover'), canAdmin)}
              {action('lease.revoke', t('lease.actions.revoke'), canAdmin)}
              {action(
                'lease.clock_rebase',
                t('lease.actions.clockRebase'),
                canAdmin,
              )}
            </div>

            {/* Saying WHY a control is inert beats a greyed button with no reason. */}
            {!canWrite && (
              <p className="text-xs text-muted-foreground">
                {t('lease.noWrite')}
              </p>
            )}
            {canWrite && !canAdmin && (
              <p className="text-xs text-muted-foreground">
                {t('lease.noAdmin')}
              </p>
            )}
          </div>
        )}
      </WorkSection>
    </div>
  )
}

function EventsTab({ itemId }: { itemId: string }) {
  const { t } = useTranslation('work')
  const { activeTenant } = useAuth()
  const query = useQuery({
    queryKey: workKeys.events(activeTenant, itemId),
    queryFn: ({ signal }) =>
      listWorkEvents(itemId, { limit: 100 }, { tenant: activeTenant }, signal),
  })
  return (
    <div className="py-4">
      <WorkSection query={query}>
        {(page) =>
          page.items.length === 0 ? (
            <p className="text-sm text-muted-foreground">{t('events.none')}</p>
          ) : (
            <>
            <ul className="flex flex-col divide-y divide-border">
              {page.items.map((e) => (
                <li
                  key={e.id}
                  className="flex items-center justify-between gap-3 py-2"
                >
                  <code className="font-mono text-xs">{e.type}</code>
                  <span className="text-xs text-muted-foreground">
                    #{e.seq} · {e.occurred_at}
                  </span>
                </li>
              ))}
            </ul>
              {/* ⚠ Este llamante pasa `limit: 100`, que es EXACTAMENTE el valor por omision del
                  motor: no protege de nada. Subirlo a 200 —el maximo que `queryLimit` acepta antes
                  de responder 400— es otro cambio; lo honesto AHORA es que la pantalla lo diga. */}
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
    </div>
  )
}
