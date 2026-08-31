---
title: "Recipe: deny-closed policies (Cedar / OPA)"
description: "Wire the restrict-only policy decision point: a Cedar forbid
  overlay or a permit-by-default OPA policy, validated and dry-run before
  publishing — policies that can only take access away, never widen it."
sidebar:
  order: 1
slug: 2026-06/how-to/cookbook/deny-closed-policies
---

**Goal:** add attribute-based restrictions on top of deny-by-default RBAC —
for example, "no one touches resources tagged `secret`, whatever their role
says."

The one invariant to keep in your head: the PDP **only restricts**. The
decision composes as RBAC ∩ native ABAC ∩ external PDP — a policy can never
grant what the role model denies
([the model](/2026-06/how-to/govern-and-approve/#the-policy-seam-abacpdp-only-restricts)).

## Cedar (embedded, primary)

Select the engine and point it at your policy file, then restart:

```bash
OLIVARES_PDP_ENGINE=cedar
OLIVARES_PDP_CEDAR_FILE=/etc/olivares/policy.cedar
```

A Cedar policy is a **forbid overlay** — the base permit stands in for "RBAC
already decided", and your `forbid` rules subtract:

```cedar
permit(principal, action, resource);

forbid(principal, action, resource)
  when { resource.kind == "credential" && resource.sensitivity == "secret" };
```

Two authoring facts, verified against the adapter: `resource.kind` and
`resource.sensitivity` are always present on the decision input
(unconditional to reference); any other attribute must be guarded with
`has()` or the rule cannot match. A `permit` you write can never widen the
decision.

## OPA (over HTTP)

```bash
OLIVARES_PDP_ENGINE=opa
OLIVARES_PDP_OPA_URL=http://opa.internal:8181
OLIVARES_PDP_OPA_PATH=/v1/data/olivares/decision
OLIVARES_PDP_OPA_TOKEN=<bearer-reference>     # optional
```

Author the Rego **permit-by-default**:

```rego
package olivares

default allow := true

allow := false if {
  input.resource.sensitivity == "secret"
  input.action == "read"
}
```

`true` = no restriction. `false`, a missing result, or **any transport or
non-2xx error fails closed** — the request is denied, never silently
ungoverned.

## Validate, dry-run, publish

The governance module exposes a policy lifecycle so a bad policy never lands
blind:

```bash
# Compile-check the source:
curl -ks -X POST "$BASE/v1/m/governance/pdp/validate" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" \
  -d @policy.json

# Pre-flight a decision WITHOUT audit side effects:
curl -ks -X POST "$BASE/v1/m/governance/pdp/dry-run" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" \
  -d '{"principal":"…","action":"…","resource":{"kind":"credential","sensitivity":"secret"}}'

# Then publish (policy-admin permission):
curl -ks -X POST "$BASE/v1/m/governance/pdp/publish" …
```

`GET /v1/m/governance/pdp/versions` lists what is deployed;
`POST /v1/m/governance/pdp/explain` explains a decision.

## Verify the safety properties

* Restart with an **invalid** policy file: the engine disables only the
  external PDP and logs it — RBAC and native ABAC keep governing; the
  control plane does not go down.
* Every restriction the PDP applies is **audited** — check the ledger after a
  denied request.

## Notes

* Policies are versioned and published, not hot-edited files in production —
  treat the publish as a reviewed change.
* For approval-gated (rather than denied) actions, see
  [HITL approvals](/2026-06/how-to/cookbook/hitl-approvals/).
