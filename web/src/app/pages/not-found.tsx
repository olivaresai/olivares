// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { Link, useLocation } from '@tanstack/react-router'
import { Compass, Package } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { viewById } from '@/features/navigation/model'
import { Button } from '@/components/ui/button'
import { EmptyState } from '@/components/ui/empty-state'
import { BUSINESS_URL, isBusinessOnlyPath } from '@/features/business-build'

function GoToNow() {
  const { t } = useTranslation('errors')
  return (
    <Button asChild variant="primary" size="sm">
      <Link to={viewById('home')!.path}>{t('boundary.goToNow')}</Link>
    </Button>
  )
}

/**
 * A bookmark of a page this edition does not include (route-census.json `business_paths`):
 * calm, never a failure. It names Business once, says the person's data and pages stay the
 * same, and its one act opens Settings > Edition («View editions»), with a quiet link to
 * Business beside it. No button offers the Business action, so nothing in Community starts
 * what only Business performs.
 */
function BusinessNotice() {
  const { t } = useTranslation('errors')
  return (
    <>
      <h1 className="sr-only">{t('businessOnly.heading')}</h1>
      <EmptyState
        title={t('businessOnly.heading')}
        description={t('businessOnly.description')}
        action={
          <Button asChild variant="secondary">
            <Link to="/settings" search={{ section: 'edition' } as never}>
              <Package aria-hidden="true" />
              {t('businessOnly.editions')}
            </Link>
          </Button>
        }
        secondaryAction={
          <a
            href={BUSINESS_URL}
            target="_blank"
            rel="noopener noreferrer"
            className="text-body font-medium text-muted-foreground hover:text-foreground hover:underline"
          >
            {t('businessOnly.link')}
          </a>
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
