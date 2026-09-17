// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Awaited process observation for the K3 communications fixture. This is test
// infrastructure: it records live engine and Chromium generations when
// K3_E2E_PROCESS_OBSERVATION=required. It is not a product process supervisor.
import fs from 'node:fs'
import { createHash } from 'node:crypto'
import os from 'node:os'
import path from 'node:path'

export const PROCESS_OBSERVATION_TIMEOUT_MS = 30_000
export const PROCESS_OBSERVATION_SCHEMA = 'olivares.k3.process-observation.v1'
export const PROCESS_OBSERVATION_DIRNAME = 'process-observations'

export const PROCESS_OBSERVATION_ERROR = {
  timeout: 'timeout',
  cdp_capability: 'cdp_capability',
  invalid_configuration: 'invalid_configuration',
  unavailable_process: 'unavailable_process',
  changed_generation: 'changed_generation',
  executable_mismatch: 'executable_mismatch',
  invalid_output_custody: 'invalid_output_custody',
} as const

export type ProcessObservationErrorCode =
  (typeof PROCESS_OBSERVATION_ERROR)[keyof typeof PROCESS_OBSERVATION_ERROR]

export class ProcessObservationError extends Error {
  readonly code: ProcessObservationErrorCode
  readonly role: string
  readonly phase: string
  readonly budget_ms: number
  readonly elapsed_ms: number
  constructor(args: {
    code: ProcessObservationErrorCode
    role: string
    phase: string
    budget_ms: number
    elapsed_ms: number
  }) {
    super(`k3 process observation: ${args.code} (${args.role}/${args.phase})`)
    this.name = 'ProcessObservationError'
    this.code = args.code
    this.role = args.role
    this.phase = args.phase
    this.budget_ms = args.budget_ms
    this.elapsed_ms = args.elapsed_ms
  }
}

export class ProcStatError extends Error {
  readonly reason: 'malformed' | 'zombie' | 'dead' | 'pid_mismatch'
  constructor(reason: ProcStatError['reason']) {
    super(`proc stat ${reason}`)
    this.name = 'ProcStatError'
    this.reason = reason
  }
}

export class ArgvError extends Error {
  constructor() {
    super('owned argv rejected')
    this.name = 'ArgvError'
  }
}

export type FileId = { dev: bigint; ino: bigint }

export type ProcAccess = {
  readStat(pid: number): string
  statExe(pid: number): FileId
  statSelfExe(): FileId
  statPath(path: string): FileId
  realpathExe(pid: number): string
  realpathCwd(pid: number): string
  readCmdline(pid: number): Buffer
  readlinkPidNs(pid: number): string
  readlinkSelfPidNs(): string
}

export type ObservationIo = {
  openExclusive(path: string, mode: number): number
  write(fd: number, buffer: Buffer, offset: number, length: number): number
  fsync(fd: number): void
  fchmod(fd: number, mode: number): void
  close(fd: number): void
  unlink(path: string): void
}

export type ObservationClock = { now(): number }

export type ObservationIndex = { next: number }

export type ObservationDependencies = {
  budgetMs?: number
  clock?: ObservationClock
  proc?: ProcAccess
  index?: ObservationIndex
  io?: ObservationIo
}

export type ProcessObservationCdpSession = {
  send(method: string): Promise<unknown>
  detach(): Promise<void>
}

export type ProcessObservationBrowser = {
  newBrowserCDPSession(): Promise<ProcessObservationCdpSession>
}

export type EngineObservationInput = {
  role: 'engine'
  phase: 'seed' | 'activated' | 'restarted'
  pid: number
  expectedExecutable: string
  expectedDataDir: string
  expectedListen: string
  expectedGrpcListen: string
}

export type BrowserObservationInput = {
  role: 'browser'
  phase: 'beforeEach'
  browser: ProcessObservationBrowser
}

export type ProcessObservationInput =
  EngineObservationInput | BrowserObservationInput

export type ParsedProcStat = {
  pid: number
  comm: string
  state: string
  ppid: number
  pgrp: number
  session: number
  startTicks: string
}

export type ParsedEngineArgv = {
  dataDir: string
  listen: string
  grpcListen: string
}

type ObservationBody = {
  schema: typeof PROCESS_OBSERVATION_SCHEMA
  captured_at: string
  pid: number
  start_ticks: string
  parent_pid: number
  process_group: number
  session: number
  executable: { canonical_path: string; device: string; inode: string }
  cwd: string
  observer: { pid: number; start_ticks: string }
  budget_ms: number
  elapsed_ms: number
} & (
  | {
      role: 'engine'
      phase: EngineObservationInput['phase']
      data_dir: string
      listen: string
      grpc_listen: string
    }
  | { role: 'browser'; phase: 'beforeEach'; user_data_dir: string }
)

type DeepReadonly<T> = {
  readonly [K in keyof T]: T[K] extends object ? DeepReadonly<T[K]> : T[K]
}
export type ProcessObservationRecord = DeepReadonly<
  ObservationBody & {
    filename: {
      role: 'engine' | 'browser'
      phase: string
      observer_pid: number
      observer_start_ticks: string
      index: number
      name: string
    }
  }
>
export type ProcessObservationReceipt = DeepReadonly<{
  record: ProcessObservationRecord
  serialized: string
  relativePath: string
  sha256: string
}>

function freezeReceipt<T>(value: T): T {
  if (value !== null && typeof value === 'object') {
    for (const child of Object.values(value)) freezeReceipt(child)
    Object.freeze(value)
  }
  return value
}

type ObservationMode = 'disabled' | 'required' | 'invalid'

type FailFn = (code: ProcessObservationErrorCode) => never

const TIMEOUT_TOKEN = Symbol('process-observation-timeout')
const POLL_MS = 10
const defaultIndex: ObservationIndex = { next: 0 }
const defaultClock: ObservationClock = { now: () => performance.now() }

export function createDefaultProcAccess(): ProcAccess {
  return {
    readStat(pid: number): string {
      return fs.readFileSync(procPath(pid, 'stat'), 'utf8')
    },
    statExe(pid: number): FileId {
      return fileIdFromBigintStats(
        fs.statSync(procPath(pid, 'exe'), { bigint: true }),
      )
    },
    statSelfExe(): FileId {
      return fileIdFromBigintStats(
        fs.statSync('/proc/self/exe', { bigint: true }),
      )
    },
    statPath(target: string): FileId {
      return fileIdFromBigintStats(fs.statSync(target, { bigint: true }))
    },
    realpathExe(pid: number): string {
      return fs.realpathSync(procPath(pid, 'exe'))
    },
    realpathCwd(pid: number): string {
      return fs.realpathSync(procPath(pid, 'cwd'))
    },
    readCmdline(pid: number): Buffer {
      return fs.readFileSync(procPath(pid, 'cmdline'))
    },
    readlinkPidNs(pid: number): string {
      return fs.readlinkSync(procPath(pid, 'ns/pid'))
    },
    readlinkSelfPidNs(): string {
      return fs.readlinkSync('/proc/self/ns/pid')
    },
  }
}

export function createDefaultObservationIo(): ObservationIo {
  return {
    openExclusive(target: string, mode: number): number {
      return fs.openSync(target, 'wx', mode)
    },
    write(fd: number, buffer: Buffer, offset: number, length: number): number {
      return fs.writeSync(fd, buffer, offset, length)
    },
    fsync(fd: number): void {
      fs.fsyncSync(fd)
    },
    fchmod(fd: number, mode: number): void {
      fs.fchmodSync(fd, mode)
    },
    close(fd: number): void {
      fs.closeSync(fd)
    },
    unlink(target: string): void {
      fs.unlinkSync(target)
    },
  }
}

const defaultProc = createDefaultProcAccess()
const defaultIo = createDefaultObservationIo()

export function parseProcStat(
  stat: string,
  expectedPid: number,
): ParsedProcStat {
  const text = stat.trimEnd()
  const head = /^([0-9]+) \(/.exec(text)
  if (!head) throw new ProcStatError('malformed')
  const close = text.lastIndexOf(')')
  if (close < head[0].length) throw new ProcStatError('malformed')
  if (!isDecimalToken(head[1])) throw new ProcStatError('malformed')
  const pid = Number(head[1])
  if (!isPositiveSafeInteger(pid)) throw new ProcStatError('malformed')
  if (pid !== expectedPid) throw new ProcStatError('pid_mismatch')
  const comm = text.slice(head[0].length, close)
  const suffix = text.slice(close + 1).trim()
  if (suffix === '') throw new ProcStatError('malformed')
  const fields = suffix.split(/\s+/)
  if (fields.length < 20) throw new ProcStatError('malformed')
  const state = fields[0]
  if (!/^[A-Za-z]$/.test(state)) throw new ProcStatError('malformed')
  if (state === 'Z') throw new ProcStatError('zombie')
  if (state === 'X' || state === 'x') throw new ProcStatError('dead')
  const ppid = parseNonNegativeSafeInt(fields[1])
  const pgrp = parseNonNegativeSafeInt(fields[2])
  const session = parseNonNegativeSafeInt(fields[3])
  const startTicks = fields[19]
  if (!isDecimalToken(startTicks)) throw new ProcStatError('malformed')
  return { pid, comm, state, ppid, pgrp, session, startTicks }
}

export function splitCmdline(buf: Buffer): string[] {
  const parts = buf.toString('utf8').split('\0')
  if (parts.length > 0 && parts[parts.length - 1] === '') parts.pop()
  return parts
}

export function parseEngineArgv(argv: string[]): ParsedEngineArgv {
  for (const arg of argv) {
    if (
      arg.startsWith('--data-dir=') ||
      arg.startsWith('--listen=') ||
      arg.startsWith('--grpc-listen=')
    ) {
      throw new ArgvError()
    }
  }
  if (argv.filter((arg) => arg === 'serve').length !== 1) throw new ArgvError()
  return {
    dataDir: exactFlagValue(argv, '--data-dir'),
    listen: exactFlagValue(argv, '--listen'),
    grpcListen: exactFlagValue(argv, '--grpc-listen'),
  }
}

export function extractChromiumUserDataDir(argv: string[]): string {
  if (argv.some((arg) => arg === '--user-data-dir')) throw new ArgvError()
  const found = argv.filter((arg) => arg.startsWith('--user-data-dir='))
  if (found.length !== 1) throw new ArgvError()
  const value = found[0].slice('--user-data-dir='.length)
  if (value === '') throw new ArgvError()
  return value
}

export function isLoopbackEndpoint(value: string): boolean {
  const match = /^127\.0\.0\.1:([0-9]+)$/.exec(value)
  if (!match) return false
  const port = Number(match[1])
  return (
    Number.isInteger(port) &&
    port >= 1 &&
    port <= 65535 &&
    match[1] === String(port)
  )
}

export async function prepareProcessObservationCustody(
  deps: ObservationDependencies = {},
): Promise<void> {
  const mode = readMode()
  if (mode === 'disabled') return
  const clock = deps.clock ?? defaultClock
  const budget = deps.budgetMs ?? PROCESS_OBSERVATION_TIMEOUT_MS
  const started = clock.now()
  const fail: FailFn = (code) => {
    throw new ProcessObservationError({
      code,
      role: 'fixture',
      phase: 'custody',
      budget_ms: budget,
      elapsed_ms: elapsedSince(clock, started),
    })
  }
  if (mode === 'invalid') fail(PROCESS_OBSERVATION_ERROR.invalid_configuration)
  try {
    assertLinuxProc(fail)
    const work = workFromEnv(fail)
    verifyTmpRoot(fail)
    expectedChromiumPath(fail, deps.proc ?? defaultProc)
    createOutputDir(work, fail)
  } catch (err) {
    if (err instanceof ProcessObservationError) throw err
    fail(PROCESS_OBSERVATION_ERROR.invalid_output_custody)
  }
}

export async function withAwaitedProcessObservation<T>(
  input: ProcessObservationInput,
  continuation: (
    observation: ProcessObservationReceipt | undefined,
  ) => T | Promise<T>,
  deps: ObservationDependencies = {},
): Promise<T> {
  const mode = readMode()
  if (mode === 'disabled') return await continuation(undefined)
  const observation = await observeRequired(input, deps, mode)
  return await continuation(observation)
}

async function observeRequired(
  input: ProcessObservationInput,
  deps: ObservationDependencies,
  mode: ObservationMode,
): Promise<ProcessObservationReceipt> {
  const clock = deps.clock ?? defaultClock
  const budget = deps.budgetMs ?? PROCESS_OBSERVATION_TIMEOUT_MS
  const started = clock.now()
  const deadlineAt = started + budget
  let abandoned = false
  const fail: FailFn = (code) => {
    throw new ProcessObservationError({
      code,
      role: input.role,
      phase: input.phase,
      budget_ms: budget,
      elapsed_ms: elapsedSince(clock, started),
    })
  }
  const timeoutFail = (): never => {
    abandoned = true
    fail(PROCESS_OBSERVATION_ERROR.timeout)
  }
  if (mode === 'invalid') fail(PROCESS_OBSERVATION_ERROR.invalid_configuration)
  const proc = deps.proc ?? defaultProc
  const index = deps.index ?? defaultIndex
  let session: ProcessObservationCdpSession | undefined
  try {
    assertLinuxProc(fail)
    const work = workFromEnv(fail)
    const tmpRoot = verifyTmpRoot(fail)
    const chromeReal = expectedChromiumPath(fail, proc)
    const outDir = attachOutputDir(work, fail)
    const observer = readObserverIdentity(proc, fail)
    const io = deps.io ?? defaultIo
    if (input.role === 'engine') {
      if (!isPositiveSafeInteger(input.pid)) {
        fail(PROCESS_OBSERVATION_ERROR.invalid_configuration)
      }
      if (!isLoopbackEndpoint(input.expectedListen)) {
        fail(PROCESS_OBSERVATION_ERROR.invalid_configuration)
      }
      if (!isLoopbackEndpoint(input.expectedGrpcListen)) {
        fail(PROCESS_OBSERVATION_ERROR.invalid_configuration)
      }
      const expected = expectedFile(input.expectedExecutable, fail, proc)
      const live = await captureLiveProcess({
        pid: input.pid,
        expected,
        allowExecTransition: true,
        proc,
        clock,
        deadlineAt,
        fail,
        timeoutFail,
      })
      const owned = parseOwnedEngine(live, input, fail)
      return writeRecord({
        outDir,
        observer,
        index,
        abandoned,
        fail,
        io,
        body: {
          schema: PROCESS_OBSERVATION_SCHEMA,
          role: input.role,
          phase: input.phase,
          captured_at: new Date().toISOString(),
          pid: live.pid,
          start_ticks: live.startTicks,
          parent_pid: live.parentPid,
          process_group: live.processGroup,
          session: live.session,
          executable: live.executable,
          cwd: live.cwd,
          data_dir: owned.dataDir,
          listen: owned.listen,
          grpc_listen: owned.grpcListen,
          observer: {
            pid: observer.pid,
            start_ticks: observer.startTicks,
          },
          budget_ms: budget,
          elapsed_ms: elapsedSince(clock, started),
        },
      })
    }
    session = await raceDeadline(
      input.browser.newBrowserCDPSession(),
      deadlineAt,
      clock,
      (pending) => {
        abandoned = true
        pending.then(
          (late) => {
            const d = late.detach()
            d.then(
              () => undefined,
              () => undefined,
            )
          },
          () => undefined,
        )
      },
    )
    const raw = await raceDeadline(
      session.send('SystemInfo.getProcessInfo'),
      deadlineAt,
      clock,
      (pending) => {
        abandoned = true
        pending.then(
          () => undefined,
          () => undefined,
        )
      },
    )
    const browserPid = browserPidFromProcessInfo(raw, fail)
    assertSamePidNamespace(proc, browserPid, fail)
    const expected = expectedFile(chromeReal, fail, proc)
    const live = await captureLiveProcess({
      pid: browserPid,
      expected,
      allowExecTransition: false,
      proc,
      clock,
      deadlineAt,
      fail,
      timeoutFail,
    })
    const profile = ownedChromiumProfile(live, tmpRoot, fail)
    const receipt = writeRecord({
      outDir,
      observer,
      index,
      abandoned,
      fail,
      io,
      body: {
        schema: PROCESS_OBSERVATION_SCHEMA,
        role: input.role,
        phase: input.phase,
        captured_at: new Date().toISOString(),
        pid: live.pid,
        start_ticks: live.startTicks,
        parent_pid: live.parentPid,
        process_group: live.processGroup,
        session: live.session,
        executable: live.executable,
        cwd: live.cwd,
        user_data_dir: profile,
        observer: {
          pid: observer.pid,
          start_ticks: observer.startTicks,
        },
        budget_ms: budget,
        elapsed_ms: elapsedSince(clock, started),
      },
    })
    await bestEffortDetach(session, deadlineAt, clock)
    return receipt
  } catch (err) {
    await bestEffortDetach(session, deadlineAt, clock)
    if (err instanceof ProcessObservationError) throw err
    if (err === TIMEOUT_TOKEN) timeoutFail()
    if (input.role === 'browser') fail(PROCESS_OBSERVATION_ERROR.cdp_capability)
    fail(PROCESS_OBSERVATION_ERROR.unavailable_process)
  }
}

type LiveProcess = {
  pid: number
  startTicks: string
  parentPid: number
  processGroup: number
  session: number
  executable: { canonical_path: string; device: string; inode: string }
  cwd: string
  argv: string[]
}

async function captureLiveProcess(opts: {
  pid: number
  expected: { real: string; id: FileId }
  allowExecTransition: boolean
  proc: ProcAccess
  clock: ObservationClock
  deadlineAt: number
  fail: FailFn
  timeoutFail: () => never
}): Promise<LiveProcess> {
  const { pid, proc, fail } = opts
  let generation: string | undefined
  for (;;) {
    if (opts.clock.now() >= opts.deadlineAt) opts.timeoutFail()
    const parsed = readParsedStat(proc, pid, fail)
    if (generation !== undefined && parsed.startTicks !== generation) {
      fail(PROCESS_OBSERVATION_ERROR.changed_generation)
    }
    generation = parsed.startTicks
    const exe = tryIdentity(() => {
      const id = proc.statExe(pid)
      const real = proc.realpathExe(pid)
      return { id, real }
    }, fail)
    const cwd = tryIdentity(() => proc.realpathCwd(pid), fail)
    if (exe.kind === 'permission' || cwd.kind === 'permission') {
      fail(PROCESS_OBSERVATION_ERROR.unavailable_process)
    }
    if (exe.kind !== 'ok' || cwd.kind !== 'ok') {
      if (!opts.allowExecTransition) {
        fail(PROCESS_OBSERVATION_ERROR.unavailable_process)
      }
      await sleepBounded(opts.clock, opts.deadlineAt, opts.timeoutFail)
      continue
    }
    if (sameFileId(exe.value.id, proc.statSelfExe())) {
      if (!opts.allowExecTransition) {
        fail(PROCESS_OBSERVATION_ERROR.executable_mismatch)
      }
      await sleepBounded(opts.clock, opts.deadlineAt, opts.timeoutFail)
      continue
    }
    if (
      !sameFileId(exe.value.id, opts.expected.id) ||
      exe.value.real !== opts.expected.real
    ) {
      fail(PROCESS_OBSERVATION_ERROR.executable_mismatch)
    }
    let argv: string[]
    try {
      argv = splitCmdline(proc.readCmdline(pid))
    } catch (err) {
      mapProcErr(err, fail)
    }
    const parsed2 = readParsedStat(proc, pid, fail)
    if (parsed2.startTicks !== generation) {
      fail(PROCESS_OBSERVATION_ERROR.changed_generation)
    }
    return {
      pid,
      startTicks: parsed2.startTicks,
      parentPid: parsed2.ppid,
      processGroup: parsed2.pgrp,
      session: parsed2.session,
      executable: {
        canonical_path: exe.value.real,
        device: exe.value.id.dev.toString(10),
        inode: exe.value.id.ino.toString(10),
      },
      cwd: cwd.value,
      argv,
    }
  }
}

function parseOwnedEngine(
  live: LiveProcess,
  input: EngineObservationInput,
  fail: FailFn,
): { dataDir: string; listen: string; grpcListen: string } {
  let parsed: ParsedEngineArgv
  try {
    parsed = parseEngineArgv(live.argv)
  } catch {
    fail(PROCESS_OBSERVATION_ERROR.invalid_configuration)
  }
  if (parsed.listen !== input.expectedListen) {
    fail(PROCESS_OBSERVATION_ERROR.invalid_configuration)
  }
  if (parsed.grpcListen !== input.expectedGrpcListen) {
    fail(PROCESS_OBSERVATION_ERROR.invalid_configuration)
  }
  if (
    !isLoopbackEndpoint(parsed.listen) ||
    !isLoopbackEndpoint(parsed.grpcListen)
  ) {
    fail(PROCESS_OBSERVATION_ERROR.invalid_configuration)
  }
  let dataDir: string
  let expectedData: string
  try {
    const abs = path.isAbsolute(parsed.dataDir)
      ? parsed.dataDir
      : path.resolve(live.cwd, parsed.dataDir)
    dataDir = fs.realpathSync(abs)
    expectedData = fs.realpathSync(input.expectedDataDir)
  } catch {
    fail(PROCESS_OBSERVATION_ERROR.invalid_configuration)
  }
  if (dataDir !== expectedData) {
    fail(PROCESS_OBSERVATION_ERROR.invalid_configuration)
  }
  return { dataDir, listen: parsed.listen, grpcListen: parsed.grpcListen }
}

function ownedChromiumProfile(
  live: LiveProcess,
  tmpRoot: string,
  fail: FailFn,
): string {
  let raw: string
  try {
    raw = extractChromiumUserDataDir(live.argv)
  } catch {
    fail(PROCESS_OBSERVATION_ERROR.invalid_configuration)
  }
  const abs = path.isAbsolute(raw) ? raw : path.resolve(live.cwd, raw)
  let lst: fs.Stats
  try {
    lst = fs.lstatSync(abs)
  } catch {
    fail(PROCESS_OBSERVATION_ERROR.invalid_configuration)
  }
  if (lst.isSymbolicLink() || !lst.isDirectory()) {
    fail(PROCESS_OBSERVATION_ERROR.invalid_configuration)
  }
  const uid = process.getuid?.()
  if (uid === undefined || lst.uid !== uid) {
    fail(PROCESS_OBSERVATION_ERROR.invalid_configuration)
  }
  let real: string
  try {
    real = fs.realpathSync(abs)
  } catch {
    fail(PROCESS_OBSERVATION_ERROR.invalid_configuration)
  }
  if (!isStrictlyBelow(tmpRoot, real)) {
    fail(PROCESS_OBSERVATION_ERROR.invalid_configuration)
  }
  return real
}

function browserPidFromProcessInfo(raw: unknown, fail: FailFn): number {
  if (raw === null || typeof raw !== 'object') {
    fail(PROCESS_OBSERVATION_ERROR.cdp_capability)
  }
  const info = (raw as { processInfo?: unknown }).processInfo
  if (!Array.isArray(info)) fail(PROCESS_OBSERVATION_ERROR.cdp_capability)
  const browsers: number[] = []
  for (const row of info) {
    if (row === null || typeof row !== 'object') continue
    if ((row as { type?: unknown }).type !== 'browser') continue
    browsers.push((row as { id?: unknown }).id as number)
  }
  if (browsers.length !== 1) fail(PROCESS_OBSERVATION_ERROR.cdp_capability)
  const pid = browsers[0]
  if (!isPositiveSafeInteger(pid)) {
    fail(PROCESS_OBSERVATION_ERROR.cdp_capability)
  }
  return pid
}

function assertSamePidNamespace(
  proc: ProcAccess,
  pid: number,
  fail: FailFn,
): void {
  let selfNs: string
  let childNs: string
  try {
    selfNs = proc.readlinkSelfPidNs()
    childNs = proc.readlinkPidNs(pid)
  } catch (err) {
    mapProcErr(err, fail)
  }
  if (selfNs !== childNs) fail(PROCESS_OBSERVATION_ERROR.unavailable_process)
}

function writeRecord(opts: {
  outDir: string
  observer: { pid: number; startTicks: string }
  index: ObservationIndex
  abandoned: boolean
  fail: FailFn
  io: ObservationIo
  body: ObservationBody
}): ProcessObservationReceipt {
  if (opts.abandoned) opts.fail(PROCESS_OBSERVATION_ERROR.timeout)
  const index = opts.index.next
  const name = `${opts.body.role}.${opts.body.phase}.${opts.observer.pid}.${opts.observer.startTicks}.${index}.json`
  const dest = path.join(opts.outDir, name)
  const record = {
    ...opts.body,
    filename: {
      role: opts.body.role,
      phase: opts.body.phase,
      observer_pid: opts.observer.pid,
      observer_start_ticks: opts.observer.startTicks,
      index,
      name,
    },
  }
  const bytes = Buffer.from(`${JSON.stringify(record, null, 2)}\n`, 'utf8')
  const io = opts.io
  let fd: number | undefined
  let created = false
  const cleanupIncomplete = (): void => {
    if (fd !== undefined) {
      try {
        io.close(fd)
      } catch {
        /* descriptor may already be unusable */
      }
      fd = undefined
    }
    if (created) {
      try {
        io.unlink(dest)
      } catch {
        /* OS refused cleanup; residue may remain; qualification is refused */
      }
    }
  }
  try {
    let opened: number | undefined
    try {
      opened = io.openExclusive(dest, 0o600)
    } catch (err) {
      if (err instanceof ProcessObservationError) throw err
      opts.fail(PROCESS_OBSERVATION_ERROR.invalid_output_custody)
    }
    if (opened === undefined) {
      opts.fail(PROCESS_OBSERVATION_ERROR.invalid_output_custody)
    }
    fd = opened
    created = true
    const owned: number = opened
    let off = 0
    while (off < bytes.length) {
      let n: number | undefined
      try {
        n = io.write(owned, bytes, off, bytes.length - off)
      } catch (err) {
        if (err instanceof ProcessObservationError) throw err
        opts.fail(PROCESS_OBSERVATION_ERROR.invalid_output_custody)
      }
      if (
        n === undefined ||
        !Number.isInteger(n) ||
        n <= 0 ||
        n > bytes.length - off
      ) {
        opts.fail(PROCESS_OBSERVATION_ERROR.invalid_output_custody)
      }
      off += n
    }
    try {
      io.fsync(owned)
    } catch (err) {
      if (err instanceof ProcessObservationError) throw err
      opts.fail(PROCESS_OBSERVATION_ERROR.invalid_output_custody)
    }
    try {
      io.fchmod(owned, 0o600)
    } catch (err) {
      if (err instanceof ProcessObservationError) throw err
      opts.fail(PROCESS_OBSERVATION_ERROR.invalid_output_custody)
    }
    try {
      io.close(owned)
      fd = undefined
    } catch (err) {
      if (err instanceof ProcessObservationError) throw err
      opts.fail(PROCESS_OBSERVATION_ERROR.invalid_output_custody)
    }
  } catch (err) {
    cleanupIncomplete()
    if (err instanceof ProcessObservationError) throw err
    opts.fail(PROCESS_OBSERVATION_ERROR.invalid_output_custody)
  }
  opts.index.next = index + 1
  return freezeReceipt({
    record,
    serialized: bytes.toString('utf8'),
    relativePath: `${PROCESS_OBSERVATION_DIRNAME}/${name}`,
    sha256: createHash('sha256').update(bytes).digest('hex'),
  })
}

function createOutputDir(work: string, fail: FailFn): string {
  const dir = path.join(work, PROCESS_OBSERVATION_DIRNAME)
  try {
    fs.lstatSync(dir)
    fail(PROCESS_OBSERVATION_ERROR.invalid_output_custody)
  } catch (err) {
    if (err instanceof ProcessObservationError) throw err
    if (errCode(err) !== 'ENOENT') {
      fail(PROCESS_OBSERVATION_ERROR.invalid_output_custody)
    }
  }
  try {
    fs.mkdirSync(dir, { mode: 0o700 })
    fs.chmodSync(dir, 0o700)
  } catch {
    fail(PROCESS_OBSERVATION_ERROR.invalid_output_custody)
  }
  return inspectOutputDir(work, dir, fail)
}

function attachOutputDir(work: string, fail: FailFn): string {
  const dir = path.join(work, PROCESS_OBSERVATION_DIRNAME)
  return inspectOutputDir(work, dir, fail)
}

function inspectOutputDir(work: string, dir: string, fail: FailFn): string {
  let st: fs.BigIntStats
  try {
    st = fs.lstatSync(dir, { bigint: true })
  } catch {
    fail(PROCESS_OBSERVATION_ERROR.invalid_output_custody)
  }
  if (st.isSymbolicLink() || !st.isDirectory()) {
    fail(PROCESS_OBSERVATION_ERROR.invalid_output_custody)
  }
  const uid = process.getuid?.()
  if (uid === undefined || st.uid !== BigInt(uid)) {
    fail(PROCESS_OBSERVATION_ERROR.invalid_output_custody)
  }
  if ((st.mode & 0o777n) !== 0o700n) {
    fail(PROCESS_OBSERVATION_ERROR.invalid_output_custody)
  }
  let dirReal: string
  try {
    dirReal = fs.realpathSync(dir)
  } catch {
    fail(PROCESS_OBSERVATION_ERROR.invalid_output_custody)
  }
  if (path.dirname(dirReal) !== work) {
    fail(PROCESS_OBSERVATION_ERROR.invalid_output_custody)
  }
  if (path.basename(dirReal) !== PROCESS_OBSERVATION_DIRNAME) {
    fail(PROCESS_OBSERVATION_ERROR.invalid_output_custody)
  }
  return dirReal
}

function workFromEnv(fail: FailFn): string {
  const work = process.env.K3_E2E_WORK
  if (!work || !path.isAbsolute(work)) {
    fail(PROCESS_OBSERVATION_ERROR.invalid_configuration)
  }
  try {
    const real = fs.realpathSync(work)
    const st = fs.statSync(real)
    if (!st.isDirectory()) fail(PROCESS_OBSERVATION_ERROR.invalid_configuration)
    return real
  } catch (err) {
    if (err instanceof ProcessObservationError) throw err
    fail(PROCESS_OBSERVATION_ERROR.invalid_configuration)
  }
}

function verifyTmpRoot(fail: FailFn): string {
  const tmpdirEnv = process.env.TMPDIR
  const k3tmp = process.env.K3_E2E_TMP
  if (!tmpdirEnv || !k3tmp) {
    fail(PROCESS_OBSERVATION_ERROR.invalid_configuration)
  }
  if (!path.isAbsolute(tmpdirEnv) || !path.isAbsolute(k3tmp)) {
    fail(PROCESS_OBSERVATION_ERROR.invalid_configuration)
  }
  let osTmp: string
  let a: string
  let b: string
  try {
    osTmp = fs.realpathSync(os.tmpdir())
    a = fs.realpathSync(tmpdirEnv)
    b = fs.realpathSync(k3tmp)
  } catch {
    fail(PROCESS_OBSERVATION_ERROR.invalid_configuration)
  }
  if (osTmp !== a || a !== b) {
    fail(PROCESS_OBSERVATION_ERROR.invalid_configuration)
  }
  let st: fs.BigIntStats
  try {
    st = fs.lstatSync(osTmp, { bigint: true })
  } catch {
    fail(PROCESS_OBSERVATION_ERROR.invalid_configuration)
  }
  if (st.isSymbolicLink() || !st.isDirectory()) {
    fail(PROCESS_OBSERVATION_ERROR.invalid_configuration)
  }
  const uid = process.getuid?.()
  if (uid === undefined || st.uid !== BigInt(uid)) {
    fail(PROCESS_OBSERVATION_ERROR.invalid_configuration)
  }
  if ((st.mode & 0o777n) !== 0o700n) {
    fail(PROCESS_OBSERVATION_ERROR.invalid_configuration)
  }
  return osTmp
}

function expectedChromiumPath(fail: FailFn, proc: ProcAccess): string {
  const raw = process.env.K3_E2E_EXPECTED_CHROMIUM_EXECUTABLE
  if (!raw || !path.isAbsolute(raw)) {
    fail(PROCESS_OBSERVATION_ERROR.invalid_configuration)
  }
  return expectedFile(raw, fail, proc).real
}

function expectedFile(
  raw: string,
  fail: FailFn,
  proc: ProcAccess,
): { real: string; id: FileId } {
  if (!raw || !path.isAbsolute(raw)) {
    fail(PROCESS_OBSERVATION_ERROR.invalid_configuration)
  }
  try {
    const real = fs.realpathSync(raw)
    const st = fs.statSync(real, { bigint: true })
    if (!st.isFile() || (st.mode & 0o111n) === 0n) {
      fail(PROCESS_OBSERVATION_ERROR.invalid_configuration)
    }
    return { real, id: proc.statPath(real) }
  } catch (err) {
    if (err instanceof ProcessObservationError) throw err
    fail(PROCESS_OBSERVATION_ERROR.invalid_configuration)
  }
}

function readObserverIdentity(
  proc: ProcAccess,
  fail: FailFn,
): { pid: number; startTicks: string } {
  const pid = process.pid
  if (!isPositiveSafeInteger(pid)) {
    fail(PROCESS_OBSERVATION_ERROR.invalid_configuration)
  }
  const parsed = readParsedStat(proc, pid, fail)
  return { pid, startTicks: parsed.startTicks }
}

function readParsedStat(
  proc: ProcAccess,
  pid: number,
  fail: FailFn,
): ParsedProcStat {
  try {
    return parseProcStat(proc.readStat(pid), pid)
  } catch (err) {
    if (err instanceof ProcStatError) {
      if (err.reason === 'pid_mismatch') {
        fail(PROCESS_OBSERVATION_ERROR.changed_generation)
      }
      fail(PROCESS_OBSERVATION_ERROR.unavailable_process)
    }
    mapProcErr(err, fail)
  }
}

function tryIdentity<T>(
  read: () => T,
  fail: FailFn,
): { kind: 'ok'; value: T } | { kind: 'absent' } | { kind: 'permission' } {
  try {
    return { kind: 'ok', value: read() }
  } catch (err) {
    const code = errCode(err)
    if (code === 'EACCES' || code === 'EPERM') return { kind: 'permission' }
    if (code === 'ENOENT' || code === 'ESRCH') return { kind: 'absent' }
    mapProcErr(err, fail)
  }
}

function mapProcErr(err: unknown, fail: FailFn): never {
  if (err instanceof ProcessObservationError) throw err
  const code = errCode(err)
  if (code === 'EACCES' || code === 'EPERM') {
    fail(PROCESS_OBSERVATION_ERROR.unavailable_process)
  }
  if (code === 'ENOENT' || code === 'ESRCH') {
    fail(PROCESS_OBSERVATION_ERROR.unavailable_process)
  }
  fail(PROCESS_OBSERVATION_ERROR.unavailable_process)
}

function assertLinuxProc(fail: FailFn): void {
  if (process.platform !== 'linux') {
    fail(PROCESS_OBSERVATION_ERROR.invalid_configuration)
  }
  try {
    fs.readFileSync('/proc/self/stat', 'utf8')
  } catch {
    fail(PROCESS_OBSERVATION_ERROR.invalid_configuration)
  }
}

function readMode(): ObservationMode {
  const raw = process.env.K3_E2E_PROCESS_OBSERVATION
  if (raw === undefined || raw === '') return 'disabled'
  if (raw === 'required') return 'required'
  return 'invalid'
}

function exactFlagValue(argv: string[], flag: string): string {
  const idxs: number[] = []
  for (let i = 0; i < argv.length; i++) {
    if (argv[i] === flag) idxs.push(i)
  }
  if (idxs.length !== 1) throw new ArgvError()
  const value = argv[idxs[0] + 1]
  if (value === undefined || value === '' || value.startsWith('-')) {
    throw new ArgvError()
  }
  return value
}

function isStrictlyBelow(root: string, target: string): boolean {
  const rel = path.relative(root, target)
  return rel !== '' && !rel.startsWith('..') && !path.isAbsolute(rel)
}

function sameFileId(a: FileId, b: FileId): boolean {
  return a.dev === b.dev && a.ino === b.ino
}

function fileIdFromBigintStats(st: fs.BigIntStats): FileId {
  return { dev: st.dev, ino: st.ino }
}

function procPath(pid: number, leaf: string): string {
  return `/proc/${pid}/${leaf}`
}

function isPositiveSafeInteger(value: unknown): value is number {
  return (
    typeof value === 'number' &&
    Number.isInteger(value) &&
    Number.isSafeInteger(value) &&
    value > 0
  )
}

function isDecimalToken(value: string): boolean {
  return /^[0-9]+$/.test(value)
}

function parseNonNegativeSafeInt(token: string): number {
  if (!isDecimalToken(token)) throw new ProcStatError('malformed')
  const n = Number(token)
  if (!Number.isInteger(n) || !Number.isSafeInteger(n) || n < 0) {
    throw new ProcStatError('malformed')
  }
  return n
}

function elapsedSince(clock: ObservationClock, started: number): number {
  return Math.max(0, Math.round(clock.now() - started))
}

function errCode(err: unknown): string | undefined {
  if (err !== null && typeof err === 'object' && 'code' in err) {
    const code = (err as { code?: unknown }).code
    if (typeof code === 'string') return code
  }
  return undefined
}

async function sleepBounded(
  clock: ObservationClock,
  deadlineAt: number,
  timeoutFail: () => never,
): Promise<void> {
  const remaining = deadlineAt - clock.now()
  if (remaining <= 0) timeoutFail()
  await new Promise<void>((resolve) => {
    setTimeout(resolve, Math.min(POLL_MS, remaining))
  })
}

async function raceDeadline<T>(
  work: Promise<T>,
  deadlineAt: number,
  clock: ObservationClock,
  onTimeout: (work: Promise<T>) => void,
): Promise<T> {
  const remaining = deadlineAt - clock.now()
  if (remaining <= 0) {
    onTimeout(work)
    throw TIMEOUT_TOKEN
  }
  let timer: ReturnType<typeof setTimeout> | undefined
  const sleeper = new Promise<typeof TIMEOUT_TOKEN>((resolve) => {
    timer = setTimeout(() => resolve(TIMEOUT_TOKEN), remaining)
  })
  try {
    const won = await Promise.race([work, sleeper])
    if (won === TIMEOUT_TOKEN) {
      onTimeout(work)
      throw TIMEOUT_TOKEN
    }
    return won as T
  } finally {
    if (timer !== undefined) clearTimeout(timer)
  }
}

async function bestEffortDetach(
  session: ProcessObservationCdpSession | undefined,
  deadlineAt: number,
  clock: ObservationClock,
): Promise<void> {
  if (!session) return
  const pending = session.detach()
  pending.then(
    () => undefined,
    () => undefined,
  )
  const remaining = deadlineAt - clock.now()
  if (remaining <= 0) return
  let timer: ReturnType<typeof setTimeout> | undefined
  try {
    await Promise.race([
      pending.then(
        () => undefined,
        () => undefined,
      ),
      new Promise<void>((resolve) => {
        timer = setTimeout(resolve, remaining)
      }),
    ])
  } finally {
    if (timer !== undefined) clearTimeout(timer)
  }
}
