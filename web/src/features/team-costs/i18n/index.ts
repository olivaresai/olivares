// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

import { registerTranslations } from '@/lib/i18n'
import de from './de.json'
import en from './en.json'
import es from './es.json'
import fr from './fr.json'
import ja from './ja.json'
import ru from './ru.json'
import zh from './zh.json'

registerTranslations('team-costs', { en, es, zh, ja, de, ru, fr })

export { en, es, zh, ja, de, ru, fr }
