// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { ReactNode } from 'react'
import type { UseQueryResult } from '@tanstack/react-query'
import { AsyncSection } from '@/features/_intel'
import { isUnknownVerdict, workErrorCode } from './api'
import { UnavailableNotice } from './verdict'

/**
 * WorkSection — AsyncSection, plus the THIRD DOOR the shared one cannot know about.
 *
 * THE GAP THIS CLOSES, found while contrasting this feature against its own promise.
 * AsyncSection maps a query to four states: loading, forbidden, error, data
 * (features/_intel/async.tsx). That is exactly right for the rest of the console, where
 * a failed read is a failed read. It is NOT right here, because it collapses the
 * kernel's THREE outcomes into TWO: everything that is not a 403 becomes one generic
 * "server error, retry" card, so ROTO and NO_HE_PODIDO_MIRAR land in the same place.
 *
 * Rule 5 of the canon is "tres respuestas, nunca dos", and this feature exists to keep
 * the third one visible. A generic error card does not commit the cardinal sin — it
 * never says clean, never says empty — but it does tell an operator "the request
 * failed" when the engine said "I could not complete the observation", and those lead
 * to different actions: one is retried, the other means the answer is unknown and the
 * screen cannot be trusted until it is not.
 *
 * The reads reach the console as a 503 whose body carries the verdict (unknown() in
 * modules/sessions/work_state.go, rendered by writeWorkError at work_api.go:357-360),
 * so the distinction is available; nothing but this wrapper was reading it.
 *
 * Everything else is delegated, deliberately: forbidden stays calm and never red,
 * loading keeps its announced skeleton, and an ordinary failure keeps the retryable
 * card every other view has. This adds one branch; it does not fork the design system.
 */
export function WorkSection<T>({
  query,
  children,
  skeletonHeight,
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
  // Checked BEFORE delegating: once AsyncSection has rendered its error card the
  // verdict is gone, and a card that says "retry" over an unknown outcome is precisely
  // the confusion this feature is built to prevent.
  if (query.isError && isUnknownVerdict(query.error)) {
    return (
      <UnavailableNotice
        code={workErrorCode(query.error) ?? 'observation_unavailable'}
        className={className}
      />
    )
  }
  return (
    <AsyncSection
      query={query}
      skeletonHeight={skeletonHeight}
      className={className}
    >
      {children}
    </AsyncSection>
  )
}
