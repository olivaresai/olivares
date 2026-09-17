# Seat spend-limit enabled state, v1

SES1. Ratified construction: `assessments/architecture/r86-seat-enabled/CONSTRUCTION-REVISION-2.md`
(Root Astra, September 12, 2026). Mandatory context: SDD 02 (data, transactions,
FinOps), SDD 04 (console/CLI/configuration consistency) and the D06 exact-money
program.

## What this contract fixes

`Policy.Enabled` is the authoritative current selection fact for user, group and
organization spending limits (`spend_limit` policies). Before SES1 the legacy
spending-limit readers ignored it, so a disabled `spend_limit` row still governed
enforcement — unlike T0, which has always queried `enabled = true` independently. A row an administrator had disabled kept
denying requests, kept contributing group membership, and kept the principal in
the effective view.

## Observable behavior

A disabled `spend_limit` row:

- does **not** govern enforcement, effective limits, group-membership queries or
  implicit effective-principal enumeration;
- **does** remain discoverable through the existing administrative list and get,
  with unchanged keyset paging;
- **does** remain eligible for create-or-replace matching, so upsert reactivates
  it in place rather than forking the logical key.

Selection happens before precedence and before any monetary interpretation.
Among enabled candidates the existing order user > group > organization and the
existing numeric / minimum / unlimited tie-breaks are unchanged. A disabled
candidate falls through to the remaining enabled ones: a disabled user spending
limit yields the enabled group or organization spending limit, and a disabled
*unlimited* user spending limit stops shadowing a stricter organization spending
limit, which makes the principal more constrained, not less. When no enabled
candidate exists, no user, group or organization spending limit applies to the
principal — the existing no-policy result, not a zero-amount spending limit.
Other budgets still apply.

`CheckSpendLimit`, `SpendLimitEffective` and `ReserveSpendLimit` all resolve
through `resolveSpendLimitForPeriod`, so they agree by construction; no extra
production seam was added for them.

Group spending limits are filtered before parsing **and** before seen-group
tracking, so a disabled group spending limit can neither reach the group-authority
read — where a malformed or inaccessible group reference would fail the whole
effective view — nor claim the group key and mask an enabled row for the same group.

Implicit effective-principal enumeration no longer seeds a principal from a
disabled user spending limit alone. Explicitly requested `user_ids` and sampled historical
spend discovery are unchanged: a principal with real spend still appears.

T0 is untouched. It filters `enabled = true` independently and verifies `Enabled`
in `listAttemptPolicies`; its validation and digest are unchanged.

## What SES1 deliberately does not change

The shared administrative and upsert listing is **not** filtered. It is the
traversal `findSpendLimitPolicies` matches against, and filtering it would make
upsert stop finding the disabled row it must reactivate, silently forking one
logical `(scope, period)` key into a second id.

That traversal is paged, and the matching is proven **across** a page boundary:
`listSpendLimitPolicies` reads `listCap` (1000) rows per page in `id ASC` order,
so a fixture whose disabled canonical row is first, followed by 1000 fillers
and then the enabled duplicate of the same scope and period, spans two pages.
The final filler and duplicate occupy page two. This gives a single-page pager a *wrong* answer rather than a slow one — it would
miss the duplicate, leave it alive, and fork the key. Create-or-replace still
reactivates the exact lowest-id row, heals the page-two duplicate, and leaves one
row holding the key, on both engines.

There is **no response, JSON, SDK or audit-snapshot shape change**. The
`SpendLimit` object is also the Anthropic-compatible gateway response on
`/v1/organizations/spend_limits` and its descendants; an additive `enabled` field
was proposed in revision 1 and withdrawn in revision 2. Legacy audit bytes retain
their current interpretation, and no retained snapshot is migrated.

Upsert semantics are unchanged: it replaces the Spec by design, reactivates the
lowest-id matching row across enabled and disabled rows, retains that id, and
heals duplicates inside the existing atomic audited mutation. Exact original
custody for replaced or deleted records is the separate, still-open R1/R2
obligation and is not claimed here.

## Known gap: there is no enable/disable control yet

Governance policy GET/PUT accepts `abac` and `approval` kinds only, so it is not
a spend-limit state route, and none exists elsewhere. SES1 does not claim
otherwise. Today the only way to flip the state is the typed Policy update, which
re-encodes the decoded Spec.

The complete product therefore still requires **SES2**: an explicitly authorized
native state interface, current and historical state representation, optimistic
version checks, and an optional typed Policy Enabled-only update that preserves
raw Spec bytes. Its exact method, permissions, audit and byte-preservation
contract are tracked separately. This is a sequencing decision, not a removal of
the enable/disable feature.
