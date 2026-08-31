// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// AsyncSection — the one place the intelligence views map a TanStack Query result to
// the four states the design system defines: loading (skeleton), forbidden (calm,
// never red — a 403 is a permission boundary), error (retryable), and data. Every
// view uses it so the states are consistent and a missing permission never reads as
// a broken page.
import type { ReactNode } from 'react'
import type { UseQueryResult } from '@tanstack/react-query'
import { ErrorState, ForbiddenState } from '@/components/ui/error-state'
import { Skeleton } from '@/components/ui/skeleton'
import { StepUpRequiredState } from '@/components/layout/step-up-state'
import { ApiError, NetworkError } from '@/lib/api/errors'
import { useTranslation } from 'react-i18next'
import { cn } from '@/lib/utils'

export function AsyncSection<T>({
  query,
  children,
  /** Height of the loading skeleton block. */
  skeletonHeight = 120,
  className,
}: {
  query: Pick<
    UseQueryResult<T>,
    'data' | 'isLoading' | 'isError' | 'error' | 'refetch'
  >
  children: (data: T) => ReactNode
  skeletonHeight?: number
  className?: string
}) {
  const { t } = useTranslation(['errors', 'common'])

  if (query.isLoading) {
    // Announce the busy state and the success swap (4.1.3): every intel/system view
    // routes through here, so the bare skeleton was silent to AT on load.
    return (
      <div role="status" aria-busy="true">
        <span className="sr-only">{t('common:states.loading')}</span>
        <Skeleton
          className={cn('w-full', className)}
          style={{ height: skeletonHeight }}
        />
      </div>
    )
  }
  if (query.isError) {
    const error = query.error
    // ASSURANCE before ROLE — the engine sends two different 403s and only one of
    // them means "you may not". Rendering a step-up demand as ForbiddenState told
    // the operator they lacked a permission they hold; the honest answer is the
    // ceremony that lifts the refusal (see components/layout/step-up-state.tsx).
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
        <div className="flex min-h-40 items-center justify-center">
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
        requestId={error instanceof ApiError ? error.requestId : undefined}
      />
    )
  }
  if (query.data === undefined) return null
  return <>{children(query.data)}</>
}
