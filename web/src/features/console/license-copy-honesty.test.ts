// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// CLC-R2: Community copy must not assert a currently verified license beside No
// license.
import { readdirSync, readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'

const DIR = 'src/features/console/i18n'
const LOCALES = ['en', 'es', 'de', 'fr', 'it', 'ja', 'zh', 'ru', 'ko'] as const

type LocaleFile = {
  license: { helpBody: string; communityNote: string }
}

function load(locale: string): LocaleFile {
  return JSON.parse(readFileSync(`${DIR}/${locale}.json`, 'utf8')) as LocaleFile
}

const expected = {
  en: {
    communityNote:
      'Community edition: applying a verified license displays its details here; it does not change this build.',
    helpCommunity:
      'On Community, applying a verified license displays its details here; it does not change this build.',
  },
  es: {
    communityNote:
      'Edición Community: aplicar una licencia verificada muestra sus datos aquí; no cambia esta compilación.',
    helpCommunity:
      'En Community, aplicar una licencia verificada muestra sus datos aquí; no cambia esta compilación.',
  },
  de: {
    communityNote:
      'Community-Edition: das Anwenden einer verifizierten Lizenz zeigt deren Angaben hier; es ändert diesen Build nicht.',
    helpCommunity:
      'In der Community-Edition zeigt das Anwenden einer verifizierten Lizenz deren Angaben hier; es ändert diesen Build nicht.',
  },
  fr: {
    communityNote:
      'Édition Community : appliquer une licence vérifiée en affiche les détails ici ; cela ne modifie pas cette compilation.',
    helpCommunity:
      'En Community, appliquer une licence vérifiée en affiche les détails ici ; cela ne modifie pas cette compilation.',
  },
  it: {
    communityNote:
      'Edizione Community: applicare una licenza verificata ne mostra qui i dettagli; la build non cambia.',
    helpCommunity:
      'Su Community, applicare una licenza verificata ne mostra qui i dettagli; la build non cambia.',
  },
  ja: {
    communityNote:
      'Community エディション: 検証済みライセンスを適用すると、その詳細がここに表示されます。このビルドは変わりません。',
    helpCommunity:
      'Community では、検証済みライセンスを適用すると、その詳細がここに表示されます。このビルドは変わりません。',
  },
  zh: {
    communityNote:
      'Community 版本：应用已验证的许可证会在此显示其详情；这不会改变此构建。',
    helpCommunity:
      '在 Community 中，应用已验证的许可证会在此显示其详情；这不会改变此构建。',
  },
  ru: {
    communityNote:
      'Редакция Community: применение проверенной лицензии показывает её сведения здесь; это не меняет эту сборку.',
    helpCommunity:
      'В Community применение проверенной лицензии показывает её сведения здесь; это не меняет эту сборку.',
  },
  ko: {
    communityNote:
      'Community 에디션: 확인된 라이선스를 적용하면 여기에 세부 정보가 표시될 뿐 이 빌드는 바뀌지 않습니다.',
    helpCommunity:
      'Community에서는 확인된 라이선스를 적용하면 여기에 세부 정보가 표시될 뿐 이 빌드는 바뀌지 않습니다.',
  },
} as const

const leftoverShown = [
  /a verified license is shown/i,
  /se muestra una licencia verificada/i,
  /una licencia verificada se muestra aquí/i,
  /eine verifizierte Lizenz wird angezeigt/i,
  /wird eine verifizierte Lizenz hier angezeigt/i,
  /une licence vérifiée s'affiche/i,
  /una licenza verificata viene mostrata/i,
  /検証済みライセンスは表示され/,
  /显示已验证的许可证/,
  /已验证的许可证会显示在此/,
  /проверенная лицензия показывается/,
  /확인된 라이선스가 여기에 표시됩니다/,
]

describe('license copy honesty (CLC-R2)', () => {
  it('covers the nine shipped console locales and no others', () => {
    const files = readdirSync(DIR)
      .filter((name) => name.endsWith('.json'))
      .map((name) => name.replace(/\.json$/, ''))
      .sort()
    expect(files).toEqual([...LOCALES].sort())
  })

  for (const locale of LOCALES) {
    it(`${locale}: Community note and apply help are conditional, not a current verified license`, () => {
      const bundle = load(locale)
      expect(bundle.license.communityNote).toBe(expected[locale].communityNote)
      expect(bundle.license.helpBody).toContain(expected[locale].helpCommunity)
      for (const leftover of leftoverShown) {
        expect(
          bundle.license.communityNote,
          `${locale} communityNote asserts a shown verified license`,
        ).not.toMatch(leftover)
        expect(
          bundle.license.helpBody,
          `${locale} helpBody asserts a shown verified license`,
        ).not.toMatch(leftover)
      }
    })
  }
})
