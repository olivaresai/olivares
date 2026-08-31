// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useTranslation } from 'react-i18next'
import { Badge, type BadgeVariant } from '@/components/ui/badge'
import './i18n'
import type { Change } from './types'

const CHANGE_VARIANT: Record<string, BadgeVariant> = {
  create: 'success',
  update: 'info',
  delete: 'danger',
  noop: 'neutral',
}

/** Renders a plan/verify diff (Change[]) as a compact list. */
export function ChangeList({ changes }: { changes: Change[] }) {
  const { t } = useTranslation('deploy')
  if (changes.length === 0) return null
  return (
    <ul className="flex flex-col gap-1.5">
      {changes.map((c, i) => (
        <li
          key={`${c.kind}:${c.resource}:${i}`}
          className="flex items-center gap-2 rounded-md border border-border bg-surface px-2.5 py-1.5 text-xs"
        >
          <Badge variant={CHANGE_VARIANT[c.kind] ?? 'neutral'}>
            {t(`change.${c.kind}`, { defaultValue: c.kind })}
          </Badge>
          <span className="font-mono text-foreground">{c.resource}</span>
          {c.detail && (
            <span className="truncate text-muted-foreground">{c.detail}</span>
          )}
        </li>
      ))}
    </ul>
  )
}

// ⛔ GateBadge se MOVIÓ a `features/_intel/badges.tsx`. No es aseo: el mismo `gate_status` lo
//    pintaban tres superficies y sólo ésta lo hacía bien — orquestación lo escribía en gris crudo
//    y el panel de workflows lo forzaba a `warning`. Se reexporta para no tocar a los importadores
//    de deploy, que ya lo usaban correctamente.
export { GateBadge } from '@/features/_intel'
