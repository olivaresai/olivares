// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useTranslation } from 'react-i18next'
import { StatusChip } from '@/components/ui/status-chip'

/**
 * InvStatus — the catalog liveness as dot and word. `stale` is amber, not a muted
 * neutral: a gone-quiet entity is a signal worth the operator's eye, surfaced, never
 * hidden. A state this page has no word for stays neutral.
 */
export function InvStatus({
  status,
  className,
}: {
  status: string
  className?: string
}) {
  const { t } = useTranslation('inventory')
  const tone =
    status === 'stale' ? 'warn' : status === 'active' ? 'ok' : 'neutral'
  return (
    <StatusChip tone={tone} className={className}>
      {t(`status.${status}`, { defaultValue: status || '—' })}
    </StatusChip>
  )
}
