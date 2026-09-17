// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Registers the `providers` i18n namespace (D19). Imported by the view, by the
// onboarding step that renders provider strings, and by the tests — a namespace a
// chunk renders and does not register is how a wizard came to print a raw key on a
// button it told the operator to press.
import { registerTranslations } from '@/lib/i18n'
import en from './en.json'
import es from './es.json'
import zh from './zh.json'
import ja from './ja.json'
import de from './de.json'
import ru from './ru.json'
import fr from './fr.json'

registerTranslations('providers', { en, es, zh, ja, de, ru, fr })

export { en, es, zh, ja, de, ru, fr }
