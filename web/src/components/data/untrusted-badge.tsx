// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { ShieldQuestion } from 'lucide-react'
import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { cn } from '@/lib/utils'

/**
 * UntrustedBadge marks a value the control plane SHOWS but did not VERIFY — an MCP
 * server's self-reported annotation (readOnlyHint / destructiveHint), a declared
 * capability, anything the agent claims about itself (ARCHITECTURE.md). It is a calm,
 * dashed, neutral chip (never alarming) with a "self-reported — not verified"
 * title, so the operator reads it as a CLAIM, not as truth. The product rule is
 * absolute: the UI never enforces on these signals; the backend never trusts them.
 */
export interface UntrustedBadgeProps {
  /** The claim to display (e.g. "Read-only", "Destructive"); defaults to "Unverified". */
  label?: ReactNode
  /** Title/tooltip text; defaults to the standard self-reported explanation. */
  hint?: string
  className?: string
}

export function UntrustedBadge({
  label,
  hint,
  className,
}: UntrustedBadgeProps) {
  const { t } = useTranslation('common')
  return (
    <span
      data-slot="untrusted"
      title={hint ?? t('untrusted.hint')}
      className={cn(
        'inline-flex items-center gap-1 rounded-sm border border-dashed border-border-strong bg-transparent px-1.5 py-0.5 text-xs font-medium text-muted-foreground',
        className,
      )}
    >
      <ShieldQuestion className="size-3 shrink-0" aria-hidden />
      {label ?? t('untrusted.label')}
    </span>
  )
}
