// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it } from 'vitest'
import type { RunDTO, RunState, Transport } from '@/features/agentops/types'
import {
  capabilities,
  controlLevel,
  isResumableRun,
  mergeSessions,
  primaryRun,
  sessionLabel,
  sessionSearchKey,
  type Grants,
} from './provenance'
import type { LiveDTO } from './types'

function live(ref: string, over: Partial<LiveDTO> = {}): LiveDTO {
  return {
    session_ref: ref,
    cc_state: 'active',
    input_tokens: 0,
    output_tokens: 0,
    cost_micro_usd: 0,
    event_count: 0,
    tool_call_count: 0,
    first_event_at: '2026-08-10T10:00:00Z',
    last_event_at: '2026-08-10T10:05:00Z',
    duration_seconds: 300,
    ...over,
  }
}

function run(ref: string, over: Partial<RunDTO> = {}): RunDTO {
  return {
    run_ref: ref,
    transport: 'stream-json' as Transport,
    permission_mode: 'default',
    isolation: 'native',
    state: 'running' as RunState,
    last_event_seq: 1,
    pep_provisioned: true,
    record_io: false,
    critical: false,
    created_at: '2026-08-10T10:00:00Z',
    ...over,
  }
}

const ALL_GRANTS: Grants = {
  liveRead: true,
  runRead: true,
  runWrite: true,
  runAdmin: true,
}

describe('mergeSessions — one session is one row, discovered or launched', () => {
  it('marks a session with no run as DISCOVERED and one with a run as LAUNCHED', () => {
    const rows = mergeSessions(
      [live('sess-found'), live('sess-ours')],
      [run('run-1', { claude_session_id: 'sess-ours' })],
    )
    const found = rows.find((r) => r.sessionRef === 'sess-found')
    const ours = rows.find((r) => r.sessionRef === 'sess-ours')
    expect(found?.provenance).toBe('discovered')
    expect(found?.runs).toHaveLength(0)
    expect(ours?.provenance).toBe('launched')
    expect(ours?.runs.map((r) => r.run_ref)).toEqual(['run-1'])
  })

  it('folds TWO runs on one session into ONE row (a resume is not a second session)', () => {
    const rows = mergeSessions(
      [live('sess-ours')],
      [
        run('run-1', { claude_session_id: 'sess-ours' }),
        run('run-2', { claude_session_id: 'sess-ours' }),
      ],
    )
    expect(rows).toHaveLength(1)
    expect(rows[0]!.runs.map((r) => r.run_ref)).toEqual(['run-1', 'run-2'])
  })

  it('keeps a launched session that has NO observation yet — as its own row', () => {
    // Its telemetry has not arrived (or is not on this page). Dropping it would hide
    // a running session; inventing an observed half for it would be worse.
    const rows = mergeSessions(
      [],
      [run('run-1', { claude_session_id: 'sess-new' })],
    )
    expect(rows).toHaveLength(1)
    expect(rows[0]!.sessionRef).toBe('sess-new')
    expect(rows[0]!.live).toBeUndefined()
    expect(rows[0]!.provenance).toBe('launched')
  })

  it('keeps a run that never announced a session id under its own key', () => {
    const rows = mergeSessions([live('sess-found')], [run('run-rc')])
    expect(rows).toHaveLength(2)
    const orphan = rows.find((r) => r.key === 'run:run-rc')
    expect(orphan?.sessionRef).toBeUndefined()
    expect(orphan?.provenance).toBe('launched')
  })

  it('does NOT fold a Claude run onto a session another ENGINE declared', () => {
    // Provider ids are opaque strings and two engines can issue the same one — which
    // is why the identity plane keys aliases on (provider, external_id). A run's
    // claude_session_id is by construction a CLAUDE id, so a row that declares
    // engine "codex" is a different session that happens to share a string.
    const rows = mergeSessions(
      [live('abc', { engine: 'codex' })],
      [run('run-1', { claude_session_id: 'abc' })],
    )
    expect(rows).toHaveLength(2)
    const observed = rows.find((r) => r.sessionRef === 'abc')
    expect(observed?.provenance).toBe('discovered')
    expect(observed?.runs).toHaveLength(0)
    const launched = rows.find((r) => r.key === 'run:run-1')
    expect(launched?.provenance).toBe('launched')
  })

  it('DOES fold onto a row that declares claude, or declares nothing', () => {
    // The Claude connector declares no engine label, so an unlabelled row is a
    // possible Claude session: a run is kept apart only by a CONTRADICTING engine,
    // never by one that merely fails to confirm.
    for (const engine of [undefined, 'claude']) {
      const rows = mergeSessions(
        [live('abc', engine ? { engine } : {})],
        [run('run-1', { claude_session_id: 'abc' })],
      )
      expect(rows).toHaveLength(1)
      expect(rows[0]!.provenance).toBe('launched')
    }
  })

  it('does NOT attribute two id-less runs to the same session', () => {
    // Both have no claude_session_id. Keying them together would invent a link the
    // plane never made — the very failure the join exists to avoid.
    const rows = mergeSessions([], [run('run-a'), run('run-b')])
    expect(rows).toHaveLength(2)
  })

  it('sorts by the most recent activity from EITHER half', () => {
    const rows = mergeSessions(
      [
        live('sess-old', { last_event_at: '2026-08-10T09:00:00Z' }),
        live('sess-new', { last_event_at: '2026-08-10T12:00:00Z' }),
      ],
      [
        run('run-1', {
          claude_session_id: 'sess-old',
          last_activity_at: '2026-08-10T18:00:00Z',
        }),
      ],
    )
    // sess-old is stale on the observed side but its RUN was active an hour ago.
    expect(rows.map((r) => r.sessionRef)).toEqual(['sess-old', 'sess-new'])
  })
})

describe('controlLevel — what the PLANE can do, never what the caller can', () => {
  it('is observe with no run at all', () => {
    expect(controlLevel([])).toBe('observe')
  })

  it('is full for a live bridged run', () => {
    expect(controlLevel([run('r', { state: 'running' })])).toBe('full')
  })

  it('is lifecycle for a live run whose I/O is relayed away', () => {
    // remote-control: Olivares owns the process but cannot see into it.
    expect(
      controlLevel([
        run('r', { state: 'running', transport: 'remote-control' }),
      ]),
    ).toBe('lifecycle')
  })

  it('is lifecycle for a stopped run that can be resumed', () => {
    expect(
      controlLevel([
        run('r', { state: 'stopped', claude_session_id: 'sess-x' }),
      ]),
    ).toBe('lifecycle')
  })

  it('is observe for a stopped stream-json run with no id to resume into', () => {
    expect(controlLevel([run('r', { state: 'stopped' })])).toBe('observe')
  })

  it('is observe for a cleaned run', () => {
    expect(controlLevel([run('r', { state: 'cleaned' })])).toBe('observe')
  })

  it('takes the STRONGEST reach across runs, not the first', () => {
    const runs = [
      run('r-dead', { state: 'cleaned' }),
      run('r-live', { state: 'running' }),
    ]
    expect(controlLevel(runs)).toBe('full')
    // …and the reverse order must give the same answer.
    expect(controlLevel([...runs].reverse())).toBe('full')
  })
})

describe('isResumableRun', () => {
  it('needs a captured id for stream-json (there is nothing to --resume into)', () => {
    expect(isResumableRun(run('r', { state: 'stopped' }))).toBe(false)
    expect(
      isResumableRun(run('r', { state: 'stopped', claude_session_id: 'x' })),
    ).toBe(true)
  })

  it('does not need one for remote-control', () => {
    expect(
      isResumableRun(
        run('r', { state: 'stopped', transport: 'remote-control' }),
      ),
    ).toBe(true)
  })

  it('is false for a running or cleaned run', () => {
    expect(isResumableRun(run('r', { state: 'running' }))).toBe(false)
    expect(isResumableRun(run('r', { state: 'cleaned' }))).toBe(false)
  })
})

describe('primaryRun — the run the card acts on', () => {
  it('prefers a LIVE run over a stopped one, whatever the order', () => {
    const dead = run('r-dead', { state: 'stopped' })
    const alive = run('r-live', { state: 'running' })
    expect(primaryRun([dead, alive])?.run_ref).toBe('r-live')
    expect(primaryRun([alive, dead])?.run_ref).toBe('r-live')
  })

  it('carries the control level the session reports, not merely the first live run', () => {
    // The contrast caught this: with a live RELAYED run and a live BRIDGED one the
    // session reports "full control" (the bridged run's reach) while the capability
    // list described the relayed one and denied the I/O the level had just promised.
    const relayed = run('r-relay', {
      state: 'running',
      transport: 'remote-control',
    })
    const bridged = run('r-bridged', { state: 'running' })
    for (const runs of [
      [relayed, bridged],
      [bridged, relayed],
    ]) {
      expect(controlLevel(runs)).toBe('full')
      expect(primaryRun(runs)?.run_ref).toBe('r-bridged')
      // …so the capability list agrees with the level by construction.
      const caps = capabilities(
        { runs: [primaryRun(runs)!], sessionRef: 'sess-x' },
        ALL_GRANTS,
      )
      expect(caps.find((c) => c.id === 'attach')!.available).toBe(true)
    }
  })

  it('falls back to a resumable run, then to the newest', () => {
    const cleaned = run('r-cleaned', { state: 'cleaned' })
    const resumable = run('r-stopped', {
      state: 'stopped',
      claude_session_id: 'x',
    })
    expect(primaryRun([cleaned, resumable])?.run_ref).toBe('r-stopped')
    expect(primaryRun([cleaned])?.run_ref).toBe('r-cleaned')
    expect(primaryRun([])).toBeUndefined()
  })
})

describe('capabilities — "you cannot" and "nobody can" are different answers', () => {
  const capOf = (caps: ReturnType<typeof capabilities>, id: string) =>
    caps.find((c) => c.id === id)!

  it('reports no-run (not permission) when the plane holds no process', () => {
    const caps = capabilities(
      { runs: [], sessionRef: 'sess-found' },
      ALL_GRANTS,
    )
    expect(capOf(caps, 'stop')).toEqual({
      id: 'stop',
      available: false,
      reason: 'no-run',
    })
    // Even with every permission: asking an operator to request a permission that
    // would change nothing is worse than telling them no.
    expect(capOf(caps, 'attach').reason).toBe('no-run')
    expect(capOf(caps, 'watch').available).toBe(true)
  })

  it('reports transport (not state) for a live relayed run', () => {
    const caps = capabilities(
      {
        runs: [run('r', { state: 'running', transport: 'remote-control' })],
        sessionRef: 'sess-x',
      },
      ALL_GRANTS,
    )
    expect(capOf(caps, 'attach').reason).toBe('transport')
    expect(capOf(caps, 'drive').reason).toBe('transport')
    // …but the lifecycle IS reachable: the plane owns the process.
    expect(capOf(caps, 'stop').available).toBe(true)
  })

  it('reports state for a capability the run is simply not eligible for', () => {
    const caps = capabilities(
      { runs: [run('r', { state: 'running' })], sessionRef: 'sess-x' },
      ALL_GRANTS,
    )
    expect(capOf(caps, 'resume').reason).toBe('state')
    expect(capOf(caps, 'cleanup').reason).toBe('state')
    expect(capOf(caps, 'delete').reason).toBe('state')
  })

  it('reports permission ONLY when the plane could, and the caller may not', () => {
    const viewer: Grants = {
      liveRead: true,
      runRead: true,
      runWrite: false,
      runAdmin: false,
    }
    const caps = capabilities(
      { runs: [run('r', { state: 'running' })], sessionRef: 'sess-x' },
      viewer,
    )
    expect(capOf(caps, 'stop')).toEqual({
      id: 'stop',
      available: false,
      reason: 'permission',
    })
    expect(capOf(caps, 'drive').reason).toBe('permission')
    // read-only attach still works: it needs run:read, which this viewer has.
    expect(capOf(caps, 'attach').available).toBe(true)
  })

  it('distinguishes "nothing observed" from "not allowed to observe"', () => {
    const noSession = capabilities(
      { runs: [], sessionRef: undefined },
      ALL_GRANTS,
    )
    expect(capOf(noSession, 'watch').reason).toBe('no-observation')

    const noPerm = capabilities(
      { runs: [], sessionRef: 'sess-x' },
      { ...ALL_GRANTS, liveRead: false },
    )
    expect(capOf(noPerm, 'watch').reason).toBe('permission')
  })

  it('lets admin-only capabilities through only for an admin', () => {
    const stopped = run('r', { state: 'stopped', claude_session_id: 'x' })
    const writer = capabilities(
      { runs: [stopped], sessionRef: 'sess-x' },
      { liveRead: true, runRead: true, runWrite: true, runAdmin: false },
    )
    expect(capOf(writer, 'resume').available).toBe(true)
    expect(capOf(writer, 'cleanup')).toEqual({
      id: 'cleanup',
      available: false,
      reason: 'permission',
    })

    const admin = capabilities(
      { runs: [stopped], sessionRef: 'sess-x' },
      ALL_GRANTS,
    )
    expect(capOf(admin, 'cleanup').available).toBe(true)
  })

  it('explains an unresumable stopped run by its TRANSPORT, not by permission', () => {
    const caps = capabilities(
      { runs: [run('r', { state: 'stopped' })], sessionRef: 'sess-x' },
      ALL_GRANTS,
    )
    expect(capOf(caps, 'resume').reason).toBe('transport')
  })
})

describe('sessionLabel / sessionSearchKey', () => {
  it('names the row after the run whose reach the row reports', () => {
    // Seen on screen: a session driven by a relayed run AND a bridged one was titled
    // by the relayed one while the row said "Full control" — a reach that came from
    // the other run. The label must follow primaryRun, not list order.
    const rows = mergeSessions(
      [live('sess-ours')],
      [
        run('run-relay', {
          claude_session_id: 'sess-ours',
          name: 'relayed-session',
          transport: 'remote-control',
          state: 'stopped',
        }),
        run('run-bridged', {
          claude_session_id: 'sess-ours',
          name: 'coder-7a3f',
          state: 'running',
        }),
      ],
    )
    expect(rows[0]!.control).toBe('full')
    expect(sessionLabel(rows[0]!)).toBe('coder-7a3f')
  })

  it('keeps a session findable by the reference the rest of the plane uses', () => {
    const rows = mergeSessions(
      [live('sess-ours')],
      [
        run('run-1', {
          claude_session_id: 'sess-ours',
          name: 'nightly-indexer',
        }),
      ],
    )
    const key = sessionSearchKey(rows[0]!)
    // The row is TITLED by the run name, so the session id has to survive somewhere
    // searchable or the id in the ledger, the API and any saved link finds nothing.
    expect(key).toContain('sess-ours')
    expect(key).toContain('nightly-indexer')
    expect(key).toContain('run-1')
  })

  it('prefers the name the operator typed at launch', () => {
    const rows = mergeSessions(
      [live('sess-ours')],
      [
        run('run-1', {
          claude_session_id: 'sess-ours',
          name: 'nightly-indexer',
        }),
      ],
    )
    expect(sessionLabel(rows[0]!)).toBe('nightly-indexer')
  })

  it('falls back to the session reference, then the run reference', () => {
    expect(sessionLabel(mergeSessions([live('sess-found')], [])[0]!)).toBe(
      'sess-found',
    )
    expect(sessionLabel(mergeSessions([], [run('run-rc')])[0]!)).toBe('run-rc')
  })
})
