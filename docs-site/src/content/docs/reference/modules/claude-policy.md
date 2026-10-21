---
title: Claude Code managed policy
description: Validate and preview Claude Code managed policy; Business publishes it fleet-wide, with signed pull, attested check-in and drift.
---

`claude-policy` is the managed-policy authoring module. It is **selected by
default** on a fresh installation and requires
[governance](/reference/modules/vi-governance/). Its descriptor is
`olivares.claude-policy`, with API routes under `/v1/m/claude-policy/`.

It validates and previews the `managed-settings`, `hooks`, `managed-mcp` and
`sandbox` surfaces in every edition. Validation and dry-run do not change host
configuration.

Fleet-wide management is Business: publishing an immutable policy revision and
its revision history, the signed artifact that agents pull, the attested
check-in and the distribution view that compares published policy with observed
host configuration. Publishing reports distribution status; it does not
directly write host files. Missing observations do not mean that hosts comply.
A Community server answers these routes with `501` and an error that names
Business.

Reading, validation and dry-run require `governance:claude-policy:read`;
check-in requires `governance:claude-policy:write`; publishing requires
`governance:claude-policy:admin`. Invalid policy and inline
credentials are rejected.

Use **Edition & modules** in the console, or the CLI:

```sh
olivares modules ls
olivares modules on claude-policy
olivares claude-policy validate managed-settings --content-file settings.json
olivares modules off claude-policy
```

See the [CLI reference](/reference/cli/#command-olivares-claude-policy) and
[Claude Code hooks as a PEP](/how-to/connectors/claude-code-hooks-pep/) for related commands
and enforcement setup.
