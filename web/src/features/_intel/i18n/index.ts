// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Registers the shared `intel` i18n namespace — the common vocabulary of the
// intelligence layer (severity/outcome/verdict badges, honesty notices, hash chips).
// It lives here, in the same shape every feature uses, rather than inline in the
// barrel: a module that translates must be able to import the namespace WITHOUT
// importing the barrel that re-exports it (that would be a cycle), and a deep import
// such as `@/features/_intel/notices` must carry its own strings — it did not, and
// the capabilities chunk rendered `intel.*` keys raw.
import { registerTranslations } from '@/lib/i18n'
import en from './en.json'
import es from './es.json'
import zh from './zh.json'
import ja from './ja.json'
import de from './de.json'
import ru from './ru.json'
import fr from './fr.json'

registerTranslations('intel', { en, es, zh, ja, de, ru, fr })

export { en, es, zh, ja, de, ru, fr }
