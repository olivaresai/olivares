# Login enforcement — operator guide

<!-- SPDX-FileCopyrightText: 2026 Olivares.AI -->
<!-- SPDX-License-Identifier: AGPL-3.0-only -->

How a deployment behaves when login enforcement is configured, what the engine does at
startup and at promotion when the artifact running cannot enforce it, and how to recover.

Related: [`08-SECURITY-AND-COMPLIANCE.md`](08-SECURITY-AND-COMPLIANCE.md) §4 (AuthN/AuthZ),
[`07-LICENSE-AND-OPEN-CORE.md`](07-LICENSE-AND-OPEN-CORE.md) §9.1 (the SSO open/enterprise
line), [`UPGRADE-AND-ROLLBACK.md`](UPGRADE-AND-ROLLBACK.md) §5 (rollback) and §7 (edition
changes).

## 1. What is open and what is additive

**Single-IdP SSO is open-core.** The default build does real OIDC and SAML 2.0 login for one
IdP, configured from the console or from the environment, with sealed secrets. It also
**stores** the login posture — require-SSO and the login-surface IP allow-list — and reports
it with `enforced_by = unavailable`. It never fakes the control.

**Enforcing that posture is additive.** The enforcement engine ships in the enterprise line;
an artifact that does not link it stores the posture and does not act on it. Nothing in the
open build is disabled to create the difference, and no part of this page depends on a
license claim being present: the enforcement component reads no license claim, so a lapsed
license leaves a linked component still enforcing.

## 2. The three artifact states

At startup, before any leader election, the engine derives one state from exactly two facts:
whether **this artifact links** the enforcement component, and whether the host operator set
an **explicit falsey** `OLIVARES_LOGIN_ENFORCEMENT` value.

| host value | artifact links the component | state |
|---|---|---|
| explicit falsey | yes **or** no | `disabled_by_operator` |
| unset or any other value | yes | `wired` |
| unset or any other value | no | `absent` |

The host selection has precedence over the artifact. That is deliberate: it is what keeps the
documented break-glass available on a build that *does* carry the component.

**The explicit falsey values are `off`, `0`, `false`, `no` and `disabled`.** Surrounding
whitespace is trimmed and letter case is ignored, so ` Off ` is falsey. **Every other value, including `on`, `1`, `true` and an empty or unset variable, does not
select the host override.** Enforcement still requires a linked component. This is not a
general boolean parser; a typo does not silently select the override.

The variable is read once, when the engine starts. A change takes effect on restart.

## 3. What counts as configured demand

Demand is read from the **global/default** federation configuration: the row scoped to the
system tenant under the reserved alias `default`. A deployment has configured demand when
**either** require-SSO is on **or** the login-surface IP allow-list is non-empty. An empty
allow-list is no restriction.

Per-tenant scopes do not create demand for this decision; the global/default row is the one
the startup and promotion checks read.

## 4. Startup, promotion and the refusal

An artifact in the `absent` state takes one read-only snapshot before the election. In a
single transaction it reads two things: whether this deployment has ever **recorded** the
enforcement component, and whether demand is configured now.

- **No recorded history.** The deployment starts, whatever the posture says. This is the
  ordinary first-run path: a Community installation may configure require-SSO before it has
  ever run an enforcing artifact, and the posture was never enforced there, so nothing is
  lost by starting. Refusing it would break a supported path.
- **Recorded history and configured demand.** The engine refuses to start. The refusal
  happens **before any listener is acquired** and before the console announcement, so a
  refused node never serves and never mints a setup token. The store is closed on the normal
  startup-failure path.
- **A failed read is not an answer.** If the snapshot cannot be taken, the error propagates
  and startup fails; the engine never treats "I could not look" as "there is nothing there".

**On a standby that is later promoted**, the same check runs inside the promotion transaction.
A refusal there rejects **that acquisition only**: the process stays alive as a follower and
the elector will try again. It does not close the store and it does not take the node down.
This matters during a rolling upgrade — a standby running a non-enforcing artifact keeps
following rather than crash-looping.

**An artifact in the `wired` state records one observation per promotion**, carrying the
artifact version. That observation is the recorded history the paragraphs above refer to. It
is how a later downgrade becomes detectable at all.

## 5. The break-glass setting and its durable record

Setting `OLIVARES_LOGIN_ENFORCEMENT` to an explicit falsey value is a **deliberate operator
action that removes enforcement until it is restored**. It is not a repair and not a
maintenance default. Use it when enforcement is locking out the people who must fix the
configuration, and restore it as soon as the posture or the artifact is corrected.

When a node in the `disabled_by_operator` state is promoted, the engine appends **one durable
recovery record** to the tamper-evident ledger, in the same transaction, under the capability
lock:

| field | value |
|---|---|
| action | `auth.login_enforcement.operator_recovery` |
| actor | `host_operator` |
| actor kind | `system` |
| target | **none** — no target entity or ID is recorded |
| metadata `capability` | `global/default/login-enforcement` |
| metadata `component_state` | `disabled_by_operator` |
| metadata `component_linked` | whether this artifact links the component |
| metadata `artifact_version` | the running artifact's version |

The record says that the engine applied a host selection. It attests no authenticated user
identity, which is why it names a capability in metadata rather than a target entity. **No
environment value and no credential is recorded.**

Two consequences worth knowing before you rely on it:

- **If that record cannot be made durable, the promotion fails.** A failed append, or a
  sequence that comes back as zero — the store's explicit answer for "this evidence was
  dropped" — refuses the promotion and rolls the transaction back. A disabled artifact is
  never promoted on a recovery record that is not in the ledger.
- **A disabled artifact records no capability observation.** Running with the break-glass on
  does not add to the deployment's enforcement history, so it neither creates nor deepens a
  future downgrade refusal.

## 6. Rollback, restore and older binaries

The recorded observation is what makes a downgrade visible, and it is **not** removed by
rolling a binary back. Once a deployment has run an enforcing artifact:

- Rolling back to an artifact **without** the component, while demand is still configured,
  produces the §4 refusal at startup. The rollback does not fail quietly; the node refuses and
  says so.
- Rolling back while demand is **not** configured starts normally.
- Do not assume that a binary predating this check can report the same refusal. Its
  schema compatibility check may reject the database earlier. Verify the target binary
  against the schema ceiling rules in [`UPGRADE-AND-ROLLBACK.md`](UPGRADE-AND-ROLLBACK.md) §5;
  do not remove schema versions or capability history to make an older binary start.

Plan a downgrade as a posture decision, not only as an image change.

## 7. Recovery

Pick one of two paths. Both are authorized operations; neither touches the database directly.

**A. Restore an enforcing, compatible artifact.** Deploy an artifact that links the
enforcement component and is compatible with the deployment's current schema. The node starts,
records its observation at promotion, and enforcement resumes with the stored posture
unchanged. This is the right path when the posture is correct and the artifact was the
mistake.

**B. Make an authorized posture change.** Through the console or the identity API, reduce the
configured demand using an account that is authorized to change it. Demand is absent only
when require-SSO is off **and** the login-surface allow-list is empty. If both are configured,
changing only one does not resolve the refusal. The change is audited like any other console action.
This is the right path when the posture itself is what should change.

If neither is possible right now and operators are locked out, use the §5 break-glass
deliberately, record why, and then complete A or B.

Preserve the following recovery requirements:

- Do not set the break-glass as a standing default or automate turning it on. It removes a
  configured security control. Its use requires an explicit operator decision.
- Do not edit the database by hand to change posture or state.
- Do not delete the capability observation history. It is the evidence that makes a downgrade
  detectable, and removing it hides the condition instead of resolving it.
- Do not work around authorization to make the posture change. If the account that should make
  it cannot, fix the authorization.

## 8. What callers see

**Login refused because enforcement is configured and unavailable** returns **HTTP 503** with
the stable code `login_enforcement_unavailable`, and the response carries `Cache-Control:
no-store`. The no-store matters: the response describes a deployment state the operator is
being told to change, so it must never be replayed from a private cache to someone who has
just changed it. The body carries no posture detail. Over gRPC the same condition translates
to `Unavailable` with the same code prefix.

**Invalid credentials keep their existing HTTP 401.** The two are deliberately distinct: 503
says the deployment cannot evaluate an otherwise permitted login right now; 401 says the
credentials were wrong.

The 503 does not promise that a retry alone will help. It resolves when an operator completes
§7.

**Existing sessions are unaffected.** The guard runs where a **new** session is created. A
session issued before the condition appeared keeps working until it expires or is revoked;
the refusal applies to new logins.

**Accepting an invitation also creates a password session.** When login policy is
installed, its network rule applies before the invitation is checked. Once the
invitation proves the account identity, require-SSO applies just as it does after
a correct password. A refusal returns `network_not_allowed` or `sso_required`
(HTTP 403), records `auth.login.blocked`, and leaves the invitation pending, the
password unchanged and no new session. A permitted retry can use the same token
before it expires. SSO login remains a separate flow; it does not redeem an
invitation. An installation without a login policy retains ordinary invitation
acceptance.

## 9. SSO completion, precisely

For a federated login, the engine **issues the session first** and waits for that transaction
to commit. That commit, under the capability lock, is the admission point. A login refused
there — including the enforcement-unavailable case — does not run the later subject-binding
or group-reconciliation steps, or emit their completion audit events. Earlier just-in-time
account creation can already have persisted the account and its initial binding; that earlier
effect is not rolled back by this refusal.

The completion effects of an **admitted** login then run in their own best-effort
transactions. They are **not atomic** with issuance and a failure among them never revokes the
issued session. They finish before the token is returned to the caller, and because a session
carries no grant snapshot — every request loads grants in its own read — the first use of the
token sees membership changes that successfully committed. A failed best-effort reconciliation
does not establish that the asserted groups were applied.

The audit order is `sso.login`, followed by `sso.user.subject_bound` and
`sso.group.reconcile` when the corresponding completion effects occur.

**Residual worth knowing:** the subject binding is stamped after the eligibility guards and
after the session is issued. It is best-effort and never overwrites an existing binding, and
it is a no-op for an account created just-in-time by this login, which is bound at creation.
A federated login is treated as AAL1 regardless of how the IdP authenticated the user: this
engine verified the assertion, not the authenticator, and does not inflate the assurance
claim.

## 10. Checking a deployment

- The running artifact's version and edition: `olivares version`.
- The stored SSO posture and who enforces it: the identity area of the console, which reports
  `enforced_by = unavailable` on an artifact that stores but does not enforce.
- `OLIVARES_LOGIN_ENFORCEMENT` and every other configuration variable: the generated
  configuration reference in the documentation site.
- A refusal at startup is logged with the reason before anything binds; a refused promotion is
  logged where the promotion is attempted.
