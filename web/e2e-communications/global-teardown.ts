// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Stops the engine this run booted — BY PID, after checking through /proc that the
// pid still belongs to a process serving OUR data directory — and keeps the work
// directory (log, identity, evidence) for the report.
import { existsSync, readFileSync } from 'node:fs'
import path from 'node:path'
import {
  engineLifecycleRequired,
  stopObservedEngine,
  EngineLifecycleError,
} from './engine-lifecycle.ts'

export default async function globalTeardown() {
  const work = process.env.K3_E2E_WORK
  if (engineLifecycleRequired()) {
    if (!work) throw new EngineLifecycleError('invalid_custody')
    await stopObservedEngine(work, 'teardown')
    return
  }
  if (!work) return
  const pidFile = path.join(work, 'engine.pid')
  if (!existsSync(pidFile)) return
  const pid = Number(readFileSync(pidFile, 'utf8').trim())
  if (!Number.isInteger(pid) || pid <= 1) return
  const cmdline = path.join('/proc', String(pid), 'cmdline')
  if (!existsSync(cmdline)) return
  const args = readFileSync(cmdline, 'utf8')
  if (!args.includes(path.join(work, 'data'))) {
    console.error(
      `k3 journey teardown: pid ${pid} no longer serves ${work}; not signalling it`,
    )
    return
  }
  try {
    process.kill(pid, 'SIGTERM')
  } catch {
    return
  }
  for (let i = 0; i < 50; i++) {
    if (!existsSync(cmdline)) return
    await new Promise((r) => setTimeout(r, 200))
  }
  try {
    process.kill(pid, 'SIGKILL')
  } catch {
    /* gone */
  }
}
