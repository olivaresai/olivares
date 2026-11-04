// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it } from 'vitest'
import { useObjectActionsStore } from './object-actions'

describe('useObjectActionsStore', () => {
  it('holds the one selected object with its actions, and clears', () => {
    useObjectActionsStore.setState({ current: null })
    const run = () => {}
    const selection = {
      label: 'Marc · Codex',
      actions: [{ id: 'approve', label: 'Approve', shortcut: 'A', run }],
    }
    useObjectActionsStore.getState().set(selection)
    expect(useObjectActionsStore.getState().current).toBe(selection)
    useObjectActionsStore.getState().set(null)
    expect(useObjectActionsStore.getState().current).toBeNull()
  })
})
