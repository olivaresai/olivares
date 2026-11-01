// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// RUSSIAN CONSOLE: ONE WORD PER CONCEPT, NO ENGLISH LEFT BEHIND (#2404). The parity gate
// proves the keys exist; it cannot see that a value is still English, or that the sidebar
// says "Провайдеры" while a hint sends the reader to «Поставщики». The glossary is the
// audit's (analysis-i18n-20261016, section 3): провайдер, сессия, рабочая область, парк,
// соответствие требованиям, Хуки, kill switch. This reads the Russian catalog and the
// English one it is translated from, so a later edit cannot bring a second word back.
// Other locales join the clean list when their own issue lands.
import { describe, expect, it } from 'vitest'

type Tree = { [key: string]: string | Tree }

const files = import.meta.glob(
  [
    '@/features/**/i18n/en.json',
    '@/features/**/i18n/ru.json',
    '@/lib/i18n/locales/en/*.json',
    '@/lib/i18n/locales/ru/*.json',
  ],
  { eager: true, import: 'default' },
) as Record<string, Tree>

type Entry = { key: string; ns: string; en: string; ru: string }

function leaves(
  node: string | Tree,
  path: string,
  out: Map<string, string>,
  file: string,
) {
  if (typeof node === 'string') out.set(path, node)
  else if (node && typeof node === 'object' && !Array.isArray(node))
    for (const [k, v] of Object.entries(node))
      leaves(v, path ? `${path}.${k}` : k, out, file)
  else throw new Error(`${file}: ${path} is neither a string nor an object`)
}

/** Russian has plural forms English lacks; their English source is the `_other` form. */
const pluralOther = (key: string) =>
  key.replace(/_(zero|two|few|many)$/, '_other')

/** Every Russian value with the English text it translates, keyed `ns:path`. */
function load() {
  const byLocale: Record<string, Map<string, string>> = {
    en: new Map(),
    ru: new Map(),
  }
  const namespaces: Record<string, Set<string>> = {
    en: new Set(),
    ru: new Set(),
  }
  for (const [file, json] of Object.entries(files)) {
    const feature = /features\/(.+)\/i18n\/([^/.]+)\.json$/.exec(file)
    const lib = /locales\/([^/]+)\/([^/.]+)\.json$/.exec(file)
    const found = feature ?? lib
    if (!found) throw new Error(`${file}: not a catalog path`)
    const [locale, ns] = feature
      ? [feature[2], feature[1]]
      : [found[1], found[2]]
    const flat = new Map<string, string>()
    leaves(json, '', flat, file)
    namespaces[locale].add(ns)
    for (const [path, text] of flat) {
      const key = `${ns}:${path}`
      if (byLocale[locale].has(key))
        throw new Error(`${file}: duplicate key ${key}`)
      byLocale[locale].set(key, text)
    }
  }
  const missingEnglish: string[] = []
  const all: Entry[] = [...byLocale.ru].map(([key, ru]) => {
    const en = byLocale.en.get(key) ?? byLocale.en.get(pluralOther(key))
    if (en === undefined) missingEnglish.push(key)
    return { key, ns: key.slice(0, key.indexOf(':')), en: en ?? '', ru }
  })
  return { all, missingEnglish, namespaces }
}

const { all, missingEnglish, namespaces } = load()

/** The Russian text of a key the test pins; a missing key is a failure with its name. */
function ruText(key: string): string {
  const found = all.find((e) => e.key === key)
  if (!found) throw new Error(`no ru key ${key}`)
  return found.ru
}

/** Text a reader sees: no {{placeholders}}, no `code` spans. */
const prose = (text: string) =>
  text.replace(/\{\{[^}]*\}\}/g, ' ').replace(/`[^`]*`/g, ' ')

function offenders(test: (e: Entry) => boolean): string[] {
  return all.filter(test).map((e) => `${e.key} = ${e.ru}`)
}

// Exceptions, by key and reason. A test fails when one names a key that is gone.
// ponytail: an exception excuses a key, not its value, so a kept key whose English later
// changes stays excused; pin the value (a Map) the first time that happens.
/** The command palette matches the old word too: the alias list is search input, not copy. */
const SEARCH_ALIASES = new Set(['nav:aliases.sessions'])
/** Code samples and product names that carry a Latin word the Russian rule would catch. */
const LATIN_OK = new Set([
  'recordings:policy.namespaces.placeholder',
  'sandbox:scenarios.create.subjectKindPlaceholder',
  'rate-limits:caveats.managedAgents',
  'nav:items.agentArtifacts',
  'agent-artifacts:title',
])
/** JSON a person types, copied verbatim into every locale. */
const EXAMPLES = new Set([
  'settings:general.languageHint',
  'catalog:editor.specPlaceholder',
  'catalog:admission.bundlePlaceholder',
])
const KEPT_ENGLISH = new Set<string>([
  // Names the tools and standards give: product, protocol, RFC and CNCF titles.
  'agentops:title',
  'agentops:wiki.title',
  'agentops:readiness.protocol.grok_acp',
  'agentops:readiness.protocol.opencode_acp',
  'agentops:transport.remote-control',
  // A GitHub page, shown as a person types it in every locale.
  'agentops:create.sourceUrlPlaceholder',
  'agentops:readiness.protocol.claude_stream_json',
  'agentops:readiness.protocol.claude_remote_control',
  'agentops:readiness.protocol.codex_app_server',
  'agentops:readiness.protocol.gemini_acp',
  'agentops:readiness.protocol.cursor_acp',
  'identity:references.labels.prm',
  'identity:references.labels.asMetadata',
  'identity:references.labels.resourceIndicators',
  'identity:references.labels.oauthSecurityBcp',
  'identity:references.labels.certManager',
  'identity:references.labels.workloadIdentity',
  'identity:references.labels.externalKeys',
  'identity:login.passkeys.defaultNameNoDevice',
  'identity:stepUpPolicy.options.passkey',
  'sandbox:runner.microvm',
  // Values the Claude Code CLI takes, shown as typed in every locale.
  'agentops:permissionMode.default',
  'agentops:permissionMode.acceptEdits',
  'agentops:permissionMode.plan',
  'agentops:permissionMode.auto',
  'agentops:permissionMode.dontAsk',
  'agentops:permissionMode.bypassPermissions',
  'agentops:effort.medium',
  'agentops:effort.high',
  'agentops:effort.xhigh',
  // The glossary writes kill switch in English, on every surface.
  'agentops:gov.killSwitch',
  'killswitch:title',
  // Paths and JSON a person types as an example.
  'agentops:workspaces.create.rootPathPlaceholder',
  'agentops:workspaces.create.containerTargetPlaceholder',
  'agentops:workspaces.create.allowSubpathsPlaceholder',
  'agentops:workspaces.readOnly.folderPlaceholder',
  'agentops:profiles.create.configHomePlaceholder',
  'agentops:profiles.create.userHomePlaceholder',
  'sandbox:scenarios.create.subjectKindPlaceholder',
  'sandbox:scenarios.create.stepKeyPlaceholder',
  'sandbox:scenarios.create.mockResourcePlaceholder',
  'sandbox:scenarios.create.mockResponsePlaceholder',
])

describe('ru console glossary', () => {
  // An empty or partial glob would make every check below pass on nothing.
  it('reads the whole catalog, with an English source for every key', () => {
    // About 13,000 values today; far fewer means the glob lost files.
    expect(all.length).toBeGreaterThan(12000)
    expect(missingEnglish).toEqual([])
    expect([...namespaces.ru].sort()).toEqual([...namespaces.en].sort())
  })

  it('names only keys that exist in its exceptions', () => {
    const keys = new Set(all.map((e) => e.key))
    const gone = [
      ...SEARCH_ALIASES,
      ...LATIN_OK,
      ...EXAMPLES,
      ...KEPT_ENGLISH,
    ].filter((k) => !keys.has(k))
    expect(gone).toEqual([])
  })

  // [what must not appear, the glossary word, the pattern]
  const banned: [string, string, RegExp][] = [
    ['сеанс', 'сессия', /сеанс/iu],
    ['рабочее пространство', 'рабочая область', /рабоч\p{L}*\s+пространств/iu],
    ['ландшафт', 'парк', /ландшафт/iu],
    ['аварийный выключатель', 'kill switch', /аварийн\p{L}*\s+выключател/iu],
    ['lazy plural "(s)"', 'a count label, or plural keys', /\p{L}\(s\)/u],
  ]
  it.each(banned)('has no "%s" (use %s)', (_what, _use, pattern) => {
    expect(
      offenders((e) => !SEARCH_ALIASES.has(e.key) && pattern.test(e.ru)),
    ).toEqual([])
  })

  // Latin words that have a Russian one; case matters for home, which is HOME in env names.
  const LATIN =
    /(?<![\p{L}\d_:/.-])(?:agents?|workspaces?|providers?|governance|compliance|hooks?|credencial)(?![\p{L}\d_:/-]|\.\p{L})/iu
  const LATIN_HOME = /(?<![\p{L}\d_:/.-])homes?(?![\p{L}\d_:/-])/u
  it('leaves no English agent, workspace, provider, governance, compliance, hook or home in running text', () => {
    expect(
      offenders((e) => {
        const text = prose(e.ru)
        return (
          !LATIN_OK.has(e.key) && (LATIN.test(text) || LATIN_HOME.test(text))
        )
      }),
    ).toEqual([])
  })

  // The English source tells provider (провайдер) from supplier and vendor (поставщик).
  it('says провайдер where the English says provider', () => {
    expect(
      offenders(
        (e) =>
          /provider/i.test(prose(e.en)) &&
          !/supplier|vendor/i.test(e.en) &&
          /поставщик/iu.test(e.ru),
      ),
    ).toEqual([])
  })

  // [the concept, the English pattern, the glossary word the Russian must carry]
  const fromEnglish: [string, RegExp, RegExp][] = [
    ['парк', /\bestates?\b/i, /парк/iu],
    ['kill switch', /kill switch/i, /kill switch/i],
    ['хук', /\bhooks?\b/i, /хук/iu],
  ]
  it.each(fromEnglish)(
    'says %s where the English names the concept',
    (_word, english, russian) => {
      expect(
        offenders((e) => english.test(prose(e.en)) && !russian.test(e.ru)),
      ).toEqual([])
    },
  )

  it('names the sidebar words the same way everywhere', () => {
    expect(ruText('nav:items.estate')).toBe('Парк')
    expect(ruText('estate:title')).toBe('Парк')
    expect(ruText('models:tabs.estate')).toBe('Парк')
    expect(ruText('nav:shell.rail.session')).toBe('Сессия')
    for (const key of [
      'nav:items.compliance',
      'settings:modules.names.compliance',
      'platforms:surfaces.columns.compliance',
    ])
      expect(ruText(key), key).toBe('Соответствие требованиям')
    expect(ruText('claude-policy:tabs.hooks')).toBe('Хуки')
  })

  it('sends the reader to the page by the sidebar word', () => {
    const providers = ruText('nav:items.providers')
    expect(providers).toBe('Провайдеры')
    for (const key of [
      'agentops:profiles.create.homesOptionalManaged',
      'agent-tools:ollama.hint',
    ])
      expect(ruText(key), key).toContain(`«${providers}»`)
  })
})

describe('ru console is translated', () => {
  const words = (text: string) => text.split(/\s+/).filter(Boolean).length

  it('has no English prose: identical to English, 40+ characters, 6+ words', () => {
    expect(
      offenders((e) => {
        const text = prose(e.ru).trim()
        return (
          !EXAMPLES.has(e.key) &&
          e.ru === e.en &&
          text.length >= 40 &&
          words(text) >= 6
        )
      }),
    ).toEqual([])
  })

  // Eleven namespaces were English wholesale. A label there identical to English with a
  // real English word in it is a missed translation unless it is a name listed above.
  const TRANSLATED = new Set([
    'protocol-bindings',
    'finops',
    'knowledge',
    'models',
    'evals',
    'agentops',
    'compliance',
    'identity',
    'killswitch',
    'sandbox',
    'security',
  ])
  // A word of four letters or more with a lowercase one: "Save", "Code", not "PII" or "OSCAL".
  const hasEnglishWord = (text: string) =>
    (prose(text).match(/[A-Za-z]{4,}/g) ?? []).some((w) => /[a-z]/.test(w))
  it('has no English label in the namespaces that were English', () => {
    expect(
      offenders(
        (e) =>
          TRANSLATED.has(e.ns) &&
          !KEPT_ENGLISH.has(e.key) &&
          e.ru === e.en &&
          hasEnglishWord(e.ru),
      ),
    ).toEqual([])
  })
})
