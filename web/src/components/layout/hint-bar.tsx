// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useTranslation } from 'react-i18next'
import { Kbd } from '@/components/ui/kbd'
import { chordKeys } from '@/lib/keybindings/model'
import { PALETTE_KEYS } from '@/lib/keybindings/table'

/** The console's list affordances, in the order the operator learns them: move,
 * open, decide, launcher, command. Each entry is one hint with its keys.
 * `/` is labelled "launcher" (not "filter") because the published binding it
 * advertises is `keys.command.launcher.focus` — see table.ts. */
const HINTS = [
  { label: 'move', keys: ['j', 'k'] },
  { label: 'open', keys: ['↵'] },
  { label: 'approve', keys: ['a'] },
  { label: 'deny', keys: ['d'] },
  { label: 'launcher', keys: ['/'] },
  // The palette's chord as this platform names it (Ctrl away from a Mac).
  { label: 'command', keys: chordKeys(PALETTE_KEYS ?? 'Mod+k') },
]

/**
 * HintBar — the keyboard affordances of the console, stated where the operator can
 * see them. A quiet strip INSIDE the topbar, not a floating pill: the shell
 * contract refuses chrome that hovers over the work (app-layout.regions.test.tsx
 * forbids anything `fixed` inside the app frame and anything rendered after the
 * work region), and the topbar is where the launcher, notifications and help
 * already live. It shows only on wide viewports — narrow layouts keep their space,
 * and the `?` overlay carries the same keys everywhere. It never takes a click, a
 * tab stop, or a screen reader's time: it is a visual duplicate of that overlay,
 * which IS the accessible surface, so it is `aria-hidden` and `pointer-events-none`.
 */
export function HintBar() {
  const { t } = useTranslation('common')
  return (
    <div
      data-testid="hint-bar"
      aria-hidden="true"
      className="pointer-events-none hidden min-[1280px]:flex shrink-0 items-center gap-3 px-3 text-caption text-muted-foreground"
    >
      {HINTS.map(({ label, keys }) => (
        <span key={label} className="flex items-center gap-1">
          {keys.map((key) => (
            <Kbd key={key}>{key}</Kbd>
          ))}
          <span>{t(`hintBar.${label}`)}</span>
        </span>
      ))}
    </div>
  )
}
