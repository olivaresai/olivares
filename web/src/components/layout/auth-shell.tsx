// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { ComponentProps, ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { cn } from '@/lib/utils'
import { ThemeToggle } from './theme-toggle'
import { Wordmark } from './brand'

/** Shell for the unauthenticated surfaces (login, first-boot setup, invitations):
 * the product's lockup, then the page's one small sheet (`AuthSheet`), centred
 * in one column on the frame ground, with a corner theme toggle.
 *
 * ONE COLUMN. The page is the form: the lockup names the product, the sheet
 * does the one job, and nothing stands beside it (the slogan column that used
 * to sit on the left is gone). The lockup is the product's own mark and traced
 * name, set larger than in the sidebar; it holds no control, so the tab order
 * stays skip link, theme toggle, then the form.
 *
 * On a phone the column keeps the page's 16 px gutter, so the sheet runs edge
 * to edge inside it; the column grows with its content and the page scrolls,
 * so a long step (the second-factor enrolment) never slides under the lockup.
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
  const { t } = useTranslation('common')
  return (
    <div className="relative flex min-h-svh flex-col bg-frame">
      <a
        href="#main-content"
        onClick={() => {
          document.getElementById('main-content')?.focus()
        }}
        className="sr-only z-50 rounded-md outline-none focus-visible:not-sr-only focus-visible:absolute focus-visible:left-2 focus-visible:top-2 focus-visible:bg-accent focus-visible:px-3 focus-visible:py-2 focus-visible:text-body focus-visible:font-medium focus-visible:text-accent-foreground focus-visible:ring-2 focus-visible:ring-ring"
      >
        {t('common:a11y.skipToContent')}
      </a>
      <div className="absolute top-4 right-4 z-10">
        <ThemeToggle />
      </div>
      <main
        id="main-content"
        tabIndex={-1}
        className="flex flex-1 flex-col outline-none"
      >
        <div className="mx-auto flex w-full max-w-[27.5rem] flex-1 flex-col justify-center gap-7 px-4 py-14">
          <Wordmark className="gap-3 self-center [&>svg:first-child]:size-[2.125rem] [&>svg:last-child]:h-[1.1875rem]" />
          {children}
        </div>
      </main>
    </div>
  )
}

/** The one sheet of a public page: 408 px of the canvas surface, raised from
 * the frame by its shadow and drawn with no outline, holding the page's h1, its
 * sentence and its form at an even 18 px rhythm. */
export function AuthSheet({ className, ...props }: ComponentProps<'div'>) {
  return (
    <div
      data-slot="auth-sheet"
      className={cn(
        'flex w-full flex-col gap-4.5 rounded-panel bg-canvas p-6 shadow-[var(--shadow-raised)] sm:p-8',
        className,
      )}
      {...props}
    />
  )
}
