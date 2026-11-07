// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { render, screen } from '@testing-library/react'
import { beforeEach, describe, expect, it } from 'vitest'
import { setEngineTools, type EngineTool } from './engine-tools'
import { ToolMark } from './tool-mark'

// The engine's row for Gemini CLI (core/driverfacts): its vendor is the `gemini` kind,
// which the console has a vendor mark for.
const geminiCli: EngineTool = {
  key: 'gemini-cli',
  name: 'Gemini CLI',
  name_stem: 'gemini',
  vendor: 'gemini',
  monogram: 'GC',
  color: '#5087E8',
  session: true,
  bindings: [{ kind: 'gemini', egress: 'vendor-default-only' }],
}

describe('ToolMark', () => {
  beforeEach(() => setEngineTools([geminiCli]))

  it("draws Gemini CLI's vendor mark, not its letters", () => {
    render(<ToolMark driver="gemini-cli" labelled />)
    const mark = screen.getByRole('img', { name: 'Gemini CLI' })
    expect(mark).toHaveAttribute('data-vendor', 'googlegemini')
    expect(mark).not.toHaveTextContent('GC')
    expect(mark.querySelector('svg path')).toHaveAttribute(
      'd',
      expect.stringMatching(/^M11\.04 19\.32/),
    )
  })
})
