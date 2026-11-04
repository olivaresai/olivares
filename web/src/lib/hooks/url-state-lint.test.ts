// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The repository's own ESLint config must refuse the two ways a view loses its place on
// reload and Back: an uncontrolled `<Tabs defaultValue>` and a one-time read of
// `window.location.search`. This runs the real config over a sample feature file.
import { ESLint } from 'eslint'
import { describe, expect, it } from 'vitest'

async function restricted(code: string, filePath: string): Promise<string[]> {
  const eslint = new ESLint({ cwd: process.cwd() })
  const [result] = await eslint.lintText(code, { filePath })
  return result.messages
    .filter((m) => m.ruleId === 'no-restricted-syntax')
    .map((m) => m.message)
}

const FEATURE = 'src/features/sample/sample-view.tsx'

describe('feature views keep their place in the address', () => {
  it('refuses an uncontrolled Tabs', async () => {
    const messages = await restricted(
      `export const V = () => <Tabs defaultValue="a"><span /></Tabs>\n`,
      FEATURE,
    )
    expect(messages).toEqual([expect.stringContaining('useTabParam')])
  })

  it('refuses a one-time read of window.location.search', async () => {
    const messages = await restricted(
      `export const f = () => new URLSearchParams(window.location.search).get('tab')\n`,
      FEATURE,
    )
    expect(messages).toEqual([expect.stringContaining('useUrlState')])
  })

  it('accepts a controlled Tabs and a router-backed read', async () => {
    const messages = await restricted(
      `export const V = ({ tab, set }: { tab: string; set: (v: string) => void }) => <Tabs value={tab} onValueChange={set}><span /></Tabs>\n` +
        `export const g = (search: string) => new URLSearchParams(search).get('tab')\n`,
      FEATURE,
    )
    expect(messages).toEqual([])
  })
})
