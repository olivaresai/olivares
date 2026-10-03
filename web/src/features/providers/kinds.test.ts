// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The console offers only what the engine accepts (Root 22:12Z, 26.10.1 privacy): this
// table is recordServesDriver in modules/sessions/provider_record.go at FH d4af6c7a. A
// change to either side without the other fails here.
import { describe, expect, it } from 'vitest'
import { recordServesDriver } from './kinds'

const KINDS = ['anthropic', 'openai', 'xai', 'openai_compatible', 'ollama']
const DRIVERS = ['claude', 'codex', 'grok', 'opencode', 'hermes']

// What each driver runs on, with no base URL and with one (a gateway or a custom address).
const SERVES: Record<string, { plain: string[]; withBaseURL: string[] }> = {
  claude: { plain: ['anthropic'], withBaseURL: ['anthropic'] },
  codex: {
    plain: ['openai', 'openai_compatible', 'ollama'],
    withBaseURL: ['openai', 'openai_compatible', 'ollama'],
  },
  grok: { plain: ['xai'], withBaseURL: ['xai'] },
  // OpenCode is held to a vendor key only at the vendor's own address.
  opencode: {
    plain: ['anthropic', 'openai', 'xai', 'ollama'],
    withBaseURL: ['ollama'],
  },
  // An unknown driver has no established way to be held to a record's endpoint.
  hermes: { plain: [], withBaseURL: [] },
}

describe("the engine's rule for which records a tool runs on", () => {
  for (const driver of DRIVERS)
    for (const kind of KINDS)
      for (const baseURL of ['', 'https://gateway.example/v1']) {
        const expected = (
          baseURL ? SERVES[driver].withBaseURL : SERVES[driver].plain
        ).includes(kind)
        it(`${driver} on ${kind}${baseURL ? ' with a base URL' : ''}: ${expected}`, () => {
          expect(recordServesDriver(kind, baseURL, driver)).toBe(expected)
        })
      }

  // The profile form's typed driver name, as the console read it before.
  it('reads a typed driver name trimmed and lower-cased', () => {
    expect(recordServesDriver('anthropic', undefined, ' Claude ')).toBe(true)
  })
})
