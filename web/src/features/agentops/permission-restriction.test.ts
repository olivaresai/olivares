// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
import { describe, expect, it } from 'vitest'
import { permissionNameForMode } from './permission-restriction'

describe('permission modes at the picker boundary', () => {
  it.each([
    ['default', 'supervised'],
    ['acceptEdits', 'auto-accept-edits'],
    ['plan', 'read-only'],
    ['auto', 'auto'],
    ['dontAsk', 'full'],
    ['bypassPermissions', 'full'],
    ['unknown', ''],
  ])('names %s', (mode, name) => {
    expect(permissionNameForMode(mode)).toBe(name)
  })
})
