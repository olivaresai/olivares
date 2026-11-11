// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// WHAT A HIGHER EDITION ADDS HERE: after the page's own content, one closed row per
// capability it adds on this page (businessRows): its line, the edition's whole name and a
// chevron to that edition's page. The registered views placed above Community that a place
// closes are one row per edition, naming them and opening the first, whose address says the
// edition line. Faint and quiet: no accent, no tab, no page; nothing here starts what only
// that edition performs. For an administrator only, who can add a license (the license
// tab's gate): a member could never open what the rows describe. A row shows exactly where
// the edition held here does not hold it.
import { useRouterState } from '@tanstack/react-router'
import { ChevronRight } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { AppLink } from '@/components/ui/app-link'
import { Badge } from '@/components/ui/badge'
import { EDITION_NAMES, PRICING_URL } from '@/components/ui/edition-note'
import { BUSINESS_URL } from '@/features/business-build'
import { useOpensAtEdition } from '@/features/console/commercial-edition'
import { resolveLocation, viewLabel } from '@/features/navigation/model'
import { businessRows } from '@/features/navigation/places'
import { useAuth } from '@/lib/auth/context'
import { useModuleEnabled } from '@/stores/modules'

const ROW =
  'flex min-h-9 items-center gap-2 rounded-ctl py-1.5 text-body text-text-3 outline-none transition-colors duration-fast hover:text-text-2 focus-visible:ring-2 focus-visible:ring-focus'

export function BusinessRows() {
  const { t, i18n } = useTranslation('nav')
  const { isSuperadmin } = useAuth()
  const opens = useOpensAtEdition()
  const moduleOn = useModuleEnabled()
  const pathname = useRouterState({ select: (s) => s.location.pathname })
  const location = resolveLocation(pathname)
  // The page itself, never an address under it (a work item, a recording's viewer).
  const rows =
    isSuperadmin &&
    location.kind === 'view' &&
    location.view.path === pathname.replace(/\/+$/, '')
      ? businessRows(
          location.view.id,
          (row) => !opens(row, moduleOn(undefined, row.id)),
        )
      : []
  if (rows.length === 0) return null
  const names = new Intl.ListFormat(i18n.language, { type: 'conjunction' })
  return (
    <ul data-slot="business-rows" className="mt-6 border-t border-border">
      {rows.map(({ id, minEdition, path, views = [id] }) => {
        const edition = (
          <span className="ml-auto flex shrink-0 items-center gap-1.5">
            <Badge variant="neutral">{EDITION_NAMES[minEdition]}</Badge>
            <ChevronRight aria-hidden="true" className="size-3.5" />
          </span>
        )
        return (
          <li key={id} className="border-b border-border">
            {path ? (
              <AppLink href={path} className={ROW}>
                {names.format(views.map((view) => viewLabel(t, view)))}{' '}
                {edition}
              </AppLink>
            ) : (
              <a
                href={minEdition === 'business' ? BUSINESS_URL : PRICING_URL}
                target="_blank"
                rel="noopener noreferrer"
                className={ROW}
              >
                {t(`nav:business.${id}`)} {edition}
              </a>
            )}
          </li>
        )
      })}
    </ul>
  )
}
