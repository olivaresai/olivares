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

function GoToNow() {
  const { t } = useTranslation('errors')
  return (
    <Button asChild variant="primary" size="sm">
      <Link to="/">{t('boundary.goToNow')}</Link>
    </Button>
  )
}

/**
 * A bookmark of a page this edition does not include (route-census.json `business_paths`):
 * one sentence that names Business and a link to it. No button offers the action, so
 * nothing in Community starts what only Business performs.
 */
function BusinessNotice() {
  const { t } = useTranslation('errors')
  return (
    <>
      <h1 className="sr-only">{t('businessOnly.title')}</h1>
      <EmptyState
        icon={<Briefcase />}
        title={t('businessOnly.title')}
        description={t('businessOnly.description')}
        action={
          <div className="flex items-center gap-4">
            <GoToNow />
            <a
              href={BUSINESS_URL}
              target="_blank"
              rel="noopener noreferrer"
              className="text-body font-medium text-accent-text hover:underline"
            >
              {t('businessOnly.link')}
            </a>
          </div>
        }
      />
    </>
  )
}

/**
 * The application's 404 and the Business notice: a calm, navigable dead end. `embedded`
 * draws it in the shell's content area (the sidebar and top bar stay, as for a view that
 * crashed); otherwise it is the whole page, for a visitor with no shell.
 */
function NotFound({ embedded }: { embedded: boolean }) {
  const { t } = useTranslation('errors')
  const pathname = useLocation({ select: (l) => l.pathname })
  const Container = embedded ? 'div' : 'main'
  return (
    <Container
      className={
        embedded
          ? 'flex min-h-[40vh] items-center justify-center p-6'
          : 'flex min-h-svh items-center justify-center bg-background p-6'
      }
    >
      {isBusinessOnlyPath(pathname) ? (
        <BusinessNotice />
      ) : (
        <>
          <h1 className="sr-only">{t('notFound.title')}</h1>
          <EmptyState
            icon={<Compass />}
            title={t('notFound.title')}
            description={t('notFound.description')}
            action={<GoToNow />}
          />
        </>
      )}
    </Container>
  )
}

/** The router's notFoundComponent: the whole page. */
export function NotFoundPage() {
  return <NotFound embedded={false} />
}

/** The same notice in the shell's content area (routes.tsx, the signed-in catch-all). */
export function NotFoundInShell() {
  return <NotFound embedded />
}
