// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// La espera por CONTENIDO del arnés de accesibilidad, en su propio módulo para que se pueda
// testificar. Vive fuera de `at-run.ts` porque ese fichero es un GUION: llama a `main()` al
// cargarse, así que importarlo desde una spec arrancaría la corrida entera.
//
// ⛔ POR QUÉ EXISTE. `at-run.ts` esperaba 1500 ms FIJOS tras un `goto` con `domcontentloaded`. Una
// vista que no había renderizado a esa altura salía con `main` ausente o vacío, y con eso
// alimentaba `sinMain` y `noH1`, que BLOQUEAN. Es decir: el gate salía rojo por el reloj. Probado
// por the orchestrator — entre dos corridas del mismo árbol el rojo se MUDÓ de `/finops` a
// `/attestation`. Un veredicto que cambia de sujeto sin que cambie el sujeto no mide el sujeto.

import type { Page } from '@playwright/test'

// Tope de la espera. Generoso a propósito: su trabajo NO es acelerar la corrida sino separar «la
// vista tardó» de «la vista no tiene encabezado». Si fuera corto volveríamos a decidir por reloj.
export const AT_CONTENIDO_MS = 15_000

/**
 * ¿Se puede juzgar esta ruta?
 *
 * Espera a que aparezca un encabezado dentro de `main`. Si el tope vence NO se declara nada por
 * reloj: se distinguen dos cosas que se parecen y no son la misma.
 *
 *   · `main` EXISTE y tiene contenido, pero sin encabezado ⇒ **medible**. Es un hallazgo REAL
 *     (`no-h1`) y se mide como siempre. Sin esta rama, esperar por el encabezado enmascararía
 *     justo el defecto que el arnés existe para encontrar: cambiaríamos un falso rojo por un
 *     falso verde, que es peor.
 *   · `main` AUSENTE o VACÍO ⇒ **no medible**. La vista no llegó a renderizar: NO PUDE MIRAR.
 *     Ni rojo ni verde — fuera de las listas que bloquean y NOMBRADA en el informe, con la misma
 *     regla que este arnés ya aplica a `crashed`: trece rutas sin medir no son trece limpias.
 */
export async function esperaContenido(
  page: Page,
  timeoutMs: number = AT_CONTENIDO_MS,
): Promise<boolean> {
  try {
    await page.waitForSelector('main :is(h1, [role="heading"])', {
      state: 'attached',
      timeout: timeoutMs,
    })
    return true
  } catch {
    const estado = await page
      .evaluate(() => {
        const m = document.querySelector('main')
        return { hayMain: !!m, hijos: m ? m.querySelectorAll('*').length : 0 }
      })
      .catch(() => ({ hayMain: false, hijos: 0 }))
    return estado.hayMain && estado.hijos > 0
  }
}

// Tope de la espera por ANIMACIONES. Corto a propósito: con `reducedMotion: 'reduce'` puesto en el
// contexto casi nunca hay nada que esperar, así que esto es el cinturón para lo que NO esté guardado
// por `prefers-reduced-motion`, no el mecanismo principal.
export const AT_ANIMACIONES_MS = 3_000

/**
 * ¿Está la página QUIETA, o la estoy fotografiando a medio movimiento?
 *
 * ⛔ POR QUÉ EXISTE, y es un caso medido, no una precaución. El 2026-08-29 el contexto REQUERIDO
 * `a11y` dio veredictos OPUESTOS sobre árboles sin diferencia renderizable: `33263227272` falló con
 * `axe /dashboards:color-contrast` y `33268462913` salió limpio. El artefacto nombró al culpable —
 * la insignia `danger` (`components/ui/badge.tsx`), `#ef867f` sobre `#44393c`, **4,42** contra 4,5.
 *
 * Ese `#ef867f` NO es el token: es el token **compuesto al 97,4 %**. axe midió el elemento a medio
 * fundido. `.animate-enter` (index.css) va de `opacity: 0` a `1` en **240 ms** y la vista Executive
 * escalona seis secciones hasta **200 ms**, mientras `esperaContenido` devuelve en cuanto hay un
 * encabezado dentro de `main` — o sea con las secciones aún entrando. **Dónde cae la muestra lo
 * decidía la CARGA de la máquina.**
 *
 * Y por qué ese par y no otro: en reposo medía **4,56**, AA por 0,06, así que cumplía sólo con la
 * animación al **99,1 %**. Cualquier muestra mínimamente temprana salía roja. El defecto no era del
 * gate: era un umbral rozado y un instante sin fijar.
 *
 * ⚠ Las animaciones SIN FIN (un spinner) no se asientan nunca y NO se esperan: colgarse de ellas
 * cambiaría un rojo intermitente por un `cancelled` fijo, que se lee como flake y es peor.
 *
 * Devuelve `false` si vence el tope. No es un veredicto: es el aviso de que la medida siguiente
 * puede describir un fotograma y no la superficie — la misma regla de «no pude mirar» que este
 * módulo ya aplica al contenido.
 */
export async function esperaAnimaciones(
  page: Page,
  timeoutMs: number = AT_ANIMACIONES_MS,
): Promise<boolean> {
  try {
    await page.waitForFunction(
      () =>
        document
          .getAnimations()
          .filter((a) => a.effect?.getComputedTiming().iterations !== Infinity)
          .every((a) => a.playState === 'finished' || a.playState === 'idle'),
      undefined,
      { timeout: timeoutMs, polling: 50 },
    )
    return true
  } catch {
    return false
  }
}
