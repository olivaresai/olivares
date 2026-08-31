// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Registers the `rateLimits` i18n namespace. Imported by the view (so the
// strings are present when the lazy chunk loads) and by the tests. Languages:
// en/es since zh/ja/de/ru/fr added in (console multilingual).
import { registerTranslations } from '@/lib/i18n'
import en from './en.json'
import es from './es.json'
import zh from './zh.json'
import ja from './ja.json'
import de from './de.json'
import ru from './ru.json'
import fr from './fr.json'

registerTranslations('rateLimits', { en, es, zh, ja, de, ru, fr })

export { en, es, zh, ja, de, ru, fr }
