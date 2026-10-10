---
title: "Skills — the immutable catalog and pinned assignments"
description: >-
  A selectable module: the immutable skills catalog and its pinned
  assignments. Skill packs are installed as digest-validated, immutable
  revisions; what a target may use is a recorded, audited pin — never a live
  folder edit.
---

Skills (`modules/skills`, `olivares.skills`) is a separately selectable module. It owns the
**immutable skills catalog** and its **pinned assignments**: a skill pack is
installed once, validated entry by entry against its manifest digest, and published
as an immutable revision — it is never edited in place afterwards. What a target (an
agent, a group, a workspace) may use is an **assignment** that pins a pack revision,
and every pin is written to the audit ledger (`skills.assignment.pin`).

**Off by default** on a fresh installation, `skills` has no required module in
the selectable module spec. Use **Edition & modules** in the console, or the CLI:

```sh
olivares modules ls
olivares modules on skills
olivares skills ls
olivares modules off skills
```

See the [CLI reference](/reference/cli/#command-olivares-skills) for installation,
revision and assignment options.

## What it ships

- **The catalog.** Installed packs under `/v1/m/skills/packs`, each immutable
  revision records its manifest digest; retiring a pack that is still assigned or
  used by recorded conversations is refused, not silently cascade-deleted.
- **The built-in catalog.** A vendored set of upstream skill packs pinned by commit
  and archive digest (`builtin/PIN.json`), validated on install exactly like an
  uploaded archive. Nothing in it runs at install.
- **Assignments.** `GET/POST /v1/m/skills/assignments` and
  `PUT/DELETE /v1/m/skills/assignments/{id}` pin a revision to a target, scoped to a
  workspace where one is active. Two assigned skills with the same name and different
  content are refused before a session starts, so a launch is deterministic.
- **Git import.** An operator-scoped importer can fetch a skills pack from a Git
  source under a pinned policy; the import lands in the catalog as another immutable
  revision, not as a live checkout the engine reads.

## Surfaces and permissions

The module's routes live in its beta namespace (`/v1/m/skills/…`), documented in the
[module-route reference](/reference/api-beta/) and rendered in the console's Skills
view. Catalog reads require `skills:catalog:read`, installs and revisions require
`skills:catalog:write`, retirement requires `skills:catalog:admin`, and assignment
changes require `skills:assignment:write`. Target authorization also applies. A session resolves its
skills only through recorded assignments — a folder that changes on disk never
changes what an already-pinned launch will run.

:::caution[Honest limits]
- **New since the 26.10.1<!-- release-fixed --> release.** The official 26.10.1<!-- release-fixed --> binary does not carry this
  module; it ships with the next release, and this page describes the module as it
  exists on the current integration line.
- **The catalog governs; it does not execute.** Installing, pinning and importing
  never run skill code — execution stays with the tools and sessions that consume a
  pinned revision.
:::

## Related

- [Modules catalog](/reference/modules/overview/) — the selectable modules and where this
  module sits among them.
- [MCP, skills & capabilities](/reference/modules/v-capabilities/) — the governing
  view over tools and capabilities this catalog feeds.
- [Internal catalog & marketplace](/reference/modules/xiv-catalog/) — the curated
  marketplace of approved agents, MCP servers and skills.
- [Module routes (beta)](/reference/api-beta/) — the `/v1/m/skills/` operations.
- [Honesty & limits](/start/honesty-and-limits/) — why immutability is stated, not
  implied.
