// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// no-raw-palette — forbid raw Tailwind palette classes under features/.
//
// The console's colors are DTCG design tokens (web/tokens → src/styles/tokens.css,
// ADM-CORE-04): semantic classes like text-success / bg-danger-soft / border-warning
// resolve per theme AND are what the AT contrast gate (e2e-visual/at-run.ts)
// measures. A raw `text-emerald-600 dark:text-emerald-400` bypasses both — it
// won't follow a token retune and its contrast is never checked. Six files had
// drifted onto the raw palette when this rule landed (see the allowlist in
// eslint.config.js).
//
// The rule flags any string/template chunk containing a Tailwind utility bound to
// a stock palette color with a numeric shade (e.g. bg-amber-500/15, including
// variant prefixes like dark: or hover:). Semantic token classes never carry a
// numeric shade, so they can't false-positive here.

const PALETTE =
  'red|orange|amber|yellow|lime|green|emerald|teal|cyan|sky|blue|indigo|violet|purple|fuchsia|pink|rose|slate|gray|zinc|neutral|stone'
const UTILITY =
  'bg|text|border|ring|outline|fill|stroke|from|via|to|divide|decoration|accent|caret|shadow|placeholder'
const RAW_PALETTE = new RegExp(
  `(?:^|[^\\w-])(?:[\\w-]+:)*(?:${UTILITY})(?:-[a-z]+)?-(?:${PALETTE})-\\d{2,3}\\b`,
  'g',
)

/** @type {import('eslint').Rule.RuleModule} */
export default {
  meta: {
    type: 'problem',
    docs: {
      description:
        'Forbid raw Tailwind palette classes (emerald-500, amber-400, …) in favor of the semantic design tokens',
    },
    schema: [],
    messages: {
      rawPalette:
        'Raw Tailwind palette class "{{cls}}" bypasses the design tokens and the AT contrast gate — use a semantic token class (text-success, bg-danger-soft, border-warning, …) from src/styles/tokens.css instead.',
    },
  },
  create(context) {
    function check(node, text) {
      if (typeof text !== 'string' || !text.includes('-')) return
      for (const m of text.matchAll(RAW_PALETTE)) {
        context.report({
          node,
          messageId: 'rawPalette',
          data: { cls: m[0].replace(/^[^\w-]/, '') },
        })
      }
    }
    return {
      Literal(node) {
        check(node, node.value)
      },
      TemplateElement(node) {
        check(node, node.value.raw)
      },
    }
  },
}
