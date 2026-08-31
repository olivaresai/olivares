// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useTranslation } from 'react-i18next'
import { cn } from '@/lib/utils'
import {
  MIN_DR_PASSPHRASE_LENGTH,
  passphraseBelowFloor,
  passphraseStrength,
} from '@/lib/passphrase'

/**
 * PassphraseStrength — the client mirror of the backend DR-passphrase policy
 * (core/api/dr_handler.go): a Field error while the value is under the
 * ≥12-character floor, plus an honest 4-segment strength hint once it passes.
 * Render it directly under the passphrase <Input> when creating a DR backup.
 * Empty input renders nothing — "required" is the form's own error.
 */
export function PassphraseStrength({
  value,
  className,
}: {
  value: string
  className?: string
}) {
  const { t } = useTranslation('common')
  if (!value) return null

  if (passphraseBelowFloor(value)) {
    return (
      <p
        role="alert"
        className={cn('text-xs text-danger', className)}
        data-testid="passphrase-floor-error"
      >
        {t('passphrase.tooShort', { min: MIN_DR_PASSPHRASE_LENGTH })}
      </p>
    )
  }
  const strength = passphraseStrength(value)
  const level = { weak: 1, fair: 2, good: 3, strong: 4 }[strength]
  return (
    <div className={cn('flex items-center gap-2', className)}>
      <div
        role="meter"
        aria-label={t('passphrase.strengthLabel')}
        aria-valuemin={1}
        aria-valuemax={4}
        aria-valuenow={level}
        aria-valuetext={t(`passphrase.strength.${strength}`)}
        className="flex flex-1 gap-1"
      >
        {[1, 2, 3, 4].map((seg) => (
          <span
            key={seg}
            className={cn(
              'h-1 flex-1 rounded-full',
              seg <= level
                ? level <= 2
                  ? 'bg-warning'
                  : 'bg-success'
                : 'bg-muted',
            )}
          />
        ))}
      </div>
      <span className="text-xs text-muted-foreground">
        {t(`passphrase.strength.${strength}`)}
      </span>
    </div>
  )
}
