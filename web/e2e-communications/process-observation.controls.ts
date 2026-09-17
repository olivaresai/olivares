// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { spawn, type ChildProcess } from 'node:child_process'
import fs from 'node:fs'
import path from 'node:path'
import { after, describe, test } from 'node:test'
import { fileURLToPath } from 'node:url'
import {
  ArgvError,
  PROCESS_OBSERVATION_DIRNAME,
  PROCESS_OBSERVATION_ERROR,
  PROCESS_OBSERVATION_SCHEMA,
  PROCESS_OBSERVATION_TIMEOUT_MS,
  ProcStatError,
  ProcessObservationError,
  createDefaultObservationIo,
  createDefaultProcAccess,
  extractChromiumUserDataDir,
  parseEngineArgv,
  parseProcStat,
  prepareProcessObservationCustody,
  splitCmdline,
  withAwaitedProcessObservation,
  type ObservationIo,
  type ProcAccess,
  type ProcessObservationBrowser,
  type ProcessObservationErrorCode,
} from './process-observation.ts'

const HERE = path.dirname(fileURLToPath(import.meta.url))
const SHELL = fs.realpathSync('/bin/sh')
const TRUE = fs.realpathSync('/bin/true')
const CONTROL_ROOT = fs.mkdtempSync(
  path.join(
    process.env.TMPDIR ?? '/workspace/olivares-ai-control/tmp',
    'k3-po-controls-',
  ),
)
fs.chmodSync(CONTROL_ROOT, 0o700)
const STAY = path.join(CONTROL_ROOT, 'stay.sh')
fs.writeFileSync(STAY, '#!/bin/sh\nwhile :; do /bin/sleep 3600; done\n', {
  mode: 0o700,
})

const ENV_KEYS = [
  'K3_E2E_PROCESS_OBSERVATION',
  'K3_E2E_WORK',
  'TMPDIR',
  'K3_E2E_TMP',
  'K3_E2E_EXPECTED_CHROMIUM_EXECUTABLE',
] as const

after(() => {
  fs.rmSync(CONTROL_ROOT, { recursive: true, force: true })
})

test('the named budget is the provisional 30s fixture ceiling', () => {
  assert.equal(PROCESS_OBSERVATION_TIMEOUT_MS, 30_000)
})

describe('proc stat parser', () => {
  test('reads start ticks as a decimal string after the final closing parenthesis', () => {
    const stat = fakeStat({
      pid: 42,
      comm: 'chrome (dev)',
      startTicks: '18446744073709551615',
      extra: '9 8 7',
    })
    const parsed = parseProcStat(stat, 42)
    assert.equal(parsed.pid, 42)
    assert.equal(parsed.comm, 'chrome (dev)')
    assert.equal(parsed.startTicks, '18446744073709551615')
    assert.equal(typeof parsed.startTicks, 'string')
    assert.equal(parsed.ppid, 7)
    assert.equal(parsed.pgrp, 8)
    assert.equal(parsed.session, 9)
  })

  test('rejects malformed, zombie, dead, and pid mismatch lines', () => {
    assert.throws(() => parseProcStat('no-paren 1 2 3', 1), ProcStatError)
    assert.throws(
      () => parseProcStat(fakeStat({ pid: 1, startTicks: '12.0' }), 1),
      ProcStatError,
    )
    assert.throws(
      () => parseProcStat(fakeStat({ pid: 1, startTicks: '1e6' }), 1),
      ProcStatError,
    )
    assert.throws(
      () => parseProcStat(fakeStat({ pid: 1, state: 'Z', startTicks: '1' }), 1),
      (err: unknown) => err instanceof ProcStatError && err.reason === 'zombie',
    )
    assert.throws(
      () => parseProcStat(fakeStat({ pid: 1, state: 'X', startTicks: '1' }), 1),
      (err: unknown) => err instanceof ProcStatError && err.reason === 'dead',
    )
    assert.throws(
      () => parseProcStat(fakeStat({ pid: 9, startTicks: '1' }), 8),
      (err: unknown) =>
        err instanceof ProcStatError && err.reason === 'pid_mismatch',
    )
    assert.throws(() => parseProcStat('12 (tiny) S 1', 12), ProcStatError)
  })
})

describe('owned argv parsers', () => {
  test('engine flags are separate tokens and reject combined forms and duplicates', () => {
    const parsed = parseEngineArgv([
      SHELL,
      'serve',
      '--insecure',
      '--listen',
      '127.0.0.1:8490',
      '--grpc-listen',
      '127.0.0.1:8491',
      '--data-dir',
      '/work/data',
    ])
    assert.deepEqual(parsed, {
      dataDir: '/work/data',
      listen: '127.0.0.1:8490',
      grpcListen: '127.0.0.1:8491',
    })
    assert.throws(
      () =>
        parseEngineArgv([
          'serve',
          '--listen=127.0.0.1:8490',
          '--grpc-listen',
          '127.0.0.1:8491',
          '--data-dir',
          '/work/data',
        ]),
      ArgvError,
    )
    assert.throws(
      () =>
        parseEngineArgv([
          'serve',
          '--listen',
          '127.0.0.1:8490',
          '--listen',
          '127.0.0.1:8490',
          '--grpc-listen',
          '127.0.0.1:8491',
          '--data-dir',
          '/work/data',
        ]),
      ArgvError,
    )
    assert.throws(
      () => parseEngineArgv(['--listen', '127.0.0.1:8490']),
      ArgvError,
    )
  })

  test('Chromium user-data-dir is the combined token only', () => {
    assert.equal(
      extractChromiumUserDataDir([
        'chrome',
        '--headless',
        '--user-data-dir=/tmp/playwright_chromiumdev_profile-abc',
      ]),
      '/tmp/playwright_chromiumdev_profile-abc',
    )
    assert.throws(
      () =>
        extractChromiumUserDataDir([
          'chrome',
          '--user-data-dir',
          '/tmp/playwright_chromiumdev_profile-abc',
        ]),
      ArgvError,
    )
    assert.throws(
      () => extractChromiumUserDataDir(['chrome', '--user-data-dir=']),
      ArgvError,
    )
    assert.throws(() => extractChromiumUserDataDir(['chrome']), ArgvError)
    assert.throws(
      () =>
        extractChromiumUserDataDir([
          'chrome',
          '--user-data-dir=/a',
          '--user-data-dir=/b',
        ]),
      ArgvError,
    )
  })
})

describe('mode', () => {
  test('unset and empty preserve execution and make no observation claim', async () => {
    for (const value of [undefined, '']) {
      const root = fs.mkdtempSync(path.join(CONTROL_ROOT, 'mode-'))
      fs.chmodSync(root, 0o700)
      let continued = 0
      await withEnv({ K3_E2E_PROCESS_OBSERVATION: value }, async () => {
        const result = await withAwaitedProcessObservation(
          {
            role: 'engine',
            phase: 'seed',
            pid: 1,
            expectedExecutable: SHELL,
            expectedDataDir: root,
            expectedListen: '127.0.0.1:1',
            expectedGrpcListen: '127.0.0.1:2',
          },
          (observation) => {
            assert.equal(observation, undefined)
            continued += 1
            return 'ok'
          },
        )
        assert.equal(result, 'ok')
      })
      assert.equal(continued, 1)
      assert.equal(
        fs.existsSync(path.join(root, PROCESS_OBSERVATION_DIRNAME)),
        false,
      )
    }
  })

  test('any other mode value is invalid configuration and skips continuation', async () => {
    for (const value of ['Required', 'optional', '1', 'true', 'REQUIRED']) {
      let continued = false
      await withEnv({ K3_E2E_PROCESS_OBSERVATION: value }, async () => {
        await assertRejectedCode(
          () =>
            withAwaitedProcessObservation(
              {
                role: 'engine',
                phase: 'seed',
                pid: 1,
                expectedExecutable: SHELL,
                expectedDataDir: '/tmp',
                expectedListen: '127.0.0.1:1',
                expectedGrpcListen: '127.0.0.1:2',
              },
              () => {
                continued = true
              },
            ),
          PROCESS_OBSERVATION_ERROR.invalid_configuration,
        )
      })
      assert.equal(continued, false)
    }
  })
})

describe('live child, custody, and engine identity', () => {
  test('captures a live disposable child through /proc and owned serve argv', async () => {
    await withRequiredRoot(async (ctx) => {
      await prepareProcessObservationCustody()
      const child = spawnSleep(engineArgs(ctx.data))
      let continued = false
      try {
        const record = await withAwaitedProcessObservation(
          engineInput(child.pid as number, ctx.data),
          () => {
            continued = true
            return readLatestRecord(ctx.work)
          },
        )
        assert.equal(continued, true)
        assert.equal(record.schema, PROCESS_OBSERVATION_SCHEMA)
        assert.equal(record.role, 'engine')
        assert.equal(record.phase, 'seed')
        assert.equal(record.pid, child.pid)
        assert.equal(typeof record.start_ticks, 'string')
        assert.match(String(record.start_ticks), /^[0-9]+$/)
        assert.equal(
          (record.executable as { canonical_path: string }).canonical_path,
          SHELL,
        )
        const exe = record.executable as {
          canonical_path: string
          device: string
          inode: string
        }
        const independent = fs.statSync(exe.canonical_path, { bigint: true })
        assert.equal(exe.device, independent.dev.toString(10))
        assert.equal(exe.inode, independent.ino.toString(10))
        assert.equal(record.data_dir, fs.realpathSync(ctx.data))
        assert.equal(record.listen, '127.0.0.1:8490')
        assert.equal(record.grpc_listen, '127.0.0.1:8491')
        assert.equal(record.budget_ms, PROCESS_OBSERVATION_TIMEOUT_MS)
        const packed = JSON.stringify(record)
        assert.equal(packed.includes('SECRET123'), false)
        assert.equal(packed.includes('--insecure'), false)
        assert.equal('argv' in record, false)
        assert.equal('cmdline' in record, false)
      } finally {
        killChild(child)
      }
    })
  })

  test('wrong stable executable fails immediately without continuation', async () => {
    await withRequiredRoot(async (ctx) => {
      await prepareProcessObservationCustody()
      const child = spawnSleep(engineArgs(ctx.data))
      let continued = false
      try {
        await assertRejectedCode(
          () =>
            withAwaitedProcessObservation(
              {
                ...engineInput(child.pid as number, ctx.data),
                expectedExecutable: TRUE,
              },
              () => {
                continued = true
              },
            ),
          PROCESS_OBSERVATION_ERROR.executable_mismatch,
        )
      } finally {
        killChild(child)
      }
      assert.equal(continued, false)
      assert.equal(fs.readdirSync(recordsDir(ctx.work)).length, 0)
    })
  })

  test('a disappeared child is unavailable', async () => {
    await withRequiredRoot(async (ctx) => {
      await prepareProcessObservationCustody()
      const child = spawnSleep(engineArgs(ctx.data))
      const pid = child.pid as number
      killChild(child)
      await waitGone(pid)
      let continued = false
      await assertRejectedCode(
        () =>
          withAwaitedProcessObservation(engineInput(pid, ctx.data), () => {
            continued = true
          }),
        PROCESS_OBSERVATION_ERROR.unavailable_process,
      )
      assert.equal(continued, false)
    })
  })

  test('changed start ticks after identity reads are a changed generation', async () => {
    await withRequiredRoot(async (ctx) => {
      await prepareProcessObservationCustody()
      const child = spawnSleep(engineArgs(ctx.data))
      const childPid = child.pid as number
      const base = createDefaultProcAccess()
      let childReads = 0
      const original = parseProcStat(base.readStat(childPid), childPid)
      const mutatedTicks = original.startTicks === '0' ? '1' : '0'
      const proc: ProcAccess = {
        ...base,
        readStat(pid: number): string {
          const raw = base.readStat(pid)
          if (pid !== childPid) return raw
          childReads += 1
          if (childReads < 2) return raw
          return withStartTicks(raw, mutatedTicks)
        },
      }
      let continued = false
      try {
        await assertRejectedCode(
          () =>
            withAwaitedProcessObservation(
              engineInput(childPid, ctx.data),
              () => {
                continued = true
              },
              { proc },
            ),
          PROCESS_OBSERVATION_ERROR.changed_generation,
        )
      } finally {
        killChild(child)
      }
      assert.equal(continued, false)
      assert.ok(childReads >= 2)
    })
  })

  test('generation replacement during cmdline capture is changed_generation', async () => {
    await withRequiredRoot(async (ctx) => {
      await prepareProcessObservationCustody()
      const child = spawnSleep(engineArgs(ctx.data))
      const childPid = child.pid as number
      const base = createDefaultProcAccess()
      const original = parseProcStat(base.readStat(childPid), childPid)
      const mutatedTicks = original.startTicks === '0' ? '1' : '0'
      let cmdlineReads = 0
      let mutateStat = false
      const proc: ProcAccess = {
        ...base,
        readCmdline(pid: number): Buffer {
          const buf = base.readCmdline(pid)
          if (pid === childPid) {
            cmdlineReads += 1
            mutateStat = true
          }
          return buf
        },
        readStat(pid: number): string {
          const raw = base.readStat(pid)
          if (pid !== childPid || !mutateStat) return raw
          return withStartTicks(raw, mutatedTicks)
        },
      }
      let continued = false
      try {
        await assertRejectedCode(
          () =>
            withAwaitedProcessObservation(
              engineInput(childPid, ctx.data),
              () => {
                continued = true
              },
              { proc },
            ),
          PROCESS_OBSERVATION_ERROR.changed_generation,
        )
      } finally {
        killChild(child)
      }
      assert.equal(continued, false)
      assert.equal(cmdlineReads, 1)
      assert.equal(fs.readdirSync(recordsDir(ctx.work)).length, 0)
    })
  })

  test('malformed /proc/pid/stat is unavailable, not a fabricated success', async () => {
    await withRequiredRoot(async (ctx) => {
      await prepareProcessObservationCustody()
      const child = spawnSleep(engineArgs(ctx.data))
      const childPid = child.pid as number
      const base = createDefaultProcAccess()
      const proc: ProcAccess = {
        ...base,
        readStat(pid: number): string {
          if (pid !== childPid) return base.readStat(pid)
          return 'this is not a stat file'
        },
      }
      let continued = false
      try {
        await assertRejectedCode(
          () =>
            withAwaitedProcessObservation(
              engineInput(childPid, ctx.data),
              () => {
                continued = true
              },
              { proc },
            ),
          PROCESS_OBSERVATION_ERROR.unavailable_process,
        )
      } finally {
        killChild(child)
      }
      assert.equal(continued, false)
    })
  })

  test('permission refusal on exe identity fails immediately', async () => {
    await withRequiredRoot(async (ctx) => {
      await prepareProcessObservationCustody()
      const child = spawnSleep(engineArgs(ctx.data))
      const base = createDefaultProcAccess()
      let calls = 0
      const proc: ProcAccess = {
        ...base,
        statExe(): never {
          calls += 1
          const err = new Error('hidden-os-detail')
          ;(err as { code?: string }).code = 'EACCES'
          throw err
        },
      }
      let continued = false
      try {
        const rejected = await assertRejectedCode(
          () =>
            withAwaitedProcessObservation(
              engineInput(child.pid as number, ctx.data),
              () => {
                continued = true
              },
              { proc },
            ),
          PROCESS_OBSERVATION_ERROR.unavailable_process,
        )
        assert.equal(rejected.message.includes('hidden-os-detail'), false)
      } finally {
        killChild(child)
      }
      assert.equal(continued, false)
      assert.equal(calls, 1)
    })
  })

  test('a Node executable identity is polled until the expected file appears', async () => {
    await withRequiredRoot(async (ctx) => {
      await prepareProcessObservationCustody()
      const child = spawnSleep(engineArgs(ctx.data))
      const base = createDefaultProcAccess()
      let exeReads = 0
      const proc: ProcAccess = {
        ...base,
        statExe(pid: number) {
          exeReads += 1
          if (exeReads < 3) return base.statSelfExe()
          return base.statExe(pid)
        },
      }
      try {
        await withAwaitedProcessObservation(
          engineInput(child.pid as number, ctx.data),
          (observation) => {
            assert.ok(observation)
            assert.equal(observation.record.role, 'engine')
            const bytes = fs.readFileSync(
              path.join(ctx.work, observation.relativePath),
            )
            assert.equal(bytes.toString('utf8'), observation.serialized)
            assert.equal(
              createHash('sha256').update(bytes).digest('hex'),
              observation.sha256,
            )
            assert.deepEqual(
              JSON.parse(observation.serialized),
              observation.record,
            )
            assert.ok(Object.isFrozen(observation))
            assert.ok(Object.isFrozen(observation.record.executable))
            assert.throws(() => {
              ;(observation.record.executable as { inode: string }).inode =
                'modified'
            }, TypeError)
          },
          { proc },
        )
        assert.ok(exeReads >= 3)
        const record = readLatestRecord(ctx.work)
        assert.equal(
          (record.executable as { canonical_path: string }).canonical_path,
          SHELL,
        )
      } finally {
        killChild(child)
      }
    })
  })

  test('duplicate exclusive records are refused', async () => {
    await withRequiredRoot(async (ctx) => {
      await prepareProcessObservationCustody()
      const child = spawnSleep(engineArgs(ctx.data))
      const index = { next: 0 }
      try {
        await withAwaitedProcessObservation(
          engineInput(child.pid as number, ctx.data),
          () => undefined,
          { index },
        )
        assert.equal(fs.readdirSync(recordsDir(ctx.work)).length, 1)
        let continued = false
        index.next = 0
        await assertRejectedCode(
          () =>
            withAwaitedProcessObservation(
              engineInput(child.pid as number, ctx.data),
              () => {
                continued = true
              },
              { index },
            ),
          PROCESS_OBSERVATION_ERROR.invalid_output_custody,
        )
        assert.equal(continued, false)
        assert.equal(fs.readdirSync(recordsDir(ctx.work)).length, 1)
      } finally {
        killChild(child)
      }
    })
  })

  test('create refuses a preexisting observations directory; attach refuses a missing one', async () => {
    await withRequiredRoot(async (ctx) => {
      fs.mkdirSync(recordsDir(ctx.work), { mode: 0o700 })
      await assertRejectedCode(
        () => prepareProcessObservationCustody(),
        PROCESS_OBSERVATION_ERROR.invalid_output_custody,
      )
    })
    await withRequiredRoot(async (ctx) => {
      let continued = false
      await assertRejectedCode(
        () =>
          withAwaitedProcessObservation(
            engineInput(process.pid, ctx.data),
            () => {
              continued = true
            },
          ),
        PROCESS_OBSERVATION_ERROR.invalid_output_custody,
      )
      assert.equal(continued, false)
    })
  })

  test('persists device and inode decimals above the Number safe range', async () => {
    await withRequiredRoot(async (ctx) => {
      await prepareProcessObservationCustody()
      const child = spawnSleep(engineArgs(ctx.data))
      const childPid = child.pid as number
      const huge = { dev: 11n, ino: 2n ** 53n + 9n }
      const base = createDefaultProcAccess()
      const proc: ProcAccess = {
        ...base,
        statExe(pid: number) {
          if (pid === childPid) return huge
          return base.statExe(pid)
        },
        statPath(target: string) {
          if (target === SHELL) return huge
          return base.statPath(target)
        },
      }
      try {
        const record = await withAwaitedProcessObservation(
          engineInput(childPid, ctx.data),
          () => readLatestRecord(ctx.work),
          { proc },
        )
        const exe = record.executable as { device: string; inode: string }
        assert.equal(exe.device, huge.dev.toString(10))
        assert.equal(exe.inode, huge.ino.toString(10))
        assert.notEqual(exe.inode, String(Number(huge.ino)))
      } finally {
        killChild(child)
      }
    })
  })
})

describe('record publication custody', () => {
  test('write, fsync, chmod and close failures after creation leave no record and skip continuation', async () => {
    const ops: Array<keyof ObservationIo> = [
      'write',
      'fsync',
      'fchmod',
      'close',
    ]
    for (const op of ops) {
      await withRequiredRoot(async (ctx) => {
        await prepareProcessObservationCustody()
        const child = spawnSleep(engineArgs(ctx.data))
        const baseIo = createDefaultObservationIo()
        const boom = (): never => {
          throw Object.assign(new Error(`injected-${op}`), { code: 'EIO' })
        }
        const io: ObservationIo = {
          ...baseIo,
          write(fd, buffer, offset, length) {
            if (op === 'write') boom()
            return baseIo.write(fd, buffer, offset, length)
          },
          fsync(fd) {
            baseIo.fsync(fd)
            if (op === 'fsync') boom()
          },
          fchmod(fd, mode) {
            baseIo.fchmod(fd, mode)
            if (op === 'fchmod') boom()
          },
          close(fd) {
            if (op === 'close') {
              baseIo.close(fd)
              boom()
            }
            baseIo.close(fd)
          },
        }
        let continued = false
        try {
          await assertRejectedCode(
            () =>
              withAwaitedProcessObservation(
                engineInput(child.pid as number, ctx.data),
                () => {
                  continued = true
                },
                { io },
              ),
            PROCESS_OBSERVATION_ERROR.invalid_output_custody,
          )
        } finally {
          killChild(child)
        }
        assert.equal(continued, false, op)
        assert.equal(fs.readdirSync(recordsDir(ctx.work)).length, 0, op)
      })
    }
  })

  test('zero write progress is invalid custody and does not loop', async () => {
    await withRequiredRoot(async (ctx) => {
      await prepareProcessObservationCustody()
      const child = spawnSleep(engineArgs(ctx.data))
      let writes = 0
      const io: ObservationIo = {
        ...createDefaultObservationIo(),
        write(): number {
          writes += 1
          return 0
        },
      }
      let continued = false
      try {
        await assertRejectedCode(
          () =>
            withAwaitedProcessObservation(
              engineInput(child.pid as number, ctx.data),
              () => {
                continued = true
              },
              { io },
            ),
          PROCESS_OBSERVATION_ERROR.invalid_output_custody,
        )
      } finally {
        killChild(child)
      }
      assert.equal(continued, false)
      assert.equal(writes, 1)
      assert.equal(fs.readdirSync(recordsDir(ctx.work)).length, 0)
    })
  })

  test('valid short writes complete without truncation', async () => {
    await withRequiredRoot(async (ctx) => {
      await prepareProcessObservationCustody()
      const child = spawnSleep(engineArgs(ctx.data))
      const baseIo = createDefaultObservationIo()
      const io: ObservationIo = {
        ...baseIo,
        write(fd, buffer, offset, length): number {
          return baseIo.write(fd, buffer, offset, Math.min(3, length))
        },
      }
      try {
        const record = await withAwaitedProcessObservation(
          engineInput(child.pid as number, ctx.data),
          () => readLatestRecord(ctx.work),
          { io },
        )
        assert.equal(record.schema, PROCESS_OBSERVATION_SCHEMA)
        const names = fs.readdirSync(recordsDir(ctx.work))
        assert.equal(names.length, 1)
        const raw = fs.readFileSync(
          path.join(recordsDir(ctx.work), names[0]),
          'utf8',
        )
        assert.equal(raw.endsWith('\n'), true)
        assert.deepEqual(JSON.parse(raw), record)
      } finally {
        killChild(child)
      }
    })
  })

  test('exclusive creation preserves a preexisting destination', async () => {
    await withRequiredRoot(async (ctx) => {
      await prepareProcessObservationCustody()
      const child = spawnSleep(engineArgs(ctx.data))
      const index = { next: 0 }
      try {
        await withAwaitedProcessObservation(
          engineInput(child.pid as number, ctx.data),
          () => undefined,
          { index },
        )
        const names = fs.readdirSync(recordsDir(ctx.work))
        assert.equal(names.length, 1)
        const dest = path.join(recordsDir(ctx.work), names[0])
        const before = fs.readFileSync(dest)
        index.next = 0
        let continued = false
        await assertRejectedCode(
          () =>
            withAwaitedProcessObservation(
              engineInput(child.pid as number, ctx.data),
              () => {
                continued = true
              },
              { index },
            ),
          PROCESS_OBSERVATION_ERROR.invalid_output_custody,
        )
        assert.equal(continued, false)
        assert.equal(fs.readdirSync(recordsDir(ctx.work)).length, 1)
        assert.deepEqual(fs.readFileSync(dest), before)
      } finally {
        killChild(child)
      }
    })
  })

  test('denied cleanup still refuses publication and does not continue', async () => {
    await withRequiredRoot(async (ctx) => {
      await prepareProcessObservationCustody()
      const child = spawnSleep(engineArgs(ctx.data))
      const io: ObservationIo = {
        ...createDefaultObservationIo(),
        write(): never {
          throw Object.assign(new Error('injected-write'), { code: 'EIO' })
        },
        unlink(): never {
          throw Object.assign(new Error('injected-unlink'), { code: 'EPERM' })
        },
      }
      let continued = false
      try {
        await assertRejectedCode(
          () =>
            withAwaitedProcessObservation(
              engineInput(child.pid as number, ctx.data),
              () => {
                continued = true
              },
              { io },
            ),
          PROCESS_OBSERVATION_ERROR.invalid_output_custody,
        )
      } finally {
        killChild(child)
      }
      assert.equal(continued, false)
      assert.equal(fs.readdirSync(recordsDir(ctx.work)).length, 1)
    })
  })
})

describe('temporary root mode', () => {
  test('setup refuses 0755 and 0770 without repairing the caller root', async () => {
    for (const mode of [0o755, 0o770]) {
      await withRequiredRoot(async (ctx) => {
        fs.chmodSync(ctx.root, mode)
        let continued = false
        await assertRejectedCode(
          () => prepareProcessObservationCustody(),
          PROCESS_OBSERVATION_ERROR.invalid_configuration,
        )
        const st = fs.lstatSync(ctx.root, { bigint: true })
        assert.equal(st.mode & 0o777n, BigInt(mode))
        await assertRejectedCode(
          () =>
            withAwaitedProcessObservation(
              engineInput(process.pid, ctx.data),
              () => {
                continued = true
              },
            ),
          PROCESS_OBSERVATION_ERROR.invalid_configuration,
        )
        assert.equal(continued, false)
        assert.equal(st.mode & 0o777n, BigInt(mode))
      })
    }
  })

  test('worker refuses 0755 and 0770 after setup created the observations directory', async () => {
    for (const mode of [0o755, 0o770]) {
      await withRequiredRoot(async (ctx) => {
        await prepareProcessObservationCustody()
        fs.chmodSync(ctx.root, mode)
        const child = spawnSleep(engineArgs(ctx.data))
        let continued = false
        try {
          await assertRejectedCode(
            () =>
              withAwaitedProcessObservation(
                engineInput(child.pid as number, ctx.data),
                () => {
                  continued = true
                },
              ),
            PROCESS_OBSERVATION_ERROR.invalid_configuration,
          )
        } finally {
          killChild(child)
        }
        assert.equal(continued, false)
        const st = fs.lstatSync(ctx.root, { bigint: true })
        assert.equal(st.mode & 0o777n, BigInt(mode))
        assert.equal(fs.readdirSync(recordsDir(ctx.work)).length, 0)
      })
    }
  })
})

describe('browser protocol seam', () => {
  test('joins a live child PID from a single browser processInfo row', async () => {
    await withRequiredRoot(async (ctx) => {
      await prepareProcessObservationCustody()
      const profile = path.join(ctx.root, 'playwright_chromiumdev_profile-x')
      fs.mkdirSync(profile, { mode: 0o700 })
      const child = spawnSleep([
        '30',
        `--user-data-dir=${profile}`,
        '--headless',
      ])
      const pid = child.pid as number
      let detached = 0
      const browser = fakeBrowser({
        send: async () => ({
          processInfo: [
            { type: 'renderer', id: pid + 10, cpuTime: 9.9 },
            { type: 'browser', id: pid, cpuTime: 3.14 },
          ],
        }),
        detach: async () => {
          detached += 1
        },
      })
      let continued = false
      try {
        const record = await withAwaitedProcessObservation(
          { role: 'browser', phase: 'beforeEach', browser },
          () => {
            continued = true
            return readLatestRecord(ctx.work)
          },
        )
        assert.equal(continued, true)
        assert.equal(record.role, 'browser')
        assert.equal(record.pid, pid)
        assert.equal(record.user_data_dir, fs.realpathSync(profile))
        const packed = JSON.stringify(record)
        assert.equal(packed.includes('cpuTime'), false)
        assert.equal(packed.includes(String(pid + 10)), false)
        assert.equal(packed.includes('renderer'), false)
        assert.equal(detached, 1)
      } finally {
        killChild(child)
      }
    })
  })

  test('absent and duplicate browser entries fail CDP capability', async () => {
    await withRequiredRoot(async () => {
      await prepareProcessObservationCustody()
      for (const processInfo of [
        [],
        [
          { type: 'browser', id: 11 },
          { type: 'browser', id: 12 },
        ],
        [{ type: 'renderer', id: 11 }],
        [{ type: 'browser', id: 0 }],
        [{ type: 'browser', id: 1.5 }],
      ]) {
        let continued = false
        await assertRejectedCode(
          () =>
            withAwaitedProcessObservation(
              {
                role: 'browser',
                phase: 'beforeEach',
                browser: fakeBrowser({
                  send: async () => ({ processInfo }),
                  detach: async () => undefined,
                }),
              },
              () => {
                continued = true
              },
            ),
          PROCESS_OBSERVATION_ERROR.cdp_capability,
        )
        assert.equal(continued, false)
      }
    })
  })

  test('CDP failure keeps a fixed error and drops the protocol message', async () => {
    await withRequiredRoot(async () => {
      await prepareProcessObservationCustody()
      let continued = false
      const rejected = await assertRejectedCode(
        () =>
          withAwaitedProcessObservation(
            {
              role: 'browser',
              phase: 'beforeEach',
              browser: fakeBrowser({
                send: async () => {
                  throw new Error('ECONNRESET secret-protocol-detail')
                },
                detach: async () => undefined,
              }),
            },
            () => {
              continued = true
            },
          ),
        PROCESS_OBSERVATION_ERROR.cdp_capability,
      )
      assert.equal(continued, false)
      assert.equal(rejected.message.includes('secret-protocol-detail'), false)
      assert.equal(rejected.message.includes('ECONNRESET'), false)
    })
  })

  test('timeout abandons a hanging CDP call, detaches, and does not continue', async () => {
    await withRequiredRoot(async (ctx) => {
      await prepareProcessObservationCustody()
      let detached = 0
      let continued = false
      const rejected = await assertRejectedCode(
        () =>
          withAwaitedProcessObservation(
            {
              role: 'browser',
              phase: 'beforeEach',
              browser: fakeBrowser({
                send: () => new Promise(() => undefined),
                detach: async () => {
                  detached += 1
                },
              }),
            },
            () => {
              continued = true
            },
            { budgetMs: 40 },
          ),
        PROCESS_OBSERVATION_ERROR.timeout,
      )
      assert.equal(continued, false)
      assert.equal(rejected.budget_ms, 40)
      assert.ok(rejected.elapsed_ms >= 20)
      assert.ok(rejected.elapsed_ms < 5_000)
      assert.equal(detached, 1)
      assert.equal(fs.readdirSync(recordsDir(ctx.work)).length, 0)
    })
  })

  test('late detach rejection is consumed and does not become success', async () => {
    await withRequiredRoot(async () => {
      await prepareProcessObservationCustody()
      let continued = false
      const rejected = await assertRejectedCode(
        () =>
          withAwaitedProcessObservation(
            {
              role: 'browser',
              phase: 'beforeEach',
              browser: fakeBrowser({
                send: () => new Promise(() => undefined),
                detach: () =>
                  new Promise((_, reject) => {
                    const timer = setTimeout(() => {
                      reject(new Error('late-detach-secret'))
                    }, 20)
                    timer.unref()
                  }),
              }),
            },
            () => {
              continued = true
            },
            { budgetMs: 30 },
          ),
        PROCESS_OBSERVATION_ERROR.timeout,
      )
      assert.equal(continued, false)
      assert.equal(rejected.message.includes('late-detach-secret'), false)
    })
  })

  test('a symlink profile entry is refused', async () => {
    await withRequiredRoot(async (ctx) => {
      await prepareProcessObservationCustody()
      const realProfile = path.join(ctx.root, 'real-profile')
      fs.mkdirSync(realProfile, { mode: 0o700 })
      const link = path.join(ctx.root, 'link-profile')
      fs.symlinkSync(realProfile, link)
      const child = spawnSleep(['30', `--user-data-dir=${link}`])
      let continued = false
      try {
        await assertRejectedCode(
          () =>
            withAwaitedProcessObservation(
              {
                role: 'browser',
                phase: 'beforeEach',
                browser: fakeBrowser({
                  send: async () => ({
                    processInfo: [{ type: 'browser', id: child.pid as number }],
                  }),
                  detach: async () => undefined,
                }),
              },
              () => {
                continued = true
              },
            ),
          PROCESS_OBSERVATION_ERROR.invalid_configuration,
        )
      } finally {
        killChild(child)
      }
      assert.equal(continued, false)
    })
  })
})

describe('callers await the same helper', () => {
  test('setup, restart, and beforeEach await withAwaitedProcessObservation', () => {
    const setup = fs.readFileSync(path.join(HERE, 'global-setup.ts'), 'utf8')
    const journey = fs.readFileSync(path.join(HERE, 'journey.spec.ts'), 'utf8')
    assert.match(setup, /from '\.\/process-observation\.ts'/)
    assert.match(setup, /await prepareProcessObservationCustody\(\)/)
    assert.match(setup, /async function boot\(/)
    const boot = extractBlock(setup, 'async function boot(')
    assert.match(boot, /writeFileSync\(path\.join\(work, 'engine\.pid'/)
    assert.ok(
      boot.indexOf("writeFileSync(path.join(work, 'engine.pid'") <
        boot.indexOf('withAwaitedProcessObservation'),
    )
    assert.match(boot, /return await withAwaitedProcessObservation\(/)
    assert.ok(
      setup.indexOf('await prepareProcessObservationCustody()') <
        setup.indexOf('await boot(work, data, true)'),
    )
    assert.match(setup, /await boot\(work, data, true\)/)
    assert.match(setup, /await boot\(work, data, false\)/)

    const restart = extractBlock(journey, 'async function restartEngine(')
    assert.match(restart, /writeFileSync\(pidFile, String\(child\.pid\)\)/)
    assert.ok(
      restart.indexOf('writeFileSync(pidFile, String(child.pid))') <
        restart.indexOf('withAwaitedProcessObservation'),
    )
    assert.match(restart, /await withAwaitedProcessObservation\(/)
    assert.ok(restart.includes('/healthz'))
    assert.ok(
      restart.indexOf('withAwaitedProcessObservation') <
        restart.indexOf('/healthz'),
    )

    assert.match(
      journey,
      /test\.beforeEach\(async \(\{ browser \}\) => \{[\s\S]*?await withAwaitedProcessObservation\(/,
    )
    assert.match(
      journey,
      /test\.beforeEach\(async \(\{ browser \}\) => \{[\s\S]*?phase: 'beforeEach'/,
    )
    const hookRegion = journey.slice(
      journey.indexOf('test.beforeEach(async ({ browser }) => {'),
      journey.indexOf('test.afterAll'),
    )
    assert.equal(hookRegion.includes('newBrowserCDPSession'), false)
  })
})

test('cmdline split keeps NULs as argv boundaries', () => {
  assert.deepEqual(splitCmdline(Buffer.from('a\0b\0c\0')), ['a', 'b', 'c'])
  assert.deepEqual(splitCmdline(Buffer.from('a\0b\0c')), ['a', 'b', 'c'])
})

function engineArgs(dataDir: string): string[] {
  return [
    'serve',
    '--insecure',
    '--listen',
    '127.0.0.1:8490',
    '--grpc-listen',
    '127.0.0.1:8491',
    '--data-dir',
    dataDir,
    '--token=SECRET123',
  ]
}

function engineInput(pid: number, dataDir: string) {
  return {
    role: 'engine' as const,
    phase: 'seed' as const,
    pid,
    expectedExecutable: SHELL,
    expectedDataDir: dataDir,
    expectedListen: '127.0.0.1:8490',
    expectedGrpcListen: '127.0.0.1:8491',
  }
}

function spawnSleep(args: string[]): ChildProcess {
  const child = spawn(SHELL, [STAY, ...args], {
    stdio: 'ignore',
    detached: true,
  })
  if (!child.pid) throw new Error('fixture child did not start')
  child.unref()
  return child
}

function killChild(child: ChildProcess): void {
  if (!child.pid) return
  try {
    process.kill(-child.pid, 'SIGKILL')
  } catch {
    /* group already gone */
  }
  try {
    process.kill(child.pid, 'SIGKILL')
  } catch {
    /* gone */
  }
}

async function waitGone(pid: number): Promise<void> {
  for (let i = 0; i < 50; i++) {
    if (!fs.existsSync(`/proc/${pid}`)) return
    await new Promise((resolve) => setTimeout(resolve, 20))
  }
  throw new Error(`pid ${pid} still present`)
}

function recordsDir(work: string): string {
  return path.join(work, PROCESS_OBSERVATION_DIRNAME)
}

function readLatestRecord(work: string): Record<string, unknown> {
  const names = fs.readdirSync(recordsDir(work)).sort()
  assert.ok(names.length > 0)
  return JSON.parse(
    fs.readFileSync(
      path.join(recordsDir(work), names[names.length - 1]),
      'utf8',
    ),
  ) as Record<string, unknown>
}

function fakeBrowser(session: {
  send: () => Promise<unknown>
  detach: () => Promise<void>
}): ProcessObservationBrowser {
  return {
    async newBrowserCDPSession() {
      return session
    },
  }
}

async function assertRejectedCode(
  run: () => Promise<unknown>,
  code: ProcessObservationErrorCode,
): Promise<ProcessObservationError> {
  try {
    await run()
  } catch (err) {
    assert.equal(err instanceof ProcessObservationError, true)
    const typed = err as ProcessObservationError
    assert.equal(typed.code, code)
    return typed
  }
  assert.fail(`expected ${code}`)
}

async function withRequiredRoot(
  fn: (ctx: { root: string; work: string; data: string }) => Promise<void>,
): Promise<void> {
  const root = fs.mkdtempSync(path.join(CONTROL_ROOT, 'run-'))
  fs.chmodSync(root, 0o700)
  const work = path.join(root, 'work')
  const data = path.join(work, 'data')
  fs.mkdirSync(data, { recursive: true, mode: 0o700 })
  await withEnv(
    {
      K3_E2E_PROCESS_OBSERVATION: 'required',
      K3_E2E_WORK: work,
      TMPDIR: root,
      K3_E2E_TMP: root,
      K3_E2E_EXPECTED_CHROMIUM_EXECUTABLE: SHELL,
    },
    async () => {
      await fn({ root, work, data })
    },
  )
}

async function withEnv(
  env: Record<string, string | undefined>,
  fn: () => Promise<void>,
): Promise<void> {
  const saved: Record<string, string | undefined> = {}
  for (const key of ENV_KEYS) saved[key] = process.env[key]
  for (const [key, value] of Object.entries(env)) {
    if (value === undefined) delete process.env[key]
    else process.env[key] = value
  }
  try {
    await fn()
  } finally {
    for (const key of ENV_KEYS) {
      const value = saved[key]
      if (value === undefined) delete process.env[key]
      else process.env[key] = value
    }
  }
}

function fakeStat(opts: {
  pid: number
  comm?: string
  state?: string
  startTicks: string
  extra?: string
}): string {
  const comm = opts.comm ?? 'sleep'
  const state = opts.state ?? 'S'
  const fields = [
    state,
    '7',
    '8',
    '9',
    '0',
    '-1',
    '0',
    '0',
    '0',
    '0',
    '0',
    '0',
    '0',
    '0',
    '0',
    '20',
    '0',
    '1',
    '0',
    opts.startTicks,
    '0',
    '0',
  ]
  const extra = opts.extra ? ` ${opts.extra}` : ''
  return `${opts.pid} (${comm}) ${fields.join(' ')}${extra}\n`
}

function withStartTicks(stat: string, ticks: string): string {
  const close = stat.lastIndexOf(')')
  const suffix = stat.slice(close + 1).trim()
  const fields = suffix.split(/\s+/)
  fields[19] = ticks
  return `${stat.slice(0, close + 1)} ${fields.join(' ')}\n`
}

function extractBlock(src: string, startToken: string): string {
  const start = src.indexOf(startToken)
  assert.ok(start >= 0, startToken)
  let depth = 0
  let begun = false
  for (let i = start; i < src.length; i++) {
    const ch = src[i]
    if (ch === '{') {
      depth += 1
      begun = true
    } else if (ch === '}') {
      depth -= 1
      if (begun && depth === 0) return src.slice(start, i + 1)
    }
  }
  throw new Error(`unterminated ${startToken}`)
}
