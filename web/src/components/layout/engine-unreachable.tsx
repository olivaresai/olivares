// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { Link, useRouterState } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'
import { isSignInPath } from '@/lib/auth/return-path'
import { Spinner } from '@/components/ui/spinner'
import { BrandMark } from './brand'

/** What the shell shows while page-load recovery cannot reach the engine (slow, or restarting):
 * not the sign-in form, because nothing says the person is signed out. The retry runs in
 * providers.tsx; this page opens by itself when the engine answers. The sign-in link is the
 * way out when the engine answers but never to this page (a proxy rule, an engine fault). */
export function EngineUnreachable() {
  const { t } = useTranslation('errors')
  const { t: tAuth } = useTranslation('auth')
  const href = useRouterState({ select: (s) => s.location.href })
  const returnTo = href && !isSignInPath(href) ? href : undefined
  return (
    <main className="flex min-h-svh items-center justify-center bg-background px-4">
      <div
        role="status"
        data-engine-state="unreachable"
        className="flex max-w-md flex-col items-center gap-3 text-center"
      >
        <BrandMark className="size-7 text-foreground" />
        <h1 className="text-heading font-semibold text-foreground">
          {t('engineRetrying.title')}
        </h1>
        <p className="text-body text-text-2">
          {t('engineRetrying.description')}
        </p>
        <Spinner aria-hidden />
        <Link
          to="/login"
          search={returnTo ? { returnTo } : {}}
          className="text-body text-text-2 underline underline-offset-2 hover:text-foreground"
        >
          {tAuth('login.title')}
        </Link>
      </div>
    </main>
  )
}
