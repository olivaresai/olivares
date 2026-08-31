// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useTranslation } from 'react-i18next'
import { ErrorState } from '@/components/ui/error-state'

/** Root errorComponent — catches a render/loader error anywhere in the tree (a 500
 * equivalent for the SPA). Offers a reset so the operator isn't stuck. */
export function RouteErrorPage({
  reset,
}: {
  error: Error
  reset?: () => void
}) {
  const { t } = useTranslation('errors')
  return (
    <main className="flex min-h-svh items-center justify-center bg-background p-6">
      <h1 className="sr-only">{t('boundary.title')}</h1>
      <ErrorState
        title={t('boundary.title')}
        description={t('boundary.description')}
        retry={reset ?? (() => window.location.reload())}
      />
    </main>
  )
}
