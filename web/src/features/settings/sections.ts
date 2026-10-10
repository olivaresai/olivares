// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { TFunction } from 'i18next'
import {
  Info,
  Keyboard,
  KeyRound,
  Package,
  Server,
  Shield,
  SlidersHorizontal,
} from 'lucide-react'
import { fold, type NavSearchEntry } from '@/features/navigation/model'

/** Sections actually offered by Settings; the palette uses the same labels and gate. */
export const SETTINGS_SECTIONS = [
  { id: 'general', icon: SlidersHorizontal, adminOnly: false },
  { id: 'keyboard', icon: Keyboard, adminOnly: false },
  { id: 'signIn', icon: Shield, adminOnly: false },
  { id: 'edition', icon: Package, adminOnly: false },
  { id: 'tracing', icon: Server, adminOnly: true },
  { id: 'signing', icon: KeyRound, adminOnly: true },
  { id: 'about', icon: Info, adminOnly: false },
] as const
export type SettingsSectionId = (typeof SETTINGS_SECTIONS)[number]['id']

export function settingsSections(isSuperadmin: boolean) {
  return SETTINGS_SECTIONS.filter((s) => !s.adminOnly || isSuperadmin)
}

export function settingsSearchEntries(
  t: TFunction,
  isSuperadmin: boolean,
): NavSearchEntry[] {
  const context = t('settings:title')
  return settingsSections(isSuperadmin).map((s, order) => {
    const label = t(`settings:nav.${s.id}`)
    const path = `/settings?section=${s.id}`
    return {
      kind: 'settings',
      id: s.id,
      path,
      icon: s.icon,
      label,
      context,
      description: '',
      areaId: null,
      sectionId: s.id,
      order,
      haystack: {
        label: fold(label),
        aliases: [fold(t(`settings:nav.${s.id}`, { lng: 'en' }))],
        path: fold(path),
        context: [fold(context)],
        description: '',
      },
    }
  })
}
