// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The console offers only what the engine accepts (Root 22:12Z, 26.10.1 privacy): this
// table is recordServesDriver in modules/sessions/provider_record.go (driverfacts CanBind),
// answered from the engine's own tool table (engine-tools.fixture.json, which
// modules/sessions/tools_api_test.go holds to the engine's answer).
import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import engineTools from '@/features/agentops/engine-tools.fixture.json'
import { setEngineTools } from '@/features/agentops/engine-tools'
import {
  LOCAL_HTTP_KINDS,
  OPENCODE_PRESET_ENDPOINTS,
  recordServesDriver,
} from './kinds'

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

  // The engine lists the presets Codex (Responses API only) is refused for; a preset that
  // serves Responses and a record with no preset are offered.
  it('does not offer Codex a preset the engine refuses for it', () => {
    const openai = (url: string, service?: string) =>
      recordServesDriver('openai_compatible', url, 'codex', service)
    expect(openai('https://api.deepseek.com', 'deepseek')).toBe(false)
    expect(openai('https://api.z.ai/api/paas/v4', 'zai')).toBe(false)
    expect(openai('https://openrouter.ai/api/v1', 'openrouter')).toBe(true)
    expect(openai('https://gateway.example/v1')).toBe(true)
  })

  // driverfacts.LocalHTTPEndpoint: OpenCode is held to an OpenAI-compatible server
  // only at a local http address (vLLM, llama.cpp), never to a gateway.
  it.each([
    ['http://127.0.0.1:8080/v1', true],
    ['http://localhost:8000/v1', true],
    ['http://10.0.0.5:8000/v1', true],
    ['http://172.16.0.2:8000/v1', true],
    ['http://192.168.1.9:8000/v1', true],
    ['http://[::1]:8080/v1', true],
    ['http://[fd12:3456:789a::1]:8080/v1', true],
    ['http://172.32.0.2:8000/v1', false],
    ['http://8.8.8.8:8000/v1', false],
    ['http://llm.example.com/v1', false],
    ['https://127.0.0.1:8080/v1', false],
    ['http://user:key@127.0.0.1:8080/v1', false],
    ['http://127.0.0.1:8080/v1?x=1', false],
    ['http://127.0.0.1:8080/v1?', false],
    ['http://127.0.0.1:8080/v1#x', false],
    ['http://LOCALHOST:8080/v1', false],
    ['http://010.0.0.1:8080/v1', false],
    ['http://127.0.0.1:99999999/v1', false],
    ['HTTP://127.0.0.1:8080/v1', true],
    ['', false],
  ])('OpenCode on openai_compatible at %s: %s', (baseURL, expected) => {
    expect(recordServesDriver('openai_compatible', baseURL, 'opencode')).toBe(
      expected,
    )
  })

  it('serves a tool the engine adds on the kinds it lists, with no console change', () => {
    setEngineTools([
      ...engineTools.tools,
      {
        key: 'hermes',
        name: 'Hermes Agent',
        name_stem: 'hermes',
        vendor: 'nous',
        monogram: 'HA',
        color: '#888892',
        session: true,
        bindings: [{ kind: 'gemini', egress: 'vendor-default-only' }],
      },
    ])
    expect(recordServesDriver('gemini', undefined, 'hermes')).toBe(true)
    expect(
      recordServesDriver('gemini', 'https://gateway.example/v1', 'hermes'),
    ).toBe(false)
    expect(recordServesDriver('openai', undefined, 'hermes')).toBe(false)
  })

  it('serves nothing on an endpoint rule it does not know', () => {
    setEngineTools([
      {
        key: 'claude',
        name: 'Claude Code',
        name_stem: 'claude',
        vendor: 'anthropic',
        monogram: 'CC',
        color: '#CA7152',
        session: true,
        bindings: [{ kind: 'anthropic', egress: 'some-future-rule' }],
      },
    ])
    expect(recordServesDriver('anthropic', undefined, 'claude')).toBe(false)
  })

  it('never holds Gemini CLI to a local OpenAI-compatible server', () => {
    expect(
      recordServesDriver(
        'openai_compatible',
        'http://127.0.0.1:8080/v1',
        'gemini-cli',
      ),
    ).toBe(false)
  })

  //: OpenCode ships a native provider for each of these vendors at their own
  // documented address; a BYO key record at exactly that address is held to it.
  it.each(OPENCODE_PRESET_ENDPOINTS)(
    'OpenCode on openai_compatible at the preset catalog address %s',
    (baseURL) => {
      expect(recordServesDriver('openai_compatible', baseURL, 'opencode')).toBe(
        true,
      )
    },
  )

  // The governed DeepSeek row is API-only; an untagged record at its address still runs.
  it('never offers the governed DeepSeek preset record to OpenCode', () => {
    const base = 'https://api.deepseek.com'
    expect(
      recordServesDriver('openai_compatible', base, 'opencode', 'deepseek'),
    ).toBe(false)
    expect(recordServesDriver('openai_compatible', base, 'opencode')).toBe(true)
  })

  it('never holds another driver to a preset catalog address it was not given', () => {
    expect(
      recordServesDriver(
        'openai_compatible',
        OPENCODE_PRESET_ENDPOINTS[0],
        'gemini-cli',
      ),
    ).toBe(false)
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

// OPENCODE_PRESET_ENDPOINTS is every openai_compatible row's own address, read from the
// preset catalog's source (connectors/modelprovider/presets.go), so a row added, moved
// or renamed there fails here instead of silently falling out of sync.
it('OPENCODE_PRESET_ENDPOINTS is the catalog openai_compatible endpoint list', () => {
  const go = readFileSync('../connectors/modelprovider/presets.go', 'utf8')
  const consts = Object.fromEntries(
    [...go.matchAll(/(\w+)\s*=\s*"([^"]+)"/g)].map((m) => [m[1], m[2]]),
  )
  const rows = [
    ...go.matchAll(
      /Ref:\s*(?:"[^"]+"|\w+),\s*Title:\s*"[^"]*",(?:\s*(?:Monogram|Color):\s*"[^"]*",)*\s*Kind:\s*KindOpenAICompatible,\s*BaseURL:\s*(?:"([^"]+)"|(\w+))/g,
    ),
  ]
  expect(rows.length, 'no openai_compatible row in presets.go').toBeGreaterThan(
    0,
  )
  const engine = rows.map((m) => m[1] ?? consts[m[2]])
  expect(engine.every((v): v is string => typeof v === 'string')).toBe(true)
  expect([...engine].sort()).toEqual([...OPENCODE_PRESET_ENDPOINTS].sort())
})
