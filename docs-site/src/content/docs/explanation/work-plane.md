---
title: The work plane
description: >-
  How agents and sessions coordinate work in Olivares AI — work items, messages,
  acknowledgements and handovers — what is real and durable today, and what is
  deliberately not wired yet. The half of the product that is not the access map.
---

Most of this documentation is about **what an agent can reach**: the access map, the
permissions, the drift between *Permitted* and *Observed*. This page is about the other
half — **how agents and sessions coordinate the work itself**, and it is the part the rest
of the site has, until now, only described as a list of commands and events.

The problem it exists for is not hypothetical, and it is the one this project has spent
its own development suffering: sessions that cannot see each other, state that diverges
between them, work done twice, and decisions that live in one person's terminal and are
lost when it closes. A control plane that governs *access* and says nothing about *work*
leaves that gap exactly where it was.

## What a work item is

A **work item** is a unit of work with an owner, a state and a durable record. It is not
a chat message and not a ticket in someone else's tracker: it lives in the same store as
the audit ledger, so what happened to it is answerable later by the same means as
everything else the control plane records.

Around it sit three primitives:

| Primitive | What it does |
|---|---|
| **Message** | One participant tells another something, durably — not a broadcast into a log nobody reads |
| **Acknowledgement** | The receiver records that it *took* the message. "Read" and "answered" stop being the same word |
| **Handover** | Ownership of a work item moves, with the reason attached to the move |

The acknowledgement is the one worth pausing on. Coordination breaks far more often
because a message was seen and not acted on than because it was never delivered, and a
system that cannot tell those apart cannot tell you which one happened.

## What is real today, and what is not

:::caution[Read this section before you build on it]
The primitives above are **implemented and durable**. Their reach is **deliberately
narrower** than the idea, and the boundary is enforced in code rather than promised in
prose. Three limits, stated plainly:
:::

**1 · Coordination is scoped to a workflow, and the public plane is deliberately not
wired.** Messages, acknowledgements and handovers are real within a workflow's own
execution. The general, cross-everything communication plane is *not* connected — and
that is not an omission waiting to be noticed: a boot test asserts which authority
sources `boot` may wire and **fails if anything else appears**
(`cmd/olivares/communicationauthorityboot_test.go`, `TestBootWiresExactCommunication
RequestAuthoritySourcesOnly`). Wiring it by accident is a red test, not a surprise in
production.

**2 · Agent-to-agent dispatch only mounts with an authorized destination.** The remote
work executor is constructed with an approval gate in front of it (`cmd/olivares/wire.go`);
there is no path that dispatches work to an arbitrary peer because a configuration file
asked nicely.

**3 · Shadow mode and final work authority DO NOT EXIST.** Not "coming soon", not
"partially": absent. A deployment cannot today hand the work plane the last word over a
session, and nothing in the product should be read as offering that. When it exists it
will arrive with the evidence that it works — a comparison window against the existing
sources, not a version bump.

## Why the limits are written here

Because the alternative is worse for you. A page that described the design and let you
discover the boundary at integration time would cost you the afternoon; a page that
called the missing half "roadmap" would be the kind of claim this project refuses to
make. The [honesty and limits](/start/honesty-and-limits/) page states the general rule;
this is that rule applied to the newest surface in the product.

## Where to look next

- [Modules overview](/reference/modules/overview/) — where orchestration sits among the
  other modules.
- [Orchestration reference](/reference/modules/iv-orchestration/) — the module that owns
  workflow execution.
- [Event bus reference](/reference/events/) — the events the work plane emits, as an
  AsyncAPI contract.
- [Build a governed workflow](/how-to/build-a-workflow/) — the practical path, once you
  know what the plane does and does not do.
