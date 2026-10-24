// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { Link, useLocation } from '@tanstack/react-router'
import { Briefcase, Compass } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { EmptyState } from '@/components/ui/empty-state'
import { isBusinessOnlyPath } from '@/features/business-build'

const BUSINESS_URL = 'https://olivares.ai/business'

/**
 * A bookmark of a page this edition does not include (route-census.json `business_paths`):
 * one sentence that names Business and a link to it. No button offers the action, so
 * nothing in Community starts what only Business performs.
 */
function BusinessNotice() {
  const { t } = useTranslation('errors')
  return (
    <main className="flex min-h-svh items-center justify-center bg-background p-6">
      <h1 className="sr-only">{t('businessOnly.title')}</h1>
      <EmptyState
        icon={<Briefcase />}
        title={t('businessOnly.title')}
        description={t('businessOnly.description')}
        action={
          <a
            href={BUSINESS_URL}
            target="_blank"
            rel="noopener noreferrer"
            className="text-body font-medium text-accent-text hover:underline"
          >
            {t('businessOnly.link')}
          </a>
        }
      />
    </main>
  )
}

/** The application's 404 — a calm, navigable dead end (root notFoundComponent). */
export function NotFoundPage() {
  const { t } = useTranslation('errors')
  const pathname = useLocation({ select: (l) => l.pathname })
  if (isBusinessOnlyPath(pathname)) return <BusinessNotice />
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
