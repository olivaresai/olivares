<!-- SPDX-FileCopyrightText: 2026 Olivares.AI -->
<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# Deployment contract — names this engine RESERVES in your database

This page exists because a protection that costs the operator something must say so **before** the
operator meets it in production, not in a source comment they will never read.

Everything here is measured on PostgreSQL **15.18, 16.14, 17.10 and 18.4**, the four certified
majors, and re-measured whenever the rule changes.

## 1. The reservation

> **A trigger whose name ends in `_immutable` cannot be dropped, in ANY schema of ANY database
> where the append-only event-trigger fence is installed — including triggers that have nothing to
> do with this engine.**

The refusal reads:

```
ERROR:  olivares: refusing to drop the append-only guard <identity>
```

It fires on `DROP TRIGGER` and on any statement that takes such a trigger **by dependency** —
`DROP TABLE` of the relation it hangs off, and `DROP SCHEMA ... CASCADE` in so far as the fence
itself survives the statement.

### What this costs you

If your own schema already contains a trigger named `..._immutable` that this engine did not
create, then:

- you cannot drop that trigger while the fence is installed;
- you cannot drop the table that carries it;
- a migration of yours that does either will **abort**.

Measured 4/4: a foreign `alt.business_immutable`, in another schema and executing an unrelated
function, is refused.

### How to pay it

Pick one:

1. **Rename your trigger** so it does not end in `_immutable`. This is the cheap answer and the
   one we recommend.
2. **Do not install the fence.** It is a separate deployment artefact applied by a superuser
   (`GuardEventFenceStmts`), not something the engine creates at boot. Without it you keep the
   row-level append-only guards and the boot verification, and you lose the between-boots
   protection the fence adds.
3. **Drop the fence for the duration of the migration and re-apply it.** The artefact is
   idempotent and re-appliable by design.

## 2. Why it is database-wide, and why that is not laziness

The rule identifies the guard **by name alone**, because at `sql_drop` time there is nothing else
left to identify it by:

- the trigger is already out of `pg_trigger`, so the function it executed is unreadable;
- the table it hung off may be dropped in the **same** statement — that is the cascade case the
  rule exists to cover;
- any binding to the **schema** is a binding the schema's owner can rename, which is exactly the
  escape that binding removed (`ALTER SCHEMA ... RENAME` walked straight past the earlier rule).

A registry lookup does not help: the registry lives in the schema being dropped.

So the choice is a database-wide name reservation or no cascade coverage at all. We take the
reservation and publish it here.

## 3. There is a SECOND cost, and it is a name COLLISION rather than a name reservation

The rule above is about `DROP`. There is a separate one about `DISABLE`, and it costs you only if
you collide on **two** names at once.

> **A foreign trigger whose name ends in `_immutable`, executing a function named
> `olivares_block_mutation` IN THE SAME SCHEMA AS ITS TABLE, cannot be left disabled or
> replica-only — and the statement that tries is aborted and rolled back.**

The `ddl_command_end` leg counts guards that are `D` or `R`, and it identifies them by the bare
function name, same-namespace, trigger suffix and state. It does **not** establish that the function
it found is this engine's. So a foreign object that reuses **both** reserved names collides.

Measured 4/4: a foreign schema with its own pass-through `olivares_block_mutation` and a
`business_immutable` trigger calling it — `ALTER TABLE ... DISABLE TRIGGER business_immutable`
aborted with the Olivares "disabled or replica-only" error on 15.18, 16.14, 17.10 and 18.4, and the
trigger rolled back to `O`.

**How to pay it:** rename either one. Breaking a single half of the collision is enough, and the
function name is usually the easier half.

This is not new in 2026-08-04 and it is not caused by the structural matching added then; it is a
pre-existing boundary of the end leg that this page previously failed to state.

## 4. What is NOT reserved

- A trigger name ending in `_immutable` can still be **created**, **renamed**, and **enabled**, and
  can be **disabled** unless it also collides on the function name as in §3.
- Table, index, column and schema names are not reserved.
- The function name `olivares_block_mutation` is not reserved **on its own** — only in combination
  with the trigger suffix and same-schema placement, per §3.
- Databases where the fence is not installed are unaffected.

## 5. Quoted names are covered

A trigger whose name needs quoting — `"User_immutable"`, `"odd name_immutable"` — is covered.

This was a **defect until 2026-08-04** and it cut the other way: the rule parsed the text form of
the object identity, which quotes names that need it, so `"User_immutable" on public."Users"` put
a closing quote between the reserved suffix and the ` on `, and the pattern missed it. A **real
guard** whose logical name merely carried a capital letter could be dropped straight through the
fence. Measured 4/4 as a false negative before the fix.

It is now matched structurally as well, off `pg_event_trigger_dropped_objects().address_names`,
which carries `{schema, table, trigger}` with the trigger name bare. The two tests are a union, so
the rule can only ever refuse more.

## 6. What the fence does NOT protect against

Stated here as well as in the source, because an operator who reads only this page should not come
away with the strong claim:

- **Not the owning role, under SingleRole.** If the role that serves your application also owns
  its schema — the default the harness provisions — that role can defeat the protection, and not
  only by dropping things: replacing the shared guard function's body leaves `pg_trigger` reading
  byte-identically to a healthy guard while the next ordinary `UPDATE` goes through. That one **is**
  detected at the next boot by the manifest's byte-for-byte body comparison; it is **not** prevented
  in-session.
- **⚠ `TRUNCATE` is caught by neither the fence NOR the next boot.** A `BEFORE UPDATE OR DELETE` row
  trigger cannot observe `TRUNCATE` at all, and truncation changes no `pg_trigger` and no `pg_proc`
  field, so there is nothing for the boot comparison to notice either. Measured 4/4: a store whose
  audit ledger had been truncated by its owner re-opened cleanly on the next boot, took the fast
  path, and logged the fence as `installed`. **The rows are gone and nothing in this design reports
  it.** If that matters to you — and for an audit ledger it should — the answer is SplitOwner plus
  revoking `TRUNCATE`, not this fence.
- **Not a superuser, and not the fence handler's own owner.** PostgreSQL exempts DDL targeting
  event triggers from firing event triggers, so `DROP EVENT TRIGGER` and
  `DROP FUNCTION <handler> CASCADE` both succeed.

The strong guarantee needs the **SplitOwner** topology: the role that serves must not own the
schema.

> ⚠ **The boot does NOT tell you which topology it resolved.** This page previously said it did.
> The internal check returns a boolean, and the only thing written to the log is a **warning when
> the deployment is SingleRole**; there is no positive line for SplitOwner. So absence of the
> warning is the only signal you get, and absence of a warning is not a report. If you need to know
> which topology you are in, check the roles directly — do not infer it from a quiet boot.

See `core/internal/store/dialect/guardeventfence.go` for the rules themselves. Its comments carry
the measurement record: which majors each behaviour was observed on, and which of the limits above
are prevented, which are merely detected at the next boot, and which are neither.
