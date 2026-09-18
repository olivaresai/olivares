// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { ThemeToggle } from './theme-toggle'
import { Wordmark } from './brand'
import { DeploymentIdentity } from './deployment-identity'

/** Shell for the unauthenticated surfaces (login, first-boot setup, invitations):
 * a two-column page — what this deployment IS on the left, the one focused card on
 * the right — with a corner theme toggle and the deployment's own identity in the
 * footer.
 *
 * ⛔ WHY IT IS TWO COLUMNS NOW, and it is a measured defect, not taste.
 *    Captured at 1600 px on 2026-09-17: the login screen was a 360 px card centred in a
 *    1600 px viewport, and the LONGEST element on the page was an amber panel explaining
 *    that passkeys do not work on an IP address. The first thing a new operator read was
 *    a limitation. A review recorded the same thing about the screen behind it —
 *    "three panes, all filled" against "a card grid over an empty lower half".
 *
 *    The left column is NOT decoration and it is NOT a marketing claim: it says what
 *    the product is and what it guarantees, in three sentences that the engine can be
 *    held to (self-hosted, hash-chained audit, one plane for providers and agents).
 *
 * ⛔ AND IT IS NOT A LANDMARK. `<aside>` would add a `complementary` landmark to the
 *    ONE screen a screen-reader user wants to leave immediately, and the at:gate
 *    landmark inventory would change on every unauthenticated route at once. The
 *    statement is rendered as text with display type, not as a heading, so `<h1>` on
 *    these screens stays exactly one — the card's — and the heading order is unmoved.
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
  const { t } = useTranslation(['common', 'auth'])
  const promises = [
    t('auth:shell.selfHosted'),
    t('auth:shell.audited'),
    t('auth:shell.onePlane'),
  ]
  return (
    <div className="relative flex min-h-svh flex-col bg-background">
      <a
        href="#main-content"
        onClick={() => {
          document.getElementById('main-content')?.focus()
        }}
        className="sr-only z-50 rounded-md bg-accent px-3 py-2 text-body font-medium text-accent-foreground outline-none focus-visible:not-sr-only focus-visible:absolute focus-visible:left-2 focus-visible:top-2 focus-visible:ring-2 focus-visible:ring-ring"
      >
        {t('common:a11y.skipToContent')}
      </a>
      <div className="absolute top-4 right-4 z-10">
        <ThemeToggle />
      </div>
      <div className="mx-auto flex w-full max-w-5xl flex-1 flex-col justify-center gap-10 px-6 py-14 lg:grid lg:grid-cols-[minmax(0,1fr)_26rem] lg:items-center lg:gap-16">
        {/* The statement column. Hidden below lg, where the card IS the page and a
            scrolling preamble above a password box helps nobody. */}
        <div className="hidden flex-col gap-6 lg:flex">
          <Wordmark />
          <p className="max-w-lg font-display text-display-lg text-foreground">
            {t('auth:shell.headline')}
          </p>
          <ul className="flex max-w-lg flex-col gap-3">
            {promises.map((line) => (
              <li
                key={line}
                className="flex items-start gap-3 text-body text-muted-foreground"
              >
                <span
                  aria-hidden
                  className="mt-[0.45rem] size-1.5 shrink-0 rounded-full bg-accent"
                />
                {line}
              </li>
            ))}
          </ul>
        </div>
        <div className="flex w-full flex-col items-center gap-6 lg:items-stretch">
          <Wordmark className="lg:hidden" />
          <main
            id="main-content"
            tabIndex={-1}
            className="w-full max-w-md outline-none"
          >
            {children}
          </main>
        </div>
      </div>
      {/* WHICH DEPLOYMENT AM I SIGNING INTO. The console keeps the scope of the next
          action on screen at all times, and on the signed-out screens that scope is the
          deployment itself. */}
      <DeploymentIdentity className="pb-6 text-center" />
    </div>
  )
}
