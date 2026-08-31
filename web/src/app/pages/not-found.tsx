// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { Link } from '@tanstack/react-router'
import { Compass } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { EmptyState } from '@/components/ui/empty-state'

/** The application's 404 — a calm, navigable dead end (root notFoundComponent). */
export function NotFoundPage() {
  const { t } = useTranslation('errors')
  return (
    <main className="flex min-h-svh items-center justify-center bg-background p-6">
      <h1 className="sr-only">{t('notFound.title')}</h1>
      <EmptyState
        icon={<Compass />}
        title={t('notFound.title')}
        description={t('notFound.description')}
        action={
          <Button asChild variant="primary" size="sm">
            <Link to="/">{t('notFound.back')}</Link>
          </Button>
        }
      />
    </main>
  )
}
