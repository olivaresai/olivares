// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Evals (module XII) — the container. It wires the queries (scorecards/runs/results/
// A/B), the tabs, the run-detail dialog (a privileged, self-audited read of per-case
// results), and RBAC gating, then composes the pure pieces inside <IntelPage>. It
// computes nothing about quality — Does; this presents. The candidate output is
// never fetched or shown — only outcomes, scores, labels and hash fingerprints.
import { useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ClipboardCheck } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { formatFraction } from '@/lib/format'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Checkbox } from '@/components/ui/checkbox'
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
import { Spinner } from '@/components/ui/spinner'
import { toast } from '@/components/ui/toaster'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { Textarea } from '@/components/ui/textarea'
import { useAuth } from '@/lib/auth/context'
import { ApiError } from '@/lib/api/errors'
import {
  AsyncSection,
  CaveatNotice,
  IntelPage,
  ListTruncationBadge,
  SeamBadge,
  SectionCard,
  SelfAuditNotice,
} from '@/features/_intel'
import { evalsApi, evalsKeys, type ScorecardGroupBy } from './api'
import {
  AbComparison,
  CaseOutcomeBar,
  CaseResultsTable,
  DriftChart,
  RunsTable,
  ScorecardGrid,
} from './components'
import type {
  AbRequest,
  EvalRun,
  GateEvaluation,
  Scorecard,
  Suite,
} from './types'
import './i18n'

const GROUP_BY: ScorecardGroupBy[] = [
  'agent',
  'model',
  'suite',
  'prompt_variant',
]

export function EvalsView() {
  const { t } = useTranslation('evals')
  const { activeTenant, can } = useAuth()

  // Read permission to enter the view (verbs collapse to a single read grant).
  if (!can('evals:run:read')) {
    return (
      <IntelPage
        icon={ClipboardCheck}
        title={t('title')}
        description={t('description')}
      >
        <SectionCard>
          <EmptyState
            title={t('forbidden.title')}
            description={t('forbidden.description')}
          />
        </SectionCard>
      </IntelPage>
    )
  }

  return (
    <IntelPage
      icon={ClipboardCheck}
      title={t('title')}
      description={t('description')}
      notices={<SelfAuditNotice />}
    >
      <Tabs defaultValue="scorecards">
        <TabsList>
          <TabsTrigger value="scorecards">{t('tabs.scorecards')}</TabsTrigger>
          <TabsTrigger value="runs">{t('tabs.runs')}</TabsTrigger>
          <TabsTrigger value="ab">{t('tabs.ab')}</TabsTrigger>
          <TabsTrigger value="drift">{t('tabs.drift')}</TabsTrigger>
          <TabsTrigger value="gate">{t('tabs.gate')}</TabsTrigger>
          <TabsTrigger value="calibration">{t('tabs.calibration')}</TabsTrigger>
        </TabsList>

        <TabsContent value="scorecards" className="flex flex-col gap-4">
          <ScorecardsTab tenant={activeTenant} />
        </TabsContent>

        <TabsContent value="runs" className="flex flex-col gap-4">
          <RunsTab tenant={activeTenant} />
        </TabsContent>

        <TabsContent value="ab" className="flex flex-col gap-4">
          <AbTab key={activeTenant ?? 'none'} tenant={activeTenant} />
        </TabsContent>

        <TabsContent value="drift" className="flex flex-col gap-4">
          <DriftTab tenant={activeTenant} />
        </TabsContent>

        <TabsContent value="gate" className="flex flex-col gap-4">
          <GateTab tenant={activeTenant} canOverride={can('evals:run:admin')} />
        </TabsContent>

        <TabsContent value="calibration" className="flex flex-col gap-4">
          <CalibrationTab tenant={activeTenant} />
          <CalibrationItemsPanel />
        </TabsContent>
      </Tabs>
    </IntelPage>
  )
}

// --- calibración del juez (C07-04) -------------------------------------------
//
// ⛔ ESTA PANTALLA MIDE CUÁNTO SE PUEDE UNO FIAR DEL JUEZ LLM, y su DTO
//    (`modules/evals/calibration.go:224-249`) es ejemplarmente honesto: distingue «no se puede
//    medir» de «salió mal» en CUATRO sitios. Fundirlos convierte una incertidumbre en un
//    veredicto, que es la peor dirección posible en la pantalla que dice si el juez vale.
//
//    1. `kappa_defined: false` — un conjunto cuyas etiquetas humanas son TODAS «pasa» (o todas
//       «falla») **no puede medir acuerdo corregido por azar**, así que **no puede certificar**
//       (`:34-36`). No es que falle: es que no se puede saber, y `meets_target` lo exige DEFINIDA.
//    2. `sensitivity_n: 0` — el propio comentario del motor lo dice: **«n=0 significa que la tasa
//       está SIN MEDIR, no que sea cero»**. Pintar `0.00` ahí afirma que el juez no acierta ni un
//       caso bueno cuando nadie lo ha medido.
//    3. `specificity_n: 0` — lo mismo por el otro lado.
//    4. `verbosity_corr_defined: false` — la correlación con la verbosidad tampoco siempre existe.
//
// ⛔ Y EL ACUERDO NO VIAJA SOLO: `agreement_ci` es el intervalo de Wilson al 95 % (`:233`), y su
//    tipo `ciDTO` (`runs.go:378-381`) es un STRUCT PLANO — sin puntero y sin `omitempty`. Es
//    decir, **llega siempre**, y cuando no se ha puntuado nada llega `{lo:0, hi:0}`. Pintarlo sin
//    mirar el denominador escribe «0 %–0 %» en pantalla, que se lee como **certeza perfecta sobre
//    cero**: exactamente lo contrario de «no se midió». La guarda es `items_scored`, no la
//    presencia del campo — un campo que siempre está no puede señalar su propia ausencia.

// --- el conjunto de referencia de la calibración (C07-04) --------------------
//
// ⛔ ESTE PANEL EXPLICA EL VEREDICTO DE ARRIBA. La pestaña de calibración ya dice «no se puede
//    certificar — kappa indefinida», y hasta ahora no ofrecía forma de ver POR QUÉ. La razón está
//    aquí: `calibration.go:34-36` — un conjunto cuyas etiquetas humanas son **todas iguales** no
//    puede medir acuerdo corregido por azar. Sin la distribución de etiquetas, ese veredicto es
//    un callejón sin salida; con ella, es una instrucción: etiquetar casos del otro signo.
//
// ⛔ Y TRES COSAS MÁS QUE EL MOTOR SEPARA:
//    · **`POST /calibration/items` es un UPSERT por (conjunto, caso)** — «a correction is an
//      audited update». Re-etiquetar SUSTITUYE la etiqueta anterior; no añade una segunda.
//    · **`human_score` es un PUNTERO** (`*float64`, `omitempty`): ausente significa **sin
//      puntuar**, que no es un cero. Un 0,00 pintado ahí afirma que un humano evaluó el caso y le
//      dio la nota mínima.
//    · **Sin juez cableado, ejecutar da 412** y el motor lo razona: «an honest 412 — **a
//      calibration cannot be simulated**». No es una avería ni una carencia de edición: es que no
//      hay a quién medir.
function CalibrationItemsPanel() {
  const { t } = useTranslation('evals')
  const { activeTenant, can } = useAuth()
  const [sinJuez, setSinJuez] = useState(false)

  const q = useQuery({
    queryKey: evalsKeys.calibrationItems(activeTenant),
    queryFn: () =>
      evalsApi.calibrationItems(undefined, { tenant: activeTenant }),
  })

  const ejecutar = useMutation({
    mutationFn: ({ tenant }: { tenant: string | null }) =>
      evalsApi.runCalibration({}, { tenant }),
    onSuccess: () => setSinJuez(false),
    onError: (e: unknown) => {
      // Se clasifica por ESTADO: el 412 es el estado deny-closed de «no hay juez», no un fallo.
      if (e instanceof ApiError && e.status === 412) setSinJuez(true)
      else toast.error(String((e as Error).message ?? e))
    },
  })

  return (
    <SectionCard
      title={t('calibItems.title')}
      description={t('calibItems.description')}
      actions={
        can('evals:run:write') ? (
          <Button
            size="sm"
            variant="outline"
            disabled={ejecutar.isPending}
            onClick={() => ejecutar.mutate({ tenant: activeTenant })}
          >
            {t('calibItems.run')}
          </Button>
        ) : null
      }
    >
      {/* ⛔ El 412 dicho como lo que es. */}
      {sinJuez ? (
        <div
          role="note"
          className="mb-3 rounded-md border border-border p-2 text-xs"
        >
          {t('calibItems.noJudge')}
        </div>
      ) : null}

      <ListTruncationBadge
        query={q}
        label={t('truncation.label', {
          n: q.data?.items?.length,
        })}
        hint={t('truncation.hint')}
        className="px-0 pt-0 pb-3"
      />

      <AsyncSection query={q} skeletonHeight={160}>
        {(res) => {
          const items = ((res as { items?: unknown[] })?.items ?? []) as Array<{
            id?: string
            set_name?: string
            case_key: string
            human_passed: boolean
            human_score?: number
          }>
          if (items.length === 0)
            return <EmptyState title={t('calibItems.empty')} />

          // ⛔ LA DISTRIBUCIÓN ES LA EXPLICACIÓN, y por eso va antes de la lista.
          const pasan = items.filter((i) => i.human_passed).length
          const fallan = items.length - pasan
          const degenerado = pasan === 0 || fallan === 0

          return (
            <div className="flex flex-col gap-2">
              <div className="flex flex-wrap items-center gap-2 text-xs">
                <Badge variant="neutral">
                  {t('calibItems.distribution', { pass: pasan, fail: fallan })}
                </Badge>
                {degenerado ? (
                  <Badge variant="warning">{t('calibItems.degenerate')}</Badge>
                ) : null}
              </div>

              <div className="flex flex-col gap-1">
                {items.map((i) => (
                  <div
                    key={`${i.set_name ?? ''}:${i.case_key}`}
                    className="flex flex-wrap items-center justify-between gap-3 rounded-md border border-border px-3 py-2 text-xs"
                  >
                    <div className="flex min-w-0 items-center gap-2">
                      <Badge variant={i.human_passed ? 'success' : 'danger'}>
                        {i.human_passed
                          ? t('calibItems.humanPass')
                          : t('calibItems.humanFail')}
                      </Badge>
                      <span className="font-mono">{i.case_key}</span>
                    </div>
                    {/* Puntero: ausente = SIN PUNTUAR, no cero. */}
                    <span className="text-muted-foreground">
                      {i.human_score === undefined || i.human_score === null
                        ? t('calibItems.unscored')
                        : i.human_score.toFixed(2)}
                    </span>
                  </div>
                ))}
              </div>
            </div>
          )
        }}
      </AsyncSection>
    </SectionCard>
  )
}

function CalibrationTab({ tenant }: { tenant: string | null }) {
  const { t } = useTranslation('evals')

  const q = useQuery({
    queryKey: ['evals', tenant, 'calibration', 'reports'] as const,
    queryFn: () => evalsApi.calibrationReports(),
  })

  return (
    <SectionCard
      title={t('calibration.title')}
      description={t('calibration.description')}
      noPadding
    >
      <div className="flex flex-col gap-2 p-4">
        <ListTruncationBadge
          query={q}
          label={t('truncation.label', {
            n: q.data?.items?.length,
          })}
          hint={t('truncation.hint')}
          className="px-0 pt-0"
        />
        <AsyncSection query={q} skeletonHeight={200}>
          {(list) => {
            const informes = ((list as { items?: unknown[] })?.items ??
              []) as Array<{
              id: string
              set_name: string
              judge_model?: string
              status: string
              items_total: number
              items_scored: number
              items_error: number
              agreement: number
              agreement_ci: { lo: number; hi: number }
              verbosity_corr: number
              verbosity_corr_defined: boolean
              kappa: number
              kappa_defined: boolean
              sensitivity: number
              sensitivity_n: number
              specificity: number
              specificity_n: number
              target: number
              kappa_floor: number
              meets_target: boolean
            }>
            return informes.length === 0 ? (
              <EmptyState title={t('calibration.empty')} />
            ) : (
              informes.map((r) => (
                <div
                  key={r.id}
                  className="flex flex-col gap-2 rounded-md border border-border p-3"
                >
                  <div className="flex flex-wrap items-center gap-2">
                    {/* Tres veredictos, no dos: sin kappa definida NO se certifica, y eso no es
                        un suspenso — es que no se puede medir. */}
                    {!r.kappa_defined ? (
                      <Badge variant="neutral">
                        {t('calibration.cannotCertify')}
                      </Badge>
                    ) : (
                      <Badge variant={r.meets_target ? 'success' : 'danger'}>
                        {r.meets_target
                          ? t('calibration.meets')
                          : t('calibration.below')}
                      </Badge>
                    )}
                    <span className="font-mono text-sm">{r.set_name}</span>
                    {r.judge_model ? (
                      <Badge variant="outline">{r.judge_model}</Badge>
                    ) : null}
                    {r.status === 'degraded' ? (
                      <Badge variant="warning">
                        {t('calibration.degradedWithErrors', {
                          n: r.items_error,
                        })}
                      </Badge>
                    ) : null}
                  </div>
                  <dl className="grid grid-cols-2 gap-x-6 gap-y-1 text-xs sm:grid-cols-4">
                    <div>
                      <dt className="text-muted-foreground">
                        {t('calibration.agreement')}
                      </dt>
                      {/* El intervalo LLEGA SIEMPRE (struct plano): sin denominador es {0,0}, y
                          «0 %–0 %» se lee como certeza, no como ausencia. Manda items_scored. */}
                      <dd>
                        {r.items_scored === 0 ? (
                          '—'
                        ) : (
                          <>
                            {formatPct(r.agreement)}{' '}
                            <span className="text-muted-foreground">
                              {t('calibration.ciOverN', {
                                lo: formatPct(r.agreement_ci.lo),
                                hi: formatPct(r.agreement_ci.hi),
                                n: r.items_scored,
                              })}
                            </span>
                          </>
                        )}
                      </dd>
                    </div>
                    <div>
                      <dt className="text-muted-foreground">
                        {t('calibration.kappa')}
                      </dt>
                      {/* Sin definir NO es cero. */}
                      <dd>{r.kappa_defined ? r.kappa.toFixed(2) : '—'}</dd>
                    </div>
                    <div>
                      <dt className="text-muted-foreground">
                        {t('calibration.sensitivity')}
                      </dt>
                      {/* n=0 = SIN MEDIR, dicho por el motor. */}
                      <dd>
                        {r.sensitivity_n === 0 ? '—' : formatPct(r.sensitivity)}
                      </dd>
                    </div>
                    <div>
                      <dt className="text-muted-foreground">
                        {t('calibration.specificity')}
                      </dt>
                      <dd>
                        {r.specificity_n === 0 ? '—' : formatPct(r.specificity)}
                      </dd>
                    </div>
                    {/* El CUARTO caso que el comentario de arriba enumera y la pantalla no
                        pintaba: un juez cuyas notas no varían no tiene correlación con la
                        verbosidad que medir. Sin `_defined` sería un 0,00 que se lee como
                        «no la hay» — que es justo la conclusión tranquilizadora falsa. */}
                    <div>
                      <dt className="text-muted-foreground">
                        {t('calibration.verbosityCorr')}
                      </dt>
                      <dd>
                        {r.verbosity_corr_defined
                          ? r.verbosity_corr.toFixed(2)
                          : '—'}
                      </dd>
                    </div>
                  </dl>
                </div>
              ))
            )
          }}
        </AsyncSection>
      </div>
    </SectionCard>
  )
}

function formatPct(v: number) {
  return `${Math.round(v * 100)}%`
}

// --- CI regression gate (C07-04) ---------------------------------------------
//
// ⛔ POR QUÉ ESTA PESTAÑA EXISTE. `modules/evals/evals.go:218-221` registra cuatro rutas de gate y
//    la consola no llamaba ninguna. La consecuencia no era cosmética: **desbloquear una release
//    parada por el gate de calidad sólo se podía hacer con `curl`**, y la anulación es la
//    superficie de decisión gobernada del módulo — la que exige motivo escrito y queda en el
//    ledger.
//
// ⛔ LOS DOS VEREDICTOS NO SE FUNDEN, y es lo único que de verdad importa de esta pantalla.
//    `verdict` es lo que el gate MIDIÓ; `effective_verdict` es lo que CI OBEDECE — el mismo, o
//    `pass` tras una anulación (`gate.go:70-72`). Enseñar sólo el efectivo convierte una release
//    desbloqueada por una persona en una que pasó sola; enseñar sólo el medido esconde que CI
//    recibió luz verde. Cuando difieren, la fila dice quién y por qué.
function GateTab({
  tenant,
  canOverride,
}: {
  tenant: string | null
  canOverride: boolean
}) {
  const { t } = useTranslation('evals')
  const [overriding, setOverriding] = useState<GateEvaluation | null>(null)

  const gatesQ = useQuery({
    queryKey: evalsKeys.gates(tenant),
    queryFn: () => evalsApi.gates(),
  })

  return (
    <>
      <SectionCard
        title={t('gate.title')}
        description={t('gate.description')}
        noPadding
      >
        <div className="flex flex-col gap-2 p-4">
          <ListTruncationBadge
            query={gatesQ}
            label={t('truncation.label', {
              n: gatesQ.data?.items?.length,
            })}
            hint={t('truncation.hint')}
            className="px-0 pt-0"
          />
          <AsyncSection query={gatesQ} skeletonHeight={240}>
            {(list) =>
              (list.items ?? []).length === 0 ? (
                <EmptyState title={t('gate.empty')} />
              ) : (
                (list.items ?? []).map((g) => (
                  <GateRow
                    key={g.id}
                    gate={g}
                    canOverride={canOverride}
                    onOverride={() => setOverriding(g)}
                  />
                ))
              )
            }
          </AsyncSection>
        </div>
      </SectionCard>
      {canOverride && overriding ? (
        <OverrideGateDialog
          tenant={tenant}
          gate={overriding}
          onOpenChange={(open) => {
            if (!open) setOverriding(null)
          }}
        />
      ) : null}
    </>
  )
}

function GateRow({
  gate,
  canOverride,
  onOverride,
}: {
  gate: GateEvaluation
  canOverride: boolean
  onOverride: () => void
}) {
  const { t } = useTranslation('evals')
  const tono = (v: string) =>
    v === 'pass' ? 'success' : v === 'warn' ? 'warning' : 'danger'
  // El motor rechaza con 409 anular un gate que ya pasa («nothing to override») y uno ya
  // anulado (`gate.go:610-613`). La consola no ofrece el botón en esos dos casos: el 403/409
  // seguiría llegando del motor, que es la autoridad, pero después de haberlo enseñado.
  const anulable = canOverride && !gate.overridden && gate.verdict !== 'pass'

  return (
    <div className="flex flex-wrap items-start justify-between gap-3 rounded-md border border-border p-3">
      <div className="flex min-w-0 flex-col gap-1">
        <div className="flex flex-wrap items-center gap-2">
          <Badge variant={tono(gate.verdict)}>
            {t('gate.measured', { verdict: gate.verdict })}
          </Badge>
          {/* La flecha sólo aparece cuando los dos veredictos DIFIEREN: si son iguales,
              repetirlo sugeriría que hubo una decisión donde no la hubo. */}
          {gate.effective_verdict !== gate.verdict ? (
            <>
              <span aria-hidden className="text-muted-foreground">
                →
              </span>
              <Badge variant={tono(gate.effective_verdict)}>
                {t('gate.effective', { verdict: gate.effective_verdict })}
              </Badge>
            </>
          ) : null}
          <span className="font-mono text-sm">{gate.suite_ref}</span>
          {gate.subject_ref ? (
            <Badge variant="outline">{gate.subject_ref}</Badge>
          ) : null}
        </div>
        <p className="text-xs text-muted-foreground">
          {t('gate.sampled', {
            sampled: gate.sampled,
            total: gate.total_cases,
          })}
          {gate.judge_model ? ` · ${gate.judge_model}` : ''}
        </p>
        {/* ⛔ LAS DOS TASAS, JUNTAS Y NUNCA UNA EN LUGAR DE LA OTRA — con esas palabras del motor
            (`gate.go:89-92`). La corregida es el estimador de Rogan–Gladen: la tasa que midió el
            juez, ajustada por la sensibilidad y especificidad REALES de ese juez. Enseñar sólo la
            cruda, con un juez cuya sensibilidad no es 1, es afirmar una precisión que nadie
            comprobó — en la pantalla que decide si CI bloquea un merge.
            Y su AUSENCIA no significa «coincide con la cruda»: significa que no hubo calibración
            de confianza con la que corregir, y eso se dice. */}
        {gate.corrected_pass_rate ? (
          <p className="text-xs">
            {t('gate.correctedRate', {
              rate: formatFraction(gate.corrected_pass_rate.pass_rate),
              sens: formatFraction(gate.corrected_pass_rate.sensitivity),
              spec: formatFraction(gate.corrected_pass_rate.specificity),
            })}
          </p>
        ) : (
          <p className="text-xs text-muted-foreground">
            {t('gate.noCorrection')}
          </p>
        )}
        {gate.overridden ? (
          <p className="text-xs text-foreground">
            {t('gate.overriddenBy', { actor: gate.override_by ?? '—' })}
            {gate.override_reason ? ` — ${gate.override_reason}` : ''}
          </p>
        ) : null}
        {(gate.reasons ?? []).length > 0 ? (
          <ul className="list-disc pl-4 text-xs text-muted-foreground">
            {(gate.reasons ?? []).slice(0, 4).map((r: string) => (
              <li key={r}>{r}</li>
            ))}
          </ul>
        ) : null}
      </div>
      {anulable ? (
        <Button variant="ghost" size="sm" onClick={onOverride}>
          {t('gate.override')}
        </Button>
      ) : null}
    </div>
  )
}

/** La anulación EXIGE motivo escrito: el motor contesta 400 sin él
 *  (`modules/evals/gate.go:588`). La consola lo exige también, no para adelantarse a la
 *  autoridad sino para no mandar una petición que ya se sabe que va a fallar. */
function OverrideGateDialog({
  tenant,
  gate,
  onOpenChange,
}: {
  tenant: string | null
  gate: GateEvaluation
  onOpenChange: (open: boolean) => void
}) {
  const { t } = useTranslation('evals')
  const qc = useQueryClient()
  const [reason, setReason] = useState('')

  const mut = useMutation({
    mutationFn: () => evalsApi.overrideGate(gate.id, reason.trim()),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: evalsKeys.gates(tenant) })
      toast.success(t('gate.overrideDone'))
      onOpenChange(false)
    },
    onError: (e: unknown) => toast.error(String((e as Error).message ?? e)),
  })

  return (
    <Dialog open onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t('gate.overrideTitle')}</DialogTitle>
          <DialogDescription>
            {t('gate.overrideDescription', { verdict: gate.verdict })}
          </DialogDescription>
        </DialogHeader>
        <Field label={t('gate.reason')} description={t('gate.reasonHint')}>
          {({ id }) => (
            <Input
              id={id}
              value={reason}
              onChange={(e) => setReason(e.target.value)}
            />
          )}
        </Field>
        <DialogFooter>
          <Button variant="ghost" onClick={() => onOpenChange(false)}>
            {t('gate.cancel')}
          </Button>
          <Button
            variant="primary"
            disabled={reason.trim() === '' || mut.isPending}
            onClick={() => mut.mutate()}
          >
            {t('gate.confirmOverride')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

// --- scorecards tab ----------------------------------------------------------

function ScorecardsTab({ tenant }: { tenant: string | null }) {
  const { t } = useTranslation('evals')
  const [groupBy, setGroupBy] = useState<ScorecardGroupBy>('agent')

  const scorecardsQ = useQuery({
    queryKey: evalsKeys.scorecards(tenant, groupBy),
    queryFn: () => evalsApi.scorecards(groupBy),
  })

  return (
    <SectionCard
      title={t('scorecards.title')}
      description={t('scorecards.description')}
      actions={
        <Select
          value={groupBy}
          onValueChange={(v) => setGroupBy(v as ScorecardGroupBy)}
        >
          <SelectTrigger className="w-44" aria-label={t('scorecards.groupBy')}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {GROUP_BY.map((g) => (
              <SelectItem key={g} value={g}>
                {t(`scorecards.by.${g}`)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      }
    >
      <AsyncSection query={scorecardsQ} skeletonHeight={220}>
        {(list) =>
          list.items.length === 0 ? (
            <EmptyState title={t('scorecards.empty')} />
          ) : (
            <ScorecardGrid scorecards={list.items} />
          )
        }
      </AsyncSection>
    </SectionCard>
  )
}

// --- runs tab ----------------------------------------------------------------

function RunsTab({ tenant }: { tenant: string | null }) {
  const { t } = useTranslation('evals')
  const [openRun, setOpenRun] = useState<EvalRun | null>(null)

  const runsQ = useQuery({
    queryKey: evalsKeys.runs(tenant),
    queryFn: () => evalsApi.runs(),
  })

  return (
    <>
      <SectionCard
        title={t('runs.title')}
        description={t('runs.description')}
        noPadding
      >
        <div className="p-4">
          <ListTruncationBadge
            query={runsQ}
            label={t('truncation.label', {
              n: runsQ.data?.items?.length,
            })}
            hint={t('truncation.hint')}
            className="px-0 pt-0 pb-3"
          />
          <AsyncSection query={runsQ} skeletonHeight={240}>
            {(list) =>
              list.items.length === 0 ? (
                <EmptyState title={t('runs.empty')} />
              ) : (
                <RunsTable
                  runs={list.items}
                  onRowClick={(r) => setOpenRun(r)}
                />
              )
            }
          </AsyncSection>
        </div>
      </SectionCard>
      <RunDetailDialog
        tenant={tenant}
        run={openRun}
        onOpenChange={(open) => {
          if (!open) setOpenRun(null)
        }}
      />
    </>
  )
}

/** A privileged, self-audited read of one run's per-case results. The candidate
 *  output is never here — only outcome/score/label and the hash fingerprint. */

// --- fijar la línea base (C07-04) --------------------------------------------
//
// ⛔ ESTA ES «LA SUPERFICIE DE DECISIÓN», con esas palabras del motor
//    (`modules/evals/evals.go:201-202`), y es admin-tier y auditada porque `schema.go:289` lo
//    razona: «a baseline pin is a decision; re-pinning is auditable».
//
// ⛔ Y LO QUE HAY QUE DECIR ANTES DE PULSAR, que es lo que una etiqueta «fijar» esconde:
//
//    1. **Hay UNA sola línea base por (suite, sujeto) y es MUTABLE** (`schema.go:107,297`).
//       Fijar no añade: **SUSTITUYE**. El motor guarda la anterior en su autoauditoría
//       (`baselines.go:41-42`), no en un endpoint legible.
//    2. ⛔ **Por eso la pantalla NO puede enseñar qué va a reemplazar**: `POST /baselines` es la
//       única ruta que existe — no hay GET. Se dice, en vez de fingir que se sabe.
//    3. ⛔ **Y fijar una ejecución PEOR baja el listón.** Toda regresión futura se mide contra
//       esto (`runs.go:257-276`), así que una línea base más floja es exactamente cómo una puerta
//       de calidad **deja de disparar sin que nadie la desactive**. Por eso la nota lleva la
//       puntuación de ESTA ejecución delante: la decisión se toma con la cifra a la vista.
function PinBaselineAction({ run }: { run: EvalRun }) {
  const { t } = useTranslation('evals')
  const { can } = useAuth()
  const [abierto, setAbierto] = useState(false)

  const fijar = useMutation({
    mutationFn: () =>
      evalsApi.pinBaseline({
        suite_ref: run.suite_ref,
        subject_ref: run.subject_ref,
        run_ref: run.id,
      }),
    onSuccess: () => {
      setAbierto(false)
      toast.success(t('baseline.pinned'))
    },
    onError: (e: unknown) => toast.error(String((e as Error).message ?? e)),
  })

  // El permiso de la DECISIÓN, no el de lanzar ejecuciones.
  if (!can('evals:run:admin')) return null

  return (
    <>
      <Button
        size="sm"
        variant="outline"
        className="self-start"
        onClick={() => setAbierto(true)}
      >
        {t('baseline.pin')}
      </Button>
      <Dialog open={abierto} onOpenChange={setAbierto}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t('baseline.pin')}</DialogTitle>
            <DialogDescription>
              {t('baseline.replaceWarning', {
                suite: run.suite_ref,
                subject: run.subject_ref,
              })}
            </DialogDescription>
          </DialogHeader>
          {/* La cifra de ESTA ejecución, delante de la decisión. */}
          <p className="text-sm">
            {t('baseline.thisRunScores', {
              score: formatFraction(run.pass_rate),
              n: run.n_scored ?? 0,
            })}
          </p>
          <p className="text-xs text-muted-foreground">
            {t('baseline.cannotShowCurrent')}
          </p>
          <DialogFooter>
            <Button disabled={fijar.isPending} onClick={() => fijar.mutate()}>
              {t('baseline.confirm')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  )
}

function RunDetailDialog({
  tenant,
  run,
  onOpenChange,
}: {
  tenant: string | null
  run: EvalRun | null
  onOpenChange: (open: boolean) => void
}) {
  const { t } = useTranslation('evals')

  const resultsQ = useQuery({
    queryKey: evalsKeys.runResults(tenant, run?.id ?? ''),
    queryFn: () => evalsApi.runResults(run!.id),
    enabled: !!run,
  })

  return (
    <Dialog open={!!run} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-3xl">
        <DialogHeader>
          <DialogTitle>
            {run
              ? t('cases.title', { subject: run.subject_ref })
              : t('cases.titleEmpty')}
          </DialogTitle>
          <DialogDescription>{t('cases.description')}</DialogDescription>
        </DialogHeader>
        {run ? (
          <div className="flex flex-col gap-4">
            <SelfAuditNotice />
            <CaseOutcomeBar run={run} />
            <CaveatNotice>{t('cases.noPayload')}</CaveatNotice>
            <AsyncSection query={resultsQ} skeletonHeight={200}>
              {(list) =>
                list.items.length === 0 ? (
                  <EmptyState title={t('cases.empty')} />
                ) : (
                  <CaseResultsTable results={list.items} />
                )
              }
            </AsyncSection>
            <PinBaselineAction run={run} />
          </div>
        ) : null}
      </DialogContent>
    </Dialog>
  )
}

// --- A/B tab -----------------------------------------------------------------

/** The engine scores case_key → output. Anything else is refused BEFORE the request:
 *  an empty map is the dangerous one — the engine accepts it, scores nothing, and
 *  persists two `degraded` runs whose 0-vs-0 reads as a real tie (measured against
 *  the live handler, modules/evals/ab_console_contract_test.go). */
type OutputsParse =
  | { ok: true; value: Record<string, string> }
  | { ok: false; reason: 'empty' | 'invalid' | 'tooLong' }

// The engine's own bounds (modules/evals/helpers.go:29-35). It does NOT refuse an
// over-long value: `clamp` TRUNCATES a key at 200 runes and an output at 8192 and
// appends "…" (helpers.go:193-199, runs.go), then scores the truncated string. So
// a silent pass here would produce a score for text the operator never submitted
// — an evaluation result that is quietly about something else. Refusing up front,
// and saying so, is the honest half.
const MAX_CASE_KEY_RUNES = 200
const MAX_OUTPUT_RUNES = 8192
const runeLength = (s: string) => [...s].length

function parseOutputs(raw: string): OutputsParse {
  const text = raw.trim()
  if (text === '') return { ok: false, reason: 'empty' }
  let parsed: unknown
  try {
    parsed = JSON.parse(text)
  } catch {
    return { ok: false, reason: 'invalid' }
  }
  if (
    parsed === null ||
    typeof parsed !== 'object' ||
    Array.isArray(parsed) ||
    Object.values(parsed as Record<string, unknown>).some(
      (v) => typeof v !== 'string',
    )
  ) {
    return { ok: false, reason: 'invalid' }
  }
  const value = parsed as Record<string, string>
  if (Object.keys(value).length === 0) return { ok: false, reason: 'empty' }
  if (
    Object.entries(value).some(
      ([k, v]) =>
        runeLength(k) > MAX_CASE_KEY_RUNES || runeLength(v) > MAX_OUTPUT_RUNES,
    )
  ) {
    return { ok: false, reason: 'tooLong' }
  }
  return { ok: true, value }
}

/** Maps a refusal to its message key — exhaustive, so a new reason cannot fall
 *  through to a generic string. */
function outputsErrorKey(reason: 'empty' | 'invalid' | 'tooLong'): string {
  switch (reason) {
    case 'empty':
      return 'ab.outputsEmpty'
    case 'tooLong':
      return 'ab.outputsTooLong'
    default:
      return 'ab.outputsInvalid'
  }
}

/**
 * The A/B tab. It asks for what POST /ab actually scores: ONE suite and TWO labelled
 * output sets.
 *
 * It used to offer two RUN pickers and send `outputs: {}` for both. That could never
 * work twice over — the body did not decode at all (RunInput inside `a`), and an
 * EVALS RUN does not keep its outputs: the run and its case results persist only a
 * hash and a clamped label (modules/evals/runs.go), and the read endpoints rebuild
 * only those. So no picker over `evalsApi.runs()` can recover what /ab scores.
 * (Narrowly: other modules DO return raw outputs — sandbox steps, evals
 * calibration — so a future picker could source them there, but it would first
 * have to establish a step_key → case_key mapping that does not exist today.)
 */
function AbTab({ tenant }: { tenant: string | null }) {
  const { t } = useTranslation('evals')
  const { can } = useAuth()
  // POST /ab is a WRITE (modules/evals: the route requires evals:run:write), while the
  // view is entered on evals:run:read. A viewer holds module reads and not writes, so
  // without this the comparison button was offered to a principal the server refuses.
  const canRunComparison = can('evals:run:write')
  const [suiteRef, setSuiteRef] = useState<string | null>(null)
  const [labelA, setLabelA] = useState('')
  const [labelB, setLabelB] = useState('')
  const [outputsA, setOutputsA] = useState('')
  const [outputsB, setOutputsB] = useState('')
  const [pairwise, setPairwise] = useState(false)

  const suitesQ = useQuery({
    queryKey: evalsKeys.suites(tenant),
    queryFn: () => evalsApi.suites(),
  })
  const comparison = useMutation({
    mutationFn: (body: AbRequest) => evalsApi.ab(body),
  })

  const parsedA = parseOutputs(outputsA)
  const parsedB = parseOutputs(outputsB)
  const canCompare = canRunComparison && !!suiteRef && parsedA.ok && parsedB.ok

  const outputsField = (
    label: string,
    value: string,
    onChange: (v: string) => void,
    parsed: OutputsParse,
  ) => (
    <Field
      label={label}
      required
      description={t('ab.outputsHint')}
      // Only once the operator has typed something: an untouched field is
      // incomplete, not wrong.
      error={
        value.trim() !== '' && !parsed.ok
          ? t(outputsErrorKey(parsed.reason), {
              maxKey: MAX_CASE_KEY_RUNES,
              maxOutput: MAX_OUTPUT_RUNES,
            })
          : undefined
      }
    >
      {({ id }) => (
        <Textarea
          id={id}
          rows={5}
          className="font-mono text-xs"
          placeholder={t('ab.outputsPlaceholder')}
          value={value}
          onChange={(e) => onChange(e.target.value)}
        />
      )}
    </Field>
  )

  return (
    <>
      <CaveatNotice>
        <span className="inline-flex flex-wrap items-center gap-2">
          {t('ab.seamNote')}
          <SeamBadge label={t('ab.seamLabel')} />
        </span>
      </CaveatNotice>
      <SectionCard
        title={t('ab.pickerTitle')}
        description={t('ab.pickerDescription')}
      >
        <ListTruncationBadge
          query={suitesQ}
          label={t('truncation.label', {
            n: suitesQ.data?.items?.length,
          })}
          hint={t('truncation.hint')}
          className="px-0 pt-0 pb-3"
        />
        <AsyncSection query={suitesQ} skeletonHeight={120}>
          {(list) => {
            if (list.items.length === 0) {
              return <EmptyState title={t('ab.noSuites')} />
            }
            return (
              <div className="flex flex-col gap-4">
                <Field label={t('ab.suiteLabel')} required>
                  {({ id }) => (
                    <Select
                      value={suiteRef ?? undefined}
                      onValueChange={setSuiteRef}
                    >
                      <SelectTrigger id={id}>
                        <SelectValue placeholder={t('ab.selectSuite')} />
                      </SelectTrigger>
                      <SelectContent>
                        {list.items.map((suite) => (
                          <SelectItem key={suite.id} value={suite.id}>
                            {suite.name}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                  )}
                </Field>
                <div className="grid gap-3 sm:grid-cols-2">
                  <Field label={t('ab.labelA')}>
                    {({ id }) => (
                      <Input
                        id={id}
                        value={labelA}
                        placeholder="A"
                        onChange={(e) => setLabelA(e.target.value)}
                      />
                    )}
                  </Field>
                  <Field label={t('ab.labelB')}>
                    {({ id }) => (
                      <Input
                        id={id}
                        value={labelB}
                        placeholder="B"
                        onChange={(e) => setLabelB(e.target.value)}
                      />
                    )}
                  </Field>
                </div>
                <div className="grid gap-3 sm:grid-cols-2">
                  {outputsField(
                    t('ab.outputsA'),
                    outputsA,
                    setOutputsA,
                    parsedA,
                  )}
                  {outputsField(
                    t('ab.outputsB'),
                    outputsB,
                    setOutputsB,
                    parsedB,
                  )}
                </div>
                <label className="flex items-start gap-2 text-sm">
                  <Checkbox
                    checked={pairwise}
                    onCheckedChange={(v) => setPairwise(v === true)}
                  />
                  <span className="flex flex-col gap-0.5">
                    <span>{t('ab.pairwiseLabel')}</span>
                    <span className="text-xs text-muted-foreground">
                      {t('ab.pairwiseHint')}
                    </span>
                  </span>
                </label>
                <SelfAuditNotice />
                <div>
                  <Button
                    variant="primary"
                    disabled={!canCompare || comparison.isPending}
                    onClick={() => {
                      if (!suiteRef || !parsedA.ok || !parsedB.ok) return
                      comparison.mutate({
                        suite_ref: suiteRef,
                        a: { label: labelA.trim(), outputs: parsedA.value },
                        b: { label: labelB.trim(), outputs: parsedB.value },
                        ...(pairwise ? { pairwise: true } : {}),
                      })
                    }}
                  >
                    {comparison.isPending ? (
                      <Spinner size="sm" aria-hidden />
                    ) : null}
                    {t('ab.runComparison')}
                  </Button>
                </div>
                {comparison.isError ? (
                  <p role="alert" className="text-sm text-danger">
                    {t('ab.runError')}
                  </p>
                ) : null}
              </div>
            )
          }}
        </AsyncSection>
      </SectionCard>
      {comparison.data ? (
        <AbComparison
          variants={comparison.data.variants}
          winner={comparison.data.winner}
          delta={comparison.data.delta}
          tie={comparison.data.tie}
          pairwise={comparison.data.pairwise}
        />
      ) : (
        <SectionCard>
          <EmptyState
            title={t('ab.empty')}
            description={t('ab.emptyDescription')}
          />
        </SectionCard>
      )}
    </>
  )
}

// --- drift tab ---------------------------------------------------------------

function DriftTab({ tenant }: { tenant: string | null }) {
  const { t } = useTranslation('evals')
  // Drift is always read per agent. This was a useState with no setter, which
  // reads as "a control is coming" and pays a re-render budget for a value that
  // never changes; a constant says what is actually true.
  const groupBy: ScorecardGroupBy = 'agent'

  const scorecardsQ = useQuery({
    queryKey: evalsKeys.scorecards(tenant, groupBy),
    queryFn: () => evalsApi.scorecards(groupBy),
  })
  const suitesQ = useQuery({
    queryKey: evalsKeys.suites(tenant),
    queryFn: () => evalsApi.suites(),
  })

  const [selectedKey, setSelectedKey] = useState<string | null>(null)

  const suites = suitesQ.data?.items ?? []
  const thresholdFor = useMemo(
    () =>
      (sc: Scorecard): number | undefined => {
        const suite: Suite | undefined =
          suites.find(
            (s) =>
              s.subject_kind === sc.subject_kind && s.name.includes(sc.key),
          ) ?? suites[0]
        return suite?.pass_threshold
      },
    [suites],
  )

  return (
    <>
      <ListTruncationBadge
        query={suitesQ}
        label={t('truncation.label', {
          n: suitesQ.data?.items?.length,
        })}
        hint={t('truncation.hint')}
        className="px-0 pt-0"
      />
      <AsyncSection query={scorecardsQ} skeletonHeight={300}>
        {(list) => {
          if (list.items.length === 0) {
            return (
              <SectionCard>
                <EmptyState title={t('drift.empty')} />
              </SectionCard>
            )
          }
          // Default to the regressed subject if there is one, else the first card.
          const active =
            list.items.find((sc) => sc.key === selectedKey) ??
            list.items.find((sc) => sc.regressed) ??
            list.items[0]
          return (
            <>
              <SectionCard
                title={t('drift.pickerTitle')}
                actions={
                  <Select
                    value={active.key}
                    onValueChange={(v) => setSelectedKey(v)}
                  >
                    <SelectTrigger
                      className="w-56"
                      aria-label={t('drift.pickerTitle')}
                    >
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {list.items.map((sc) => (
                        <SelectItem
                          key={`${sc.subject_kind}:${sc.key}`}
                          value={sc.key}
                        >
                          {sc.key}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                }
              >
                <CaveatNotice>{t('drift.note')}</CaveatNotice>
              </SectionCard>
              <DriftChart scorecard={active} threshold={thresholdFor(active)} />
            </>
          )
        }}
      </AsyncSection>
    </>
  )
}
