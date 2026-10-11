// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useTranslation } from 'react-i18next'
import { cn } from '@/lib/utils'

/**
 * The small "Off" tag of a page whose module is switched off here. Navigation lists such a
 * page as a calm entry (dimmed label, this tag) instead of hiding it; its page offers to
 * turn the module on. A boxed tag is for a fact that is not a state, and this one is a
 * fact about the installation, so it stays quiet: 12 px, muted fill.
 */
export function OffTag({ className }: { className?: string }) {
  const { t } = useTranslation('nav')
  return (
    <span
      data-slot="off-tag"
      className={cn(
        'ml-auto shrink-0 rounded-md bg-hover px-1.5 text-caption leading-5 font-medium text-text-3',
        className,
      )}
    >
      <span className="sr-only">, </span>
      {t('directory.off')}
    </span>
  )
}
