---
title: Claude Code managed policy
description: Validate and preview managed policy for Claude Code, Codex, Grok Build, OpenCode and Gemini CLI; Business publishes it fleet-wide, with signed pull, attested check-in and drift.
---

`claude-policy` is the managed-policy authoring module. It is **selected by
default** on a fresh installation and requires
[governance](/reference/modules/vi-governance/). Its descriptor is
`olivares.claude-policy`, with API routes under `/v1/m/claude-policy/`.

It validates and previews one surface per managed file, in every edition:

| Tool | Surfaces |
| --- | --- |
| Claude Code | `managed-settings`, `hooks`, `managed-mcp`, `sandbox` |
| Codex | `codex-requirements`, `codex-managed-config` |
| Grok Build | `grok-requirements` |
| OpenCode | `opencode-config` |
| Gemini CLI | `gemini-cli-settings` |

Validation and dry-run do not change host configuration.

Fleet-wide management is Business: publishing an immutable policy revision and
its revision history, the signed artifact that workstations pull and verify, the
attested check-in and the distribution view that compares published policy with
observed host configuration. Drift names the key that differs, not its value (a
Codex allowlist finding also names the entries the host adds). A host whose file
is absent, empty or invalid is reported as ungoverned. Publishing
reports distribution status; it does not directly write host files. Missing
observations do not mean that hosts comply. A Community server answers these
routes with `501` and an error that names Business.

A workstation applies a surface with `olivares claude-policy pull`. It checks the
artifact against the signing key fingerprint that `publish` printed, which you
hand to the host out of band (the 16-hex fingerprint, or better the full 64-hex
SHA-256 of the public key), then writes the file and checks in with what it
reads back from disk. An artifact that does not verify, or that carries an inline
credential, exits `7` and leaves the file as it was. An inline credential is a
written-out `sk-`, GitHub, Slack, Google, webhook or AWS key, a JWT, or a literal
value under a name such as `token`, `api_key`, `access_key`, `credential`,
`secret` or `Authorization`, in JSON or TOML; a reference such as `${TOKEN}`,
`Bearer ${TOKEN}`, `{env:TOKEN}` or the password in
`postgres://app:${DB_PASS}@db/app` is applied. The host also keeps the
highest revision it applied in a record beside the file
(`.<name>.olivares-revision`): a lower revision, such as an older signed artifact
replayed by the plane, exits `7` before anything is written, and applying one on
purpose takes `--revision N --allow-rollback`. Two pulls of one file take turns
on a lock file beside it (`.<name>.olivares-lock`), so a stale revision cannot
replace a newer one that is being applied at the same time.

Limits: the check goes by the shape of the text, not its meaning. A
`managed-instructions` file that quotes a sample AWS key, a sample JWT or a
`user:password@` URL also exits `7`, and the host keeps its old file. Write such
an example with a placeholder such as `${TOKEN}`.

Reading, validation and dry-run require `governance:claude-policy:read`;
check-in requires `governance:claude-policy:write` and also `read`, because it
answers whether the host's file equals the published policy; publishing requires
`governance:claude-policy:admin`. Invalid policy and inline
credentials are rejected.

Use **Settings > Modules** in the console, or the CLI:

```sh
olivares modules ls
olivares modules on claude-policy
olivares claude-policy validate managed-settings --content-file settings.json
olivares modules off claude-policy
```

See the [CLI reference](/reference/cli/#command-olivares-claude-policy) and
[Claude Code hooks as a PEP](/how-to/connectors/claude-code-hooks-pep/) for related commands
and enforcement setup.
