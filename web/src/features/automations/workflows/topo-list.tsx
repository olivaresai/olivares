// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { Pencil, Plus, Trash2, TriangleAlert } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { cn } from '@/lib/utils'
import { stepSummary, validationMessage } from './presenters'
import {
  stepRefSchema,
  topologicalSteps,
  type GraphValidationError,
  type WorkflowStep,
} from './types'

export interface TopoListProps {
  steps: WorkflowStep[]
  errors: GraphValidationError[]
  selectedRef?: string
  canWrite: boolean
  onAdd: () => void
  onSelect: (ref: string) => void
  onDelete: (ref: string) => void
  onToggleDependency: (
    stepRef: string,
    dependencyRef: string,
    checked: boolean,
  ) => void
}

export function TopoList({
  steps,
  errors,
  selectedRef,
  canWrite,
  onAdd,
  onSelect,
  onDelete,
  onToggleDependency,
}: TopoListProps) {
  const { t } = useTranslation('automations-workflows')
  const ordered = topologicalSteps(steps)
  const counts = new Map<string, number>()
  for (const step of steps)
    counts.set(step.ref, (counts.get(step.ref) ?? 0) + 1)
  const validRefs = steps
    .filter(
      (step) =>
        stepRefSchema.safeParse(step.ref).success && counts.get(step.ref) === 1,
    )
    .map((step) => step.ref)

  return (
    <section className="rounded-lg border border-border bg-surface p-4">
      <div className="mb-4 flex items-center justify-between gap-3">
        <h3 className="text-sm font-medium text-foreground">
          {t('editor.list')}
        </h3>
        {canWrite ? (
          <Button size="sm" variant="secondary" onClick={onAdd}>
            <Plus aria-hidden />
            {t('editor.add')}
          </Button>
        ) : null}
      </div>
      {ordered.length === 0 ? (
        <p className="py-8 text-center text-sm text-muted-foreground">
          {t('list.emptyHint')}
        </p>
      ) : (
        <ol className="space-y-3">
          {ordered.map((step, index) => {
            const stepErrors = errors.filter(
              (error) => error.stepRef === step.ref,
            )
            const candidates = validRefs.filter((ref) => ref !== step.ref)
            const isSelected = selectedRef === step.ref
            return (
              <li
                key={`${step.ref}-${index}`}
                //the selected step used to be identified by --accent-line
                // alone (1.34:1 dark / 1.60:1 light), i.e. by colour only and below
                // the SC 1.4.11 floor. Now: --accent-strong (>=3:1, gated), plus the
                // rail below as a shape, plus aria-current for AT.
                aria-current={isSelected ? 'true' : undefined}
                className={cn(
                  'rounded-lg border p-3',
                  stepErrors.length > 0
                    ? 'border-danger bg-danger-soft/30'
                    : isSelected
                      ? 'border-accent-strong bg-accent-soft/30'
                      : 'border-border bg-elevated',
                )}
              >
                <div className="flex flex-wrap items-start gap-3">
                  <span
                    aria-hidden
                    className={cn(
                      'h-6 w-1 shrink-0 rounded-full',
                      isSelected ? 'bg-accent-strong' : 'bg-transparent',
                    )}
                  />
                  <span className="flex size-6 shrink-0 items-center justify-center rounded-full bg-muted font-mono text-xs text-muted-foreground">
                    {index + 1}
                  </span>
                  <div className="min-w-0 flex-1">
                    <div className="flex flex-wrap items-center gap-2">
                      <code className="font-mono text-sm text-foreground">
                        {step.ref}
                      </code>
                      <Badge variant="outline">{t(`kind.${step.kind}`)}</Badge>
                      {stepErrors.length > 0 ? (
                        <Badge variant="danger">
                          <TriangleAlert aria-hidden />
                          {t('editor.nodeError', { ref: step.ref })}
                        </Badge>
                      ) : null}
                    </div>
                    <p className="mt-1 text-xs text-muted-foreground">
                      {stepSummary(step, t)}
                    </p>
                    {stepErrors.length > 0 ? (
                      <ul className="mt-2 space-y-1" role="alert">
                        {stepErrors.map((error) => (
                          <li
                            key={error.message}
                            className="text-xs text-danger"
                          >
                            {validationMessage(error.message, t)}
                          </li>
                        ))}
                      </ul>
                    ) : null}
                  </div>
                  <div className="flex items-center gap-1">
                    <Button
                      size="icon-sm"
                      variant="ghost"
                      aria-label={t('editor.editNode', { ref: step.ref })}
                      onClick={() => onSelect(step.ref)}
                    >
                      <Pencil aria-hidden />
                    </Button>
                    {canWrite ? (
                      <Button
                        size="icon-sm"
                        variant="destructive"
                        aria-label={t('editor.deleteNode', { ref: step.ref })}
                        onClick={() => onDelete(step.ref)}
                      >
                        <Trash2 aria-hidden />
                      </Button>
                    ) : null}
                  </div>
                </div>

                <fieldset className="mt-3 border-t border-border pt-3">
                  <legend className="px-1 text-xs font-medium text-foreground">
                    {t('deps.title')}
                  </legend>
                  {candidates.length === 0 ? (
                    <p className="text-xs text-muted-foreground">
                      {t('deps.none')}
                    </p>
                  ) : (
                    <div className="flex flex-wrap gap-x-4 gap-y-2">
                      {candidates.map((dependency) => {
                        const checked = step.depends_on.includes(dependency)
                        const disabled =
                          !canWrite || (!checked && step.depends_on.length >= 8)
                        const label = t('deps.toggle', {
                          step: step.ref,
                          dependency,
                        })
                        return (
                          <label
                            key={dependency}
                            className="flex min-h-6 items-center gap-2 text-xs text-foreground"
                          >
                            <Checkbox
                              checked={checked}
                              disabled={disabled}
                              aria-label={label}
                              onCheckedChange={(value) =>
                                onToggleDependency(
                                  step.ref,
                                  dependency,
                                  value === true,
                                )
                              }
                            />
                            <code>{dependency}</code>
                          </label>
                        )
                      })}
                    </div>
                  )}
                  {step.depends_on.length >= 8 ? (
                    <p className="mt-2 text-xs text-warning">
                      {t('deps.limit')}
                    </p>
                  ) : null}
                </fieldset>
              </li>
            )
          })}
        </ol>
      )}
    </section>
  )
}
