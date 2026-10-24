// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it } from 'vitest'
import { monogram } from './tool-mark'

describe('monogram', () => {
  it.each([
    ['Claude Code', 'CC'],
    ['Gemini CLI', 'GC'],
    ['Grok Build', 'GB'],
    ['OpenCode', 'OC'],
    ['Codex', 'CO'],
  ])('draws %s as %s', (name, mark) => {
    expect(monogram(name)).toBe(mark)
  })
})
