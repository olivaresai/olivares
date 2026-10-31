// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// no-primitive-restyle (#3135): a primitive from components/ui is restyled only through its
// variants.
//
// A primitive decides its own colour, size and padding, and offers variants for the choices a
// screen may make. `<Button className="bg-accent px-2">` is a second decision about the same
// thing; the console drifts one such class at a time, and a retune of the primitive then
// misses the sites that overrode it. The rule flags a JSX element whose tag is bound by an
// import from `@/components/ui/...` and whose `className` carries a colour, size or padding
// utility. Layout (width, flex, gap, margin, truncation, alignment) is the call site's own
// business and is left alone.
//
// THE BASELINE IS A COUNT, AND A COUNT CAN ONLY GO DOWN. The sites that restyled a primitive
// before this rule are listed per file with how many (`primitive-restyle-baseline.json`, passed
// in as the `baseline` option). A file over its count is reported at the site beyond it; a
// file UNDER its count is reported too, with the number to lower it to, so the allowance
// cannot sit above what the file does. A row at 0 is deleted. A count cannot tell which site
// it is: fixing one site and adding another in the same file leaves the count where it was.
//
// ponytail: the scan reads class strings written at the element (literals, template text and
// the strings inside cn()/clsx()/ternaries); a class set through a variable declared elsewhere
// is not seen. Add constant following when a site is found that hides behind one.
import { relative, resolve, sep } from 'node:path'

const UI_SOURCE = '@/components/ui/'

// `text-*` is a colour or a size; these are neither.
const TEXT_LAYOUT =
  /^text-(?:left|right|center|justify|start|end|wrap|nowrap|balance|pretty|ellipsis|clip|shadow-.*)$/
// `bg-*` is a colour unless it positions, sizes or clips the background.
const BG_SETTING =
  /^bg-(?:clip|origin|repeat|no-repeat|cover|contain|auto|fixed|local|scroll|blend|bottom|center|left|right|top)(?:-|$)/
// `border-*`, `ring-*` and `stroke-*` are colours unless they are a width, a side or a style.
const BORDER_NOT_COLOUR =
  /^border-(?:[trblxyse](?:-(?:\d+|\[.*\]))?|\d+|solid|dashed|dotted|double|none|hidden|collapse|separate|spacing-.*|\[\d.*\])$/
const RING_NOT_COLOUR = /^ring-(?:\d+|inset|offset-(?:\d+|\[\d.*\])|\[\d.*\])$/
const STROKE_NOT_COLOUR = /^stroke-(?:\d+|\[\d.*\])$/

/** The utility of a class token: variants (`hover:`, `[&_svg]:`, `data-[x=y]:`) and the
 * important marker dropped. A `:` inside brackets belongs to the variant's argument. */
function utilityOf(token) {
  let depth = 0
  let cut = -1
  for (let i = 0; i < token.length; i++) {
    const c = token[i]
    if (c === '[' || c === '(') depth++
    else if (c === ']' || c === ')') depth--
    else if (c === ':' && depth === 0) cut = i
  }
  let utility = token.slice(cut + 1)
  if (utility.startsWith('!')) utility = utility.slice(1)
  if (utility.endsWith('!')) utility = utility.slice(0, -1)
  return utility
}

function restylesColourSizeOrPadding(utility, boxSize) {
  if (/^p[xytrblse]?-/.test(utility)) return true // padding
  if (
    boxSize &&
    /^(?:size|h)-/.test(utility) &&
    !/^(?:size|h)-(?:full|auto)$/.test(utility)
  )
    return true // size of the box; full and auto are layout
  if (utility.startsWith('text-')) return !TEXT_LAYOUT.test(utility) // colour or size
  if (utility.startsWith('bg-')) return !BG_SETTING.test(utility)
  if (utility.startsWith('border-')) return !BORDER_NOT_COLOUR.test(utility)
  if (utility.startsWith('ring-')) return !RING_NOT_COLOUR.test(utility)
  if (utility.startsWith('stroke-')) return !STROKE_NOT_COLOUR.test(utility)
  return utility.startsWith('fill-')
}

/**
 * The class tokens of `className` that set a colour, a size or a padding. `boxSize: false`
 * leaves the height and `size-*` of the box alone, for a primitive the caller sizes by design.
 */
export function restyleClasses(className, { boxSize = true } = {}) {
  return className
    .split(/\s+/)
    .filter(Boolean)
    .filter((token) => restylesColourSizeOrPadding(utilityOf(token), boxSize))
}

// A placeholder is sized by the caller to mirror the content it stands for: skeleton.tsx says
// "size it with width/height utilities", and 89 of the 358 sites the rule first counted were a
// Skeleton with nothing but a height. Its colour and padding are still the primitive's.
const SIZED_BY_CALLER = new Set(['Skeleton'])

/** Every string written inside an expression: literals, template text, object keys. */
function stringsIn(node, out = []) {
  if (!node || typeof node !== 'object') return out
  if (node.type === 'Literal' && typeof node.value === 'string') {
    out.push(node.value)
    return out
  }
  if (node.type === 'TemplateElement') {
    out.push(node.value.cooked ?? node.value.raw)
    return out
  }
  for (const [key, value] of Object.entries(node)) {
    if (key === 'parent' || key === 'loc' || key === 'range') continue
    if (Array.isArray(value)) for (const v of value) stringsIn(v, out)
    else if (value && typeof value === 'object' && value.type)
      stringsIn(value, out)
  }
  return out
}

/** `Button` -> {root:'Button', full:'Button'}; `Dialog.Content` -> {root:'Dialog', full:'Dialog.Content'}. */
function tagName(name) {
  if (name.type === 'JSXIdentifier') return { root: name.name, full: name.name }
  if (name.type === 'JSXMemberExpression') {
    const parts = []
    let n = name
    while (n.type === 'JSXMemberExpression') {
      parts.unshift(n.property.name)
      n = n.object
    }
    if (n.type !== 'JSXIdentifier') return null
    parts.unshift(n.name)
    return { root: n.name, full: parts.join('.') }
  }
  return null
}

/** @type {import('eslint').Rule.RuleModule} */
export default {
  meta: {
    type: 'problem',
    docs: {
      description:
        'A primitive from components/ui is restyled only through its variants: no colour, size or padding utility in its className',
    },
    schema: [
      {
        type: 'object',
        properties: {
          baseline: {
            type: 'object',
            additionalProperties: { type: 'integer', minimum: 1 },
          },
        },
        additionalProperties: false,
      },
    ],
    messages: {
      restyle:
        '<{{name}}> sets its own colour, size and padding; {{classes}} restyles it from the call site. Use one of its variants, or add the variant to the primitive in components/ui.',
      baselineStale:
        'eslint-rules/primitive-restyle-baseline.json allows {{allowed}} restyled primitives in this file and {{found}} remain: lower the number to {{found}} (delete the row at 0).',
    },
  },
  create(context) {
    const baseline = context.options[0]?.baseline ?? {}
    const file = relative(context.cwd, resolve(context.cwd, context.filename))
      .split(sep)
      .join('/')
    const allowed = Object.hasOwn(baseline, file) ? baseline[file] : 0
    const primitives = new Set()
    let found = 0

    return {
      ImportDeclaration(node) {
        if (node.importKind === 'type') return
        if (!String(node.source.value).startsWith(UI_SOURCE)) return
        for (const specifier of node.specifiers)
          if (specifier.importKind !== 'type')
            primitives.add(specifier.local.name)
      },
      JSXOpeningElement(node) {
        const tag = tagName(node.name)
        if (!tag || !primitives.has(tag.root)) return
        const attribute = node.attributes.find(
          (a) => a.type === 'JSXAttribute' && a.name.name === 'className',
        )
        if (!attribute?.value) return
        const classes = restyleClasses(stringsIn(attribute.value).join(' '), {
          boxSize: !SIZED_BY_CALLER.has(tag.full),
        })
        if (classes.length === 0) return
        found++
        if (found > allowed)
          context.report({
            node,
            messageId: 'restyle',
            data: { name: tag.full, classes: classes.join(', ') },
          })
      },
      'Program:exit'(node) {
        if (found < allowed)
          context.report({
            node,
            messageId: 'baselineStale',
            data: { allowed: String(allowed), found: String(found) },
          })
      },
    }
  },
}
