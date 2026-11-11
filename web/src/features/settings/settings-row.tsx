// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { BoxedListRow } from '@/components/ui/boxed-list'
import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

export function ChangedReset({
  show,
  onReset,
  label,
}: {
  show: boolean
  onReset: () => void
  label?: string
}) {
  const { t } = useTranslation('settings')
  if (!show) return null
  return (
    <button
      type="button"
      onClick={onReset}
      aria-label={label}
      className="inline-flex items-center gap-1 text-caption font-medium text-accent-text"
    >
      {t('changedReset')}
    </button>
  )
}

export function SettingsRow({
  name,
  description,
  changed = false,
  onReset,
  control,
  resetLabel,
  descriptionId,
}: {
  name: string
  description: string
  /** A row with no default to return to passes neither. */
  changed?: boolean
  onReset?: () => void
  control: ReactNode
  resetLabel?: string
  /** The sentence's id, for a control it describes (`aria-describedby`). */
  descriptionId?: string
}) {
  return (
    <BoxedListRow className="grid grid-cols-1 sm:grid-cols-[minmax(0,1fr)_auto]">
      <div className="min-w-0">
        <div className="flex flex-wrap items-center gap-2 text-body font-medium text-text">
          <span>{name}</span>
          {onReset ? (
            <ChangedReset show={changed} onReset={onReset} label={resetLabel} />
          ) : null}
        </div>
        {description ? (
          <p id={descriptionId} className="text-caption text-text-2">
            {description}
          </p>
        ) : null}
      </div>
      <div className="justify-self-start sm:justify-self-end">{control}</div>
    </BoxedListRow>
  )
}
