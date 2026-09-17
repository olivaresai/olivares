// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// site-locales — the ONE machine-readable declaration of which locales the docs
// site publishes and which version snapshots are archived.
//
// WHY THIS FILE EXISTS. The docs-parity gate and the ADR publisher both
// need this list. The first version of the gate recovered it by text-scanning
// `astro.config.mjs` (brace balancing over JavaScript source) and the ADR
// publisher simply hardcoded six locale strings. An adversarial review broke
// the scan with FIVE legal
// Astro configurations — a quoted key such as Starlight's own documented
// `'zh-cn'`, a `}` inside a legal label string, an unrelated `locales:` object
// appearing first, a nested `more.it` map, an unrelated `versions:` array — and
// every one of them failed the same way: a NON-EMPTY, WRONG locale set, so the
// checker reported zero findings while checking the wrong thing. A silent green
// is the one outcome a parity gate must never produce.
//
// So there is no second parser. `astro.config.mjs` imports these values, and so
// does `scripts/check-docs-parity.mjs`. Plain
// ESM with no dependencies, so it imports inside the Go-toolchain push gate where
// no `pnpm install` has run.
//
// Adding a locale: add it to LOCALES here. Nothing else needs to change — the
// site, the parity gate and the ADR register all pick it up, and the parity gate
// starts demanding its pages immediately.

/**
 * Starlight's `locales` map. `root` is English (defaultLocale: 'root'); it is not
 * a directory, so it is excluded from PUBLISHED_LOCALES below.
 */
export const LOCALES = {
  root: { label: 'English', lang: 'en' },
  es: { label: 'Español', lang: 'es' },
  zh: { label: '简体中文', lang: 'zh-CN' },
  ru: { label: 'Русский', lang: 'ru' },
  ja: { label: '日本語', lang: 'ja' },
  de: { label: 'Deutsch', lang: 'de' },
  fr: { label: 'Français', lang: 'fr' },
}

/** The locale DIRECTORY names under src/content/docs — every locale but English. */
export const PUBLISHED_LOCALES = Object.keys(LOCALES).filter((l) => l !== 'root')

/**
 * starlight-versions' archived snapshots. These are English-authored point-in-time
 * archives; the plugin also copies each locale into `<locale>/<slug>/` when it cuts
 * one, so BOTH `<slug>/` and `<locale>/<slug>/` are outside current-content parity.
 */
export const VERSIONS = [{ slug: '2026-06', label: '2026-06 (pre-1.0 preview)' }]

/** Just the slugs — the directory names the parity gate must skip. */
export const ARCHIVED_SLUGS = VERSIONS.map((v) => v.slug)

/**
 * Las clases de Diátaxis que TIENEN tarjeta social propia en `public/og/<clase>.png`.
 *
 * ⛔ VIVE AQUÍ, y no en el componente, por la misma razón que `LOCALES`: la puerta
 * (`scripts/check-structured-data.mjs`) y el `<Head>` tienen que leer LA MISMA declaración, no cada
 * uno la suya. Un segundo parser sobre el componente es exactamente lo que la cabecera de este
 * fichero documenta como el fallo que produce un verde en silencio.
 *
 * Añadir una clase son DOS cosas en el mismo commit: el PNG en `public/og/` y su nombre aquí. La
 * puerta comprueba que las dos coinciden en las dos direcciones, así que ni una tarjeta sin
 * declarar ni una declaración sin tarjeta pueden colarse.
 *
 * VACÍA A PROPÓSITO: hoy las 2.156 páginas comparten una sola tarjeta. Las imágenes por clase las
 * produce el carril de marca, que es quien tiene la plantilla paramétrica y decide cómo se ven; el
 * cableado ya está probado en las dos direcciones y se enciende solo cuando lleguen.
 */
export const OG_CLASS_CARDS = []
