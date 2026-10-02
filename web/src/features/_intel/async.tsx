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
import { QueryErrorState } from '@/components/layout/query-error-state'
import { Skeleton } from '@/components/ui/skeleton'
import { ApiError, isModuleNotEnabled } from '@/lib/api/errors'
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
    // The one mapping (components/layout/query-error-state.tsx): step-up, 403, a module that
    // is off, and only then the failure with Retry. A 403 keeps its calm block here.
    const error = query.error
    const calm =
      error instanceof ApiError &&
      (error.isForbidden || isModuleNotEnabled(error)) &&
      !error.isStepUpRequired
    const state = (
      <QueryErrorState error={error} retry={() => void query.refetch()} />
    )
    return calm ? (
      <div className="flex min-h-40 items-center justify-center">{state}</div>
    ) : (
      state
    )
  }
  if (query.data === undefined) return null
  return <>{children(query.data)}</>
}
