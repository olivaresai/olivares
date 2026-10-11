---
title: Claude Code managed policy
description: Versioned authoring, distribution status and observed drift for Claude Code managed policy.
---

`claude-policy` is the managed-policy authoring module. It is **selected by
default** on a fresh installation and requires
[governance](/reference/modules/vi-governance/). Its descriptor is
`olivares.claude-policy`, with API routes under `/v1/m/claude-policy/`.

It validates, previews and publishes the `managed-settings`, `hooks`,
`managed-mcp` and `sandbox` surfaces. Validation and dry-run do not change host
configuration. Publishing persists an immutable policy revision and reports
distribution status; it does not directly write host files. Artifact pull,
attested check-in and the distribution view support comparison of published
policy with observed host configuration. Missing observations do not mean
that hosts comply.

Reading, validation and dry-run require `governance:claude-policy:read`;
check-in requires `governance:claude-policy:write`; publishing requires
`governance:claude-policy:admin`. Invalid policy and inline
credentials are rejected.

Use **Edition & modules** in the console, or the CLI:

```sh
olivares modules ls
olivares modules on claude-policy
olivares claude-policy versions ls managed-settings
olivares modules off claude-policy
```

See the [CLI reference](/reference/cli/#command-olivares-claude-policy) and
[Claude Code hooks as a PEP](/how-to/connectors/claude-code-hooks-pep/) for related commands
and enforcement setup.
