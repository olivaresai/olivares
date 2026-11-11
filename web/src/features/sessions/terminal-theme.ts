// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The terminal wears the console's own colors, and only those: every color is a design
// token of the theme in front, read when the terminal opens and again when the theme
// changes, so Light, Dark and Black each get the contrast the tokens were measured for.
// A program that prints red, green, yellow or blue gets the danger, success, warning and
// info colors. A token that cannot be read leaves xterm's own default for that color.
import type { ITheme } from '@xterm/xterm'

function token(name: string): string | undefined {
  const value = getComputedStyle(document.documentElement)
    .getPropertyValue(name)
    .trim()
  return value || undefined
}

/** The console's mono face, loaded, so the screen measures its cells with the right glyphs. */
export async function terminalFont(): Promise<string | undefined> {
  // The face is a Tailwind theme value, not a variable on the page: an element wearing it says.
  const probe = document.createElement('span')
  probe.className = 'font-mono'
  document.body.appendChild(probe)
  const family = getComputedStyle(probe).fontFamily.trim()
  probe.remove()
  if (!family) return undefined
  await Promise.resolve(document.fonts?.load(`13px ${family}`)).catch(
    () => undefined, // It is drawn in the fallback face of the same stack.
  )
  return family
}

export function terminalTheme(): ITheme {
  // The screen is the well the drawer is; the frame until the console has one.
  const background = token('--well') ?? token('--frame')
  const foreground = token('--foreground')
  const muted = token('--muted')
  const dim = token('--muted-foreground')
  const danger = token('--danger')
  const success = token('--success')
  const warning = token('--warning')
  const info = token('--info')
  const accent = token('--accent-text')
  return {
    background,
    foreground,
    cursor: accent,
    cursorAccent: background,
    // xterm draws an opaque selection at 30 %: of the orange, like the console's ::selection.
    selectionBackground: token('--accent'),
    black: muted,
    red: danger,
    green: success,
    yellow: warning,
    blue: info,
    magenta: accent,
    cyan: info,
    white: dim,
    brightBlack: dim,
    brightRed: danger,
    brightGreen: success,
    brightYellow: warning,
    brightBlue: info,
    brightMagenta: accent,
    brightCyan: info,
    brightWhite: foreground,
  }
}
