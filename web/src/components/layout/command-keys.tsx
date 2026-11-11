// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { Kbd } from '@/components/ui/kbd'
import { useCommandKeys } from '@/lib/keybindings/effective'
import { chordKeys } from '@/lib/keybindings/model'

/** The keys that run a command, at the end of the menu item or the button that does the
 * same: the keys that fire now, a person's own from Settings › Keyboard. Drawn, not
 * read: the name stays its words, and the keyboard help lists every key. Not
 * `DropdownMenuShortcut`: that is a menu's part and draws the keys as one muted text
 * run, where each key here is its own `Kbd` cap, in a menu item and in a button alike. */
export function CommandKeys({ command }: { command: string }) {
  const keys = chordKeys(useCommandKeys(command))
  if (keys.length === 0) return null
  return (
    <span aria-hidden className="ms-auto flex shrink-0 gap-1">
      {keys.map((key) => (
        <Kbd key={key}>{key.length === 1 ? key.toUpperCase() : key}</Kbd>
      ))}
    </span>
  )
}
