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

export function terminalTheme(): ITheme {
  const background = token('--background')
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
    selectionBackground: token('--accent-soft'),
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
