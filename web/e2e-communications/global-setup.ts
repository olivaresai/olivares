// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Boots ONE disposable engine for the journey and performs the K3 ACTIVATION
// CEREMONY the product requires before any communication route answers — exactly
// what the Go HTTP fixtures do (cmd/olivares/communicationmessaging_http_test.go,
// bootActivatedCommunicationHTTPTestEngine):
//
//   1. owned custody for the content sealer and the cursor keyring (two JSON
//      keyrings with random 32-byte roots, written into the run's own directory);
//   2. a first boot with `--seed-demo` under OLIVARES_COMMUNICATION_ACTIVATION=on,
//      which creates the store and the synthetic estate;
//   3. `olivares db activate-directory-writer` on the STOPPED store, with the two
//      operator assertions the database cannot inspect;
//   4. a second boot on the same data directory (never `--seed-demo` again), where
//      readiness observes the enforced writer control.
//
// The binary is the one built from this tree; its identity — source SHA, tree,
// binary digest, bundle stamp — is recorded so the evidence names exactly what
// was measured. A missing fixture FAILS the run; nothing is skipped.
import { execFileSync, spawn, type ChildProcess } from 'node:child_process'
import { createHash, randomBytes } from 'node:crypto'
import {
  existsSync,
  mkdirSync,
  chmodSync,
  openSync,
  readFileSync,
  writeFileSync,
} from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import {
  prepareProcessObservationCustody,
  withAwaitedProcessObservation,
} from './process-observation.ts'

import {
  engineLifecycleRequired,
  publishObservedEngine,
  stopObservedEngine,
  recordUnobservedSpawn,
} from './engine-lifecycle.ts'

const HERE = path.dirname(fileURLToPath(import.meta.url))
const ROOT = path.resolve(HERE, '..', '..')
const BIN = process.env.K3_E2E_BIN ?? path.join(ROOT, 'bin', 'olivares')
const PORT = Number(process.env.K3_E2E_PORT ?? 8490)
const GRPC = PORT + 1
const DEMO_EMAIL = 'demo@olivares.local'
const DEMO_PASSWORD = 'olivares-demo-estate'

function fail(msg: string): never {
  throw new Error(`k3 journey setup: ${msg}`)
}

async function healthy(base: string): Promise<boolean> {
  try {
    const r = await fetch(`${base}/healthz`)
    return r.ok
  } catch {
    return false
  }
}

/** The engine's environment for every boot of this run: activation on, the two
 * custody files, an executable TMPDIR, and no key wrap (custody is file-based). */
export function engineEnv(work: string): NodeJS.ProcessEnv {
  return {
    ...process.env,
    TMPDIR: work,
    OLIVARES_COMMUNICATION_ACTIVATION: 'on',
    OLIVARES_COMMUNICATION_CONTENT_KEYRING_FILE: path.join(
      work,
      'custody',
      'content-keyring.json',
    ),
    OLIVARES_COMMUNICATION_CURSOR_KEYRING_FILE: path.join(
      work,
      'custody',
      'cursor-keyring.json',
    ),
    OLIVARES_KEY_WRAP: '',
  }
}

function writeCustody(work: string) {
  const dir = path.join(work, 'custody')
  mkdirSync(dir, { recursive: true, mode: 0o700 })
  const root = () => randomBytes(32).toString('base64')
  writeFileSync(
    path.join(dir, 'content-keyring.json'),
    JSON.stringify({
      format: 'olivares.communication-content-keyring.v1',
      current_seal_version: 'seal-v1',
      current_digest_version: 'digest-v1',
      keys: [
        { version: 'seal-v1', root_key_base64: root() },
        { version: 'digest-v1', root_key_base64: root() },
      ],
    }),
    { mode: 0o600 },
  )
  writeFileSync(
    path.join(dir, 'cursor-keyring.json'),
    JSON.stringify({
      format: 'olivares.communication-cursor-keyring.v1',
      current_kid: 'cursor-k1',
      keys: [{ kid: 'cursor-k1', key_base64: root() }],
    }),
    { mode: 0o600 },
  )
}

async function boot(
  work: string,
  data: string,
  seed: boolean,
): Promise<ChildProcess> {
  const out = openSync(path.join(work, 'engine.log'), 'a')
  const args = ['serve', '--insecure']
  if (seed) args.push('--seed-demo')
  args.push(
    '--listen',
    `127.0.0.1:${PORT}`,
    '--grpc-listen',
    `127.0.0.1:${GRPC}`,
    '--data-dir',
    data,
  )
  const child = spawn(BIN, args, {
    detached: true,
    stdio: ['ignore', out, out],
    env: engineEnv(work),
  })
  child.unref()
  if (
    child.pid === undefined ||
    !Number.isInteger(child.pid) ||
    !Number.isSafeInteger(child.pid) ||
    child.pid <= 0
  ) {
    fail('the engine did not start')
  }
  writeFileSync(path.join(work, 'engine.pid'), String(child.pid))
  let observationPublished = false
  try {
    return await withAwaitedProcessObservation(
      {
        role: 'engine',
        phase: seed ? 'seed' : 'activated',
        pid: child.pid,
        expectedExecutable: BIN,
        expectedDataDir: data,
        expectedListen: `127.0.0.1:${PORT}`,
        expectedGrpcListen: `127.0.0.1:${GRPC}`,
      },
      async (receipt) => {
        observationPublished = receipt !== undefined
        await publishObservedEngine(work, receipt)
        return child
      },
    )
  } catch (error) {
    try {
      if (!observationPublished) recordUnobservedSpawn(work, child)
    } catch {
      /* original required failure remains authoritative */
    }
    throw error
  }
}

async function waitHealthy(base: string, log: string) {
  // A first boot on this box mints four signing keys and classifies the store
  // controls; under load that has taken more than a minute. Five minutes is the
  // ceiling before "never became healthy" is a verdict rather than impatience.
  for (let i = 0; i < 600; i++) {
    if (await healthy(base)) return
    await new Promise((r) => setTimeout(r, 500))
  }
  fail(`engine never became healthy; log at ${log}`)
}

async function stop(pid: number) {
  process.kill(pid, 'SIGTERM')
  for (let i = 0; i < 200; i++) {
    if (!existsSync(`/proc/${pid}`)) return
    await new Promise((r) => setTimeout(r, 100))
  }
  fail(`engine pid ${pid} did not stop`)
}

export default async function globalSetup() {
  if (!existsSync(BIN))
    fail(`binary ${BIN} is missing — build it from this tree first`)
  const tmpBase =
    process.env.K3_E2E_TMP ??
    process.env.TMPDIR ??
    path.join(ROOT, '.olivares-tmptest')
  const stamp = new Date().toISOString().replace(/[:.]/g, '-')
  const work = path.join(tmpBase, `k3-journey-${stamp}`)
  const data = path.join(work, 'data')
  if (engineLifecycleRequired()) {
    mkdirSync(work, { mode: 0o700 })
    chmodSync(work, 0o700)
  }
  mkdirSync(data, { recursive: true })
  const base = `http://127.0.0.1:${PORT}`
  const log = path.join(work, 'engine.log')
  // Exported BEFORE the first boot so the teardown can stop an engine whose setup
  // failed half-way (measured: a slow first boot left one running).
  process.env.K3_E2E_WORK = work
  if (await healthy(base))
    fail(
      `127.0.0.1:${PORT} already serves an engine; refusing to boot on top of it`,
    )
  writeCustody(work)
  await prepareProcessObservationCustody()

  // 1 · first boot: create the store and the synthetic estate.
  const first = await boot(work, data, true)
  await waitHealthy(base, log)

  // Resolve the demo tenant through the real login; the token stays in this process.
  let token = ''
  for (let i = 0; i < 60 && !token; i++) {
    const r = await fetch(`${base}/v1/auth/login`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ email: DEMO_EMAIL, password: DEMO_PASSWORD }),
    })
    if (r.ok) token = ((await r.json()) as { token: string }).token
    else await new Promise((res) => setTimeout(res, 500))
  }
  if (!token) fail('the demo superadmin could not log in (seed not ready?)')
  const orgs = (await (
    await fetch(`${base}/v1/system/orgs`, {
      headers: { Authorization: `Bearer ${token}` },
    })
  ).json()) as { items: { tenant_id: string; slug: string }[] }
  const demo = orgs.items.find((o) => o.slug === 'demo')
  if (!demo) fail('the demo tenant is not seeded')

  // 2 · the activation ceremony on the STOPPED store.
  if (engineLifecycleRequired()) await stopObservedEngine(work, 'activation')
  else await stop(first.pid as number)
  const ceremony = execFileSync(
    BIN,
    [
      'db',
      'activate-directory-writer',
      '--data-dir',
      data,
      '--expected-generation',
      '1',
      '--writers-upgraded',
      '--writers-drained',
      '--actor',
      'k3-journey-harness',
      '--reason',
      'disposable browser-journey estate, serve stopped',
    ],
    { env: engineEnv(work), encoding: 'utf8' },
  )
  writeFileSync(path.join(work, 'activation.txt'), ceremony)

  // 3 · second boot, same store, activation observed by readiness.
  await boot(work, data, false)
  await waitHealthy(base, log)
  const probe = await fetch(
    `${base}/v1/m/sessions/inbox?workspace_id=00000000-0000-7000-8000-000000000000`,
    {
      headers: {
        Authorization: `Bearer ${token}`,
        'X-Olivares-Tenant': demo.tenant_id,
      },
    },
  )
  // A canonical-but-absent workspace is a 400/404 from the module, never a 503: that
  // is the witness that K3 readiness is EFFECTIVE after the ceremony.
  if (probe.status === 503)
    fail(
      `K3 readiness is not effective after the ceremony (inbox probe 503); see ${log}`,
    )

  const sha = execFileSync('git', ['-C', ROOT, 'rev-parse', 'HEAD'], {
    encoding: 'utf8',
  }).trim()
  const tree = execFileSync('git', ['-C', ROOT, 'rev-parse', 'HEAD^{tree}'], {
    encoding: 'utf8',
  }).trim()
  const dirty = execFileSync('git', ['-C', ROOT, 'status', '--porcelain=v1'], {
    encoding: 'utf8',
  }).trim()
  const binDigest = createHash('sha256').update(readFileSync(BIN)).digest('hex')
  const bundleStamp = readFileSync(
    path.join(ROOT, 'core', 'internal', 'webui', 'bundle-source.stamp'),
    'utf8',
  ).trim()
  const version = execFileSync(BIN, ['--version'], {
    encoding: 'utf8',
    env: engineEnv(work),
  }).trim()
  const identity = {
    source_sha: sha,
    source_tree: tree,
    working_tree_dirty: dirty !== '',
    binary: BIN,
    binary_sha256: binDigest,
    binary_version: version,
    bundle_source_stamp: bundleStamp,
    engine_port: PORT,
    data_dir: data,
    work_dir: work,
    demo_tenant: demo.tenant_id,
    activation_probe_status: probe.status,
    booted_at: new Date().toISOString(),
  }
  writeFileSync(
    path.join(work, 'identity.json'),
    JSON.stringify(identity, null, 2),
  )

  process.env.K3_E2E_WORK = work
  process.env.K3_E2E_BASE = base
  process.env.DEMO_TENANT = demo.tenant_id
  process.env.PLAYWRIGHT_BASE_URL = base
}
