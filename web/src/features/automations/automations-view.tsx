// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Automations — the unified page over the three automation rails
// (schedules · event subscriptions · alert routes) plus the trigger catalog.
// Deliberately an AGGREGATOR: each card summarizes one rail and links to its
// own feature, where authoring (and its RBAC) lives. Every panel loads and
// fails independently — a 403 on one rail never blanks the others.
import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { CalendarClock, Bell, Siren, Zap } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Badge } from '@/components/ui/badge'
import { StepUpRequiredState } from '@/components/layout/step-up-state'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardDescription,
  CardFooter,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import { PageHeader } from '@/components/ui/page-header'
import { Skeleton } from '@/components/ui/skeleton'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { useAuth } from '@/lib/auth/context'
import { ApiError } from '@/lib/api/errors'

import { ListTruncationBadge, listaRecortada } from '@/features/_intel'
import { automationsApi, automationsKeys, EVIDENCE_PAGE } from './api'
import { WorkflowsTab } from './workflows/workflows-tab'
import './i18n'

export function AutomationsView() {
  const { t } = useTranslation('automations')
  const { activeTenant } = useAuth()

  // El techo viaja en la PETICIÓN, no en la clave: `EVIDENCE_PAGE` es una constante, así que
  // no hay dos variantes de la misma lista que puedan pisarse en la caché.
  const listParams = { limit: EVIDENCE_PAGE }
  const schedules = useQuery({
    queryKey: automationsKeys.schedules(activeTenant),
    queryFn: () => automationsApi.schedules(listParams),
  })
  const subscriptions = useQuery({
    queryKey: automationsKeys.subscriptions(activeTenant),
    queryFn: () => automationsApi.subscriptions(listParams),
  })
  const routes = useQuery({
    queryKey: automationsKeys.routes(activeTenant),
    queryFn: () => automationsApi.routes(listParams),
  })
  const eventTypes = useQuery({
    queryKey: automationsKeys.eventTypes(activeTenant),
    queryFn: () => automationsApi.eventTypes(),
  })
  const matchTypes = useQuery({
    queryKey: automationsKeys.matchTypes(activeTenant),
    queryFn: () => automationsApi.matchTypes(),
  })

  const scheduleItems = schedules.data?.items ?? []
  const stalledCount = scheduleItems.filter(
    (s) => s.health === 'stalled',
  ).length
  const subscriptionItems = subscriptions.data?.items ?? []
  const routeItems = routes.data?.items ?? []
  const routable = new Set(
    (matchTypes.data?.match_types ?? []).map((m) => m.type),
  )

  return (
    <div className="space-y-6">
      <PageHeader
        icon={Zap}
        title={t('title')}
        description={t('description')}
      />

      <Tabs defaultValue="overview">
        <TabsList>
          <TabsTrigger value="overview">{t('tabs.overview')}</TabsTrigger>
          <TabsTrigger value="workflows">{t('tabs.workflows')}</TabsTrigger>
        </TabsList>

        <TabsContent value="overview" className="space-y-6">
          <div className="grid gap-4 md:grid-cols-3">
            <RailCard
              icon={<CalendarClock className="size-5" aria-hidden />}
              title={t('rails.schedules.title')}
              description={t('rails.schedules.description')}
              to="/orchestration"
              openLabel={t('rails.openAction', {
                feature: t('rails.schedules.title'),
              })}
              query={schedules}
              count={scheduleItems.length}
              badges={[
                {
                  key: 'active',
                  variant: 'success' as const,
                  label: t('rails.schedules.active', {
                    count: scheduleItems.filter((s) => s.health === 'active')
                      .length,
                  }),
                },
                {
                  key: 'stalled',
                  // ⛔ El rojo era INCONDICIONAL, así que «0 stalled» —que es la
                  // BUENA noticia— se pintaba como una alarma. Un contador en rojo
                  // dice «atiéndeme»; a cero no hay nada que atender, y una alarma
                  // que suena sin causa entrena a ignorar las que sí la tienen.
                  variant:
                    stalledCount > 0
                      ? ('danger' as const)
                      : ('neutral' as const),
                  label: t('rails.schedules.stalled', { count: stalledCount }),
                },
                {
                  key: 'paused',
                  variant: 'neutral' as const,
                  label: t('rails.schedules.paused', {
                    count: scheduleItems.filter((s) => s.health === 'paused')
                      .length,
                  }),
                },
              ]}
            />
            <RailCard
              icon={<Bell className="size-5" aria-hidden />}
              title={t('rails.subscriptions.title')}
              description={t('rails.subscriptions.description')}
              to="/eventing"
              openLabel={t('rails.openAction', {
                feature: t('rails.subscriptions.title'),
              })}
              query={subscriptions}
              count={subscriptionItems.length}
              badges={[
                {
                  key: 'enabled',
                  variant: 'success' as const,
                  label: t('rails.subscriptions.enabled', {
                    count: subscriptionItems.filter((s) => s.enabled).length,
                  }),
                },
                {
                  key: 'disabled',
                  variant: 'neutral' as const,
                  label: t('rails.subscriptions.disabled', {
                    count: subscriptionItems.filter((s) => !s.enabled).length,
                  }),
                },
              ]}
            />
            <RailCard
              icon={<Siren className="size-5" aria-hidden />}
              title={t('rails.routes.title')}
              description={t('rails.routes.description')}
              to="/alerting"
              openLabel={t('rails.openAction', {
                feature: t('rails.routes.title'),
              })}
              query={routes}
              count={routeItems.length}
              badges={[
                {
                  key: 'enabled',
                  variant: 'success' as const,
                  label: t('rails.routes.enabled', {
                    count: routeItems.filter((r) => r.enabled).length,
                  }),
                },
                {
                  key: 'disabled',
                  variant: 'neutral' as const,
                  label: t('rails.routes.disabled', {
                    count: routeItems.filter((r) => !r.enabled).length,
                  }),
                },
              ]}
            />
          </div>

          <Card>
            <CardHeader>
              <CardTitle>{t('triggers.title')}</CardTitle>
              <CardDescription>{t('triggers.description')}</CardDescription>
            </CardHeader>
            <CardContent>
              {eventTypes.isPending ? (
                <Skeleton className="h-24 w-full" />
              ) : eventTypes.isError ? (
                /* Este no aparecía en el censo porque el censo buscaba `isForbidden` y
                   aquí NO HAY NINGUNO: manda cualquier 403 —de rol y de ceremonia— al
                   mismo «no se pudo cargar». Un barrido por token no ve un sitio que
                   no distingue NADA; lo encontró leer el fichero entero. */
                eventTypes.error instanceof ApiError &&
                eventTypes.error.isStepUpRequired ? (
                  <StepUpRequiredState
                    action="generic"
                    onElevated={() => void eventTypes.refetch()}
                  />
                ) : (
                  <p className="text-sm text-muted-foreground">
                    {t('triggers.loadFailed')}
                  </p>
                )
              ) : (eventTypes.data?.event_types.length ?? 0) === 0 ? (
                <p className="text-sm text-muted-foreground">
                  {t('triggers.empty')}
                </p>
              ) : (
                <div className="overflow-x-auto">
                  <table className="w-full text-sm">
                    <thead>
                      <tr className="border-b text-left text-muted-foreground">
                        <th className="py-2 pr-4 font-medium">
                          {t('triggers.typeColumn')}
                        </th>
                        <th className="py-2 pr-4 font-medium">
                          {t('triggers.stabilityColumn')}
                        </th>
                        <th className="py-2 pr-4 font-medium">
                          {t('triggers.permissionColumn')}
                        </th>
                        <th className="py-2 font-medium">
                          {t('triggers.descriptionColumn')}
                        </th>
                      </tr>
                    </thead>
                    <tbody>
                      {eventTypes.data?.event_types.map((et) => (
                        <tr
                          key={et.type}
                          className="border-b last:border-0 align-top"
                        >
                          <td className="py-2 pr-4 whitespace-nowrap">
                            <code className="font-mono text-xs">{et.type}</code>
                            {routable.has(et.type) ? (
                              <Badge
                                variant="outline"
                                className="ml-2"
                                title={t('triggers.routableHint')}
                              >
                                {t('triggers.routable')}
                              </Badge>
                            ) : null}
                          </td>
                          <td className="py-2 pr-4">
                            <Badge
                              variant={
                                et.stability === 'stable'
                                  ? 'success'
                                  : 'neutral'
                              }
                            >
                              {et.stability}
                            </Badge>
                          </td>
                          <td className="py-2 pr-4">
                            <code className="font-mono text-xs">
                              {et.permission}
                            </code>
                          </td>
                          <td className="py-2 text-muted-foreground">
                            {et.description}
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              )}
            </CardContent>
          </Card>
        </TabsContent>

        <TabsContent value="workflows">
          <WorkflowsTab />
        </TabsContent>
      </Tabs>
    </div>
  )
}

function RailCard({
  icon,
  title,
  description,
  to,
  openLabel,
  query,
  count,
  badges,
}: {
  icon: React.ReactNode
  title: string
  description: string
  to: string
  openLabel: string
  query: {
    isPending: boolean
    isError: boolean
    error: unknown
    // ⛔ Y `data` IGUAL QUE `refetch`: los tres call sites pasan el UseQueryResult entero, así
    // que en runtime ya viajaba; sin declararlo, el aviso de recorte no podía leer `has_more`.
    data?: unknown
    // Para reintentar la lectura DESPUÉS de la ceremonia. Los tres call sites ya pasan el
    // UseQueryResult entero, así que en runtime ya viaja: sólo faltaba declararlo.
    refetch: () => void
  }
  count: number
  badges: {
    key: string
    variant: 'success' | 'neutral' | 'danger'
    label: string
  }[]
}) {
  const { t } = useTranslation('automations')
  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          {icon}
          {title}
        </CardTitle>
        <CardDescription>{description}</CardDescription>
      </CardHeader>
      <CardContent>
        {/* El recuento de abajo se deriva de `items.length`. Si el motor recortó, ese número es
            MENOR que la realidad y la tarjeta no lo diría: el aviso existe para que la cifra no
            se lea como un censo. Sale sólo con `has_more` y sin error. */}
        <ListTruncationBadge
          query={query}
          label={t('rails.truncated', { n: count })}
          hint={t('rails.truncatedHint')}
        />
        {query.isPending ? (
          <Skeleton className="h-12 w-full" />
        ) : query.isError ? (
          /* ⛔ ASEGURAMIENTO ANTES QUE ROL. `isForbidden` es SÓLO el status 403
             (lib/api/errors.ts:59) y un `step_up_required` lo satisface también, así
             que esta tarjeta afirmaba EN PROSA algo falso —«no tienes autorización
             para ver este raíl»— sobre un permiso que el operador SÍ tiene, en la
             pestaña de aterrizaje y junto a dos tarjetas sanas. */
          query.error instanceof ApiError && query.error.isStepUpRequired ? (
            <StepUpRequiredState
              action="generic"
              onElevated={() => void query.refetch()}
            />
          ) : (
            <p className="text-sm text-muted-foreground">
              {query.error instanceof ApiError && query.error.isForbidden
                ? t('rails.forbidden')
                : t('rails.loadFailed')}
            </p>
          )
        ) : (
          <div className="space-y-2">
            {/* ⛔ SI EL MOTOR DICE QUE FALTAN FILAS, ESTA CIFRA NO ES UN TOTAL. Antes se pintaba
                `countLabel` («N total») incondicionalmente, JUSTO DEBAJO del aviso de recorte:
                la tarjeta declaraba «hay más» y a continuación se desmentía. Declarar el recorte
                y seguir llamando total a la página es peor que no declararlo, porque las dos
                afirmaciones juntas parecen una sola verificada. */}
            <p className="text-2xl font-semibold">
              {t(
                listaRecortada(query)
                  ? 'rails.countLoaded'
                  : 'rails.countLabel',
                { count },
              )}
            </p>
            <div className="flex flex-wrap gap-1.5">
              {badges.map((b) => (
                <Badge key={b.key} variant={b.variant}>
                  {b.label}
                </Badge>
              ))}
            </div>
          </div>
        )}
      </CardContent>
      <CardFooter>
        <Button asChild variant="outline" size="sm">
          <Link to={to}>{openLabel}</Link>
        </Button>
      </CardFooter>
    </Card>
  )
}
