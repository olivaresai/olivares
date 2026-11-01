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
  /** Words from which an untranslated English text counts as a sentence (default PROSE_MIN_WORDS). */
  proseMinWords?: number
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

const frEnvironment = 'environnement' // language-data: fr word for an environment, rejected for the Estate
// \b is ASCII-only in JS, so it would cut «Hôtes» at the accent and read a "tes" inside:
// the boundaries must be Unicode lookarounds.
const frTu = /(?<![\p{L}\p{N}_])(?:tu|ta|tes|ton)(?![\p{L}\p{N}_])/u // language-data: fr informal register, rejected in a vous console
const frLazyPlural = /\p{L}\(s\)/u // language-data: fr lazy plural "(s)", rejected; a label or a count reads better
// The fr API key spelled without "d'", rejected for the form with it.
const frApiKey = /cl[e\u00e9]s?\s+api\b/i
// The fr settings word the Settings module must not use.
const frReglages = /(?<![\p{L}\p{N}_])r\u00e9glages?(?![\p{L}\p{N}_])/iu
const frWorkspaceAbsent = 'Workspace absent de la liste' // language-data: fr sentence that leaves "workspace" in English
const frSettingsWord = 'Paramètres' // language-data: fr word for the Settings module
const frSettingsRejected = 'Réglages' // language-data: fr settings word the Settings module must not use
const frLazyPluralExample = '{{count}} élément(s)' // language-data: fr plural written with "(s)"
const frApiKeyRight = "Clé d'API" // language-data: fr API key, the pinned form
const frApiKeyWrong = 'Clé API' // language-data: fr API key without "d'", rejected
const frWorkspaceUnresolved = 'Espace de travail absent de la liste' // language-data: fr pinned nav:workspace.unresolved
const frComplianceColumn = 'Conformité' // language-data: fr pinned compliance column
const frKey = 'Clé' // language-data: fr pinned cost centre key

const PROVIDER = /\bproviders?\b/i
const AGENT = /\bagents?\b/i
const ESTATE = /\bestate\b/i
/** The English words the fr rules reject inside a translated sentence. */
const WORKSPACE = /\bworkspaces?\b/i
const COMPLIANCE = /\bcompliance\b/i
/** The workspace word for the de rules, not a path segment such as the literal "/workspace" hint. */
const DE_WORKSPACE = /(?<!\/)\bworkspaces?\b(?!\/)/i
/** The tool word, not a scope literal ("tools:call") or a path segment. */
const TOOL = /(?<!\/)\btools?\b(?![:\/])/i
const APPROVAL = /\bapprovals?\b/i
/** A German informal form: du, dich, dir, dein..., euch, euer... (Sie is the console's form). */
const DU_FORM = /\b(?:du|dich|dir|dein\w*|euch|euer\w*)\b/i
/**
 * A German du imperative: the verb that opens a sentence or a clause ("Versuche es erneut.",
 * "... und komm hierher zurück", "...; grenze sie ein"), where the Sie form is "Versuchen Sie".
 * A sentence opener needs more words after it, so the noun label "Versuche" or "Max. Versuche" is
 * not one; "ich" after a clause verb marks the first person ("oder nutze ich wieder").
 * ponytail: a list of the verbs the console uses, not every German verb; add the verb with the next du found.
 */
const DE_IMPERATIVE_VERBS = (
  'Gib Lies Nimm Sieh Wähle Gehe Geh Komm Lass Halte Prüfe Überprüfe Wende Klicke Öffne ' +
  'Schließe Speichere Lösche Füge Ergänze Erstelle Starte Stoppe Beende Setze Lege Warte ' +
  'Beachte Melde Trage Verwende Nutze Aktiviere Deaktiviere Kehre Kopiere Entferne ' +
  'Bestätige Genehmige Lehne Wiederhole Versuche Verbinde Führe Schreibe Erlaube Teste ' +
  'Tippe Wechsle Ändere Passe Vergib Erhöhe Aktualisiere Ersetze Lade Schalte Ordne'
).split(' ')
/** Verbs that are also nouns when capitalised ("Grenze"), so only a clause can open with them. */
const DE_IMPERATIVE_CLAUSE_ONLY = ['grenze']
const deVerbs = (initial: (c: string) => string) =>
  DE_IMPERATIVE_VERBS.map((v) => initial(v[0]) + v.slice(1)).join('|')
const DE_IMPERATIVE_START = new RegExp(
  `(?:^|[.!?:]\\s+)(?:${deVerbs((c) => c.toUpperCase())})(?![\\p{L}])(?! Sie)(?=\\s+\\S|…)`,
  'u',
)
const DE_IMPERATIVE_CLAUSE = new RegExp(
  `(?:[;,—–(]|(?<![\\p{L}])(?:und|dann|oder|bitte))\\s*(?:${deVerbs((c) => c.toLowerCase())}|${DE_IMPERATIVE_CLAUSE_ONLY.join('|')})(?![\\p{L}])(?! Sie\\b| ich\\b)`,
  'u',
)
/** A statement in the first person ("Ich habe ... geprüft und genehmige ...") instructs nobody. */
const DE_FIRST_PERSON = /^Ich(?![\p{L}])/u
const isDeImperative = (text: string) =>
  DE_IMPERATIVE_START.test(text) ||
  (DE_IMPERATIVE_CLAUSE.test(text) && !DE_FIRST_PERSON.test(text))
/** A plural attached to its noun as a bracketed suffix, such as "Frame(s)" or "Ergebnis(se)". A unit in its own brackets ("Fenster (s)", seconds) has a space before the bracket and is not a plural. */
const LAZY_PLURAL = /\w\((?:s|e|en|n|se|er|r)\)/
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
/** A path a person types into the field, not a word a reader reads. */
const FR_WORKSPACE_PATH = new Set([
  'agentops:workspaces.create.containerTargetPlaceholder',
])
/** The Compliance API keeps its product name, and the permission token is typed as is. */
const FR_COMPLIANCE_NAME = new Set([
  'attestation:forbidden',
  'compliance:posture.capabilityHint.external_activity',
  'platforms:surfaces.apiSupportDescription',
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
      'attestation:scorecard.title',
      'attestation:airgap.gpgProv',
      'agentops:title',
      'agentops:create.envAllowPlaceholder',
      'agentops:workspaces.create.allowSubpathsPlaceholder',
      'agentops:readiness.protocol.claude_stream_json',
      'agentops:readiness.protocol.claude_remote_control',
      'agentops:readiness.protocol.codex_app_server',
      'agentops:readiness.protocol.grok_acp',
      'agentops:readiness.protocol.opencode_acp',
      'agentops:readiness.protocol.gemini_acp',
      'agentops:readiness.protocol.cursor_acp',
      'console:sso.acsUrl',
      'console:sso.idpSsoUrl',
      'console:sso.entityId',
      'model-ops:documents.aibom.cyclonedx',
      'model-ops:documents.aibom.spdx',
      'model-ops:documents.card.markdown',
      'first-hour:tools.claude',
      'first-hour:tools.grok',
      'first-hour:tools.gemini-cli',
      'first-hour:tools.cursor',
      'adoption:boundary.providers.microsoft-foundry',
      'adoption:boundary.providers.amazon-bedrock',
      'adoption:boundary.providers.vertex-ai',
      'platforms:surfaces.columns.fedramp',
      'eventing:dialog.sinkKinds.https',
      'eventing:dialog.sinkKinds.splunk_hec',
      'eventing:dialog.sinkKinds.sentinel_dcr',
      'eventing:dialog.sinkKinds.newrelic',
      'catalog:admission.bundlePlaceholder',
      'common:app.name',
      'auth:firstRun.namePlaceholder',
    ]),
    proseMinWords: 2,
  },
  de: {
    rules: [
      {
        name: 'the console speaks to the reader as Sie, never du',
        bad: (_en, own) => DU_FORM.test(withoutCode(own)),
        example: ['Saved in your account', 'In deinem Konto gespeichert'],
      },
      {
        name: 'an instruction to the reader is a Sie form, never a du imperative',
        bad: (_en, own) => isDeImperative(withoutCode(own)),
        example: ['Try again.', 'Es ging nicht. Versuche es erneut.'],
      },
      {
        name: 'a workspace is an Arbeitsbereich, not the English word',
        bad: (en, own) =>
          DE_WORKSPACE.test(en) && DE_WORKSPACE.test(withoutCode(own)),
        example: ['Workspace list', 'Workspace-Liste'],
      },
      {
        name: 'a tool is a Werkzeug, not the English word',
        bad: (en, own) => TOOL.test(en) && TOOL.test(withoutCode(own)),
        example: ['A tool started', 'Ein Tool gestartet'],
      },
      {
        name: 'an approval is a Genehmigung; Freigabe is for release or sharing',
        bad: (en, own) => APPROVAL.test(en) && /Freigabe/.test(own),
        example: ['Approval', 'Freigabe'],
      },
      {
        name: 'the estate is Bestand, not Umgebung',
        bad: (en, own) => ESTATE.test(en) && own.includes('Umgebung'),
        example: ['Estate', 'Umgebung'],
      },
      {
        name: 'a counted noun is a plural key, not a (s) suffix',
        bad: (_en, own) => LAZY_PLURAL.test(own),
        example: ['{{count}} frame(s)', '{{count}} Frame(s)'],
      },
    ],
    pinned: [
      ['nav:items.estate', 'Bestand'],
      ['nav:items.killswitch', 'Not-Aus'],
      ['nav:workspace.unresolved', 'Arbeitsbereich'],
      ['nav:personal.saveFailed', 'Ihrem Konto'],
      ['nav:shell.rail.session', 'Sitzung'],
      ['nav:launcher.draftSaveFailed', 'Lassen Sie'],
      ['common:notifications.events.toolInstalled', 'Werkzeug'],
      ['settings:modules.lines.governance', 'Genehmigungen'],
      ['finops:costCentres.key', 'Schlüssel'],
      ['sessions:limit.rate', 'Versuchen Sie es'],
      ['governance:breakGlass.activationHardwareRequired', 'kehren Sie'],
      ['console:granular.accessReview.resultCount_one', 'Ergebnis'],
      ['console:granular.accessReview.resultCount_other', 'Ergebnisse'],
    ],
    englishAllowed: new Set([
      'catalog:editor.specPlaceholder',
      'settings:general.languageHint',
      // Product and standard names: Claude Platform on AWS, Anthropic's
      // workload identity and external keys, and the RFC titles.
      'adoption:boundary.providers.claude-platform-aws',
      'identity:references.labels.workloadIdentity',
      'identity:references.labels.externalKeys',
      'identity:references.labels.prm',
      'identity:references.labels.asMetadata',
      'identity:references.labels.oauthSecurityBcp',
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
      // models:title is the place's name, Models, since #3388; the key references keep the term.
      ['models:keys.title', '프로바이더'],
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
  fr: {
    rules: [
      {
        name: 'a workspace is not left in English inside a sentence',
        bad: (en, own, id) =>
          WORKSPACE.test(en) &&
          /workspaces?/i.test(withoutCode(own)) &&
          !FR_WORKSPACE_PATH.has(id),
        example: ['Workspace not in list', frWorkspaceAbsent],
      },
      {
        name: 'compliance is not left in English (the Compliance API keeps its name)',
        bad: (en, own, id) =>
          COMPLIANCE.test(en) &&
          /compliance/i.test(withoutCode(own)) &&
          !FR_COMPLIANCE_NAME.has(id),
        example: ['Compliance', 'Compliance'],
      },
      {
        name: 'the estate is not an environment',
        bad: (en, own) =>
          ESTATE.test(en) &&
          !/environment/i.test(en) &&
          own.toLowerCase().includes(frEnvironment),
        example: ['Estate', 'Environnement'],
      },
      {
        name: `the Settings module is ${frSettingsWord}, not ${frSettingsRejected}`,
        bad: (en, own) => /\bsettings\b/i.test(en) && frReglages.test(own),
        example: ['Settings', frSettingsRejected],
      },
      {
        name: 'the reader is vous, not tu',
        bad: (_en, own) => frTu.test(own),
        example: ['The fence you act on', 'Le fence sur lequel tu agis'],
      },
      {
        name: 'a plural is not written with "(s)"',
        bad: (_en, own) => frLazyPlural.test(own),
        example: ['{{count}} item(s)', frLazyPluralExample],
      },
      {
        name: `an API key is a ${frApiKeyRight}, not a ${frApiKeyWrong}`,
        bad: (en, own) => /\bapi keys?\b/i.test(en) && frApiKey.test(own),
        example: ['API key', frApiKeyWrong],
      },
    ],
    pinned: [
      ['settings:title', 'Paramètres'],
      ['common:commandPalette.settingsGroup', 'Paramètres'],
      ['nav:items.settings', 'Paramètres'],
      ['nav:items.estate', 'Parc'],
      ['estate:title', 'Parc'],
      ['models:tabs.estate', 'Parc'],
      ['nav:workspace.unresolved', frWorkspaceUnresolved],
      ['nav:items.workspace-templates', 'Modèles d’espace de travail'],
      ['platforms:surfaces.columns.compliance', frComplianceColumn],
      ['console:activation.title', 'Activation de Business'],
      ['finops:costCentres.key', frKey],
      ['models:keys.kind.api_key', frApiKeyRight],
      ['providers:create.apiKey', frApiKeyRight],
      ['communications:cursor.fields.workspace', 'Espace de travail'],
    ],
    englishAllowed: new Set([
      'catalog:editor.specPlaceholder',
      'catalog:admission.bundlePlaceholder',
      'settings:general.languageHint',
      'adoption:boundary.providers.claude-platform-aws',
      'identity:references.labels.prm',
      'identity:references.labels.asMetadata',
      'identity:references.labels.oauthSecurityBcp',
      'identity:references.labels.workloadIdentity',
      'identity:references.labels.externalKeys',
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

describe('de gate patterns', () => {
  it.each([
    'Versuche es erneut.',
    'Der Lauf schlug fehl. Öffne das Profil, um es zu prüfen.',
    'Teste…',
    'Gehe zu',
    'Sitzung beendet und komm dann hierher zurück.',
    'Die Liste ist lang; grenze sie mit den Filtern ein.',
    'Ordne einer Klasse eine Aktion zu.',
  ])('flags the du imperative in %j', (text) => {
    expect(isDeImperative(text)).toBe(true)
  })

  it.each([
    'Versuchen Sie es erneut.',
    'Öffnen Sie das Profil, um es zu prüfen.',
    'Versuche',
    'Max. Versuche',
    'Grenze aktiver Routinen',
    'Welche Artefakte verbinde oder nutze ich wieder?',
    'Wie bereite ich eine Bereitstellung vor und steuere sie?',
    'Kehren Sie dann hierher zurück.',
    'Ich habe diese Skills geprüft und genehmige, dass sie laden.',
  ])('leaves %j alone', (text) => {
    expect(isDeImperative(text)).toBe(false)
  })

  it.each([
    'Frame(s)',
    'Ergebnis(se)',
    'Hinweis(e)',
    'Wert(er)',
    'Zahl(en)',
    'Ding(r)',
  ])('flags the bracketed plural in %j', (text) => {
    expect(LAZY_PLURAL.test(text)).toBe(true)
  })

  it('leaves a unit in its own brackets alone', () => {
    expect(LAZY_PLURAL.test('Fenster (s)')).toBe(false)
  })
})

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
            englishWords(text) >= (language.proseMinWords ?? PROSE_MIN_WORDS) &&
            !language.englishAllowed.has(id),
        )
        .map(([id, text]) => `${locale} ${id}: ${text.slice(0, 90)}`)
      expect(offenders).toEqual([])
    })
  },
)
