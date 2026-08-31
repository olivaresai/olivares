// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// layout.mjs — THE shared email layout. One definition, two runtimes.
//
// WHY THIS IS A GENERATOR AND NOT A LIBRARY. The three emails are composed in two
// languages that share nothing: a Cloudflare Worker in TypeScript and the Go
// engine. A "shared template" implemented once per runtime is two templates that
// happen to agree today — the same shape of defect as two hand-written hexes that
// happen to match. So the layout exists exactly once, here, and build.mjs renders
// it to finished HTML and text per (template, locale). Each runtime does one
// thing: escape a value and substitute a {{PLACEHOLDER}}. Neither can drift,
// because neither decides anything.
//
// DELIVERABILITY RULES BAKED IN, not left to the caller:
//   * ZERO images. Not remote, not data: URI, not inline SVG, not a cid
//     attachment. Gmail strips data: and SVG outright, half of corporate mail
//     blocks remote images by default, and the errand's constraint is that the
//     message must not depend on them. The brand is carried by the MARK (drawn
//     below out of table cells, not fetched), the wordmark, the type stack, the
//     hairline and the single accent — all of which survive with images off,
//     because none of them is an image.
//     ⛔ AND IT IS NOT A PREFERENCE HERE, IT IS A GATE: this file is on the
//     MIGRATED list of scripts/check-email-brand.mjs, whose rules `image-tag`
//     and `vector-tag` red an `<img` or an `<svg` in any string literal, and
//     build.mjs asserts the same set again on the RENDERED body. A logo in this
//     message can only be something a mail client draws from markup it already
//     paints — which is what markHtml() below is.
//   * ZERO external fonts. No @font-face, no <link>. The token stacks name
//     'Inter Variable' and 'Space Grotesk Variable' first; a mail client that has
//     never heard of them falls through to system-ui and the message still reads.
//     Naming a family is not loading one.
//   * Table-based structure and inline styles, because Outlook renders with Word.
//   * A light-first inline palette plus a prefers-color-scheme override, so a
//     client with no dark support shows the light design rather than nothing.

const PX = { h1: 22, h2: 17, body: 16, small: 13, code: 13, wordmark: 18 }

// --- the brand mark, in the only units a mail client is guaranteed to paint ---
//
// THE LEDGER O, DRAWN WITH TABLE CELLS. The canonical glyph (brandv4, locked
// 2026-06-03, `sessions-logo-spec.md`) is an arc closing the counter on
// the right and four stacked audit-ledger rows on the left, the SECOND of them
// orange — the flagged WRITE. It exists as SVG in five places, and none of them
// can be used here: an `<svg>` is stripped by Gmail, an `<img>` is blocked by
// default in most corporate mail, and both are refused by the gate named above.
//
// So the mark is rebuilt out of the two primitives every mail client renders,
// Word included: a table cell with a background colour, and a div with a border.
// The canonical artboard coordinates are DATA in ARTBOARD below and the pixels
// are computed from them — see the note under the degradation model for why they
// are not simply written out here as constants.
//
// HOW IT DEGRADES, which is the part that had to be decided rather than hoped:
// Word ignores `border-radius`, so in desktop Outlook the round caps square off
// and the arc becomes a bracket. The mark still reads — four rows, one of them
// orange, closed on the right — because its identity is the ROW STACK and the
// flagged row, not the curvature. Nothing here depends on the network, so with
// images off it is byte-identical to images on.
//
// ⛔ THE PIXELS ARE COMPUTED FROM THE COORDINATES, NOT TYPED BESIDE THEM. The
// first version of this block wrote the constants by hand under a comment that
// said "geometry transcribed from the canonical artboard at scale 1.25" — and
// the Codex `sol max` contrast of 2026-08-27 did the arithmetic and found they
// were not: the pitch was 6 where the coordinates give 5, every ink row was 4 px
// where the strokes give 3, and all four widths were 1-2 px over. The arc
// matched; nothing else did. The numbers were fine to look at, which is exactly
// how they got there — they were chosen by eye and then described as derived.
//
// A comment claiming a derivation nobody performs is worse than no comment: it
// is the shape of defect this whole directory exists to remove, reintroduced in
// the file that removes it. So the canonical artboard numbers are DATA here, and
// the pixels come out of them. The two cannot disagree because there is now only
// one of them.
const ARTBOARD = {
  // The canonical Ledger O on its 32x32 artboard (brandv4, BRAND-01 locked
  // 2026-06-03; `sessions-logo-spec.md`). Rows top to bottom, x=8.4;
  // index 1 is the flagged WRITE and is the only one that carries the accent.
  rows: [
    { y: 10.5, len: 4.8, stroke: 2.6 },
    { y: 14.5, len: 7.0, stroke: 3.1 },
    { y: 18.5, len: 6.6, stroke: 2.6 },
    { y: 22.5, len: 5.0, stroke: 2.6 },
  ],
  // `M16 5.5 A10.5 10.5 0 0 1 16 26.5`, stroke 3: a half-circle closing the
  // counter on the right. Its outer box is the radius plus the stroke on each
  // side vertically, and the radius plus half a stroke horizontally.
  arc: { r: 10.5, stroke: 3 },
  // 1.5 and not 1.25: the round caps of the SVG carry weight that a square
  // table cell does not, so the glyph needs the extra pixel of stroke to hold
  // the same presence beside an 18 px wordmark. It is one number, and it is the
  // ONLY thing here chosen by eye.
  scale: 1.5,
}

/** Whole pixels, because a mail client cannot be trusted with a fraction of one. */
const px = (v) => Math.round(v * ARTBOARD.scale)

const MARK = {
  // Row height IS the stroke: 2.6 for ink, 3.1 for the flagged row, so the
  // accent row comes out thicker exactly as the artboard draws it.
  barH: ARTBOARD.rows.map((r) => px(r.stroke)),
  // Length plus the round cap the SVG adds at each end (one stroke in total).
  barW: ARTBOARD.rows.map((r) => px(r.len + r.stroke)),
  // The gap BELOW each row, from the centre-to-centre pitch minus the two half
  // heights it spans. The last row has none. Derived, so moving a row's y or its
  // stroke moves the gap with it.
  barGap: ARTBOARD.rows.slice(0, -1).map((r, i) => {
    const next = ARTBOARD.rows[i + 1]
    return Math.max(1, px(next.y - r.y - r.stroke / 2 - next.stroke / 2))
  }),
  // The arc's OUTER box. The div carries the stroke as a border, so its own
  // width/height are this minus the borders it draws.
  arcW: px(ARTBOARD.arc.r + ARTBOARD.arc.stroke / 2),
  arcH: px(2 * ARTBOARD.arc.r + ARTBOARD.arc.stroke),
  arcStroke: px(ARTBOARD.arc.stroke),
}

/** HTML-escape a build-time string. Also escapes quotes, so one escaper covers
 *  both text nodes and attribute values — the same rule the runtimes apply. */
export function esc(s) {
  return String(s)
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;')
}

/** CSS-escape a value destined for a style attribute. Token values are hexes and
 *  font stacks, but a `"` or a `;` slipping into a style attribute would break out
 *  of the declaration, so the emitter refuses rather than sanitising silently. */
function css(value, what) {
  if (/[<>"]/.test(value))
    throw new Error(`email/layout: token ${what} is not style-attribute safe: ${value}`)
  return value
}

// --- the dark-mode contract --------------------------------------------------
// Every element that carries colour gets a class AND an inline light value. The
// inline value is what every client paints. The class exists only so the
// prefers-color-scheme block can repaint it where that block is honoured (Apple
// Mail, Outlook.com, Thunderbird, iOS). Clients that ignore the block — Gmail's
// web client among them — keep the light design, which is why light is inline and
// dark is the override and not the other way round.
const CLASS = {
  canvas: 'o-canvas',
  card: 'o-card',
  text: 'o-text',
  muted: 'o-muted',
  rule: 'o-rule',
  btnCell: 'o-btn',
  btnText: 'o-btn-a',
  well: 'o-well',
  // The mark's ledger rows and its arc. They carry INK as a background and as a
  // border respectively, which `.o-text` cannot repaint — that class only moves
  // `color`. A mark that stayed dark-on-dark would be the one element of the
  // brand that disappears in the theme this product is designed in.
  ink: 'o-ink',
  arc: 'o-arc',
  flag: 'o-flag',
  // The outlined button. Its fill is the card, so it needs the card's dark value
  // and the border's, and its label is `.o-text`.
  btn2Cell: 'o-btn2',
}

function darkStyles(dark) {
  const d = (k) => css(dark[k], k)
  return [
    `      .${CLASS.canvas} { background-color: ${d('canvas')} !important; }`,
    `      .${CLASS.card} { background-color: ${d('surface')} !important; border-color: ${d('border')} !important; }`,
    `      .${CLASS.text} { color: ${d('text')} !important; }`,
    `      .${CLASS.muted} { color: ${d('textMuted')} !important; }`,
    `      .${CLASS.rule} { border-color: ${d('border')} !important; background-color: ${d('border')} !important; }`,
    `      .${CLASS.btnCell} { background-color: ${d('accent')} !important; }`,
    `      .${CLASS.btnText} { color: ${d('accentText')} !important; }`,
    `      .${CLASS.well} { background-color: ${d('well')} !important; border-color: ${d('border')} !important; }`,
    `      .${CLASS.ink} { background-color: ${d('text')} !important; }`,
    `      .${CLASS.arc} { border-color: ${d('text')} !important; }`,
    `      .${CLASS.flag} { background-color: ${d('accent')} !important; }`,
    `      .${CLASS.btn2Cell} { background-color: ${d('surface')} !important; border-color: ${d('border')} !important; }`,
  ].join('\n')
}

/**
 * The brand mark. Returns the `<table>` that draws it; the caller places it.
 *
 * Every colour arrives from the resolved tokens, exactly like the rest of this
 * file: the mark cannot wear an orange the console has stopped wearing, which is
 * the whole reason this directory generates instead of hand-writing.
 */
function markHtml(L) {
  const t = (k) => css(L[k], k)
  const rows = MARK.barW.map((w, i) => {
    const flagged = i === 1
    const fill = flagged ? t('accent') : t('text')
    const cls = flagged ? CLASS.flag : CLASS.ink
    const h = MARK.barH[i]
    const padBottom = MARK.barGap[i] ?? 0
    return (
      `<tr><td style="padding:0 0 ${padBottom}px 0;font-size:0;line-height:0;">` +
      `<table role="presentation" border="0" cellpadding="0" cellspacing="0" width="${w}" style="width:${w}px;border-collapse:collapse;">` +
      // `height` as an attribute AND in the style: Word reads the attribute and
      // ignores the declaration, every other client does the opposite, and a row
      // with only one of the two collapses in whichever client reads the other.
      `<tr><td height="${h}" style="height:${h}px;line-height:${h}px;font-size:0;background-color:${fill};border-radius:${Math.round(h / 2)}px;" class="${cls}">&nbsp;</td></tr>` +
      '</table></td></tr>'
    )
  })
  const arcInnerW = MARK.arcW - MARK.arcStroke
  const arcInnerH = MARK.arcH - 2 * MARK.arcStroke
  return [
    '<table role="presentation" border="0" cellpadding="0" cellspacing="0" style="border-collapse:collapse;"><tr>',
    '  <td valign="middle" style="padding:0;font-size:0;line-height:0;">',
    '    <table role="presentation" border="0" cellpadding="0" cellspacing="0" style="border-collapse:collapse;">',
    `      ${rows.join('')}`,
    '    </table>',
    '  </td>',
    '  <td valign="middle" style="padding:0;font-size:0;line-height:0;">',
    `    <div class="${CLASS.arc}" style="width:${arcInnerW}px;height:${arcInnerH}px;border:${MARK.arcStroke}px solid ${t('text')};border-left:0;border-radius:0 ${MARK.arcH}px ${MARK.arcH}px 0;font-size:0;line-height:0;">&nbsp;</div>`,
    '  </td>',
    '</tr></table>',
  ].join('\n')
}

// --- blocks ------------------------------------------------------------------
// A template is a list of blocks. Both renderers walk the SAME list, so an HTML
// body and its plain-text twin cannot say different things: the text version is
// not a summary of the email, it is the email.

function blockHtml(b, L, brand) {
  const t = (k) => css(L[k], k)
  const f = (k) => css(brand.font[k], `font.${k}`)
  const r = (k) => css(brand.radius[k], `radius.${k}`)

  switch (b.t) {
    case 'h1':
      return `        <tr><td class="${CLASS.text}" style="padding:0 0 16px 0;font-family:${f('display')};font-size:${PX.h1}px;line-height:1.3;font-weight:600;color:${t('text')};">${b.text}</td></tr>`

    // Section headings. Added after LOOKING at the rendered licence email, where
    // "Install it", "Download the enterprise binary" and "Your licence key" came
    // out as body paragraphs and the message read as one flat column with no way
    // in. They were headings in the copy all along; only the renderer disagreed.
    case 'h2':
      return `        <tr><td class="${CLASS.text}" style="padding:4px 0 10px 0;font-family:${f('display')};font-size:${PX.h2}px;line-height:1.35;font-weight:600;color:${t('text')};">${b.text}</td></tr>`

    case 'p':
      return `        <tr><td class="${b.muted ? CLASS.muted : CLASS.text}" style="padding:0 0 16px 0;font-family:${f('sans')};font-size:${PX.body}px;line-height:1.55;color:${b.muted ? t('textMuted') : t('text')};">${b.text}</td></tr>`

    case 'small':
      return `        <tr><td class="${CLASS.muted}" style="padding:0 0 12px 0;font-family:${f('sans')};font-size:${PX.small}px;line-height:1.5;color:${t('textMuted')};">${b.text}</td></tr>`

    // The one orange. A table cell rather than a padded <a>, because Word drops
    // padding on inline elements and the button would collapse to bare link text
    // in every desktop Outlook.
    //
    // TWO SHAPES, AND THE SECOND ONE EXISTS SO THE FIRST STAYS RARE. Brand rule
    // BRAND-02(d) ("naranja escaso") spends the accent once: in the mark it
    // is exactly one row, and in a message it is the ONE action being asked for.
    // The licence email now offers two destinations — the binary and the portal —
    // and painting both orange would spend it twice and flatten the hierarchy at
    // the same time. `secondary` is the outlined form: card fill, hairline
    // border, ink label. Which of the two a destination gets is decided by the
    // template, not here, because it depends on what the message is FOR: with a
    // download link the download is primary, and without one the portal is.
    case 'button': {
      const secondary = b.variant === 'secondary'
      const cellClass = secondary ? CLASS.btn2Cell : CLASS.btnCell
      const cellStyle = secondary
        ? `background-color:${t('surface')};border:1px solid ${t('border')};border-radius:${r('md')};`
        : `background-color:${t('accent')};border-radius:${r('md')};`
      const labelClass = secondary ? CLASS.text : CLASS.btnText
      const labelColour = secondary ? t('text') : t('accentText')
      return [
        `        <tr><td style="padding:8px 0 24px 0;">`,
        `          <table role="presentation" border="0" cellpadding="0" cellspacing="0"><tr>`,
        `            <td class="${cellClass}" style="${cellStyle}">`,
        `              <a class="${labelClass}" href="${b.href}" style="display:inline-block;padding:13px 26px;font-family:${f('sans')};font-size:${PX.body}px;font-weight:600;line-height:1.2;color:${labelColour};text-decoration:none;">${b.label}</a>`,
        `            </td>`,
        `          </tr></table>`,
        `        </td></tr>`,
      ].join('\n')
    }

    // Shell commands and licence keys. `word-break` is what stops a 700-character
    // signed blob from forcing a horizontal scrollbar on a phone.
    case 'pre':
      return `        <tr><td style="padding:0 0 16px 0;"><div class="${CLASS.well}" style="background-color:${t('well')};border:1px solid ${t('border')};border-radius:${r('sm')};padding:12px 14px;"><pre class="${CLASS.text}" style="margin:0;font-family:${f('mono')};font-size:${PX.code}px;line-height:1.5;color:${t('text')};white-space:pre-wrap;word-break:break-all;">${b.text}</pre></div></td></tr>`

    // The fallback address, printed in full. Never shortened, never wrapped in
    // link text that hides where it goes: a customer who cannot click must be
    // able to read and copy exactly what they are about to visit.
    case 'link':
      return [
        `        <tr><td class="${CLASS.muted}" style="padding:0 0 6px 0;font-family:${f('sans')};font-size:${PX.small}px;line-height:1.5;color:${t('textMuted')};">${b.intro}</td></tr>`,
        `        <tr><td class="${CLASS.muted}" style="padding:0 0 16px 0;font-family:${f('mono')};font-size:${PX.small}px;line-height:1.5;color:${t('textMuted')};word-break:break-all;">${b.href}</td></tr>`,
      ].join('\n')

    case 'rule':
      return `        <tr><td style="padding:8px 0 20px 0;"><div class="${CLASS.rule}" style="height:1px;line-height:1px;font-size:0;background-color:${t('border')};">&nbsp;</div></td></tr>`

    default:
      throw new Error(`email/layout: unknown block ${JSON.stringify(b.t)}`)
  }
}

// A plain-text email that says "use the button below" is a broken email: there is
// no button. Blocks therefore carry an optional `textAlt` (the same sentence
// written for a reader with no HTML) and an optional `htmlOnly` (the fallback
// address block, which in plain text would print the URL a second time directly
// under the copy that already printed it).
function blockText(b) {
  if (b.htmlOnly) return []
  const say = b.textAlt ?? b.text
  switch (b.t) {
    case 'h1':
      return [say, '='.repeat(Math.min(say.length, 60)), '']
    case 'h2':
      return [say, '-'.repeat(Math.min(say.length, 60)), '']
    case 'p':
    case 'small':
      return [say, '']
    // A button has no plain-text form, so it becomes what it always was: a label
    // and the address it goes to, on its own line so no client can wrap it.
    case 'button':
      return [`${b.label}:`, b.href, '']
    // Commands are indented because that is how a reader tells them from prose. A
    // licence key is NOT: it is copied out of the message and pasted into a file,
    // and two leading spaces per line would corrupt every line of the blob.
    case 'pre':
      return b.indent === false
        ? [...b.text.split('\n'), '']
        : [...b.text.split('\n').map((l) => `  ${l}`), '']
    case 'link':
      return [b.intro, b.href, '']
    case 'rule':
      return ['--', '']
    default:
      throw new Error(`email/layout: unknown block ${JSON.stringify(b.t)}`)
  }
}

/**
 * Render one template to a complete HTML document.
 *
 * `blocks` carry BUILD-TIME text that is already HTML-escaped by the caller;
 * {{PLACEHOLDER}} markers pass through untouched and are escaped by the runtime
 * when it substitutes a real value.
 */
export function renderHtml({ blocks, preheader, wordmark, footer }, brand) {
  const L = brand.theme.light
  const t = (k) => css(L[k], k)
  const f = (k) => css(brand.font[k], `font.${k}`)
  const r = (k) => css(brand.radius[k], `radius.${k}`)

  return [
    '<!DOCTYPE html>',
    '<html lang="{{LANG}}" dir="{{DIR}}">',
    '  <head>',
    '    <meta charset="utf-8">',
    '    <meta name="viewport" content="width=device-width, initial-scale=1">',
    // Declares the message renders in both schemes so clients stop force-inverting.
    '    <meta name="color-scheme" content="light dark">',
    '    <meta name="supported-color-schemes" content="light dark">',
    '    <title>{{SUBJECT}}</title>',
    '    <style>',
    '      :root { color-scheme: light dark; supported-color-schemes: light dark; }',
    '      @media (prefers-color-scheme: dark) {',
    darkStyles(brand.theme.dark),
    '      }',
    // Small screens: the 600px card becomes fluid and the side gutter shrinks.
    '      @media only screen and (max-width: 620px) {',
    '        .o-shell { width: 100% !important; }',
    '        .o-pad { padding-left: 20px !important; padding-right: 20px !important; }',
    '      }',
    '    </style>',
    '  </head>',
    `  <body class="${CLASS.canvas}" style="margin:0;padding:0;background-color:${t('canvas')};">`,
    // The preview line, then a run of zero-width joiners that eats the body copy
    // the inbox list would otherwise append to it.
    `    <div style="display:none;max-height:0;overflow:hidden;mso-hide:all;">${preheader}</div>`,
    `    <div style="display:none;max-height:0;overflow:hidden;mso-hide:all;">${'&#8204;&nbsp;'.repeat(30)}</div>`,
    `    <table role="presentation" border="0" cellpadding="0" cellspacing="0" width="100%" class="${CLASS.canvas}" style="background-color:${t('canvas')};">`,
    '      <tr><td align="center" style="padding:32px 12px;">',
    `        <table role="presentation" border="0" cellpadding="0" cellspacing="0" width="600" class="o-shell ${CLASS.card}" style="width:600px;max-width:600px;background-color:${t('surface')};border:1px solid ${t('border')};border-radius:${r('lg')};">`,
    '          <tr><td class="o-pad" style="padding:28px 32px 0 32px;">',
    '            <table role="presentation" border="0" cellpadding="0" cellspacing="0" width="100%">',
    // The brand header: the LOCKUP — mark then wordmark — not a line of text.
    // Until 2026-08-27 this row was the wordmark ALONE, and the message opened
    // looking like any transactional template with our name typed at the top.
    // The mark is drawn out of table cells (markHtml above), so it costs no
    // request, is not blocked by an images-off client, and is not an image the
    // gate would refuse.
    //
    // The wordmark stays in the display stack and in the TEXT colour, never the
    // accent — brand manual BRAND-02(d) — and the mark beside it is already
    // spending the single row of orange that same rule allows the glyph.
    '        <tr><td style="padding:0 0 20px 0;">',
    '          <table role="presentation" border="0" cellpadding="0" cellspacing="0" style="border-collapse:collapse;"><tr>',
    '            <td valign="middle" style="padding:0 11px 0 0;font-size:0;line-height:0;">',
    markHtml(L),
    '            </td>',
    `            <td valign="middle" class="${CLASS.text}" style="font-family:${f('display')};font-size:${PX.wordmark}px;line-height:1.2;font-weight:600;letter-spacing:0.01em;color:${t('text')};">${wordmark}</td>`,
    '          </tr></table>',
    '        </td></tr>',
    `        <tr><td style="padding:0 0 24px 0;"><div class="${CLASS.rule}" style="height:1px;line-height:1px;font-size:0;background-color:${t('border')};">&nbsp;</div></td></tr>`,
    ...blocks.map((b) => blockHtml(b, L, brand)),
    `        <tr><td style="padding:4px 0 18px 0;"><div class="${CLASS.rule}" style="height:1px;line-height:1px;font-size:0;background-color:${t('border')};">&nbsp;</div></td></tr>`,
    `        <tr><td class="${CLASS.muted}" style="padding:0 0 28px 0;font-family:${f('sans')};font-size:12px;line-height:1.5;color:${t('textMuted')};">${footer}</td></tr>`,
    '            </table>',
    '          </td></tr>',
    '        </table>',
    '      </td></tr>',
    '    </table>',
    '  </body>',
    '</html>',
  ].join('\n')
}

/**
 * Render the SAME blocks to plain text.
 *
 * Nothing is hard-wrapped, on purpose. Wrapping needs a word boundary, and
 * Japanese and Chinese do not put spaces at theirs — a 78-column wrap would cut
 * mid-word in two of the seven locales. Worse, the one thing a wrapped
 * transactional email reliably destroys is the URL, which is the only part that
 * has to survive. Clients soft-wrap; we do not hard-wrap.
 */
export function renderText({ blocks, wordmark, footer, signoff }) {
  const out = [wordmark, '']
  for (const b of blocks) out.push(...blockText(b))
  out.push('--', signoff, footer, '')
  return out.join('\n')
}
