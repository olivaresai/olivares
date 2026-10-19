// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useTranslation } from 'react-i18next'

const LEVELS = ['read', 'write', 'admin'] as const

/**
 * The level a permission id grants, in the words the role editor's columns use. The id
 * is `<kind>:<verb>` and the verb is its last segment; a verb outside read, write and
 * admin is named "more access" rather than guessed. This is display only: whether the
 * principal holds the permission is `can()`, never this.
 */
export function usePermissionWords(): (permission: string) => string {
  const { t } = useTranslation('common')
  return (permission) => {
    const verb = permission.slice(permission.lastIndexOf(':') + 1)
    const level = LEVELS.find((l) => l === verb) ?? 'other'
    return t(`permissionWords.${level}`)
  }
}
