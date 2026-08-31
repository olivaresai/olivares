// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE CLASS THIS CLOSES.
//
//   Changing a string the product PRINTS obliges you to sweep everything that CITES it.
//
// Nothing checked that. Changed two things the engine prints — the first-boot TLS
// line, and the log key `fingerprint_sha256` -> `cert_fingerprint_sha256` — swept
// `reference/`, and left `how-to/troubleshooting.md` quoting the deleted line VERBATIM
// inside a text fence and `tutorials/getting-started/single-node.mdx` telling operators
// to paste a key the binary no longer emits, in all seven locales. Its own log recorded
// the sweep as complete, honestly, because the sweep it ran WAS complete for the subtree
// it ran over.
//
// Sweeping for those turned up a third case that did not cause and nobody had
// noticed: every page documented `grep -A4 "FIRST-BOOT SETUP"`, and the banner's `Token:`
// line is the SIXTH after the header, so the documented command printed everything except
// the token it exists to obtain — 60 occurrences: eight per locale across six pages, times
// seven locales, plus four under `deploy/` including the Helm NOTES.txt an operator reads
// right after `helm install`. Three independent instances is not a coincidence, it is a
// class, and a class needs a gate.
//
// (That figure was recorded as 59 until the contrast recounted it. The error is worth
// keeping visible: the fixing script reported 59 and the sixtieth was fixed by hand
// afterwards, so the script's total was quoted as if it were the measurement. Counted with
// `grep -o … | wc -l` over the diff, both the removals and the replacements are 60.)
//
// WHAT THIS GATE ASKS, and why each question is shaped the way it is:
//
//   1. ANCHORING — every registered quotation still appears VERBATIM among the string
//      literals the engine's sources CONTAIN. This is the check that catches the class:
//      change the wording in Go, and the pages quoting it go red in the same push. Read
//      L-6 for the difference between "contains" and "emits" — it is not pedantry.
//
//   2. CITATION — every registered quotation still appears in every page and locale it
//      declares. Without this the registry rots into a list of strings nobody quotes,
//      and check 1 would be guarding nothing.
//
//   3. FENCE COMPLETENESS, over the SET — every line inside a ```text fence that quotes
//      an engine literal must be registered. This is what makes the gate prospective:
//      quotations get registered WHILE THEY ARE TRUE, so that a later edit to the Go
//      string reddens check 1. A quotation that was already stale when this was written
//      cannot be resurrected by any gate; that is why the registry was seeded by hand.
//
//   4. IDENTIFIER ANCHORING, over the SET — every snake_case token inside an inline code
//      span names something the engine actually has, or is listed as not being an engine
//      identifier at all. This is the check that would have caught `fingerprint_sha256`
//      retroactively: after renamed the key, the bare token appeared in NO engine
//      literal and in no exemption, so it fails.
//
//   5. FIXED-WINDOW BANNER CAPTURE — no documented command may select the first-boot
//      banner with a fixed `-A<n>` window smaller than the measured distance to its
//      `Token:` line. This is the only clause aimed at a COMMAND rather than a
//      quotation, and it exists because the contrast proved the other four let all 60
//      occurrences of the original defect back in unnoticed. The distance is re-derived
//      from the banner literal on every run, so the rule cannot go stale the way the
//      documentation it guards did. It reads the WHOLE repository, joins shell line
//      continuations, and accepts `--after-context=N` as well as `-A N`: every narrowing
//      of that predicate was a bypass the contrast demonstrated, including five real
//      instances outside docs-site — one of them printed by the product itself.
//
// ⚠ THE SHAPE OF CHECKS 3 AND 4 IS DELIBERATE, and it is the one lesson taken from the
// sibling gate. `check-i18n-disclaimers.mjs` ratchets on a NUMBER: a pull request that
// adds one untranslated disclaimer and translates another leaves the count identical and
// PASSES. Neither ratchet here is a count — but they are not the same shape, and saying
// "both compare sets" was itself an overclaim the contrast had to correct:
//
//   * Check 4 IS set equality, in both directions. An exemption added while another is
//     removed fails, because the sets differ though the counts do not; an exemption that
//     stopped being necessary fails on its own.
//   * Check 3 is a COVERAGE relation, not equality: every discovered fence line must be
//     covered by some registered quotation. It has to be, because one registered
//     multi-line chunk legitimately covers several discovered lines. It is still not a
//     count — a newly quoted engine line fails until somebody registers it — but an
//     equality it is not, and calling it one would be the kind of claim this gate exists
//     to catch.
//
// ⚠ AND IT READS LITERALS, NOT SOURCE TEXT. Measured while writing this: after the
// bare token `fingerprint_sha256` still occurs twice in the engine tree, and BOTH are
// inside comments that describe the defect — cmd_serve.go:544 and, in a file this gate
// does not even read, tlspin_test.go:33. A gate grepping raw source would have found the
// first and reported green about a key the binary stopped emitting — the same trap the
// sibling gate documents hitting. (Those two line numbers are as of this commit and will
// drift; the claim to re-check is "both occurrences are comments", not the digits.) So the Go sources
// are lexed: comments are dropped, string literals are collected, and adjacent literals
// joined by `+` are concatenated the way the compiler concatenates them (the first-boot
// TLS line is written across two source lines and exists as one string only after that).
// Cost of dropping comments, measured over the living docs: 2 tokens of 253, and both are
// genuinely not engine identifiers.
//
// WHAT THIS DOES NOT CATCH — stated, because a gate whose limits are unwritten gets read
// as covering everything, and the session that wrote it is the last one that knows:
//
//   L-1  Checks 3 and 4 only read ```text fences and inline code spans in the living
//        docs. Measured today: every line that looks like engine output is in a ```text
//        fence, and no other fence language carries any — but that is a CONVENTION, not
//        an invariant. Output pasted into a ```console fence is invisible to them.
//        (Check 5 is not so limited: it reads every non-binary text file in the
//        repository, regardless of fence, extension or directory.)
//   L-2  It compares STRINGS. Documentation that PARAPHRASES what the engine says ("the
//        engine warns about the key") is outside its reach, and always will be.
//   L-7  Check 4 proves an identifier EXISTS in the comment-free code — not that it is an
//        EMITTED key. It searches declarations, locals and inactive build files alike, so
//        `fingerprint_sha256 := sha256.Sum256(cert)` anywhere would keep a page claiming
//        the engine logs `fingerprint_sha256` green. It caught the real rename because the
//        token vanished from the code entirely; it does not catch the rename class in
//        general, and saying otherwise would be the overclaim this file keeps having to
//        correct in itself.
//   L-3  Quotations shorter than MIN_QUOTED_LENGTH are not required to register, so a
//        short banner header changing is caught only through the longer lines around it.
//   L-4  Constants are resolved per package directory. A message assembled from a
//        constant in ANOTHER package resolves to nothing and the sentence is invisible;
//        the failure is a false RED (an anchored quotation reported missing), which is
//        the safe direction, but it will read as a false alarm to whoever hits it.
//   L-5  It cannot resurrect a quotation that was ALREADY stale when it was written. That
//        is not a defect of the mechanism, it is the mechanism: checks 3 and 4 exist to
//        register things while they are true. The seed registry was built by hand.
//   L-6  "CONTAINS a literal" is weaker than "EMITS it", and check 1 can only ask the
//        first. Three false greens follow, all named by the contrast and all stated here
//        rather than papered over, because the success line used to say "emits":
//          (a) NO REACHABILITY. An unused package-level constant still carrying the OLD
//              wording keeps a stale quotation anchored and green.
//          (b) LEXICAL SCOPE — CLOSED by narrowing the contract, not by documenting the
//              wrong answer: only PACKAGE-LEVEL constants are collected now, so a
//              function-local `const msg` can no longer overwrite the package one and
//              synthesise a message the compiler never builds. It is NOT fully safe, and
//              the first version of this note claimed it was: two package-level constants
//              of one name in a directory cannot both compile IN THE SAME BUILD, but
//              mutually exclusive `//go:build` files can each declare one, and this merges
//              them and lets the last win. So a message can still be synthesised from a
//              constant that no single build sees. Unresolved, like a constant from
//              ANOTHER package (L-4).
//          (c) NO RUNTIME ASSEMBLY. A line built by `fmt.Sprintf("tenant %s is down", id)`
//              exists as no compile-time string, so it can never be discovered by check 3
//              or anchored by check 1.
//        Closing any of these means type-checking Go, which is a different tool. What is
//        NOT acceptable is a gate that implies it already did.
//
// Pure Node, no dependencies, like the other docs/i18n gates, so it runs inside the
// Go-toolchain leg. Its red cases run FIRST (`--self-test`) on throwaway trees under
// TMPDIR: a gate nobody has watched fail is a gate that reports green because it looked
// at nothing. Run from the repository root.

import { execFileSync } from 'node:child_process'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import process from 'node:process'
import { fileURLToPath } from 'node:url'

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')

// The living locales. `2026-06` is deliberately absent: it is a DATED SNAPSHOT of what
// the documentation said on that date, and on that date the engine did print those lines.
// Rewriting it would be falsifying a record, so it is excluded here rather than exempted.
const LOCALES = ['', 'de', 'es', 'fr', 'ja', 'ru', 'zh']
const FROZEN_TREES = ['2026-06']

// The Go trees whose strings reach a user: the engine, the modules, the connectors, the
// CLIs and the providers the documentation writes about.
const GO_ROOTS = [
  'cmd/olivares',
  'core',
  'modules',
  'connectors',
  'sdk',
  'operator',
  'clients/go',
  'terraform-provider-olivares',
]

// Shorter than this inside a fence is a fragment, a prompt or a column header, not a
// quotation of a sentence the product emits.
const MIN_QUOTED_LENGTH = 40

// --- the registry ----------------------------------------------------------------
//
// Every entry is a string the engine emits that the documentation quotes. `emitted` must
// appear verbatim in a literal of `source`; `cited` lists the pages that must contain it,
// checked in all seven living locales (a fenced console line is the same English
// everywhere — it is terminal output, not prose).
const CITATIONS = [
  {
    id: 'tls-selfsigned-first-boot',
    emitted:
      'generated a self-signed TLS certificate; clients must trust it, or pin it with ' +
      '--pin-sha256=<pin_sha256> (that value, verbatim)',
    source: 'cmd/olivares/cmd_serve.go',
    cited: ['how-to/troubleshooting.md'],
    why:
      'This line was reworded once and troubleshooting.md went on quoting the deleted one. The ' +
      'wording is also shared with the every-other-boot line via the pinAdvice constant, ' +
      'so editing it here without editing the pages is the exact mistake this gate exists for.',
  },
  {
    id: 'audit-signing-key-first-boot',
    emitted: 'generated a new audit signing key; back it up',
    source: 'cmd/olivares/boot.go',
    cited: ['how-to/troubleshooting.md'],
    why: 'Quoted beside the TLS line in the same fence; the page is about what these warnings mean.',
  },
  {
    id: 'first-boot-banner-transport',
    emitted:
      'The console serves HTTPS with a self-signed certificate on first boot — your\n' +
      'browser will warn once; that is expected. ',
    source: 'cmd/olivares/cmd_serve.go',
    cited: ['how-to/self-hosting.md', 'tutorials/getting-started/single-node.mdx'],
    why:
      'The banner interpolates this paragraph, and it is the one that changes under --insecure. ' +
      'Anchored separately because it is a separate string in the source.',
  },
  {
    id: 'ingest-wired-source',
    emitted: 'ingest: wired source (in-process fast-path)',
    source: 'cmd/olivares/sources.go',
    cited: ['tutorials/getting-started/single-node.mdx'],
    why: 'The tutorial tells the operator this is the line proving their first source is wired.',
  },
  // The banner is registered as its three PLACEHOLDER-FREE CHUNKS, not as four convenient
  // lines. The first version registered two single lines and looked complete; splitting
  // the literals into lines showed four more banner lines quoted in every locale and
  // anchored to nothing. A chunk is the honest unit: it is exactly as much text as the
  // engine emits contiguously, so nothing between the placeholders can drift unnoticed.
  {
    id: 'first-boot-banner-opening',
    emitted:
      '=== FIRST-BOOT SETUP ===\n' +
      'No accounts exist yet. Open the console and create the first administrator\n' +
      'with this one-time token — setup also creates your first organization and\n' +
      'makes that administrator its owner:',
    source: 'cmd/olivares/cmd_serve.go',
    cited: ['how-to/self-hosting.md', 'tutorials/getting-started/single-node.mdx'],
    why:
      'Both pages reproduce the setup banner. They quoted an older one ("No users exist yet…") ' +
      'for long enough that the capture command beneath it had gone wrong unnoticed.',
  },
  {
    id: 'first-boot-banner-tail',
    emitted:
      'The token is shown ONCE and is\n' +
      'single-use. Prefer the API? POST /v1/setup {"token":"…","email":"…",\n' +
      '"password":"…"} — add "organization":"…" to name it (default: "Default\n' +
      "Organization\"). The reply carries the new organization's tenant_id.",
    source: 'cmd/olivares/cmd_serve.go',
    cited: ['how-to/self-hosting.md', 'tutorials/getting-started/single-node.mdx'],
    why: 'The tail of the same banner: it changes independently of the opening, so it is anchored separately.',
  },
  {
    id: 'no-sources-configured',
    emitted:
      'ingest: no observation sources configured (OLIVARES_SOURCES_CONFIG.sources is empty); ' +
      'no connector will ingest — the estate runs on no live traffic',
    source: 'cmd/olivares/sources.go',
    cited: ['how-to/troubleshooting.md'],
    why: 'The page tells an operator to look for this exact line when the access map is empty.',
  },
  //the three integration guides (Claude Code, Codex, Grok) and their six locales.
  // Every one of these was read back from the binary before it was registered, not copied
  // out of the prose: a citation registered while it is FALSE anchors nothing, and a
  // citation registered while it is TRUE is what turns this gate red on the next edit to
  // the Go string. That is the whole mechanism, and it only works in that order.
  {
    id: 'sources-reload-hint',
    emitted:
      '\u2192 reload a running engine to apply: POST /v1/console/runtime/reload, ' +
      'or `kill -HUP <pid>` (it also applies at next boot)',
    source: 'cmd/olivares/cmd_sources.go',
    cited: [
      'how-to/integrations/claude-code.md',
      'how-to/integrations/codex.md',
      'how-to/integrations/grok.md',
    ],
    why:
      'All three guides end their "wire the connector" step on this line. It is the only ' +
      'place the reader is told the change applies WITHOUT a restart, so if the engine ' +
      'ever stops offering the live path the guides must stop promising it.',
  },
  {
    id: 'sources-validate-ok',
    emitted: 'configuration: VALID (everything that can be decided without the network)',
    source: 'cmd/olivares/cmd_sources_plan.go',
    cited: [
      'how-to/integrations/claude-code.md',
      'how-to/integrations/codex.md',
      'how-to/integrations/grok.md',
    ],
    why:
      'The parenthesis is the load-bearing half: it is what tells the reader a green ' +
      '`validate` has NOT talked to their source yet. Registered whole for that reason — ' +
      'trimming it to "configuration: VALID" would leave the guides promising more.',
  },
  {
    id: 'sources-plan-nothing-written',
    emitted: 'NO SOURCE ROW WAS WRITTEN and nothing was wired into a running engine.',
    source: 'cmd/olivares/cmd_sources_plan.go',
    cited: [
      'how-to/integrations/claude-code.md',
      'how-to/integrations/codex.md',
      'how-to/integrations/grok.md',
    ],
    why:
      'The guides use this line to prove `plan` is safe to run against production. If the ' +
      'engine ever starts writing on plan, this citation is how the docs find out.',
  },
  {
    id: 'codex-managed-config-ok',
    emitted: 'ok: policy renders to valid Codex managed-config TOML',
    source: 'cmd/olivares/cmd_codexmanagedconfig.go',
    cited: ['how-to/integrations/codex.md'],
    why: 'The Codex guide tells the reader this is the line that confirms the rendered TOML is accepted.',
  },
  {
    id: 'grok-hook-no-endpoint-deny',
    emitted: 'no governance endpoint is configured (deny-closed)',
    source: 'connectors/grok/session/hookclient.go',
    cited: ['how-to/integrations/grok.md'],
    why:
      'The deny-closed proof in the Grok guide: with no governance endpoint the hook denies ' +
      'rather than failing open. THE REGISTRY DID ITS JOB AND THIS ENTRY IS THE RECEIPT: it ' +
      'used to hold the Spanish string the engine emitted, registered while that was TRUE and ' +
      'with a note saying the move to English would turn this gate red and name the seven ' +
      'copies. The move landed (connectors/grok in main), the gate went red naming ja, ru, zh ' +
      'and the rest, and the seven were re-cited from the Go source rather than from a message. ' +
      'It is engine output, so the same string stands in all seven: it is not translated.',
  },
]

// --- the exemptions --------------------------------------------------------------
//
// snake_case tokens the documentation writes in code spans that are NOT engine
// identifiers. Each needs a reason: the point of the list is that somebody had to look.
// It is compared as a SET — an entry that stops being needed fails the gate, so the list
// cannot quietly accumulate.
// ⛔ SIETE ENTRADAS DE AP2 RETIRADAS EL 2026-08-25, Y NO PORQUE SU RAZÓN DEJARA DE SER CIERTA.
//    `allowed_merchants`, `allowed_payees`, `allowed_payment_instruments`, `amount_range`,
//    `execution_date`, `sd_hash` y `transaction_id` SIGUEN sin ser identificadores del motor —
//    verificado: ninguno aparece en core/, modules/ ni cmd/. Lo que desapareció es el SUJETO:
//    los citaba la página publicada de ADR-0026, y la sección ADR entera salió del docs-site ese
//    día por orden de. Sin página que haga la cita, no hay nada que eximir, y este gate
//    compara la lista como un CONJUNTO: una entrada que deja de hacer falta lo pone rojo.
//    Es la segunda exención que esa retirada dejó huérfana — la otra fue `waivers.tsv:39` de
//    commerce-lint. Quien retire una superficie hereda las exenciones que la citaban.
const NON_ENGINE_IDENTIFIERS = new Map([
  ['get_v1_agents', 'generated method name in the Python SDK, which is not a Go tree'],
  ['olivares_client', 'constructor name in the Python SDK, which is not a Go tree'],
  ['on_deprecation', 'a policy value in the API-stability contract prose, not an emitted identifier'],
  ['owner_group', 'a PostgreSQL role name in the operator-side setup, owned by Postgres, not by us'],
  // The Terraform provider composes every resource type name at RUNTIME —
  // `resp.TypeName = req.ProviderTypeName + "_agent"` with ProviderTypeName "olivares"
  // (terraform-provider-olivares/internal/provider/*.go). The full name therefore exists
  // in no literal, and the suffix alone would be too weak a thing to anchor on. Verified
  // one by one against its Metadata method, not assumed from the shared prefix.
  ['olivares_access_edges', 'Terraform data source; the provider composes the name from its prefix at runtime'],
  ['olivares_agent', 'Terraform resource; name composed at runtime from ProviderTypeName'],
  ['olivares_agent_identity_binding', 'Terraform resource; name composed at runtime'],
  ['olivares_deployment', 'Terraform resource; name composed at runtime'],
  ['olivares_identities', 'Terraform data source; name composed at runtime'],
  ['olivares_policies', 'Terraform data source; name composed at runtime'],
  ['olivares_policy', 'Terraform resource; name composed at runtime'],
  ['olivares_server_info', 'Terraform data source; name composed at runtime'],
])

// --- Go lexing --------------------------------------------------------------------

// decodeEscape decodes ONE Go escape sequence at `i` (which must be a backslash) and
// returns [text, charactersConsumed].
//
// The first version knew five escapes and, for everything else, dropped the backslash and
// copied the next character — so `"\x41B\101"`, whose value is `ABA`, decoded to
// `x41u0042101`. The contrast found it by calling the lexer directly. It matters less for
// what it would MISS than for what it would INVENT: a wrong decoding is a string in the
// corpus that the engine never emits, and anchoring is exactly a question of whether a
// quotation is in that corpus.
// It returns BYTES, and that is the whole subtlety. `\x` and octal escapes in Go denote
// bytes, not code points: `"\xC3\xA9"` is two bytes that are the UTF-8 encoding of `é`.
// Decoding each to its own code point yields `Ã©` — a string the engine never emits, put
// into the corpus that anchoring searches. The first repair of this function fixed the
// arithmetic and kept that error, and its self-test used `"\x41B\101"`, pure ASCII, where
// byte and code point coincide; the fixture agreed with the bug. So literals are assembled
// as a byte sequence and decoded as UTF-8 once, at the end.
const UTF8 = new TextEncoder()
const UTF8_DECODER = new TextDecoder()
export function decodeEscape(src, i) {
  const e = src[i + 1]
  const simple = { a: 0x07, b: 0x08, f: 0x0c, n: 0x0a, r: 0x0d, t: 0x09, v: 0x0b, '\\': 0x5c, "'": 0x27, '"': 0x22 }
  if (e in simple) return [[simple[e]], 2]
  if (e === 'x') {
    const d = src.slice(i + 2, i + 4)
    if (/^[0-9a-fA-F]{2}$/.test(d)) return [[parseInt(d, 16)], 4]
  }
  if (e === 'u' || e === 'U') {
    const width = e === 'u' ? 4 : 8
    const d = src.slice(i + 2, i + 2 + width)
    if (d.length === width && /^[0-9a-fA-F]+$/.test(d)) {
      // \u and \U ARE code points, and Go encodes them as UTF-8 in the string.
      return [[...UTF8.encode(String.fromCodePoint(parseInt(d, 16)))], 2 + width]
    }
  }
  if (/^[0-7]{3}$/.test(src.slice(i + 1, i + 4))) return [[parseInt(src.slice(i + 1, i + 4), 8)], 4]
  // Not a valid escape. Copy the character; keeping this only HERE bounds the damage to
  // genuinely malformed source.
  return [e === undefined ? [] : [...UTF8.encode(e)], 2]
}

// lexGo returns the source with comments removed, and every string literal in it, with
// the offsets they occupy in that comment-free text so adjacent literals can be joined.
export function lexGo(src) {
  let code = ''
  const literals = []
  let i = 0
  const n = src.length
  while (i < n) {
    const c = src[i]
    const d = src[i + 1]
    if (c === '/' && d === '/') {
      while (i < n && src[i] !== '\n') i++
      continue
    }
    if (c === '/' && d === '*') {
      i += 2
      while (i < n && !(src[i] === '*' && src[i + 1] === '/')) i++
      i += 2
      code += ' '
      continue
    }
    if (c === '"') {
      let j = i + 1
      const bytes = []
      let terminated = false
      while (j < n) {
        if (src[j] === '\\') {
          const [decoded, width] = decodeEscape(src, j)
          bytes.push(...decoded)
          j += width
          continue
        }
        if (src[j] === '"') {
          terminated = true
          break
        }
        if (src[j] === '\n') break
        // A CODE POINT, not a UTF-16 code unit. `src[j]` on an astral character is half a
        // surrogate pair, and encoding half a pair yields U+FFFD: the byte-assembly repair
        // turned "😀" into "��" and none of its four fixtures could see it, because all
        // four were escapes. Fixing one representation and breaking the other is the same
        // mistake in the same function, one round apart.
        const ch = String.fromCodePoint(src.codePointAt(j))
        bytes.push(...UTF8.encode(ch))
        j += ch.length
      }
      if (terminated) {
        const text = UTF8_DECODER.decode(new Uint8Array(bytes))
        literals.push({ text, start: code.length, end: code.length + (j + 1 - i) })
      }
      code += src.slice(i, j + 1)
      i = j + 1
      continue
    }
    if (c === '`') {
      let j = i + 1
      while (j < n && src[j] !== '`') j++
      literals.push({ text: src.slice(i + 1, j), start: code.length, end: code.length + (j + 1 - i) })
      code += src.slice(i, j + 1)
      i = j + 1
      continue
    }
    if (c === "'") {
      // Rune literals are BLANKED, not copied: `const brace = '{'` is valid Go, and copying
      // it left every later declaration in the file at a false brace depth of 1, so the
      // package-level filter silently collected nothing after it. Length is preserved so
      // every offset already recorded stays correct.
      let j = i + 1
      while (j < n && src[j] !== "'") {
        if (src[j] === '\\') j++
        j++
      }
      // Same length (offsets already recorded must stay valid), braces neutralised.
      code += src.slice(i, j + 1).replace(/[{}]/g, ' ')
      i = j + 1
      continue
    }
    code += c
    i++
  }
  return { code, literals }
}

// stringConsts finds `const NAME = "…"` (and NAME = "…" inside a `const (…)` block) so a
// message assembled from a named constant is still visible as one string.
//
// This is not gold-plating, it is the gate refusing to punish good factoring. The
// first-boot advice and the every-other-boot advice are ONE constant precisely so the two
// cannot drift; a gate that only understood adjacent literals would have reported the
// sentence missing and pushed the next author to paste it twice — which is the drift the
// whole file exists to prevent. Resolution is per PACKAGE DIRECTORY, which is where Go
// constants are visible without qualification; a constant used from another package is
// not resolved, and that limit is stated rather than hidden.
export function stringConsts(code, literals) {
  const consts = new Map()
  const inLiteral = (i) => literals.some((l) => i >= l.start && i < l.end)
  // PACKAGE LEVEL ONLY, and this narrows the contract rather than documenting a wrong
  // answer. Merging every same-named constant in a directory let a function-local
  // `const msg = "local"` overwrite the package `const msg = "global"`, so the resolver
  // synthesised `prefix local` for a call the compiler resolves to `prefix global` — an
  // invented string in the corpus that anchoring searches. Depth is counted over the
  // comment-free code with literal ranges skipped, so a brace inside a string cannot move
  // it. Function-local constants are now simply not collected: fewer strings, none wrong.
  let li = 0
  const inLiteralFast = (i) => {
    while (li < literals.length && literals[li].end <= i) li++
    return li < literals.length && i >= literals[li].start && i < literals[li].end
  }
  const depth = new Int32Array(code.length)
  {
    let d = 0
    for (let i = 0; i < code.length; i++) {
      depth[i] = d
      if (inLiteralFast(i)) continue
      if (code[i] === '{') d++
      else if (code[i] === '}') d--
    }
  }
  const atomAt = (offset) => {
    const lit = literals.find((l) => l.start === offset)
    return lit ? lit : null
  }
  const chainFrom = (offset) => {
    let lit = atomAt(offset)
    if (!lit) return null
    let text = lit.text
    let end = lit.end
    for (;;) {
      const rest = code.slice(end)
      const m = rest.match(/^\s*\+\s*/)
      if (!m) break
      const next = atomAt(end + m[0].length)
      if (!next) break
      text += next.text
      end = next.end
    }
    return text
  }
  for (const m of code.matchAll(/\bconst\s+([A-Za-z_]\w*)\s*=\s*/g)) {
    if (inLiteral(m.index) || depth[m.index] !== 0) continue
    const value = chainFrom(m.index + m[0].length)
    if (value !== null) consts.set(m[1], value)
  }
  for (const block of code.matchAll(/\bconst\s*\(([\s\S]*?)\n\)/g)) {
    const base = block.index + block[0].indexOf('(') + 1
    for (const m of block[1].matchAll(/(?:^|\n)\s*([A-Za-z_]\w*)\s*=\s*/g)) {
      const at = base + m.index + m[0].length
      if (inLiteral(at) || depth[block.index] !== 0) continue
      const value = chainFrom(at)
      if (value !== null) consts.set(m[1], value)
    }
  }
  return consts
}

// joinConcatenated reproduces what the compiler does to `"a" + "b"` and `"a" + NAME`
// written across lines. Without it the first-boot TLS line does not exist as a string
// anywhere and the gate would report that the documentation quotes something the engine
// never says.
export function joinConcatenated(code, literals, consts = new Map()) {
  // Atoms are the things that can take part in a compile-time string concatenation:
  // literals, and identifiers naming a string constant. Identifiers found INSIDE a
  // literal are text, not code.
  const atoms = literals.map((l) => ({ start: l.start, end: l.end, text: l.text }))
  if (consts.size) {
    const names = [...consts.keys()].map((k) => k.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')).join('|')
    const re = new RegExp(`(?<![A-Za-z0-9_.])(${names})(?![A-Za-z0-9_])`, 'g')
    for (const m of code.matchAll(re)) {
      if (literals.some((l) => m.index >= l.start && m.index < l.end)) continue
      atoms.push({ start: m.index, end: m.index + m[0].length, text: consts.get(m[1]) })
    }
    atoms.sort((a, b) => a.start - b.start)
  }
  const out = []
  for (let i = 0; i < atoms.length; i++) {
    out.push(atoms[i].text)
    let joined = atoms[i].text
    let k = i
    while (k + 1 < atoms.length && /^\s*\+\s*$/.test(code.slice(atoms[k].end, atoms[k + 1].start))) {
      joined += atoms[k + 1].text
      out.push(joined)
      k++
    }
  }
  return out
}

// --- filesystem helpers ------------------------------------------------------------

function walk(dir, out = []) {
  let entries
  try {
    entries = fs.readdirSync(dir, { withFileTypes: true })
  } catch {
    return out
  }
  for (const e of entries) {
    if (e.name === 'node_modules' || e.name === '.git' || e.name === 'dist') continue
    const p = path.join(dir, e.name)
    if (e.isDirectory()) walk(p, out)
    else out.push(p)
  }
  return out
}

export function readEngine(root, goRoots = GO_ROOTS) {
  // Lex once, then resolve constants per package directory, then join. Three passes
  // because a message can be assembled from a constant declared in a sibling file of the
  // same package, and one pass cannot know that yet.
  const lexed = []
  for (const r of goRoots) {
    for (const f of walk(path.join(root, r))) {
      if (!f.endsWith('.go') || f.endsWith('_test.go')) continue
      const { code, literals } = lexGo(fs.readFileSync(f, 'utf8'))
      lexed.push({ file: path.relative(root, f), dir: path.dirname(f), code, literals })
    }
  }
  const constsByDir = new Map()
  for (const l of lexed) {
    if (!constsByDir.has(l.dir)) constsByDir.set(l.dir, new Map())
    const into = constsByDir.get(l.dir)
    for (const [k, v] of stringConsts(l.code, l.literals)) into.set(k, v)
  }
  const strings = []
  let codeBlob = ''
  for (const l of lexed) {
    codeBlob += l.code + '\n'
    for (const text of joinConcatenated(l.code, l.literals, constsByDir.get(l.dir))) {
      strings.push({ text, file: l.file })
    }
  }
  return { strings, codeBlob, files: lexed.length }
}

const LOCALE_OF = (rel) => {
  const head = rel.split(path.sep)[0]
  return LOCALES.includes(head) && head !== '' ? head : ''
}

export function readDocs(docsRoot) {
  const pages = []
  for (const f of walk(docsRoot)) {
    if (!/\.mdx?$/.test(f)) continue
    const rel = path.relative(docsRoot, f)
    if (FROZEN_TREES.includes(rel.split(path.sep)[0])) continue
    const locale = LOCALE_OF(rel)
    const page = locale === '' ? rel : rel.slice(locale.length + 1)
    pages.push({ rel, locale, page, text: fs.readFileSync(f, 'utf8') })
  }
  return pages
}

// readCaptureSurfaces returns every operator-facing text file that can carry a command
// against the engine's output: the living documentation AND `deploy/`. It is deliberately
// wider and dumber than readDocs — no locale model, no extension whitelist beyond "text a
// human reads" — because the worst instance of the defect it guards lived in a Helm
// NOTES.txt that a `.mdx?` filter could never have reached.
// Files that DESCRIBE the defect and must be able to name it: this gate, its task entry,
// the hook comment, the session log and the audit. Excluding them by name is honest;
// excluding a whole directory pattern would quietly hide real instances living beside them.
const NARRATIVE_EXCLUSIONS = [
  'scripts/check-engine-output-citations.mjs',
  'Taskfile.yml',
  '.githooks/pre-push',
]
const NARRATIVE_TREES = ['sessions', 'design/audits']

export function readCaptureSurfaces(root, docsRoot) {
  const out = []
  const seen = new Set()
  const take = (file, rel) => {
    if (seen.has(rel)) return
    if (/\.(png|jpe?g|gif|svg|ico|woff2?|ttf|pdf|zip|tgz|gz|wasm|bin)$/i.test(file)) return
    if (NARRATIVE_EXCLUSIONS.includes(rel)) return
    if (NARRATIVE_TREES.some((t) => rel === t || rel.startsWith(t + path.sep))) return
    let text
    try {
      text = fs.readFileSync(file, 'utf8')
    } catch {
      return
    }
    if (text.includes('\0')) return
    seen.add(rel)
    out.push({ rel, text })
  }
  for (const f of walk(docsRoot)) {
    const rel = path.relative(docsRoot, f)
    if (FROZEN_TREES.includes(rel.split(path.sep)[0])) continue
    take(f, path.join(path.relative(root, docsRoot), rel))
  }
  // EVERY operator-facing surface, not a hand-picked list of directories. The contrast
  // found five more instances of the defect after the first sweep declared it closed —
  // INSTALL.md, the nfpm postinstall, the Docker Hub overview, an installer script, and
  // cmd_setup.go, where the ENGINE ITSELF told the operator to run the command that hides
  // the token. Every one of them was outside a hand-picked list. The pattern was mine:
  // I swept for the exact string I had already found, so I found exactly it.
  //
  // …but it enumerates what git TRACKS, not what happens to sit on disk. Walking the
  // worktree made the gate's answer depend on transient local artifacts: `task lint:export`
  // builds an ignored `.export-tmp/` holding a copy of the whole tree — INCLUDING a nested
  // copy of the frozen `2026-06` pages with all five old commands in them, at a path whose
  // first component is not `docs-site`, so the frozen-tree exclusion above could not see it.
  // Measured by the contrast around a concurrent export: 7,945 files one moment and 13,607
  // the next, at the same commit. A gate whose verdict depends on whether a sibling task is
  // mid-run is not a gate.
  let tracked = null
  try {
    tracked = execFileSync('git', ['-C', root, 'ls-files', '-z'], { encoding: 'utf8', maxBuffer: 64 << 20 })
      .split('\0')
      .filter(Boolean)
  } catch {
    tracked = null // not a repository: the self-test's throwaway trees take the walk below
  }
  if (tracked) {
    for (const rel of tracked) {
      if (rel.split(path.sep)[0] === 'docs-site') continue // handled above, frozen tree excluded
      take(path.join(root, rel), rel)
    }
  } else {
    for (const f of walk(root)) {
      const rel = path.relative(root, f)
      const head = rel.split(path.sep)[0]
      if (['.git', 'node_modules', 'dist', 'vendor', '.gotmp', 'bin', '.export-tmp'].includes(head)) continue
      if (head === 'docs-site') continue
      take(f, rel)
    }
  }
  return out
}

// Lines inside ```text fences. Those fences are how the documentation says "this is what
// the terminal shows you".
export function fenceLines(text) {
  const out = []
  let inFence = null
  text.split('\n').forEach((line, i) => {
    const fence = line.match(/^\s*```(\w*)/)
    if (fence) {
      inFence = inFence === null ? fence[1] : null
      return
    }
    if (inFence === 'text') out.push({ line: line.trim(), n: i + 1 })
  })
  return out
}

export function snakeTokens(text) {
  const out = []
  text.split('\n').forEach((line, i) => {
    for (const m of line.matchAll(/`([^`\n]+)`/g)) {
      if (/^[a-z][a-z0-9]*(_[a-z0-9]+)+$/.test(m[1])) out.push({ token: m[1], n: i + 1 })
    }
  })
  return out
}

const wordRe = (t) => new RegExp(`(?<![A-Za-z0-9_])${t.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}(?![A-Za-z0-9_])`)

// --- the checks ---------------------------------------------------------------------

export function check({ root, docsRoot, citations = CITATIONS, exemptions = NON_ENGINE_IDENTIFIERS, goRoots = GO_ROOTS }) {
  const problems = []
  const blindSpots = []
  const engine = readEngine(root, goRoots)
  const docs = readDocs(docsRoot)
  const captureSurfaces = readCaptureSurfaces(root, docsRoot)

  // 0. NON-VACUITY. Every clause below is a search; a search over nothing finds nothing
  //    and looks exactly like a clean run.
  //
  // ⛔ ESTAS CUATRO VAN A `blindSpots`, NO A `problems`, Y LA DIFERENCIA ES EL CÓDIGO DE SALIDA.
  //    El gate ya distinguía «no he podido mirar» de «está limpio» —lo dice ahí arriba con todas
  //    las letras— pero las empujaba a `problems` y salía **1**, igual que un defecto real. Quien
  //    consume el veredicto es un paso de CI, un hook u otro guion, y ninguno lee prosa: obligarles
  //    a parsear el mensaje convierte la distinción en una dependencia del TEXTO, que se rompe con
  //    la primera reescritura y se rompe en silencio.
  //
  //    Adjudicado por el carril de integración el 2026-08-17. hallazgo = 1 · punto ciego = 2 · limpio = 0.
  //
  // ⚠ DENY-CLOSED: sólo entra aquí lo que es inequívocamente «el examen no ha ocurrido». Las cuatro
  //   lo son —cero ficheros, cero literales, cero páginas, registro vacío— y la propia línea del
  //   lexer lo dice: «the lexer is broken, not the tree».
  if (engine.files === 0) blindSpots.push('read ZERO Go files: the gate examined nothing, which is not the same as clean.')
  if (engine.strings.length === 0) blindSpots.push('extracted ZERO string literals from the engine: the lexer is broken, not the tree.')
  if (docs.length === 0) blindSpots.push('found ZERO documentation pages: the gate examined nothing.')
  if (citations.length === 0) blindSpots.push('the citation registry is EMPTY: checks 1 and 2 would pass vacuously.')
  if (blindSpots.length) return { problems, blindSpots }

  // 1. ANCHORING.
  for (const c of citations) {
    const hits = engine.strings.filter((s) => s.text.includes(c.emitted))
    if (hits.length === 0) {
      problems.push(
        `${c.id}: this text is quoted by the documentation and NO string literal in the ` +
          `engine sources contains it any more. Either the wording changed in Go and the pages now quote a line ` +
          `the product never prints, or the message was deleted. Update ${c.cited.join(', ')} in all ` +
          `${LOCALES.length} locales, then this entry. Text sought:\n      ${c.emitted}`,
      )
      continue
    }
    // …and it must END WHERE A LINE ENDS. Substring containment alone let an engine line
    // GROW: append "; pinning is disabled in FIPS mode" to a registered sentence and every
    // check stayed green while the documentation kept the shorter, now-misleading version.
    // Requiring the match to be followed by a newline or the end of the literal makes a
    // suffix a change, which is what it is.
    const bounded = hits.some((h) => {
      let at = h.text.indexOf(c.emitted)
      while (at >= 0) {
        const after = at + c.emitted.length
        if (after === h.text.length || h.text[after] === '\n') return true
        at = h.text.indexOf(c.emitted, at + 1)
      }
      return false
    })
    if (!bounded) {
      problems.push(
        `${c.id}: the engine still contains this text, but it no longer ENDS there — something ` +
          `was appended to the same output line. The documentation quotes the shorter version, ` +
          `which is now a truncation of what the product prints. Sweep ${c.cited.join(', ')} in ` +
          `all ${LOCALES.length} locales, then re-anchor this entry.`,
      )
      continue
    }
    if (!hits.some((h) => h.file === c.source)) {
      problems.push(
        `${c.id}: still emitted, but no longer from ${c.source} (now: ${[...new Set(hits.map((h) => h.file))].join(', ')}). ` +
          `Point the entry at the file that owns it, so the next reader looks in the right place.`,
      )
    }
  }

  // 2. CITATION.
  for (const c of citations) {
    for (const page of c.cited) {
      for (const locale of LOCALES) {
        const rel = locale === '' ? page : path.join(locale, page)
        const doc = docs.find((d) => d.rel === rel)
        if (!doc) {
          problems.push(`${c.id}: declares ${rel}, which does not exist.`)
          continue
        }
        if (!doc.text.includes(c.emitted)) {
          problems.push(
            `${c.id}: ${rel} no longer contains the quotation it is registered for. Either the ` +
              `page was rewritten and the entry is stale, or a translation silently dropped a line ` +
              `the other six still show.`,
          )
        }
      }
    }
  }

  // 3. FENCE COMPLETENESS, as a SET.
  //
  // Compared LINE BY LINE, not literal by literal, and that is the difference between a
  // check and a decoration. Engine literals routinely end in `\n` or carry several lines
  // and a `%s`; a Markdown fence line can never contain one of those whole. Asking
  // "does this fence line contain a whole literal" therefore found 8 matches in 371 fence
  // lines and — measured — missed the banner THIS FILE registers. Splitting the literals
  // into lines first asks the question the fence actually answers.
  const registered = citations.map((c) => c.emitted)
  const engineLines = new Map() // trimmed output line -> file that emits it
  for (const s of engine.strings) {
    for (const raw of s.text.split('\n')) {
      const t = raw.trim()
      if (t.length >= MIN_QUOTED_LENGTH && !engineLines.has(t)) engineLines.set(t, s.file)
    }
  }
  const found = new Map() // engine output line -> first documentation location
  for (const doc of docs) {
    for (const { line, n } of fenceLines(doc.text)) {
      if (line.length < MIN_QUOTED_LENGTH) continue
      for (const [t] of engineLines) {
        if (line.includes(t) && !found.has(t)) found.set(t, `${doc.rel}:${n}`)
      }
    }
  }
  const unregistered = [...found.keys()].filter((t) => !registered.some((r) => t.includes(r) || r.includes(t)))
  for (const t of unregistered) {
    problems.push(
      `a text fence quotes an engine string that is NOT in the registry, at ${found.get(t)}:\n` +
        `      ${t.slice(0, 140)}\n` +
        `      Register it. That is the whole mechanism: a quotation registered while it is TRUE ` +
        `is what makes the next edit to the Go string turn this gate red.`,
    )
  }

  // 5. FIXED-WINDOW CAPTURE OF THE FIRST-BOOT BANNER.
  //
  // The defect that opened this gate's third case was NOT a quotation: it was a COMMAND
  // whose correctness depends on the banner's shape. `grep -A4 "FIRST-BOOT SETUP"` was
  // documented in 60 places while the `Token:` line sat six lines below the header, so the
  // documented command printed everything except the value the step exists to obtain.
  //
  // Checks 1-4 could not see it, and did not: the contrast pointed out that reverting all
  // 60 occurrences left this gate green. So the window is compared against the banner
  // MEASURED FROM THE ENGINE, not against a number written here — the check re-derives the
  // distance on every run and reports it, so it stays true when the banner changes again.
  //
  // This clause reads `deploy/` as well. The worst occurrence was in the Helm chart's
  // NOTES.txt, which is what an operator sees seconds after `helm install`, and neither a
  // .md filter nor a docs-site root would ever have reached it.
  const banner = engine.strings
    .filter((s) => s.text.includes('=== FIRST-BOOT SETUP ==='))
    .sort((a, b) => b.text.length - a.text.length)[0]
  // Only ask the question when something documents the banner. If no page mentions it there
  // is no capture to protect; if a page DOES mention it and the engine has no such literal,
  // that is the class in its purest form and must be said.
  const documentsBanner = captureSurfaces.some((s) => s.text.includes('FIRST-BOOT SETUP'))
  if (!banner && documentsBanner) {
    problems.push(
      'documentation captures a "FIRST-BOOT SETUP" banner, and no literal in the engine produces ' +
        'one. Either it was renamed — in which case every page reproducing it is now wrong — or ' +
        'this clause is looking in the wrong place.',
    )
  } else if (banner) {
    const lines = banner.text.split('\n')
    const header = lines.findIndex((l) => l.includes('=== FIRST-BOOT SETUP ==='))
    const token = lines.findIndex((l, i) => i > header && /\bToken:/.test(l))
    if (token < 0) {
      problems.push('the first-boot banner no longer has a `Token:` line; this clause cannot measure it.')
    } else {
      // THE GATE CHECKS ITS OWN ADVICE. Every failure below tells the reader to use a range
      // ending at the banner footer, and until now nothing verified that the range reaches
      // the token — the self-test's "green" for it asserted only that a command without a
      // window is not flagged, which is a different sentence. So the recommended span is
      // applied to the measured banner here: header to the first footer line after it, and
      // the `Token:` line must fall inside. If a future banner puts the token after the
      // footer, the remedy this gate recommends would be as wrong as the command it
      // rejects, and it must say so rather than keep recommending it.
      const footer = lines.findIndex((l, i) => i > header && /={6,}/.test(l))
      if (footer < 0 || footer < token) {
        problems.push(
          'the range this gate recommends does not reach the `Token:` line of the banner as the ' +
            `engine now emits it (header at ${header}, token at ${token}, footer at ${footer}). The ` +
            'advice in every message below is stale — fix the recommendation before enforcing it.',
        )
      }
      const need = token - header
      const headerLine = lines[header]
      // A command targets the banner if its grep PATTERN would select the banner header —
      // not if it happens to contain the string this session first searched for. The five
      // instances found after the first sweep used `'SETUP'`, bare `SETUP` and
      // `'FIRST-BOOT SETUP'`; matching one spelling is how a sweep confirms its own
      // starting assumption. Candidates are quoted strings and bare words, 4+ characters
      // so that punctuation cannot match by accident.
      const targetsBanner = (line) => {
        const candidates = []
        for (const m of line.matchAll(/'([^']+)'|"([^"]+)"/g)) candidates.push(m[1] ?? m[2])
        for (const m of line.matchAll(/(?:^|\s)([A-Za-z0-9_.*[\]-]{4,})(?=\s|$|`|\)|'|")/g)) candidates.push(m[1])
        return candidates.some((c) => {
          if (c.length < 4) return false
          if (headerLine.includes(c)) return true
          // grep takes a REGEX, and a substring test is not one. `'FIRST-BOOT.SETUP'`
          // selects the header because `.` matches the space — the contrast reproduced it
          // against real grep. Trying the candidate as a pattern costs nothing and closes a
          // whole family of spellings; an unparseable pattern simply falls through.
          // Only a candidate with real letters in it, so `.*`-ish noise cannot match
          // everything by accident.
          if (!/[A-Za-z]{4,}/.test(c.replace(/[^A-Za-z]/g, ''))) return false
          try {
            return new RegExp(c).test(headerLine)
          } catch {
            return false
          }
        })
      }
      // Shell line continuations are joined first, and the LONG option is recognised too.
      // The contrast's on-paper drift was exactly this: `--after-context=4` selects the same
      // four lines as `-A4`, and splitting the flag from its pattern across two lines defeats
      // a same-line predicate. A rule that only knows one spelling of a thing is the defect
      // this whole session is about, one level up.
      for (const s of captureSurfaces) {
        const raw = s.text.split('\n')
        const joined = []
        for (let k = 0; k < raw.length; k++) {
          let line = raw[k]
          let first = k
          while (/\\\s*$/.test(line) && k + 1 < raw.length) {
            line = line.replace(/\\\s*$/, ' ') + raw[++k]
          }
          joined.push({ line, n: first + 1 })
        }
        joined.forEach(({ line, n: lineNo }) => {
          // FAIL CLOSED on a window this cannot read. `grep -A$((2+2))` is a perfectly
          // ordinary four-line window that no decimal-digit parser sees, and the contrast
          // showed it hiding the token exactly like the 65 repaired commands. "I could not
          // read the window" is not "the window is big enough".
          // The flag must be a REAL option on a REAL grep invocation. Without the word
          // boundary, `-A` matched inside the word "after"; without requiring `grep`, a
          // minified JS bundle and a prose line about a document both looked like captures.
          // Failing closed is only useful if what it fails closed ON is a capture command.
          if (!/(?:^|[\s|(`'"])grep\b/.test(line)) return
          // ⛔ Y EL `-A` TIENE QUE SER DE ESE `grep`, no de cualquier comando de la linea.
          // Medido el 2026-08-19: `cp -al $(ls -A "$RAIZ" | grep -v '^\.git$' | ...)` traia un
          // `grep` y un `-A`, y el `-A` era de `ls` — mostrar ocultos. El gate lo leyo como una
          // ventana de contexto ilegible y puso ROJO un lint RAPIDO, que bloquea el push de los
          // cinco carriles por una linea que no cita al motor en absoluto.
          //
          // Se acota al tramo de argumentos del propio grep: desde `grep` hasta el siguiente
          // separador de comandos. Fuera de ese tramo, un `-A` es de otro.
          const tramo = line.match(/(?:^|[\s|(`'"])grep\b([^|;&]*)/)
          if (!tramo) return
          const m = tramo[1].match(/(?:^|\s)(?:-A\s*|--after-context[=\s])(\S+)/)
          if (!m) return
          if (!targetsBanner(line)) return
          const parsed = /^\d+$/.test(m[1]) ? Number(m[1]) : null
          if (parsed !== null && parsed >= need) return
          const i = lineNo - 1
          if (parsed === null) {
            problems.push(
              `${s.rel}:${i + 1} captures the first-boot banner with an after-context window this ` +
                `gate cannot evaluate (\`${m[1]}\`). The \`Token:\` line is ${need} lines below the ` +
                `header, and an unreadable window is not a proven-large-enough one. Use a range ` +
                `that ends at the banner footer.`,
            )
            return
          }
          problems.push(
            `${s.rel}:${i + 1} captures the first-boot banner with a fixed window of ${m[1]} lines, ` +
              `but the \`Token:\` line is ${need} lines below the header — measured from the banner ` +
              `literal in ${banner.file} on this run. The documented command prints everything except ` +
              `the token it exists to obtain. Use a range that ends at the banner footer; any fixed ` +
              `window is brittle, because the banner has changed length before.`,
          )
        })
      }
    }
  }

  // 4. IDENTIFIER ANCHORING, as a SET.
  const needed = new Set()
  const seen = new Map() // token -> first location
  for (const doc of docs) {
    for (const { token, n } of snakeTokens(doc.text)) {
      if (!seen.has(token)) seen.set(token, `${doc.rel}:${n}`)
      if (!wordRe(token).test(engine.codeBlob)) needed.add(token)
    }
  }
  for (const token of [...needed].sort()) {
    if (exemptions.has(token)) continue
    problems.push(
      `\`${token}\` (${seen.get(token)}) names nothing in the engine sources. If the engine ` +
        `renamed it, every page using it is now wrong — sweep all ${LOCALES.length} locales. If it was ` +
        `never an engine identifier, add it to NON_ENGINE_IDENTIFIERS with the reason.`,
    )
  }
  for (const token of [...exemptions.keys()].sort()) {
    // The reason is the whole value of the list — it is the evidence that somebody looked
    // rather than silenced. Until the contrast pointed it out, nothing read these values,
    // so an empty string bought the same silence as a researched sentence.
    if (!String(exemptions.get(token) || '').trim()) {
      problems.push(
        `\`${token}\` is exempted with no reason. The reason is the point of the list: it is what ` +
          `distinguishes "somebody checked this is not an engine identifier" from "somebody wanted ` +
          `the gate quiet".`,
      )
    }
    if (needed.has(token)) continue
    problems.push(
      `\`${token}\` is exempted as "not an engine identifier", but it is no longer needed: either ` +
        `the engine now has it, or no page mentions it. Remove the exemption — a ratchet that does ` +
        `not ratchet stops catching anything.`,
    )
  }

  return { problems, blindSpots }
}

// --- self-test -----------------------------------------------------------------------

function tmpTree(parent = '') {
  return fs.mkdtempSync(
    parent
      ? path.join(parent, 'case-')
      : path.join(process.env.TMPDIR || os.tmpdir(), 'engine-citations-'),
  )
}

function writeTree(dir, files) {
  for (const [rel, body] of Object.entries(files)) {
    const p = path.join(dir, rel)
    fs.mkdirSync(path.dirname(p), { recursive: true })
    fs.writeFileSync(p, body)
  }
}

function selfTest() {
  // One owned root per run, not one top-level /tmp entry per clause. Five concurrent hook
  // runs used to leave 125 engine-citations-* directories behind even when all 25 clauses
  // passed. The Taskfile wrapper is the abnormal-exit backstop; this removes the normal path.
  const selfTestRoot = tmpTree()
  const cases = []
  // mustMention takes a LIST, and every entry must appear. It took one string until the
  // contrast noted that the exemption-swap case then proved only its stale half: the clause
  // stayed "ok" even if detection of the newly added token regressed, which is exactly the
  // half that distinguishes a set ratchet from a count.
  // ⛔ SE AFIRMA SOBRE LA CLASE DE SALIDA (0/1/2), que es lo único que consume un paso de CI, y no
  //    sobre «¿hay algo en la lista?». La condición que exige el carril de integración es de DOS MITADES:
  //    forzar el punto ciego y ver un 2, y forzar un hallazgo y ver un 1. Sin las dos, la conversión
  //    no está probada — un gate que las mezclara pasaría afirmando sólo la mitad esperada.
  //
  //    El 2 GANA al 1 cuando coinciden: si el examen no ocurrió, lo que el gate diga sobre hallazgos
  //    tampoco es fiable. Un caso de HALLAZGO, en cambio, no puede disparar punto ciego, o su
  //    defecto quedaría indistinguible de «no he podido mirar».
  const record = (name, expectRed, res, mustMention = [], expectBlind = false) => {
    const wanted = Array.isArray(mustMention) ? mustMention : [mustMention]
    const { problems, blindSpots } = res
    const clase = blindSpots.length > 0 ? 2 : problems.length > 0 ? 1 : 0
    const esperada = expectBlind ? 2 : expectRed ? 1 : 0
    const lista = expectBlind ? blindSpots : problems
    const mentioned = wanted.every((w) => lista.some((p) => p.includes(w)))
    cases.push({
      name,
      ok: clase === esperada && mentioned,
      expectRed: esperada,
      got: [...problems, ...blindSpots],
    })
  }

  const base = (goBody, docBody, extra = {}) => {
    const dir = tmpTree(selfTestRoot)
    writeTree(dir, {
      'core/x.go': goBody,
      'docs/how-to/p.md': docBody,
      ...Object.fromEntries(
        ['de', 'es', 'fr', 'ja', 'ru', 'zh'].map((l) => [`docs/${l}/how-to/p.md`, docBody]),
      ),
      ...extra,
    })
    return dir
  }

  const GOOD_GO = 'package x\nfunc f() { log.Warn("the estate runs on no live traffic at all, which is deliberate") }\n'
  const GOOD_DOC = '```text\nthe estate runs on no live traffic at all, which is deliberate\n```\n'
  const REG = [
    {
      id: 't',
      emitted: 'the estate runs on no live traffic at all, which is deliberate',
      source: 'core/x.go',
      cited: ['how-to/p.md'],
      why: 'self-test',
    },
  ]
  const run = (dir, reg = REG, ex = new Map()) =>
    check({ root: dir, docsRoot: path.join(dir, 'docs'), citations: reg, exemptions: ex, goRoots: ['core'] })

  // GREEN control. Without it, every red below could be red for the wrong reason.
  record('green: engine and docs agree', false, run(base(GOOD_GO, GOOD_DOC)))

  // 1. the engine's wording changes; the docs do not. THE CLASS.
  record(
    'red: engine reworded, docs not swept',
    true,
    run(base(GOOD_GO.replace('no live traffic', 'NO live traffic'), GOOD_DOC)),
    'NO string literal in the engine sources contains it',
  )

  // The trap that made a naive gate green: the old wording survives IN A COMMENT.
  record(
    'red: old wording survives only in a comment',
    true,
    run(
      base(
        'package x\n// was: the estate runs on no live traffic at all, which is deliberate\nfunc f() { log.Warn("something else entirely here") }\n',
        GOOD_DOC,
      ),
    ),
    'NO string literal in the engine sources contains it',
  )

  // 2. a translation drops the quoted line while the other six keep it.
  {
    const dir = base(GOOD_GO, GOOD_DOC)
    fs.writeFileSync(path.join(dir, 'docs/ja/how-to/p.md'), '```text\nnothing quoted here\n```\n')
    record('red: one locale dropped the quotation', true, run(dir), 'no longer contains the quotation')
  }

  // 3. a fence quotes an engine string nobody registered.
  record(
    'red: unregistered fence quotation',
    true,
    run(
      base(
        GOOD_GO + 'func g() { log.Warn("a second sentence the engine prints and nobody registered") }\n',
        GOOD_DOC + '\n```text\na second sentence the engine prints and nobody registered\n```\n',
      ),
    ),
    'NOT in the registry',
  )

  // 4. a doc names a snake_case identifier the engine does not have. THE `fingerprint_sha256` CASE.
  record(
    'red: doc names an identifier the engine lost',
    true,
    run(base(GOOD_GO, GOOD_DOC + '\nThe engine logs `fingerprint_sha256` on that line.\n')),
    'names nothing in the engine sources',
  )

  // …and the same token, exempted with a reason, is green.
  record(
    'green: that identifier, exempted',
    false,
    run(base(GOOD_GO, GOOD_DOC + '\nThe engine logs `fingerprint_sha256` on that line.\n'), REG, new Map([['fingerprint_sha256', 'self-test']])),
  )

  // 5. an exemption that is no longer needed. THE SET RATCHET: this is what a COUNT misses.
  record(
    'red: unnecessary exemption',
    true,
    run(base(GOOD_GO, GOOD_DOC), REG, new Map([['never_mentioned_anywhere', 'stale']])),
    'no longer needed',
  )

  // 6. the swap a COUNT-based ratchet lets through: one exemption removed, one added.
  //    Counts identical (1 and 1); the SETS differ, so it must fail. BOTH halves are
  //    asserted — the stale exemption AND the newly unexempted token — because requiring
  //    only the first would keep this clause green if the second ever regressed, and the
  //    second is the half a count-based ratchet actually misses.
  record(
    'red: exemption swapped, count unchanged',
    true,
    run(
      base(GOOD_GO, GOOD_DOC + '\nSee `alpha_token` for details.\n'),
      REG,
      new Map([['beta_token', 'was needed once']]),
    ),
    ['no longer needed', 'alpha_token'],
  )

  // 7. an exemption with no reason buys the same silence as a researched one — until now.
  record(
    'red: exemption with an empty reason',
    true,
    run(base(GOOD_GO, GOOD_DOC + '\nThe engine logs `mystery_key` there.\n'), REG, new Map([['mystery_key', '  ']])),
    'exempted with no reason',
  )

  // 8. a fixed-window capture of the first-boot banner. THE 60-OCCURRENCE DEFECT: checks
  //    1-4 cannot see it, because a command is not a quotation.
  const BANNER_GO =
    '\nfunc b() {\n\tfmt.Fprintf(out, "\\n=== FIRST-BOOT SETUP ===\\n"+\n' +
    '\t\t"a line\\n"+"another line\\n"+"third line\\n"+"\\n"+"  Console:  %s\\n"+"  Token:    %s\\n"+\n' +
    '\t\t"========================\\n", u, t)\n}\n'
  record(
    'red: fixed-window banner capture that cannot reach the token',
    true,
    run(base(GOOD_GO + BANNER_GO, GOOD_DOC + '\n```bash\nkubectl logs sts/x | grep -A4 "FIRST-BOOT SETUP"\n```\n')),
    ['fixed window of 4 lines', '`Token:` line is 6 lines below'],
  )

  // The GREEN counterpart, named for what it actually asserts. It does NOT validate that a
  // sed range reaches the token — production has no range parser, and calling this clause
  // "the range form works" would claim an assertion nobody wrote. What it pins is narrower
  // and still worth pinning: a capture with no fixed window is not flagged, so clause 8
  // discriminates instead of failing on every line that mentions the banner.
  record(
    'green: a banner capture with NO fixed window is not flagged',
    false,
    run(
      base(
        GOOD_GO + BANNER_GO,
        GOOD_DOC + "\n```bash\nkubectl logs sts/x | sed -n '/FIRST-BOOT SETUP/,/========================/p'\n```\n",
      ),
    ),
  )

  // …and the two spellings the contrast showed bypassing a same-line `-A<n>` predicate:
  // GNU grep's long option, and the flag split from its pattern by a continuation. Both
  // select the same four lines, so both still hide the token.
  record(
    'red: --after-context long option',
    true,
    run(base(GOOD_GO + BANNER_GO, GOOD_DOC + '\n```bash\nkubectl logs sts/x | grep --after-context=4 \'SETUP\'\n```\n')),
    'fixed window of 4 lines',
  )
  record(
    'red: window and pattern split across a line continuation',
    true,
    run(base(GOOD_GO + BANNER_GO, GOOD_DOC + '\n```bash\nkubectl logs sts/x | grep -A4 \\\n  "FIRST-BOOT SETUP"\n```\n')),
    'fixed window of 4 lines',
  )

  // …and the gate's OWN ADVICE, proved wrong on purpose: a banner whose token falls AFTER
  // the footer makes the recommended range stale, and recommending it would then be the
  // same defect one level up.
  {
    const badBanner =
      '\nfunc b2() {\n\tfmt.Fprintf(out, "\\n=== FIRST-BOOT SETUP ===\\n"+\n' +
      '\t\t"a line\\n"+"========================\\n"+"  Token:    %s\\n", t)\n}\n'
    record(
      'red: the recommended range would not reach the token on this banner',
      true,
      run(base(GOOD_GO + badBanner, GOOD_DOC)),
      'the range this gate recommends does not reach',
    )
  }

  // …and a rune literal must not move brace depth. `const brace = '{'` is valid Go, and
  // copying it into the depth scan left every later declaration in the file at a false
  // depth of 1, so the package-level filter silently collected nothing after it — a
  // narrowing that quietly became a blanking.
  {
    const { code, literals } = lexGo(
      "package x\nconst brace = '{'\nconst msg = \"global\"\nfunc f() { log.Print(\"prefix \" + msg) }\n",
    )
    const got = stringConsts(code, literals).get('msg')
    cases.push({
      name: "green: a rune literal '{' does not hide the constants after it",
      ok: got === 'global',
      expectRed: false,
      got: got === 'global' ? [] : [`resolved msg=${JSON.stringify(got)}, expected "global"`],
    })
  }

  // …and a function-local constant must not overwrite the package one. Go resolves the call
  // in f() to "prefix global"; a directory-wide merge synthesised "prefix local".
  {
    const { code, literals } = lexGo(
      'package x\nconst msg = "global"\nfunc f() { log.Print("prefix " + msg) }\nfunc g() { const msg = "local"; _ = msg }\n',
    )
    const joined = joinConcatenated(code, literals, stringConsts(code, literals))
    const ok = joined.includes('prefix global') && !joined.includes('prefix local')
    cases.push({
      name: 'green: a function-local constant does not shadow the package one',
      ok,
      expectRed: false,
      got: ok ? [] : [`synthesised ${JSON.stringify(joined.filter((x) => x.startsWith('prefix')))}`],
    })
  }

  // …and the two spellings the contrast reproduced against real grep after the first
  // widening: a COMPUTED window no decimal parser sees, and a basic-regex pattern whose
  // `.` matches the space in the header.
  // Y la direccion que NO debe disparar: un `-A` que es de OTRO comando de la misma linea.
  // `ls -A` lista ocultos; que haya un `grep` mas adelante en la tuberia no convierte ese `-A`
  // en una ventana de contexto. Medido el 2026-08-19: esta forma exacta puso rojo un lint
  // RAPIDO —el que bloquea el push de los cinco carriles— por una linea que no cita al motor.
  record(
    'green: a -A belonging to another command in the pipeline is not a window',
    false,
    run(base(GOOD_GO + BANNER_GO, GOOD_DOC + '\n```bash\ncp -al $(ls -A "$RAIZ" | grep -v \'^[.]git$\' | sed "s|^|$RAIZ/|") "$D/"\n```\n')),
  )
  record(
    'red: computed after-context window the gate cannot evaluate',
    true,
    run(base(GOOD_GO + BANNER_GO, GOOD_DOC + '\n```bash\nkubectl logs x | grep -A$((2+2)) "FIRST-BOOT SETUP"\n```\n')),
    'cannot evaluate',
  )
  record(
    'red: a regex pattern that selects the header without containing it',
    true,
    run(base(GOOD_GO + BANNER_GO, GOOD_DOC + '\n```bash\nkubectl logs x | grep -A4 \'FIRST-BOOT.SETUP\'\n```\n')),
    'fixed window of 4 lines',
  )
  // …and a window that IS big enough must not be flagged, or the clause above is just noise.
  record(
    'green: an after-context window large enough to reach the token',
    false,
    run(base(GOOD_GO + BANNER_GO, GOOD_DOC + '\n```bash\nkubectl logs x | grep -A20 "FIRST-BOOT SETUP"\n```\n')),
  )

  // …and an engine line that GROWS while the documentation keeps the shorter version.
  record(
    'red: text appended to a registered output line',
    true,
    run(base(GOOD_GO.replace('deliberate")', 'deliberate; unless FIPS is on")'), GOOD_DOC)),
    'it no longer ENDS there',
  )

  // …and a page that captures a banner the engine does not produce at all.
  record(
    'red: documentation captures a banner the engine has no literal for',
    true,
    run(base(GOOD_GO, GOOD_DOC + '\n```bash\nkubectl logs sts/x | grep -A4 "FIRST-BOOT SETUP"\n```\n')),
    'no literal in the engine produces one',
  )

  // 9. the escape decoder. A wrong decoding does not merely miss a string, it INVENTS one,
  //    and anchoring is precisely the question of what is in that corpus.
  // The ASCII fixture agreed with the bug it was meant to catch: for `\x41`, byte and code
  // point coincide. The UTF-8 cases are the ones that discriminate — `"\xC3\xA9"` is two
  // BYTES that encode `é`, and decoding them as two code points invents `Ã©`.
  for (const [lit, want] of [
    ['"\\x41B\\101"', 'ABA'],
    ['"\\xC3\\xA9"', 'é'],
    ['"caf\\303\\251"', 'café'],
    ['"\\u00e9"', 'é'],
    // NON-BMP, unescaped. The byte-assembly repair encoded one UTF-16 code unit at a time,
    // so an astral character became two U+FFFD — and all four fixtures above are escapes,
    // none of which walks that branch. A fixture that cannot see the bug is not a fixture.
    ['"😀"', '😀'],
    ['"a😀b"', 'a😀b'],
  ]) {
    const { literals } = lexGo(`package x\nconst s = ${lit}\n`)
    const got = literals.map((l) => l.text).join('')
    cases.push({
      name: `green: Go literal ${lit} decodes to its compiled value`,
      ok: got === want,
      expectRed: false,
      got: got === want ? [] : [`decoded ${JSON.stringify(got)}, Go compiles it to ${JSON.stringify(want)}`],
    })
  }

  // 10. a fence quoting a NEWLINE-TERMINATED literal. Before line-level comparison this was
  //     invisible: no Markdown line can contain a literal that ends in \n, so the check
  //     found 8 matches in 371 fence lines and missed the banner this file registers.
  record(
    'red: fence quotes a newline-terminated literal, unregistered',
    true,
    run(
      base(
        GOOD_GO + '\nfunc h() { fmt.Fprintf(out, "a new engine sentence longer than forty chars\\n") }\n',
        GOOD_DOC + '\n```text\na new engine sentence longer than forty chars\n```\n',
      ),
    ),
    'NOT in the registry',
  )

  // 7. examining nothing is not clean.
  {
    const dir = tmpTree(selfTestRoot)
    writeTree(dir, { 'docs/how-to/p.md': GOOD_DOC })
    // ⛔ LA MITAD «PUNTO CIEGO ⇒ 2». Este caso existía declarado como rojo, y ahí estaba el defecto
    //    que el carril de integración adjudicó: un árbol sin un solo fichero Go no es un hallazgo, es que el
    //    examen no ha ocurrido. Ahora exige clase 2 y se pondría rojo si alguien lo devolviera a 1.
    record('blind: no Go files at all', false, run(dir), 'examined nothing', true)
  }

  // ⛔ LA OTRA MITAD, «REGISTRO VACÍO ⇒ 2», que es el punto ciego más traicionero de los cuatro:
  //    con el registro de citas vacío las comprobaciones 1 y 2 no encuentran nada QUE COMPARAR y
  //    salían 0 hallazgos. El árbol está perfecto; el gate no ha mirado.
  {
    const dir = base(GOOD_GO, GOOD_DOC)
    record('blind: the citation registry is empty', false, run(dir, []), 'registry is EMPTY', true)
  }

  // ⛔ Y LA MITAD QUE FALTABA PARA QUE LA CONVERSIÓN ESTÉ PROBADA: un HALLAZGO real sobre un árbol
  //    que SÍ se pudo examinar tiene que seguir saliendo 1, no 2. Sin esta, convertir todo a 2
  //    también habría pasado la batería.
  {
    const dir = base(GOOD_GO.replace('no live traffic', 'NO live traffic'), GOOD_DOC)
    record(
      'red: a real finding on an examinable tree stays class 1 (not 2)',
      true,
      run(dir),
      'NO string literal in the engine sources contains it',
    )
  }

  const failed = cases.filter((c) => !c.ok)
  for (const c of cases) {
    console.log(`  ${c.ok ? 'ok  ' : 'FAIL'}  ${c.name}`)
    if (!c.ok) for (const p of c.got) console.log(`          ${p.split('\n')[0]}`)
  }
  console.log(`\nself-test: ${cases.length - failed.length}/${cases.length} clauses behaved as declared`)
  fs.rmSync(selfTestRoot, { recursive: true, force: true })
  if (failed.length) {
    console.error('engine-output-citations: SELF-TEST FAILED — the gate does not do what it says.')
    process.exit(1)
  }
}

// --- entry point ----------------------------------------------------------------------

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  if (process.argv.includes('--self-test')) {
    selfTest()
  } else {
    const { problems, blindSpots } = check({ root: ROOT, docsRoot: path.join(ROOT, 'docs-site/src/content/docs') })
    // El punto ciego se mira ANTES y sale 2: cero hallazgos sobre cero material no es un aprobado,
    // y un 1 lo haría indistinguible de un defecto real para quien consuma el veredicto.
    if (blindSpots.length) {
      console.error('engine-output-citations: NO HE PODIDO MIRAR — el examen no ha ocurrido.\n')
      for (const b of blindSpots) console.error(`  - ${b}\n`)
      process.exit(2)
    }
    if (problems.length) {
      console.error('engine-output-citations: FAIL — the documentation quotes the engine inaccurately.\n')
      for (const p of problems) console.error(`  - ${p}\n`)
      console.error(
        `${problems.length} problem(s). The rule: changing a string the product PRINTS obliges you ` +
          `to sweep everything that CITES it, in all ${LOCALES.length} locales.`,
      )
      process.exit(1)
    }
    console.log(
      `engine-output-citations: ok — ${CITATIONS.length} quotations found in the engine's string ` +
        `literals and present in ${LOCALES.length} locales; identifier exemption sets match ` +
        `exactly, every quoted engine line is covered by a registered quotation, and no ` +
        `documented banner capture uses a window too small to reach the token.`,
    )
  }
}
