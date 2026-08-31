// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Charts are SVG, but our palette lives in CSS variables (the brand tokens, switched
// by the `.dark` class on <html>). Recharts wants concrete color strings, and a
// theme switch must RE-RESOLVE them at runtime. useChartTheme reads the
// design tokens via getComputedStyle and recomputes whenever the theme class
// changes — so every chart tracks light/dark without a remount, using the SAME
// tokens as the rest of the console (never a raw hex).

import { useEffect, useState } from 'react'

export interface ChartTheme {
  /** Primary tick / label text. */
  text: string
  /** Quieter axis text. */
  mutedText: string
  /** Gridlines + axis lines (hairline). */
  grid: string
  /** Brand copper — the one accented series / primary trend. */
  accent: string
  success: string
  warning: string
  danger: string
  info: string
  /** Confidence teal (attributed) — also the cool categorical anchor. */
  teal: string
  /** Confidence slate (approximate / neutral category). */
  slate: string
  surface: string
  elevated: string
  border: string
  /** A distinguishable, on-brand categorical ramp for multi-series charts. */
  series: string[]
}

// Fallbacks for environments with no layout engine (jsdom in unit tests): the dark
// operator palette. In a real browser getComputedStyle wins; here we stay truthful
// instead of handing Recharts empty strings.
export const FALLBACK: ChartTheme = {
  text: '#fafaf9',
  mutedText: '#aaaab3',
  grid: '#3a3a40',
  accent: '#f08000',
  success: '#86c58a',
  warning: '#e7b65a',
  danger: '#f7928b',
  info: '#6fb6e6',
  teal: '#5be0d8',
  slate: '#9aa3b0',
  surface: '#2f2f33',
  elevated: '#38383d',
  border: '#3a3a40',
  series: ['#f08000', '#5be0d8', '#6fb6e6', '#e7b65a', '#86c58a', '#9c9ca3'],
}

function readVar(
  style: CSSStyleDeclaration,
  name: string,
  fallback: string,
): string {
  const v = style.getPropertyValue(name).trim()
  return v || fallback
}

function resolveTheme(): ChartTheme {
  if (typeof window === 'undefined' || typeof document === 'undefined') {
    return FALLBACK
  }
  const s = getComputedStyle(document.documentElement)
  const accent = readVar(s, '--accent-text', FALLBACK.accent)
  const info = readVar(s, '--info', FALLBACK.info)
  const teal = readVar(s, '--confidence-attributed', FALLBACK.teal)
  const warning = readVar(s, '--warning', FALLBACK.warning)
  const success = readVar(s, '--success', FALLBACK.success)
  const slate = readVar(s, '--confidence-approximate', FALLBACK.slate)
  return {
    text: readVar(s, '--foreground', FALLBACK.text),
    mutedText: readVar(s, '--muted-foreground', FALLBACK.mutedText),
    grid: readVar(s, '--border', FALLBACK.grid),
    accent,
    success,
    warning,
    danger: readVar(s, '--danger', FALLBACK.danger),
    info,
    teal,
    slate,
    surface: readVar(s, '--surface', FALLBACK.surface),
    elevated: readVar(s, '--elevated', FALLBACK.elevated),
    border: readVar(s, '--border', FALLBACK.border),
    // Cool-leaning, on-brand, non-semantic ordering so categories don't read as
    // status. Copper leads (the highlighted bucket), then the cool/neutral hues.
    series: [accent, teal, info, warning, success, slate],
  }
}

/** Resolve the chart palette from the live CSS tokens, re-resolving on theme flip. */
export function useChartTheme(): ChartTheme {
  const [theme, setTheme] = useState<ChartTheme>(resolveTheme)

  useEffect(() => {
    if (typeof document === 'undefined') return
    // The only signal that matters for light/dark is the class on <html> (applied
    // pre-paint by the no-FOUC bootstrap and toggled by the theme store).
    const observer = new MutationObserver(() => setTheme(resolveTheme()))
    observer.observe(document.documentElement, {
      attributes: true,
      attributeFilter: ['class'],
    })
    // The lazy useState initializer already resolved the palette for the first
    // paint (the no-FOUC bootstrap set the theme class before React mounted), so
    // the observer only needs to handle subsequent light/dark flips.
    return () => observer.disconnect()
  }, [])

  return theme
}
