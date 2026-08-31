---
title: "Source & credential scoping"
description: >-
  Binds a connected source — an MCP server, model, provider, knowledge base or
  data source — to a workspace or agent-group, and resolves, at the point an
  agent or session reaches for it, whether the actor is in scope and which
  credential reference applies. Deny-closed by construction.
---

Source & credential scoping (`modules/sourcescope`) answers a single
question at runtime: when an agent or session reaches for a connected source —
an MCP server, a model, a provider, a knowledge base or a data source — **is
this actor in scope, and which credential reference applies?** It is **LIVE**:
the binding table, its write API and the resolver the runtime PEPs call all
ship in the binary.

It is a module rather than a column because the scope it enforces is not a
property of any one source entity — MCP config, models, providers and knowledge
bases live in different modules, and only the agent/session/resource axis
carries a workspace at all. The scope is a **binding**: `(source) → (workspace
or agent-group)`, with an optional scoped credential reference. This module owns
that binding table and the resolver.

## The binding and its API

`/v1/m/sourcescope/bindings` is a standard CRUD surface, gated by
`sourcescope:binding:read` and `:binding:write`. A binding targets one source
type (`mcp`, `model`, `provider`, `knowledge`, `data`) and one scope tree
(`workspace`, `agent_group`), and carries a **value-free `CredRef`** — a logical
name, a `ref_kind` locator (`env`, `vault`, `secret_manager`, `file`, `other`)
and an optional masked hint. No field can hold a usable secret; the handler
rejects an inline credential, the same minimal-data invariant as
`capabilities.mcp_config.secret_refs`.

## How the Resolver decides

The decision is deny-closed and composed, not a second authorization engine:

- **Containment** — a source bound to workspace W is resolvable by an agent or
  session in W with no further configuration.
- **Grant** — an [`x-models`](/reference/modules/x-models/)-spanning, scoped
  Cedar grant from [`vi-governance`](/reference/modules/vi-governance/) opens a
  foreign workspace.
- **RBAC** — tenant-wide authority still sees everything; workspace is
  soft-isolation, the tenant is the hard boundary.
- **Forbid** — a scoped Cedar forbid overrides all of the above.

The gate is **additive**: an unbound source stays global for back-compat; a
bound source with no containing scope, no grant and no RBAC is **denied**. The
resolver is wired as the `ScopeGate` on the model execute-chain and on
[`viii-knowledge`](/reference/modules/viii-knowledge/) retrieval.

## Bounded context, stated plainly

- This is **reference-binding only**. Scoped-credential **consumption** in an
  actual provider call, and a runtime **MCP broker** that dials a server on an
  agent's behalf, **do not exist in-tree yet** — the resolver returns the
  in-scope reference, but nothing here uses it to authenticate an outbound call.
- The actor's scope comes from the agent/session **named by the caller's actor
  reference**. The scope values are read from the stored row (a caller cannot
  inject a workspace), but the choice of agent is the caller's; binding that
  reference to the principal is a hardening follow-up. See
  [honesty and limits](/start/honesty-and-limits/).

## Related

- [Governance (vi)](/reference/modules/vi-governance/) — the Cedar
  grant/forbid algebra and RBAC the resolver composes.
- [Models (x)](/reference/modules/x-models/) — the execute-chain where the
  `ScopeGate` runs.
- [Knowledge (viii)](/reference/modules/viii-knowledge/) — governed retrieval,
  the second place the resolver gates.
