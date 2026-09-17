<!-- SPDX-FileCopyrightText: 2026 Olivares.AI -->
<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# BR1 bounded storage reads (source adapter)

Status: source adapter for Root review. Contract: finops-exact-money
BOUNDED-READ-DESIGN v0.3 (option A), its BR1 construction ratification
(R1-R3), and the BR1-W1 wide-descriptor construction (r96
ROOT-CONSTRUCTION-REVISION-1 with ROOT-RATIFICATION R4.1/R4.2). No production
caller exists. R1/R2 custody, HTTP activation,
migrations and deployment quotas are out of scope.

## Interface

`store.BoundedReaderFactory` is an optional Scope capability.
`NewBoundedReader(BoundedReadOptions)` validates nine explicit positive limits
and engine eligibility. It opens no transaction and runs no SQL. A reader
offers `GetPolicySnapshot`, `ListPolicySnapshots`, `GetExtension`,
`ListExtensions` and a content-free `Usage` snapshot. There are no defaults
and no fallback to ordinary Get/List/Lock.

## SQLite journey (per data method)

1. Pre-I/O validation: canonical ID or cursor, positive `Limit` within
   `maxLimit` and `MaxRowsPerPage`, no custom sort, `MaxFilters` counted
   before confinement rewrites filters, and `MaxParameterBytes` over kind,
   ID, cursor, filter names, operators and finite model values. A refusal
   consumes nothing and leaves the reader usable.
2. Once per reader: a budgeted encoding observation (`pragma_encoding`
   mapped to an integer). UTF-8 multiplies TEXT octets by 1, UTF-16LE/BE by
   the ratified 2. BLOB is never multiplied.
3. List only: guarded key selection of `Limit+1` IDs in ID order. Keys are
   projected only when they are TEXT of canonical length. A rejected or
   non-canonical key is an explicit metadata error. The extra key sets
   `HasMore` only.
4. Per selected ID: inspection of fixed storage-class codes, octet lengths
   and boolean range flags, one inspection group per statement (below). Go
   admits each column (class, nullability, `MaxCellBytes`) and computes the
   row charge (`MaxRowUnits`).
5. Complete page admission against `MaxPageUnits`, `MaxUnits` and `MaxRows`,
   and the counted size of every planned payload statement against
   `MaxQueryBytes`, before the first payload statement.
6. Per row: one exact-ID payload statement per payload group. A private
   subquery computes `row_ok` from the complete row's admitted classes and
   octet counts. Every returned value is `CASE WHEN row_ok THEN col END`, plus
   a reject flag and a NULL flag per nullable column of that group. Nothing is
   exposed until the call succeeds.

Tenant, soft-delete, filters and forced lineage apply to every statement.
Size is never a WHERE predicate.

## Construction order and private names (correction 1)

- Input admission is two-pass. The first pass validates and sizes every caller
  filter and the forced workspace predicate against `MaxFilters` and
  `MaxParameterBytes` without copying or proportional allocation. The second
  copies into an exactly sized slice. A typed nil `[]byte` stays nil, keeping
  the ordinary repository's SQL NULL binding. Present bytes, including empty,
  are copied non-nil.
- Every data statement renders twice through one renderer. A checked counting
  pass computes its exact length. Then `MaxQueryBytes`, the envelope and the
  argument count are checked and the envelope reserved. Only then are the SQL
  text and argument slice built. Inspection computes checked descriptor arity
  and expression counts before allocating admissions or scan destinations.
- `MaxQueryBytes` bounds the exact statement string passed to `QueryContext`,
  after the dialect's placeholder rewrite (PostgreSQL `?` becomes `$1`, `$10`,
  and so on). It excludes wire framing, parameter values and driver-internal
  rewrites (BR1-W1 correction 1). Both renderer passes apply the dialect's
  `dialect.Rebinder` as text is written, the same quote-aware state machine
  behind `Rebind`, so the nonallocating count is the emitted length and the
  statement is built once, already rewritten. This holds for the setting and
  encoding observations, key selection, row locks, inspection, payload and the
  complete-page payload preflight.
- The payload derived table exposes only renderer-owned aliases: `olivares_br_ok`
  and `olivares_br_c<ordinal>`. Descriptor names appear only as inner source
  columns and at the Record mapping, so no valid field name can collide with an
  alias.
- Superseded limit (BR1-W1): the single-statement reader failed first on
  SQLite 3.53.3 / modernc v1.54.0 at A >= 997 descriptor columns (n >= 992 - s
  fields), with `Expression tree is too large (maximum depth 1000)` from the flat
  payload `row_ok` chain, not at the inspection column ceiling (r97 boundary
  measurement). On PostgreSQL 16 a 1600-column table exceeded the 1664-entry
  target list. Both are removed by the group plan below.

## Wide descriptors (BR1-W1)

Groups are private to the SQL adapter; callers never select a group size or
reassemble a row. `C` is the qualified result-column ceiling: 1664 for
PostgreSQL 16 and 2000 for this SQLite build. A new engine build needs new
qualification. There is no descriptor cap.

- Plan: before any descriptor-sized allocation, the reader computes the checked
  arity, both group plans and the metadata total `16 * groups + 17 * sum(e_j)`,
  and admits that total against the remaining page and traversal units. This is
  a calculation; each issued group still reserves its own envelope.
- Inspection groups: the largest consecutive ordinal runs whose metadata fits
  `C`. A column's class, length and SQLite boolean flag stay in one group. Each
  group is `SELECT <classes>, <lengths>, <flags> ... WHERE <predicate> AND id = ?`
  and is decoded by the same plan. Only the first group may report ordinary
  `ErrNotFound` (unlocked Get); absence in a later group, after a row lock or for
  a selected List key is `ErrBoundedReadConsistency`.
- Payload groups: the largest consecutive runs of `g` columns, `v` nullable, with
  `g + 1 <= C` (inner projection) and `g + 1 + v <= C` (outer projection). Aliases
  use absolute ordinals. The derived table projects only the group's columns.
- Guard: the complete row predicate is a balanced binary conjunction, `(left AND
  right)` split at the midpoint, so its depth is logarithmic in arity. Every
  payload group carries the complete guard over every admitted column.
- Both the derived table and the outer payload projection carry `LIMIT 1`.
  Without the outer limit SQLite flattens the derived table and copies the
  complete guard into every outer `CASE`, which is quadratic in width.
- Every statement runs on the reader's transaction with the complete tenant,
  soft-delete, caller-filter and forced-lineage predicate and the exact ID. The
  PostgreSQL Mutate row lock is taken before the first group and held until the
  transaction ends.
- SQL NULL flags remain separate, budgeted metadata under the same authorized
  predicate. They never authorize a Record or a value after a rejection.

Accounting per row with `k` payload groups: group row `R_j = 8 + 17 + 17 v_j +
cells_j`; `MaxRowUnits` charge `sum(R_j)`; statement envelope `8 + R_j`. Page
admission checks the sum of every selected row's payload envelopes and the
count of logical rows (`MaxRows` never counts groups). The row slot is reserved
on its first payload group only, and nothing is refunded.

Observation (R4.1): after each group's scan and close, the delivered values are
observed. The final group increments `PayloadRowsObserved` before validation. A
known error stops before any later group: an early rejected group leaves zero
observed rows, a rejected final group one. Neither returns a Record.
`ObservedComplete` becomes false only after a driver, cancellation or
rows-close failure.

## Accounting and state

Envelope per statement: `8 + maxRows x (8 + sum of column maximum charges)`,
reserved before `QueryContext` and never refunded. Charges: NULL 9, fixed
scalar or SQLite integer flag 17, string or bytes 9 + bound. All additions,
multiplications and the SQL length are checked. A reservation failure is
terminal; the first one charges nothing. After SQL starts, every error except
`ErrNotFound` makes the reader terminal and later calls return
`ErrBoundedReadTerminal` wrapping the cause. A concurrent call gets
`ErrBoundedReadConcurrent` without disturbing the active call.

## Confinement

`ConfineWorkspace` attaches the factory in the same single final step as the
bundle and DAB1 ports (eight independent bits, 256 sets). The confined port
installs its own boundary. Policy reads are denied before I/O. Ext reads
force the descriptor lineage predicate, with a SQLite storage-class guard,
before inspection or projection. Narrow named or embedded-Scope wrappers do
not gain the capability.

## PostgreSQL

Construction inputs: `r88-bounded-reader-pg` ROOT-DECISION and ROOT-ACCEPTANCE
(PostgreSQL 16, pgx v5.10.0). The store records a private five-mode pgx
execution-mode fact from the parsed connection config; an unparsed mode is
refused by the factory without SQL.

Every data method first runs a reserved setting-class statement in the
current transaction: five INTEGER classes for server encoding, client
encoding, `bytea_output`, `standard_conforming_strings` and server major 16.
Server and client encodings must each be UTF8 or LATIN1; SQL_ASCII and unknown
classes are unavailable. `simple_protocol` also requires a UTF8 client and
`standard_conforming_strings=on`. A descriptor with BYTEA read in `exec` or
`simple_protocol` requires `bytea_output=hex`. Nothing is SET or retried.

TEXT is measured as
`pg_catalog.octet_length(pg_catalog.convert_to(c, pg_catalog.current_setting('client_encoding')::pg_catalog.name))`,
the returned client bytes, for keys, inspection and the payload `row_ok`.
BYTEA uses logical `octet_length`. Column types come from the descriptor DDL,
so inspection returns NULL flags and lengths only; native booleans are
charged 10 units.

In Mutate (READ COMMITTED), each selected payload row is locked `FOR UPDATE`
in ascending ID order under the complete authorized predicate before it is
inspected. A row that no longer matches after the wait is a consistency
failure (a Get lock that finds nothing is ordinary NotFound). The lookahead
key is not locked. Row locks do not exclude phantoms or logical duplicates.

Conversion failures (22P05, 22021) and the driver's simple-protocol
configuration refusal make the method unavailable with the backend cause
wrapped, and the reader terminal.
