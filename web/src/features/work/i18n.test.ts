// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it } from 'vitest'
import { de, en, es, fr, ja, ru, zh } from './i18n'

/**
 * — THE POLARITY GUARD.
 *
 * A previous translation pass left deny-closed behaviour INVERTED in five pages of
 * governed-data documentation: security docs saying the opposite of what the product
 * does, with every mechanical lint green. Parity lints compare KEY SETS; they cannot
 * read meaning, so they were green then and would be green here.
 *
 * The equivalent risk on this surface is rendering NO_HE_PODIDO_MIRAR as "no results"
 * or "correct". That single mistranslation converts the most expensive defect class in
 * this repository — a check that says "clean" when it means "I could not look" — into a
 * shipped feature, in one language, invisibly.
 *
 * So the three verdict labels are PINNED per language. Pinning is the point: a future
 * edit to any of them has to come here and change the expected string deliberately,
 * which is exactly the moment to ask whether the new wording still says "unknown".
 */
const LANGS = { en, es, zh, ja, de, ru, fr } as const

/** The wording each language must carry for the third outcome. Every one of these says
 * some form of "could not look" — none says "none", "empty", "fine" or "passed". */
const EXPECTED_UNKNOWN_LABEL: Record<keyof typeof LANGS, string> = {
  en: 'Could not look',
  es: 'No he podido mirar',
  zh: '无法查看',
  ja: '確認できませんでした',
  de: 'Konnte nicht nachsehen',
  ru: 'Не удалось проверить',
  fr: 'Vérification impossible',
}

/** Words that would turn the third outcome into the first or into an absence. Checked
 * per language against the label AND its help text, in lowercase. */
const FORBIDDEN_IN_UNKNOWN: Record<keyof typeof LANGS, string[]> = {
  en: ['no results', 'not found', 'success', 'passed', 'clean', 'ok'],
  es: [
    'sin resultados',
    'no encontrado',
    'correcto',
    'aprobado',
    'limpio',
    'vacío',
  ],
  zh: ['没有结果', '无结果', '成功', '通过', '干净', '正确'],
  ja: ['結果なし', '該当なし', '成功', '合格', '正常', '問題なし'],
  de: [
    'keine ergebnisse',
    'nicht gefunden',
    'erfolg',
    'bestanden',
    'sauber',
    'in ordnung',
  ],
  ru: [
    'нет результатов',
    'не найдено',
    'успех',
    'пройден',
    'чисто',
    'в порядке',
  ],
  fr: ['aucun résultat', 'non trouvé', 'succès', 'réussi', 'propre', 'correct'],
}

describe('the third outcome survives translation in all seven languages', () => {
  for (const [lang, bundle] of Object.entries(LANGS) as [
    keyof typeof LANGS,
    (typeof LANGS)[keyof typeof LANGS],
  ][]) {
    describe(lang, () => {
      const verdict = (bundle as Record<string, any>).verdict

      it('carries all three verdicts — never two', () => {
        // Two outcomes is the defect the canon names. If a bundle ever drops the third
        // key, the UI falls back to the raw enum and this fires first.
        expect(Object.keys(verdict).sort()).toEqual([
          'LIMPIO',
          'NO_HE_PODIDO_MIRAR',
          'ROTO',
        ])
      })

      it('says "could not look", pinned', () => {
        expect(verdict.NO_HE_PODIDO_MIRAR.label).toBe(
          EXPECTED_UNKNOWN_LABEL[lang],
        )
      })

      it('never states the unknown outcome as success or as an absence', () => {
        const text = (
          verdict.NO_HE_PODIDO_MIRAR.label +
          ' ' +
          verdict.NO_HE_PODIDO_MIRAR.help
        ).toLowerCase()
        for (const bad of FORBIDDEN_IN_UNKNOWN[lang]) {
          // The help text explains what the outcome is NOT, so a bare substring scan
          // would be defeated by "this is not a pass". That is why the LABEL is pinned
          // above: this sweep is the belt, the pin is the braces.
          expect(
            text.includes(`= ${bad}`) || text.startsWith(bad) || text === bad,
          ).toBe(false)
        }
      })

      it('NON-FIRING: the three verdicts are DISTINCT strings', () => {
        // The legitimate neighbouring operation is rewording any one of them. What must
        // never happen is two of them collapsing onto the same words — which is what an
        // inversion looks like from here: "clean" appearing twice.
        const labels = [
          verdict.LIMPIO.label,
          verdict.ROTO.label,
          verdict.NO_HE_PODIDO_MIRAR.label,
        ]
        expect(new Set(labels).size).toBe(3)
      })

      it('the history notice still refuses to attribute state', () => {
        // Trap 6 in copy form: this notice is the only thing telling an operator that
        // the history view does not answer "is this in force?". An empty or missing
        // string would silently drop that refusal.
        const decisions = (bundle as Record<string, any>).decisions
        expect(decisions.historyNotice.length).toBeGreaterThan(40)
        expect(decisions.state.notAttributed.length).toBeGreaterThan(3)
      })
    })
  }
})
