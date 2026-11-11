// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useNavigate } from '@tanstack/react-router'
import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { viewById } from '@/features/navigation/model'
import { ErrorState, errorDetail } from '@/components/ui/error-state'
import {
  createBoundaryErrorId,
  isChunkLoadError,
  reportBoundaryError,
} from '@/lib/errors/report-boundary-error'

/** Root and per-route errorComponent. It keeps raw diagnostics off screen and
 * offers re-mount, navigation home, or a reload only for chunk failures. */
export function RouteErrorPage({
  error,
  reset,
  embedded = false,
}: {
  error: unknown
  reset?: () => void
  embedded?: boolean
}) {
  const { t } = useTranslation('errors')
  const navigate = useNavigate()
  const [errorId] = useState(createBoundaryErrorId)
  const reportedRef = useRef(false)

  useEffect(() => {
    if (reportedRef.current) return
    reportedRef.current = true
    reportBoundaryError(errorId, error)
  }, [error, errorId])

  const retry = isChunkLoadError(error)
    ? () => window.location.reload()
    : (reset ?? (() => window.location.reload()))
  const Container = embedded ? 'div' : 'main'

  return (
    <Container
      className={
        embedded
          ? 'flex min-h-[40vh] items-center justify-center p-6'
          : 'flex min-h-svh items-center justify-center bg-background p-6'
      }
    >
      <ErrorState
        heading
        title={t('boundary.title')}
        description={t('boundary.description')}
        retry={retry}
        goToNow={() => navigate({ to: viewById('home')!.path })}
        errorId={errorId}
        detail={errorDetail(error)}
      />
    </Container>
  )
}
