// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it } from 'vitest'
import { existsSync, readFileSync, readdirSync, statSync } from 'node:fs'
import { join, relative, resolve } from 'node:path'

/**
 * Every console request goes through the shared client (lib/api/client.ts): it pins the
 * tenant, renews the session, replays a recoverable 401, signs out on a dead one and maps
 * the error envelope. A download that calls fetch() itself skips all of that, so the
 * CSV/NDJSON/PDF/archive downloads use apiFetchRaw. The files below are the only callers of
 * fetch() and each says why it cannot use the client.
 */
const ALLOWED: Record<string, string> = {
  'lib/api/client.ts': 'the shared client itself',
  'lib/auth/browser-session.ts':
    'cookie sign-in recovery runs before the client has a session',
  'lib/i18n/index.ts': 'static translation files, not the API',
  'features/shared/sse.ts': 'an event stream read incrementally, not a body',
  'features/agentops/launch-agent-api.ts':
    'sends an exchanged agent token the client refuses to override',
  'features/api-playground/request-panel.tsx':
    'the playground sends the operator request verbatim',
  'features/api-playground/api-playground-view.tsx':
    'loads the OpenAPI document the playground shows',
}

const FETCH_CALL = /(^|[^\w.$])fetch\(/

function sourceFiles(dir: string, acc: string[] = []): string[] {
  for (const e of readdirSync(dir)) {
    if (e === 'node_modules' || e === 'dist') continue
    const p = join(dir, e)
    if (statSync(p).isDirectory()) sourceFiles(p, acc)
    else if (/\.tsx?$/.test(p) && !/\.test\.tsx?$/.test(p)) acc.push(p)
  }
  return acc
}

function callsFetch(src: string): boolean {
  return src
    .split('\n')
    .some((l) => !/^\s*(\/\/|\*)/.test(l) && FETCH_CALL.test(l))
}

describe('downloads go through the shared client', () => {
  it('no fetch() outside the allowed transports', () => {
    const root = [
      resolve(process.cwd(), 'src'),
      resolve(process.cwd(), 'web/src'),
    ].find((c) => existsSync(c))
    expect(root).toBeTruthy()
    const files = sourceFiles(root as string)
    const found = files
      .filter((f) => callsFetch(readFileSync(f, 'utf8')))
      .map((f) =>
        relative(root as string, f)
          .split('\\')
          .join('/'),
      )
    // The sweep walked the tree and still sees the transports it allows.
    expect(files.length).toBeGreaterThan(400)
    expect(found).toContain('lib/api/client.ts')
    expect(found.filter((f) => !(f in ALLOWED))).toEqual([])
  })

  it('one save helper: no object-URL download outside lib/api/download.ts', () => {
    const root = [
      resolve(process.cwd(), 'src'),
      resolve(process.cwd(), 'web/src'),
    ].find((c) => existsSync(c))
    const found = sourceFiles(root as string)
      .filter((f) => readFileSync(f, 'utf8').includes('URL.createObjectURL('))
      .map((f) =>
        relative(root as string, f)
          .split('\\')
          .join('/'),
      )
    expect(found).toEqual(['lib/api/download.ts'])
  })

  it('control: the probe sees a call and ignores refetch and comments', () => {
    expect(callsFetch('  res = await fetch(url, {')).toBe(true)
    expect(callsFetch('void fetch(url)')).toBe(true)
    expect(callsFetch('retry={() => void query.refetch()}')).toBe(false)
    expect(callsFetch('  // a raw fetch() used to live here')).toBe(false)
  })
})
