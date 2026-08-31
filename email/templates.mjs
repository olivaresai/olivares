// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// templates.mjs — WHAT each of the four bodies says, as a list of blocks.
// layout.mjs decides how a block looks; this file decides which blocks there are.
//
// The strings arriving in `c` are the locale's copy, already HTML-escaped by
// build.mjs. {{PLACEHOLDER}} markers are literal ASCII and survive escaping
// untouched; the runtime escapes the values it substitutes for them.
//
// ON VARIANTS RATHER THAN CONDITIONALS. The licence email exists in two shapes,
// with and without a download link, and they are emitted as two finished
// templates instead of one template with an `{{#if}}`. A conditional would need a
// template engine, and a template engine would have to be implemented once in
// TypeScript and once in Go — two engines that agree today, which is the exact
// defect this whole directory exists to close. Picking one of two strings is a
// ternary, and a ternary cannot drift.

// Commands are product surface and identical in all seven locales. A command block
// therefore holds COMMANDS AND NOTHING ELSE.
//
// It used to hold this instead:
//   olivares license install olivares-license.key
//   # or: olivares license install -   (paste the key, then Ctrl-D)
// One line doing two jobs — offering a second real form (cmd_license.go:197-199
// reads stdin when the argument is `-`) and explaining it in English prose. That
// line could neither be translated where it stood nor left alone: translating it
// puts prose on a line a reader is meant to use, and leaving it ships English into
// six other languages. Splitting is the only move that is right in both directions.
const CMD_INSTALL = 'olivares license install olivares-license.key'
const CMD_INSTALL_STDIN = 'olivares license install -'
const CMD_VERIFY = 'olivares license verify "$(cat olivares-license.key)"'

const MAGIC_LINK_MINUTES = '15'

// --- the licence email, and the two things it may or may not carry -----------
//
// TWO INDEPENDENT AXES, FOUR FINISHED TEMPLATES. The licence email can carry a
// download link and can carry a portal link, and neither implies the other:
//
//                          | portal configured | portal not configured
//   download link minted   | license           | licenseNoPortal
//   no download link       | licenseNoDownload | licenseNoDownloadNoPortal
//
// Four templates and not one template with two `{{#if}}`s, for the reason stated
// at the top of this file and not re-derived here: a conditional needs an engine,
// an engine has to exist once in TypeScript and once in Go, and two engines that
// agree today are the exact defect this directory closes. The runtime's whole job
// stays "pick one of four and substitute", which is a pair of ternaries in
// delivery/resend.ts and cannot drift.
//
// The cost was measured before choosing it rather than waved past: the generated
// worker bundle is 13 kB gzipped for 21 (template, locale) renders, so the 14 new
// renders add roughly 9 kB gzipped to a Worker whose ceiling is megabytes.
//
// THE PORTAL LINK IS BACK, AND WHY IT LEFT IS THE REASON IT CAN RETURN. An
// earlier version of the no-download copy named the customer portal as the
// recovery route, and it was retired — correctly — because that sentence was NOT
// TRUE IN EVERY STATE THAT COULD SEND IT: PORTAL_SESSION_SECRET is a separate
// prerequisite, and an absent PUBLIC_BASE_URL yields a magic-link URL this
// Worker's own assertHttpUrl rejects. Nothing about that reasoning was wrong.
// What was missing was a GUARD, and a guard cannot live in a static template —
// so the fix is a fourth template rather than a braver sentence. The Worker
// answers the question once, in delivery/deliver.ts::buildPortalUrl, and picks
// the shape that matches the answer. A deployment with no portal still promises
// nothing; a deployment with one stops hiding it.
//
// WHAT THE NO-DOWNLOAD SHAPES ARE, because it is not a product state:
// buildDownloadUrl returns null only when the Worker's own download configuration
// is incomplete, and its call site is unconditional on every purchase. The buyer
// has paid for the binary and is entitled to it; the email omits it because WE
// are misconfigured. The copy therefore says the licence is attached and active —
// which is what the customer most needs to know and what the old bare sentence
// left them to guess — and names the portal only in the shape where there is one.
// The real fix is still to make incomplete download configuration a RETRYABLE
// FULFILMENT FAILURE instead of an acknowledged delivery; that lives in
// commercial/ and is owned there.
function licenseBlocks(c, { download, portal }) {
  return [
    { t: 'h1', text: c.license.heading },
    { t: 'p', text: c.license.greeting },
    { t: 'p', text: c.license.intro },
    { t: 'p', text: '{{EXPIRY_STATUS}}' },
    { t: 'rule' },
    ...(download
      ? [
          { t: 'h2', text: c.license.downloadHeading },
          { t: 'p', text: c.license.downloadNote },
          { t: 'button', label: c.license.downloadCta, href: '{{DOWNLOAD_URL}}' },
          // The upgrade command is GONE, and its absence is the fix.
          //
          // It arrived to answer a first review: the email printed
          // `olivares upgrade --enterprise`, which exits without --token
          // (cmd_upgrade.go:658). Adding the token made the sentence true and the
          // PATH still false at the time. A later fix taught the gate to
          // read `kind`, so the manifest route now works — but the command still
          // needs a token this email would have to print twice, and
          // cmd_upgrade.go:323-327 refuses --enterprise until a licence is already
          // installed, which is AFTER this section.
          //
          // The button above already delivers the binary, and the portal section
          // below is the durable route. An email should not print a command whose
          // end-to-end path nobody has proven.
        ]
      : [{ t: 'p', text: portal ? c.license.noDownloadPortalNote : c.license.noDownloadNote }]),
    ...(portal
      ? [
          { t: 'rule' },
          { t: 'h2', text: c.license.portalHeading },
          { t: 'p', text: c.license.portalNote },
          // Primary when it is the only destination, outlined when the download
          // is. See the `button` case in layout.mjs for why the accent is spent
          // once per message.
          {
            t: 'button',
            label: c.license.portalCta,
            href: '{{PORTAL_URL}}',
            ...(download ? { variant: 'secondary' } : {}),
          },
          // The portal address, printed in full — and the DOWNLOAD link is not,
          // which is a deliberate asymmetry rather than an oversight. A gated
          // download URL is a two-hundred-character signed token that nobody
          // retypes; the portal address is a permanent one a customer will want to
          // keep. Printing the first would be noise, printing the second is the
          // difference between a message and a bookmark.
          { t: 'link', intro: c.common.fallbackIntro, href: '{{PORTAL_URL}}', htmlOnly: true },
        ]
      : []),
    { t: 'rule' },
    { t: 'h2', text: c.license.installHeading },
    { t: 'p', text: c.license.installNote },
    { t: 'pre', text: CMD_INSTALL },
    { t: 'small', text: c.license.installStdinNote },
    { t: 'pre', text: CMD_INSTALL_STDIN },
    { t: 'p', text: c.license.verifyNote },
    { t: 'pre', text: CMD_VERIFY },
    { t: 'rule' },
    { t: 'h2', text: c.license.keyHeading },
    { t: 'pre', text: '{{LICENSE_KEY}}', indent: false },
    { t: 'small', text: c.license.attestation },
  ]
}

/**
 * The four licence shapes, generated from ONE block list.
 *
 * The shapes are data — a name and two booleans — so the matrix in the comment
 * above and the templates that implement it are the same thing. Writing the four
 * out by hand would be four copies of a body that must not diverge, which is the
 * failure this whole directory exists to prevent, reintroduced inside it.
 *
 * `preheaderNoDownload` is deliberately shared by BOTH no-download shapes: it
 * states only that the key is ready and the download link is not in the message,
 * which is true whether or not a portal exists. A portal-specific preview line
 * would be a seventh sentence to keep true for no gain in the one place a reader
 * sees three words of it.
 */
const LICENSE_SHAPES = [
  { id: 'license', download: true, portal: true },
  { id: 'licenseNoPortal', download: true, portal: false },
  { id: 'licenseNoDownload', download: false, portal: true },
  { id: 'licenseNoDownloadNoPortal', download: false, portal: false },
]

function licenseVariants() {
  const out = {}
  for (const shape of LICENSE_SHAPES) {
    out[shape.id] = {
      runtime: 'worker',
      subject: (c) => c.license.subject,
      build: (c) => ({
        preheader: shape.download ? c.license.preheader : c.license.preheaderNoDownload,
        blocks: licenseBlocks(c, shape),
      }),
    }
  }
  return out
}

/**
 * Every template, keyed by id. `runtime` says which bundle it is emitted into:
 * `worker` for the Cloudflare licence worker, `core` for the Go engine. Nothing
 * is emitted into a runtime that has no call site for it — a template shipped to
 * a bundle that never renders it is dead weight in a Worker's size budget.
 */
export const TEMPLATES = {
  signin: {
    runtime: 'worker',
    subject: (c) => c.signin.subject,
    build: (c) => ({
      preheader: c.signin.preheader,
      blocks: [
        { t: 'h1', text: c.signin.heading },
        { t: 'p', text: c.signin.intro, textAlt: c.signin.textIntro },
        { t: 'button', label: c.signin.cta, href: '{{VERIFY_URL}}' },
        { t: 'small', text: c.signin.expiry.replace('{{MINUTES}}', MAGIC_LINK_MINUTES) },
        // The link is bound to a cookie set on the browser that asked for it
        // (auth/magic-link.ts:98-100, checked at portal/handler.ts:325-330), and
        // the rejection redirects to a page that says "invalid or has expired" —
        // so a customer opening it elsewhere reads the wrong cause and requests
        // another link that fails the same way. The behaviour is right; it just
        // was not said anywhere.
        { t: 'small', text: c.signin.sameBrowser },
        { t: 'link', intro: c.common.fallbackIntro, href: '{{VERIFY_URL}}', htmlOnly: true },
        { t: 'rule' },
        { t: 'small', text: c.signin.ignore },
      ],
    }),
  },

  ...licenseVariants(),

  // The one that did not exist. core/api declares InviteSender and defines no
  // body, so whoever wires it invents the email — which is not a brand problem
  // but the absence of one.
  invite: {
    runtime: 'core',
    subject: (c) => c.invite.subject,
    build: (c) => ({
      preheader: c.invite.preheader,
      blocks: [
        { t: 'h1', text: c.invite.heading },
        { t: 'p', text: c.invite.intro, textAlt: c.invite.textIntro },
        { t: 'button', label: c.invite.cta, href: '{{ACCEPT_URL}}' },
        { t: 'p', text: c.invite.setPassword },
        { t: 'small', text: c.invite.expiry },
        { t: 'link', intro: c.common.fallbackIntro, href: '{{ACCEPT_URL}}', htmlOnly: true },
        { t: 'rule' },
        { t: 'small', text: c.invite.ignore },
      ],
    }),
  },
}

/**
 * Fragments the runtime picks between and substitutes itself. They are copy, so
 * they are translated; they are not whole templates, so they live here.
 * `statusUntil` keeps its {{DATE}} marker for the runtime to fill.
 */
/**
 * Markers each runtime fragment must carry — exactly, in both directions. Nothing
 * validated these until an independent review pointed out that a typo turning
 * {{DATE}} into {{DAT}} would ship literal braces in a licence email.
 */
export const RUNTIME_FRAGMENT_MARKERS = {
  statusPerpetual: [],
  statusUntil: ['DATE'],
}

export const RUNTIME_STRINGS = {
  worker: (c) => ({
    statusPerpetual: c.license.statusPerpetual,
    statusUntil: c.license.statusUntil,
  }),
  core: () => ({}),
}

/**
 * Placeholders each rendered template is allowed to contain — the union of the
 * ones the block list writes and the ones a copy string carries (LICENSEE lives
 * inside `license.greeting`, not in a block). The build asserts the rendered
 * output against this set in BOTH directions: an unknown marker would ship
 * `{{FOO}}` to a customer, and a missing one means the runtime is substituting
 * something the template stopped asking for.
 */
/**
 * The licence shapes' placeholder sets are DERIVED from the same two booleans
 * that build their bodies, not listed again. A hand-written second list is how a
 * template grows a marker its contract does not declare — and build.mjs asserts
 * the rendered output against this set in both directions, so the two lists
 * disagreeing would be a red gate rather than a bad email, which is the good
 * outcome and still one nobody has to reach.
 */
function licensePlaceholders() {
  const out = {}
  for (const shape of LICENSE_SHAPES) {
    out[shape.id] = [
      'LICENSEE',
      'EXPIRY_STATUS',
      ...(shape.download ? ['DOWNLOAD_URL'] : []),
      ...(shape.portal ? ['PORTAL_URL'] : []),
      'LICENSE_KEY',
    ]
  }
  return out
}

export const ALLOWED_PLACEHOLDERS = {
  signin: ['VERIFY_URL'],
  ...licensePlaceholders(),
  invite: ['ACCEPT_URL', 'EXPIRES_AT'],
}
