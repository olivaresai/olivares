// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import i18n from 'i18next'
import { beforeAll, describe, expect, it } from 'vitest'
import { BUSINESS_ONLY_VIEW_IDS, FEATURE_VIEWS } from '@/features/registry'
import {
  SETTINGS_SECTIONS,
  settingsSearchEntries,
  settingsSections,
} from './sections'

// The sections that host another page of the console (#2905), and the page each hosts.
const HOSTED = [
  ['aiTools', 'providers'],
  ['workspaces', 'workspaceDashboard'],
  ['setup', 'onboarding'],
] as const

describe('settings sections that host a page', () => {
  beforeAll(async () => {
    await i18n.changeLanguage('en')
  })

  it.each(HOSTED)(
    'offers %s to a person who may open %s, in the list and in the palette',
    (id) => {
      const gate = () => true
      expect(settingsSections(false, gate).map((s) => s.id)).toContain(id)
      const entry = settingsSearchEntries(i18n.t, false, gate).find(
        (e) => e.sectionId === id,
      )
      // The palette opens the section inside Settings, never the page it hosts.
      expect(entry?.path).toBe(`/settings?section=${id}`)
    },
  )

  it.each(HOSTED)(
    'withholds %s from the list and the palette when %s is not navigable',
    (id, view) => {
      const gate = (v: { id: string }) => v.id !== view
      expect(settingsSections(true, gate).map((s) => s.id)).not.toContain(id)
      expect(
        settingsSearchEntries(i18n.t, true, gate).map((e) => e.sectionId),
      ).not.toContain(id)
    },
  )

  // Settings gates a hosted page by navigable() alone, which treats an unknown capability
  // answer as open; the route gate (RequirePermission) is stricter. A page that declares a
  // capability question or a continuity boundary must keep its route gate, so it may not be hosted.
  it('hosts only pages that declare no capability question and no continuity boundary', () => {
    const entries: readonly { id: string; view?: string }[] = SETTINGS_SECTIONS
    const hosted = entries.filter((s) => s.view)
    expect(hosted.map((s) => s.id)).toEqual([
      'aiTools',
      'workspaces',
      'deploy',
      'setup',
    ])
    for (const { id, view } of hosted) {
      // Deployment is a Business page: a Community registry does not hold it.
      const page = FEATURE_VIEWS.find((v) => v.id === view)
      expect(
        page !== undefined || BUSINESS_ONLY_VIEW_IDS.includes(view!),
        `${id}: ${view} is neither registered nor a Business-only page`,
      ).toBe(true)
      expect(page?.capability, id).toBeUndefined()
      expect(page?.continuity, id).toBeUndefined()
    }
  })

  it('keeps the plain sections whatever the gate answers', () => {
    const none = () => false
    expect(settingsSections(false, none).map((s) => s.id)).toEqual([
      'general',
      'keyboard',
      'signIn',
      'edition',
      'about',
    ])
  })
})
