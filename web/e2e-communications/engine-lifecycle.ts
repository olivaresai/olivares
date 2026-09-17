// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
// Required-mode fixture custody. This is not a product process supervisor.
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { createHash, randomUUID } from 'node:crypto'
import { spawn, type ChildProcess } from 'node:child_process'
import { isDeepStrictEqual } from 'node:util'
import { fileURLToPath } from 'node:url'
import {
  parseEngineArgv,
  parseProcStat,
  splitCmdline,
  isLoopbackEndpoint,
  PROCESS_OBSERVATION_SCHEMA,
  type ProcessObservationRecord,
  type ProcessObservationReceipt,
} from './process-observation.ts'

export const LIFECYCLE_DIR = 'engine-lifecycle'
export const RECORD_LIMIT = 64 * 1024
export const HELPER_DEADLINE_MS = 30_000
export const LEASE_SCHEMA = 'olivares.k3.engine-lease.v1'
export const POINTER_SCHEMA = 'olivares.k3.engine-current.v1'
export const STOP_SCHEMA = 'olivares.k3.engine-stop.v1'
export type StopPurpose = 'activation' | 'restart' | 'teardown'
const STOP_PURPOSES: readonly StopPurpose[] = [
  'activation',
  'restart',
  'teardown',
]
export type LifecycleCode =
  | 'invalid_lease'
  | 'invalid_custody'
  | 'lock_conflict'
  | 'capability_refusal'
  | 'unavailable_process'
  | 'terminal_unobserved'
  | 'changed_generation'
  | 'ownership_mismatch'
  | 'signal_failure'
  | 'deadline_exceeded'
  | 'helper_interruption'
  | 'result_publication_failure'
const CODES: LifecycleCode[] = [
  'invalid_lease',
  'invalid_custody',
  'lock_conflict',
  'capability_refusal',
  'unavailable_process',
  'terminal_unobserved',
  'changed_generation',
  'ownership_mismatch',
  'signal_failure',
  'deadline_exceeded',
  'helper_interruption',
  'result_publication_failure',
]
export type LifecycleOperation = 'publish' | 'stop'
// Readonly classified context for the two public lifecycle boundaries. It carries
// only the operation, a verified canonical owned work directory and a validated
// stop purpose. An unvalidated work argument or purpose is reported as absent and
// is never echoed, and no raw exception text, argv or environment is included.
export type LifecycleErrorContext = {
  readonly operation: LifecycleOperation
  readonly work_dir: string | null
  readonly purpose: StopPurpose | null
}
const UNAVAILABLE_WORK_DIR = 'unavailable'
const NO_VALIDATED_PURPOSE = 'not_applicable'
function lifecycleMessage(
  code: LifecycleCode,
  context: LifecycleErrorContext | undefined,
): string {
  const classified = `k3 engine lifecycle: ${code}`
  if (!context) return classified
  return (
    `${classified}; operation=${context.operation}` +
    `; work_dir=${context.work_dir ?? UNAVAILABLE_WORK_DIR}` +
    `; purpose=${context.purpose ?? NO_VALIDATED_PURPOSE}`
  )
}
export class EngineLifecycleError extends Error {
  readonly code: LifecycleCode
  readonly context: LifecycleErrorContext | undefined
  constructor(code: LifecycleCode, context?: LifecycleErrorContext) {
    super(lifecycleMessage(code, context))
    this.name = 'EngineLifecycleError'
    this.code = code
    this.context =
      context &&
      freeze({
        operation: context.operation,
        work_dir: context.work_dir,
        purpose: context.purpose,
      })
  }
}
// Internal validation keeps its code-only error; only the public boundary attaches
// the validated context.
function fail(code: LifecycleCode): never {
  throw new EngineLifecycleError(code)
}
function refuse(code: LifecycleCode, context: LifecycleErrorContext): never {
  throw new EngineLifecycleError(code, context)
}
// Classification never carries an original exception's raw message forward.
function classify(
  e: unknown,
  context: LifecycleErrorContext,
): EngineLifecycleError {
  return new EngineLifecycleError(
    e instanceof EngineLifecycleError ? e.code : 'invalid_custody',
    context,
  )
}
export type FileIdentity = {
  canonical_path: string
  device: string
  inode: string
}
export type ArtifactIdentity = FileIdentity & { sha256: string }
export type RecordRef = { name: string; sha256: string }
export type Predecessor = { lease: RecordRef; stop: RecordRef }
export type EngineLease = {
  schema: typeof LEASE_SCHEMA
  lease_id: string
  work_dir: string
  data_dir: string
  data_directory: FileIdentity
  phase: 'seed' | 'activated' | 'restarted'
  pid: number
  start_ticks: string
  boot_id: string
  pid_namespace: string
  executable: FileIdentity
  cwd: string
  listen: string
  grpc_listen: string
  observation: RecordRef
  published_at: string
  predecessor: Predecessor | null
}
export type CurrentPointer =
  | { schema: typeof POINTER_SCHEMA; state: 'live'; lease: RecordRef }
  | {
      schema: typeof POINTER_SCHEMA
      state: 'stopped'
      lease: RecordRef
      stop: RecordRef
    }
export type HelperIdentity = {
  pid: number
  start_ticks: string
  boot_id: string
  pid_namespace: string
  executable: ArtifactIdentity
  script: ArtifactIdentity
}
export type StopReceipt = {
  schema: typeof STOP_SCHEMA
  lease: RecordRef
  purpose: StopPurpose
  helper: HelperIdentity
  validation: 'matched'
  signals: Array<{
    signal: 'SIGTERM' | 'SIGKILL'
    outcome: 'sent' | 'esrch'
    elapsed_ms: number
  }>
  terminal: { observation: 'pidfd'; events: number; elapsed_ms: number }
  elapsed_ms: number
}
export type LiveEngine = Pick<
  EngineLease,
  | 'pid'
  | 'start_ticks'
  | 'boot_id'
  | 'pid_namespace'
  | 'executable'
  | 'cwd'
  | 'data_dir'
  | 'data_directory'
  | 'listen'
  | 'grpc_listen'
>
export type ToolIdentities = {
  interpreter: ArtifactIdentity
  script: ArtifactIdentity
}
export type HelperRequest = {
  work: string
  lease: RecordRef
  resultName: string
  purpose: StopPurpose
  tools: ToolIdentities
}
export type HelperCompletion = {
  pid: number
  exitCode: number | null
  signal: string | null
  stdout: string
}
export type HelperRun = {
  completion: Promise<HelperCompletion>
  abandon(): void
}
export type LifecycleIo = {
  openDirectory(path: string): number
  statDirectory(fd: number): fs.BigIntStats
  write(fd: number, bytes: Buffer, offset: number, length: number): number
  sync(fd: number): void
  close(fd: number): void
  rename(from: string, to: string): void
}
export type LifecycleDependencies = {
  inspect?: (pid: number) => LiveEngine
  startHelper?: (request: HelperRequest) => HelperRun
  tools?: () => ToolIdentities
  io?: LifecycleIo
  helperDeadlineMs?: number
}
export function createLifecycleIo(): LifecycleIo {
  return {
    openDirectory: (target) =>
      fs.openSync(
        target,
        fs.constants.O_RDONLY |
          fs.constants.O_DIRECTORY |
          fs.constants.O_NOFOLLOW,
      ),
    statDirectory: (fd) => fs.fstatSync(fd, { bigint: true }),
    write: (fd, b, o, n) => fs.writeSync(fd, b, o, n),
    sync: fs.fsyncSync,
    close: fs.closeSync,
    rename: fs.renameSync,
  }
}
export function engineLifecycleRequired(): boolean {
  const mode = process.env.K3_E2E_PROCESS_OBSERVATION
  if (!mode) return false
  if (mode !== 'required') fail('invalid_lease')
  return true
}
function requiredMode(context: LifecycleErrorContext): boolean {
  try {
    return engineLifecycleRequired()
  } catch (e) {
    throw classify(e, context)
  }
}
export function sha256(bytes: string | Buffer): string {
  return createHash('sha256').update(bytes).digest('hex')
}
function freeze<T>(value: T): T {
  if (value && typeof value === 'object') {
    for (const child of Object.values(value)) freeze(child)
    Object.freeze(value)
  }
  return value
}
function object(v: unknown, keys: string[]): Record<string, unknown> {
  if (
    !v ||
    typeof v !== 'object' ||
    Array.isArray(v) ||
    Object.keys(v).sort().join('|') !== keys.sort().join('|')
  )
    fail('invalid_lease')
  return v as Record<string, unknown>
}
function text(v: unknown, pattern?: RegExp): asserts v is string {
  if (typeof v !== 'string' || (pattern && !pattern.test(v)))
    fail('invalid_lease')
}
function decimal(v: unknown): void {
  text(v, /^(0|[1-9][0-9]*)$/)
}
function positive(v: unknown): void {
  if (typeof v !== 'number' || !Number.isSafeInteger(v) || v <= 0)
    fail('invalid_lease')
}
function elapsed(v: unknown): void {
  if (typeof v !== 'number' || !Number.isFinite(v) || v < 0)
    fail('invalid_lease')
}
function absolute(v: unknown): asserts v is string {
  text(v)
  if (
    !path.posix.isAbsolute(v) ||
    path.posix.normalize(v) !== v ||
    v.includes('\0')
  )
    fail('invalid_lease')
}
const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/
function file(v: unknown, artifact = false): void {
  const o = object(
    v,
    artifact
      ? ['canonical_path', 'device', 'inode', 'sha256']
      : ['canonical_path', 'device', 'inode'],
  )
  absolute(o.canonical_path)
  decimal(o.device)
  decimal(o.inode)
  if (artifact) text(o.sha256, /^[0-9a-f]{64}$/)
}
export function validateRecordRef(
  v: unknown,
  kind: 'lease' | 'stop' | 'observation',
): RecordRef {
  const o = object(v, ['name', 'sha256'])
  text(o.name)
  text(o.sha256, /^[0-9a-f]{64}$/)
  const pattern =
    kind === 'observation'
      ? /^process-observations\/engine\.(seed|activated|restarted)\.[1-9][0-9]*\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.json$/
      : new RegExp(
          `^${kind}\\.[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\\.json$`,
        )
  if (!pattern.test(o.name)) fail('invalid_lease')
  return v as RecordRef
}
function generation(o: Record<string, unknown>): void {
  positive(o.pid)
  decimal(o.start_ticks)
  text(o.boot_id, UUID)
  text(o.pid_namespace, /^pid:\[[1-9][0-9]*\]$/)
}
export function validateEngineLease(v: unknown): EngineLease {
  const o = object(v, [
    'schema',
    'lease_id',
    'work_dir',
    'data_dir',
    'data_directory',
    'phase',
    'pid',
    'start_ticks',
    'boot_id',
    'pid_namespace',
    'executable',
    'cwd',
    'listen',
    'grpc_listen',
    'observation',
    'published_at',
    'predecessor',
  ])
  if (
    o.schema !== LEASE_SCHEMA ||
    !['seed', 'activated', 'restarted'].includes(String(o.phase))
  )
    fail('invalid_lease')
  text(o.lease_id, UUID)
  absolute(o.work_dir)
  absolute(o.data_dir)
  absolute(o.cwd)
  if (!o.data_dir.startsWith(`${o.work_dir}/`)) fail('invalid_lease')
  generation(o)
  file(o.executable)
  file(o.data_directory)
  if ((o.data_directory as FileIdentity).canonical_path !== o.data_dir)
    fail('invalid_lease')
  text(o.listen)
  text(o.grpc_listen)
  if (!isLoopbackEndpoint(o.listen) || !isLoopbackEndpoint(o.grpc_listen))
    fail('invalid_lease')
  validateRecordRef(o.observation, 'observation')
  text(o.published_at, /^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{3}Z$/)
  if (o.predecessor !== null) {
    const p = object(o.predecessor, ['lease', 'stop'])
    validateRecordRef(p.lease, 'lease')
    validateRecordRef(p.stop, 'stop')
  }
  return v as EngineLease
}
export function validateCurrentPointer(v: unknown): CurrentPointer {
  const state = (v as { state?: unknown } | null)?.state
  const o = object(
    v,
    state === 'stopped'
      ? ['schema', 'state', 'lease', 'stop']
      : ['schema', 'state', 'lease'],
  )
  if (o.schema !== POINTER_SCHEMA || (state !== 'live' && state !== 'stopped'))
    fail('invalid_lease')
  validateRecordRef(o.lease, 'lease')
  if (state === 'stopped') validateRecordRef(o.stop, 'stop')
  return v as CurrentPointer
}
export function validateStopReceipt(v: unknown): StopReceipt {
  const o = object(v, [
    'schema',
    'lease',
    'purpose',
    'helper',
    'validation',
    'signals',
    'terminal',
    'elapsed_ms',
  ])
  if (
    o.schema !== STOP_SCHEMA ||
    o.validation !== 'matched' ||
    !['activation', 'restart', 'teardown'].includes(String(o.purpose))
  )
    fail('invalid_lease')
  validateRecordRef(o.lease, 'lease')
  const h = object(o.helper, [
    'pid',
    'start_ticks',
    'boot_id',
    'pid_namespace',
    'executable',
    'script',
  ])
  generation(h)
  file(h.executable, true)
  file(h.script, true)
  if (!Array.isArray(o.signals) || o.signals.length < 1 || o.signals.length > 2)
    fail('invalid_lease')
  let previous = 0
  for (const [i, value] of o.signals.entries()) {
    const s = object(value, ['signal', 'outcome', 'elapsed_ms'])
    if (
      s.signal !== (i === 0 ? 'SIGTERM' : 'SIGKILL') ||
      (s.outcome !== 'sent' && s.outcome !== 'esrch')
    )
      fail('invalid_lease')
    elapsed(s.elapsed_ms)
    if ((s.elapsed_ms as number) < previous) fail('invalid_lease')
    previous = s.elapsed_ms as number
  }
  const t = object(o.terminal, ['observation', 'events', 'elapsed_ms'])
  if (
    t.observation !== 'pidfd' ||
    typeof t.events !== 'number' ||
    !Number.isInteger(t.events) ||
    ![1, 16, 17].includes(t.events)
  )
    fail('invalid_lease')
  elapsed(t.elapsed_ms)
  elapsed(o.elapsed_ms)
  if (
    (t.elapsed_ms as number) < previous ||
    (o.elapsed_ms as number) < (t.elapsed_ms as number)
  )
    fail('invalid_lease')
  return v as StopReceipt
}
function same(a: unknown, b: unknown): boolean {
  return isDeepStrictEqual(a, b)
}
function id(target: string): FileIdentity {
  const real = fs.realpathSync(target)
  const st = fs.statSync(real, { bigint: true })
  return { canonical_path: real, device: String(st.dev), inode: String(st.ino) }
}
function inspectEngine(pid: number): LiveEngine {
  try {
    const stat = () =>
      parseProcStat(fs.readFileSync(`/proc/${pid}/stat`, 'utf8'), pid)
    const first = stat()
    const executable = id(`/proc/${pid}/exe`)
    const cwd = fs.realpathSync(`/proc/${pid}/cwd`)
    const args = parseEngineArgv(
      splitCmdline(fs.readFileSync(`/proc/${pid}/cmdline`)),
    )
    const data_dir = fs.realpathSync(path.resolve(cwd, args.dataDir))
    const data_directory = id(data_dir)
    if (!fs.statSync(data_dir).isDirectory()) fail('ownership_mismatch')
    const boot_id = fs
      .readFileSync('/proc/sys/kernel/random/boot_id', 'utf8')
      .trim()
    const pid_namespace = fs.readlinkSync(`/proc/${pid}/ns/pid`)
    if (pid_namespace !== fs.readlinkSync('/proc/self/ns/pid'))
      fail('ownership_mismatch')
    if (first.startTicks !== stat().startTicks) fail('changed_generation')
    return {
      pid,
      start_ticks: first.startTicks,
      executable,
      cwd,
      data_dir,
      data_directory,
      boot_id,
      pid_namespace,
      listen: args.listen,
      grpc_listen: args.grpcListen,
    }
  } catch (e) {
    if (e instanceof EngineLifecycleError) throw e
    fail('unavailable_process')
  }
}
function directory(target: string): fs.BigIntStats {
  const st = fs.lstatSync(target, { bigint: true })
  if (
    !st.isDirectory() ||
    st.isSymbolicLink() ||
    st.uid !== BigInt(process.getuid!()) ||
    (st.mode & 0o777n) !== 0o700n ||
    fs.realpathSync(target) !== target
  )
    fail('invalid_custody')
  return st
}
class Estate {
  readonly work: string
  readonly fd: number
  readonly root: string
  readonly io: LifecycleIo
  private lockId: fs.BigIntStats | undefined
  constructor(work: string, io: LifecycleIo) {
    this.work = work
    this.io = io
    // Local custody of the directory descriptor between a successful open and the
    // transfer to this.fd. The single catch below owns the one cleanup attempt.
    let owned: number | undefined
    try {
      if (process.platform !== 'linux' || !process.getuid)
        fail('capability_refusal')
      absolute(work)
      const tmp = process.env.K3_E2E_TMP
      if (!tmp || tmp !== process.env.TMPDIR || tmp !== os.tmpdir())
        fail('invalid_custody')
      directory(tmp)
      directory(work)
      if (!work.startsWith(`${tmp}/`)) fail('invalid_custody')
      this.root = path.join(work, LIFECYCLE_DIR)
      try {
        fs.mkdirSync(this.root, { mode: 0o700 })
      } catch (e) {
        if ((e as NodeJS.ErrnoException).code !== 'EEXIST') throw e
      }
      const st = directory(this.root)
      const fd = io.openDirectory(this.root)
      // A returned number that cannot be a descriptor is refused, and nothing is
      // owned, so no close is attempted against it. A thrown open owns nothing.
      if (!Number.isSafeInteger(fd) || fd < 0) fail('invalid_custody')
      owned = fd
      const opened = io.statDirectory(fd)
      if (st.dev !== opened.dev || st.ino !== opened.ino)
        fail('invalid_custody')
      // Custody transfers to the estate; the constructor never closes after this.
      this.fd = fd
      owned = undefined
    } catch (e) {
      if (owned !== undefined) {
        // This catch runs at most once per construction and holds the only cleanup
        // call: one close, never a retry against a number the kernel may already
        // have released and reused.
        try {
          io.close(owned)
        } catch {
          // The first causal failure is preserved. This correction has no estate,
          // lock or record that could carry a durable cleanup-failure entry, so a
          // failed close is discarded here and never claimed as successful cleanup.
        }
      }
      if (e instanceof EngineLifecycleError) throw e
      fail('invalid_custody')
    }
  }
  leaf(name: string): string {
    if (!/^[a-zA-Z0-9.-]+$/.test(name) || name === '.' || name === '..')
      fail('invalid_custody')
    return `/proc/self/fd/${this.fd}/${name}`
  }
  read(name: string): Buffer {
    const fd = fs.openSync(
      this.leaf(name),
      fs.constants.O_RDONLY | fs.constants.O_NOFOLLOW,
    )
    try {
      const st = fs.fstatSync(fd, { bigint: true })
      if (
        !st.isFile() ||
        st.uid !== BigInt(process.getuid!()) ||
        st.nlink !== 1n ||
        (st.mode & 0o777n) !== 0o600n ||
        st.size > BigInt(RECORD_LIMIT)
      )
        fail('invalid_custody')
      const bytes = Buffer.alloc(RECORD_LIMIT + 1)
      let used = 0
      while (used < bytes.length) {
        const n = fs.readSync(fd, bytes, used, bytes.length - used, null)
        if (!n) break
        used += n
      }
      if (used > RECORD_LIMIT) fail('invalid_custody')
      return bytes.subarray(0, used)
    } finally {
      fs.closeSync(fd)
    }
  }
  record(ref: RecordRef): unknown {
    const bytes = this.read(ref.name)
    if (sha256(bytes) !== ref.sha256) fail('invalid_lease')
    return JSON.parse(bytes.toString('utf8'))
  }
  write(name: string, value: unknown): RecordRef {
    const bytes = Buffer.from(`${JSON.stringify(value, null, 2)}\n`)
    if (bytes.length > RECORD_LIMIT) fail('invalid_custody')
    const fd = fs.openSync(
      this.leaf(name),
      fs.constants.O_WRONLY |
        fs.constants.O_CREAT |
        fs.constants.O_EXCL |
        fs.constants.O_NOFOLLOW,
      0o600,
    )
    let closed = false
    try {
      fs.fchmodSync(fd, 0o600)
      const st = fs.fstatSync(fd, { bigint: true })
      if (
        !st.isFile() ||
        (st.mode & 0o777n) !== 0o600n ||
        st.uid !== BigInt(process.getuid!()) ||
        st.nlink !== 1n
      )
        fail('invalid_custody')
      let offset = 0
      while (offset < bytes.length) {
        const n = this.io.write(fd, bytes, offset, bytes.length - offset)
        if (!Number.isInteger(n) || n <= 0 || n > bytes.length - offset)
          fail('invalid_custody')
        offset += n
      }
      this.io.sync(fd)
      this.io.close(fd)
      closed = true
      this.io.sync(this.fd)
      return { name, sha256: sha256(bytes) }
    } finally {
      if (!closed) {
        try {
          fs.closeSync(fd)
        } catch {
          /* retained failed publication */
        }
      }
    }
  }
  lock(): void {
    try {
      this.write('operation.lock', { operation_id: randomUUID() })
      this.lockId = fs.lstatSync(this.leaf('operation.lock'), { bigint: true })
    } catch (e) {
      if ((e as NodeJS.ErrnoException).code === 'EEXIST') fail('lock_conflict')
      throw e
    }
  }
  release(): void {
    if (!this.lockId) return
    const st = fs.lstatSync(this.leaf('operation.lock'), { bigint: true })
    if (st.dev !== this.lockId.dev || st.ino !== this.lockId.ino)
      fail('invalid_custody')
    fs.unlinkSync(this.leaf('operation.lock'))
    this.io.sync(this.fd)
    this.lockId = undefined
  }
  pointer(): CurrentPointer | undefined {
    try {
      return validateCurrentPointer(
        JSON.parse(this.read('current.json').toString('utf8')),
      )
    } catch (e) {
      if ((e as NodeJS.ErrnoException).code !== 'ENOENT') throw e
      // This is an empty-estate check, never receipt discovery or selection.
      if (
        fs
          .readdirSync(`/proc/self/fd/${this.fd}`)
          .some((name) => name !== 'operation.lock')
      )
        fail('invalid_lease')
      return undefined
    }
  }
  current(value: CurrentPointer): void {
    const temp = `current.${randomUUID()}.tmp`
    try {
      validateCurrentPointer(value)
      this.write(temp, value)
      this.io.rename(this.leaf(temp), this.leaf('current.json'))
      this.io.sync(this.fd)
    } catch {
      fail('result_publication_failure')
    }
  }
  close(): void {
    // Normal teardown is the only owner of this close, through the same seam that
    // opened the descriptor, so custody transfer and release stay observable.
    this.io.close(this.fd)
  }
}
function accepted(
  estate: Estate,
  pointer: CurrentPointer,
): { lease: EngineLease; receipt: StopReceipt } {
  if (pointer.state !== 'stopped') fail('invalid_lease')
  const lease = validateEngineLease(estate.record(pointer.lease))
  const receipt = validateStopReceipt(estate.record(pointer.stop))
  if (
    lease.work_dir !== estate.work ||
    pointer.lease.name !== `lease.${lease.lease_id}.json` ||
    !same(receipt.lease, pointer.lease)
  )
    fail('invalid_lease')
  return { lease, receipt }
}
const RETAINED_STOP_CODES: readonly LifecycleCode[] = [
  'helper_interruption',
  'deadline_exceeded',
  'result_publication_failure',
]
function openEstate(
  work: string,
  deps: LifecycleDependencies,
  kind: LifecycleOperation,
  purpose: StopPurpose | null,
): Estate {
  try {
    return new Estate(work, deps.io ?? createLifecycleIo())
  } catch (e) {
    // Path custody is not established here: this argument is never reported as owned.
    throw classify(e, { operation: kind, work_dir: null, purpose })
  }
}
async function operation<T>(
  work: string,
  deps: LifecycleDependencies,
  kind: LifecycleOperation,
  purpose: StopPurpose | null,
  act: (e: Estate) => Promise<T>,
): Promise<T> {
  const estate = openEstate(work, deps, kind, purpose)
  // The constructor verified this exact canonical current-UID 0700 owned directory.
  const context: LifecycleErrorContext = {
    operation: kind,
    work_dir: estate.work,
    purpose,
  }
  let locked = false
  let retain = false
  let failure: EngineLifecycleError | undefined
  // Finalization records what it threw instead of throwing from a finally block.
  // The box keeps a thrown undefined distinct from a finalization that succeeded.
  let finalization: { error: unknown } | undefined
  let result: T
  try {
    try {
      estate.lock()
      locked = true
      result = await act(estate)
    } catch (e) {
      failure = classify(e, context)
      retain = kind === 'publish' || RETAINED_STOP_CODES.includes(failure.code)
      if (locked && retain) {
        try {
          estate.write(`failure.${randomUUID()}.json`, {
            schema: 'olivares.k3.lifecycle-failure.v1',
            code: failure.code,
            operation: kind,
            work_dir: context.work_dir,
            purpose,
            observed_at: new Date().toISOString(),
            helper_state: 'unknown',
            accepted: false,
            reconciliation: 'root_required',
          })
        } catch {
          // Preserve the original failure and lock even if evidence cannot be written.
        }
      }
      throw failure
    } finally {
      // Exactly one finalization attempt on every exit: release this operation's
      // lock unless it is retained, then close the estate even if release threw.
      // When both throw, the close failure is the one reported.
      if (locked && !retain) {
        try {
          estate.release()
        } catch (e) {
          finalization = { error: e }
        }
      }
      try {
        estate.close()
      } catch (e) {
        finalization = { error: e }
      }
    }
  } catch (e) {
    // A finalization failure never replaces an established original failure. The
    // failure is unestablished here only when classifying the original error threw.
    if (finalization && !failure) throw classify(finalization.error, context)
    throw e
  }
  // A finalization failure never turns an unfinished operation into a successful
  // result.
  if (finalization) throw classify(finalization.error, context)
  return result
}
function observationBytes(
  work: string,
  receipt: ProcessObservationReceipt,
): void {
  const ref = validateRecordRef(
    { name: receipt.relativePath, sha256: receipt.sha256 },
    'observation',
  )
  if (
    Buffer.byteLength(receipt.serialized) > RECORD_LIMIT ||
    sha256(receipt.serialized) !== ref.sha256 ||
    !same(JSON.parse(receipt.serialized), receipt.record)
  )
    fail('invalid_lease')
  const r = receipt.record
  if (r.schema !== PROCESS_OBSERVATION_SCHEMA || r.role !== 'engine')
    fail('invalid_lease')
  const f = r.filename
  const name = `engine.${r.phase}.${r.observer.pid}.${r.observer.start_ticks}.${f.index}.json`
  if (
    f.name !== name ||
    ref.name !== `process-observations/${name}` ||
    f.role !== r.role ||
    f.phase !== r.phase ||
    f.observer_pid !== r.observer.pid ||
    f.observer_start_ticks !== r.observer.start_ticks
  )
    fail('invalid_lease')
  directory(path.join(work, 'process-observations'))
  const fd = fs.openSync(
    path.join(work, ref.name),
    fs.constants.O_RDONLY | fs.constants.O_NOFOLLOW,
  )
  try {
    const st = fs.fstatSync(fd, { bigint: true })
    if (
      !st.isFile() ||
      st.uid !== BigInt(process.getuid!()) ||
      st.nlink !== 1n ||
      st.size > BigInt(RECORD_LIMIT) ||
      (st.mode & 0o777n) !== 0o600n
    )
      fail('invalid_custody')
    const bytes = Buffer.alloc(RECORD_LIMIT + 1)
    let used = 0
    while (used < bytes.length) {
      const n = fs.readSync(fd, bytes, used, bytes.length - used, null)
      if (!n) break
      used += n
    }
    if (used > RECORD_LIMIT) fail('invalid_custody')
    if (sha256(bytes.subarray(0, used)) !== ref.sha256) fail('invalid_lease')
  } finally {
    fs.closeSync(fd)
  }
}
function validateLeaseObservation(work: string, lease: EngineLease): void {
  validateRecordRef(lease.observation, 'observation')
  directory(path.join(work, 'process-observations'))
  const fd = fs.openSync(
    path.join(work, lease.observation.name),
    fs.constants.O_RDONLY | fs.constants.O_NOFOLLOW,
  )
  let serialized: string
  try {
    const st = fs.fstatSync(fd, { bigint: true })
    if (
      !st.isFile() ||
      st.size > BigInt(RECORD_LIMIT) ||
      st.nlink !== 1n ||
      st.uid !== BigInt(process.getuid!()) ||
      (st.mode & 0o777n) !== 0o600n
    )
      fail('invalid_custody')
    const bytes = Buffer.alloc(RECORD_LIMIT + 1)
    let used = 0
    while (used < bytes.length) {
      const n = fs.readSync(fd, bytes, used, bytes.length - used, null)
      if (!n) break
      used += n
    }
    if (used > RECORD_LIMIT) fail('invalid_custody')
    serialized = bytes.subarray(0, used).toString('utf8')
  } finally {
    fs.closeSync(fd)
  }
  const record = JSON.parse(serialized) as ProcessObservationRecord
  observationBytes(work, {
    record,
    serialized,
    relativePath: lease.observation.name,
    sha256: lease.observation.sha256,
  })
  if (
    record.role !== 'engine' ||
    record.phase !== lease.phase ||
    record.pid !== lease.pid ||
    record.start_ticks !== lease.start_ticks ||
    !same(record.executable, lease.executable) ||
    record.cwd !== lease.cwd ||
    record.data_dir !== lease.data_dir ||
    record.listen !== lease.listen ||
    record.grpc_listen !== lease.grpc_listen
  )
    fail('invalid_lease')
}
export async function publishObservedEngine(
  work: string,
  receipt: ProcessObservationReceipt | undefined,
  deps: LifecycleDependencies = {},
): Promise<Readonly<EngineLease> | undefined> {
  if (!requiredMode({ operation: 'publish', work_dir: null, purpose: null }))
    return undefined
  return operation(work, deps, 'publish', null, async (estate) => {
    if (!receipt || receipt.record.role !== 'engine') fail('invalid_lease')
    observationBytes(work, receipt)
    const previous = estate.pointer()
    if (previous) accepted(estate, previous)
    const r = receipt.record
    const live = (deps.inspect ?? inspectEngine)(r.pid)
    if (live.pid !== r.pid || live.start_ticks !== r.start_ticks)
      fail('changed_generation')
    if (
      !same(live.executable, r.executable) ||
      live.cwd !== r.cwd ||
      live.data_dir !== r.data_dir ||
      live.listen !== r.listen ||
      live.grpc_listen !== r.grpc_listen
    )
      fail('ownership_mismatch')
    const lease = validateEngineLease({
      schema: LEASE_SCHEMA,
      lease_id: randomUUID(),
      work_dir: work,
      ...live,
      phase: r.phase,
      observation: { name: receipt.relativePath, sha256: receipt.sha256 },
      published_at: new Date().toISOString(),
      predecessor:
        previous?.state === 'stopped'
          ? { lease: previous.lease, stop: previous.stop }
          : null,
    })
    if (
      previous &&
      same(previous.lease, lease.predecessor?.lease) &&
      accepted(estate, previous).lease.start_ticks === lease.start_ticks &&
      accepted(estate, previous).lease.pid === lease.pid &&
      accepted(estate, previous).lease.boot_id === lease.boot_id
    )
      fail('changed_generation')
    const ref = estate.write(`lease.${lease.lease_id}.json`, lease)
    estate.current({ schema: POINTER_SCHEMA, state: 'live', lease: ref })
    return freeze(lease)
  })
}
function toolIdentities(): ToolIdentities {
  const artifact = (p: string): ArtifactIdentity => ({
    ...id(p),
    sha256: sha256(fs.readFileSync(p)),
  })
  return {
    interpreter: artifact('/usr/bin/python3'),
    script: artifact(
      fileURLToPath(new URL('./engine-lifecycle-helper.py', import.meta.url)),
    ),
  }
}
function startHelper(request: HelperRequest): HelperRun {
  const child = spawn(
    request.tools.interpreter.canonical_path,
    [
      request.tools.script.canonical_path,
      request.work,
      request.lease.name,
      request.lease.sha256,
      request.resultName,
      request.purpose,
    ],
    {
      shell: false,
      stdio: ['ignore', 'pipe', 'ignore'],
      env: { LANG: 'C.UTF-8' },
      cwd: request.work,
    },
  )
  let stdout = ''
  let abandoned = false
  const abandon = () => {
    abandoned = true
    child.unref()
    child.stdout?.destroy()
  }
  const completion = new Promise<HelperCompletion>((resolve, reject) => {
    child.on('error', () =>
      reject(new EngineLifecycleError('helper_interruption')),
    )
    child.stdout.on('error', () => {
      if (!abandoned) reject(new EngineLifecycleError('helper_interruption'))
    })
    child.stdout.on('data', (chunk: Buffer) => {
      if (!abandoned) {
        stdout += chunk.toString('utf8')
        if (Buffer.byteLength(stdout) > RECORD_LIMIT) {
          abandon()
          reject(new EngineLifecycleError('helper_interruption'))
        }
      }
    })
    child.on('close', (exitCode, signal) => {
      if (!abandoned) resolve({ pid: child.pid ?? 0, exitCode, signal, stdout })
    })
  })
  return { completion, abandon }
}
async function helperWithDeadline(
  run: HelperRun,
  milliseconds: number,
): Promise<HelperCompletion> {
  let timer: ReturnType<typeof setTimeout> | undefined
  try {
    return await Promise.race([
      run.completion,
      new Promise<never>((_, reject) => {
        timer = setTimeout(() => {
          reject(new EngineLifecycleError('helper_interruption'))
        }, milliseconds)
      }),
    ])
  } catch {
    // Closing our pipe/reference is not signaling or proof the helper ended.
    try {
      run.abandon()
    } catch {
      // The one abandonment attempt cannot change this outcome: the stop is already a
      // helper_interruption that retains its lock, lease and failure record with
      // helper_state unknown. The thrown value is never retried or surfaced.
    }
    throw new EngineLifecycleError('helper_interruption')
  } finally {
    if (timer) clearTimeout(timer)
  }
}
export async function stopObservedEngine(
  work: string,
  purpose: StopPurpose,
  deps: LifecycleDependencies = {},
): Promise<Readonly<StopReceipt> | undefined> {
  // Only a validated purpose may appear in a public error or retained evidence.
  const validated: StopPurpose | null = STOP_PURPOSES.includes(purpose)
    ? purpose
    : null
  if (!requiredMode({ operation: 'stop', work_dir: null, purpose: validated }))
    return undefined
  if (!validated)
    refuse('invalid_lease', {
      operation: 'stop',
      work_dir: null,
      purpose: null,
    })
  return operation(work, deps, 'stop', validated, async (estate) => {
    const pointer = estate.pointer()
    if (!pointer) fail('invalid_lease')
    if (pointer.state === 'stopped')
      return freeze(accepted(estate, pointer).receipt)
    const lease = validateEngineLease(estate.record(pointer.lease))
    if (
      lease.work_dir !== work ||
      pointer.lease.name !== `lease.${lease.lease_id}.json`
    )
      fail('invalid_lease')
    validateLeaseObservation(work, lease)
    const tools = (deps.tools ?? toolIdentities)()
    file(tools.interpreter, true)
    file(tools.script, true)
    const resultName = `stop.${randomUUID()}.json`
    const request = { work, lease: pointer.lease, resultName, purpose, tools }
    const run = (deps.startHelper ?? startHelper)(request)
    const complete = await helperWithDeadline(
      run,
      deps.helperDeadlineMs ?? HELPER_DEADLINE_MS,
    )
    if (Buffer.byteLength(complete.stdout) > RECORD_LIMIT)
      fail('helper_interruption')
    let outcome: Record<string, unknown>
    try {
      outcome = JSON.parse(complete.stdout) as Record<string, unknown>
      if (
        !outcome ||
        typeof outcome !== 'object' ||
        Array.isArray(outcome) ||
        Object.keys(outcome).sort().join('|') !==
          (outcome.ok === false ? 'code|ok' : 'ok')
      )
        fail('helper_interruption')
    } catch {
      fail('helper_interruption')
    }
    if (
      complete.signal ||
      !Number.isSafeInteger(complete.pid) ||
      complete.pid <= 0
    )
      fail('helper_interruption')
    if (complete.exitCode !== 0 || outcome.ok !== true) {
      if (
        outcome.ok === false &&
        typeof outcome.code === 'string' &&
        CODES.includes(outcome.code as LifecycleCode)
      )
        fail(outcome.code as LifecycleCode)
      fail('helper_interruption')
    }
    try {
      const bytes = estate.read(resultName)
      const receipt = validateStopReceipt(JSON.parse(bytes.toString('utf8')))
      if (
        !same(receipt.lease, pointer.lease) ||
        receipt.purpose !== purpose ||
        receipt.helper.pid !== complete.pid ||
        receipt.helper.boot_id !== lease.boot_id ||
        receipt.helper.pid_namespace !== lease.pid_namespace ||
        !same(receipt.helper.executable, tools.interpreter) ||
        !same(receipt.helper.script, tools.script)
      )
        fail('invalid_lease')
      estate.current({
        schema: POINTER_SCHEMA,
        state: 'stopped',
        lease: pointer.lease,
        stop: { name: resultName, sha256: sha256(bytes) },
      })
      return freeze(receipt)
    } catch {
      fail('result_publication_failure')
    }
  })
}

// An observed-spawn failure is an unresolved fixture, never cleanup authority.
export function recordUnobservedSpawn(work: string, child: ChildProcess): void {
  if (!engineLifecycleRequired()) return
  const estate = new Estate(work, createLifecycleIo())
  try {
    estate.lock()
    let start_ticks: string | null = null
    try {
      if (child.pid)
        start_ticks = parseProcStat(
          fs.readFileSync(`/proc/${child.pid}/stat`, 'utf8'),
          child.pid,
        ).startTicks
    } catch {
      /* unavailable is recorded honestly */
    }
    estate.write(`spawn-failure.${randomUUID()}.json`, {
      schema: 'olivares.k3.unobserved-spawn.v1',
      pid: child.pid ?? null,
      start_ticks,
      generation_verified: false,
      exit_code: child.exitCode,
      signal: child.signalCode,
      observed_at: new Date().toISOString(),
      lease_adopted: false,
    })
    // Retain the lock: no observed generation may be adopted for cleanup.
  } finally {
    estate.close()
  }
}
