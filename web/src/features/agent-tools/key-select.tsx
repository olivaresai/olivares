// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The keys a tool can run on, as one select: Add profile and the profile sheet both
// choose a key from it.
import type { ComponentProps } from 'react'
import { useTranslation } from 'react-i18next'
import type { ProviderRecordDTO } from '@/features/providers/types'
import './i18n'

/** The value of the "Add a key…" option: add it, then choose it. */
export const ADD_KEY = ' add'

export function KeySelect({
  records,
  addKey = false,
  ...props
}: Omit<ComponentProps<'select'>, 'children'> & {
  records: readonly ProviderRecordDTO[]
  /** Offer "Add a key…" as the last option. */
  addKey?: boolean
}) {
  const { t } = useTranslation('agentTools')
  return (
    <select
      className="h-9 w-full min-w-0 rounded-md border border-ctl-border bg-background px-3 text-body focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
      {...props}
    >
      <option value="">{t('add.chooseKey')}</option>
      {records.map((r) => (
        <option key={r.provider_ref} value={r.provider_ref}>
          {r.key_hint
            ? `${r.display_name || r.kind} (${r.key_hint})`
            : r.display_name || r.kind}
        </option>
      ))}
      {addKey ? <option value={ADD_KEY}>{t('add.addKey')}</option> : null}
    </select>
  )
}
