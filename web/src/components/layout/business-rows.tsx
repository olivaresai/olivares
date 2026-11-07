// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// WHAT BUSINESS ADDS HERE: after the page's own content, one closed row per capability
// Business adds on this page (businessRows): its line, the Business label and a chevron to
// About Business. Faint and quiet: no accent, no tab, no page; nothing here starts what only
// Business performs. A Business build lists none.
import { useRouterState } from '@tanstack/react-router'
import { ChevronRight } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { BUSINESS_URL } from '@/features/business-build'
import { resolveLocation } from '@/features/navigation/model'
import { businessRows } from '@/features/navigation/places'

export function BusinessRows() {
  const { t } = useTranslation(['nav', 'settings'])
  const pathname = useRouterState({ select: (s) => s.location.pathname })
  const location = resolveLocation(pathname)
  // The page itself, never an address under it (a work item, a recording's viewer).
  const rows =
    location.kind === 'view' &&
    location.view.path === pathname.replace(/\/+$/, '')
      ? businessRows(location.view.id)
      : []
  if (rows.length === 0) return null
  return (
    <ul data-slot="business-rows" className="mt-6 border-t border-border">
      {rows.map((id) => (
        <li key={id} className="border-b border-border">
          <a
            href={BUSINESS_URL}
            target="_blank"
            rel="noopener noreferrer"
            className="flex min-h-9 items-center gap-2 rounded-ctl py-1.5 text-body text-text-3 outline-none transition-colors duration-fast hover:text-text-2 focus-visible:ring-2 focus-visible:ring-focus"
          >
            {t(`nav:business.${id}`)}{' '}
            <span className="ml-auto flex shrink-0 items-center gap-1.5">
              <Badge variant="neutral">
                {t('settings:edition.badge.business')}
              </Badge>
              <ChevronRight aria-hidden="true" className="size-3.5" />
            </span>
          </a>
        </li>
      ))}
    </ul>
  )
}
