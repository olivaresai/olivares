// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { Plus, Trash2 } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { Field } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import {
  newReferenceDraft,
  type ReferenceDraft,
} from './handoff-reference-drafts'

/**
 * ReferenceEditor — the rows of typed references shared by the offer's artifacts and
 * the rejection's reason. Its validation lives in `handoff-reference-drafts`, beside
 * the engine rule it mirrors; this file is only the form.
 */
export function ReferenceEditor({
  drafts,
  onChange,
  idPrefix,
  max,
  label,
  error,
  disabled = false,
}: {
  drafts: readonly ReferenceDraft[]
  onChange: (next: ReferenceDraft[]) => void
  idPrefix: string
  max: number
  label: string
  /** Marks every row invalid and names the problem on the group. */
  error?: string
  disabled?: boolean
}) {
  const { t } = useTranslation('communications')
  const patch = (id: string, over: Partial<ReferenceDraft>) =>
    onChange(drafts.map((d) => (d.id === id ? { ...d, ...over } : d)))

  return (
    <fieldset
      className="flex flex-col gap-2"
      data-slot="handoff-references"
      aria-invalid={error ? true : undefined}
      aria-describedby={error ? `${idPrefix}-error` : undefined}
    >
      <legend className="text-sm font-medium">{label}</legend>
      <p className="text-xs text-muted-foreground">
        {t('handoff.references.hint')}
      </p>
      {error ? (
        <p
          id={`${idPrefix}-error`}
          role="alert"
          className="text-xs text-danger"
          data-slot="handoff-references-error"
        >
          {error}
        </p>
      ) : null}
      {drafts.map((d, i) => (
        <div
          key={d.id}
          className="grid gap-2 rounded-md border border-border p-2 sm:grid-cols-3"
          data-slot="handoff-reference-row"
        >
          <Field
            label={t('handoff.references.kind')}
            htmlFor={`${idPrefix}-ref-kind-${d.id}`}
          >
            <Input
              id={`${idPrefix}-ref-kind-${d.id}`}
              value={d.kind}
              onChange={(e) => patch(d.id, { kind: e.target.value })}
              autoComplete="off"
              mono
              disabled={disabled}
              aria-invalid={error && d.kind.trim() === '' ? true : undefined}
            />
          </Field>
          <Field
            label={t('handoff.references.ref')}
            htmlFor={`${idPrefix}-ref-ref-${d.id}`}
          >
            <Input
              id={`${idPrefix}-ref-ref-${d.id}`}
              value={d.ref}
              onChange={(e) => patch(d.id, { ref: e.target.value })}
              autoComplete="off"
              mono
              disabled={disabled}
              aria-invalid={error && d.ref.trim() === '' ? true : undefined}
            />
          </Field>
          <div className="flex items-end gap-2">
            <Field
              label={t('handoff.references.hash')}
              htmlFor={`${idPrefix}-ref-hash-${d.id}`}
            >
              <Input
                id={`${idPrefix}-ref-hash-${d.id}`}
                value={d.hash}
                onChange={(e) => patch(d.id, { hash: e.target.value })}
                autoComplete="off"
                mono
                disabled={disabled}
              />
            </Field>
            <Button
              type="button"
              variant="ghost"
              size="sm"
              aria-label={`${t('handoff.references.remove')} ${i + 1}`}
              disabled={disabled}
              onClick={() => onChange(drafts.filter((x) => x.id !== d.id))}
            >
              <Trash2 className="size-4" aria-hidden="true" />
            </Button>
          </div>
        </div>
      ))}
      <div>
        <Button
          type="button"
          variant="outline"
          size="sm"
          disabled={disabled || drafts.length >= max}
          onClick={() => onChange([...drafts, newReferenceDraft()])}
        >
          <Plus className="size-4" aria-hidden="true" />
          {t('handoff.references.add')}
        </Button>
      </div>
    </fieldset>
  )
}
