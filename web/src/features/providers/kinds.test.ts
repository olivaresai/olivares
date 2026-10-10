// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The console offers only what the engine accepts (Root 22:12Z, 26.10.1 privacy): this
// table is recordServesDriver in modules/sessions/provider_record.go at FH d4af6c7a. A
// change to either side without the other fails here.
import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import { LOCAL_HTTP_KINDS, recordServesDriver } from './kinds'

const KINDS = [
  'anthropic',
  'openai',
  'xai',
  'gemini',
  'openai_compatible',
  'ollama',
]
const DRIVERS = ['claude', 'codex', 'grok', 'opencode', 'gemini-cli', 'hermes']

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
  'gemini-cli': { plain: ['gemini'], withBaseURL: [] },
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

// The endpoint hint promises plain http at a local address for exactly the kinds the
// engine accepts it for. Read from the engine's source, so a change to the Go rule
// without LOCAL_HTTP_KINDS fails here. The path is relative to vitest's cwd, web/.
it("LOCAL_HTTP_KINDS is the engine's plain-http kind list", () => {
  const go = readFileSync('../modules/sessions/provider_record.go', 'utf8')
  const branches = [
    ...go.matchAll(
      /if \(([^)]*)\) && strings\.HasPrefix\(lower, "http:\/\/"\)/g,
    ),
  ]
  // A renamed branch is "could not look", never a pass. Every branch, not only
  // the first: a rule split across two conditions must widen, never shrink, the
  // kinds the hint promises.
  expect(
    branches.length,
    'no plain-http branch in validProviderBaseURL',
  ).toBeGreaterThan(0)
  const value = Object.fromEntries(
    [...go.matchAll(/(ProviderKind\w+)\s*=\s*"([^"]+)"/g)].map((m) => [
      m[1],
      m[2],
    ]),
  )
  const engine = [
    ...new Set(
      branches.flatMap(([, cond]) =>
        [...cond.matchAll(/kind == (ProviderKind\w+)/g)].map(
          (m) => value[m[1]],
        ),
      ),
    ),
  ]
  expect(engine.length).toBeGreaterThan(0)
  expect([...engine].sort()).toEqual([...LOCAL_HTTP_KINDS].sort())
})
