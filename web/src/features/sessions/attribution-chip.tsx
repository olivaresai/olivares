// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useTranslation } from 'react-i18next'
import { cn } from '@/lib/utils'
import type { Attribution } from './types'
import './i18n'

/**
 * AttributionChip — which CHANNEL a live row was folded from. The value is the
 * server's: a payload label never decides it. `managed` is the only value that
 * carries a proven process; the others are read-only facts about where the
 * observation came from.
 */
export function AttributionChip({ attribution }: { attribution: Attribution }) {
  const { t } = useTranslation('sessions')
  return (
    <span
      title={t(`card.attributionExplain.${attribution}`, { defaultValue: '' })}
      className={cn(
        'inline-flex items-center rounded-sm border border-border bg-muted px-1.5 py-0.5 text-[10px] font-medium text-muted-foreground',
      )}
    >
      {t(`card.attribution.${attribution}`, { defaultValue: attribution })}
    </span>
  )
}
