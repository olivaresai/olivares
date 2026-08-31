// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useCallback, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Check, KeyRound, RefreshCcw, ShieldAlert } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Field } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import {
  applyWork,
  classifyApplyFailure,
  foldFields,
  isUnknownVerdict,
  planWork,
  requiredFieldsFor,
  workErrorCode,
  workErrorReason,
  type CommandField,
  type ApplyFailure,
  type ApplyOutcome,
  type WorkIntent,
} from './api'
import type { Plan } from './types'
import { ChecksList, UnavailableNotice, VerdictBadge } from './verdict'

/**
 * THE APPLY FLOW — plan first, then apply, with ONE key for the whole intention.
 *
 * This component is where four of the kernel's traps are either handled or shipped as
 * defects, so each is named against the behaviour that answers it:
 *
 * 1. MODE IS THE UI, NOT TRANSPORT. `mode=validate|plan|apply` is mandatory, and
 *    validate/plan write ZERO rows. A console that posts straight to apply throws away
 *    the kernel's best property — the operator's chance to see the row effects and the
 *    canonical plan hash BEFORE anything is written. So this dialog cannot apply until
 *    a plan has been shown: `plan` is the only entry point and the apply button does
 *    not exist before it returns.
 *
 * 2. ONE KEY PER INTENTION, REUSED ON RETRY. The key belongs to the WorkIntent and is
 *    generated when the intent is built, never here. The retry button re-sends the SAME
 *    intent. There is no code path in this file that constructs an intent, which is what
 *    makes "regenerate on retry" unwritable rather than merely discouraged.
 *
 * 3. THE KEY IS SHOWN BEFORE IT IS TRANSMITTED. The canonical CLI prints it
 *    (cmd/olivares/cmd_work.go:305) so an operator can resolve an ambiguous timeout by
 *    hand; the console shows it in the plan step, before the apply button is pressed.
 *
 * 4. REPLAY IS "ALREADY APPLIED", NOT A NEW SUCCESS. The replayed body is byte-identical
 *    to the original, so only the Idempotency-Replayed header distinguishes them. The
 *    result panel branches on it and says so in words.
 *
 * 5. A 409/412 IS NEVER RESOLVED BY RE-SENDING. On a version conflict this offers
 *    RE-READ, not retry: silently re-applying with the fresh ETag would overwrite the
 *    other writer's change while looking like a success.
 */

export interface ApplyFlowProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** Built ONCE by the caller, per operator intention. Its key is the whole point. */
  intent: WorkIntent | null
  title: string
  description?: string
  /** Called after a successful (or replayed) apply, so the caller can refetch. */
  onApplied?: (outcome: ApplyOutcome) => void
  /** Called when the operator asks to re-read after a version conflict. */
  onReread?: () => void
  /** The acceptance verdict being recorded, when the command is acceptance.evaluate:
   * it decides WHICH evidence the engine will demand (work_state.go:196-201). */
  acceptanceState?: 'passed' | 'failed' | 'waived'
}

type Phase =
  'input' | 'planning' | 'planned' | 'applying' | 'applied' | 'failed'

/** The intent the wire actually sees: the caller's intent with the operator's values
 * folded in at the level each field belongs to. The KEY is untouched — filling in a
 * reason completes an intention, it does not start a second one. */
function effectiveIntent(
  intent: WorkIntent,
  fields: CommandField[],
  values: Record<string, string>,
): WorkIntent {
  if (fields.length === 0) return intent
  return { ...intent, body: foldFields(intent.body, fields, values) }
}

/**
 * Lo que la intención ya SABE, en la forma que el formulario consume.
 *
 * ⛔ EXTRAÍDA PORQUE HABÍA DOS EFECTOS PELEÁNDOSE POR `values`. Uno sembraba desde la intención y
 *    otro, al abrir el diálogo con campos requeridos, hacía `setValues({})` — **borraba lo
 *    sembrado**. El resultado era el que el sembrado existe para evitar: el operador tenía que
 *    transcribir a mano un dato que la pantalla de al lado ya exhibe, y cada errata se paga con
 *    un `invalid_command` del motor. Con una sola fuente, los dos caminos siembran igual.
 */
export function sembrarDesde(intent: WorkIntent | null): Record<string, string> {
  const body = (intent?.body ?? {}) as Record<string, unknown>
  const out: Record<string, string> = {}
  for (const [key, value] of Object.entries(body)) {
    if (typeof value === 'string' || typeof value === 'number') {
      out[key] = String(value)
    }
  }
  return out
}

export function ApplyFlow({
  open,
  onOpenChange,
  intent,
  title,
  description,
  onApplied,
  onReread,
  acceptanceState,
}: ApplyFlowProps) {
  const { t } = useTranslation('work')
  const [phase, setPhase] = useState<Phase>('planning')
  const [plan, setPlan] = useState<Plan | null>(null)
  const [outcome, setOutcome] = useState<ApplyOutcome | null>(null)
  const [failure, setFailure] = useState<ApplyFailure | null>(null)
  const [failureCode, setFailureCode] = useState<string | null>(null)
  /**
   * ⛔ EL MOTIVO QUE DA EL MOTOR, y no una categoría nuestra. C-14: la consola clasificaba el
   *    rechazo (`conflict-domain`, `conflict-version`…) y mostraba un texto GENÉRICO traducido,
   *    tirando el `message` que el motor había enviado. Para una transición imposible el usuario
   *    leía «conflicto de dominio» y un código, nunca «no se puede pasar de draft a complete».
   *
   *    Se muestra ADEMÁS de la categoría, nunca en su lugar: la categoría decide QUÉ ACCIÓN se
   *    ofrece (re-leer no es reintentar) y eso no puede depender de una prosa que cambia. Y no se
   *    PARSEA: `errors.ts:20` avisa de que el mensaje es prosa y que lo legible por máquina son
   *    `code` y `details`. Aquí sólo se enseña.
   */
  const [failureReason, setFailureReason] = useState<string | null>(null)
  /** En qué fase murió: decide si el reintento re-planifica o re-aplica. */
  const [fallaronEnPlan, setFallaronEnPlan] = useState(false)
  const [values, setValues] = useState<Record<string, string>>({})

  // What the engine will REQUIRE for this command. Empty for most; not empty for the
  // six actions that shipped inoperable in the first pass.
  const fields: CommandField[] = intent
    ? requiredFieldsFor(intent.command, acceptanceState)
    : []

  const reset = useCallback(() => {
    setPhase('planning')
    setPlan(null)
    setOutcome(null)
    setFailure(null)
    setFailureCode(null)
    setFailureReason(null)
    setFallaronEnPlan(false)
    // ⛔ SEMBRADO DESDE LA PROPIA INTENCIÓN, y no es comodidad. Un contraste `sol max` midió el
    // 2026-08-16 que los comandos de lease piden `holder_sid` —una identidad de SESIÓN— a un
    // humano que no la tiene: la pantalla EXHIBE el titular en la fila de al lado y el diálogo
    // le pedía que lo transcribiera. Un campo obligatorio que sólo se puede rellenar copiando lo
    // que ya está en la misma pantalla no es una pregunta: es una trampa de transcripción, y
    // cada error tipográfico se paga con un `invalid_command` del motor.
    //
    // Se siembra lo que la intención TRAE, no lo que el diálogo adivina: quien la levanta decide
    // qué sabe. Y sigue siendo editable, porque un titular sembrado es un valor por defecto, no
    // una afirmación — `takeover` existe justamente para nombrar a otro.
    setValues(sembrarDesde(intent))
  }, [intent])

  const runPlan = useCallback(
    async (withValues: Record<string, string>) => {
      if (!intent) return
      setPhase('planning')
      setFailure(null)
      setFailureCode(null)
    setFailureReason(null)
      try {
        const p = await planWork(effectiveIntent(intent, fields, withValues))
        setPlan(p)
        setPhase('planned')
      } catch (err) {
        setFailure(classifyApplyFailure(err))
        setFailureCode(workErrorCode(err))
        setFailureReason(workErrorReason(err))
      setFailureReason(workErrorReason(err))
        setFallaronEnPlan(true)
        setPhase('failed')
      }
      // eslint-disable-next-line react-hooks/exhaustive-deps
    },
    [intent, acceptanceState],
  )

  // Plan as soon as the dialog opens with an intent: the operator asked for the action,
  // and planning writes nothing, so there is no reason to make them press twice.
  //
  // Keyed on intent.key rather than on `intent`, so a parent that rebuilds the object
  // each render cannot re-plan in a loop — and, more importantly, so a NEW intent (a
  // genuinely new intention, hence a new key) always gets its own plan.
  const intentKey = intent?.key ?? null
  useEffect(() => {
    if (!open || !intentKey) {
      reset()
      return
    }
    if (fields.length > 0) {
      // The operator has to supply what the engine demands BEFORE anything is planned.
      // Planning without it would just render a ROTO plan the operator cannot act on.
      // ⛔ SEMBRAR, NO BORRAR: `setValues({})` tiraba lo que la intención ya traía.
      setValues(sembrarDesde(intent))
      setPhase('input')
      return
    }
    void runPlan({})
    // runPlan closes over `intent`, which is pinned by its key for this effect.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, intentKey])

  const runApply = useCallback(async () => {
    if (!intent) return
    setPhase('applying')
    try {
      // The SAME intent — therefore the same Idempotency-Key — every time this runs.
      // The plan hash binds this apply to the plan the operator actually approved; if
      // the world moved underneath it the engine answers 412 plan_changed rather than
      // applying something nobody read.
      const result = await applyWork(
        effectiveIntent(intent, fields, values),
        plan?.plan_hash || undefined,
      )
      setOutcome(result)
      setPhase('applied')
      onApplied?.(result)
    } catch (err) {
      setFailure(classifyApplyFailure(err))
      setFailureCode(workErrorCode(err))
      setFailureReason(workErrorReason(err))
      setFallaronEnPlan(false)
      setPhase('failed')
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [intent, onApplied, plan, values, acceptanceState])

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-2xl">
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          {description ? (
            <DialogDescription>{description}</DialogDescription>
          ) : null}
        </DialogHeader>

        <div className="flex flex-col gap-4">
          {phase === 'input' ? (
            <RequiredFieldsForm
              fields={fields}
              values={values}
              onChange={setValues}
            />
          ) : null}

          {phase === 'planning' ? (
            <p className="text-sm text-muted-foreground">
              {t('apply.planning')}
            </p>
          ) : null}

          {plan ? (
            <PlanPanel plan={plan} intentKey={intent?.key ?? ''} />
          ) : null}

          {phase === 'applied' && outcome ? (
            <ResultPanel outcome={outcome} />
          ) : null}

          {phase === 'failed' && failure ? (
            <FailurePanel
              failure={failure}
              code={failureCode}
              reason={failureReason}
              onReread={() => {
                onReread?.()
                onOpenChange(false)
              }}
              // ⛔ SE REINTENTA LA FASE QUE FALLÓ, no siempre `apply`. Un fallo en el PLAN
              //    reintentado como APPLY salta el paso que existe para que nadie escriba sin
              //    haber visto antes lo que va a escribir — y con la misma clave, además, así
              //    que el motor lo trataría como el reintento de una intención ya planificada.
              //    Sólo un APPLY ambiguo se repite como APPLY, y ahí la misma clave es
              //    justamente lo que lo hace seguro.
              onRetrySameKey={
                fallaronEnPlan ? () => void runPlan(values) : runApply
              }
            />
          ) : null}
        </div>

        <DialogFooter>
          {phase === 'applied' ? (
            <Button onClick={() => onOpenChange(false)}>
              {t('apply.close')}
            </Button>
          ) : (
            <>
              <Button variant="ghost" onClick={() => onOpenChange(false)}>
                {t('apply.cancel')}
              </Button>
              {phase === 'input' ? (
                <Button
                  onClick={() => void runPlan(values)}
                  disabled={fields.some(
                    (f) => f.required && !(values[f.name] ?? '').trim(),
                  )}
                >
                  {t('apply.continue')}
                </Button>
              ) : null}
              {/* The apply button exists ONLY once a plan has been shown. This is the
                  structural half of trap 2: there is no "skip the plan" path. */}
              {phase === 'planned' && plan ? (
                <Button
                  onClick={runApply}
                  // A plan the engine could not compute must not be applicable: the
                  // operator would be confirming a plan nobody read.
                  disabled={isUnknownVerdict(plan) || plan.verdict === 'ROTO'}
                >
                  {t('apply.confirm')}
                </Button>
              ) : null}
              {phase === 'applying' ? (
                <Button disabled>{t('apply.applying')}</Button>
              ) : null}
            </>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

/** The plan: what would happen, the canonical hash, and the key that will carry it. */
function PlanPanel({ plan, intentKey }: { plan: Plan; intentKey: string }) {
  const { t } = useTranslation('work')

  // The plan itself can come back NO_HE_PODIDO_MIRAR on a 200 (work_api.go:199-205).
  // Rendering it as an ordinary plan with an empty effects list would tell the operator
  // "this change does nothing", which is the exact inversion of what the engine said.
  if (isUnknownVerdict(plan)) {
    return (
      <UnavailableNotice code={plan.code}>
        <ChecksList checks={plan.checks ?? []} />
      </UnavailableNotice>
    )
  }

  return (
    <div className="flex flex-col gap-3 rounded-lg border border-border p-4">
      <div className="flex items-center justify-between gap-3">
        <span className="text-sm font-medium">{t('apply.planTitle')}</span>
        <VerdictBadge verdict={plan.verdict} />
      </div>

      <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5 text-sm">
        <dt className="text-muted-foreground">{t('apply.command')}</dt>
        <dd className="font-mono text-xs">{plan.command}</dd>
        <dt className="text-muted-foreground">{t('apply.permission')}</dt>
        <dd className="font-mono text-xs">{plan.permission}</dd>
        <dt className="text-muted-foreground">{t('apply.eventType')}</dt>
        <dd className="font-mono text-xs">{plan.event_type}</dd>
      </dl>

      <div>
        <p className="mb-1 text-sm text-muted-foreground">
          {t('apply.rowEffects')}
        </p>
        {plan.row_effects?.length ? (
          <ul className="flex flex-col gap-1">
            {plan.row_effects.map((e) => (
              <li key={e} className="font-mono text-xs">
                {e}
              </li>
            ))}
          </ul>
        ) : (
          /* A LIMPIO plan with no row effects genuinely means "this writes nothing".
             That is a real answer and is worth stating, not left as blank space. */
          <p className="text-xs text-muted-foreground">
            {t('apply.noRowEffects')}
          </p>
        )}
      </div>

      {plan.external_calls?.length ? (
        <div>
          <p className="mb-1 text-sm text-muted-foreground">
            {t('apply.externalCalls')}
          </p>
          <ul className="flex flex-col gap-1">
            {plan.external_calls.map((c) => (
              <li key={c} className="font-mono text-xs">
                {c}
              </li>
            ))}
          </ul>
        </div>
      ) : null}

      <ChecksList checks={plan.checks ?? []} />

      {plan.plan_hash ? (
        <div className="flex items-center gap-2 border-t border-border pt-3">
          <span className="text-xs text-muted-foreground">
            {t('apply.planHash')}
          </span>
          <code className="font-mono text-xs break-all">{plan.plan_hash}</code>
        </div>
      ) : null}

      {/* THE KEY, BEFORE TRANSMISSION. Same contract as the CLI's printed line: an
          operator who holds it can settle an ambiguous timeout without guessing. */}
      <div className="flex items-start gap-2 rounded-md bg-muted p-3">
        <KeyRound
          aria-hidden
          className="mt-0.5 size-4 shrink-0 text-muted-foreground"
        />
        <div className="min-w-0">
          <p className="text-xs font-medium">{t('apply.keyTitle')}</p>
          <code className="font-mono text-xs break-all">{intentKey}</code>
          <p className="mt-1 text-xs text-muted-foreground">
            {t('apply.keyHelp')}
          </p>
        </div>
      </div>
    </div>
  )
}

/** The outcome. Replay is stated as replay — never dressed up as a fresh write. */
function ResultPanel({ outcome }: { outcome: ApplyOutcome }) {
  const { t } = useTranslation('work')
  const { result, replayed, etag } = outcome
  return (
    <div className="flex flex-col gap-3 rounded-lg border border-border p-4">
      <div className="flex items-center justify-between gap-3">
        <span className="flex items-center gap-2 text-sm font-medium">
          {replayed ? (
            <RefreshCcw aria-hidden className="size-4" />
          ) : (
            <Check aria-hidden className="size-4" />
          )}
          {replayed ? t('apply.replayedTitle') : t('apply.appliedTitle')}
        </span>
        <VerdictBadge verdict={result.verdict} />
      </div>

      {replayed ? (
        <p className="text-sm text-muted-foreground">
          {t('apply.replayedBody')}
        </p>
      ) : null}

      <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5 text-sm">
        <dt className="text-muted-foreground">{t('apply.commandId')}</dt>
        <dd className="font-mono text-xs break-all">{result.command_id}</dd>
        {result.result_id ? (
          <>
            <dt className="text-muted-foreground">{t('apply.resultId')}</dt>
            <dd className="font-mono text-xs break-all">{result.result_id}</dd>
          </>
        ) : null}
        {result.status ? (
          <>
            <dt className="text-muted-foreground">{t('apply.status')}</dt>
            <dd>
              <Badge variant="outline">{result.status}</Badge>
            </dd>
          </>
        ) : null}
        {etag ? (
          <>
            <dt className="text-muted-foreground">{t('apply.etag')}</dt>
            <dd className="font-mono text-xs">{etag}</dd>
          </>
        ) : null}
        <dt className="text-muted-foreground">{t('apply.auditSeq')}</dt>
        <dd className="font-mono text-xs">{result.audit_seq}</dd>
      </dl>
    </div>
  )
}

/**
 * The failure panel. Each class gets its OWN action, because the actions are
 * incompatible: re-reading after a version conflict is correct and retrying is
 * destructive; retrying after an unknown verdict is correct and re-reading tells you
 * nothing.
 */
function FailurePanel({
  failure,
  code,
  reason,
  onReread,
  onRetrySameKey,
}: {
  failure: ApplyFailure
  code: string | null
  reason: string | null
  onReread: () => void
  onRetrySameKey: () => void
}) {
  const { t } = useTranslation('work')

  if (failure === 'unknown') {
    return (
      <UnavailableNotice code={code ?? 'unknown'} retryHint>
        <Button
          size="sm"
          variant="outline"
          className="self-start"
          onClick={onRetrySameKey}
        >
          {t('apply.retrySameKey')}
        </Button>
      </UnavailableNotice>
    )
  }

  const body =
    failure === 'conflict-version'
      ? t('apply.conflictVersionBody')
      : failure === 'plan-changed'
        ? t('apply.planChangedBody')
        : failure === 'version-required'
          ? t('apply.versionRequiredBody')
          : failure === 'conflict-idempotency'
            ? t('apply.conflictIdempotencyBody')
            : failure === 'conflict-domain'
              ? t('apply.conflictDomainBody')
              : t('apply.otherBody')

  return (
    <div
      role="alert"
      className="flex flex-col gap-2 rounded-lg border border-danger-line bg-danger-soft p-4 text-sm text-danger"
    >
      <div className="flex items-center gap-2 font-medium">
        <ShieldAlert aria-hidden className="size-4 shrink-0" />
        {t(`apply.failure.${failure}`)}
      </div>
      <p className="text-danger">{body}</p>
      {/* El motivo TAL CUAL lo dio el motor. Sin él, una transición imposible se lee como un
          «conflicto» sin decir cuál — y el usuario no sabe si reintentar o cambiar de acción. */}
      {reason ? (
        <p className="text-danger" data-testid="engine-reason">
          {reason}
        </p>
      ) : null}
      {code ? (
        <p className="font-mono text-xs text-danger">
          {t('unavailable.code', { code })}
        </p>
      ) : null}
      {/* A version conflict offers RE-READ and deliberately offers no retry: the
          console must not re-send with the fresh ETag, which would overwrite the other
          writer's change with the face of a success. */}
      {failure === 'conflict-version' ? (
        <Button
          size="sm"
          variant="outline"
          className="self-start"
          onClick={onReread}
        >
          {t('apply.reread')}
        </Button>
      ) : null}
    </div>
  )
}

/**
 * The fields the engine will reject the command without.
 *
 * This form is the answer to six buttons that could never work: block/fail/cancel were
 * sent with an empty body against work_state.go:292-294, which demands a code token and
 * a non-empty reason, and the three acceptance verdicts were sent as a bare state
 * against :318-321, which requires the acceptance array — with an evidence ref for
 * passed/failed and an evidence hash for passed (:196-201).
 *
 * The continue button stays disabled until every required field has a value, so the
 * operator is never sent to a plan that is guaranteed to come back ROTO.
 */
function RequiredFieldsForm({
  fields,
  values,
  onChange,
}: {
  fields: CommandField[]
  values: Record<string, string>
  onChange: (v: Record<string, string>) => void
}) {
  const { t } = useTranslation('work')
  return (
    <div className="flex flex-col gap-3">
      <p className="text-sm text-muted-foreground">{t('apply.inputIntro')}</p>
      {fields.map((f) => (
        <Field
          key={f.name}
          label={t(`apply.field.${f.name}.label`)}
          description={t(`apply.field.${f.name}.hint`)}
          required={f.required}
        >
          <Input
            value={values[f.name] ?? ''}
            onChange={(e) =>
              onChange({ ...values, [f.name]: e.currentTarget.value })
            }
            placeholder={t(`apply.field.${f.name}.placeholder`)}
          />
        </Field>
      ))}
    </div>
  )
}
