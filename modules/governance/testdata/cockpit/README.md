<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->

# Session cockpit Cedar fixtures

These sample policies from the cockpit architecture (§7.3) use this tree's evaluator
(cedar-go v1.8.0, `modules/governance/grants.go`). They adapt the entity model assumed
by the design document. `modules/governance/cockpit_policy_test.go` executes them.

## Adapting the entity model

The v3 examples use `resource is Session`, `resource is ShellTarget`, and
`resource is InputTarget`. The engine does not materialize those entity types:

- The resource is always `Resource::"<id>"` (`modules/governance/grants.go`,
  `resourceUID`). Its kind is an attribute, so a dotted kind such as `core.agent`,
  which is not a valid Cedar type name, does not break compilation
  (`modules/governance/cedar.go:32-34`).
- The action is `Action::"<route permission>"` (`modules/governance/grants.go`,
  `actionUID`). The route-action boundary that would translate a permission into a
  separate Action ID does not exist yet, so these fixtures use the route permission.

A condition referencing an attribute Cedar cannot resolve makes the rule fail, and
Cedar skips it. A `forbid` using `resource is Session` would silently leave users
unconfined. This is why `baseResourceAttrs` always supplies `kind` and `sensitivity`
(`modules/governance/grants.go`, the `baseResourceAttrs` comment).

The fixtures show how to express the design's policies in this engine. When the
`CedarAction` boundary is implemented, change only the left side of `action ==`.

## Files

| File | What it demonstrates |
|---|---|
| `department-forbid.cedar` | Department confinement with **forbid-unless**, the only form that confines under `(RBAC ∨ Grant)` |
| `delegated-transcript.cedar` | A positive permit for a delegated viewer on an AgentGroup |
| `terminal-operators.cedar` | Exact permits for `shell:open` and `input:write`, with host, UID, classification, and AAL3 |

The test suite compiles and evaluates all three policies against the real engine.
