// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
// Pure process outcomes: these controls never invoke the Linux helper adapter.
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { test, type TestContext } from 'node:test'
import { fileURLToPath } from 'node:url'
import type { ChildProcess } from 'node:child_process'
import {
  EngineLifecycleError,
  createLifecycleIo,
  engineLifecycleRequired,
  publishObservedEngine,
  stopObservedEngine,
  recordUnobservedSpawn,
  validateEngineLease,
  validateCurrentPointer,
  validateStopReceipt,
  sha256,
  type LifecycleDependencies,
  type LifecycleIo,
  type LiveEngine,
  type HelperRequest,
  type HelperCompletion,
  type StopReceipt,
  type ToolIdentities,
  type LifecycleCode,
  type LifecycleErrorContext,
  type StopPurpose,
} from './engine-lifecycle.ts'
import {
  PROCESS_OBSERVATION_SCHEMA,
  type ProcessObservationReceipt,
} from './process-observation.ts'

type CorpusCase = {
  name: string
  kind: 'lease' | 'pointer' | 'stop'
  valid: boolean
  changes: Array<[Array<string | number>, unknown]>
  remove: Array<Array<string | number>>
}
const corpus = JSON.parse(
  fs.readFileSync(
    fileURLToPath(new URL('./engine-lifecycle-corpus.json', import.meta.url)),
    'utf8',
  ),
) as { bases: Record<CorpusCase['kind'], unknown>; cases: CorpusCase[] }
for (const c of corpus.cases)
  test(`shared corpus: ${c.name}`, () => {
    const value = structuredClone(corpus.bases[c.kind])
    const parent = (
      keys: Array<string | number>,
    ): Record<string | number, unknown> =>
      keys
        .slice(0, -1)
        .reduce(
          (o, k) => (o as Record<string | number, unknown>)[k],
          value,
        ) as Record<string | number, unknown>
    for (const [keys, v] of c.changes) parent(keys)[keys[keys.length - 1]] = v
    for (const keys of c.remove) delete parent(keys)[keys[keys.length - 1]]
    const parse = {
      lease: validateEngineLease,
      pointer: validateCurrentPointer,
      stop: validateStopReceipt,
    }[c.kind]
    if (c.valid) assert.doesNotThrow(() => parse(value))
    else assert.throws(() => parse(value), EngineLifecycleError)
  })

type Fixture = {
  root: string
  work: string
  live: LiveEngine
  receipt: ProcessObservationReceipt
  deps: LifecycleDependencies
  calls: HelperRequest[]
  current(): ReturnType<typeof validateCurrentPointer>
  result(request: HelperRequest): HelperCompletion
}
async function fixture(fn: (f: Fixture) => Promise<void>): Promise<void> {
  const env = {
    TMPDIR: process.env.TMPDIR,
    K3_E2E_TMP: process.env.K3_E2E_TMP,
    K3_E2E_PROCESS_OBSERVATION: process.env.K3_E2E_PROCESS_OBSERVATION,
  }
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'k3-lifecycle-pure-'))
  fs.chmodSync(root, 0o700)
  const work = path.join(root, 'work')
  const data = path.join(work, 'data')
  fs.mkdirSync(data, { recursive: true, mode: 0o700 })
  fs.chmodSync(work, 0o700)
  Object.assign(process.env, {
    TMPDIR: root,
    K3_E2E_TMP: root,
    K3_E2E_PROCESS_OBSERVATION: 'required',
  })
  const base = structuredClone(corpus.bases.lease) as ReturnType<
    typeof validateEngineLease
  >
  const dataStat = fs.statSync(data, { bigint: true })
  const live: LiveEngine = {
    pid: base.pid,
    start_ticks: base.start_ticks,
    boot_id: base.boot_id,
    pid_namespace: base.pid_namespace,
    executable: base.executable,
    cwd: work,
    data_dir: data,
    data_directory: {
      canonical_path: data,
      device: String(dataStat.dev),
      inode: String(dataStat.ino),
    },
    listen: base.listen,
    grpc_listen: base.grpc_listen,
  }
  const tools: ToolIdentities = {
    interpreter: (corpus.bases.stop as StopReceipt).helper.executable,
    script: (corpus.bases.stop as StopReceipt).helper.script,
  }
  const record: ProcessObservationReceipt['record'] = {
    schema: PROCESS_OBSERVATION_SCHEMA,
    role: 'engine',
    phase: 'seed',
    captured_at: '2026-09-10T12:00:00.000Z',
    pid: live.pid,
    start_ticks: live.start_ticks,
    parent_pid: 1,
    process_group: 4242,
    session: 4242,
    executable: live.executable,
    cwd: work,
    data_dir: data,
    listen: live.listen,
    grpc_listen: live.grpc_listen,
    observer: { pid: 43, start_ticks: '123' },
    budget_ms: 30000,
    elapsed_ms: 1,
    filename: {
      role: 'engine',
      phase: 'seed',
      observer_pid: 43,
      observer_start_ticks: '123',
      index: 0,
      name: 'engine.seed.43.123.0.json',
    },
  }
  const serialized = JSON.stringify(record, null, 2) + '\n'
  const receipt = {
    record,
    serialized,
    relativePath: 'process-observations/' + record.filename.name,
    sha256: sha256(serialized),
  }
  fs.mkdirSync(path.join(work, 'process-observations'), { mode: 0o700 })
  fs.writeFileSync(path.join(work, receipt.relativePath), serialized, {
    mode: 0o600,
  })
  const calls: HelperRequest[] = []
  const f: Fixture = {
    root,
    work,
    live,
    receipt,
    calls,
    deps: {},
    current: () =>
      validateCurrentPointer(
        JSON.parse(
          fs.readFileSync(
            path.join(work, 'engine-lifecycle/current.json'),
            'utf8',
          ),
        ),
      ),
    result(request) {
      const stop = {
        ...(structuredClone(corpus.bases.stop) as StopReceipt),
        lease: request.lease,
        purpose: request.purpose,
      }
      fs.writeFileSync(
        path.join(work, 'engine-lifecycle', request.resultName),
        JSON.stringify(stop) + '\n',
        { flag: 'wx', mode: 0o600 },
      )
      return {
        pid: stop.helper.pid,
        exitCode: 0,
        signal: null,
        stdout: '{"ok":true}',
      }
    },
  }
  f.deps = {
    inspect: () => structuredClone(live),
    tools: () => tools,
    startHelper: (request) => {
      calls.push(request)
      return {
        completion: Promise.resolve(f.result(request)),
        abandon: () => assert.fail('unexpected abandonment'),
      }
    },
  }
  try {
    await fn(f)
  } finally {
    for (const [key, value] of Object.entries(env)) {
      if (value === undefined) delete process.env[key]
      else process.env[key] = value
    }
    fs.rmSync(root, { recursive: true, force: true })
  }
}
async function refused(
  fn: () => Promise<unknown>,
  code: LifecycleCode,
): Promise<void> {
  await assert.rejects(
    fn,
    (e: unknown) => e instanceof EngineLifecycleError && e.code === code,
  )
}
function lockExists(f: Fixture): boolean {
  return fs.existsSync(path.join(f.work, 'engine-lifecycle/operation.lock'))
}
// A synthetic sentinel: it is not a credential and is never read from a real secret.
const SECRET_SENTINEL = 'k3-synthetic-secret-4f2b9c17e05d43a8'
const RAW_INSPECT_SENTINEL = 'k3-synthetic-inspect-error-767f82fce06f4ba3'
const RELEASE_SYNC_SENTINEL = 'k3-synthetic-release-sync-2f5ecb403c4a4d07'
const OPEN_DIRECTORY_SENTINEL = 'k3-synthetic-open-directory-3d6a1f84c2b74e50'
const STAT_DIRECTORY_SENTINEL = 'k3-synthetic-stat-directory-8b02e7d5416f4a93'
const CUSTODY_CLOSE_SENTINEL = 'k3-synthetic-custody-close-5c74f0a9e83b41d6'
async function refusedWithContext(
  fn: () => Promise<unknown>,
  code: LifecycleCode,
  context: LifecycleErrorContext,
): Promise<EngineLifecycleError> {
  const thrown: unknown = await fn().then(
    () => assert.fail('the public lifecycle boundary must refuse'),
    (e: unknown) => e,
  )
  assert.ok(thrown instanceof EngineLifecycleError)
  assert.equal(thrown.code, code)
  assert.ok(thrown.context)
  assert.equal(Object.isFrozen(thrown.context), true)
  assert.deepEqual(
    {
      operation: thrown.context.operation,
      work_dir: thrown.context.work_dir,
      purpose: thrown.context.purpose,
    },
    {
      operation: context.operation,
      work_dir: context.work_dir,
      purpose: context.purpose,
    },
  )
  assert.equal(
    thrown.message,
    `k3 engine lifecycle: ${code}; operation=${context.operation}` +
      `; work_dir=${context.work_dir ?? 'unavailable'}` +
      `; purpose=${context.purpose ?? 'not_applicable'}`,
  )
  return thrown
}
function lifecycleFiles(f: Fixture): string[] {
  const dir = path.join(f.work, 'engine-lifecycle')
  return fs.existsSync(dir) ? fs.readdirSync(dir).sort() : []
}
function retainedFailures(f: Fixture): Array<Record<string, unknown>> {
  const dir = path.join(f.work, 'engine-lifecycle')
  return lifecycleFiles(f)
    .filter((name) => name.startsWith('failure.'))
    .map(
      (name) =>
        JSON.parse(fs.readFileSync(path.join(dir, name), 'utf8')) as Record<
          string,
          unknown
        >,
    )
}
function assertRetainedContext(
  f: Fixture,
  code: LifecycleCode,
  context: LifecycleErrorContext,
): void {
  const records = retainedFailures(f)
  assert.equal(records.length, 1)
  assert.deepEqual(
    {
      schema: records[0].schema,
      code: records[0].code,
      operation: records[0].operation,
      work_dir: records[0].work_dir,
      purpose: records[0].purpose,
      helper_state: records[0].helper_state,
      accepted: records[0].accepted,
      reconciliation: records[0].reconciliation,
    },
    {
      schema: 'olivares.k3.lifecycle-failure.v1',
      code,
      operation: context.operation,
      work_dir: context.work_dir,
      purpose: context.purpose,
      helper_state: 'unknown',
      accepted: false,
      reconciliation: 'root_required',
    },
  )
}
// Directory-descriptor custody tracking for the constructor controls. It records
// the number the real openDirectory returned and every number passed to io.close,
// so a close of an evidence file can never be counted as a close of the estate
// descriptor. Counted closes of the captured number are the primary oracle.
type DirectoryCustody = {
  io: LifecycleIo
  opened: number[]
  closed: number[]
  closesOf(fd: number): number
}
function directoryCustody(): DirectoryCustody {
  const real = createLifecycleIo()
  const opened: number[] = []
  const closed: number[] = []
  const io: LifecycleIo = {
    ...real,
    openDirectory(target) {
      const fd = real.openDirectory(target)
      opened.push(fd)
      return fd
    },
    close(fd) {
      closed.push(fd)
      real.close(fd)
    },
  }
  return {
    io,
    opened,
    closed,
    closesOf: (fd) => closed.filter((n) => n === fd).length,
  }
}
// Secondary evidence only: a descriptor number can be reused by a later open, so
// this check supports the counted closes above and never replaces them.
function descriptorClosed(fd: number): boolean {
  try {
    fs.fstatSync(fd)
    return false
  } catch (e) {
    return (e as NodeJS.ErrnoException).code === 'EBADF'
  }
}
function assertSentinelAbsent(
  f: Fixture,
  error: EngineLifecycleError,
  sentinel: string,
): void {
  assert.equal(error.message.includes(sentinel), false)
  assert.equal(JSON.stringify(error.context).includes(sentinel), false)
  for (const name of lifecycleFiles(f))
    assert.equal(
      fs
        .readFileSync(path.join(f.work, 'engine-lifecycle', name), 'utf8')
        .includes(sentinel),
      false,
    )
}

test('disabled path has no estate or helper and preserves absence of qualification', async () =>
  fixture(async (f) => {
    delete process.env.K3_E2E_PROCESS_OBSERVATION
    assert.equal(engineLifecycleRequired(), false)
    assert.equal(
      await publishObservedEngine(f.work, undefined, f.deps),
      undefined,
    )
    assert.equal(
      await stopObservedEngine(f.work, 'teardown', f.deps),
      undefined,
    )
    assert.equal(fs.existsSync(path.join(f.work, 'engine-lifecycle')), false)
    process.env.K3_E2E_PROCESS_OBSERVATION = 'optional'
    assert.throws(engineLifecycleRequired, EngineLifecycleError)
  }))
test('publication, terminal stop, idempotence and successor bind immutable predecessor', async () =>
  fixture(async (f) => {
    const lease = await publishObservedEngine(f.work, f.receipt, f.deps)
    assert.ok(lease)
    assert.ok(Object.isFrozen(lease))
    assert.ok(Object.isFrozen(lease.data_directory))
    const old = f.current()
    assert.equal(old.state, 'live')
    assert.equal(lockExists(f), false)
    const stopped = await stopObservedEngine(f.work, 'activation', f.deps)
    assert.ok(stopped)
    assert.ok(Object.isFrozen(stopped))
    assert.equal(f.current().state, 'stopped')
    assert.deepEqual(
      await stopObservedEngine(f.work, 'teardown', f.deps),
      stopped,
    )
    assert.equal(f.calls.length, 1)
    if (f.receipt.record.role !== 'engine')
      assert.fail('engine fixture required')
    const nextRecord = {
      ...f.receipt.record,
      start_ticks: '2000',
      phase: 'activated' as const,
      filename: {
        ...f.receipt.record.filename,
        phase: 'activated',
        name: 'engine.activated.43.123.1.json',
        index: 1,
      },
    }
    const serialized = JSON.stringify(nextRecord) + '\n'
    const receipt = {
      record: nextRecord,
      serialized,
      relativePath: 'process-observations/' + nextRecord.filename.name,
      sha256: sha256(serialized),
    }
    fs.writeFileSync(path.join(f.work, receipt.relativePath), serialized, {
      mode: 0o600,
    })
    f.live.start_ticks = '2000'
    const next = await publishObservedEngine(f.work, receipt, f.deps)
    assert.ok(next)
    assert.notEqual(next.lease_id, lease.lease_id)
    assert.deepEqual(next.predecessor?.lease, old.lease)
    assert.ok(next.predecessor?.stop)
    assert.equal(
      JSON.parse(
        fs.readFileSync(
          path.join(f.work, 'engine-lifecycle', old.lease.name),
          'utf8',
        ),
      ).start_ticks,
      lease.start_ticks,
    )
  }))
test('a live predecessor refuses replacement before a second helper can run', async () =>
  fixture(async (f) => {
    await publishObservedEngine(f.work, f.receipt, f.deps)
    await refused(
      () => publishObservedEngine(f.work, f.receipt, f.deps),
      'invalid_lease',
    )
    assert.equal(f.calls.length, 0)
  }))
for (const kind of [
  'missing',
  'browser',
  'hash',
  'bytes',
  'filename',
  'generation',
  'ownership',
] as const)
  test(`publication refuses ${kind} and retains unresolved spawn custody`, async () =>
    fixture(async (f) => {
      let receipt: ProcessObservationReceipt | undefined = f.receipt
      let deps = f.deps
      let code: LifecycleCode = 'invalid_lease'
      if (kind === 'missing') receipt = undefined
      if (kind === 'browser')
        receipt = {
          ...f.receipt,
          record: { ...f.receipt.record, role: 'browser' },
        } as ProcessObservationReceipt
      if (kind === 'hash') receipt = { ...f.receipt, sha256: 'b'.repeat(64) }
      if (kind === 'bytes')
        receipt = {
          ...f.receipt,
          record: { ...f.receipt.record, cwd: '/elsewhere' },
        }
      if (kind === 'filename')
        receipt = {
          ...f.receipt,
          relativePath: 'process-observations/engine.seed.43.123.1.json',
        }
      if (kind === 'generation') {
        deps = { ...deps, inspect: () => ({ ...f.live, start_ticks: '999' }) }
        code = 'changed_generation'
      }
      if (kind === 'ownership') {
        deps = { ...deps, inspect: () => ({ ...f.live, cwd: '/elsewhere' }) }
        code = 'ownership_mismatch'
      }
      await refused(() => publishObservedEngine(f.work, receipt, deps), code)
      assert.equal(f.calls.length, 0)
      assert.equal(lockExists(f), true)
    }))

for (const kind of ['mode', 'symlink', 'oversized'] as const)
  test(`publication refuses ${kind} custody`, async () =>
    fixture(async (f) => {
      const target = path.join(f.work, f.receipt.relativePath)
      if (kind === 'mode') fs.chmodSync(f.work, 0o755)
      if (kind === 'symlink') {
        fs.unlinkSync(target)
        fs.symlinkSync('/unused', target)
      }
      if (kind === 'oversized') fs.writeFileSync(target, 'x'.repeat(65537))
      await refused(
        () => publishObservedEngine(f.work, f.receipt, f.deps),
        'invalid_custody',
      )
    }))

test('incomplete exclusive lease write and pointer rename failure cannot publish current', async () =>
  fixture(async (f) => {
    const io = createLifecycleIo()
    let writes = 0
    await refused(
      () =>
        publishObservedEngine(f.work, f.receipt, {
          ...f.deps,
          io: {
            ...io,
            write(...args) {
              if (++writes === 2) return 0
              return io.write(...args)
            },
          },
        }),
      'invalid_custody',
    )
    assert.equal(
      fs.existsSync(path.join(f.work, 'engine-lifecycle/current.json')),
      false,
    )
    assert.equal(lockExists(f), true)
    // The incomplete publication retains custody until root reconciliation.
    await refused(
      () => publishObservedEngine(f.work, f.receipt, f.deps),
      'lock_conflict',
    )
  }))
test('failed current-pointer replacement retains the operation lock and lease', async () =>
  fixture(async (f) => {
    await refused(
      () =>
        publishObservedEngine(f.work, f.receipt, {
          ...f.deps,
          io: {
            ...createLifecycleIo(),
            rename() {
              throw new Error('injected')
            },
          },
        }),
      'result_publication_failure',
    )
    assert.equal(lockExists(f), true)
    assert.equal(
      fs.existsSync(path.join(f.work, 'engine-lifecycle/current.json')),
      false,
    )
    await refused(
      () => stopObservedEngine(f.work, 'teardown', f.deps),
      'lock_conflict',
    )
  }))
test('concurrent stop conflicts and parent timeout rejects late receipt without cleanup signaling', async () =>
  fixture(async (f) => {
    await publishObservedEngine(f.work, f.receipt, f.deps)
    let complete!: (c: HelperCompletion) => void
    let request!: HelperRequest
    let abandoned = 0
    const deps = {
      ...f.deps,
      helperDeadlineMs: 15,
      startHelper: (r: HelperRequest) => {
        request = r
        return {
          completion: new Promise<HelperCompletion>((resolve) => {
            complete = resolve
          }),
          abandon() {
            abandoned++
          },
        }
      },
    }
    const stop = stopObservedEngine(f.work, 'restart', deps)
    await refused(
      () => stopObservedEngine(f.work, 'teardown', f.deps),
      'lock_conflict',
    )
    await refused(() => stop, 'helper_interruption')
    assert.equal(abandoned, 1)
    assert.equal(lockExists(f), true)
    complete(f.result(request))
    await new Promise((resolve) => setImmediate(resolve))
    assert.equal(f.current().state, 'live')
    assert.equal(lockExists(f), true)
    await refused(
      () => publishObservedEngine(f.work, f.receipt, f.deps),
      'lock_conflict',
    )
  }))
for (const code of [
  'capability_refusal',
  'terminal_unobserved',
  'unavailable_process',
  'changed_generation',
  'ownership_mismatch',
  'signal_failure',
  'deadline_exceeded',
  'helper_interruption',
  'result_publication_failure',
] as const)
  test(`classified helper ${code} cannot pass stop`, async () =>
    fixture(async (f) => {
      await publishObservedEngine(f.work, f.receipt, f.deps)
      await refused(
        () =>
          stopObservedEngine(f.work, 'restart', {
            ...f.deps,
            startHelper: () => ({
              completion: Promise.resolve({
                pid: 43,
                exitCode: 1,
                signal: null,
                stdout: JSON.stringify({ ok: false, code }),
              }),
              abandon() {
                assert.fail('not timeout')
              },
            }),
          }),
        code,
      )
      assert.equal(f.current().state, 'live')
      assert.equal(
        lockExists(f),
        [
          'deadline_exceeded',
          'helper_interruption',
          'result_publication_failure',
        ].includes(code),
      )
    }))
test('helper zero status without an accepted receipt cannot pass', async () =>
  fixture(async (f) => {
    await publishObservedEngine(f.work, f.receipt, f.deps)
    await refused(
      () =>
        stopObservedEngine(f.work, 'teardown', {
          ...f.deps,
          startHelper: () => ({
            completion: Promise.resolve({
              pid: 43,
              exitCode: 0,
              signal: null,
              stdout: '{"ok":true}',
            }),
            abandon() {},
          }),
        }),
      'result_publication_failure',
    )
    assert.equal(lockExists(f), true)
    assert.equal(f.current().state, 'live')
  }))
test('helper receipt belongs to its exact process, lease and interpreter', async () =>
  fixture(async (f) => {
    await publishObservedEngine(f.work, f.receipt, f.deps)
    await refused(
      () =>
        stopObservedEngine(f.work, 'restart', {
          ...f.deps,
          startHelper: (r) => {
            const completion = f.result(r)
            return {
              completion: Promise.resolve({ ...completion, pid: 44 }),
              abandon() {},
            }
          },
        }),
      'result_publication_failure',
    )
    assert.equal(f.current().state, 'live')
  }))
test('tampered observer evidence is refused before invoking helper', async () =>
  fixture(async (f) => {
    await publishObservedEngine(f.work, f.receipt, f.deps)
    fs.appendFileSync(path.join(f.work, f.receipt.relativePath), ' ')
    await refused(
      () => stopObservedEngine(f.work, 'teardown', f.deps),
      'invalid_lease',
    )
    assert.equal(f.calls.length, 0)
  }))
test('orphan result, missing receipt and missing current pointer cannot authorize advancement', async () =>
  fixture(async (f) => {
    await publishObservedEngine(f.work, f.receipt, f.deps)
    await stopObservedEngine(f.work, 'restart', f.deps)
    const pointer = f.current()
    assert.equal(pointer.state, 'stopped')
    if (pointer.state !== 'stopped') assert.fail()
    fs.unlinkSync(path.join(f.work, 'engine-lifecycle', pointer.stop.name))
    await refused(
      () => stopObservedEngine(f.work, 'restart', f.deps),
      'invalid_custody',
    )
    fs.unlinkSync(path.join(f.work, 'engine-lifecycle/current.json'))
    await refused(
      () => publishObservedEngine(f.work, f.receipt, f.deps),
      'invalid_lease',
    )
    assert.equal(f.calls.length, 1)
  }))
test('a failed unobserved spawn retains a lock and never adopts a lease', async () =>
  fixture(async (f) => {
    recordUnobservedSpawn(f.work, {
      pid: undefined,
      exitCode: null,
      signalCode: null,
    } as ChildProcess)
    assert.equal(lockExists(f), true)
    assert.equal(
      fs.existsSync(path.join(f.work, 'engine-lifecycle/current.json')),
      false,
    )
    await refused(
      () => stopObservedEngine(f.work, 'teardown', f.deps),
      'lock_conflict',
    )
  }))

test('helper pipe rejection abandons references and retains uncertainty', async () =>
  fixture(async (f) => {
    await publishObservedEngine(f.work, f.receipt, f.deps)
    let abandoned = 0
    await refused(
      () =>
        stopObservedEngine(f.work, 'restart', {
          ...f.deps,
          startHelper: () => ({
            completion: Promise.reject(new Error('injected pipe failure')),
            abandon() {
              abandoned++
            },
          }),
        }),
      'helper_interruption',
    )
    assert.equal(abandoned, 1)
    assert.equal(lockExists(f), true)
    assert.equal(f.current().state, 'live')
    const name = fs
      .readdirSync(path.join(f.work, 'engine-lifecycle'))
      .find((n) => n.startsWith('failure.'))
    assert.ok(name)
    const record = JSON.parse(
      fs.readFileSync(path.join(f.work, 'engine-lifecycle', name), 'utf8'),
    )
    assert.equal(record.helper_state, 'unknown')
    assert.equal(record.accepted, false)
  }))

for (const stdout of [
  'null',
  '[]',
  '{}',
  '{"ok":true,"extra":"untrusted"}',
  'not-json',
])
  test(`malformed helper outcome is interruption: ${stdout}`, async () =>
    fixture(async (f) => {
      await publishObservedEngine(f.work, f.receipt, f.deps)
      await refused(
        () =>
          stopObservedEngine(f.work, 'restart', {
            ...f.deps,
            startHelper: () => ({
              completion: Promise.resolve({
                pid: 43,
                exitCode: 0,
                signal: null,
                stdout,
              }),
              abandon() {},
            }),
          }),
        'helper_interruption',
      )
      assert.equal(lockExists(f), true)
    }))

test('stop pointer publication failure keeps terminal receipt unaccepted', async () =>
  fixture(async (f) => {
    await publishObservedEngine(f.work, f.receipt, f.deps)
    await refused(
      () =>
        stopObservedEngine(f.work, 'restart', {
          ...f.deps,
          io: {
            ...createLifecycleIo(),
            rename() {
              throw new Error('injected pointer replacement failure')
            },
          },
        }),
      'result_publication_failure',
    )
    assert.equal(f.calls.length, 1)
    assert.equal(f.current().state, 'live')
    assert.equal(
      fs.existsSync(
        path.join(f.work, 'engine-lifecycle', f.calls[0].resultName),
      ),
      true,
    )
    assert.equal(lockExists(f), true)
    await refused(
      () => stopObservedEngine(f.work, 'teardown', f.deps),
      'lock_conflict',
    )
  }))

test('changed accepted receipt hash refuses duplicate stop and successor', async () =>
  fixture(async (f) => {
    await publishObservedEngine(f.work, f.receipt, f.deps)
    await stopObservedEngine(f.work, 'restart', f.deps)
    const pointer = f.current()
    if (pointer.state !== 'stopped') assert.fail('stopped pointer required')
    pointer.stop.sha256 = 'b'.repeat(64)
    fs.writeFileSync(
      path.join(f.work, 'engine-lifecycle/current.json'),
      JSON.stringify(pointer),
    )
    await refused(
      () => stopObservedEngine(f.work, 'teardown', f.deps),
      'invalid_lease',
    )
    await refused(
      () => publishObservedEngine(f.work, f.receipt, f.deps),
      'invalid_lease',
    )
    assert.equal(f.calls.length, 1)
  }))

for (const method of ['sync', 'close'] as const)
  test(`lease ${method} failure never advances current`, async () =>
    fixture(async (f) => {
      const io = createLifecycleIo()
      let calls = 0
      const patched = {
        ...io,
        [method]: (fd: number) => {
          calls++
          // Lock publication is the first file close, or first file/directory sync pair.
          if (calls === (method === 'sync' ? 3 : 2))
            throw new Error('injected custody failure')
          io[method](fd)
        },
      }
      await refused(
        () =>
          publishObservedEngine(f.work, f.receipt, { ...f.deps, io: patched }),
        'invalid_custody',
      )
      assert.equal(
        fs.existsSync(path.join(f.work, 'engine-lifecycle/current.json')),
        false,
      )
      assert.equal(f.calls.length, 0)
    }))

test('failed publication after a stopped predecessor cannot let teardown accept the old generation', async () =>
  fixture(async (f) => {
    await publishObservedEngine(f.work, f.receipt, f.deps)
    await stopObservedEngine(f.work, 'restart', f.deps)
    await refused(
      () =>
        publishObservedEngine(f.work, f.receipt, {
          ...f.deps,
          inspect() {
            throw new EngineLifecycleError('unavailable_process')
          },
        }),
      'unavailable_process',
    )
    assert.equal(lockExists(f), true)
    await refused(
      () => stopObservedEngine(f.work, 'teardown', f.deps),
      'lock_conflict',
    )
    assert.equal(f.calls.length, 1)
  }))

test('abandonment failure cannot release an interrupted helper operation', async () =>
  fixture(async (f) => {
    await publishObservedEngine(f.work, f.receipt, f.deps)
    await refused(
      () =>
        stopObservedEngine(f.work, 'restart', {
          ...f.deps,
          startHelper: () => ({
            completion: Promise.reject(new Error('pipe failure')),
            abandon() {
              throw new Error('reference cleanup failure')
            },
          }),
        }),
      'helper_interruption',
    )
    assert.equal(lockExists(f), true)
    assert.equal(f.current().state, 'live')
  }))

test('publication refusal names the owned estate, operation and retained evidence', async () =>
  fixture(async (f) => {
    const context: LifecycleErrorContext = {
      operation: 'publish',
      work_dir: f.work,
      purpose: null,
    }
    await refusedWithContext(
      () =>
        publishObservedEngine(f.work, f.receipt, {
          ...f.deps,
          inspect: () => ({ ...f.live, cwd: '/elsewhere' }),
        }),
      'ownership_mismatch',
      context,
    )
    assert.equal(f.calls.length, 0)
    assert.equal(lockExists(f), true)
    assert.equal(
      fs.existsSync(path.join(f.work, 'engine-lifecycle/current.json')),
      false,
    )
    assertRetainedContext(f, 'ownership_mismatch', context)
  }))

test('raw inspect failure is classified at the owned publication boundary', async () =>
  fixture(async (f) => {
    const context: LifecycleErrorContext = {
      operation: 'publish',
      work_dir: f.work,
      purpose: null,
    }
    const error = await refusedWithContext(
      () =>
        publishObservedEngine(f.work, f.receipt, {
          ...f.deps,
          inspect() {
            throw new Error(RAW_INSPECT_SENTINEL)
          },
        }),
      'invalid_custody',
      context,
    )
    assert.equal(f.calls.length, 0)
    assert.equal(lockExists(f), true)
    assert.equal(
      fs.existsSync(path.join(f.work, 'engine-lifecycle/current.json')),
      false,
    )
    assertRetainedContext(f, 'invalid_custody', context)
    assertSentinelAbsent(f, error, RAW_INSPECT_SENTINEL)
  }))

for (const purpose of ['activation', 'restart', 'teardown'] as const)
  test(`pre-signal ${purpose} stop refusal names the owned estate and purpose`, async () =>
    fixture(async (f) => {
      await publishObservedEngine(f.work, f.receipt, f.deps)
      fs.appendFileSync(path.join(f.work, f.receipt.relativePath), ' ')
      await refusedWithContext(
        () => stopObservedEngine(f.work, purpose, f.deps),
        'invalid_lease',
        { operation: 'stop', work_dir: f.work, purpose },
      )
      // A pre-signal refusal keeps the existing release and pointer semantics.
      assert.equal(f.calls.length, 0)
      assert.equal(lockExists(f), false)
      assert.deepEqual(retainedFailures(f), [])
      assert.equal(f.current().state, 'live')
    }))

test('pre-signal refusal survives a release directory sync failure', async () =>
  fixture(async (f) => {
    await publishObservedEngine(f.work, f.receipt, f.deps)
    fs.appendFileSync(path.join(f.work, f.receipt.relativePath), ' ')
    const io = createLifecycleIo()
    let releaseSyncFailed = false
    const context: LifecycleErrorContext = {
      operation: 'stop',
      work_dir: f.work,
      purpose: 'teardown',
    }
    const error = await refusedWithContext(
      () =>
        stopObservedEngine(f.work, 'teardown', {
          ...f.deps,
          io: {
            ...io,
            sync(fd) {
              if (!lockExists(f)) {
                releaseSyncFailed = true
                throw new Error(RELEASE_SYNC_SENTINEL)
              }
              io.sync(fd)
            },
          },
        }),
      'invalid_lease',
      context,
    )
    assert.equal(releaseSyncFailed, true)
    assert.equal(f.calls.length, 0)
    assert.equal(f.current().state, 'live')
    assert.equal(lockExists(f), false)
    assert.deepEqual(retainedFailures(f), [])
    assertSentinelAbsent(f, error, RELEASE_SYNC_SENTINEL)
  }))

test('successful stop rejects when release directory sync fails after acceptance', async () =>
  fixture(async (f) => {
    await publishObservedEngine(f.work, f.receipt, f.deps)
    const io = createLifecycleIo()
    let releaseSyncFailed = false
    const context: LifecycleErrorContext = {
      operation: 'stop',
      work_dir: f.work,
      purpose: 'restart',
    }
    const error = await refusedWithContext(
      () =>
        stopObservedEngine(f.work, 'restart', {
          ...f.deps,
          io: {
            ...io,
            sync(fd) {
              if (!lockExists(f)) {
                releaseSyncFailed = true
                throw new Error(RELEASE_SYNC_SENTINEL)
              }
              io.sync(fd)
            },
          },
        }),
      'invalid_custody',
      context,
    )
    assert.equal(releaseSyncFailed, true)
    assert.equal(f.calls.length, 1)
    assert.equal(lockExists(f), false)
    const pointer = f.current()
    assert.equal(pointer.state, 'stopped')
    if (pointer.state !== 'stopped') assert.fail('stopped pointer required')
    assert.equal(pointer.stop.name, f.calls[0].resultName)
    assert.equal(
      fs.existsSync(path.join(f.work, 'engine-lifecycle', pointer.stop.name)),
      true,
    )
    assert.deepEqual(retainedFailures(f), [])
    assertSentinelAbsent(f, error, RELEASE_SYNC_SENTINEL)
  }))

test('helper interruption binds its owned estate context to retained evidence', async () =>
  fixture(async (f) => {
    await publishObservedEngine(f.work, f.receipt, f.deps)
    const context: LifecycleErrorContext = {
      operation: 'stop',
      work_dir: f.work,
      purpose: 'restart',
    }
    await refusedWithContext(
      () =>
        stopObservedEngine(f.work, 'restart', {
          ...f.deps,
          startHelper: () => ({
            completion: Promise.reject(new Error('injected pipe failure')),
            abandon() {},
          }),
        }),
      'helper_interruption',
      context,
    )
    assert.equal(lockExists(f), true)
    assert.equal(f.current().state, 'live')
    assertRetainedContext(f, 'helper_interruption', context)
  }))

test('result-publication failure binds the teardown purpose to retained evidence', async () =>
  fixture(async (f) => {
    await publishObservedEngine(f.work, f.receipt, f.deps)
    const context: LifecycleErrorContext = {
      operation: 'stop',
      work_dir: f.work,
      purpose: 'teardown',
    }
    await refusedWithContext(
      () =>
        stopObservedEngine(f.work, 'teardown', {
          ...f.deps,
          io: {
            ...createLifecycleIo(),
            rename() {
              throw new Error('injected pointer replacement failure')
            },
          },
        }),
      'result_publication_failure',
      context,
    )
    assert.equal(f.calls.length, 1)
    assert.equal(lockExists(f), true)
    assert.equal(f.current().state, 'live')
    assertRetainedContext(f, 'result_publication_failure', context)
  }))

test('custody refused before ownership reports an unavailable work directory', async () =>
  fixture(async (f) => {
    fs.chmodSync(f.work, 0o755)
    await refusedWithContext(
      () => stopObservedEngine(f.work, 'teardown', f.deps),
      'invalid_custody',
      { operation: 'stop', work_dir: null, purpose: 'teardown' },
    )
    await refusedWithContext(
      () => publishObservedEngine(f.work, f.receipt, f.deps),
      'invalid_custody',
      { operation: 'publish', work_dir: null, purpose: null },
    )
    // No estate exists, so no owned path may be claimed and no evidence is written.
    assert.equal(fs.existsSync(path.join(f.work, 'engine-lifecycle')), false)
    assert.equal(f.calls.length, 0)
  }))

test('an unvalidated stop purpose is refused without being echoed', async () =>
  fixture(async (f) => {
    const error = await refusedWithContext(
      () =>
        stopObservedEngine(
          f.work,
          'shutdown-everything' as unknown as StopPurpose,
          f.deps,
        ),
      'invalid_lease',
      { operation: 'stop', work_dir: null, purpose: null },
    )
    assert.equal(error.message.includes('shutdown-everything'), false)
    assert.equal(fs.existsSync(path.join(f.work, 'engine-lifecycle')), false)
    assert.equal(f.calls.length, 0)
  }))

for (const source of ['helper_stdout', 'raw_exception', 'environment'] as const)
  test(`a synthetic secret in ${source} reaches neither errors nor evidence`, async () =>
    fixture(async (f) => {
      process.env.K3_E2E_SECRET_SENTINEL = SECRET_SENTINEL
      try {
        let error: EngineLifecycleError
        if (source === 'helper_stdout') {
          await publishObservedEngine(f.work, f.receipt, f.deps)
          error = await refusedWithContext(
            () =>
              stopObservedEngine(f.work, 'restart', {
                ...f.deps,
                startHelper: () => ({
                  completion: Promise.resolve({
                    pid: 43,
                    exitCode: 0,
                    signal: null,
                    stdout: `{"ok":true,"credential":"${SECRET_SENTINEL}"}`,
                  }),
                  abandon() {},
                }),
              }),
            'helper_interruption',
            { operation: 'stop', work_dir: f.work, purpose: 'restart' },
          )
        } else if (source === 'raw_exception') {
          error = await refusedWithContext(
            () =>
              publishObservedEngine(f.work, f.receipt, {
                ...f.deps,
                io: {
                  ...createLifecycleIo(),
                  rename() {
                    throw new Error(`pointer replacement ${SECRET_SENTINEL}`)
                  },
                },
              }),
            'result_publication_failure',
            { operation: 'publish', work_dir: f.work, purpose: null },
          )
        } else {
          error = await refusedWithContext(
            () =>
              publishObservedEngine(f.work, f.receipt, {
                ...f.deps,
                inspect: () => ({ ...f.live, cwd: '/elsewhere' }),
              }),
            'ownership_mismatch',
            { operation: 'publish', work_dir: f.work, purpose: null },
          )
        }
        assert.equal(error.message.includes(SECRET_SENTINEL), false)
        assert.equal(
          JSON.stringify(error.context).includes(SECRET_SENTINEL),
          false,
        )
        assert.equal(retainedFailures(f).length, 1)
        for (const name of lifecycleFiles(f))
          assert.equal(
            fs
              .readFileSync(path.join(f.work, 'engine-lifecycle', name), 'utf8')
              .includes(SECRET_SENTINEL),
            false,
          )
      } finally {
        delete process.env.K3_E2E_SECRET_SENTINEL
      }
    }))

// Node's test runner executes this file's top-level tests sequentially by default.
// These constructor controls rely on that assumption: each one performs its own
// lifecycle operations and reads only the descriptors those operations opened.
test('a thrown directory open and an invalid descriptor return establish no close authority', async () =>
  fixture(async (f) => {
    const thrown = directoryCustody()
    const openError = await refusedWithContext(
      () =>
        publishObservedEngine(f.work, f.receipt, {
          ...f.deps,
          io: {
            ...thrown.io,
            openDirectory() {
              throw new Error(OPEN_DIRECTORY_SENTINEL)
            },
          },
        }),
      'invalid_custody',
      { operation: 'publish', work_dir: null, purpose: null },
    )
    // A thrown open owns nothing, so the constructor closes nothing.
    assert.deepEqual(thrown.opened, [])
    assert.deepEqual(thrown.closed, [])
    assertSentinelAbsent(f, openError, OPEN_DIRECTORY_SENTINEL)
    for (const returned of [-1, 2 ** 53, Number.NaN]) {
      const invalid = directoryCustody()
      const invalidError = await refusedWithContext(
        () =>
          stopObservedEngine(f.work, 'teardown', {
            ...f.deps,
            io: { ...invalid.io, openDirectory: () => returned },
          }),
        'invalid_custody',
        { operation: 'stop', work_dir: null, purpose: 'teardown' },
      )
      // An unowned number is refused and is never passed to close.
      assert.deepEqual(invalid.opened, [])
      assert.deepEqual(invalid.closed, [])
      assert.equal(invalid.closesOf(returned), 0)
      assertSentinelAbsent(f, invalidError, OPEN_DIRECTORY_SENTINEL)
    }
    assert.deepEqual(lifecycleFiles(f), [])
    assert.equal(f.calls.length, 0)
  }))

test('a thrown directory stat closes the one owned descriptor exactly once', async () =>
  fixture(async (f) => {
    const custody = directoryCustody()
    const error = await refusedWithContext(
      () =>
        publishObservedEngine(f.work, f.receipt, {
          ...f.deps,
          io: {
            ...custody.io,
            statDirectory() {
              throw new Error(STAT_DIRECTORY_SENTINEL)
            },
          },
        }),
      'invalid_custody',
      { operation: 'publish', work_dir: null, purpose: null },
    )
    assert.equal(custody.opened.length, 1)
    const fd = custody.opened[0]
    assert.deepEqual(custody.closed, [fd])
    assert.equal(custody.closesOf(fd), 1)
    assert.equal(descriptorClosed(fd), true)
    assert.deepEqual(lifecycleFiles(f), [])
    assert.equal(f.calls.length, 0)
    assertSentinelAbsent(f, error, STAT_DIRECTORY_SENTINEL)
  }))

test('a real directory stat with a perturbed inode is refused and closed once', async () =>
  fixture(async (f) => {
    const custody = directoryCustody()
    const real = createLifecycleIo()
    const error = await refusedWithContext(
      () =>
        stopObservedEngine(f.work, 'restart', {
          ...f.deps,
          io: {
            ...custody.io,
            statDirectory(fd) {
              // A real fstat of the owned descriptor with only its inode perturbed:
              // the identity comparison must reject it.
              const st = real.statDirectory(fd)
              return Object.assign(
                Object.create(Object.getPrototypeOf(st) as object),
                st,
                { ino: st.ino + 1n },
              ) as fs.BigIntStats
            },
          },
        }),
      'invalid_custody',
      { operation: 'stop', work_dir: null, purpose: 'restart' },
    )
    assert.equal(custody.opened.length, 1)
    const fd = custody.opened[0]
    // The mismatch branch uses the one cleanup path; it never closes separately.
    assert.deepEqual(custody.closed, [fd])
    assert.equal(custody.closesOf(fd), 1)
    assert.equal(descriptorClosed(fd), true)
    assert.deepEqual(lifecycleFiles(f), [])
    assert.equal(f.calls.length, 0)
    assertSentinelAbsent(f, error, STAT_DIRECTORY_SENTINEL)
  }))

test('a successful construction keeps its descriptor through the operation and closes it once at teardown', async () =>
  fixture(async (f) => {
    const custody = directoryCustody()
    let observed: { closes: number; device: string; inode: string } | undefined
    const lease = await publishObservedEngine(f.work, f.receipt, {
      ...f.deps,
      inspect: () => {
        // Mid-operation: the transferred descriptor is still open and still names
        // the owned estate directory.
        const st = fs.fstatSync(custody.opened[0], { bigint: true })
        observed = {
          closes: custody.closesOf(custody.opened[0]),
          device: String(st.dev),
          inode: String(st.ino),
        }
        return structuredClone(f.live)
      },
      io: custody.io,
    })
    assert.ok(lease)
    assert.equal(custody.opened.length, 1)
    const fd = custody.opened[0]
    const root = fs.statSync(path.join(f.work, 'engine-lifecycle'), {
      bigint: true,
    })
    assert.deepEqual(observed, {
      closes: 0,
      device: String(root.dev),
      inode: String(root.ino),
    })
    // Exactly one close of the estate descriptor, and it is the last close of the
    // operation: the evidence-file closes precede it and target other numbers.
    assert.equal(custody.closesOf(fd), 1)
    assert.equal(custody.closed.indexOf(fd), custody.closed.length - 1)
    assert.equal(descriptorClosed(fd), true)
    assert.equal(f.current().state, 'live')
    assert.equal(lockExists(f), false)
  }))

test('a failed cleanup close preserves the first classification and is not retried', async () =>
  fixture(async (f) => {
    const custody = directoryCustody()
    const error = await refusedWithContext(
      () =>
        publishObservedEngine(f.work, f.receipt, {
          ...f.deps,
          io: {
            ...custody.io,
            statDirectory() {
              // A distinct classified code: if the discarded close failure replaced
              // it, this control would observe invalid_custody instead.
              throw new EngineLifecycleError('capability_refusal')
            },
            close(fd) {
              // The real descriptor is closed first so this control leaks nothing.
              // That proves no retry follows; it does not establish cleanup after a
              // close that actually failed.
              custody.io.close(fd)
              if (custody.opened.includes(fd))
                throw new Error(CUSTODY_CLOSE_SENTINEL)
            },
          },
        }),
      'capability_refusal',
      { operation: 'publish', work_dir: null, purpose: null },
    )
    assert.equal(custody.opened.length, 1)
    const fd = custody.opened[0]
    assert.equal(custody.closesOf(fd), 1)
    assert.deepEqual(custody.closed, [fd])
    assert.equal(descriptorClosed(fd), true)
    assert.deepEqual(lifecycleFiles(f), [])
    assert.equal(f.calls.length, 0)
    assertSentinelAbsent(f, error, CUSTODY_CLOSE_SENTINEL)
  }))

// Finalization and helper-deadline controls. They drive operation() and
// helperWithDeadline() through the public boundaries with the counted directory
// custody above. Injected classified codes make the reported finalization failure
// observable; with the default io every raw finalization error is invalid_custody.
// A failing close first closes the real descriptor, so these controls prove attempt
// counts, classification and state, not cleanup after a close that truly failed.
const ESTATE_CLOSE_SENTINEL = 'k3-synthetic-estate-close-9e41b7c2d05a4f86'
const ABANDON_SENTINEL = 'k3-synthetic-abandon-6b3f0d8e27c94a15'
const EXPIRING_DEADLINE_MS = 19
const UNEXPIRED_DEADLINE_MS = 4_111
type FinalizationFaults = {
  io: LifecycleIo
  custody: DirectoryCustody
  releaseSyncs(): number
}
// releaseFailure is thrown by the directory sync that Estate.release performs after
// it unlinks the lock, which is the only sync of an operation with no lock file.
// closeFailure is thrown only for the estate directory descriptor.
function finalizationFaults(
  f: Fixture,
  faults: { releaseFailure?: Error; closeFailure?: Error },
): FinalizationFaults {
  const custody = directoryCustody()
  let releaseSyncs = 0
  const io: LifecycleIo = {
    ...custody.io,
    sync(fd) {
      if (!lockExists(f)) {
        releaseSyncs++
        if (faults.releaseFailure) throw faults.releaseFailure
      }
      custody.io.sync(fd)
    },
    close(fd) {
      custody.io.close(fd)
      if (faults.closeFailure && custody.opened.includes(fd))
        throw faults.closeFailure
    },
  }
  return { io, custody, releaseSyncs: () => releaseSyncs }
}
// Call before any other descriptor is opened: the closed-number check is secondary.
function assertEstateClosedOnce(faults: FinalizationFaults): void {
  assert.equal(faults.custody.opened.length, 1)
  const fd = faults.custody.opened[0]
  assert.equal(faults.custody.closesOf(fd), 1)
  assert.equal(
    faults.custody.closed.indexOf(fd),
    faults.custody.closed.length - 1,
  )
  assert.equal(descriptorClosed(fd), true)
}
// Counts the helper deadline timer with node:test spies that call through to the
// real global timer functions and are restored when the test ends. Only timers armed
// with this delay are counted, so other timers in the process cannot affect it.
function deadlineTimers(
  t: TestContext,
  delay: number,
): () => { armed: number; cleared: number } {
  const set = t.mock.method(globalThis, 'setTimeout')
  const clear = t.mock.method(globalThis, 'clearTimeout')
  return () => {
    const handles: unknown[] = set.mock.calls
      .filter((call) => call.arguments[1] === delay)
      .map((call) => call.result)
    return {
      armed: handles.length,
      cleared: clear.mock.calls.filter((call) =>
        handles.includes(call.arguments[0]),
      ).length,
    }
  }
}

test('a completed publication rejects when only its estate close fails, after one close', async () =>
  fixture(async (f) => {
    const faults = finalizationFaults(f, {
      closeFailure: new Error(ESTATE_CLOSE_SENTINEL),
    })
    const error = await refusedWithContext(
      () =>
        publishObservedEngine(f.work, f.receipt, { ...f.deps, io: faults.io }),
      'invalid_custody',
      { operation: 'publish', work_dir: f.work, purpose: null },
    )
    assertEstateClosedOnce(faults)
    assert.equal(faults.releaseSyncs(), 1)
    // The publication's effects stay as written, but the result is not a success.
    assert.equal(lockExists(f), false)
    assert.equal(f.current().state, 'live')
    assert.deepEqual(retainedFailures(f), [])
    assertSentinelAbsent(f, error, ESTATE_CLOSE_SENTINEL)
  }))

test('a completed stop attempts a failing release and close once each and reports the close failure', async () =>
  fixture(async (f) => {
    await publishObservedEngine(f.work, f.receipt, f.deps)
    const faults = finalizationFaults(f, {
      releaseFailure: new EngineLifecycleError('signal_failure'),
      closeFailure: new EngineLifecycleError('capability_refusal'),
    })
    await refusedWithContext(
      () => stopObservedEngine(f.work, 'restart', { ...f.deps, io: faults.io }),
      'capability_refusal',
      { operation: 'stop', work_dir: f.work, purpose: 'restart' },
    )
    assertEstateClosedOnce(faults)
    assert.equal(faults.releaseSyncs(), 1)
    assert.equal(f.calls.length, 1)
    assert.equal(lockExists(f), false)
    assert.equal(f.current().state, 'stopped')
    assert.deepEqual(retainedFailures(f), [])
  }))

test('a pre-signal stop refusal keeps its classification when release and close both fail', async () =>
  fixture(async (f) => {
    await publishObservedEngine(f.work, f.receipt, f.deps)
    fs.appendFileSync(path.join(f.work, f.receipt.relativePath), ' ')
    const faults = finalizationFaults(f, {
      releaseFailure: new EngineLifecycleError('signal_failure'),
      closeFailure: new EngineLifecycleError('capability_refusal'),
    })
    await refusedWithContext(
      () =>
        stopObservedEngine(f.work, 'teardown', { ...f.deps, io: faults.io }),
      'invalid_lease',
      { operation: 'stop', work_dir: f.work, purpose: 'teardown' },
    )
    assertEstateClosedOnce(faults)
    assert.equal(faults.releaseSyncs(), 1)
    assert.equal(f.calls.length, 0)
    assert.equal(lockExists(f), false)
    assert.equal(f.current().state, 'live')
    assert.deepEqual(retainedFailures(f), [])
  }))

test('a retained helper interruption keeps its classification, lock and record when the estate close fails', async () =>
  fixture(async (f) => {
    await publishObservedEngine(f.work, f.receipt, f.deps)
    const faults = finalizationFaults(f, {
      closeFailure: new EngineLifecycleError('capability_refusal'),
    })
    const context: LifecycleErrorContext = {
      operation: 'stop',
      work_dir: f.work,
      purpose: 'activation',
    }
    let abandoned = 0
    await refusedWithContext(
      () =>
        stopObservedEngine(f.work, 'activation', {
          ...f.deps,
          io: faults.io,
          startHelper: () => ({
            completion: Promise.reject(new Error('injected pipe failure')),
            abandon() {
              abandoned++
            },
          }),
        }),
      'helper_interruption',
      context,
    )
    assertEstateClosedOnce(faults)
    // A retained operation is never released.
    assert.equal(faults.releaseSyncs(), 0)
    assert.equal(abandoned, 1)
    assert.equal(lockExists(f), true)
    assert.equal(f.current().state, 'live')
    assertRetainedContext(f, 'helper_interruption', context)
  }))

test('a lock conflict keeps its classification and the held lock when the estate close fails', async () =>
  fixture(async (f) => {
    // A retained failed publication holds the lock for the conflicting stop below.
    await refused(
      () =>
        publishObservedEngine(f.work, f.receipt, {
          ...f.deps,
          inspect() {
            throw new EngineLifecycleError('unavailable_process')
          },
        }),
      'unavailable_process',
    )
    const lock = path.join(f.work, 'engine-lifecycle/operation.lock')
    const held = fs.readFileSync(lock)
    const faults = finalizationFaults(f, {
      closeFailure: new EngineLifecycleError('capability_refusal'),
    })
    await refusedWithContext(
      () =>
        stopObservedEngine(f.work, 'teardown', { ...f.deps, io: faults.io }),
      'lock_conflict',
      { operation: 'stop', work_dir: f.work, purpose: 'teardown' },
    )
    assertEstateClosedOnce(faults)
    assert.equal(faults.releaseSyncs(), 0)
    assert.deepEqual(fs.readFileSync(lock), held)
    assert.equal(retainedFailures(f).length, 1)
    assert.equal(f.calls.length, 0)
  }))

test('an expired helper deadline is an interruption when abandonment throws, after one abandon and one timer clear', async (t) =>
  fixture(async (f) => {
    await publishObservedEngine(f.work, f.receipt, f.deps)
    const timers = deadlineTimers(t, EXPIRING_DEADLINE_MS)
    const context: LifecycleErrorContext = {
      operation: 'stop',
      work_dir: f.work,
      purpose: 'restart',
    }
    let abandoned = 0
    const error = await refusedWithContext(
      () =>
        stopObservedEngine(f.work, 'restart', {
          ...f.deps,
          helperDeadlineMs: EXPIRING_DEADLINE_MS,
          startHelper: () => ({
            // The helper never reports, so only the parent deadline ends the wait.
            completion: new Promise<HelperCompletion>(() => {}),
            abandon() {
              abandoned++
              throw new Error(ABANDON_SENTINEL)
            },
          }),
        }),
      'helper_interruption',
      context,
    )
    assert.equal(abandoned, 1)
    assert.deepEqual(timers(), { armed: 1, cleared: 1 })
    // Abandonment drops parent references only: the helper state stays unknown, and
    // the operation keeps its lock, live pointer and unaccepted failure record.
    assert.equal(lockExists(f), true)
    assert.equal(f.current().state, 'live')
    assertRetainedContext(f, 'helper_interruption', context)
    assertSentinelAbsent(f, error, ABANDON_SENTINEL)
  }))

test('a helper result before its deadline clears the one armed timer once and never abandons', async (t) =>
  fixture(async (f) => {
    await publishObservedEngine(f.work, f.receipt, f.deps)
    const timers = deadlineTimers(t, UNEXPIRED_DEADLINE_MS)
    // The fixture helper reports at once; an abandonment would refuse this stop.
    const stopped = await stopObservedEngine(f.work, 'teardown', {
      ...f.deps,
      helperDeadlineMs: UNEXPIRED_DEADLINE_MS,
    })
    assert.ok(stopped)
    assert.equal(f.calls.length, 1)
    assert.deepEqual(timers(), { armed: 1, cleared: 1 })
    assert.equal(f.current().state, 'stopped')
    assert.equal(lockExists(f), false)
  }))

test('a rejected helper completion is an interruption when abandonment throws, after one abandon and one timer clear', async (t) =>
  fixture(async (f) => {
    await publishObservedEngine(f.work, f.receipt, f.deps)
    const timers = deadlineTimers(t, UNEXPIRED_DEADLINE_MS)
    const context: LifecycleErrorContext = {
      operation: 'stop',
      work_dir: f.work,
      purpose: 'teardown',
    }
    let abandoned = 0
    const error = await refusedWithContext(
      () =>
        stopObservedEngine(f.work, 'teardown', {
          ...f.deps,
          helperDeadlineMs: UNEXPIRED_DEADLINE_MS,
          startHelper: () => ({
            completion: Promise.reject(new Error('injected pipe failure')),
            abandon() {
              abandoned++
              throw new Error(ABANDON_SENTINEL)
            },
          }),
        }),
      'helper_interruption',
      context,
    )
    assert.equal(abandoned, 1)
    // The unexpired deadline timer is cleared on this failure path too.
    assert.deepEqual(timers(), { armed: 1, cleared: 1 })
    assert.equal(lockExists(f), true)
    assert.equal(f.current().state, 'live')
    assertRetainedContext(f, 'helper_interruption', context)
    assertSentinelAbsent(f, error, ABANDON_SENTINEL)
  }))
