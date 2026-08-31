// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// ⛔ EL DEFECTO, de la revisión de capturas del 2026-08-31: la descripción de la
// página de tenants decía «Nothing is deleted here» y en ESA MISMA página hay un
// botón **Delete** que purga el tenant y sus datos de inmediato, sin ventana de
// recuperación (`i18n/en.json:deleteDescription`).
//
// No es maquetación: es una afirmación FALSA en el producto, y la clase más grave de
// las que la revisión encontró. Un operador que la lea y pulse Delete confiando en
// ella pierde datos. Corregido en los SIETE locales.
import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'

const en = JSON.parse(
  readFileSync('src/features/tenants/i18n/en.json', 'utf8'),
) as Record<string, string>

describe('P11 — la página de tenants no niega lo que hace', () => {
  it('si ofrece borrar, su descripción no puede negar el borrado', () => {
    // No-vacuidad: el caso sólo significa algo mientras la página SIGA ofreciendo
    // borrar. Si eso cambia, que falle y se relea, en vez de pasar por vacío.
    expect(en.delete, 'la página ya no ofrece borrar; revisa este caso').toBe(
      'Delete',
    )
    expect(en.deleteDescription).toMatch(/cannot be undone/i)

    expect(
      en.description,
      `la descripción niega un borrado que la página SÍ hace: «${en.description}»`,
    ).not.toMatch(/nothing is deleted here/i)
  })
})
