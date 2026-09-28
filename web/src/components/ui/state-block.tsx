// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { History, RefreshCw, Shield, TriangleAlert, X } from 'lucide-react'
import type { HTMLAttributes, ReactNode } from 'react'
import { Trans, useTranslation } from 'react-i18next'
import { cn } from '@/lib/utils'
import { Button } from './button'
import { CodeLine } from './code-line'
import { DisabledReason, type DisabledReasonProps } from './disabled-reason'
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

type Tone = 'bad' | 'warn' | 'info'

const BANNER_TONE: Record<Tone, string> = {
  bad: 'border-bad bg-bad-soft',
  warn: 'border-warn bg-warn-soft',
  info: 'border-info bg-info-soft',
}

const ICON_TONE: Record<Tone, string> = {
  bad: 'text-bad',
  warn: 'text-text',
  info: 'text-info',
}

/** The banner of the error, stale and outcome-not-known states (and of ErrorState). */
export function StateBanner({
  tone,
  icon,
  className,
  children,
  ...props
}: HTMLAttributes<HTMLDivElement> & { tone: Tone; icon: ReactNode }) {
  return (
    <div
      data-slot="state-banner"
      data-tone={tone}
      className={cn(
        'flex items-start gap-2.5 rounded-card border px-3 py-2.5 text-start text-caption text-text',
        BANNER_TONE[tone],
        className,
      )}
      {...props}
    >
      <span
        aria-hidden="true"
        className={cn('mt-0.5 flex shrink-0 [&_svg]:size-3.5', ICON_TONE[tone])}
      >
        {icon}
      </span>
      <div className="min-w-0">{children}</div>
    </div>
  )
}

const CENTERED =
  'flex flex-col items-center justify-center gap-2.5 px-4 py-10 text-center'
const STACKED = 'flex flex-col gap-2.5 p-4'
const SKELETON_WIDTHS = ['w-full', 'w-4/5', 'w-[65%]', 'w-[72%]']

/** The empty-list mark: four ledger lines, one of them orange. */
function Ledger() {
  return (
    <span aria-hidden="true" className="mb-1 flex w-16 flex-col gap-[5px]">
      <span className="h-1.5 rounded-[3px] bg-line-strong" />
      <span className="h-1.5 w-4/5 rounded-[3px] bg-accent" />
      <span className="h-1.5 w-[90%] rounded-[3px] bg-line-strong" />
      <span className="h-1.5 w-3/5 rounded-[3px] bg-line-strong" />
    </span>
  )
}

function Title({ children }: { children: ReactNode }) {
  return <p className="m-0 text-heading text-text">{children}</p>
}

function Sentence({ children }: { children: ReactNode }) {
  return (
    <p className="m-0 max-w-[18rem] text-caption text-text-2">{children}</p>
  )
}

export function StateBlock(props: StateBlockProps) {
  const { t } = useTranslation('common')
  const slot = { 'data-slot': 'state-block', 'data-state': props.state }

  switch (props.state) {
    case 'loading': {
      const { state: _s, label, rows = 4, className, ...rest } = props
      return (
        <div
          role="status"
          aria-busy="true"
          {...slot}
          className={cn(STACKED, className)}
          {...rest}
        >
          <p className="m-0 text-caption text-text-2">
            {label ?? t('ui.state.loading')}
          </p>
          {Array.from({ length: rows }, (_, i) => (
            <div
              key={i}
              data-slot="state-block-skeleton"
              aria-hidden="true"
              className="grid grid-cols-[14px_minmax(0,1fr)_50px] items-center gap-2.5"
            >
              <Skeleton className="size-3.5 rounded-full" />
              <Skeleton
                className={cn(
                  'h-3',
                  SKELETON_WIDTHS[i % SKELETON_WIDTHS.length],
                )}
              />
              <Skeleton className="h-3" />
            </div>
          ))}
        </div>
      )
    }

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
          className={cn(CENTERED, className)}
          {...rest}
        >
          <Ledger />
          <Title>{title}</Title>
          <Sentence>{description}</Sentence>
          {command ? <CodeLine inline command={command} /> : null}
          <div className="mt-1 flex flex-wrap items-center justify-center gap-2">
            {action}
          </div>
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
        className,
        ...rest
      } = props
      return (
        <div
          role="status"
          {...slot}
          className={cn(CENTERED, className)}
          {...rest}
        >
          <Title>{title}</Title>
          <Sentence>
            <span className="block">{filters}</span>
            <span className="block">
              {t('ui.state.filtered.hidden', { count: hiddenCount })}
            </span>
          </Sentence>
          <Button size="sm" onClick={onClear} className="mt-1">
            <X aria-hidden="true" />
            {t('ui.state.filtered.clear')}
          </Button>
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
          className={cn(STACKED, className)}
          {...rest}
        >
          <StateBanner tone="bad" icon={<TriangleAlert />}>
            <strong className="font-semibold">
              {title ?? t('ui.state.error.title')}
            </strong>{' '}
            {description ?? t('ui.state.error.description')}
          </StateBanner>
          {request || idLine ? (
            <div
              data-slot="state-block-request"
              className="rounded-card border border-line bg-frame px-3.5 py-2.5 font-mono text-mono-s [overflow-wrap:anywhere] text-text"
            >
              {request ? <span className="block">{request}</span> : null}
              {idLine ? (
                <span className="block text-text-3 select-all">{idLine}</span>
              ) : null}
            </div>
          ) : null}
          <div className="flex flex-wrap items-center gap-2">
            <Button size="sm" onClick={onRetry}>
              <RefreshCw aria-hidden="true" />
              {t('ui.state.error.retry')}
            </Button>
            {onOpenDiagnostics ? (
              <Button size="sm" variant="ghost" onClick={onOpenDiagnostics}>
                {t('ui.state.error.diagnostics')}
              </Button>
            ) : null}
          </div>
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
          <StateBanner tone="warn" icon={<History />} role="status">
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
          </StateBanner>
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
          className={cn(CENTERED, className)}
          {...rest}
        >
          <Shield
            data-slot="state-icon"
            aria-hidden="true"
            className="size-5 text-text-3"
          />
          <Title>{title ?? t('ui.state.noAccess.title')}</Title>
          <Sentence>
            {description ?? t('ui.state.noAccess.description')}
          </Sentence>
          {action ? <div className="mt-1">{action}</div> : null}
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
          <StateBanner tone="info" icon={<RunningRing />} role="status">
            <strong className="font-semibold">
              {title ?? t('ui.state.unknown.title')}
            </strong>{' '}
            {description ?? t('ui.state.unknown.description')}
          </StateBanner>
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
