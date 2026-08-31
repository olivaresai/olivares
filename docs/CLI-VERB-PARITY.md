<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->
# CLI verb parity — which missing verbs are DEFECTS and which are DECISIONS

**Date:** 2026-08-10 · **Measured** by running the binary and reading each group in
execution order.

> **Read this before proposing a missing verb.** A CRUD census over the groups that look
> like resources reports **eight** with an incomplete lifecycle, and **16** missing verbs
> between them. **13 of the 16 are not holes**: the verb exists under the name its domain
> uses, or its absence is a deliberate, load-bearing decision. Three are real, and one of
> those three belongs to another track. A deliberate gap that is not written down is
> indistinguishable from an oversight — which is why this file exists, and why
> `cmd/olivares/cmd_verb_parity_test.go` **pins** the decided absences. If you add one of
> the verbs listed as DELIBERATE, that test fails and points you here: the way to add it
> is to change the decision on purpose, not to discover the gap again.

## How the census over-reports, in three distinct ways

1. **The domain verb is not the CRUD verb.** `secrets put`, `sources set`,
   `holds place`, `erasure request`, `mcp pins approve`, `dr push`, `workspace add` are
   all creates. A census keyed on the word `create` sees none of them.
2. **The group is not the resource.** `compliance` is a department with several
   resources (holds, erasure, OSCAL, subject), each with its own complete lifecycle.
   Aggregating them produces a row that describes nothing.
3. **The nouns are mixed.** In `agent workspace`, `get` and `rm` operate on **files
   inside** a workspace; the workspace registration's own verbs are `add`, `ls` and
   `rm-workspace` — stated in the group's help (`cmd/olivares/cmd_agent_workspace.go:48-49`).

## The verdicts

| group | reported missing | verdict | why |
|---|---|---|---|
| `agent workspace` | create | **NOT MISSING** | `add` registers the workspace (`cmd_agent_workspace.go:69-115`, `POST …/workspaces` → 201). |
| `compliance` | create, rm | **NOT MISSING · rm DELIBERATE** | holds: `place` / `release` (`cmd_compliance.go:616`, `:694`). erasure: `request` / `execute` (`:958`, `:1025`). |
| `dr` (offsite) | get, create, rm | **get/create NOT MISSING · rm DELIBERATE** | `pull` downloads (`cmd_dr_offsite.go:244`), `push` uploads (`:210`). |
| `eventing subscriptions` | get, update, rotate-secret | **ALL THREE FILLED** | `get` shows the six write-only fields; `update` edits in place WITHOUT reissuing the secret; `rotate-secret` reissues it ON PURPOSE, alone. |
| `mcp pins` | get, create | **NOT MISSING · get NOT A DEFECT** | `approve` creates the pin (`cmd_mcp.go:124`); `ls` already carries every field the API returns. |
| `secrets` | get, create | **create NOT MISSING · get DELIBERATE** | `put` creates or updates (`cmd_secrets.go:94`); a `get` would print the secret. |
| `sources` | get, create | **create NOT MISSING · get FILLED 2026-08-20** | `set` is «create or update» (`cmd_sources.go:117`). `get` now reads the config back, masked through `planValue`. |
| `work` | ls, create, rm | **ls REAL (kernel) · create NOT MISSING · rm DELIBERATE** | `list` exists (`cmd_work.go:865`) — the tree's only listing not named `ls`, and it carries no alias. |

## The decisions, with the reason each one is load-bearing

### `secrets` has no `get` — SECURITY

Its `ls` says what it is for: *«names and non-secret hints; **never the value**»*
(`cmd/olivares/cmd_secrets.go:45`). The absence is not a missing capability: the store
**does** expose `Get`, and `secrets rotate` calls it internally to preserve a
description (`cmd_secrets.go:179`). The CLI declines to surface it. A `secrets get` would
print a sealed credential to a terminal, a shell history, a CI log or a ticket — it would
be a defect, not a feature. Read-back of the *reference* is what operators need, and
`ls` gives it.

### `dr` has no offsite `rm` — LAST-COPY SAFETY

`OffsiteClient.Delete` exists (`core/dr/offsite.go:185`) and has exactly **one** caller:
`applyGFSOffsite`, the Grandfather-Father-Son retention pass
(`cmd/olivares/cmd_dr_offsite.go:172-193`). Deletion of the offsite mirror is therefore
**policy-driven only**. That is the whole point of an offsite copy in a 3-2-1 posture:
the bundle is the last thing standing when the host it protects is gone, and an
interactive delete verb is the one keystroke that turns an operator's bad afternoon into
an unrecoverable one. Retention changes go through the GFS flags, where they are declared
and repeatable.

### `compliance` has no `rm` — EVIDENCE

A legal hold is not deleted, it is **released**: `POST /holds/{id}/release` leaves the
record with `status = released` and an append-only chain of custody
(`cmd/olivares/cmd_compliance.go:690-757`, `holds custody` at `:759`). An erasure request
is registered and executed, never removed (`:958`, `:1025`), and its receipt is
ledger-anchored (`:1187`). A `compliance rm` would destroy the proof that the
preservation duty was honoured — which is the artefact the whole subsystem exists to
produce. `oscal` is `ls`-only for the same class of reason: those artefacts are
**ingested**, not authored here.

### `work` has no `rm` — DURABLE AND APPEND-ONLY

Work items terminate through transitions — `item.cancel`, `item.archive` — and decisions
through `decision.revoke` (`cmd/olivares/cmd_work.go:43-83`). Nothing is deleted, because
the point of the durable work plane is that a later session can read what an earlier one
decided. `create` is likewise not missing: it is `work apply item.create`
(`cmd_work.go:44`), because every mutation crosses the API through `validate` / `plan` /
`apply` (`cmd_work.go:154-156`).

### `mcp pins` has no `get` — IT WOULD ADD NOTHING

The API exposes one route for tool pins, `GET /v1/m/capabilities/toolpins`, returning the
whole list (`cmd/olivares/cmd_mcp.go:95`), and `pins ls` renders every field it carries —
tool, fingerprint, pinned-at, count and observed drift (`:108-117`). A `get` would be a
client-side filter over data `ls` already shows; `pins ls -o json` preserves the raw
response for exactly that. This is the one verdict on this page that a future measurement
could reasonably overturn: **if the API ever grows a per-tool route with fields the list
omits, add the verb.**

## The defects

### `sources` has no `get` — a source's configuration cannot be read back — ✅ FILLED 2026-08-20

> **Closed by `sourcesGetCmd` (`cmd/olivares/cmd_sources.go`).** The reasoning below is kept because
> it is not a description of the gap — it is the SPEC the implementation had to meet, and the
> masking constraint in its last paragraph is the reason `get` reuses `planValue` instead of printing
> config. Witnesses in `cmd_sources_get_test.go`: the literal is masked in the table **and** in the
> JSON pane, a `store:` reference stays readable (or the verb cannot audit what it exists to audit),
> and an unknown name exits **4**, not the generic 1.
>
> ⚠ Two traps met while writing those witnesses, recorded so the next verb does not pay them again:
> `-o/--output` is a ROOT persistent flag, so a test that executes the group directly cannot reach
> the JSON pane at all; and `json.Marshal` HTML-escapes, so the mask lands as `\u003credacted\u003e`
> and a substring check for `<redacted>` FAILS on a correctly masked document.

`ls` renders six columns — name, kind, tenant, mode, poll, enabled
(`cmd/olivares/cmd_sources.go:49-56`) — and **not** `--config`. So the connector settings,
including which `store:<name>` secret reference a source resolves at Open, are writable
and not readable. `plan` does not fill the hole: on an unchanged row it prints
`NO-OP` with an empty change set (`cmd_sources_plan.go:578-585`), because it reports a
*diff*, not a state.

This matters more than an ergonomic gap because the roster's contract is that config
carries **references, never values** (`cmd_sources.go:18-26`); an operator who cannot see
which reference a source uses cannot audit that contract. Any `sources get` must reuse
the plan's masking rule (`planValue`, `cmd_sources_plan.go:253-274`) rather than print
config verbatim — a row written before the inline-secret guard existed can still hold a
literal.

### `eventing subscriptions` — `get` and `update`: BOTH FILLED

`create` accepts `--description`, `--auth-header-name`, `--max-attempts`,
`--initial-interval` and writes `sources` and a secret hint
(`cmd/olivares/cmd_eventing.go:294-310`); `ls` renders eight columns and **none of those
five** (`:171-176`). The retry policy an operator configured is therefore unreadable
after the fact. The group's deeper asymmetry is larger than the missing `get` and is
recorded here rather than half-fixed: there is **no update verb at all**, so changing an
endpoint means `rm` + `create`, which mints a new signing secret and silently breaks the
consumer's verification.

✅ **FILLED, both halves.** `get` prints every field including the retry policy and the auth
shape, and never the secret — the store keeps a hint and a read verb that reprinted a delivery
credential would be a worse tool than no verb at all.

✅ **And the deeper asymmetry this section named is closed too: `update` exists.** The paragraph
above turned out to be the whole specification — the verb was built to the sentence *«changing an
endpoint means rm + create, which mints a new signing secret»*, and that is exactly the failure its
witness pins. Worth recording: **this gap was described correctly here before anyone filled it**,
so the document paid for itself.

**What `update` does differently from the API, deliberately:** the HTTP handler
(`modules/eventing/subscription.go:436`) is a PUT and clears any field the caller omits. The CLI
applies **only the flags actually typed** (`Flags().Changed`), because a human fixing one URL must
not have to re-supply the retry policy, the type list and the auth header to avoid losing them.

**The secret columns are never addressed** — not blanked, not re-sealed — and there is no
`--secret` flag: rotating is a different operation with different consequences, and giving it a
home inside `update` is how it ends up happening by accident again. The witness asserts the
**stored column**, not `get`'s output, because that verb prints a hint and would look identical
against a version that rotated on every edit.

✅ **And `rotate-secret`, which the pair needed to be defensible.** `update` never touching the
secret is only a good decision if rotating ON PURPOSE has a door of its own — otherwise the only way
to reissue was `rm` + `create`, which rotates AND mints a new subscription id, so every dashboard and
runbook naming the old one breaks with it. The API endpoint had been there all along
(`modules/eventing/eventing.go:470`).

It is the ONLY verb that puts a NEW value in the sealed secret and its hint, and the hint travels WITH the
secret: `get` prints the hint and never the value, so a hint left behind would keep telling the
operator the old credential is current — the one reading they can actually see. It is destructive and
says so: between the command returning and the receiver being reconfigured every delivery fails its
signature check, so it sits behind `confirmDestructive` like `rm`, and its witness proves the refused
rotation wrote nothing.

> ⛔ **Aqui decia «escribe EXACTAMENTE dos columnas» y es FALSO.** Lo probo el contraste Codex
> `sol max` del 2026-08-20 (hallazgo C08-04-2, VERIFICADO): el repositorio generico pone **todas
> las columnas del descriptor** en el `SET`, asi que ningun verbo de esta familia escribe dos y
> solo dos. Lo que si es cierto y es lo que importa —el invariante que el verbo defiende— es que
> **`rotate-secret` es el unico que pone un valor NUEVO** en esas dos, y que `update` nunca genera
> uno: reescribe el mismo. La frase vieja describia el SQL, que no controlamos; la nueva describe
> la intencion, que si.

⚠ **It deliberately does NOT re-check the endpoint policy**: the destination does not change, and
re-asking would make a subscription whose host predates today's policy impossible to re-key —
locking an operator out of the one action that recovers from a leaked secret.

⚠ **Two harness traps recorded so the next verb does not pay them again:** with no operator policy
authored the checker **refuses** any destination change, so the fixture authorizes explicitly rather
than being written around the refusal; and the checker **resolves** the host before comparing it to
the policy, so a `.invalid` name fails with *«did not resolve»* and the test silently measures DNS
instead of authorization. Loopback hosts (`127.0.0.1` authorized, `localhost` not) give two
resolvable names on opposite sides of one policy.

### `work list` has no `ls` alias — KERNEL, declared and NOT filled

Measured against the binary's own command dump (`olivares commands`): the tree has
**13** listings named `ls` and **exactly one** named `list` — `olivares work list`. It
carries **no aliases at all**, so `olivares work ls` answers `unknown command "ls" for
"olivares work"`. It is the only listing in the CLI that cannot be reached at the
canonical spelling this CLI settled on (`cmd_dr_offsite.go:298-301` states the rule while
applying it: *«Canonical short verb first … the old name stays as an alias so nothing
breaks»*).

⚠ **The stronger version of this claim is false, and it is worth writing down so nobody
re-derives it:** it is NOT true that every other `ls` carries a `list` alias. Six of the
twelve `Use: "ls"` blocks in the package declare no alias at all — the four in
`cmd_eventing.go` (`:120`, `:566`, `:650`, `:793`), plus `cmd_secrets.go:44` and
`cmd_sources.go:61`. The alias is a courtesy some groups extend; the **spelling** is the
convention, and `work` is the only group that breaks it.

This is a one-line fix and it is **deliberately not made here**: the work kernel is owned
by a separate track, and the change belongs with it. Recorded rather than applied, so the
owner decides.
