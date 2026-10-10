// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Sidebar label translations. The documentation sidebar in astro.config.mjs
// uses explicit English labels; this attaches per-locale `translations` to each
// one so the navigation localizes alongside the page content. Leaf entries that
// rely on `autogenerate` localize automatically from each locale's page title.
//
// Machine-translated; English is authoritative (native review pending). Generated
// from the docs-i18n translation pass — to regenerate, re-run that pass. A label
// with no entry (or a missing locale) falls back to the English label.
//
// Keys are Starlight `translations` keys and MUST be each locale's BCP-47 `lang`
// from src/site-locales.mjs — for Chinese that is "zh-CN", not "zh" (with
// "zh" keys every zh page silently fell back to the English sidebar).

import { readFileSync } from 'node:fs'
import { PUBLISHED_LOCALES } from './site-locales.mjs'

/** @type {Record<string, Record<string, string>>} */
export const SIDEBAR_LABELS = {}
for (const locale of PUBLISHED_LOCALES) {
  const labels = JSON.parse(readFileSync(new URL(`./locales/${locale}.json`, import.meta.url), 'utf8'))
  for (const [label, translations] of Object.entries(labels)) {
    SIDEBAR_LABELS[label] ??= {}
    Object.assign(SIDEBAR_LABELS[label], translations)
  }
}

/**
 * Recursively attach `translations` to sidebar items whose English label has a
 * mapping in SIDEBAR_LABELS. Items without a string label (the OpenAPI sidebar
 * group placeholder, `autogenerate` directives) are returned untouched so their
 * identity and plugin wiring are preserved.
 *
 * @param {any[]} items
 * @returns {any[]}
 */
export function localizeSidebar(items) {
  return items.map((item) => {
    if (typeof item.label !== 'string') return item
    const next = { ...item }
    const map = SIDEBAR_LABELS[item.label]
    if (map && Object.keys(map).length > 0) next.translations = map
    if (Array.isArray(item.items)) next.items = localizeSidebar(item.items)
    return next
  })
}
