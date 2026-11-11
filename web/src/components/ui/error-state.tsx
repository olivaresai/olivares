// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { Check, CircleAlert, Copy, Lock, RefreshCw } from 'lucide-react'
import type { HTMLAttributes, ReactNode } from 'react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ApiError, NetworkError } from '@/lib/api/errors'
import { cn } from '@/lib/utils'
import { Button } from './button'
import { CopyStatus } from './copy-button'
import {
  STATE_ACTIONS,
  STATE_ICON,
  STATE_PAGE,
  STATE_PANEL,
  STATE_SENTENCE,
  STATE_TITLE,
  type StateSize,
} from './empty-state'
import { useCopy } from './use-copy'

/**
 * The cause of a failed read as text for support: the status, the code and the message the
 * engine answered and the request id it logged, or the transport's own sentence. Never the
 * response's details, body or headers, and never a crash's own words: those may hold what is
 * not the person's to paste.
 */
export function errorDetail(error: unknown): string | undefined {
  if (error instanceof ApiError) {
    const line = `${error.status} ${error.code}: ${error.message}`
    return error.requestId ? `${line}\nrequest_id=${error.requestId}` : line
  }
  if (error instanceof NetworkError) return `${error.name}: ${error.message}`
  return undefined
}

/**
 * ErrorState — the GENUINE-failure state for a panel that could not load: a
 * network drop, a 5xx, a timeout. A state page (direction A2): the large icon in the
 * failure color, a title naming what could not load, one sentence, `Retry`, and the
 * request id and «Copy detail» last, in the quiet cause line. Reserve this for real errors the operator can act on —
 * an empty list is an EmptyState, and a 403 / insufficient permission is a
 * ForbiddenState (which must NOT look like an error). Title/description default to
 * localized copy; callers pass context-specific (already-translated) overrides.
 */
export interface ErrorStateProps extends Omit<
  HTMLAttributes<HTMLDivElement>,
  'title'
> {
  /** Override the default lucide CircleAlert. Rendered in the failure color. */
  icon?: ReactNode
  title?: ReactNode
  description?: ReactNode
  /** Use a page-heading title for a full-view failure, not a panel error. */
  heading?: boolean
  /** `panel` inside a side panel, a sheet, a card or a table body. */
  size?: StateSize
  /** When provided, renders a "Retry" button wired to this callback. */
  retry?: () => void
  /** When provided, renders a "Go to Overview" button that calls this callback. */
  goToNow?: () => void
  /** A short correlation id the operator can copy for support. Never the raw error. */
  errorId?: string
  /**
   * El `X-Request-ID` de la respuesta que falló, si la trajo.
   *
   * ⛔ POR QUÉ SE ENSEÑA. El cliente lo captura en CADA error (`lib/api/client.ts`) y hasta hoy
   * no lo leía nadie: el operador veía un genérico y la línea del log del motor existía, pero no
   * había forma de casar las dos desde la pantalla. Es el MISMO `request_id` que sale en el log
   * de peticiones del motor, así que enseñarlo hace casable la pantalla con el servidor.
   *
   * Opcional a propósito: **si el error no trae id, no se pinta un hueco**. Un campo vacío que
   * dice «id: —» es peor que no decir nada.
   */
  requestId?: string
  /** The cause as text (`errorDetail`). Copied by «Copy detail», never printed unless the
   * clipboard refuses it; no act without it. */
  detail?: string
}

export function ErrorState({
  icon,
  title,
  description,
  heading = false,
  size,
  retry,
  goToNow,
  errorId,
  requestId,
  detail,
  className,
  ...props
}: ErrorStateProps) {
  const { t } = useTranslation(['errors', 'common'])
  const [copied, setCopied] = useState(false)
  const detailCopy = useCopy()

  const copyErrorId = async () => {
    if (!errorId) return
    try {
      await navigator.clipboard.writeText(errorId)
      setCopied(true)
      window.setTimeout(() => setCopied(false), 2_000)
    } catch {
      // If the clipboard API is unavailable, the operator can still select the id.
    }
  }

  const Title = heading ? 'h1' : 'p'
  const panel = size === 'panel'
  return (
    <div
      role="alert"
      className={cn(STATE_PAGE, panel && STATE_PANEL.page, className)}
      {...props}
    >
      <span
        data-slot="state-icon"
        aria-hidden="true"
        className={cn(STATE_ICON, 'text-bad', panel && STATE_PANEL.icon)}
      >
        {icon ?? <CircleAlert />}
      </span>
      <Title className={cn(STATE_TITLE, panel && STATE_PANEL.title)}>
        {title ?? t('errors:serverError.title')}
      </Title>
      <p className={STATE_SENTENCE}>
        {description ?? t('errors:serverError.description')}
      </p>
      {retry || goToNow ? (
        <div className={STATE_ACTIONS}>
          {retry ? (
            <Button variant="secondary" onClick={retry}>
              <RefreshCw aria-hidden="true" />
              {t('common:actions.retry')}
            </Button>
          ) : null}
          {goToNow ? (
            <Button variant="ghost" onClick={goToNow}>
              {t('errors:boundary.goToNow')}
            </Button>
          ) : null}
        </div>
      ) : null}
      {requestId || errorId || detail ? (
        <div
          data-slot="state-cause"
          className="mt-2 flex flex-col items-center gap-1 text-caption text-text-3"
        >
          {requestId ? (
            <p className="m-0">
              {t('errors:requestId')}:{' '}
              <span className="font-mono text-mono-s text-text-2 select-all">
                {requestId}
              </span>
            </p>
          ) : null}
          {errorId ? (
            <div className="flex items-center gap-2">
              <span>
                {t('errors:boundary.errorId')}:{' '}
                <span className="font-mono text-mono-s text-text-2 select-all">
                  {errorId}
                </span>
              </span>
              <Button
                variant="ghost"
                size="icon-sm"
                aria-label={t('errors:boundary.copyErrorId')}
                onClick={() => void copyErrorId()}
              >
                {copied ? (
                  <Check aria-hidden="true" />
                ) : (
                  <Copy aria-hidden="true" />
                )}
              </Button>
            </div>
          ) : null}
          {detail ? (
            <p className="m-0">
              <Button
                variant="link"
                className="h-auto text-caption"
                onClick={() => void detailCopy.copy(detail)}
              >
                {t('common:ui.copy.detail')}
              </Button>{' '}
              <CopyStatus
                state={detailCopy.state}
                copied={t('common:ui.copy.copied')}
                failed={t('common:ui.copy.failed')}
                showFailure
              />
            </p>
          ) : null}
          {detail && detailCopy.state === 'failed' ? (
            <p className="m-0 max-w-[42ch] font-mono text-mono-s [overflow-wrap:anywhere] whitespace-pre-line text-text-2 select-all">
              {detail}
            </p>
          ) : null}
        </div>
      ) : null}
    </div>
  )
}

/**
 * ForbiddenState — the 403 / insufficient-permission / paywall view. This is
 * deliberately CALM and NEUTRAL (muted lock, never red): a free user or an
 * operator without the right role must read this as "you are not authorized to
 * see this", not as a broken page. Per the product's paywall/permission
 * empty-state pattern, never surface a genuine-error treatment here. Pass
 * `children` to attach an upgrade / request-access CTA.
 */
export interface ForbiddenStateProps extends Omit<
  HTMLAttributes<HTMLDivElement>,
  'title'
> {
  /** Override the default lucide Lock. Rendered in the third tone, never danger. */
  icon?: ReactNode
  title?: ReactNode
  description?: ReactNode
  /** `panel` inside a side panel, a sheet, a card or a table body. */
  size?: StateSize
}

export function ForbiddenState({
  icon,
  title,
  description,
  size,
  className,
  children,
  ...props
}: ForbiddenStateProps) {
  const { t } = useTranslation('errors')
  const panel = size === 'panel'
  return (
    <div
      // role="status" (not "alert" — a permission boundary is calm, not a failure)
      // so it is announced when it replaces a spinner, matching EmptyState.
      role="status"
      className={cn(STATE_PAGE, panel && STATE_PANEL.page, className)}
      {...props}
    >
      <span
        data-slot="state-icon"
        aria-hidden="true"
        className={cn(STATE_ICON, 'text-text-3', panel && STATE_PANEL.icon)}
      >
        {icon ?? <Lock />}
      </span>
      <p className={cn(STATE_TITLE, panel && STATE_PANEL.title)}>
        {title ?? t('forbidden.title')}
      </p>
      <p className={STATE_SENTENCE}>
        {description ?? t('forbidden.description')}
      </p>
      {children ? <div className={STATE_ACTIONS}>{children}</div> : null}
    </div>
  )
}
