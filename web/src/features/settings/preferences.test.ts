// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE PERSON'S SETTINGS keep the scripts they add to a folder: a stored value is read
// as untrusted, and restoring the preferences leaves the scripts in place.
import { beforeEach, expect, it } from 'vitest'
import { preferenceStorage } from '@/lib/preference-storage'
import { setKeyOverride } from '@/lib/keybindings/effective'
import { resolveBinding } from '@/lib/keybindings/model'
import { KEYBINDINGS } from '@/lib/keybindings/table'
import { CLIENT_SETTING_DEFAULTS, useClientSettings } from './preferences'

const lint = { name: 'lint', command: 'pnpm lint' }

beforeEach(() => {
  preferenceStorage.attach(null, {})
  useClientSettings.setState({ ...CLIENT_SETTING_DEFAULTS })
})

it('reads only well-formed scripts from a stored value', async () => {
  preferenceStorage.attach(null, {
    'olivares.settings': JSON.stringify({
      state: {
        projectScripts: {
          'ws-main': [lint, { name: 'build', command: 3 }, null, 'pnpm test'],
          'ws-other': 'pnpm dev',
        },
      },
      version: 0,
    }),
  })
  await useClientSettings.persist.rehydrate()
  expect(useClientSettings.getState().projectScripts).toEqual({
    'ws-main': [lint],
    'ws-other': [],
  })
})

it('restoring the preferences keeps the scripts a person added', () => {
  useClientSettings.getState().setProjectScripts('ws-main', [lint])
  useClientSettings.getState().setClock('12')
  useClientSettings.getState().restore()
  expect(useClientSettings.getState().clock).toBe('24')
  expect(useClientSettings.getState().projectScripts).toEqual({
    'ws-main': [lint],
  })
})

// Settings' «Restore all defaults» calls `restore()`: every row takes the table's keys.
it('restoring the preferences gives every row back the keys of the table', () => {
  setKeyOverride('palette.open|', 'Mod+p')
  useClientSettings.getState().restore()
  expect(useClientSettings.getState().keyOverrides).toEqual({})
  expect(resolveBinding(KEYBINDINGS, { key: 'k', ctrlKey: true })).toBe(
    'palette.open',
  )
})
