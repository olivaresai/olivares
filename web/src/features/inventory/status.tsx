// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'

/**
 * InvStatus — the catalog liveness chip. `active` is success; `stale` is WARNING,
 * not a muted neutral: a gone-quiet entity is a signal worth the operator's eye
 * (docs/SECURITY-HARDENING.md), surfaced, never hidden. Localized via the inventory namespace.
 */
export function InvStatus({
  status,
  className,
}: {
  status: string
  className?: string
}) {
  const { t } = useTranslation('inventory')
  const stale = status === 'stale'
  return (
    <Badge variant={stale ? 'warning' : 'success'} className={className}>
      {t(`status.${status}`, { defaultValue: status })}
    </Badge>
  )
}
