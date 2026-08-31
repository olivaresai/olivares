// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { ReactNode } from 'react'
import { ThemeToggle } from './theme-toggle'
import { Wordmark } from './brand'

/** Centered shell for the unauthenticated surfaces (login, first-boot setup):
 * brand lockup, a single focused card, and a corner theme toggle.
 *
 * ⛔ POR QUÉ EL CONTENIDO VA EN `<main>` Y NO EN UN `<div>` — medido el 2026-08-18 al capturar estas
 *    pantallas para la documentación pública: `/login` y `/accept-invite` renderizaban **sin ninguna
 *    región `main`**, y `<main id="main-content">` existe únicamente en `app-layout.tsx`, que es el
 *    layout AUTENTICADO. Es decir: la app entera cumple el bypass de bloques (WCAG 2.4.1) salvo
 *    justo en las pantallas que ve un cliente ANTES de tener cuenta.
 *
 *    Y hay un detalle que lo hace peor de lo que suena: las páginas de ERROR —`not-found.tsx`,
 *    `route-error.tsx`— sí traen su `<main>`. La estructura de landmarks estaba puesta en todas
 *    partes menos en la puerta de entrada, así que un lector de pantalla que navega por landmarks no
 *    encontraba nada que saltar precisamente donde hay un formulario que rellenar.
 *
 * ⚠ El `id` es el MISMO que el del layout autenticado a propósito. No hay dos pantallas a la vez, y
 *   que el ancla se llame igual en las dos mitades del producto es lo que permite que el enlace de
 *   salto —hoy sólo en el layout autenticado— valga aquí el día que se añada, sin un segundo nombre. */
export function AuthShell({ children }: { children: ReactNode }) {
  return (
    <div className="relative flex min-h-svh flex-col items-center justify-center gap-7 bg-background px-4 py-12">
      <div className="absolute top-4 right-4">
        <ThemeToggle />
      </div>
      <Wordmark />
      <main
        id="main-content"
        tabIndex={-1}
        className="w-full max-w-sm outline-none"
      >
        {children}
      </main>
    </div>
  )
}
