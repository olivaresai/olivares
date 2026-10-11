// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import type { SsoProvider } from '@/lib/api/types'
import { isPlainOriginPath } from '@/lib/auth/return-path'

/** The label the engine gives its default provider; any other label is the
 * operator's own name for the provider and is shown as given. */
const DEFAULT_LABEL = 'Single sign-on'

/**
 * The address one SSO button opens. The engine answers each provider with a
 * same-origin start path; the button navigates the whole page there (never a
 * fetch), always with the cookie session (`browser_session=1`, never the JSON
 * token transport), and carries the same return path the password sign-in uses.
 * Anything that would leave this origin gets no button.
 */
export function ssoStartHref(
  startUrl: unknown,
  returnTo: string | null,
  origin: string,
): string | null {
  if (!isPlainOriginPath(startUrl)) return null
  try {
    const url = new URL(startUrl, origin)
    if (url.origin !== origin) return null
    url.searchParams.set('browser_session', '1')
    if (returnTo) url.searchParams.set('return_to', returnTo)
    return url.pathname + url.search
  } catch {
    return null
  }
}

/** One button per sign-in provider the engine lists, then a quiet "or" above
 * the password form. No list (or an empty one) renders nothing. */
export function SsoButtons({
  providers,
  returnTo,
}: {
  providers: SsoProvider[] | undefined
  returnTo: string | null
}) {
  const { t } = useTranslation('auth')
  const buttons = (providers ?? []).flatMap((p) => {
    const label = typeof p?.label === 'string' ? p.label.trim() : ''
    const href = ssoStartHref(p?.start_url, returnTo, window.location.origin)
    return label && href ? [{ label, href }] : []
  })
  if (buttons.length === 0) return null
  return (
    <div className="mb-4 flex flex-col gap-4" data-slot="sso-sign-in">
      <div className="flex flex-col gap-2">
        {buttons.map((b) => (
          <Button
            key={b.href}
            asChild
            variant="secondary"
            className="h-auto min-h-10 w-full whitespace-normal py-2"
          >
            <a href={b.href}>
              {b.label === DEFAULT_LABEL
                ? t('login.sso')
                : t('login.ssoWith', { label: b.label })}
            </a>
          </Button>
        ))}
      </div>
      <div
        className="flex items-center gap-3 text-caption text-muted-foreground"
        role="separator"
        aria-label={t('login.or')}
      >
        <span className="h-px flex-1 bg-border" aria-hidden />
        <span aria-hidden>{t('login.or')}</span>
        <span className="h-px flex-1 bg-border" aria-hidden />
      </div>
    </div>
  )
}
