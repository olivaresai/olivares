// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { RefreshCw, Shield, TriangleAlert } from 'lucide-react'
import type { HTMLAttributes, ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { cn } from '@/lib/utils'
import { Button } from './button'
import { StateBanner } from './state-block'

/**
 * ErrorState — the GENUINE-failure state for a panel that could not load: a
 * network drop, a 5xx, a timeout. A banner on the failure fill (the state set's Error
 * look, shared with StateBlock) with the request id under it and a `Retry` affordance. Reserve this for real errors the operator can act on —
 * an empty list is an EmptyState, and a 403 / insufficient permission is a
 * ForbiddenState (which must NOT look like an error). Title/description default to
 * localized copy; callers pass context-specific (already-translated) overrides.
 */
export interface ErrorStateProps extends Omit<
  HTMLAttributes<HTMLDivElement>,
  'title'
> {
  /** Override the default lucide TriangleAlert. Rendered in the failure color. */
  icon?: ReactNode
  title?: ReactNode
  description?: ReactNode
  /** When provided, renders a "Retry" button wired to this callback. */
  retry?: () => void
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
}

export function ErrorState({
  icon,
  title,
  description,
  retry,
  requestId,
  className,
  ...props
}: ErrorStateProps) {
  const { t } = useTranslation(['errors', 'common'])
  return (
    <div
      role="alert"
      className={cn(
        'mx-auto flex w-full max-w-md flex-col gap-2.5 px-4 py-8',
        className,
      )}
      {...props}
    >
      <StateBanner tone="bad" icon={icon ?? <TriangleAlert />}>
        <p className="m-0 font-semibold">
          {title ?? t('errors:serverError.title')}
        </p>
        <p className="m-0 text-text-2">
          {description ?? t('errors:serverError.description')}
        </p>
      </StateBanner>
      {requestId ? (
        <p className="m-0 text-caption text-text-3">
          {t('errors:requestId')}:{' '}
          <span className="font-mono text-mono-s text-text-2 select-all">
            {requestId}
          </span>
        </p>
      ) : null}
      {retry ? (
        <div>
          <Button variant="secondary" size="sm" onClick={retry}>
            <RefreshCw aria-hidden="true" />
            {t('common:actions.retry')}
          </Button>
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
  /** Override the default lucide Shield. Rendered in the third tone, never danger. */
  icon?: ReactNode
  title?: ReactNode
  description?: ReactNode
}

export function ForbiddenState({
  icon,
  title,
  description,
  className,
  children,
  ...props
}: ForbiddenStateProps) {
  const { t } = useTranslation('errors')
  return (
    <div
      // role="status" (not "alert" — a permission boundary is calm, not a failure)
      // so it is announced when it replaces a spinner, matching EmptyState.
      role="status"
      className={cn(
        'flex flex-col items-center justify-center gap-2.5 px-6 py-12 text-center',
        className,
      )}
      {...props}
    >
      <span
        data-slot="state-icon"
        aria-hidden="true"
        className="flex text-text-3 [&_svg]:size-5 [&_svg]:shrink-0"
      >
        {icon ?? <Shield />}
      </span>
      <div className="flex flex-col items-center gap-1.5">
        <p className="m-0 text-heading text-text">
          {title ?? t('forbidden.title')}
        </p>
        <p className="m-0 max-w-sm text-caption text-text-2">
          {description ?? t('forbidden.description')}
        </p>
      </div>
      {children ? <div className="mt-1">{children}</div> : null}
    </div>
  )
}
