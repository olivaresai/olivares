// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Shared building blocks for the Claude Code governance console.
import type { UseQueryResult } from '@tanstack/react-query'
import { StepUpRequiredState } from '@/components/layout/step-up-state'
import { CircleAlert, CircleCheck, Info, TriangleAlert } from 'lucide-react'
import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { CaveatNotice, SeamBadge } from '@/features/_intel'
import { ErrorState, ForbiddenState } from '@/components/ui/error-state'
import { Skeleton } from '@/components/ui/skeleton'
import { ApiError, NetworkError } from '@/lib/api/errors'
import { cn } from '@/lib/utils'
import { isContractPending } from './api'
import type { IssueSeverity, SchemaIssue } from './schema'

/**
 * The honest "backend pending" seam. A DECLARED authoring endpoint is not
 * live yet: we say so plainly (never a fake success, never a red error). Local
 * schema validation still works — only the backend-dependent action is gated.
 */
export function ContractPendingNotice({ what }: { what: string }) {
  const { t } = useTranslation('claudePolicy')
  return (
    <CaveatNotice tone="info" className="items-center">
      <span className="inline-flex flex-wrap items-center gap-x-2 gap-y-1">
        <SeamBadge label={t('seam.badge')} />
        <span>{t('seam.body', { what })}</span>
      </span>
    </CaveatNotice>
  )
}

/**
 * Like AsyncSection, but a DECLARED endpoint that is not yet implemented
 * (404/501/405) renders the honest pending seam instead of a red error.
 */
export function DeclaredSection<T>({
  query,
  what,
  live = false,
  skeletonHeight = 120,
  children,
}: {
  query: Pick<
    UseQueryResult<T>,
    'data' | 'isLoading' | 'isError' | 'error' | 'refetch'
  >
  /** Human label of the capability for the pending seam copy. */
  what: string
  /**
   * Set when the endpoint IS MOUNTED on the engine. The pending seam says "the
   * backend endpoint is not live yet … Nothing is faked" in a calm info tone —
   * true and useful for a contract a backend session has yet to implement, and a
   * flat falsehood for a route that is serving today. A live route's 404 is a
   * real answer (a missing row, another tenant's, a module that is not loaded in
   * this deployment); dressing it as unbuilt hides a failure behind a roadmap
   * note and leaves the operator with nothing to retry.
   */
  live?: boolean
  skeletonHeight?: number
  children: (data: T) => ReactNode
}) {
  const { t } = useTranslation('errors')
  if (query.isLoading) {
    return <Skeleton className="w-full" style={{ height: skeletonHeight }} />
  }
  if (query.isError) {
    const error = query.error
    if (!live && isContractPending(error))
      return <ContractPendingNotice what={what} />
    /* ⛔ ASEGURAMIENTO ANTES QUE ROL, y en el ayudante que envuelve a TODA esta feature:
       un solo sitio, todas sus pantallas. Este fichero ya sabía que «una negativa tiene
       clases» —trata `isContractPending` justo arriba—, y aun así `isForbidden`, que es
       sólo el status (lib/api/errors.ts:59), se tragaba el 403 de ceremonia. */
    if (error instanceof ApiError && error.isStepUpRequired) {
      return (
        <StepUpRequiredState
          action="generic"
          onElevated={() => void query.refetch()}
        />
      )
    }
    if (error instanceof ApiError && error.isForbidden) {
      return (
        <div className="flex min-h-32 items-center justify-center">
          <ForbiddenState
            title={t('forbidden.title')}
            description={t('forbidden.description')}
          />
        </div>
      )
    }
    const isNetwork = error instanceof NetworkError
    return (
      <ErrorState
        title={isNetwork ? t('network.title') : t('serverError.title')}
        description={
          isNetwork ? t('network.description') : t('serverError.description')
        }
        retry={() => void query.refetch()}
      />
    )
  }
  if (query.data === undefined) return null
  return <>{children(query.data)}</>
}

const SEVERITY_META: Record<
  IssueSeverity,
  { icon: typeof CircleAlert; cls: string }
> = {
  error: { icon: CircleAlert, cls: 'text-danger' },
  warning: { icon: TriangleAlert, cls: 'text-warning' },
  info: { icon: Info, cls: 'text-info' },
}

/**
 * Live schema-validation results (LOCAL, real — runs against the verified schemas
 * with no backend). Empty = a calm "valid" state.
 */
export function ValidationPanel({
  issues,
  className,
}: {
  issues: SchemaIssue[]
  className?: string
}) {
  const { t } = useTranslation('claudePolicy')
  if (issues.length === 0) {
    return (
      <p
        className={cn(
          'flex items-center gap-2 text-xs text-success',
          className,
        )}
        role="status"
      >
        <CircleCheck className="size-4 shrink-0" aria-hidden />
        {t('validation.ok')}
      </p>
    )
  }
  const errors = issues.filter((i) => i.severity === 'error').length
  return (
    <div className={cn('flex flex-col gap-1.5', className)}>
      <p className="text-xs font-medium text-muted-foreground">
        {t('validation.summary', { count: issues.length, errors })}
      </p>
      <ul
        className="flex flex-col gap-1"
        aria-label={t('validation.listLabel')}
      >
        {issues.map((issue, i) => {
          const meta = SEVERITY_META[issue.severity]
          const Icon = meta.icon
          return (
            <li
              key={`${issue.path}-${i}`}
              className="flex items-start gap-2 rounded-md border border-border bg-surface px-2.5 py-1.5 text-xs"
            >
              <Icon
                className={cn('mt-px size-3.5 shrink-0', meta.cls)}
                aria-hidden
              />
              <span className="min-w-0">
                {issue.path ? (
                  <code className="mr-1 font-mono text-[0.7rem] text-muted-foreground">
                    {issue.path}
                  </code>
                ) : null}
                <span className="text-foreground">{issue.message}</span>
              </span>
            </li>
          )
        })}
      </ul>
    </div>
  )
}

/** A compact key/value reference row used by the surface reference panels. */
export function ReferenceRow({
  label,
  children,
  mono = false,
}: {
  label: ReactNode
  children: ReactNode
  mono?: boolean
}) {
  return (
    <div className="grid grid-cols-[minmax(0,12rem)_1fr] gap-x-3 gap-y-0.5 py-1.5">
      <dt className="text-xs font-medium text-muted-foreground">{label}</dt>
      <dd
        className={cn(
          'min-w-0 text-xs text-foreground',
          mono && 'font-mono break-all',
        )}
      >
        {children}
      </dd>
    </div>
  )
}
