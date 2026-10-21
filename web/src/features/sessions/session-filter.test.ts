// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE FILTER FIELD matches what a row shows, and nothing it does not.
import { describe, expect, it } from 'vitest'
import type { RunDTO } from '@/features/agentops/types'
import { filterSessions } from './session-filter'
import { mergeSessions } from './provenance'
import type { LiveDTO } from './types'

function run(over: Partial<RunDTO>): RunDTO {
  return {
    run_ref: 'run-x',
    transport: 'stream-json',
    permission_mode: 'default',
    isolation: 'native',
    state: 'running',
    last_event_seq: 0,
    pep_provisioned: true,
    record_io: false,
    critical: false,
    ...over,
  }
}

const sessions = mergeSessions(
  [],
  [
    run({
      run_ref: 'run-1',
      name: 'fix-parser',
      provider_driver: 'claude',
      workspace_path: '/srv/work/api',
    }),
    run({
      run_ref: 'run-2',
      name: 'write-docs',
      provider_driver: 'codex',
      workspace_path: '/srv/work/site',
    }),
  ],
)

const names = (q: string) =>
  filterSessions(sessions, q, 'Untitled session').map((s) => s.runs[0]!.name)

describe('filterSessions', () => {
  it('keeps every session for an empty query', () => {
    expect(names('')).toEqual(['fix-parser', 'write-docs'])
    expect(names('   ')).toEqual(['fix-parser', 'write-docs'])
  })

  it("matches the name, the tool's product name and the folder name, case-insensitively", () => {
    expect(names('PARSER')).toEqual(['fix-parser'])
    expect(names('codex')).toEqual(['write-docs'])
    expect(names('claude code')).toEqual(['fix-parser'])
    expect(names('site')).toEqual(['write-docs'])
  })

  it('finds a session by the tool its row is named after, wherever the tool was declared', () => {
    const observed = mergeSessions(
      [
        {
          session_ref: 'sess-obs',
          live_ref: 'lr-obs',
          attribution: 'legacy',
          cc_state: 'active',
          input_tokens: 0,
          output_tokens: 0,
          cost_micro_usd: 0,
          event_count: 1,
          tool_call_count: 1,
          first_event_at: '2026-10-15T10:00:00Z',
          last_event_at: '2026-10-15T10:01:00Z',
          duration_seconds: 60,
          // Named by its summary; the connector only declared the engine.
          summary: 'Filed PR #7',
          engine: 'claude',
        },
      ],
      [],
    )
    const found = filterSessions(
      [...sessions, ...observed],
      'claude',
      'Untitled session',
    )
    expect(found.map((s) => s.sessionRef ?? s.runs[0]?.name)).toEqual([
      'fix-parser',
      'sess-obs',
    ])
  })

  it('needs every word, in any order', () => {
    expect(names('api fix')).toEqual(['fix-parser'])
    expect(names('api docs')).toEqual([])
  })

  it('does not match a path or a reference the row does not paint', () => {
    expect(names('srv')).toEqual([])
    expect(names('run-1')).toEqual([])
  })

  it('finds an observed session by the project its hooks reported', () => {
    const observed = mergeSessions(
      [
        {
          session_ref: 'sess-a',
          live_ref: 'live-a',
          cc_state: 'active',
          engine: 'claude',
          project: 'billing-api',
        } as LiveDTO,
        {
          session_ref: 'sess-b',
          live_ref: 'live-b',
          cc_state: 'active',
          engine: 'claude',
          project: 'ledger',
        } as LiveDTO,
      ],
      [],
    )
    const found = filterSessions(observed, 'billing', 'Untitled session')
    expect(found.map((s) => s.live?.project)).toEqual(['billing-api'])
  })
})
