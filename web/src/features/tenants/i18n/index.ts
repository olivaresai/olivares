// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Registra el namespace `tenants` (C07-02). Lo importa la vista —para que las cadenas estén cuando
// carga su chunk perezoso— y los tests.
//
// ⚠ El import va en la VISTA, no sólo aquí: `check-i18n-namespaces.mjs` existe porque la paridad y
// la resolución de claves salían verdes mientras un botón mostraba su propia clave — el bundle no
// estaba cargado en el chunk que lo renderiza.
import { registerTranslations } from '@/lib/i18n'
import en from './en.json'
import es from './es.json'
import zh from './zh.json'
import ja from './ja.json'
import de from './de.json'
import ru from './ru.json'
import fr from './fr.json'

registerTranslations('tenants', { en, es, zh, ja, de, ru, fr })

export { en, es, zh, ja, de, ru, fr }
