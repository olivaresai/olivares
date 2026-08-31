<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->

# `docs/claims/` — the canonical public-claims contract

`public-claims.v1.json` is the **single source of truth** for what the product may claim in
public and under which conditions. The public storefront does not author claim state: it
synchronizes this manifest, validates it, and derives every visible label from it.

Written from the adversarial public-claims truth-matrix audit of 2026-08-09 (Codex
`gpt-5.6-sol`, report digest sha256 `505b6ef5…`, retained internally and pinned by
`measurement.auditReportSha256`). **Every field in the manifest traces to a line of that report.**
Nothing in it was widened by eye.

## Why this exists

Before this contract, `Built` was a hand-authored string in a storefront TypeScript file
(`src/data/roadmap.ts`) and the public product sentences were prose nobody could fail. Seven rows
said `built` with no owner, no control route, no job, no test and no evidence — so implementation
drift could not block publication. The audit graded all six reviewed claim families **ROTO** for
publication as written.

The contract does **not** delete or deny any implemented function. It separates *implementation
inventory* from *publication acceptance*.

## Canonical lifecycle

| State | Meaning |
|---|---|
| `implemented_unaccepted` | Relevant code exists, but claim acceptance is absent, stale, partial or failed. **Publishable only with its limits stated.** |
| `accepted` | The complete bounded claim satisfies every acceptance field at exact refs. |
| `shipped` | An `accepted` claim included in a signed public release and recorded in the changelog. |
| `planned` | Intended work without an accepted current implementation. No availability promise. |
| `exploring` | Direction under consideration, no committed scope or date. No availability promise. |

Two rules the validator enforces mechanically, not by convention:

1. **`Built` is not a source state.** It is a presentation alias for `accepted`. No source file may
   carry a hand-authored `built`.
2. **A composite claim is `accepted` only if every component is `accepted`.** One
   `implemented_unaccepted` child keeps the whole composite `implemented_unaccepted`.

At the measured baseline **every claim is `implemented_unaccepted`**, because the acceptance jobs
and their evidence artifacts do not exist yet. That is the measurement, not a placeholder: a
function existing, or a unit test passing, does not raise the state.

## Schema (`public-claims/v1`)

Top level:

| Field | Meaning |
|---|---|
| `schemaVersion` | `public-claims/v1`. An unknown version is **NO VERIFICADO**, never a green skip. |
| `measuredAt` | Date of the measurement (`YYYY-MM-DD`). Deterministic; never "now". |
| `measurement.auditReportTitle` / `auditReportSha256` / `auditor` | Provenance of the measurement. **A title and a digest, never a path** — see below. |
| `measurement.refs` | Exact `hub` / `web` / `enterprise` SHAs the measurement was taken at. |
| `jobs[]` | The acceptance jobs the manifest **names but does not run**: `id`, `implemented`, `mustProve`. |
| `evidenceFormat` | The fields an evidence artifact must carry before any claim can be `accepted`. |
| `claims[]` | The claims themselves. |

Per claim:

| Field | Meaning |
|---|---|
| `id` | Stable claim ID. Unique; referenced by the storefront bindings. |
| `auditRef` | The audit section this entry traces to (`C1`…`C6`). |
| `title` | Short human label. Not published copy. |
| `state` | One of the five canonical states. |
| `components[]` | Child claim IDs. Empty means atomic. |
| `owner.implementation` | Who owns the control. |
| `owner.publication` | Who owns the public surface. |
| `owner.externalApplication` | Present when the last mile is outside the product (e.g. a host administrator). |
| `boundedProposition` | The claim as it may be stated — already narrowed to the measured control. |
| `conditions[]` | Operating conditions that must remain visible wherever the claim is published. |
| `unpromoted[]` | What the claim explicitly does **not** promise, with the measurement that says so. |
| `controlRoutes[]` | `file:line` anchors of the effective control. Empty means *not measured* — never *absent*. |
| `surfaces[]` | Where the claim is published: `repo`, `route`, `localeKey`, optional `anchor`, optional `roadmapItemId`. |
| `acceptance.job` | The job that would produce acceptance. Must exist in `jobs[]`. |
| `acceptance.tests[]` | Existing component tests. |
| `acceptance.testsAreAcceptance` | **`false` throughout the measured baseline.** A component test is not a claim test. |
| `acceptance.evidence` | The evidence artifact, or `null` for *explicitly absent*. |
| `acceptance.mutation` | The killed mutant record, or `null` for *explicitly absent*. |
| `gaps[]` | Why this claim is not `accepted`, in words that name the missing artifact. |

`surfaces[].anchor` pins the binding to a stable field of the target object (for example
`{"field": "roman", "value": "II"}`), so reordering a list in the storefront breaks loudly instead
of silently rebinding a claim to somebody else's sentence.

## Acceptance — all of it, or the state does not move

Taken from the audit's *Definition of Built*:

1. Named implementation owner and publication owner.
2. A bounded proposition, a control route and explicit operating conditions.
3. A user-observable route or UI path; API-only claims must say so.
4. A current-ref deterministic claim job, named tests and retained evidence.
5. At least one valid, compiled or parsed mutant that the job kills.
6. A non-trigger fixture proving an honest limitation or negation is still allowed.
7. Copy whose scope and conditions exactly match the exercised control.
8. Exact HUB / WEB / ENTERPRISE SHAs and an artifact digest.

`evidenceFormat.required` lists the fields an evidence artifact must carry. **Evidence without
exact refs, or with a skipped required mutant, cannot yield `accepted`.**

## Named jobs that do not exist yet

`claim-sessions-work`, `claim-agent-coordination`, `claim-sandbox-agent-isolation`,
`claim-model-routing-providers`, `claim-managed-settings-fleet`, `claim-roadmap-bindings`.

All carry `implemented: false`. The manifest **names** them so the gap is auditable; it does not
pretend to run them.

## How the storefront consumes it

The storefront extends its existing hub-sync boundary — it does not open a second source
of truth:

1. `scripts/sync-hub.mjs` reads this manifest from an explicitly named product checkout (`HUB_DIR`).
2. `scripts/claims-contract.mjs` validates it and derives the public view.
3. The view is written to `src/data/hub/public-claims.json` and recorded in
   `src/data/hub/provenance.json` with its SHA-256 and both repository SHAs.
4. `src/data/claim-bindings.ts` maps `route + locale key → claim ID`, validated in **both**
   directions: no orphan binding, no declared surface without a binding.
5. `npm run check:claims` fails closed. `0` = LIMPIO, `1` = ROTO (a violation was found), `2` =
   NO HE PODIDO MIRAR (missing, unreadable, unparseable, unknown schema version, a digest that no
   longer matches, or a `HUB_DIR` that names a checkout with no manifest). **CI fails on both 1
   and 2.**

### The source comparison is strict by default

Two different things get verified, and they are not interchangeable:

| | What it proves | Where it runs |
|---|---|---|
| **The committed view** | It is internally valid and still matches the digest recorded at sync time. | `check:claims`, in `test`, `build` and `predeploy`. Needs no product checkout. |
| **The source comparison** | The committed view still re-derives from the product manifest — the half that catches a claim corrected upstream that never crossed. | `check:claims` itself when `HUB_DIR` names a product, **and** `tests/claims-contract.test.ts`. |

The storefront battery **fails** when the product is absent. That inversion came out of the Codex
contrast: it first followed the repo's older hub-fidelity convention — warn, pass, and let
`HUB_REQUIRED=1` opt *in* to strictness — and the contrast measured what that means in practice:
`npm test` with no product checkout is green with two silent skips. This contract's rule is the
opposite one, so **silence is strict**.

#### `CLAIMS_SOURCE_UNAVAILABLE` is RETIRED from the paths that publish

This section used to say the exemption "still exists, because a storefront-only CI genuinely cannot
check out the private product", and that it was safe because it was *declared* in the workflow where
a reviewer could see it. **A declared exemption is still an exemption**, and the re-audit measured
what it bought: with it set, the two re-deriving tests become SKIPs — `70 tests, 68 pass, 2 skipped,
exit 0` — and a product claim promoted from `implemented_unaccepted` to **`accepted` with no
evidence at all** survived CI *and* deploy with the run green. Two `SKIP`s are NO HE PODIDO MIRAR,
never evidence.

So the topology is now:

1. **`check:claims` re-derives** whenever `HUB_DIR` names a product: it builds the view from the
   manifest and compares it byte for byte, printing `RE-DERIVED here … byte-identical`. Different is
   `1`; absent or unreadable is `2`. No environment variable excuses it.
2. **CI and deploy check out the product** at the object the committed provenance records and run
   `scripts/pin-product.mjs`, which refuses a ref that does not resolve, a dirty checkout, and a
   checkout whose manifest is not the recorded blob. `actions/checkout` is pinned to an immutable
   SHA with `persist-credentials: false`.
3. **Provenance names the blob, not only the commit.** A commit says which tree was checked out; the
   blob is the object holding exactly those bytes. Both sync commands — the full one and
   `--only=claims` — obey one precondition and write `{commit, manifestBlob}`.

**Test anchors**, so this prose is checkable rather than believable:

| Claim | Anchor |
|---|---|
| The publish path refuses a drifted source | `tests/claims-contract.test.ts` · *the publishing gate itself refuses a drifted source* |
| Provenance names the object holding the bytes | *the recorded blob is the object that holds the manifest, and it re-derives* |
| The pin refuses mismatch / absent ref / dirty | *MISMATCH…*, *ABSENT REF…*, *DIRTY…*, *INCONSISTENT…* |
| Both sync commands share one precondition | *both sync commands obey one precondition* |
| Mutants covering all of it | `scripts/verify-claims-mutants.mjs` · M40, M41, M42, M43, M44, M45 |

The public view is a lossy transform. Dropped on purpose:

- `controlRoutes` and `acceptance.tests` — internal `file:line` anchors;
- `measurement.auditReport` — a path inside the private product tree, with no consumer on the
  public site. `measurement.auditReportSha256` stays, so the measurement is still pinned to an
  exact report without publishing where that report lives.

Everything the storefront renders or derives is kept, and the validator **rejects** a view that
projects `auditReport` anyway.

### Two evasions that were measured, not imagined

A post-fix audit walked through the first version of this gate twice. Both holes are closed, both
have a test and a killed mutant, and both are worth knowing about before touching the code:

1. **A skipped check is not a passed check.** Turning the one real re-derivation test into a skip
   parsed cleanly and left the battery green at `49 pass / 1 skip`. The battery now runs a
   **census**: with the product on disk it must record **zero skips** and must name each fidelity
   test as passed.
2. **Compare labels the way a reader sees them.** `'Built '` — one trailing space — is a different
   string and the same word on screen, and it passed a raw `===`. Labels are now compared
   NFKC-folded, format-controls stripped, whitespace collapsed and case folded, against **every**
   accepted-presentation label rather than only `built`.

A third pass then found two more, and both are worth stating because they are opposite failures:

3. **A validator that rejects a valid claim is as broken as one that accepts an invalid one.**
   Two vocabularies were being mixed: `state` is canonical (`accepted`, `shipped`, …) and `status`
   is **derived** for presentation (`built`, `shipped`, …). The label check asked whether the
   *derived* `built` was one of the *canonical* accepted states — it is not — concluded that an
   accepted row was unaccepted, and compared `Built` with itself, rejecting a structurally valid
   `accepted` claim in all 13 locales. The accepted-presentation set is now **derived from** the
   canonical one, so the two cannot drift apart again.
4. **A hand-written list of invisible characters is a guess that ages.** The old class ran to
   U+2064 and stopped, so the bidi isolates **U+2066–U+2069** walked through: `⁦Built⁩`
   renders as *Built* and normalised to something "different".
5. **And `\p{Cf}` was not the set either.** A fourth pass got through with **U+034F** COMBINING
   GRAPHEME JOINER and the **variation selectors U+FE00–U+FE0F** — all `Mn`, none of them `Cf`,
   all `Default_Ignorable_Code_Point`. Per **UAX #44** that property is derived as
   `Other_Default_Ignorable_Code_Point + Cf + Variation_Selector` minus `White_Space`,
   `FFF9..FFFB`, `13430..1343F` and `Prepended_Concatenation_Mark` — so neither set contains the
   other. The normalizer now folds the **union**, and a second rule refuses **any** label carrying
   a default-ignorable character at all, whether or not it collides with anything: published copy
   has no legitimate use for a character the reader cannot see.

### The measurement is pinned by digest, not by path

`measurement` carries `auditReportTitle` and `auditReportSha256`. It deliberately does **not** name
where the report lives, because `design/` is a root the product's own export curation declares
private and "never ships" — and a re-audit measured the export gate returning **CLEAN** while the
exported tree still cited that private audits root from 27 files, this manifest among them. The
digest proves the exact bytes; the path only says where they sit. The validator refuses any string
under a private root anywhere in the manifest or the projection, and refuses the old path-shaped
field by name.

**Handoff, because this manifest was one carrier out of twenty-seven.** The other 26 are
pre-existing files under `core/`, `connectors/`, `modules/`, `operator/`, the build scripts, the
task file, the hooks and the CI workflows — none touched by this work, all outside the paths this
contract may edit. The systemic fix belongs in the export curation itself, which is the integrator's
surface. To reproduce: run the public export into a scratch directory and grep the result for the
private audits root.

### What this still does NOT cover

**Homoglyphs.** Cyrillic `В`, Greek `Β` and Latin `B` are different characters that render
identically; folding them needs the **UTS #39** confusables table, which is not vendored here. A
Greek Beta in "Built" is **not** caught, and it cannot be: every non-Latin locale here is made of
letters the rule admits wholesale and cannot tell apart from lookalikes. This is stated rather than
discovered because the unbounded version of the claim — "compare the way a reader sees them" — has
been falsified three times. The invisible-character class is closed **by construction** (see
*The fifth evasion*, below — the denylist described in findings 4–6 did **not** close it); the
confusable class is **open**, and belongs to the same later job as the absolute-language parser.

**The closure is scoped to the status labels, and the prose beside them is now closed too.**

The allowlist is wired to the status labels and only there. `validateCopy`, which checks the
translated prose, was a separate and open hole — and it is the one this contract cared about most,
because it is where a retired promotion lives:

- `forbidden[locale]` was a raw `String.includes`, so **one `U+200B` inside a forbidden phrase made
  the test false and the promotion shipped uncaught**. Measured on the real bindings:
  `sessions-live-operations [en]` with *"its tasks, its progress"* raised **one** `still promotes`
  error in plain text and **zero** with a ZWSP after the first letter. It compiled as a `\u200B`
  escape, kept both limit markers, and left `npm run check` at zero errors, `check:claims` LIMPIO
  and the battery green. Chromium 151 measured both strings at the same **186.71875 px** canvas
  width with **zero** differing pixel channels.
- **CLOSED.** Prose is now compared through a fold of the invisible class — NFC, line-breaking
  controls to spaces, invisibles removed, whitespace collapsed — applied to **both operands**, since
  folding only the sentence leaves the phrase carrying whatever the binding author typed.
- **`limits[locale]` deliberately does NOT fold, and that asymmetry is the rule.** It fails when the
  marker is *absent*, so a stray invisible there RAISES an error; folding it would let a broken
  marker pass. `forbidden` fails when the phrase is *present*, so an invisible there SUPPRESSES the
  error. Each check errs toward raising. "Normalise everything consistently" is the rule that breaks
  it.

The allowlist remains the wrong instrument for prose — sentences legitimately carry punctuation the
five-character inventory excludes — so the fold, not the allowlist, is what closes this.

**Test anchors:** `tests/claims-contract.test.ts` · *a retired promotion hidden behind an invisible
character is still ROTO* · *BOTH operands are folded, not just the sentence* · *NON-TRIGGER: honest
copy, and a negation built from the same words, both pass* · *the limit marker stays deny-closed*.
**Mutants:** M37 (compare raw), M38 (fold one side), and **M39**, which writes the promotion back
into the shipped copy as a compilable `\u200B` escape and must die.

**What stays open** is the confusable class, which no fold reaches: Cyrillic `В` is a letter and
passes, as it must.

### Four more, from the re-audit of the fixed tips

6. **`\p{Cc}` was missing too.** `U+007F` DELETE is neither `Cf` nor `Default_Ignorable`, and Chrome
   was measured rendering `Bu<U+007F>ilt` with the same DOM width, the same canvas width and the
   **same pixel hash** as `Built`. The set became `Cc ∪ Cf ∪ Default_Ignorable`, and the order of
   operations is fixed: line-breaking controls become spaces first (so a tab cannot invent a
   collision), then everything invisible is stripped (so `U+FEFF`, which JS `\s` matches, does not
   survive as a space). **That set was itself evaded and is no longer the barrier** — the
   normalizer keeps it, but what a label may contain is now decided by the allowlist in
   *The fifth evasion*, below.
7. **`false` was passing as a killed mutant.** `mutation !== null` is true for `false`, so an
   `accepted` claim could carry a mutant record that says the mutant survived. A record, or nothing
   — and its `applied`/`compiledOrParsed`/`killed` must all be true.
8. **`refs.enterprise` was never checked.** Two of three repositories in a three-repository
   measurement were validated. All three are now.
9. **A surface's `route` was decorative.** `/not-the-roadmap`, synced across manifest and view so the
   digest agreed, left every check green. Routes are now checked against a census of the pages the
   site actually serves, and a roadmap surface must name `/roadmap` — a real page that is the wrong
   page still fails.

And the mutation ledger itself was wrong about what it proved: "some failing test matches" scored a
kill on any test in the wreckage. Attribution is now **exclusive** — the named test must fail and
every other casualty must be declared, so each mutant's blast radius is written down rather than
ignored.

### The fifth evasion, and why the rule is now inverted

Findings 4, 5 and 6 above are the same mistake three times: each widened a **denylist** of invisible
characters, and each was falsified by the next character nobody had thought of. `Cc ∪ Cf ∪
Default_Ignorable` was the third such set, and a canvas sweep walked through it too, with two
characters that belong to none of those classes:

| Character | Category | Why the denylist missed it |
|---|---|---|
| **U+FFFC** OBJECT REPLACEMENT | `So` | a symbol, not a control or a format character |
| **U+18A9** MONGOLIAN ALI GALI DAGALGA | `Mn` | a combining mark, and **not** `Default_Ignorable` |

Both were measured rendering pixel-identical to `Built` and `Shipped`. **Enumerating what is
invisible is unbounded, because "invisible" is a property of the font, not of Unicode** — so the
rule is inverted. The contract now allows only what the labels **demonstrably need**:

- **Measured, not guessed.** Across all **65** published status labels (13 locales × 5 statuses) the
  complete inventory outside letters and digits is **five** characters: 30 × `U+0020` SPACE,
  11 × `U+002C` COMMA, 1 × `U+2019` (fr *"À l'étude"*), 1 × `U+30FB` (ja) and 1 × `U+FF0C` (zh).
  **No character of category M, S or C appears in any label**, so refusing those outright breaks
  nothing that exists.
- **Everything else is refused by default** — every stray dash, quote and separator included, not
  only the invisible ones. The category rows for M, S and C remain as **diagnostics**: they name
  *why* a character was refused, for a gate a human has to act on.
- **NFC first, and not NFKC.** NFC composes a legitimately decomposed `é` into one letter instead of
  refusing it as a combining mark — the opposite failure direction, and the one a denylist never
  had to think about. NFKC is wrong here: it would fold `U+FF0C` onto the ASCII comma and quietly
  rewrite Chinese copy.

**This class is now closed by construction rather than by enumeration**, which is a different and
much stronger statement than findings 4–6 were able to make. It is verified by sweeping the whole
scalar space: of **1 112 064** scalars, the **963 593** in categories M, S or C are admitted
**zero** times.

**And the tidy version of that sentence was wrong, so here is the measured one.** This first said
the admitted set is "exactly letters, digits and those five marks". It is not: **147 542** scalars
are admitted — 145 613 letters, 1 924 digits and the five marks — while **59 letters are REFUSED**.
They are the Unicode **composition exclusions**: NFC *decomposes* `U+0958` DEVANAGARI LETTER QA into
`U+0915` + `U+093C`, and the nukta that comes out is a combining mark, which the M row then refuses.
The same happens across Devanagari, Bengali, Gurmukhi, Oriya and others.

⇒ **At the 2026-08-09 measurement, the 13 shipped locales are LIMPIO; future Indic support is NOT VERIFIED.** Those are two
different statements and the distinction is the point. None of the 65 published labels contains one
of the 59, and all 65 pass — that is measured. A Hindi or Bengali label written with a precomposed
nukta **would be refused**, which is a **known false rejection**, not a verified pass and not a
live defect. Closing it needs the (base, mark) pairs of the canonical decompositions, and **not** a
blanket "allow `Mn` after a letter": that would re-admit `U+18A9`, which is exactly what evaded the
denylist in the first place.

**Every figure above is recomputable, and fails loudly when it drifts.** They are not prose:

```
# in the storefront repository, where the rule itself lives
npm run census:labels     # scripts/census-label-charset.mjs
```

It re-derives the census from `scripts/claims-contract.mjs` and exits **1** if any documented figure
disagrees — in either direction, so a rule change that nobody wrote down here is as loud as a
number typed wrong. The qualitative half (composition-exclusion letters refused, no shipped label
affected) is a test in the battery, so it runs on every gate rather than on demand.

## Handoff — the product-side gate belongs to the integrator

This contract does **not** wire the product gate, because `Taskfile.yml`, `.githooks/pre-push` and
`.github/workflows/` are the integrator's surface and a mixed PR would collide. The contract and
its anchors, so it can be cabled without re-deriving anything:

- **Task name:** `lint:public-claims`, read-only. It never modifies content.
- **Exit codes:** `0` LIMPIO · `1` ROTO · `2` NO HE PODIDO MIRAR. **CI must fail on 1 and 2.**
  Missing roots, unreadable files, parser crashes, absent canonical claim data or unavailable exact
  refs are `2` — "I could not look" is not "it is clean".
- **Anchors to cable:**
  - `Taskfile.yml` — add `lint:public-claims` beside the other `lint:*` tasks.
  - `.githooks/pre-push` — add it to the fast-lint set, so it runs on feature branches too
    (a claims defect is cheap to catch and expensive to land).
  - `.github/workflows/mainline-ci.yml` — add it to the lint job.
- **What it must check, product-side:** this manifest parses; `schemaVersion` is known; claim IDs
  are unique; every `acceptance.job` exists in `jobs[]`; every `components[]` entry resolves;
  no claim is `accepted` while a component is not; no claim is `accepted` with `evidence: null`,
  `mutation: null` or missing refs; `controlRoutes` entries point at files that exist.
- **The forbidden-language corpus is a separate, later job.** The audit measured **245 lines in
  176 files**, of which only **79 are positive absolutes**. It needs a real parser that separates
  rendered copy from code, comments, test fixtures and non-promotional error strings, plus a
  manifest of exact hashed exceptions. **Do not run a global replacement** — 166 of those lines are
  honest negations, French UI error text, citations or frozen snapshots, and deleting them would
  make the product *less* honest. Only the copy directly bound to these claims was corrected.
