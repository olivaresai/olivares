// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// ⛔ POR QUÉ EXISTE. La revisión de capturas del 2026-08-31 reportó «1 open incidents»
// y «1 agents». No eran cadenas sueltas: i18next, cuando falta la forma de la
// categoría que toca, cae a `_other`, así que el defecto es de CLASE y silencioso.
//
// ⛔⛔ Y POR QUÉ ESTÁ ESCRITO ASÍ, que es la corrección: mi PRIMERA versión exigía
// sólo `_one` y recorría un único nivel de directorios. El contraste `sol max`
// (F-01, ALTA) la desmontó con dos medidas:
//
//   · exigir `_one` NO es el invariante. Las categorías cardinales son las que CLDR
//     da a cada locale: en/de {one,other}; es/fr {one,many,other}; ru
//     {one,few,many,other}; ja/zh {other}. Con la regla vieja, el ruso podía
//     —y de hecho lo hacía— renderizar mal `few` y `many` con el test EN VERDE.
//   · el barrido veía `features/<x>/i18n/<loc>.json` y NADA MÁS, así que
//     `features/automations/workflows/i18n/` quedaba fuera. Ahí estaba la familia
//     rota.
//
// Ahora la lista de categorías la da `Intl.PluralRules` —la misma fuente que usa el
// navegador— en vez de una lista escrita a mano que envejece con CLDR, y el barrido
// es RECURSIVO. Una regla que se escribe a mano acaba describiendo lo que su autor
// recordaba, no lo que el idioma pide.
import { describe, expect, it } from 'vitest'
import fs from 'node:fs'
import path from 'node:path'

const LOCALES = ['en', 'es', 'de', 'fr', 'ru', 'ja', 'zh'] as const

/** Todos los `<algo>/i18n/<loc>.json` bajo `src`, a cualquier profundidad. */
function recursos(loc: string): string[] {
  const out: string[] = []
  const anda = (dir: string) => {
    for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
      const p = path.join(dir, e.name)
      if (e.isDirectory()) anda(p)
      else if (e.name === `${loc}.json` && path.basename(dir) === 'i18n')
        out.push(p)
    }
  }
  anda('src')
  return out
}

function hojas(o: unknown, p = ''): string[] {
  if (typeof o === 'string') return [p]
  if (o && typeof o === 'object')
    return Object.entries(o as Record<string, unknown>).flatMap(([k, v]) =>
      hojas(v, p ? `${p}.${k}` : k),
    )
  return []
}

describe('P11 — un contador tiene TODAS las formas que su idioma pide', () => {
  for (const loc of LOCALES) {
    it(`${loc}: ninguna familia plural deja una categoría CLDR sin cubrir`, () => {
      const categorias = new Intl.PluralRules(loc, {
        type: 'cardinal',
      }).resolvedOptions().pluralCategories
      const ficheros = recursos(loc)
      // No-vacuidad, en las DOS direcciones: si el barrido deja de encontrar
      // ficheros, o si deja de encontrar familias plurales, el caso pasaría sin
      // comparar nada — que es exactamente cómo la versión anterior salía verde.
      expect(ficheros.length).toBeGreaterThan(30)

      let familias = 0
      const incompletas: string[] = []
      for (const f of ficheros) {
        const claves = new Set(hojas(JSON.parse(fs.readFileSync(f, 'utf8'))))
        const bases = new Set(
          [...claves]
            .filter((k) => k.endsWith('_other'))
            .map((k) => k.slice(0, -'_other'.length)),
        )
        for (const base of bases) {
          familias++
          const faltan = categorias.filter((c) => !claves.has(`${base}_${c}`))
          if (faltan.length)
            incompletas.push(`${f}:${base} — falta ${faltan.join(', ')}`)
        }
      }
      expect(
        familias,
        'el barrido no encontró familias plurales',
      ).toBeGreaterThan(5)
      expect(
        incompletas,
        `estas familias caerán a «other» en la categoría que falta:\n  ${incompletas.join('\n  ')}`,
      ).toEqual([])
    })
  }
})
