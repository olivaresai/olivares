# ADR-0022: Source scoping by subject axis (session / agent / user / user-group / role), with row-level effect and a versioned, dual-controlled enforcement posture

- **Status:** accepted
- **Date:** 2026-07-07
- **Deciders:** Fran Olivares
- **References:** ADR-0003 (RRW map — permitted vs observed), ADR-0019 (Cedar scoped grants).

## Context and problem statement

The source binding (`modules/sourcescope`) binds a connected source — an MCP
server, a model, a provider, a knowledge base, a data source — to exactly one of three
**containment** scope-trees: `workspace`, `agent_group`, or `folder`
(`schema.go:52-62`, `binding.go:33`). It answers "an actor **in** this scope may reach
this source".

The product vision requires four more axes the containment model cannot express
ergonomically:

- **"this SESSION sees source X"** — a single running session.
- **"this USER / group-of-users accesses sources Y"** — a named human, and a directory
  group of humans.
- **"this specific AGENT (not its group) sees only Z"** — one agent, not the agent-group
  it belongs to.

Today those axes are only *approximated* by authoring a raw Cedar grant — no binding
ergonomics, no listable/auditable row, no access-map projection, and (for the reverse
question "which sources can subject S reach?") the unsolved reverse-query problem
(`accessmap.go:44`). Meanwhile **model** governance already carries a rich
SUBJECT model — `subject_kind ∈ {user, role, agent_group}` with allow/forbid rows and
a `forbid-overrides-allow` algebra (`modelgovernance.go:98-100`,
`modelaccessgate.go:204`). There is a governance asymmetry: **models are governed richly
by subject; sources are governed narrowly by containment.** This decision closes it.

A second-order requirement comes from the incumbent analysis (verified
against vendor docs 2026-07-07): AWS Q Business makes *relaxing* an ACL a dedicated,
one-way, audited IAM operation (`qbusiness:DisableAclOnDataSource`); Google's data-store
ACL posture is **immutable after creation**. Our differentiator is a posture that is
**mutable, versioned and audited** — but relaxing it must be a **privileged, dual-control,
audited** operation, never a silent toggle. No incumbent expresses per-agent or
per-session source scoping — this is verified white space, not a hypothesis.

## Decision drivers

- **Consistency with model-access.** The same subject vocabulary and the same
  `forbid-overrides-allow` algebra, so operators reason about "who may reach a source"
  exactly as they reason about "who may use a model".
- **Hot-path cost.** The resolver runs on the models EXECUTE path (`ScopeGate`) and the
  knowledge retrieval path (`RetrievalScopeGate`). The identity axes must not add a
  policy round-trip per resolve.
- **Auditability & reverse-query.** "List every source scoped to session S / user U /
  group G" must be a single indexed query, not a Cedar reverse-walk (the reverse-query
  is not solved).
- **UI.** One binding shape the console (a follow-up) can render and author.
- **Back-compat & security.** A deployment with no new bindings decides exactly as before;
  the identity axes must bind to the **authenticated principal**, not a caller-declared
  string, wherever possible; the control plane must never gain a second authorization
  engine to keep the attack surface small.

## Decision outcome

**Enrich the existing source binding in place (candidate A1): add subject scope-trees and a
row-level `effect`, giving `sourcescope` a subject-scoped allow/forbid algebra that mirrors
model-access over its own table — while leaving the containment model and the cross-scope Cedar
override exactly as they are.** Do **not** author raw Cedar for the new axes (candidate B),
and do **not** stand up a parallel model_access-twin decision plane (candidate C). One
control plane, one query surface, one place to get authorization right.

### 1. Five new subject scope-trees, uniform with the existing containment trees

`scope_tree` grows from `{workspace, agent_group, folder}` to also include:

| tree | `scope_ref` | matches when… | identity source | forgeable? |
|---|---|---|---|---|
| `session` | session `external_id` | the acting session == ref | the session-aware caller ref, agent-identity-hardened | route-gated (see §4) |
| `agent` | agent `external_id` | the acting agent == ref | `principal.AgentIdentity` ∨ the session's agent ∨ the agent ref | route-gated / authenticated |
| `user` | user id | `principal.UserID` == ref | **authenticated principal** | no |
| `user_group` | `UserGroup.ID` | ref ∈ `principal.GroupsIn(tenant)` | **authenticated principal** (directory-group-gated nested closure) | no |
| `role` | tenant role name | `principal.RoleIn(tenant)` == ref | **authenticated principal** | no |

`user_group` is the **directory group** — matched by **group id** against
`principal.GroupsIn(tenant)`, which already travels on the authenticated principal and
already folds in the whole nested-ancestor closure (`principal.go:67-77,151-164`); no
per-resolve group read is added. `UserGroup` has no slug (`model/auth.go:122`), so id is
the stable identifier. `role` is added for **full model-access parity** (Fran Olivares, 2026-07-07):
governing a source by tenant role is the coarse "group of users" lever model-access also exposes.

The three identity-of-one axes (`session`, `agent`, `user`) are degenerate containments
(equality); `user_group` and `role` are true memberships. All are evaluated as a uniform
**scope predicate** over the actor — no new decision engine.

**Validation follows a containment-vs-subject dichotomy (verified constraint).** The
module's write handlers hold a business-tenant `store.Scope`; the auth subjects
(`model.User`, `model.UserGroup`, roles) live in `store.AuthScope` (the system tenant) and
are **not reachable** from it (`core/store/store.go` vs `auth.go:24-36`). So:

- **Containment trees** `workspace` / `agent_group` / `folder` **and** the store-resident
  subject trees are validated for existence at bind time as today (deny-closed, "no
  dangling scope") — but to keep one uniform rule and to support binding a source ahead of
  an ephemeral session, this decision treats **all five subject trees as shape-only** at authoring:
  a non-empty `scope_ref` of the right kind, no store lookup.
- Correctness does not depend on existence validation: an unknown subject ref simply never
  matches the authenticated actor at resolve ⇒ deny-closed — **exactly the model-access pattern**
  (`modelaccessgate.go` validates subject *shape* only; `validateGrantRefs` checks only the
  store-resident TARGET). Typo-prevention is the console's job (author from a directory/agent
  picker), not the binding layer's. The containment trees keep their existing existence
  validation unchanged.

### 2. A row-level `effect` (allow | forbid), with **absolute** forbid-overrides-allow

Each binding carries `effect ∈ {allow (default; empty stored value = allow), forbid}`
(the same convention as model-access's `normalizeEffect`). The resolver algebra becomes, for one
`(actor, source)`:

```
1. If ANY enabled binding matching the actor has effect=forbid  → DENY   (absolute)
   — OR the cross-scope Cedar engine returns EffectForbid for a resource-anchored (workspace/folder) binding.
2. Else, if the source is UNCONFINED (no enabled ALLOW binding at all) → ALLOW   (global / back-compat),
   subject to the per-workspace connector-assignment gate for unbound connectors.
3. Else (confined), ALLOW iff the actor matches an ALLOW binding (its tree's containment),
   OR a cross-scope Cedar EffectGrant, OR tenant RBAC soft-isolation;
   the credential is taken from the MOST-SPECIFIC matching ALLOW (§3). Otherwise DENY-CLOSED.
```

**Behaviour change, documented (as ADR-0019 documented its own).** Today the source binding's forbid is
*per-binding*: a cross-scope `EffectForbid` on one binding is `continue`d and a *different*
binding may still allow (`resolver.go:243-248`). This decision makes **all** forbids **absolute**
(row-level `effect=forbid` and cross-scope `EffectForbid` alike): any matching forbid denies the
source, overriding containment, cross-scope grant **and** tenant RBAC — the exact algebra
of model-access (`modelaccessgate.go:204`) and of the Cedar core (`EffectForbid` "OVERRIDES
everything", `authorizer.go:101`). The direction is strictly safer (a forbid can only ever
deny), and no existing single-binding forbid test regresses; the change is only observable
in the previously-underspecified multi-binding case.

**Confinement trigger.** A source is *confined* iff it has ≥1 enabled **allow** binding.
Pre-existing bindings are all allows, so this is identical to today's "bound ⇔ has bindings".
A source that carries **only forbids** stays global except for the subjects those forbids
name — the model-access "restrict certain subjects" posture, now available for sources too. The
connector-assignment gate keys on "no allow binding" (was "no binding") so a forbid-only source
still honours connector assignments.

### 3. Precedence: forbid absolute; credential by most-specific allow

Forbid is absolute (§2), so precedence never decides allow-vs-deny — it decides **which
credential** a permitted actor receives when several allow bindings match. The order,
most-specific → least, is:

```
session > agent > user > user_group > role > agent_group > folder > workspace
```

Identity-of-one first, then directory group, then RBAC role, then the acting agent's
group, then resource containment. This total order makes credential selection
deterministic (replacing `loadEnabledBindings`' lexical sort) and is the documented
`session > agent > group > workspace` precedence, refined for the five axes.

### 4. Axis availability is per enforcement point — and honest about it

The resolver has two entrypoints and they carry different actor context:

| axis | `ResolveForSession` (models `ScopeGate`, runtime) | `ResolveForAgent` (knowledge `RetrievalScopeGate`) |
|---|---|---|
| session | ✅ the acting session ref | ❌ no session in context → never matches |
| agent | ✅ session's agent (agent-identity override) | ✅ the agent ref (agent-identity override) |
| user / user_group / role | ✅ authenticated principal | ✅ authenticated principal |
| workspace / agent_group / folder | ✅ (existing) | ✅ (existing) |

A `session` binding on a knowledge base is **not** enforced on the agent-only retrieval
path because no session exists there — it is not silently "allowed", it simply is not that
actor's scope; other bindings/axes on the same source still apply. This asymmetry is
documented in the contract, not hidden. The `session`/`agent` axes remain route-gated,
caller-influenced references hardened by the agent-identity check (`principal.AgentIdentity` overrides a
caller-declared ref); `user`/`user_group`/`role` bind to the **unforgeable authenticated
principal** and are therefore the stronger axes.

### 5. Enforcement posture is mutable, versioned, audited — and relaxing it is dual-controlled

The *posture* of a source is the set of its enabled bindings and their effects. Per Fran Olivares
(2026-07-07, "robust without duplication"): **governance's `revision.go` and
`approvals.go` are module-internal and NOT reusable from `sourcescope`** (verified: unexported
helpers, their own entities, a REST approval flow). Forking them into `sourcescope` would be
duplicated tech debt, so the posture controls are **self-contained** and reuse the one shared
immutable primitive that already exists — the audit ledger:

- **Audited & versioned via the audit chain.** Every posture mutation records the posture
  **delta** in the append-only, hash-chained audit ledger (ADR-0009) — `sourcescope.binding.*`
  for create/update/delete (extending `auditBinding` with the `effect`) and
  `sourcescope.posture.{propose,approve,reject}` for the dual-control lifecycle. The ledger IS
  the immutable, sequenced version history; a dedicated numbered-revision *table with rollback*
  is deliberately NOT added (it would duplicate `governance/revision.go`). The pending/decided
  **posture-request** rows are the first-class, queryable record of every *relaxation* (who
  proposed, who approved).
- **Dual-controlled in the relaxing direction only, self-contained.** A mutation that
  could **widen** who may reach a source is a *relaxation*: it is NOT applied by the actor —
  it is recorded as a pending `sourcescope_posture_request` and applied only when a **SECOND,
  DISTINCT** principal approves it (the `proposer != approver` check is the two-person
  integrity), the approver holding the admin-tier `sourcescope:posture:admin` permission
  (separation of duty from the editor-tier proposer).

  > **Status amendment, 2026-08-07.** The enumeration below is CORRECTED. As
  > originally written it listed *broadening an allow* and *moving an allow*, named no scope
  > operation on a `forbid` at all, and placed "narrowing to a more-specific tree" among the
  > ordinary single-actor writes **without qualifying it by effect**. The code implemented
  > that faithfully, so a `forbid` that stayed an enabled `forbid` and only changed which
  > population it covered applied in the act, by one actor — while DELETING that same forbid
  > needed two people. The two-person gate was bypassable by editing instead of deleting.
  > Inverting the classifiers to whitelists surfaced three further leaks of the identical
  > class: an `allow` moved to a "more-specific" tree; the LAST enabled `allow` turned into a
  > `forbid`; and creating an `allow` on a source that was already confined (creation was
  > classified by nothing at all). The general rule at the top of this bullet never changed
  > and is what authorises the correction — the enumeration was always narrower than the rule
  > it claimed to make precise.

  **The classifiers are WHITELISTS.** They enumerate the writes that provably cannot widen
  access and treat **everything else — including any shape they do not recognise — as a
  relaxation**. A blacklist of relaxing shapes leaks by construction, and this one leaked in
  four places. Three were edits to an existing binding — a `forbid` shrinking its scope, an
  `allow` moved to a "more-specific" tree, and the LAST enabled `allow` turned into a
  `forbid`; the fourth was creation, classified by nothing at all. The first two came from
  reading a scope operation with the polarity of an `allow`. The third came from reading a
  row's EFFECT while forgetting the CONFINEMENT that same row carried: a source is confined
  only while it has an enabled `allow`, so the write that reads as "this row can only deny
  from here" is also the write that makes the source global.

  **A `forbid` INVERTS THE POLARITY of every scope operation, and that is the trap.** For an
  `allow`, a smaller scope reaches fewer actors — a tightening. For a `forbid`, a smaller
  scope PROTECTS fewer actors: everyone it stops covering is un-denied by that single write.

  **Two scopes are comparable only when they are the SAME scope.** `specificityRank`
  (`resolver.go`) **orders trees to choose a CREDENTIAL** among matching allow bindings; **it
  is not a containment relation** and must never be used as one. `role:admin` and
  `user_group:g1`, `workspace:eng` and `agent_group:core`, a folder and its child are
  different POPULATIONS and neither side contains the other — and a folder binding carries no
  containment dimension at all (it rides the cross-scope Cedar grant). Membership is not fixed either: a
  superset proved by reading rows today is not a superset tomorrow. So the certificate for
  "this write cannot widen access" is **identity of the scope and nothing weaker**, and "I
  cannot compare these two scopes" resolves to *relaxation* — a false positive costs one
  extra approval, a false negative is the bypass of a two-person gate.

  **Relaxations**, precisely (`classifyCreate`/`classifyUpdate`/`classifyDelete`): deleting or
  disabling an enabled **forbid**; flipping `forbid→allow`; **any change of scope on an
  enabled forbid** (it un-denies part of its population); **enabling** an allow; disabling or
  deleting the **last** enabled allow (it unconfines the source → global); **any change of
  scope on an enabled allow** — broader, narrower or sideways alike; **creating an allow on a
  source that is ALREADY confined** (a grant for a population that could not reach it); and
  the dedicated one-way **`POST /sources/disable-scoping`** operation (the mirror of AWS
  `qbusiness:DisableAclOnDataSource`).

  **Tightening / neutral** mutations are ordinary single-actor writes — audited, but not
  gated: adding a **forbid**; `allow→forbid`; creating the **FIRST** enabled allow on an
  unconfined source (it brings the source under governance — the largest tightening in the
  module, deliberately un-gated so the safe move is never the expensive one); creating a
  **disabled** row; enabling a parked **forbid**; deleting or disabling a **non-last** allow;
  and a note/credential edit that leaves effect, enabled and scope untouched (the credential
  locator selects WHICH reference an authorized actor receives, never WHETHER it is
  authorized). A row that is disabled before and after enforces nothing, so any write to it
  is neutral.

  This asymmetry matches AWS's (relaxing is the privileged op) and beats Google's immutable
  posture: ours is mutable *and* governed. Endpoints: the relaxing create/update/delete are
  PROPOSED through the existing `POST /bindings` and `PUT`/`DELETE /bindings/{id}` (they
  return `202` with the pending request); `POST /posture-requests/{id}/{approve,reject}`
  decide; `GET /posture-requests` is the reviewer queue.

### 6. Access-map projects the new origins (ADR-0003)

`publishBindingEdges` projects the permitted side of the RRW map. `EdgeObservation`
already supports `OriginKind ∈ {agent, session, identity}` (`sdk/model/observation.go:55`),
so the three identity-of-one axes each project ONE edge: a `session` binding → a
`session`-origin edge (a per-session binding appears as an edge of **that** session);
`agent` → an `agent`-origin edge; `user` → an `identity`-origin edge. The GROUP subject
axes (`user_group`, `role`) would need to enumerate their MEMBERS to project edges — but the
members are auth-scope entities (directory groups, users) not reachable from the module's
tenant `store.Scope`, so, exactly like a folder binding's reverse-grant projection (the
reverse-query defer), they **DEFER**: log and project nothing. Forbid bindings project nothing (a forbid is not a
permitted edge). Enforcement is always the resolver's live decision against the live
principal; the map is best-effort drift observability, and a deferred/absent edge never
weakens it.

## Consequences

- **Good:** the four vision axes (five with `role`) are expressible, enforced deny-closed
  on both real PEPs, and visible in scope resolution and the access-map; one auditable/
  listable binding shape for the console; identity axes bind to the authenticated principal
  (unforgeable); no second authorization engine (small attack surface); hot path pays one
  cheap membership check and **zero** new policy round-trips for the identity axes; a
  mutable-yet-governed posture that is a verified differentiator vs AWS (one-way) and
  Google (immutable).
- **Bad / trade-offs:** `scope_tree` now carries both "containment scope" and "subject
  identity" semantics (mitigated: the contract frames both as a uniform *scope predicate*);
  the posture/dual-control machinery adds real surface that a
  minimal deployment does not exercise until it authors a relaxation; making forbid
  absolute is a documented behaviour change (safe direction).
- **Neutral:** `role` overlaps conceptually with the existing tenant-RBAC soft-isolation
  bypass (`rbacAllows`) — they compose (a `role` binding is a positive scope; the RBAC
  bypass is the tenant-operator visibility rule), and a forbid overrides **both**.

## Why the alternatives were rejected

- **(B) A high-level API that generates Cedar policies for the new axes.** Rejected:
  (1) it would be the *only* plane that authors raw Cedar, whereas model-access — the consistency
  target — does **not** generate Cedar; it decides over its own rows
  (`modelaccessgate.go:11-14`). (2) It pays a Cedar round-trip per resolve on the hot path.
  (3) The reverse question the console needs ("which sources can subject S reach?") is the
  unsolved Cedar reverse-query, so the UI and the access-map would be blocked or
  approximate. (4) Auditing "who scoped what" means reading policy text, not rows.
- **(C) A separate model_access-twin table for source-subject grants, composed with the
  existing containment binding.** Rejected as over-engineering that *reduces* robustness:
  two decision planes must be composed at every PEP and kept consistent — a classic source
  of security drift (one updated, the other not; ambiguous cross-plane precedence).
  "Most complete/enterprise" is achieved by **depth on one plane** (all axes + effect +
  versioned dual-controlled posture + full test matrix), not by duplicating the plumbing.
  A single control plane with a uniform algebra is easier to audit ("everything governing
  source X" = one query) and to prove correct.
- **Extending the custom-role scopeSpec vocabulary instead of a local enum.** Rejected:
  `sourcescope`'s `scope_tree` is a module-local constant that only *mirrors* the custom-role
  catalog (`schema.go:49`); widening a shared catalog would leak the source axes into what custom
  roles may target. The new trees stay local to `sourcescope`.
