// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useMemo, useState } from 'react'
import { useInfiniteQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Gavel, History, Info } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { EmptyState } from '@/components/ui/empty-state'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { SectionCard } from '@/features/_intel'
import { useAuth } from '@/lib/auth/context'
import {
  attributesCurrentState,
  buildIntent,
  listDecisions,
  workKeys,
  type DecisionView,
  type WorkIntent,
} from './api'
import type { WorkDecision, WorkPage } from './types'
import { ApplyFlow } from './apply-flow'
import { WorkSection } from './work-section'

/**
 * THE DECISION LEDGER — and the one screen in this feature that can tell a lie the
 * engine explicitly refused to tell.
 *
 * `GET /decisions` is TWO endpoints behind one path, and the contract is exact about it:
 * without `effective` or `revoked` it returns append-only HISTORY and "no atribuye
 * estado actual a filas históricas". With either boolean it returns the DecisionHead
 * projection, which does.
 *
 * ⇒ PAINTING AN "IN FORCE" BADGE ON A HISTORY ROW IS AN ASSERTION THE ENGINE DECLINES
 * TO MAKE. So the badge is not driven by which tab is open, nor by a local flag, but by
 * `state` — the field the engine itself only emits in the projection
 * (work_api.go:666). In the history view the column is not empty-because-unknown; it
 * says, in words, that this view does not attribute current state. An operator must be
 * able to tell "this decision is not in force" from "this view cannot tell you".
 *
 * THE CURSOR ORDERS HEADS, NOT DECISIONS (work_api.go:607-612). It is opaque and it is
 * NOT interchangeable between the two views: a head-row id handed to the history scan
 * would resume at an unrelated place in a different table. So switching view resets it,
 * and that reset is deliberate rather than incidental — see onViewChange.
 */
export function DecisionsPanel({ workItemId }: { workItemId?: string }) {
  const { t } = useTranslation('work')
  const { activeTenant, can } = useAuth()
  const queryClient = useQueryClient()
  const [view, setView] = useState<DecisionView>('effective')
  const [revokeIntent, setRevokeIntent] = useState<WorkIntent | null>(null)

  const canRevoke = can('sessions:decision:admin')

  const params = { view, work_item_id: workItemId, limit: 50 }
  // ⛔ ACUMULA, NO REEMPLAZA. Antes esto era un `useQuery` con el cursor DENTRO de la clave: al
  //    pulsar «cargar más» la consulta traía la página siguiente y el render pintaba sólo
  //    `page.items`, así que las decisiones ya leídas DESAPARECÍAN de la pantalla. En una consola
  //    de gobierno eso es peor que una lista recortada sin avisar: el recorte no miente sobre lo
  //    que ya se vio, y esto sí. `useInfiniteQuery` es la forma de la casa —catorce ficheros la
  //    usan y ninguno acumula a mano— y además resuelve el reseteo por sí solo: `view` está en la
  //    clave, así que cambiar de vista tira TODAS las páginas, no sólo el cursor.
  const query = useInfiniteQuery({
    queryKey: workKeys.decisions(activeTenant, params),
    queryFn: ({ pageParam, signal }) =>
      listDecisions(
        { ...params, cursor: pageParam },
        { tenant: activeTenant },
        signal,
      ),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => (last.has_more ? last.next_cursor : undefined),
  })

  /**
   * Adaptador para `WorkSection`, que consume un `UseQueryResult` de UNA página.
   *
   * ⛔ `has_more` sale de la ÚLTIMA página, no de la primera: es la única que sabe si queda algo.
   *    Y las filas se aplanan de todas, que es el arreglo.
   */
  const paginas = query.data?.pages
  const acumulado = useMemo(
    () =>
      paginas
        ? {
            ...paginas[paginas.length - 1],
            items: paginas.flatMap((p) => p.items),
          }
        : undefined,
    [paginas],
  )
  /**
   * Adaptador de la consulta INFINITA a la de UNA página que `WorkSection` consume.
   *
   * ⛔ EL CAST VA AQUÍ Y EN NINGÚN OTRO SITIO, y es el único de este cambio. `WorkSection`
   *    reenvía la consulta a `AsyncSection`, así que la cadena entera exige el `refetch` de un
   *    `UseQueryResult<WorkPage<…>>`; el de una consulta infinita devuelve `InfiniteData<…>`,
   *    que no encaja por un valor que nadie de la cadena mira. Las dos salidas eran mentir en el
   *    contrato de `WorkSection` —que lo reenvía y no puede— o mentir aquí, en un objeto de tres
   *    líneas cuyo trabajo ES traducir entre las dos formas. Un adaptador que necesita un cast
   *    sigue siendo un adaptador; un contrato relajado para que quepa un caso deja de proteger a
   *    los otros tres llamantes.
   *
   *    Lo que el cast NO apaga: `data` está tipado de verdad (`acumulado` es
   *    `WorkPage<WorkDecision>`), que es el campo del que depende el render.
   */
  const seccion = {
    ...query,
    data: acumulado,
  } as unknown as Parameters<typeof WorkSection<WorkPage<WorkDecision>>>[0]['query']


  const refreshIntentTenant = (operation: WorkIntent | null) => {
    if (!operation) return
    void queryClient.invalidateQueries({
      queryKey: workKeys.all(operation.tenant),
    })
    if (activeTenant === operation.tenant) void query.refetch()
  }

  /**
   * Cambiar de vista tira TODAS las páginas, no sólo el cursor: las dos vistas paginan tablas
   * distintas y mezclar sus filas sería peor que perderlas. Lo hace `view` al estar en la
   * `queryKey` — por eso aquí ya no hay cursor que limpiar a mano.
   */
  const onViewChange = (next: string) => {
    setView(next as DecisionView)
  }

  return (
    <SectionCard
      title={t('decisions.title')}
      description={t('decisions.subtitle')}
      actions={
        <Tabs value={view} onValueChange={onViewChange}>
          <TabsList>
            <TabsTrigger value="effective">
              {t('decisions.view.effective')}
            </TabsTrigger>
            <TabsTrigger value="revoked">
              {t('decisions.view.revoked')}
            </TabsTrigger>
            <TabsTrigger value="history">
              {t('decisions.view.history')}
            </TabsTrigger>
          </TabsList>
        </Tabs>
      }
    >
      {view === 'history' ? (
        <p className="mb-3 flex items-start gap-2 rounded-md border border-info-line bg-info-soft p-3 text-xs text-info">
          <Info aria-hidden className="mt-0.5 size-4 shrink-0" />
          {t('decisions.historyNotice')}
        </p>
      ) : null}

      <WorkSection query={seccion}>
        {(page) =>
          page.items.length === 0 ? (
            <EmptyState
              icon={<Gavel />}
              title={t(`decisions.empty.${view}.title`)}
              description={t(`decisions.empty.${view}.body`)}
            />
          ) : (
            <div className="flex flex-col gap-3">
              <ul className="flex flex-col divide-y divide-border">
                {page.items.map((d) => (
                  <DecisionRow
                    key={d.id}
                    decision={d}
                    canRevoke={canRevoke}
                    onRevoke={() =>
                      setRevokeIntent(
                        buildIntent({
                          tenant: activeTenant,
                          command: 'decision.revoke',
                          decisionId: d.id,
                          body: { decision_id: d.id },
                        }),
                      )
                    }
                  />
                ))}
              </ul>
              {page.has_more ? (
                <Button
                  variant="outline"
                  size="sm"
                  className="self-start"
                  disabled={query.isFetchingNextPage}
                  onClick={() => void query.fetchNextPage()}
                >
                  {t('common.loadMore')}
                </Button>
              ) : null}
            </div>
          )
        }
      </WorkSection>

      <ApplyFlow
        open={revokeIntent !== null}
        onOpenChange={(open) => {
          if (!open) setRevokeIntent(null)
        }}
        intent={revokeIntent}
        title={t('decisions.revokeTitle')}
        description={t('decisions.revokeBody')}
        onApplied={() => refreshIntentTenant(revokeIntent)}
      />
    </SectionCard>
  )
}

function DecisionRow({
  decision,
  canRevoke,
  onRevoke,
}: {
  decision: WorkDecision
  canRevoke: boolean
  onRevoke: () => void
}) {
  const { t } = useTranslation('work')
  // The engine's own projection marker — NOT the open tab, and NOT a local guess.
  const attributed = attributesCurrentState(decision)

  return (
    <li className="flex items-start justify-between gap-4 py-3">
      <div className="min-w-0 space-y-1">
        <div className="flex flex-wrap items-center gap-2">
          <code className="font-mono text-xs">{decision.decision_key}</code>
          {attributed ? (
            <Badge
              variant={decision.state === 'effective' ? 'success' : 'neutral'}
            >
              {t(`decisions.state.${decision.state}`)}
            </Badge>
          ) : (
            /* NOT a blank cell. A blank would read as "not in force"; this says the
               view does not answer the question. */
            <Badge variant="outline" className="gap-1">
              <History aria-hidden className="size-3" />
              {t('decisions.state.notAttributed')}
            </Badge>
          )}
        </div>
        <p className="text-sm">{decision.statement_md}</p>
        <p className="text-xs text-muted-foreground">
          {t('decisions.meta', {
            subject: `${decision.subject_kind}:${decision.subject_ref}`,
            actor: `${decision.decided_by_kind}:${decision.decided_by_ref}`,
            at: decision.effective_at,
          })}
        </p>
      </div>
      {/* Revoking is only offered where the engine can attribute current state AND the
          decision is actually in force. Offering it on a history row would be asking
          the operator to revoke something whose status this view cannot report. */}
      {canRevoke && attributed && decision.state === 'effective' ? (
        <Button variant="outline" size="sm" onClick={onRevoke}>
          {t('decisions.revoke')}
        </Button>
      ) : null}
    </li>
  )
}
