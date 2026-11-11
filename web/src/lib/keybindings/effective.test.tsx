// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE PERSON'S KEYS ARE READ ONCE PER STORED VALUE: every key hint of a render (each row of
// the palette) asks for the table as the person uses it, so the check against the table
// runs when the stored edits change, not on every render.
import { renderHook } from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'

vi.mock('./model', async (importOriginal) => {
  const model = await importOriginal<typeof import('./model')>()
  return { ...model, validKeyEdits: vi.fn(model.validKeyEdits) }
})

import { validKeyEdits } from './model'
import { useKeybindings } from './effective'
import { useClientSettings } from '@/features/settings/preferences'

afterEach(() => useClientSettings.setState({ keyOverrides: {} }))

it('reads the stored edits against the table once for one stored value, however many renders', () => {
  vi.mocked(validKeyEdits).mockClear()
  useClientSettings.setState({ keyOverrides: { 'palette.open|': 'Mod+p' } })
  const { result, rerender } = renderHook(() => useKeybindings())
  rerender()
  expect(
    result.current.find((rule) => rule.command === 'palette.open')?.keys,
  ).toBe('Mod+p')
  expect(validKeyEdits).toHaveBeenCalledTimes(1)
})
