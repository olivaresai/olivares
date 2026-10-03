// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// EU18 and EU20 as a CLASS. On 08b /console?tab=roles read two routes of the dormant `models`
// module and showed "Something went wrong / Retry" with New buttons, and a real group forbid
// read the same on the Agents tab. Two guards keep the class closed:
//   1. every console read of /v1/m/<module>/ for a module that can be off is behind that
//      module's gate: the page's own (RequirePermission, by view), or the panel's
//      (ModuleGate / useModuleOn / useModuleEnabled with that module's permission);
//   2. a panel renders a failed read through QueryErrorState, never the bare ErrorState, so a
//      403 or module_not_enabled can never read as a failure with Retry.
import { readFileSync, readdirSync, statSync } from 'node:fs'
import { join, relative, resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

const SRC = resolve(__dirname, '..')

function files(dir: string, out: string[] = []): string[] {
  for (const n of readdirSync(dir)) {
    const p = join(dir, n)
    if (statSync(p).isDirectory()) files(p, out)
    else if (/\.tsx?$/.test(n) && !n.includes('.test.') && !n.endsWith('.d.ts'))
      out.push(p)
  }
  return out
}

/** Source without comments: prose about a route is not a call to it. */
function code(text: string): string {
  return text
    .replace(/\/\*[\s\S]*?\*\//g, '')
    .replace(/^\s*\/\/.*$/gm, '')
    .replace(/\s\/\/\s.*$/gm, '')
}

const SOURCES = new Map(
  files(SRC).map((p) => [relative(SRC, p), code(readFileSync(p, 'utf8'))]),
)

/** The modules an installation can run without: the engine catalog as Settings names it
 * (cmd/olivares/moduleprofile.go), less the kernel, which always runs. */
const settings = JSON.parse(
  readFileSync(join(SRC, 'lib/i18n/locales/en/settings.json'), 'utf8'),
) as { modules: { names: Record<string, string> } }
const KERNEL = new Set(['governance', 'sessions'])
const DORMANT = new Set(
  Object.keys(settings.modules.names).filter((m) => !KERNEL.has(m)),
)

/** Not call sites: generated route types, and ARCH's K3 readiness probe. */
const NOT_CALLS = new Set(['lib/api/openapi.gen.ts', 'stores/modules.ts'])

const ROUTE = /\/v1\/m\/([a-z0-9-]+)/g

/** `xApi.method` → the modules its route reads, from every exported API object. */
function apiMethods(): Map<string, Set<string>> {
  const out = new Map<string, Set<string>>()
  for (const [file, src] of SOURCES) {
    if (NOT_CALLS.has(file)) continue
    const prefixes = new Map<string, string>()
    for (const m of src.matchAll(
      /const (\w+)\s*=\s*[`'"]\/v1\/m\/([a-z0-9-]+)/g,
    ))
      prefixes.set(m[1]!, m[2]!)
    for (const obj of src.matchAll(/export const (\w+)\s*(?::[^=]+)?=\s*\{/g)) {
      let depth = 0
      let end = obj.index! + obj[0].length - 1
      for (; end < src.length; end++) {
        if (src[end] === '{') depth++
        else if (src[end] === '}' && --depth === 0) break
      }
      const body = src.slice(obj.index! + obj[0].length - 1, end)
      const props = [...body.matchAll(/\n {2}(?:async )?(\w+)\s*[:(]/g)]
      props.forEach((p, i) => {
        const seg = body.slice(p.index, props[i + 1]?.index ?? body.length)
        const mods = new Set([...seg.matchAll(ROUTE)].map((r) => r[1]!))
        for (const [name, mod] of prefixes)
          if (new RegExp(`\\b${name}\\b`).test(seg)) mods.add(mod)
        if (mods.size) out.set(`${obj[1]}.${p[1]}`, mods)
      })
    }
  }
  return out
}

/** The module a registry view's page gate checks (stores/modules.ts moduleOfView). */
function pageModules(): Map<string, Set<string>> {
  const registry = SOURCES.get('features/registry.tsx')!
  const viewModule = new Map(
    [
      ...SOURCES.get('stores/modules.ts')!.matchAll(
        /^\s+([\w]+): '([a-z0-9-]+)',$/gm,
      ),
    ].map((m) => [m[1]!, m[2]!]),
  )
  const lazy = new Map(
    [
      ...registry.matchAll(
        /const (\w+) = lazy\(\s*\(\)\s*=>\s*import\('\.\/([\w/-]+)'\)/g,
      ),
    ].map((m) => [m[1]!, m[2]!.split('/')[0]!]),
  )
  // One view per `id:`, read up to the next one: a view without a lazy element (Now is
  // rendered directly) must not lend its id to the next view's element.
  const ids = [...registry.matchAll(/\n\s*id: '([\w-]+)',/g)]
  const byDir = new Map<string, Set<string>>()
  ids.forEach((v, i) => {
    const body = registry.slice(v.index, ids[i + 1]?.index ?? registry.length)
    const element = /element: lazyView\((\w+)\)/.exec(body)?.[1]
    const permission = /permission: '([^']+)'/.exec(body)?.[1]
    const dir = element ? lazy.get(element) : undefined
    if (!dir || !permission) return
    const mod = viewModule.get(v[1]!) ?? permission.split(':')[0]!
    byDir.set(dir, (byDir.get(dir) ?? new Set()).add(mod))
  })
  return byDir
}

/** Each file that calls a route of a module that can be off, and that module. */
export function ungatedModuleReads(): string[] {
  const methods = apiMethods()
  const pages = pageModules()
  const out: string[] = []
  for (const [file, src] of SOURCES) {
    if (NOT_CALLS.has(file)) continue
    const isApiFile = /export const \w+(?:Api|API)\s*(?::[^=]+)?=\s*\{/.test(
      src,
    )
    const called = new Set<string>()
    for (const c of src.matchAll(/\b(\w+)\.(\w+)\(/g))
      for (const m of methods.get(`${c[1]}.${c[2]}`) ?? []) called.add(m)
    // An API file's own calls are its wrappers; the gate belongs where they are used.
    if (isApiFile) continue
    for (const r of src.matchAll(ROUTE)) called.add(r[1]!)
    const dir = /^features\/([^/]+)\//.exec(file)?.[1]
    const owner = dir ? pages.get(dir) : undefined
    for (const mod of called) {
      if (!DORMANT.has(mod)) continue
      // The page gate covers a feature whose every page is this module's.
      if (owner?.size === 1 && owner.has(mod)) continue
      const gated =
        src.includes(`module="${mod}"`) ||
        src.includes(`useModuleOn('${mod}')`) ||
        src.includes(`moduleOn('${mod}')`) ||
        (src.includes('useModuleEnabled') && src.includes(`'${mod}:`))
      if (!gated) out.push(`${file} → ${mod}`)
    }
  }
  return out.sort()
}

/** The bare ErrorState, outside the mapping itself, with the reason each may stay. */
const BARE_ERROR_STATE: Record<string, string> = {
  'components/layout/query-error-state.tsx':
    'the one mapping renders it for a failure',
  'app/pages/route-error.tsx':
    'a render crash caught by the router, not a read',
  'features/home/recent-work.tsx':
    'one state for two kernel reads (live sessions and runs), each mounted only with its permission',
  'features/finops/model-rate-catalog.tsx':
    'the catalog was read and its CONTENT is invalid: a data finding, not a read failure',
}

describe('module gate and error mapping census (EU18, EU20)', () => {
  it('every read of a module that can be off is behind that module’s gate', () => {
    expect(ungatedModuleReads()).toEqual([])
  })

  it('finds the API calls it guards (the census reads real routes)', () => {
    const methods = apiMethods()
    expect(methods.get('consoleApi.listModelGroups')).toEqual(
      new Set(['models']),
    )
    expect(methods.size).toBeGreaterThan(300)
    expect(pageModules().get('finops')).toEqual(new Set(['finops']))
  })

  it('no panel renders a failed read with the bare ErrorState', () => {
    const bare = [...SOURCES]
      .filter(([, src]) => src.includes('<ErrorState'))
      .map(([file]) => file)
      .filter((file) => !(file in BARE_ERROR_STATE))
    expect(bare).toEqual([])
  })

  it('every listed exception still renders the bare ErrorState', () => {
    for (const file of Object.keys(BARE_ERROR_STATE))
      expect(SOURCES.get(file), file).toContain('<ErrorState')
  })
})
