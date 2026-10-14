# Install a tool from a Git or release URL

An administrator first curates an `agent` (CLI) or `mcp` catalog entry with a
pinned `spec.tool_install`. Create it through Catalog or
`olivares catalog entries create --spec-file declaration.json` with the entry's
kind, name, slug and version. The spec file contains:

```json
{
  "tool_install": {
    "source_url": "https://github.com/example/fixture-mcp.git",
    "version": "1.0.0",
    "sha256": "<SHA-256 of the compressed archive, 64 lowercase hexadecimal characters>",
    "program": "fixture-mcp",
    "config_home": "fixture-mcp",
    "kind": "mcp"
  }
}
```

The entry's version must match the declaration. `program` and `config_home` are
lowercase identifiers. Approval freezes the declaration in the catalog's
content hash and signs it when catalog signing is configured. Existing MCP
attestation policies still apply; a tenant that requires signing must supply
its verified attestation before approval.

Admit the entry, then install its exact approved payload:

```sh
olivares catalog admit <entry-id>
olivares tool install --from https://github.com/example/fixture-mcp.git --version 1.0.0
```

In the console, open **AI tools** (`/agent-tools`), **Install from a URL**, enter
the same URL and version, then review and approve the plan. CLI and console use
the same administrator routes, step-up policy, digest-bound plan, job, receipt
and audit ledger. A declaration must remain approved in the selected tenant
when installation starts. Select that organization in the console or with the
CLI’s `--tenant`; REST uses `X-Olivares-Tenant`. Host authority and its ledger
remain system-scoped. Deprecated, unknown and ambiguous declarations return
`unadmitted_declaration` (CLI exit 5). A failed archive digest returns
`digest_mismatch` (CLI exit 1), with no tools destination created or modified.
The job and audit ledger retain the refusal.

This installs Linux amd64/arm64 `.tar.gz` payloads containing `bin/<program>`
and optional `lib/<program>/...` resources. A GitHub repository URL selects the
`v<version>` tag archive. Other Git hosts need an explicit HTTPS `archive_url`
in the declaration; release URLs can point directly to the archive. Set
`archive_prefix` when the payload has a single enclosing directory. GitHub tag
archives infer `<repository>-<version>` unless explicitly overridden.

Downloads are bounded to 64 MiB compressed, 256 MiB expanded and 4096 members.
Paths and links cannot escape placement; links and special files are refused.
Other ordinary source files are skipped. No build scripts or package lifecycle
scripts run. The declared executable must support `--version` and report the
pinned version. Resources and the executable are rechecked against the receipt
on inventory and repeated installation. `config_home` remains part of the
pinned declaration and receipt; it does not import a login or configure a
session.

## Give a provider profile's sessions programs and npm registries

A provider profile can declare the installed programs and the npm registries
every session launched under it gets:

```sh
olivares agent profile update <profile-ref> --program fixture-mcp
olivares agent profile update <profile-ref> \
  --npm-registry url=https://npm.example.com/,scope=@example,secret=env/npm-example
```

REST uses `session_programs` and `session_registries` on
`PATCH /v1/m/sessions/provider-profiles/{ref}`; `[]` (or `--program ""`,
`--npm-registry ""`) withdraws a declaration. A change applies from the next
launch or resume. AI tools > Installed here lists, for each installed tool, the
programs and registries (URL and scope) of the profile its new session would
use; `GET /v1/m/sessions/provider-profiles/resolve?driver=<tool>` returns that
profile, without creating one.

Each program's newest verified install goes first on the session's `PATH`; a
launch whose program is not installed is refused with its name. A registry
names its `https` URL (`http` only on a loopback host), an optional scope and,
optionally, a session secret (`env/...`) holding its token. Declaring or
withdrawing programs or registries needs tenant administration: the token is
then given to every session launched under the profile, as the profile's
provider key is, and its agent can read it. At launch the engine writes the npm configuration
for that session alone, mode 0600, removes it when the session ends and points
npm at it with `npm_config_globalconfig`: the token is never in the session's
environment, argv or the run's record, and its output shows `[secret <name>]`
in its place. A session whose network is confined to its provider also
reaches the declared registry hosts and nothing else, so a session on another
profile cannot reach them. A launch that forwards `npm_config_*` through
`env_allow` beside declared registries is refused.

Limits: npm only, native sessions only. A registry that serves its tarballs
from another host fails in a confined session. An unconfined session (a tool's
own sign-in, or a tool whose network is not yet confined) keeps the host
network: another profile can reach the registry host but has no token. An
engine stopped before a session ends leaves that session's file in its data
directory, as a repository read credential does.

## Use an installed MCP server in sessions

Add the installed release as a local MCP server, pinned to its version:

```sh
olivares mcp add rea --tool rea@1.0.0 -- serve
olivares mcp enable rea
```

The engine takes the command and the SHA-256 of the executable from the
verified install. A version that is not installed here is refused. A session
start whose executable no longer has that digest is refused with
`process_start`; reinstall the release or pin the server again. Add
`--egress-host <host>` to limit the server's network to those HTTPS hosts.

By default an enabled server serves every session of the organization. To give
it only to some sessions, assign it to the targets skills use:

```sh
olivares mcp assign rea --workspace <workspace-id>
olivares mcp assign rea --agent <agent-id>
olivares mcp unassign rea --workspace <workspace-id>
```

A session gets the server's tools when its workspace, registered folder,
template, agent groups, agent or the session itself is a target. Every change
is in the audit ledger.
