// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Registers the `work` i18n namespace. Imported by the view (so the strings are
// present when the lazy chunk loads) and by the tests. All seven languages from day one.
//
// ⚠ THE ONE TRANSLATION THAT MUST NOT DRIFT. `verdict.NO_HE_PODIDO_MIRAR` means "the
// engine could not complete the observation". It is NOT "no results", NOT "none found"
// and NOT "correct". A previous pass on governed-data documentation inverted a
// deny-closed behaviour in five pages while every mechanical lint stayed green — the
// same class of defect, and mechanical lints cannot see it here either.
// i18n.test.ts pins the polarity of this key in all seven languages.
import { registerTranslations } from '@/lib/i18n'
import en from './en.json'
import es from './es.json'
import zh from './zh.json'
import ja from './ja.json'
import de from './de.json'
import ru from './ru.json'
import fr from './fr.json'

registerTranslations('work', { en, es, zh, ja, de, ru, fr })

export { en, es, zh, ja, de, ru, fr }
