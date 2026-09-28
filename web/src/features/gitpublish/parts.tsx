// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Small pieces the Git publication screens share: the state badge, the authority line an
// action carries, and the panel that says what an answer means.
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'
import {
  actionAuthority,
  refusalKey,
  stateTone,
  type GovernedAction,
  type PublicationOutcome,
} from './model'
import type { IntentState } from './types'
import './i18n'

export function IntentStateBadge({ state }: { state: IntentState }) {
  const { t } = useTranslation('gitpublish')
  return <Badge variant={stateTone(state)}>{t(`states.${state}`)}</Badge>
}

/** The authority the engine mounts an action with, stated before the operator acts. */
export function AuthorityNote({
  action,
  className,
}: {
  action: GovernedAction
  className?: string
}) {
  const { t } = useTranslation('gitpublish')
  const { permission, aal3 } = actionAuthority(action)
  return (
    <p className={cn('text-caption text-muted-foreground', className)}>
      {aal3
        ? t('authority.requiresAal3', { permission })
        : t('authority.requires', { permission })}
    </p>
  )
}

/**
 * What an answer means, in the operator's terms. Every branch that follows a dispatch
 * offers only reconcile or a look at the intent — never a resend.
 */
export function OutcomePanel({
  outcome,
  onOpenIntent,
  onReconcile,
  reconciling = false,
  canReconcile,
}: {
  outcome: PublicationOutcome
  onOpenIntent: (id: string) => void
  onReconcile?: (id: string) => void
  reconciling?: boolean
  canReconcile: boolean
}) {
  const { t } = useTranslation('gitpublish')
  switch (outcome.kind) {
    case 'settled':
      return (
        <section
          role="status"
          className="space-y-2 rounded-md border border-success-line bg-success-soft p-3 text-body"
        >
          <div className="flex flex-wrap items-center gap-2">
            <h3 className="font-medium text-success">
              {t('outcome.settled.title')}
            </h3>
            <IntentStateBadge state={outcome.intent.state} />
          </div>
          <p className="text-foreground">
            {t(`receipts.${outcome.intent.receipt}`)}
          </p>
          <Button
            size="sm"
            variant="outline"
            onClick={() => onOpenIntent(outcome.intent.id)}
          >
            {t('intents.open', { id: outcome.intent.id })}
          </Button>
        </section>
      )
    case 'uncertain':
      return (
        <section
          role="status"
          className="space-y-2 rounded-md border border-warning-line bg-warning-soft p-3 text-body"
        >
          <div className="flex flex-wrap items-center gap-2">
            <h3 className="font-medium text-warning">
              {t('outcome.uncertain.title')}
            </h3>
            <IntentStateBadge state={outcome.intent.state} />
          </div>
          <p className="text-foreground">{t('outcome.uncertain.body')}</p>
          <div className="flex flex-wrap gap-2">
            {canReconcile && onReconcile ? (
              <Button
                size="sm"
                variant="primary"
                disabled={reconciling}
                onClick={() => onReconcile(outcome.intent.id)}
              >
                {t('actions.reconcile')}
              </Button>
            ) : null}
            <Button
              size="sm"
              variant="outline"
              onClick={() => onOpenIntent(outcome.intent.id)}
            >
              {t('intents.open', { id: outcome.intent.id })}
            </Button>
          </div>
        </section>
      )
    case 'rejected':
      return (
        <section
          role="alert"
          className="space-y-2 rounded-md border border-danger-line bg-danger-soft p-3 text-body"
        >
          <h3 className="font-medium text-danger">
            {t('outcome.rejected.title')}
          </h3>
          <p className="text-foreground">
            {outcome.intent.reason
              ? t(refusalKey(outcome.intent.reason))
              : t('outcome.rejected.body')}
          </p>
          {outcome.intent.reason ? (
            <code className="break-all font-mono text-caption">
              {outcome.intent.reason}
            </code>
          ) : null}
          <div>
            <Button
              size="sm"
              variant="outline"
              onClick={() => onOpenIntent(outcome.intent.id)}
            >
              {t('intents.open', { id: outcome.intent.id })}
            </Button>
          </div>
        </section>
      )
    case 'refused':
      return (
        <section
          role="alert"
          className="space-y-2 rounded-md border border-danger-line bg-danger-soft p-3 text-body"
        >
          <h3 className="font-medium text-danger">
            {t('outcome.refused.title')}
          </h3>
          <p className="text-foreground">{t(refusalKey(outcome.code))}</p>
          <p className="text-caption text-muted-foreground">
            {t('outcome.refused.code')}{' '}
            <code className="font-mono">{outcome.code}</code>
          </p>
          {outcome.intentId ? (
            <Button
              size="sm"
              variant="outline"
              onClick={() => onOpenIntent(outcome.intentId!)}
            >
              {t('intents.open', { id: outcome.intentId })}
            </Button>
          ) : null}
        </section>
      )
    case 'unknown':
      return (
        <section
          role="alert"
          className="space-y-2 rounded-md border border-warning-line bg-warning-soft p-3 text-body"
        >
          <h3 className="font-medium text-warning">
            {t('outcome.unknown.title')}
          </h3>
          <p className="text-foreground">{t('outcome.unknown.body')}</p>
        </section>
      )
  }
}
