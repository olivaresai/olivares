// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Recordings and its session viewer are Business pages (docs/editions.md). The Community tree
// carries neither their source nor an entry for them, and answers their bookmarks with the
// Business notice (route-census.json business_paths); a Business build brings both through
// its extensions module.
import { existsSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'
import { BUSINESS_BUILD } from './business-build'
import { FEATURE_EXTENSIONS, RECORDING_NOTICE_EXTENSION } from './extensions'
import census from './route-census.json'
import { FEATURE_VIEWS } from './registry'

const PAGES = [
  ['recordings', '/recordings'],
  ['session-viewer', '/session-viewer/$id'],
] as const

describe('Business recordings console entries', () => {
  for (const [id, path] of PAGES) {
    it(`${id} is registered only by the paid console extension`, () => {
      const entry = FEATURE_VIEWS.find((view) => view.id === id)
      const extension = FEATURE_EXTENSIONS.find((view) => view.id === id)

      expect(census.business_paths).toContain(path)
      expect(entry?.path).toBe(extension?.path)
      if (BUSINESS_BUILD) expect(entry?.path).toBe(path)
      else expect(entry).toBeUndefined()
    })

    it(`${id} keeps its source out of the Community tree`, () => {
      expect(existsSync(join(__dirname, id))).toBe(BUSINESS_BUILD)
    })
  }

  it('offers the monitoring notice only where a Business build provides it', () => {
    expect(RECORDING_NOTICE_EXTENSION !== undefined).toBe(BUSINESS_BUILD)
  })
})
