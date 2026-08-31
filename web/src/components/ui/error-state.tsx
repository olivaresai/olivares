// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { AlertTriangle, ShieldOff } from 'lucide-react'
import type { HTMLAttributes, ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { cn } from '@/lib/utils'
import { Button } from './button'

/**
 * ErrorState — the GENUINE-failure state for a panel that could not load: a
 * network drop, a 5xx, a timeout. Accented with `text-danger` and offers a
 * `Retry` affordance. Reserve this for real errors the operator can act on —
 * an empty list is an EmptyState, and a 403 / insufficient permission is a
 * ForbiddenState (which must NOT look like an error). Title/description default to
 * localized copy; callers pass context-specific (already-translated) overrides.
 */
export interface ErrorStateProps extends Omit<
  HTMLAttributes<HTMLDivElement>,
  'title'
> {
  /** Override the default lucide AlertTriangle. Rendered in danger color. */
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
        'flex flex-col items-center justify-center gap-3 px-6 py-12 text-center',
        className,
      )}
      {...props}
    >
      <div className="flex size-10 items-center justify-center rounded-lg bg-danger-soft text-danger [&_svg]:size-5 [&_svg]:shrink-0">
        {icon ?? <AlertTriangle />}
      </div>
      <div className="flex flex-col items-center gap-1.5">
        <p className="text-sm font-medium text-foreground">
          {title ?? t('errors:serverError.title')}
        </p>
        <p className="max-w-sm text-sm text-muted-foreground">
          {description ?? t('errors:serverError.description')}
        </p>
      </div>
      {requestId ? (
        <p className="text-xs text-muted-foreground">
          {t('errors:requestId')}:{' '}
          <span className="font-mono select-all">{requestId}</span>
        </p>
      ) : null}
      {retry ? (
        <Button variant="secondary" size="sm" onClick={retry} className="mt-1">
          {t('common:actions.retry')}
        </Button>
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
  /** Override the default lucide ShieldOff. Rendered muted, never danger. */
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
        'flex flex-col items-center justify-center gap-3 px-6 py-12 text-center',
        className,
      )}
      {...props}
    >
      <div className="flex size-10 items-center justify-center rounded-lg bg-muted text-muted-foreground [&_svg]:size-5 [&_svg]:shrink-0">
        {icon ?? <ShieldOff />}
      </div>
      <div className="flex flex-col items-center gap-1.5">
        <p className="text-sm font-medium text-foreground">
          {title ?? t('forbidden.title')}
        </p>
        <p className="max-w-sm text-sm text-muted-foreground">
          {description ?? t('forbidden.description')}
        </p>
      </div>
      {children ? <div className="mt-1">{children}</div> : null}
    </div>
  )
}
