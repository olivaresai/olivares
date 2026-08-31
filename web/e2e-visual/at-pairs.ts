// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// DERIVE the contrast pairs the AT gate measures from the console's own
// source, instead of listing them by hand.
//
// WHY THIS EXISTS. `at-run.ts` used to measure a hand-written array of token
// pairings. A composition that nobody had thought to add did not exist for the
// gate, and its green did not say so: a selected trace row painted `bg-accent`
// with `text-muted-foreground` inside measured 1.17:1 and the gate passed. This
// is the same defect `console:walk` fixed for routes — it discovers them from the
// app's own navigation "rather than from a hand-written list that goes stale the
// day a screen is added" (Taskfile.yml) — brought to the accessibility gate.
//
// WHAT IT DOES NOT DUPLICATE. axe-core already measures the contrast of every
// text node that is RENDERED during the walk, on every route, in both themes. It
// is structurally blind to one thing: a composition that only exists in a STATE
// the walk never reaches. In this codebase the state lives in the JSX condition
// (`selected ? 'bg-accent' : …`), not in a CSS state selector, so no amount of
// DOM poking makes it render — the class is simply never emitted. That blind
// spot is exactly what this module covers, by reading the conditional itself.
//
// HOW. Parse each .tsx with the TypeScript compiler API (a real parser: a regex
// scan goes blind after a generic type argument, `<Comp<T> …>`), walk the JSX
// tree keeping a stack of the colour utilities each element sets, and pair every
// foreground with the nearest enclosing background. Guards — the printed text of
// the ternary/`&&` condition, and the variant prefix (`hover:`,
// `data-[state=checked]:`) — travel with each class, so two classes that can
// never be true at the same time are never paired. What it cannot resolve
// (a className built from a variable, a helper call) is COUNTED and reported, so
// "derived" never reads as "exhaustive".
import { readFileSync, readdirSync, statSync } from 'node:fs'
import { dirname, join, relative } from 'node:path'
import { fileURLToPath } from 'node:url'
import ts from 'typescript'

/** Origins are printed repo-relative so a failure is clickable from anywhere —
 *  `process.cwd()` is whatever directory the gate happened to be launched from. */
const repoRoot = join(dirname(fileURLToPath(import.meta.url)), '..', '..')

/** A condition that must hold for a class to be painted. Two atoms are only
 *  paired when they agree on every guard id they share. */
export type Guard = { id: string; val: string }

export type ClassAtom = {
  /** as written, e.g. `hover:bg-muted/60` */
  cls: string
  /** variant prefixes stripped, e.g. `bg-muted/60` */
  base: string
  variants: string[]
  guards: Guard[]
  file: string
  line: number
}

export type DerivedPair = {
  name: string
  kind: 'text'
  /** the utility class, not a token name: it is resolved against the SHIPPED
   *  stylesheet, so alpha (`/60`), `color-mix` and arbitrary values are exact. */
  fgClass: string
  bgClass: string
  /** what the background is painted ON, when the background is translucent */
  underClass: string
  /** true when the foreground is the inherited `body` colour, not a class */
  fgInherited: boolean
  origin: string
}

export type DeriveResult = {
  pairs: DerivedPair[]
  /** className expressions no parser can resolve to literal classes */
  unresolved: { file: string; line: number; expr: string }[]
  filesScanned: number
  elementsScanned: number
  /** colour utilities on childless elements: icons and canvases, 1.4.11 not 1.4.3 */
  nonTextForegrounds: number
}

const CLASS_ATTRS = new Set(['className', 'class'])
// NOT `cva`: a cva result is always CALLED (`badgeVariants({ variant })`), so the
// className expression is that call, whose callee is an ordinary identifier — it
// lands in `unresolved` and is COUNTED, which is the honest answer. The first
// draft carried a whole variant-map branch for it that could never execute (the
// callee of `cva(...)(...)` is itself a CallExpression) and that, when forced,
// paired variants which exclude each other because it attached no guard.
const CLASS_HELPERS = new Set(['cn', 'clsx', 'classNames', 'twMerge', 'twJoin'])

/** Tailwind `text-*` utilities that are NOT colours. */
const TEXT_NON_COLOUR =
  /^text-(xs|sm|base|lg|xl|\d?xl|left|center|right|justify|start|end|wrap|nowrap|balance|pretty|ellipsis|clip|top|bottom|middle|super|sub|\[[\d.]+(rem|px|em)\]|\[length:)/

/** `text-current`/`text-inherit` do not CHOOSE a colour, they keep the inherited
 *  one; treating them as a colour of their own measures the wrong pair (a
 *  checkbox tick is `text-current` and inherits `accent-foreground`, not the
 *  document default). `text-transparent` paints no text at all — usually a
 *  background-clip technique — and has no contrast to measure. */
const TEXT_INHERITS = /^text-(current|inherit)$/
const TEXT_INVISIBLE = /^text-transparent$/

function isColourText(base: string) {
  return (
    base.startsWith('text-') &&
    !TEXT_NON_COLOUR.test(base) &&
    !TEXT_INHERITS.test(base) &&
    !TEXT_INVISIBLE.test(base)
  )
}
/** Variants that give the declaration a box of its OWN: a `before:bg-accent` rail
 *  paints a pseudo-element, and the element's text never sits on it. As a
 *  BACKGROUND they invent a composition that cannot occur. (`placeholder:`,
 *  `marker:`, `selection:` and `first-letter:` are NOT in this set: those DO
 *  render text inside the element, so as foregrounds they are real.) */
const SEPARATE_BOX_VARIANT = /^(before|after|backdrop|file)$/

function paintsOwnBox(atom: { variants: string[] }) {
  return !atom.variants.some((v) => SEPARATE_BOX_VARIANT.test(v))
}

function isBackground(base: string) {
  // `bg-none`/`bg-gradient-*`/`bg-cover` etc. are not solid colours.
  return (
    base.startsWith('bg-') &&
    !/^bg-(none|transparent|gradient|linear|radial|conic|cover|contain|center|top|bottom|left|right|repeat|no-repeat|fixed|local|scroll|origin|clip|blend|\[url)/.test(
      base,
    )
  )
}

/** `hover:data-[state=open]:bg-x` → variants ['hover','data-[state=open]'], base 'bg-x'.
 *  Splits on `:` only outside `[]`, so arbitrary variants survive intact. */
function splitVariants(cls: string): { variants: string[]; base: string } {
  const parts: string[] = []
  let depth = 0
  let cur = ''
  for (const ch of cls) {
    if (ch === '[' || ch === '(') depth++
    else if (ch === ']' || ch === ')') depth--
    if (ch === ':' && depth === 0) {
      parts.push(cur)
      cur = ''
      continue
    }
    cur += ch
  }
  parts.push(cur)
  const base = parts.pop() ?? ''
  return { variants: parts, base }
}

/** A variant becomes a guard so mutually exclusive states are never paired:
 *  `data-[state=checked]` and `data-[state=unchecked]` share an id, differ in value. */
function variantGuards(variants: string[]): Guard[] {
  return variants.map((v) => {
    const m = v.match(/^(data|aria)-\[([^=\]]+)=([^\]]*)\]$/)
    if (m) return { id: `${m[1]}-${m[2]}`, val: m[3] }
    const neg = v.match(/^(data|aria)-\[([^\]=]+)\]$/)
    if (neg) return { id: `${neg[1]}-${neg[2]}`, val: 'present' }
    return { id: `variant:${v}`, val: 'on' }
  })
}

function compatible(a: Guard[], b: Guard[]): boolean {
  for (const g of a) {
    for (const h of b) {
      if (g.id === h.id && g.val !== h.val) return false
    }
  }
  return true
}

function satisfies(atom: ClassAtom, g: Guard): boolean {
  return atom.guards.some((x) => x.id === g.id && x.val === g.val)
}

/** Is `atom` still the painted value when `paired` is painted? The cascade says
 *  no when a SIBLING class for the SAME property is scoped to a condition that
 *  `paired` turns on. It cuts both ways, and both halves are load-bearing:
 *
 *   - the sidebar's base `text-muted-foreground` loses to
 *     `data-[status=active]:text-foreground` the moment
 *     `data-[status=active]:bg-accent-soft` paints, so muted-on-soft never renders;
 *   - the checkbox's base `bg-surface` loses to `data-[state=checked]:bg-accent`
 *     the moment `data-[state=checked]:text-accent-foreground` paints, so
 *     accent-foreground-on-surface never renders either.
 *
 *  Without both, the derived set reports pairs the product cannot paint — the
 *  fastest way to make a gate ignorable. */
function shadowed(
  atom: ClassAtom,
  paired: ClassAtom,
  siblings: ClassAtom[],
): boolean {
  for (const g of paired.guards) {
    if (satisfies(atom, g)) continue // atom is already scoped to this condition
    for (const other of siblings) {
      if (other === atom) continue
      if (satisfies(other, g) && compatible(other.guards, paired.guards))
        return true
    }
  }
  return false
}

/** The `dark:` variant shadows its own base in the dark theme: with
 *  `text-red-600 dark:text-red-400`, only the 400 paints there. Measuring the
 *  600 against the dark palette reports a colour that screen never shows. */
function themeShadowed(
  atom: ClassAtom,
  siblings: ClassAtom[],
  theme: 'dark' | 'light',
): boolean {
  if (theme === 'light') return atom.variants.includes('dark')
  if (atom.variants.includes('dark')) return false
  return siblings.some(
    (o) =>
      o !== atom &&
      o.variants.includes('dark') &&
      compatible(dropDark(o.guards), atom.guards),
  )
}
const dropDark = (gs: Guard[]) => gs.filter((g) => g.id !== 'variant:dark')

function normaliseCond(node: ts.Node): string {
  return node.getText().replace(/\s+/g, ' ').trim().slice(0, 120)
}

/** Collect literal classes out of a className expression, carrying the conditions
 *  under which each one is painted. Anything not statically resolvable is pushed
 *  to `unresolved` rather than silently dropped. */
function collectClasses(
  node: ts.Node,
  guards: Guard[],
  sf: ts.SourceFile,
  file: string,
  out: ClassAtom[],
  unresolved: DeriveResult['unresolved'],
): void {
  const push = (text: string, n: ts.Node) => {
    const line = sf.getLineAndCharacterOfPosition(n.getStart(sf)).line + 1
    for (const raw of text.split(/\s+/)) {
      if (!raw) continue
      const { variants, base } = splitVariants(raw)
      out.push({
        cls: raw,
        base,
        variants,
        guards: [...guards, ...variantGuards(variants)],
        file,
        line,
      })
    }
  }

  if (ts.isStringLiteral(node) || ts.isNoSubstitutionTemplateLiteral(node)) {
    push(node.text, node)
    return
  }
  if (ts.isTemplateExpression(node)) {
    push(node.head.text, node)
    for (const span of node.templateSpans) {
      collectClasses(
        span.expression,
        guards,
        sf,
        file,
        out,
        unresolved,
      )
      push(span.literal.text, span.literal)
    }
    return
  }
  if (ts.isParenthesizedExpression(node)) {
    return collectClasses(
      node.expression,
      guards,
      sf,
      file,
      out,
      unresolved,
    )
  }
  if (ts.isConditionalExpression(node)) {
    const id = normaliseCond(node.condition)
    collectClasses(
      node.whenTrue,
      [...guards, { id, val: 'T' }],
      sf,
      file,
      out,
      unresolved,
    )
    collectClasses(
      node.whenFalse,
      [...guards, { id, val: 'F' }],
      sf,
      file,
      out,
      unresolved,
    )
    return
  }
  if (ts.isBinaryExpression(node)) {
    const k = node.operatorToken.kind
    if (k === ts.SyntaxKind.AmpersandAmpersandToken) {
      collectClasses(
        node.right,
        [...guards, { id: normaliseCond(node.left), val: 'T' }],
        sf,
        file,
        out,
        unresolved,
      )
      return
    }
    if (
      k === ts.SyntaxKind.BarBarToken ||
      k === ts.SyntaxKind.QuestionQuestionToken
    ) {
      // Either side can be the painted value; neither implies the other.
      collectClasses(node.left, guards, sf, file, out, unresolved)
      collectClasses(
        node.right,
        guards,
        sf,
        file,
        out,
        unresolved,
      )
      return
    }
    if (k === ts.SyntaxKind.PlusToken) {
      collectClasses(node.left, guards, sf, file, out, unresolved)
      collectClasses(
        node.right,
        guards,
        sf,
        file,
        out,
        unresolved,
      )
      return
    }
  }
  if (ts.isArrayLiteralExpression(node)) {
    for (const el of node.elements)
      collectClasses(el, guards, sf, file, out, unresolved)
    return
  }
  if (ts.isCallExpression(node)) {
    const callee = ts.isIdentifier(node.expression)
      ? node.expression.text
      : ts.isPropertyAccessExpression(node.expression)
        ? node.expression.name.text
        : ''
    if (CLASS_HELPERS.has(callee)) {
      for (const arg of node.arguments)
        collectClasses(arg, guards, sf, file, out, unresolved)
      return
    }
    // A call we cannot follow (a variant factory, a formatter): record the gap.
    unresolved.push({
      file,
      line: sf.getLineAndCharacterOfPosition(node.getStart(sf)).line + 1,
      expr: normaliseCond(node),
    })
    return
  }
  if (ts.isObjectLiteralExpression(node)) {
    for (const prop of node.properties) {
      if (ts.isPropertyAssignment(prop)) {
        const key =
          ts.isStringLiteral(prop.name) || ts.isIdentifier(prop.name)
            ? prop.name.text
            : ''
        if (key) {
          // clsx object form: the KEY is the class, the value is its condition.
          const id = normaliseCond(prop.initializer)
          const line =
            sf.getLineAndCharacterOfPosition(prop.getStart(sf)).line + 1
          for (const raw of key.split(/\s+/)) {
            if (!raw) continue
            const { variants, base } = splitVariants(raw)
            out.push({
              cls: raw,
              base,
              variants,
              guards: [...guards, { id, val: 'T' }, ...variantGuards(variants)],
              file,
              line,
            })
          }
        }
      } else if (ts.isSpreadAssignment(prop)) {
        collectClasses(
          prop.expression,
          guards,
          sf,
          file,
          out,
          unresolved,
        )
      }
    }
    return
  }
  if (
    node.kind === ts.SyntaxKind.NullKeyword ||
    node.kind === ts.SyntaxKind.UndefinedKeyword
  )
    return
  if (
    ts.isAsExpression(node) ||
    ts.isNonNullExpression(node) ||
    ts.isSatisfiesExpression(node)
  ) {
    return collectClasses(
      node.expression,
      guards,
      sf,
      file,
      out,
      unresolved,
    )
  }
  if (
    ts.isIdentifier(node) ||
    ts.isPropertyAccessExpression(node) ||
    ts.isElementAccessExpression(node)
  ) {
    unresolved.push({
      file,
      line: sf.getLineAndCharacterOfPosition(node.getStart(sf)).line + 1,
      expr: normaliseCond(node),
    })
    return
  }
}

/** Does this element forward an unknown className through a spread? 86 elements
 *  in `web/src` do. The walk cannot read it, and staying silent about it would
 *  make the residue count — the whole reason "derived" is not read as
 *  "exhaustive" — quietly incomplete. */
function hasSpreadAttr(
  el: ts.JsxOpeningElement | ts.JsxSelfClosingElement,
): boolean {
  return el.attributes.properties.some(ts.isJsxSpreadAttribute)
}

function classNameExpr(
  el: ts.JsxOpeningElement | ts.JsxSelfClosingElement,
): ts.Node | null {
  for (const attr of el.attributes.properties) {
    if (!ts.isJsxAttribute(attr) || !ts.isIdentifier(attr.name)) continue
    if (!CLASS_ATTRS.has(attr.name.text)) continue
    const init = attr.initializer
    if (!init) return null
    if (ts.isStringLiteral(init)) return init
    if (ts.isJsxExpression(init) && init.expression) return init.expression
  }
  return null
}

function containsJsx(node: ts.Node): boolean {
  if (
    ts.isJsxElement(node) ||
    ts.isJsxSelfClosingElement(node) ||
    ts.isJsxFragment(node)
  )
    return true
  return node.getChildren().some(containsJsx)
}

/** Does this element render text of its OWN — a literal, or an interpolation that
 *  yields text? An interpolation that yields elements (`{cond && <span/>}`,
 *  `{rows.map(r => <li/>)}`) renders no text here; counting it would invent a
 *  composition of this element's background with a colour it never paints. */
function hasOwnText(node: ts.JsxElement): boolean {
  return node.children.some(
    (c) =>
      (ts.isJsxText(c) && c.text.trim().length > 0) ||
      (ts.isJsxExpression(c) && !!c.expression && !containsJsx(c.expression)),
  )
}

/** Is there any text under this element that this element's COLOUR governs? The
 *  walk stops at a descendant that sets its own colour — that subtree is measured
 *  on its own terms. An element whose only child is an icon
 *  (`<div className="…text-success"><Check className="size-4"/></div>`) governs no
 *  text at all: 4.5:1 is the wrong threshold and the wrong success criterion. */
function governsText(
  node: ts.Node,
  setsOwnColour: (
    el: ts.JsxOpeningElement | ts.JsxSelfClosingElement,
  ) => boolean,
): boolean {
  if (ts.isJsxElement(node)) {
    if (hasOwnText(node)) return true
    return node.children.some((c) => {
      const child = ts.isJsxElement(c)
        ? c.openingElement
        : ts.isJsxSelfClosingElement(c)
          ? c
          : null
      if (child && setsOwnColour(child)) return false
      return governsText(c, setsOwnColour)
    })
  }
  if (ts.isJsxSelfClosingElement(node)) {
    // NOT every childless element is an icon. `<input>`/`<textarea>` render text
    // and carry their colour AND their background on the same element — the one
    // case that needs no cross-component reasoning, and the first draft skipped
    // every one of them (`input.tsx:26`, `textarea.tsx:19`).
    const tag = node.tagName.getText()
    return tag === 'input' || tag === 'textarea'
  }
  if (ts.isJsxFragment(node))
    return node.children.some((c) => governsText(c, setsOwnColour))
  if (ts.isJsxText(node)) return node.text.trim().length > 0
  if (ts.isJsxExpression(node))
    return !!node.expression && !containsJsx(node.expression)
  return false
}

/** What an element inherits from its JSX ancestors: the stack of backgrounds
 *  (nearest first — an opaque one occludes the rest) and the nearest colour. */
type Ctx = { bgStack: ClassAtom[][]; fgInherited: ClassAtom[] | null }

export function derivePairs(
  roots: string[],
  theme: 'dark' | 'light',
): DeriveResult {
  const pairs = new Map<string, DerivedPair>()
  const unresolved: DeriveResult['unresolved'] = []
  let filesScanned = 0
  let elementsScanned = 0
  let nonTextForegrounds = 0

  const files: string[] = []
  const walkDir = (dir: string) => {
    for (const entry of readdirSync(dir)) {
      const p = join(dir, entry)
      if (statSync(p).isDirectory()) {
        if (entry === 'node_modules' || entry === '__tests__') continue
        walkDir(p)
      } else if (
        /\.tsx$/.test(entry) &&
        !/\.(test|spec|stories)\.tsx$/.test(entry)
      ) {
        files.push(p)
      }
    }
  }
  for (const r of roots) walkDir(r)
  files.sort()

  for (const file of files) {
    filesScanned++
    const rel = relative(repoRoot, file)
    const sf = ts.createSourceFile(
      file,
      readFileSync(file, 'utf8'),
      ts.ScriptTarget.ESNext,
      true,
      ts.ScriptKind.TSX,
    )

    /** Cheap enough, and it keeps `governsText` from having to re-parse: does
     *  this element pin its own text colour? */
    const setsOwnColour = (
      el: ts.JsxOpeningElement | ts.JsxSelfClosingElement,
    ) => {
      const e = classNameExpr(el)
      if (!e) return false
      const a: ClassAtom[] = []
      collectClasses(e, [], sf, rel, a, [])
      return a.some((x) => isColourText(x.base))
    }

    const emit = (
      fg: ClassAtom | null,
      bg: ClassAtom,
      under: ClassAtom | null,
      origin: string,
    ) => {
      // The ORIGIN is part of the key. Without it this Map is global and the
      // first file in sort order owns the pairing for the whole console — which
      // makes a debt entry waive the class TRIPLE everywhere instead of the one
      // site it names, so a new file painting the same failing combination
      // inherits the waiver silently. Measured and demonstrated by the review
      // panel; keying per site is what makes "one origin, one pairing" true.
      const key = `${fg ? fg.cls : '<inherited>'}|${bg.cls}|${under?.cls ?? ''}|${origin}`
      if (pairs.has(key)) return
      pairs.set(key, {
        name: `${fg ? fg.base.replace(/^text-/, '') : 'foreground(inherited)'} on ${bg.base.replace(/^bg-/, '')}`,
        kind: 'text',
        fgClass: fg ? fg.cls : '',
        bgClass: bg.cls,
        underClass: under?.cls ?? '',
        fgInherited: !fg,
        origin,
      })
    }

    /** Walks the JSX tree carrying the enclosing background/colour context. */
    const visitJsx = (node: ts.Node, ctx: Ctx) => {
      const isEl = ts.isJsxElement(node) || ts.isJsxSelfClosingElement(node)
      if (!isEl) {
        node.forEachChild((c) => visitJsx(c, ctx))
        return
      }
      elementsScanned++
      const opening = ts.isJsxElement(node) ? node.openingElement : node
      const expr = classNameExpr(opening)
      const atoms: ClassAtom[] = []
      if (expr) collectClasses(expr, [], sf, rel, atoms, unresolved)
      if (hasSpreadAttr(opening)) {
        unresolved.push({
          file: rel,
          line: sf.getLineAndCharacterOfPosition(opening.getStart(sf)).line + 1,
          expr: `{...} spread may carry a className on <${opening.tagName.getText()}>`,
        })
      }

      const allBg = atoms.filter((a) => isBackground(a.base) && paintsOwnBox(a))
      const allFg = atoms.filter((a) => isColourText(a.base))
      const bgAtoms = allBg.filter((a) => !themeShadowed(a, allBg, theme))
      // A childless element renders no text of its own: `<Background
      // className="text-border-strong"/>` colours React Flow's dot canvas, and
      // `<Check className="text-current"/>` an icon. Those are 1.4.11 non-text
      // contrast, a different threshold and a different gate — counted here, not
      // measured, so the omission is visible instead of assumed.
      const rendersText = governsText(node, setsOwnColour)
      const fgAtoms = rendersText
        ? allFg.filter((a) => !themeShadowed(a, allFg, theme))
        : []
      if (!rendersText) nonTextForegrounds += allFg.length
      const line =
        sf.getLineAndCharacterOfPosition(opening.getStart(sf)).line + 1
      const origin = `${rel}:${line}`

      // The background this element's text actually sits on: its own if it sets
      // one, otherwise the nearest enclosing one. Only the NEAREST — an opaque
      // background occludes everything above it. `under` is what a translucent
      // one composites over.
      const ownBg = bgAtoms.length > 0
      const bgHere = ownBg ? bgAtoms : (ctx.bgStack[0] ?? [])
      // What a translucent background composites over. It must be a background
      // that can paint AT THE SAME TIME as the pair: the first draft took `[0]`
      // blindly and put a `hover:`-only fill under a pair with no hover guard —
      // load-bearing, because one such pairing sits on the debt ledger.
      const underLevel = (ownBg ? ctx.bgStack[0] : ctx.bgStack[1]) ?? []
      const underFor = (bg: ClassAtom) =>
        underLevel.find((u) => compatible(u.guards, bg.guards)) ?? null

      // A pair only exists when BOTH classes still win their property: the
      // foreground against its own siblings, the background against its own.
      const pairable = (fg: ClassAtom, bg: ClassAtom, fgSibs: ClassAtom[]) =>
        compatible(fg.guards, bg.guards) &&
        !shadowed(fg, bg, fgSibs) &&
        !shadowed(bg, fg, bgHere)

      for (const fg of fgAtoms) {
        for (const bg of bgHere)
          if (pairable(fg, bg, fgAtoms)) emit(fg, bg, underFor(bg), origin)
      }
      // Text with no colour of its own takes the nearest ancestor's colour, or
      // the document default (`body { color: var(--foreground) }`).
      if (
        fgAtoms.length === 0 &&
        bgHere.length &&
        ts.isJsxElement(node) &&
        hasOwnText(node)
      ) {
        for (const bg of bgHere) {
          if (ctx.fgInherited) {
            for (const fg of ctx.fgInherited)
              if (pairable(fg, bg, ctx.fgInherited))
                emit(fg, bg, underFor(bg), origin)
          } else {
            emit(null, bg, underFor(bg), origin)
          }
        }
      }

      const next: Ctx = {
        bgStack: ownBg ? [bgAtoms, ...ctx.bgStack] : ctx.bgStack,
        fgInherited: fgAtoms.length ? fgAtoms : ctx.fgInherited,
      }
      if (ts.isJsxElement(node)) {
        for (const attr of node.openingElement.attributes.properties)
          visitJsx(attr, next)
        for (const c of node.children) visitJsx(c, next)
      } else {
        for (const attr of node.attributes.properties) visitJsx(attr, next)
      }
    }

    visitJsx(sf, { bgStack: [], fgInherited: null })
  }

  return {
    pairs: [...pairs.values()],
    unresolved,
    filesScanned,
    elementsScanned,
    nonTextForegrounds,
  }
}
