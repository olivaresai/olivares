// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useTranslation } from 'react-i18next'
import { cn } from '@/lib/utils'
import { Mono } from './content-blocks'
import type { Failure } from './errors'

/**
 * One failure, said the way the design system says it: a refusal of ROLE and a
 * "could not look" are calm (`role="status"`), everything the operator can act on is
 * an alert. The kind decides the sentence; the code, status and request id ride
 * beside it so the screen can be matched with the engine's log.
 */
export function FailureNotice({
  failure,
  title,
  className,
}: {
  failure: Failure
  title?: string
  className?: string
}) {
  const { t } = useTranslation('communications')
  const calm =
    failure.kind === 'forbidden' ||
    failure.kind === 'unavailable' ||
    failure.kind === 'aborted' ||
    failure.kind === 'not_found'
  const warning =
    failure.kind === 'ambiguous' ||
    failure.kind === 'unavailable' ||
    failure.kind === 'version_mismatch' ||
    failure.kind === 'conflict' ||
    failure.kind === 'plan_changed'
  return (
    <div
      role={calm ? 'status' : 'alert'}
      data-slot="failure-notice"
      data-failure-kind={failure.kind}
      className={cn(
        'flex flex-col gap-1 rounded-md border px-3 py-2 text-sm',
        warning
          ? 'border-warning-line bg-warning-soft text-warning'
          : calm
            ? 'border-border bg-muted text-muted-foreground'
            : 'border-danger-line bg-danger-soft text-danger',
        className,
      )}
    >
      {title ? <p className="font-medium">{title}</p> : null}
      <p>{t(`failure.${failure.kind}`)}</p>
      <dl className="flex flex-wrap gap-x-3 gap-y-0.5 text-xs">
        {failure.code ? (
          <div className="flex gap-1">
            <dt>{t('failure.code')}</dt>
            <dd>
              <Mono>{failure.code}</Mono>
            </dd>
          </div>
        ) : null}
        {failure.status ? (
          <div className="flex gap-1">
            <dt>{t('failure.status')}</dt>
            <dd>
              <Mono>{failure.status}</Mono>
            </dd>
          </div>
        ) : null}
        {failure.requestId ? (
          <div className="flex gap-1">
            <dt>{t('failure.requestId')}</dt>
            <dd>
              <Mono>{failure.requestId}</Mono>
            </dd>
          </div>
        ) : null}
        {failure.message && failure.kind !== 'ambiguous' ? (
          <div className="flex gap-1">
            <dt>{t('failure.message')}</dt>
            <dd className="break-words">{failure.message}</dd>
          </div>
        ) : null}
      </dl>
    </div>
  )
}
