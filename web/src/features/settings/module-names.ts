// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useTranslation } from 'react-i18next'

/** A module this console has no words for yet is named by its id, readably. */
export function readable(id: string): string {
  const text = id.replace(/[-_]/g, ' ')
  return text.charAt(0).toUpperCase() + text.slice(1)
}

/** A module's name as Settings > Edition & modules shows it; undefined for no module. Kept
 * apart from the Modules screen so a panel's "not enabled" line does not load that screen. */
export function useModuleName(id?: string): string | undefined {
  const { t } = useTranslation('settings')
  return id
    ? t(`modules.names.${id}`, { defaultValue: readable(id) })
    : undefined
}
