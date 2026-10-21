// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// ONE TERM, ONE WORD, PER LANGUAGE. The translation audit of 2026-10-16 (#2405) found the
// Chinese catalog calling a provider by three words, an agent by the word for a proxy, the
// Estate module by the generic word for an environment, and over a hundred sentences still
// in English. The English catalog is the source: wherever it says "provider", "agent" or
// "estate", the translation uses its language's one word. A language joins LANGUAGES when
// its quality issue lands. A rule rejects one retired word for one English meaning, and the
// only way to excuse a string is to name its id, so a wrong word cannot hide behind a
// sentence that happens to contain the right one too.
import { describe, expect, it } from 'vitest'

import { LANGUAGE_CODES } from './index'

/** A rule flags an English text and its translation that carry the defect. */
type Rule = {
  name: string
  bad: (english: string, own: string, id: string) => boolean
  /** A pair the rule must flag: proof that the rule can fail. */
  example: [english: string, own: string]
}

type Language = {
  rules: Rule[]
  /** `namespace:key` -> the word its translation must contain (labels set by hand). */
  pinned: [id: string, term: string][]
  /** English text that is right as it is: a JSON example, a list of language names, a product name. */
  englishAllowed: Set<string>
}

const zhRetiredProvider = '提供方|服务商' // language-data: zh retired provider words, rejected
const zhSupplier = '供应商' // language-data: zh supplier word, rejected for a provider
const zhProxy = '代理' // language-data: zh word for a proxy, rejected for an agent
const zhEnvironment = '环境' // language-data: zh word for an environment, rejected for the Estate
const zhProvider = '提供商' // language-data: zh word for a provider
const zhAgent = '智能体' // language-data: zh word for an agent
const zhWorkspace = '工作区' // language-data: zh word for a workspace
const zhGovernance = '治理' // language-data: zh word for governance
const zhCompliance = '合规' // language-data: zh word for compliance
const zhExecute = '执行' // language-data: zh word for execute
const zhEstate = '资产' // language-data: zh word for the Estate module label
const zhEstateDomain = '资产域' // language-data: zh word for the estate in running text

const PROVIDER = /\bproviders?\b/i
const AGENT = /\bagents?\b/i
const ESTATE = /\bestate\b/i
const PROXY = /prox(?:y|ies)|broker/i
const HAN = /\p{Script=Han}/u
/** Hangul: the script a Korean sentence is written in. */
const HANGUL = /\p{Script=Hangul}/u
/** The English word "agent" on its own: not agent_aibom, agent:read or AGENTS.md. */
const LATIN_AGENT = /(?<![\w-])agents?(?![\w:.-])/i
/** Text a translator does not write: placeholders and code spans. */
const withoutCode = (text: string) => text.replace(/\{\{[^}]*\}\}|`[^`]*`/g, '')

/** "Act as" is the proxy sense of the zh word for a proxy. */
const PROXY_SENSE = new Set(['session-viewer:detail.actAs'])
/** A product name that keeps its English words inside a Chinese sentence. */
const AGENT_PRODUCT_NAME = new Set(['rate-limits:caveats.managedAgents'])
/** The one place a provider sentence is about a supplier as well. */
const SUPPLIER_SENSE = new Set(['models:gpai.description'])
/** Korean sentences where the English talks about a supplier, not the provider. */
const KO_SUPPLIER_SENSE = new Set([
  'models:gpai.description',
  'models:gpai.emptyHint',
])

const LANGUAGES: Record<string, Language> = {
  zh: {
    rules: [
      {
        name: 'a provider is not called by a retired word',
        bad: (en, own) =>
          PROVIDER.test(en) && new RegExp(zhRetiredProvider).test(own),
        example: ['Add a provider', zhRetiredProvider.split('|')[0]],
      },
      {
        name: 'a provider is not called a supplier',
        bad: (en, own, id) =>
          PROVIDER.test(en) &&
          own.includes(zhSupplier) &&
          !SUPPLIER_SENSE.has(id),
        example: ['Add a provider', zhSupplier],
      },
      {
        name: 'the word for a proxy is only for a proxy',
        bad: (en, own, id) =>
          own.includes(zhProxy) && !PROXY.test(en) && !PROXY_SENSE.has(id),
        example: ['Worker', zhProxy],
      },
      {
        name: 'an agent is not left in English inside a sentence',
        bad: (en, own, id) =>
          AGENT.test(en) &&
          !AGENT_PRODUCT_NAME.has(id) &&
          HAN.test(own) &&
          LATIN_AGENT.test(withoutCode(own)),
        example: ['Agent artifacts', `Agent ${zhAgent}`],
      },
      {
        name: 'the estate is not an environment',
        bad: (en, own) =>
          ESTATE.test(en) &&
          !/environment/i.test(en) &&
          own.includes(zhEnvironment),
        example: ['Estate', zhEnvironment],
      },
    ],
    pinned: [
      ['finops:statements.agent', zhAgent],
      ['finops:outcomes.subject', zhAgent],
      ['finops:statements.provider', zhProvider],
      ['platforms:surfaces.columns.compliance', zhCompliance],
      ['models:routing.refusal', zhProvider],
      ['models:routing.execute', zhExecute],
      ['models:routing.executeConfirm', zhExecute],
      ['models:gpai.description', zhProvider],
      ['protocol-bindings:workspace.scope', zhWorkspace],
      ['protocol-bindings:composer.diff.fields.workspace_id', zhWorkspace],
      ['protocol-bindings:composer.steps.governance', zhGovernance],
      ['protocol-bindings:localKind.agent', zhAgent],
      ['sessions:context.workRoles.worker', zhAgent],
      ['sessions:context.workRoles.orchestrator', zhAgent],
      ['nav:items.estate', zhEstate],
      ['estate:title', zhEstate],
      ['backups:schedule.dualControlDescription', zhEstateDomain],
    ],
    englishAllowed: new Set([
      'catalog:editor.specPlaceholder',
      'settings:general.languageHint',
      'adoption:boundary.providers.claude-platform-aws',
      'identity:references.labels.workloadIdentity',
    ]),
  },
  ko: {
    rules: [
      {
        name: 'a provider is not called by a retired word',
        bad: (en, own) => PROVIDER.test(en) && own.includes('제공자'),
        example: ['Add a provider', '제공자'],
      },
      {
        name: 'a provider is not called a supplier',
        bad: (en, own, id) =>
          PROVIDER.test(en) &&
          own.includes('공급자') &&
          !KO_SUPPLIER_SENSE.has(id),
        example: ['Add a provider', '공급자'],
      },
      {
        name: 'an agent is not left in English inside a sentence',
        bad: (en, own) =>
          AGENT.test(en) &&
          HANGUL.test(own) &&
          LATIN_AGENT.test(withoutCode(own)),
        example: ['Agent artifacts', 'Agent 에이전트'],
      },
      {
        name: 'the estate is not an environment',
        bad: (en, own) =>
          ESTATE.test(en) && !/environment/i.test(en) && own.includes('환경'),
        example: ['Estate', '환경'],
      },
    ],
    pinned: [
      ['finops:statements.agent', '에이전트'],
      ['finops:outcomes.subject', '에이전트'],
      ['models:title', '프로바이더'],
      ['models:gpai.description', '프로바이더'],
      ['protocol-bindings:localKind.agent', '에이전트'],
      ['protocol-bindings:workspace.scope', '워크스페이스'],
      ['nav:items.providers', '프로바이더'],
      ['nav:items.estate', '에스테이트'],
      ['estate:title', '에스테이트'],
      ['compliance:retention.disclosure', '프로바이더'],
    ],
    englishAllowed: new Set([
      'catalog:editor.specPlaceholder',
      'catalog:admission.bundlePlaceholder',
      'settings:general.languageHint',
    ]),
  },
}

/** A text of this many English words is a sentence, not a label or a name. */
const PROSE_MIN_WORDS = 4
/** The console has about 12,900 strings per language; fewer means the glob missed files. */
const CATALOG_FLOOR = 10_000
/** A language with fewer plural categories than English has no key for the ones it lacks. */
const PLURAL_FORM = /_(zero|one|two|few|many)$/

const englishWords = (text: string) =>
  text
    .replace(/\{\{[^}]*\}\}/g, '')
    .split(/\s+/)
    .filter((word) => /[A-Za-z]{2,}/.test(word)).length

const files = import.meta.glob(
  ['@/features/**/i18n/*.json', '@/lib/i18n/locales/**/*.json'],
  { eager: true, import: 'default' },
) as Record<string, unknown>

function leaves(node: unknown, path: string, out: Map<string, string>) {
  if (typeof node === 'string') out.set(path, node)
  else if (node && typeof node === 'object')
    for (const [k, v] of Object.entries(node))
      leaves(v, path ? `${path}.${k}` : k, out)
}

/** locale -> `namespace:key.path` -> text, from the files the console loads. */
const catalog = new Map<string, Map<string, string>>()
for (const [file, json] of Object.entries(files)) {
  const feature = /features\/(.+)\/i18n\/([^/.]+)\.json$/.exec(file)
  const lib = /locales\/([^/]+)\/([^/.]+)\.json$/.exec(file)
  const [locale, ns] = feature ? [feature[2], feature[1]] : [lib![1], lib![2]]
  const flat = new Map<string, string>()
  leaves(json, '', flat)
  const texts = catalog.get(locale) ?? new Map<string, string>()
  for (const [path, text] of flat) {
    const id = `${ns}:${path}`
    // Two files must never share an id: the second would hide the first from every rule.
    if (texts.has(id)) throw new Error(`${locale} ${id} is defined twice`)
    texts.set(id, text)
  }
  catalog.set(locale, texts)
}
const english = catalog.get('en') ?? new Map<string, string>()

describe.each(Object.entries(LANGUAGES))(
  'glossary (%s)',
  (locale, language) => {
    const own = catalog.get(locale) ?? new Map<string, string>()

    it('is a language the console ships', () => {
      expect(LANGUAGE_CODES).toContain(locale)
    })

    it('reads the whole catalog', () => {
      expect(english.size, 'English catalog').toBeGreaterThan(CATALOG_FLOOR)
      const missing = [...english.keys()].filter(
        (id) => !own.has(id) && !PLURAL_FORM.test(id),
      )
      expect(missing, `${locale} keys missing`).toEqual([])
    })

    describe.each(language.rules)('$name', (rule) => {
      it('finds nothing', () => {
        const offenders = [...english]
          .filter(([id, text]) => rule.bad(text, own.get(id) ?? '', id))
          .map(([id]) => `${locale} ${id}: ${String(own.get(id)).slice(0, 90)}`)
        expect(offenders).toEqual([])
      })
      it('would flag its own example', () => {
        expect(rule.bad(rule.example[0], rule.example[1], 'example:none')).toBe(
          true,
        )
      })
    })

    it.each(language.pinned)('%s carries %s', (id, term) => {
      expect(own.get(id) ?? '<missing>').toContain(term)
    })

    it('leaves no English sentence', () => {
      const offenders = [...english]
        .filter(
          ([id, text]) =>
            own.get(id) === text &&
            englishWords(text) >= PROSE_MIN_WORDS &&
            !language.englishAllowed.has(id),
        )
        .map(([id, text]) => `${locale} ${id}: ${text.slice(0, 90)}`)
      expect(offenders).toEqual([])
    })
  },
)
