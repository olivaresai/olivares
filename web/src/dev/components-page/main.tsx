// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Entry of the capture-only components page. The address picks what the capture needs:
// `?theme=dark|light` (dark by default, as the mockups), `?lang=en|de|ja` and any other
// console language for the components' own words, `?view=states|primitives`, and
// `?motion=reduce` for the console's Reduce motion setting.
import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import '@/index.css'
import i18n from '@/lib/i18n'
import { ComponentsPageApp } from './components-page'
import { demoFor } from './demo-text'

const query = new URLSearchParams(window.location.search)
const theme = query.get('theme') === 'light' ? 'light' : 'dark'
const lang = query.get('lang') ?? 'en'
const html = document.documentElement
html.classList.toggle('dark', theme === 'dark')
html.dataset.theme = theme
html.lang = lang
if (query.get('motion') === 'reduce') html.dataset.motion = 'reduce'
void i18n.changeLanguage(lang)

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <ComponentsPageApp
      d={demoFor(lang)}
      initialView={query.get('view') === 'primitives' ? 'primitives' : 'states'}
    />
  </StrictMode>,
)
