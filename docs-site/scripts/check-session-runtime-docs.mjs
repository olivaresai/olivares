// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Content regression check: *_BIN overrides executable resolution, not registration.
// These patterns cover the reported wording in all seven languages, not every
// possible paraphrase. Scope them to paragraphs about binary overrides so valid
// readiness failures elsewhere (missing CLI, authentication, policy) stay valid.
import { readFile, readdir } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'
import path from 'node:path'

const root = fileURLToPath(new URL('../../', import.meta.url))
const docs = path.join(root, 'docs-site/src/content/docs')
const bin = /OLIVARES_SESSION_RUNTIME_(?:CLAUDE|CODEX|GROK|OPENCODE)_BIN/
const localeData = await Promise.all(['en', 'de', 'es', 'fr', 'ja', 'ru', 'zh'].map(async (locale) => {
  const data = JSON.parse(await readFile(new URL(`./locales/${locale}.json`, import.meta.url), 'utf8'))
  return {
    locale,
    staleClaim: localeRegex(locale, 'staleClaims', data),
    noProvider: localeRegex(locale, 'noProvider', data?.noProvider),
  }
}))
const staleClaims = localeData.map(({ staleClaim }) => staleClaim)
const stale = (text) => staleClaims.some((pattern) => pattern.test(text.replace(/\s+/g, ' ')))
const findings = []
const noProvider = Object.fromEntries(localeData.map(({ locale, noProvider }) => [locale, noProvider]))

function localeRegex(locale, name, pattern) {
  if (typeof pattern?.source !== 'string' || !pattern.source || typeof pattern?.flags !== 'string') {
    throw new Error(`Invalid ${locale} locale regex (${name})`)
  }
  return new RegExp(pattern.source, pattern.flags)
}

async function checkPages(dir) {
  let count = 0
  for (const entry of await readdir(dir, { withFileTypes: true })) {
    const file = path.join(dir, entry.name)
    if (entry.isDirectory()) count += await checkPages(file)
    else if (/\.mdx?$/.test(entry.name)) {
      const text = await readFile(file, 'utf8')
      if (/\/how-to\/(?:first-hour|operate-provider-sessions|add-a-provider)\.md$/.test(file)) {
        const locale = path.relative(docs, file).split(path.sep)[0]
        const unbound = noProvider[locale === 'how-to' ? 'en' : locale]
        for (const paragraph of text.split(/\n\s*\n/)) {
          if (/OLIVARES_SESSION_RUNTIME_(?:WIF|TOKEN_FILE)/.test(paragraph) &&
              (!unbound?.test(paragraph.replace(/\s+/g, ' ')) ||
               !paragraph.includes('`managed_injection`') ||
               !paragraph.includes('`provider_account_home`') ||
               (!file.endsWith('/add-a-provider.md') &&
                !/\/how-to\/add-a-provider\//.test(paragraph)))) {
            findings.push(`${path.relative(root, file)}: scope host-wide Claude credentials to managed injection without a provider; explain bound keys and own logins`)
          }
        }
      }
      if (bin.test(text) && /does not search `?PATH/i.test(text)) {
        findings.push(`${path.relative(root, file)}: executable resolution must include the engine's PATH`)
      }
      if (/\/how-to\/integrations\/(codex|grok)\.md$/.test(file) &&
          !text.split(/\n\s*\n/).some((paragraph) => bin.test(paragraph) && /PATH/.test(paragraph))) {
        findings.push(`${path.relative(root, file)}: session executable choices must include PATH`)
      }
      for (const paragraph of text.split(/\n\s*\n/)) {
        if (bin.test(paragraph) && stale(paragraph)) {
          findings.push(`${path.relative(root, file)}: binary override described as a registration or launch prerequisite`)
        }
      }
      count++
    }
  }
  return count
}

const pages = await checkPages(docs)
if (pages === 0) throw new Error('No documentation pages scanned')
for (const driver of ['codex', 'grok']) {
  const file = `connectors/${driver}/README.md`
  const text = await readFile(path.join(root, file), 'utf8')
  const launch = text.split('\n').find((line) => line.startsWith('| Session launch |'))
  if (!launch || !bin.test(launch) || !/PATH/.test(launch)) {
    findings.push(`${file}: session executable choices must include PATH`)
  }
}
const catalog = await readFile(path.join(root, 'scripts/config-env-catalog.tsv'), 'utf8')
for (const driver of ['CODEX', 'GROK', 'OPENCODE']) {
  const key = `OLIVARES_SESSION_RUNTIME_${driver}_BIN`
  const rows = catalog.split('\n').filter((line) => line.startsWith(`${key}\t`))
  if (rows.length !== 1 || stale(rows[0]) || !/override/i.test(rows[0]) ||
      !/managed install/i.test(rows[0]) || !/PATH/.test(rows[0])) {
    findings.push(`scripts/config-env-catalog.tsv: ${key} must describe an override of managed install/PATH resolution`)
  }
}

if (findings.length) {
  console.error(findings.join('\n'))
  process.exit(1)
}
console.log(`session-runtime docs: ${pages} pages, two connector READMEs and three catalog overrides checked`)
