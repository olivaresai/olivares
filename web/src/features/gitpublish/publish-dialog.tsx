// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// One governed publication request — a push, a pull request or a merge — and its answer.
//
// ⛔ ONE DECISION, ONE OPERATION ID, AND NO RESEND AFTER A DISPATCH. The id is minted when the
//    dialog opens and again whenever the request changes, so an unchanged request carries the
//    same id (the engine replays it instead of dispatching again). Once the engine has
//    answered with an intent — or no answer arrived — the form is gone: an uncertain outcome
//    offers reconcile, and a lost answer sends the operator to the intent list.
import { useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
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
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Textarea } from '@/components/ui/textarea'
import { useAuth } from '@/lib/auth/context'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { ApiError } from '@/lib/api/errors'
import { gitpublishApi, gitpublishKeys } from './api'
import {
  actionAuthority,
  newOperationId,
  outcomeOf,
  reconcileAction,
  type PublicationOutcome,
} from './model'
import { AuthorityNote, OutcomePanel } from './parts'
import type {
  MergeInput,
  PublicationEffect,
  PublicationIntent,
  PublicationTarget,
} from './types'
import './i18n'

type Draft = Record<string, string>

const EMPTY: Record<PublicationEffect, Draft> = {
  push: { ref: '', expected_old: '', commit: '', tree: '', ack: '' },
  pull_request: {
    head_ref: '',
    base: '',
    commit: '',
    title: '',
    body: '',
    draft: '',
    ack: '',
  },
  merge: { number: '', expected_head: '', method: 'merge', ack: '' },
}

export function PublishDialog({
  target,
  effect,
  onClose,
  onOpenIntent,
}: {
  target: PublicationTarget
  effect: PublicationEffect
  onClose: () => void
  onOpenIntent: (id: string) => void
}) {
  const { t } = useTranslation('gitpublish')
  const { activeTenant, can } = useAuth()
  const [draft, setDraft] = useState<Draft>(() => ({
    ...EMPTY[effect],
    base: target.merge_bases[0] ?? '',
  }))
  const [operationId, setOperationId] = useState(newOperationId)
  // The engine's answer to this request or to a reconcile of its intent, once there is one.
  const [answer, setAnswer] = useState<PublicationOutcome | null>(null)

  const invalidate = (intent?: PublicationIntent) => [
    gitpublishKeys.intents(activeTenant, target.id),
    ...(intent ? [gitpublishKeys.intent(activeTenant, intent.id)] : []),
  ]
  const request = usePrivilegedMutation<void, PublicationIntent>({
    mutationFn: () => send(effect, target.id, draft, operationId, activeTenant),
    invalidateKeys: (intent) => invalidate(intent),
    successMessage: (intent) =>
      t('outcome.toast', {
        state: t(`states.${intent.answer ?? intent.state}`),
      }),
    onDone: (intent) => setAnswer(outcomeOf({ data: intent })),
    // Refusals and lost answers are rendered here with their code; the hook still owns
    // the step-up ceremony and the authorization toast.
    onError: () => true,
  })
  const reconcile = usePrivilegedMutation<string, PublicationIntent>({
    mutationFn: (id) => gitpublishApi.reconcile(id, { tenant: activeTenant }),
    invalidateKeys: (intent) => invalidate(intent),
    successMessage: (intent) =>
      t('outcome.toast', { state: t(`states.${intent.state}`) }),
    onDone: (intent) => setAnswer(outcomeOf({ data: intent })),
    onError: () => true,
  })

  const set = (key: string, value: string) => {
    setDraft((d) => ({ ...d, [key]: value }))
    // A changed request is a new decision, so it gets a new operation id.
    setOperationId(newOperationId())
    if (request.error) request.reset()
  }

  const requestFailure = failureOf(request.error)
  const reconcileFailure = failureOf(reconcile.error)
  const outcome = answer ?? requestFailure
  // Once the engine answered with an intent, or nothing came back, this dialog cannot send.
  const locked = outcome !== null && outcome.kind !== 'refused'
  const valid = isComplete(effect, draft)

  return (
    <Dialog open onOpenChange={(open) => (open ? undefined : onClose())}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>{t(`publish.${effect}.title`)}</DialogTitle>
          <DialogDescription>
            {t(`publish.${effect}.description`, {
              prefix: target.push_prefix,
            })}
          </DialogDescription>
        </DialogHeader>

        {!locked ? (
          <form
            id="gitpublish-publish"
            className="space-y-3"
            onSubmit={(e) => {
              e.preventDefault()
              if (valid && !request.isPending) request.mutate()
            }}
          >
            {effect === 'push' ? (
              <>
                <Field
                  label={t('fields.ref')}
                  description={t('fields.refHint', {
                    prefix: target.push_prefix,
                  })}
                  required
                >
                  <Input
                    mono
                    value={draft.ref}
                    placeholder={`refs/heads/${target.push_prefix}`}
                    onChange={(e) => set('ref', e.target.value)}
                  />
                </Field>
                <Field
                  label={t('fields.expectedOld')}
                  description={t('fields.expectedOldHint')}
                >
                  <Input
                    mono
                    value={draft.expected_old}
                    onChange={(e) => set('expected_old', e.target.value)}
                  />
                </Field>
                <ShaField
                  label={t('fields.commit')}
                  value={draft.commit}
                  onChange={(v) => set('commit', v)}
                />
                <ShaField
                  label={t('fields.tree')}
                  hint={t('fields.treeHint')}
                  value={draft.tree}
                  onChange={(v) => set('tree', v)}
                />
              </>
            ) : null}
            {effect === 'pull_request' ? (
              <>
                <Field
                  label={t('fields.headRef')}
                  description={t('fields.headRefHint', {
                    prefix: target.push_prefix,
                  })}
                  required
                >
                  <Input
                    mono
                    value={draft.head_ref}
                    placeholder={target.push_prefix}
                    onChange={(e) => set('head_ref', e.target.value)}
                  />
                </Field>
                <BaseField
                  label={t('fields.base')}
                  bases={target.merge_bases}
                  value={draft.base}
                  onChange={(v) => set('base', v)}
                />
                <ShaField
                  label={t('fields.commit')}
                  value={draft.commit}
                  onChange={(v) => set('commit', v)}
                />
                <Field label={t('fields.title')} required>
                  <Input
                    value={draft.title}
                    maxLength={256}
                    onChange={(e) => set('title', e.target.value)}
                  />
                </Field>
                <Field label={t('fields.body')}>
                  <Textarea
                    value={draft.body}
                    maxLength={65536}
                    rows={4}
                    onChange={(e) => set('body', e.target.value)}
                  />
                </Field>
                <div className="flex items-center gap-2">
                  <Checkbox
                    id="gitpublish-draft"
                    checked={draft.draft === 'true'}
                    onCheckedChange={(c) =>
                      set('draft', c === true ? 'true' : '')
                    }
                  />
                  <Label htmlFor="gitpublish-draft">{t('fields.draft')}</Label>
                </div>
              </>
            ) : null}
            {effect === 'merge' ? (
              <>
                <Field label={t('fields.number')} required>
                  <Input
                    inputMode="numeric"
                    value={draft.number}
                    onChange={(e) => set('number', e.target.value)}
                  />
                </Field>
                <ShaField
                  label={t('fields.expectedHead')}
                  hint={t('fields.expectedHeadHint')}
                  value={draft.expected_head}
                  onChange={(v) => set('expected_head', v)}
                />
                <Field label={t('fields.method')} required>
                  <Select
                    value={draft.method}
                    onValueChange={(v) => set('method', v)}
                  >
                    <SelectTrigger>
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {(['merge', 'squash', 'rebase'] as const).map((m) => (
                        <SelectItem key={m} value={m}>
                          {t(`methods.${m}`)}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </Field>
              </>
            ) : null}
            <Field
              label={t('fields.acknowledgeIntent')}
              description={t('fields.acknowledgeIntentHint')}
            >
              <Input
                mono
                value={draft.ack}
                onChange={(e) => set('ack', e.target.value)}
              />
            </Field>
            <div className="rounded-md border border-border bg-muted/30 p-2">
              <p className="text-caption text-muted-foreground">
                {t('fields.operationId')}
              </p>
              <code className="break-all font-mono text-caption">
                {operationId}
              </code>
              <p className="text-caption text-muted-foreground">
                {t('fields.operationIdHint')}
              </p>
            </div>
            <AuthorityNote action={effect} />
          </form>
        ) : null}

        {outcome ? (
          <OutcomePanel
            outcome={outcome}
            onOpenIntent={onOpenIntent}
            onReconcile={(id) => reconcile.mutate(id)}
            reconciling={reconcile.isPending}
            canReconcile={can(
              actionAuthority(reconcileAction(effect)).permission,
            )}
          />
        ) : null}
        {reconcileFailure ? (
          <OutcomePanel
            outcome={reconcileFailure}
            onOpenIntent={onOpenIntent}
            canReconcile={false}
          />
        ) : null}

        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            {t('actions.close')}
          </Button>
          {!locked ? (
            <Button
              type="submit"
              form="gitpublish-publish"
              variant="primary"
              disabled={!valid || request.isPending}
            >
              {t(`publish.${effect}.submit`)}
            </Button>
          ) : null}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function ShaField({
  label,
  hint,
  value,
  onChange,
}: {
  label: ReactNode
  hint?: ReactNode
  value: string
  onChange: (value: string) => void
}) {
  return (
    <Field label={label} description={hint} required>
      <Input
        mono
        value={value}
        spellCheck={false}
        autoComplete="off"
        onChange={(e) => onChange(e.target.value.trim())}
      />
    </Field>
  )
}

function BaseField({
  label,
  bases,
  value,
  onChange,
}: {
  label: ReactNode
  bases: string[]
  value: string
  onChange: (value: string) => void
}) {
  return (
    <Field label={label} required>
      <Select value={value} onValueChange={onChange}>
        <SelectTrigger>
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {bases.map((b) => (
            <SelectItem key={b} value={b}>
              {b}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    </Field>
  )
}

/** A failed request as an outcome, except the step-up demand: the hook runs that ceremony
 * and sends the same request once it succeeds. */
function failureOf(error: unknown): PublicationOutcome | null {
  if (!error) return null
  if (error instanceof ApiError && error.isStepUpRequired) return null
  return outcomeOf({ error })
}

function isComplete(effect: PublicationEffect, d: Draft): boolean {
  switch (effect) {
    case 'push':
      return d.ref !== '' && d.commit !== '' && d.tree !== ''
    case 'pull_request':
      return (
        d.head_ref !== '' && d.base !== '' && d.commit !== '' && d.title !== ''
      )
    case 'merge':
      return /^[1-9][0-9]*$/.test(d.number) && d.expected_head !== ''
  }
}

function send(
  effect: PublicationEffect,
  targetId: string,
  d: Draft,
  operationId: string,
  tenant: string | null,
): Promise<PublicationIntent> {
  const request = { tenant }
  switch (effect) {
    case 'push':
      return gitpublishApi.push(
        targetId,
        {
          operation_id: operationId,
          ref: d.ref,
          expected_old: d.expected_old,
          commit: d.commit,
          tree: d.tree,
          acknowledge_intent: d.ack,
        },
        request,
      )
    case 'pull_request':
      return gitpublishApi.openPullRequest(
        targetId,
        {
          operation_id: operationId,
          head_ref: d.head_ref,
          base: d.base,
          commit: d.commit,
          title: d.title,
          body: d.body,
          draft: d.draft === 'true',
          acknowledge_intent: d.ack,
        },
        request,
      )
    case 'merge':
      return gitpublishApi.merge(
        targetId,
        {
          operation_id: operationId,
          number: Number(d.number),
          expected_head: d.expected_head,
          method: d.method as MergeInput['method'],
          acknowledge_intent: d.ack,
        },
        request,
      )
  }
}
