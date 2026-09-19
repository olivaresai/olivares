// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// EVERY SCREEN ON THE SHARED ROW HANGS ITS PHONE PANEL FROM THAT ROW. Beside a tab strip
// the header is only as wide as the screen's name; a panel of collapsed controls hung from
// the header starts mid-screen and runs off the left edge of a phone (measured in a browser:
// two screens of four). The prop that prevents it is set per screen, so a screen that adopts
// the row and forgets the prop stays green everywhere except here.
import { readFileSync, readdirSync, statSync } from 'node:fs'
import { join, relative } from 'node:path'
import { describe, expect, it } from 'vitest'

const SRC = join(__dirname, '..', '..')

function sources(dir: string, out: string[] = []): string[] {
  for (const name of readdirSync(dir)) {
    const path = join(dir, name)
    if (statSync(path).isDirectory()) sources(path, out)
    else if (/\.tsx$/.test(name) && !/\.test\.tsx$/.test(name)) out.push(path)
  }
  return out
}

describe('the row a header shares with a tab strip', () => {
  const users = sources(SRC).filter((path) => {
    const text = readFileSync(path, 'utf8')
    return /className=\{[^}]*WORK_CHROME_ROW/.test(text)
  })

  it('is used by the tabbed screens this census knows', () => {
    expect(users.map((path) => relative(SRC, path)).sort()).toEqual([
      'features/agentops/provider-admin-view.tsx',
      'features/console/console-view.tsx',
      'features/sessions/sessions-workspace-view.tsx',
    ])
  })

  it('always comes with the panel hung from the row', () => {
    const missing = users.filter(
      (path) => !/actionsPanelAnchor="row"/.test(readFileSync(path, 'utf8')),
    )
    expect(missing.map((path) => relative(SRC, path))).toEqual([])
  })
})
