// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
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
  changed,
  onReset,
  control,
  resetLabel,
}: {
  name: string
  description: string
  changed: boolean
  onReset: () => void
  control: ReactNode
  resetLabel?: string
}) {
  return (
    <div className="grid grid-cols-1 items-center gap-3 border-t border-line py-3.5 sm:grid-cols-[minmax(0,1fr)_auto]">
      <div className="min-w-0">
        <div className="flex flex-wrap items-center gap-2 text-body font-medium text-text">
          <span>{name}</span>
          <ChangedReset show={changed} onReset={onReset} label={resetLabel} />
        </div>
        {description ? (
          <p className="text-caption text-text-2">{description}</p>
        ) : null}
      </div>
      <div className="justify-self-start sm:justify-self-end">{control}</div>
    </div>
  )
}
