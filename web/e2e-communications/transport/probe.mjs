// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Serves the bundled witness to a real Chromium over loopback and COUNTS THE POSTS THE
// RECEIVER READS. Usage:
//
//   node probe.mjs [--mutant] [--name <dir>] [--out result.json] [--expect green|red]
//
// Exit 0 when every case holds its expected property, 1 otherwise. On a --mutant
// bundle the expected outcome is the failure: the run is reported as the causal
// control and exits 0 only if the mutant is RED (the queued rotation case sends).
//
// Expected, per case (networkSends counts POSTs whose body was fully read):
//   refresh control, rotate=false: replay ? 2 : 1 sends, resolved
//   refresh control, rotate=true : replay ? 1 : 0 sends, never the rotated credential
//   queued, rotate=false         : 1 send, resolved, original credential
//   queued, rotate=true          : 0 sends, never the rotated credential
import { createServer } from 'node:http'
import { readFileSync, writeFileSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { chromium } from '@playwright/test'

const here = path.dirname(fileURLToPath(import.meta.url))
const out = path.resolve(here, '..', '..', 'playwright-report', 'k3-transport')
const mutant = process.argv.includes('--mutant')
const arg = (flag, fallback) => {
  const i = process.argv.indexOf(flag)
  return i > 0 ? process.argv[i + 1] : fallback
}
// --expect green|red: what the run must show to exit 0. A mutant is expected RED
// (the queued rotation case sends with the new credential) unless told otherwise —
// a single-layer mutant caught by the other layer is expected green.
const expectRed = arg('--expect', mutant ? 'red' : 'green') === 'red'
// --name <dir>: the bundle to serve (build.mjs --name); --out <file>: the result.
const name = arg('--name', mutant ? 'dist-mutant' : 'dist')
const outFile = arg('--out', path.join(out, `result-${name}.json`))
const dist = path.join(out, name, 'probe.js')
const CRED_B = 'synthetic-review-credential-B'

let current = { replay: false, rotate: false }
let sends = []
const server = createServer(async (req, res) => {
  const url = new URL(req.url, 'http://local')
  if (url.pathname === '/') {
    res.setHeader('Content-Type', 'text/html')
    res.end(
      '<!doctype html><html><body><script type="module" src="/probe.js"></script></body></html>',
    )
    return
  }
  if (url.pathname === '/probe.js') {
    res.setHeader('Content-Type', 'application/javascript')
    res.end(readFileSync(dist))
    return
  }
  res.setHeader('Content-Type', 'application/json')
  if (url.pathname === '/fixture/case') {
    let data = ''
    for await (const chunk of req) data += chunk
    current = JSON.parse(data)
    sends = []
    res.end('{}')
    return
  }
  if (url.pathname === '/fixture/refresh') {
    res.end(
      JSON.stringify({
        token: current.rotate ? CRED_B : 'synthetic-review-credential-A',
        sessionId: 'same-local-fixture-session',
        expiresAt: new Date(Date.now() + 3_600_000).toISOString(),
      }),
    )
    return
  }
  if (url.pathname === '/fixture/result') {
    res.end(
      JSON.stringify({
        networkSends: sends.length,
        usedRotatedCredential: sends.some((x) => x.rotated),
        sends,
      }),
    )
    return
  }
  if (url.pathname === '/v1/m/sessions/messages/send') {
    let body = ''
    for await (const chunk of req) body += chunk
    // Counted only after the body was read: this is a POST that LEFT the browser.
    sends.push({
      rotated: req.headers.authorization === `Bearer ${CRED_B}`,
      tenantCaptured: req.headers['x-olivares-tenant'] === 'tenant',
      method: req.method,
    })
    const unauthorized = current.replay && sends.length === 1
    res.statusCode = unauthorized ? 401 : 201
    res.end(
      JSON.stringify(
        unauthorized
          ? { error: { code: 'unauthenticated', message: 'expired' } }
          : { replayed: false },
      ),
    )
    return
  }
  res.statusCode = 404
  res.end('{}')
})
await new Promise((r) => server.listen(0, '127.0.0.1', r))

const records = []
let browser
try {
  browser = await chromium.launch({
    executablePath: process.env.K3_E2E_CHROMIUM || undefined,
    headless: true,
  })
  const page = await browser.newPage()
  page.on('pageerror', (e) => console.log('PROBE_PAGE_ERROR', e.message))
  await page.goto(`http://127.0.0.1:${server.address().port}`)
  await page.waitForFunction(() => typeof window.runCase === 'function')
  for (const replay of [false, true])
    for (const rotate of [false, true])
      records.push(
        await page.evaluate(([r, c]) => window.runCase(r, c), [replay, rotate]),
      )
  for (const viaBegin of [true, false])
    for (const rotate of [false, true])
      records.push(
        await page.evaluate(
          ([c, v]) => window.runQueuedCase(c, v),
          [rotate, viaBegin],
        ),
      )
  const holds = (x) =>
    x.rotate
      ? x.networkSends === (x.replay ? 1 : 0) && !x.usedRotatedCredential
      : x.networkSends === (x.replay ? 2 : 1) && x.outcome === 'resolved'
  const failures = records.filter((x) => !holds(x))
  const result = {
    browser: browser.version(),
    bundle_dir: name,
    bundle: mutant
      ? 'MUTANT: dispatch guard removed at bundle time'
      : 'as committed',
    test: 'native browser fetch + unchanged production client/hook/session store; synthetic local HTTP fixture, no product authorization claim',
    cases: records,
    failing_cases: failures.length,
  }
  writeFileSync(outFile, JSON.stringify(result, null, 2) + '\n')
  console.log(JSON.stringify(result, null, 2))
  const queuedRotated = records.filter(
    (x) => x.case === 'queued_mutation_boundary_remount' && x.rotate,
  )
  const red = queuedRotated.some(
    (x) => x.networkSends > 0 && x.usedRotatedCredential,
  )
  console.log(
    red
      ? 'RED: a queued rotation case sent with the NEW credential'
      : `GREEN: no queued rotation case sent (${failures.length} failing case(s) overall)`,
  )
  if (expectRed) {
    // The causal control: this bundle must make the queued rotation case send.
    process.exitCode = red ? 0 : 1
  } else {
    process.exitCode = failures.length ? 1 : 0
  }
} finally {
  if (browser) await browser.close()
  await new Promise((r) => server.close(r))
}
