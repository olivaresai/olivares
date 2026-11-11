// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE TABLE AS THIS PERSON USES IT: the declared table with the keys they chose in
// Settings › Keyboard. The settings store keeps those edits as text and reads nothing of
// the keybindings (the registry's views read it, and the table reads the registry): this
// module reads the store, checks each edit against the table, and is the one way to
// change them. A handler needs nothing from here: `resolveBinding` reads the same edits,
// handed over below, at the keystroke.
import { useClientSettings } from '@/features/settings/preferences'
import {
  checkKeyEdit,
  setPersonalKeys,
  validKeyEdits,
  withOverrides,
  type KeyEditResult,
  type KeyRule,
} from './model'
import { KEYBINDINGS, NAV_LEADER } from './table'

/** The stored edits that work: a stored one that cannot is read as none. Read once per
 * stored value, since every key hint of a render (each row of the palette) asks. */
let read: { stored: object; edits: Record<string, string> } | undefined
const working = (stored: Readonly<Record<string, string>>) => {
  if (read?.stored !== stored)
    read = { stored, edits: validKeyEdits(KEYBINDINGS, stored, NAV_LEADER) }
  return read.edits
}

/** Sets the keys of one row (`ruleId`), or gives it back the table's with null; an edit
 * that cannot work changes nothing and says why. */
export function setKeyOverride(id: string, keys: string | null): KeyEditResult {
  const settings = useClientSettings.getState()
  const edits = working(settings.keyOverrides)
  const checked = checkKeyEdit(KEYBINDINGS, edits, id, keys, NAV_LEADER)
  if (!checked.ok) return checked
  const { [id]: _previous, ...rest } = edits
  const next = keys === null ? rest : { ...rest, [id]: keys }
  if (!settings.replaceKeyOverrides(next))
    return { ok: false, reason: 'tooLarge' }
  return checked
}

// The resolver applies the person's own keys that work: once this module loads (every
// keyboard surface reads it), and on every change of the settings.
setPersonalKeys(working(useClientSettings.getState().keyOverrides))
useClientSettings.subscribe((s, prev) => {
  if (s.keyOverrides !== prev.keyOverrides)
    setPersonalKeys(working(s.keyOverrides))
})

/** The table with the person's own keys; a render follows an edit at once. */
export function useKeybindings(): readonly KeyRule[] {
  const overrides = working(useClientSettings((s) => s.keyOverrides))
  return withOverrides(KEYBINDINGS, overrides)
}

/** The keys that run a command in this table (its first row), or '' when it has none. */
export function keysFor(bindings: readonly KeyRule[], command: string): string {
  return bindings.find((rule) => rule.command === command)?.keys ?? ''
}

/** The keys that run a command now, or '' when it has none. */
export function useCommandKeys(command: string): string {
  return keysFor(useKeybindings(), command)
}
