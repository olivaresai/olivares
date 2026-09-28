// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// One publication intent: what was requested, what the host was last observed to hold, and
// whether the host acknowledged our request — three records the module keeps apart, shown
// apart — with every observation appended to it and the two follow-ups it may offer.
import { useQuery } from '@tanstack/react-query'
import { useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { AsyncSection } from '@/features/_intel'
import { Button } from '@/components/ui/button'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import { Field } from '@/components/ui/field'
import { KvList, KvRow } from '@/components/ui/kv'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { Textarea } from '@/components/ui/textarea'
import { useAuth } from '@/lib/auth/context'
import { ApiError } from '@/lib/api/errors'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { gitpublishApi, gitpublishKeys } from './api'
import {
  intentActions,
  outcomeOf,
  reconcileAction,
  refusalKey,
  type PublicationOutcome,
} from './model'
import { AuthorityNote, IntentStateBadge, OutcomePanel } from './parts'
import type { PublicationIntent } from './types'
import './i18n'

export function IntentSheet({
  intentId,
  onClose,
  onOpenIntent,
}: {
  intentId: string
  onClose: () => void
  onOpenIntent: (id: string) => void
}) {
  const { t } = useTranslation('gitpublish')
  const { activeTenant } = useAuth()
  const intent = useQuery({
    queryKey: gitpublishKeys.intent(activeTenant, intentId),
    queryFn: () => gitpublishApi.intent(intentId),
  })
  const observations = useQuery({
    queryKey: gitpublishKeys.observations(activeTenant, intentId),
    queryFn: () => gitpublishApi.observations(intentId),
  })

  return (
    <Sheet open onOpenChange={(open) => (open ? undefined : onClose())}>
      <SheetContent className="overflow-y-auto sm:max-w-2xl">
        <SheetHeader>
          <SheetTitle>{t('intent.title', { id: intentId })}</SheetTitle>
          <SheetDescription>{t('intent.description')}</SheetDescription>
        </SheetHeader>
        <div className="space-y-4 px-4 pb-4">
          <AsyncSection query={intent}>
            {(data) => (
              <IntentBody
                intent={data}
                onOpenIntent={onOpenIntent}
                observations={
                  <AsyncSection query={observations} skeletonHeight={80}>
                    {(list) =>
                      list.items.length === 0 ? (
                        <p className="text-body text-muted-foreground">
                          {t('intent.observationsEmpty')}
                        </p>
                      ) : (
                        <table className="w-full text-left text-caption">
                          <thead className="text-muted-foreground">
                            <tr>
                              <th className="py-1 pr-2 font-medium">
                                {t('intent.obs.at')}
                              </th>
                              <th className="py-1 pr-2 font-medium">
                                {t('intent.obs.attempt')}
                              </th>
                              <th className="py-1 pr-2 font-medium">
                                {t('intent.obs.source')}
                              </th>
                              <th className="py-1 pr-2 font-medium">
                                {t('intent.obs.result')}
                              </th>
                              <th className="py-1 font-medium">
                                {t('intent.obs.host')}
                              </th>
                            </tr>
                          </thead>
                          <tbody className="divide-y divide-border">
                            {list.items.map((o, i) => (
                              <tr key={`${o.at}-${i}`}>
                                <td className="py-1 pr-2 font-mono">{o.at}</td>
                                <td className="py-1 pr-2 tabular-nums">
                                  {o.attempt}
                                </td>
                                <td className="py-1 pr-2">{o.source}</td>
                                <td className="py-1 pr-2 font-mono">
                                  {o.result}
                                </td>
                                <td className="py-1 font-mono break-all">
                                  {[
                                    o.host_object,
                                    o.status ? String(o.status) : '',
                                    o.request_id,
                                  ]
                                    .filter(Boolean)
                                    .join(' · ')}
                                </td>
                              </tr>
                            ))}
                          </tbody>
                        </table>
                      )
                    }
                  </AsyncSection>
                }
              />
            )}
          </AsyncSection>
        </div>
      </SheetContent>
    </Sheet>
  )
}

function IntentBody({
  intent,
  observations,
  onOpenIntent,
}: {
  intent: PublicationIntent
  observations: ReactNode
  onOpenIntent: (id: string) => void
}) {
  const { t } = useTranslation('gitpublish')
  const { activeTenant, can } = useAuth()
  const actions = intentActions(intent, can)
  const [abandoning, setAbandoning] = useState(false)
  const [reason, setReason] = useState('')
  const [answer, setAnswer] = useState<PublicationOutcome | null>(null)

  const invalidateKeys = [
    gitpublishKeys.intent(activeTenant, intent.id),
    gitpublishKeys.observations(activeTenant, intent.id),
    gitpublishKeys.intents(activeTenant, intent.target_id),
  ]
  const reconcile = usePrivilegedMutation<void, PublicationIntent>({
    mutationFn: () =>
      gitpublishApi.reconcile(intent.id, { tenant: activeTenant }),
    invalidateKeys,
    successMessage: (next) =>
      t('outcome.toast', { state: t(`states.${next.state}`) }),
    onDone: (next) => setAnswer(outcomeOf({ data: next })),
    onError: () => true,
  })
  const abandon = usePrivilegedMutation<string, PublicationIntent>({
    mutationFn: (why) =>
      gitpublishApi.abandon(intent.id, why, { tenant: activeTenant }),
    invalidateKeys,
    successMessage: t('intent.abandoned'),
    onDone: () => {
      setAbandoning(false)
      setReason('')
    },
    onError: () => true,
  })
  const failure = [reconcile.error, abandon.error].find(
    (e) => e && !(e instanceof ApiError && e.isStepUpRequired),
  )

  const r = intent.requested
  const o = intent.observed
  const a = intent.acknowledged
  return (
    <div className="space-y-4">
      <KvList>
        <KvRow label={t('intent.state')}>
          <IntentStateBadge state={intent.state} />
        </KvRow>
        <KvRow label={t('intent.effect')}>
          {t(`effects.${intent.effect}`)}
        </KvRow>
        <KvRow label={t('intent.receipt')}>
          {t(`receipts.${intent.receipt}`)}
        </KvRow>
        <KvRow label={t('intent.operationId')} mono>
          {intent.operation_id}
        </KvRow>
        <KvRow label={t('intent.attempt')} mono>
          {intent.attempt}
        </KvRow>
        <KvRow label={t('intent.targetVersion')} mono>
          {intent.target_version}
        </KvRow>
        {intent.reason ? (
          <KvRow label={t('intent.reason')} align="start">
            <span className="block">{t(refusalKey(intent.reason))}</span>
            <code className="font-mono text-caption">{intent.reason}</code>
          </KvRow>
        ) : null}
        {intent.authorized_by ? (
          <KvRow label={t('intent.authorizedBy')} mono>
            {intent.authorized_by}
          </KvRow>
        ) : null}
        {intent.dispatch_deadline ? (
          <KvRow label={t('intent.dispatchDeadline')} mono>
            {intent.dispatch_deadline}
          </KvRow>
        ) : null}
        {intent.release_failure ? (
          <KvRow label={t('intent.releaseFailure')} mono>
            {intent.release_failure}
          </KvRow>
        ) : null}
      </KvList>

      {intent.state === 'uncertain' ? (
        <p className="rounded-md border border-warning-line bg-warning-soft p-2 text-body text-warning">
          {t('outcome.uncertain.body')}
        </p>
      ) : null}
      {intent.state === 'abandoned' ? (
        <p className="rounded-md border border-border bg-muted/30 p-2 text-body text-muted-foreground">
          {t('intent.abandonedNote')}
        </p>
      ) : null}

      <section className="space-y-1">
        <h3 className="text-body font-medium text-foreground">
          {t('intent.requested')}
        </h3>
        <KvList>
          <Row label={t('fields.ref')} value={r.ref} />
          <Row label={t('fields.expectedOld')} value={r.expected_old} />
          <Row label={t('fields.headRef')} value={r.head_ref} />
          <Row label={t('fields.base')} value={r.base} />
          <Row label={t('fields.commit')} value={r.commit} />
          <Row label={t('fields.tree')} value={r.tree} />
          <Row label={t('fields.title')} value={r.title} plain />
          <Row label={t('fields.number')} value={r.number} />
          <Row label={t('fields.expectedHead')} value={r.expected_head} />
          <Row
            label={t('fields.method')}
            value={r.method ? t(`methods.${r.method}`) : undefined}
            plain
          />
        </KvList>
      </section>

      <section className="space-y-1">
        <h3 className="text-body font-medium text-foreground">
          {t('intent.observed')}
        </h3>
        {o.source || o.at ? (
          <KvList>
            <KvRow label={t('intent.present')}>
              {o.present ? t('intent.yes') : t('intent.no')}
            </KvRow>
            <Row label={t('intent.sha')} value={o.sha} />
            <Row label={t('intent.headSha')} value={o.head_sha} />
            <Row label={t('fields.number')} value={o.number} />
            <KvRow label={t('intent.merged')}>
              {o.merged ? t('intent.yes') : t('intent.no')}
            </KvRow>
            <Row label={t('intent.mergeCommit')} value={o.merge_commit_sha} />
            <Row label={t('intent.mergeTree')} value={o.merge_tree} />
            {intent.content_match !== undefined ? (
              <KvRow label={t('intent.contentMatch')}>
                {intent.content_match ? t('intent.yes') : t('intent.no')}
              </KvRow>
            ) : null}
            <Row label={t('intent.source')} value={o.source} plain />
            <Row label={t('intent.at')} value={o.at} />
          </KvList>
        ) : (
          <p className="text-body text-muted-foreground">
            {t('intent.notObserved')}
          </p>
        )}
      </section>

      <section className="space-y-1">
        <h3 className="text-body font-medium text-foreground">
          {t('intent.acknowledged')}
        </h3>
        <KvList>
          <KvRow label={t('intent.acknowledgedValue')}>
            {a.acknowledged ? t('intent.yes') : t('intent.no')}
          </KvRow>
          <Row label={t('intent.status')} value={a.status} />
          <Row label={t('intent.requestId')} value={a.request_id} />
          <Row label={t('intent.at')} value={a.at} />
        </KvList>
        <p className="text-caption text-muted-foreground">
          {t('intent.acknowledgedHint')}
        </p>
      </section>

      <section className="space-y-2">
        <h3 className="text-body font-medium text-foreground">
          {t('intent.observations')}
        </h3>
        {observations}
      </section>

      {actions.reconcile || actions.abandon ? (
        <section className="space-y-2 border-t border-border pt-3">
          <div className="flex flex-wrap gap-2">
            {actions.reconcile ? (
              <Button
                size="sm"
                variant="primary"
                disabled={reconcile.isPending}
                onClick={() => reconcile.mutate()}
              >
                {t('actions.reconcile')}
              </Button>
            ) : null}
            {actions.abandon ? (
              <Button
                size="sm"
                variant="outline"
                onClick={() => setAbandoning(true)}
              >
                {t('actions.abandon')}
              </Button>
            ) : null}
          </div>
          {actions.reconcile ? (
            <AuthorityNote action={reconcileAction(intent.effect)} />
          ) : null}
          {actions.abandon ? <AuthorityNote action="abandon" /> : null}
        </section>
      ) : null}

      {answer ? (
        <OutcomePanel
          outcome={answer}
          onOpenIntent={onOpenIntent}
          canReconcile={false}
        />
      ) : null}
      {failure ? (
        <OutcomePanel
          outcome={outcomeOf({ error: failure })}
          onOpenIntent={onOpenIntent}
          canReconcile={false}
        />
      ) : null}

      <ConfirmDialog
        open={abandoning}
        onOpenChange={setAbandoning}
        tone="danger"
        title={t('intent.abandonTitle', { id: intent.id })}
        description={t('intent.abandonDescription')}
        confirmLabel={t('actions.abandon')}
        pending={abandon.isPending}
        onConfirm={() => abandon.mutate(reason)}
      >
        <Field label={t('intent.abandonReason')}>
          <Textarea
            value={reason}
            maxLength={256}
            rows={3}
            onChange={(e) => setReason(e.target.value)}
          />
        </Field>
      </ConfirmDialog>
    </div>
  )
}

function Row({
  label,
  value,
  plain = false,
}: {
  label: string
  value: string | number | undefined
  plain?: boolean
}) {
  if (value === undefined || value === '' || value === 0) return null
  return (
    <KvRow label={label} mono={!plain}>
      <span className="break-all">{value}</span>
    </KvRow>
  )
}
