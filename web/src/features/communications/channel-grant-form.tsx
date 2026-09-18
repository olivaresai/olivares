// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useId, useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Field } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import type { CommunicationsScope } from './boundary'
import { localToIso } from './channel-create-form'
import { SubjectPicker, type Me, type PickedSubject } from './subject-picker'
import type { GrantCreateInput } from './types'

export interface GrantDraft {
  subject: PickedSubject
  read: boolean
  write: boolean
  admin: boolean
  /** `datetime-local` value; empty for no expiry. */
  expiresAt: string
}

export const emptyGrantDraft = (): GrantDraft => ({
  subject: { kind: 'user', ref: '' },
  read: false,
  write: false,
  admin: false,
  expiresAt: '',
})

/**
 * ChannelGrantForm — the body of ONE `POST /channels/{id}/grants`, composed
 * explicitly: a subject (the directory is offered only under `user:read` /
 * `agent:read` and only ever fills the reference the operator could also type),
 * three INDEPENDENT bits, an optional expiry. At least one bit is required, as the
 * engine requires. The form yields the body to its owner, which freezes it into an
 * intention against the Channel ETag on screen and asks for a separate
 * confirmation: nothing here sends. A row in the directory is a candidate, not an
 * eligible subject, and a recorded generation is not a promise of effective access.
 */
export function ChannelGrantForm({
  initial,
  scope,
  canUserRead,
  canAgentRead,
  me,
  disabled,
  onReview,
  onCancel,
}: {
  initial?: GrantDraft
  scope: CommunicationsScope
  canUserRead: boolean
  canAgentRead: boolean
  me: Me
  disabled: boolean
  onReview: (body: GrantCreateInput) => void
  onCancel: () => void
}) {
  const { t } = useTranslation('communications')
  const idp = useId()
  const [draft, setDraft] = useState<GrantDraft>(
    () => initial ?? emptyGrantDraft(),
  )
  const [errors, setErrors] = useState<string[]>([])

  const submit = (e: FormEvent) => {
    e.preventDefault()
    if (disabled) return
    const problems: string[] = []
    const ref = draft.subject.ref.trim()
    if (ref === '') problems.push(t('create.validation.grantRef'))
    if (!draft.read && !draft.write && !draft.admin)
      problems.push(t('create.validation.grantBits'))
    const iso = localToIso(draft.expiresAt)
    if (draft.expiresAt !== '' && iso === null)
      problems.push(t('grants.validation.expiry'))
    if (iso !== null && new Date(iso).getTime() <= Date.now())
      problems.push(t('grants.validation.expiryPast'))
    setErrors(problems)
    if (problems.length > 0) return
    const body: GrantCreateInput = {
      subject: { kind: draft.subject.kind, ref },
      can_read: draft.read,
      can_write: draft.write,
      can_admin: draft.admin,
    }
    if (iso) body.expires_at = iso
    onReview(body)
  }

  return (
    <form
      onSubmit={submit}
      className="flex flex-col gap-3 rounded-md border border-border bg-surface p-3"
      noValidate
      data-slot="grant-form"
      aria-label={t('grants.form.title')}
    >
      <p className="text-body font-medium">{t('grants.form.title')}</p>
      <p className="text-caption text-muted-foreground">
        {t('grants.form.hint')}
      </p>
      {errors.length > 0 ? (
        <ul role="alert" className="list-disc pl-5 text-body text-danger">
          {errors.map((e) => (
            <li key={e}>{e}</li>
          ))}
        </ul>
      ) : null}
      <SubjectPicker
        mode="grant"
        value={draft.subject}
        onChange={(subject) => setDraft((d) => ({ ...d, subject }))}
        scope={scope}
        canUserRead={canUserRead}
        canAgentRead={canAgentRead}
        me={me}
        idPrefix={`${idp}-subject`}
        disabled={disabled}
      />
      <div className="flex flex-wrap items-center gap-4">
        {(['read', 'write', 'admin'] as const).map((bit) => (
          <div key={bit} className="flex items-center gap-2">
            <Checkbox
              id={`${idp}-${bit}`}
              checked={draft[bit]}
              onCheckedChange={(c) =>
                setDraft((d) => ({ ...d, [bit]: c === true }))
              }
              disabled={disabled}
            />
            <Label htmlFor={`${idp}-${bit}`}>{t(`create.grant.${bit}`)}</Label>
          </div>
        ))}
      </div>
      <Field
        label={t('create.grant.expiresAt')}
        htmlFor={`${idp}-expires`}
        description={t('create.grant.expiresHint')}
        className="sm:max-w-72"
      >
        <Input
          id={`${idp}-expires`}
          type="datetime-local"
          value={draft.expiresAt}
          onChange={(e) =>
            setDraft((d) => ({ ...d, expiresAt: e.target.value }))
          }
          disabled={disabled}
        />
      </Field>
      <div className="flex flex-wrap justify-end gap-2">
        <Button type="button" variant="ghost" size="sm" onClick={onCancel}>
          {t('actions.cancel')}
        </Button>
        <Button type="submit" variant="primary" size="sm" disabled={disabled}>
          {t('actions.reviewGrant')}
        </Button>
      </div>
    </form>
  )
}
