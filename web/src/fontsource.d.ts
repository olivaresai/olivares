// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// @fontsource-variable/* packages ship CSS (with bundled woff2) and no .d.ts, so
// TypeScript needs ambient module declarations for the side-effect imports in
// main.tsx. Vite resolves them at build time and bundles the fonts same-origin.
declare module '@fontsource-variable/inter'
declare module '@fontsource-variable/jetbrains-mono'
declare module '@fontsource-variable/space-grotesk'
