// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// no-raw-palette — forbid raw colors in the console source.
//
// The console's colors are DTCG design tokens (web/tokens → src/styles/tokens.css,
// ADM-CORE-04): semantic classes like text-success / bg-danger-soft / border-warning
// resolve per theme (Light, Dark, Black) AND are what the contrast tests measure. A raw
// color bypasses both: it paints the same in every theme, so a grey stays grey on the
// black theme, and its contrast is never checked.
//
// The rule flags, in any string or template chunk:
//   - a Tailwind utility bound to a stock palette color with a numeric shade
//     (bg-amber-500/15, dark:text-emerald-400). Token classes never carry a shade.
//   - a Tailwind arbitrary value that is a color (bg-[#5b8def], border-[rgba(…)],
//     text-[oklch(…)]). Arbitrary sizes, grids and var() values are not colors, and a
//     color function over a token (color-mix(in oklab, var(--text) 12%, …)) follows it.
//   - a Tailwind arbitrary value that is a named color (text-[red], bg-[tomato]/50).
//   - a string that is nothing but a color ('#141414', 'rgb(0 0 0 / .5)'), as in a style
//     prop. Prose that mentions "#578" is not a color string, so it passes.
//   - a color among the parts of a style shorthand (border: '1px solid #ccc', boxShadow:
//     '0 0 0 1px #000', outline: '2px dotted tomato'), unless it is computed over a token.
//   - Tailwind's white and black (bg-white, bg-black/20), and a CSS named color ('white',
//     'tomato') where a color goes: a color style property or an SVG color attribute. A
//     bare 'black' elsewhere is a word (the Black theme is stored as "black"), not a color.
// The files whose colors must stay literal are named in eslint.config.js, each with its
// reason.

const PALETTE =
  'red|orange|amber|yellow|lime|green|emerald|teal|cyan|sky|blue|indigo|violet|purple|fuchsia|pink|rose|slate|gray|zinc|neutral|stone'
const UTILITY =
  'bg|text|border|ring|outline|fill|stroke|from|via|to|divide|decoration|accent|caret|shadow|placeholder'
const HEX = '#(?:[0-9a-fA-F]{8}|[0-9a-fA-F]{6}|[0-9a-fA-F]{3,4})'
const COLOR_FN = 'rgba?|hsla?|oklch|oklab|lab|lch|hwb|color|color-mix'
const RAW_PALETTE = new RegExp(
  `(?:^|[^\\w-])(?:[\\w-]+:)*(?:${UTILITY})(?:-[a-z]+)?-(?:${PALETTE})-\\d{2,3}\\b`,
  'g',
)
const RAW_ARBITRARY = new RegExp(
  `(?:^|[^\\w-])((?:[\\w-]+:)*(?:${UTILITY})(?:-[a-z]+)?-\\[(?:${HEX}|(?:${COLOR_FN})\\()[^\\]\\s]*\\])`,
  'g',
)
const RAW_VALUE = new RegExp(`^\\s*(?:${HEX}|(?:${COLOR_FN})\\([^)]*\\))\\s*$`)
const RAW_WHITE_BLACK = new RegExp(
  `(?:^|[^\\w-])(?:[\\w-]+:)*(?:${UTILITY})(?:-[a-z]+)?-(?:white|black)(?:\\/\\d+)?(?![\\w-])`,
  'g',
)
// A Tailwind arbitrary value that is one word, checked against NAMED (text-[red]).
const RAW_ARBITRARY_WORD = new RegExp(
  `(?:^|[^\\w-])((?:[\\w-]+:)*(?:${UTILITY})(?:-[a-z]+)?-\\[([a-zA-Z]+)\\])`,
  'g',
)
// The style properties and SVG attributes whose value is a color.
const COLOR_KEY =
  /^(?:color|background|backgroundColor|border(?:Top|Right|Bottom|Left|Block|Inline)?(?:Start|End)?Color|outlineColor|caretColor|accentColor|textDecorationColor|columnRuleColor|fill|stroke|stopColor|floodColor|lightingColor)$/
// The style shorthands whose value carries a color among other parts ('1px solid #ccc').
const COMPOUND_KEY =
  /^(?:background|border(?:Top|Right|Bottom|Left|Block|Inline)?(?:Start|End)?|outline|boxShadow|textShadow|columnRule|textDecoration)$/
const HEX_PART = new RegExp(`(?:^|[\\s,(])${HEX}(?![\\w-])`)
// A color function, unless it is computed over a token: color-mix(in oklab, var(--x) …).
const COLOR_FN_PART = new RegExp(`\\b(?:${COLOR_FN})\\(([^)]*)\\)`, 'g')
// CSS Color 4 named colors.
const NAMED = new Set(
  'aliceblue antiquewhite aqua aquamarine azure beige bisque black blanchedalmond blue blueviolet brown burlywood cadetblue chartreuse chocolate coral cornflowerblue cornsilk crimson cyan darkblue darkcyan darkgoldenrod darkgray darkgreen darkgrey darkkhaki darkmagenta darkolivegreen darkorange darkorchid darkred darksalmon darkseagreen darkslateblue darkslategray darkslategrey darkturquoise darkviolet deeppink deepskyblue dimgray dimgrey dodgerblue firebrick floralwhite forestgreen fuchsia gainsboro ghostwhite gold goldenrod gray green greenyellow grey honeydew hotpink indianred indigo ivory khaki lavender lavenderblush lawngreen lemonchiffon lightblue lightcoral lightcyan lightgoldenrodyellow lightgray lightgreen lightgrey lightpink lightsalmon lightseagreen lightskyblue lightslategray lightslategrey lightsteelblue lightyellow lime limegreen linen magenta maroon mediumaquamarine mediumblue mediumorchid mediumpurple mediumseagreen mediumslateblue mediumspringgreen mediumturquoise mediumvioletred midnightblue mintcream mistyrose moccasin navajowhite navy oldlace olive olivedrab orange orangered orchid palegoldenrod palegreen paleturquoise palevioletred papayawhip peachpuff peru pink plum powderblue purple rebeccapurple red rosybrown royalblue saddlebrown salmon sandybrown seagreen seashell sienna silver skyblue slateblue slategray slategrey snow springgreen steelblue tan teal thistle tomato turquoise violet wheat white whitesmoke yellow yellowgreen'.split(
    ' ',
  ),
)

/** @type {import('eslint').Rule.RuleModule} */
export default {
  meta: {
    type: 'problem',
    docs: {
      description:
        'Forbid raw colors (palette classes, arbitrary color values, color strings) in favor of the semantic design tokens',
    },
    schema: [],
    messages: {
      rawPalette:
        'Raw Tailwind palette class "{{cls}}" bypasses the design tokens and the AT contrast gate — use a semantic token class (text-success, bg-danger-soft, border-warning, …) from src/styles/tokens.css instead.',
      rawColor:
        'Raw color "{{cls}}" bypasses the design tokens: it paints the same in Light, Dark and Black and the contrast tests never measure it — use a token class (bg-surface, text-danger, border-line, …) or var(--color-…) from src/styles/tokens.css instead.',
    },
  },
  create(context) {
    function check(node, text) {
      if (typeof text !== 'string') return
      for (const m of text.matchAll(RAW_PALETTE)) {
        context.report({
          node,
          messageId: 'rawPalette',
          data: { cls: m[0].replace(/^[^\w-]/, '') },
        })
      }
      for (const m of text.matchAll(RAW_WHITE_BLACK)) {
        context.report({
          node,
          messageId: 'rawPalette',
          data: { cls: m[0].replace(/^[^\w-]/, '') },
        })
      }
      for (const m of text.matchAll(RAW_ARBITRARY)) {
        if (m[1].includes('var(')) continue
        context.report({ node, messageId: 'rawColor', data: { cls: m[1] } })
      }
      for (const m of text.matchAll(RAW_ARBITRARY_WORD)) {
        if (NAMED.has(m[2].toLowerCase()))
          context.report({ node, messageId: 'rawColor', data: { cls: m[1] } })
      }
      if (RAW_VALUE.test(text) && !text.includes('var(')) {
        context.report({
          node,
          messageId: 'rawColor',
          data: { cls: text.trim() },
        })
      }
    }
    // The strings a value can take: a literal, a template (whole when it has no
    // expressions, else its parts), and each branch of a JSX expression container, a
    // conditional or a logical expression.
    function leaves(value) {
      switch (value?.type) {
        case 'Literal':
          return typeof value.value === 'string'
            ? [{ node: value, text: value.value, whole: true }]
            : []
        case 'TemplateLiteral':
          return value.quasis.map((q) => ({
            node: value,
            text: q.value.cooked ?? '',
            whole: value.expressions.length === 0,
          }))
        case 'JSXExpressionContainer':
          return leaves(value.expression)
        case 'ConditionalExpression':
          return [...leaves(value.consequent), ...leaves(value.alternate)]
        case 'LogicalExpression':
          return [...leaves(value.left), ...leaves(value.right)]
        default:
          return []
      }
    }
    function checkNamed(key, value) {
      if (!COLOR_KEY.test(key ?? '')) return
      for (const { node, text, whole } of leaves(value))
        if (whole && NAMED.has(text.trim().toLowerCase()))
          context.report({ node, messageId: 'rawColor', data: { cls: text } })
    }
    // A color among the parts of a shorthand ('1px solid #ccc', '0 1px hsl(…)', '2px dotted
    // tomato'), reported once per string. A string that is one color alone is left to
    // check() and checkNamed(), which already report it.
    function checkCompound(key, value) {
      if (!COMPOUND_KEY.test(key ?? '')) return
      for (const { node, text, whole } of leaves(value)) {
        if (RAW_VALUE.test(text)) continue
        const words = text.toLowerCase().split(/[\s,()/]+/)
        if (whole && words.length === 1 && COLOR_KEY.test(key)) continue
        const raw =
          HEX_PART.test(text) ||
          [...text.matchAll(COLOR_FN_PART)].some(
            (m) => !m[0].includes('var('),
          ) ||
          words.some((w) => NAMED.has(w))
        if (raw)
          context.report({ node, messageId: 'rawColor', data: { cls: text } })
      }
    }
    return {
      Literal(node) {
        check(node, node.value)
      },
      TemplateElement(node) {
        check(node, node.value.raw)
      },
      Property(node) {
        const key = node.key.name ?? node.key.value
        checkNamed(key, node.value)
        checkCompound(key, node.value)
      },
      JSXAttribute(node) {
        checkNamed(node.name.name, node.value)
      },
    }
  },
}
