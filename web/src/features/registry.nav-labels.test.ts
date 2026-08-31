// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Structural guard: every registered view must have a NAV LABEL in every
// published language.
//
// The whole shell is generated from FEATURE_VIEWS and labelled by a single key
// per view — `nav:items.<id>`. Nothing passes a defaultValue at those call
// sites, so i18next's contract on a miss is to return the key itself, and the
// key is then painted verbatim on screen:
//   - components/layout/sidebar.tsx:154   `t(\`items.${v.id}\`)`  (sidebar entry)
//   - components/layout/topbar.tsx:59     `t(\`items.${id}\`)`    (breadcrumb, every page)
//   - components/layout/command-menu.tsx:159,165 (⌘K label AND its search value)
//   - components/layout/shortcuts.tsx:150 (the g-<key> jump list)
// A view registered without its label therefore shows the operator the literal
// string "items.routinePolicies" on all 50 screens — which is how this guard
// came to exist. Compare with `nav:descriptions.<id>`, which command-menu.tsx:99
// asks for WITH `{ defaultValue: '' }`: a missing description degrades to no
// subtitle, so it is not part of this contract.
//
// The check runs against the locale FILES rather than the i18next store on
// purpose. `fallbackLng: 'en'` (lib/i18n/index.ts:141) means a key missing in
// one language silently resolves to the ENGLISH label — a shipped untranslated
// string, invisible at runtime — and only a key missing in English too degrades
// to the raw identifier. Reading the files sees both failure modes for what
// they are, and names the language that owes the translation.
//
// parity.test.ts and scripts/check-i18n-parity.mjs are the complement, not a
// substitute: they prove the seven languages carry the SAME keys as English,
// which stays true when English is the one missing the key. Only this file
// crosses the registry against the translations.
import { existsSync, readFileSync, readdirSync, statSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import { LANGUAGE_CODES } from '@/lib/i18n'
import { FEATURE_VIEWS } from './registry'

const LOCALES_ROOT = resolve(__dirname, '../lib/i18n/locales')

/**
 * Keys under `nav:items` the shell renders that are NOT registry ids. Declare
 * them here — an undeclared one is reported as drift, and a missing one paints
 * the raw key exactly like a registry id would.
 *
 * `settings` is the static footer entry: sidebar.tsx:171, command-menu.tsx:176
 * and :180, user-menu.tsx:71, plus topbar.tsx:40 which maps /settings to it for
 * the breadcrumb.
 */
const NON_REGISTRY_ITEM_KEYS = ['settings'] as const

/**
 * Every id needing a label — INCLUDING `hideInNav` views. Hidden means absent
 * from the sidebar and the palette, not unreachable: currentViewId()
 * (topbar.tsx:39-51) matches a deep-linked /session-viewer/<id> by base path
 * and labels the breadcrumb with it like any other view.
 */
const REQUIRED_ITEM_KEYS: readonly string[] = [
  ...FEATURE_VIEWS.map((v) => v.id),
  ...NON_REGISTRY_ITEM_KEYS,
]

/** The `items` map of one language's nav.json, straight off disk. */
function navItems(lng: string): Record<string, unknown> {
  const parsed = JSON.parse(
    readFileSync(join(LOCALES_ROOT, lng, 'nav.json'), 'utf8'),
  ) as { items?: Record<string, unknown> }
  return parsed.items ?? {}
}

/** A label that is present but unusable is the same defect as an absent one. */
function isUsableLabel(value: unknown): value is string {
  return typeof value === 'string' && value.trim().length > 0
}

describe('nav labels for every registered view', () => {
  it('every published language ships a nav.json', () => {
    const onDisk = readdirSync(LOCALES_ROOT)
      .filter((e) => statSync(join(LOCALES_ROOT, e)).isDirectory())
      .sort()
    expect(
      onDisk,
      `The locale directories and the published languages (lib/i18n SUPPORTED_LANGUAGES) must be the same set — an unpublished directory is dead weight, and a published language with no directory cannot resolve anything.`,
    ).toEqual([...LANGUAGE_CODES].sort())

    for (const lng of LANGUAGE_CODES)
      expect(
        existsSync(join(LOCALES_ROOT, lng, 'nav.json')),
        `no nav.json for the published language "${lng}"`,
      ).toBe(true)
  })

  it.each([...LANGUAGE_CODES])(
    '"%s" translates every registered view id',
    (lng) => {
      const items = navItems(lng)
      const missing = REQUIRED_ITEM_KEYS.filter(
        (id) => !isUsableLabel(items[id]),
      )
      expect(
        missing,
        `Language "${lng}" has no nav:items entry for: ${missing.join(', ')}\n` +
          `Add each one to web/src/lib/i18n/locales/${lng}/nav.json under "items" ` +
          `(translated into ${lng} — an English copy is a shipped untranslated string).\n` +
          `Until then the console paints the raw key, e.g. "items.${missing[0] ?? '<id>'}", ` +
          `in the sidebar, the breadcrumb of every page and the ⌘K palette.`,
      ).toEqual([])
    },
  )

  it.each([...LANGUAGE_CODES])(
    '"%s" never passes an identifier off as a label',
    (lng) => {
      const items = navItems(lng)
      const raw = REQUIRED_ITEM_KEYS.filter((id) => {
        const value = items[id]
        if (!isUsableLabel(value)) return false // reported by the test above
        // The key path itself — what i18next returns on a miss, pasted in as a
        // "fix". And, for a camelCase or kebab id, the bare id: no language
        // labels a menu entry `routinePolicies` or `workspace-templates`.
        return (
          value === `items.${id}` ||
          value === `nav:items.${id}` ||
          (value === id && /[A-Z-]/.test(id))
        )
      })
      expect(
        raw,
        `Language "${lng}" labels these views with their own identifier: ${raw.join(', ')}\n` +
          `That renders exactly like the missing-key bug it was meant to fix. Write a real label.`,
      ).toEqual([])
    },
  )

  it.each([...LANGUAGE_CODES])(
    '"%s" carries no nav:items key without a view',
    (lng) => {
      const stale = Object.keys(navItems(lng)).filter(
        (id) => !REQUIRED_ITEM_KEYS.includes(id),
      )
      expect(
        stale,
        `Language "${lng}" has nav:items entries no registered view claims: ${stale.join(', ')}\n` +
          `Either the view was removed or renamed (delete/rename the key — a renamed id ` +
          `also FAILS the missing-label test above), or the shell renders it outside the ` +
          `registry, in which case declare it in NON_REGISTRY_ITEM_KEYS with the file:line ` +
          `that renders it.`,
      ).toEqual([])
    },
  )
})
