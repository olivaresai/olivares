// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// CLC-R1/R2: unknown must not deny a failed/unavailable read, and Community copy
// must not assert a currently verified license beside No license.
import { readdirSync, readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'

const DIR = 'src/features/console/i18n'
const LOCALES = ['en', 'es', 'de', 'fr', 'ja', 'zh', 'ru'] as const

type LocaleFile = {
  license: { helpBody: string; communityNote: string }
  entitlement: { threeQuestions: string }
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
    threeQuestions:
      'Each column uses its own current information. Unknown means there is not enough information to determine that status. Use the status and retry controls to refresh unavailable information.',
  },
  es: {
    communityNote:
      'Edición Community: aplicar una licencia verificada muestra sus datos aquí; no cambia esta compilación.',
    helpCommunity:
      'En Community, aplicar una licencia verificada muestra sus datos aquí; no cambia esta compilación.',
    threeQuestions:
      'Cada columna usa su propia información actual. «No se sabe» significa que no hay información suficiente para determinar ese estado. Use el estado y «Reintentar» para actualizar la información no disponible.',
  },
  de: {
    communityNote:
      'Community-Edition: das Anwenden einer verifizierten Lizenz zeigt deren Angaben hier; es ändert diesen Build nicht.',
    helpCommunity:
      'In der Community-Edition zeigt das Anwenden einer verifizierten Lizenz deren Angaben hier; es ändert diesen Build nicht.',
    threeQuestions:
      'Jede Spalte verwendet ihre eigenen aktuellen Angaben. „Nicht bekannt“ bedeutet, dass nicht genug Informationen vorliegen, um diesen Status zu bestimmen. Nutzen Sie die Statusanzeige und «Erneut versuchen», um nicht verfügbare Angaben erneut abzurufen.',
  },
  fr: {
    communityNote:
      'Édition Community : appliquer une licence vérifiée en affiche les détails ici ; cela ne modifie pas cette compilation.',
    helpCommunity:
      'En Community, appliquer une licence vérifiée en affiche les détails ici ; cela ne modifie pas cette compilation.',
    threeQuestions:
      "Chaque colonne utilise ses propres informations actuelles. « Non connu » signifie qu'il n'y a pas assez d'informations pour déterminer cet état. Utilisez l'état et « Réessayer » pour actualiser les informations indisponibles.",
  },
  ja: {
    communityNote:
      'Community エディション: 検証済みライセンスを適用すると、その詳細がここに表示されます。このビルドは変わりません。',
    helpCommunity:
      'Community では、検証済みライセンスを適用すると、その詳細がここに表示されます。このビルドは変わりません。',
    threeQuestions:
      '各列はそれぞれ現在の情報を使用します。「不明」は、その状態を判断できる情報が足りないことを意味します。状態と再試行の操作で、利用できない情報を再取得してください。',
  },
  zh: {
    communityNote:
      'Community 版本：应用已验证的许可证会在此显示其详情；这不会改变此构建。',
    helpCommunity:
      '在 Community 中，应用已验证的许可证会在此显示其详情；这不会改变此构建。',
    threeQuestions:
      '每一列使用各自的当前信息。「未知」表示没有足够的信息来确定该状态。使用状态和重试控件刷新不可用的信息。',
  },
  ru: {
    communityNote:
      'Редакция Community: применение проверенной лицензии показывает её сведения здесь; это не меняет эту сборку.',
    helpCommunity:
      'В Community применение проверенной лицензии показывает её сведения здесь; это не меняет эту сборку.',
    threeQuestions:
      'Каждый столбец использует свои текущие данные. «Неизвестно» означает, что информации недостаточно, чтобы определить это состояние. Используйте состояние и «Повторить», чтобы обновить недоступные данные.',
  },
} as const

const leftoverUnknown = [
  /not a loading failure/i,
  /no es un fallo de carga/i,
  /kein Ladefehler/i,
  /pas un échec de chargement/i,
  /ce n'est pas un échec de chargement/i,
  /読み込み失敗でもありません/,
  /也不是加载失败/,
  /не сбой загрузки/,
]

const leftoverShown = [
  /a verified license is shown/i,
  /se muestra una licencia verificada/i,
  /una licencia verificada se muestra aquí/i,
  /eine verifizierte Lizenz wird angezeigt/i,
  /wird eine verifizierte Lizenz hier angezeigt/i,
  /une licence vérifiée s'affiche/i,
  /検証済みライセンスは表示され/,
  /显示已验证的许可证/,
  /已验证的许可证会显示在此/,
  /проверенная лицензия показывается/,
]

describe('license/add-on copy honesty (CLC-R1, CLC-R2)', () => {
  it('covers the seven shipped console locales and no others', () => {
    const files = readdirSync(DIR)
      .filter((name) => name.endsWith('.json'))
      .map((name) => name.replace(/\.json$/, ''))
      .sort()
    expect(files).toEqual([...LOCALES].sort())
  })

  for (const locale of LOCALES) {
    it(`${locale}: unknown copy names insufficient evidence, not a denied load failure`, () => {
      const bundle = load(locale)
      const text = bundle.entitlement.threeQuestions
      expect(text).toBe(expected[locale].threeQuestions)
      for (const leftover of leftoverUnknown) {
        expect(text, `${locale} still denies a loading failure`).not.toMatch(
          leftover,
        )
      }
    })

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
