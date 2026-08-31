// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// RedactionToggle — click-to-reveal for sensitive fields in the session recording
// viewer. The reveal is gated on the caller's RBAC permission (useAuth().can()),
// so a viewer who cannot read the value never sees the toggle at all.
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { useAuth } from '@/lib/auth/context'
import './i18n'

export interface RedactionToggleProps {
  /** The actual (possibly sensitive) value. */
  value: string
  /** RBAC permission required to reveal the value. */
  permission: string
}

/**
 * A redacted-by-default inline block. When collapsed it shows a "[REDACTED]"
 * placeholder; clicking reveals the real value if the principal holds the
 * required permission. A "Hide" button collapses it again. If the principal
 * lacks the permission the placeholder is shown without a toggle.
 */
export function RedactionToggle({ value, permission }: RedactionToggleProps) {
  const { t } = useTranslation('session-viewer')
  const { can } = useAuth()
  const [revealed, setRevealed] = useState(false)
  const allowed = can(permission)

  if (revealed && allowed) {
    return (
      <span className="inline-flex items-center gap-1.5">
        <span className="break-all font-mono text-xs text-foreground">
          {value}
        </span>
        <Button
          variant="ghost"
          size="sm"
          className="h-5 px-1 text-[11px]"
          onClick={() => setRevealed(false)}
        >
          {t('redaction.hide')}
        </Button>
      </span>
    )
  }

  return (
    <span className="inline-flex items-center gap-1.5">
      <span className="text-xs italic text-muted-foreground">
        {t('redaction.hidden')}
      </span>
      {allowed && (
        <Button
          variant="ghost"
          size="sm"
          className="h-5 px-1 text-[11px]"
          onClick={() => setRevealed(true)}
        >
          {t('redaction.reveal')}
        </Button>
      )}
    </span>
  )
}
