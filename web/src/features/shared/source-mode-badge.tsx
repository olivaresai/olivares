// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useTranslation } from 'react-i18next'
import { Badge, type BadgeVariant } from '@/components/ui/badge'
import { cn } from '@/lib/utils'
import './i18n'
import {
  normalizeSourceMode,
  type NormalizedSourceMode,
  type SourceMode,
} from './source-mode'

const MODE_VARIANT: Record<NormalizedSourceMode, BadgeVariant> = {
  export: 'warning',
  live: 'success',
  direct: 'info',
}

export function SourceModeBadge({
  value,
  className,
}: {
  value?: SourceMode | string | null
  className?: string
}) {
  const { t } = useTranslation('shared')
  const mode = normalizeSourceMode(value)
  return (
    <Badge
      variant={MODE_VARIANT[mode]}
      className={cn('uppercase tracking-normal', className)}
      title={t(`sourceModes.hints.${mode}`)}
      data-source-mode={mode}
    >
      {t(`sourceModes.${mode}`)}
    </Badge>
  )
}
