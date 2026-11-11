// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { CircleAlert, History, Lock, RefreshCw, X } from 'lucide-react'
import type { HTMLAttributes, ReactNode } from 'react'
import { useEffect, useState } from 'react'
import { Trans, useTranslation } from 'react-i18next'
import { cn } from '@/lib/utils'
import { Button } from './button'
import { CodeLine } from './code-line'
import { DisabledReason, type DisabledReasonProps } from './disabled-reason'
import {
  STATE_ACTIONS,
  STATE_ICON,
  STATE_PAGE,
  STATE_PANEL,
  STATE_SENTENCE,
  STATE_TITLE,
  StateMark,
  type StateSize,
} from './empty-state'
import { Notice } from './notice'
import { Skeleton } from './skeleton'
import { RunningRing } from './status-glyph'

/**
 * StateBlock — the eight states every list and every action shows, one component for all
 * areas, each with the content the design's state set fixes for it:
 *
 * | state             | shows                                                               |
 * |-------------------|---------------------------------------------------------------------|
 * | `loading`         | what is loading and skeleton rows; `aria-busy`; never a count        |
 * | `empty`           | complete with zero rows: title, one sentence, the next action, the   |
 * |                   | CLI line                                                             |
 * | `filtered`        | the filters, how many rows they hide, Clear filters                  |
 * | `error`           | the list could not be read (not "empty"): request, time, request id, |
 * |                   | Try again, Open diagnostics                                          |
 * | `stale`           | the last good time, the failed refresh time, Retry; the rows stay at |
 * |                   | full contrast behind a dashed warning edge                           |
 * | `disabled`        | the control, unable to act, with its visible reason (DisabledReason) |
 * | `no-access`       | refused, calm, and nothing about the object                          |
 * | `unknown-outcome` | the same request is being checked; the retry stays disabled with its |
 * |                   | reason until the engine confirms nothing started                     |
 *
 * The props are a union on `state`, so each state asks for exactly its content. A live
 * region announces a state once; timers (the elapsed check time) sit outside it.
 */
type Base = Omit<HTMLAttributes<HTMLDivElement>, 'title' | 'children'>

export type StateBlockProps = Base &
  (
    | { state: 'loading'; label?: string; rows?: number }
    | {
        state: 'empty'
        title: ReactNode
        description: NonNullable<ReactNode>
        /** The one next action, usually the screen's primary button. */
        action: ReactNode
        /** The same action as a terminal command; absent only for a recorded parity gap. */
        command?: string
      }
    | {
        state: 'filtered'
        title: ReactNode
        /** The active filters, as the operator set them. */
        filters: ReactNode
        /** How many rows the filters hide. */
        hiddenCount: number
        onClear: () => void
        /** `panel` inside a side panel, a sheet, a card or a table body. */
        size?: StateSize
      }
    | {
        state: 'error'
        title?: ReactNode
        description?: ReactNode
        /** The operation that failed: "GET /v1/sessions · timeout after 10 s". */
        request?: string
        /** The response's request id, the same one the engine logs. */
        requestId?: string
        /** When it failed, as the operator reads times. */
        time?: string
        onRetry: () => void
        onOpenDiagnostics?: () => void
      }
    | {
        state: 'stale'
        lastGood: string
        failedAt: string
        onRetry: () => void
        /** The rows last read. They stay at full contrast. */
        children: ReactNode
      }
    | {
        state: 'disabled'
        reason: string
        action?: ReactNode
        mode?: DisabledReasonProps['mode']
        children: DisabledReasonProps['children']
      }
    | {
        state: 'no-access'
        /** Names the kind of object at most ("You cannot open this workspace"), never the object. */
        title?: ReactNode
        description?: ReactNode
        action?: ReactNode
      }
    | {
        state: 'unknown-outcome'
        title?: ReactNode
        description?: ReactNode
        /** The operation id being checked; the retry reuses it. */
        requestId: string
        /** How long the check has run: "8 s". */
        elapsed: string
        /** True only once the engine confirms that nothing started. */
        confirmedNothingStarted: boolean
        retryLabel?: string
        onRetry: () => void
      }
  )

const STACKED = 'flex flex-col gap-2.5 p-4'
const SKELETON_WIDTHS = ['w-55', 'w-45', 'w-65', 'w-35']

function Title({ size, children }: { size?: StateSize; children: ReactNode }) {
  return (
    <p className={cn(STATE_TITLE, size === 'panel' && STATE_PANEL.title)}>
      {children}
    </p>
  )
}

function Sentence({ children }: { children: ReactNode }) {
  return <p className={STATE_SENTENCE}>{children}</p>
}

/** A fast load never flashes a skeleton: its rows show after this long. */
const SKELETON_DELAY_MS = 300

/** Loading: the words for a screen reader at once, so a slow read is heard as one; the box at
 * its final height from the start, its rows hidden until the delay, so nothing shifts when the
 * rows or the data arrive. */
function LoadingBlock({
  state,
  label,
  rows = 4,
  className,
  ...rest
}: Extract<StateBlockProps, { state: 'loading' }>) {
  const { t } = useTranslation('common')
  const [shown, setShown] = useState(false)
  useEffect(() => {
    const timer = setTimeout(() => setShown(true), SKELETON_DELAY_MS)
    return () => clearTimeout(timer)
  }, [])
  return (
    <div
      role="status"
      aria-busy="true"
      data-slot="state-block"
      data-state={state}
      className={cn(STACKED, className)}
      {...rest}
    >
      {/* The rows say it; the words are for a screen reader. */}
      <p className="sr-only">{label ?? t('ui.state.loading')}</p>
      <div
        data-slot="state-block-rows"
        className={cn(
          'overflow-hidden rounded-card bg-surface shadow-card',
          !shown && 'invisible',
        )}
      >
        {Array.from({ length: rows }, (_, i) => (
          <div
            key={i}
            data-slot="state-block-skeleton"
            aria-hidden="true"
            className="flex h-11 items-center gap-3.5 border-t border-line px-4 first:border-t-0"
          >
            <Skeleton className="size-6 shrink-0 rounded-[7px]" />
            <Skeleton
              className={cn(
                'h-2.5 max-w-[60%] rounded-full',
                SKELETON_WIDTHS[i % SKELETON_WIDTHS.length],
              )}
            />
            <Skeleton className="ms-auto h-2.5 w-16 shrink-0 rounded-full" />
          </div>
        ))}
      </div>
    </div>
  )
}

export function StateBlock(props: StateBlockProps) {
  const { t } = useTranslation('common')
  const slot = { 'data-slot': 'state-block', 'data-state': props.state }

  switch (props.state) {
    case 'loading':
      return <LoadingBlock {...props} />

    case 'empty': {
      const {
        state: _s,
        title,
        description,
        action,
        command,
        className,
        ...rest
      } = props
      return (
        <div
          role="status"
          {...slot}
          className={cn(STATE_PAGE, className)}
          {...rest}
        >
          <StateMark />
          <Title>{title}</Title>
          <Sentence>{description}</Sentence>
          {command ? <CodeLine inline command={command} /> : null}
          {/* The one next action is a pill (the button has no pill size yet). */}
          <div className={cn(STATE_ACTIONS, '*:rounded-full')}>{action}</div>
        </div>
      )
    }

    case 'filtered': {
      const {
        state: _s,
        title,
        filters,
        hiddenCount,
        onClear,
        size,
        className,
        ...rest
      } = props
      return (
        <div
          role="status"
          {...slot}
          className={cn(
            STATE_PAGE,
            size === 'panel' && STATE_PANEL.page,
            className,
          )}
          {...rest}
        >
          <Title size={size}>{title}</Title>
          <Sentence>
            <span className="block">{filters}</span>
            <span className="block">
              {t('ui.state.filtered.hidden', { count: hiddenCount })}
            </span>
          </Sentence>
          <div className={STATE_ACTIONS}>
            <Button onClick={onClear}>
              <X aria-hidden="true" />
              {t('ui.state.filtered.clear')}
            </Button>
          </div>
        </div>
      )
    }

    case 'error': {
      const {
        state: _s,
        title,
        description,
        request,
        requestId,
        time,
        onRetry,
        onOpenDiagnostics,
        className,
        ...rest
      } = props
      const idLine = requestId
        ? time
          ? t('ui.state.error.request', { id: requestId, time })
          : t('ui.state.error.requestId', { id: requestId })
        : null
      return (
        <div
          role="alert"
          {...slot}
          className={cn(STATE_PAGE, className)}
          {...rest}
        >
          <span
            data-slot="state-icon"
            aria-hidden="true"
            className={cn(STATE_ICON, 'text-bad')}
          >
            <CircleAlert />
          </span>
          <Title>{title ?? t('ui.state.error.title')}</Title>
          <Sentence>{description ?? t('ui.state.error.description')}</Sentence>
          <div className={STATE_ACTIONS}>
            <Button onClick={onRetry}>
              <RefreshCw aria-hidden="true" />
              {t('ui.state.error.retry')}
            </Button>
            {onOpenDiagnostics ? (
              <Button variant="ghost" onClick={onOpenDiagnostics}>
                {t('ui.state.error.diagnostics')}
              </Button>
            ) : null}
          </div>
          {/* The cause, last and quiet: what failed and the id the engine logs. */}
          {request || idLine ? (
            <div
              data-slot="state-block-request"
              className="mt-2 max-w-[42ch] font-mono text-mono-s [overflow-wrap:anywhere] text-text-3"
            >
              {request ? <span className="block">{request}</span> : null}
              {idLine ? (
                <span className="block select-all">{idLine}</span>
              ) : null}
            </div>
          ) : null}
        </div>
      )
    }

    case 'stale': {
      const {
        state: _s,
        lastGood,
        failedAt,
        onRetry,
        children,
        className,
        ...rest
      } = props
      return (
        <div {...slot} className={cn(STACKED, 'gap-3', className)} {...rest}>
          <Notice tone="warn" icon={<History />} role="status">
            <Trans
              t={t}
              i18nKey="ui.state.stale.message"
              values={{ lastGood, failedAt }}
              components={[<strong key="0" className="font-semibold" />]}
            />{' '}
            <Button
              variant="link"
              onClick={onRetry}
              className="h-auto text-caption"
            >
              {t('ui.state.stale.retry')}
            </Button>
          </Notice>
          <div
            data-slot="state-block-stale-rows"
            className="border-s-2 border-dashed border-warn ps-2.5"
          >
            {children}
          </div>
        </div>
      )
    }

    case 'disabled': {
      const {
        state: _s,
        reason,
        action,
        mode,
        children,
        className,
        ...rest
      } = props
      return (
        <div {...slot} className={cn(STACKED, className)} {...rest}>
          <DisabledReason disabled reason={reason} action={action} mode={mode}>
            {children}
          </DisabledReason>
        </div>
      )
    }

    case 'no-access': {
      const {
        state: _s,
        title,
        description,
        action,
        className,
        ...rest
      } = props
      return (
        <div
          role="status"
          {...slot}
          className={cn(STATE_PAGE, className)}
          {...rest}
        >
          <span
            data-slot="state-icon"
            aria-hidden="true"
            className={cn(STATE_ICON, 'text-text-3')}
          >
            <Lock />
          </span>
          <Title>{title ?? t('ui.state.noAccess.title')}</Title>
          <Sentence>
            {description ?? t('ui.state.noAccess.description')}
          </Sentence>
          {action ? <div className={STATE_ACTIONS}>{action}</div> : null}
        </div>
      )
    }

    case 'unknown-outcome': {
      const {
        state: _s,
        title,
        description,
        requestId,
        elapsed,
        confirmedNothingStarted,
        retryLabel,
        onRetry,
        className,
        ...rest
      } = props
      return (
        <div {...slot} className={cn(STACKED, className)} {...rest}>
          <Notice icon={<RunningRing />} role="status">
            <strong className="font-semibold">
              {title ?? t('ui.state.unknown.title')}
            </strong>{' '}
            {description ?? t('ui.state.unknown.description')}
          </Notice>
          <p className="m-0 font-mono text-mono-s text-text-3">
            {t('ui.state.unknown.checking', { id: requestId, elapsed })}
          </p>
          <DisabledReason
            disabled={!confirmedNothingStarted}
            reason={t('ui.state.unknown.retryReason')}
          >
            <Button size="sm" onClick={onRetry}>
              {retryLabel ?? t('ui.state.unknown.retry')}
            </Button>
          </DisabledReason>
        </div>
      )
    }
  }
}
