// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// ⛔ POR QUÉ EXISTE, y por qué NO es un barrido. Curé «0 stalled» en rojo y publiqué
// que era el ÚNICO, apoyado en un `grep` del literal `variant: 'danger' as const`.
// El contraste `sol max` (F-02, MEDIA) hizo un análisis AST sobre los 360 `.tsx` y
// encontró CINCO más, con la prueba de que el cero es alcanzable tomada del BACKEND:
//
//   · agentcore-export: un plan que sólo crea deja Updates y Deletes vacíos
//     (`modules/governance/agentcoreexport_route_test.go:47-57`);
//   · SLA: `modules/health/sla.go:44-69` inicializa downtime y degraded a cero y
//     sólo incrementa el del estado recorrido — una ventana sana llega con dos ceros;
//   · red-team: las corridas `degraded`/`error` llegan con `failed: 0`.
//
// ⛔⛔ Y POR QUÉ ESTE FICHERO NO BARRE EL ÁRBOL: lo intenté. Un regex que busque «un
// color de alarma constante junto a un contador» marcó DIEZ sitios, y al menos dos
// eran correctos — un badge que sólo se RENDERIZA cuando hay algo que avisar no
// necesita condicionar su color. Distinguir «siempre rojo» de «sólo aparece si hay
// causa» exige alcanzabilidad, no texto. Un gate con diez falsos positivos el primer
// día lo desactiva el siguiente, así que **este caso fija los SEIS sitios medidos y
// declara que el barrido general está pendiente**, en vez de fingir cobertura.
import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'

/** Los seis sitios, con el contador que puede valer cero. */
const SITIOS: { fichero: string; contador: string }[] = [
  {
    fichero: 'src/features/automations/automations-view.tsx',
    contador: 'stalledCount',
  },
  {
    fichero: 'src/features/governance/agentcore-export-view.tsx',
    contador: '(plan.Updates ?? []).length',
  },
  {
    fichero: 'src/features/governance/agentcore-export-view.tsx',
    contador: '(plan.Deletes ?? []).length',
  },
  {
    fichero: 'src/features/health/health-view.tsx',
    contador: 'data.downtime_seconds',
  },
  {
    fichero: 'src/features/health/health-view.tsx',
    contador: 'data.degraded_seconds',
  },
  { fichero: 'src/features/redteam/components.tsx', contador: 'failed' },
]

describe('P11 — los seis contadores medidos condicionan su color de alarma', () => {
  for (const { fichero, contador } of SITIOS) {
    it(`${fichero.split('/').slice(-1)[0]}: ${contador} apaga la alarma a cero`, () => {
      const src = readFileSync(fichero, 'utf8')
      // No-vacuidad: si el contador desaparece o se renombra, este caso falla y se
      // relee, en vez de pasar sobre un fichero que ya no dice lo que creía.
      expect(src, `${contador} ya no está en ${fichero}`).toContain(contador)
      // Comprobación LITERAL a propósito: la primera versión construía un regex
      // y su escapado se comía los paréntesis de `(plan.Updates ?? []).length`,
      // así que dos casos fallaban por el instrumento y no por el sujeto.
      const guardas = [`${contador} > 0`, `${contador}\n`]
      expect(
        src.includes(guardas[0]!),
        `${contador} se pinta de alarma sin comprobar que sea > 0`,
      ).toBe(true)
    })
  }
})
