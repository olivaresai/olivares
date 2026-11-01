// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { TFunction } from 'i18next'
import {
  Info,
  Bell,
  Keyboard,
  KeyRound,
  Package,
  Play,
  Server,
  Shield,
  SlidersHorizontal,
  type LucideIcon,
} from 'lucide-react'
import {
  fold,
  viewById,
  type NavSearchEntry,
  type ViewGate,
} from '@/features/navigation/model'

export interface SettingsSection {
  id: string
  icon: LucideIcon
  adminOnly: boolean
  /** The console page this section hosts (a registry id): Settings shows that page's own
   * element inside itself, and offers the section only where the page opens (#2905). */
  view?: string
}

/** Sections actually offered by Settings; the palette uses the same labels and gate. */
export const SETTINGS_SECTIONS = [
  { id: 'general', icon: SlidersHorizontal, adminOnly: false },
  { id: 'newSessions', icon: Play, adminOnly: false },
  { id: 'notifications', icon: Bell, adminOnly: false },
  { id: 'keyboard', icon: Keyboard, adminOnly: false },
  { id: 'permissions', icon: Shield, adminOnly: false },
  { id: 'signIn', icon: Shield, adminOnly: false },
  { id: 'setup', icon: Play, adminOnly: false, view: 'onboarding' },
  // The License place of the sidebar (features/navigation/places.ts), not a row of Settings.
  { id: 'edition', icon: Package, adminOnly: false },
  { id: 'sessionIdle', icon: Server, adminOnly: true },
  { id: 'tracing', icon: Server, adminOnly: true },
  { id: 'signing', icon: KeyRound, adminOnly: true },
  { id: 'about', icon: Info, adminOnly: false },
] as const satisfies readonly SettingsSection[]
export type SettingsSectionId = (typeof SETTINGS_SECTIONS)[number]['id']

/**
 * Sections that repeated a place of the sidebar (0.5 one navigation): AI tools, Files &
 * workspaces and Operations hold them now. An old `/settings?section=<id>` lands on the page
 * that holds it, with the rest of its query (the wizard's `add` and `returnTo`) and its hash.
 */
export const MOVED_SECTIONS: Readonly<Record<string, string>> = {
  aiTools: '/providers',
  workspaces: '/workspace',
  deploy: '/deploy',
  updates: '/updates',
}

const ALL_SECTIONS: readonly SettingsSection[] = SETTINGS_SECTIONS

/** What this person is offered: administrator sections for a system administrator, and a
 * hosted page only where it opens (the gate the sidebar uses, #474). */
export function settingsSections(
  isSuperadmin: boolean,
  navigable: ViewGate,
): readonly SettingsSection[] {
  return ALL_SECTIONS.filter((s) => {
    if (s.adminOnly && !isSuperadmin) return false
    const view = s.view ? viewById(s.view) : undefined
    return !s.view || (view !== undefined && navigable(view))
  })
}

export function settingsSearchEntries(
  t: TFunction,
  isSuperadmin: boolean,
  navigable: ViewGate,
): NavSearchEntry[] {
  const context = t('settings:title')
  return settingsSections(isSuperadmin, navigable).map((s, order) => {
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
      sectionId: s.id,
      order,
      haystack: {
        label: fold(label),
        aliases: [
          fold(t(`settings:nav.${s.id}`, { lng: 'en' })),
          // The palette builds its haystack from eagerly loaded namespaces only,
          // so the license word lives here — not in the console bundle.
          ...(s.id === 'edition'
            ? [
                fold(t('settings:edition.licenseAlias')),
                fold(t('settings:edition.licenseAlias', { lng: 'en' })),
              ]
            : []),
        ],
        path: fold(path),
        context: [fold(context)],
        description: '',
      },
    }
  })
}
