// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { useEditionBadge } from './use-edition-badge'

export function EditionBadge({ compact = false }: { compact?: boolean }) {
  const { t } = useTranslation('settings')
  const { edition, shown } = useEditionBadge()
  if (!edition || !shown) return null
  const name = t(`edition.badge.${edition}`)
  return (
    <Badge
      variant="neutral"
      role="img"
      aria-label={t('edition.badge.accessibleName', { edition: name })}
      className="shrink-0 bg-muted/50 text-foreground"
    >
      {compact ? t(`edition.badge.${edition}Short`) : name}
    </Badge>
  )
}
