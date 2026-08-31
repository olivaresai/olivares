// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Author a sandbox scenario from the console. The engine has served
// `POST /v1/m/sandbox/scenarios` since module XVII was written
// (modules/sandbox/sandbox.go:166) and the console had never called it: an operator
// could run, replay and compare scenarios but could not create one, so the screen
// only worked for scenarios that arrived some other way.
//
// The form is the engine's `createScenarioRequest`, READ (scenarios.go:53-59), not
// inferred from the console's own DTO: name (the only required field), description,
// subject_kind, and the two synthetic halves — steps (key + input) and mocks
// (resource + response). The handler decodes with `DisallowUnknownFields`, so this
// sends those five properties and nothing else.
//
// It computes nothing the engine decides: a blank step key becomes `step-<n>` THERE
// (clampSteps), the spec hash is derived THERE, and a duplicate name is the engine's
// 409 — surfaced verbatim inside the dialog instead of a generic red toast, because
// "a scenario with this name already exists" is an answer the operator can act on.
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Plus, X } from 'lucide-react'
import { useRef, useState, type FormEvent } from 'react'
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
import { Field } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Spinner } from '@/components/ui/spinner'
import { Textarea } from '@/components/ui/textarea'
import { toast } from '@/components/ui/toaster'
import { ApiError } from '@/lib/api/errors'
import { useFailedActionReporter } from '@/lib/hooks/use-privileged-mutation'
import { useAuth } from '@/lib/auth/context'
import { sandboxApi, sandboxKeys } from './api'
import type { CreateScenarioInput, ScenarioMock, ScenarioStep } from './types'
import './i18n'

/** A row the operator has not filled in at all — dropped before the request. */
const isBlankStep = (s: ScenarioStep) =>
  s.key.trim() === '' && s.input.trim() === ''
const isBlankMock = (m: ScenarioMock) =>
  m.resource.trim() === '' && m.response.trim() === ''

export function ScenarioCreateDialog({
  open,
  onOpenChange,
}: {
  open: boolean
  onOpenChange: (o: boolean) => void
}) {
  const { t } = useTranslation(['sandbox', 'common'])
  const report = useFailedActionReporter()
  const { activeTenant } = useAuth()
  const qc = useQueryClient()

  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [subjectKind, setSubjectKind] = useState('')
  const [steps, setSteps] = useState<ScenarioStep[]>([{ key: '', input: '' }])
  const [mocks, setMocks] = useState<ScenarioMock[]>([])
  // The engine's refusal, shown where the operator is working. Cleared on every
  // submit so a stale 409 can never sit next to a request that has not run yet.
  const [failure, setFailure] = useState<string | null>(null)

  const reset = () => {
    setName('')
    setDescription('')
    setSubjectKind('')
    setSteps([{ key: '', input: '' }])
    setMocks([])
    setFailure(null)
  }

  // El presupuesto es de la ACCIÓN, no del componente: cada envío del operador lo repone,
  // y la reanudación automática no (esa es la que lo gasta).
  const reanudadoRef = useRef(false)

  const create = useMutation({
    mutationFn: () => {
      const body: CreateScenarioInput = {
        name: name.trim(),
        description: description.trim(),
        subject_kind: subjectKind.trim(),
        steps: steps.filter((s) => !isBlankStep(s)),
        mocks: mocks.filter((m) => !isBlankMock(m)),
      }
      return sandboxApi.createScenario(body)
    },
    onSuccess: async () => {
      // The list must reflect it without a manual reload; the key is tenant-scoped,
      // so a principal who switches org does not see the other tenant's refetch.
      await qc.invalidateQueries({
        queryKey: sandboxKeys.scenarios(activeTenant),
      })
      toast.success(t('sandbox:scenarios.create.success'))
      reset()
      onOpenChange(false)
    },
    onError: (err) => {
      // A 403 is a permission boundary, not a failure (the action is hidden when the
      // principal lacks the right — this is the race net). Everything else carries
      // the engine's own message: 400 "name is required", 409 duplicate name.
      // ⛔ ASEGURAMIENTO ANTES QUE ROL. `isForbidden` es SÓLO el status 403
      // (lib/api/errors.ts:59) y un `step_up_required` lo satisface también, así que el
      // operador recibía «no tienes autorización» —falso— con el escenario recién tecleado
      // por medio. La rama de rol NO se delega: escribe en el área de fallo del formulario,
      // no un toast, y `report` no sabe de este formulario.
      if (err instanceof ApiError && err.isStepUpRequired) {
        // ⛔ UNA sola reanudación automática por acción del operador, que es exactamente lo
        // que hace la política común (lib/hooks/use-privileged-mutation.ts:121-138) y yo me
        // había saltado: la ceremonia concede AAL3, el nivel más alto que el motor emite, así
        // que un SEGUNDO `step_up_required` justo después significa que lo que refusa no es
        // el aseguramiento — reintentar entonces es un bucle que el operador no ve. El panel
        // se sigue abriendo; lo que se gasta es el reintento automático. Lo cazó `sol max`.
        report(
          err,
          reanudadoRef.current
            ? undefined
            : () => {
                reanudadoRef.current = true
                create.mutate()
              },
        )
        return
      }
      if (err instanceof ApiError && err.isForbidden) {
        setFailure(t('common:privileged.notAuthorizedToast'))
        return
      }
      setFailure(
        err instanceof ApiError && err.message
          ? err.message
          : t('sandbox:scenarios.create.failed'),
      )
    },
  })

  const submittable = name.trim() !== '' && !create.isPending

  const onSubmit = (e: FormEvent) => {
    e.preventDefault()
    // Mirrors the engine's own rule (it trims, then 400s on an empty name) rather
    // than inventing a stricter one — the button is disabled for the same reason.
    if (!submittable) return
    setFailure(null)
    reanudadoRef.current = false
    create.mutate()
  }

  const close = () => {
    reset()
    onOpenChange(false)
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        if (create.isPending) return
        if (!o) reset()
        onOpenChange(o)
      }}
    >
      <DialogContent className="max-h-[88vh] max-w-2xl overflow-y-auto">
        <DialogHeader>
          <DialogTitle>{t('sandbox:scenarios.create.title')}</DialogTitle>
          <DialogDescription>
            {t('sandbox:scenarios.create.description')}
          </DialogDescription>
        </DialogHeader>

        <form onSubmit={onSubmit} className="flex flex-col gap-3">
          <Field label={t('sandbox:scenarios.create.name')} required>
            <Input
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder={t('sandbox:scenarios.create.namePlaceholder')}
            />
          </Field>

          <Field
            label={t('sandbox:scenarios.create.subjectKind')}
            description={t('sandbox:scenarios.create.subjectKindHint')}
          >
            <Input
              value={subjectKind}
              onChange={(e) => setSubjectKind(e.target.value)}
              placeholder={t('sandbox:scenarios.create.subjectKindPlaceholder')}
              mono
            />
          </Field>

          <Field label={t('sandbox:scenarios.create.descriptionLabel')}>
            <Textarea
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              placeholder={t('sandbox:scenarios.create.descriptionPlaceholder')}
            />
          </Field>

          <RowGroup
            title={t('sandbox:scenarios.create.steps')}
            hint={t('sandbox:scenarios.create.stepsHint')}
            addLabel={t('sandbox:scenarios.create.addStep')}
            empty={t('sandbox:scenarios.create.stepsEmpty')}
            onAdd={() => setSteps((rows) => [...rows, { key: '', input: '' }])}
            rows={steps.map((step, i) => ({
              removeLabel: t('sandbox:scenarios.create.removeStep', {
                index: i + 1,
              }),
              onRemove: () =>
                setSteps((rows) => rows.filter((_, at) => at !== i)),
              fields: (
                <>
                  <Field
                    label={t('sandbox:scenarios.create.stepKey', {
                      index: i + 1,
                    })}
                  >
                    <Input
                      value={step.key}
                      onChange={(e) =>
                        setSteps((rows) =>
                          rows.map((r, at) =>
                            at === i ? { ...r, key: e.target.value } : r,
                          ),
                        )
                      }
                      placeholder={t(
                        'sandbox:scenarios.create.stepKeyPlaceholder',
                      )}
                      mono
                    />
                  </Field>
                  <Field
                    label={t('sandbox:scenarios.create.stepInput', {
                      index: i + 1,
                    })}
                  >
                    <Input
                      value={step.input}
                      onChange={(e) =>
                        setSteps((rows) =>
                          rows.map((r, at) =>
                            at === i ? { ...r, input: e.target.value } : r,
                          ),
                        )
                      }
                      placeholder={t(
                        'sandbox:scenarios.create.stepInputPlaceholder',
                      )}
                    />
                  </Field>
                </>
              ),
            }))}
          />

          <RowGroup
            title={t('sandbox:scenarios.create.mocks')}
            hint={t('sandbox:scenarios.create.mocksHint')}
            addLabel={t('sandbox:scenarios.create.addMock')}
            empty={t('sandbox:scenarios.create.mocksEmpty')}
            onAdd={() =>
              setMocks((rows) => [...rows, { resource: '', response: '' }])
            }
            rows={mocks.map((mock, i) => ({
              removeLabel: t('sandbox:scenarios.create.removeMock', {
                index: i + 1,
              }),
              onRemove: () =>
                setMocks((rows) => rows.filter((_, at) => at !== i)),
              fields: (
                <>
                  <Field
                    label={t('sandbox:scenarios.create.mockResource', {
                      index: i + 1,
                    })}
                  >
                    <Input
                      value={mock.resource}
                      onChange={(e) =>
                        setMocks((rows) =>
                          rows.map((r, at) =>
                            at === i ? { ...r, resource: e.target.value } : r,
                          ),
                        )
                      }
                      placeholder={t(
                        'sandbox:scenarios.create.mockResourcePlaceholder',
                      )}
                      mono
                    />
                  </Field>
                  <Field
                    label={t('sandbox:scenarios.create.mockResponse', {
                      index: i + 1,
                    })}
                  >
                    <Input
                      value={mock.response}
                      onChange={(e) =>
                        setMocks((rows) =>
                          rows.map((r, at) =>
                            at === i ? { ...r, response: e.target.value } : r,
                          ),
                        )
                      }
                      placeholder={t(
                        'sandbox:scenarios.create.mockResponsePlaceholder',
                      )}
                    />
                  </Field>
                </>
              ),
            }))}
          />

          <p className="text-xs text-muted-foreground">
            {t('sandbox:scenarios.create.syntheticNote')}
          </p>

          {failure ? (
            <p
              role="alert"
              className="rounded-md border border-danger-line bg-danger-soft px-2.5 py-2 text-xs text-danger"
            >
              {failure}
            </p>
          ) : null}

          <DialogFooter>
            <Button
              type="button"
              variant="secondary"
              onClick={close}
              disabled={create.isPending}
            >
              {t('common:actions.cancel')}
            </Button>
            <Button type="submit" variant="primary" disabled={!submittable}>
              {create.isPending && <Spinner size="sm" aria-hidden />}
              {create.isPending
                ? t('sandbox:scenarios.create.submitting')
                : t('sandbox:scenarios.create.submit')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

/** A repeatable group (steps / mocks): a heading, an add button, and one removable
 *  block per row. Presentational — the rows and their handlers come from the caller. */
function RowGroup({
  title,
  hint,
  addLabel,
  empty,
  rows,
  onAdd,
}: {
  title: string
  hint: string
  addLabel: string
  empty: string
  rows: {
    fields: React.ReactNode
    removeLabel: string
    onRemove: () => void
  }[]
  onAdd: () => void
}) {
  return (
    <fieldset className="flex flex-col gap-2 rounded-md border border-border p-3">
      <legend className="px-1 text-xs font-medium text-foreground">
        {title}
      </legend>
      <p className="text-xs text-muted-foreground">{hint}</p>
      {rows.length === 0 ? (
        <p className="text-xs text-muted-foreground italic">{empty}</p>
      ) : (
        rows.map((row, i) => (
          <div
            key={i}
            className="flex items-end gap-2 rounded-md border border-border bg-muted/30 p-2"
          >
            <div className="grid flex-1 gap-2 sm:grid-cols-2">{row.fields}</div>
            <Button
              type="button"
              variant="ghost"
              size="icon-sm"
              aria-label={row.removeLabel}
              onClick={row.onRemove}
            >
              <X aria-hidden />
            </Button>
          </div>
        ))
      )}
      <div>
        <Button type="button" variant="secondary" size="sm" onClick={onAdd}>
          <Plus className="size-3.5" aria-hidden />
          {addLabel}
        </Button>
      </div>
    </fieldset>
  )
}
