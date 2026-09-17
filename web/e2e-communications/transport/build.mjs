// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Bundles entry.ts against the product sources for the browser transport witness.
//
//   node build.mjs            → playwright-report/k3-transport/dist/probe.js, as committed
//   node build.mjs --mutant   → …/dist-mutant/probe.js with the transport's dispatch
//                               guard call REMOVED from lib/api/client.ts at bundle
//                               time (the working tree is not touched). This is the
//                               causal control: the witness must go RED on it.
import { fileURLToPath } from 'node:url'
import path from 'node:path'
import { build } from 'vite'

const here = path.dirname(fileURLToPath(import.meta.url))
const web = path.resolve(here, '..', '..')
// Generated output lives under the ignored Playwright report directory.
const out = path.join(web, 'playwright-report', 'k3-transport')
const mutant = process.argv.includes('--mutant')
// --mutant-hook: ALSO (or only) remove the guard hook's live refusal in begin(), so
// begin() mints a controller as the frozen d8c5ad3e9e version did. With --mutant too
// this is the double mutant: nothing left decides, and the witness must go RED.
const mutantHook = process.argv.includes('--mutant-hook')
const arg = (flag, fallback) => {
  const i = process.argv.indexOf(flag)
  return i > 0 ? process.argv[i + 1] : fallback
}
// --entry <file>: another witness entry (e.g. the independent review's verbatim file,
// read from its own directory); --name <dir>: the output directory under
// playwright-report/k3-transport (default: dist / dist-mutant).
const entry = path.resolve(arg('--entry', path.join(here, 'entry.ts')))
const name = arg(
  '--name',
  mutant && mutantHook
    ? 'dist-mutant-both'
    : mutant
      ? 'dist-mutant'
      : mutantHook
        ? 'dist-mutant-hook'
        : 'dist',
)
const GUARD_CALL = 'opts.dispatchGuard?.()'
const HOOK_CHECK = 'if (moved() !== null) return null'

/** Strips the one guard call from the client at bundle time, and refuses to build a
 * "mutant" that would be identical to the real thing. */
function stripDispatchGuard() {
  let stripped = 0
  return {
    name: 'k3-strip-dispatch-guard',
    transform(code, id) {
      if (!id.endsWith('/src/lib/api/client.ts')) return null
      const count = code.split(GUARD_CALL).length - 1
      if (count !== 1)
        throw new Error(
          `expected exactly one guard call in client.ts, found ${count}`,
        )
      stripped++
      return {
        code: code.replace(GUARD_CALL, '/* MUTANT: guard removed */'),
        map: null,
      }
    },
    buildEnd() {
      if (stripped !== 1)
        throw new Error('mutant build did not strip the guard')
    },
  }
}

function stripHookRefusal() {
  let stripped = 0
  return {
    name: 'k3-strip-hook-refusal',
    transform(code, id) {
      if (!id.endsWith('/src/features/communications/intent.ts')) return null
      const count = code.split(HOOK_CHECK).length - 1
      if (count !== 1)
        throw new Error(
          `expected exactly one live refusal in begin(), found ${count}`,
        )
      stripped++
      return {
        code: code.replace(
          HOOK_CHECK,
          '/* MUTANT: begin() live refusal removed */',
        ),
        map: null,
      }
    },
    buildEnd() {
      if (stripped !== 1)
        throw new Error('hook mutant build did not strip the refusal')
    },
  }
}

await build({
  configFile: false,
  logLevel: 'warn',
  define: { 'process.env.NODE_ENV': JSON.stringify('production') },
  root: here,
  resolve: { alias: { '@': path.join(web, 'src') } },
  cacheDir: path.join(out, `.vite-${name}`),
  plugins: [
    ...(mutant ? [stripDispatchGuard()] : []),
    ...(mutantHook ? [stripHookRefusal()] : []),
  ],
  build: {
    outDir: path.join(out, name),
    emptyOutDir: true,
    lib: {
      entry,
      formats: ['es'],
      fileName: () => 'probe.js',
    },
    minify: false,
  },
})
console.log(
  `transport witness bundled → ${name} from ${entry}` +
    (mutant ? ' — MUTANT: transport guard removed' : '') +
    (mutantHook ? ' — MUTANT: begin() live refusal removed' : ''),
)
