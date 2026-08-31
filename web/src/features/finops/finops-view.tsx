// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// FinOps (module XI) — the container. It wires the queries, the range, the tabs and
// the one privileged write (create budget, gated by RBAC), and composes the pure
// presentational pieces. It computes nothing about cost — Does; this presents.
import './i18n'
import { useMemo, useState } from 'react'
import {
  useMutation,
  useQueries,
  useQuery,
  useQueryClient,
} from '@tanstack/react-query'
import { Archive, Coins, Download, Pencil, Plus, Trash2 } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Badge } from '@/components/ui/badge'
import { EmptyState } from '@/components/ui/empty-state'
import { formatMicroUsd } from '@/lib/format'
import { Field } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Skeleton } from '@/components/ui/skeleton'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { toast } from '@/components/ui/toaster'
import { useAuth } from '@/lib/auth/context'
import { ApiError } from '@/lib/api/errors'
import { useFailedActionReporter } from '@/lib/hooks/use-privileged-mutation'
import {
  AsyncSection,
  CaveatNotice,
  IntelPage,
  ListTruncationBadge,
  MetricStat,
  SectionCard,
  StatGrid,
} from '@/features/_intel'
import {
  fetchFocusExport,
  fetchStatementExport,
  finopsApi,
  finopsKeys,
} from './api'
import { StatementDetail, StatementList } from './chargeback-components'
import {
  AlertsTable,
  AllocationTable,
  BudgetCard,
  CacheEfficiencyPanel,
  CostTrend,
  DimensionBreakdown,
  ForecastCard,
  FutureDimensionsPanel,
  RecommendationCard,
  ReconciliationView,
  SpendBreakdown,
  SpendStats,
} from './components'
import {
  BUDGET_DIMENSIONS,
  SPEND_DIMENSIONS,
  type Budget,
  type BudgetAction,
  type BudgetPeriod,
  type BudgetStatus,
  type ChargebackStatement,
  type CostCenter,
  type ExportProvenance,
  type SpendDimension,
} from './types'

const RANGE_DAYS: Record<string, number> = { '7d': 7, '30d': 30, '90d': 90 }

function sinceFor(rangeId: string): string {
  if (rangeId === 'mtd') {
    const now = new Date()
    return new Date(
      Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), 1),
    ).toISOString()
  }
  const days = RANGE_DAYS[rangeId] ?? 30
  return new Date(Date.now() - days * 86_400_000).toISOString()
}

// --- centros de coste y sus reglas de mapeo (C07-04) -------------------------
//
// ⛔ ESTA ES LA PANTALLA QUE ARREGLA EL «COSTE SIN ATRIBUIR» que la pestaña de valor ya enseña.
//    Sin ella, la consola dice «410 000 € sin dueño» y no ofrece ninguna forma de darles uno: la
//    única vía era `curl`. Siete métodos de cliente sin una sola pantalla que los pulsara.
//
// ⛔ Y TIENE TRES VERDADES QUE UNA PANTALLA INGENUA ESCONDE, las tres medidas en el motor:
//
//    1. **La resolución ocurre EN LA INGESTA y se denormaliza en la fila**
//       (`modules/finops/ingest.go:71-72`, `schema.go:151-154`). Una regla nueva **no reatribuye
//       el gasto ya registrado**. Quien la crea esperando que la cifra de «sin atribuir» baje va
//       a mirar el mismo número mañana y concluir que el producto no funciona. La pantalla lo
//       dice ANTES de crear la regla, no en una nota al pie.
//    2. **Gana la prioridad MÁS ALTA** (`costcenter.go:414-440`: `prio > bestPriority` desde −1),
//       y sólo **entre dimensiones distintas**.
//    3. ⛔ **`Limit: 1` por dimensión** (`costcenter.go:427`). Si existen DOS reglas para la misma
//       dimensión y la misma clave, el motor consulta una sola y se queda con **la que el store
//       devuelva primero — NO con la de mayor prioridad**. Presentar la prioridad como un orden
//       global sería mentir sobre qué regla se aplica de verdad. La pantalla ordena por prioridad
//       *dentro de cada dimensión* y avisa cuando hay un empate que el motor no resuelve por ahí.
/**
 * El estado de un centro de coste, con sus TRES valores y no dos.
 *
 * ⛔ `""` NO ES «activo». La atribución de coste exige EXACTAMENTE `"active"`
 *    (`modules/finops/costcenter.go`, resolvedor de mapeos), así que un centro con el estado sin
 *    fijar **no atribuye nada** — y hasta hoy podía quedarse así, porque `PUT /cost-centers/{id}`
 *    es un REEMPLAZO COMPLETO y `validate()` defaulteaba en una copia. Pintarlo como activo diría
 *    que funciona cuando es justo el caso en que no lo hace.
 */
export function estadoCentro(
  status: string | undefined,
): 'active' | 'archived' | 'unknown' {
  if (status === 'active') return 'active'
  if (status === 'archived') return 'archived'
  return 'unknown'
}

function CostCentresTab() {
  const { t } = useTranslation('finops')
  const qc = useQueryClient()
  const { activeTenant, can } = useAuth()
  const reportFailure = useFailedActionReporter()
  const [seleccion, setSeleccion] = useState<string | null>(null)
  const [nuevoCC, setNuevoCC] = useState(false)
  const [nuevaRegla, setNuevaRegla] = useState(false)
  const [editando, setEditando] = useState<CostCenter | null>(null)

  const centros = useQuery({
    queryKey: finopsKeys.costCenters(activeTenant),
    queryFn: () => finopsApi.costCenters(undefined, { tenant: activeTenant }),
  })

  const reglas = useQuery({
    queryKey: finopsKeys.costCenterMappings(activeTenant, seleccion as string),
    queryFn: () =>
      finopsApi.costCenterMappings(seleccion as string, {
        tenant: activeTenant,
      }),
    enabled: Boolean(seleccion),
  })

  const invalidar = (tenant: string | null) => {
    void qc.invalidateQueries({
      queryKey: finopsKeys.costCenters(tenant),
    })
  }

  // ⛔ EL TIPO ES `Pick<CostCenter, …>` Y NO UNA FORMA A MANO, Y ES EL ARREGLO DE VERDAD.
  //
  //    Esto decía `{ code: string; cc_name: string; owner: string }`, y `cc_name` NO EXISTE en el
  //    motor: `costCenterDTO` declara `json:"name"` (`modules/finops/costcenter.go:33`) y el
  //    creador escribe `colCCName: in.Name` (`:165`). El cliente pasa el cuerpo tal cual
  //    (`api.ts:201-202`) y el handler no usa `DisallowUnknownFields`, así que `cc_name` se
  //    ignoraba en silencio y **el nombre que tecleaba el usuario se perdía: todo centro de coste
  //    creado desde la consola nacía SIN NOMBRE**.
  //
  //    Renombrar el campo arregla el caso. Tiparlo contra `CostCenter` arregla la CLASE: cualquier
  //    clave que el motor no declare pasa a ser un error de compilación, no una petición que se
  //    ignora.
  const crearCC = useMutation({
    mutationFn: ({
      body,
      tenant,
    }: {
      body: Pick<CostCenter, 'code' | 'name' | 'owner'>
      tenant: string | null
    }) => finopsApi.createCostCenter(body, { tenant }),
    onSuccess: (_data, { tenant }) => {
      setNuevoCC(false)
      invalidar(tenant)
      toast.success(t('costCentres.created'))
    },
    onError: (e) => reportFailure(e as ApiError),
  })

  // ⛔ EL `PUT` ES UN REEMPLAZO COMPLETO, NO UN PARCHE: el handler escribe code, name, description,
  //    owner y status con lo que llegue (`costcenter.go`), así que **omitir un campo lo BORRA**.
  //    Por eso estas dos mutaciones mandan el registro ENTERO y no sólo lo que cambia — y por eso
  //    archivar tampoco puede ser `{status: 'archived'}` a secas.
  const guardarCC = useMutation({
    mutationFn: ({ cc, tenant }: { cc: CostCenter; tenant: string | null }) =>
      finopsApi.updateCostCenter(
        cc.id,
        {
          code: cc.code,
          name: cc.name,
          description: cc.description ?? '',
          owner: cc.owner ?? '',
          status: cc.status,
        },
        { tenant },
      ),
    onSuccess: (_data, { tenant }) => {
      setEditando(null)
      invalidar(tenant)
      toast.success(t('costCentres.saved'))
    },
    onError: (e) => reportFailure(e as ApiError),
  })

  const archivarCC = useMutation({
    mutationFn: ({ cc, tenant }: { cc: CostCenter; tenant: string | null }) =>
      finopsApi.updateCostCenter(
        cc.id,
        {
          code: cc.code,
          name: cc.name,
          description: cc.description ?? '',
          owner: cc.owner ?? '',
          status: cc.status === 'archived' ? 'active' : 'archived',
        },
        { tenant },
      ),
    onSuccess: (_data, { tenant }) => {
      invalidar(tenant)
      toast.success(t('costCentres.archived'))
    },
    onError: (e) => reportFailure(e as ApiError),
  })

  const borrarCC = useMutation({
    mutationFn: ({ id, tenant }: { id: string; tenant: string | null }) =>
      finopsApi.deleteCostCenter(id, { tenant }),
    onSuccess: (_data, { tenant }) => {
      setSeleccion(null)
      invalidar(tenant)
    },
    onError: (e) => reportFailure(e as ApiError),
  })

  const crearRegla = useMutation({
    mutationFn: ({
      costCenterId,
      body,
      tenant,
    }: {
      costCenterId: string
      body: {
        source_dimension: string
        source_key: string
        priority: number
      }
      tenant: string | null
    }) => finopsApi.createCostCenterMapping(costCenterId, body, { tenant }),
    onSuccess: (_data, { tenant }) => {
      setNuevaRegla(false)
      invalidar(tenant)
      toast.success(t('costCentres.ruleCreated'))
    },
    onError: (e) => reportFailure(e as ApiError),
  })

  const borrarRegla = useMutation({
    mutationFn: ({
      costCenterId,
      id,
      tenant,
    }: {
      costCenterId: string
      id: string
      tenant: string | null
    }) => finopsApi.deleteCostCenterMapping(costCenterId, id, { tenant }),
    onSuccess: (_data, { tenant }) => invalidar(tenant),
    onError: (e) => reportFailure(e as ApiError),
  })

  // ⛔ EL PERMISO ES EL DEL MOTOR, no uno inventado. Las ocho rutas de centros de coste y
  // mapeos exigen `finops:budget:write` (`modules/finops/api.go:82-88`); `finops:write` no
  // existe en NINGUNA forma que el motor declare, así que gatear con él habría dejado los
  // botones de crear y borrar OCULTOS PARA TODOS LOS ROLES — una pantalla de sólo lectura que
  // nadie podría desbloquear y ningún error explicaría. Lo cazó `check-console-perms`.
  const puedeEscribir = can('finops:budget:write')

  return (
    <div className="flex flex-col gap-4">
      {/* ⛔ VERDAD 1, y va ARRIBA porque condiciona todo lo que se haga debajo. */}
      <div
        role="note"
        className="rounded-md border border-warning/40 bg-warning/5 p-3 text-xs"
      >
        {t('costCentres.ingestOnlyNotice')}
      </div>

      <SectionCard
        title={t('costCentres.title')}
        description={t('costCentres.description')}
        actions={
          puedeEscribir ? (
            <Button size="sm" onClick={() => setNuevoCC(true)}>
              <Plus className="mr-1 h-4 w-4" />
              {t('costCentres.new')}
            </Button>
          ) : null
        }
      >
        <ListTruncationBadge
          query={centros}
          label={t('costCentres.truncated', {
            n: centros.data?.items?.length ?? 0,
          })}
          hint={t('costCentres.truncatedHint')}
        />
        <AsyncSection query={centros} skeletonHeight={160}>
          {(res) => {
            const items = ((res as { items?: unknown[] })?.items ??
              // ⛔ EL TIPO REAL, NO UNA FORMA A MANO. Esto declaraba `cc_name?: string`, un campo que
              //    el motor NO envía —su DTO dice `json:"name"`, `modules/finops/costcenter.go:33`— y
              //    al ser OPCIONAL TypeScript no se quejaba: la lista pintaba `undefined`, o sea TODOS
              //    los centros de coste SIN NOMBRE. Una forma escrita a mano no puede discrepar del
              //    motor porque no lo mira; `CostCenter` sí.
              []) as CostCenter[]
            return items.length === 0 ? (
              <EmptyState title={t('costCentres.empty')} />
            ) : (
              <div className="flex flex-col gap-1">
                {items.map((c) => (
                  <div
                    key={c.id}
                    className="flex flex-wrap items-center justify-between gap-3 rounded-md border border-border px-3 py-2 text-sm"
                  >
                    <button
                      type="button"
                      className="flex items-center gap-2 text-left"
                      onClick={() =>
                        setSeleccion(seleccion === c.id ? null : c.id)
                      }
                    >
                      <Coins className="h-4 w-4 text-muted-foreground" />
                      <span className="font-mono text-xs">{c.code}</span>
                      <span>{c.name}</span>
                    </button>
                    <div className="flex items-center gap-2">
                      {c.owner ? (
                        <Badge variant="outline">{c.owner}</Badge>
                      ) : null}
                      {/* ⛔ EL ESTADO SE PINTA SIEMPRE QUE NO SEA `active`, y con sus TRES valores. Un centro
                          sin estado fijado NO atribuye gasto —la atribución exige exactamente `active`— y
                          ésa es justo la información que quien mira la lista necesita: sin ella, un centro
                          inerte se ve igual que uno que funciona. */}
                      {estadoCentro(c.status) === 'active' ? null : (
                        <Badge
                          variant={
                            estadoCentro(c.status) === 'archived'
                              ? 'neutral'
                              : 'warning'
                          }
                        >
                          {t(`costCentres.status.${estadoCentro(c.status)}`)}
                        </Badge>
                      )}
                      {puedeEscribir ? (
                        <>
                          <Button
                            size="sm"
                            variant="ghost"
                            aria-label={t('costCentres.edit', { code: c.code })}
                            onClick={() => setEditando(c)}
                          >
                            <Pencil className="h-4 w-4" />
                          </Button>
                          {/* Archivar es REVERSIBLE y conserva las reglas de asignación; borrar es duro y
                              se las lleva por delante. Se ofrecen los dos, no sólo el destructivo. */}
                          <Button
                            size="sm"
                            variant="ghost"
                            aria-label={t(
                              estadoCentro(c.status) === 'archived'
                                ? 'costCentres.restore'
                                : 'costCentres.archive',
                              { code: c.code },
                            )}
                            onClick={() =>
                              archivarCC.mutate({
                                cc: c,
                                tenant: activeTenant,
                              })
                            }
                          >
                            <Archive className="h-4 w-4" />
                          </Button>
                          <Button
                            size="sm"
                            variant="ghost"
                            aria-label={t('costCentres.delete', {
                              code: c.code,
                            })}
                            onClick={() =>
                              borrarCC.mutate({
                                id: c.id,
                                tenant: activeTenant,
                              })
                            }
                          >
                            <Trash2 className="h-4 w-4" />
                          </Button>
                        </>
                      ) : null}
                    </div>
                  </div>
                ))}
              </div>
            )
          }}
        </AsyncSection>
      </SectionCard>

      {seleccion ? (
        <SectionCard
          title={t('costCentres.rulesTitle')}
          description={t('costCentres.rulesDescription')}
          actions={
            puedeEscribir ? (
              <Button size="sm" onClick={() => setNuevaRegla(true)}>
                <Plus className="mr-1 h-4 w-4" />
                {t('costCentres.newRule')}
              </Button>
            ) : null
          }
        >
          <ListTruncationBadge
            query={reglas}
            label={t('costCentres.rulesTruncated', {
              n: reglas.data?.items?.length ?? 0,
            })}
            hint={t('costCentres.rulesTruncatedHint')}
          />
          <AsyncSection query={reglas} skeletonHeight={140}>
            {(res) => {
              const items = ((res as { items?: unknown[] })?.items ??
                []) as Array<{
                id: string
                source_dimension: string
                source_key: string
                priority: number
              }>
              if (items.length === 0)
                return <EmptyState title={t('costCentres.noRules')} />

              // ⛔ VERDAD 3: la prioridad ordena DENTRO de una dimensión y sólo decide ENTRE
              //    dimensiones distintas. Dos reglas con la MISMA dimensión y la MISMA clave son
              //    un empate que el motor no resuelve por prioridad — coge la primera que le dé
              //    el store. Se agrupa por dimensión para no sugerir un orden global falso.
              const porDim = new Map<string, typeof items>()
              for (const r of items) {
                const l = porDim.get(r.source_dimension) ?? []
                l.push(r)
                porDim.set(r.source_dimension, l)
              }
              const claves = items.map(
                (r) => `${r.source_dimension} ${r.source_key}`,
              )
              const empatados = new Set(
                claves.filter((k, i) => claves.indexOf(k) !== i),
              )

              return (
                <div className="flex flex-col gap-3">
                  {[...porDim.entries()].map(([dim, lista]) => (
                    <div key={dim} className="flex flex-col gap-1">
                      <div className="text-xs font-medium text-muted-foreground">
                        {t('costCentres.dimension', { dim })}
                      </div>
                      {[...lista]
                        .sort((a, b) => b.priority - a.priority)
                        .map((r) => (
                          <div
                            key={r.id}
                            className="flex flex-wrap items-center justify-between gap-3 rounded-md border border-border px-3 py-2 text-sm"
                          >
                            <span className="font-mono text-xs">
                              {r.source_key}
                            </span>
                            <div className="flex items-center gap-2">
                              <Badge variant="outline">
                                {t('costCentres.priority', { n: r.priority })}
                              </Badge>
                              {empatados.has(
                                `${r.source_dimension} ${r.source_key}`,
                              ) ? (
                                <Badge variant="warning">
                                  {t('costCentres.ambiguous')}
                                </Badge>
                              ) : null}
                              {puedeEscribir ? (
                                <Button
                                  size="sm"
                                  variant="ghost"
                                  aria-label={t('costCentres.deleteRule', {
                                    key: r.source_key,
                                  })}
                                  onClick={() =>
                                    borrarRegla.mutate({
                                      costCenterId: seleccion as string,
                                      id: r.id,
                                      tenant: activeTenant,
                                    })
                                  }
                                >
                                  <Trash2 className="h-4 w-4" />
                                </Button>
                              ) : null}
                            </div>
                          </div>
                        ))}
                    </div>
                  ))}
                  {/* ⛔ VERDAD 2, dicha donde se leen las reglas. */}
                  <p className="text-xs text-muted-foreground">
                    {t('costCentres.priorityNote')}
                  </p>
                </div>
              )
            }}
          </AsyncSection>
        </SectionCard>
      ) : null}

      <ResultadosCard />
      <TarifasCard />

      <NuevoCostCentreDialog
        open={nuevoCC}
        onOpenChange={setNuevoCC}
        onSubmit={(body) => crearCC.mutate({ body, tenant: activeTenant })}
        pending={crearCC.isPending}
      />
      <EditarCostCentreDialog
        centro={editando}
        onOpenChange={(v) => !v && setEditando(null)}
        onSubmit={(cc) => guardarCC.mutate({ cc, tenant: activeTenant })}
        pending={guardarCC.isPending}
      />
      <NuevaReglaDialog
        open={nuevaRegla}
        onOpenChange={setNuevaRegla}
        onSubmit={(body) =>
          crearRegla.mutate({
            costCenterId: seleccion as string,
            body,
            tenant: activeTenant,
          })
        }
        pending={crearRegla.isPending}
      />
    </div>
  )
}

// --- catálogo de tarifas de modelo (C07-04) ----------------------------------
//
// ⛔ ES EL LIBRO DE PRECIOS DEL QUE DEPENDE TODA CIFRA DE COSTE ESTIMADA, y no tenía pantalla:
//    `ratecatalog.go:19-23` — «the per-provider/model pricing table that resolves list-price
//    rates (micro-USD per 1M tokens) for a given instant, **enabling the FinOps module to
//    estimate cost when a provider's cost API does not report a dollar amount**».
//
// ⛔ DOS COSAS QUE UNA TABLA DE PRECIOS INGENUA DIRÍA MAL:
//
//    1. **`effective_until` vacío NO es «sin fecha» ni «caducada»: es que la tarifa SIGUE
//       VIGENTE** (`:26-28`, «a null/empty effective_until means the rate is still current»).
//       Pintar un guion ahí y ordenar por caducidad esconde justo la tarifa que se está
//       aplicando ahora mismo.
//    2. **El dinero es micro-USD ENTERO por 1M de tokens, sin floats** (`:22-23`, README.md).
//       Un campo que acepte «3,00» y lo interprete como dólares mete un error de seis órdenes de
//       magnitud en cada estimación que salga de aquí. La unidad se dice en la etiqueta, no se
//       supone.

// --- resultados graduados (C07-04) -------------------------------------------
//
// ⛔ SON EL NUMERADOR DEL «COSTE POR RESULTADO». La pestaña de valor ya enseña gasto sin
//    resultados graduados y lo señala como riesgo de cancelación; esto es lo que lo arregla, y
//    sólo existía en `curl`.
//
// ⛔ Y EL FORMULARIO TIENE UNA TRAMPA DE IDEMPOTENCIA que el motor documenta en su validación
//    (`modules/finops/value.go`, `outcomeIngestRequest.validate`):
//
//      «with no outcome_ref the dedup key falls back to the instant, so a server-minted clock
//       would make a retried POST a NEW row (double-counting value)»
//
//    ⇒ El instante se captura UNA VEZ al abrir el diálogo y se reenvía igual en cada reintento.
//    Un `new Date()` en el momento del envío haría que reintentar **cuente el valor dos veces**, e
//    inflar el numerador del coste por resultado es exactamente la cifra que un CFO usa para
//    decidir si el producto se queda.
//
// ⛔ Y LA RESPUESTA NO DICE SI SE CREÓ ALGO. Es `202 {"accepted": true}` y el propio handler
//    advierte que «dedup can make the write a no-op». Así que la pantalla dice **aceptado**, no
//    «registrado», y **recarga la lista**: la lista es la verdad, el 202 sólo dice que se recibió.
function ResultadosCard() {
  const { t } = useTranslation('finops')
  const qc = useQueryClient()
  const { activeTenant, can } = useAuth()
  const [abierto, setAbierto] = useState(false)
  const [sujeto, setSujeto] = useState('')
  const [veredicto, setVeredicto] = useState('satisfied')
  // ⛔ UNA SOLA VEZ, al abrir: reenviar el mismo instante es lo que hace idempotente el reintento.
  const [instante, setInstante] = useState<string | null>(null)

  const q = useQuery({
    queryKey: finopsKeys.outcomes(activeTenant),
    queryFn: () => finopsApi.outcomes(undefined, { tenant: activeTenant }),
  })

  const enviar = useMutation({
    mutationFn: ({
      body,
      tenant,
    }: {
      body: Parameters<typeof finopsApi.ingestOutcome>[0]
      tenant: string | null
    }) => finopsApi.ingestOutcome(body, { tenant }),
    onSuccess: (_data, { tenant }) => {
      setAbierto(false)
      // La lista es la verdad; el 202 no distingue creado de deduplicado.
      void qc.invalidateQueries({ queryKey: finopsKeys.outcomes(tenant) })
      toast.success(t('outcomes.accepted'))
    },
    onError: (e: unknown) => toast.error(String((e as Error).message ?? e)),
  })

  function abrir() {
    setInstante(new Date().toISOString())
    setAbierto(true)
  }

  return (
    <SectionCard
      title={t('outcomes.title')}
      description={t('outcomes.description')}
      actions={
        can('finops:outcomes:write') ? (
          <Button size="sm" onClick={abrir}>
            {t('outcomes.grade')}
          </Button>
        ) : null
      }
    >
      <ListTruncationBadge
        query={q}
        label={t('outcomes.truncated', { n: q.data?.items?.length ?? 0 })}
        hint={t('outcomes.truncatedHint')}
      />
      <AsyncSection query={q} skeletonHeight={140}>
        {(res) => {
          const items = ((res as { items?: unknown[] })?.items ?? []) as Array<{
            outcome_ref?: string
            subject_kind: string
            subject_ref: string
            verdict: string
            value_micro_usd?: number
            source?: string
            occurred_at: string
          }>
          return items.length === 0 ? (
            <EmptyState
              title={t('outcomes.empty')}
              description={t('outcomes.emptyHint')}
            />
          ) : (
            <div className="flex flex-col gap-1">
              {items.map((o) => (
                <div
                  key={`${o.subject_ref}:${o.occurred_at}:${o.outcome_ref ?? ''}`}
                  className="flex flex-wrap items-center justify-between gap-3 rounded-md border border-border px-3 py-2 text-sm"
                >
                  <div className="flex min-w-0 items-center gap-2">
                    <Badge
                      variant={
                        o.verdict === 'satisfied' ? 'success' : 'neutral'
                      }
                    >
                      {o.verdict}
                    </Badge>
                    <span className="font-mono text-xs">{o.subject_ref}</span>
                  </div>
                  <span className="text-xs text-muted-foreground">
                    {/* Sin valor NO es valor cero: el campo es opcional. */}
                    {o.value_micro_usd === undefined
                      ? t('outcomes.noValue')
                      : formatMicroUsd(o.value_micro_usd)}
                    {o.source ? ` · ${o.source}` : ''}
                  </span>
                </div>
              ))}
            </div>
          )
        }}
      </AsyncSection>

      <Dialog open={abierto} onOpenChange={setAbierto}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t('outcomes.grade')}</DialogTitle>
            <DialogDescription>{t('outcomes.gradeHint')}</DialogDescription>
          </DialogHeader>
          <div className="flex flex-col gap-3">
            <Field label={t('outcomes.subject')}>
              <Input
                value={sujeto}
                onChange={(e) => setSujeto(e.target.value)}
              />
            </Field>
            <Field label={t('outcomes.verdict')}>
              <Select value={veredicto} onValueChange={setVeredicto}>
                <SelectTrigger aria-label={t('outcomes.verdict')}>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="satisfied">satisfied</SelectItem>
                  <SelectItem value="unsatisfied">unsatisfied</SelectItem>
                </SelectContent>
              </Select>
            </Field>
          </div>
          <DialogFooter>
            <Button
              disabled={enviar.isPending || sujeto.trim() === ''}
              onClick={() =>
                enviar.mutate({
                  body: {
                    subject_kind: 'agent',
                    subject_ref: sujeto.trim(),
                    verdict: veredicto,
                    occurred_at: instante ?? undefined,
                  },
                  tenant: activeTenant,
                })
              }
            >
              {t('outcomes.send')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </SectionCard>
  )
}

function TarifasCard() {
  const { t } = useTranslation('finops')
  const { activeTenant, can } = useAuth()
  const q = useQuery({
    queryKey: finopsKeys.modelRates(activeTenant),
    queryFn: () => finopsApi.modelRates(undefined, { tenant: activeTenant }),
  })

  return (
    <SectionCard
      title={t('rates.title')}
      description={t('rates.description')}
      actions={
        can('finops:budget:write') ? (
          <Badge variant="outline">{t('rates.unit')}</Badge>
        ) : null
      }
    >
      <ListTruncationBadge
        query={q}
        label={t('rates.truncated', { n: q.data?.items?.length ?? 0 })}
        hint={t('rates.truncatedHint')}
      />
      <AsyncSection query={q} skeletonHeight={140}>
        {(res) => {
          const items = ((res as { items?: unknown[] })?.items ?? []) as Array<{
            id: string
            provider: string
            model_ref?: string
            input_rate_micro_usd?: number
            output_rate_micro_usd?: number
            effective_from?: string
            effective_until?: string
          }>
          return items.length === 0 ? (
            <EmptyState title={t('rates.empty')} />
          ) : (
            <div className="flex flex-col gap-1">
              {items.map((r) => (
                <div
                  key={r.id}
                  className="flex flex-wrap items-center justify-between gap-3 rounded-md border border-border px-3 py-2 text-sm"
                >
                  <div className="flex min-w-0 items-center gap-2">
                    <Badge variant="outline">{r.provider}</Badge>
                    <span className="font-mono text-xs">{r.model_ref}</span>
                  </div>
                  <div className="flex flex-wrap items-center gap-3 text-xs">
                    <span className="font-mono">
                      {t('rates.inOut', {
                        in: r.input_rate_micro_usd ?? 0,
                        out: r.output_rate_micro_usd ?? 0,
                      })}
                    </span>
                    {/* ⛔ Sin fecha de fin la tarifa está VIGENTE, y así se dice: un guion aquí
                        se lee como «le falta un dato» y esconde la que se aplica hoy. */}
                    {r.effective_until ? (
                      <Badge variant="neutral">
                        {t('rates.until', { at: r.effective_until })}
                      </Badge>
                    ) : (
                      <Badge variant="success">{t('rates.current')}</Badge>
                    )}
                  </div>
                </div>
              ))}
            </div>
          )
        }}
      </AsyncSection>
    </SectionCard>
  )
}

/**
 * Editar un centro de coste.
 *
 * ⛔ MANDA EL REGISTRO ENTERO, incluidos `code` y `status`, porque el motor trata este `PUT` como un
 *    REEMPLAZO COMPLETO: lo que no viaje se escribe vacío. Un diálogo que enviara sólo los campos
 *    que el usuario tocó borraría el código y dejaría el estado en `""` — y un estado vacío hace
 *    que el centro deje de atribuir gasto, porque la atribución exige exactamente `active`.
 *
 * ⚠ El CÓDIGO se muestra y NO se edita. Es la clave por la que las reglas de asignación resuelven y
 *   la que los extractos ya emitidos denormalizan; cambiarlo desde aquí rompería esa
 *   correspondencia en silencio. Se envía tal cual, para no borrarlo.
 */
function EditarCostCentreDialog({
  centro,
  onOpenChange,
  onSubmit,
  pending,
}: {
  centro: CostCenter | null
  onOpenChange: (v: boolean) => void
  onSubmit: (cc: CostCenter) => void
  pending?: boolean
}) {
  const { t } = useTranslation('finops')
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [owner, setOwner] = useState('')
  const [ultimoId, setUltimoId] = useState<string | null>(null)

  // Al abrirse con OTRO centro, los campos parten de SUS valores y no de los del anterior.
  if (centro && centro.id !== ultimoId) {
    setUltimoId(centro.id)
    setName(centro.name)
    setDescription(centro.description ?? '')
    setOwner(centro.owner ?? '')
  }

  if (!centro) return null
  return (
    <Dialog open onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t('costCentres.editTitle')}</DialogTitle>
          <DialogDescription>
            {t('costCentres.editDescription')}
          </DialogDescription>
        </DialogHeader>
        <div className="flex flex-col gap-3">
          <Field label={t('costCentres.code')}>
            <Input value={centro.code} readOnly disabled />
          </Field>
          <Field label={t('costCentres.name')}>
            <Input value={name} onChange={(e) => setName(e.target.value)} />
          </Field>
          <Field label={t('costCentres.descriptionField')}>
            <Input
              value={description}
              onChange={(e) => setDescription(e.target.value)}
            />
          </Field>
          <Field label={t('costCentres.owner')}>
            <Input value={owner} onChange={(e) => setOwner(e.target.value)} />
          </Field>
        </div>
        <DialogFooter>
          <Button
            disabled={pending || name.trim() === ''}
            onClick={() =>
              onSubmit({ ...centro, name: name.trim(), description, owner })
            }
          >
            {t('costCentres.save')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function NuevoCostCentreDialog({
  open,
  onOpenChange,
  onSubmit,
  pending,
}: {
  open: boolean
  onOpenChange: (v: boolean) => void
  onSubmit: (b: Pick<CostCenter, 'code' | 'name' | 'owner'>) => void
  pending: boolean
}) {
  const { t } = useTranslation('finops')
  const [code, setCode] = useState('')
  const [name, setName] = useState('')
  const [owner, setOwner] = useState('')
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t('costCentres.new')}</DialogTitle>
          <DialogDescription>
            {t('costCentres.newDescription')}
          </DialogDescription>
        </DialogHeader>
        <div className="flex flex-col gap-3">
          <Field label={t('costCentres.code')}>
            <Input value={code} onChange={(e) => setCode(e.target.value)} />
          </Field>
          <Field label={t('costCentres.name')}>
            <Input value={name} onChange={(e) => setName(e.target.value)} />
          </Field>
          <Field label={t('costCentres.owner')}>
            <Input value={owner} onChange={(e) => setOwner(e.target.value)} />
          </Field>
        </div>
        <DialogFooter>
          <Button
            disabled={pending || code.trim() === ''}
            onClick={() => onSubmit({ code: code.trim(), name, owner })}
          >
            {t('costCentres.create')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function NuevaReglaDialog({
  open,
  onOpenChange,
  onSubmit,
  pending,
}: {
  open: boolean
  onOpenChange: (v: boolean) => void
  onSubmit: (b: {
    source_dimension: string
    source_key: string
    priority: number
  }) => void
  pending: boolean
}) {
  const { t } = useTranslation('finops')
  // Las seis dimensiones que `resolveCostCenter` consulta (`costcenter.go:415`). Ni una más:
  // una dimensión que el motor no mira produce una regla que nunca casa y nadie sabe por qué.
  const DIMS = [
    'team',
    'workspace',
    'project',
    'agent',
    'provider',
    'identity',
  ] as const
  const [dim, setDim] = useState<string>('team')
  const [key, setKey] = useState('')
  const [prio, setPrio] = useState('100')
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t('costCentres.newRule')}</DialogTitle>
          <DialogDescription>
            {t('costCentres.newRuleDescription')}
          </DialogDescription>
        </DialogHeader>
        <div className="flex flex-col gap-3">
          <Field label={t('costCentres.dimensionLabel')}>
            <Select value={dim} onValueChange={setDim}>
              <SelectTrigger aria-label={t('costCentres.dimensionLabel')}>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {DIMS.map((d) => (
                  <SelectItem key={d} value={d}>
                    {d}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>
          <Field label={t('costCentres.key')}>
            <Input value={key} onChange={(e) => setKey(e.target.value)} />
          </Field>
          <Field label={t('costCentres.priorityLabel')}>
            <Input
              type="number"
              value={prio}
              onChange={(e) => setPrio(e.target.value)}
            />
          </Field>
        </div>
        <DialogFooter>
          <Button
            disabled={pending || key.trim() === ''}
            onClick={() =>
              onSubmit({
                source_dimension: dim,
                source_key: key.trim(),
                priority: Number(prio) || 0,
              })
            }
          >
            {t('costCentres.create')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

// --- utilización de asientos (C07-04) ----------------------------------------
//
// ⛔ ES LA MEDICIÓN QUE DICE SI SE PAGA POR ASIENTOS QUE NADIE USA, y la consola no la llamaba
//    (`modules/finops/api.go:70`). Cruza los asientos asignados contra los actores realmente
//    activos por día.
//
// ⛔ Y `has_seats` DISTINGUE DOS COSAS QUE UN PORCENTAJE FUNDE, dicho por el propio DTO
//    (`modules/finops/seats.go:137-140`): «distingue "no se publicó snapshot de asientos para
//    este día" de un día real con cero asignados». Además `utilization_pct` vale 0 cuando el
//    denominador es 0 o no se reportó — **«no fabricated percentage»**.
//
//    ⇒ Pintar `0 %` en un día sin snapshot afirma **«nadie usó sus asientos»** cuando la verdad
//    es **«nadie publicó cuántos había»**. La primera lectura lleva a cancelar licencias; la
//    segunda, a arreglar la ingesta. Son acciones opuestas a partir del mismo píxel.
function SeatsTab() {
  const { t } = useTranslation('finops')
  const { activeTenant } = useAuth()
  const [provider, setProvider] = useState('anthropic')

  const q = useQuery({
    queryKey: finopsKeys.seatUtilization(activeTenant, provider),
    // El motor EXIGE `provider` (400 sin él, `seats.go:166-168`), así que nunca se pide sin uno.
    queryFn: () =>
      finopsApi.seatUtilization({ provider }, { tenant: activeTenant }),
  })

  return (
    <SectionCard
      title={t('seats.title')}
      description={t('seats.description')}
      actions={
        <Select value={provider} onValueChange={setProvider}>
          <SelectTrigger className="w-44" aria-label={t('seats.provider')}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="anthropic">anthropic</SelectItem>
            <SelectItem value="openai">openai</SelectItem>
          </SelectContent>
        </Select>
      }
    >
      <AsyncSection query={q} skeletonHeight={220}>
        {(res) => {
          const dias = ((res as { days?: unknown[] })?.days ?? []) as Array<{
            day: string
            assigned_seats: number
            active_actors: number
            utilization_pct: number
            has_seats: boolean
          }>
          return dias.length === 0 ? (
            <EmptyState title={t('seats.empty')} />
          ) : (
            <div className="flex flex-col gap-1">
              {dias.map((d) => (
                <div
                  key={d.day}
                  className="flex flex-wrap items-center justify-between gap-3 rounded-md border border-border px-3 py-2 text-sm"
                >
                  <span className="font-mono text-xs">{d.day}</span>
                  <span className="text-xs text-muted-foreground">
                    {/* Sin snapshot NO se habla de asientos: no los hay que contar. */}
                    {d.has_seats
                      ? t('seats.assigned', {
                          assigned: d.assigned_seats,
                          active: d.active_actors,
                        })
                      : t('seats.noSnapshot')}
                  </span>
                  <span className="font-medium">
                    {/* El porcentaje SÓLO existe con denominador. */}
                    {d.has_seats ? `${Math.round(d.utilization_pct)}%` : '—'}
                  </span>
                </div>
              ))}
            </div>
          )
        }}
      </AsyncSection>
    </SectionCard>
  )
}

// --- coste por resultado (C07-04) --------------------------------------------
//
// ⛔ POR QUÉ EXISTE, y por qué la escribo el mismo día que la guarda que la exige: añadí el
//    cliente de `/value/summary` con su prueba de contrato y **sin ninguna pantalla que lo
//    pulsara**. Un cliente sin llamante pasa todas sus pruebas y no hace la operación posible.
//
// ⛔ Y LAS DOS COSAS QUE ESTA PANTALLA NO PUEDE FUNDIR, porque los dos son gastos y sólo uno
//    está atribuido:
//    1. `total_cost_micro_usd` es el gasto ENTERO — los cubos MÁS `unattributed`
//       (`modules/finops/dto.go:236-238`). Pintar la suma de los cubos y llamarlo «el gasto»
//       infra-declara la factura, y hacia abajo. Aquí se pintan los dos, y lo no atribuido
//       tiene su propia cifra en vez de desaparecer dentro del total.
//    2. `has_outcomes: false` NO es `outcomes: 0`. «No lo medimos» y «medimos cero» son
//       distintos, y sólo el segundo permite hablar de coste por resultado.
function ValueTab({ tenant }: { tenant: string | null }) {
  const { t } = useTranslation('finops')

  const q = useQuery({
    queryKey: ['finops', tenant, 'value', 'summary'] as const,
    queryFn: () => finopsApi.valueSummary(),
  })

  return (
    <AsyncSection query={q} skeletonHeight={280}>
      {(s) => (
        <div className="flex flex-col gap-4">
          <SectionCard
            title={t('value.title')}
            description={t('value.description')}
          >
            <StatGrid>
              <MetricStat
                label={t('value.totalCost')}
                value={formatMicroUsd(s.total_cost_micro_usd)}
              />
              {/* Lo NO atribuido va con su propia cifra: es parte del total y no tiene dueño.
                  Esconderlo dentro del total es lo que convierte una factura en un resumen. */}
              <MetricStat
                label={t('value.unattributed')}
                value={
                  s.unattributed_cost_micro_usd === undefined
                    ? '—'
                    : formatMicroUsd(s.unattributed_cost_micro_usd)
                }
              />
              <MetricStat
                label={t('value.outcomes')}
                value={
                  s.total_outcomes === 0 &&
                  s.satisfied === 0 &&
                  s.unsatisfied === 0
                    ? '—'
                    : `${s.satisfied}/${s.total_outcomes}`
                }
              />
              <MetricStat
                label={t('value.costPerOutcome')}
                value={
                  s.cost_per_outcome_micro_usd === undefined
                    ? '—'
                    : formatMicroUsd(s.cost_per_outcome_micro_usd)
                }
              />
            </StatGrid>
            {s.note ? (
              <p className="mt-3 text-xs text-muted-foreground">{s.note}</p>
            ) : null}
          </SectionCard>

          <SectionCard
            title={t('value.riskTitle')}
            description={t('value.riskDescription')}
          >
            {(s.cancellation_risk ?? []).length === 0 ? (
              <EmptyState title={t('value.riskEmpty')} />
            ) : (
              <div className="flex flex-col gap-2">
                {(s.cancellation_risk ?? []).map((r) => (
                  <div
                    key={`${r.dimension}:${r.key}`}
                    className="flex flex-wrap items-center justify-between gap-3 rounded-md border border-border p-3"
                  >
                    <div className="flex min-w-0 flex-col gap-1">
                      <div className="flex items-center gap-2">
                        <Badge variant="warning">{r.dimension}</Badge>
                        <span className="font-mono text-sm">{r.key}</span>
                      </div>
                      <p className="text-xs text-muted-foreground">
                        {r.reason}
                      </p>
                    </div>
                    <div className="text-right">
                      <p className="font-medium">
                        {formatMicroUsd(r.cost_micro_usd)}
                      </p>
                      <p className="text-xs text-muted-foreground">
                        {t('value.satisfiedOf', {
                          satisfied: r.satisfied,
                          outcomes: r.outcomes,
                        })}
                      </p>
                    </div>
                  </div>
                ))}
              </div>
            )}
          </SectionCard>
        </div>
      )}
    </AsyncSection>
  )
}

export function FinOpsView() {
  const { t } = useTranslation(['finops', 'common'])
  const { activeTenant, can } = useAuth()
  const [rangeId, setRangeId] = useState('30d')
  const since = useMemo(() => sinceFor(rangeId), [rangeId])
  const params = useMemo(() => ({ since }), [since])

  const summaryQ = useQuery({
    queryKey: finopsKeys.summary(activeTenant, params),
    queryFn: () => finopsApi.summary(params),
  })
  const trendQ = useQuery({
    queryKey: finopsKeys.trend(activeTenant, params),
    queryFn: () => finopsApi.trend(params),
  })
  const forecastQ = useQuery({
    queryKey: finopsKeys.forecast(activeTenant, 'monthly'),
    queryFn: () => finopsApi.forecast('monthly'),
  })
  const recsQ = useQuery({
    queryKey: finopsKeys.recommendations(activeTenant),
    queryFn: () => finopsApi.recommendations(),
  })

  const canWriteBudget = can('finops:budget:write')

  return (
    <IntelPage
      icon={Coins}
      title={t('title')}
      description={t('description')}
      actions={
        <Select value={rangeId} onValueChange={setRangeId}>
          <SelectTrigger className="w-40" aria-label={t('range.label')}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="7d">{t('range.7d')}</SelectItem>
            <SelectItem value="30d">{t('range.30d')}</SelectItem>
            <SelectItem value="90d">{t('range.90d')}</SelectItem>
            <SelectItem value="mtd">{t('range.mtd')}</SelectItem>
          </SelectContent>
        </Select>
      }
    >
      <Tabs defaultValue="spend">
        <TabsList>
          <TabsTrigger value="spend">{t('tabs.spend')}</TabsTrigger>
          <TabsTrigger value="chargeback">{t('tabs.chargeback')}</TabsTrigger>
          <TabsTrigger value="reconciliation">
            {t('tabs.reconciliation')}
          </TabsTrigger>
          <TabsTrigger value="budgets">{t('tabs.budgets')}</TabsTrigger>
          <TabsTrigger value="optimization">
            {t('tabs.optimization')}
          </TabsTrigger>
          <TabsTrigger value="value">{t('tabs.value')}</TabsTrigger>
          <TabsTrigger value="seats">{t('tabs.seats')}</TabsTrigger>
          <TabsTrigger value="costcentres">{t('tabs.costCentres')}</TabsTrigger>
        </TabsList>

        <TabsContent value="spend" className="flex flex-col gap-4">
          <AsyncSection query={summaryQ} skeletonHeight={96}>
            {(summary) => (
              <SpendStats summary={summary} forecast={forecastQ.data} />
            )}
          </AsyncSection>
          <AsyncSection query={trendQ} skeletonHeight={300}>
            {(trend) => <CostTrend trend={trend} />}
          </AsyncSection>
          <AsyncSection query={summaryQ} skeletonHeight={320}>
            {(summary) => <SpendBreakdown summary={summary} />}
          </AsyncSection>
          <AsyncSection query={summaryQ} skeletonHeight={240}>
            {(summary) => <CacheEfficiencyPanel cache={summary.cache} />}
          </AsyncSection>
          <AsyncSection query={forecastQ} skeletonHeight={180}>
            {(forecast) => <ForecastCard forecast={forecast} />}
          </AsyncSection>
        </TabsContent>

        <TabsContent value="chargeback" className="flex flex-col gap-4">
          <ChargebackTab range={params} canExport={can('finops:spend:read')} />
          <StatementsSection />
        </TabsContent>

        <TabsContent value="reconciliation" className="flex flex-col gap-4">
          <ReconciliationTab range={params} />
          <AllocationSection range={params} />
          <FutureDimensionsPanel />
        </TabsContent>

        <TabsContent value="budgets" className="flex flex-col gap-4">
          <BudgetsTab canWrite={canWriteBudget} />
        </TabsContent>

        <TabsContent value="optimization" className="flex flex-col gap-4">
          <SectionCard
            title={t('optimization.title')}
            description={t('optimization.description')}
          >
            <AsyncSection query={recsQ} skeletonHeight={180}>
              {(recs) =>
                recs.recommendations.length === 0 ? (
                  <EmptyState title={t('optimization.empty')} />
                ) : (
                  <div className="flex flex-col gap-3">
                    {recs.recommendations.map((rec, i) => (
                      <RecommendationCard
                        key={`${rec.kind}-${rec.subject}-${i}`}
                        rec={rec}
                      />
                    ))}
                  </div>
                )
              }
            </AsyncSection>
          </SectionCard>
        </TabsContent>
        <TabsContent value="value" className="flex flex-col gap-4">
          <ValueTab tenant={activeTenant} />
        </TabsContent>

        <TabsContent value="seats" className="flex flex-col gap-4">
          <SeatsTab />
        </TabsContent>

        <TabsContent value="costcentres" className="flex flex-col gap-4">
          <CostCentresTab />
        </TabsContent>
      </Tabs>
    </IntelPage>
  )
}

// --- chargeback tab (free-form 15-dimension slice + FOCUS export) ------------

/**
 * Extractos de reparto (chargeback statements).
 *
 * ⛔ EL MOTOR LOS GENERA, LISTA, DETALLA Y EXPORTA DESDE Y LA CONSOLA NO EXPONÍA NINGUNO. Los
 *    cuatro métodos de cliente existían sin una sola pantalla que los pulsara, y los componentes que
 *    los pintan llevaban escritos —y muertos— en `chargeback-components.tsx`. Esto los cablea.
 *
 * ⛔ TRES COSAS QUE ESTA PANTALLA DICE Y UNA INGENUA CALLARÍA:
 *
 *    1. **Un extracto es un SNAPSHOT congelado**, no una vista viva: se calcula al generarlo y
 *       denormaliza el nombre y el código del centro de coste. Regenerar el mismo periodo NO lo
 *       recalcula — el motor es idempotente y se salta el conflicto—, así que un gasto ingerido
 *       después no aparece. Quien no lo sepa mira el mismo total mañana y concluye que el producto
 *       no suma.
 *    2. **`delta_pct` viene en CENTÉSIMAS de punto** y vale 0 en tres situaciones distintas: sin
 *       extracto anterior, con un anterior de total cero, y con un cambio real del 0 %. El
 *       componente ya oculta la insignia cuando es 0 en vez de afirmar «sin cambio», que es lo
 *       correcto: sólo se pinta un porcentaje cuando de verdad se pudo calcular.
 *    3. **`sample_count` por línea es un DENOMINADOR**: un coste sobre una muestra y sobre mil no
 *       son la misma afirmación. La tabla del detalle lo trae ya.
 *
 * ⚠ Y el estado: el motor sólo escribe `draft` y no publica ninguna operación que finalice un
 *   extracto. Se muestra tal cual; inventar un «final» sería afirmar un ciclo de vida que no existe.
 */
function StatementsSection() {
  const { t } = useTranslation('finops')
  const qc = useQueryClient()
  const { activeTenant, can } = useAuth()
  const reportFailure = useFailedActionReporter()
  const [seleccion, setSeleccion] = useState<string | null>(null)
  const [periodo, setPeriodo] = useState<'monthly' | 'weekly'>('monthly')

  const lista = useQuery({
    queryKey: finopsKeys.statements(activeTenant),
    queryFn: () => finopsApi.statements(undefined, { tenant: activeTenant }),
  })
  const detalle = useQuery({
    queryKey: finopsKeys.statement(activeTenant, seleccion as string),
    queryFn: () =>
      finopsApi.statement(seleccion as string, { tenant: activeTenant }),
    enabled: Boolean(seleccion),
  })

  // El motor exige RFC3339 y sólo acepta `monthly` o `weekly`; el inicio se deriva del periodo
  // elegido para que el usuario no tenga que teclear una fecha con formato.
  const inicioPeriodo = () => {
    const hoy = new Date()
    if (periodo === 'monthly') {
      return new Date(
        Date.UTC(hoy.getUTCFullYear(), hoy.getUTCMonth(), 1),
      ).toISOString()
    }
    const d = new Date(
      Date.UTC(hoy.getUTCFullYear(), hoy.getUTCMonth(), hoy.getUTCDate()),
    )
    d.setUTCDate(d.getUTCDate() - d.getUTCDay())
    return d.toISOString()
  }

  const generar = useMutation({
    mutationFn: ({
      body,
      tenant,
    }: {
      body: Parameters<typeof finopsApi.generateStatements>[0]
      tenant: string | null
    }) => finopsApi.generateStatements(body, { tenant }),
    onSuccess: (_data, { tenant }) => {
      void qc.invalidateQueries({
        queryKey: finopsKeys.statements(tenant),
      })
      toast.success(t('statements.generated'))
    },
    onError: (e) => reportFailure(e as ApiError),
  })

  const exportar = useMutation({
    mutationFn: (id: string) => fetchStatementExport(id),
    onSuccess: (blob) => {
      const url = URL.createObjectURL(blob)
      const a = document.createElement('a')
      a.href = url
      a.download = `olivares-statement-${seleccion ?? 'export'}.csv`
      document.body.appendChild(a)
      a.click()
      a.remove()
      URL.revokeObjectURL(url)
      toast.success(t('export.done'))
    },
    onError: (e) => reportFailure(e as ApiError),
  })

  const items = (lista.data as { items?: ChargebackStatement[] })?.items ?? []
  const abierto = detalle.data as ChargebackStatement | undefined

  return (
    <SectionCard
      title={t('statements.title')}
      description={t('statements.description')}
      actions={
        can('finops:budget:write') ? (
          <div className="flex items-center gap-2">
            <Select
              value={periodo}
              onValueChange={(v) => setPeriodo(v as 'monthly' | 'weekly')}
            >
              <SelectTrigger
                className="w-36"
                aria-label={t('statements.period')}
              >
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="monthly">
                  {t('statements.monthly')}
                </SelectItem>
                <SelectItem value="weekly">{t('statements.weekly')}</SelectItem>
              </SelectContent>
            </Select>
            <Button
              size="sm"
              disabled={generar.isPending}
              onClick={() =>
                generar.mutate({
                  body: {
                    period: periodo,
                    period_start: inicioPeriodo(),
                  },
                  tenant: activeTenant,
                })
              }
            >
              <Plus className="mr-1 h-4 w-4" />
              {t('statements.generate')}
            </Button>
          </div>
        ) : null
      }
    >
      {/* ⛔ VA ARRIBA, no en una nota al pie: un extracto es un snapshot y regenerarlo no recalcula
          el periodo ya emitido. Sin esto, quien ingiera gasto después mira el mismo total y concluye
          que el producto no suma. */}
      <CaveatNotice tone="info" className="mb-3">
        {t('statements.snapshotNotice')}
      </CaveatNotice>
      <ListTruncationBadge
        query={lista}
        label={t('statements.truncated', { n: lista.data?.items?.length ?? 0 })}
        hint={t('statements.truncatedHint')}
      />
      <AsyncSection query={lista}>
        {() => (
          <div className="flex flex-col gap-4">
            <StatementList
              statements={items}
              onSelect={(s) => setSeleccion(s.id === seleccion ? null : s.id)}
            />
            {abierto ? (
              <StatementDetail
                statement={abierto}
                onExport={() => exportar.mutate(abierto.id)}
              />
            ) : null}
          </div>
        )}
      </AsyncSection>
    </SectionCard>
  )
}

function ChargebackTab({
  range,
  canExport,
}: {
  range: { since: string }
  canExport: boolean
}) {
  const { t } = useTranslation(['finops', 'common'])
  const { activeTenant } = useAuth()
  const [dimension, setDimension] = useState<SpendDimension>('workspace')
  const params = useMemo(() => ({ since: range.since }), [range.since])

  const spendQ = useQuery({
    queryKey: finopsKeys.spend(activeTenant, dimension, params),
    queryFn: () => finopsApi.spend(dimension, params),
  })

  const picker = (
    <Select
      value={dimension}
      onValueChange={(v) => setDimension(v as SpendDimension)}
    >
      <SelectTrigger
        className="w-44"
        aria-label={t('chargeback.dimensionLabel')}
      >
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        {SPEND_DIMENSIONS.map((d) => (
          <SelectItem key={d} value={d}>
            {t(`dimensions.${d}`, { defaultValue: d })}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )

  return (
    <>
      {canExport ? <FocusExportCard range={range} /> : null}
      <AsyncSection query={spendQ} skeletonHeight={360}>
        {(spend) => (
          <DimensionBreakdown
            dimension={dimension}
            spend={spend}
            picker={picker}
          />
        )}
      </AsyncSection>
    </>
  )
}

// --- FOCUS export (text/csv blob download) -----------------------------------

const PROVENANCES: ExportProvenance[] = ['estimated', 'billed', 'all']

function FocusExportCard({ range }: { range: { since: string } }) {
  const { t } = useTranslation('finops')
  const [provenance, setProvenance] = useState<ExportProvenance>('estimated')

  const exportM = useMutation({
    mutationFn: () => fetchFocusExport({ provenance, since: range.since }),
    onSuccess: (blob) => {
      const url = URL.createObjectURL(blob)
      const a = document.createElement('a')
      a.href = url
      a.download = `olivares-focus-${provenance}.csv`
      document.body.appendChild(a)
      a.click()
      a.remove()
      URL.revokeObjectURL(url)
      toast.success(t('export.done'))
    },
    onError: (e: unknown) =>
      toast.error(
        e instanceof ApiError ? e.message : String((e as Error).message ?? e),
      ),
  })

  return (
    <SectionCard
      title={t('export.title')}
      description={t('export.description')}
      actions={
        <div className="flex items-center gap-2">
          <Select
            value={provenance}
            onValueChange={(v) => setProvenance(v as ExportProvenance)}
          >
            <SelectTrigger
              className="w-40"
              aria-label={t('export.provenanceLabel')}
            >
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {PROVENANCES.map((p) => (
                <SelectItem key={p} value={p}>
                  {t(`export.provenance.${p}`)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <Button
            variant="secondary"
            size="sm"
            disabled={exportM.isPending}
            onClick={() => exportM.mutate()}
          >
            <Download />
            {t('export.download')}
          </Button>
        </div>
      }
    >
      <p className="text-xs text-muted-foreground">{t('export.note')}</p>
    </SectionCard>
  )
}

// --- reconciliation + allocation sections ------------------------------------

function ReconciliationTab({ range }: { range: { since: string } }) {
  const { activeTenant } = useAuth()
  const params = useMemo(() => ({ since: range.since }), [range.since])
  const reconQ = useQuery({
    queryKey: finopsKeys.reconciliation(activeTenant, params),
    queryFn: () => finopsApi.reconciliation(params),
  })
  return (
    <AsyncSection query={reconQ} skeletonHeight={320}>
      {(reconciliation) => (
        <ReconciliationView reconciliation={reconciliation} />
      )}
    </AsyncSection>
  )
}

function AllocationSection({ range }: { range: { since: string } }) {
  const { activeTenant } = useAuth()
  const params = useMemo(() => ({ since: range.since }), [range.since])
  const allocQ = useQuery({
    queryKey: finopsKeys.allocation(activeTenant, params),
    queryFn: () => finopsApi.allocation(params),
  })
  return (
    <AsyncSection query={allocQ} skeletonHeight={260}>
      {(allocation) => <AllocationTable allocation={allocation} />}
    </AsyncSection>
  )
}

// --- budgets tab (fans out a status query per budget) ------------------------

function BudgetsTab({ canWrite }: { canWrite: boolean }) {
  const { t } = useTranslation(['finops', 'common'])
  const report = useFailedActionReporter()
  const { activeTenant } = useAuth()
  const qc = useQueryClient()
  const [dialogOpen, setDialogOpen] = useState(false)
  // Edit reuses the create dialog in edit mode; delete uses a confirm dialog.
  const [editing, setEditing] = useState<Budget | null>(null)
  const [deleting, setDeleting] = useState<Budget | null>(null)

  const del = useMutation({
    mutationFn: (id: string) => finopsApi.deleteBudget(id),
    onSuccess: () => {
      toast.success(t('budgets.deleteDialog.deleted'))
      void qc.invalidateQueries({ queryKey: finopsKeys.budgets(activeTenant) })
      setDeleting(null)
    },
    onError: (e: unknown) => {
      // ⛔ La limpieza se queda aquí —es de esta pantalla— y el REPORTE se delega en la
      // política común (lib/hooks/use-privileged-mutation.ts:33-59), que separa la ceremonia
      // del rol. `isForbidden` es sólo el status 403 (lib/api/errors.ts:59) y un
      // `step_up_required` lo satisface también: leerlo primero acusaba al operador de no
      // tener un permiso que SÍ tiene.
      // ⛔ La limpieza es SÓLO de las negativas, como antes. Moverla delante de `report`
      // sin condición cerraba el diálogo también ante red o 500 —donde el original lo dejaba
      // abierto para reintentar— y eso es comportamiento perdido, no refactor. Lo cazó el
      // contraste `sol max`.
      if (e instanceof ApiError && (e.isForbidden || e.isStepUpRequired)) {
        setDeleting(null)
      }
      report(e)
    },
  })

  const budgetsQ = useQuery({
    queryKey: finopsKeys.budgets(activeTenant),
    queryFn: () => finopsApi.budgets(),
  })
  const alertsQ = useQuery({
    queryKey: finopsKeys.alerts(activeTenant),
    queryFn: () => finopsApi.alerts(),
  })

  const budgets = budgetsQ.data?.items ?? []
  const statusQueries = useQueries({
    queries: budgets.map((b) => ({
      queryKey: finopsKeys.budgetStatus(activeTenant, b.id),
      queryFn: () => finopsApi.budgetStatus(b.id),
    })),
  })
  const statuses = statusQueries
    .map((q) => q.data)
    .filter((s): s is BudgetStatus => s !== undefined)

  return (
    <>
      <SectionCard
        title={t('budgets.title')}
        description={t('budgets.description')}
        actions={
          canWrite ? (
            <Button
              variant="primary"
              size="sm"
              onClick={() => setDialogOpen(true)}
            >
              <Plus />
              {t('budgets.new')}
            </Button>
          ) : null
        }
      >
        <ListTruncationBadge
          query={budgetsQ}
          label={t('budgets.truncated', {
            n: budgetsQ.data?.items?.length ?? 0,
          })}
          hint={t('budgets.truncatedHint')}
        />
        <AsyncSection query={budgetsQ} skeletonHeight={160}>
          {(list) =>
            list.items.length === 0 ? (
              <EmptyState
                title={t('budgets.empty')}
                description={t('budgets.emptyHint')}
              />
            ) : (
              <div className="grid gap-3 md:grid-cols-2">
                {budgets.map((b, i) => {
                  const status = statuses.find((s) => s.id === b.id)
                  return status ? (
                    <BudgetCard
                      key={b.id}
                      status={status}
                      actions={
                        canWrite ? (
                          <>
                            <Button
                              variant="ghost"
                              size="icon-sm"
                              aria-label={t('budgets.editAction', {
                                name: b.name,
                              })}
                              onClick={() => setEditing(b)}
                            >
                              <Pencil />
                            </Button>
                            <Button
                              variant="ghost"
                              size="icon-sm"
                              aria-label={t('budgets.deleteAction', {
                                name: b.name,
                              })}
                              onClick={() => setDeleting(b)}
                            >
                              <Trash2 />
                            </Button>
                          </>
                        ) : null
                      }
                    />
                  ) : (
                    <div key={b.id ?? i} role="status">
                      <span className="sr-only">
                        {t('common:states.loading')}
                      </span>
                      <Skeleton className="h-32 w-full" />
                    </div>
                  )
                })}
              </div>
            )
          }
        </AsyncSection>
      </SectionCard>

      <SectionCard
        title={t('alerts.title')}
        description={t('alerts.description')}
        noPadding
      >
        <div className="p-4">
          <ListTruncationBadge
            query={alertsQ}
            label={t('alerts.truncated', {
              n: alertsQ.data?.items?.length ?? 0,
            })}
            hint={t('alerts.truncatedHint')}
          />
          <AsyncSection query={alertsQ} skeletonHeight={140}>
            {(list) =>
              list.items.length === 0 ? (
                <EmptyState title={t('alerts.empty')} />
              ) : (
                <AlertsTable alerts={list.items} />
              )
            }
          </AsyncSection>
        </div>
      </SectionCard>

      {canWrite ? (
        <>
          <BudgetDialog open={dialogOpen} onOpenChange={setDialogOpen} />
          <BudgetDialog
            key={editing?.id ?? 'new'}
            open={editing != null}
            onOpenChange={(v) => {
              if (!v) setEditing(null)
            }}
            budget={editing ?? undefined}
          />
          <Dialog
            open={deleting != null}
            onOpenChange={(v) => {
              if (!v) setDeleting(null)
            }}
          >
            <DialogContent>
              <DialogHeader>
                <DialogTitle>{t('budgets.deleteDialog.title')}</DialogTitle>
                <DialogDescription>
                  {t('budgets.deleteDialog.description', {
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
                  onClick={() => deleting && del.mutate(deleting.id)}
                  disabled={del.isPending}
                >
                  {t('budgets.deleteDialog.confirm')}
                </Button>
              </DialogFooter>
            </DialogContent>
          </Dialog>
        </>
      ) : null}
    </>
  )
}

// --- create-budget dialog (the privileged write) -----------------------------

// The create-budget form uses BUDGET_DIMENSIONS (cost_type excluded — it never
// accrues on the estimated stream budgets aggregate; schema.go:budgetDimensions).
const PERIODS: BudgetPeriod[] = ['daily', 'weekly', 'monthly', 'total']
const ACTIONS: BudgetAction[] = ['alert', 'throttle', 'block']

function BudgetDialog({
  open,
  onOpenChange,
  budget,
}: {
  open: boolean
  onOpenChange: (v: boolean) => void
  /** When present, the dialog edits this budget (PUT) instead of creating one. The
   * caller re-keys the dialog per budget so these initial values seed fresh state. */
  budget?: Budget
}) {
  const { t } = useTranslation(['finops', 'common'])
  const report = useFailedActionReporter()
  const { activeTenant } = useAuth()
  const qc = useQueryClient()
  const isEdit = budget != null
  const [name, setName] = useState(budget?.name ?? '')
  const [dimension, setDimension] = useState<SpendDimension>(
    budget?.dimension ?? 'global',
  )
  const [key, setKey] = useState(budget?.key ?? '')
  const [limitUsd, setLimitUsd] = useState(
    budget ? String(budget.limit_micro_usd / 1_000_000) : '',
  )
  const [reservedUsd, setReservedUsd] = useState(
    budget?.reserved_micro_usd
      ? String(budget.reserved_micro_usd / 1_000_000)
      : '',
  )
  const [period, setPeriod] = useState<BudgetPeriod>(
    budget?.period ?? 'monthly',
  )
  const [action, setAction] = useState<BudgetAction>(budget?.action ?? 'alert')

  const save = useMutation({
    mutationFn: () => {
      const body = {
        name: name.trim(),
        dimension,
        key: dimension === 'global' ? undefined : key.trim(),
        limit_micro_usd: Math.round(Number(limitUsd) * 1_000_000),
        reserved_micro_usd: reservedUsd
          ? Math.round(Number(reservedUsd) * 1_000_000)
          : undefined,
        period,
        action,
      }
      return isEdit
        ? finopsApi.updateBudget(budget.id, body)
        : finopsApi.createBudget(body)
    },
    onSuccess: () => {
      toast.success(
        isEdit ? t('budgets.dialog.saved') : t('budgets.dialog.created'),
      )
      void qc.invalidateQueries({ queryKey: finopsKeys.budgets(activeTenant) })
      if (isEdit) {
        void qc.invalidateQueries({
          queryKey: finopsKeys.budgetStatus(activeTenant, budget.id),
        })
      }
      onOpenChange(false)
      if (!isEdit) {
        setName('')
        setKey('')
        setLimitUsd('')
        setReservedUsd('')
        setAction('alert')
      }
    },
    onError: (e: unknown) => {
      report(e)
    },
  })

  const valid =
    name.trim().length > 0 &&
    Number(limitUsd) > 0 &&
    (reservedUsd === '' || Number(reservedUsd) >= 0) &&
    (dimension === 'global' || key.trim().length > 0)

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>
            {isEdit ? t('budgets.dialog.editTitle') : t('budgets.dialog.title')}
          </DialogTitle>
          <DialogDescription>{t('budgets.description')}</DialogDescription>
        </DialogHeader>
        <form
          className="flex flex-col gap-3"
          onSubmit={(e) => {
            e.preventDefault()
            if (valid) save.mutate()
          }}
        >
          <Field label={t('budgets.dialog.name')} required>
            {({ id }) => (
              <Input
                id={id}
                value={name}
                onChange={(e) => setName(e.target.value)}
              />
            )}
          </Field>
          <div className="grid grid-cols-2 gap-3">
            <Field label={t('budgets.dialog.dimension')}>
              <Select
                value={dimension}
                onValueChange={(v) => setDimension(v as SpendDimension)}
              >
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {BUDGET_DIMENSIONS.map((d) => (
                    <SelectItem key={d} value={d}>
                      {t(`dimensions.${d}`, { defaultValue: d })}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>
            <Field label={t('budgets.dialog.period')}>
              <Select
                value={period}
                onValueChange={(v) => setPeriod(v as BudgetPeriod)}
              >
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {PERIODS.map((p) => (
                    <SelectItem key={p} value={p}>
                      {t(`periods.${p}`)}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>
          </div>
          {dimension !== 'global' ? (
            <Field
              label={t('budgets.dialog.key')}
              description={t('budgets.dialog.keyHint')}
              required
            >
              {({ id }) => (
                <Input
                  id={id}
                  value={key}
                  onChange={(e) => setKey(e.target.value)}
                />
              )}
            </Field>
          ) : null}
          <div className="grid grid-cols-2 gap-3">
            <Field label={t('budgets.dialog.limitUsd')} required>
              {({ id }) => (
                <Input
                  id={id}
                  type="number"
                  min="0"
                  step="0.01"
                  value={limitUsd}
                  onChange={(e) => setLimitUsd(e.target.value)}
                />
              )}
            </Field>
            <Field
              label={t('budgets.dialog.reservedUsd')}
              description={t('budgets.dialog.reservedHint')}
            >
              {({ id }) => (
                <Input
                  id={id}
                  type="number"
                  min="0"
                  step="0.01"
                  value={reservedUsd}
                  onChange={(e) => setReservedUsd(e.target.value)}
                />
              )}
            </Field>
          </div>
          <Field
            label={t('budgets.dialog.action')}
            description={t('budgets.dialog.actionHint')}
          >
            <Select
              value={action}
              onValueChange={(v) => setAction(v as BudgetAction)}
            >
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {ACTIONS.map((a) => (
                  <SelectItem key={a} value={a}>
                    {t(`budgets.actions.${a}`)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>
          <DialogFooter>
            <Button
              type="button"
              variant="ghost"
              onClick={() => onOpenChange(false)}
            >
              {t('common:actions.cancel')}
            </Button>
            <Button
              type="submit"
              variant="primary"
              disabled={!valid || save.isPending}
            >
              {isEdit ? t('budgets.dialog.save') : t('budgets.dialog.create')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
