// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useCallback, useMemo, useState } from 'react'
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
import {
  IntelPage,
  ListTruncationBadge,
  SectionCard,
} from '@/features/_intel'
import { LiveDot } from '@/features/shared'
import { useOwnerLabel } from './owner-label'
import { useAuth } from '@/lib/auth/context'
import { listWorkItems, workKeys, type ListWorkParams } from './api'
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
  // invalidates the list; the stream's own cursor handles resume (stream.ts).
  // ⛔ `activeTenant` VA EN LAS DEPENDENCIAS. Sin él la retrollamada se queda con el inquilino
  // del primer render: tras cambiar de inquilino, cada evento del flujo invalidaría la clave del
  // ANTERIOR y la lista que el operador está mirando no se refrescaría nunca. Lo señaló
  // `react-hooks/exhaustive-deps` en el mismo cambio que metió la variable.
  const onEvent = useCallback(() => {
    void qc.invalidateQueries({ queryKey: workKeys.all(activeTenant) })
  }, [qc, activeTenant])
  const { status: streamStatus } = useWorkStream({
    enabled: can('sessions:work:read'),
    onEvent,
    onUnavailable: setStreamUnavailable,
  })

  return (
    <IntelPage
      icon={ClipboardList}
      title={t('title')}
      description={t('subtitle')}
      actions={
        <div className="flex items-center gap-3">
          <LiveDot status={streamStatus} />
          <Button
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
            <p className="text-xs">{t('stream.unavailableBody')}</p>
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
                            <p className="truncate text-sm font-medium">
                              {item.title}
                            </p>
                            <p className="font-mono text-xs text-muted-foreground">
                              {item.work_kind} ·{' '}
                              {etiquetaDuenno(item.owner_kind, item.owner_ref)}
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
      />
    </IntelPage>
  )
}
