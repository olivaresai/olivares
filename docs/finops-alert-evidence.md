<!-- SPDX-FileCopyrightText: 2026 Olivares.AI -->
<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# FinOps budget-alert evidence (A4.2 and A4.3)

A budget alert has always recorded a number. What it could not record is **what kind
of number it is**, which policy produced it, or what was actually established when it
fired. A4.2 adds that evidence to the alert itself, keeps it durable, and lets an
authorized reader retrieve it.

The A4.3 transport preserves a bounded summary and the digest captured in the
transaction; delivery happens after commit. Consumers preserve unknown evidence,
and the durable backstop checks the recorded evidence and an eligible current
target before the existing governed action. This is evidence about an evaluation,
not operational spend admission or a guarantee that a cap cannot be exceeded.

The beta OpenAPI contract and the FinOps console describe the existing status and
alert-list responses. They do not add a second monetary evaluator.

## What the classification means

Every consumption figure now carries one of three classes:

| Class         | Meaning                                                     | What a reader may do with it                                                                               |
| ------------- | ----------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------- |
| `exact`       | Every component was established and their sum is valid.     | Use the amount.                                                                                            |
| `lower_bound` | A monetary floor is provable; the total is not established. | Trust the crossings the floor **reaches**. A threshold it does not reach is _unproven_, not "not crossed". |
| `unknown`     | Neither.                                                    | Nothing. It is **not** zero, **not** "within budget" and **not** a smaller total.                          |

The crossing decision is a **separate** field: `proven`, `not_reached` or `unproven`.
A `lower_bound` amount with a `proven` decision is a real crossing whose _amount
evidence_ is incomplete — never a doubtful crossing. No reader should infer any of
this from severity, the title or the legacy `truncated` flag.

Two rules produce most of the classifications:

- **A signed cost prefix is not a bound.** Cost samples carry legitimate credits, so
  an unread page can hold the −6 USD that cancels the 12 USD already read. A partial
  cost enumeration is `unknown` however large the prefix looks.
- **An unreadable dynamic reservation is not zero.** Reservation obligations are
  non-negative by domain invariant, so an exact cost plus a known static reserve is a
  genuine floor — _provided nothing observed contradicts the invariant_. A reservation
  row that is negative, malformed, outside the requested scope **or carrying no usable
  tenant evidence** makes the component `indeterminate`, and no bound may rest on it.
  The row's own `tenant_id` is checked the same way the strict cost reader checks it —
  entry, then type, then value — and is never filled in from the scope that asked for
  the read.
- **An unclassified state is not a figure.** A reserved total that does not carry a
  typed state — including a zero value — is `indeterminate`. Reading the _absence of a
  diagnostic string_ as a positive finding is what turned an uninitialised struct into
  an exact reserve of zero.
- **An absent optional policy field is not a stored `null`.** Absence takes the
  documented default; a present `null` (or any representation this reader cannot use)
  is a typed configuration fault, and no threshold is proven from one.

## The stored evidence

Two **nullable** columns were added to `finops.budget_alert`, with no defaults and no
back-fill:

- `amount_evidence` (`KindJSON`): the versioned envelope.
- `evidence_hash` (`KindText`): its digest.

The envelope (`schema_version = 1`) carries the identity of the alert/tenant/budget,
the policy **as read** (id, version and the financial fields actually used), the
classified amount as a canonical decimal string or `null`, each component with its own
state and causes, the decision with its normalized threshold and exact rational
target, the window/provenance/scope context, and the classification of the legacy
numeric projection. Money never round-trips through a JSON float.

The row, its envelope and its digest are written in **one insert**, under an id
pre-assigned before the envelope is built, inside the ingestion transaction. There is
no second transaction that could leave a row without its proof, and a deduplicated
crossing neither rewrites the first crossing's evidence nor emits again.

### Deduplication is a lookup, not a caught conflict

The writer takes one `store.TransactionLocker` key per tenant
(`finops.budget_alert.writer.v1:<tenant>`) on the ingestion's own transaction, before
it reads or writes any alert row, and then decides deduplication with an **exact
lookup** of the historical identity — tenant, budget, period start, threshold percent,
which is the unique index and nothing more. The insert happens only when that lookup
found nothing, and an unexpected conflict from it is **propagated**.

Catching the conflict instead is not equivalent, and the difference is a lost
ingestion. On PostgreSQL a unique violation aborts the whole transaction, so absorbing
it and reporting "already recorded" left the caller committing a dead transaction: the
second, perfectly good cost sample and its ledger row went down with it
(`SQLSTATE 25P02`, reproduced on PostgreSQL 16). SQLite does serialize its single
writer, which is why the defect was invisible there.

The writer **fails closed** when the lock capability is absent or fails: a scope that
silently dropped it would restore exactly that race, and an ingestion that refuses can
be retried while a lost sample cannot be recovered. The guarantee covers cooperating
writers on the same key; it does not repair an older binary that ignores the protocol,
and rows such a binary already wrote are recognised and preserved as
`legacy_unversioned`.

### The digest

`SHA-256` over the domain prefix `olivares.finops.alert-evidence.v1`, a NUL separator
and the envelope's canonical JSON (struct field order, no maps), i.e. **everything in
the envelope except the hash itself**, which is stored beside it.

It is a **content commitment**. It does not authenticate a producer, does not sign
anything, does not prove that any event was delivered, and does not let a later reader
reconstruct a past authorization decision.

## Reading it back

`GET /api/finops/alerts` accepts an optional `alert_id`, validated with the engine's
id parser, under the **same permission and tenant scope** as the list and inside the
same paginated envelope. Holding a reference grants nothing: another tenant's id
simply matches nothing, and a malformed one is rejected at the boundary.

The reader's rules:

| Stored state                                                      | Result                                                                                                                                            |
| ----------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------- |
| Both columns absent/NULL                                          | `unknown`, cause `legacy_unversioned`. The historical number stays visible as _the figure recorded then_; no exactness is inferred.               |
| Unsupported version                                               | `unknown`, cause `unsupported_evidence_version`.                                                                                                  |
| Malformed envelope, missing hash                                  | `unknown`, with the matching cause.                                                                                                               |
| Hash does not verify, or the envelope is about another row/tenant | `unknown`, cause `evidence_hash_mismatch` / `evidence_identity_mismatch`. Nothing is repaired on read.                                            |
| The envelope's own invariants do not hold                         | `unknown`, cause `evidence_envelope_incomplete`, `evidence_enum_invalid`, `evidence_decimal_not_canonical` or `evidence_internally_inconsistent`. |
| A row cell disagrees with the evidence that authorized it         | `unknown`, cause `evidence_row_mismatch`.                                                                                                         |
| Valid v1 envelope                                                 | The preserved classification and proof, as captured. Today's policy is never consulted to rebuild it.                                             |

### A digest is not validation

The hash commits to **bytes**, not to meaning: an envelope with empty components, an
unknown amount class, a decision that was never proven, a non-canonical decimal or a
target that is not `threshold × limit` hashes exactly as well as a real one, because
the digest is computed over whatever structure it is given. So a v1 envelope is also
checked against the contract before it is accepted, and three of its claims are
**re-derived** rather than trusted:

- `exact` = cost + static + dynamic, all three known; `lower_bound` = cost + static,
  with the dynamic component carrying the non-negative invariant.
- `proven` ⇔ amount ≥ target, and target = threshold × limit at lowest terms.
- the legacy `value_kind`/`spend_micro_usd` the envelope authorized are the ones its
  own class and figure produce.

The same reasoning covers the relations _between_ the envelope's parts, which a
digest cannot see either:

- the **effective reserve** appears twice — `policy.reserved_micro_usd` and the static
  component — and must agree, including its absence: a reserve that was not
  established is stated as an absence in both places and never completed with a zero.
- a **known reserve** carries its producer's domain: non-negative and int64, for the
  static reserve (a policy money cell) and the dynamic one (a checked int64 sum). Cost
  is deliberately exempt — credits are ordinary and a wide total keeps its exact
  decimal.
- a **configuration fault** that would have prevented this crossing cannot be stored
  beside it; every fault in today's vocabulary leaves either the limit non-positive or
  the reserve unestablished, and neither can produce a proven crossing.
- the **window and the subject are re-derived** from the captured policy and the
  sample instant with the same primitives the evaluator used (`periodStart`/
  `periodEnd`, `strictCostScope`), so a still-increasing interval that is not the
  sample's period, or a scope column the captured dimension does not express, is
  refused.
- every **cause** must be a word of the closed v1 vocabulary, not merely sorted,
  unique and non-empty.

Separately, the **row is bound to the envelope**: the historical spend, the limit, the
threshold percent, the severity, the dimension/key/period, the period bucket and the
trigger instant must be the ones the evidence committed to. Keeping a valid envelope
and changing one cell of the row beside it used to read as `valid`, so the DTO copied
`legacy_value_kind = exact` from the envelope and served the changed number. Instants
are compared as instants, not as text, because the two engines render a stored
timestamp their own way.

A refusal is reported with its cause. Nothing is repaired, and today's policy is never
read to fill a gap.

Budget **status** exposes the same evaluation in an authoritative `amount` section:
class, effective amount, remaining, components and per-threshold decisions. `remaining`
is `null` whenever the amount is not established — that is "not established", never
"nothing left" and never "plenty left". `forecast_certified` is `false` because
`projected_*` and the exhaustion prediction still come from aggregates this increment
does not certify.

`over_limit` is the authoritative decision about the limit **itself** (threshold 1.0),
evaluated whether or not the operator configured that threshold: `thresholds` lists
only what was configured, so a budget with `{0.5, 0.8}` otherwise had no authoritative
statement about being over its limit, and the only "over" a reader could find was the
legacy boolean — which cannot say `unproven`.

The older numeric fields are classified **one by one** in `legacy_fields`:

| Legacy field          | Classified `exact` only when                                                                            |
| --------------------- | ------------------------------------------------------------------------------------------------------- |
| `spend_micro_usd`     | the strict cost read re-derives the same raw actual cost.                                               |
| `remaining_micro_usd` | the authoritative remaining fits int64 **and** equals the figure that was published.                    |
| `over`                | takes the authoritative 1.0 decision; a published boolean that disagrees with it is not its projection. |
| `projected_micro_usd` | never — A4.2 does not certify the forecast.                                                             |

`legacy_projection` aggregates **exactly three** of them — spend, remaining and over —
and is the weakest of those three. **It does not cover the forecast**: `projected_*`
and the exhaustion prediction are always `unavailable` with `forecast_certified:false`,
and folding a permanent "unavailable" into the label would pin it there forever while
saying nothing about the three fields it is for. The forecast's standing is read from
`legacy_fields` and `forecast_certified`, never from that word.

This is a comparison, not an inference. Status keeps **two traversals** — the older
aggregates and this strict evaluation — and under PostgreSQL READ COMMITTED they may
not observe the same rows, so a field the authoritative evaluation cannot re-derive is
`unavailable`: "this older field is not a projection of this evaluation", never "the
budget has nothing left". An exact amount whose `limit − effective` leaves int64 is
the case that makes the distinction concrete: the authoritative remaining is the wide
decimal, and the legacy field is `unavailable` rather than a certified zero.

## Upgrade

Declaring the two columns in the descriptor is the whole migration: the existing boot
path (`applyModuleTables` → `reconcileColumns` → module SQL migrations → verification)
adds missing nullable columns on both SQLite and PostgreSQL. `KindJSON` uses this
engine's portable representation — on PostgreSQL the current dialect maps it to TEXT,
and no SQL depends on JSONB.

The reconciler runs **separate statements per column**, so an interruption can leave a
partially expanded schema; the next start adds only what is missing and fills no
evidence into rows written meanwhile — verified in **both directions** (either column
missing) on SQLite and on PostgreSQL. A failed migration must prevent serving with an
incomplete schema.

Reopening the storage with the **old descriptor** and writing through it does not
destroy the evidence columns or the rows that carry them: the rows an older writer
creates read as `legacy_unversioned`, and a later reopen with the current descriptor
still verifies the digests written before. The scope of that claim is storage and CRUD
across the descriptor — not that an arbitrary older binary is compatible in every
respect, and specifically not that a binary predating the deduplication protocol above
is safe to run against this schema.

## Limits of A4.2

- **A4.3 is required before publication**: the typed SDK/Protobuf/NATS summary, the
  notify/security/backstop consumers, the OpenAPI response contract and the console.
  Until then the emitted events carry the durable digest in `DetailHash` but no
  structured evidence, and any consumer reading the old numbers/booleans directly has
  not been certified.
- Group accounting, the completeness of the budget catalogue, the older aggregates,
  `CheckSpendLimit` and the forecast remain open D02 work. An unresolved group scope is
  refused here rather than approximated with tenant-wide spend.
- The evidence describes **the evaluation that actually happened**. It does not
  reconstruct a policy that was in force at some earlier instant, an authorization
  decision, or a snapshot nobody kept. Read consistency is what the caller's `Scope`
  provides: a bounded, self-consistent enumeration, not an instantaneous global
  snapshot under PostgreSQL READ COMMITTED.

## Console and beta API (A4.3 cut 3)

`GET /v1/m/finops/budgets/{id}/status` publishes `amount`: classified decimal
strings or null, the three components, threshold decisions, `over_limit`, the
individual `legacy_fields`, and `forecast_certified: false`. The main amount uses
`effective_micro_usd`; it is not the older raw-cost `spend_micro_usd`. Remaining
is available only for exact consumption. Both console status helpers use this
same contract.

`GET /v1/m/finops/alerts?alert_id=<UUID>` uses the existing `finops:budget:read`
permission and tenant scope. The optional reference combines with `budget_id`,
`limit` and the opaque `cursor`; the response retains `items`, `cursor` and
`has_more`. A malformed nonempty reference is 400, and an authorized other tenant
receives an empty list. Possession of a reference grants no access. The console
uses its normal authenticated HTTP client, keys queries by tenant and reference,
clears the reference on tenant change, and withdraws the result on request error.

Each alert exposes `id`, `legacy_value_kind` and
`amount_evidence.{state,cause,evidence_hash,envelope}`. A stored digest alone is
not validation. Unknown or unsupported evidence never falls back to the older
numeric figure. Only explicitly unversioned historical evidence may show a safe
legacy number as **originally reported**, without certifying it; a legacy number
outside JavaScript's safe integer range is unavailable.

Canonical micro-USD strings are formatted without floating-point conversion and
retain all fractional micro digits. The console bounds presentation to 128
characters; malformed or longer strings are unavailable, without imposing a new
ledger limit. A lower bound says **At least** in the main figure. Unknown is not
zero and supplies no consumption bar or financial risk color. The bar is based
only on exact canonical consumption and a positive canonical limit; its percentage
is clamped for display. A proven crossing can still be displayed for a lower bound.
Legacy run-rate forecast remains visible, explicitly **not certified**, and does
not determine the crossing color. Alert timestamps and recorded references remain
visible; historical evidence is never rewritten from today's policy.

These controls do not close D02 reserve/dispatch/settle, concurrent admission,
external unknown-outcome reconciliation, outage behavior, certified forecast,
HA, Enterprise integration or release qualification. Browser demonstration and
console bundling are separate integration work.
