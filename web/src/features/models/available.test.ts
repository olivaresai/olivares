// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The rows of Models > Available: what each tool and provider can run now, and the live
// sessions that run it. The rule under test is the join: a row's sessions are the live
// runs whose tool and model it names, each run on one row only.
import { describe, expect, it } from 'vitest'
import type {
  ProfileModelCatalogDTO,
  ProfileModelDTO,
  RunDTO,
} from '@/features/agentops/types'
import type { OllamaStatus } from '@/features/agent-tools/api'
import { buildAvailableRows } from './available'

function catalog(
  state: ProfileModelCatalogDTO['state'],
  models: Partial<ProfileModelDTO>[],
): ProfileModelCatalogDTO {
  return {
    profile_ref: 'p1',
    driver: 'x',
    state,
    models: models.map((m) => ({ id: 'm', ...m })),
    default_model: '',
    free_text: state !== 'fresh',
    checked_at: '2026-10-23T10:00:00Z',
    refresh_interval_seconds: 120,
  }
}

function run(over: Partial<RunDTO>): RunDTO {
  return {
    run_ref: 'r1',
    state: 'running',
    provider_driver: 'opencode',
    model_ref: '',
    ...over,
  } as RunDTO
}

const OLLAMA_UP: OllamaStatus = {
  installed: true,
  state: 'running',
  endpoint: 'http://127.0.0.1:11434',
  models: ['qwen3:8b', 'llama3.2:3b'],
}

const API_PROVIDER = {
  source_ref: 'provider:1',
  provider_ref: 'provider:1',
  provider_kind: 'openai_compatible',
  state: 'fresh',
  models: [{ id: 'api-stub-model', seen_at: '2026-10-25T10:00:00Z' }],
}

describe('buildAvailableRows', () => {
  it('lists one row per model a tool reports now', () => {
    const rows = buildAvailableRows({
      catalogs: [
        {
          driver: 'claude',
          catalog: catalog('fresh', [
            { id: 'opus', display_name: 'Opus' },
            { id: 'sonnet' },
          ]),
        },
      ],
      availability: [],
      runs: [],
    })
    expect(rows.map((r) => [r.source, r.owner, r.model, r.state])).toEqual([
      ['tool', 'claude', 'opus', 'ready'],
      ['tool', 'claude', 'sonnet', 'ready'],
    ])
    expect(rows[0].label).toBe('Opus')
    expect(rows[1].label).toBe('sonnet')
  })

  it('lists a model once when two accounts of one tool report it', () => {
    const same = catalog('fresh', [{ id: 'opus' }])
    const rows = buildAvailableRows({
      catalogs: [
        { driver: 'claude', catalog: same },
        { driver: 'claude', catalog: same },
      ],
      availability: [],
      runs: [],
    })
    expect(rows).toHaveLength(1)
  })

  it('says a tool that does not list its models, in one row without a model', () => {
    const rows = buildAvailableRows({
      catalogs: [
        { driver: 'codex', catalog: catalog('unsupported', []) },
        { driver: 'opencode', catalog: catalog('failed', []) },
      ],
      availability: [],
      runs: [],
    })
    expect(rows.map((r) => [r.owner, r.model, r.state])).toEqual([
      ['codex', '', 'unlisted'],
      ['opencode', '', 'failed'],
    ])
  })

  it('lists the models Ollama has installed, ready only while it runs', () => {
    const up = buildAvailableRows({
      catalogs: [],
      ollama: OLLAMA_UP,
      availability: [],
      runs: [],
    })
    expect(up.map((r) => [r.source, r.model, r.state])).toEqual([
      ['ollama', 'qwen3:8b', 'ready'],
      ['ollama', 'llama3.2:3b', 'ready'],
    ])
  })

  it('lists one Ollama row without a model while an installed Ollama is not running', () => {
    // The engine fills `models` only while Ollama runs (agenttoolsapi/ollama.go): stopped,
    // starting and failed arrive with an empty list.
    const state = (s: OllamaStatus['state']) =>
      buildAvailableRows({
        catalogs: [],
        ollama: { installed: true, state: s, models: [] },
        availability: [],
        runs: [],
      }).map((r) => [r.source, r.owner, r.model, r.state])
    expect(state('stopped')).toEqual([['ollama', 'ollama', '', 'stopped']])
    expect(state('starting')).toEqual([['ollama', 'ollama', '', 'starting']])
    expect(state('failed')).toEqual([['ollama', 'ollama', '', 'ollamaFailed']])
  })

  it('lists no Ollama row while Ollama runs with no model installed', () => {
    const rows = buildAvailableRows({
      catalogs: [],
      ollama: { installed: true, state: 'running', models: [] },
      availability: [],
      runs: [],
    })
    expect(rows).toEqual([])
  })

  it('lists no Ollama row when Ollama is not installed', () => {
    const rows = buildAvailableRows({
      catalogs: [],
      ollama: { installed: false, state: 'stopped', models: ['ghost'] },
      availability: [],
      runs: [],
    })
    expect(rows).toEqual([])
  })

  it('lists an API provider from configured availability before any model was used', () => {
    const rows = buildAvailableRows({
      catalogs: [],
      availability: [API_PROVIDER],
      runs: [],
    })
    expect(rows).toHaveLength(1)
    expect(rows[0]).toMatchObject({
      source: 'provider',
      owner: 'provider:1',
      model: 'api-stub-model',
      state: 'fresh',
    })
  })

  it('dedupes only the provider record at the installed Ollama endpoint', () => {
    const rows = buildAvailableRows({
      catalogs: [],
      ollama: OLLAMA_UP,
      providers: [
        {
          provider_ref: 'provider:1',
          kind: 'ollama',
          display_name: 'Ollama on this server',
          base_url: 'http://127.0.0.1:11434',
          state: 'active',
        },
      ],
      availability: [
        {
          ...API_PROVIDER,
          provider_kind: 'ollama',
          models: [
            { id: 'qwen3:8b', seen_at: '2026-10-25T10:00:00Z' },
            { id: 'mistral:7b', seen_at: '2026-10-25T10:00:00Z' },
          ],
        },
      ],
      runs: [],
    })
    expect(rows.map((r) => [r.source, r.model])).toEqual([
      ['ollama', 'qwen3:8b'],
      ['ollama', 'llama3.2:3b'],
      ['provider', 'mistral:7b'],
    ])
  })

  it('keeps the same model id on an Ollama provider at another endpoint', () => {
    const rows = buildAvailableRows({
      catalogs: [],
      ollama: OLLAMA_UP,
      providers: [
        {
          provider_ref: 'provider:1',
          kind: 'ollama',
          display_name: 'Remote Ollama',
          base_url: 'http://192.0.2.10:11434',
          state: 'active',
        },
      ],
      availability: [
        {
          ...API_PROVIDER,
          provider_kind: 'ollama',
          models: [{ id: 'qwen3:8b', seen_at: '2026-10-25T10:00:00Z' }],
        },
      ],
      runs: [],
    })
    expect(rows.map((r) => [r.source, r.owner, r.model])).toEqual([
      ['ollama', 'ollama', 'qwen3:8b'],
      ['ollama', 'ollama', 'llama3.2:3b'],
      ['provider', 'Remote Ollama', 'qwen3:8b'],
    ])
  })

  it.each(['fresh', 'stale', 'failed'])(
    'gives no row to a %s provider with no models at the installed Ollama endpoint',
    (state) => {
      // The Ollama rows (or the Ollama empty state) already speak for that endpoint.
      const rows = buildAvailableRows({
        catalogs: [],
        ollama: { ...OLLAMA_UP, models: [] },
        providers: [
          {
            provider_ref: 'provider:1',
            kind: 'ollama',
            display_name: 'Ollama on this server',
            base_url: 'http://127.0.0.1:11434',
            state: 'active',
          },
        ],
        availability: [
          { ...API_PROVIDER, provider_kind: 'ollama', state, models: [] },
        ],
        runs: [],
      })
      expect(rows).toEqual([])
    },
  )

  it('keeps the row of an empty provider at another Ollama endpoint', () => {
    const rows = buildAvailableRows({
      catalogs: [],
      ollama: OLLAMA_UP,
      providers: [
        {
          provider_ref: 'provider:1',
          kind: 'ollama',
          display_name: 'Remote Ollama',
          base_url: 'http://192.0.2.10:11434',
          state: 'active',
        },
      ],
      availability: [
        {
          ...API_PROVIDER,
          provider_kind: 'ollama',
          state: 'stale',
          models: [],
        },
      ],
      runs: [],
    })
    expect(
      rows
        .filter((r) => r.source === 'provider')
        .map((r) => [r.owner, r.state]),
    ).toEqual([['Remote Ollama', 'stale']])
  })

  it('gives no row to an empty provider that a fresh profile list already names', () => {
    const rows = buildAvailableRows({
      catalogs: [
        {
          driver: 'opencode',
          profileRef: 'p1',
          providerRef: 'provider:1',
          viaProvider: true,
          catalog: catalog('fresh', [{ id: 'api-stub-model' }]),
        },
      ],
      availability: [{ ...API_PROVIDER, state: 'stale', models: [] }],
      runs: [],
    })
    expect(rows.map((r) => [r.source, r.model])).toEqual([
      ['tool', 'api-stub-model'],
    ])
  })

  it('attributes a session on a remote Ollama provider to that provider', () => {
    const rows = buildAvailableRows({
      catalogs: [
        {
          driver: 'opencode',
          profileRef: 'p-remote',
          providerRef: 'provider:1',
          viaProvider: true,
          catalog: catalog('unsupported', []),
        },
      ],
      ollama: OLLAMA_UP,
      providers: [
        {
          provider_ref: 'provider:1',
          kind: 'ollama',
          display_name: 'Remote Ollama',
          base_url: 'http://192.0.2.10:11434',
          state: 'active',
        },
      ],
      availability: [
        {
          ...API_PROVIDER,
          provider_kind: 'ollama',
          models: [{ id: 'qwen3:8b', seen_at: '2026-10-25T10:00:00Z' }],
        },
      ],
      runs: [
        run({
          run_ref: 'remote',
          provider_profile_ref: 'p-remote',
          model_ref: 'olivares_ollama/qwen3:8b',
        }),
      ],
    })
    expect(rows.map((r) => [r.source, r.owner, r.sessions.length])).toEqual([
      ['tool', 'opencode', 0],
      ['ollama', 'ollama', 0],
      ['ollama', 'ollama', 0],
      ['provider', 'Remote Ollama', 1],
    ])
  })

  it('does not guess a local row when the run profile identity is unavailable', () => {
    const rows = buildAvailableRows({
      catalogs: [],
      ollama: OLLAMA_UP,
      availability: [
        {
          ...API_PROVIDER,
          provider_kind: 'ollama',
          models: [{ id: 'qwen3:8b', seen_at: '2026-10-25T10:00:00Z' }],
        },
      ],
      runs: [
        run({
          run_ref: 'remote',
          provider_profile_ref: 'p-remote-unreadable',
          model_ref: 'olivares_ollama/qwen3:8b',
        }),
      ],
    })
    expect(rows.map((r) => [r.source, r.sessions.length])).toEqual([
      ['ollama', 0],
      ['ollama', 0],
      ['provider', 0],
      ['session', 1],
    ])
  })

  it('does not repeat a model already listed by a profile bound to that provider', () => {
    const rows = buildAvailableRows({
      catalogs: [
        {
          driver: 'opencode',
          providerRef: 'provider:1',
          viaProvider: true,
          catalog: catalog('fresh', [{ id: 'api-stub-model' }]),
        },
      ],
      availability: [API_PROVIDER],
      runs: [],
    })
    expect(rows.map((r) => [r.source, r.owner, r.model])).toEqual([
      ['tool', 'opencode', 'api-stub-model'],
    ])
  })

  it('counts the live sessions that run a tool model, by tool and model', () => {
    const rows = buildAvailableRows({
      catalogs: [
        {
          driver: 'opencode',
          catalog: catalog('fresh', [{ id: 'gpt-5' }, { id: 'o3' }]),
        },
      ],
      availability: [],
      runs: [
        run({ run_ref: 'a', model_ref: 'gpt-5' }),
        run({ run_ref: 'b', model_ref: 'gpt-5', state: 'idle' }),
        run({ run_ref: 'c', model_ref: 'o3', provider_driver: 'codex' }),
      ],
    })
    expect(rows[0].sessions.map((s) => s.run_ref)).toEqual(['a', 'b'])
    expect(rows[1].sessions).toEqual([])
  })

  it('does not count a session that has ended', () => {
    const rows = buildAvailableRows({
      catalogs: [
        {
          driver: 'opencode',
          catalog: catalog('fresh', [{ id: 'gpt-5' }]),
        },
      ],
      availability: [],
      runs: [
        run({ run_ref: 'a', model_ref: 'gpt-5', state: 'stopped' }),
        run({ run_ref: 'b', model_ref: 'gpt-5', state: 'failed' }),
      ],
    })
    expect(rows[0].sessions).toEqual([])
  })

  // A launch on the tool's default carries no `model_ref` (RunDTO omits it); the first turn
  // adds `usage_model_ref`, the provider's full id, never the tool's alias. Claude's own list
  // holds aliases and marks `default` with is_default (provider_catalog_tools.go).
  const claudeList = catalog('fresh', [
    { id: 'default', is_default: true },
    { id: 'opus' },
  ])
  const countsOf = (rows: ReturnType<typeof buildAvailableRows>) =>
    Object.fromEntries(rows.map((r) => [r.key, r.sessions.length]))

  it("counts a session launched on the tool default on that tool's default row, before any turn", () => {
    const rows = buildAvailableRows({
      catalogs: [{ driver: 'claude', catalog: claudeList }],
      availability: [],
      runs: [run({ run_ref: 'a', provider_driver: 'claude' })],
    })
    expect(countsOf(rows)).toEqual({
      'tool:claude:default': 1,
      'tool:claude:opus': 0,
    })
  })

  it('keeps that session on the default row after its first turn reports a full provider id', () => {
    const rows = buildAvailableRows({
      catalogs: [{ driver: 'claude', catalog: claudeList }],
      availability: [
        {
          ...API_PROVIDER,
          models: [{ id: 'claude-opus-4-8', seen_at: '2026-10-25T10:00:00Z' }],
        },
      ],
      runs: [
        run({
          run_ref: 'a',
          provider_driver: 'claude',
          usage_model_ref: 'claude-opus-4-8',
        }),
      ],
    })
    expect(countsOf(rows)).toEqual({
      'tool:claude:default': 1,
      'tool:claude:opus': 0,
      'provider:provider:1:claude-opus-4-8': 0,
    })
  })

  it('takes the default row from default_model when the list marks none', () => {
    const rows = buildAvailableRows({
      catalogs: [
        {
          driver: 'opencode',
          catalog: {
            ...catalog('fresh', [{ id: 'gpt-5' }, { id: 'o3' }]),
            default_model: 'o3',
          },
        },
      ],
      availability: [],
      runs: [run({ run_ref: 'a' })],
    })
    expect(countsOf(rows)).toEqual({
      'tool:opencode:gpt-5': 0,
      'tool:opencode:o3': 1,
    })
  })

  it('keeps an own-default target when a fresh OpenCode list marks no default', () => {
    const rows = buildAvailableRows({
      catalogs: [
        {
          driver: 'opencode',
          catalog: catalog('fresh', [
            { id: 'opencode/big-pickle' },
            { id: 'opencode/gpt-5' },
          ]),
        },
      ],
      availability: [],
      runs: [
        run({ run_ref: 'before-turn' }),
        run({
          run_ref: 'after-turn',
          usage_model_ref: 'opencode/big-pickle',
        }),
      ],
    })
    expect(countsOf(rows)).toEqual({
      'tool:opencode:opencode/big-pickle': 0,
      'tool:opencode:opencode/gpt-5': 0,
      'tool:opencode': 2,
    })
    expect(rows.find((r) => r.key === 'tool:opencode')).toMatchObject({
      model: '',
      state: 'ready',
    })
  })

  it('counts a default-model session on the own-default row of a tool whose list is unlisted or failed', () => {
    const rows = buildAvailableRows({
      catalogs: [
        { driver: 'codex', catalog: catalog('unsupported', []) },
        { driver: 'opencode', catalog: null },
      ],
      availability: [],
      runs: [
        run({ run_ref: 'a', provider_driver: 'codex' }),
        run({
          run_ref: 'b',
          provider_driver: 'opencode',
          usage_model_ref: 'gpt-5',
        }),
      ],
    })
    expect(countsOf(rows)).toEqual({ 'tool:codex': 1, 'tool:opencode': 1 })
  })

  it("does not put a default-model session of one tool on another tool's default row", () => {
    const rows = buildAvailableRows({
      catalogs: [
        { driver: 'claude', catalog: claudeList },
        { driver: 'codex', catalog: catalog('unsupported', []) },
      ],
      availability: [],
      runs: [run({ run_ref: 'a', provider_driver: 'codex' })],
    })
    expect(countsOf(rows)).toEqual({
      'tool:claude:default': 0,
      'tool:claude:opus': 0,
      'tool:codex': 1,
    })
  })

  it('counts a session on an Ollama model under the Ollama row, once', () => {
    const rows = buildAvailableRows({
      catalogs: [],
      ollama: OLLAMA_UP,
      availability: [],
      runs: [run({ run_ref: 'a', model_ref: 'ollama/qwen3:8b' })],
    })
    expect(rows.map((r) => r.sessions.length)).toEqual([1, 0])
  })

  it('puts a session on its tool row when the tool lists the model too', () => {
    const rows = buildAvailableRows({
      catalogs: [
        {
          driver: 'opencode',
          catalog: catalog('fresh', [{ id: 'ollama/qwen3:8b' }]),
        },
      ],
      ollama: OLLAMA_UP,
      availability: [],
      runs: [run({ run_ref: 'a', model_ref: 'ollama/qwen3:8b' })],
    })
    const counts = Object.fromEntries(
      rows.map((r) => [`${r.source}:${r.model}`, r.sessions.length]),
    )
    expect(counts).toEqual({
      'tool:ollama/qwen3:8b': 1,
      'ollama:qwen3:8b': 0,
      'ollama:llama3.2:3b': 0,
    })
  })
  it('lists a tool whose model list could not be read as failed', () => {
    const rows = buildAvailableRows({
      catalogs: [{ driver: 'claude', catalog: null }],
      availability: [],
      runs: [],
    })
    expect(rows.map((r) => [r.owner, r.model, r.state])).toEqual([
      ['claude', '', 'failed'],
    ])
  })

  it("keeps a provider-bound profile's list apart from the tool's own list, flagged", () => {
    const rows = buildAvailableRows({
      catalogs: [
        { driver: 'claude', catalog: catalog('fresh', [{ id: 'opus' }]) },
        {
          driver: 'claude',
          catalog: catalog('fresh', [
            { id: 'opus' },
            { id: 'claude-opus-4-8' },
          ]),
          viaProvider: true,
        },
      ],
      availability: [],
      runs: [],
    })
    expect(rows.map((r) => [r.key, !!r.viaProvider])).toEqual([
      ['tool:claude:opus', false],
      ['tool:claude:provider:opus', true],
      ['tool:claude:provider:claude-opus-4-8', true],
    ])
  })

  it('keeps direct and provider-bound fallback rows apart, with their sources', () => {
    const rows = buildAvailableRows({
      catalogs: [
        { driver: 'claude', catalog: catalog('unsupported', []) },
        {
          driver: 'claude',
          catalog: catalog('unsupported', []),
          viaProvider: true,
        },
      ],
      availability: [],
      runs: [],
    })
    expect(rows.map((r) => [r.key, !!r.viaProvider, r.state])).toEqual([
      ['tool:claude', false, 'unlisted'],
      ['tool:claude:provider', true, 'unlisted'],
    ])
  })

  it('keeps the default flag when a second account of the tool lists the same model', () => {
    const rows = buildAvailableRows({
      catalogs: [
        { driver: 'claude', catalog: catalog('fresh', [{ id: 'opus' }]) },
        {
          driver: 'claude',
          catalog: catalog('fresh', [{ id: 'opus', is_default: true }]),
        },
      ],
      availability: [],
      runs: [run({ run_ref: 'a', provider_driver: 'claude' })],
    })
    expect(countsOf(rows)).toEqual({ 'tool:claude:opus': 1 })
  })

  it("counts a session that names a model of a provider-bound list on that list's row", () => {
    const rows = buildAvailableRows({
      catalogs: [
        {
          driver: 'claude',
          catalog: catalog('fresh', [{ id: 'claude-opus-4-8' }]),
          viaProvider: true,
        },
      ],
      availability: [],
      runs: [
        run({
          run_ref: 'a',
          provider_driver: 'claude',
          model_ref: 'claude-opus-4-8',
        }),
      ],
    })
    expect(countsOf(rows)).toEqual({
      'tool:claude:provider:claude-opus-4-8': 1,
    })
  })

  it('names Ollama as starting or failed, not only stopped', () => {
    const state = (s: OllamaStatus['state']) =>
      buildAvailableRows({
        catalogs: [],
        ollama: { installed: true, state: s, models: [] },
        availability: [],
        runs: [],
      })[0].state
    expect([state('starting'), state('failed'), state('stopped')]).toEqual([
      'starting',
      'ollamaFailed',
      'stopped',
    ])
  })

  it("does not put another provider's model of the same tag on an Ollama row", () => {
    const rows = buildAvailableRows({
      catalogs: [],
      ollama: OLLAMA_UP,
      availability: [],
      runs: [run({ run_ref: 'a', model_ref: 'acme/qwen3:8b' })],
    })
    expect(rows.slice(0, 2).map((r) => r.sessions.length)).toEqual([0, 0])
    expect(rows[2]).toMatchObject({
      source: 'session',
      model: 'acme/qwen3:8b',
      sessions: [expect.objectContaining({ run_ref: 'a' })],
    })
  })
  it('shows an in-use exact model the tool catalog did not list as its own row', () => {
    const rows = buildAvailableRows({
      catalogs: [{ driver: 'claude', catalog: claudeList }],
      availability: [],
      runs: [
        run({
          run_ref: 'exact',
          provider_driver: 'claude',
          model_ref: 'claude-opus-5-5',
        }),
      ],
    })
    expect(rows.find((r) => r.model === 'claude-opus-5-5')).toMatchObject({
      source: 'session',
      owner: 'claude',
      state: 'ready',
      sessions: [expect.objectContaining({ run_ref: 'exact' })],
    })
  })

  it('matches a legacy run with no driver only when one tool row has that model', () => {
    const rows = buildAvailableRows({
      catalogs: [
        {
          driver: 'opencode',
          catalog: catalog('fresh', [{ id: 'gpt-5' }]),
        },
      ],
      availability: [],
      runs: [
        run({ run_ref: 'legacy', provider_driver: '', model_ref: 'gpt-5' }),
      ],
    })
    expect(rows[0].sessions.map((s) => s.run_ref)).toEqual(['legacy'])
  })

  it('counts the native Olivares Ollama model prefix on the installed row', () => {
    const rows = buildAvailableRows({
      catalogs: [],
      ollama: OLLAMA_UP,
      availability: [],
      runs: [
        run({
          run_ref: 'native',
          model_ref: 'olivares_ollama/qwen3:8b',
        }),
      ],
    })
    expect(rows.map((r) => r.sessions.length)).toEqual([1, 0])
  })

  it("counts an OpenCode session on its provider-bound row when the run names the model with Olivares' Ollama prefix", () => {
    const rows = buildAvailableRows({
      catalogs: [
        {
          driver: 'opencode',
          profileRef: 'p-oc',
          viaProvider: true,
          catalog: catalog('fresh', [{ id: 'qwen3:8b' }]),
        },
      ],
      ollama: { installed: true, state: 'running', models: ['qwen3:8b'] },
      availability: [],
      runs: [
        run({
          run_ref: 'native',
          provider_profile_ref: 'p-oc',
          model_ref: 'olivares_ollama/qwen3:8b',
        }),
      ],
    })
    expect(rows.map((r) => [r.source, r.sessions.length])).toEqual([
      ['tool', 1],
      ['ollama', 0],
    ])
  })

  it("prefers the tool row that names the run's model exactly over one that only ends with it", () => {
    const rows = buildAvailableRows({
      catalogs: [
        {
          driver: 'opencode',
          profileRef: 'p-oc',
          viaProvider: true,
          catalog: catalog('fresh', [{ id: 'm' }, { id: 'x/m' }]),
        },
      ],
      availability: [],
      runs: [
        run({ run_ref: 'a', provider_profile_ref: 'p-oc', model_ref: 'x/m' }),
      ],
    })
    expect(rows.map((r) => [r.model, r.sessions.length])).toEqual([
      ['m', 0],
      ['x/m', 1],
    ])
  })

  it('holds a session of a tool whose list is still loading, instead of counting it on another row first', () => {
    const loading = {
      driver: 'opencode',
      profileRef: 'p-oc',
      viaProvider: true,
      loading: true,
      catalog: undefined,
    }
    const input = {
      ollama: {
        installed: true,
        state: 'running',
        models: ['qwen3:8b'],
      } as OllamaStatus,
      availability: [],
      runs: [
        run({
          run_ref: 'native',
          provider_profile_ref: 'p-oc',
          model_ref: 'olivares_ollama/qwen3:8b',
        }),
      ],
    }
    const held = buildAvailableRows({ ...input, catalogs: [loading] })
    expect(held.map((r) => [r.source, r.sessions.length])).toEqual([
      ['ollama', 0],
    ])
    const loaded = buildAvailableRows({
      ...input,
      catalogs: [
        {
          ...loading,
          loading: false,
          catalog: catalog('fresh', [{ id: 'qwen3:8b' }]),
        },
      ],
    })
    expect(loaded.map((r) => [r.source, r.sessions.length])).toEqual([
      ['tool', 1],
      ['ollama', 0],
    ])
  })

  it('tries the model the tool reported when the launch name matches no row', () => {
    const rows = buildAvailableRows({
      catalogs: [
        {
          driver: 'claude',
          catalog: catalog('fresh', [{ id: 'claude-opus-5-5' }]),
        },
      ],
      availability: [],
      runs: [
        run({
          run_ref: 'a',
          provider_driver: 'claude',
          model_ref: 'opus',
          usage_model_ref: 'claude-opus-5-5',
        }),
      ],
    })
    expect(rows[0].sessions.map((s) => s.run_ref)).toEqual(['a'])
  })
})
