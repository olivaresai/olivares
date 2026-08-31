#!/usr/bin/env node
// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// check-email-brand.mjs — an email may not decide how the brand looks.
//
// THE DEFECT THIS CLOSES. Until 2026-08-06 every outgoing email wrote its own
// brand: `background:#f08000` and `font-family:system-ui` inline at
// portal/handler.ts:94-98, the same pair again at delivery/resend.ts:77-87, the
// same pair a third time in the cloud lifecycle notifier. Those hexes were the
// brand's, which is what made it invisible: nothing was WRONG, so nothing failed.
// The day the brand moves, the console moves and the emails do not, and the first
// to notice is a customer holding last season's orange.
//
// So this gate does not check that emails use the right colour. It checks that
// they do not choose one.
//
// THREE QUESTIONS, all of which must be answerable or the gate fails:
//
//   1. DERIVATION — are the generated bundles what web/tokens + email/copy
//      currently produce? Delegated to `node email/build.mjs --check`, which also
//      measures every foreground/background pair against WCAG 2.2 AA. Change a
//      token and this goes red until the artefacts are regenerated; regenerate
//      and the emails carry the new value. That is the property the whole
//      directory exists for, and it is checked first.
//
//   2. AUTHORSHIP — does any hand-written email surface name a colour, a font or
//      an image? Only STRING LITERALS are examined, never comments: a hex in
//      prose explaining this defect is documentation, a hex in a string is the
//      defect. Generated bundles are exempt because question 1 pins them byte for
//      byte to sources that contain no literal at all.
//
//   WHAT RULE 2 DELIBERATELY DOES NOT COVER, named so nobody mistakes silence for
//   safety (each demonstrated by an independent review, 2026-08-06):
//     * a value ASSEMBLED across expressions — `"col" + "or:orange"`, or a colour
//       reached through an interpolation. Catching it needs evaluation, not lexing.
//     * CSS escape sequences inside a value (`\23` for a hash). The lexer decodes
//       the HOST language's escapes, not CSS's.
//     * a bare value with no adjacent property — `{ color: "orange" }` puts the
//       property in a key and the value in a separate literal.
//   Closing these means parsing JS/TS/Go construction and CSS declarations, which is
//   a different instrument from this one. No tracked surface uses any of them today.
//
//   3. COMPLETENESS — is there an email surface nobody declared? A gate over a
//      hand-written list is a gate you walk around by adding a file. Candidate
//      surfaces are DISCOVERED (anything that talks to the mail API or pulls in a
//      template bundle) and every one must be either migrated or waived by name.
//      An undeclared candidate is red, and so is a waiver that no longer matches.
//
// Usage:
//   node scripts/check-email-brand.mjs             # gate mode
//   node scripts/check-email-brand.mjs --selftest  # fixture battery
//   node scripts/check-email-brand.mjs --list      # show what was discovered

import { execFileSync } from 'node:child_process'
import { readFileSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), '..')

import { outputPaths, treeProfile } from '../email/build.mjs'

// --- 2. what counts as writing a brand ---------------------------------------

// HOLE marks where a ${...} interpolation stood. Eliding a hole silently would
// let two unrelated halves touch and read as one token, in both directions: it
// could invent a colour that nobody wrote, and it could hide a property whose
// value is a token lookup. A character that cannot occur in source keeps the two
// sides apart.
const HOLE = '\u0000'

// A CSS colour: 3, 4, 6 or 8 hex digits and no more, so a 40-character object id
// or a 7-digit fragment is not mistaken for one. `(?<!&)` keeps an HTML numeric
// entity out of it — the preheader spacer is a run of `&#8204;`, and the first
// version of this rule read that as the colour #8204.
// The CSS Color 4 named-colour set. CLOSED and standardised, which is why naming
// them one by one is a finite rule and not a chase.
const CSS_NAMED_COLOURS = ['aliceblue', 'antiquewhite', 'aqua', 'aquamarine', 'azure', 'beige', 'bisque', 'black', 'blanchedalmond', 'blue', 'blueviolet', 'brown', 'burlywood', 'cadetblue', 'chartreuse', 'chocolate', 'coral', 'cornflowerblue', 'cornsilk', 'crimson', 'cyan', 'darkblue', 'darkcyan', 'darkgoldenrod', 'darkgray', 'darkgreen', 'darkgrey', 'darkkhaki', 'darkmagenta', 'darkolivegreen', 'darkorange', 'darkorchid', 'darkred', 'darksalmon', 'darkseagreen', 'darkslateblue', 'darkslategray', 'darkslategrey', 'darkturquoise', 'darkviolet', 'deeppink', 'deepskyblue', 'dimgray', 'dimgrey', 'dodgerblue', 'firebrick', 'floralwhite', 'forestgreen', 'fuchsia', 'gainsboro', 'ghostwhite', 'gold', 'goldenrod', 'gray', 'green', 'greenyellow', 'grey', 'honeydew', 'hotpink', 'indianred', 'indigo', 'ivory', 'khaki', 'lavender', 'lavenderblush', 'lawngreen', 'lemonchiffon', 'lightblue', 'lightcoral', 'lightcyan', 'lightgoldenrodyellow', 'lightgray', 'lightgreen', 'lightgrey', 'lightpink', 'lightsalmon', 'lightseagreen', 'lightskyblue', 'lightslategray', 'lightslategrey', 'lightsteelblue', 'lightyellow', 'lime', 'limegreen', 'linen', 'magenta', 'maroon', 'mediumaquamarine', 'mediumblue', 'mediumorchid', 'mediumpurple', 'mediumseagreen', 'mediumslateblue', 'mediumspringgreen', 'mediumturquoise', 'mediumvioletred', 'midnightblue', 'mintcream', 'mistyrose', 'moccasin', 'navajowhite', 'navy', 'oldlace', 'olive', 'olivedrab', 'orange', 'orangered', 'orchid', 'palegoldenrod', 'palegreen', 'paleturquoise', 'palevioletred', 'papayawhip', 'peachpuff', 'peru', 'pink', 'plum', 'powderblue', 'purple', 'rebeccapurple', 'red', 'rosybrown', 'royalblue', 'saddlebrown', 'salmon', 'sandybrown', 'seagreen', 'seashell', 'sienna', 'silver', 'skyblue', 'slateblue', 'slategray', 'slategrey', 'snow', 'springgreen', 'steelblue', 'tan', 'teal', 'thistle', 'tomato', 'turquoise', 'violet', 'wheat', 'white', 'whitesmoke', 'yellow', 'yellowgreen']

const HEX = /(?<!&)#(?:[0-9a-fA-F]{8}|[0-9a-fA-F]{6}|[0-9a-fA-F]{4}|[0-9a-fA-F]{3})(?![0-9a-fA-F])/
const RULES = [
  { id: 'hex-colour', re: HEX, says: 'names a colour' },
  { id: 'colour-function', re: /\b(?:rgba?|hsla?|oklch|oklab|lab|lch|color-mix)\s*\(/i, says: 'names a colour' },
  // The PROPERTY is not the defect; a literal VALUE for it is. `font-family:` in
  // the shared layout is always followed by a token lookup, which leaves a hole
  // marker where the family would be. A hand-written `font-family:system-ui` has
  // a real character there, and that is what this matches.
  { id: 'font-family', re: new RegExp(`font-family\\s*:\\s*[^;\\s${HOLE}]`, 'i'), says: 'names a typeface' },
  { id: 'font-shorthand', re: /\bfont\s*:\s*(?:\d|normal|bold|italic)/i, says: 'names a typeface' },
  { id: 'image-tag', re: /<img\b/i, says: 'embeds an image' },
  { id: 'vector-tag', re: /<svg\b/i, says: 'embeds an image' },
  { id: 'remote-resource', re: /(?:src|background)\s*=\s*["']?https?:/i, says: 'loads a remote resource' },
  { id: 'data-uri', re: /["'(]\s*data:[a-z]+\//i, says: 'embeds an image' },
  { id: 'web-font', re: /@font-face|@import\b/i, says: 'loads an external font' },
  // A colour does not have to be a hex. `background:orange` is a brand decision
  // spelled in English.
  //
  // The rule matches a CSS NAMED COLOUR anywhere in a colour-bearing property's
  // value, not just straight after the colon — the first version only looked at the
  // first token, so `border:1px solid orange` and `fill:orange` walked past it. The
  // named-colour set is closed and standardised (CSS Color 4), so this is a finite
  // list rather than an open-ended chase; matching any literal instead would red
  // `border:1px solid ${token}`, which is the CORRECT spelling and appears in the
  // shared layout.
  {
    id: 'named-colour',
    // Only the CUSTOM-PROPERTY NAME is removed, not the whole reference.
    //
    // `var(--brand-orange)` made its own NAME the finding once the rule read whole
    // values, because --brand-orange contains "orange". The first repair blanked the
    // entire `var(...)` call, and a review showed that traded one miss for another:
    // it hid `var(--tone, red)`'s fallback and, worse, put a barrier in the value so
    // `border:var(--width) solid red` stopped being read at all. Stripping just the
    // identifier keeps the value continuous, so a literal colour before, after or
    // inside the reference is still found.
    pre: (v) => v.replace(/--[A-Za-z][\w-]*/g, ' '),
    re: new RegExp(
      `(?:^|[;"'\\s{])(?:background|background-color|color|border(?:-(?:top|right|bottom|left))?(?:-color)?|outline(?:-color)?|fill|stroke|caret-color|text-decoration-color|column-rule-color|box-shadow|text-shadow)\\s*:[^;"'${HOLE}]*\\b(?:${CSS_NAMED_COLOURS.join('|')})\\b`,
      'i',
    ),
    says: 'names a colour',
  },
  // url() reaches the network from CSS rather than from an attribute, so the
  // remote-resource rule above never looked at it. `filter`, `fill`, `stroke`,
  // `clip-path` and `mask` were added after a review demonstrated each of them
  // getting through.
  {
    id: 'css-url',
    re: /(?:background|background-image|src|content|mask|mask-image|list-style-image|border-image|border-image-source|filter|fill|stroke|clip-path|cursor|offset-path)\s*:\s*[^;"']*url\s*\(/i,
    says: 'loads a remote resource',
  },
]

// --- the scanner -------------------------------------------------------------

/**
 * Every string literal in a JavaScript/TypeScript or Go source, with its line.
 *
 * Reading raw text would be wrong in both directions. It would flag the comment
 * above this function for containing `#f08000`, and it would MISS nothing —
 * which sounds harmless until the noise makes someone add an exemption that also
 * hides a real one. Reading only literals asks the exact question: does anything
 * that can reach a customer name a colour?
 *
 * `kind` is 'js' or 'go'. Go has no template literals but has raw backtick
 * strings with no escapes; JS has template literals whose ${...} holes are code
 * and must be skipped, not scanned.
 */
/**
 * Decode one backslash escape at `i`, returning [text, indexAfter].
 *
 * Only the escapes that can spell a character the rules look for are decoded —
 * \xHH, \uHHHH, \u{...} and \OOO — plus the ordinary ones. Anything else keeps
 * its literal character, which is what the naive version did for everything.
 */
function decodeEscape(src, i, isGo) {
  const c = src[i + 1]
  if (c === undefined) return ['', i + 1]
  const hex = (start, len) => {
    const h = src.slice(start, start + len)
    return /^[0-9a-fA-F]+$/.test(h) && h.length === len ? h : null
  }
  if (c === 'x') {
    const h = hex(i + 2, 2)
    if (h) return [String.fromCharCode(parseInt(h, 16)), i + 4]
  }
  // Go spells a code point \U with EIGHT hex digits; a review showed \U00000023
  // (a #) walking straight past a decoder that only knew the four-digit form.
  // GO ONLY: JavaScript has no \U escape, so decoding it there invented a colour
  // out of the plain text "U00000023" — a false positive created by the fix.
  if (isGo && c === 'U') {
    const h = hex(i + 2, 8)
    if (h) return [String.fromCodePoint(parseInt(h, 16)), i + 10]
  }
  if (c === 'u') {
    if (src[i + 2] === '{') {
      const end = src.indexOf('}', i + 3)
      const h = end > 0 ? src.slice(i + 3, end) : ''
      if (/^[0-9a-fA-F]{1,6}$/.test(h))
        return [String.fromCodePoint(parseInt(h, 16)), end + 1]
    }
    const h = hex(i + 2, 4)
    if (h) return [String.fromCharCode(parseInt(h, 16)), i + 6]
  }
  if (c >= '0' && c <= '7') {
    const m = /^[0-7]{1,3}/.exec(src.slice(i + 1))
    if (m) return [String.fromCharCode(parseInt(m[0], 8)), i + 1 + m[0].length]
  }
  const simple = { n: '\n', t: '\t', r: '\r', b: '\b', f: '\f', v: '\v', '0': '\0' }
  return [simple[c] ?? c, i + 2]
}

export function stringLiterals(src, kind) {
  const isGo = kind === 'go'
  const out = []
  const lineOf = (idx) => {
    let n = 1
    for (let k = 0; k < idx && k < src.length; k++) if (src[k] === '\n') n++
    return n
  }

  let i = 0
  const n = src.length

  // Walk a balanced ${ ... } hole, respecting strings inside it so a brace in a
  // string cannot unbalance the count.
  //
  // Strings inside the hole are COLLECTED, not discarded. The first version of
  // this function threw them away, and its own battery caught it: the hole is
  // code, but `${cond ? "#f08000" : y}` puts a hand-written colour into the
  // output through it. Skipping the hole entirely would have left one spelling of
  // the defect able to walk straight past the gate.
  const skipHole = (start) => {
    let j = start + 2 // past "${"
    let depth = 1
    while (j < n && depth > 0) {
      const c = src[j]
      if (c === '{') { depth++; j++; continue }
      if (c === '}') { depth--; j++; continue }
      if (c === '"' || c === "'" || c === '`') {
        const at = j
        const [value, end] = readQuoted(j, c)
        out.push({ value, line: lineOf(at) })
        j = end
        continue
      }
      if (c === '/' && src[j + 1] === '/') { while (j < n && src[j] !== '\n') j++; continue }
      if (c === '/' && src[j + 1] === '*') { j += 2; while (j < n && !(src[j] === '*' && src[j + 1] === '/')) j++; j += 2; continue }
      j++
    }
    return j
  }

  // Read one quoted run starting at `start`, returning [contents, indexAfter].
  function readQuoted(start, quote) {
    let j = start + 1
    let buf = ''
    const raw = isGo && quote === '`'
    while (j < n) {
      const c = src[j]
      if (!raw && c === '\\') {
        // DECODE the escape, do not just drop the backslash. Keeping the letter
        // turned "color:\x23f08000" into the harmless-looking `color:x23f08000`,
        // so a hex written as an escape reached a customer with the gate green —
        // demonstrated by an independent review, not imagined.
        const [text, next] = decodeEscape(src, j, isGo)
        buf += text
        j = next
        continue
      }
      if (c === quote) return [buf, j + 1]
      if (!isGo && quote === '`' && c === '$' && src[j + 1] === '{') { buf += HOLE; j = skipHole(j); continue }
      // An unterminated single/double-quoted string cannot span a line in either
      // language; bailing keeps a malformed file from swallowing the rest.
      if (c === '\n' && quote !== '`') return [buf, j]
      buf += c
      j++
    }
    return [buf, j]
  }

  while (i < n) {
    const c = src[i]
    if (c === '/' && src[i + 1] === '/') { while (i < n && src[i] !== '\n') i++; continue }
    if (c === '/' && src[i + 1] === '*') { i += 2; while (i < n && !(src[i] === '*' && src[i + 1] === '/')) i++; i += 2; continue }
    if (c === '"' || c === "'" || c === '`') {
      const at = i
      const [value, after] = readQuoted(i, c)
      out.push({ value, line: lineOf(at) })
      i = after
      continue
    }
    i++
  }
  return out
}

/** Every string value in a JSON document, with a dotted path. */
export function jsonStrings(src) {
  const out = []
  const walk = (node, path) => {
    if (typeof node === 'string') { out.push({ value: node, line: path || '(root)' }); return }
    if (node && typeof node === 'object')
      for (const [k, v] of Object.entries(node)) walk(v, path ? `${path}.${k}` : k)
  }
  walk(JSON.parse(src), '')
  return out
}

/** Findings for one surface. `kind`: 'js' | 'go' | 'json'. */
export function scanSurface(path, src, kind) {
  const literals = kind === 'json' ? jsonStrings(src) : stringLiterals(src, kind)
  const found = []
  for (const { value, line } of literals)
    for (const rule of RULES) {
      const m = rule.re.exec(rule.pre ? rule.pre(value) : value)
      if (m) found.push({ path, line, rule: rule.id, says: rule.says, literal: m[0] })
    }
  return found
}

// --- 3. the inventory --------------------------------------------------------

// A surface is MIGRATED when it composes from the generated bundles and writes no
// brand of its own. These are the files the gate holds to rule 2.
const MIGRATED = [
  'commercial/license-worker/src/portal/handler.ts',
  'commercial/license-worker/src/delivery/resend.ts',
  'commercial/license-worker/src/email/render.ts',
  'core/emailtemplate/emailtemplate.go',
  'email/layout.mjs',
  'email/templates.mjs',
  'email/brand.mjs',
  'email/build.mjs',
  // The render harness reads the bundles and sends nothing, so it could have gone
  // in the not-a-surface list. It goes HERE instead, where the literal rule still
  // applies to it: exempting a file in the email pipeline from that rule buys
  // nothing and creates somewhere to put a colour.
  'email/render-evidence.mjs',
]

// TRANSPORT-ONLY: it sends mail but composes no body — subject and both bodies
// arrive from a caller. It is NOT required to consume the generated templates,
// because there is nothing for it to render; it IS held to rule 2, so the day it
// grows a body with a colour in it, this gate says so. That is the difference
// between this list and the not-a-surface list, which exempts entirely.
// Estas tres rutas viven en `cloud/`, que el export NO publica. El guion NO las lee alli: se
// guarda por PERFIL antes del `readFileSync` — `applicable()` devuelve false para `cloud/` en
// todo arbol que no sea el hub, y esta corrida lo imprime como NOT APPLICABLE. El gate de
// cierre no puede VER esa guarda (lee sintaxis de shell), asi que la declara y lo dice.
// export-closure: hub-only cloud/control-plane/internal/lifecycle/resend.go — el modulo cloud/ no viaja al export; guardado por perfil en applicable()
// export-closure: hub-only cloud/control-plane/internal/billing/unmapped_alert.go — el modulo cloud/ no viaja al export; guardado por perfil en applicable()
// export-closure: hub-only cloud/control-plane/internal/lifecycle/notifier.go — el modulo cloud/ no viaja al export; guardado por perfil en applicable()
const TRANSPORT_ONLY = [
  {
    path: 'cloud/control-plane/internal/lifecycle/resend.go',
    why: 'a minimal HTTP client for the mail API; every field comes from its caller',
  },
  {
    path: 'connectors/email/email.go',
    why:
      'the SMTP notification connector. It builds a MIME message but its only body ' +
      'part is text/plain (email.go:197), so it composes no brand at all; the rule ' +
      'below still applies, so the day it grows an HTML part with a colour in it ' +
      'this goes red.',
  },
  {
    path: 'cloud/control-plane/internal/billing/unmapped_alert.go',
    why:
      'the operator alert for a paid product with no PRODUCT_MAP entry (C05-15). It ' +
      'posts a single "text" field to the mail API and never a "html" one, so it ' +
      'composes no brand: measured on the file, zero colour literals, zero style ' +
      'properties and zero HTML tags. Same shape and same reasoning as the SMTP ' +
      'connector above, and held to the same rule 2 — the day it grows an HTML body ' +
      'with a colour in it, this goes red instead of being waived.',
  },
]

// Copy is prose. It may not carry markup, colour or anything else that belongs to
// the layout — a translator handed a `<div style=...>` will eventually change one.
const COPY_GLOB = 'email/copy/'

// Generated bundles legitimately contain every literal in the palette. They are
// exempt from rule 2 and covered instead by rule 1, which pins them byte for byte
// to sources that contain no literal at all. This is not a hole: a hand-typed hex
// in one of these files is erased by the next regeneration, and the drift check
// fails until it is.
const GENERATED = [
  'commercial/license-worker/src/email/templates.generated.ts',
  'core/emailtemplate/templates.generated.json',
  'email/email.manifest.json',
]

// A WAIVER names a surface that has NOT been migrated, its owner, and the exact
// literals it carries today. It is deliberately hard to leave lying around: if the
// literals disappear the waiver is stale and the gate says so, and if new ones
// appear the gate fails on those. Unrelated edits to the file do not trip it, so
// waiving somebody else's surface does not tax their unrelated work.
const WAIVED = [
  {
    path: 'cloud/control-plane/internal/lifecycle/notifier.go',
    owner: 'the cloud lane',
    since: '2026-08-06',
    why:
      'The data-deletion warning email, composed inline with the same defect as the ' +
      'other three. NOT migrated because the cross-domain authorisation for this work ' +
      'names two files in commercial/license-worker and not this module, and because ' +
      'cloud/control-plane is a separate Go module (olivares-cloud-cp) that does not ' +
      'depend on core/ and therefore cannot import core/emailtemplate without a ' +
      'require+replace that belongs to its owner.',
    literals: ['font-family:system-ui,sans-serif;line-height:1.5', 'color:#666;font-size:13px'],
  },
]

// How a candidate email surface is DISCOVERED. Nothing here is a list of files:
// these are the marks a file leaves when it composes or sends mail.
const CANDIDATE_MARKS = [
  { id: 'mail-api', re: /api\.resend\.com/ },
  { id: 'template-bundle', re: /templates\.generated|core\/emailtemplate|emailtemplate"/ },
  { id: 'email-render', re: /from ["'][^"']*email\/render/ },
  // Provider-independent marks. The first version of this list recognised one
  // vendor's endpoint, so an SMTP sender that has been in this tree all along was
  // invisible to a check whose whole claim is completeness. A future SES or
  // SendGrid composer would have been invisible the same way.
  { id: 'smtp', re: /net\/smtp|smtp\.SendMail|smtp\.Dial/ },
  { id: 'mail-provider', re: /api\.sendgrid\.com|email\.[a-z0-9-]+\.amazonaws\.com|\bses(?:v2)?\.SendEmail|api\.postmarkapp\.com|api\.mailgun\.net/ },
]

// Files that MENTION a mark without being a surface. Each needs a reason, and the
// reason has to be that it cannot reach a customer's inbox.
const NOT_A_SURFACE = [
  // The gate itself and its battery: they name the marks in order to find them.
  { path: 'scripts/check-email-brand.mjs', why: 'this gate' },
  // A SIBLING gate, and the same reason: check-portal-brand.mjs names api.resend.com and
  // templates.generated in a COMMENT (its :14) that explains what THIS gate discovers, so
  // that a reader knows why the licence portal fell outside both. It sends no mail and
  // decides nothing about how mail looks. Undeclared it made the trunk red — and
  // `lint:email-brand` is a FAST-LINT, so it was not just main: it bounced every lane's
  // branch push, mine among them, for a file none of them touch.
  //
  // ⚠ TRES CARRILES ESCRIBIERON ESTA MISMA EXENCIÓN. Dos aterrizaron en `main` —el MISMO path
  // excluido dos veces, con dos razones distintas, por dos carriles que no se vieron— y el claim
  // kernel-k3 traía una tercera. Se queda UNA. La duplicación no es el defecto: el defecto es que
  // la lista aceptó la segunda EN SILENCIO. Quien escribe una exención cree estar añadiendo algo
  // nuevo y nada se lo desmiente; el día que dos entradas se CONTRADIGAN tampoco dirá nada, y la
  // segunda es invisible para quien lee la primera. Por eso ahora se rechazan rutas repetidas.
  { path: 'scripts/check-portal-brand.mjs', why: 'a sibling gate' },
  // Test doubles: a mocked endpoint sends nothing.
  { path: 'commercial/license-worker/test/e2e-hermetic.test.ts', why: 'mocks the mail API' },
  // Same shape and for a stricter reason: it stubs globalThis.fetch and THROWS on any host
  // other than the Resend endpoint, so an accidental real send fails the test. It names the
  // endpoint in order to refuse everything else, and decides nothing about how mail looks.
  { path: 'commercial/license-worker/test/dodo-issue.test.ts', why: 'stubs the mail API' },
  {
    path: 'commercial/license-worker/test/c03-19-operator-deliveries.test.ts',
    why: 'stubs the mail API the same way dodo-issue.test.ts does; sends nothing',
  },
  {
    path: 'commercial/license-worker/test/portal-login-csrf.test.ts',
    why:
      'the login-CSRF battery. It stubs globalThis.fetch and ASSERTS the endpoint, failing on any ' +
      'other host, so an accidental real send fails the test — the same shape and the same ' +
      'reasoning as dodo-issue.test.ts above. It names the endpoint in order to count sends: its ' +
      'central claim is that a cross-site request causes ZERO of them. It decides nothing about ' +
      'how mail looks and composes no body.',
  },
  // replay battery for H-04, and the SAME shape again: it swaps globalThis.fetch,
  // THROWS on any host that is not the Resend endpoint ("the worker must not call ..."), and
  // restores the real fetch afterwards. It names the endpoint in order to refuse every other
  // one, so it cannot reach an inbox and decides nothing about how mail looks.
  {
    path: 'commercial/license-worker/test/h04-delivery-failed-replay.test.ts',
    why: 'stubs the mail API and refuses any other host; sends nothing',
  },
  // The two batteries written for the 2026-08-27 licence-mail change. Both are DISCOVERED — one
  // names the mail endpoint, the other imports the generated bundle — and both are exactly the
  // shape this list exists for: they observe what the pipeline produced and send nothing.
  {
    path: 'commercial/license-worker/test/portal-cross-device.test.ts',
    why:
      'stubs globalThis.fetch and ASSERTS the endpoint, failing on any other host, so an ' +
      'accidental real send fails the test. It names the endpoint in order to refuse everything ' +
      'else — same shape and same reasoning as dodo-issue.test.ts above. Its one email-related ' +
      'claim is that the confirmation code is ABSENT from the body, which is a property of the ' +
      'body, not a decision about how it looks.',
  },
  {
    path: 'commercial/license-worker/test/email-portal-link.test.ts',
    why:
      'reads templates.generated.ts to assert that the four licence shapes are reachable, that ' +
      'each carries exactly the markers its contract declares, and that the mark is drawn rather ' +
      'than fetched. It composes through the real renderer and never reaches the network: it is ' +
      'the same class as core/emailtemplate/emailtemplate_test.go, which asserts the derived ' +
      'values from the other runtime.',
  },
  { path: 'core/emailtemplate/emailtemplate_test.go', why: 'asserts the derived values' },
]

// Se rechaza al ARRANCAR, antes de mirar un solo fichero: un gate que no puede confiar en su
// propia configuración no puede dar un verde. Ver el comentario de la exención de
// check-portal-brand.mjs, arriba, para el caso medido que lo motivó.
{
  const vistos = new Map()
  const repetidos = []
  for (const n of NOT_A_SURFACE) {
    if (vistos.has(n.path)) repetidos.push(`${n.path} — «${vistos.get(n.path)}» y «${n.why}»`)
    else vistos.set(n.path, n.why)
  }
  if (repetidos.length > 0) {
    console.error('check-email-brand: NO_HE_PODIDO_MIRAR — NOT_A_SURFACE declara la misma ruta más de una vez:')
    for (const r of repetidos) console.error(`    ${r}`)
    console.error('  Deja UNA entrada con la razón completa. Dos razones para la misma ruta no se pueden')
    console.error('  auditar: la segunda es invisible para quien lee la primera.')
    process.exit(2)
  }
}

function tracked() {
  return execFileSync('git', ['ls-files'], { cwd: ROOT, encoding: 'utf8', maxBuffer: 64 * 1024 * 1024 })
    .split('\n')
    .filter(Boolean)
}

function kindOf(path) {
  if (path.endsWith('.go')) return 'go'
  if (path.endsWith('.json')) return 'json'
  return 'js'
}

function discover() {
  const found = []
  for (const path of tracked()) {
    if (!/\.(ts|mjs|js|go)$/.test(path)) continue
    let src
    try {
      src = readFileSync(join(ROOT, path), 'utf8')
    } catch {
      continue
    }
    const marks = CANDIDATE_MARKS.filter((m) => m.re.test(src)).map((m) => m.id)
    if (marks.length) found.push({ path, marks })
  }
  return found
}

// --- the gate ----------------------------------------------------------------

/**
 * Surfaces live in trees the public export does not ship (commercial/, cloud/).
 * The same three-answer rule as the builder: present -> check it; absent in a
 * marked public tree -> not applicable, say so; absent with no marker -> the
 * builder already refused, and this never runs.
 */
function applicable(path, profile) {
  if (profile.name === 'hub') return true
  return !/^(commercial|cloud)\//.test(path)
}

function run() {
  const problems = []
  const notes = []
  const profile = treeProfile()
  if (profile.name !== 'hub')
    notes.push(
      `${profile.name} tree — commercial/ and cloud/ are not part of it; their ` +
        'surfaces are NOT APPLICABLE here and were not examined.',
    )

  // 1. DERIVATION
  try {
    const out = execFileSync('node', [join(ROOT, 'email/build.mjs'), '--check'], {
      cwd: ROOT,
      encoding: 'utf8',
    })
    notes.push(out.trim())
  } catch (e) {
    problems.push(
      `derivation: the generated bundles are not what web/tokens + email/copy produce.\n` +
        `${(e.stdout ?? '').trim()}\n${(e.stderr ?? '').trim()}`.trim(),
    )
  }

  // 2. AUTHORSHIP
  for (const path of [...MIGRATED, ...TRANSPORT_ONLY.map((t) => t.path)]) {
    if (!applicable(path, profile)) continue
    let src
    try {
      src = readFileSync(join(ROOT, path), 'utf8')
    } catch {
      problems.push(`inventory: declared surface ${path} does not exist`)
      continue
    }
    for (const f of scanSurface(path, src, kindOf(path)))
      problems.push(`${f.path}:${f.line} ${f.says} in a string literal: ${f.literal} (${f.rule})`)
  }

  for (const path of tracked().filter((p) => p.startsWith(COPY_GLOB))) {
    const src = readFileSync(join(ROOT, path), 'utf8')
    for (const f of scanSurface(path, src, 'json'))
      problems.push(`${path} (${f.line}) ${f.says} in the copy: ${f.literal} (${f.rule})`)
    if (/[<>]/.test(src)) problems.push(`${path} contains markup; copy is prose only`)
  }

  // The exemption list is not maintained: it IS the builder's output set. An
  // independently typed list is a place to park a hand-written branded file where
  // the authorship rule exempts it and the drift check never looks at it, which
  // makes the claim two lines above false without anything going red.
  const generatedHere = GENERATED.filter((p) => applicable(p, profile))
  const OUT = outputPaths()
  const extraExempt = generatedHere.filter((p) => !OUT.includes(p))
  const missingExempt = OUT.filter((p) => !generatedHere.includes(p))
  for (const p of extraExempt)
    problems.push(`generated: ${p} is exempt from the literal rule but email/build.mjs does not emit it`)
  for (const p of missingExempt)
    problems.push(`generated: email/build.mjs emits ${p} but it is not classified`)

  // 3. COMPLETENESS
  const declared = new Set([
    ...MIGRATED,
    ...GENERATED,
    ...TRANSPORT_ONLY.map((t) => t.path),
    ...WAIVED.map((w) => w.path),
    ...NOT_A_SURFACE.map((n) => n.path),
  ])
  for (const { path, marks } of discover())
    if (!declared.has(path))
      problems.push(
        `inventory: ${path} looks like an email surface (${marks.join(', ')}) and is in ` +
          `neither the migrated list, the waiver list nor the not-a-surface list. ` +
          `Decide explicitly — nothing is exempt by default.`,
      )

  // waivers must still describe reality
  for (const w of WAIVED) {
    if (!applicable(w.path, profile)) continue
    let src
    try {
      src = readFileSync(join(ROOT, w.path), 'utf8')
    } catch {
      problems.push(`waiver: ${w.path} no longer exists — remove the waiver`)
      continue
    }
    const gone = w.literals.filter((lit) => !src.includes(lit))
    if (gone.length)
      problems.push(
        `waiver: ${w.path} no longer carries ${gone.map((l) => JSON.stringify(l)).join(', ')}. ` +
          `If it was migrated, remove the waiver and add it to MIGRATED.`,
      )
    const known = new Set(w.literals)
    for (const f of scanSurface(w.path, src, kindOf(w.path))) {
      const covered = w.literals.some((lit) => lit.includes(f.literal))
      if (!covered && !known.has(f.literal))
        problems.push(
          `waiver: ${f.path}:${f.line} grew a NEW literal the waiver does not cover: ` +
            `${f.literal} (${f.rule})`,
        )
    }
    notes.push(`waived: ${w.path} — ${w.owner}, since ${w.since}`)
  }

  return { problems, notes }
}

// --- the battery -------------------------------------------------------------
//
// A detector nobody has watched fail is not a detector (the lint:boundary
// pattern). Every red case below must be RED and every green case GREEN, and the
// scanner itself is exercised first, because a tokenizer that silently
// mis-tokenizes turns this whole file into decoration.

const CASES = [
  // --- the scanner, on its own -----------------------------------------------
  {
    name: 'a hex in a COMMENT is not a finding',
    kind: 'js',
    src: '// the old code wrote #f08000 by hand\nconst x = "clean";\n',
    red: false,
  },
  {
    name: 'a hex in a STRING is a finding',
    kind: 'js',
    src: 'const x = "background:#f08000";\n',
    red: true,
  },
  {
    name: 'a hex in a TEMPLATE literal is a finding',
    kind: 'js',
    src: 'const x = `<a style="background:#f08000">hi</a>`;\n',
    red: true,
  },
  {
    name: 'a token lookup inside a ${} hole is not scanned as text',
    kind: 'js',
    src: 'const x = `<a style="background:${t("accent")}">hi</a>`;\n',
    red: false,
  },
  {
    name: 'a hex inside a ${} hole IS still caught, via its own literal',
    kind: 'js',
    src: 'const x = `<a style="background:${cond ? "#f08000" : y}">hi</a>`;\n',
    red: true,
  },
  {
    name: 'a // inside a URL string does not start a comment and hide the rest',
    kind: 'js',
    src: 'const u = "https://example.invalid/x";\nconst x = "color:#123456";\n',
    red: true,
  },
  {
    name: 'a block comment spanning a hex is not a finding',
    kind: 'js',
    src: '/* was background:#f08000\n   for a year */\nconst x = "clean";\n',
    red: false,
  },
  {
    name: 'a Go raw string with a hex is a finding',
    kind: 'go',
    src: 'const s = `<p style="color:#666">x</p>`\n',
    red: true,
  },
  {
    name: 'an escaped quote does not end the string early',
    kind: 'js',
    src: 'const x = "say \\"hi\\" then color:#abcdef";\n',
    red: true,
  },
  {
    name: 'a 40-character object id is not a colour',
    kind: 'js',
    src: 'const sha = "#0123456789abcdef0123456789abcdef01234567";\n',
    red: false,
  },
  {
    name: 'a seven-digit fragment is not a colour',
    kind: 'js',
    src: 'const frag = "#1234567";\n',
    red: false,
  },
  {
    // The preheader spacer is a run of these. The first version of the hex rule
    // read &#8204; as the colour #8204 and turned the shared layout red.
    name: 'an HTML numeric entity is not a colour',
    kind: 'js',
    src: 'const spacer = "&#8204;&nbsp;";\n',
    red: false,
  },
  {
    name: 'font-family whose value is a token lookup is not a finding',
    kind: 'js',
    src: 'const x = `<p style="font-family:${f("sans")};font-size:16px">y</p>`;\n',
    red: false,
  },
  {
    name: 'font-family with a literal family IS a finding',
    kind: 'js',
    src: 'const x = `<p style="font-family:system-ui,sans-serif">y</p>`;\n',
    red: true,
  },
  {
    name: 'a hole marker keeps two halves from reading as one colour',
    kind: 'js',
    src: 'const x = `#ff${a}ffff`;\n',
    red: false,
  },
  // --- the six an independent review demonstrated, 2026-08-06 ----------------
  // Every one of these returned ZERO findings before that review. They are the
  // reason this battery is not decoration: the author had already declared the
  // scanner sound.
  { name: 'a QUOTED font family (the rule excluded quotes)', kind: 'js', src: `const x = "font-family:'Arial'";\n`, red: true },
  { name: 'a NAMED colour', kind: 'js', src: 'const x = "background:orange";\n', red: true },
  { name: 'a css url()', kind: 'js', src: 'const x = "background-image:url(https://x.invalid/a.png)";\n', red: true },
  { name: 'a hex written as a \\x escape', kind: 'js', src: 'const x = "color:\\x23f08000";\n', red: true },
  { name: 'a hex written as a \\u escape', kind: 'js', src: 'const x = "color:\\u0023f08000";\n', red: true },
  { name: 'a hex as \\x in a Go string', kind: 'go', src: 'const s = "color:\\x23f08000"\n', red: true },
  // ...and the prose these rules must NOT read as brand.
  { name: 'prose containing URL ( is not a css url', kind: 'go', src: 'const e = "SMTP URL (host:port) is required"\n', red: false },
  { name: 'prose containing the word background', kind: 'js', src: 'const x = "contact support for background information";\n', red: false },
  { name: 'a CSS-wide keyword chooses no colour', kind: 'js', src: 'const x = "background:transparent";\n', red: false },
  // --- the second review's mutants, 2026-08-06 -------------------------------
  // The false positive first, because it is the one that gets a gate switched off:
  // var() is the CORRECT way to reference a token and this rule used to red it.
  { name: 'var() is a reference, not a colour', kind: 'js', src: 'const x = "color:var(--brand-text)";\n', red: false },
  { name: 'env() likewise', kind: 'js', src: 'const x = "color:env(--x)";\n', red: false },
  { name: 'a shorthand with a TOKEN is correct code', kind: 'js', src: 'const x = `<td style="border:1px solid ${t("border")}">`;\n', red: false },
  // ...and the bypasses it demonstrated.
  { name: 'a named colour inside a shorthand', kind: 'js', src: 'const x = "border:1px solid orange";\n', red: true },
  { name: 'a named colour on an SVG fill', kind: 'js', src: 'const x = "fill:orange";\n', red: true },
  { name: 'a named colour on a stroke', kind: 'js', src: 'const x = "stroke:rebeccapurple";\n', red: true },
  { name: 'a url() on filter', kind: 'js', src: 'const x = "filter:url(https://x.invalid/a.svg)";\n', red: true },
  { name: "Go's eight-digit \\U spelling of a hash", kind: 'go', src: 'const s = "color:\\U00000023f08000"\n', red: true },
  // --- the sign-off's mutants: three of these were CREATED by the fix above ----
  { name: 'a token NAMED after a colour is a reference', kind: 'js', src: 'const x = "color:var(--brand-orange)";\n', red: false },
  { name: 'env() named after a colour, likewise', kind: 'js', src: 'const x = "color:env(--red)";\n', red: false },
  { name: 'JavaScript has no \\U escape', kind: 'js', src: 'const x = "color:\\U00000023f08000";\n', red: false },
  { name: 'a named colour in a box-shadow', kind: 'js', src: 'const x = "box-shadow:0 0 2px red";\n', red: true },
  { name: 'a url() on border-image-source', kind: 'js', src: 'const x = "border-image-source:url(https://x.invalid/a.png)";\n', red: true },
  // ...and the miss the FIRST repair of that false positive introduced.
  { name: 'a literal AFTER a reference in the same value', kind: 'js', src: 'const x = "border:var(--width) solid red";\n', red: true },
  { name: "a literal in a reference's FALLBACK", kind: 'js', src: 'const x = "color:var(--tone, red)";\n', red: true },
  // --- the rules --------------------------------------------------------------
  { name: 'font-family in a string', kind: 'js', src: 'const x = "font-family:system-ui";\n', red: true },
  { name: 'an rgb() colour', kind: 'js', src: 'const x = "color:rgb(1,2,3)";\n', red: true },
  { name: 'a colour-mix()', kind: 'js', src: 'const x = "color-mix(in oklab, a, b)";\n', red: true },
  { name: 'an img tag', kind: 'js', src: 'const x = "<img alt=\\"logo\\">";\n', red: true },
  { name: 'an inline vector', kind: 'js', src: 'const x = "<svg viewBox=\\"0 0 1 1\\">";\n', red: true },
  { name: 'a remote resource', kind: 'js', src: 'const x = "<td background=\\"https://x.invalid/a.png\\">";\n', red: true },
  { name: 'a data URI', kind: 'js', src: 'const x = "src=\\"data:image/png;base64,AAA\\"";\n', red: true },
  { name: 'a web font', kind: 'js', src: 'const x = "@font-face { src: local(x) }";\n', red: true },
  { name: 'ordinary code with no brand in it', kind: 'js', src: 'const x = renderEmail("signin", locale, v);\n', red: false },
  // --- copy -------------------------------------------------------------------
  {
    name: 'copy carrying a colour',
    kind: 'json',
    src: '{"a":{"b":"press the #f08000 button"}}',
    red: true,
  },
  { name: 'plain copy', kind: 'json', src: '{"a":{"b":"Sign in to your portal"}}', red: false },
]

function selftest() {
  let pass = 0
  let fail = 0
  for (const c of CASES) {
    let findings
    try {
      findings = scanSurface('fixture', c.src, c.kind)
    } catch (e) {
      findings = null
      console.log(`FAIL ${c.name.padEnd(62)} threw: ${e.message}`)
      fail++
      continue
    }
    const isRed = findings.length > 0
    if (isRed === c.red) {
      pass++
      console.log(`ok   ${c.name.padEnd(62)} ${isRed ? 'RED' : 'green'}`)
    } else {
      fail++
      console.log(
        `FAIL ${c.name.padEnd(62)} got ${isRed ? 'RED' : 'green'}, want ${c.red ? 'RED' : 'green'}` +
          (findings.length ? ` (${findings.map((f) => f.literal).join(', ')})` : ''),
      )
    }
  }

  // The waiver rules, exercised against synthetic content rather than the tree, so
  // the battery keeps testing them after the real waiver is retired.
  const waiver = { literals: ['color:#666;font-size:13px'] }
  const stale = !'nothing here'.includes(waiver.literals[0])
  if (stale) { pass++; console.log(`ok   ${'a waiver whose literal is gone is STALE'.padEnd(62)} RED`) }
  else { fail++; console.log('FAIL a waiver whose literal is gone is STALE') }

  const grew = scanSurface('f', 'const s = "color:#666;font-size:13px" + "background:#abcdef"', 'js')
    .filter((f) => !waiver.literals.some((l) => l.includes(f.literal)))
  if (grew.length === 1 && grew[0].literal === '#abcdef') {
    pass++
    console.log(`ok   ${'a waived surface that grew a NEW literal'.padEnd(62)} RED`)
  } else {
    fail++
    console.log(`FAIL a waived surface that grew a NEW literal (got ${JSON.stringify(grew.map((f) => f.literal))})`)
  }

  console.log(`\ncheck-email-brand: ${pass} passed, ${fail} failed`)
  return fail === 0
}

// --- main --------------------------------------------------------------------

const argv = process.argv.slice(2)
const isEntry =
  process.argv[1] && fileURLToPath(import.meta.url) === resolve(process.argv[1])

if (!isEntry) {
  // imported for its scanner; do nothing
} else if (argv.includes('--selftest')) {
  process.exit(selftest() ? 0 : 1)
} else if (argv.includes('--list')) {
  for (const { path, marks } of discover()) console.log(`${path}\t${marks.join(',')}`)
} else {
  let problems, notes
  try {
    ;({ problems, notes } = run())
  } catch (e) {
    // A refusal is a verdict and has to be readable. Letting this escape as an
    // unhandled module error printed a stack trace and buried the reason.
    console.error(`check-email-brand: ${e.message}`)
    process.exit(2)
  }
  for (const n of notes) console.log(`check-email-brand: ${n}`)
  if (problems.length) {
    console.error('\ncheck-email-brand: an email is deciding how the brand looks.\n')
    for (const p of problems) console.error(`  ${p}`)
    console.error(
      '\nColours, typefaces and layout come from web/tokens via email/build.mjs.\n' +
        'If a token needs to change, change the token and regenerate.\n',
    )
    process.exit(1)
  }
  const prof = treeProfile()
  console.log(
    `check-email-brand: OK — ${MIGRATED.filter((p) => applicable(p, prof)).length} migrated surfaces ` +
      `carry no brand of their own, ${WAIVED.filter((w) => applicable(w.path, prof)).length} waived ` +
      `and named, ${GENERATED.filter((p) => applicable(p, prof)).length} generated bundles pinned to ` +
      `their sources${prof.name === 'hub' ? '.' : ` (${prof.name} tree).`}`,
  )
}
