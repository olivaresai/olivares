// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { KeyRound } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { cn } from '@/lib/utils'

/**
 * SecretRef renders a REFERENCE to a secret — its name/handle — and nothing else.
 * The control plane never exposes secret VALUES to the console (docs/SECURITY-HARDENING.md): MCP /
 * deploy / data config carries references, not values. This component takes only a
 * `name`, so by construction it is impossible to render a value here. Use it
 * everywhere the management views surface a secret-backed setting.
 */
export interface SecretRefProps {
  /** The reference name/handle of the secret — NEVER a secret value. */
  name?: string | null
  className?: string
}

export function SecretRef({ name, className }: SecretRefProps) {
  const { t } = useTranslation('common')
  if (!name) {
    return (
      <span className={cn('text-sm text-muted-foreground', className)}>
        {t('secretRef.none')}
      </span>
    )
  }
  return (
    <span
      data-slot="secret-ref"
      title={t('secretRef.hint')}
      className={cn(
        'inline-flex items-center gap-1 rounded-sm border border-border bg-muted px-1.5 py-0.5 font-mono text-xs',
        className,
      )}
    >
      <KeyRound className="size-3 shrink-0 text-accent-text" aria-hidden />
      <span className="text-muted-foreground">{t('secretRef.prefix')}:</span>
      <span className="text-foreground">{name}</span>
    </span>
  )
}
