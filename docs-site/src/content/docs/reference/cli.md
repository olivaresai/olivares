---
title: "CLI reference: olivares"
description: "Verified subcommands and flags for the single olivares binary, including the secure-by-default serve options."
---

Olivares AI ships as one static Go binary named `olivares`. The same artifact is the engine, the embedded web UI (served from the same origin as the API), and the edge collector — the role is selected by the subcommand you run. This page documents the command surface of the community (AGPL) build. The sections below explain the commands you meet first; the [Complete command reference](#complete-command-reference) at the end is generated from the binary and covers every command in that build. The surface is still moving (see [Stability](#stability) at the end).

For how to obtain and run the binary, see [Self-hosting](/how-to/self-hosting/). For configuration that lives in environment variables rather than flags, see [Configuration](/reference/configuration/).

## Overview

```
olivares <subcommand> [flags]
```

The root command sorts its subcommands into the sections `olivares --help` prints: setup and configuration, operate, govern, observe and diagnose, security, and release and upgrade.

The sections that follow explain the commands you meet first — what they do to your install, and what is safe to expose — which is the part a table cannot carry. For the exhaustive list, [Complete command reference](#complete-command-reference) at the end of this page is **generated from the binary** and covers every command in the build with its flags, its exit codes and its output contract. It was written by enumerating the command tree of the built binary, so treat it as a snapshot of that build rather than as live output: the push gate and CI run `lint:cli-coverage`, which checks that the commands declared in the source stay documented somewhere in this reference and names the ones that are not, but it does not re-derive this section and does not compare flags, exit codes or output contracts. When this page and your binary disagree, `olivares <subcommand> --help` is authoritative.

:::note[Single binary, multiple roles]
There is no separate "server" and "agent" download. `olivares serve` is the engine; `olivares collector` is the data-plane collector for the distributed topology. Both load the same source connectors the same way — only where the observations go differs. See [Architecture](/explanation/architecture/overview/).
:::

## olivares version

Prints the version, commit, build date, OS/arch, and Go runtime version to stdout.

```sh
olivares version
```

The version string is injected at build time. A build from a working tree that was not produced by a tagged release reports a development version (for example `dev`), so do not treat the version string as a guarantee of provenance — verify releases with the signed artifacts instead. See [Verify a release](/how-to/verify-a-release/).

## olivares serve

Runs the control plane: the HTTP server (REST API plus the embedded web UI on the same origin) and the gRPC server. **TLS is on by default**, the listeners bind **loopback by default**, and there are **no default credentials**.

```sh
olivares serve [flags]
```

### Secure defaults

These are properties of `serve`, not opt-ins:

- **TLS is on by default.** If no certificate is supplied, the engine generates a self-signed certificate in the data directory and logs both its SHA-256 certificate fingerprint and, as `pin_sha256`, the leaf SPKI pin; clients either trust the certificate or pass that `pin_sha256` value to `--pin-sha256`. The two are different digests of different bytes — the certificate fingerprint is not a pin. The gRPC server fails closed: outside `--insecure` it will not start in plaintext.
- **Loopback by default.** Both the HTTP and gRPC listeners default to `127.0.0.1`. Exposing the control plane beyond the local host is a deliberate change you make by setting a non-loopback bind and fronting it with your own ingress.
- **No default credentials.** On a fresh install with no users, the engine mints a **one-time, single-use setup token** (prefix `olst_`) and prints it to **stdout only** (never to the logs). You create the first administrator by posting that token to the setup endpoint, then log in. See [First-boot setup](#first-boot-setup).

### Flags

Every flag `serve` takes, with its type and default, is listed under [`olivares serve`](#command-olivares-serve) in the generated reference below. This section covers only the four that change what the install exposes: `--listen` and `--grpc-listen` (where it binds), `--insecure` (whether the transport is encrypted) and `--seed-demo` (whether the data is real).

The default store is SQLite (pure-Go, single-node, suitable for air-gapped installs). Selecting `postgres` is what you do for multi-tenant or scale-out deployments, where row-level security provides the tenant backstop. See [Configuration](/reference/configuration/) and [Self-hosting](/how-to/self-hosting/).

:::caution[`--insecure` is for localhost development only]
`--insecure` serves plaintext HTTP and gRPC. Bearer tokens travel in the clear on a plaintext transport, so never use this flag on any address reachable beyond the local host. Outside `--insecure`, the gRPC server refuses to start without TLS rather than silently downgrading.
:::

### `--seed-demo` is demo-only and refuses non-loopback

`--seed-demo` provisions a **synthetic, fabricated** estate together with a demo administrator whose password is **public** (it lives in the source tree). It exists purely to make the web UI and end-to-end tests render against live-shaped data.

Because the demo credential is public, `serve` **refuses to start with `--seed-demo` on any non-loopback bind** and exits with an error directing you to bind `127.0.0.1` or to run a real install without the flag. Treat `--seed-demo` as throwaway: use a disposable data directory, and never point it at data you care about.

:::danger[Never run `--seed-demo` as a real install]
The demo administrator has a publicly known password. A real install is `serve` **without** `--seed-demo`, where the engine mints a one-time setup token and you create your own administrator. Do not mix the two: a demo data directory is not a production data directory.
:::

### First-boot setup

On the first boot of an install that has no users, `serve` prints a block to stdout containing the one-time setup token (prefix `olst_`) and the request you need to bootstrap the first administrator. The flow is:

1. Read the `olst_` token from the engine's stdout (with the container deployment, read it from the container logs).
2. Create the first administrator by posting the token, an email, and a password to the setup endpoint (`POST /v1/setup`).
3. Log in (`POST /v1/auth/login`) to obtain a session token (prefix `olvs_`).

The setup token is shown once and is single-use; once a user exists, no token is minted. Olivares AI uses **opaque** bearer tokens (not JWTs); API keys carry the prefix `olvk_`. For the full authentication contract and tenant resolution rules, see the [Security model](/explanation/security/security-model/) and the [API reference](/reference/api/).

### Run it

```sh
# Build, then run (there is no "task serve" / "task run" target).
task build
./bin/olivares serve
# Read the one-time olst_ setup token from this process's stdout.
```

Or run the container deployment and read the setup token from the logs; see [Self-hosting](/how-to/self-hosting/).

## olivares collector

Runs the binary as an **edge collector** for the distributed topology. A collector loads the source connectors named in your sources configuration locally and **pushes** their observations to a remote core over gRPC. It opens **no inbound listener** — the collector is the secure-default data plane: it dials out, it does not accept connections.

```sh
olivares collector --core-addr host:port [flags]
```

The collector authenticates to the core in two layers: a bearer token holding an ingest principal, and — when the core enforces mutual TLS — a collector client certificate. This is how the data plane runs on customer infrastructure while a central core aggregates: a failing collector never sits in the data path of any agent.

:::note[Which sources a collector loads]
Both `serve` and `collector` wire their source connectors from the same configuration, read from the environment before the runtime starts. An unconfigured or empty source warns honestly rather than failing the process. Configuring sources is covered in [Connect a source](/how-to/connect-a-source/) and [Connect Claude Code](/how-to/connect-claude-code/); the configuration mechanism is in [Configuration](/reference/configuration/).
:::

The collector subcommand is the **mechanism** for the distributed path. The packaging around it (a fleet of collectors, signed charts, OCI images) is part of the deployment story rather than this CLI page — see [Architecture](/explanation/architecture/overview/) and [Self-hosting](/how-to/self-hosting/).

## olivares openapi

Prints the engine's OpenAPI 3.1 document to stdout, without needing a running server.

```sh
olivares openapi > openapi.json            # stable core contract
olivares openapi --beta > openapi.beta.json  # beta module-route document
```

The default emits the same contract the engine serves at `GET /openapi.json` (the stable core paths), deterministically indented so the output diffs cleanly; it is the source of truth for the rendered [API reference](/reference/api/) and for the web client's typed code generation. `--beta` emits the **beta** module-route document (`/v1/m/<ns>/…`, served at `GET /openapi.beta.json`, rendered at the [module-route reference](/reference/api-beta/)) — reflected from the routes the modules register, with field-level shapes still expressed as typed interfaces (see also [Modules overview](/reference/modules/overview/)).

<!-- BEGIN GENERATED olivares-cli-reference -->
<!-- Generated from the olivares command tree by `bash scripts/check-cli-ref-docs.sh --write`.
     Do not edit inside this region: the push gate compares it against the binary. -->

## Complete command reference

This section is generated from the command tree of the community (AGPL) build of the `olivares` binary at this commit. It covers 786 command nodes — the root command and 785 subcommands, of which 174 are groups that carry subcommands and 9 are hidden diagnostics — together with the 2571 flags they declare. It is regenerated from the binary rather than kept by hand, so a command or flag added without a documentation change fails the push gate.

Nothing here is a stability promise: see [Stability](#stability) below for what may still change.

### Exit codes

Every command in the tree exits with one of these codes. Scripts and CI pipelines branch on them, so an existing code is never renumbered — only appended to.

| Code | Name | Meaning |
|---|---|---|
| `0` | `OK` | the command succeeded. |
| `1` | `Err` | generic failure with no more specific classification. |
| `2` | `Usage` | the invocation itself is wrong (unknown flag, bad arguments). |
| `3` | `Auth` | the control plane rejected the caller (401/403). |
| `4` | `NotFound` | the addressed entity does not exist (404). |
| `5` | `Conflict` | the request contradicts current state (409). |
| `6` | `Server` | the control plane failed or was unreachable (5xx, transport). |
| `7` | `Degraded` | the command succeeded but reports a degraded condition (`status` when the engine is not fully ok; `security check` on an affected version). |
| `8` | `Indeterminate` | the command could not reach a verdict because an input it needs is UNKNOWN, as distinct from a verdict of "fine" (0) or "bad" (7) and from a failure to run (1). `security check` returns it when the build declares no usable version, so no advisory range can be evaluated against it: a clean answer there would be an artifact, not a measurement. A fleet sweep must treat this as "not yet answered", never as "clean". |

### Flags every command accepts

`-h`, `--help` prints the command's own help and exits `0`. The flags below are declared on the root command and inherited by every command in the tree.

| Flag | Type | Default | Description |
|---|---|---|---|
| `-o`, `--output` | `string` | `text` | **inherited**. global output format: text or json (report commands keep json unless -o is given) |

Command groups declare further flags that their own subcommands inherit. A flag marked **inherited** in any table below is declared there and taken by everything under it, so it is listed once, at the command that declares it, rather than repeated on each of its subcommands.

### Command index

All 786 commands, in alphabetical order.

| Command | Summary |
|---|---|
| [`olivares`](#command-olivares) | Olivares AI — self-hosted engine for enterprise AI |
| `olivares __extract` | internal: extract text from a rich document on stdin (sandboxed re-exec target) _(hidden)_ |
| [`olivares accessmap`](#command-olivares-accessmap) | Query the access graph, least-privilege drift and attack paths |
| [`olivares accessmap attack-paths`](#command-olivares-accessmap-attack-paths) | Reachability, privilege-escalation and exfiltration analyses |
| [`olivares accessmap attack-paths escalation`](#command-olivares-accessmap-attack-paths-escalation) | List the privilege-escalation chains open to one agent |
| [`olivares accessmap attack-paths exfil`](#command-olivares-accessmap-attack-paths-exfil) | List the exfiltration routes out of one resource |
| [`olivares accessmap attack-paths reachability`](#command-olivares-accessmap-attack-paths-reachability) | List the resources one agent can reach |
| [`olivares accessmap attack-paths summary`](#command-olivares-accessmap-attack-paths-summary) | Show the estate-wide attack-surface counts |
| [`olivares accessmap drift`](#command-olivares-accessmap-drift) | Show permitted-vs-observed least-privilege drift |
| [`olivares accessmap graph`](#command-olivares-accessmap-graph) | List the access graph as nodes and edges |
| [`olivares accessmap neighbors`](#command-olivares-accessmap-neighbors) | List the edges touching one node |
| [`olivares adoption`](#command-olivares-adoption) | Report Claude adoption by org, team, trend and developer |
| [`olivares adoption developers`](#command-olivares-adoption-developers) | Break adoption down by developer (privileged: exposes identity) |
| [`olivares adoption discrepancy`](#command-olivares-adoption-discrepancy) | Measure how far the two lenses disagree |
| [`olivares adoption summary`](#command-olivares-adoption-summary) | Show both adoption lenses over one window |
| [`olivares adoption teams`](#command-olivares-adoption-teams) | Break adoption down by team |
| [`olivares adoption trend`](#command-olivares-adoption-trend) | Show a per-day series for ONE lens |
| [`olivares agent`](#command-olivares-agent) | Operate governed Claude Code sessions (launch, attach, stop, resume, clean up) |
| [`olivares agent managed-settings`](#command-olivares-agent-managed-settings) | Render the Claude Code managed-settings.json that governs operated sessions (PEP hook) |
| [`olivares agent session`](#command-olivares-agent-session) | Manage the lifecycle of governed Claude Code sessions |
| [`olivares agent session attach`](#command-olivares-agent-session-attach) | Stream a live session's I/O (server-sent events) to stdout |
| [`olivares agent session cleanup`](#command-olivares-agent-session-cleanup) | Release a stopped session (mark cleaned) |
| [`olivares agent session create`](#command-olivares-agent-session-create) | Launch a governed Claude Code session |
| [`olivares agent session events`](#command-olivares-agent-session-events) | Show a session's lifecycle ledger |
| [`olivares agent session get`](#command-olivares-agent-session-get) | Show one session |
| [`olivares agent session input`](#command-olivares-agent-session-input) | Send one NDJSON line to a live session's stdin ('-' or empty reads stdin) |
| [`olivares agent session ls`](#command-olivares-agent-session-ls) | List operated sessions |
| [`olivares agent session resume`](#command-olivares-agent-session-resume) | Resume a stopped session |
| [`olivares agent session rm`](#command-olivares-agent-session-rm) | Delete a cleaned session's record |
| [`olivares agent session stop`](#command-olivares-agent-session-stop) | Stop a running session |
| [`olivares agent workspace`](#command-olivares-agent-workspace) | Manage governed workspaces and their files (browse/read/write/move/delete) |
| [`olivares agent workspace add`](#command-olivares-agent-workspace-add) | Register a host directory as a governed workspace |
| [`olivares agent workspace files`](#command-olivares-agent-workspace-files) | List one directory level in a workspace |
| [`olivares agent workspace get`](#command-olivares-agent-workspace-get) | Read a file's content to stdout (DLP-governed) |
| [`olivares agent workspace ls`](#command-olivares-agent-workspace-ls) | List registered workspaces |
| [`olivares agent workspace mkdir`](#command-olivares-agent-workspace-mkdir) | Create a directory (and parents) |
| [`olivares agent workspace mv`](#command-olivares-agent-workspace-mv) | Move/rename a path within the workspace |
| [`olivares agent workspace put`](#command-olivares-agent-workspace-put) | Write a file from --from (a local file or '-' for stdin) |
| [`olivares agent workspace rm`](#command-olivares-agent-workspace-rm) | Delete a file or (with --recursive) a directory subtree |
| [`olivares agent workspace rm-workspace`](#command-olivares-agent-workspace-rm-workspace) | Deregister a workspace (does NOT delete host files) |
| [`olivares agent workspace stat`](#command-olivares-agent-workspace-stat) | Show metadata for one path |
| [`olivares audit`](#command-olivares-audit) | Inspect and checkpoint the evidence ledger |
| [`olivares audit archive`](#command-olivares-audit-archive) | Export and verify the immutable ledger archive |
| [`olivares audit archive export`](#command-olivares-audit-archive-export) | Export a tenant's ledger as verifiable archive segments to a directory |
| [`olivares audit archive verify`](#command-olivares-audit-archive-verify) | Verify an exported archive directory offline (no store, no network) |
| [`olivares audit checkpoint`](#command-olivares-audit-checkpoint) | Write a signed checkpoint (all tenants, or one with --tenant) |
| [`olivares audit export`](#command-olivares-audit-export) | Export a tenant's ledger to a SIEM format (cef\|leef\|syslog\|otlp\|otlp_envelope\|otlp_log_record\|ocsf) |
| [`olivares audit key-transition`](#command-olivares-audit-key-transition) | Record the off-box-signed signing-key epoch boundary after `keys rotate` |
| [`olivares audit observe-report`](#command-olivares-audit-observe-report) | Summarize constrained-observe shadows for an observe→enforce promotion decision |
| [`olivares audit recover`](#command-olivares-audit-recover) | Seal a corrupt audit tail and start a governed recovery epoch |
| [`olivares audit verify`](#command-olivares-audit-verify) | Verify a tenant's chain and its signed checkpoints |
| [`olivares auth`](#command-olivares-auth) | Manage CLI authentication and named client contexts |
| [`olivares auth bootstrap`](#command-olivares-auth-bootstrap) | Redeem the one-time first-boot token: create the first organization and superadmin |
| [`olivares auth login`](#command-olivares-auth-login) | Validate a credential and save it in a client context |
| [`olivares auth logout`](#command-olivares-auth-logout) | Remove a saved token from a client context |
| [`olivares auth status`](#command-olivares-auth-status) | Show the effective CLI identity and authentication context |
| [`olivares auth use-context`](#command-olivares-auth-use-context) | Select the current CLI client context |
| [`olivares capabilities`](#command-olivares-capabilities) | What this estate can do: connected servers, and the tools and skills they bring |
| [`olivares capabilities servers`](#command-olivares-capabilities-servers) | The MCP servers this estate talks to |
| [`olivares capabilities servers get`](#command-olivares-capabilities-servers-get) | Show one MCP server and what it brings |
| [`olivares capabilities servers ls`](#command-olivares-capabilities-servers-ls) | List the connected MCP servers |
| [`olivares capabilities skills`](#command-olivares-capabilities-skills) | The skills the connected servers contribute |
| [`olivares capabilities tools`](#command-olivares-capabilities-tools) | The tools the connected servers expose, with their destructive hints |
| [`olivares capabilities wiring`](#command-olivares-capabilities-wiring) | Who is actually using which capability, as observed edges |
| [`olivares catalog`](#command-olivares-catalog) | Admit and govern catalog entries, connectors and MCP servers |
| [`olivares catalog connector-admission`](#command-olivares-catalog-connector-admission) | Read and set the connector supply-chain admission policy |
| [`olivares catalog connector-admission ls`](#command-olivares-catalog-connector-admission-ls) | List recorded connector admission verdicts |
| [`olivares catalog connector-admission policy`](#command-olivares-catalog-connector-admission-policy) | Read or replace the connector admission policy |
| [`olivares catalog connector-admission policy get`](#command-olivares-catalog-connector-admission-policy-get) | Show the connector admission policy |
| [`olivares catalog connector-admission policy set`](#command-olivares-catalog-connector-admission-policy-set) | Replace the connector admission policy |
| [`olivares catalog entries`](#command-olivares-catalog-entries) | Author, review and admit catalog entries |
| [`olivares catalog entries admit`](#command-olivares-catalog-entries-admit) | Verify a supply-chain attestation for an entry |
| [`olivares catalog entries approve`](#command-olivares-catalog-entries-approve) | Approve a submitted entry, hashing and signing it |
| [`olivares catalog entries create`](#command-olivares-catalog-entries-create) | Author a draft catalog entry |
| [`olivares catalog entries deprecate`](#command-olivares-catalog-entries-deprecate) | Retire an approved entry |
| [`olivares catalog entries get`](#command-olivares-catalog-entries-get) | Show one catalog entry |
| [`olivares catalog entries instantiate`](#command-olivares-catalog-entries-instantiate) | Request an instance from an approved entry |
| [`olivares catalog entries ls`](#command-olivares-catalog-entries-ls) | List catalog entries |
| [`olivares catalog entries rm`](#command-olivares-catalog-entries-rm) | Delete a catalog entry |
| [`olivares catalog entries set`](#command-olivares-catalog-entries-set) | Replace a draft entry's authored fields |
| [`olivares catalog entries submit`](#command-olivares-catalog-entries-submit) | Submit a draft entry for review |
| [`olivares catalog entries verify`](#command-olivares-catalog-entries-verify) | Recompute an entry's hash and check its signature |
| [`olivares catalog instances`](#command-olivares-catalog-instances) | Review and decide self-service instantiation requests |
| [`olivares catalog instances get`](#command-olivares-catalog-instances-get) | Show one instantiation request |
| [`olivares catalog instances ls`](#command-olivares-catalog-instances-ls) | List instantiation requests |
| [`olivares catalog instances transition`](#command-olivares-catalog-instances-transition) | Record a governance decision on an instance |
| [`olivares catalog mcp-admission`](#command-olivares-catalog-mcp-admission) | Read and set the MCP server supply-chain admission policy |
| [`olivares catalog mcp-admission ls`](#command-olivares-catalog-mcp-admission-ls) | List recorded MCP server admission verdicts |
| [`olivares catalog mcp-admission policy`](#command-olivares-catalog-mcp-admission-policy) | Read or replace the MCP server admission policy |
| [`olivares catalog mcp-admission policy get`](#command-olivares-catalog-mcp-admission-policy-get) | Show the MCP server admission policy |
| [`olivares catalog mcp-admission policy set`](#command-olivares-catalog-mcp-admission-policy-set) | Replace the MCP server admission policy |
| [`olivares catalog pubkey`](#command-olivares-catalog-pubkey) | Show the public key catalog approvals are signed with |
| [`olivares claude-agents`](#command-olivares-claude-agents) | Read a managed agent session's thread events and answer its tool confirmations |
| [`olivares claude-agents sessions`](#command-olivares-claude-agents-sessions) | Inspect and answer one managed agent session |
| [`olivares claude-agents sessions events`](#command-olivares-claude-agents-sessions-events) | List one managed session's thread events |
| [`olivares claude-agents sessions tool-confirmation`](#command-olivares-claude-agents-sessions-tool-confirmation) | Answer a managed agent's pending tool use (allow or deny) |
| [`olivares claude-hook`](#command-olivares-claude-hook) | Governed PEP hook client: forward a Claude Code hook to the control plane and relay the decision (deny-closed) |
| [`olivares claude-policy`](#command-olivares-claude-policy) | Author, publish and track the Claude Code managed-* policy surfaces |
| [`olivares claude-policy artifact`](#command-olivares-claude-policy-artifact) | Fetch the signed artifact a distribution agent would pull |
| [`olivares claude-policy checkin`](#command-olivares-claude-policy-checkin) | Report an agent's applied artifact and observed config (exit 7 when unverified) |
| [`olivares claude-policy distribution`](#command-olivares-claude-policy-distribution) | Show published vs signed vs observed, scope by scope |
| [`olivares claude-policy dry-run`](#command-olivares-claude-policy-dry-run) | Resolve a document against observed hosts without writing anything |
| [`olivares claude-policy publish`](#command-olivares-claude-policy-publish) | Publish a new revision and, when a distributor is wired, sign it |
| [`olivares claude-policy validate`](#command-olivares-claude-policy-validate) | Validate a policy document server-side (exit 7 when it has errors) |
| [`olivares claude-policy versions`](#command-olivares-claude-policy-versions) | List and read published revisions of a surface |
| [`olivares claude-policy versions get`](#command-olivares-claude-policy-versions-get) | Show one revision with its document content |
| [`olivares claude-policy versions ls`](#command-olivares-claude-policy-versions-ls) | List a surface's published revisions |
| [`olivares codex`](#command-olivares-codex) | Author OpenAI Codex governance artifacts (managed config) |
| [`olivares codex managed-config`](#command-olivares-codex-managed-config) | Render the Codex requirements.toml + managed_config.toml from a governance Policy JSON |
| [`olivares codex-hook`](#command-olivares-codex-hook) | Governed PEP hook client for Codex: forward a Codex hook to the control plane and relay the decision (deny-closed) |
| [`olivares collector`](#command-olivares-collector) | Run as an edge collector: push local source observations to a remote core over gRPC+mTLS |
| [`olivares commands`](#command-olivares-commands) | Print the full command tree of this binary (diagnostic) _(hidden)_ |
| [`olivares completion`](#command-olivares-completion) | Generate shell autocompletion scripts |
| [`olivares completion bash`](#command-olivares-completion-bash) | Generate bash autocompletion script |
| [`olivares completion fish`](#command-olivares-completion-fish) | Generate fish autocompletion script |
| [`olivares completion powershell`](#command-olivares-completion-powershell) | Generate PowerShell autocompletion script |
| [`olivares completion zsh`](#command-olivares-completion-zsh) | Generate zsh autocompletion script |
| [`olivares compliance`](#command-olivares-compliance) | Operate legal holds, GDPR erasure and regulatory artifacts |
| [`olivares compliance calendar`](#command-olivares-compliance-calendar) | Show the regulatory calendar and watchlist |
| [`olivares compliance depth`](#command-olivares-compliance-depth) | Inspect compliance-depth packs and control monitoring |
| [`olivares compliance depth drift`](#command-olivares-compliance-depth-drift) | List detected control drift |
| [`olivares compliance depth sector`](#command-olivares-compliance-depth-sector) | List sector overlay packs |
| [`olivares compliance depth snapshots`](#command-olivares-compliance-depth-snapshots) | List CCM control snapshots |
| [`olivares compliance depth us-law`](#command-olivares-compliance-depth-us-law) | List US state-law packs |
| [`olivares compliance dora`](#command-olivares-compliance-dora) | Inspect DORA registers and classified incidents |
| [`olivares compliance dora incidents`](#command-olivares-compliance-dora-incidents) | List classified DORA incidents |
| [`olivares compliance dora registers`](#command-olivares-compliance-dora-registers) | List DORA registers of information |
| [`olivares compliance erasure`](#command-olivares-compliance-erasure) | Register, execute and evidence GDPR erasure requests |
| [`olivares compliance erasure custody`](#command-olivares-compliance-erasure-custody) | Show an erasure's append-only chain of custody |
| [`olivares compliance erasure execute`](#command-olivares-compliance-erasure-execute) | Execute an erasure (IRREVERSIBLE, dual-control) |
| [`olivares compliance erasure get`](#command-olivares-compliance-erasure-get) | Show one erasure request |
| [`olivares compliance erasure ls`](#command-olivares-compliance-erasure-ls) | List erasure requests |
| [`olivares compliance erasure receipt`](#command-olivares-compliance-erasure-receipt) | Show the sealed, ledger-anchored erasure receipt |
| [`olivares compliance erasure request`](#command-olivares-compliance-erasure-request) | Register an erasure request (destroys nothing) |
| [`olivares compliance holds`](#command-olivares-compliance-holds) | Place, inspect and release legal holds |
| [`olivares compliance holds check`](#command-olivares-compliance-holds-check) | Ask whether any active hold already covers a subject or class |
| [`olivares compliance holds custody`](#command-olivares-compliance-holds-custody) | Show a hold's append-only chain of custody |
| [`olivares compliance holds get`](#command-olivares-compliance-holds-get) | Show one legal hold |
| [`olivares compliance holds ls`](#command-olivares-compliance-holds-ls) | List legal holds |
| [`olivares compliance holds place`](#command-olivares-compliance-holds-place) | Place a legal hold (takes effect immediately) |
| [`olivares compliance holds release`](#command-olivares-compliance-holds-release) | Release a legal hold (dual-control, no break-glass) |
| [`olivares compliance oscal`](#command-olivares-compliance-oscal) | Inspect ingested OSCAL profiles and SSPs |
| [`olivares compliance oscal ls`](#command-olivares-compliance-oscal-ls) | List registered OSCAL documents |
| [`olivares compliance subject`](#command-olivares-compliance-subject) | Answer a data subject's erasure request by subject id |
| [`olivares compliance subject erase`](#command-olivares-compliance-subject-erase) | Register and execute an erasure for one subject (IRREVERSIBLE) |
| [`olivares compliance subject status`](#command-olivares-compliance-subject-status) | Show erasure status for one data subject |
| [`olivares config`](#command-olivares-config) | Generate validated engine configuration (the non-interactive setup) |
| [`olivares config effective`](#command-olivares-config-effective) | Print configured OLIVARES_* values with secrets redacted |
| [`olivares config generate`](#command-olivares-config-generate) | Compose a validated /etc/olivares/olivares.env (or k8s snippet) from flags |
| [`olivares config validate`](#command-olivares-config-validate) | Validate configured OLIVARES_* environment keys |
| [`olivares connector`](#command-olivares-connector) | Scaffold out-of-tree connector projects |
| [`olivares connector init`](#command-olivares-connector-init) | Generate a connector repository from an archetype template |
| [`olivares consoleviews`](#command-olivares-consoleviews) | Manage saved console views (filter and parameter sets) |
| [`olivares consoleviews create`](#command-olivares-consoleviews-create) | Save a new view |
| [`olivares consoleviews get`](#command-olivares-consoleviews-get) | Show one saved view in full |
| [`olivares consoleviews ls`](#command-olivares-consoleviews-ls) | List the views you can see |
| [`olivares consoleviews rm`](#command-olivares-consoleviews-rm) | Delete your own saved view |
| [`olivares consoleviews update`](#command-olivares-consoleviews-update) | Replace the writable fields of your own view |
| [`olivares db`](#command-olivares-db) | Prepare and verify the database before serving (Postgres roles, RLS posture) |
| [`olivares db check`](#command-olivares-db-check) | Probe a DSN's role posture and report whether the engine will accept it (read-only) |
| [`olivares db init`](#command-olivares-db-init) | Provision the least-privilege Postgres roles + database idempotently (no psql by hand) |
| [`olivares ddil`](#command-olivares-ddil) | Air-gap DDIL bundles: export, verify and import governance state across a disconnected gap |
| [`olivares ddil export`](#command-olivares-ddil-export) | Assemble and sign a DDIL bundle from the local governance store |
| [`olivares ddil import`](#command-olivares-ddil-import) | Verify, reconcile and apply a DDIL courier bundle fail-closed |
| [`olivares ddil keygen`](#command-olivares-ddil-keygen) | Generate an Ed25519 DDIL transport keypair |
| [`olivares ddil verify`](#command-olivares-ddil-verify) | Verify and inspect a DDIL courier bundle without applying it |
| [`olivares deploy`](#command-olivares-deploy) | Declare, plan, apply, retire and roll back governed agent deployments |
| [`olivares deploy apply`](#command-olivares-deploy-apply) | Actuate the current version through the approval gate (two-phase) |
| [`olivares deploy definitions`](#command-olivares-deploy-definitions) | Declare and version deployment definitions |
| [`olivares deploy definitions create`](#command-olivares-deploy-definitions-create) | Declare a deployment definition from a JSON spec |
| [`olivares deploy definitions get`](#command-olivares-deploy-definitions-get) | Show one definition with its current spec and real state |
| [`olivares deploy definitions ls`](#command-olivares-deploy-definitions-ls) | List deployment definitions with their drift |
| [`olivares deploy definitions revisions`](#command-olivares-deploy-definitions-revisions) | List a definition's revision history |
| [`olivares deploy definitions rm`](#command-olivares-deploy-definitions-rm) | Delete a definition and its revisions (destructive; needs --yes when unattended) |
| [`olivares deploy definitions update`](#command-olivares-deploy-definitions-update) | Publish a new revision of a definition (PUT) |
| [`olivares deploy operations`](#command-olivares-deploy-operations) | List the append-only ledger of plan/apply/retire/rollback operations |
| [`olivares deploy plan`](#command-olivares-deploy-plan) | Compute the change set an apply WOULD make (nothing is actuated) |
| [`olivares deploy retire`](#command-olivares-deploy-retire) | Take a live deployment down (destructive POST; needs --yes when unattended) |
| [`olivares deploy rollback`](#command-olivares-deploy-rollback) | Revert a definition to an earlier version (destructive POST; needs --yes when unattended) |
| [`olivares deploy verify`](#command-olivares-deploy-verify) | Check the real deployment against its declared spec |
| [`olivares deploy wirings`](#command-olivares-deploy-wirings) | List what each deployment is wired to, and how that was attributed |
| [`olivares dr`](#command-olivares-dr) | Disaster recovery: ledger-continuity-safe backup and restore |
| [`olivares dr backup`](#command-olivares-dr-backup) | Write a ledger-continuity-safe DR bundle |
| [`olivares dr drill`](#command-olivares-dr-drill) | Full DR round-trip drill (backup→destroy→restore→verify) with a measured RTO |
| [`olivares dr inspect`](#command-olivares-dr-inspect) | Print a DR bundle's manifest (no KEK needed; no secrets shown) |
| [`olivares dr ls`](#command-olivares-dr-ls) | List DR bundles (local, or --offsite for the S3/R2 mirror) |
| [`olivares dr pull`](#command-olivares-dr-pull) | Download a DR bundle from the offsite S3/R2 target |
| [`olivares dr push`](#command-olivares-dr-push) | Upload an existing DR bundle to the offsite S3/R2 target |
| [`olivares dr restore`](#command-olivares-dr-restore) | Restore a DR bundle and verify ledger continuity (non-zero exit if not safe) |
| [`olivares dr verify`](#command-olivares-dr-verify) | Test a DR bundle WITHOUT touching the live data dir (the DR drill) |
| [`olivares evals`](#command-olivares-evals) | Eval methodology tools: the CI regression gate and the judge-calibration labeler |
| [`olivares evals gate`](#command-olivares-evals-gate) | Run the CI regression gate (exit 0 pass/warn, 1 fail) or re-check one after a governed override |
| [`olivares evals label`](#command-olivares-evals-label) | Guided human-labeling session for the judge↔human calibration set |
| [`olivares eventing`](#command-olivares-eventing) | Manage the eventing platform (webhook event subscriptions, deliveries, event log) |
| [`olivares eventing dead-letters`](#command-olivares-eventing-dead-letters) | Inspect and redeliver dead-lettered deliveries |
| [`olivares eventing dead-letters ls`](#command-olivares-eventing-dead-letters-ls) | List dead-lettered deliveries (status=dead) |
| [`olivares eventing dead-letters redeliver`](#command-olivares-eventing-dead-letters-redeliver) | Requeue a dead-lettered delivery for retry |
| [`olivares eventing deliveries`](#command-olivares-eventing-deliveries) | Inspect delivery state (ls) |
| [`olivares eventing deliveries ls`](#command-olivares-eventing-deliveries-ls) | List deliveries (optionally filtered by --subscription, --status) |
| [`olivares eventing egress`](#command-olivares-eventing-egress) | Inspect and actuate the egress destination control's rollout |
| [`olivares eventing egress actuate`](#command-olivares-eventing-egress-actuate) | Apply a deliberate rollout decision for the egress destination control |
| [`olivares eventing egress status`](#command-olivares-eventing-egress-status) | Report the rollout disposition and what enforcing would block |
| [`olivares eventing events`](#command-olivares-eventing-events) | Inspect the captured event log |
| [`olivares eventing events ls`](#command-olivares-eventing-events-ls) | List captured events (optionally from a seq cursor, filtered by --type) |
| [`olivares eventing fence`](#command-olivares-eventing-fence) | Inspect, arm and verify the cross-version egress writer fence |
| [`olivares eventing fence arm`](#command-olivares-eventing-fence-arm) | Require every writer to prove it carries the egress gate |
| [`olivares eventing fence status`](#command-olivares-eventing-fence-status) | Report the writer fence's posture and whether the database enforces it |
| [`olivares eventing fence verify`](#command-olivares-eventing-fence-verify) | Fail unless the database is actually enforcing an armed writer fence |
| [`olivares eventing subscriptions`](#command-olivares-eventing-subscriptions) | Manage event subscriptions (ls, get, create, update, rotate-secret, rm, test) |
| [`olivares eventing subscriptions create`](#command-olivares-eventing-subscriptions-create) | Create a new event subscription |
| [`olivares eventing subscriptions get`](#command-olivares-eventing-subscriptions-get) | Show one event subscription in full |
| [`olivares eventing subscriptions ls`](#command-olivares-eventing-subscriptions-ls) | List event subscriptions for a tenant |
| [`olivares eventing subscriptions rm`](#command-olivares-eventing-subscriptions-rm) | Delete an event subscription |
| [`olivares eventing subscriptions rotate-secret`](#command-olivares-eventing-subscriptions-rotate-secret) | Reissue the signing secret for one subscription (breaks delivery until the receiver is updated) |
| [`olivares eventing subscriptions test`](#command-olivares-eventing-subscriptions-test) | Send a test delivery to a subscription's endpoint |
| [`olivares eventing subscriptions update`](#command-olivares-eventing-subscriptions-update) | Edit one event subscription in place (never reissues the secret) |
| [`olivares findings`](#command-olivares-findings) | Export governed security findings |
| [`olivares findings export`](#command-olivares-findings-export) | Export all matching findings as SARIF 2.1.0 |
| [`olivares finops`](#command-olivares-finops) | Report AI spend and value, and govern budgets, rates and cost centers |
| [`olivares finops alerts`](#command-olivares-finops-alerts) | List budget threshold alerts |
| [`olivares finops budgets`](#command-olivares-finops-budgets) | Govern spend budgets and read their status |
| [`olivares finops budgets create`](#command-olivares-finops-budgets-create) | Create a budget |
| [`olivares finops budgets get`](#command-olivares-finops-budgets-get) | Show one budget |
| [`olivares finops budgets ls`](#command-olivares-finops-budgets-ls) | List budgets |
| [`olivares finops budgets rm`](#command-olivares-finops-budgets-rm) | Delete a budget |
| [`olivares finops budgets status`](#command-olivares-finops-budgets-status) | Show one budget's live status against its cap |
| [`olivares finops budgets update`](#command-olivares-finops-budgets-update) | Replace a budget |
| [`olivares finops comparison`](#command-olivares-finops-comparison) | Compare what a workload would cost on other models |
| [`olivares finops cost`](#command-olivares-finops-cost) | Record an observed cost sample |
| [`olivares finops cost ingest`](#command-olivares-finops-cost-ingest) | Record one observed cost sample |
| [`olivares finops cost-centers`](#command-olivares-finops-cost-centers) | Govern cost centers and the rules that map spend to them |
| [`olivares finops cost-centers create`](#command-olivares-finops-cost-centers-create) | Create a cost center |
| [`olivares finops cost-centers get`](#command-olivares-finops-cost-centers-get) | Show one cost center |
| [`olivares finops cost-centers ls`](#command-olivares-finops-cost-centers-ls) | List cost centers |
| [`olivares finops cost-centers mappings`](#command-olivares-finops-cost-centers-mappings) | Govern the rules that map spend onto one cost center |
| [`olivares finops cost-centers mappings add`](#command-olivares-finops-cost-centers-mappings-add) | Add a mapping rule to a cost center |
| [`olivares finops cost-centers mappings ls`](#command-olivares-finops-cost-centers-mappings-ls) | List one cost centre's mapping rules |
| [`olivares finops cost-centers mappings rm`](#command-olivares-finops-cost-centers-mappings-rm) | Remove a mapping rule from a cost center |
| [`olivares finops cost-centers rm`](#command-olivares-finops-cost-centers-rm) | Delete a cost center |
| [`olivares finops cost-centers update`](#command-olivares-finops-cost-centers-update) | Replace a cost center |
| [`olivares finops forecast`](#command-olivares-finops-forecast) | Forecast spend from the observed history |
| [`olivares finops outcomes`](#command-olivares-finops-outcomes) | Record and read business outcomes attributed to AI work |
| [`olivares finops outcomes ingest`](#command-olivares-finops-outcomes-ingest) | Record one business outcome |
| [`olivares finops outcomes ls`](#command-olivares-finops-outcomes-ls) | List recorded outcomes |
| [`olivares finops rates`](#command-olivares-finops-rates) | Govern the model rate catalog used to price usage |
| [`olivares finops rates create`](#command-olivares-finops-rates-create) | Add a model rate |
| [`olivares finops rates get`](#command-olivares-finops-rates-get) | Show one model rate |
| [`olivares finops rates ls`](#command-olivares-finops-rates-ls) | List model rates |
| [`olivares finops rates rm`](#command-olivares-finops-rates-rm) | Delete a model rate |
| [`olivares finops rates update`](#command-olivares-finops-rates-update) | Replace a model rate |
| [`olivares finops recommendations`](#command-olivares-finops-recommendations) | Show cost-reduction recommendations |
| [`olivares finops seats`](#command-olivares-finops-seats) | Record seat counts and read seat utilization |
| [`olivares finops seats ingest`](#command-olivares-finops-seats-ingest) | Record a provider's seat counts for a day |
| [`olivares finops seats utilization`](#command-olivares-finops-seats-utilization) | Show seat utilization |
| [`olivares finops spend`](#command-olivares-finops-spend) | Report observed AI spend over a window |
| [`olivares finops spend allocation`](#command-olivares-finops-spend-allocation) | Show how spend allocates to cost centers |
| [`olivares finops spend export`](#command-olivares-finops-spend-export) | Export spend in the FOCUS interchange format |
| [`olivares finops spend ls`](#command-olivares-finops-spend-ls) | Show the spend series for a window |
| [`olivares finops spend reconciliation`](#command-olivares-finops-spend-reconciliation) | Compare observed spend against provider-reported cost |
| [`olivares finops spend summary`](#command-olivares-finops-spend-summary) | Show the spend summary for a window |
| [`olivares finops spend trend`](#command-olivares-finops-spend-trend) | Show the spend trend over a window |
| [`olivares finops spend unified`](#command-olivares-finops-spend-unified) | Show the unified cross-source spend view |
| [`olivares finops statements`](#command-olivares-finops-statements) | Generate, read and export per-cost-center statements |
| [`olivares finops statements export`](#command-olivares-finops-statements-export) | Export one statement |
| [`olivares finops statements generate`](#command-olivares-finops-statements-generate) | Generate statements for a period |
| [`olivares finops statements get`](#command-olivares-finops-statements-get) | Show one statement with its lines |
| [`olivares finops statements ls`](#command-olivares-finops-statements-ls) | List generated statements |
| [`olivares finops team-summary`](#command-olivares-finops-team-summary) | Show the per-team spend summary |
| [`olivares finops value`](#command-olivares-finops-value) | Report the value side of the unit economics |
| [`olivares finops value ls`](#command-olivares-finops-value-ls) | Show the value series for a window |
| [`olivares finops value summary`](#command-olivares-finops-value-summary) | Show the value summary and cost-per-outcome |
| [`olivares firstparty-bins`](#command-olivares-firstparty-bins) | List the first-party connector plugins embedded in this binary (diagnostic) _(hidden)_ |
| [`olivares governance`](#command-olivares-governance) | Inspect the governance plane: what is stopped, and why |
| [`olivares governance approvals`](#command-olivares-governance-approvals) | The approval queue: what is waiting on a human, and who decided what |
| [`olivares governance approvals decisions`](#command-olivares-governance-approvals-decisions) | Who voted which way on one approval, and why |
| [`olivares governance approvals get`](#command-olivares-governance-approvals-get) | Show one approval |
| [`olivares governance approvals ls`](#command-olivares-governance-approvals-ls) | List approvals, pending and decided |
| [`olivares governance breakglass`](#command-olivares-governance-breakglass) | Emergency access grants: who has one, until when, and what they did with it |
| [`olivares governance breakglass get`](#command-olivares-governance-breakglass-get) | Show one break-glass grant |
| [`olivares governance breakglass ls`](#command-olivares-governance-breakglass-ls) | List break-glass grants, live and expired |
| [`olivares governance breakglass uses`](#command-olivares-governance-breakglass-uses) | Every action actually taken under one grant |
| [`olivares governance guardian`](#command-olivares-governance-guardian) | The rules that act on findings without a human, and what they have done |
| [`olivares governance guardian actions`](#command-olivares-governance-guardian-actions) | What guardian actually did, rule by rule |
| [`olivares governance guardian rules`](#command-olivares-governance-guardian-rules) | List the guardian rules and whether each is armed |
| [`olivares governance killswitch`](#command-olivares-governance-killswitch) | The estate-wide and per-scope stops that deny work while they are active |
| [`olivares governance killswitch ls`](#command-olivares-governance-killswitch-ls) | List kill switches, active and historical |
| [`olivares governance killswitch state`](#command-olivares-governance-killswitch-state) | Whether the estate is stopped, and every kill switch active right now |
| [`olivares governance nhi`](#command-olivares-governance-nhi) | Non-human identities: ownership, rotation age and what is already being refused |
| [`olivares governance nhi events`](#command-olivares-governance-nhi-events) | The lifecycle events recorded for one identity |
| [`olivares governance nhi get`](#command-olivares-governance-nhi-get) | One non-human identity, in full |
| [`olivares governance nhi ls`](#command-olivares-governance-nhi-ls) | List the non-human identities |
| [`olivares governance nhi posture`](#command-olivares-governance-nhi-posture) | The estate-wide identity posture in one screen |
| [`olivares governance pdp`](#command-olivares-governance-pdp) | The policy decision point: which revision is actually deciding, and is it in force |
| [`olivares governance pdp active`](#command-olivares-governance-pdp-active) | Which policy this process is deciding with, and whether it is fully in force |
| [`olivares governance pdp get-version`](#command-olivares-governance-pdp-get-version) | One stored revision, with the policy document itself |
| [`olivares governance pdp tests`](#command-olivares-governance-pdp-tests) | The stored test results for a policy revision |
| [`olivares governance pdp versions`](#command-olivares-governance-pdp-versions) | Every stored policy revision, both surfaces, metadata only |
| [`olivares governance rbac`](#command-olivares-governance-rbac) | Who can do what: the grant vocabulary, the custom roles and the scoped grants |
| [`olivares governance rbac catalog`](#command-olivares-governance-rbac-catalog) | The vocabulary a grant can be built from |
| [`olivares governance rbac delegation-authority`](#command-olivares-governance-rbac-delegation-authority) | What the calling principal may delegate, and where |
| [`olivares governance rbac grants`](#command-olivares-governance-rbac-grants) | The scoped grants in force: who holds what, where |
| [`olivares governance rbac grants get`](#command-olivares-governance-rbac-grants-get) | One scoped grant |
| [`olivares governance rbac grants ls`](#command-olivares-governance-rbac-grants-ls) | List every scoped grant |
| [`olivares governance rbac permission-groups`](#command-olivares-governance-rbac-permission-groups) | Named bundles of permissions that roles reuse |
| [`olivares governance rbac permission-groups get`](#command-olivares-governance-rbac-permission-groups-get) | One permission group, with its members |
| [`olivares governance rbac permission-groups ls`](#command-olivares-governance-rbac-permission-groups-ls) | List the permission groups |
| [`olivares governance rbac roles`](#command-olivares-governance-rbac-roles) | Custom roles: what each one grants, and what it takes away |
| [`olivares governance rbac roles get`](#command-olivares-governance-rbac-roles-get) | One custom role, with its full permission set |
| [`olivares governance rbac roles ls`](#command-olivares-governance-rbac-roles-ls) | List the custom roles |
| [`olivares grok-hook`](#command-olivares-grok-hook) | Governed PEP hook client for Grok Build: forward a Grok hook to the control plane and relay the decision (deny-closed) |
| [`olivares health`](#command-olivares-health) | Watch subject health, incidents, SLA and dependencies |
| [`olivares health checks`](#command-olivares-health-checks) | Declare, inspect, probe and retire health checks |
| [`olivares health checks create`](#command-olivares-health-checks-create) | Declare a new monitored subject |
| [`olivares health checks get`](#command-olivares-health-checks-get) | Show one check |
| [`olivares health checks ls`](#command-olivares-health-checks-ls) | List declared checks |
| [`olivares health checks report`](#command-olivares-health-checks-report) | Post a probe result against a check |
| [`olivares health checks rm`](#command-olivares-health-checks-rm) | Delete a check (admin-tier) |
| [`olivares health checks update`](#command-olivares-health-checks-update) | Change a check's configuration |
| [`olivares health dependencies`](#command-olivares-health-dependencies) | Show the observed dependency graph |
| [`olivares health events`](#command-olivares-health-events) | List the append-only reliability transition ledger |
| [`olivares health incidents`](#command-olivares-health-incidents) | List, open and resolve health incidents |
| [`olivares health incidents get`](#command-olivares-health-incidents-get) | Show one incident |
| [`olivares health incidents ls`](#command-olivares-health-incidents-ls) | List health incidents |
| [`olivares health incidents resolve`](#command-olivares-health-incidents-resolve) | Declare an incident resolved |
| [`olivares health sla`](#command-olivares-health-sla) | Report observed uptime for one subject against its target |
| [`olivares health status`](#command-olivares-health-status) | Show the current health of every monitored subject |
| [`olivares health watch`](#command-olivares-health-watch) | Follow health changes as they happen (one JSON object per line) |
| [`olivares help`](#command-olivares-help) | Help about any command |
| [`olivares hookpep`](#command-olivares-hookpep) | Author and inspect PDP policy through the control plane |
| [`olivares hookpep dry-run`](#command-olivares-hookpep-dry-run) | Evaluate a request against a candidate policy without publishing it |
| [`olivares hookpep explain`](#command-olivares-hookpep-explain) | Explain a request decision against a candidate policy without publishing it |
| [`olivares hookpep publish`](#command-olivares-hookpep-publish) | Compile, publish, and activate an authored policy revision |
| [`olivares hookpep rollback`](#command-olivares-hookpep-rollback) | Re-activate a prior immutable policy revision |
| [`olivares hookpep tests`](#command-olivares-hookpep-tests) | Show the stored compile-validation artifact for a policy revision |
| [`olivares hookpep validate`](#command-olivares-hookpep-validate) | Compile and validate a candidate policy without publishing it |
| [`olivares hookpep versions`](#command-olivares-hookpep-versions) | List immutable authored policy revisions |
| [`olivares hooks`](#command-olivares-hooks) | Hooks-hardening add-on: fleet deployed-verified attestation + conformance cert (enterprise) _(hidden)_ |
| [`olivares hooks attest`](#command-olivares-hooks-attest) | Attest a fleet's deployed managed-settings against the canonical PEP-hook bundle (deployed-verified) |
| [`olivares hooks conform`](#command-olivares-hooks-conform) | Certify conformance of the managed-settings + PEP hook against the real claude binary |
| [`olivares identity`](#command-olivares-identity) | Read federation, SSO, customer-managed key and residency posture |
| [`olivares identity external-keys`](#command-olivares-identity-external-keys) | List the customer-managed encryption key inventory |
| [`olivares identity residency`](#command-olivares-identity-residency) | List each workspace's data-residency and CMEK posture |
| [`olivares identity sso`](#command-olivares-identity-sso) | Report the SSO connection state |
| [`olivares identity wif`](#command-olivares-identity-wif) | Show the workload-identity federation graph |
| [`olivares inference-proxy`](#command-olivares-inference-proxy) | Govern the inference gateway: gates, DLP rules and device grants |
| [`olivares inference-proxy config`](#command-olivares-inference-proxy-config) | Read and replace the gateway's gate configuration |
| [`olivares inference-proxy config get`](#command-olivares-inference-proxy-config-get) | Show the gateway's effective gate configuration |
| [`olivares inference-proxy config set`](#command-olivares-inference-proxy-config-set) | Replace the gateway's gate configuration |
| [`olivares inference-proxy device`](#command-olivares-inference-proxy-device) | Approve or deny a pending device grant |
| [`olivares inference-proxy device approve`](#command-olivares-inference-proxy-device-approve) | Resolve a pending device grant by its user code |
| [`olivares inference-proxy dlp`](#command-olivares-inference-proxy-dlp) | Govern the per-class DLP rules applied to inference egress |
| [`olivares inference-proxy dlp ls`](#command-olivares-inference-proxy-dlp-ls) | List the effective DLP rules |
| [`olivares inference-proxy dlp rm`](#command-olivares-inference-proxy-dlp-rm) | Remove a DLP override and restore its secure default |
| [`olivares inference-proxy dlp set`](#command-olivares-inference-proxy-dlp-set) | Set the action for one DLP class |
| [`olivares inventory`](#command-olivares-inventory) | List the observed entity catalog and its coverage summary |
| [`olivares inventory entities`](#command-olivares-inventory-entities) | List and open catalog entities |
| [`olivares inventory entities get`](#command-olivares-inventory-entities-get) | Show one catalog entity and the core entity it overlays |
| [`olivares inventory entities ls`](#command-olivares-inventory-entities-ls) | List catalog entities |
| [`olivares inventory summary`](#command-olivares-inventory-summary) | Count catalog entities by kind and by signal source |
| [`olivares keys`](#command-olivares-keys) | Key custody (BYOK/HYOK/CMEK): seal, rotate and inspect signing keys |
| [`olivares keys rewrap`](#command-olivares-keys-rewrap) | Re-seal an envelope under the KEK's CURRENT version/primary (KEK rotation; the sealed key does not change) |
| [`olivares keys rotate`](#command-olivares-keys-rotate) | Mint a NEW signing key sealed under the KEK, preserving the prior public keys as verifiable history |
| [`olivares keys seal`](#command-olivares-keys-seal) | Seal an operator config file (its secrets at rest only exist KEK-wrapped) |
| [`olivares keys status`](#command-olivares-keys-status) | Show the key-custody posture (declared vs configured, envelopes, FIPS mode) |
| [`olivares keys unseal`](#command-olivares-keys-unseal) | Open a sealed operator config to STDOUT (debugging; never writes plaintext to disk) |
| [`olivares keys wrap`](#command-olivares-keys-wrap) | Seal a signing key into a CMEK envelope (mint a new key, or migrate an existing plaintext key file) |
| [`olivares knowledge`](#command-olivares-knowledge) | Govern knowledge bases, data products, memory and DLP |
| [`olivares knowledge context-policies`](#command-olivares-knowledge-context-policies) | Read and set context/compaction policies |
| [`olivares knowledge context-policies ls`](#command-olivares-knowledge-context-policies-ls) | List context policies |
| [`olivares knowledge context-policies put`](#command-olivares-knowledge-context-policies-put) | Create or replace a context policy |
| [`olivares knowledge data-products`](#command-olivares-knowledge-data-products) | Govern data products and their versioned contracts |
| [`olivares knowledge data-products archive`](#command-olivares-knowledge-data-products-archive) | Archive a data product |
| [`olivares knowledge data-products contracts`](#command-olivares-knowledge-data-products-contracts) | Read and add a data product's versioned contracts |
| [`olivares knowledge data-products contracts active`](#command-olivares-knowledge-data-products-contracts-active) | Show the contract version currently in force |
| [`olivares knowledge data-products contracts add`](#command-olivares-knowledge-data-products-contracts-add) | Add a new contract version to a data product |
| [`olivares knowledge data-products contracts get`](#command-olivares-knowledge-data-products-contracts-get) | Show one contract version |
| [`olivares knowledge data-products contracts ls`](#command-olivares-knowledge-data-products-contracts-ls) | List a data product's contract versions |
| [`olivares knowledge data-products create`](#command-olivares-knowledge-data-products-create) | Declare a data product |
| [`olivares knowledge data-products deprecate`](#command-olivares-knowledge-data-products-deprecate) | Deprecate a data product |
| [`olivares knowledge data-products events`](#command-olivares-knowledge-data-products-events) | List a data product's enforcement events |
| [`olivares knowledge data-products get`](#command-olivares-knowledge-data-products-get) | Show one data product |
| [`olivares knowledge data-products health`](#command-olivares-knowledge-data-products-health) | Report a data product's freshness and quality |
| [`olivares knowledge data-products ls`](#command-olivares-knowledge-data-products-ls) | List data products |
| [`olivares knowledge data-products publish`](#command-olivares-knowledge-data-products-publish) | Publish a data product so its contract governs the corpus |
| [`olivares knowledge data-products rm`](#command-olivares-knowledge-data-products-rm) | Delete a data product |
| [`olivares knowledge data-products set`](#command-olivares-knowledge-data-products-set) | Update a data product's authored fields |
| [`olivares knowledge data-products validate`](#command-olivares-knowledge-data-products-validate) | Validate a payload against the product's active contract |
| [`olivares knowledge dlp`](#command-olivares-knowledge-dlp) | Read and set the DLP egress rules |
| [`olivares knowledge dlp ls`](#command-olivares-knowledge-dlp-ls) | List the DLP egress rules |
| [`olivares knowledge dlp put`](#command-olivares-knowledge-dlp-put) | Create or replace one DLP rule |
| [`olivares knowledge dlp rm`](#command-olivares-knowledge-dlp-rm) | Delete one DLP rule |
| [`olivares knowledge documents`](#command-olivares-knowledge-documents) | Inspect an individual knowledge document |
| [`olivares knowledge documents get`](#command-olivares-knowledge-documents-get) | Show one knowledge document |
| [`olivares knowledge kbs`](#command-olivares-knowledge-kbs) | Declare, inspect and operate knowledge bases |
| [`olivares knowledge kbs create`](#command-olivares-knowledge-kbs-create) | Declare a knowledge base |
| [`olivares knowledge kbs documents`](#command-olivares-knowledge-kbs-documents) | List a knowledge base's documents |
| [`olivares knowledge kbs get`](#command-olivares-knowledge-kbs-get) | Show one knowledge base |
| [`olivares knowledge kbs ingest`](#command-olivares-knowledge-kbs-ingest) | Ingest documents into a knowledge base |
| [`olivares knowledge kbs ls`](#command-olivares-knowledge-kbs-ls) | List the tenant's knowledge bases |
| [`olivares knowledge kbs query`](#command-olivares-knowledge-kbs-query) | Run a governed retrieval against a knowledge base |
| [`olivares knowledge kbs reindex`](#command-olivares-knowledge-kbs-reindex) | Embed and index the knowledge base's pending chunks |
| [`olivares knowledge kbs rm`](#command-olivares-knowledge-kbs-rm) | Delete a knowledge base and cascade its documents |
| [`olivares knowledge kbs scan`](#command-olivares-knowledge-kbs-scan) | Run PII discovery over a knowledge base |
| [`olivares knowledge kbs set`](#command-olivares-knowledge-kbs-set) | Replace a knowledge base's authored fields |
| [`olivares knowledge kbs sync`](#command-olivares-knowledge-kbs-sync) | Delta-sync a knowledge base from its content source |
| [`olivares knowledge labels`](#command-olivares-knowledge-labels) | Read the sensitivity labels PII discovery wrote |
| [`olivares knowledge labels ls`](#command-olivares-knowledge-labels-ls) | List sensitivity labels |
| [`olivares knowledge lineage`](#command-olivares-knowledge-lineage) | Read the append-only retrieval lineage |
| [`olivares knowledge lineage get`](#command-olivares-knowledge-lineage-get) | Show one lineage record |
| [`olivares knowledge lineage ls`](#command-olivares-knowledge-lineage-ls) | List retrieval lineage records |
| [`olivares knowledge memory`](#command-olivares-knowledge-memory) | Govern agent memory: read, write, verify, export and purge |
| [`olivares knowledge memory all`](#command-olivares-knowledge-memory-all) | List every memory entry (admin-tier cross-scope view) |
| [`olivares knowledge memory export`](#command-olivares-knowledge-memory-export) | Export a signed, portable memory bundle |
| [`olivares knowledge memory get`](#command-olivares-knowledge-memory-get) | Show one memory entry |
| [`olivares knowledge memory import`](#command-olivares-knowledge-memory-import) | Import a signed portability bundle |
| [`olivares knowledge memory ls`](#command-olivares-knowledge-memory-ls) | List memory entries visible in the declared scope |
| [`olivares knowledge memory purge`](#command-olivares-knowledge-memory-purge) | Purge expired memory entries |
| [`olivares knowledge memory put`](#command-olivares-knowledge-memory-put) | Write one governed memory entry |
| [`olivares knowledge memory rm`](#command-olivares-knowledge-memory-rm) | Delete one memory entry |
| [`olivares knowledge memory verify`](#command-olivares-knowledge-memory-verify) | Verify memory integrity against the ledger anchor |
| [`olivares knowledge prompts`](#command-olivares-knowledge-prompts) | Manage the versioned prompt registry |
| [`olivares knowledge prompts create`](#command-olivares-knowledge-prompts-create) | Register a prompt and its first revision |
| [`olivares knowledge prompts get`](#command-olivares-knowledge-prompts-get) | Show one prompt |
| [`olivares knowledge prompts ls`](#command-olivares-knowledge-prompts-ls) | List registered prompts |
| [`olivares knowledge prompts revisions`](#command-olivares-knowledge-prompts-revisions) | List, read and append immutable prompt revisions |
| [`olivares knowledge prompts revisions add`](#command-olivares-knowledge-prompts-revisions-add) | Append an immutable revision to a prompt |
| [`olivares knowledge prompts revisions get`](#command-olivares-knowledge-prompts-revisions-get) | Show one prompt revision |
| [`olivares knowledge prompts revisions ls`](#command-olivares-knowledge-prompts-revisions-ls) | List a prompt's revisions |
| [`olivares knowledge prompts rollback`](#command-olivares-knowledge-prompts-rollback) | Point a prompt at an earlier revision |
| [`olivares knowledge scans`](#command-olivares-knowledge-scans) | Read the append-only PII scan evidence |
| [`olivares knowledge scans ls`](#command-olivares-knowledge-scans-ls) | List PII scan runs |
| [`olivares knowledge sources`](#command-olivares-knowledge-sources) | Run discovery over a registered content source |
| [`olivares knowledge sources scan`](#command-olivares-knowledge-sources-scan) | Scan a content source for personal data without ingesting |
| [`olivares license`](#command-olivares-license) | Manage commercial licenses (install/uninstall/status + keygen/sign/verify; offline Ed25519, never a feature gate) |
| [`olivares license install`](#command-olivares-license-install) | Install a license into the data dir (verify + persist; apply live with SIGHUP / runtime reload) |
| [`olivares license keygen`](#command-olivares-license-keygen) | Generate one Ed25519 keypair for a license or OTA trust domain |
| [`olivares license sign`](#command-olivares-license-sign) | Sign a license (requires --key in a release build; uses the dev key only in dev/test builds) |
| [`olivares license status`](#command-olivares-license-status) | Show the installed license and its status (offline; resolves --license &gt; env &gt; data-dir) |
| [`olivares license uninstall`](#command-olivares-license-uninstall) | Remove the installed license from the data dir (the offline half of DELETE /v1/console/license) |
| [`olivares license verify`](#command-olivares-license-verify) | Verify a license against a public key (default: embedded key), with profile/grace and optional CRL status |
| [`olivares mcp`](#command-olivares-mcp) | Govern Model Context Protocol resources |
| [`olivares mcp pins`](#command-olivares-mcp-pins) | List and manage approved MCP tool fingerprints |
| [`olivares mcp pins approve`](#command-olivares-mcp-pins-approve) | Approve an explicit or currently drifted tool fingerprint |
| [`olivares mcp pins ls`](#command-olivares-mcp-pins-ls) | List approved MCP tool fingerprints and current drift |
| [`olivares mcp pins rm`](#command-olivares-mcp-pins-rm) | Remove an approved MCP tool fingerprint |
| [`olivares members`](#command-olivares-members) | List a tenant's member roster and grant accounts a role in it |
| [`olivares members grant`](#command-olivares-members-grant) | Grant an existing account a role in a tenant |
| [`olivares members invites`](#command-olivares-members-invites) | List and revoke the tenant's pending invitations |
| [`olivares members invites ls`](#command-olivares-members-invites-ls) | List the tenant's pending, unexpired invitations |
| [`olivares members invites revoke`](#command-olivares-members-invites-revoke) | Revoke a pending invitation |
| [`olivares members ls`](#command-olivares-members-ls) | List the resolved tenant's member roster |
| [`olivares migrate`](#command-olivares-migrate) | Inspect the engine's schema-migration state (read-only) |
| [`olivares migrate manifest`](#command-olivares-migrate-manifest) | Print this binary's registered schema manifest (deterministic; the open≡enterprise parity oracle) |
| [`olivares migrate status`](#command-olivares-migrate-status) | List applied schema migrations and their expand/contract phase (read-only) |
| [`olivares models`](#command-olivares-models) | Govern the model estate, routing, registry and model access |
| [`olivares models access`](#command-olivares-models-access) | Author model-access grants (who may use which model) |
| [`olivares models access create`](#command-olivares-models-access-create) | Create a model-access grant |
| [`olivares models access ls`](#command-olivares-models-access-ls) | List model-access grants |
| [`olivares models access rm`](#command-olivares-models-access-rm) | Delete a model-access grant |
| [`olivares models access update`](#command-olivares-models-access-update) | Replace a model-access grant |
| [`olivares models admission`](#command-olivares-models-admission) | Govern the signed-model admission trust root and read its verdicts |
| [`olivares models admission ls`](#command-olivares-models-admission-ls) | List recorded admission verdicts |
| [`olivares models admission policy`](#command-olivares-models-admission-policy) | Show the admission trust root |
| [`olivares models admission set-policy`](#command-olivares-models-admission-set-policy) | Replace the admission trust root |
| [`olivares models agent-artifacts`](#command-olivares-models-agent-artifacts) | Govern the agent-artifact supply chain |
| [`olivares models agent-artifacts aibom`](#command-olivares-models-agent-artifacts-aibom) | Generate the agent-supply-chain BOM |
| [`olivares models agent-artifacts create`](#command-olivares-models-agent-artifacts-create) | Register an agent artifact |
| [`olivares models agent-artifacts ls`](#command-olivares-models-agent-artifacts-ls) | List governed agent artifacts |
| [`olivares models agent-artifacts rm`](#command-olivares-models-agent-artifacts-rm) | Remove an agent artifact |
| [`olivares models agent-artifacts seal`](#command-olivares-models-agent-artifacts-seal) | Seal the agent-supply-chain BOM to the ledger |
| [`olivares models agent-artifacts seals`](#command-olivares-models-agent-artifacts-seals) | List agent-supply-chain BOM seals |
| [`olivares models aibom`](#command-olivares-models-aibom) | Generate, seal and list AI bills of materials |
| [`olivares models aibom card`](#command-olivares-models-aibom-card) | Render the model card for one owned model |
| [`olivares models aibom get`](#command-olivares-models-aibom-get) | Generate the AIBOM for one owned model |
| [`olivares models aibom ls`](#command-olivares-models-aibom-ls) | List AIBOM seals |
| [`olivares models aibom seal`](#command-olivares-models-aibom-seal) | Seal the current AIBOM to the ledger as evidence |
| [`olivares models catalog`](#command-olivares-models-catalog) | Show the declared reference catalog (capabilities and list pricing) |
| [`olivares models data-governance`](#command-olivares-models-data-governance) | Show the context-management / memory / ZDR matrix |
| [`olivares models datasets`](#command-olivares-models-datasets) | Govern dataset lineage components |
| [`olivares models datasets create`](#command-olivares-models-datasets-create) | Register a dataset |
| [`olivares models datasets ls`](#command-olivares-models-datasets-ls) | List governed datasets |
| [`olivares models datasets rm`](#command-olivares-models-datasets-rm) | Remove a dataset |
| [`olivares models deployments`](#command-olivares-models-deployments) | Govern local inference deployments |
| [`olivares models deployments create`](#command-olivares-models-deployments-create) | Register an inference deployment |
| [`olivares models deployments ls`](#command-olivares-models-deployments-ls) | List inference deployments |
| [`olivares models deployments rm`](#command-olivares-models-deployments-rm) | Remove an inference deployment |
| [`olivares models deployments update`](#command-olivares-models-deployments-update) | Replace an inference deployment |
| [`olivares models entitlements`](#command-olivares-models-entitlements) | Attest provider entitlement state for restricted access tiers |
| [`olivares models entitlements ls`](#command-olivares-models-entitlements-ls) | List access-tier entitlement attestations |
| [`olivares models entitlements set`](#command-olivares-models-entitlements-set) | Attest the entitlement state of one access tier |
| [`olivares models features`](#command-olivares-models-features) | Show which model families declare each API capability |
| [`olivares models finetune`](#command-olivares-models-finetune) | Record fine-tune jobs and their outcome |
| [`olivares models finetune create`](#command-olivares-models-finetune-create) | Record a fine-tune job |
| [`olivares models finetune get`](#command-olivares-models-finetune-get) | Show one fine-tune job record |
| [`olivares models finetune ls`](#command-olivares-models-finetune-ls) | List fine-tune job records |
| [`olivares models finetune update`](#command-olivares-models-finetune-update) | Replace a fine-tune job record |
| [`olivares models get`](#command-olivares-models-get) | Show one governed model |
| [`olivares models gpai`](#command-olivares-models-gpai) | Attest per-provider GPAI compliance posture |
| [`olivares models gpai attest`](#command-olivares-models-gpai-attest) | Attest one provider's GPAI posture |
| [`olivares models gpai ls`](#command-olivares-models-gpai-ls) | List attested GPAI posture per provider |
| [`olivares models groups`](#command-olivares-models-groups) | Author named model groups |
| [`olivares models groups create`](#command-olivares-models-groups-create) | Create a model group |
| [`olivares models groups get`](#command-olivares-models-groups-get) | Show one model group |
| [`olivares models groups ls`](#command-olivares-models-groups-ls) | List model groups |
| [`olivares models groups rm`](#command-olivares-models-groups-rm) | Delete a model group |
| [`olivares models groups update`](#command-olivares-models-groups-update) | Replace a model group |
| [`olivares models keys`](#command-olivares-models-keys) | Govern provider API-key and workspace references |
| [`olivares models keys create`](#command-olivares-models-keys-create) | Register a provider key or workspace reference |
| [`olivares models keys ls`](#command-olivares-models-keys-ls) | List provider key and workspace references |
| [`olivares models keys rm`](#command-olivares-models-keys-rm) | Remove a key or workspace reference |
| [`olivares models keys update`](#command-olivares-models-keys-update) | Replace a key or workspace reference |
| [`olivares models ls`](#command-olivares-models-ls) | List the governed model estate |
| [`olivares models owned`](#command-olivares-models-owned) | Govern the own-model registry |
| [`olivares models owned create`](#command-olivares-models-owned-create) | Register an owned model |
| [`olivares models owned get`](#command-olivares-models-owned-get) | Show one owned model |
| [`olivares models owned ls`](#command-olivares-models-owned-ls) | List owned models |
| [`olivares models owned rm`](#command-olivares-models-owned-rm) | Remove an owned model from the registry |
| [`olivares models owned update`](#command-olivares-models-owned-update) | Replace an owned-model entry |
| [`olivares models platforms`](#command-olivares-models-platforms) | Show the deployment-surface matrix and per-platform lifecycle |
| [`olivares models rate-limits`](#command-olivares-models-rate-limits) | Show the provider rate-limit inventory a gateway must mirror |
| [`olivares models residency`](#command-olivares-models-residency) | Govern per-workspace inference-geo residency |
| [`olivares models residency ls`](#command-olivares-models-residency-ls) | List per-workspace residency records |
| [`olivares models residency set`](#command-olivares-models-residency-set) | Declare a workspace's permitted inference geographies |
| [`olivares models routing`](#command-olivares-models-routing) | Author routing policies and resolve or execute them |
| [`olivares models routing create`](#command-olivares-models-routing-create) | Create a routing policy |
| [`olivares models routing execute`](#command-olivares-models-routing-execute) | Execute a routing policy through the governed executor (SPENDS) |
| [`olivares models routing get`](#command-olivares-models-routing-get) | Show one routing policy |
| [`olivares models routing ls`](#command-olivares-models-routing-ls) | List routing policies |
| [`olivares models routing resolve`](#command-olivares-models-routing-resolve) | Resolve a policy to the routing decision it would produce |
| [`olivares models routing rm`](#command-olivares-models-routing-rm) | Delete a routing policy |
| [`olivares models routing update`](#command-olivares-models-routing-update) | Replace a routing policy in place |
| [`olivares models tool-types`](#command-olivares-models-tool-types) | Show the dated tool-type catalog and its cost cross-walk |
| [`olivares models versions`](#command-olivares-models-versions) | Govern owned-model versions and their signed admission |
| [`olivares models versions admit`](#command-olivares-models-versions-admit) | Run the signed-model admission ceremony against a version |
| [`olivares models versions create`](#command-olivares-models-versions-create) | Register an owned-model version |
| [`olivares models versions ls`](#command-olivares-models-versions-ls) | List owned-model versions |
| [`olivares models versions rm`](#command-olivares-models-versions-rm) | Remove an owned-model version |
| [`olivares notify`](#command-olivares-notify) | Author notification routes and inspect deliveries and the outbox |
| [`olivares notify deliveries`](#command-olivares-notify-deliveries) | List the append-only delivery ledger |
| [`olivares notify destinations`](#command-olivares-notify-destinations) | List the destinations THIS tenant may address |
| [`olivares notify evaluate`](#command-olivares-notify-evaluate) | Ask which routes a signal WOULD select, delivering nothing |
| [`olivares notify match-types`](#command-olivares-notify-match-types) | List the event types a route may match |
| [`olivares notify outbox`](#command-olivares-notify-outbox) | Inspect the durable outbox and requeue terminal rows |
| [`olivares notify outbox ls`](#command-olivares-notify-outbox-ls) | List durable outbox rows |
| [`olivares notify outbox redeliver`](#command-olivares-notify-outbox-redeliver) | Requeue a terminal outbox row for another delivery attempt (admin-tier) |
| [`olivares notify routes`](#command-olivares-notify-routes) | Author, inspect, test and roll back notification routes |
| [`olivares notify routes create`](#command-olivares-notify-routes-create) | Declare a notification route |
| [`olivares notify routes get`](#command-olivares-notify-routes-get) | Show one route's full predicate |
| [`olivares notify routes ls`](#command-olivares-notify-routes-ls) | List notification routes |
| [`olivares notify routes restore`](#command-olivares-notify-routes-restore) | Put a route back to an earlier revision |
| [`olivares notify routes revisions`](#command-olivares-notify-routes-revisions) | List a route's revision ledger |
| [`olivares notify routes rm`](#command-olivares-notify-routes-rm) | Delete a route (admin-tier) |
| [`olivares notify routes test`](#command-olivares-notify-routes-test) | Send a REAL test notification through a route (admin-tier) |
| [`olivares notify routes update`](#command-olivares-notify-routes-update) | Replace a route's predicate |
| [`olivares observability`](#command-olivares-observability) | Inspect ingestion health, ledger traces and binary attestation |
| [`olivares observability attestation`](#command-olivares-observability-attestation) | Show the measured attestation of the running binary |
| [`olivares observability ingestion-health`](#command-olivares-observability-ingestion-health) | Report per-standard and per-source telemetry ingestion |
| [`olivares observability traces`](#command-olivares-observability-traces) | List, open and export ledger-derived traces |
| [`olivares observability traces export`](#command-olivares-observability-traces-export) | Export one trace as OTLP-compatible JSON |
| [`olivares observability traces get`](#command-olivares-observability-traces-get) | Show one trace's spans |
| [`olivares observability traces ls`](#command-olivares-observability-traces-ls) | List correlated traces |
| [`olivares openapi`](#command-olivares-openapi) | Print an OpenAPI 3.1 document (stable core, or --beta module routes) for client codegen |
| [`olivares orchestration`](#command-olivares-orchestration) | Inspect the agent communication graph and operate governed schedules and workflows |
| [`olivares orchestration decisions`](#command-olivares-orchestration-decisions) | List the append-only fire/miss decision ledger for the tenant |
| [`olivares orchestration flows`](#command-olivares-orchestration-flows) | List the derived multi-agent flows and their lifecycle state |
| [`olivares orchestration graph`](#command-olivares-orchestration-graph) | List the live agent→agent relations (a privileged, self-audited read) |
| [`olivares orchestration neighbors`](#command-olivares-orchestration-neighbors) | Show the subgraph around one agent (incoming, outgoing or both) |
| [`olivares orchestration schedules`](#command-olivares-orchestration-schedules) | Declare, retarget and fire governed schedules |
| [`olivares orchestration schedules create`](#command-olivares-orchestration-schedules-create) | Declare a governed schedule |
| [`olivares orchestration schedules decisions`](#command-olivares-orchestration-schedules-decisions) | List one schedule's append-only fire/miss ledger |
| [`olivares orchestration schedules fire`](#command-olivares-orchestration-schedules-fire) | Fire a schedule now, through the approval gate (two-phase) |
| [`olivares orchestration schedules get`](#command-olivares-orchestration-schedules-get) | Show one schedule |
| [`olivares orchestration schedules ls`](#command-olivares-orchestration-schedules-ls) | List the tenant's governed schedules with their derived health |
| [`olivares orchestration schedules restore`](#command-olivares-orchestration-schedules-restore) | Re-apply an earlier revision of a schedule |
| [`olivares orchestration schedules revisions`](#command-olivares-orchestration-schedules-revisions) | List a schedule's revision history |
| [`olivares orchestration schedules update`](#command-olivares-orchestration-schedules-update) | Partially update a schedule — only the flags you type are sent |
| [`olivares orchestration stream`](#command-olivares-orchestration-stream) | Follow the live communication graph as NDJSON (one object per event) |
| [`olivares orchestration timeline`](#command-olivares-orchestration-timeline) | Show one subject's merged delegation and fire/miss history |
| [`olivares orchestration workflows`](#command-olivares-orchestration-workflows) | Author, dry-run and execute DAG workflows |
| [`olivares orchestration workflows create`](#command-olivares-orchestration-workflows-create) | Declare a workflow from a JSON step graph |
| [`olivares orchestration workflows dry-run`](#command-olivares-orchestration-workflows-dry-run) | Resolve and validate a workflow without executing a single step |
| [`olivares orchestration workflows get`](#command-olivares-orchestration-workflows-get) | Show one workflow with its full step graph |
| [`olivares orchestration workflows ls`](#command-olivares-orchestration-workflows-ls) | List the tenant's workflows |
| [`olivares orchestration workflows restore`](#command-olivares-orchestration-workflows-restore) | Re-apply an earlier revision of a workflow |
| [`olivares orchestration workflows revisions`](#command-olivares-orchestration-workflows-revisions) | List a workflow's revision history |
| [`olivares orchestration workflows run`](#command-olivares-orchestration-workflows-run) | Execute a workflow through the approval gate (two-phase) |
| [`olivares orchestration workflows runs`](#command-olivares-orchestration-workflows-runs) | Inspect a workflow's runs |
| [`olivares orchestration workflows runs get`](#command-olivares-orchestration-workflows-runs-get) | Show one run's step timeline |
| [`olivares orchestration workflows runs ls`](#command-olivares-orchestration-workflows-runs-ls) | List one workflow's runs, newest first |
| [`olivares orchestration workflows set-steps`](#command-olivares-orchestration-workflows-set-steps) | Replace a workflow's whole step graph (PUT — one unit, one hash) |
| [`olivares orchestration workflows update`](#command-olivares-orchestration-workflows-update) | Partially update a workflow's metadata — only the flags you type are sent |
| [`olivares posture`](#command-olivares-posture) | Export the tenant's governance posture as one document |
| [`olivares posture export`](#command-olivares-posture-export) | Export inventory, drift and findings as one posture document |
| [`olivares quickstart`](#command-olivares-quickstart) | Start Olivares AI for the first time — secure by default, one command to the console |
| [`olivares quickstart governed-rag`](#command-olivares-quickstart-governed-rag) | Prepare live governed data for Claude Code (S3/Drive -&gt; semantic KB -&gt; MCP retrieval) |
| [`olivares recording`](#command-olivares-recording) | Read the session-recording trail, verify its chain and set the recording policy |
| [`olivares recording ack`](#command-olivares-recording-ack) | Acknowledge the recording notice for this caller |
| [`olivares recording config`](#command-olivares-recording-config) | Read and replace the tenant's recording policy |
| [`olivares recording config get`](#command-olivares-recording-config-get) | Show the tenant's recording policy |
| [`olivares recording config set`](#command-olivares-recording-config-set) | Replace the tenant's recording policy (PUT — the whole policy) |
| [`olivares recording notice`](#command-olivares-recording-notice) | Show what is recorded for this caller, and whether consent is required |
| [`olivares recording sessions`](#command-olivares-recording-sessions) | List, verify, export and seal recorded sessions |
| [`olivares recording sessions export`](#command-olivares-recording-sessions-export) | Export one session as evidence (json or summary) |
| [`olivares recording sessions get`](#command-olivares-recording-sessions-get) | Show one recorded session |
| [`olivares recording sessions ls`](#command-olivares-recording-sessions-ls) | List recorded sessions |
| [`olivares recording sessions replay`](#command-olivares-recording-sessions-replay) | Reconstruct one session's frames and ledger window |
| [`olivares recording sessions seal`](#command-olivares-recording-sessions-seal) | Close one active session explicitly |
| [`olivares recording sessions summarize`](#command-olivares-recording-sessions-summarize) | Produce the derived reviewer summary of a sealed session |
| [`olivares recording sessions unified`](#command-olivares-recording-sessions-unified) | Show one session's frames and audit timeline merged |
| [`olivares recording sessions verify`](#command-olivares-recording-sessions-verify) | Verify a session's hash chain — exit 7 when it does not verify |
| [`olivares recording sweep`](#command-olivares-recording-sweep) | Seal every idle active session (the lazy-seal safety net) |
| [`olivares redteam`](#command-olivares-redteam) | Run the consent-gated adversarial battery against your own agents |
| [`olivares redteam catalog`](#command-olivares-redteam-catalog) | List the probe battery and its OWASP/ATLAS coverage |
| [`olivares redteam runs`](#command-olivares-redteam-runs) | Launch and inspect scored red-team runs |
| [`olivares redteam runs get`](#command-olivares-redteam-runs-get) | Show one run's scorecard |
| [`olivares redteam runs launch`](#command-olivares-redteam-runs-launch) | Run the battery against an authorized target |
| [`olivares redteam runs ls`](#command-olivares-redteam-runs-ls) | List red-team runs and their scores |
| [`olivares redteam runs results`](#command-olivares-redteam-runs-results) | List one run's per-probe results |
| [`olivares redteam targets`](#command-olivares-redteam-targets) | Register agents as red-team targets and grant or withdraw consent |
| [`olivares redteam targets authorize`](#command-olivares-redteam-targets-authorize) | Consent to red-teaming this target (confirmed; needs --yes when unattended) |
| [`olivares redteam targets get`](#command-olivares-redteam-targets-get) | Show one target and its consent record |
| [`olivares redteam targets ls`](#command-olivares-redteam-targets-ls) | List registered red-team targets and their consent state |
| [`olivares redteam targets register`](#command-olivares-redteam-targets-register) | Register an agent from your inventory as a red-team target |
| [`olivares redteam targets revoke`](#command-olivares-redteam-targets-revoke) | Withdraw consent to red-team this target |
| [`olivares release`](#command-olivares-release) | Release/OTA tooling (manifest generation) — ops use _(hidden)_ |
| [`olivares release export-mirror`](#command-olivares-release-export-mirror) | Mirror the entitled manifest and artifacts from the licensed gate into an air-gap bundle |
| [`olivares release manifest`](#command-olivares-release-manifest) | Build (and optionally sign) a per-channel OTA update manifest from a release directory |
| [`olivares release sign-manifest`](#command-olivares-release-sign-manifest) | Sign an existing OTA manifest during the off-box release ceremony |
| [`olivares release verify-channel-advance`](#command-olivares-release-verify-channel-advance) | Refuse a channel publication that would not move the LIVE channel forward (CFG-06 monotonicity fence) |
| [`olivares release verify-manifest`](#command-olivares-release-verify-manifest) | Cross-check an OTA manifest against the cosign-verified checksums.txt (and, with --dir, the published bytes) |
| [`olivares reporting`](#command-olivares-reporting) | Generate reports and manage schedules, branding and templates |
| [`olivares reporting branding`](#command-olivares-reporting-branding) | Read and set the tenant's report branding |
| [`olivares reporting branding get`](#command-olivares-reporting-branding-get) | Show the tenant's report branding |
| [`olivares reporting branding set`](#command-olivares-reporting-branding-set) | Replace the tenant's report branding |
| [`olivares reporting enterprise`](#command-olivares-reporting-enterprise) | Read the enterprise posture, risk and evidence-bundle reports |
| [`olivares reporting enterprise bundle`](#command-olivares-reporting-enterprise-bundle) | Enterprise evidence bundle |
| [`olivares reporting enterprise posture`](#command-olivares-reporting-enterprise-posture) | Enterprise governance posture report |
| [`olivares reporting enterprise risk`](#command-olivares-reporting-enterprise-risk) | Enterprise risk report |
| [`olivares reporting reports`](#command-olivares-reporting-reports) | List the report catalog and generate a report |
| [`olivares reporting reports get`](#command-olivares-reporting-reports-get) | Generate one report and write it to a file |
| [`olivares reporting reports ls`](#command-olivares-reporting-reports-ls) | List the reports this build can generate |
| [`olivares reporting schedules`](#command-olivares-reporting-schedules) | Manage scheduled reports and read their runs |
| [`olivares reporting schedules create`](#command-olivares-reporting-schedules-create) | Schedule a report on a cron cadence |
| [`olivares reporting schedules ls`](#command-olivares-reporting-schedules-ls) | List report schedules |
| [`olivares reporting schedules rm`](#command-olivares-reporting-schedules-rm) | Delete a report schedule |
| [`olivares reporting schedules run`](#command-olivares-reporting-schedules-run) | Fetch one run's stored report artifact |
| [`olivares reporting schedules runs`](#command-olivares-reporting-schedules-runs) | List a schedule's executions |
| [`olivares reporting templates`](#command-olivares-reporting-templates) | Read, store and remove custom report templates |
| [`olivares reporting templates get`](#command-olivares-reporting-templates-get) | Fetch the custom template stored for one report type |
| [`olivares reporting templates rm`](#command-olivares-reporting-templates-rm) | Remove the custom template for one report type |
| [`olivares reporting templates set`](#command-olivares-reporting-templates-set) | Store a custom HTML template for one report type |
| [`olivares sandbox`](#command-olivares-sandbox) | Run agents against synthetic scenarios and compare two variants |
| [`olivares sandbox compare`](#command-olivares-sandbox-compare) | Run the same scenario as two variants and record the verdict |
| [`olivares sandbox comparisons`](#command-olivares-sandbox-comparisons) | Inspect the append-only A/B comparison ledger |
| [`olivares sandbox comparisons get`](#command-olivares-sandbox-comparisons-get) | Show one comparison |
| [`olivares sandbox comparisons ls`](#command-olivares-sandbox-comparisons-ls) | List recorded comparisons |
| [`olivares sandbox replay`](#command-olivares-sandbox-replay) | Deterministically re-execute a recorded session against supplied mocks |
| [`olivares sandbox runs`](#command-olivares-sandbox-runs) | Inspect sandbox runs, their outputs and their live stream |
| [`olivares sandbox runs get`](#command-olivares-sandbox-runs-get) | Show one run |
| [`olivares sandbox runs ls`](#command-olivares-sandbox-runs-ls) | List sandbox runs |
| [`olivares sandbox runs outputs`](#command-olivares-sandbox-runs-outputs) | List one run's per-step outputs |
| [`olivares sandbox runs stream`](#command-olivares-sandbox-runs-stream) | Follow a live run as NDJSON (one object per event) |
| [`olivares sandbox scenarios`](#command-olivares-sandbox-scenarios) | Author, inspect, run and archive sandbox scenarios |
| [`olivares sandbox scenarios archive`](#command-olivares-sandbox-scenarios-archive) | Archive a scenario (destructive; needs --yes when unattended) |
| [`olivares sandbox scenarios create`](#command-olivares-sandbox-scenarios-create) | Author a scenario from JSON step and mock files |
| [`olivares sandbox scenarios get`](#command-olivares-sandbox-scenarios-get) | Show one scenario with its steps and mocks |
| [`olivares sandbox scenarios ls`](#command-olivares-sandbox-scenarios-ls) | List the tenant's scenarios |
| [`olivares sandbox scenarios run`](#command-olivares-sandbox-scenarios-run) | Run a scenario against the isolated runner (synchronous) |
| [`olivares secrets`](#command-olivares-secrets) | Manage the runtime secret store (sealed; referenced from configs as store:&lt;name&gt;) |
| [`olivares secrets ls`](#command-olivares-secrets-ls) | List stored secrets (names and non-secret hints; never the value) |
| [`olivares secrets put`](#command-olivares-secrets-put) | Create or update a secret (seals the value at rest) |
| [`olivares secrets rm`](#command-olivares-secrets-rm) | Delete a secret (a reference to it then fails closed) |
| [`olivares secrets rotate`](#command-olivares-secrets-rotate) | Replace a secret's value (a new value is required) |
| [`olivares security`](#command-olivares-security) | Security self-checks (advisory feed verification and affected-version reporting) |
| [`olivares security advisories`](#command-olivares-security-advisories) | Build and sign an OSV advisory feed the product self-checks — PSIRT use _(hidden)_ |
| [`olivares security check`](#command-olivares-security-check) | Check a product version against a signed advisories feed |
| [`olivares security drill`](#command-olivares-security-drill) | Timed end-to-end PSIRT advisory-pipeline drill |
| [`olivares security rulepack`](#command-olivares-security-rulepack) | Author/verify signed hot-reload security rule-packs (deny-lists, MCP blocks, patterns) |
| [`olivares security rulepack sign`](#command-olivares-security-rulepack-sign) | Build and sign a rule-pack from a draft (writes &lt;out&gt; + &lt;out&gt;.sig) _(hidden)_ |
| [`olivares security rulepack verify`](#command-olivares-security-rulepack-verify) | Verify a signed rule-pack against a trusted key and print its summary |
| [`olivares serve`](#command-olivares-serve) | Run the engine (REST + gRPC + embedded console), TLS-on-by-default |
| [`olivares setup`](#command-olivares-setup) | Guided, validated first-run configuration (profiles, Postgres onboarding, no SQL by hand) |
| [`olivares sources`](#command-olivares-sources) | Manage the durable source roster (connectors the engine ingests from) |
| [`olivares sources get`](#command-olivares-sources-get) | Show one source's definition, including the config `ls` cannot render |
| [`olivares sources ls`](#command-olivares-sources-ls) | List the source roster (name, kind, tenant, mode, poll, enabled) |
| [`olivares sources plan`](#command-olivares-sources-plan) | Show what a `sources set` with these flags WOULD change — no source is written or opened |
| [`olivares sources rm`](#command-olivares-sources-rm) | Delete a source from the roster |
| [`olivares sources set`](#command-olivares-sources-set) | Create or update a source (only the flags you pass are changed on an existing source) |
| [`olivares sources test`](#command-olivares-sources-test) | Open the source for real to prove it answers, then close it — nothing is wired or written |
| [`olivares sources validate`](#command-olivares-sources-validate) | Check a source definition is coherent by itself — offline, no network, no writes |
| [`olivares sourcescope`](#command-olivares-sourcescope) | Decide which sources a workspace or agent may reach |
| [`olivares sourcescope assignments`](#command-olivares-sourcescope-assignments) | Assign global connectors to workspaces |
| [`olivares sourcescope assignments create`](#command-olivares-sourcescope-assignments-create) | Assign a connector to a workspace |
| [`olivares sourcescope assignments get`](#command-olivares-sourcescope-assignments-get) | Show one assignment |
| [`olivares sourcescope assignments ls`](#command-olivares-sourcescope-assignments-ls) | List connector-to-workspace assignments |
| [`olivares sourcescope assignments rm`](#command-olivares-sourcescope-assignments-rm) | Delete an assignment |
| [`olivares sourcescope assignments set`](#command-olivares-sourcescope-assignments-set) | Replace an assignment |
| [`olivares sourcescope bindings`](#command-olivares-sourcescope-bindings) | Confine a source to a workspace or agent group |
| [`olivares sourcescope bindings create`](#command-olivares-sourcescope-bindings-create) | Bind a source to a scope |
| [`olivares sourcescope bindings get`](#command-olivares-sourcescope-bindings-get) | Show one binding |
| [`olivares sourcescope bindings ls`](#command-olivares-sourcescope-bindings-ls) | List source-to-scope bindings |
| [`olivares sourcescope bindings rm`](#command-olivares-sourcescope-bindings-rm) | Delete a binding |
| [`olivares sourcescope bindings set`](#command-olivares-sourcescope-bindings-set) | Replace a binding |
| [`olivares sourcescope guard-postures`](#command-olivares-sourcescope-guard-postures) | Read and set the retrieval guard posture |
| [`olivares sourcescope guard-postures ls`](#command-olivares-sourcescope-guard-postures-ls) | List explicit guard-posture overrides |
| [`olivares sourcescope guard-postures set`](#command-olivares-sourcescope-guard-postures-set) | Set the guard posture of one source |
| [`olivares sourcescope posture-requests`](#command-olivares-sourcescope-posture-requests) | Review the dual-control queue of proposed relaxations |
| [`olivares sourcescope posture-requests approve`](#command-olivares-sourcescope-posture-requests-approve) | Approve a pending relaxation and apply it |
| [`olivares sourcescope posture-requests get`](#command-olivares-sourcescope-posture-requests-get) | Show one posture-change request |
| [`olivares sourcescope posture-requests ls`](#command-olivares-sourcescope-posture-requests-ls) | List posture-change requests |
| [`olivares sourcescope posture-requests reject`](#command-olivares-sourcescope-posture-requests-reject) | Reject a pending relaxation, changing nothing |
| [`olivares sourcescope resolve`](#command-olivares-sourcescope-resolve) | Preview what one actor would resolve for one source |
| [`olivares sourcescope resources`](#command-olivares-sourcescope-resources) | Navigate the tenant's resource tree |
| [`olivares sourcescope resources ls`](#command-olivares-sourcescope-resources-ls) | List resources, by children or by subtree |
| [`olivares sourcescope sources`](#command-olivares-sourcescope-sources) | Source-wide posture operations |
| [`olivares sourcescope sources disable-scoping`](#command-olivares-sourcescope-sources-disable-scoping) | Propose removing ALL scoping from a source |
| [`olivares sourcescope workspace-connectors`](#command-olivares-sourcescope-workspace-connectors) | Manage connectors that belong to one workspace |
| [`olivares sourcescope workspace-connectors create`](#command-olivares-sourcescope-workspace-connectors-create) | Declare a workspace connector |
| [`olivares sourcescope workspace-connectors get`](#command-olivares-sourcescope-workspace-connectors-get) | Show one workspace connector |
| [`olivares sourcescope workspace-connectors ls`](#command-olivares-sourcescope-workspace-connectors-ls) | List workspace connectors |
| [`olivares sourcescope workspace-connectors rm`](#command-olivares-sourcescope-workspace-connectors-rm) | Delete a workspace connector |
| [`olivares sourcescope workspace-connectors set`](#command-olivares-sourcescope-workspace-connectors-set) | Replace a workspace connector |
| [`olivares status`](#command-olivares-status) | Show the engine public status, including knowledge retrieval posture |
| [`olivares superadmin`](#command-olivares-superadmin) | Enable/disable internal superadmin accounts (never deletes) |
| [`olivares superadmin disable`](#command-olivares-superadmin-disable) | Disable an internal superadmin (marks it inactive and revokes its sessions/tokens; never deletes) |
| [`olivares superadmin enable`](#command-olivares-superadmin-enable) | Re-enable a previously disabled internal superadmin |
| [`olivares superadmin status`](#command-olivares-superadmin-status) | List internal superadmin accounts and their active/inactive status |
| [`olivares support`](#command-olivares-support) | Collect redacted diagnostics for support and incident response |
| [`olivares support bundle`](#command-olivares-support-bundle) | Build a redacted diagnostic tarball with an integrity manifest |
| [`olivares tenants`](#command-olivares-tenants) | Create, list, suspend and delete tenants (superadmin) |
| [`olivares tenants create`](#command-olivares-tenants-create) | Create a tenant |
| [`olivares tenants ls`](#command-olivares-tenants-ls) | List the tenants this installation serves |
| [`olivares tenants rm`](#command-olivares-tenants-rm) | Delete a tenant and everything in it — unrecoverable |
| [`olivares tenants set-region`](#command-olivares-tenants-set-region) | Pin or clear a tenant's data-residency region (requires an AAL3 session) |
| [`olivares tenants set-status`](#command-olivares-tenants-set-status) | Withdraw or restore a tenant's service without deleting anything |
| [`olivares threatintel`](#command-olivares-threatintel) | Manage the AI threat-intel catalog and its signed catalog releases (enterprise add-on) _(hidden)_ |
| [`olivares threatintel apply`](#command-olivares-threatintel-apply) | Verify and apply a signed catalog release (fail-closed, anti-rollback); persists it for the engine |
| [`olivares threatintel pull`](#command-olivares-threatintel-pull) | Pull the catalog release from the configured endpoint, then verify and apply it (fail-closed) |
| [`olivares threatintel sign`](#command-olivares-threatintel-sign) | Sign an unsigned catalog envelope (publisher side; key minted with `olivares license keygen`) |
| [`olivares threatintel status`](#command-olivares-threatintel-status) | Show the active catalog release (versions, expiry, channels) and the governance crosswalk summary |
| [`olivares threatintel verify`](#command-olivares-threatintel-verify) | Verify a signed catalog release (signature + expiry + schema); does not apply it |
| [`olivares tokens`](#command-olivares-tokens) | Issue, list, rotate and revoke API tokens (the credential a script authenticates with) |
| [`olivares tokens issue`](#command-olivares-tokens-issue) | Issue an API token and print its secret ONCE |
| [`olivares tokens ls`](#command-olivares-tokens-ls) | List the API tokens the caller may see |
| [`olivares tokens revoke`](#command-olivares-tokens-revoke) | Revoke an API token |
| [`olivares tokens rotate`](#command-olivares-tokens-rotate) | Rotate an API token: issue a replacement with the same spec and revoke the old one |
| [`olivares upgrade`](#command-olivares-upgrade) | Upgrade this binary in place to a newer signed release (verified, atomic, reversible) |
| [`olivares users`](#command-olivares-users) | List, create, disable and re-enable the global user accounts (superadmin) |
| [`olivares users create`](#command-olivares-users-create) | Create a global user account (superadmin) |
| [`olivares users disable`](#command-olivares-users-disable) | Disable a superadmin account (reversible; requires an AAL3 session) |
| [`olivares users enable`](#command-olivares-users-enable) | Re-enable a disabled superadmin account (requires an AAL3 session) |
| [`olivares users ls`](#command-olivares-users-ls) | List the global user accounts |
| [`olivares users superadmins`](#command-olivares-users-superadmins) | List the superadmin accounts and whether each is active |
| [`olivares version`](#command-olivares-version) | Print the olivares version, build metadata and FIPS 140-3 mode |
| [`olivares voice`](#command-olivares-voice) | Inspect governed voice sessions and set the per-agent voice policy |
| [`olivares voice decisions`](#command-olivares-voice-decisions) | List the append-only voice decision ledger for the tenant |
| [`olivares voice policies`](#command-olivares-voice-policies) | Read and replace the per-agent voice policy |
| [`olivares voice policies ls`](#command-olivares-voice-policies-ls) | List the voice policies in force |
| [`olivares voice policies set`](#command-olivares-voice-policies-set) | Replace one agent's voice policy (PUT — the whole policy) |
| [`olivares voice sessions`](#command-olivares-voice-sessions) | List, follow and open governed voice sessions |
| [`olivares voice sessions decisions`](#command-olivares-voice-sessions-decisions) | List one session's governance decisions |
| [`olivares voice sessions get`](#command-olivares-voice-sessions-get) | Show one voice session |
| [`olivares voice sessions ls`](#command-olivares-voice-sessions-ls) | List voice sessions with their derived state |
| [`olivares voice sessions open`](#command-olivares-voice-sessions-open) | Open a governed voice session through the approval gate (two-phase) |
| [`olivares voice sessions stream`](#command-olivares-voice-sessions-stream) | Follow one live voice session as NDJSON (one object per event) |
| [`olivares webui-files`](#command-olivares-webui-files) | List the web UI assets embedded in this binary (diagnostic) _(hidden)_ |
| [`olivares work`](#command-olivares-work) | Manage durable cross-session work, leases, decisions, and acceptance |
| [`olivares work apply`](#command-olivares-work-apply) | Apply one validated work command idempotently |
| [`olivares work get`](#command-olivares-work-get) | Get one durable work item, decision, or lease |
| [`olivares work list`](#command-olivares-work-list) | List durable work items, decisions, or leases with keyset pagination |
| [`olivares work plan`](#command-olivares-work-plan) | Plan one work command and its expected durable effects without writing |
| [`olivares work protocol-binding`](#command-olivares-work-protocol-binding) | Compose and reconcile durable A2A and MCP protocol bindings |
| [`olivares work protocol-binding binding`](#command-olivares-work-protocol-binding-binding) | Inspect and reconcile durable protocol bindings |
| [`olivares work protocol-binding binding get`](#command-olivares-work-protocol-binding-binding-get) | Get one durable protocol binding generation |
| [`olivares work protocol-binding binding list`](#command-olivares-work-protocol-binding-binding-list) | List durable protocol bindings in one workspace |
| [`olivares work protocol-binding binding reconcile`](#command-olivares-work-protocol-binding-binding-reconcile) | Validate, plan, test, or apply one exact-generation remote observation |
| [`olivares work protocol-binding spec`](#command-olivares-work-protocol-binding-spec) | Manage immutable protocol binding specifications |
| [`olivares work protocol-binding spec activate`](#command-olivares-work-protocol-binding-spec-activate) | Activate one protocol binding spec generation |
| [`olivares work protocol-binding spec create`](#command-olivares-work-protocol-binding-spec-create) | Validate, plan, or create one draft protocol binding spec |
| [`olivares work protocol-binding spec disable`](#command-olivares-work-protocol-binding-spec-disable) | Disable one protocol binding spec generation |
| [`olivares work protocol-binding spec get`](#command-olivares-work-protocol-binding-spec-get) | Get one immutable protocol binding spec generation |
| [`olivares work protocol-binding spec list`](#command-olivares-work-protocol-binding-spec-list) | List protocol binding spec generations in one workspace |
| [`olivares work replay`](#command-olivares-work-replay) | Replay a dead-lettered durable work event |
| [`olivares work replay event`](#command-olivares-work-replay-event) | Requeue one dead-lettered WorkEvent under its stable event ID |
| [`olivares work validate`](#command-olivares-work-validate) | Validate one work command without writing |
| [`olivares work watch`](#command-olivares-work-watch) | Watch the durable work-event stream from a resumable cursor |

### Command detail

#### Command: olivares

Olivares AI — self-hosted engine for enterprise AI

```
olivares
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `-o`, `--output` | `string` | `text` | **inherited**. global output format: text or json (report commands keep json unless -o is given) |

#### Command: olivares __extract

Hidden diagnostic: it does not appear in `--help` output and is not part of the supported surface.

internal: extract text from a rich document on stdin (sandboxed re-exec target)

```
olivares __extract
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--kind` | `string` | — | rich-document kind (ooxml) |

#### Command: olivares accessmap

Query the access graph, least-privilege drift and attack paths

```
olivares accessmap
```

Aliases: `access-map`

| Flag | Type | Default | Description |
|---|---|---|---|
| `--allow-cleartext` | `bool` | `false` | **inherited**. allow sending the credential to a non-loopback host over plain HTTP (DANGEROUS: it travels readable) |
| `--ca-cert` | `string` | — | **inherited**. PEM file containing an additional trusted root CA (default: current context) |
| `--insecure` | `bool` | `false` | **inherited**. skip TLS certificate verification (DANGEROUS; development only) |
| `--pin-sha256` | `stringArray` | `[]` | **inherited**. trusted leaf SPKI SHA-256 pin, base64 or hex, repeatable — the engine prints it as pin_sha256 on the line reporting its certificate (default: current context) |
| `--server` | `string` | — | **inherited**. control-plane base URL (default $OLIVARES_SERVER_URL, then current context) |
| `--tenant` | `string` | — | **inherited**. tenant id (default $OLIVARES_TENANT, then current context) |
| `--timeout` | `duration` | `10s` | **inherited**. request timeout |
| `--token` | `string` | — | **inherited**. API bearer token (prefer --token-file: this form is visible in the process table and in shell history; default $OLIVARES_TOKEN, then current context) |
| `--token-file` | `string` | — | **inherited**. read the API bearer token from a file, or - for stdin |

#### Command: olivares accessmap attack-paths

Reachability, privilege-escalation and exfiltration analyses

```
olivares accessmap attack-paths
```

Aliases: `attackpaths`

Declares no flags of its own; it takes those of [`olivares accessmap`](#command-olivares-accessmap) and the root command.

#### Command: olivares accessmap attack-paths escalation

List the privilege-escalation chains open to one agent

```
olivares accessmap attack-paths escalation
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--agent-id` | `string` | — | the agent to analyze (required) |

#### Command: olivares accessmap attack-paths exfil

List the exfiltration routes out of one resource

```
olivares accessmap attack-paths exfil
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--resource-id` | `string` | — | the resource to analyze (required) |

#### Command: olivares accessmap attack-paths reachability

List the resources one agent can reach

```
olivares accessmap attack-paths reachability
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--agent-id` | `string` | — | the agent to analyze (required) |

#### Command: olivares accessmap attack-paths summary

Show the estate-wide attack-surface counts

```
olivares accessmap attack-paths summary
```

Declares no flags of its own; it takes those of [`olivares accessmap attack-paths`](#command-olivares-accessmap-attack-paths) and the root command.

#### Command: olivares accessmap drift

Show permitted-vs-observed least-privilege drift

```
olivares accessmap drift
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--confidence` | `string` | — | filter by attribution confidence |
| `--cursor` | `string` | — | continue from the cursor printed by the previous page |
| `--limit` | `int` | `0` | maximum rows to return in one page (0 = the engine's default) |
| `--mode` | `string` | — | filter by access mode (r, rw) |
| `--origin-id` | `string` | — | filter by origin id |
| `--origin-kind` | `string` | — | filter by origin kind (agent, session, identity) |
| `--resource-id` | `string` | — | filter by resource id |
| `--signal-source` | `string` | — | filter by the signal that produced the edge |

#### Command: olivares accessmap graph

List the access graph as nodes and edges

```
olivares accessmap graph
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--confidence` | `string` | — | filter by attribution confidence |
| `--cursor` | `string` | — | continue from the cursor printed by the previous page |
| `--limit` | `int` | `0` | maximum rows to return in one page (0 = the engine's default) |
| `--mode` | `string` | — | filter by access mode (r, rw) |
| `--origin-id` | `string` | — | filter by origin id |
| `--origin-kind` | `string` | — | filter by origin kind (agent, session, identity) |
| `--resource-id` | `string` | — | filter by resource id |
| `--signal-source` | `string` | — | filter by the signal that produced the edge |

#### Command: olivares accessmap neighbors

List the edges touching one node

```
olivares accessmap neighbors
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--direction` | `string` | — | outgoing, incoming or both (default both) |
| `--id` | `string` | — | node id to expand (required) |
| `--kind` | `string` | — | node kind, when the id alone is ambiguous |

#### Command: olivares adoption

Report Claude adoption by org, team, trend and developer

```
olivares adoption
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--allow-cleartext` | `bool` | `false` | **inherited**. allow sending the credential to a non-loopback host over plain HTTP (DANGEROUS: it travels readable) |
| `--ca-cert` | `string` | — | **inherited**. PEM file containing an additional trusted root CA (default: current context) |
| `--insecure` | `bool` | `false` | **inherited**. skip TLS certificate verification (DANGEROUS; development only) |
| `--pin-sha256` | `stringArray` | `[]` | **inherited**. trusted leaf SPKI SHA-256 pin, base64 or hex, repeatable — the engine prints it as pin_sha256 on the line reporting its certificate (default: current context) |
| `--server` | `string` | — | **inherited**. control-plane base URL (default $OLIVARES_SERVER_URL, then current context) |
| `--tenant` | `string` | — | **inherited**. tenant id (default $OLIVARES_TENANT, then current context) |
| `--timeout` | `duration` | `10s` | **inherited**. request timeout |
| `--token` | `string` | — | **inherited**. API bearer token (prefer --token-file: this form is visible in the process table and in shell history; default $OLIVARES_TOKEN, then current context) |
| `--token-file` | `string` | — | **inherited**. read the API bearer token from a file, or - for stdin |

#### Command: olivares adoption developers

Break adoption down by developer (privileged: exposes identity)

```
olivares adoption developers
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--limit` | `int` | `0` | top-N rows (0 = the engine's default of 100 for this route). NOT a page size: this namespace has no cursor |
| `--since` | `string` | — | window start, RFC3339 (default: the engine's window) |
| `--until` | `string` | — | window end, RFC3339 (default: now) |

#### Command: olivares adoption discrepancy

Measure how far the two lenses disagree

```
olivares adoption discrepancy
```

Aliases: `discrepancies`

| Flag | Type | Default | Description |
|---|---|---|---|
| `--since` | `string` | — | window start, RFC3339 (default: the engine's window) |
| `--until` | `string` | — | window end, RFC3339 (default: now) |

#### Command: olivares adoption summary

Show both adoption lenses over one window

```
olivares adoption summary
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--limit` | `int` | `0` | top-N rows (0 = the engine's default of 10 for this route). NOT a page size: this namespace has no cursor |
| `--since` | `string` | — | window start, RFC3339 (default: the engine's window) |
| `--until` | `string` | — | window end, RFC3339 (default: now) |

#### Command: olivares adoption teams

Break adoption down by team

```
olivares adoption teams
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--since` | `string` | — | window start, RFC3339 (default: the engine's window) |
| `--until` | `string` | — | window end, RFC3339 (default: now) |

#### Command: olivares adoption trend

Show a per-day series for ONE lens

```
olivares adoption trend
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--lens` | `string` | — | analytics or telemetry (default analytics) |
| `--since` | `string` | — | window start, RFC3339 (default: the engine's window) |
| `--until` | `string` | — | window end, RFC3339 (default: now) |

#### Command: olivares agent

Operate governed Claude Code sessions (launch, attach, stop, resume, clean up)

```
olivares agent
```

Declares no flags of its own; it takes those of [`olivares`](#command-olivares) and the root command.

#### Command: olivares agent managed-settings

Render the Claude Code managed-settings.json that governs operated sessions (PEP hook)

```
olivares agent managed-settings
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--gateway-base-url` | `string` | — | managed ANTHROPIC_BASE_URL pin for the governed Olivares inference gateway |
| `--matcher` | `string` | — | tool-name matcher for the PEP hook ("" = all tools) |
| `--no-hook` | `bool` | `false` | render env/telemetry only, no PEP hook |
| `--otel-endpoint` | `string` | — | managed OTEL collector endpoint (enables the sanctioned telemetry env) |
| `--out` | `string` | `-` | output path ('-' = stdout) |
| `--pep-command` | `string` | `olivares claude-hook` | the managed PreToolUse PEP-client command (deny-closed: required unless --no-hook) |
| `--redact` | `bool` | `true` | also install the paired PostToolUse output-redaction hook |
| `--timeout` | `int` | `5` | PEP hook timeout in seconds (a hung control plane must fail fast, deny-closed) |

#### Command: olivares agent session

Manage the lifecycle of governed Claude Code sessions

```
olivares agent session
```

Declares no flags of its own; it takes those of [`olivares agent`](#command-olivares-agent) and the root command.

#### Command: olivares agent session attach

Stream a live session's I/O (server-sent events) to stdout

```
olivares agent session attach <run-ref>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--ca-cert` | `string` | — | PEM CA bundle used to verify the control plane (default: the active client context) |
| `--from` | `int64` | `0` | replay from this output sequence number |
| `--insecure` | `bool` | `false` | skip TLS certificate verification (self-signed dev planes only) |
| `--pin-sha256` | `stringArray` | `[]` | pinned leaf SPKI SHA-256, base64 or hex (repeatable) — the engine prints it as pin_sha256 on the line reporting its certificate; default: the active client context |
| `--server` | `string` | — | control-plane base URL (default $OLIVARES_SERVER_URL or the active client context) |
| `--tenant` | `string` | — | tenant id (default $OLIVARES_TENANT or the active client context) |
| `--timeout` | `duration` | `30s` | request timeout |
| `--token` | `string` | — | API bearer token (default $OLIVARES_TOKEN or the active client context) |

#### Command: olivares agent session cleanup

Release a stopped session (mark cleaned)

```
olivares agent session cleanup <run-ref>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--ca-cert` | `string` | — | PEM CA bundle used to verify the control plane (default: the active client context) |
| `--insecure` | `bool` | `false` | skip TLS certificate verification (self-signed dev planes only) |
| `--pin-sha256` | `stringArray` | `[]` | pinned leaf SPKI SHA-256, base64 or hex (repeatable) — the engine prints it as pin_sha256 on the line reporting its certificate; default: the active client context |
| `--server` | `string` | — | control-plane base URL (default $OLIVARES_SERVER_URL or the active client context) |
| `--tenant` | `string` | — | tenant id (default $OLIVARES_TENANT or the active client context) |
| `--timeout` | `duration` | `30s` | request timeout |
| `--token` | `string` | — | API bearer token (default $OLIVARES_TOKEN or the active client context) |

#### Command: olivares agent session create

Launch a governed Claude Code session

```
olivares agent session create
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--ca-cert` | `string` | — | PEM CA bundle used to verify the control plane (default: the active client context) |
| `--effort` | `string` | — | low\|medium\|high\|xhigh\|max |
| `--env-allow` | `stringSlice` | `[]` | host env var NAMES to forward to the session (allowlist; nothing else is inherited) |
| `--insecure` | `bool` | `false` | skip TLS certificate verification (self-signed dev planes only) |
| `--isolation` | `string` | `native` | native (the only runner wired this release) \| container \| sandbox — container and sandbox are accepted by the API but refused by the launcher until their runner ships |
| `--json` | `bool` | `false` | deprecated alias for -o json |
| `--model` | `string` | — | model alias (opus) or id (claude-opus-4-8) |
| `--name` | `string` | — | display name for the session |
| `--permission-mode` | `string` | `default` | default\|acceptEdits\|plan\|auto\|dontAsk\|bypassPermissions |
| `--pin-sha256` | `stringArray` | `[]` | pinned leaf SPKI SHA-256, base64 or hex (repeatable) — the engine prints it as pin_sha256 on the line reporting its certificate; default: the active client context |
| `--server` | `string` | — | control-plane base URL (default $OLIVARES_SERVER_URL or the active client context) |
| `--tenant` | `string` | — | tenant id (default $OLIVARES_TENANT or the active client context) |
| `--timeout` | `duration` | `30s` | request timeout |
| `--token` | `string` | — | API bearer token (default $OLIVARES_TOKEN or the active client context) |
| `--transport` | `string` | `stream-json` | transport: stream-json (governed) \| remote-control (lifecycle-only) |
| `--workspace` | `string` | — | workspace reference (the session's working directory) |

#### Command: olivares agent session events

Show a session's lifecycle ledger

```
olivares agent session events <run-ref>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--ca-cert` | `string` | — | PEM CA bundle used to verify the control plane (default: the active client context) |
| `--insecure` | `bool` | `false` | skip TLS certificate verification (self-signed dev planes only) |
| `--pin-sha256` | `stringArray` | `[]` | pinned leaf SPKI SHA-256, base64 or hex (repeatable) — the engine prints it as pin_sha256 on the line reporting its certificate; default: the active client context |
| `--server` | `string` | — | control-plane base URL (default $OLIVARES_SERVER_URL or the active client context) |
| `--tenant` | `string` | — | tenant id (default $OLIVARES_TENANT or the active client context) |
| `--timeout` | `duration` | `30s` | request timeout |
| `--token` | `string` | — | API bearer token (default $OLIVARES_TOKEN or the active client context) |

#### Command: olivares agent session get

Show one session

```
olivares agent session get <run-ref>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--ca-cert` | `string` | — | PEM CA bundle used to verify the control plane (default: the active client context) |
| `--insecure` | `bool` | `false` | skip TLS certificate verification (self-signed dev planes only) |
| `--pin-sha256` | `stringArray` | `[]` | pinned leaf SPKI SHA-256, base64 or hex (repeatable) — the engine prints it as pin_sha256 on the line reporting its certificate; default: the active client context |
| `--server` | `string` | — | control-plane base URL (default $OLIVARES_SERVER_URL or the active client context) |
| `--tenant` | `string` | — | tenant id (default $OLIVARES_TENANT or the active client context) |
| `--timeout` | `duration` | `30s` | request timeout |
| `--token` | `string` | — | API bearer token (default $OLIVARES_TOKEN or the active client context) |

#### Command: olivares agent session input

Send one NDJSON line to a live session's stdin ('-' or empty reads stdin)

```
olivares agent session input <run-ref>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--ca-cert` | `string` | — | PEM CA bundle used to verify the control plane (default: the active client context) |
| `--insecure` | `bool` | `false` | skip TLS certificate verification (self-signed dev planes only) |
| `--line` | `string` | — | the NDJSON message to write (default: read from stdin) |
| `--pin-sha256` | `stringArray` | `[]` | pinned leaf SPKI SHA-256, base64 or hex (repeatable) — the engine prints it as pin_sha256 on the line reporting its certificate; default: the active client context |
| `--server` | `string` | — | control-plane base URL (default $OLIVARES_SERVER_URL or the active client context) |
| `--tenant` | `string` | — | tenant id (default $OLIVARES_TENANT or the active client context) |
| `--timeout` | `duration` | `30s` | request timeout |
| `--token` | `string` | — | API bearer token (default $OLIVARES_TOKEN or the active client context) |

#### Command: olivares agent session ls

List operated sessions

```
olivares agent session ls
```

Aliases: `list`

| Flag | Type | Default | Description |
|---|---|---|---|
| `--ca-cert` | `string` | — | PEM CA bundle used to verify the control plane (default: the active client context) |
| `--insecure` | `bool` | `false` | skip TLS certificate verification (self-signed dev planes only) |
| `--json` | `bool` | `false` | deprecated alias for -o json |
| `--pin-sha256` | `stringArray` | `[]` | pinned leaf SPKI SHA-256, base64 or hex (repeatable) — the engine prints it as pin_sha256 on the line reporting its certificate; default: the active client context |
| `--server` | `string` | — | control-plane base URL (default $OLIVARES_SERVER_URL or the active client context) |
| `--state` | `string` | — | filter by state (pending\|running\|idle\|stopped\|failed\|cleaned) |
| `--tenant` | `string` | — | tenant id (default $OLIVARES_TENANT or the active client context) |
| `--timeout` | `duration` | `30s` | request timeout |
| `--token` | `string` | — | API bearer token (default $OLIVARES_TOKEN or the active client context) |

#### Command: olivares agent session resume

Resume a stopped session

```
olivares agent session resume <run-ref>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--ca-cert` | `string` | — | PEM CA bundle used to verify the control plane (default: the active client context) |
| `--insecure` | `bool` | `false` | skip TLS certificate verification (self-signed dev planes only) |
| `--pin-sha256` | `stringArray` | `[]` | pinned leaf SPKI SHA-256, base64 or hex (repeatable) — the engine prints it as pin_sha256 on the line reporting its certificate; default: the active client context |
| `--server` | `string` | — | control-plane base URL (default $OLIVARES_SERVER_URL or the active client context) |
| `--tenant` | `string` | — | tenant id (default $OLIVARES_TENANT or the active client context) |
| `--timeout` | `duration` | `30s` | request timeout |
| `--token` | `string` | — | API bearer token (default $OLIVARES_TOKEN or the active client context) |

#### Command: olivares agent session rm

Delete a cleaned session's record

```
olivares agent session rm <run-ref>
```

Aliases: `delete`, `remove`

| Flag | Type | Default | Description |
|---|---|---|---|
| `--ca-cert` | `string` | — | PEM CA bundle used to verify the control plane (default: the active client context) |
| `--insecure` | `bool` | `false` | skip TLS certificate verification (self-signed dev planes only) |
| `--pin-sha256` | `stringArray` | `[]` | pinned leaf SPKI SHA-256, base64 or hex (repeatable) — the engine prints it as pin_sha256 on the line reporting its certificate; default: the active client context |
| `--server` | `string` | — | control-plane base URL (default $OLIVARES_SERVER_URL or the active client context) |
| `--tenant` | `string` | — | tenant id (default $OLIVARES_TENANT or the active client context) |
| `--timeout` | `duration` | `30s` | request timeout |
| `--token` | `string` | — | API bearer token (default $OLIVARES_TOKEN or the active client context) |

#### Command: olivares agent session stop

Stop a running session

```
olivares agent session stop <run-ref>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--ca-cert` | `string` | — | PEM CA bundle used to verify the control plane (default: the active client context) |
| `--insecure` | `bool` | `false` | skip TLS certificate verification (self-signed dev planes only) |
| `--pin-sha256` | `stringArray` | `[]` | pinned leaf SPKI SHA-256, base64 or hex (repeatable) — the engine prints it as pin_sha256 on the line reporting its certificate; default: the active client context |
| `--server` | `string` | — | control-plane base URL (default $OLIVARES_SERVER_URL or the active client context) |
| `--tenant` | `string` | — | tenant id (default $OLIVARES_TENANT or the active client context) |
| `--timeout` | `duration` | `30s` | request timeout |
| `--token` | `string` | — | API bearer token (default $OLIVARES_TOKEN or the active client context) |

#### Command: olivares agent workspace

Manage governed workspaces and their files (browse/read/write/move/delete)

```
olivares agent workspace
```

Declares no flags of its own; it takes those of [`olivares agent`](#command-olivares-agent) and the root command.

#### Command: olivares agent workspace add

Register a host directory as a governed workspace

```
olivares agent workspace add <root-path>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--ca-cert` | `string` | — | PEM CA bundle used to verify the control plane (default: the active client context) |
| `--dlp` | `string` | `label` | DLP posture on reads: label\|deny\|off |
| `--insecure` | `bool` | `false` | skip TLS certificate verification (self-signed dev planes only) |
| `--json` | `bool` | `false` | deprecated alias for -o json |
| `--max-read` | `int64` | `0` | per-read size cap in bytes (0 = default 5 MiB) |
| `--mode` | `string` | `rw` | mount mode: rw\|ro |
| `--name` | `string` | — | display name |
| `--pin-sha256` | `stringArray` | `[]` | pinned leaf SPKI SHA-256, base64 or hex (repeatable) — the engine prints it as pin_sha256 on the line reporting its certificate; default: the active client context |
| `--server` | `string` | — | control-plane base URL (default $OLIVARES_SERVER_URL or the active client context) |
| `--subpath` | `stringSlice` | `[]` | restrict the file API to these relative subpaths (repeatable) |
| `--target` | `string` | `/workspace` | container mount target path |
| `--tenant` | `string` | — | tenant id (default $OLIVARES_TENANT or the active client context) |
| `--timeout` | `duration` | `30s` | request timeout |
| `--token` | `string` | — | API bearer token (default $OLIVARES_TOKEN or the active client context) |

#### Command: olivares agent workspace files

List one directory level in a workspace

```
olivares agent workspace files <ref>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--ca-cert` | `string` | — | PEM CA bundle used to verify the control plane (default: the active client context) |
| `--insecure` | `bool` | `false` | skip TLS certificate verification (self-signed dev planes only) |
| `--json` | `bool` | `false` | deprecated alias for -o json |
| `--path` | `string` | — | relative directory path (default: workspace root) |
| `--pin-sha256` | `stringArray` | `[]` | pinned leaf SPKI SHA-256, base64 or hex (repeatable) — the engine prints it as pin_sha256 on the line reporting its certificate; default: the active client context |
| `--server` | `string` | — | control-plane base URL (default $OLIVARES_SERVER_URL or the active client context) |
| `--tenant` | `string` | — | tenant id (default $OLIVARES_TENANT or the active client context) |
| `--timeout` | `duration` | `30s` | request timeout |
| `--token` | `string` | — | API bearer token (default $OLIVARES_TOKEN or the active client context) |

#### Command: olivares agent workspace get

Read a file's content to stdout (DLP-governed)

```
olivares agent workspace get <ref> <path>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--ca-cert` | `string` | — | PEM CA bundle used to verify the control plane (default: the active client context) |
| `--insecure` | `bool` | `false` | skip TLS certificate verification (self-signed dev planes only) |
| `--pin-sha256` | `stringArray` | `[]` | pinned leaf SPKI SHA-256, base64 or hex (repeatable) — the engine prints it as pin_sha256 on the line reporting its certificate; default: the active client context |
| `--server` | `string` | — | control-plane base URL (default $OLIVARES_SERVER_URL or the active client context) |
| `--tenant` | `string` | — | tenant id (default $OLIVARES_TENANT or the active client context) |
| `--timeout` | `duration` | `30s` | request timeout |
| `--token` | `string` | — | API bearer token (default $OLIVARES_TOKEN or the active client context) |

#### Command: olivares agent workspace ls

List registered workspaces

```
olivares agent workspace ls
```

Aliases: `list`

| Flag | Type | Default | Description |
|---|---|---|---|
| `--ca-cert` | `string` | — | PEM CA bundle used to verify the control plane (default: the active client context) |
| `--insecure` | `bool` | `false` | skip TLS certificate verification (self-signed dev planes only) |
| `--json` | `bool` | `false` | deprecated alias for -o json |
| `--pin-sha256` | `stringArray` | `[]` | pinned leaf SPKI SHA-256, base64 or hex (repeatable) — the engine prints it as pin_sha256 on the line reporting its certificate; default: the active client context |
| `--server` | `string` | — | control-plane base URL (default $OLIVARES_SERVER_URL or the active client context) |
| `--tenant` | `string` | — | tenant id (default $OLIVARES_TENANT or the active client context) |
| `--timeout` | `duration` | `30s` | request timeout |
| `--token` | `string` | — | API bearer token (default $OLIVARES_TOKEN or the active client context) |

#### Command: olivares agent workspace mkdir

Create a directory (and parents)

```
olivares agent workspace mkdir <ref> <path>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--ca-cert` | `string` | — | PEM CA bundle used to verify the control plane (default: the active client context) |
| `--insecure` | `bool` | `false` | skip TLS certificate verification (self-signed dev planes only) |
| `--pin-sha256` | `stringArray` | `[]` | pinned leaf SPKI SHA-256, base64 or hex (repeatable) — the engine prints it as pin_sha256 on the line reporting its certificate; default: the active client context |
| `--server` | `string` | — | control-plane base URL (default $OLIVARES_SERVER_URL or the active client context) |
| `--tenant` | `string` | — | tenant id (default $OLIVARES_TENANT or the active client context) |
| `--timeout` | `duration` | `30s` | request timeout |
| `--token` | `string` | — | API bearer token (default $OLIVARES_TOKEN or the active client context) |

#### Command: olivares agent workspace mv

Move/rename a path within the workspace

```
olivares agent workspace mv <ref> <from> <to>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--ca-cert` | `string` | — | PEM CA bundle used to verify the control plane (default: the active client context) |
| `--insecure` | `bool` | `false` | skip TLS certificate verification (self-signed dev planes only) |
| `--pin-sha256` | `stringArray` | `[]` | pinned leaf SPKI SHA-256, base64 or hex (repeatable) — the engine prints it as pin_sha256 on the line reporting its certificate; default: the active client context |
| `--server` | `string` | — | control-plane base URL (default $OLIVARES_SERVER_URL or the active client context) |
| `--tenant` | `string` | — | tenant id (default $OLIVARES_TENANT or the active client context) |
| `--timeout` | `duration` | `30s` | request timeout |
| `--token` | `string` | — | API bearer token (default $OLIVARES_TOKEN or the active client context) |

#### Command: olivares agent workspace put

Write a file from --from (a local file or '-' for stdin)

```
olivares agent workspace put <ref> <path>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--ca-cert` | `string` | — | PEM CA bundle used to verify the control plane (default: the active client context) |
| `--from` | `string` | `-` | source: a local file path, or '-' for stdin |
| `--insecure` | `bool` | `false` | skip TLS certificate verification (self-signed dev planes only) |
| `--pin-sha256` | `stringArray` | `[]` | pinned leaf SPKI SHA-256, base64 or hex (repeatable) — the engine prints it as pin_sha256 on the line reporting its certificate; default: the active client context |
| `--server` | `string` | — | control-plane base URL (default $OLIVARES_SERVER_URL or the active client context) |
| `--tenant` | `string` | — | tenant id (default $OLIVARES_TENANT or the active client context) |
| `--timeout` | `duration` | `30s` | request timeout |
| `--token` | `string` | — | API bearer token (default $OLIVARES_TOKEN or the active client context) |

#### Command: olivares agent workspace rm

Delete a file or (with --recursive) a directory subtree

```
olivares agent workspace rm <ref> <path>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--ca-cert` | `string` | — | PEM CA bundle used to verify the control plane (default: the active client context) |
| `--insecure` | `bool` | `false` | skip TLS certificate verification (self-signed dev planes only) |
| `--pin-sha256` | `stringArray` | `[]` | pinned leaf SPKI SHA-256, base64 or hex (repeatable) — the engine prints it as pin_sha256 on the line reporting its certificate; default: the active client context |
| `--recursive` | `bool` | `false` | delete a directory and its contents |
| `--server` | `string` | — | control-plane base URL (default $OLIVARES_SERVER_URL or the active client context) |
| `--tenant` | `string` | — | tenant id (default $OLIVARES_TENANT or the active client context) |
| `--timeout` | `duration` | `30s` | request timeout |
| `--token` | `string` | — | API bearer token (default $OLIVARES_TOKEN or the active client context) |
| `-y`, `--yes` | `bool` | `false` | proceed without the confirmation prompt (required in a non-interactive session) |

#### Command: olivares agent workspace rm-workspace

Deregister a workspace (does NOT delete host files)

```
olivares agent workspace rm-workspace <ref>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--ca-cert` | `string` | — | PEM CA bundle used to verify the control plane (default: the active client context) |
| `--insecure` | `bool` | `false` | skip TLS certificate verification (self-signed dev planes only) |
| `--pin-sha256` | `stringArray` | `[]` | pinned leaf SPKI SHA-256, base64 or hex (repeatable) — the engine prints it as pin_sha256 on the line reporting its certificate; default: the active client context |
| `--server` | `string` | — | control-plane base URL (default $OLIVARES_SERVER_URL or the active client context) |
| `--tenant` | `string` | — | tenant id (default $OLIVARES_TENANT or the active client context) |
| `--timeout` | `duration` | `30s` | request timeout |
| `--token` | `string` | — | API bearer token (default $OLIVARES_TOKEN or the active client context) |

#### Command: olivares agent workspace stat

Show metadata for one path

```
olivares agent workspace stat <ref> <path>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--ca-cert` | `string` | — | PEM CA bundle used to verify the control plane (default: the active client context) |
| `--insecure` | `bool` | `false` | skip TLS certificate verification (self-signed dev planes only) |
| `--pin-sha256` | `stringArray` | `[]` | pinned leaf SPKI SHA-256, base64 or hex (repeatable) — the engine prints it as pin_sha256 on the line reporting its certificate; default: the active client context |
| `--server` | `string` | — | control-plane base URL (default $OLIVARES_SERVER_URL or the active client context) |
| `--tenant` | `string` | — | tenant id (default $OLIVARES_TENANT or the active client context) |
| `--timeout` | `duration` | `30s` | request timeout |
| `--token` | `string` | — | API bearer token (default $OLIVARES_TOKEN or the active client context) |

#### Command: olivares audit

Inspect and checkpoint the evidence ledger

```
olivares audit
```

Declares no flags of its own; it takes those of [`olivares`](#command-olivares) and the root command.

#### Command: olivares audit archive

Export and verify the immutable ledger archive

```
olivares audit archive
```

Declares no flags of its own; it takes those of [`olivares audit`](#command-olivares-audit) and the root command.

#### Command: olivares audit archive export

Export a tenant's ledger as verifiable archive segments to a directory

```
olivares audit archive export
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--data-dir` | `string` | — | data directory (default $OLIVARES_DATA_DIR, an existing ./olivares-data, else $XDG_DATA_HOME/olivares or ~/.local/share/olivares) |
| `--dsn` | `string` | — | store DSN (default a SQLite file in the data dir) |
| `--engine` | `string` | `sqlite` | store engine: sqlite or postgres |
| `--from-seq` | `int64` | `1` | first sequence number to export (resume an earlier export at its last to_seq+1) |
| `--out` | `string` | — | **required**. output directory (files are written read-only; WORM when the substrate is) |
| `--segment-events` | `int` | `10000` | maximum events per segment |
| `--tenant` | `string` | — | tenant id to export (default $OLIVARES_TENANT) |

#### Command: olivares audit archive verify

Verify an exported archive directory offline (no store, no network)

```
olivares audit archive verify
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--dir` | `string` | — | **required**. archive directory to verify (the export's --out) |
| `--event-pubkey` | `stringArray` | `[]` | per-event Ed25519 public key pin, repeatable (raw base64), optionally epoch-FENCED as "&lt;base64&gt;@&lt;last_seq&gt;" (retired generation, valid only up to that sequence) or "&lt;base64&gt;@&lt;lo&gt;:&lt;hi&gt;" (explicit window); a bare key is the current generation. Pins REPLACE the archive's advisory keys.json — pin EVERY generation with its boundary (the audit.key.rotation marker's prior_last_seq) for the attacker-resistant fenced check; without a boundary a retired key is trusted for every sequence |
| `--pubkey` | `stringArray` | `[]` | checkpoint public key pin, repeatable: raw base64 Ed25519, or "&lt;alg&gt;:&lt;base64 DER SPKI&gt;" for an off-box key. Pins REPLACE the archive's advisory keys.json (docs/SECURITY-HARDENING.md §5) |
| `--pubkey-alg` | `string` | — | algorithm of a SINGLE bare --pubkey (compat form, as in `audit verify`) |
| `--strict` | `bool` | `false` | exit non-zero if the archive fails to verify; for on-call cron/CI. The default exits 0 and reports status only in the JSON |

#### Command: olivares audit checkpoint

Write a signed checkpoint (all tenants, or one with --tenant)

```
olivares audit checkpoint
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--data-dir` | `string` | — | data directory (default $OLIVARES_DATA_DIR, an existing ./olivares-data, else $XDG_DATA_HOME/olivares or ~/.local/share/olivares) |
| `--dsn` | `string` | — | store DSN (default a SQLite file in the data dir) |
| `--engine` | `string` | `sqlite` | store engine: sqlite or postgres |
| `--tenant` | `string` | — | tenant id (empty = all tenants) |

#### Command: olivares audit export

Export a tenant's ledger to a SIEM format (cef\|leef\|syslog\|otlp\|otlp_envelope\|otlp_log_record\|ocsf)

```
olivares audit export
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--data-dir` | `string` | — | data directory (default $OLIVARES_DATA_DIR, an existing ./olivares-data, else $XDG_DATA_HOME/olivares or ~/.local/share/olivares) |
| `--dsn` | `string` | — | store DSN (default a SQLite file in the data dir) |
| `--engine` | `string` | `sqlite` | store engine: sqlite or postgres |
| `--format` | `string` | `cef` | export format: cef\|leef\|syslog\|otlp\|otlp_envelope\|otlp_log_record\|ocsf (this selects the SIEM export format and is fully supported — it is not the deprecated -o/--output alias other commands spell the same way) |
| `--tenant` | `string` | — | tenant id to export (default $OLIVARES_TENANT) |

#### Command: olivares audit key-transition

Record the off-box-signed signing-key epoch boundary after `keys rotate`

```
olivares audit key-transition
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--admin-dsn` | `string` | — | Postgres only: DSN of the dedicated NOSUPERUSER BYPASSRLS role used for the cross-tenant org enumeration. Without it the default (every tenant) sweep CANNOT enumerate the estate and this command fails closed rather than fencing a short list; --tenant needs no enumeration and so needs no admin pool |
| `--data-dir` | `string` | — | data directory (default $OLIVARES_DATA_DIR, an existing ./olivares-data, else $XDG_DATA_HOME/olivares or ~/.local/share/olivares) |
| `--dsn` | `string` | — | store DSN (default a SQLite file in the data dir) |
| `--engine` | `string` | `sqlite` | store engine: sqlite or postgres |
| `--prior-pubkey` | `string` | — | retired key (raw base64 Ed25519); default: the most recent prior generation in the sealed envelope |
| `--tenant` | `string` | — | record only this tenant's boundary (default: every tenant + the system chain) |
| `--yes` | `bool` | `false` | skip the confirmation prompt |

#### Command: olivares audit observe-report

Summarize constrained-observe shadows for an observe→enforce promotion decision

```
olivares audit observe-report
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--data-dir` | `string` | — | data directory (default $OLIVARES_DATA_DIR, an existing ./olivares-data, else $XDG_DATA_HOME/olivares or ~/.local/share/olivares) |
| `--dsn` | `string` | — | store DSN (default a SQLite file in the data dir) |
| `--engine` | `string` | `sqlite` | store engine: sqlite or postgres |
| `--from` | `int64` | `1` | first ledger sequence to include (a recovered epoch begins at its recover_seq) |
| `--json` | `bool` | `false` | emit the report as JSON instead of a human summary |
| `--strict` | `bool` | `false` | exit non-zero if the report is INCOMPLETE (chain break, declared gap, or malformed rows) — use to gate an observe→enforce promotion in CI |
| `--tenant` | `string` | — | tenant id to report on (default $OLIVARES_TENANT) |

#### Command: olivares audit recover

Seal a corrupt audit tail and start a governed recovery epoch

```
olivares audit recover
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--archive-dir` | `string` | — | optional off-box archive directory that must verify and cover the trusted prefix |
| `--data-dir` | `string` | — | data directory (default $OLIVARES_DATA_DIR, an existing ./olivares-data, else $XDG_DATA_HOME/olivares or ~/.local/share/olivares) |
| `--dry-run` | `bool` | `true` | run every deny-closed check and print the plan without appending the recovery marker |
| `--dsn` | `string` | — | store DSN (default a SQLite file in the data dir) |
| `--engine` | `string` | `sqlite` | store engine: sqlite or postgres |
| `--pubkey` | `stringArray` | `[]` | **required**. required pinned off-box checkpoint public key, repeatable: raw base64 Ed25519 or "&lt;alg&gt;:&lt;base64 DER SPKI&gt;" |
| `--pubkey-alg` | `string` | — | algorithm of a SINGLE bare --pubkey (compat form, as in `audit verify`) |
| `--reason` | `string` | — | operator reason recorded in the signed recovery evidence |
| `--requested-by` | `string` | — | non-secret requester identity recorded in the signed recovery evidence |
| `--tenant` | `string` | — | tenant id whose corrupt audit tail will be sealed (default $OLIVARES_TENANT) |

#### Command: olivares audit verify

Verify a tenant's chain and its signed checkpoints

```
olivares audit verify
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--data-dir` | `string` | — | data directory (default $OLIVARES_DATA_DIR, an existing ./olivares-data, else $XDG_DATA_HOME/olivares or ~/.local/share/olivares) |
| `--dsn` | `string` | — | store DSN (default a SQLite file in the data dir) |
| `--engine` | `string` | `sqlite` | store engine: sqlite or postgres |
| `--event-pubkey` | `stringArray` | `[]` | per-event Ed25519 public key pin, repeatable (raw base64), optionally epoch-FENCED as "&lt;base64&gt;@&lt;last_seq&gt;" (retired generation, valid only up to that sequence) or "&lt;base64&gt;@&lt;lo&gt;:&lt;hi&gt;" (explicit window); a bare key is the current key. Pins REPLACE the advisory defaults — pin EVERY generation with its boundary (`keys status` lists prior_public_keys; the boundary is the audit.key.rotation marker's prior_last_seq). Without a boundary a retired key is trusted for every sequence |
| `--from` | `int64` | `1` | first sequence of the structural walk (a recovered epoch begins at its recover_seq; genesis remains the default) |
| `--pubkey` | `stringArray` | `[]` | checkpoint public key pin, repeatable (key rotation): raw base64 Ed25519, or "&lt;alg&gt;:&lt;base64 DER SPKI&gt;" for an off-box key (default: the engine's own keys — advisory only; pin OFF-BOX keys for an attacker-resistant check, docs/SECURITY-HARDENING.md §5) |
| `--pubkey-alg` | `string` | — | algorithm of a SINGLE bare --pubkey (compat form): ed25519 (raw, default) \| ecdsa-p256-sha256 \| ecdsa-p384-sha384 \| rsa-pkcs1-sha256 \| rsa-pss-sha256 (DER SubjectPublicKeyInfo); with multiple --pubkey use the "&lt;alg&gt;:&lt;base64&gt;" form |
| `--strict` | `bool` | `false` | exit non-zero if any integrity check fails (chain/checkpoints/event_sigs); for on-call cron/CI. The default exits 0 and reports status only in the JSON |
| `--tenant` | `string` | — | tenant id to verify (default $OLIVARES_TENANT) |

#### Command: olivares auth

Manage CLI authentication and named client contexts

```
olivares auth
```

Declares no flags of its own; it takes those of [`olivares`](#command-olivares) and the root command.

#### Command: olivares auth bootstrap

Redeem the one-time first-boot token: create the first organization and superadmin

```
olivares auth bootstrap
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--allow-cleartext` | `bool` | `false` | allow sending the credential to a non-loopback host over plain HTTP (DANGEROUS: it travels readable) |
| `--ca-cert` | `string` | — | PEM file containing an additional trusted root CA (default: current context) |
| `--context` | `string` | — | context name to create or update with --save-context (default: server hostname) |
| `--email` | `string` | — | email address of the first superadmin (required) |
| `--insecure` | `bool` | `false` | skip TLS certificate verification (DANGEROUS; development only) |
| `--organization` | `string` | — | name of the first organization (default: "Default Organization") |
| `--password` | `string` | — | password of the first superadmin (prefer --password-file) |
| `--password-file` | `string` | — | read the first superadmin's password from a file, or - for stdin |
| `--pin-sha256` | `stringArray` | `[]` | trusted leaf SPKI SHA-256 pin, base64 or hex, repeatable — the engine prints it as pin_sha256 on the line reporting its certificate (default: current context) |
| `--save-context` | `bool` | `false` | log in as the new superadmin and save the session in a client context |
| `--server` | `string` | — | control-plane base URL (default $OLIVARES_SERVER_URL, then current context) |
| `--setup-token` | `string` | — | the one-time first-boot token (prefer --setup-token-file: this form is visible in the process table) |
| `--setup-token-file` | `string` | — | read the one-time first-boot token from a file, or - for stdin |
| `--tenant` | `string` | — | tenant id (default $OLIVARES_TENANT, then current context) |
| `--timeout` | `duration` | `10s` | request timeout |
| `--token` | `string` | — | API bearer token (prefer --token-file: this form is visible in the process table and in shell history; default $OLIVARES_TOKEN, then current context) |
| `--token-file` | `string` | — | read the API bearer token from a file, or - for stdin |

#### Command: olivares auth login

Validate a credential and save it in a client context

```
olivares auth login
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--allow-cleartext` | `bool` | `false` | allow sending the credential to a non-loopback host over plain HTTP (DANGEROUS: it travels readable) |
| `--ca-cert` | `string` | — | PEM file containing an additional trusted root CA (default: current context) |
| `--context` | `string` | — | context name to create or update (default: server hostname) |
| `--email` | `string` | — | sign in with this account's password instead of a bearer token |
| `--insecure` | `bool` | `false` | skip TLS certificate verification (DANGEROUS; development only) |
| `--password` | `string` | — | password for --email (prefer --password-file: this form is visible in the process table) |
| `--password-file` | `string` | — | read the password for --email from a file, or - for stdin |
| `--pin-sha256` | `stringArray` | `[]` | trusted leaf SPKI SHA-256 pin, base64 or hex, repeatable — the engine prints it as pin_sha256 on the line reporting its certificate (default: current context) |
| `--server` | `string` | — | control-plane base URL (default $OLIVARES_SERVER_URL, then current context) |
| `--tenant` | `string` | — | tenant id (default $OLIVARES_TENANT, then current context) |
| `--timeout` | `duration` | `10s` | request timeout |
| `--token` | `string` | — | API bearer token (prefer --token-file: this form is visible in the process table and in shell history; default $OLIVARES_TOKEN, then current context) |
| `--token-file` | `string` | — | read the API bearer token from a file, or - for stdin |

#### Command: olivares auth logout

Remove a saved token from a client context

```
olivares auth logout
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--context` | `string` | — | context to log out (default: current context) |
| `--purge` | `bool` | `false` | delete the entire context instead of only its token |

#### Command: olivares auth status

Show the effective CLI identity and authentication context

```
olivares auth status
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--allow-cleartext` | `bool` | `false` | allow sending the credential to a non-loopback host over plain HTTP (DANGEROUS: it travels readable) |
| `--ca-cert` | `string` | — | PEM file containing an additional trusted root CA (default: current context) |
| `--insecure` | `bool` | `false` | skip TLS certificate verification (DANGEROUS; development only) |
| `--pin-sha256` | `stringArray` | `[]` | trusted leaf SPKI SHA-256 pin, base64 or hex, repeatable — the engine prints it as pin_sha256 on the line reporting its certificate (default: current context) |
| `--server` | `string` | — | control-plane base URL (default $OLIVARES_SERVER_URL, then current context) |
| `--tenant` | `string` | — | tenant id (default $OLIVARES_TENANT, then current context) |
| `--timeout` | `duration` | `10s` | request timeout |
| `--token` | `string` | — | API bearer token (prefer --token-file: this form is visible in the process table and in shell history; default $OLIVARES_TOKEN, then current context) |
| `--token-file` | `string` | — | read the API bearer token from a file, or - for stdin |

#### Command: olivares auth use-context

Select the current CLI client context

```
olivares auth use-context <name>
```

Declares no flags of its own; it takes those of [`olivares auth`](#command-olivares-auth) and the root command.

#### Command: olivares capabilities

What this estate can do: connected servers, and the tools and skills they bring

```
olivares capabilities
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--allow-cleartext` | `bool` | `false` | **inherited**. allow sending the credential to a non-loopback host over plain HTTP (DANGEROUS: it travels readable) |
| `--ca-cert` | `string` | — | **inherited**. PEM file containing an additional trusted root CA (default: current context) |
| `--insecure` | `bool` | `false` | **inherited**. skip TLS certificate verification (DANGEROUS; development only) |
| `--pin-sha256` | `stringArray` | `[]` | **inherited**. trusted leaf SPKI SHA-256 pin, base64 or hex, repeatable — the engine prints it as pin_sha256 on the line reporting its certificate (default: current context) |
| `--server` | `string` | — | **inherited**. control-plane base URL (default $OLIVARES_SERVER_URL, then current context) |
| `--tenant` | `string` | — | **inherited**. tenant id (default $OLIVARES_TENANT, then current context) |
| `--timeout` | `duration` | `10s` | **inherited**. request timeout |
| `--token` | `string` | — | **inherited**. API bearer token (prefer --token-file: this form is visible in the process table and in shell history; default $OLIVARES_TOKEN, then current context) |
| `--token-file` | `string` | — | **inherited**. read the API bearer token from a file, or - for stdin |

#### Command: olivares capabilities servers

The MCP servers this estate talks to

```
olivares capabilities servers
```

Declares no flags of its own; it takes those of [`olivares capabilities`](#command-olivares-capabilities) and the root command.

#### Command: olivares capabilities servers get

Show one MCP server and what it brings

```
olivares capabilities servers get <server-id>
```

Declares no flags of its own; it takes those of [`olivares capabilities servers`](#command-olivares-capabilities-servers) and the root command.

#### Command: olivares capabilities servers ls

List the connected MCP servers

```
olivares capabilities servers ls
```

Aliases: `list`

| Flag | Type | Default | Description |
|---|---|---|---|
| `--cursor` | `string` | — | continue from the cursor printed by the previous page |
| `--limit` | `int` | `0` | maximum rows to return in one page (0 = the engine's default) |

#### Command: olivares capabilities skills

The skills the connected servers contribute

```
olivares capabilities skills
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--cursor` | `string` | — | continue from the cursor printed by the previous page |
| `--limit` | `int` | `0` | maximum rows to return in one page (0 = the engine's default) |
| `--server-id` | `string` | — | only skills from this MCP server |

#### Command: olivares capabilities tools

The tools the connected servers expose, with their destructive hints

```
olivares capabilities tools
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--cursor` | `string` | — | continue from the cursor printed by the previous page |
| `--limit` | `int` | `0` | maximum rows to return in one page (0 = the engine's default) |
| `--server-id` | `string` | — | only tools from this MCP server |

#### Command: olivares capabilities wiring

Who is actually using which capability, as observed edges

```
olivares capabilities wiring
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--capability-kind` | `string` | — | only edges to this kind of capability |
| `--capability-ref` | `string` | — | only edges to this capability |
| `--origin-kind` | `string` | — | only edges from this kind of origin |
| `--origin-ref` | `string` | — | only edges from this origin |

#### Command: olivares catalog

Admit and govern catalog entries, connectors and MCP servers

```
olivares catalog
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--allow-cleartext` | `bool` | `false` | **inherited**. allow sending the credential to a non-loopback host over plain HTTP (DANGEROUS: it travels readable) |
| `--ca-cert` | `string` | — | **inherited**. PEM file containing an additional trusted root CA (default: current context) |
| `--insecure` | `bool` | `false` | **inherited**. skip TLS certificate verification (DANGEROUS; development only) |
| `--pin-sha256` | `stringArray` | `[]` | **inherited**. trusted leaf SPKI SHA-256 pin, base64 or hex, repeatable — the engine prints it as pin_sha256 on the line reporting its certificate (default: current context) |
| `--server` | `string` | — | **inherited**. control-plane base URL (default $OLIVARES_SERVER_URL, then current context) |
| `--tenant` | `string` | — | **inherited**. tenant id (default $OLIVARES_TENANT, then current context) |
| `--timeout` | `duration` | `10s` | **inherited**. request timeout |
| `--token` | `string` | — | **inherited**. API bearer token (prefer --token-file: this form is visible in the process table and in shell history; default $OLIVARES_TOKEN, then current context) |
| `--token-file` | `string` | — | **inherited**. read the API bearer token from a file, or - for stdin |

#### Command: olivares catalog connector-admission

Read and set the connector supply-chain admission policy

```
olivares catalog connector-admission
```

Declares no flags of its own; it takes those of [`olivares catalog`](#command-olivares-catalog) and the root command.

#### Command: olivares catalog connector-admission ls

List recorded connector admission verdicts

```
olivares catalog connector-admission ls
```

Aliases: `list`

| Flag | Type | Default | Description |
|---|---|---|---|
| `--cursor` | `string` | — | opaque cursor from a previous page's has_more result |
| `--entry-ref` | `string` | — | only verdicts for this entry |
| `--limit` | `int` | `0` | maximum rows per page (server default when unset) |
| `--verified` | `bool` | `false` | only verdicts that verified |

#### Command: olivares catalog connector-admission policy

Read or replace the connector admission policy

```
olivares catalog connector-admission policy
```

Declares no flags of its own; it takes those of [`olivares catalog connector-admission`](#command-olivares-catalog-connector-admission) and the root command.

#### Command: olivares catalog connector-admission policy get

Show the connector admission policy

```
olivares catalog connector-admission policy get
```

Declares no flags of its own; it takes those of [`olivares catalog connector-admission policy`](#command-olivares-catalog-connector-admission-policy) and the root command.

#### Command: olivares catalog connector-admission policy set

Replace the connector admission policy

```
olivares catalog connector-admission policy set
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--allowed-identity` | `stringArray` | `[]` | trusted keyless identity, repeatable |
| `--allowed-issuer` | `stringArray` | `[]` | trusted OIDC issuer, repeatable |
| `--allowed-predicate` | `stringArray` | `[]` | accepted attestation predicate type, repeatable |
| `--note` | `string` | — | note recorded with the policy |
| `--replace` | `bool` | `false` | accept that every field not passed is RESET to its server default (this endpoint replaces, it does not patch) |
| `--require-signed` | `bool` | `false` | refuse artifacts without a verifying signature |
| `--require-subject-digest` | `bool` | `false` | require the attestation to cover the subject digest |
| `--trusted-key` | `stringArray` | `[]` | trusted PUBLIC key, repeatable |
| `--trusted-root` | `stringArray` | `[]` | trusted root certificate, repeatable |
| `-y`, `--yes` | `bool` | `false` | proceed without the confirmation prompt (required in a non-interactive session) |

#### Command: olivares catalog entries

Author, review and admit catalog entries

```
olivares catalog entries
```

Declares no flags of its own; it takes those of [`olivares catalog`](#command-olivares-catalog) and the root command.

#### Command: olivares catalog entries admit

Verify a supply-chain attestation for an entry

```
olivares catalog entries admit <entry-id>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--bundle` | `string` | — | attestation bundle as JSON |
| `--bundle-file` | `string` | — | file holding the JSON attestation bundle (- for stdin) |
| `--expected-digest` | `string` | — | subject digest the attestation must cover |
| `--note` | `string` | — | note recorded with the verdict |
| `--predicate-type` | `stringArray` | `[]` | predicate type to accept, repeatable |

#### Command: olivares catalog entries approve

Approve a submitted entry, hashing and signing it

```
olivares catalog entries approve <entry-id>
```

Declares no flags of its own; it takes those of [`olivares catalog entries`](#command-olivares-catalog-entries) and the root command.

#### Command: olivares catalog entries create

Author a draft catalog entry

```
olivares catalog entries create
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--kind` | `string` | — | agent, mcp, skill, template, model or connector |
| `--name` | `string` | — | entry name |
| `--owner-ref` | `string` | — | owning team or principal |
| `--slug` | `string` | — | lowercase identifier (a-z, 0-9, - and _) |
| `--spec` | `string` | — | entry specification as a JSON object |
| `--spec-file` | `string` | — | file holding the JSON specification (- for stdin) |
| `--summary` | `string` | — | one-line summary |
| `--version` | `string` | — | semantic version, e.g. 1.2.3 |

#### Command: olivares catalog entries deprecate

Retire an approved entry

```
olivares catalog entries deprecate <entry-id>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `-y`, `--yes` | `bool` | `false` | proceed without the confirmation prompt (required in a non-interactive session) |

#### Command: olivares catalog entries get

Show one catalog entry

```
olivares catalog entries get <entry-id>
```

Declares no flags of its own; it takes those of [`olivares catalog entries`](#command-olivares-catalog-entries) and the root command.

#### Command: olivares catalog entries instantiate

Request an instance from an approved entry

```
olivares catalog entries instantiate <entry-id>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--name` | `string` | — | name for the requested instance |
| `--note` | `string` | — | note recorded with the request |
| `--target-ref` | `string` | — | where the instance is meant to land |

#### Command: olivares catalog entries ls

List catalog entries

```
olivares catalog entries ls
```

Aliases: `list`

| Flag | Type | Default | Description |
|---|---|---|---|
| `--cursor` | `string` | — | opaque cursor from a previous page's has_more result |
| `--kind` | `string` | — | only entries of this kind |
| `--limit` | `int` | `0` | maximum rows per page (server default when unset) |
| `--slug` | `string` | — | only entries with this slug |
| `--status` | `string` | — | only entries in this status |

#### Command: olivares catalog entries rm

Delete a catalog entry

```
olivares catalog entries rm <entry-id>
```

Aliases: `delete`

| Flag | Type | Default | Description |
|---|---|---|---|
| `-y`, `--yes` | `bool` | `false` | proceed without the confirmation prompt (required in a non-interactive session) |

#### Command: olivares catalog entries set

Replace a draft entry's authored fields

```
olivares catalog entries set <entry-id>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--kind` | `string` | — | agent, mcp, skill, template, model or connector |
| `--name` | `string` | — | entry name |
| `--owner-ref` | `string` | — | owning team or principal |
| `--replace` | `bool` | `false` | accept that every field not passed is RESET to its server default (this endpoint replaces, it does not patch) |
| `--slug` | `string` | — | lowercase identifier (a-z, 0-9, - and _) |
| `--spec` | `string` | — | entry specification as a JSON object |
| `--spec-file` | `string` | — | file holding the JSON specification (- for stdin) |
| `--summary` | `string` | — | one-line summary |
| `--version` | `string` | — | semantic version, e.g. 1.2.3 |

#### Command: olivares catalog entries submit

Submit a draft entry for review

```
olivares catalog entries submit <entry-id>
```

Declares no flags of its own; it takes those of [`olivares catalog entries`](#command-olivares-catalog-entries) and the root command.

#### Command: olivares catalog entries verify

Recompute an entry's hash and check its signature

```
olivares catalog entries verify <entry-id>
```

Declares no flags of its own; it takes those of [`olivares catalog entries`](#command-olivares-catalog-entries) and the root command.

#### Command: olivares catalog instances

Review and decide self-service instantiation requests

```
olivares catalog instances
```

Declares no flags of its own; it takes those of [`olivares catalog`](#command-olivares-catalog) and the root command.

#### Command: olivares catalog instances get

Show one instantiation request

```
olivares catalog instances get <instance-id>
```

Declares no flags of its own; it takes those of [`olivares catalog instances`](#command-olivares-catalog-instances) and the root command.

#### Command: olivares catalog instances ls

List instantiation requests

```
olivares catalog instances ls
```

Aliases: `list`

| Flag | Type | Default | Description |
|---|---|---|---|
| `--cursor` | `string` | — | opaque cursor from a previous page's has_more result |
| `--entry-id` | `string` | — | only instances of this catalog entry |
| `--limit` | `int` | `0` | maximum rows per page (server default when unset) |
| `--status` | `string` | — | only instances in this status |

#### Command: olivares catalog instances transition

Record a governance decision on an instance

```
olivares catalog instances transition <instance-id>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--note` | `string` | — | note recorded with the decision |
| `--status` | `string` | — | approved, rejected or active |
| `-y`, `--yes` | `bool` | `false` | proceed without the confirmation prompt (required in a non-interactive session) |

#### Command: olivares catalog mcp-admission

Read and set the MCP server supply-chain admission policy

```
olivares catalog mcp-admission
```

Declares no flags of its own; it takes those of [`olivares catalog`](#command-olivares-catalog) and the root command.

#### Command: olivares catalog mcp-admission ls

List recorded MCP server admission verdicts

```
olivares catalog mcp-admission ls
```

Aliases: `list`

| Flag | Type | Default | Description |
|---|---|---|---|
| `--cursor` | `string` | — | opaque cursor from a previous page's has_more result |
| `--entry-ref` | `string` | — | only verdicts for this entry |
| `--limit` | `int` | `0` | maximum rows per page (server default when unset) |
| `--verified` | `bool` | `false` | only verdicts that verified |

#### Command: olivares catalog mcp-admission policy

Read or replace the MCP server admission policy

```
olivares catalog mcp-admission policy
```

Declares no flags of its own; it takes those of [`olivares catalog mcp-admission`](#command-olivares-catalog-mcp-admission) and the root command.

#### Command: olivares catalog mcp-admission policy get

Show the MCP server admission policy

```
olivares catalog mcp-admission policy get
```

Declares no flags of its own; it takes those of [`olivares catalog mcp-admission policy`](#command-olivares-catalog-mcp-admission-policy) and the root command.

#### Command: olivares catalog mcp-admission policy set

Replace the MCP server admission policy

```
olivares catalog mcp-admission policy set
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--allowed-identity` | `stringArray` | `[]` | trusted keyless identity, repeatable |
| `--allowed-issuer` | `stringArray` | `[]` | trusted OIDC issuer, repeatable |
| `--allowed-predicate` | `stringArray` | `[]` | accepted attestation predicate type, repeatable |
| `--note` | `string` | — | note recorded with the policy |
| `--replace` | `bool` | `false` | accept that every field not passed is RESET to its server default (this endpoint replaces, it does not patch) |
| `--require-signed` | `bool` | `false` | refuse artifacts without a verifying signature |
| `--require-subject-digest` | `bool` | `false` | require the attestation to cover the subject digest |
| `--trusted-key` | `stringArray` | `[]` | trusted PUBLIC key, repeatable |
| `--trusted-root` | `stringArray` | `[]` | trusted root certificate, repeatable |
| `-y`, `--yes` | `bool` | `false` | proceed without the confirmation prompt (required in a non-interactive session) |

#### Command: olivares catalog pubkey

Show the public key catalog approvals are signed with

```
olivares catalog pubkey
```

Declares no flags of its own; it takes those of [`olivares catalog`](#command-olivares-catalog) and the root command.

#### Command: olivares claude-agents

Read a managed agent session's thread events and answer its tool confirmations

```
olivares claude-agents
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--allow-cleartext` | `bool` | `false` | **inherited**. allow sending the credential to a non-loopback host over plain HTTP (DANGEROUS: it travels readable) |
| `--ca-cert` | `string` | — | **inherited**. PEM file containing an additional trusted root CA (default: current context) |
| `--insecure` | `bool` | `false` | **inherited**. skip TLS certificate verification (DANGEROUS; development only) |
| `--pin-sha256` | `stringArray` | `[]` | **inherited**. trusted leaf SPKI SHA-256 pin, base64 or hex, repeatable — the engine prints it as pin_sha256 on the line reporting its certificate (default: current context) |
| `--server` | `string` | — | **inherited**. control-plane base URL (default $OLIVARES_SERVER_URL, then current context) |
| `--tenant` | `string` | — | **inherited**. tenant id (default $OLIVARES_TENANT, then current context) |
| `--timeout` | `duration` | `10s` | **inherited**. request timeout |
| `--token` | `string` | — | **inherited**. API bearer token (prefer --token-file: this form is visible in the process table and in shell history; default $OLIVARES_TOKEN, then current context) |
| `--token-file` | `string` | — | **inherited**. read the API bearer token from a file, or - for stdin |

#### Command: olivares claude-agents sessions

Inspect and answer one managed agent session

```
olivares claude-agents sessions
```

Declares no flags of its own; it takes those of [`olivares claude-agents`](#command-olivares-claude-agents) and the root command.

#### Command: olivares claude-agents sessions events

List one managed session's thread events

```
olivares claude-agents sessions events <session-id>
```

Declares no flags of its own; it takes those of [`olivares claude-agents sessions`](#command-olivares-claude-agents-sessions) and the root command.

#### Command: olivares claude-agents sessions tool-confirmation

Answer a managed agent's pending tool use (allow or deny)

```
olivares claude-agents sessions tool-confirmation <session-id>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--deny-message` | `string` | — | why the tool was denied, shown to the agent |
| `--result` | `string` | — | **required**. allow or deny (required) |
| `--tool-use-id` | `string` | — | **required**. the pending tool use to answer (required) |

#### Command: olivares claude-hook

Governed PEP hook client: forward a Claude Code hook to the control plane and relay the decision (deny-closed)

```
olivares claude-hook
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--account` | `string` | — | account identity hint (default $OLIVARES_HOOK_PEP_ACCOUNT) |
| `--agent` | `string` | — | agent identity hint (default $OLIVARES_HOOK_PEP_AGENT) |
| `--endpoint` | `string` | — | governed PEP URL (default $OLIVARES_HOOK_PEP_URL); --server is the canonical spelling |
| `--org` | `string` | — | org identity hint (default $OLIVARES_HOOK_PEP_ORG) |
| `--server` | `string` | — | control-plane base URL (default $OLIVARES_SERVER_URL; the canonical spelling of --endpoint) |
| `--tenant` | `string` | — | the tenant the agent acts in (default $OLIVARES_HOOK_PEP_TENANT) |
| `--timeout` | `duration` | `5s` | PEP request timeout |
| `--token` | `string` | — | the agent's PEP bearer credential (default $OLIVARES_HOOK_PEP_TOKEN) |

#### Command: olivares claude-policy

Author, publish and track the Claude Code managed-* policy surfaces

```
olivares claude-policy
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--allow-cleartext` | `bool` | `false` | **inherited**. allow sending the credential to a non-loopback host over plain HTTP (DANGEROUS: it travels readable) |
| `--ca-cert` | `string` | — | **inherited**. PEM file containing an additional trusted root CA (default: current context) |
| `--insecure` | `bool` | `false` | **inherited**. skip TLS certificate verification (DANGEROUS; development only) |
| `--pin-sha256` | `stringArray` | `[]` | **inherited**. trusted leaf SPKI SHA-256 pin, base64 or hex, repeatable — the engine prints it as pin_sha256 on the line reporting its certificate (default: current context) |
| `--server` | `string` | — | **inherited**. control-plane base URL (default $OLIVARES_SERVER_URL, then current context) |
| `--tenant` | `string` | — | **inherited**. tenant id (default $OLIVARES_TENANT, then current context) |
| `--timeout` | `duration` | `10s` | **inherited**. request timeout |
| `--token` | `string` | — | **inherited**. API bearer token (prefer --token-file: this form is visible in the process table and in shell history; default $OLIVARES_TOKEN, then current context) |
| `--token-file` | `string` | — | **inherited**. read the API bearer token from a file, or - for stdin |

#### Command: olivares claude-policy artifact

Fetch the signed artifact a distribution agent would pull

```
olivares claude-policy artifact <surface>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--revision` | `int64` | `0` | a specific revision (0 uses the newest) |

#### Command: olivares claude-policy checkin

Report an agent's applied artifact and observed config (exit 7 when unverified)

```
olivares claude-policy checkin <surface>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--artifact-sha256` | `string` | — | the artifact hash the agent verified |
| `--key-fingerprint` | `string` | — | the signing key fingerprint the agent verified against |
| `--observed-file` | `string` | — | the config observed on the host, '-' for stdin |
| `--revision` | `int64` | `0` | the revision the agent applied |
| `--scope` | `string` | — | **required**. the host id / distribution name this check-in reports for (required) |

#### Command: olivares claude-policy distribution

Show published vs signed vs observed, scope by scope

```
olivares claude-policy distribution <surface>
```

Declares no flags of its own; it takes those of [`olivares claude-policy`](#command-olivares-claude-policy) and the root command.

#### Command: olivares claude-policy dry-run

Resolve a document against observed hosts without writing anything

```
olivares claude-policy dry-run <surface>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--content-file` | `string` | — | **required**. the policy document, '-' for stdin (required) |

#### Command: olivares claude-policy publish

Publish a new revision and, when a distributor is wired, sign it

```
olivares claude-policy publish <surface>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--content-file` | `string` | — | **required**. the policy document, '-' for stdin (required) |
| `--note` | `string` | — | why this revision exists (recorded on the revision) |

#### Command: olivares claude-policy validate

Validate a policy document server-side (exit 7 when it has errors)

```
olivares claude-policy validate <surface>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--content-file` | `string` | — | **required**. the policy document, '-' for stdin (required) |

#### Command: olivares claude-policy versions

List and read published revisions of a surface

```
olivares claude-policy versions
```

Declares no flags of its own; it takes those of [`olivares claude-policy`](#command-olivares-claude-policy) and the root command.

#### Command: olivares claude-policy versions get

Show one revision with its document content

```
olivares claude-policy versions get <surface> <revision>
```

Declares no flags of its own; it takes those of [`olivares claude-policy versions`](#command-olivares-claude-policy-versions) and the root command.

#### Command: olivares claude-policy versions ls

List a surface's published revisions

```
olivares claude-policy versions ls <surface>
```

Declares no flags of its own; it takes those of [`olivares claude-policy versions`](#command-olivares-claude-policy-versions) and the root command.

#### Command: olivares codex

Author OpenAI Codex governance artifacts (managed config)

```
olivares codex
```

Declares no flags of its own; it takes those of [`olivares`](#command-olivares) and the root command.

#### Command: olivares codex managed-config

Render the Codex requirements.toml + managed_config.toml from a governance Policy JSON

```
olivares codex managed-config
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--managed-config-out` | `string` | `-` | output path for managed_config.toml ('-' = stdout) |
| `--policy` | `string` | `-` | path to the governance Policy JSON ('-' = stdin) |
| `--requirements-out` | `string` | `-` | output path for requirements.toml ('-' = stdout, prefixed with a header when both files go to stdout) |
| `--validate` | `bool` | `false` | validate the policy renders to valid TOML, but write nothing |

#### Command: olivares codex-hook

Governed PEP hook client for Codex: forward a Codex hook to the control plane and relay the decision (deny-closed)

```
olivares codex-hook
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--account` | `string` | — | account identity hint (default $OLIVARES_CODEX_HOOK_ACCOUNT) |
| `--agent` | `string` | — | agent identity hint (default $OLIVARES_CODEX_HOOK_AGENT) |
| `--endpoint` | `string` | — | governed PEP URL (default $OLIVARES_CODEX_HOOK_URL); --server is the canonical spelling |
| `--org` | `string` | — | org identity hint (default $OLIVARES_CODEX_HOOK_ORG) |
| `--server` | `string` | — | control-plane base URL (default $OLIVARES_SERVER_URL; the canonical spelling of --endpoint) |
| `--tenant` | `string` | — | the tenant the agent acts in (default $OLIVARES_CODEX_HOOK_TENANT) |
| `--timeout` | `duration` | `5s` | PEP request timeout |
| `--token` | `string` | — | the agent's PEP bearer credential (default $OLIVARES_CODEX_HOOK_TOKEN) |

#### Command: olivares collector

Run as an edge collector: push local source observations to a remote core over gRPC+mTLS

```
olivares collector
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--ca` | `string` | — | PEM of the CA that signed the core's server certificate (pins a self-signed core cert; empty uses system roots) |
| `--client-cert` | `string` | — | collector client certificate PEM (required when the core enforces mutual TLS) |
| `--client-key` | `string` | — | collector client private key PEM |
| `--core-addr` | `string` | — | **required**. host:port of the remote core's gRPC ingest endpoint (required) |
| `--insecure` | `bool` | `false` | push over plaintext (DANGEROUS; localhost dev only) |
| `--server-name` | `string` | — | override the core's TLS verification name (when dialing by IP) |
| `--token-file` | `string` | — | file holding the bearer token of an ingest:write principal (or set OLIVARES_INGEST_TOKEN) |

#### Command: olivares commands

Hidden diagnostic: it does not appear in `--help` output and is not part of the supported surface.

Print the full command tree of this binary (diagnostic)

```
olivares commands
```

Declares no flags of its own; it takes those of [`olivares`](#command-olivares) and the root command.

#### Command: olivares completion

Generate shell autocompletion scripts

```
olivares completion
```

Declares no flags of its own; it takes those of [`olivares`](#command-olivares) and the root command.

#### Command: olivares completion bash

Generate bash autocompletion script

```
olivares completion bash
```

Declares no flags of its own; it takes those of [`olivares completion`](#command-olivares-completion) and the root command.

#### Command: olivares completion fish

Generate fish autocompletion script

```
olivares completion fish
```

Declares no flags of its own; it takes those of [`olivares completion`](#command-olivares-completion) and the root command.

#### Command: olivares completion powershell

Generate PowerShell autocompletion script

```
olivares completion powershell
```

Declares no flags of its own; it takes those of [`olivares completion`](#command-olivares-completion) and the root command.

#### Command: olivares completion zsh

Generate zsh autocompletion script

```
olivares completion zsh
```

Declares no flags of its own; it takes those of [`olivares completion`](#command-olivares-completion) and the root command.

#### Command: olivares compliance

Operate legal holds, GDPR erasure and regulatory artifacts

```
olivares compliance
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--allow-cleartext` | `bool` | `false` | **inherited**. allow sending the credential to a non-loopback host over plain HTTP (DANGEROUS: it travels readable) |
| `--ca-cert` | `string` | — | **inherited**. PEM file containing an additional trusted root CA (default: current context) |
| `--insecure` | `bool` | `false` | **inherited**. skip TLS certificate verification (DANGEROUS; development only) |
| `--pin-sha256` | `stringArray` | `[]` | **inherited**. trusted leaf SPKI SHA-256 pin, base64 or hex, repeatable — the engine prints it as pin_sha256 on the line reporting its certificate (default: current context) |
| `--server` | `string` | — | **inherited**. control-plane base URL (default $OLIVARES_SERVER_URL, then current context) |
| `--tenant` | `string` | — | **inherited**. tenant id (default $OLIVARES_TENANT, then current context) |
| `--timeout` | `duration` | `10s` | **inherited**. request timeout |
| `--token` | `string` | — | **inherited**. API bearer token (prefer --token-file: this form is visible in the process table and in shell history; default $OLIVARES_TOKEN, then current context) |
| `--token-file` | `string` | — | **inherited**. read the API bearer token from a file, or - for stdin |

#### Command: olivares compliance calendar

Show the regulatory calendar and watchlist

```
olivares compliance calendar
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--framework` | `string` | — | filter to one framework id |

#### Command: olivares compliance depth

Inspect compliance-depth packs and control monitoring

```
olivares compliance depth
```

Declares no flags of its own; it takes those of [`olivares compliance`](#command-olivares-compliance) and the root command.

#### Command: olivares compliance depth drift

List detected control drift

```
olivares compliance depth drift
```

Aliases: `list`

Declares no flags of its own; it takes those of [`olivares compliance depth`](#command-olivares-compliance-depth) and the root command.

#### Command: olivares compliance depth sector

List sector overlay packs

```
olivares compliance depth sector
```

Aliases: `list`

Declares no flags of its own; it takes those of [`olivares compliance depth`](#command-olivares-compliance-depth) and the root command.

#### Command: olivares compliance depth snapshots

List CCM control snapshots

```
olivares compliance depth snapshots
```

Aliases: `list`

Declares no flags of its own; it takes those of [`olivares compliance depth`](#command-olivares-compliance-depth) and the root command.

#### Command: olivares compliance depth us-law

List US state-law packs

```
olivares compliance depth us-law
```

Aliases: `list`

Declares no flags of its own; it takes those of [`olivares compliance depth`](#command-olivares-compliance-depth) and the root command.

#### Command: olivares compliance dora

Inspect DORA registers and classified incidents

```
olivares compliance dora
```

Declares no flags of its own; it takes those of [`olivares compliance`](#command-olivares-compliance) and the root command.

#### Command: olivares compliance dora incidents

List classified DORA incidents

```
olivares compliance dora incidents
```

Aliases: `list`

Declares no flags of its own; it takes those of [`olivares compliance dora`](#command-olivares-compliance-dora) and the root command.

#### Command: olivares compliance dora registers

List DORA registers of information

```
olivares compliance dora registers
```

Aliases: `list`

Declares no flags of its own; it takes those of [`olivares compliance dora`](#command-olivares-compliance-dora) and the root command.

#### Command: olivares compliance erasure

Register, execute and evidence GDPR erasure requests

```
olivares compliance erasure
```

Declares no flags of its own; it takes those of [`olivares compliance`](#command-olivares-compliance) and the root command.

#### Command: olivares compliance erasure custody

Show an erasure's append-only chain of custody

```
olivares compliance erasure custody <erasure-id>
```

Declares no flags of its own; it takes those of [`olivares compliance erasure`](#command-olivares-compliance-erasure) and the root command.

#### Command: olivares compliance erasure execute

Execute an erasure (IRREVERSIBLE, dual-control)

```
olivares compliance erasure execute <erasure-id>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--provider-user-id` | `stringArray` | `[]` | provider-side user id to erase, repeatable |
| `--reason` | `string` | — | why this erasure is being executed |
| `-y`, `--yes` | `bool` | `false` | proceed without the confirmation prompt (required in a non-interactive session) |

#### Command: olivares compliance erasure get

Show one erasure request

```
olivares compliance erasure get <erasure-id>
```

Declares no flags of its own; it takes those of [`olivares compliance erasure`](#command-olivares-compliance-erasure) and the root command.

#### Command: olivares compliance erasure ls

List erasure requests

```
olivares compliance erasure ls
```

Aliases: `list`

| Flag | Type | Default | Description |
|---|---|---|---|
| `--status` | `string` | — | filter by status (received, pending_approval, completed, ...) |

#### Command: olivares compliance erasure receipt

Show the sealed, ledger-anchored erasure receipt

```
olivares compliance erasure receipt <erasure-id>
```

Declares no flags of its own; it takes those of [`olivares compliance erasure`](#command-olivares-compliance-erasure) and the root command.

#### Command: olivares compliance erasure request

Register an erasure request (destroys nothing)

```
olivares compliance erasure request
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--alias` | `stringArray` | `[]` | additional identifier for the same person, repeatable |
| `--case-ref` | `string` | — | **required**. your DSAR case reference (required) |
| `--data-class` | `stringArray` | `[]` | narrow to these registered data classes, repeatable |
| `--reason` | `string` | — | why this request exists |
| `--subject-kind` | `string` | `user` | subject kind |
| `--subject-ref` | `string` | — | **required**. subject reference (required) |

#### Command: olivares compliance holds

Place, inspect and release legal holds

```
olivares compliance holds
```

Declares no flags of its own; it takes those of [`olivares compliance`](#command-olivares-compliance) and the root command.

#### Command: olivares compliance holds check

Ask whether any active hold already covers a subject or class

```
olivares compliance holds check
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--data-class` | `string` | — | registered data class id |
| `--subject-kind` | `string` | — | subject kind (e.g. user) |
| `--subject-ref` | `string` | — | subject reference |

#### Command: olivares compliance holds custody

Show a hold's append-only chain of custody

```
olivares compliance holds custody <hold-id>
```

Declares no flags of its own; it takes those of [`olivares compliance holds`](#command-olivares-compliance-holds) and the root command.

#### Command: olivares compliance holds get

Show one legal hold

```
olivares compliance holds get <hold-id>
```

Declares no flags of its own; it takes those of [`olivares compliance holds`](#command-olivares-compliance-holds) and the root command.

#### Command: olivares compliance holds ls

List legal holds

```
olivares compliance holds ls
```

Aliases: `list`

| Flag | Type | Default | Description |
|---|---|---|---|
| `--status` | `string` | — | filter by status (active, released) |

#### Command: olivares compliance holds place

Place a legal hold (takes effect immediately)

```
olivares compliance holds place
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--data-class` | `string` | — | registered data class id (scope=data_class) |
| `--matter` | `string` | — | **required**. matter or case reference (required) |
| `--on-behalf-of` | `string` | — | the person this order is placed for |
| `--reason` | `string` | — | **required**. why this hold exists (required; recorded in custody) |
| `--scope` | `string` | `subject` | scope: tenant, data_class or subject |
| `--subject-kind` | `string` | — | subject kind (scope=subject) |
| `--subject-ref` | `string` | — | subject reference (scope=subject) |
| `--title` | `string` | — | human-readable title |

#### Command: olivares compliance holds release

Release a legal hold (dual-control, no break-glass)

```
olivares compliance holds release <hold-id>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--on-behalf-of` | `string` | — | the person this release is made for |
| `--reason` | `string` | — | why the hold is being released (recorded in custody) |
| `-y`, `--yes` | `bool` | `false` | proceed without the confirmation prompt (required in a non-interactive session) |

#### Command: olivares compliance oscal

Inspect ingested OSCAL profiles and SSPs

```
olivares compliance oscal
```

Declares no flags of its own; it takes those of [`olivares compliance`](#command-olivares-compliance) and the root command.

#### Command: olivares compliance oscal ls

List registered OSCAL documents

```
olivares compliance oscal ls
```

Aliases: `list`

Declares no flags of its own; it takes those of [`olivares compliance oscal`](#command-olivares-compliance-oscal) and the root command.

#### Command: olivares compliance subject

Answer a data subject's erasure request by subject id

```
olivares compliance subject
```

Declares no flags of its own; it takes those of [`olivares compliance`](#command-olivares-compliance) and the root command.

#### Command: olivares compliance subject erase

Register and execute an erasure for one subject (IRREVERSIBLE)

```
olivares compliance subject erase <subject-id>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--alias` | `stringArray` | `[]` | additional identifier for the same person, repeatable |
| `--case-ref` | `string` | — | your DSAR case reference |
| `--data-class` | `stringArray` | `[]` | narrow to these registered data classes, repeatable |
| `--provider-user-id` | `stringArray` | `[]` | provider-side user id to erase, repeatable |
| `--reason` | `string` | — | why this erasure is being executed |
| `--subject-kind` | `string` | — | subject kind (default: user) |
| `-y`, `--yes` | `bool` | `false` | proceed without the confirmation prompt (required in a non-interactive session) |

#### Command: olivares compliance subject status

Show erasure status for one data subject

```
olivares compliance subject status <subject-id>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--subject-kind` | `string` | — | subject kind (default: user) |

#### Command: olivares config

Generate validated engine configuration (the non-interactive setup)

```
olivares config
```

Declares no flags of its own; it takes those of [`olivares`](#command-olivares) and the root command.

#### Command: olivares config effective

Print configured OLIVARES_* values with secrets redacted

```
olivares config effective
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--format` | `string` | `text` | deprecated alias for -o/--output on this command (text or json) — NOT the export-format flag of 'audit export' / 'findings export' |
| `--strict` | `bool` | `false` | fail if any unrecognized OLIVARES_* environment key is present |

#### Command: olivares config generate

Compose a validated /etc/olivares/olivares.env (or k8s snippet) from flags

```
olivares config generate
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--admin-dsn` | `string` | — | cross-tenant admin-role DSN or reference |
| `--allow-privileged-db-role` | `bool` | `false` | permit a superuser/BYPASSRLS Postgres role (DANGEROUS; disables the RLS backstop) |
| `--audit-signing-key-file` | `string` | — | operator-provisioned Ed25519 audit signing key file (required external BYOK custody for postgres-prod) |
| `--checkpoint-interval` | `string` | — | audit checkpoint cadence override (e.g. 30m; default 1h) |
| `--data-dir` | `string` | — | data directory override (default the unit's /var/lib/olivares) |
| `--dsn` | `string` | — | store DSN or a file:/env: reference (required for postgres) |
| `--engine` | `string` | — | store engine override: sqlite or postgres (profile default: postgres for postgres-prod, sqlite otherwise) |
| `--force` | `bool` | `false` | overwrite --out if it already exists |
| `--grpc-client-ca` | `string` | — | PEM bundle of CAs for collector mTLS |
| `--grpc-listen` | `string` | `127.0.0.1:8444` | gRPC listen address |
| `--insecure` | `bool` | `false` | serve plaintext (loopback dev only) |
| `--known-regions` | `stringSlice` | `[]` | deployment-wide region codes (comma-separated; home region added implicitly) |
| `--license` | `string` | — | path to a commercial license file |
| `--listen` | `string` | `127.0.0.1:8443` | HTTP (REST + console) listen address |
| `--max-conns` | `int` | `0` | OLIVARES_DB_MAX_CONNS — Postgres app-pool cap per node (0 = engine default) |
| `--out` | `string` | `-` | output path (default - = stdout); for systemd use /etc/olivares/olivares.env |
| `--owner-dsn` | `string` | — | owner-role DSN or reference (enables the least-privilege owner/app split) |
| `--profile` | `string` | `single-node-prod` | install profile: eval \| single-node-prod \| postgres-prod \| k8s |
| `--region` | `string` | — | data-residency home region of this instance (e.g. eu) |
| `--tls-cert` | `string` | — | TLS certificate PEM path (with --tls-key) |
| `--tls-key` | `string` | — | TLS private key PEM path (with --tls-cert) |

#### Command: olivares config validate

Validate configured OLIVARES_* environment keys

```
olivares config validate
```

Declares no flags of its own; it takes those of [`olivares config`](#command-olivares-config) and the root command.

#### Command: olivares connector

Scaffold out-of-tree connector projects

```
olivares connector
```

Declares no flags of its own; it takes those of [`olivares`](#command-olivares) and the root command.

#### Command: olivares connector init

Generate a connector repository from an archetype template

```
olivares connector init <name>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--dir` | `string` | — | target directory (default ./&lt;connector part&gt;; non-empty dirs are refused) |
| `--module` | `string` | — | **required**. Go module path of the generated repository |
| `--plugin` | `bool` | `true` | emit cmd/&lt;vendor-connector&gt;/main.go and the sdk/plugin dependency |
| `--sdk-path` | `string` | — | DEV: path to a local checkout of the upstream repo's sdk/ for replace directives |
| `--template` | `string` | — | **required**. archetype template: content-source \| access-edge-source \| output-sink \| agent-surface \| model-provider |

#### Command: olivares consoleviews

Manage saved console views (filter and parameter sets)

```
olivares consoleviews
```

Aliases: `views`

| Flag | Type | Default | Description |
|---|---|---|---|
| `--allow-cleartext` | `bool` | `false` | **inherited**. allow sending the credential to a non-loopback host over plain HTTP (DANGEROUS: it travels readable) |
| `--ca-cert` | `string` | — | **inherited**. PEM file containing an additional trusted root CA (default: current context) |
| `--insecure` | `bool` | `false` | **inherited**. skip TLS certificate verification (DANGEROUS; development only) |
| `--pin-sha256` | `stringArray` | `[]` | **inherited**. trusted leaf SPKI SHA-256 pin, base64 or hex, repeatable — the engine prints it as pin_sha256 on the line reporting its certificate (default: current context) |
| `--server` | `string` | — | **inherited**. control-plane base URL (default $OLIVARES_SERVER_URL, then current context) |
| `--tenant` | `string` | — | **inherited**. tenant id (default $OLIVARES_TENANT, then current context) |
| `--timeout` | `duration` | `10s` | **inherited**. request timeout |
| `--token` | `string` | — | **inherited**. API bearer token (prefer --token-file: this form is visible in the process table and in shell history; default $OLIVARES_TOKEN, then current context) |
| `--token-file` | `string` | — | **inherited**. read the API bearer token from a file, or - for stdin |

#### Command: olivares consoleviews create

Save a new view

```
olivares consoleviews create
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--description` | `string` | — | an optional description |
| `--feature-id` | `string` | — | the console feature this view belongs to (lowercase slug, required) |
| `--name` | `string` | — | the view's name, unique per feature and owner (required) |
| `--params` | `string` | — | the view's parameters as a JSON object |
| `--params-file` | `string` | — | read the parameters JSON from a file; `-` reads stdin |
| `--shared` | `bool` | `false` | make the view visible to the whole tenant (only you can still change it) |

#### Command: olivares consoleviews get

Show one saved view in full

```
olivares consoleviews get <view-id>
```

Declares no flags of its own; it takes those of [`olivares consoleviews`](#command-olivares-consoleviews) and the root command.

#### Command: olivares consoleviews ls

List the views you can see

```
olivares consoleviews ls
```

Aliases: `list`

| Flag | Type | Default | Description |
|---|---|---|---|
| `--feature-id` | `string` | — | only views belonging to this console feature |

#### Command: olivares consoleviews rm

Delete your own saved view

```
olivares consoleviews rm <view-id>
```

Aliases: `delete`, `remove`

| Flag | Type | Default | Description |
|---|---|---|---|
| `-y`, `--yes` | `bool` | `false` | proceed without the confirmation prompt (required in a non-interactive session) |

#### Command: olivares consoleviews update

Replace the writable fields of your own view

```
olivares consoleviews update <view-id>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--description` | `string` | — | the description; omitting it CLEARS the stored one |
| `--name` | `string` | — | the view's name (required: this is a replace, not a patch) |
| `--params` | `string` | — | the view's parameters as a JSON object |
| `--params-file` | `string` | — | read the parameters JSON from a file; `-` reads stdin |
| `--shared` | `bool` | `false` | share with the tenant; omitting it makes the view private again |

#### Command: olivares db

Prepare and verify the database before serving (Postgres roles, RLS posture)

```
olivares db
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--format` | `string` | `text` | **inherited**. deprecated alias for -o/--output on this command (text or json) — NOT the export-format flag of 'audit export' / 'findings export' |

#### Command: olivares db check

Probe a DSN's role posture and report whether the engine will accept it (read-only)

```
olivares db check
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--admin-dsn` | `string` | — | cross-tenant admin-role DSN to probe (must be BYPASSRLS, NOT a superuser). Accepts a file:/env: reference |
| `--dsn` | `string` | — | application-role DSN to probe (must be NOSUPERUSER NOBYPASSRLS). Accepts a file:/env: reference |
| `--engine` | `string` | `postgres` | store engine the DSNs target: postgres or sqlite |
| `--owner-dsn` | `string` | — | owner-role DSN to probe (must be NOSUPERUSER NOBYPASSRLS). Accepts a file:/env: reference |
| `--strict` | `bool` | `false` | exit non-zero if any DSN would be refused at boot (pre-flight gate) |

#### Command: olivares db init

Provision the least-privilege Postgres roles + database idempotently (no psql by hand)

```
olivares db init
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--admin-password` | `string` | — | admin role password (prefer --admin-password-file) |
| `--admin-password-file` | `string` | — | read the admin role password from a file, or - for stdin |
| `--admin-role` | `string` | — | cross-tenant admin role for --admin-dsn (NOSUPERUSER BYPASSRLS). Empty = not provisioned |
| `--app-password` | `string` | — | application role password (prefer --app-password-file) |
| `--app-password-file` | `string` | — | read the application role password from a file, or - for stdin |
| `--app-role` | `string` | `olivares_app` | application role (runtime traffic; NOSUPERUSER NOBYPASSRLS) |
| `--database` | `string` | `olivares` | application database name to create/own |
| `--owner-password` | `string` | — | owner role password (prefer --owner-password-file) |
| `--owner-password-file` | `string` | — | read the owner role password from a file, or - for stdin |
| `--owner-role` | `string` | — | SEPARATE owner role that owns the schema and runs DDL (enables the least-privilege split). Empty = the app role owns the schema. Use on a FRESH database; adopting the split on an existing single-role db needs a manual REASSIGN OWNED first (see deploy/postgres/README.md) |
| `--print-sql` | `bool` | `false` | print the provisioning SQL (passwords redacted) and exit, without connecting |
| `--sslmode` | `string` | `verify-full` | libpq sslmode for the printed DSN hints |
| `--superuser-dsn` | `string` | — | superuser / maintenance DSN used ONLY to provision (e.g. postgres://postgres@host:5432/postgres). Accepts a file:/env: reference |

#### Command: olivares ddil

Air-gap DDIL bundles: export, verify and import governance state across a disconnected gap

```
olivares ddil
```

Declares no flags of its own; it takes those of [`olivares`](#command-olivares) and the root command.

#### Command: olivares ddil export

Assemble and sign a DDIL bundle from the local governance store

```
olivares ddil export
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--data-dir` | `string` | — | data directory (default $OLIVARES_DATA_DIR, an existing ./olivares-data, else $XDG_DATA_HOME/olivares or ~/.local/share/olivares) |
| `--dsn` | `string` | — | store DSN (default a SQLite file in the data dir) |
| `--engine` | `string` | `sqlite` | store engine: sqlite or postgres |
| `--evidence` | `stringArray` | `[]` | evidence file as name=path (repeatable) |
| `--expires` | `duration` | `0s` | bundle lifetime from creation (zero means no expiry) |
| `--from-seq` | `int64` | `1` | first ledger sequence number to include |
| `--max-staleness` | `duration` | `0s` | per-tenant policy freshness bound carried with the snapshot |
| `--no-policy` | `bool` | `false` | omit the active policy snapshot plane |
| `--notes` | `string` | — | optional bundle notes |
| `--out` | `string` | — | **required**. output DDIL bundle file |
| `--segment-events` | `int` | `10000` | maximum events per audit segment |
| `--sign-key` | `string` | — | **required**. Ed25519 private key (base64 key/seed, or @file) |
| `--tenant` | `string` | — | tenant id to export (default $OLIVARES_TENANT) |

#### Command: olivares ddil import

Verify, reconcile and apply a DDIL courier bundle fail-closed

```
olivares ddil import
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--audit-out` | `string` | — | local WORM archive directory for carried audit segments |
| `--bundle` | `string` | — | **required**. DDIL courier bundle file |
| `--checkpoint-pubkey` | `stringArray` | `[]` | checkpoint public key pin for staged archive verification (repeatable; raw Ed25519 or &lt;alg&gt;:&lt;base64 DER SPKI&gt;) |
| `--data-dir` | `string` | — | data directory (default $OLIVARES_DATA_DIR, an existing ./olivares-data, else $XDG_DATA_HOME/olivares or ~/.local/share/olivares) |
| `--dsn` | `string` | — | store DSN (default a SQLite file in the data dir) |
| `--engine` | `string` | `sqlite` | store engine: sqlite or postgres |
| `--event-pubkey` | `stringArray` | `[]` | per-event Ed25519 public key pin for staged archive verification (repeatable), optionally epoch-FENCED as "&lt;base64&gt;@&lt;last_seq&gt;" or "&lt;base64&gt;@&lt;lo&gt;:&lt;hi&gt;"; a bare key is the current generation (pin every retired generation with its boundary to fence it) |
| `--evidence-out` | `string` | — | directory under which carried evidence is extracted read-only |
| `--json` | `bool` | `false` | deprecated alias for -o json |
| `--pubkey` | `string` | — | **required**. pinned raw Ed25519 bundle public key (base64, or @file) |
| `--tenant` | `string` | — | tenant that is allowed to receive the bundle (default $OLIVARES_TENANT) |

#### Command: olivares ddil keygen

Generate an Ed25519 DDIL transport keypair

```
olivares ddil keygen
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--out` | `string` | — | write the base64 private seed to this 0600 file |

#### Command: olivares ddil verify

Verify and inspect a DDIL courier bundle without applying it

```
olivares ddil verify
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--bundle` | `string` | — | **required**. DDIL courier bundle file |
| `--json` | `bool` | `false` | deprecated alias for -o json |
| `--pubkey` | `string` | — | **required**. pinned raw Ed25519 public key (base64, or @file) |

#### Command: olivares deploy

Declare, plan, apply, retire and roll back governed agent deployments

```
olivares deploy
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--allow-cleartext` | `bool` | `false` | **inherited**. allow sending the credential to a non-loopback host over plain HTTP (DANGEROUS: it travels readable) |
| `--ca-cert` | `string` | — | **inherited**. PEM file containing an additional trusted root CA (default: current context) |
| `--insecure` | `bool` | `false` | **inherited**. skip TLS certificate verification (DANGEROUS; development only) |
| `--pin-sha256` | `stringArray` | `[]` | **inherited**. trusted leaf SPKI SHA-256 pin, base64 or hex, repeatable — the engine prints it as pin_sha256 on the line reporting its certificate (default: current context) |
| `--server` | `string` | — | **inherited**. control-plane base URL (default $OLIVARES_SERVER_URL, then current context) |
| `--tenant` | `string` | — | **inherited**. tenant id (default $OLIVARES_TENANT, then current context) |
| `--timeout` | `duration` | `10s` | **inherited**. request timeout |
| `--token` | `string` | — | **inherited**. API bearer token (prefer --token-file: this form is visible in the process table and in shell history; default $OLIVARES_TOKEN, then current context) |
| `--token-file` | `string` | — | **inherited**. read the API bearer token from a file, or - for stdin |

#### Command: olivares deploy apply

Actuate the current version through the approval gate (two-phase)

```
olivares deploy apply <id>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--approval-ref` | `string` | — | phase 2: the approval that authorizes this apply |

#### Command: olivares deploy definitions

Declare and version deployment definitions

```
olivares deploy definitions
```

Declares no flags of its own; it takes those of [`olivares deploy`](#command-olivares-deploy) and the root command.

#### Command: olivares deploy definitions create

Declare a deployment definition from a JSON spec

```
olivares deploy definitions create
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--environment` | `string` | — | **required**. target environment (required) |
| `--name` | `string` | — | **required**. deployment name (required) |
| `--runtime` | `string` | — | **required**. the runtime kind (required) |
| `--source-ref` | `string` | — | provenance reference for the spec, e.g. a commit |
| `--spec-file` | `string` | — | **required**. JSON spec object, '-' for stdin (required) |
| `--subject-kind` | `string` | `agent` | what is being deployed |
| `--subject-ref` | `string` | — | **required**. the subject's reference (required) |
| `--target` | `string` | — | **required**. the runtime target this deploys onto (required) |

#### Command: olivares deploy definitions get

Show one definition with its current spec and real state

```
olivares deploy definitions get <id>
```

Declares no flags of its own; it takes those of [`olivares deploy definitions`](#command-olivares-deploy-definitions) and the root command.

#### Command: olivares deploy definitions ls

List deployment definitions with their drift

```
olivares deploy definitions ls
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--cursor` | `string` | — | continue from the cursor a previous page reported |
| `--limit` | `int` | `0` | page size (0 uses the engine's default) |

#### Command: olivares deploy definitions revisions

List a definition's revision history

```
olivares deploy definitions revisions <id>
```

Declares no flags of its own; it takes those of [`olivares deploy definitions`](#command-olivares-deploy-definitions) and the root command.

#### Command: olivares deploy definitions rm

Delete a definition and its revisions (destructive; needs --yes when unattended)

```
olivares deploy definitions rm <id>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `-y`, `--yes` | `bool` | `false` | proceed without the confirmation prompt (required in a non-interactive session) |

#### Command: olivares deploy definitions update

Publish a new revision of a definition (PUT)

```
olivares deploy definitions update <id>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--note` | `string` | — | why this revision exists (recorded on the revision) |
| `--source-ref` | `string` | — | provenance reference for this revision |
| `--spec-file` | `string` | — | **required**. JSON spec object, '-' for stdin (required) |
| `--target` | `string` | — | retarget the deployment |

#### Command: olivares deploy operations

List the append-only ledger of plan/apply/retire/rollback operations

```
olivares deploy operations
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--cursor` | `string` | — | continue from the cursor a previous page reported |
| `--definition-id` | `string` | — | only operations on this definition |
| `--limit` | `int` | `0` | page size (0 uses the engine's default) |
| `--op` | `string` | — | only operations of this kind |
| `--status` | `string` | — | only operations in this status |

#### Command: olivares deploy plan

Compute the change set an apply WOULD make (nothing is actuated)

```
olivares deploy plan <id>
```

Declares no flags of its own; it takes those of [`olivares deploy`](#command-olivares-deploy) and the root command.

#### Command: olivares deploy retire

Take a live deployment down (destructive POST; needs --yes when unattended)

```
olivares deploy retire <id>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--approval-ref` | `string` | — | phase 2: the approval that authorizes this retire |
| `-y`, `--yes` | `bool` | `false` | proceed without the confirmation prompt (required in a non-interactive session) |

#### Command: olivares deploy rollback

Revert a definition to an earlier version (destructive POST; needs --yes when unattended)

```
olivares deploy rollback <id>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--note` | `string` | — | why the rollback happened (recorded on the revision) |
| `--to-version` | `int64` | `0` | **required**. the revision number to restore (required) |
| `-y`, `--yes` | `bool` | `false` | proceed without the confirmation prompt (required in a non-interactive session) |

#### Command: olivares deploy verify

Check the real deployment against its declared spec

```
olivares deploy verify <id>
```

Declares no flags of its own; it takes those of [`olivares deploy`](#command-olivares-deploy) and the root command.

#### Command: olivares deploy wirings

List what each deployment is wired to, and how that was attributed

```
olivares deploy wirings
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--cursor` | `string` | — | continue from the cursor a previous page reported |
| `--definition-id` | `string` | — | only wirings of this definition |
| `--limit` | `int` | `0` | page size (0 uses the engine's default) |
| `--status` | `string` | — | only wirings in this status |

#### Command: olivares dr

Disaster recovery: ledger-continuity-safe backup and restore

```
olivares dr
```

Declares no flags of its own; it takes those of [`olivares`](#command-olivares) and the root command.

#### Command: olivares dr backup

Write a ledger-continuity-safe DR bundle

```
olivares dr backup
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--admin-dsn` | `string` | — | Postgres only: NOSUPERUSER BYPASSRLS role DSN. REQUIRED to run pg_dump directly (it keeps row_security=off and ABORTS as the application role under FORCE RLS); also used for the cross-tenant org list, without which a backup may MISS tenants — see deploy/postgres/01-app-role.sql |
| `--allow-unverified` | `bool` | `false` | capture even if a tenant chain fails verification at backup time (NOT recommended) |
| `--data-dir` | `string` | — | data directory (default $OLIVARES_DATA_DIR, an existing ./olivares-data, else $XDG_DATA_HOME/olivares or ~/.local/share/olivares) |
| `--dsn` | `string` | — | store DSN (default a SQLite file in the data dir) |
| `--engine` | `string` | `sqlite` | store engine: sqlite or postgres |
| `--gfs-daily` | `int` | `0` | GFS retention: keep the newest bundle of each of the last N days (0 = tier off) |
| `--gfs-keep-last` | `int` | `0` | GFS retention: always keep the N newest bundles regardless of period |
| `--gfs-monthly` | `int` | `0` | GFS retention: keep the newest bundle of each of the last N months |
| `--gfs-weekly` | `int` | `0` | GFS retention: keep the newest bundle of each of the last N ISO weeks |
| `--gfs-yearly` | `int` | `0` | GFS retention: keep the newest bundle of each of the last N years |
| `--kek-key-file` | `string` | — | file holding a raw/base64 32-byte key-encryption key (the KMS-unwrapped path); or $OLIVARES_DR_KEK_FILE |
| `--notes` | `string` | — | free-form operator note recorded in the manifest (no secrets) |
| `--offsite-access-key-id-file` | `string` | — | file holding the offsite access key id (credential by reference; falls back to $AWS_ACCESS_KEY_ID) |
| `--offsite-bucket` | `string` | — | offsite bucket for DR bundles (set to enable offsite replication) |
| `--offsite-endpoint` | `string` | — | S3-compatible endpoint for offsite replication (R2/MinIO/Wasabi); empty = AWS S3 from --offsite-region |
| `--offsite-path-style` | `bool` | `false` | force path-style S3 addressing (implied by a custom --offsite-endpoint) |
| `--offsite-prefix` | `string` | — | key prefix within the offsite bucket |
| `--offsite-region` | `string` | — | offsite region (default us-east-1; Cloudflare R2 uses 'auto') |
| `--offsite-secret-access-key-file` | `string` | — | file holding the offsite secret access key (credential by reference; falls back to $AWS_SECRET_ACCESS_KEY) |
| `--offsite-session-token-file` | `string` | — | optional file holding an STS session token (falls back to $AWS_SESSION_TOKEN) |
| `--out` | `string` | — | **required**. path to write the DR bundle to (required) |
| `--passphrase-file` | `string` | — | file holding the backup passphrase (Argon2id-derived KEK); or $OLIVARES_DR_PASSPHRASE_FILE |
| `--pg-dump` | `string` | `pg_dump` | pg_dump executable (Postgres engine only) |
| `--pitr-ref` | `string` | — | Postgres only: build a keys+manifest companion bundle for a point-in-time-recovery archive (no store bytes); the value is a human pointer to the WAL archive |
| `--retain-days` | `int` | `0` | after a successful write, prune sibling *.drbundle files older than N days in the --out directory (0 = keep all). The offsite mirror keeps longer (3-2-1) |
| `--snapshot-file` | `string` | — | Postgres only: use this pre-made dump as the store snapshot instead of running pg_dump (e.g. produced by a postgres-client sidecar) |

#### Command: olivares dr drill

Full DR round-trip drill (backup→destroy→restore→verify) with a measured RTO

```
olivares dr drill
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--events` | `int` | `500` | ledger events to seed in the ephemeral estate |
| `--keep-artifacts` | `bool` | `false` | keep the scratch dir instead of removing it (debugging) |

#### Command: olivares dr inspect

Print a DR bundle's manifest (no KEK needed; no secrets shown)

```
olivares dr inspect
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--in` | `string` | — | **required**. DR bundle to inspect (required) |

#### Command: olivares dr ls

List DR bundles (local, or --offsite for the S3/R2 mirror)

```
olivares dr ls
```

Aliases: `list`

| Flag | Type | Default | Description |
|---|---|---|---|
| `--dir` | `string` | — | local backup directory (default &lt;data-dir&gt;/backups) |
| `--offsite` | `bool` | `false` | list the offsite mirror instead of the local directory |
| `--offsite-access-key-id-file` | `string` | — | file holding the offsite access key id (credential by reference; falls back to $AWS_ACCESS_KEY_ID) |
| `--offsite-bucket` | `string` | — | offsite bucket for DR bundles (set to enable offsite replication) |
| `--offsite-endpoint` | `string` | — | S3-compatible endpoint for offsite replication (R2/MinIO/Wasabi); empty = AWS S3 from --offsite-region |
| `--offsite-path-style` | `bool` | `false` | force path-style S3 addressing (implied by a custom --offsite-endpoint) |
| `--offsite-prefix` | `string` | — | key prefix within the offsite bucket |
| `--offsite-region` | `string` | — | offsite region (default us-east-1; Cloudflare R2 uses 'auto') |
| `--offsite-secret-access-key-file` | `string` | — | file holding the offsite secret access key (credential by reference; falls back to $AWS_SECRET_ACCESS_KEY) |
| `--offsite-session-token-file` | `string` | — | optional file holding an STS session token (falls back to $AWS_SESSION_TOKEN) |

#### Command: olivares dr pull

Download a DR bundle from the offsite S3/R2 target

```
olivares dr pull
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--name` | `string` | — | **required**. offsite bundle name to download (required) |
| `--offsite-access-key-id-file` | `string` | — | file holding the offsite access key id (credential by reference; falls back to $AWS_ACCESS_KEY_ID) |
| `--offsite-bucket` | `string` | — | offsite bucket for DR bundles (set to enable offsite replication) |
| `--offsite-endpoint` | `string` | — | S3-compatible endpoint for offsite replication (R2/MinIO/Wasabi); empty = AWS S3 from --offsite-region |
| `--offsite-path-style` | `bool` | `false` | force path-style S3 addressing (implied by a custom --offsite-endpoint) |
| `--offsite-prefix` | `string` | — | key prefix within the offsite bucket |
| `--offsite-region` | `string` | — | offsite region (default us-east-1; Cloudflare R2 uses 'auto') |
| `--offsite-secret-access-key-file` | `string` | — | file holding the offsite secret access key (credential by reference; falls back to $AWS_SECRET_ACCESS_KEY) |
| `--offsite-session-token-file` | `string` | — | optional file holding an STS session token (falls back to $AWS_SESSION_TOKEN) |
| `--out` | `string` | — | **required**. local path to write the bundle to (required) |

#### Command: olivares dr push

Upload an existing DR bundle to the offsite S3/R2 target

```
olivares dr push
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--in` | `string` | — | **required**. local DR bundle to upload (required) |
| `--offsite-access-key-id-file` | `string` | — | file holding the offsite access key id (credential by reference; falls back to $AWS_ACCESS_KEY_ID) |
| `--offsite-bucket` | `string` | — | offsite bucket for DR bundles (set to enable offsite replication) |
| `--offsite-endpoint` | `string` | — | S3-compatible endpoint for offsite replication (R2/MinIO/Wasabi); empty = AWS S3 from --offsite-region |
| `--offsite-path-style` | `bool` | `false` | force path-style S3 addressing (implied by a custom --offsite-endpoint) |
| `--offsite-prefix` | `string` | — | key prefix within the offsite bucket |
| `--offsite-region` | `string` | — | offsite region (default us-east-1; Cloudflare R2 uses 'auto') |
| `--offsite-secret-access-key-file` | `string` | — | file holding the offsite secret access key (credential by reference; falls back to $AWS_SECRET_ACCESS_KEY) |
| `--offsite-session-token-file` | `string` | — | optional file holding an STS session token (falls back to $AWS_SESSION_TOKEN) |

#### Command: olivares dr restore

Restore a DR bundle and verify ledger continuity (non-zero exit if not safe)

```
olivares dr restore
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--admin-dsn` | `string` | — | Postgres only: NOSUPERUSER BYPASSRLS role DSN. REQUIRED to run pg_dump directly (it keeps row_security=off and ABORTS as the application role under FORCE RLS); also used for the cross-tenant org list, without which a backup may MISS tenants — see deploy/postgres/01-app-role.sql |
| `--data-dir` | `string` | — | data directory (default $OLIVARES_DATA_DIR, an existing ./olivares-data, else $XDG_DATA_HOME/olivares or ~/.local/share/olivares) |
| `--dsn` | `string` | — | store DSN (default a SQLite file in the data dir) |
| `--engine` | `string` | `sqlite` | store engine: sqlite or postgres |
| `--force` | `bool` | `false` | overwrite existing keys / store file in the data dir |
| `--in` | `string` | — | **required**. DR bundle to restore (required) |
| `--in-place` | `bool` | `false` | replace a LIVE data dir safely: stage + verify BEFORE promoting, auto-preserving the current store/keys as *.pre-restore-&lt;ts&gt; (sqlite only) |
| `--kek-key-file` | `string` | — | file holding a raw/base64 32-byte key-encryption key (the KMS-unwrapped path); or $OLIVARES_DR_KEK_FILE |
| `--operator` | `string` | — | who is performing this restore (required when the restore REPLACES an existing estate; recorded in the restored ledger). A declaration, not an authentication: the console's dual-control gate does not reach this path |
| `--passphrase-file` | `string` | — | file holding the backup passphrase (Argon2id-derived KEK); or $OLIVARES_DR_PASSPHRASE_FILE |
| `--pg-restore` | `string` | `pg_restore` | pg_restore executable (Postgres engine only) |
| `--reason` | `string` | — | why this restore is being performed — an incident id or change reference (required when the restore REPLACES an existing estate; recorded in the restored ledger) |

#### Command: olivares dr verify

Test a DR bundle WITHOUT touching the live data dir (the DR drill)

```
olivares dr verify
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--in` | `string` | — | **required**. DR bundle to verify (required) |
| `--kek-key-file` | `string` | — | file holding a raw/base64 32-byte key-encryption key (the KMS-unwrapped path); or $OLIVARES_DR_KEK_FILE |
| `--passphrase-file` | `string` | — | file holding the backup passphrase (Argon2id-derived KEK); or $OLIVARES_DR_PASSPHRASE_FILE |

#### Command: olivares evals

Eval methodology tools: the CI regression gate and the judge-calibration labeler

```
olivares evals
```

Declares no flags of its own; it takes those of [`olivares`](#command-olivares) and the root command.

#### Command: olivares evals gate

Run the CI regression gate (exit 0 pass/warn, 1 fail) or re-check one after a governed override

```
olivares evals gate
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--baseline` | `string` | — | explicit baseline run id (default: pinned baseline or latest prior run) |
| `--ca-cert` | `string` | — | PEM CA bundle used to verify the control plane |
| `--check-id` | `string` | — | re-check an existing gate id (after a governed override) |
| `--insecure` | `bool` | `false` | skip TLS certificate verification (self-signed dev planes only) |
| `--json` | `bool` | `false` | deprecated alias for -o json |
| `--outputs` | `string` | — | JSON file mapping case_key → candidate output ('-' = stdin) |
| `--pin-sha256` | `stringArray` | `[]` | pinned leaf SPKI SHA-256, base64 or hex (repeatable) — the engine prints it as pin_sha256 on the line reporting its certificate |
| `--sample-size` | `int` | `0` | judge at most N cases (deterministic subset; 0 = all) |
| `--seed` | `string` | — | deterministic sample seed (default: derived from the suite version) |
| `--server` | `string` | — | control-plane base URL (default $OLIVARES_SERVER_URL) |
| `--subject` | `string` | — | subject ref (e.g. the agent/model under test) |
| `--subject-kind` | `string` | — | subject kind (defaults to the suite's) |
| `--suite` | `string` | — | suite id to gate against |
| `--tenant` | `string` | — | tenant id (default $OLIVARES_TENANT) |
| `--timeout` | `duration` | `10m0s` | request timeout (a judged gate can take a while) |
| `--token` | `string` | — | API bearer token (default $OLIVARES_TOKEN) |

#### Command: olivares evals label

Guided human-labeling session for the judge↔human calibration set

```
olivares evals label
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--ca-cert` | `string` | — | PEM CA bundle used to verify the control plane |
| `--criterion` | `string` | — | default criterion for items that carry none |
| `--in` | `string` | — | JSONL file of candidate items to label |
| `--insecure` | `bool` | `false` | skip TLS certificate verification (self-signed dev planes only) |
| `--pin-sha256` | `stringArray` | `[]` | pinned leaf SPKI SHA-256, base64 or hex (repeatable) — the engine prints it as pin_sha256 on the line reporting its certificate |
| `--server` | `string` | — | control-plane base URL (default $OLIVARES_SERVER_URL) |
| `--set` | `string` | `default` | calibration set name |
| `--tenant` | `string` | — | tenant id (default $OLIVARES_TENANT) |
| `--timeout` | `duration` | `10m0s` | request timeout (a judged gate can take a while) |
| `--token` | `string` | — | API bearer token (default $OLIVARES_TOKEN) |

#### Command: olivares eventing

Manage the eventing platform (webhook event subscriptions, deliveries, event log)

```
olivares eventing
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--format` | `string` | `text` | **inherited**. deprecated alias for -o/--output on this command (text or json) — NOT the export-format flag of 'audit export' / 'findings export' |

#### Command: olivares eventing dead-letters

Inspect and redeliver dead-lettered deliveries

```
olivares eventing dead-letters
```

Declares no flags of its own; it takes those of [`olivares eventing`](#command-olivares-eventing) and the root command.

#### Command: olivares eventing dead-letters ls

List dead-lettered deliveries (status=dead)

```
olivares eventing dead-letters ls
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--data-dir` | `string` | — | data directory (default $OLIVARES_DATA_DIR, an existing ./olivares-data, else $XDG_DATA_HOME/olivares or ~/.local/share/olivares) |
| `--dsn` | `string` | — | store DSN (default a SQLite file in the data dir) |
| `--engine` | `string` | `sqlite` | store engine: sqlite or postgres |
| `--subscription` | `string` | — | filter by subscription id |
| `--tenant` | `string` | — | tenant id (default $OLIVARES_TENANT) |

#### Command: olivares eventing dead-letters redeliver

Requeue a dead-lettered delivery for retry

```
olivares eventing dead-letters redeliver
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--data-dir` | `string` | — | data directory (default $OLIVARES_DATA_DIR, an existing ./olivares-data, else $XDG_DATA_HOME/olivares or ~/.local/share/olivares) |
| `--dsn` | `string` | — | store DSN (default a SQLite file in the data dir) |
| `--engine` | `string` | `sqlite` | store engine: sqlite or postgres |
| `--id` | `string` | — | **required**. delivery id to redeliver |
| `--tenant` | `string` | — | tenant id (default $OLIVARES_TENANT) |

#### Command: olivares eventing deliveries

Inspect delivery state (ls)

```
olivares eventing deliveries
```

Declares no flags of its own; it takes those of [`olivares eventing`](#command-olivares-eventing) and the root command.

#### Command: olivares eventing deliveries ls

List deliveries (optionally filtered by --subscription, --status)

```
olivares eventing deliveries ls
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--data-dir` | `string` | — | data directory (default $OLIVARES_DATA_DIR, an existing ./olivares-data, else $XDG_DATA_HOME/olivares or ~/.local/share/olivares) |
| `--dsn` | `string` | — | store DSN (default a SQLite file in the data dir) |
| `--engine` | `string` | `sqlite` | store engine: sqlite or postgres |
| `--status` | `string` | — | filter by status (queued\|delivering\|delivered\|dead\|denied) |
| `--subscription` | `string` | — | filter by subscription id |
| `--tenant` | `string` | — | tenant id (default $OLIVARES_TENANT) |

#### Command: olivares eventing egress

Inspect and actuate the egress destination control's rollout

```
olivares eventing egress
```

Declares no flags of its own; it takes those of [`olivares eventing`](#command-olivares-eventing) and the root command.

#### Command: olivares eventing egress actuate

Apply a deliberate rollout decision for the egress destination control

```
olivares eventing egress actuate
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--accept-blocked` | `bool` | `false` | proceed even though listed destinations stop delivering |
| `--accept-unfenced` | `bool` | `false` | proceed with the egress writer fence dormant, so nothing enforces --assert-writers-upgraded |
| `--actor` | `string` | — | who is deciding (default: $OLIVARES_ACTOR or the OS user) |
| `--admin-dsn` | `string` | — | Postgres: the dedicated BYPASSRLS role, required to enumerate every tenant |
| `--assert-writers-upgraded` | `bool` | `false` | assert that every node able to author a subscription runs a binary carrying this control — required |
| `--data-dir` | `string` | — | data directory (default $OLIVARES_DATA_DIR, an existing ./olivares-data, else $XDG_DATA_HOME/olivares or ~/.local/share/olivares) |
| `--dsn` | `string` | — | store DSN (default a SQLite file in the data dir) |
| `--engine` | `string` | `sqlite` | store engine: sqlite or postgres |
| `--mode` | `string` | — | enforced \| policy_optional |
| `--owner-dsn` | `string` | — | Postgres: the owner role, required in a split-role deployment (the app role has no schema CREATE) |
| `--reason` | `string` | — | why (a change ticket reference belongs here) — required |

#### Command: olivares eventing egress status

Report the rollout disposition and what enforcing would block

```
olivares eventing egress status
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--admin-dsn` | `string` | — | Postgres: the dedicated BYPASSRLS role, required to enumerate every tenant |
| `--data-dir` | `string` | — | data directory (default $OLIVARES_DATA_DIR, an existing ./olivares-data, else $XDG_DATA_HOME/olivares or ~/.local/share/olivares) |
| `--dsn` | `string` | — | store DSN (default a SQLite file in the data dir) |
| `--engine` | `string` | `sqlite` | store engine: sqlite or postgres |
| `--json` | `bool` | `false` | deprecated alias for -o json |
| `--owner-dsn` | `string` | — | Postgres: the owner role, required in a split-role deployment (the app role has no schema CREATE) |

#### Command: olivares eventing events

Inspect the captured event log

```
olivares eventing events
```

Declares no flags of its own; it takes those of [`olivares eventing`](#command-olivares-eventing) and the root command.

#### Command: olivares eventing events ls

List captured events (optionally from a seq cursor, filtered by --type)

```
olivares eventing events ls
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--data-dir` | `string` | — | data directory (default $OLIVARES_DATA_DIR, an existing ./olivares-data, else $XDG_DATA_HOME/olivares or ~/.local/share/olivares) |
| `--dsn` | `string` | — | store DSN (default a SQLite file in the data dir) |
| `--engine` | `string` | `sqlite` | store engine: sqlite or postgres |
| `--since-seq` | `int64` | `0` | list events with seq &gt;= this value |
| `--tenant` | `string` | — | tenant id (default $OLIVARES_TENANT) |
| `--type` | `string` | — | filter by event type |

#### Command: olivares eventing fence

Inspect, arm and verify the cross-version egress writer fence

```
olivares eventing fence
```

Declares no flags of its own; it takes those of [`olivares eventing`](#command-olivares-eventing) and the root command.

#### Command: olivares eventing fence arm

Require every writer to prove it carries the egress gate

```
olivares eventing fence arm
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--actor` | `string` | — | who is deciding (default: $OLIVARES_ACTOR or the OS user) |
| `--admin-dsn` | `string` | — | Postgres: the dedicated BYPASSRLS role |
| `--assert-writers-upgraded` | `bool` | `false` | acknowledge that arming makes an un-upgraded authoring node fail — required |
| `--data-dir` | `string` | — | data directory (default $OLIVARES_DATA_DIR, an existing ./olivares-data, else $XDG_DATA_HOME/olivares or ~/.local/share/olivares) |
| `--dsn` | `string` | — | store DSN (default a SQLite file in the data dir) |
| `--engine` | `string` | `sqlite` | store engine: sqlite or postgres |
| `--owner-dsn` | `string` | — | Postgres: the owner role, required in a split-role deployment (the app role has no schema CREATE) |
| `--reason` | `string` | — | why (a change ticket reference belongs here) — required |

#### Command: olivares eventing fence status

Report the writer fence's posture and whether the database enforces it

```
olivares eventing fence status
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--admin-dsn` | `string` | — | Postgres: the dedicated BYPASSRLS role |
| `--data-dir` | `string` | — | data directory (default $OLIVARES_DATA_DIR, an existing ./olivares-data, else $XDG_DATA_HOME/olivares or ~/.local/share/olivares) |
| `--dsn` | `string` | — | store DSN (default a SQLite file in the data dir) |
| `--engine` | `string` | `sqlite` | store engine: sqlite or postgres |
| `--json` | `bool` | `false` | emit JSON |
| `--owner-dsn` | `string` | — | Postgres: the owner role, required in a split-role deployment (the app role has no schema CREATE) |

#### Command: olivares eventing fence verify

Fail unless the database is actually enforcing an armed writer fence

```
olivares eventing fence verify
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--admin-dsn` | `string` | — | Postgres: the dedicated BYPASSRLS role |
| `--data-dir` | `string` | — | data directory (default $OLIVARES_DATA_DIR, an existing ./olivares-data, else $XDG_DATA_HOME/olivares or ~/.local/share/olivares) |
| `--dsn` | `string` | — | store DSN (default a SQLite file in the data dir) |
| `--engine` | `string` | `sqlite` | store engine: sqlite or postgres |
| `--owner-dsn` | `string` | — | Postgres: the owner role, required in a split-role deployment (the app role has no schema CREATE) |

#### Command: olivares eventing subscriptions

Manage event subscriptions (ls, get, create, update, rotate-secret, rm, test)

```
olivares eventing subscriptions
```

Declares no flags of its own; it takes those of [`olivares eventing`](#command-olivares-eventing) and the root command.

#### Command: olivares eventing subscriptions create

Create a new event subscription

```
olivares eventing subscriptions create
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--auth-header-name` | `string` | — | custom header name (required when --auth-type=header) |
| `--auth-type` | `string` | `none` | additional auth header type: none\|bearer\|basic\|header |
| `--data-dir` | `string` | — | data directory (default $OLIVARES_DATA_DIR, an existing ./olivares-data, else $XDG_DATA_HOME/olivares or ~/.local/share/olivares) |
| `--description` | `string` | — | optional description |
| `--dsn` | `string` | — | store DSN (default a SQLite file in the data dir) |
| `--endpoint` | `string` | — | **required**. webhook endpoint URL (https required) |
| `--engine` | `string` | `sqlite` | store engine: sqlite or postgres |
| `--event-types` | `stringSlice` | `[]` | **required**. event types to subscribe to (comma-separated) |
| `--initial-interval` | `int64` | `0` | initial retry interval in seconds (0 = module default) |
| `--max-attempts` | `int64` | `0` | max delivery attempts (0 = module default) |
| `--name` | `string` | — | **required**. subscription name |
| `--role` | `string` | `viewer` | authorization role for the per-event RBAC filter (viewer\|editor\|admin\|owner) |
| `--tenant` | `string` | — | tenant id (default $OLIVARES_TENANT) |

#### Command: olivares eventing subscriptions get

Show one event subscription in full

```
olivares eventing subscriptions get
```

Aliases: `show`

| Flag | Type | Default | Description |
|---|---|---|---|
| `--data-dir` | `string` | — | data directory (default $OLIVARES_DATA_DIR, an existing ./olivares-data, else $XDG_DATA_HOME/olivares or ~/.local/share/olivares) |
| `--dsn` | `string` | — | store DSN (default a SQLite file in the data dir) |
| `--engine` | `string` | `sqlite` | store engine: sqlite or postgres |
| `--id` | `string` | — | **required**. subscription id |
| `--tenant` | `string` | — | tenant id (default $OLIVARES_TENANT) |

#### Command: olivares eventing subscriptions ls

List event subscriptions for a tenant

```
olivares eventing subscriptions ls
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--data-dir` | `string` | — | data directory (default $OLIVARES_DATA_DIR, an existing ./olivares-data, else $XDG_DATA_HOME/olivares or ~/.local/share/olivares) |
| `--dsn` | `string` | — | store DSN (default a SQLite file in the data dir) |
| `--engine` | `string` | `sqlite` | store engine: sqlite or postgres |
| `--tenant` | `string` | — | tenant id (default $OLIVARES_TENANT) |

#### Command: olivares eventing subscriptions rm

Delete an event subscription

```
olivares eventing subscriptions rm
```

Aliases: `delete`, `remove`

| Flag | Type | Default | Description |
|---|---|---|---|
| `--data-dir` | `string` | — | data directory (default $OLIVARES_DATA_DIR, an existing ./olivares-data, else $XDG_DATA_HOME/olivares or ~/.local/share/olivares) |
| `--dsn` | `string` | — | store DSN (default a SQLite file in the data dir) |
| `--engine` | `string` | `sqlite` | store engine: sqlite or postgres |
| `--id` | `string` | — | **required**. subscription id |
| `--tenant` | `string` | — | tenant id (default $OLIVARES_TENANT) |
| `-y`, `--yes` | `bool` | `false` | proceed without the confirmation prompt (required in a non-interactive session) |

#### Command: olivares eventing subscriptions rotate-secret

Reissue the signing secret for one subscription (breaks delivery until the receiver is updated)

```
olivares eventing subscriptions rotate-secret
```

Aliases: `rotate`

| Flag | Type | Default | Description |
|---|---|---|---|
| `--data-dir` | `string` | — | data directory (default $OLIVARES_DATA_DIR, an existing ./olivares-data, else $XDG_DATA_HOME/olivares or ~/.local/share/olivares) |
| `--dsn` | `string` | — | store DSN (default a SQLite file in the data dir) |
| `--engine` | `string` | `sqlite` | store engine: sqlite or postgres |
| `--id` | `string` | — | subscription id |
| `--tenant` | `string` | — | tenant id (default $OLIVARES_TENANT) |
| `-y`, `--yes` | `bool` | `false` | proceed without the confirmation prompt (required in a non-interactive session) |

#### Command: olivares eventing subscriptions test

Send a test delivery to a subscription's endpoint

```
olivares eventing subscriptions test
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--data-dir` | `string` | — | data directory (default $OLIVARES_DATA_DIR, an existing ./olivares-data, else $XDG_DATA_HOME/olivares or ~/.local/share/olivares) |
| `--dsn` | `string` | — | store DSN (default a SQLite file in the data dir) |
| `--engine` | `string` | `sqlite` | store engine: sqlite or postgres |
| `--id` | `string` | — | **required**. subscription id to test |
| `--tenant` | `string` | — | tenant id (default $OLIVARES_TENANT) |

#### Command: olivares eventing subscriptions update

Edit one event subscription in place (never reissues the secret)

```
olivares eventing subscriptions update
```

Aliases: `edit`, `set`

| Flag | Type | Default | Description |
|---|---|---|---|
| `--auth-header-name` | `string` | — | new auth header name |
| `--auth-type` | `string` | — | new auth type |
| `--data-dir` | `string` | — | data directory (default $OLIVARES_DATA_DIR, an existing ./olivares-data, else $XDG_DATA_HOME/olivares or ~/.local/share/olivares) |
| `--description` | `string` | — | new description |
| `--dsn` | `string` | — | store DSN (default a SQLite file in the data dir) |
| `--enabled` | `bool` | `true` | enable or disable delivery (--enabled=false to pause) |
| `--endpoint` | `string` | — | new https endpoint (the signing secret is NOT reissued) |
| `--engine` | `string` | `sqlite` | store engine: sqlite or postgres |
| `--event-types` | `stringSlice` | `[]` | replacement event type list |
| `--id` | `string` | — | subscription id |
| `--initial-interval` | `int64` | `0` | new initial retry interval in seconds |
| `--max-attempts` | `int64` | `0` | new maximum delivery attempts |
| `--name` | `string` | — | new subscription name |
| `--role` | `string` | — | new delivery role |
| `--tenant` | `string` | — | tenant id (default $OLIVARES_TENANT) |

#### Command: olivares findings

Export governed security findings

```
olivares findings
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--allow-cleartext` | `bool` | `false` | **inherited**. allow sending the credential to a non-loopback host over plain HTTP (DANGEROUS: it travels readable) |
| `--ca-cert` | `string` | — | **inherited**. PEM file containing an additional trusted root CA (default: current context) |
| `--insecure` | `bool` | `false` | **inherited**. skip TLS certificate verification (DANGEROUS; development only) |
| `--pin-sha256` | `stringArray` | `[]` | **inherited**. trusted leaf SPKI SHA-256 pin, base64 or hex, repeatable — the engine prints it as pin_sha256 on the line reporting its certificate (default: current context) |
| `--server` | `string` | — | **inherited**. control-plane base URL (default $OLIVARES_SERVER_URL, then current context) |
| `--tenant` | `string` | — | **inherited**. tenant id (default $OLIVARES_TENANT, then current context) |
| `--timeout` | `duration` | `10s` | **inherited**. request timeout |
| `--token` | `string` | — | **inherited**. API bearer token (prefer --token-file: this form is visible in the process table and in shell history; default $OLIVARES_TOKEN, then current context) |
| `--token-file` | `string` | — | **inherited**. read the API bearer token from a file, or - for stdin |

#### Command: olivares findings export

Export all matching findings as SARIF 2.1.0

```
olivares findings export
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--format` | `string` | `sarif` | export format: sarif (this selects the EXPORT format and is fully supported — it is not the deprecated -o/--output alias other commands spell the same way) |
| `--out` | `string` | — | output file (default: stdout) |

#### Command: olivares finops

Report AI spend and value, and govern budgets, rates and cost centers

```
olivares finops
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--allow-cleartext` | `bool` | `false` | **inherited**. allow sending the credential to a non-loopback host over plain HTTP (DANGEROUS: it travels readable) |
| `--ca-cert` | `string` | — | **inherited**. PEM file containing an additional trusted root CA (default: current context) |
| `--insecure` | `bool` | `false` | **inherited**. skip TLS certificate verification (DANGEROUS; development only) |
| `--pin-sha256` | `stringArray` | `[]` | **inherited**. trusted leaf SPKI SHA-256 pin, base64 or hex, repeatable — the engine prints it as pin_sha256 on the line reporting its certificate (default: current context) |
| `--server` | `string` | — | **inherited**. control-plane base URL (default $OLIVARES_SERVER_URL, then current context) |
| `--tenant` | `string` | — | **inherited**. tenant id (default $OLIVARES_TENANT, then current context) |
| `--timeout` | `duration` | `10s` | **inherited**. request timeout |
| `--token` | `string` | — | **inherited**. API bearer token (prefer --token-file: this form is visible in the process table and in shell history; default $OLIVARES_TOKEN, then current context) |
| `--token-file` | `string` | — | **inherited**. read the API bearer token from a file, or - for stdin |

#### Command: olivares finops alerts

List budget threshold alerts

```
olivares finops alerts
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--all` | `bool` | `false` | follow the cursor to the end and emit one merged page (json output carries has_more:false and no cursor) |
| `--budget-id` | `string` | — | only alerts raised by this budget |
| `--cursor` | `string` | — | opaque cursor from a previous page's cursor field |
| `--limit` | `int` | `0` | page size to request (0 leaves the control plane's default) |

#### Command: olivares finops budgets

Govern spend budgets and read their status

```
olivares finops budgets
```

Declares no flags of its own; it takes those of [`olivares finops`](#command-olivares-finops) and the root command.

#### Command: olivares finops budgets create

Create a budget

```
olivares finops budgets create
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--data` | `string` | — | request document: inline JSON, @FILE, or - for stdin |

#### Command: olivares finops budgets get

Show one budget

```
olivares finops budgets get <budget-id>
```

Declares no flags of its own; it takes those of [`olivares finops budgets`](#command-olivares-finops-budgets) and the root command.

#### Command: olivares finops budgets ls

List budgets

```
olivares finops budgets ls
```

Aliases: `list`

| Flag | Type | Default | Description |
|---|---|---|---|
| `--all` | `bool` | `false` | follow the cursor to the end and emit one merged page (json output carries has_more:false and no cursor) |
| `--cursor` | `string` | — | opaque cursor from a previous page's cursor field |
| `--limit` | `int` | `0` | page size to request (0 leaves the control plane's default) |

#### Command: olivares finops budgets rm

Delete a budget

```
olivares finops budgets rm <budget-id>
```

Aliases: `delete`

| Flag | Type | Default | Description |
|---|---|---|---|
| `-y`, `--yes` | `bool` | `false` | proceed without the confirmation prompt (required in a non-interactive session) |

#### Command: olivares finops budgets status

Show one budget's live status against its cap

```
olivares finops budgets status <budget-id>
```

Declares no flags of its own; it takes those of [`olivares finops budgets`](#command-olivares-finops-budgets) and the root command.

#### Command: olivares finops budgets update

Replace a budget

```
olivares finops budgets update <budget-id>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--data` | `string` | — | request document: inline JSON, @FILE, or - for stdin |

#### Command: olivares finops comparison

Compare what a workload would cost on other models

```
olivares finops comparison
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--dim-key` | `string` | — | restrict the comparison to this key within the dimension |
| `--dimension` | `string` | — | restrict the comparison to this dimension |
| `--forecast-period` | `string` | — | period to project the saving over |
| `--since` | `string` | — | start of the window, RFC3339 (e.g. 2026-08-01T00:00:00Z) |
| `--source-model` | `string` | — | the model the observed workload ran on |
| `--target-models` | `string` | — | candidate models to price the same workload against |
| `--until` | `string` | — | end of the window, RFC3339 |
| `--window-days` | `string` | — | days of history to compare over |

#### Command: olivares finops cost

Record an observed cost sample

```
olivares finops cost
```

Declares no flags of its own; it takes those of [`olivares finops`](#command-olivares-finops) and the root command.

#### Command: olivares finops cost ingest

Record one observed cost sample

```
olivares finops cost ingest
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--data` | `string` | — | request document: inline JSON, @FILE, or - for stdin |

#### Command: olivares finops cost-centers

Govern cost centers and the rules that map spend to them

```
olivares finops cost-centers
```

Aliases: `cost-centres`

Declares no flags of its own; it takes those of [`olivares finops`](#command-olivares-finops) and the root command.

#### Command: olivares finops cost-centers create

Create a cost center

```
olivares finops cost-centers create
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--data` | `string` | — | request document: inline JSON, @FILE, or - for stdin |

#### Command: olivares finops cost-centers get

Show one cost center

```
olivares finops cost-centers get <cost-center-id>
```

Declares no flags of its own; it takes those of [`olivares finops cost-centers`](#command-olivares-finops-cost-centers) and the root command.

#### Command: olivares finops cost-centers ls

List cost centers

```
olivares finops cost-centers ls
```

Aliases: `list`

| Flag | Type | Default | Description |
|---|---|---|---|
| `--all` | `bool` | `false` | follow the cursor to the end and emit one merged page (json output carries has_more:false and no cursor) |
| `--cursor` | `string` | — | opaque cursor from a previous page's cursor field |
| `--limit` | `int` | `0` | page size to request (0 leaves the control plane's default) |
| `--status` | `string` | — | only cost centers in this status |

#### Command: olivares finops cost-centers mappings

Govern the rules that map spend onto one cost center

```
olivares finops cost-centers mappings
```

Declares no flags of its own; it takes those of [`olivares finops cost-centers`](#command-olivares-finops-cost-centers) and the root command.

#### Command: olivares finops cost-centers mappings add

Add a mapping rule to a cost center

```
olivares finops cost-centers mappings add <cost-center-id>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--data` | `string` | — | request document: inline JSON, @FILE, or - for stdin |

#### Command: olivares finops cost-centers mappings ls

List one cost centre's mapping rules

```
olivares finops cost-centers mappings ls <cost-center-id>
```

Aliases: `list`

Declares no flags of its own; it takes those of [`olivares finops cost-centers mappings`](#command-olivares-finops-cost-centers-mappings) and the root command.

#### Command: olivares finops cost-centers mappings rm

Remove a mapping rule from a cost center

```
olivares finops cost-centers mappings rm <cost-center-id> <mapping-id>
```

Aliases: `delete`

| Flag | Type | Default | Description |
|---|---|---|---|
| `-y`, `--yes` | `bool` | `false` | proceed without the confirmation prompt (required in a non-interactive session) |

#### Command: olivares finops cost-centers rm

Delete a cost center

```
olivares finops cost-centers rm <cost-center-id>
```

Aliases: `delete`

| Flag | Type | Default | Description |
|---|---|---|---|
| `-y`, `--yes` | `bool` | `false` | proceed without the confirmation prompt (required in a non-interactive session) |

#### Command: olivares finops cost-centers update

Replace a cost center

```
olivares finops cost-centers update <cost-center-id>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--data` | `string` | — | request document: inline JSON, @FILE, or - for stdin |

#### Command: olivares finops forecast

Forecast spend from the observed history

```
olivares finops forecast
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--dimension` | `string` | — | forecast per this dimension |
| `--period` | `string` | — | forecast period (e.g. monthly) |
| `--window-days` | `string` | — | days of history the projection is built from |

#### Command: olivares finops outcomes

Record and read business outcomes attributed to AI work

```
olivares finops outcomes
```

Declares no flags of its own; it takes those of [`olivares finops`](#command-olivares-finops) and the root command.

#### Command: olivares finops outcomes ingest

Record one business outcome

```
olivares finops outcomes ingest
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--data` | `string` | — | request document: inline JSON, @FILE, or - for stdin |

#### Command: olivares finops outcomes ls

List recorded outcomes

```
olivares finops outcomes ls
```

Aliases: `list`

| Flag | Type | Default | Description |
|---|---|---|---|
| `--all` | `bool` | `false` | follow the cursor to the end and emit one merged page (json output carries has_more:false and no cursor) |
| `--cursor` | `string` | — | opaque cursor from a previous page's cursor field |
| `--limit` | `int` | `0` | page size to request (0 leaves the control plane's default) |
| `--subject-kind` | `string` | — | only outcomes whose subject is of this kind |
| `--subject-ref` | `string` | — | only outcomes for this subject reference |

#### Command: olivares finops rates

Govern the model rate catalog used to price usage

```
olivares finops rates
```

Aliases: `model-rates`

Declares no flags of its own; it takes those of [`olivares finops`](#command-olivares-finops) and the root command.

#### Command: olivares finops rates create

Add a model rate

```
olivares finops rates create
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--data` | `string` | — | request document: inline JSON, @FILE, or - for stdin |

#### Command: olivares finops rates get

Show one model rate

```
olivares finops rates get <rate-id>
```

Declares no flags of its own; it takes those of [`olivares finops rates`](#command-olivares-finops-rates) and the root command.

#### Command: olivares finops rates ls

List model rates

```
olivares finops rates ls
```

Aliases: `list`

| Flag | Type | Default | Description |
|---|---|---|---|
| `--all` | `bool` | `false` | follow the cursor to the end and emit one merged page (json output carries has_more:false and no cursor) |
| `--cursor` | `string` | — | opaque cursor from a previous page's cursor field |
| `--limit` | `int` | `0` | page size to request (0 leaves the control plane's default) |
| `--model` | `string` | — | only rates for this model reference |
| `--provider` | `string` | — | only rates for this provider |

#### Command: olivares finops rates rm

Delete a model rate

```
olivares finops rates rm <rate-id>
```

Aliases: `delete`

| Flag | Type | Default | Description |
|---|---|---|---|
| `-y`, `--yes` | `bool` | `false` | proceed without the confirmation prompt (required in a non-interactive session) |

#### Command: olivares finops rates update

Replace a model rate

```
olivares finops rates update <rate-id>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--data` | `string` | — | request document: inline JSON, @FILE, or - for stdin |

#### Command: olivares finops recommendations

Show cost-reduction recommendations

```
olivares finops recommendations
```

Declares no flags of its own; it takes those of [`olivares finops`](#command-olivares-finops) and the root command.

#### Command: olivares finops seats

Record seat counts and read seat utilization

```
olivares finops seats
```

Declares no flags of its own; it takes those of [`olivares finops`](#command-olivares-finops) and the root command.

#### Command: olivares finops seats ingest

Record a provider's seat counts for a day

```
olivares finops seats ingest
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--data` | `string` | — | request document: inline JSON, @FILE, or - for stdin |

#### Command: olivares finops seats utilization

Show seat utilization

```
olivares finops seats utilization
```

Declares no flags of its own; it takes those of [`olivares finops seats`](#command-olivares-finops-seats) and the root command.

#### Command: olivares finops spend

Report observed AI spend over a window

```
olivares finops spend
```

Declares no flags of its own; it takes those of [`olivares finops`](#command-olivares-finops) and the root command.

#### Command: olivares finops spend allocation

Show how spend allocates to cost centers

```
olivares finops spend allocation
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--since` | `string` | — | start of the window, RFC3339 (e.g. 2026-08-01T00:00:00Z) |
| `--until` | `string` | — | end of the window, RFC3339 |

#### Command: olivares finops spend export

Export spend in the FOCUS interchange format

```
olivares finops spend export
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--format` | `string` | — | export format the module publishes (e.g. focus) |
| `--provenance` | `string` | — | restrict to rows of this provenance |
| `--since` | `string` | — | start of the window, RFC3339 (e.g. 2026-08-01T00:00:00Z) |
| `--until` | `string` | — | end of the window, RFC3339 |

#### Command: olivares finops spend ls

Show the spend series for a window

```
olivares finops spend ls
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--dimension` | `string` | — | group by this dimension (e.g. provider, model, workspace) |
| `--since` | `string` | — | start of the window, RFC3339 (e.g. 2026-08-01T00:00:00Z) |
| `--until` | `string` | — | end of the window, RFC3339 |

#### Command: olivares finops spend reconciliation

Compare observed spend against provider-reported cost

```
olivares finops spend reconciliation
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--since` | `string` | — | start of the window, RFC3339 (e.g. 2026-08-01T00:00:00Z) |
| `--until` | `string` | — | end of the window, RFC3339 |

#### Command: olivares finops spend summary

Show the spend summary for a window

```
olivares finops spend summary
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--since` | `string` | — | start of the window, RFC3339 (e.g. 2026-08-01T00:00:00Z) |
| `--until` | `string` | — | end of the window, RFC3339 |

#### Command: olivares finops spend trend

Show the spend trend over a window

```
olivares finops spend trend
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--since` | `string` | — | start of the window, RFC3339 (e.g. 2026-08-01T00:00:00Z) |
| `--until` | `string` | — | end of the window, RFC3339 |

#### Command: olivares finops spend unified

Show the unified cross-source spend view

```
olivares finops spend unified
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--since` | `string` | — | start of the window, RFC3339 (e.g. 2026-08-01T00:00:00Z) |
| `--until` | `string` | — | end of the window, RFC3339 |

#### Command: olivares finops statements

Generate, read and export per-cost-center statements

```
olivares finops statements
```

Declares no flags of its own; it takes those of [`olivares finops`](#command-olivares-finops) and the root command.

#### Command: olivares finops statements export

Export one statement

```
olivares finops statements export <statement-id>
```

Declares no flags of its own; it takes those of [`olivares finops statements`](#command-olivares-finops-statements) and the root command.

#### Command: olivares finops statements generate

Generate statements for a period

```
olivares finops statements generate
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--data` | `string` | — | request document: inline JSON, @FILE, or - for stdin |

#### Command: olivares finops statements get

Show one statement with its lines

```
olivares finops statements get <statement-id>
```

Declares no flags of its own; it takes those of [`olivares finops statements`](#command-olivares-finops-statements) and the root command.

#### Command: olivares finops statements ls

List generated statements

```
olivares finops statements ls
```

Aliases: `list`

| Flag | Type | Default | Description |
|---|---|---|---|
| `--all` | `bool` | `false` | follow the cursor to the end and emit one merged page (json output carries has_more:false and no cursor) |
| `--cost-center-id` | `string` | — | only statements for this cost center |
| `--cursor` | `string` | — | opaque cursor from a previous page's cursor field |
| `--limit` | `int` | `0` | page size to request (0 leaves the control plane's default) |
| `--period` | `string` | — | only statements of this period kind (monthly or weekly) |
| `--status` | `string` | — | only statements in this status |

#### Command: olivares finops team-summary

Show the per-team spend summary

```
olivares finops team-summary
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--period` | `string` | — | summary period (e.g. monthly) |

#### Command: olivares finops value

Report the value side of the unit economics

```
olivares finops value
```

Declares no flags of its own; it takes those of [`olivares finops`](#command-olivares-finops) and the root command.

#### Command: olivares finops value ls

Show the value series for a window

```
olivares finops value ls
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--dimension` | `string` | — | group by this dimension (e.g. provider, model, workspace) |
| `--since` | `string` | — | start of the window, RFC3339 (e.g. 2026-08-01T00:00:00Z) |
| `--until` | `string` | — | end of the window, RFC3339 |

#### Command: olivares finops value summary

Show the value summary and cost-per-outcome

```
olivares finops value summary
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--dimension` | `string` | — | group by this dimension (e.g. provider, model, workspace) |
| `--since` | `string` | — | start of the window, RFC3339 (e.g. 2026-08-01T00:00:00Z) |
| `--until` | `string` | — | end of the window, RFC3339 |

#### Command: olivares firstparty-bins

Hidden diagnostic: it does not appear in `--help` output and is not part of the supported surface.

List the first-party connector plugins embedded in this binary (diagnostic)

```
olivares firstparty-bins
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--require` | `stringSlice` | `[]` | comma-separated plugin binary names that MUST be embedded (exit non-zero otherwise) |

#### Command: olivares governance

Inspect the governance plane: what is stopped, and why

```
olivares governance
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--allow-cleartext` | `bool` | `false` | **inherited**. allow sending the credential to a non-loopback host over plain HTTP (DANGEROUS: it travels readable) |
| `--ca-cert` | `string` | — | **inherited**. PEM file containing an additional trusted root CA (default: current context) |
| `--insecure` | `bool` | `false` | **inherited**. skip TLS certificate verification (DANGEROUS; development only) |
| `--pin-sha256` | `stringArray` | `[]` | **inherited**. trusted leaf SPKI SHA-256 pin, base64 or hex, repeatable — the engine prints it as pin_sha256 on the line reporting its certificate (default: current context) |
| `--server` | `string` | — | **inherited**. control-plane base URL (default $OLIVARES_SERVER_URL, then current context) |
| `--tenant` | `string` | — | **inherited**. tenant id (default $OLIVARES_TENANT, then current context) |
| `--timeout` | `duration` | `10s` | **inherited**. request timeout |
| `--token` | `string` | — | **inherited**. API bearer token (prefer --token-file: this form is visible in the process table and in shell history; default $OLIVARES_TOKEN, then current context) |
| `--token-file` | `string` | — | **inherited**. read the API bearer token from a file, or - for stdin |

#### Command: olivares governance approvals

The approval queue: what is waiting on a human, and who decided what

```
olivares governance approvals
```

Declares no flags of its own; it takes those of [`olivares governance`](#command-olivares-governance) and the root command.

#### Command: olivares governance approvals decisions

Who voted which way on one approval, and why

```
olivares governance approvals decisions <approval-id>
```

Declares no flags of its own; it takes those of [`olivares governance approvals`](#command-olivares-governance-approvals) and the root command.

#### Command: olivares governance approvals get

Show one approval

```
olivares governance approvals get <approval-id>
```

Declares no flags of its own; it takes those of [`olivares governance approvals`](#command-olivares-governance-approvals) and the root command.

#### Command: olivares governance approvals ls

List approvals, pending and decided

```
olivares governance approvals ls
```

Aliases: `list`

| Flag | Type | Default | Description |
|---|---|---|---|
| `--action` | `string` | — | only approvals gating this action |
| `--cursor` | `string` | — | continue from the cursor printed by the previous page |
| `--limit` | `int` | `0` | maximum rows to return in one page (0 = the engine's default) |
| `--status` | `string` | — | only approvals in this status (e.g. pending) |

#### Command: olivares governance breakglass

Emergency access grants: who has one, until when, and what they did with it

```
olivares governance breakglass
```

Declares no flags of its own; it takes those of [`olivares governance`](#command-olivares-governance) and the root command.

#### Command: olivares governance breakglass get

Show one break-glass grant

```
olivares governance breakglass get <grant-id>
```

Declares no flags of its own; it takes those of [`olivares governance breakglass`](#command-olivares-governance-breakglass) and the root command.

#### Command: olivares governance breakglass ls

List break-glass grants, live and expired

```
olivares governance breakglass ls
```

Aliases: `list`

| Flag | Type | Default | Description |
|---|---|---|---|
| `--cursor` | `string` | — | continue from the cursor printed by the previous page |
| `--limit` | `int` | `0` | maximum rows to return in one page (0 = the engine's default) |
| `--status` | `string` | — | only grants in this status (e.g. active) |

#### Command: olivares governance breakglass uses

Every action actually taken under one grant

```
olivares governance breakglass uses <grant-id>
```

Declares no flags of its own; it takes those of [`olivares governance breakglass`](#command-olivares-governance-breakglass) and the root command.

#### Command: olivares governance guardian

The rules that act on findings without a human, and what they have done

```
olivares governance guardian
```

Declares no flags of its own; it takes those of [`olivares governance`](#command-olivares-governance) and the root command.

#### Command: olivares governance guardian actions

What guardian actually did, rule by rule

```
olivares governance guardian actions
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--cursor` | `string` | — | continue from the cursor printed by the previous page |
| `--limit` | `int` | `0` | maximum rows to return in one page (0 = the engine's default) |
| `--status` | `string` | — | only actions in this status (e.g. executed) |

#### Command: olivares governance guardian rules

List the guardian rules and whether each is armed

```
olivares governance guardian rules
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--cursor` | `string` | — | continue from the cursor printed by the previous page |
| `--limit` | `int` | `0` | maximum rows to return in one page (0 = the engine's default) |

#### Command: olivares governance killswitch

The estate-wide and per-scope stops that deny work while they are active

```
olivares governance killswitch
```

Declares no flags of its own; it takes those of [`olivares governance`](#command-olivares-governance) and the root command.

#### Command: olivares governance killswitch ls

List kill switches, active and historical

```
olivares governance killswitch ls
```

Aliases: `list`

| Flag | Type | Default | Description |
|---|---|---|---|
| `--cursor` | `string` | — | continue from the cursor printed by the previous page |
| `--limit` | `int` | `0` | maximum rows to return in one page (0 = the engine's default) |
| `--status` | `string` | — | only switches in this status (e.g. active) |

#### Command: olivares governance killswitch state

Whether the estate is stopped, and every kill switch active right now

```
olivares governance killswitch state
```

Declares no flags of its own; it takes those of [`olivares governance killswitch`](#command-olivares-governance-killswitch) and the root command.

#### Command: olivares governance nhi

Non-human identities: ownership, rotation age and what is already being refused

```
olivares governance nhi
```

Declares no flags of its own; it takes those of [`olivares governance`](#command-olivares-governance) and the root command.

#### Command: olivares governance nhi events

The lifecycle events recorded for one identity

```
olivares governance nhi events <identity-ref>
```

Declares no flags of its own; it takes those of [`olivares governance nhi`](#command-olivares-governance-nhi) and the root command.

#### Command: olivares governance nhi get

One non-human identity, in full

```
olivares governance nhi get <identity-ref>
```

Declares no flags of its own; it takes those of [`olivares governance nhi`](#command-olivares-governance-nhi) and the root command.

#### Command: olivares governance nhi ls

List the non-human identities

```
olivares governance nhi ls
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--cursor` | `string` | — | continue from the cursor printed by the previous page |
| `--enforcement` | `string` | — | only identities in this enforcement state |
| `--limit` | `int` | `0` | maximum rows to return in one page (0 = the engine's default) |
| `--offboard-state` | `string` | — | only identities in this offboarding state |

#### Command: olivares governance nhi posture

The estate-wide identity posture in one screen

```
olivares governance nhi posture
```

Declares no flags of its own; it takes those of [`olivares governance nhi`](#command-olivares-governance-nhi) and the root command.

#### Command: olivares governance pdp

The policy decision point: which revision is actually deciding, and is it in force

```
olivares governance pdp
```

Declares no flags of its own; it takes those of [`olivares governance`](#command-olivares-governance) and the root command.

#### Command: olivares governance pdp active

Which policy this process is deciding with, and whether it is fully in force

```
olivares governance pdp active
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--engine` | `string` | — | **required**. policy surface to read (required; the engine states which are legal) |

#### Command: olivares governance pdp get-version

One stored revision, with the policy document itself

```
olivares governance pdp get-version <revision>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--engine` | `string` | — | **required**. policy surface to read (required; the engine states which are legal) |

#### Command: olivares governance pdp tests

The stored test results for a policy revision

```
olivares governance pdp tests
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--engine` | `string` | — | **required**. policy surface to read (required; the engine states which are legal) |
| `--revision` | `int64` | `0` | a specific revision (default: the newest with a stored artifact) |

#### Command: olivares governance pdp versions

Every stored policy revision, both surfaces, metadata only

```
olivares governance pdp versions
```

Declares no flags of its own; it takes those of [`olivares governance pdp`](#command-olivares-governance-pdp) and the root command.

#### Command: olivares governance rbac

Who can do what: the grant vocabulary, the custom roles and the scoped grants

```
olivares governance rbac
```

Declares no flags of its own; it takes those of [`olivares governance`](#command-olivares-governance) and the root command.

#### Command: olivares governance rbac catalog

The vocabulary a grant can be built from

```
olivares governance rbac catalog
```

Declares no flags of its own; it takes those of [`olivares governance rbac`](#command-olivares-governance-rbac) and the root command.

#### Command: olivares governance rbac delegation-authority

What the calling principal may delegate, and where

```
olivares governance rbac delegation-authority
```

Declares no flags of its own; it takes those of [`olivares governance rbac`](#command-olivares-governance-rbac) and the root command.

#### Command: olivares governance rbac grants

The scoped grants in force: who holds what, where

```
olivares governance rbac grants
```

Declares no flags of its own; it takes those of [`olivares governance rbac`](#command-olivares-governance-rbac) and the root command.

#### Command: olivares governance rbac grants get

One scoped grant

```
olivares governance rbac grants get <id>
```

Declares no flags of its own; it takes those of [`olivares governance rbac grants`](#command-olivares-governance-rbac-grants) and the root command.

#### Command: olivares governance rbac grants ls

List every scoped grant

```
olivares governance rbac grants ls
```

Declares no flags of its own; it takes those of [`olivares governance rbac grants`](#command-olivares-governance-rbac-grants) and the root command.

#### Command: olivares governance rbac permission-groups

Named bundles of permissions that roles reuse

```
olivares governance rbac permission-groups
```

Declares no flags of its own; it takes those of [`olivares governance rbac`](#command-olivares-governance-rbac) and the root command.

#### Command: olivares governance rbac permission-groups get

One permission group, with its members

```
olivares governance rbac permission-groups get <name>
```

Declares no flags of its own; it takes those of [`olivares governance rbac permission-groups`](#command-olivares-governance-rbac-permission-groups) and the root command.

#### Command: olivares governance rbac permission-groups ls

List the permission groups

```
olivares governance rbac permission-groups ls
```

Declares no flags of its own; it takes those of [`olivares governance rbac permission-groups`](#command-olivares-governance-rbac-permission-groups) and the root command.

#### Command: olivares governance rbac roles

Custom roles: what each one grants, and what it takes away

```
olivares governance rbac roles
```

Declares no flags of its own; it takes those of [`olivares governance rbac`](#command-olivares-governance-rbac) and the root command.

#### Command: olivares governance rbac roles get

One custom role, with its full permission set

```
olivares governance rbac roles get <name>
```

Declares no flags of its own; it takes those of [`olivares governance rbac roles`](#command-olivares-governance-rbac-roles) and the root command.

#### Command: olivares governance rbac roles ls

List the custom roles

```
olivares governance rbac roles ls
```

Declares no flags of its own; it takes those of [`olivares governance rbac roles`](#command-olivares-governance-rbac-roles) and the root command.

#### Command: olivares grok-hook

Governed PEP hook client for Grok Build: forward a Grok hook to the control plane and relay the decision (deny-closed)

```
olivares grok-hook
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--account` | `string` | — | account identity hint (default $OLIVARES_GROK_HOOK_ACCOUNT) |
| `--agent` | `string` | — | agent identity hint (default $OLIVARES_GROK_HOOK_AGENT) |
| `--endpoint` | `string` | — | governed PEP URL (default $OLIVARES_GROK_HOOK_URL); --server is the canonical alias |
| `--org` | `string` | — | org identity hint (default $OLIVARES_GROK_HOOK_ORG) |
| `--server` | `string` | — | control-plane base URL (default $OLIVARES_SERVER_URL; the canonical spelling of --endpoint) |
| `--tenant` | `string` | — | the tenant the agent acts in (default $OLIVARES_GROK_HOOK_TENANT) |
| `--timeout` | `duration` | `5s` | PEP request timeout |
| `--token` | `string` | — | the agent's PEP bearer credential (default $OLIVARES_GROK_HOOK_TOKEN) |

#### Command: olivares health

Watch subject health, incidents, SLA and dependencies

```
olivares health
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--allow-cleartext` | `bool` | `false` | **inherited**. allow sending the credential to a non-loopback host over plain HTTP (DANGEROUS: it travels readable) |
| `--ca-cert` | `string` | — | **inherited**. PEM file containing an additional trusted root CA (default: current context) |
| `--insecure` | `bool` | `false` | **inherited**. skip TLS certificate verification (DANGEROUS; development only) |
| `--pin-sha256` | `stringArray` | `[]` | **inherited**. trusted leaf SPKI SHA-256 pin, base64 or hex, repeatable — the engine prints it as pin_sha256 on the line reporting its certificate (default: current context) |
| `--server` | `string` | — | **inherited**. control-plane base URL (default $OLIVARES_SERVER_URL, then current context) |
| `--tenant` | `string` | — | **inherited**. tenant id (default $OLIVARES_TENANT, then current context) |
| `--timeout` | `duration` | `10s` | **inherited**. request timeout |
| `--token` | `string` | — | **inherited**. API bearer token (prefer --token-file: this form is visible in the process table and in shell history; default $OLIVARES_TOKEN, then current context) |
| `--token-file` | `string` | — | **inherited**. read the API bearer token from a file, or - for stdin |

#### Command: olivares health checks

Declare, inspect, probe and retire health checks

```
olivares health checks
```

Aliases: `check`

Declares no flags of its own; it takes those of [`olivares health`](#command-olivares-health) and the root command.

#### Command: olivares health checks create

Declare a new monitored subject

```
olivares health checks create
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--desired-status` | `string` | — | lifecycle status: active, paused or retired |
| `--grace` | `int64` | `0` | multiplier on the interval before silence reads as unknown |
| `--interval` | `int64` | `0` | expected seconds between probes (required, positive) |
| `--name` | `string` | — | a human name for the check |
| `--sla-target-ppm` | `int64` | `0` | uptime target in parts per million (999000 = 99.9%) |
| `--subject-kind` | `string` | — | the subject's kind: agent or mcp (required) |
| `--subject-ref` | `string` | — | the subject's reference (required) |

#### Command: olivares health checks get

Show one check

```
olivares health checks get <check-id>
```

Declares no flags of its own; it takes those of [`olivares health checks`](#command-olivares-health-checks) and the root command.

#### Command: olivares health checks ls

List declared checks

```
olivares health checks ls
```

Aliases: `list`

| Flag | Type | Default | Description |
|---|---|---|---|
| `--cursor` | `string` | — | continue from the cursor printed by the previous page |
| `--desired-status` | `string` | — | filter by lifecycle status (active, paused, retired) |
| `--limit` | `int` | `0` | maximum rows to return in one page (0 = the engine's default) |
| `--subject-kind` | `string` | — | filter by subject kind (agent, mcp) |

#### Command: olivares health checks report

Post a probe result against a check

```
olivares health checks report <check-id>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--detail` | `string` | — | a short, non-sensitive note (the engine stores only its hash) |
| `--latency` | `int64` | `0` | observed latency in milliseconds |
| `--state` | `string` | — | the observed state: healthy, degraded or down (required) |

#### Command: olivares health checks rm

Delete a check (admin-tier)

```
olivares health checks rm <check-id>
```

Aliases: `delete`, `remove`

| Flag | Type | Default | Description |
|---|---|---|---|
| `-y`, `--yes` | `bool` | `false` | proceed without the confirmation prompt (required in a non-interactive session) |

#### Command: olivares health checks update

Change a check's configuration

```
olivares health checks update <check-id>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--desired-status` | `string` | — | lifecycle status: active, paused or retired |
| `--grace` | `int64` | `0` | multiplier on the interval before silence reads as unknown |
| `--interval` | `int64` | `0` | expected seconds between probes |
| `--name` | `string` | — | a human name for the check |
| `--sla-target-ppm` | `int64` | `0` | uptime target in parts per million; SENT ONLY IF PASSED, so omitting it keeps the stored target |

#### Command: olivares health dependencies

Show the observed dependency graph

```
olivares health dependencies
```

Aliases: `deps`

| Flag | Type | Default | Description |
|---|---|---|---|
| `--cursor` | `string` | — | continue from the cursor printed by the previous page |
| `--limit` | `int` | `0` | maximum rows to return in one page (0 = the engine's default) |

#### Command: olivares health events

List the append-only reliability transition ledger

```
olivares health events
```

Aliases: `transitions`

| Flag | Type | Default | Description |
|---|---|---|---|
| `--cursor` | `string` | — | continue from the cursor printed by the previous page |
| `--limit` | `int` | `0` | maximum rows to return in one page (0 = the engine's default) |
| `--subject-kind` | `string` | — | filter by subject kind (agent, mcp) |
| `--subject-ref` | `string` | — | filter by subject reference |

#### Command: olivares health incidents

List, open and resolve health incidents

```
olivares health incidents
```

Aliases: `incident`

Declares no flags of its own; it takes those of [`olivares health`](#command-olivares-health) and the root command.

#### Command: olivares health incidents get

Show one incident

```
olivares health incidents get <incident-id>
```

Declares no flags of its own; it takes those of [`olivares health incidents`](#command-olivares-health-incidents) and the root command.

#### Command: olivares health incidents ls

List health incidents

```
olivares health incidents ls
```

Aliases: `list`

| Flag | Type | Default | Description |
|---|---|---|---|
| `--cursor` | `string` | — | continue from the cursor printed by the previous page |
| `--limit` | `int` | `0` | maximum rows to return in one page (0 = the engine's default) |
| `--state` | `string` | — | filter by state (open, resolved) |
| `--subject-kind` | `string` | — | filter by subject kind (agent, mcp) |
| `--subject-ref` | `string` | — | filter by subject reference |

#### Command: olivares health incidents resolve

Declare an incident resolved

```
olivares health incidents resolve <incident-id>
```

Declares no flags of its own; it takes those of [`olivares health incidents`](#command-olivares-health-incidents) and the root command.

#### Command: olivares health sla

Report observed uptime for one subject against its target

```
olivares health sla
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--strict` | `bool` | `true` | exit 8 (indeterminate) when no observation exists in the window; --strict=false exits 0 instead |
| `--subject-kind` | `string` | — | the subject's kind: agent or mcp (required) |
| `--subject-ref` | `string` | — | the subject's reference (required) |
| `--window` | `int64` | `0` | window in seconds (0 = the engine's default) |

#### Command: olivares health status

Show the current health of every monitored subject

```
olivares health status
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--cursor` | `string` | — | continue from the cursor printed by the previous page |
| `--limit` | `int` | `0` | maximum rows to return in one page (0 = the engine's default) |
| `--state` | `string` | — | filter by state (healthy, degraded, down, unknown) |
| `--subject-kind` | `string` | — | filter by subject kind (agent, mcp) |

#### Command: olivares health watch

Follow health changes as they happen (one JSON object per line)

```
olivares health watch
```

Aliases: `stream`

| Flag | Type | Default | Description |
|---|---|---|---|
| `--subject-ref` | `string` | — | follow one subject instead of every subject in the tenant |

#### Command: olivares help

Help about any command

```
olivares help [command]
```

Declares no flags of its own; it takes those of [`olivares`](#command-olivares) and the root command.

#### Command: olivares hookpep

Author and inspect PDP policy through the control plane

```
olivares hookpep
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--ca-cert` | `string` | — | **inherited**. PEM CA bundle used to verify the control plane |
| `--format` | `string` | `text` | **inherited**. deprecated alias for -o/--output on this command (text or json) — NOT the export-format flag of 'audit export' / 'findings export' |
| `--insecure` | `bool` | `false` | **inherited**. skip TLS certificate verification (self-signed development planes only) |
| `--pin-sha256` | `stringArray` | `[]` | **inherited**. pinned leaf SPKI SHA-256, base64 or hex (repeatable) — the engine prints it as pin_sha256 on the line reporting its certificate |
| `--server` | `string` | — | **inherited**. control-plane base URL (default $OLIVARES_SERVER_URL; the canonical spelling of --url) |
| `--timeout` | `duration` | `30s` | **inherited**. request timeout |
| `--token` | `string` | — | **inherited**. API bearer token (default $OLIVARES_HOOK_PEP_TOKEN) |
| `--url` | `string` | — | **inherited**. control-plane base URL (default $OLIVARES_HOOK_PEP_URL); --server is the canonical spelling |

#### Command: olivares hookpep dry-run

Evaluate a request against a candidate policy without publishing it

```
olivares hookpep dry-run
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--engine` | `string` | `cedar` | policy engine: cedar or opa |
| `--file` | `string` | — | policy source file ('-' reads stdin) |
| `--request` | `string` | — | inline example-request JSON |
| `--request-file` | `string` | — | example-request JSON file ('-' reads stdin) |
| `--source` | `string` | — | inline policy source |

#### Command: olivares hookpep explain

Explain a request decision against a candidate policy without publishing it

```
olivares hookpep explain
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--engine` | `string` | `cedar` | policy engine: cedar or opa |
| `--file` | `string` | — | policy source file ('-' reads stdin) |
| `--request` | `string` | — | inline example-request JSON |
| `--request-file` | `string` | — | example-request JSON file ('-' reads stdin) |
| `--source` | `string` | — | inline policy source |

#### Command: olivares hookpep publish

Compile, publish, and activate an authored policy revision

```
olivares hookpep publish
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--engine` | `string` | `cedar` | policy engine: cedar or opa |
| `--file` | `string` | — | policy source file ('-' reads stdin) |
| `--note` | `string` | — | optional publication note |
| `--source` | `string` | — | inline policy source |

#### Command: olivares hookpep rollback

Re-activate a prior immutable policy revision

```
olivares hookpep rollback
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--engine` | `string` | `cedar` | policy engine: cedar or opa |
| `--revision` | `int64` | `0` | immutable policy revision to re-activate |

#### Command: olivares hookpep tests

Show the stored compile-validation artifact for a policy revision

```
olivares hookpep tests
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--engine` | `string` | — | **required**. policy engine: cedar or opa |
| `--revision` | `int64` | `0` | immutable policy revision (default newest) |

#### Command: olivares hookpep validate

Compile and validate a candidate policy without publishing it

```
olivares hookpep validate
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--engine` | `string` | `cedar` | policy engine: cedar or opa |
| `--file` | `string` | — | policy source file ('-' reads stdin) |
| `--source` | `string` | — | inline policy source |

#### Command: olivares hookpep versions

List immutable authored policy revisions

```
olivares hookpep versions
```

Declares no flags of its own; it takes those of [`olivares hookpep`](#command-olivares-hookpep) and the root command.

#### Command: olivares hooks

Hidden diagnostic: it does not appear in `--help` output and is not part of the supported surface.

Hooks-hardening add-on: fleet deployed-verified attestation + conformance cert (enterprise)

```
olivares hooks
```

Declares no flags of its own; it takes those of [`olivares`](#command-olivares) and the root command.

#### Command: olivares hooks attest

Attest a fleet's deployed managed-settings against the canonical PEP-hook bundle (deployed-verified)

```
olivares hooks attest
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--matcher` | `string` | — | tool-name matcher for the PEP hook ("" = all tools) |
| `--nodes` | `string` | — | JSON file: an array of node reports to attest |
| `--pep-command` | `string` | `olivares claude-hook` | the managed PreToolUse PEP-client command |
| `--policy-file` | `string` | — | load a full managed-settings Policy JSON instead of building one from the flags above |
| `--redact` | `bool` | `true` | also install the paired PostToolUse output-redaction hook |
| `--signature-out` | `string` | — | write the signed blob to this file (default: print to stderr when signed) |
| `--signing-key-file` | `string` | — | file holding the base64 ed25519 private key to sign the attestation (optional) |
| `--timeout` | `int` | `5` | PEP hook timeout in seconds |
| `--version` | `string` | — | a label for the canonical bundle version |

#### Command: olivares hooks conform

Certify conformance of the managed-settings + PEP hook against the real claude binary

```
olivares hooks conform
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--behavioral` | `bool` | `false` | also run the behavioral hook-deny e2e against a mock model (drives the real binary twice; no creds needed) |
| `--matcher` | `string` | — | tool-name matcher for the PEP hook ("" = all tools) |
| `--pep-command` | `string` | `olivares claude-hook` | the managed PreToolUse PEP-client command |
| `--policy-file` | `string` | — | load a full managed-settings Policy JSON instead of building one from the flags above |
| `--redact` | `bool` | `true` | also install the paired PostToolUse output-redaction hook |
| `--signature-out` | `string` | — | write the signed cert blob to this file (default: print to stderr when signed) |
| `--signing-key-file` | `string` | — | file holding the base64 ed25519 private key to sign the certificate (optional) |
| `--timeout` | `int` | `5` | PEP hook timeout in seconds |

#### Command: olivares identity

Read federation, SSO, customer-managed key and residency posture

```
olivares identity
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--allow-cleartext` | `bool` | `false` | **inherited**. allow sending the credential to a non-loopback host over plain HTTP (DANGEROUS: it travels readable) |
| `--ca-cert` | `string` | — | **inherited**. PEM file containing an additional trusted root CA (default: current context) |
| `--insecure` | `bool` | `false` | **inherited**. skip TLS certificate verification (DANGEROUS; development only) |
| `--pin-sha256` | `stringArray` | `[]` | **inherited**. trusted leaf SPKI SHA-256 pin, base64 or hex, repeatable — the engine prints it as pin_sha256 on the line reporting its certificate (default: current context) |
| `--server` | `string` | — | **inherited**. control-plane base URL (default $OLIVARES_SERVER_URL, then current context) |
| `--tenant` | `string` | — | **inherited**. tenant id (default $OLIVARES_TENANT, then current context) |
| `--timeout` | `duration` | `10s` | **inherited**. request timeout |
| `--token` | `string` | — | **inherited**. API bearer token (prefer --token-file: this form is visible in the process table and in shell history; default $OLIVARES_TOKEN, then current context) |
| `--token-file` | `string` | — | **inherited**. read the API bearer token from a file, or - for stdin |

#### Command: olivares identity external-keys

List the customer-managed encryption key inventory

```
olivares identity external-keys
```

Aliases: `cmek`

| Flag | Type | Default | Description |
|---|---|---|---|
| `--strict` | `bool` | `true` | exit 8 (indeterminate) when the engine reports it could not establish this posture; --strict=false exits 0 instead |

#### Command: olivares identity residency

List each workspace's data-residency and CMEK posture

```
olivares identity residency
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--strict` | `bool` | `true` | exit 8 (indeterminate) when the engine reports it could not establish this posture; --strict=false exits 0 instead |

#### Command: olivares identity sso

Report the SSO connection state

```
olivares identity sso
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--strict` | `bool` | `true` | exit 8 (indeterminate) when the engine reports it could not establish this posture; --strict=false exits 0 instead |

#### Command: olivares identity wif

Show the workload-identity federation graph

```
olivares identity wif
```

Aliases: `federation`

Declares no flags of its own; it takes those of [`olivares identity`](#command-olivares-identity) and the root command.

#### Command: olivares inference-proxy

Govern the inference gateway: gates, DLP rules and device grants

```
olivares inference-proxy
```

Aliases: `inferenceproxy`

| Flag | Type | Default | Description |
|---|---|---|---|
| `--allow-cleartext` | `bool` | `false` | **inherited**. allow sending the credential to a non-loopback host over plain HTTP (DANGEROUS: it travels readable) |
| `--ca-cert` | `string` | — | **inherited**. PEM file containing an additional trusted root CA (default: current context) |
| `--insecure` | `bool` | `false` | **inherited**. skip TLS certificate verification (DANGEROUS; development only) |
| `--pin-sha256` | `stringArray` | `[]` | **inherited**. trusted leaf SPKI SHA-256 pin, base64 or hex, repeatable — the engine prints it as pin_sha256 on the line reporting its certificate (default: current context) |
| `--server` | `string` | — | **inherited**. control-plane base URL (default $OLIVARES_SERVER_URL, then current context) |
| `--tenant` | `string` | — | **inherited**. tenant id (default $OLIVARES_TENANT, then current context) |
| `--timeout` | `duration` | `10s` | **inherited**. request timeout |
| `--token` | `string` | — | **inherited**. API bearer token (prefer --token-file: this form is visible in the process table and in shell history; default $OLIVARES_TOKEN, then current context) |
| `--token-file` | `string` | — | **inherited**. read the API bearer token from a file, or - for stdin |

#### Command: olivares inference-proxy config

Read and replace the gateway's gate configuration

```
olivares inference-proxy config
```

Declares no flags of its own; it takes those of [`olivares inference-proxy`](#command-olivares-inference-proxy) and the root command.

#### Command: olivares inference-proxy config get

Show the gateway's effective gate configuration

```
olivares inference-proxy config get
```

Declares no flags of its own; it takes those of [`olivares inference-proxy config`](#command-olivares-inference-proxy-config) and the root command.

#### Command: olivares inference-proxy config set

Replace the gateway's gate configuration

```
olivares inference-proxy config set
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--data` | `string` | — | request document: inline JSON, @FILE, or - for stdin |

#### Command: olivares inference-proxy device

Approve or deny a pending device grant

```
olivares inference-proxy device
```

Declares no flags of its own; it takes those of [`olivares inference-proxy`](#command-olivares-inference-proxy) and the root command.

#### Command: olivares inference-proxy device approve

Resolve a pending device grant by its user code

```
olivares inference-proxy device approve
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--deny` | `bool` | `false` | refuse the grant instead of approving it |
| `--user-code` | `string` | — | the user code the waiting device displayed |
| `-y`, `--yes` | `bool` | `false` | proceed without the confirmation prompt (required in a non-interactive session) |

#### Command: olivares inference-proxy dlp

Govern the per-class DLP rules applied to inference egress

```
olivares inference-proxy dlp
```

Declares no flags of its own; it takes those of [`olivares inference-proxy`](#command-olivares-inference-proxy) and the root command.

#### Command: olivares inference-proxy dlp ls

List the effective DLP rules

```
olivares inference-proxy dlp ls
```

Aliases: `list`

| Flag | Type | Default | Description |
|---|---|---|---|
| `--all` | `bool` | `false` | follow the cursor to the end and emit one merged page (json output carries has_more:false and no cursor) |
| `--cursor` | `string` | — | opaque cursor from a previous page's cursor field |
| `--limit` | `int` | `0` | page size to request (0 leaves the control plane's default) |

#### Command: olivares inference-proxy dlp rm

Remove a DLP override and restore its secure default

```
olivares inference-proxy dlp rm <rule-id>
```

Aliases: `delete`

| Flag | Type | Default | Description |
|---|---|---|---|
| `-y`, `--yes` | `bool` | `false` | proceed without the confirmation prompt (required in a non-interactive session) |

#### Command: olivares inference-proxy dlp set

Set the action for one DLP class

```
olivares inference-proxy dlp set
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--data` | `string` | — | request document: inline JSON, @FILE, or - for stdin |

#### Command: olivares inventory

List the observed entity catalog and its coverage summary

```
olivares inventory
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--allow-cleartext` | `bool` | `false` | **inherited**. allow sending the credential to a non-loopback host over plain HTTP (DANGEROUS: it travels readable) |
| `--ca-cert` | `string` | — | **inherited**. PEM file containing an additional trusted root CA (default: current context) |
| `--insecure` | `bool` | `false` | **inherited**. skip TLS certificate verification (DANGEROUS; development only) |
| `--pin-sha256` | `stringArray` | `[]` | **inherited**. trusted leaf SPKI SHA-256 pin, base64 or hex, repeatable — the engine prints it as pin_sha256 on the line reporting its certificate (default: current context) |
| `--server` | `string` | — | **inherited**. control-plane base URL (default $OLIVARES_SERVER_URL, then current context) |
| `--tenant` | `string` | — | **inherited**. tenant id (default $OLIVARES_TENANT, then current context) |
| `--timeout` | `duration` | `10s` | **inherited**. request timeout |
| `--token` | `string` | — | **inherited**. API bearer token (prefer --token-file: this form is visible in the process table and in shell history; default $OLIVARES_TOKEN, then current context) |
| `--token-file` | `string` | — | **inherited**. read the API bearer token from a file, or - for stdin |

#### Command: olivares inventory entities

List and open catalog entities

```
olivares inventory entities
```

Aliases: `entity`

Declares no flags of its own; it takes those of [`olivares inventory`](#command-olivares-inventory) and the root command.

#### Command: olivares inventory entities get

Show one catalog entity and the core entity it overlays

```
olivares inventory entities get <kind> <id>
```

Declares no flags of its own; it takes those of [`olivares inventory entities`](#command-olivares-inventory-entities) and the root command.

#### Command: olivares inventory entities ls

List catalog entities

```
olivares inventory entities ls
```

Aliases: `list`

| Flag | Type | Default | Description |
|---|---|---|---|
| `--cursor` | `string` | — | continue from the cursor printed by the previous page |
| `--kind` | `string` | — | filter by entity kind (agent, tool, resource, skill, model, provider) |
| `--limit` | `int` | `0` | maximum rows to return in one page (0 = the engine's default) |
| `--status` | `string` | — | filter by status (active, stale) |

#### Command: olivares inventory summary

Count catalog entities by kind and by signal source

```
olivares inventory summary
```

Declares no flags of its own; it takes those of [`olivares inventory`](#command-olivares-inventory) and the root command.

#### Command: olivares keys

Key custody (BYOK/HYOK/CMEK): seal, rotate and inspect signing keys

```
olivares keys
```

Declares no flags of its own; it takes those of [`olivares`](#command-olivares) and the root command.

#### Command: olivares keys rewrap

Re-seal an envelope under the KEK's CURRENT version/primary (KEK rotation; the sealed key does not change)

```
olivares keys rewrap
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--in` | `string` | — | **required**. envelope path to rewrap |
| `--out` | `string` | — | output path (default: overwrite --in atomically) |
| `--yes` | `bool` | `false` | proceed without the in-place overwrite confirmation |

#### Command: olivares keys rotate

Mint a NEW signing key sealed under the KEK, preserving the prior public keys as verifiable history

```
olivares keys rotate
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--in` | `string` | — | **required**. current envelope path (its public key becomes rotation history) |
| `--out` | `string` | — | new envelope path (default: overwrite --in atomically) |
| `--yes` | `bool` | `false` | proceed without the in-place overwrite confirmation |

#### Command: olivares keys seal

Seal an operator config file (its secrets at rest only exist KEK-wrapped)

```
olivares keys seal
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--in` | `string` | — | **required**. plaintext config file |
| `--out` | `string` | — | **required**. sealed output path |

#### Command: olivares keys status

Show the key-custody posture (declared vs configured, envelopes, FIPS mode)

```
olivares keys status
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--audit-envelope` | `string` | — | audit key envelope path (default $OLIVARES_AUDIT_SIGNING_KEY_WRAPPED_FILE) |
| `--catalog-envelope` | `string` | — | catalog key envelope path (default $OLIVARES_CATALOG_SIGNING_KEY_WRAPPED_FILE) |
| `--policy-envelope` | `string` | — | policy key envelope path (default $OLIVARES_POLICY_SIGNING_KEY_WRAPPED_FILE) |
| `--verify-envelopes` | `bool` | `false` | open each envelope under the configured KEK to PROVE its purpose, public key and rotation history are unedited (one KMS call per envelope; without it the report is parsed, not proven) |

#### Command: olivares keys unseal

Open a sealed operator config to STDOUT (debugging; never writes plaintext to disk)

```
olivares keys unseal
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--in` | `string` | — | **required**. sealed config file |

#### Command: olivares keys wrap

Seal a signing key into a CMEK envelope (mint a new key, or migrate an existing plaintext key file)

```
olivares keys wrap
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--from` | `string` | — | existing plaintext key file to migrate (the base64 form in the data dir) |
| `--mint` | `bool` | `false` | mint a fresh key inside the ceremony (never persisted in clear) |
| `--out` | `string` | — | **required**. envelope output path (e.g. audit-signing.key.sealed) |
| `--purpose` | `string` | `audit` | key purpose: audit\|catalog\|policy |

#### Command: olivares knowledge

Govern knowledge bases, data products, memory and DLP

```
olivares knowledge
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--allow-cleartext` | `bool` | `false` | **inherited**. allow sending the credential to a non-loopback host over plain HTTP (DANGEROUS: it travels readable) |
| `--ca-cert` | `string` | — | **inherited**. PEM file containing an additional trusted root CA (default: current context) |
| `--insecure` | `bool` | `false` | **inherited**. skip TLS certificate verification (DANGEROUS; development only) |
| `--pin-sha256` | `stringArray` | `[]` | **inherited**. trusted leaf SPKI SHA-256 pin, base64 or hex, repeatable — the engine prints it as pin_sha256 on the line reporting its certificate (default: current context) |
| `--server` | `string` | — | **inherited**. control-plane base URL (default $OLIVARES_SERVER_URL, then current context) |
| `--tenant` | `string` | — | **inherited**. tenant id (default $OLIVARES_TENANT, then current context) |
| `--timeout` | `duration` | `10s` | **inherited**. request timeout |
| `--token` | `string` | — | **inherited**. API bearer token (prefer --token-file: this form is visible in the process table and in shell history; default $OLIVARES_TOKEN, then current context) |
| `--token-file` | `string` | — | **inherited**. read the API bearer token from a file, or - for stdin |

#### Command: olivares knowledge context-policies

Read and set context/compaction policies

```
olivares knowledge context-policies
```

Declares no flags of its own; it takes those of [`olivares knowledge`](#command-olivares-knowledge) and the root command.

#### Command: olivares knowledge context-policies ls

List context policies

```
olivares knowledge context-policies ls
```

Aliases: `list`

| Flag | Type | Default | Description |
|---|---|---|---|
| `--cursor` | `string` | — | opaque cursor from a previous page's has_more result |
| `--limit` | `int` | `0` | maximum rows per page (server default when unset) |
| `--scope-kind` | `string` | — | only policies of this scope kind |
| `--scope-ref` | `string` | — | only policies of this scope reference |

#### Command: olivares knowledge context-policies put

Create or replace a context policy

```
olivares knowledge context-policies put
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--effect` | `string` | — | policy effect |
| `--max-tokens` | `int64` | `0` | context token budget |
| `--redaction-required` | `bool` | `false` | require redaction for this scope |
| `--scope-kind` | `string` | — | scope kind the policy applies to |
| `--scope-ref` | `string` | — | scope reference the policy applies to |
| `--spec` | `string` | — | extra policy specification as JSON |
| `--spec-file` | `string` | — | file holding the JSON specification (- for stdin) |
| `--strategy` | `string` | — | compaction strategy |

#### Command: olivares knowledge data-products

Govern data products and their versioned contracts

```
olivares knowledge data-products
```

Declares no flags of its own; it takes those of [`olivares knowledge`](#command-olivares-knowledge) and the root command.

#### Command: olivares knowledge data-products archive

Archive a data product

```
olivares knowledge data-products archive <product-id>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `-y`, `--yes` | `bool` | `false` | proceed without the confirmation prompt (required in a non-interactive session) |

#### Command: olivares knowledge data-products contracts

Read and add a data product's versioned contracts

```
olivares knowledge data-products contracts
```

Declares no flags of its own; it takes those of [`olivares knowledge data-products`](#command-olivares-knowledge-data-products) and the root command.

#### Command: olivares knowledge data-products contracts active

Show the contract version currently in force

```
olivares knowledge data-products contracts active <product-id>
```

Declares no flags of its own; it takes those of [`olivares knowledge data-products contracts`](#command-olivares-knowledge-data-products-contracts) and the root command.

#### Command: olivares knowledge data-products contracts add

Add a new contract version to a data product

```
olivares knowledge data-products contracts add <product-id>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--completeness-threshold` | `int64` | `0` | minimum completeness percentage |
| `--freshness-override-seconds` | `int64` | `0` | freshness override for this contract |
| `--note` | `string` | — | note recorded with the contract version |
| `--schema` | `string` | — | contract schema as JSON |
| `--schema-file` | `string` | — | file holding the JSON schema (- for stdin) |
| `--validation-mode` | `string` | — | validation mode the contract enforces |

#### Command: olivares knowledge data-products contracts get

Show one contract version

```
olivares knowledge data-products contracts get <product-id> <version>
```

Declares no flags of its own; it takes those of [`olivares knowledge data-products contracts`](#command-olivares-knowledge-data-products-contracts) and the root command.

#### Command: olivares knowledge data-products contracts ls

List a data product's contract versions

```
olivares knowledge data-products contracts ls <product-id>
```

Aliases: `list`

| Flag | Type | Default | Description |
|---|---|---|---|
| `--cursor` | `string` | — | opaque cursor from a previous page's has_more result |
| `--limit` | `int` | `0` | maximum rows per page (server default when unset) |

#### Command: olivares knowledge data-products create

Declare a data product

```
olivares knowledge data-products create
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--availability-target` | `string` | — | availability target |
| `--description` | `string` | — | human description |
| `--enforcement-mode` | `string` | — | contract enforcement mode |
| `--freshness-sla-seconds` | `int64` | `0` | freshness SLA in seconds |
| `--kb-id` | `string` | — | knowledge base id the product publishes |
| `--kb-ref` | `string` | — | knowledge base reference the product publishes |
| `--name` | `string` | — | data product name |
| `--owner-ref` | `string` | — | owning team or principal |
| `--quality-score` | `int64` | `0` | quality score override |
| `--tags` | `string` | — | tags as a JSON object |
| `--tags-file` | `string` | — | file holding the JSON tags object (- for stdin) |

#### Command: olivares knowledge data-products deprecate

Deprecate a data product

```
olivares knowledge data-products deprecate <product-id>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `-y`, `--yes` | `bool` | `false` | proceed without the confirmation prompt (required in a non-interactive session) |

#### Command: olivares knowledge data-products events

List a data product's enforcement events

```
olivares knowledge data-products events <product-id>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--cursor` | `string` | — | opaque cursor from a previous page's has_more result |
| `--event-type` | `string` | — | only events of this type |
| `--limit` | `int` | `0` | maximum rows per page (server default when unset) |

#### Command: olivares knowledge data-products get

Show one data product

```
olivares knowledge data-products get <product-id>
```

Declares no flags of its own; it takes those of [`olivares knowledge data-products`](#command-olivares-knowledge-data-products) and the root command.

#### Command: olivares knowledge data-products health

Report a data product's freshness and quality

```
olivares knowledge data-products health <product-id>
```

Declares no flags of its own; it takes those of [`olivares knowledge data-products`](#command-olivares-knowledge-data-products) and the root command.

#### Command: olivares knowledge data-products ls

List data products

```
olivares knowledge data-products ls
```

Aliases: `list`

| Flag | Type | Default | Description |
|---|---|---|---|
| `--cursor` | `string` | — | opaque cursor from a previous page's has_more result |
| `--kb-ref` | `string` | — | only products backed by this knowledge base |
| `--limit` | `int` | `0` | maximum rows per page (server default when unset) |
| `--owner-ref` | `string` | — | only products with this owner |
| `--status` | `string` | — | only products in this status |

#### Command: olivares knowledge data-products publish

Publish a data product so its contract governs the corpus

```
olivares knowledge data-products publish <product-id>
```

Declares no flags of its own; it takes those of [`olivares knowledge data-products`](#command-olivares-knowledge-data-products) and the root command.

#### Command: olivares knowledge data-products rm

Delete a data product

```
olivares knowledge data-products rm <product-id>
```

Aliases: `delete`

| Flag | Type | Default | Description |
|---|---|---|---|
| `-y`, `--yes` | `bool` | `false` | proceed without the confirmation prompt (required in a non-interactive session) |

#### Command: olivares knowledge data-products set

Update a data product's authored fields

```
olivares knowledge data-products set <product-id>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--availability-target` | `string` | — | availability target |
| `--description` | `string` | — | human description |
| `--enforcement-mode` | `string` | — | contract enforcement mode |
| `--freshness-sla-seconds` | `int64` | `0` | freshness SLA in seconds |
| `--kb-id` | `string` | — | knowledge base id the product publishes |
| `--kb-ref` | `string` | — | knowledge base reference the product publishes |
| `--name` | `string` | — | data product name |
| `--owner-ref` | `string` | — | owning team or principal |
| `--quality-score` | `int64` | `0` | quality score override |
| `--tags` | `string` | — | tags as a JSON object |
| `--tags-file` | `string` | — | file holding the JSON tags object (- for stdin) |

#### Command: olivares knowledge data-products validate

Validate a payload against the product's active contract

```
olivares knowledge data-products validate <product-id>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--metadata` | `string` | — | validation metadata as a JSON object |
| `--metadata-file` | `string` | — | file holding the JSON metadata (- for stdin) |
| `--payload` | `string` | — | candidate payload as JSON |
| `--payload-file` | `string` | — | file holding the JSON payload (- for stdin) |

#### Command: olivares knowledge dlp

Read and set the DLP egress rules

```
olivares knowledge dlp
```

Declares no flags of its own; it takes those of [`olivares knowledge`](#command-olivares-knowledge) and the root command.

#### Command: olivares knowledge dlp ls

List the DLP egress rules

```
olivares knowledge dlp ls
```

Aliases: `list`

| Flag | Type | Default | Description |
|---|---|---|---|
| `--cursor` | `string` | — | opaque cursor from a previous page's has_more result |
| `--limit` | `int` | `0` | maximum rows per page (server default when unset) |

#### Command: olivares knowledge dlp put

Create or replace one DLP rule

```
olivares knowledge dlp put
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--action` | `string` | — | allow or deny |
| `--class` | `string` | — | sensitivity class the rule governs |
| `--note` | `string` | — | note recorded with the rule |

#### Command: olivares knowledge dlp rm

Delete one DLP rule

```
olivares knowledge dlp rm <rule-id>
```

Aliases: `delete`

| Flag | Type | Default | Description |
|---|---|---|---|
| `-y`, `--yes` | `bool` | `false` | proceed without the confirmation prompt (required in a non-interactive session) |

#### Command: olivares knowledge documents

Inspect an individual knowledge document

```
olivares knowledge documents
```

Declares no flags of its own; it takes those of [`olivares knowledge`](#command-olivares-knowledge) and the root command.

#### Command: olivares knowledge documents get

Show one knowledge document

```
olivares knowledge documents get <document-id>
```

Declares no flags of its own; it takes those of [`olivares knowledge documents`](#command-olivares-knowledge-documents) and the root command.

#### Command: olivares knowledge kbs

Declare, inspect and operate knowledge bases

```
olivares knowledge kbs
```

Declares no flags of its own; it takes those of [`olivares knowledge`](#command-olivares-knowledge) and the root command.

#### Command: olivares knowledge kbs create

Declare a knowledge base

```
olivares knowledge kbs create
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--acl` | `stringArray` | `[]` | default ACL entry granted to every document, repeatable |
| `--classification` | `string` | — | public, internal, confidential or secret (server default: internal) |
| `--embed-policy` | `string` | — | embedding egress policy, e.g. local_only or auto (server default: auto) |
| `--name` | `string` | — | knowledge base name |
| `--residency-region` | `string` | — | region the corpus is pinned to (server default: global) |
| `--status` | `string` | — | knowledge base status (server default: active) |

#### Command: olivares knowledge kbs documents

List a knowledge base's documents

```
olivares knowledge kbs documents <kb-id>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--cursor` | `string` | — | opaque cursor from a previous page's has_more result |
| `--limit` | `int` | `0` | maximum rows per page (server default when unset) |
| `--status` | `string` | — | only documents in this status |

#### Command: olivares knowledge kbs get

Show one knowledge base

```
olivares knowledge kbs get <kb-id>
```

Declares no flags of its own; it takes those of [`olivares knowledge kbs`](#command-olivares-knowledge-kbs) and the root command.

#### Command: olivares knowledge kbs ingest

Ingest documents into a knowledge base

```
olivares knowledge kbs ingest <kb-id>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--documents` | `string` | — | inline documents as a JSON array |
| `--documents-file` | `string` | — | file holding the JSON document array (- for stdin) |
| `--source` | `string` | — | name of a registered content source to pull from |

#### Command: olivares knowledge kbs ls

List the tenant's knowledge bases

```
olivares knowledge kbs ls
```

Aliases: `list`

| Flag | Type | Default | Description |
|---|---|---|---|
| `--cursor` | `string` | — | opaque cursor from a previous page's has_more result |
| `--limit` | `int` | `0` | maximum rows per page (server default when unset) |
| `--status` | `string` | — | only knowledge bases in this status |

#### Command: olivares knowledge kbs query

Run a governed retrieval against a knowledge base

```
olivares knowledge kbs query <kb-id>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--query` | `string` | — | retrieval text |
| `--query-file` | `string` | — | file holding the retrieval text (- for stdin) |
| `--session-ref` | `string` | — | session this retrieval belongs to, recorded in lineage |
| `--top-k` | `int` | `0` | maximum chunks to return (server default when unset) |

#### Command: olivares knowledge kbs reindex

Embed and index the knowledge base's pending chunks

```
olivares knowledge kbs reindex <kb-id>
```

Declares no flags of its own; it takes those of [`olivares knowledge kbs`](#command-olivares-knowledge-kbs) and the root command.

#### Command: olivares knowledge kbs rm

Delete a knowledge base and cascade its documents

```
olivares knowledge kbs rm <kb-id>
```

Aliases: `delete`

| Flag | Type | Default | Description |
|---|---|---|---|
| `-y`, `--yes` | `bool` | `false` | proceed without the confirmation prompt (required in a non-interactive session) |

#### Command: olivares knowledge kbs scan

Run PII discovery over a knowledge base

```
olivares knowledge kbs scan <kb-id>
```

Declares no flags of its own; it takes those of [`olivares knowledge kbs`](#command-olivares-knowledge-kbs) and the root command.

#### Command: olivares knowledge kbs set

Replace a knowledge base's authored fields

```
olivares knowledge kbs set <kb-id>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--acl` | `stringArray` | `[]` | default ACL entry granted to every document, repeatable |
| `--classification` | `string` | — | public, internal, confidential or secret (server default: internal) |
| `--embed-policy` | `string` | — | embedding egress policy, e.g. local_only or auto (server default: auto) |
| `--name` | `string` | — | knowledge base name |
| `--replace` | `bool` | `false` | accept that every field not passed is RESET to its server default (this endpoint replaces, it does not patch) |
| `--residency-region` | `string` | — | region the corpus is pinned to (server default: global) |
| `--status` | `string` | — | knowledge base status (server default: active) |

#### Command: olivares knowledge kbs sync

Delta-sync a knowledge base from its content source

```
olivares knowledge kbs sync <kb-id>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--source` | `string` | — | name of the registered content source to sync from |

#### Command: olivares knowledge labels

Read the sensitivity labels PII discovery wrote

```
olivares knowledge labels
```

Declares no flags of its own; it takes those of [`olivares knowledge`](#command-olivares-knowledge) and the root command.

#### Command: olivares knowledge labels ls

List sensitivity labels

```
olivares knowledge labels ls
```

Aliases: `list`

| Flag | Type | Default | Description |
|---|---|---|---|
| `--cursor` | `string` | — | opaque cursor from a previous page's has_more result |
| `--kb-id` | `string` | — | only labels for this knowledge base |
| `--limit` | `int` | `0` | maximum rows per page (server default when unset) |
| `--subject-kind` | `string` | — | only labels of this subject kind |

#### Command: olivares knowledge lineage

Read the append-only retrieval lineage

```
olivares knowledge lineage
```

Declares no flags of its own; it takes those of [`olivares knowledge`](#command-olivares-knowledge) and the root command.

#### Command: olivares knowledge lineage get

Show one lineage record

```
olivares knowledge lineage get <lineage-id>
```

Declares no flags of its own; it takes those of [`olivares knowledge lineage`](#command-olivares-knowledge-lineage) and the root command.

#### Command: olivares knowledge lineage ls

List retrieval lineage records

```
olivares knowledge lineage ls
```

Aliases: `list`

| Flag | Type | Default | Description |
|---|---|---|---|
| `--agent-ref` | `string` | — | only lineage for this agent |
| `--cursor` | `string` | — | opaque cursor from a previous page's has_more result |
| `--decision` | `string` | — | allowed or denied |
| `--kb-id` | `string` | — | only lineage for this knowledge base |
| `--limit` | `int` | `0` | maximum rows per page (server default when unset) |

#### Command: olivares knowledge memory

Govern agent memory: read, write, verify, export and purge

```
olivares knowledge memory
```

Declares no flags of its own; it takes those of [`olivares knowledge`](#command-olivares-knowledge) and the root command.

#### Command: olivares knowledge memory all

List every memory entry (admin-tier cross-scope view)

```
olivares knowledge memory all
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--agent-ref` | `string` | — | only entries of this agent |
| `--cursor` | `string` | — | opaque cursor from a previous page's has_more result |
| `--limit` | `int` | `0` | maximum rows per page (server default when unset) |
| `--session-ref` | `string` | — | only entries declared in this session scope |
| `--user-ref` | `string` | — | only entries declared in this user scope |

#### Command: olivares knowledge memory export

Export a signed, portable memory bundle

```
olivares knowledge memory export
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--agent-ref` | `string` | — | only entries of this agent |
| `--out` | `string` | — | write the bundle to this file instead of stdout |
| `--session-ref` | `string` | — | only entries declared in this session scope |
| `--user-ref` | `string` | — | only entries declared in this user scope |

#### Command: olivares knowledge memory get

Show one memory entry

```
olivares knowledge memory get <entry-id>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--agent-ref` | `string` | — | only entries of this agent |
| `--session-ref` | `string` | — | only entries declared in this session scope |
| `--user-ref` | `string` | — | only entries declared in this user scope |

#### Command: olivares knowledge memory import

Import a signed portability bundle

```
olivares knowledge memory import
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--bundle-file` | `string` | — | file holding the exported bundle (- for stdin) |

#### Command: olivares knowledge memory ls

List memory entries visible in the declared scope

```
olivares knowledge memory ls
```

Aliases: `list`

| Flag | Type | Default | Description |
|---|---|---|---|
| `--agent-ref` | `string` | — | only entries of this agent |
| `--cursor` | `string` | — | opaque cursor from a previous page's has_more result |
| `--limit` | `int` | `0` | maximum rows per page (server default when unset) |
| `--session-ref` | `string` | — | only entries declared in this session scope |
| `--user-ref` | `string` | — | only entries declared in this user scope |

#### Command: olivares knowledge memory purge

Purge expired memory entries

```
olivares knowledge memory purge
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--agent-ref` | `string` | — | purge only this agent's expired entries |
| `-y`, `--yes` | `bool` | `false` | proceed without the confirmation prompt (required in a non-interactive session) |

#### Command: olivares knowledge memory put

Write one governed memory entry

```
olivares knowledge memory put
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--agent-ref` | `string` | — | agent the entry belongs to |
| `--classification` | `string` | — | entry classification |
| `--content` | `string` | — | entry content |
| `--content-file` | `string` | — | file holding the content (- for stdin) |
| `--key` | `string` | — | entry key within the agent's namespace |
| `--residency-region` | `string` | — | region the entry is pinned to |
| `--session-ref` | `string` | — | declare the entry's session scope |
| `--ttl-seconds` | `int64` | `0` | retention in seconds (0 leaves the module default) |
| `--user-ref` | `string` | — | declare the entry's user scope |

#### Command: olivares knowledge memory rm

Delete one memory entry

```
olivares knowledge memory rm <entry-id>
```

Aliases: `delete`

| Flag | Type | Default | Description |
|---|---|---|---|
| `-y`, `--yes` | `bool` | `false` | proceed without the confirmation prompt (required in a non-interactive session) |

#### Command: olivares knowledge memory verify

Verify memory integrity against the ledger anchor

```
olivares knowledge memory verify
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--agent-ref` | `string` | — | only entries of this agent |
| `--session-ref` | `string` | — | only entries declared in this session scope |
| `--user-ref` | `string` | — | only entries declared in this user scope |

#### Command: olivares knowledge prompts

Manage the versioned prompt registry

```
olivares knowledge prompts
```

Declares no flags of its own; it takes those of [`olivares knowledge`](#command-olivares-knowledge) and the root command.

#### Command: olivares knowledge prompts create

Register a prompt and its first revision

```
olivares knowledge prompts create
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--label` | `string` | — | label for this revision |
| `--name` | `string` | — | prompt name |
| `--note` | `string` | — | note recorded with this revision |
| `--template` | `string` | — | prompt template text |
| `--template-file` | `string` | — | file holding the template (- for stdin) |

#### Command: olivares knowledge prompts get

Show one prompt

```
olivares knowledge prompts get <prompt-id>
```

Declares no flags of its own; it takes those of [`olivares knowledge prompts`](#command-olivares-knowledge-prompts) and the root command.

#### Command: olivares knowledge prompts ls

List registered prompts

```
olivares knowledge prompts ls
```

Aliases: `list`

| Flag | Type | Default | Description |
|---|---|---|---|
| `--cursor` | `string` | — | opaque cursor from a previous page's has_more result |
| `--limit` | `int` | `0` | maximum rows per page (server default when unset) |

#### Command: olivares knowledge prompts revisions

List, read and append immutable prompt revisions

```
olivares knowledge prompts revisions
```

Declares no flags of its own; it takes those of [`olivares knowledge prompts`](#command-olivares-knowledge-prompts) and the root command.

#### Command: olivares knowledge prompts revisions add

Append an immutable revision to a prompt

```
olivares knowledge prompts revisions add <prompt-id>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--label` | `string` | — | label for this revision |
| `--note` | `string` | — | note recorded with this revision |
| `--template` | `string` | — | revision template text |
| `--template-file` | `string` | — | file holding the template (- for stdin) |

#### Command: olivares knowledge prompts revisions get

Show one prompt revision

```
olivares knowledge prompts revisions get <prompt-id> <rev>
```

Declares no flags of its own; it takes those of [`olivares knowledge prompts revisions`](#command-olivares-knowledge-prompts-revisions) and the root command.

#### Command: olivares knowledge prompts revisions ls

List a prompt's revisions

```
olivares knowledge prompts revisions ls <prompt-id>
```

Aliases: `list`

| Flag | Type | Default | Description |
|---|---|---|---|
| `--cursor` | `string` | — | opaque cursor from a previous page's has_more result |
| `--limit` | `int` | `0` | maximum rows per page (server default when unset) |

#### Command: olivares knowledge prompts rollback

Point a prompt at an earlier revision

```
olivares knowledge prompts rollback <prompt-id>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--rev` | `int64` | `0` | revision number to roll back to |
| `-y`, `--yes` | `bool` | `false` | proceed without the confirmation prompt (required in a non-interactive session) |

#### Command: olivares knowledge scans

Read the append-only PII scan evidence

```
olivares knowledge scans
```

Declares no flags of its own; it takes those of [`olivares knowledge`](#command-olivares-knowledge) and the root command.

#### Command: olivares knowledge scans ls

List PII scan runs

```
olivares knowledge scans ls
```

Aliases: `list`

| Flag | Type | Default | Description |
|---|---|---|---|
| `--cursor` | `string` | — | opaque cursor from a previous page's has_more result |
| `--limit` | `int` | `0` | maximum rows per page (server default when unset) |
| `--scope-kind` | `string` | — | only scans of this scope kind |
| `--scope-ref` | `string` | — | only scans of this scope reference |

#### Command: olivares knowledge sources

Run discovery over a registered content source

```
olivares knowledge sources
```

Declares no flags of its own; it takes those of [`olivares knowledge`](#command-olivares-knowledge) and the root command.

#### Command: olivares knowledge sources scan

Scan a content source for personal data without ingesting

```
olivares knowledge sources scan <source-name>
```

Declares no flags of its own; it takes those of [`olivares knowledge sources`](#command-olivares-knowledge-sources) and the root command.

#### Command: olivares license

Manage commercial licenses (install/uninstall/status + keygen/sign/verify; offline Ed25519, never a feature gate)

```
olivares license
```

Declares no flags of its own; it takes those of [`olivares`](#command-olivares) and the root command.

#### Command: olivares license install

Install a license into the data dir (verify + persist; apply live with SIGHUP / runtime reload)

```
olivares license install <file|->
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--data-dir` | `string` | — | data directory to install into (default $OLIVARES_DATA_DIR, an existing ./olivares-data, else $XDG_DATA_HOME/olivares or ~/.local/share/olivares) |
| `--force` | `bool` | `false` | install even though a --license/OLIVARES_LICENSE* override OUTRANKS the data-dir file. Without it the install is REFUSED, because it would change nothing the engine reads; with it the file is staged and the warning says so |
| `--pubkey` | `string` | — | base64 Ed25519 public key to verify against (default: embedded key) |

#### Command: olivares license keygen

Generate one Ed25519 keypair for a license or OTA trust domain

```
olivares license keygen
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--force` | `bool` | `false` | replace existing key files. Without it an existing path is REFUSED, because re-running a ceremony with the wrong path used to destroy the signing anchor in silence. With it the replacement is written to a temporary file beside the target, chmod'ed and verified, and renamed into place |
| `--out-private` | `string` | — | write the private key to this file (created 0600; refuses to overwrite without --force) instead of stdout |
| `--out-public` | `string` | — | write the public key to this file (created 0644; refuses to overwrite without --force) instead of stdout |

#### Command: olivares license sign

Sign a license (requires --key in a release build; uses the dev key only in dev/test builds)

```
olivares license sign
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--expires` | `string` | — | expiry (RFC3339). Empty signs a blob with NO expiry, which the wire format still accepts but commercial entitlements are term-only — every real license gets a date |
| `--features` | `string` | — | comma-separated add-on ids from the fused pricing canon (informational; never a gate) |
| `--holder` | `string` | — | opaque holder id |
| `--key` | `string` | — | base64 Ed25519 private key (default: dev key) |
| `--licensee` | `string` | — | the organization the exception is granted to |
| `--max-users` | `int` | `0` | attested seat figure, DISPLAY-ONLY since B10 — no build caps users on it; leave 0 (unlimited), which is what every self-hosted tier gets |
| `--plan` | `string` | `commercial` | plan label |
| `--support-tier` | `string` | — | attested support relationship label for display only, e.g. standard\|enterprise (empty = none; never gates — SUPPORT.md) |

#### Command: olivares license status

Show the installed license and its status (offline; resolves --license &gt; env &gt; data-dir)

```
olivares license status
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--data-dir` | `string` | — | data directory holding the license (default $OLIVARES_DATA_DIR, an existing ./olivares-data, else $XDG_DATA_HOME/olivares or ~/.local/share/olivares) |
| `--license` | `string` | — | explicit license file path (highest precedence, like serve --license) |
| `--manifest` | `string` | — | OTA channel manifest to read the license CRL from (its signature must verify) |
| `--manifest-sig` | `string` | — | detached manifest signature (default &lt;manifest&gt;.sig) |
| `--ota-pubkey` | `string` | — | base64 or @file Ed25519 OTA key for the manifest (default: the key embedded in this build) |
| `--pubkey` | `string` | — | base64 Ed25519 public key to verify against (default: embedded key) |

#### Command: olivares license uninstall

Remove the installed license from the data dir (the offline half of DELETE /v1/console/license)

```
olivares license uninstall
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--data-dir` | `string` | — | data directory holding the license (default $OLIVARES_DATA_DIR or ./olivares-data) |
| `-y`, `--yes` | `bool` | `false` | proceed without the confirmation prompt (required in a non-interactive session) |

#### Command: olivares license verify

Verify a license against a public key (default: embedded key), with profile/grace and optional CRL status

```
olivares license verify <license-blob>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--manifest` | `string` | — | OTA channel manifest to read the license CRL from (its signature must verify) |
| `--manifest-sig` | `string` | — | detached manifest signature (default &lt;manifest&gt;.sig) |
| `--ota-pubkey` | `string` | — | base64 or @file Ed25519 OTA key for the manifest (default: the key embedded in this build) |
| `--pubkey` | `string` | — | base64 Ed25519 public key (default: embedded key) |

#### Command: olivares mcp

Govern Model Context Protocol resources

```
olivares mcp
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--allow-cleartext` | `bool` | `false` | **inherited**. allow sending the credential to a non-loopback host over plain HTTP (DANGEROUS: it travels readable) |
| `--ca-cert` | `string` | — | **inherited**. PEM file containing an additional trusted root CA (default: current context) |
| `--insecure` | `bool` | `false` | **inherited**. skip TLS certificate verification (DANGEROUS; development only) |
| `--pin-sha256` | `stringArray` | `[]` | **inherited**. trusted leaf SPKI SHA-256 pin, base64 or hex, repeatable — the engine prints it as pin_sha256 on the line reporting its certificate (default: current context) |
| `--server` | `string` | — | **inherited**. control-plane base URL (default $OLIVARES_SERVER_URL, then current context) |
| `--tenant` | `string` | — | **inherited**. tenant id (default $OLIVARES_TENANT, then current context) |
| `--timeout` | `duration` | `10s` | **inherited**. request timeout |
| `--token` | `string` | — | **inherited**. API bearer token (prefer --token-file: this form is visible in the process table and in shell history; default $OLIVARES_TOKEN, then current context) |
| `--token-file` | `string` | — | **inherited**. read the API bearer token from a file, or - for stdin |

#### Command: olivares mcp pins

List and manage approved MCP tool fingerprints

```
olivares mcp pins
```

Declares no flags of its own; it takes those of [`olivares mcp`](#command-olivares-mcp) and the root command.

#### Command: olivares mcp pins approve

Approve an explicit or currently drifted tool fingerprint

```
olivares mcp pins approve <tool>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--fingerprint` | `string` | — | explicit tool-definition fingerprint to approve |
| `--from-drift` | `bool` | `false` | approve the tool's current drift fingerprint |

#### Command: olivares mcp pins ls

List approved MCP tool fingerprints and current drift

```
olivares mcp pins ls
```

Aliases: `list`

Declares no flags of its own; it takes those of [`olivares mcp pins`](#command-olivares-mcp-pins) and the root command.

#### Command: olivares mcp pins rm

Remove an approved MCP tool fingerprint

```
olivares mcp pins rm <tool>
```

Aliases: `remove`, `unpin`

Declares no flags of its own; it takes those of [`olivares mcp pins`](#command-olivares-mcp-pins) and the root command.

#### Command: olivares members

List a tenant's member roster and grant accounts a role in it

```
olivares members
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--allow-cleartext` | `bool` | `false` | **inherited**. allow sending the credential to a non-loopback host over plain HTTP (DANGEROUS: it travels readable) |
| `--ca-cert` | `string` | — | **inherited**. PEM file containing an additional trusted root CA (default: current context) |
| `--insecure` | `bool` | `false` | **inherited**. skip TLS certificate verification (DANGEROUS; development only) |
| `--pin-sha256` | `stringArray` | `[]` | **inherited**. trusted leaf SPKI SHA-256 pin, base64 or hex, repeatable — the engine prints it as pin_sha256 on the line reporting its certificate (default: current context) |
| `--server` | `string` | — | **inherited**. control-plane base URL (default $OLIVARES_SERVER_URL, then current context) |
| `--tenant` | `string` | — | **inherited**. tenant id (default $OLIVARES_TENANT, then current context) |
| `--timeout` | `duration` | `10s` | **inherited**. request timeout |
| `--token` | `string` | — | **inherited**. API bearer token (prefer --token-file: this form is visible in the process table and in shell history; default $OLIVARES_TOKEN, then current context) |
| `--token-file` | `string` | — | **inherited**. read the API bearer token from a file, or - for stdin |

#### Command: olivares members grant

Grant an existing account a role in a tenant

```
olivares members grant
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--role` | `string` | `viewer` | role to grant: viewer, editor, admin or owner |
| `--user` | `string` | — | id of the account to grant (required) |
| `--workspace` | `string` | — | confine the membership to one workspace of the tenant (default: tenant-wide) |

#### Command: olivares members invites

List and revoke the tenant's pending invitations

```
olivares members invites
```

Declares no flags of its own; it takes those of [`olivares members`](#command-olivares-members) and the root command.

#### Command: olivares members invites ls

List the tenant's pending, unexpired invitations

```
olivares members invites ls
```

Aliases: `list`

Declares no flags of its own; it takes those of [`olivares members invites`](#command-olivares-members-invites) and the root command.

#### Command: olivares members invites revoke

Revoke a pending invitation

```
olivares members invites revoke <invite-id>
```

Aliases: `delete`, `rm`

| Flag | Type | Default | Description |
|---|---|---|---|
| `-y`, `--yes` | `bool` | `false` | proceed without the confirmation prompt (required in a non-interactive session) |

#### Command: olivares members ls

List the resolved tenant's member roster

```
olivares members ls
```

Aliases: `list`

Declares no flags of its own; it takes those of [`olivares members`](#command-olivares-members) and the root command.

#### Command: olivares migrate

Inspect the engine's schema-migration state (read-only)

```
olivares migrate
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--format` | `string` | `text` | **inherited**. deprecated alias for -o/--output on this command (text or json) — NOT the export-format flag of 'audit export' / 'findings export' |

#### Command: olivares migrate manifest

Print this binary's registered schema manifest (deterministic; the open≡enterprise parity oracle)

```
olivares migrate manifest
```

Declares no flags of its own; it takes those of [`olivares migrate`](#command-olivares-migrate) and the root command.

#### Command: olivares migrate status

List applied schema migrations and their expand/contract phase (read-only)

```
olivares migrate status
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--data-dir` | `string` | — | data directory holding olivares.db (sqlite; defaults to $OLIVARES_DATA_DIR, an existing ./olivares-data, else $XDG_DATA_HOME/olivares or ~/.local/share/olivares) |
| `--dsn` | `string` | — | connection string to read (postgres; or an explicit sqlite file path). Accepts a file:/env: reference |
| `--engine` | `string` | `sqlite` | store engine: sqlite or postgres |

#### Command: olivares models

Govern the model estate, routing, registry and model access

```
olivares models
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--allow-cleartext` | `bool` | `false` | **inherited**. allow sending the credential to a non-loopback host over plain HTTP (DANGEROUS: it travels readable) |
| `--ca-cert` | `string` | — | **inherited**. PEM file containing an additional trusted root CA (default: current context) |
| `--insecure` | `bool` | `false` | **inherited**. skip TLS certificate verification (DANGEROUS; development only) |
| `--pin-sha256` | `stringArray` | `[]` | **inherited**. trusted leaf SPKI SHA-256 pin, base64 or hex, repeatable — the engine prints it as pin_sha256 on the line reporting its certificate (default: current context) |
| `--server` | `string` | — | **inherited**. control-plane base URL (default $OLIVARES_SERVER_URL, then current context) |
| `--tenant` | `string` | — | **inherited**. tenant id (default $OLIVARES_TENANT, then current context) |
| `--timeout` | `duration` | `10s` | **inherited**. request timeout |
| `--token` | `string` | — | **inherited**. API bearer token (prefer --token-file: this form is visible in the process table and in shell history; default $OLIVARES_TOKEN, then current context) |
| `--token-file` | `string` | — | **inherited**. read the API bearer token from a file, or - for stdin |

#### Command: olivares models access

Author model-access grants (who may use which model)

```
olivares models access
```

Aliases: `model-access`

Declares no flags of its own; it takes those of [`olivares models`](#command-olivares-models) and the root command.

#### Command: olivares models access create

Create a model-access grant

```
olivares models access create
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--data` | `string` | — | request document: inline JSON, @FILE, or - for stdin |

#### Command: olivares models access ls

List model-access grants

```
olivares models access ls
```

Aliases: `list`

| Flag | Type | Default | Description |
|---|---|---|---|
| `--all` | `bool` | `false` | follow the cursor to the end and emit one merged page (json output carries has_more:false and no cursor) |
| `--cursor` | `string` | — | opaque cursor from a previous page's cursor field |
| `--limit` | `int` | `0` | page size to request (0 leaves the control plane's default) |
| `--subject-kind` | `string` | — | only grants whose subject is of this kind (user, role, agent_group) |
| `--subject-ref` | `string` | — | only grants for this subject reference |
| `--target-kind` | `string` | — | only grants whose target is of this kind (model, model_group) |
| `--target-ref` | `string` | — | only grants for this target reference |

#### Command: olivares models access rm

Delete a model-access grant

```
olivares models access rm <grant-id>
```

Aliases: `delete`

| Flag | Type | Default | Description |
|---|---|---|---|
| `-y`, `--yes` | `bool` | `false` | proceed without the confirmation prompt (required in a non-interactive session) |

#### Command: olivares models access update

Replace a model-access grant

```
olivares models access update <grant-id>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--data` | `string` | — | request document: inline JSON, @FILE, or - for stdin |

#### Command: olivares models admission

Govern the signed-model admission trust root and read its verdicts

```
olivares models admission
```

Declares no flags of its own; it takes those of [`olivares models`](#command-olivares-models) and the root command.

#### Command: olivares models admission ls

List recorded admission verdicts

```
olivares models admission ls
```

Aliases: `list`

| Flag | Type | Default | Description |
|---|---|---|---|
| `--all` | `bool` | `false` | follow the cursor to the end and emit one merged page (json output carries has_more:false and no cursor) |
| `--cursor` | `string` | — | opaque cursor from a previous page's cursor field |
| `--limit` | `int` | `0` | page size to request (0 leaves the control plane's default) |
| `--verified` | `string` | — | only verdicts with this verification outcome (true or false) |
| `--version-ref` | `string` | — | only verdicts for this version |

#### Command: olivares models admission policy

Show the admission trust root

```
olivares models admission policy
```

Declares no flags of its own; it takes those of [`olivares models admission`](#command-olivares-models-admission) and the root command.

#### Command: olivares models admission set-policy

Replace the admission trust root

```
olivares models admission set-policy
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--data` | `string` | — | request document: inline JSON, @FILE, or - for stdin |

#### Command: olivares models agent-artifacts

Govern the agent-artifact supply chain

```
olivares models agent-artifacts
```

Aliases: `artifacts`

Declares no flags of its own; it takes those of [`olivares models`](#command-olivares-models) and the root command.

#### Command: olivares models agent-artifacts aibom

Generate the agent-supply-chain BOM

```
olivares models agent-artifacts aibom
```

Declares no flags of its own; it takes those of [`olivares models agent-artifacts`](#command-olivares-models-agent-artifacts) and the root command.

#### Command: olivares models agent-artifacts create

Register an agent artifact

```
olivares models agent-artifacts create
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--data` | `string` | — | request document: inline JSON, @FILE, or - for stdin |

#### Command: olivares models agent-artifacts ls

List governed agent artifacts

```
olivares models agent-artifacts ls
```

Aliases: `list`

| Flag | Type | Default | Description |
|---|---|---|---|
| `--all` | `bool` | `false` | follow the cursor to the end and emit one merged page (json output carries has_more:false and no cursor) |
| `--artifact-class` | `string` | — | only artifacts of this class (skill, mcpb_extension, mcp_app_template, agents_md) |
| `--cursor` | `string` | — | opaque cursor from a previous page's cursor field |
| `--limit` | `int` | `0` | page size to request (0 leaves the control plane's default) |

#### Command: olivares models agent-artifacts rm

Remove an agent artifact

```
olivares models agent-artifacts rm <artifact-id>
```

Aliases: `delete`

| Flag | Type | Default | Description |
|---|---|---|---|
| `-y`, `--yes` | `bool` | `false` | proceed without the confirmation prompt (required in a non-interactive session) |

#### Command: olivares models agent-artifacts seal

Seal the agent-supply-chain BOM to the ledger

```
olivares models agent-artifacts seal
```

Declares no flags of its own; it takes those of [`olivares models agent-artifacts`](#command-olivares-models-agent-artifacts) and the root command.

#### Command: olivares models agent-artifacts seals

List agent-supply-chain BOM seals

```
olivares models agent-artifacts seals
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--all` | `bool` | `false` | follow the cursor to the end and emit one merged page (json output carries has_more:false and no cursor) |
| `--cursor` | `string` | — | opaque cursor from a previous page's cursor field |
| `--limit` | `int` | `0` | page size to request (0 leaves the control plane's default) |

#### Command: olivares models aibom

Generate, seal and list AI bills of materials

```
olivares models aibom
```

Declares no flags of its own; it takes those of [`olivares models`](#command-olivares-models) and the root command.

#### Command: olivares models aibom card

Render the model card for one owned model

```
olivares models aibom card <owned-id>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--format` | `string` | — | card format: the module's default (JSON) or md |

#### Command: olivares models aibom get

Generate the AIBOM for one owned model

```
olivares models aibom get <owned-id>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--format` | `string` | — | document format: the module's default (CycloneDX) or spdx |

#### Command: olivares models aibom ls

List AIBOM seals

```
olivares models aibom ls
```

Aliases: `list`

| Flag | Type | Default | Description |
|---|---|---|---|
| `--all` | `bool` | `false` | follow the cursor to the end and emit one merged page (json output carries has_more:false and no cursor) |
| `--cursor` | `string` | — | opaque cursor from a previous page's cursor field |
| `--limit` | `int` | `0` | page size to request (0 leaves the control plane's default) |
| `--owned-ref` | `string` | — | only seals for this owned model |

#### Command: olivares models aibom seal

Seal the current AIBOM to the ledger as evidence

```
olivares models aibom seal <owned-id>
```

Declares no flags of its own; it takes those of [`olivares models aibom`](#command-olivares-models-aibom) and the root command.

#### Command: olivares models catalog

Show the declared reference catalog (capabilities and list pricing)

```
olivares models catalog
```

Declares no flags of its own; it takes those of [`olivares models`](#command-olivares-models) and the root command.

#### Command: olivares models data-governance

Show the context-management / memory / ZDR matrix

```
olivares models data-governance
```

Declares no flags of its own; it takes those of [`olivares models`](#command-olivares-models) and the root command.

#### Command: olivares models datasets

Govern dataset lineage components

```
olivares models datasets
```

Declares no flags of its own; it takes those of [`olivares models`](#command-olivares-models) and the root command.

#### Command: olivares models datasets create

Register a dataset

```
olivares models datasets create
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--data` | `string` | — | request document: inline JSON, @FILE, or - for stdin |

#### Command: olivares models datasets ls

List governed datasets

```
olivares models datasets ls
```

Aliases: `list`

| Flag | Type | Default | Description |
|---|---|---|---|
| `--all` | `bool` | `false` | follow the cursor to the end and emit one merged page (json output carries has_more:false and no cursor) |
| `--cursor` | `string` | — | opaque cursor from a previous page's cursor field |
| `--limit` | `int` | `0` | page size to request (0 leaves the control plane's default) |
| `--owned-ref` | `string` | — | only datasets of this owned model |

#### Command: olivares models datasets rm

Remove a dataset

```
olivares models datasets rm <dataset-id>
```

Aliases: `delete`

| Flag | Type | Default | Description |
|---|---|---|---|
| `-y`, `--yes` | `bool` | `false` | proceed without the confirmation prompt (required in a non-interactive session) |

#### Command: olivares models deployments

Govern local inference deployments

```
olivares models deployments
```

Declares no flags of its own; it takes those of [`olivares models`](#command-olivares-models) and the root command.

#### Command: olivares models deployments create

Register an inference deployment

```
olivares models deployments create
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--data` | `string` | — | request document: inline JSON, @FILE, or - for stdin |

#### Command: olivares models deployments ls

List inference deployments

```
olivares models deployments ls
```

Aliases: `list`

| Flag | Type | Default | Description |
|---|---|---|---|
| `--all` | `bool` | `false` | follow the cursor to the end and emit one merged page (json output carries has_more:false and no cursor) |
| `--cursor` | `string` | — | opaque cursor from a previous page's cursor field |
| `--limit` | `int` | `0` | page size to request (0 leaves the control plane's default) |
| `--runtime` | `string` | — | only deployments on this runtime |
| `--status` | `string` | — | only deployments in this status |

#### Command: olivares models deployments rm

Remove an inference deployment

```
olivares models deployments rm <deployment-id>
```

Aliases: `delete`

| Flag | Type | Default | Description |
|---|---|---|---|
| `-y`, `--yes` | `bool` | `false` | proceed without the confirmation prompt (required in a non-interactive session) |

#### Command: olivares models deployments update

Replace an inference deployment

```
olivares models deployments update <deployment-id>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--data` | `string` | — | request document: inline JSON, @FILE, or - for stdin |

#### Command: olivares models entitlements

Attest provider entitlement state for restricted access tiers

```
olivares models entitlements
```

Declares no flags of its own; it takes those of [`olivares models`](#command-olivares-models) and the root command.

#### Command: olivares models entitlements ls

List access-tier entitlement attestations

```
olivares models entitlements ls
```

Aliases: `list`

| Flag | Type | Default | Description |
|---|---|---|---|
| `--all` | `bool` | `false` | follow the cursor to the end and emit one merged page (json output carries has_more:false and no cursor) |
| `--cursor` | `string` | — | opaque cursor from a previous page's cursor field |
| `--limit` | `int` | `0` | page size to request (0 leaves the control plane's default) |
| `--state` | `string` | — | only entitlements in this state |
| `--tier` | `string` | — | only this access tier |

#### Command: olivares models entitlements set

Attest the entitlement state of one access tier

```
olivares models entitlements set
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--data` | `string` | — | request document: inline JSON, @FILE, or - for stdin |

#### Command: olivares models features

Show which model families declare each API capability

```
olivares models features
```

Declares no flags of its own; it takes those of [`olivares models`](#command-olivares-models) and the root command.

#### Command: olivares models finetune

Record fine-tune jobs and their outcome

```
olivares models finetune
```

Declares no flags of its own; it takes those of [`olivares models`](#command-olivares-models) and the root command.

#### Command: olivares models finetune create

Record a fine-tune job

```
olivares models finetune create
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--data` | `string` | — | request document: inline JSON, @FILE, or - for stdin |

#### Command: olivares models finetune get

Show one fine-tune job record

```
olivares models finetune get <job-id>
```

Declares no flags of its own; it takes those of [`olivares models finetune`](#command-olivares-models-finetune) and the root command.

#### Command: olivares models finetune ls

List fine-tune job records

```
olivares models finetune ls
```

Aliases: `list`

| Flag | Type | Default | Description |
|---|---|---|---|
| `--all` | `bool` | `false` | follow the cursor to the end and emit one merged page (json output carries has_more:false and no cursor) |
| `--cursor` | `string` | — | opaque cursor from a previous page's cursor field |
| `--limit` | `int` | `0` | page size to request (0 leaves the control plane's default) |
| `--status` | `string` | — | only jobs in this status |

#### Command: olivares models finetune update

Replace a fine-tune job record

```
olivares models finetune update <job-id>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--data` | `string` | — | request document: inline JSON, @FILE, or - for stdin |

#### Command: olivares models get

Show one governed model

```
olivares models get <model-id>
```

Declares no flags of its own; it takes those of [`olivares models`](#command-olivares-models) and the root command.

#### Command: olivares models gpai

Attest per-provider GPAI compliance posture

```
olivares models gpai
```

Declares no flags of its own; it takes those of [`olivares models`](#command-olivares-models) and the root command.

#### Command: olivares models gpai attest

Attest one provider's GPAI posture

```
olivares models gpai attest
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--data` | `string` | — | request document: inline JSON, @FILE, or - for stdin |

#### Command: olivares models gpai ls

List attested GPAI posture per provider

```
olivares models gpai ls
```

Aliases: `list`

| Flag | Type | Default | Description |
|---|---|---|---|
| `--all` | `bool` | `false` | follow the cursor to the end and emit one merged page (json output carries has_more:false and no cursor) |
| `--cursor` | `string` | — | opaque cursor from a previous page's cursor field |
| `--limit` | `int` | `0` | page size to request (0 leaves the control plane's default) |
| `--provider-ref` | `string` | — | only this provider |

#### Command: olivares models groups

Author named model groups

```
olivares models groups
```

Aliases: `model-groups`

Declares no flags of its own; it takes those of [`olivares models`](#command-olivares-models) and the root command.

#### Command: olivares models groups create

Create a model group

```
olivares models groups create
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--data` | `string` | — | request document: inline JSON, @FILE, or - for stdin |

#### Command: olivares models groups get

Show one model group

```
olivares models groups get <group-id>
```

Declares no flags of its own; it takes those of [`olivares models groups`](#command-olivares-models-groups) and the root command.

#### Command: olivares models groups ls

List model groups

```
olivares models groups ls
```

Aliases: `list`

| Flag | Type | Default | Description |
|---|---|---|---|
| `--all` | `bool` | `false` | follow the cursor to the end and emit one merged page (json output carries has_more:false and no cursor) |
| `--cursor` | `string` | — | opaque cursor from a previous page's cursor field |
| `--limit` | `int` | `0` | page size to request (0 leaves the control plane's default) |

#### Command: olivares models groups rm

Delete a model group

```
olivares models groups rm <group-id>
```

Aliases: `delete`

| Flag | Type | Default | Description |
|---|---|---|---|
| `-y`, `--yes` | `bool` | `false` | proceed without the confirmation prompt (required in a non-interactive session) |

#### Command: olivares models groups update

Replace a model group

```
olivares models groups update <group-id>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--data` | `string` | — | request document: inline JSON, @FILE, or - for stdin |

#### Command: olivares models keys

Govern provider API-key and workspace references

```
olivares models keys
```

Declares no flags of its own; it takes those of [`olivares models`](#command-olivares-models) and the root command.

#### Command: olivares models keys create

Register a provider key or workspace reference

```
olivares models keys create
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--data` | `string` | — | request document: inline JSON, @FILE, or - for stdin |

#### Command: olivares models keys ls

List provider key and workspace references

```
olivares models keys ls
```

Aliases: `list`

| Flag | Type | Default | Description |
|---|---|---|---|
| `--all` | `bool` | `false` | follow the cursor to the end and emit one merged page (json output carries has_more:false and no cursor) |
| `--cursor` | `string` | — | opaque cursor from a previous page's cursor field |
| `--limit` | `int` | `0` | page size to request (0 leaves the control plane's default) |
| `--provider-ref` | `string` | — | only references for this provider |
| `--ref-kind` | `string` | — | only references of this kind (e.g. api_key, workspace) |
| `--status` | `string` | — | only references in this status |

#### Command: olivares models keys rm

Remove a key or workspace reference

```
olivares models keys rm <ref-id>
```

Aliases: `delete`

| Flag | Type | Default | Description |
|---|---|---|---|
| `-y`, `--yes` | `bool` | `false` | proceed without the confirmation prompt (required in a non-interactive session) |

#### Command: olivares models keys update

Replace a key or workspace reference

```
olivares models keys update <ref-id>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--data` | `string` | — | request document: inline JSON, @FILE, or - for stdin |

#### Command: olivares models ls

List the governed model estate

```
olivares models ls
```

Aliases: `list`

| Flag | Type | Default | Description |
|---|---|---|---|
| `--all` | `bool` | `false` | follow the cursor to the end and emit one merged page (json output carries has_more:false and no cursor) |
| `--cursor` | `string` | — | opaque cursor from a previous page's cursor field |
| `--limit` | `int` | `0` | page size to request (0 leaves the control plane's default) |

#### Command: olivares models owned

Govern the own-model registry

```
olivares models owned
```

Declares no flags of its own; it takes those of [`olivares models`](#command-olivares-models) and the root command.

#### Command: olivares models owned create

Register an owned model

```
olivares models owned create
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--data` | `string` | — | request document: inline JSON, @FILE, or - for stdin |

#### Command: olivares models owned get

Show one owned model

```
olivares models owned get <owned-id>
```

Declares no flags of its own; it takes those of [`olivares models owned`](#command-olivares-models-owned) and the root command.

#### Command: olivares models owned ls

List owned models

```
olivares models owned ls
```

Aliases: `list`

| Flag | Type | Default | Description |
|---|---|---|---|
| `--all` | `bool` | `false` | follow the cursor to the end and emit one merged page (json output carries has_more:false and no cursor) |
| `--cursor` | `string` | — | opaque cursor from a previous page's cursor field |
| `--kind` | `string` | — | only models of this kind |
| `--limit` | `int` | `0` | page size to request (0 leaves the control plane's default) |
| `--status` | `string` | — | only models in this status |

#### Command: olivares models owned rm

Remove an owned model from the registry

```
olivares models owned rm <owned-id>
```

Aliases: `delete`

| Flag | Type | Default | Description |
|---|---|---|---|
| `-y`, `--yes` | `bool` | `false` | proceed without the confirmation prompt (required in a non-interactive session) |

#### Command: olivares models owned update

Replace an owned-model entry

```
olivares models owned update <owned-id>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--data` | `string` | — | request document: inline JSON, @FILE, or - for stdin |

#### Command: olivares models platforms

Show the deployment-surface matrix and per-platform lifecycle

```
olivares models platforms
```

Declares no flags of its own; it takes those of [`olivares models`](#command-olivares-models) and the root command.

#### Command: olivares models rate-limits

Show the provider rate-limit inventory a gateway must mirror

```
olivares models rate-limits
```

Declares no flags of its own; it takes those of [`olivares models`](#command-olivares-models) and the root command.

#### Command: olivares models residency

Govern per-workspace inference-geo residency

```
olivares models residency
```

Declares no flags of its own; it takes those of [`olivares models`](#command-olivares-models) and the root command.

#### Command: olivares models residency ls

List per-workspace residency records

```
olivares models residency ls
```

Aliases: `list`

| Flag | Type | Default | Description |
|---|---|---|---|
| `--all` | `bool` | `false` | follow the cursor to the end and emit one merged page (json output carries has_more:false and no cursor) |
| `--cursor` | `string` | — | opaque cursor from a previous page's cursor field |
| `--limit` | `int` | `0` | page size to request (0 leaves the control plane's default) |
| `--workspace-ref` | `string` | — | only this workspace |

#### Command: olivares models residency set

Declare a workspace's permitted inference geographies

```
olivares models residency set
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--data` | `string` | — | request document: inline JSON, @FILE, or - for stdin |

#### Command: olivares models routing

Author routing policies and resolve or execute them

```
olivares models routing
```

Declares no flags of its own; it takes those of [`olivares models`](#command-olivares-models) and the root command.

#### Command: olivares models routing create

Create a routing policy

```
olivares models routing create
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--data` | `string` | — | request document: inline JSON, @FILE, or - for stdin |

#### Command: olivares models routing execute

Execute a routing policy through the governed executor (SPENDS)

```
olivares models routing execute <policy-id>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--data` | `string` | — | request document: inline JSON, @FILE, or - for stdin |

#### Command: olivares models routing get

Show one routing policy

```
olivares models routing get <policy-id>
```

Declares no flags of its own; it takes those of [`olivares models routing`](#command-olivares-models-routing) and the root command.

#### Command: olivares models routing ls

List routing policies

```
olivares models routing ls
```

Aliases: `list`

| Flag | Type | Default | Description |
|---|---|---|---|
| `--all` | `bool` | `false` | follow the cursor to the end and emit one merged page (json output carries has_more:false and no cursor) |
| `--cursor` | `string` | — | opaque cursor from a previous page's cursor field |
| `--limit` | `int` | `0` | page size to request (0 leaves the control plane's default) |

#### Command: olivares models routing resolve

Resolve a policy to the routing decision it would produce

```
olivares models routing resolve <policy-id>
```

Declares no flags of its own; it takes those of [`olivares models routing`](#command-olivares-models-routing) and the root command.

#### Command: olivares models routing rm

Delete a routing policy

```
olivares models routing rm <policy-id>
```

Aliases: `delete`

| Flag | Type | Default | Description |
|---|---|---|---|
| `-y`, `--yes` | `bool` | `false` | proceed without the confirmation prompt (required in a non-interactive session) |

#### Command: olivares models routing update

Replace a routing policy in place

```
olivares models routing update <policy-id>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--data` | `string` | — | request document: inline JSON, @FILE, or - for stdin |

#### Command: olivares models tool-types

Show the dated tool-type catalog and its cost cross-walk

```
olivares models tool-types
```

Declares no flags of its own; it takes those of [`olivares models`](#command-olivares-models) and the root command.

#### Command: olivares models versions

Govern owned-model versions and their signed admission

```
olivares models versions
```

Declares no flags of its own; it takes those of [`olivares models`](#command-olivares-models) and the root command.

#### Command: olivares models versions admit

Run the signed-model admission ceremony against a version

```
olivares models versions admit <version-id>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--data` | `string` | — | request document: inline JSON, @FILE, or - for stdin |

#### Command: olivares models versions create

Register an owned-model version

```
olivares models versions create
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--data` | `string` | — | request document: inline JSON, @FILE, or - for stdin |

#### Command: olivares models versions ls

List owned-model versions

```
olivares models versions ls
```

Aliases: `list`

| Flag | Type | Default | Description |
|---|---|---|---|
| `--all` | `bool` | `false` | follow the cursor to the end and emit one merged page (json output carries has_more:false and no cursor) |
| `--cursor` | `string` | — | opaque cursor from a previous page's cursor field |
| `--limit` | `int` | `0` | page size to request (0 leaves the control plane's default) |
| `--owned-ref` | `string` | — | only versions of this owned model |
| `--status` | `string` | — | only versions in this status |

#### Command: olivares models versions rm

Remove an owned-model version

```
olivares models versions rm <version-id>
```

Aliases: `delete`

| Flag | Type | Default | Description |
|---|---|---|---|
| `-y`, `--yes` | `bool` | `false` | proceed without the confirmation prompt (required in a non-interactive session) |

#### Command: olivares notify

Author notification routes and inspect deliveries and the outbox

```
olivares notify
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--allow-cleartext` | `bool` | `false` | **inherited**. allow sending the credential to a non-loopback host over plain HTTP (DANGEROUS: it travels readable) |
| `--ca-cert` | `string` | — | **inherited**. PEM file containing an additional trusted root CA (default: current context) |
| `--insecure` | `bool` | `false` | **inherited**. skip TLS certificate verification (DANGEROUS; development only) |
| `--pin-sha256` | `stringArray` | `[]` | **inherited**. trusted leaf SPKI SHA-256 pin, base64 or hex, repeatable — the engine prints it as pin_sha256 on the line reporting its certificate (default: current context) |
| `--server` | `string` | — | **inherited**. control-plane base URL (default $OLIVARES_SERVER_URL, then current context) |
| `--tenant` | `string` | — | **inherited**. tenant id (default $OLIVARES_TENANT, then current context) |
| `--timeout` | `duration` | `10s` | **inherited**. request timeout |
| `--token` | `string` | — | **inherited**. API bearer token (prefer --token-file: this form is visible in the process table and in shell history; default $OLIVARES_TOKEN, then current context) |
| `--token-file` | `string` | — | **inherited**. read the API bearer token from a file, or - for stdin |

#### Command: olivares notify deliveries

List the append-only delivery ledger

```
olivares notify deliveries
```

Aliases: `ledger`

| Flag | Type | Default | Description |
|---|---|---|---|
| `--cursor` | `string` | — | continue from the cursor printed by the previous page |
| `--destination` | `string` | — | filter by destination |
| `--finding-kind` | `string` | — | filter by finding kind |
| `--limit` | `int` | `0` | maximum rows to return in one page (0 = the engine's default) |
| `--route` | `string` | — | filter by route id |
| `--status` | `string` | — | filter by delivery status |

#### Command: olivares notify destinations

List the destinations THIS tenant may address

```
olivares notify destinations
```

Aliases: `dests`

Declares no flags of its own; it takes those of [`olivares notify`](#command-olivares-notify) and the root command.

#### Command: olivares notify evaluate

Ask which routes a signal WOULD select, delivering nothing

```
olivares notify evaluate
```

Aliases: `dry-run`, `eval`

| Flag | Type | Default | Description |
|---|---|---|---|
| `--event-type` | `string` | — | the signal's event type (required) |
| `--kind` | `string` | — | the finding kind |
| `--severity` | `string` | — | the signal's severity |
| `--source` | `string` | — | the signal's source |
| `--subject-kind` | `string` | — | the subject's kind |

#### Command: olivares notify match-types

List the event types a route may match

```
olivares notify match-types
```

Aliases: `types`

Declares no flags of its own; it takes those of [`olivares notify`](#command-olivares-notify) and the root command.

#### Command: olivares notify outbox

Inspect the durable outbox and requeue terminal rows

```
olivares notify outbox
```

Aliases: `dlq`

Declares no flags of its own; it takes those of [`olivares notify`](#command-olivares-notify) and the root command.

#### Command: olivares notify outbox ls

List durable outbox rows

```
olivares notify outbox ls
```

Aliases: `list`

| Flag | Type | Default | Description |
|---|---|---|---|
| `--cursor` | `string` | — | continue from the cursor printed by the previous page |
| `--destination` | `string` | — | filter by destination |
| `--limit` | `int` | `0` | maximum rows to return in one page (0 = the engine's default) |
| `--status` | `string` | — | filter by status: queued, delivering, delivered or dead |

#### Command: olivares notify outbox redeliver

Requeue a terminal outbox row for another delivery attempt (admin-tier)

```
olivares notify outbox redeliver <outbox-id>
```

Aliases: `requeue`

Declares no flags of its own; it takes those of [`olivares notify outbox`](#command-olivares-notify-outbox) and the root command.

#### Command: olivares notify routes

Author, inspect, test and roll back notification routes

```
olivares notify routes
```

Aliases: `route`

Declares no flags of its own; it takes those of [`olivares notify`](#command-olivares-notify) and the root command.

#### Command: olivares notify routes create

Declare a notification route

```
olivares notify routes create
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--dedup-window` | `int64` | `0` | seconds within which an identical signal is suppressed |
| `--destination` | `string` | — | the provisioned destination to send to (required; see `notify destinations`) |
| `--enabled` | `bool` | `true` | whether the route may fire |
| `--match-kind` | `stringSlice` | `[]` | finding kind to match, repeatable |
| `--match-source` | `stringSlice` | `[]` | signal source to match, repeatable |
| `--match-subject-kind` | `stringSlice` | `[]` | subject kind to match, repeatable |
| `--match-type` | `stringSlice` | `[]` | event type to match, repeatable (see `notify match-types`) |
| `--min-severity` | `string` | — | severity floor: info, low, medium, high or critical (empty = no floor) |
| `--name` | `string` | — | the route's name (required, unique in the tenant) |
| `--priority` | `int64` | `0` | ordering among matching routes |
| `--throttle-window` | `int64` | `0` | seconds within which this route sends at most once |

#### Command: olivares notify routes get

Show one route's full predicate

```
olivares notify routes get <route-id>
```

Declares no flags of its own; it takes those of [`olivares notify routes`](#command-olivares-notify-routes) and the root command.

#### Command: olivares notify routes ls

List notification routes

```
olivares notify routes ls
```

Aliases: `list`

| Flag | Type | Default | Description |
|---|---|---|---|
| `--cursor` | `string` | — | continue from the cursor printed by the previous page |
| `--destination` | `string` | — | only routes targeting this destination |
| `--enabled` | `string` | — | only enabled (true) or only disabled (false) routes |
| `--limit` | `int` | `0` | maximum rows to return in one page (0 = the engine's default) |

#### Command: olivares notify routes restore

Put a route back to an earlier revision

```
olivares notify routes restore <route-id>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--revision-id` | `string` | — | the revision to restore (required) |

#### Command: olivares notify routes revisions

List a route's revision ledger

```
olivares notify routes revisions <route-id>
```

Aliases: `history`

| Flag | Type | Default | Description |
|---|---|---|---|
| `--cursor` | `string` | — | continue from the cursor printed by the previous page |
| `--limit` | `int` | `0` | maximum rows to return in one page (0 = the engine's default) |

#### Command: olivares notify routes rm

Delete a route (admin-tier)

```
olivares notify routes rm <route-id>
```

Aliases: `delete`, `remove`

| Flag | Type | Default | Description |
|---|---|---|---|
| `-y`, `--yes` | `bool` | `false` | proceed without the confirmation prompt (required in a non-interactive session) |

#### Command: olivares notify routes test

Send a REAL test notification through a route (admin-tier)

```
olivares notify routes test <route-id>
```

Declares no flags of its own; it takes those of [`olivares notify routes`](#command-olivares-notify-routes) and the root command.

#### Command: olivares notify routes update

Replace a route's predicate

```
olivares notify routes update <route-id>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--dedup-window` | `int64` | `0` | seconds within which an identical signal is suppressed |
| `--destination` | `string` | — | the provisioned destination to send to (required; see `notify destinations`) |
| `--enabled` | `bool` | `true` | whether the route may fire |
| `--match-kind` | `stringSlice` | `[]` | finding kind to match, repeatable |
| `--match-source` | `stringSlice` | `[]` | signal source to match, repeatable |
| `--match-subject-kind` | `stringSlice` | `[]` | subject kind to match, repeatable |
| `--match-type` | `stringSlice` | `[]` | event type to match, repeatable (see `notify match-types`) |
| `--min-severity` | `string` | — | severity floor: info, low, medium, high or critical (empty = no floor) |
| `--name` | `string` | — | the route's name |
| `--priority` | `int64` | `0` | ordering among matching routes |
| `--throttle-window` | `int64` | `0` | seconds within which this route sends at most once |

#### Command: olivares observability

Inspect ingestion health, ledger traces and binary attestation

```
olivares observability
```

Aliases: `obs`

| Flag | Type | Default | Description |
|---|---|---|---|
| `--allow-cleartext` | `bool` | `false` | **inherited**. allow sending the credential to a non-loopback host over plain HTTP (DANGEROUS: it travels readable) |
| `--ca-cert` | `string` | — | **inherited**. PEM file containing an additional trusted root CA (default: current context) |
| `--insecure` | `bool` | `false` | **inherited**. skip TLS certificate verification (DANGEROUS; development only) |
| `--pin-sha256` | `stringArray` | `[]` | **inherited**. trusted leaf SPKI SHA-256 pin, base64 or hex, repeatable — the engine prints it as pin_sha256 on the line reporting its certificate (default: current context) |
| `--server` | `string` | — | **inherited**. control-plane base URL (default $OLIVARES_SERVER_URL, then current context) |
| `--tenant` | `string` | — | **inherited**. tenant id (default $OLIVARES_TENANT, then current context) |
| `--timeout` | `duration` | `10s` | **inherited**. request timeout |
| `--token` | `string` | — | **inherited**. API bearer token (prefer --token-file: this form is visible in the process table and in shell history; default $OLIVARES_TOKEN, then current context) |
| `--token-file` | `string` | — | **inherited**. read the API bearer token from a file, or - for stdin |

#### Command: olivares observability attestation

Show the measured attestation of the running binary

```
olivares observability attestation
```

Declares no flags of its own; it takes those of [`olivares observability`](#command-olivares-observability) and the root command.

#### Command: olivares observability ingestion-health

Report per-standard and per-source telemetry ingestion

```
olivares observability ingestion-health
```

Aliases: `ingestion`

Declares no flags of its own; it takes those of [`olivares observability`](#command-olivares-observability) and the root command.

#### Command: olivares observability traces

List, open and export ledger-derived traces

```
olivares observability traces
```

Declares no flags of its own; it takes those of [`olivares observability`](#command-olivares-observability) and the root command.

#### Command: olivares observability traces export

Export one trace as OTLP-compatible JSON

```
olivares observability traces export <trace-id>
```

Declares no flags of its own; it takes those of [`olivares observability traces`](#command-olivares-observability-traces) and the root command.

#### Command: olivares observability traces get

Show one trace's spans

```
olivares observability traces get <trace-id>
```

Declares no flags of its own; it takes those of [`olivares observability traces`](#command-olivares-observability-traces) and the root command.

#### Command: olivares observability traces ls

List correlated traces

```
olivares observability traces ls
```

Aliases: `list`

| Flag | Type | Default | Description |
|---|---|---|---|
| `--cursor` | `string` | — | continue from the cursor printed by the previous page |
| `--limit` | `int` | `0` | maximum rows to return in one page (0 = the engine's default) |

#### Command: olivares openapi

Print an OpenAPI 3.1 document (stable core, or --beta module routes) for client codegen

```
olivares openapi
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--beta` | `bool` | `false` | print the BETA module-route document (/v1/m/&lt;ns&gt;/…) instead of the stable core contract |

#### Command: olivares orchestration

Inspect the agent communication graph and operate governed schedules and workflows

```
olivares orchestration
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--allow-cleartext` | `bool` | `false` | **inherited**. allow sending the credential to a non-loopback host over plain HTTP (DANGEROUS: it travels readable) |
| `--ca-cert` | `string` | — | **inherited**. PEM file containing an additional trusted root CA (default: current context) |
| `--insecure` | `bool` | `false` | **inherited**. skip TLS certificate verification (DANGEROUS; development only) |
| `--pin-sha256` | `stringArray` | `[]` | **inherited**. trusted leaf SPKI SHA-256 pin, base64 or hex, repeatable — the engine prints it as pin_sha256 on the line reporting its certificate (default: current context) |
| `--server` | `string` | — | **inherited**. control-plane base URL (default $OLIVARES_SERVER_URL, then current context) |
| `--tenant` | `string` | — | **inherited**. tenant id (default $OLIVARES_TENANT, then current context) |
| `--timeout` | `duration` | `10s` | **inherited**. request timeout |
| `--token` | `string` | — | **inherited**. API bearer token (prefer --token-file: this form is visible in the process table and in shell history; default $OLIVARES_TOKEN, then current context) |
| `--token-file` | `string` | — | **inherited**. read the API bearer token from a file, or - for stdin |

#### Command: olivares orchestration decisions

List the append-only fire/miss decision ledger for the tenant

```
olivares orchestration decisions
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--cursor` | `string` | — | continue from the cursor a previous page reported |
| `--limit` | `int` | `0` | page size (0 uses the engine's default) |

#### Command: olivares orchestration flows

List the derived multi-agent flows and their lifecycle state

```
olivares orchestration flows
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--state` | `string` | — | only flows in this lifecycle state |

#### Command: olivares orchestration graph

List the live agent→agent relations (a privileged, self-audited read)

```
olivares orchestration graph
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--cursor` | `string` | — | continue from the cursor a previous page reported |
| `--limit` | `int` | `0` | page size (0 uses the engine's default) |
| `--link-kind` | `string` | — | only edges of this link kind |
| `--supervisor` | `string` | — | only edges whose supervisor is this agent ref |
| `--worker` | `string` | — | only edges whose worker is this agent ref |

#### Command: olivares orchestration neighbors

Show the subgraph around one agent (incoming, outgoing or both)

```
olivares orchestration neighbors <node>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--direction` | `string` | `both` | incoming, outgoing or both |

#### Command: olivares orchestration schedules

Declare, retarget and fire governed schedules

```
olivares orchestration schedules
```

Declares no flags of its own; it takes those of [`olivares orchestration`](#command-olivares-orchestration) and the root command.

#### Command: olivares orchestration schedules create

Declare a governed schedule

```
olivares orchestration schedules create
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--approval-ref` | `string` | — | phase 2: the approval that authorizes this declaration |
| `--cadence-spec` | `string` | — | the trigger's cadence, e.g. a cron expression |
| `--expected-interval-seconds` | `int64` | `0` | arm the cadence-miss check (0 disables it; cron triggers only) |
| `--grace-factor` | `int64` | `0` | multiple of the interval tolerated before a miss (engine default when 0) |
| `--name` | `string` | — | **required**. human name for the routine (required) |
| `--subject-kind` | `string` | `agent` | what the schedule drives |
| `--subject-ref` | `string` | — | **required**. the subject's reference (required) |
| `--trigger-kind` | `string` | `cron` | how the routine is triggered |

#### Command: olivares orchestration schedules decisions

List one schedule's append-only fire/miss ledger

```
olivares orchestration schedules decisions <id>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--cursor` | `string` | — | continue from the cursor a previous page reported |
| `--limit` | `int` | `0` | page size (0 uses the engine's default) |

#### Command: olivares orchestration schedules fire

Fire a schedule now, through the approval gate (two-phase)

```
olivares orchestration schedules fire <id>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--approval-ref` | `string` | — | phase 2: the approval that authorizes this fire |

#### Command: olivares orchestration schedules get

Show one schedule

```
olivares orchestration schedules get <id>
```

Declares no flags of its own; it takes those of [`olivares orchestration schedules`](#command-olivares-orchestration-schedules) and the root command.

#### Command: olivares orchestration schedules ls

List the tenant's governed schedules with their derived health

```
olivares orchestration schedules ls
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--cursor` | `string` | — | continue from the cursor a previous page reported |
| `--limit` | `int` | `0` | page size (0 uses the engine's default) |

#### Command: olivares orchestration schedules restore

Re-apply an earlier revision of a schedule

```
olivares orchestration schedules restore <id>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--approval-ref` | `string` | — | phase 2: the approval that authorizes the restore |
| `--revision` | `string` | — | **required**. the revision id to re-apply (required) |

#### Command: olivares orchestration schedules revisions

List a schedule's revision history

```
olivares orchestration schedules revisions <id>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--cursor` | `string` | — | continue from the cursor a previous page reported |
| `--limit` | `int` | `0` | page size (0 uses the engine's default) |

#### Command: olivares orchestration schedules update

Partially update a schedule — only the flags you type are sent

```
olivares orchestration schedules update <id>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--approval-ref` | `string` | — | phase 2: the approval that authorizes this change |
| `--cadence-spec` | `string` | — | replace the cadence expression |
| `--desired-status` | `string` | — | active, paused or retired |
| `--expected-interval-seconds` | `int64` | `0` | replace the cadence-miss window (0 disables the check) |
| `--grace-factor` | `int64` | `0` | replace the grace factor |
| `--subject-ref` | `string` | — | retarget the routine at another subject |

#### Command: olivares orchestration stream

Follow the live communication graph as NDJSON (one object per event)

```
olivares orchestration stream
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--node` | `string` | — | only events touching this agent ref |

#### Command: olivares orchestration timeline

Show one subject's merged delegation and fire/miss history

```
olivares orchestration timeline <subject>
```

Declares no flags of its own; it takes those of [`olivares orchestration`](#command-olivares-orchestration) and the root command.

#### Command: olivares orchestration workflows

Author, dry-run and execute DAG workflows

```
olivares orchestration workflows
```

Declares no flags of its own; it takes those of [`olivares orchestration`](#command-olivares-orchestration) and the root command.

#### Command: olivares orchestration workflows create

Declare a workflow from a JSON step graph

```
olivares orchestration workflows create
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--description` | `string` | — | what this workflow is for |
| `--enabled` | `bool` | `true` | declare the workflow enabled |
| `--name` | `string` | — | **required**. human name for the workflow (required) |
| `--steps-file` | `string` | — | **required**. JSON array of step objects, '-' for stdin (required) |

#### Command: olivares orchestration workflows dry-run

Resolve and validate a workflow without executing a single step

```
olivares orchestration workflows dry-run <id>
```

Declares no flags of its own; it takes those of [`olivares orchestration workflows`](#command-olivares-orchestration-workflows) and the root command.

#### Command: olivares orchestration workflows get

Show one workflow with its full step graph

```
olivares orchestration workflows get <id>
```

Declares no flags of its own; it takes those of [`olivares orchestration workflows`](#command-olivares-orchestration-workflows) and the root command.

#### Command: olivares orchestration workflows ls

List the tenant's workflows

```
olivares orchestration workflows ls
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--cursor` | `string` | — | continue from the cursor a previous page reported |
| `--limit` | `int` | `0` | page size (0 uses the engine's default) |

#### Command: olivares orchestration workflows restore

Re-apply an earlier revision of a workflow

```
olivares orchestration workflows restore <id>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--revision` | `string` | — | **required**. the revision id to re-apply (required) |

#### Command: olivares orchestration workflows revisions

List a workflow's revision history

```
olivares orchestration workflows revisions <id>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--cursor` | `string` | — | continue from the cursor a previous page reported |
| `--limit` | `int` | `0` | page size (0 uses the engine's default) |

#### Command: olivares orchestration workflows run

Execute a workflow through the approval gate (two-phase)

```
olivares orchestration workflows run <id>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--approval-ref` | `string` | — | phase 2: the approval that authorizes this run |

#### Command: olivares orchestration workflows runs

Inspect a workflow's runs

```
olivares orchestration workflows runs
```

Declares no flags of its own; it takes those of [`olivares orchestration workflows`](#command-olivares-orchestration-workflows) and the root command.

#### Command: olivares orchestration workflows runs get

Show one run's step timeline

```
olivares orchestration workflows runs get <workflow-id> <run-id>
```

Declares no flags of its own; it takes those of [`olivares orchestration workflows runs`](#command-olivares-orchestration-workflows-runs) and the root command.

#### Command: olivares orchestration workflows runs ls

List one workflow's runs, newest first

```
olivares orchestration workflows runs ls <workflow-id>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--cursor` | `string` | — | continue from the cursor a previous page reported |
| `--limit` | `int` | `0` | page size (0 uses the engine's default) |

#### Command: olivares orchestration workflows set-steps

Replace a workflow's whole step graph (PUT — one unit, one hash)

```
olivares orchestration workflows set-steps <id>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--steps-file` | `string` | — | **required**. JSON array of step objects, '-' for stdin (required) |

#### Command: olivares orchestration workflows update

Partially update a workflow's metadata — only the flags you type are sent

```
olivares orchestration workflows update <id>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--description` | `string` | — | replace the description |
| `--enabled` | `bool` | `true` | enable or disable the workflow |
| `--name` | `string` | — | rename the workflow |

#### Command: olivares posture

Export the tenant's governance posture as one document

```
olivares posture
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--allow-cleartext` | `bool` | `false` | **inherited**. allow sending the credential to a non-loopback host over plain HTTP (DANGEROUS: it travels readable) |
| `--ca-cert` | `string` | — | **inherited**. PEM file containing an additional trusted root CA (default: current context) |
| `--insecure` | `bool` | `false` | **inherited**. skip TLS certificate verification (DANGEROUS; development only) |
| `--pin-sha256` | `stringArray` | `[]` | **inherited**. trusted leaf SPKI SHA-256 pin, base64 or hex, repeatable — the engine prints it as pin_sha256 on the line reporting its certificate (default: current context) |
| `--server` | `string` | — | **inherited**. control-plane base URL (default $OLIVARES_SERVER_URL, then current context) |
| `--tenant` | `string` | — | **inherited**. tenant id (default $OLIVARES_TENANT, then current context) |
| `--timeout` | `duration` | `10s` | **inherited**. request timeout |
| `--token` | `string` | — | **inherited**. API bearer token (prefer --token-file: this form is visible in the process table and in shell history; default $OLIVARES_TOKEN, then current context) |
| `--token-file` | `string` | — | **inherited**. read the API bearer token from a file, or - for stdin |

#### Command: olivares posture export

Export inventory, drift and findings as one posture document

```
olivares posture export
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--category` | `string` | — | match a finding kind or subject kind |
| `--kind` | `string` | — | narrow the inventory half to one entity kind |
| `--out` | `string` | — | write the document verbatim here; `-` means stdout (default: render a summary) |
| `--severity` | `string` | — | minimum finding severity: low, medium, high or critical |
| `--strict` | `bool` | `true` | exit 7 (degraded) when the engine truncated any half of the export; --strict=false exits 0 instead |

#### Command: olivares quickstart

Start Olivares AI for the first time — secure by default, one command to the console

```
olivares quickstart
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--data-dir` | `string` | — | data directory (default $OLIVARES_DATA_DIR, an existing ./olivares-data, else $XDG_DATA_HOME/olivares or ~/.local/share/olivares) |
| `--grpc-listen` | `string` | `127.0.0.1:8444` | gRPC listen address |
| `--listen` | `string` | `127.0.0.1:8443` | HTTP (REST + web console) listen address |
| `--quiet` | `bool` | `false` | print only the guided panel, holding the engine's startup checks back to errors (they are still evaluated, and `olivares status` reports the same posture) |

#### Command: olivares quickstart governed-rag

Prepare live governed data for Claude Code (S3/Drive -&gt; semantic KB -&gt; MCP retrieval)

```
olivares quickstart governed-rag
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--agent-gateway-listen` | `string` | `127.0.0.1:8446` | MCP gateway listen address |
| `--agent-name` | `string` | `Claude Code governed RAG` | human label for the agent created by the bootstrap script |
| `--agent-ref` | `string` | `claude-code-governed` | Claude Code agent external_id / MCP token subject |
| `--bucket` | `string` | — | S3 bucket for --source s3 |
| `--clearance` | `string` | `confidential` | expected roster clearance on the identity (documented and checked by the guard) |
| `--credential-ref` | `string` | — | secret-store reference for the source credential, e.g. store:s3/prod-read |
| `--data-dir` | `string` | — | data directory (default $OLIVARES_DATA_DIR, an existing ./olivares-data, else $XDG_DATA_HOME/olivares or ~/.local/share/olivares) |
| `--drive-api-base` | `string` | — | Google Drive API base override |
| `--drive-id` | `string` | — | shared Drive ID for --source gdrive (optional) |
| `--endpoint` | `string` | — | optional S3-compatible endpoint (R2/MinIO/GCS interop) |
| `--group-ref` | `string` | `group:engineering` | expected roster group/ACL ref on the identity |
| `--grpc-listen` | `string` | `127.0.0.1:8444` | gRPC listen address when --start is used |
| `--identity-ref` | `string` | `agent:claude-code-governed` | NHI identity external_id to bind to the agent |
| `--kb-name` | `string` | `governed-data` | knowledge base name to create in the bootstrap script |
| `--listen` | `string` | `127.0.0.1:8443` | HTTP (REST + web console) listen address when --start is used |
| `--mcp-authorization-server` | `string` | — | authorization server metadata URL (default --mcp-issuer) |
| `--mcp-issuer` | `string` | — | trusted OAuth issuer for MCP access tokens |
| `--mcp-jwks-file` | `string` | — | inline JWKS JSON file for the MCP issuer |
| `--mcp-jwks-url` | `string` | — | JWKS URL for the MCP issuer |
| `--mcp-resource` | `string` | — | MCP protected resource URI (default http://&lt;agent-gateway-listen&gt;/mcp) |
| `--out-dir` | `string` | — | directory for generated governed-RAG config (default &lt;data-dir&gt;/quickstart/governed-rag) |
| `--path-style` | `bool` | `false` | force S3 path-style bucket addressing |
| `--prefix` | `string` | — | S3 key prefix for --source s3 |
| `--region` | `string` | `us-east-1` | S3 signing region |
| `--source` | `string` | `s3` | content source kind: s3 or gdrive |
| `--source-name` | `string` | `governed-rag-live` | registered knowledge content-source name |
| `--start` | `bool` | `false` | start the engine after writing config |
| `--tenant-id` | `string` | — | tenant id for the MCP retrieval surface and bootstrap script |

#### Command: olivares recording

Read the session-recording trail, verify its chain and set the recording policy

```
olivares recording
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--allow-cleartext` | `bool` | `false` | **inherited**. allow sending the credential to a non-loopback host over plain HTTP (DANGEROUS: it travels readable) |
| `--ca-cert` | `string` | — | **inherited**. PEM file containing an additional trusted root CA (default: current context) |
| `--insecure` | `bool` | `false` | **inherited**. skip TLS certificate verification (DANGEROUS; development only) |
| `--pin-sha256` | `stringArray` | `[]` | **inherited**. trusted leaf SPKI SHA-256 pin, base64 or hex, repeatable — the engine prints it as pin_sha256 on the line reporting its certificate (default: current context) |
| `--server` | `string` | — | **inherited**. control-plane base URL (default $OLIVARES_SERVER_URL, then current context) |
| `--tenant` | `string` | — | **inherited**. tenant id (default $OLIVARES_TENANT, then current context) |
| `--timeout` | `duration` | `10s` | **inherited**. request timeout |
| `--token` | `string` | — | **inherited**. API bearer token (prefer --token-file: this form is visible in the process table and in shell history; default $OLIVARES_TOKEN, then current context) |
| `--token-file` | `string` | — | **inherited**. read the API bearer token from a file, or - for stdin |

#### Command: olivares recording ack

Acknowledge the recording notice for this caller

```
olivares recording ack
```

Declares no flags of its own; it takes those of [`olivares recording`](#command-olivares-recording) and the root command.

#### Command: olivares recording config

Read and replace the tenant's recording policy

```
olivares recording config
```

Declares no flags of its own; it takes those of [`olivares recording`](#command-olivares-recording) and the root command.

#### Command: olivares recording config get

Show the tenant's recording policy

```
olivares recording config get
```

Declares no flags of its own; it takes those of [`olivares recording config`](#command-olivares-recording-config) and the root command.

#### Command: olivares recording config set

Replace the tenant's recording policy (PUT — the whole policy)

```
olivares recording config set
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--ai-summaries` | `bool` | `false` | permit AI summaries: the transcript LEAVES the trust boundary (off unless passed) |
| `--consent` | `string` | `notice` | notice or required |
| `--idle-seconds` | `int64` | `900` | seconds of inactivity before a sweep may seal a session |
| `--namespace` | `stringArray` | `[]` | a namespace to record, repeatable (required) |
| `--retention-days` | `int64` | `90` | days a sealed trail is retained |

#### Command: olivares recording notice

Show what is recorded for this caller, and whether consent is required

```
olivares recording notice
```

Declares no flags of its own; it takes those of [`olivares recording`](#command-olivares-recording) and the root command.

#### Command: olivares recording sessions

List, verify, export and seal recorded sessions

```
olivares recording sessions
```

Declares no flags of its own; it takes those of [`olivares recording`](#command-olivares-recording) and the root command.

#### Command: olivares recording sessions export

Export one session as evidence (json or summary)

```
olivares recording sessions export <id>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--format` | `string` | `json` | export format: json (full trail) or summary — NOT an alias of -o/--output |

#### Command: olivares recording sessions get

Show one recorded session

```
olivares recording sessions get <id>
```

Declares no flags of its own; it takes those of [`olivares recording sessions`](#command-olivares-recording-sessions) and the root command.

#### Command: olivares recording sessions ls

List recorded sessions

```
olivares recording sessions ls
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--cursor` | `string` | — | continue from the cursor a previous page reported |
| `--grant` | `string` | — | only sessions opened under this break-glass grant |
| `--limit` | `int` | `0` | page size (0 uses the engine's default) |
| `--opened-after` | `string` | — | only sessions opened at or after this RFC3339 instant |
| `--opened-before` | `string` | — | only sessions opened before this RFC3339 instant |
| `--seal-reason` | `string` | — | only sessions sealed for this reason |
| `--status` | `string` | — | only sessions in this status |
| `--subject-contains` | `string` | — | only sessions whose subject contains this substring |
| `--subject-user` | `string` | — | only sessions of this user |

#### Command: olivares recording sessions replay

Reconstruct one session's frames and ledger window

```
olivares recording sessions replay <id>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--cursor` | `string` | — | continue from the cursor a previous page reported |
| `--limit` | `int` | `0` | page size (0 uses the engine's default) |

#### Command: olivares recording sessions seal

Close one active session explicitly

```
olivares recording sessions seal <id>
```

Declares no flags of its own; it takes those of [`olivares recording sessions`](#command-olivares-recording-sessions) and the root command.

#### Command: olivares recording sessions summarize

Produce the derived reviewer summary of a sealed session

```
olivares recording sessions summarize <id>
```

Declares no flags of its own; it takes those of [`olivares recording sessions`](#command-olivares-recording-sessions) and the root command.

#### Command: olivares recording sessions unified

Show one session's frames and audit timeline merged

```
olivares recording sessions unified <id>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--cursor` | `string` | — | continue from the cursor a previous page reported |
| `--frame-cursor` | `string` | — | page the frames independently of the timeline |
| `--limit` | `int` | `0` | page size (0 uses the engine's default) |

#### Command: olivares recording sessions verify

Verify a session's hash chain — exit 7 when it does not verify

```
olivares recording sessions verify <id>
```

Declares no flags of its own; it takes those of [`olivares recording sessions`](#command-olivares-recording-sessions) and the root command.

#### Command: olivares recording sweep

Seal every idle active session (the lazy-seal safety net)

```
olivares recording sweep
```

Declares no flags of its own; it takes those of [`olivares recording`](#command-olivares-recording) and the root command.

#### Command: olivares redteam

Run the consent-gated adversarial battery against your own agents

```
olivares redteam
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--allow-cleartext` | `bool` | `false` | **inherited**. allow sending the credential to a non-loopback host over plain HTTP (DANGEROUS: it travels readable) |
| `--ca-cert` | `string` | — | **inherited**. PEM file containing an additional trusted root CA (default: current context) |
| `--insecure` | `bool` | `false` | **inherited**. skip TLS certificate verification (DANGEROUS; development only) |
| `--pin-sha256` | `stringArray` | `[]` | **inherited**. trusted leaf SPKI SHA-256 pin, base64 or hex, repeatable — the engine prints it as pin_sha256 on the line reporting its certificate (default: current context) |
| `--server` | `string` | — | **inherited**. control-plane base URL (default $OLIVARES_SERVER_URL, then current context) |
| `--tenant` | `string` | — | **inherited**. tenant id (default $OLIVARES_TENANT, then current context) |
| `--timeout` | `duration` | `10s` | **inherited**. request timeout |
| `--token` | `string` | — | **inherited**. API bearer token (prefer --token-file: this form is visible in the process table and in shell history; default $OLIVARES_TOKEN, then current context) |
| `--token-file` | `string` | — | **inherited**. read the API bearer token from a file, or - for stdin |

#### Command: olivares redteam catalog

List the probe battery and its OWASP/ATLAS coverage

```
olivares redteam catalog
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--suite` | `string` | — | only probes of this suite |

#### Command: olivares redteam runs

Launch and inspect scored red-team runs

```
olivares redteam runs
```

Declares no flags of its own; it takes those of [`olivares redteam`](#command-olivares-redteam) and the root command.

#### Command: olivares redteam runs get

Show one run's scorecard

```
olivares redteam runs get <id>
```

Declares no flags of its own; it takes those of [`olivares redteam runs`](#command-olivares-redteam-runs) and the root command.

#### Command: olivares redteam runs launch

Run the battery against an authorized target

```
olivares redteam runs launch
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--suite` | `string` | — | run only this suite of the battery |
| `--target-ref` | `string` | — | **required**. the authorized target to probe (required) |

#### Command: olivares redteam runs ls

List red-team runs and their scores

```
olivares redteam runs ls
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--cursor` | `string` | — | continue from the cursor a previous page reported |
| `--limit` | `int` | `0` | page size (0 uses the engine's default) |
| `--suite` | `string` | — | only runs of this suite |
| `--target-ref` | `string` | — | only runs against this target |

#### Command: olivares redteam runs results

List one run's per-probe results

```
olivares redteam runs results <id>
```

Declares no flags of its own; it takes those of [`olivares redteam runs`](#command-olivares-redteam-runs) and the root command.

#### Command: olivares redteam targets

Register agents as red-team targets and grant or withdraw consent

```
olivares redteam targets
```

Declares no flags of its own; it takes those of [`olivares redteam`](#command-olivares-redteam) and the root command.

#### Command: olivares redteam targets authorize

Consent to red-teaming this target (confirmed; needs --yes when unattended)

```
olivares redteam targets authorize <id>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--scope` | `string` | — | limit the consent to this scope |
| `-y`, `--yes` | `bool` | `false` | proceed without the confirmation prompt (required in a non-interactive session) |

#### Command: olivares redteam targets get

Show one target and its consent record

```
olivares redteam targets get <id>
```

Declares no flags of its own; it takes those of [`olivares redteam targets`](#command-olivares-redteam-targets) and the root command.

#### Command: olivares redteam targets ls

List registered red-team targets and their consent state

```
olivares redteam targets ls
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--cursor` | `string` | — | continue from the cursor a previous page reported |
| `--limit` | `int` | `0` | page size (0 uses the engine's default) |
| `--status` | `string` | — | only targets in this status |

#### Command: olivares redteam targets register

Register an agent from your inventory as a red-team target

```
olivares redteam targets register
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--agent-ref` | `string` | — | **required**. an agent in this tenant's inventory (required) |
| `--endpoint` | `string` | — | where the target is reachable |
| `--name` | `string` | — | **required**. human name for the target (required) |
| `--scope` | `string` | — | the scope consent will be limited to |

#### Command: olivares redteam targets revoke

Withdraw consent to red-team this target

```
olivares redteam targets revoke <id>
```

Declares no flags of its own; it takes those of [`olivares redteam targets`](#command-olivares-redteam-targets) and the root command.

#### Command: olivares release

Hidden diagnostic: it does not appear in `--help` output and is not part of the supported surface.

Release/OTA tooling (manifest generation) — ops use

```
olivares release
```

Declares no flags of its own; it takes those of [`olivares`](#command-olivares) and the root command.

#### Command: olivares release export-mirror

Mirror the entitled manifest and artifacts from the licensed gate into an air-gap bundle

```
olivares release export-mirror
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--channel` | `string` | `stable` | release channel: stable \| security |
| `--endpoint` | `string` | — | licensed worker base URL (required) |
| `--force` | `bool` | `false` | replace a non-empty --out (it is refused otherwise) |
| `--out` | `string` | — | output directory, or a path ending in .tar.gz (required) |
| `--platform` | `stringSlice` | `[]` | os/arch to mirror; repeatable (default: every platform the manifest names) |
| `--pubkey` | `string` | — | base64 Ed25519 OTA public key (default: the key embedded in this binary) |
| `--set` | `string` | — | entitled set slug, e.g. biz+reg (required: the gate never defaults it) |
| `--timeout` | `duration` | `10m0s` | HTTP timeout for each gate request |
| `--token` | `string` | — | licence download token (required) |

#### Command: olivares release manifest

Build (and optionally sign) a per-channel OTA update manifest from a release directory

```
olivares release manifest
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--advisory` | `stringArray` | `[]` | advisory/CVE id fixed by this release (repeatable) |
| `--channel` | `string` | `stable` | channel: stable \| security (lts is accepted by the validator, but no lts line is produced) |
| `--dir` | `string` | `.` | directory holding the release archives |
| `--eol-at` | `string` | — | channel/line end-of-life date (RFC3339): recorded and printed, never enforced — a past date only warns, it never refuses (core/release/manifest.go:638-640) |
| `--expires-in` | `string` | `2160h` | freshness window as a duration (e.g. 168h): clients REFUSE the manifest after released_at+this (anti-freeze; re-sign periodically) |
| `--license-key-epoch` | `string` | — | key-compromise fence (RFC3339, the PAST compromise time): licenses issued before it are invalid; set only during an O03 rotation |
| `--min-version` | `string` | — | minimum current version allowed to jump directly to this release |
| `--no-expiry` | `bool` | `false` | UNSAFE: emit a manifest with NO freshness bound — a mirror can then serve it forever. Only for a throwaway/test manifest |
| `--notes` | `string` | — | short human note or URL |
| `--out` | `string` | `manifest.json` | output manifest path (a .sig is written beside it when --sign-key is set) |
| `--revoke-holder` | `stringArray` | `[]` | holder_id whose EVERY license is revoked via this channel's CRL (repeatable) |
| `--revoke-serial` | `stringArray` | `[]` | license serial to revoke via this channel's CRL (repeatable) |
| `--rollout` | `int` | `-1` | staged rollout percentage 0..100 (-1 = full rollout / omit) |
| `--security` | `bool` | `false` | mark this as a security release |
| `--sign-key` | `string` | — | base64 (or @file) Ed25519 PRIVATE key to sign the manifest |
| `--start-at` | `string` | — | rollout start time (RFC3339); before it no node upgrades |
| `--version` | `string` | — | release version (semver), e.g. 26.8.0 (required) |

#### Command: olivares release sign-manifest

Sign an existing OTA manifest during the off-box release ceremony

```
olivares release sign-manifest
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--checksums` | `string` | — | the cosign-verified checksums.txt the manifest must agree with (REQUIRED: signing binds these digests) |
| `--manifest` | `string` | — | existing manifest JSON to sign (required) |
| `--out` | `string` | — | detached signature output (default &lt;manifest&gt;.sig) |
| `--sign-key` | `string` | — | base64 (or @file) dedicated OTA Ed25519 PRIVATE key |
| `--unsafe-no-crosscheck` | `bool` | `false` | UNSAFE: sign without binding the manifest to checksums.txt or reviewing its policy |

#### Command: olivares release verify-channel-advance

Refuse a channel publication that would not move the LIVE channel forward (CFG-06 monotonicity fence)

```
olivares release verify-channel-advance
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--candidate` | `string` | — | the manifest JSON about to be published (required) |
| `--channel` | `string` | `stable` | channel to compare (stable \| security \| lts) |
| `--endpoint` | `string` | `https://github.com/olivaresai/olivares` | the channel to read: a GitHub repository, one of its releases, or a static mirror base |
| `--pubkey` | `string` | — | base64 or @file Ed25519 OTA key; when set the LIVE manifest's signature is verified before its version is believed |
| `--timeout` | `duration` | `1m0s` | network timeout for reading the live channel |

#### Command: olivares release verify-manifest

Cross-check an OTA manifest against the cosign-verified checksums.txt (and, with --dir, the published bytes)

```
olivares release verify-manifest
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--allow-no-expiry` | `bool` | `false` | UNSAFE: accept a manifest with no freshness bound (anti-freeze disabled) |
| `--allow-paused-rollout` | `bool` | `false` | accept a SECURITY manifest whose rollout is paused (percentage 0 or a future start_at) — only when the pause is deliberate |
| `--checksums` | `string` | — | the release's checksums.txt, ALREADY verified with cosign (required) |
| `--dir` | `string` | — | directory holding the published archives; every manifest artifact must be present and re-hash to its digest |
| `--expect-channel` | `string` | — | fail unless the manifest declares this channel |
| `--expect-version` | `string` | — | fail unless the manifest declares this version (a leading v is ignored) |
| `--manifest` | `string` | — | manifest JSON to cross-check (required) |
| `--max-expires-in` | `string` | `4320h0m0s` | upper bound on the freshness window (expires-released_at and expires-now): beyond it the anti-freeze defense is effectively off |
| `--pubkey` | `string` | — | base64 or @file Ed25519 OTA key for --sig (default: the key embedded in this build) |
| `--require-expiry` | `bool` | `true` | _hidden_, _deprecated: a freshness bound is required by default; use --allow-no-expiry to opt OUT_. DEPRECATED (now the default): a freshness bound is required unless --allow-no-expiry |
| `--sig` | `string` | — | detached manifest signature; when set the signature is verified BEFORE the cross-check |

#### Command: olivares reporting

Generate reports and manage schedules, branding and templates

```
olivares reporting
```

Aliases: `reports`

| Flag | Type | Default | Description |
|---|---|---|---|
| `--allow-cleartext` | `bool` | `false` | **inherited**. allow sending the credential to a non-loopback host over plain HTTP (DANGEROUS: it travels readable) |
| `--ca-cert` | `string` | — | **inherited**. PEM file containing an additional trusted root CA (default: current context) |
| `--insecure` | `bool` | `false` | **inherited**. skip TLS certificate verification (DANGEROUS; development only) |
| `--pin-sha256` | `stringArray` | `[]` | **inherited**. trusted leaf SPKI SHA-256 pin, base64 or hex, repeatable — the engine prints it as pin_sha256 on the line reporting its certificate (default: current context) |
| `--server` | `string` | — | **inherited**. control-plane base URL (default $OLIVARES_SERVER_URL, then current context) |
| `--tenant` | `string` | — | **inherited**. tenant id (default $OLIVARES_TENANT, then current context) |
| `--timeout` | `duration` | `10s` | **inherited**. request timeout |
| `--token` | `string` | — | **inherited**. API bearer token (prefer --token-file: this form is visible in the process table and in shell history; default $OLIVARES_TOKEN, then current context) |
| `--token-file` | `string` | — | **inherited**. read the API bearer token from a file, or - for stdin |

#### Command: olivares reporting branding

Read and set the tenant's report branding

```
olivares reporting branding
```

Declares no flags of its own; it takes those of [`olivares reporting`](#command-olivares-reporting) and the root command.

#### Command: olivares reporting branding get

Show the tenant's report branding

```
olivares reporting branding get
```

Declares no flags of its own; it takes those of [`olivares reporting branding`](#command-olivares-reporting-branding) and the root command.

#### Command: olivares reporting branding set

Replace the tenant's report branding

```
olivares reporting branding set
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--company-name` | `string` | — | company name shown on reports |
| `--footer-text` | `string` | — | footer text for every page |
| `--logo-path` | `string` | — | path to the logo the renderer should use |
| `--primary-color` | `string` | — | primary brand color |
| `--secondary-color` | `string` | — | secondary brand color |

#### Command: olivares reporting enterprise

Read the enterprise posture, risk and evidence-bundle reports

```
olivares reporting enterprise
```

Declares no flags of its own; it takes those of [`olivares reporting`](#command-olivares-reporting) and the root command.

#### Command: olivares reporting enterprise bundle

Enterprise evidence bundle

```
olivares reporting enterprise bundle
```

Declares no flags of its own; it takes those of [`olivares reporting enterprise`](#command-olivares-reporting-enterprise) and the root command.

#### Command: olivares reporting enterprise posture

Enterprise governance posture report

```
olivares reporting enterprise posture
```

Declares no flags of its own; it takes those of [`olivares reporting enterprise`](#command-olivares-reporting-enterprise) and the root command.

#### Command: olivares reporting enterprise risk

Enterprise risk report

```
olivares reporting enterprise risk
```

Declares no flags of its own; it takes those of [`olivares reporting enterprise`](#command-olivares-reporting-enterprise) and the root command.

#### Command: olivares reporting reports

List the report catalog and generate a report

```
olivares reporting reports
```

Declares no flags of its own; it takes those of [`olivares reporting`](#command-olivares-reporting) and the root command.

#### Command: olivares reporting reports get

Generate one report and write it to a file

```
olivares reporting reports get <report-type>
```

Aliases: `generate`

| Flag | Type | Default | Description |
|---|---|---|---|
| `--format` | `string` | — | html (default) or pdf |
| `--framework` | `string` | — | compliance-evidence only: filter by framework |
| `--from` | `string` | — | window start: RFC3339 or YYYY-MM-DD |
| `--locale` | `string` | — | i18n locale for the rendered report (default en) |
| `--out` | `string` | — | write the artifact here; `-` means stdout (required: these routes answer with a rendered document, not JSON) |
| `--team` | `string` | — | finops-report only: filter by team |
| `--to` | `string` | — | window end: RFC3339 or YYYY-MM-DD |

#### Command: olivares reporting reports ls

List the reports this build can generate

```
olivares reporting reports ls
```

Aliases: `list`

Declares no flags of its own; it takes those of [`olivares reporting reports`](#command-olivares-reporting-reports) and the root command.

#### Command: olivares reporting schedules

Manage scheduled reports and read their runs

```
olivares reporting schedules
```

Aliases: `schedule`

Declares no flags of its own; it takes those of [`olivares reporting`](#command-olivares-reporting) and the root command.

#### Command: olivares reporting schedules create

Schedule a report on a cron cadence

```
olivares reporting schedules create
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--cron` | `string` | — | five-field cron spec, e.g. "0 6 * * *" (required) |
| `--enabled` | `bool` | `true` | whether the schedule may fire |
| `--format` | `string` | — | html (default) or pdf |
| `--framework` | `string` | — | compliance-evidence only: filter by framework |
| `--locale` | `string` | — | i18n locale for the rendered report |
| `--report-type` | `string` | — | the report to generate (required) |
| `--team` | `string` | — | finops-report only: filter by team |

#### Command: olivares reporting schedules ls

List report schedules

```
olivares reporting schedules ls
```

Aliases: `list`

Declares no flags of its own; it takes those of [`olivares reporting schedules`](#command-olivares-reporting-schedules) and the root command.

#### Command: olivares reporting schedules rm

Delete a report schedule

```
olivares reporting schedules rm <schedule-id>
```

Aliases: `delete`, `remove`

| Flag | Type | Default | Description |
|---|---|---|---|
| `-y`, `--yes` | `bool` | `false` | proceed without the confirmation prompt (required in a non-interactive session) |

#### Command: olivares reporting schedules run

Fetch one run's stored report artifact

```
olivares reporting schedules run <schedule-id> <run-id>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--out` | `string` | — | write the artifact here; `-` means stdout (required: these routes answer with a rendered document, not JSON) |

#### Command: olivares reporting schedules runs

List a schedule's executions

```
olivares reporting schedules runs <schedule-id>
```

Declares no flags of its own; it takes those of [`olivares reporting schedules`](#command-olivares-reporting-schedules) and the root command.

#### Command: olivares reporting templates

Read, store and remove custom report templates

```
olivares reporting templates
```

Aliases: `template`

Declares no flags of its own; it takes those of [`olivares reporting`](#command-olivares-reporting) and the root command.

#### Command: olivares reporting templates get

Fetch the custom template stored for one report type

```
olivares reporting templates get <report-type>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--out` | `string` | — | write the artifact here; `-` means stdout (required: these routes answer with a rendered document, not JSON) |

#### Command: olivares reporting templates rm

Remove the custom template for one report type

```
olivares reporting templates rm <report-type>
```

Aliases: `delete`, `remove`

| Flag | Type | Default | Description |
|---|---|---|---|
| `-y`, `--yes` | `bool` | `false` | proceed without the confirmation prompt (required in a non-interactive session) |

#### Command: olivares reporting templates set

Store a custom HTML template for one report type

```
olivares reporting templates set <report-type> <template-file>
```

Declares no flags of its own; it takes those of [`olivares reporting templates`](#command-olivares-reporting-templates) and the root command.

#### Command: olivares sandbox

Run agents against synthetic scenarios and compare two variants

```
olivares sandbox
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--allow-cleartext` | `bool` | `false` | **inherited**. allow sending the credential to a non-loopback host over plain HTTP (DANGEROUS: it travels readable) |
| `--ca-cert` | `string` | — | **inherited**. PEM file containing an additional trusted root CA (default: current context) |
| `--insecure` | `bool` | `false` | **inherited**. skip TLS certificate verification (DANGEROUS; development only) |
| `--pin-sha256` | `stringArray` | `[]` | **inherited**. trusted leaf SPKI SHA-256 pin, base64 or hex, repeatable — the engine prints it as pin_sha256 on the line reporting its certificate (default: current context) |
| `--server` | `string` | — | **inherited**. control-plane base URL (default $OLIVARES_SERVER_URL, then current context) |
| `--tenant` | `string` | — | **inherited**. tenant id (default $OLIVARES_TENANT, then current context) |
| `--timeout` | `duration` | `10s` | **inherited**. request timeout |
| `--token` | `string` | — | **inherited**. API bearer token (prefer --token-file: this form is visible in the process table and in shell history; default $OLIVARES_TOKEN, then current context) |
| `--token-file` | `string` | — | **inherited**. read the API bearer token from a file, or - for stdin |

#### Command: olivares sandbox compare

Run the same scenario as two variants and record the verdict

```
olivares sandbox compare
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--baseline-variant` | `string` | — | **required**. the variant label to treat as the baseline (required) |
| `--candidate-variant` | `string` | — | **required**. the variant label to treat as the candidate (required) |
| `--scenario-ref` | `string` | — | compare using this scenario's steps |
| `--session-ref` | `string` | — | compare using this recorded session's steps |
| `--suite-ref` | `string` | — | score both runs against this evals suite |

#### Command: olivares sandbox comparisons

Inspect the append-only A/B comparison ledger

```
olivares sandbox comparisons
```

Declares no flags of its own; it takes those of [`olivares sandbox`](#command-olivares-sandbox) and the root command.

#### Command: olivares sandbox comparisons get

Show one comparison

```
olivares sandbox comparisons get <id>
```

Declares no flags of its own; it takes those of [`olivares sandbox comparisons`](#command-olivares-sandbox-comparisons) and the root command.

#### Command: olivares sandbox comparisons ls

List recorded comparisons

```
olivares sandbox comparisons ls
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--cursor` | `string` | — | continue from the cursor a previous page reported |
| `--limit` | `int` | `0` | page size (0 uses the engine's default) |
| `--scenario-ref` | `string` | — | only comparisons of this scenario |
| `--verdict` | `string` | — | only comparisons with this verdict |

#### Command: olivares sandbox replay

Deterministically re-execute a recorded session against supplied mocks

```
olivares sandbox replay
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--mocks-file` | `string` | — | JSON array of mock objects, '-' for stdin |
| `--session-ref` | `string` | — | **required**. the recorded session to replay (required) |
| `--suite-ref` | `string` | — | score the replayed outputs against this evals suite |

#### Command: olivares sandbox runs

Inspect sandbox runs, their outputs and their live stream

```
olivares sandbox runs
```

Declares no flags of its own; it takes those of [`olivares sandbox`](#command-olivares-sandbox) and the root command.

#### Command: olivares sandbox runs get

Show one run

```
olivares sandbox runs get <id>
```

Declares no flags of its own; it takes those of [`olivares sandbox runs`](#command-olivares-sandbox-runs) and the root command.

#### Command: olivares sandbox runs ls

List sandbox runs

```
olivares sandbox runs ls
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--cursor` | `string` | — | continue from the cursor a previous page reported |
| `--kind` | `string` | — | only runs of this kind |
| `--limit` | `int` | `0` | page size (0 uses the engine's default) |
| `--scenario-ref` | `string` | — | only runs of this scenario |

#### Command: olivares sandbox runs outputs

List one run's per-step outputs

```
olivares sandbox runs outputs <id>
```

Declares no flags of its own; it takes those of [`olivares sandbox runs`](#command-olivares-sandbox-runs) and the root command.

#### Command: olivares sandbox runs stream

Follow a live run as NDJSON (one object per event)

```
olivares sandbox runs stream <id>
```

Declares no flags of its own; it takes those of [`olivares sandbox runs`](#command-olivares-sandbox-runs) and the root command.

#### Command: olivares sandbox scenarios

Author, inspect, run and archive sandbox scenarios

```
olivares sandbox scenarios
```

Declares no flags of its own; it takes those of [`olivares sandbox`](#command-olivares-sandbox) and the root command.

#### Command: olivares sandbox scenarios archive

Archive a scenario (destructive; needs --yes when unattended)

```
olivares sandbox scenarios archive <id>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `-y`, `--yes` | `bool` | `false` | proceed without the confirmation prompt (required in a non-interactive session) |

#### Command: olivares sandbox scenarios create

Author a scenario from JSON step and mock files

```
olivares sandbox scenarios create
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--description` | `string` | — | what the fixture exercises |
| `--mocks-file` | `string` | — | JSON array of mock objects, '-' for stdin |
| `--name` | `string` | — | **required**. scenario name (required, unique per tenant) |
| `--steps-file` | `string` | — | JSON array of step objects, '-' for stdin |
| `--subject-kind` | `string` | — | what kind of subject the scenario drives |

#### Command: olivares sandbox scenarios get

Show one scenario with its steps and mocks

```
olivares sandbox scenarios get <id>
```

Declares no flags of its own; it takes those of [`olivares sandbox scenarios`](#command-olivares-sandbox-scenarios) and the root command.

#### Command: olivares sandbox scenarios ls

List the tenant's scenarios

```
olivares sandbox scenarios ls
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--cursor` | `string` | — | continue from the cursor a previous page reported |
| `--limit` | `int` | `0` | page size (0 uses the engine's default) |
| `--status` | `string` | — | only scenarios in this status |

#### Command: olivares sandbox scenarios run

Run a scenario against the isolated runner (synchronous)

```
olivares sandbox scenarios run <id>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--suite-ref` | `string` | — | score the outputs against this evals suite |
| `--variant` | `string` | — | label this run's variant (used by compare) |

#### Command: olivares secrets

Manage the runtime secret store (sealed; referenced from configs as store:&lt;name&gt;)

```
olivares secrets
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--format` | `string` | `text` | **inherited**. deprecated alias for -o/--output on this command (text or json) — NOT the export-format flag of 'audit export' / 'findings export' |

#### Command: olivares secrets ls

List stored secrets (names and non-secret hints; never the value)

```
olivares secrets ls
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--data-dir` | `string` | — | data directory (default $OLIVARES_DATA_DIR, an existing ./olivares-data, else $XDG_DATA_HOME/olivares or ~/.local/share/olivares) |
| `--dsn` | `string` | — | store DSN (default a SQLite file in the data dir) |
| `--engine` | `string` | `sqlite` | store engine: sqlite or postgres |

#### Command: olivares secrets put

Create or update a secret (seals the value at rest)

```
olivares secrets put
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--actor` | `string` | — | REQUIRED: who is performing this privileged operation (an operator or service identity; recorded in the audit ledger) |
| `--data-dir` | `string` | — | data directory (default $OLIVARES_DATA_DIR, an existing ./olivares-data, else $XDG_DATA_HOME/olivares or ~/.local/share/olivares) |
| `--description` | `string` | — | optional non-secret note |
| `--dsn` | `string` | — | store DSN (default a SQLite file in the data dir) |
| `--engine` | `string` | `sqlite` | store engine: sqlite or postgres |
| `--name` | `string` | — | **required**. secret name (referenced as store:&lt;name&gt;) |
| `--reason` | `string` | — | REQUIRED: why this privileged operation is being performed (recorded in the audit ledger) |
| `--value` | `string` | — | secret value (prefer --value-file to keep it out of shell history) |
| `--value-file` | `string` | — | read the value from a file, or - for stdin |

#### Command: olivares secrets rm

Delete a secret (a reference to it then fails closed)

```
olivares secrets rm
```

Aliases: `delete`, `remove`

| Flag | Type | Default | Description |
|---|---|---|---|
| `--actor` | `string` | — | REQUIRED: who is performing this privileged operation (an operator or service identity; recorded in the audit ledger) |
| `--data-dir` | `string` | — | data directory (default $OLIVARES_DATA_DIR, an existing ./olivares-data, else $XDG_DATA_HOME/olivares or ~/.local/share/olivares) |
| `--dsn` | `string` | — | store DSN (default a SQLite file in the data dir) |
| `--engine` | `string` | `sqlite` | store engine: sqlite or postgres |
| `--name` | `string` | — | **required**. secret name |
| `--reason` | `string` | — | REQUIRED: why this privileged operation is being performed (recorded in the audit ledger) |
| `-y`, `--yes` | `bool` | `false` | proceed without the confirmation prompt (required in a non-interactive session) |

#### Command: olivares secrets rotate

Replace a secret's value (a new value is required)

```
olivares secrets rotate
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--actor` | `string` | — | REQUIRED: who is performing this privileged operation (an operator or service identity; recorded in the audit ledger) |
| `--data-dir` | `string` | — | data directory (default $OLIVARES_DATA_DIR, an existing ./olivares-data, else $XDG_DATA_HOME/olivares or ~/.local/share/olivares) |
| `--dsn` | `string` | — | store DSN (default a SQLite file in the data dir) |
| `--engine` | `string` | `sqlite` | store engine: sqlite or postgres |
| `--name` | `string` | — | **required**. secret name |
| `--reason` | `string` | — | REQUIRED: why this privileged operation is being performed (recorded in the audit ledger) |
| `--value` | `string` | — | new secret value (prefer --value-file) |
| `--value-file` | `string` | — | read the new value from a file, or - for stdin |

#### Command: olivares security

Security self-checks (advisory feed verification and affected-version reporting)

```
olivares security
```

Declares no flags of its own; it takes those of [`olivares`](#command-olivares) and the root command.

#### Command: olivares security advisories

Hidden diagnostic: it does not appear in `--help` output and is not part of the supported surface.

Build and sign an OSV advisory feed the product self-checks — PSIRT use

```
olivares security advisories
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--author` | `string` | — | override the feed author |
| `--expect-pubkey` | `string` | — | **required**. base64 (or @file) Ed25519 PUBLIC key the signature must verify against — the anchor the fleet pins (required) |
| `--in` | `string` | — | draft advisory JSON ({"author":"…","advisories":[…OSV…]}) (required) |
| `--out` | `string` | `advisories.json` | output feed path (a .sig is written beside it) |
| `--sign-key` | `string` | — | base64 (or @file) Ed25519 private key (required) |

#### Command: olivares security check

Check a product version against a signed advisories feed

```
olivares security check
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--feed` | `string` | — | path to the signed advisories feed (OSV JSON) |
| `--product-version` | `string` | — | product version to check (default: the running binary version) |
| `--pubkey` | `string` | — | release public key (base64 or @file); default: the embedded key |
| `--quiet` | `bool` | `false` | print nothing when unaffected |
| `--sig` | `string` | — | path to the detached signature (default: &lt;feed&gt;.sig) |

#### Command: olivares security drill

Timed end-to-end PSIRT advisory-pipeline drill

```
olivares security drill
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--draft` | `string` | — | override the embedded advisory draft fixture |
| `--keep-artifacts` | `bool` | `false` | keep the scratch dir instead of removing it (debugging) |

#### Command: olivares security rulepack

Author/verify signed hot-reload security rule-packs (deny-lists, MCP blocks, patterns)

```
olivares security rulepack
```

Declares no flags of its own; it takes those of [`olivares security`](#command-olivares-security) and the root command.

#### Command: olivares security rulepack sign

Hidden diagnostic: it does not appear in `--help` output and is not part of the supported surface.

Build and sign a rule-pack from a draft (writes &lt;out&gt; + &lt;out&gt;.sig)

```
olivares security rulepack sign
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--expect-pubkey` | `string` | — | **required**. base64 (or @file) Ed25519 PUBLIC key the signature must verify against — the anchor the fleet pins (required) |
| `--in` | `string` | — | draft rule-pack JSON (required) |
| `--out` | `string` | `rulepack.json` | output rule-pack path |
| `--sign-key` | `string` | — | base64 (or @file) Ed25519 private key (required) |

#### Command: olivares security rulepack verify

Verify a signed rule-pack against a trusted key and print its summary

```
olivares security rulepack verify
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--in` | `string` | — | rule-pack JSON to verify (required) |
| `--pubkey` | `string` | — | base64 Ed25519 trusted key (required) |
| `--sig` | `string` | — | signature path (default: &lt;in&gt;.sig) |

#### Command: olivares serve

Run the engine (REST + gRPC + embedded console), TLS-on-by-default

```
olivares serve
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--admin-dsn` | `string` | — | Postgres only: DSN of a dedicated NOSUPERUSER BYPASSRLS role used ONLY for cross-tenant System reads (org list, multi-tenant checkpoint coverage). Without it those reads are RLS-limited (see deploy/postgres/01-app-role.sql) |
| `--allow-privileged-db-role` | `bool` | `false` | allow connecting Postgres as a superuser/BYPASSRLS role (DANGEROUS: disables the row-level-security tenant backstop; single-tenant/dev only) |
| `--checkpoint-interval` | `duration` | `1h0m0s` | how often to write a signed audit checkpoint over every tenant chain (0 disables; tamper-evidence anchor, docs/SECURITY-HARDENING.md §5) |
| `--data-dir` | `string` | — | data directory (default $OLIVARES_DATA_DIR, an existing ./olivares-data, else $XDG_DATA_HOME/olivares or ~/.local/share/olivares) |
| `--dsn` | `string` | — | store DSN (default a SQLite file in the data dir). May be a file:&lt;path&gt; or env:&lt;VAR&gt; reference resolved at boot, so the password stays out of the env file |
| `--engine` | `string` | `sqlite` | store engine: sqlite or postgres |
| `--grpc-client-ca` | `string` | — | PEM bundle of CAs authorized to issue collector client certs; when set, the gRPC server requires mutual TLS (verified client cert) for collector→core (docs/SECURITY-HARDENING.md §1/§3) |
| `--grpc-listen` | `string` | `127.0.0.1:8444` | gRPC listen address |
| `--insecure` | `bool` | `false` | serve plaintext HTTP/gRPC (DANGEROUS; localhost dev only). A non-loopback bind is REFUSED unless --insecure-allow-public-bind is also given |
| `--insecure-allow-public-bind` | `bool` | `false` | with --insecure, allow binding a non-loopback address (DANGEROUS: the console, bearer tokens and the first-boot setup token cross the network in CLEAR TEXT). Only for a deployment where something in front of the engine terminates TLS. Inert without --insecure |
| `--known-regions` | `stringSlice` | `[]` | comma-separated region codes valid across the whole deployment (e.g. eu,us); a tenant pin must be one of these. The home --region is always included. Only meaningful with --region set |
| `--license` | `string` | — | path to a commercial license file (informational only) |
| `--listen` | `string` | `127.0.0.1:8443` | HTTP (REST + web) listen address |
| `--owner-dsn` | `string` | — | Postgres only: DSN of the owner role that owns the schema and runs DDL/migrations. Set it to a SEPARATE NOSUPERUSER NOBYPASSRLS role to make --dsn a least-privilege non-owner app role with only DML grants (provision both with `olivares db init`). Empty = the --dsn role owns the schema (single-role). Accepts a file:/env: reference like --dsn |
| `--region` | `string` | — | data-residency HOME region of THIS instance (e.g. eu, us). When set, the instance is region-scoped: it serves only tenants pinned to this region and denies cross-region access fail-closed. Empty = single-region mode, no residency enforcement |
| `--reuse-port` | `bool` | `false` | bind listeners with SO_REUSEPORT so a NEW instance can hold the same ports while this one drains — enables a zero-downtime restart/upgrade handover on a single node (Linux/BSD; docs/UPGRADE-AND-ROLLBACK.md) |
| `--seed-demo` | `bool` | `false` | load a SYNTHETIC sample estate for demos/E2E (fabricated data; use a throwaway data-dir) |
| `--tls-cert` | `string` | — | TLS certificate PEM (default a self-signed cert in the data dir) |
| `--tls-key` | `string` | — | TLS private key PEM |

#### Command: olivares setup

Guided, validated first-run configuration (profiles, Postgres onboarding, no SQL by hand)

```
olivares setup
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--force` | `bool` | `false` | overwrite the env file / secret files if they exist |
| `--out` | `string` | `/etc/olivares/olivares.env` | env file to write |
| `--secrets-dir` | `string` | `/etc/olivares/secrets` | directory for 0600 secret files (DSNs) |

#### Command: olivares sources

Manage the durable source roster (connectors the engine ingests from)

```
olivares sources
```

Declares no flags of its own; it takes those of [`olivares`](#command-olivares) and the root command.

#### Command: olivares sources get

Show one source's definition, including the config `ls` cannot render

```
olivares sources get <name>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--data-dir` | `string` | — | data directory (default $OLIVARES_DATA_DIR, an existing ./olivares-data, else $XDG_DATA_HOME/olivares or ~/.local/share/olivares) |
| `--dsn` | `string` | — | store DSN (default a SQLite file in the data dir) |
| `--engine` | `string` | `sqlite` | store engine: sqlite or postgres |

#### Command: olivares sources ls

List the source roster (name, kind, tenant, mode, poll, enabled)

```
olivares sources ls
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--data-dir` | `string` | — | data directory (default $OLIVARES_DATA_DIR, an existing ./olivares-data, else $XDG_DATA_HOME/olivares or ~/.local/share/olivares) |
| `--dsn` | `string` | — | store DSN (default a SQLite file in the data dir) |
| `--engine` | `string` | `sqlite` | store engine: sqlite or postgres |

#### Command: olivares sources plan

Show what a `sources set` with these flags WOULD change — no source is written or opened

```
olivares sources plan
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--config` | `stringArray` | `[]` | connector setting key=value (repeatable); use store:&lt;name&gt; for secrets |
| `--data-dir` | `string` | — | data directory (default $OLIVARES_DATA_DIR, an existing ./olivares-data, else $XDG_DATA_HOME/olivares or ~/.local/share/olivares) |
| `--dsn` | `string` | — | store DSN (default a SQLite file in the data dir) |
| `--enabled` | `bool` | `true` | whether the source is wired into the engine |
| `--engine` | `string` | `sqlite` | store engine: sqlite or postgres |
| `--kind` | `string` | — | first-party connector kind (e.g. vault, claude); omit for a plugin source |
| `--name` | `string` | — | **required**. source name (the roster key) |
| `--plugin-bundle` | `string` | — | external plugin Sigstore attestation bundle path |
| `--plugin-path` | `string` | — | external connector plugin binary path |
| `--plugin-predicate` | `stringArray` | `[]` | narrow the trust policy's predicate allow-list for this source (repeatable) |
| `--plugin-sha256` | `string` | — | external plugin pinned sha256 digest |
| `--poll-seconds` | `int` | `0` | re-run a batch source every N seconds (0 = run once / streaming) |
| `--tenant` | `string` | — | business tenant the observations belong to |

#### Command: olivares sources rm

Delete a source from the roster

```
olivares sources rm
```

Aliases: `delete`, `remove`

| Flag | Type | Default | Description |
|---|---|---|---|
| `--actor` | `string` | — | REQUIRED: who is performing this privileged operation (an operator or service identity; recorded in the audit ledger) |
| `--data-dir` | `string` | — | data directory (default $OLIVARES_DATA_DIR, an existing ./olivares-data, else $XDG_DATA_HOME/olivares or ~/.local/share/olivares) |
| `--dsn` | `string` | — | store DSN (default a SQLite file in the data dir) |
| `--engine` | `string` | `sqlite` | store engine: sqlite or postgres |
| `--name` | `string` | — | **required**. source name |
| `--reason` | `string` | — | REQUIRED: why this privileged operation is being performed (recorded in the audit ledger) |
| `-y`, `--yes` | `bool` | `false` | proceed without the confirmation prompt (required in a non-interactive session) |

#### Command: olivares sources set

Create or update a source (only the flags you pass are changed on an existing source)

```
olivares sources set
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--actor` | `string` | — | REQUIRED: who is performing this privileged operation (an operator or service identity; recorded in the audit ledger) |
| `--config` | `stringArray` | `[]` | connector setting key=value (repeatable); use store:&lt;name&gt; for secrets |
| `--data-dir` | `string` | — | data directory (default $OLIVARES_DATA_DIR, an existing ./olivares-data, else $XDG_DATA_HOME/olivares or ~/.local/share/olivares) |
| `--dsn` | `string` | — | store DSN (default a SQLite file in the data dir) |
| `--enabled` | `bool` | `true` | whether the source is wired into the engine |
| `--engine` | `string` | `sqlite` | store engine: sqlite or postgres |
| `--kind` | `string` | — | first-party connector kind (e.g. vault, claude); omit for a plugin source |
| `--name` | `string` | — | **required**. source name (the roster key) |
| `--plugin-bundle` | `string` | — | external plugin Sigstore attestation bundle path |
| `--plugin-path` | `string` | — | external connector plugin binary path |
| `--plugin-predicate` | `stringArray` | `[]` | narrow the trust policy's predicate allow-list for this source (repeatable) |
| `--plugin-sha256` | `string` | — | external plugin pinned sha256 digest |
| `--poll-seconds` | `int` | `0` | re-run a batch source every N seconds (0 = run once / streaming) |
| `--reason` | `string` | — | REQUIRED: why this privileged operation is being performed (recorded in the audit ledger) |
| `--tenant` | `string` | — | business tenant the observations belong to |

#### Command: olivares sources test

Open the source for real to prove it answers, then close it — nothing is wired or written

```
olivares sources test
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--config` | `stringArray` | `[]` | connector setting key=value (repeatable); use store:&lt;name&gt; for secrets |
| `--data-dir` | `string` | — | data directory (default $OLIVARES_DATA_DIR, an existing ./olivares-data, else $XDG_DATA_HOME/olivares or ~/.local/share/olivares) |
| `--dsn` | `string` | — | store DSN (default a SQLite file in the data dir) |
| `--enabled` | `bool` | `true` | whether the source is wired into the engine |
| `--engine` | `string` | `sqlite` | store engine: sqlite or postgres |
| `--kind` | `string` | — | first-party connector kind (e.g. vault, claude); omit for a plugin source |
| `--name` | `string` | — | **required**. source name (the roster key) |
| `--plugin-bundle` | `string` | — | external plugin Sigstore attestation bundle path |
| `--plugin-path` | `string` | — | external connector plugin binary path |
| `--plugin-predicate` | `stringArray` | `[]` | narrow the trust policy's predicate allow-list for this source (repeatable) |
| `--plugin-sha256` | `string` | — | external plugin pinned sha256 digest |
| `--poll-seconds` | `int` | `0` | re-run a batch source every N seconds (0 = run once / streaming) |
| `--show-connector-error` | `bool` | `false` | print the connector's own failure message. It was produced against the RESOLVED configuration and can embed credential material, so it is off by default |
| `--tenant` | `string` | — | business tenant the observations belong to |
| `--timeout` | `duration` | `30s` | give up on the connector after this long (a source that never answers must not hang the command forever) |

#### Command: olivares sources validate

Check a source definition is coherent by itself — offline, no network, no writes

```
olivares sources validate
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--config` | `stringArray` | `[]` | connector setting key=value (repeatable); use store:&lt;name&gt; for secrets |
| `--data-dir` | `string` | — | data directory (default $OLIVARES_DATA_DIR, an existing ./olivares-data, else $XDG_DATA_HOME/olivares or ~/.local/share/olivares) |
| `--dsn` | `string` | — | store DSN (default a SQLite file in the data dir) |
| `--enabled` | `bool` | `true` | whether the source is wired into the engine |
| `--engine` | `string` | `sqlite` | store engine: sqlite or postgres |
| `--kind` | `string` | — | first-party connector kind (e.g. vault, claude); omit for a plugin source |
| `--name` | `string` | — | source name (the roster key) |
| `--plugin-bundle` | `string` | — | external plugin Sigstore attestation bundle path |
| `--plugin-path` | `string` | — | external connector plugin binary path |
| `--plugin-predicate` | `stringArray` | `[]` | narrow the trust policy's predicate allow-list for this source (repeatable) |
| `--plugin-sha256` | `string` | — | external plugin pinned sha256 digest |
| `--poll-seconds` | `int` | `0` | re-run a batch source every N seconds (0 = run once / streaming) |
| `--tenant` | `string` | — | business tenant the observations belong to |

#### Command: olivares sourcescope

Decide which sources a workspace or agent may reach

```
olivares sourcescope
```

Aliases: `source-scope`

| Flag | Type | Default | Description |
|---|---|---|---|
| `--allow-cleartext` | `bool` | `false` | **inherited**. allow sending the credential to a non-loopback host over plain HTTP (DANGEROUS: it travels readable) |
| `--ca-cert` | `string` | — | **inherited**. PEM file containing an additional trusted root CA (default: current context) |
| `--insecure` | `bool` | `false` | **inherited**. skip TLS certificate verification (DANGEROUS; development only) |
| `--pin-sha256` | `stringArray` | `[]` | **inherited**. trusted leaf SPKI SHA-256 pin, base64 or hex, repeatable — the engine prints it as pin_sha256 on the line reporting its certificate (default: current context) |
| `--server` | `string` | — | **inherited**. control-plane base URL (default $OLIVARES_SERVER_URL, then current context) |
| `--tenant` | `string` | — | **inherited**. tenant id (default $OLIVARES_TENANT, then current context) |
| `--timeout` | `duration` | `10s` | **inherited**. request timeout |
| `--token` | `string` | — | **inherited**. API bearer token (prefer --token-file: this form is visible in the process table and in shell history; default $OLIVARES_TOKEN, then current context) |
| `--token-file` | `string` | — | **inherited**. read the API bearer token from a file, or - for stdin |

#### Command: olivares sourcescope assignments

Assign global connectors to workspaces

```
olivares sourcescope assignments
```

Declares no flags of its own; it takes those of [`olivares sourcescope`](#command-olivares-sourcescope) and the root command.

#### Command: olivares sourcescope assignments create

Assign a connector to a workspace

```
olivares sourcescope assignments create
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--connector-name` | `string` | — | name of the global connector being assigned |
| `--enabled` | `bool` | `false` | whether the assignment is in force |
| `--mode` | `string` | — | rw (default) or r |
| `--note` | `string` | — | note recorded with the assignment |
| `--workspace-ref` | `string` | — | workspace the connector is assigned to |

#### Command: olivares sourcescope assignments get

Show one assignment

```
olivares sourcescope assignments get <assignment-id>
```

Declares no flags of its own; it takes those of [`olivares sourcescope assignments`](#command-olivares-sourcescope-assignments) and the root command.

#### Command: olivares sourcescope assignments ls

List connector-to-workspace assignments

```
olivares sourcescope assignments ls
```

Aliases: `list`

| Flag | Type | Default | Description |
|---|---|---|---|
| `--connector-name` | `string` | — | only assignments of this connector |
| `--cursor` | `string` | — | opaque cursor from a previous page's has_more result |
| `--limit` | `int` | `0` | maximum rows per page (server default when unset) |
| `--workspace-ref` | `string` | — | only assignments to this workspace |

#### Command: olivares sourcescope assignments rm

Delete an assignment

```
olivares sourcescope assignments rm <assignment-id>
```

Aliases: `delete`

| Flag | Type | Default | Description |
|---|---|---|---|
| `-y`, `--yes` | `bool` | `false` | proceed without the confirmation prompt (required in a non-interactive session) |

#### Command: olivares sourcescope assignments set

Replace an assignment

```
olivares sourcescope assignments set <assignment-id>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--connector-name` | `string` | — | name of the global connector being assigned |
| `--enabled` | `bool` | `false` | whether the assignment is in force |
| `--mode` | `string` | — | rw (default) or r |
| `--note` | `string` | — | note recorded with the assignment |
| `--replace` | `bool` | `false` | accept that every field not passed is RESET to its server default (this endpoint replaces, it does not patch) |
| `--workspace-ref` | `string` | — | workspace the connector is assigned to |

#### Command: olivares sourcescope bindings

Confine a source to a workspace or agent group

```
olivares sourcescope bindings
```

Declares no flags of its own; it takes those of [`olivares sourcescope`](#command-olivares-sourcescope) and the root command.

#### Command: olivares sourcescope bindings create

Bind a source to a scope

```
olivares sourcescope bindings create
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--cred-hint` | `string` | — | non-secret hint shown to operators |
| `--cred-name` | `string` | — | name of the scoped credential the binding carries |
| `--cred-ref` | `string` | — | credential reference (a locator, never a secret) |
| `--cred-ref-kind` | `string` | — | kind of the credential reference |
| `--effect` | `string` | — | allow or forbid |
| `--enabled` | `bool` | `false` | whether the binding is in force |
| `--folder-path` | `string` | — | folder or subtree the binding is anchored to |
| `--note` | `string` | — | note recorded with the binding |
| `--scope-ref` | `string` | — | reference within the scope tree |
| `--scope-tree` | `string` | — | scope tree the binding attaches to (e.g. workspace, agent_group) |
| `--source-ref` | `string` | — | reference of the source being confined |
| `--source-type` | `string` | — | mcp, model, provider, knowledge or data |

#### Command: olivares sourcescope bindings get

Show one binding

```
olivares sourcescope bindings get <binding-id>
```

Declares no flags of its own; it takes those of [`olivares sourcescope bindings`](#command-olivares-sourcescope-bindings) and the root command.

#### Command: olivares sourcescope bindings ls

List source-to-scope bindings

```
olivares sourcescope bindings ls
```

Aliases: `list`

| Flag | Type | Default | Description |
|---|---|---|---|
| `--cursor` | `string` | — | opaque cursor from a previous page's has_more result |
| `--limit` | `int` | `0` | maximum rows per page (server default when unset) |
| `--scope-tree` | `string` | — | only bindings in this scope tree |
| `--source-ref` | `string` | — | only bindings of this source reference |
| `--source-type` | `string` | — | only bindings of this source type |

#### Command: olivares sourcescope bindings rm

Delete a binding

```
olivares sourcescope bindings rm <binding-id>
```

Aliases: `delete`

| Flag | Type | Default | Description |
|---|---|---|---|
| `-y`, `--yes` | `bool` | `false` | proceed without the confirmation prompt (required in a non-interactive session) |

#### Command: olivares sourcescope bindings set

Replace a binding

```
olivares sourcescope bindings set <binding-id>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--cred-hint` | `string` | — | non-secret hint shown to operators |
| `--cred-name` | `string` | — | name of the scoped credential the binding carries |
| `--cred-ref` | `string` | — | credential reference (a locator, never a secret) |
| `--cred-ref-kind` | `string` | — | kind of the credential reference |
| `--effect` | `string` | — | allow or forbid |
| `--enabled` | `bool` | `false` | whether the binding is in force |
| `--folder-path` | `string` | — | folder or subtree the binding is anchored to |
| `--note` | `string` | — | note recorded with the binding |
| `--replace` | `bool` | `false` | accept that every field not passed is RESET to its server default (this endpoint replaces, it does not patch) |
| `--scope-ref` | `string` | — | reference within the scope tree |
| `--scope-tree` | `string` | — | scope tree the binding attaches to (e.g. workspace, agent_group) |
| `--source-ref` | `string` | — | reference of the source being confined |
| `--source-type` | `string` | — | mcp, model, provider, knowledge or data |

#### Command: olivares sourcescope guard-postures

Read and set the retrieval guard posture

```
olivares sourcescope guard-postures
```

Declares no flags of its own; it takes those of [`olivares sourcescope`](#command-olivares-sourcescope) and the root command.

#### Command: olivares sourcescope guard-postures ls

List explicit guard-posture overrides

```
olivares sourcescope guard-postures ls
```

Aliases: `list`

| Flag | Type | Default | Description |
|---|---|---|---|
| `--cursor` | `string` | — | opaque cursor from a previous page's has_more result |
| `--limit` | `int` | `0` | maximum rows per page (server default when unset) |
| `--profile` | `string` | — | only postures with this profile |
| `--source-ref` | `string` | — | only postures of this source reference |
| `--source-type` | `string` | — | only postures of this source type |

#### Command: olivares sourcescope guard-postures set

Set the guard posture of one source

```
olivares sourcescope guard-postures set
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--profile` | `string` | — | acl_aware (tightens) or public_only (relaxes, dual-controlled) |
| `--reason` | `string` | — | reason an approver will read |
| `--source-ref` | `string` | — | reference of the source the posture applies to |
| `--source-type` | `string` | — | source type (the control plane requires knowledge here) |

#### Command: olivares sourcescope posture-requests

Review the dual-control queue of proposed relaxations

```
olivares sourcescope posture-requests
```

Declares no flags of its own; it takes those of [`olivares sourcescope`](#command-olivares-sourcescope) and the root command.

#### Command: olivares sourcescope posture-requests approve

Approve a pending relaxation and apply it

```
olivares sourcescope posture-requests approve <request-id>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `-y`, `--yes` | `bool` | `false` | proceed without the confirmation prompt (required in a non-interactive session) |

#### Command: olivares sourcescope posture-requests get

Show one posture-change request

```
olivares sourcescope posture-requests get <request-id>
```

Declares no flags of its own; it takes those of [`olivares sourcescope posture-requests`](#command-olivares-sourcescope-posture-requests) and the root command.

#### Command: olivares sourcescope posture-requests ls

List posture-change requests

```
olivares sourcescope posture-requests ls
```

Aliases: `list`

| Flag | Type | Default | Description |
|---|---|---|---|
| `--cursor` | `string` | — | opaque cursor from a previous page's has_more result |
| `--limit` | `int` | `0` | maximum rows per page (server default when unset) |
| `--source-ref` | `string` | — | only requests for this source reference |
| `--source-type` | `string` | — | only requests for this source type |
| `--status` | `string` | — | only requests in this status |

#### Command: olivares sourcescope posture-requests reject

Reject a pending relaxation, changing nothing

```
olivares sourcescope posture-requests reject <request-id>
```

Declares no flags of its own; it takes those of [`olivares sourcescope posture-requests`](#command-olivares-sourcescope-posture-requests) and the root command.

#### Command: olivares sourcescope resolve

Preview what one actor would resolve for one source

```
olivares sourcescope resolve
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--actor-kind` | `string` | — | session or agent |
| `--actor-ref` | `string` | — | reference of the actor to resolve for |
| `--source-ref` | `string` | — | reference of the source to resolve |
| `--source-type` | `string` | — | mcp, model, provider, knowledge or data |

#### Command: olivares sourcescope resources

Navigate the tenant's resource tree

```
olivares sourcescope resources
```

Declares no flags of its own; it takes those of [`olivares sourcescope`](#command-olivares-sourcescope) and the root command.

#### Command: olivares sourcescope resources ls

List resources, by children or by subtree

```
olivares sourcescope resources ls
```

Aliases: `list`

| Flag | Type | Default | Description |
|---|---|---|---|
| `--cursor` | `string` | — | opaque cursor from a previous page's has_more result |
| `--kind` | `string` | — | only resources of this kind |
| `--limit` | `int` | `0` | maximum rows per page (server default when unset) |
| `--parent` | `string` | — | list the direct children of this resource id |
| `--subtree` | `string` | — | list everything beneath this resource id |
| `--workspace-id` | `string` | — | only resources of this workspace |

#### Command: olivares sourcescope sources

Source-wide posture operations

```
olivares sourcescope sources
```

Declares no flags of its own; it takes those of [`olivares sourcescope`](#command-olivares-sourcescope) and the root command.

#### Command: olivares sourcescope sources disable-scoping

Propose removing ALL scoping from a source

```
olivares sourcescope sources disable-scoping
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--source-ref` | `string` | — | reference of the source to unconfine |
| `--source-type` | `string` | — | mcp, model, provider, knowledge or data |
| `-y`, `--yes` | `bool` | `false` | proceed without the confirmation prompt (required in a non-interactive session) |

#### Command: olivares sourcescope workspace-connectors

Manage connectors that belong to one workspace

```
olivares sourcescope workspace-connectors
```

Aliases: `ws-connectors`

Declares no flags of its own; it takes those of [`olivares sourcescope`](#command-olivares-sourcescope) and the root command.

#### Command: olivares sourcescope workspace-connectors create

Declare a workspace connector

```
olivares sourcescope workspace-connectors create
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--config` | `stringArray` | `[]` | config entry as key=value, repeatable |
| `--enabled` | `bool` | `false` | whether the connector is in force |
| `--kind` | `string` | — | connector kind |
| `--name` | `string` | — | workspace connector name |
| `--note` | `string` | — | note recorded with the connector |
| `--poll-seconds` | `int` | `0` | polling interval in seconds |
| `--secrets-file` | `string` | — | file holding the secrets as a JSON object of string values (- for stdin) |
| `--workspace-ref` | `string` | — | workspace the connector belongs to |

#### Command: olivares sourcescope workspace-connectors get

Show one workspace connector

```
olivares sourcescope workspace-connectors get <connector-id>
```

Declares no flags of its own; it takes those of [`olivares sourcescope workspace-connectors`](#command-olivares-sourcescope-workspace-connectors) and the root command.

#### Command: olivares sourcescope workspace-connectors ls

List workspace connectors

```
olivares sourcescope workspace-connectors ls
```

Aliases: `list`

| Flag | Type | Default | Description |
|---|---|---|---|
| `--cursor` | `string` | — | opaque cursor from a previous page's has_more result |
| `--kind` | `string` | — | only connectors of this kind |
| `--limit` | `int` | `0` | maximum rows per page (server default when unset) |
| `--workspace-ref` | `string` | — | only connectors of this workspace |

#### Command: olivares sourcescope workspace-connectors rm

Delete a workspace connector

```
olivares sourcescope workspace-connectors rm <connector-id>
```

Aliases: `delete`

| Flag | Type | Default | Description |
|---|---|---|---|
| `-y`, `--yes` | `bool` | `false` | proceed without the confirmation prompt (required in a non-interactive session) |

#### Command: olivares sourcescope workspace-connectors set

Replace a workspace connector

```
olivares sourcescope workspace-connectors set <connector-id>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--config` | `stringArray` | `[]` | config entry as key=value, repeatable |
| `--enabled` | `bool` | `false` | whether the connector is in force |
| `--kind` | `string` | — | connector kind |
| `--name` | `string` | — | workspace connector name |
| `--note` | `string` | — | note recorded with the connector |
| `--poll-seconds` | `int` | `0` | polling interval in seconds |
| `--replace` | `bool` | `false` | accept that every field not passed is RESET to its server default (this endpoint replaces, it does not patch) |
| `--secrets-file` | `string` | — | file holding the secrets as a JSON object of string values (- for stdin) |
| `--workspace-ref` | `string` | — | workspace the connector belongs to |

#### Command: olivares status

Show the engine public status, including knowledge retrieval posture

```
olivares status
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--ca-cert` | `string` | — | PEM file containing an additional trusted root CA (default: current context) |
| `--insecure` | `bool` | `false` | skip TLS certificate verification (self-signed dev planes only) |
| `--json` | `bool` | `false` | deprecated alias for -o json |
| `--pin-sha256` | `stringArray` | `[]` | trusted leaf SPKI SHA-256 pin, base64 or hex, repeatable — the engine prints it as pin_sha256 on the line reporting its certificate (default: current context) |
| `--server` | `string` | — | control-plane base URL (default $OLIVARES_SERVER_URL, then current context) |
| `--timeout` | `duration` | `10s` | request timeout |

#### Command: olivares superadmin

Enable/disable internal superadmin accounts (never deletes)

```
olivares superadmin
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--format` | `string` | `text` | **inherited**. deprecated alias for -o/--output on this command (text or json) — NOT the export-format flag of 'audit export' / 'findings export' |

#### Command: olivares superadmin disable

Disable an internal superadmin (marks it inactive and revokes its sessions/tokens; never deletes)

```
olivares superadmin disable
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--actor` | `string` | — | REQUIRED: who is performing this privileged operation (an operator or service identity; recorded in the audit ledger) |
| `--data-dir` | `string` | — | data directory (default $OLIVARES_DATA_DIR, an existing ./olivares-data, else $XDG_DATA_HOME/olivares or ~/.local/share/olivares) |
| `--dsn` | `string` | — | store DSN (default a SQLite file in the data dir) |
| `--email` | `string` | — | superadmin email (alternative to --id) |
| `--engine` | `string` | `sqlite` | store engine: sqlite or postgres |
| `--id` | `string` | — | superadmin user id (see `superadmin status`) |
| `--reason` | `string` | — | REQUIRED: why this privileged operation is being performed (recorded in the audit ledger) |

#### Command: olivares superadmin enable

Re-enable a previously disabled internal superadmin

```
olivares superadmin enable
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--actor` | `string` | — | REQUIRED: who is performing this privileged operation (an operator or service identity; recorded in the audit ledger) |
| `--data-dir` | `string` | — | data directory (default $OLIVARES_DATA_DIR, an existing ./olivares-data, else $XDG_DATA_HOME/olivares or ~/.local/share/olivares) |
| `--dsn` | `string` | — | store DSN (default a SQLite file in the data dir) |
| `--email` | `string` | — | superadmin email (alternative to --id) |
| `--engine` | `string` | `sqlite` | store engine: sqlite or postgres |
| `--id` | `string` | — | superadmin user id (see `superadmin status`) |
| `--reason` | `string` | — | REQUIRED: why this privileged operation is being performed (recorded in the audit ledger) |

#### Command: olivares superadmin status

List internal superadmin accounts and their active/inactive status

```
olivares superadmin status
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--data-dir` | `string` | — | data directory (default $OLIVARES_DATA_DIR, an existing ./olivares-data, else $XDG_DATA_HOME/olivares or ~/.local/share/olivares) |
| `--dsn` | `string` | — | store DSN (default a SQLite file in the data dir) |
| `--engine` | `string` | `sqlite` | store engine: sqlite or postgres |

#### Command: olivares support

Collect redacted diagnostics for support and incident response

```
olivares support
```

Declares no flags of its own; it takes those of [`olivares`](#command-olivares) and the root command.

#### Command: olivares support bundle

Build a redacted diagnostic tarball with an integrity manifest

```
olivares support bundle
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--ca-cert` | `string` | — | PEM CA bundle used to verify the control plane |
| `--config` | `string` | `/etc/olivares/olivares.env` | effective systemd env file to redact |
| `--data-dir` | `string` | — | data directory (default $OLIVARES_DATA_DIR, an existing ./olivares-data, else $XDG_DATA_HOME/olivares or ~/.local/share/olivares) |
| `--dr-bundle` | `stringArray` | `[]` | DR bundle whose non-secret manifest to include (repeatable) |
| `--dsn` | `string` | — | store DSN (default a SQLite file in the data dir) |
| `--engine` | `string` | `sqlite` | store engine: sqlite or postgres |
| `--exclude` | `stringSlice` | `[]` | sections to exclude after --include selection |
| `--include` | `stringSlice` | `[]` | sections to include: config,status,logs,manifests,verify,secrets (default all) |
| `--insecure` | `bool` | `false` | skip TLS certificate verification for the status request |
| `--journal` | `bool` | `false` | collect journalctl output for the olivares unit |
| `--logs` | `string` | — | engine log file to redact line by line |
| `--offline` | `bool` | `false` | skip the live GET /status request |
| `--out` | `string` | — | output tar.gz path (default olivares-support-&lt;UTC timestamp&gt;.tar.gz) |
| `--pin-sha256` | `stringArray` | `[]` | pinned leaf SPKI SHA-256, base64 or hex (repeatable) — the engine prints it as pin_sha256 on the line reporting its certificate |
| `--server` | `string` | — | control-plane base URL (default $OLIVARES_SERVER_URL) |
| `--since` | `string` | `24 hours ago` | journalctl --since value (used with --journal) |
| `--timeout` | `duration` | `10s` | status request timeout |
| `--verify-report` | `stringArray` | `[]` | JSON output from audit verify or dr.RestoreVerify to redact and include (repeatable) |

#### Command: olivares tenants

Create, list, suspend and delete tenants (superadmin)

```
olivares tenants
```

Aliases: `orgs`

| Flag | Type | Default | Description |
|---|---|---|---|
| `--allow-cleartext` | `bool` | `false` | **inherited**. allow sending the credential to a non-loopback host over plain HTTP (DANGEROUS: it travels readable) |
| `--ca-cert` | `string` | — | **inherited**. PEM file containing an additional trusted root CA (default: current context) |
| `--insecure` | `bool` | `false` | **inherited**. skip TLS certificate verification (DANGEROUS; development only) |
| `--pin-sha256` | `stringArray` | `[]` | **inherited**. trusted leaf SPKI SHA-256 pin, base64 or hex, repeatable — the engine prints it as pin_sha256 on the line reporting its certificate (default: current context) |
| `--server` | `string` | — | **inherited**. control-plane base URL (default $OLIVARES_SERVER_URL, then current context) |
| `--tenant` | `string` | — | **inherited**. tenant id (default $OLIVARES_TENANT, then current context) |
| `--timeout` | `duration` | `10s` | **inherited**. request timeout |
| `--token` | `string` | — | **inherited**. API bearer token (prefer --token-file: this form is visible in the process table and in shell history; default $OLIVARES_TOKEN, then current context) |
| `--token-file` | `string` | — | **inherited**. read the API bearer token from a file, or - for stdin |

#### Command: olivares tenants create

Create a tenant

```
olivares tenants create
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--name` | `string` | — | human name of the organization (required) |
| `--region` | `string` | — | residency region to pin the tenant to (default: unpinned) |
| `--slug` | `string` | — | unique URL-safe handle (default: derived from the name) |

#### Command: olivares tenants ls

List the tenants this installation serves

```
olivares tenants ls
```

Aliases: `list`

Declares no flags of its own; it takes those of [`olivares tenants`](#command-olivares-tenants) and the root command.

#### Command: olivares tenants rm

Delete a tenant and everything in it — unrecoverable

```
olivares tenants rm <tenant-id>
```

Aliases: `delete`, `remove`

| Flag | Type | Default | Description |
|---|---|---|---|
| `-y`, `--yes` | `bool` | `false` | proceed without the confirmation prompt (required in a non-interactive session) |

#### Command: olivares tenants set-region

Pin or clear a tenant's data-residency region (requires an AAL3 session)

```
olivares tenants set-region <tenant-id>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--clear` | `bool` | `false` | remove the tenant's residency pin instead of setting one |
| `--region` | `string` | — | residency region to pin the tenant to |
| `-y`, `--yes` | `bool` | `false` | proceed without the confirmation prompt (required in a non-interactive session) |

#### Command: olivares tenants set-status

Withdraw or restore a tenant's service without deleting anything

```
olivares tenants set-status <tenant-id>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--status` | `string` | — | active or suspended (required) |
| `-y`, `--yes` | `bool` | `false` | proceed without the confirmation prompt (required in a non-interactive session) |

#### Command: olivares threatintel

Hidden diagnostic: it does not appear in `--help` output and is not part of the supported surface.

Manage the AI threat-intel catalog and its signed catalog releases (enterprise add-on)

```
olivares threatintel
```

Declares no flags of its own; it takes those of [`olivares`](#command-olivares) and the root command.

#### Command: olivares threatintel apply

Verify and apply a signed catalog release (fail-closed, anti-rollback); persists it for the engine

```
olivares threatintel apply <catalog-file>
```

Declares no flags of its own; it takes those of [`olivares threatintel`](#command-olivares-threatintel) and the root command.

#### Command: olivares threatintel pull

Pull the catalog release from the configured endpoint, then verify and apply it (fail-closed)

```
olivares threatintel pull
```

Declares no flags of its own; it takes those of [`olivares threatintel`](#command-olivares-threatintel) and the root command.

#### Command: olivares threatintel sign

Sign an unsigned catalog envelope (publisher side; key minted with `olivares license keygen`)

```
olivares threatintel sign
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--in` | `string` | `-` | unsigned feed envelope JSON file ("-" = stdin) |
| `--key` | `string` | — | base64-std Ed25519 private key file (else $OLIVARES_THREATINTEL_SIGNING_KEY) |
| `--out` | `string` | `-` | signed feed output file ("-" = stdout) |

#### Command: olivares threatintel status

Show the active catalog release (versions, expiry, channels) and the governance crosswalk summary

```
olivares threatintel status
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--crosswalk` | `bool` | `false` | print the Claude/Anthropic governance crosswalk instead of the feed status |

#### Command: olivares threatintel verify

Verify a signed catalog release (signature + expiry + schema); does not apply it

```
olivares threatintel verify <catalog-file>
```

Declares no flags of its own; it takes those of [`olivares threatintel`](#command-olivares-threatintel) and the root command.

#### Command: olivares tokens

Issue, list, rotate and revoke API tokens (the credential a script authenticates with)

```
olivares tokens
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--allow-cleartext` | `bool` | `false` | **inherited**. allow sending the credential to a non-loopback host over plain HTTP (DANGEROUS: it travels readable) |
| `--ca-cert` | `string` | — | **inherited**. PEM file containing an additional trusted root CA (default: current context) |
| `--insecure` | `bool` | `false` | **inherited**. skip TLS certificate verification (DANGEROUS; development only) |
| `--pin-sha256` | `stringArray` | `[]` | **inherited**. trusted leaf SPKI SHA-256 pin, base64 or hex, repeatable — the engine prints it as pin_sha256 on the line reporting its certificate (default: current context) |
| `--server` | `string` | — | **inherited**. control-plane base URL (default $OLIVARES_SERVER_URL, then current context) |
| `--tenant` | `string` | — | **inherited**. tenant id (default $OLIVARES_TENANT, then current context) |
| `--timeout` | `duration` | `10s` | **inherited**. request timeout |
| `--token` | `string` | — | **inherited**. API bearer token (prefer --token-file: this form is visible in the process table and in shell history; default $OLIVARES_TOKEN, then current context) |
| `--token-file` | `string` | — | **inherited**. read the API bearer token from a file, or - for stdin |

#### Command: olivares tokens issue

Issue an API token and print its secret ONCE

```
olivares tokens issue
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--name` | `string` | — | human label for the token (required; shown in `tokens ls`) |
| `--role` | `string` | `viewer` | role the bound token carries: viewer, editor, admin or owner |
| `--superadmin` | `bool` | `false` | mint a CROSS-TENANT superadmin token instead of a tenant-bound one (superadmin callers only) |

#### Command: olivares tokens ls

List the API tokens the caller may see

```
olivares tokens ls
```

Aliases: `list`

| Flag | Type | Default | Description |
|---|---|---|---|
| `--cursor` | `string` | — | continue from the cursor a previous page reported |
| `--include-revoked` | `bool` | `false` | also list tokens that have been revoked |
| `--limit` | `int` | `0` | server-side page size (0 = the engine's default) |

#### Command: olivares tokens revoke

Revoke an API token

```
olivares tokens revoke <token-id>
```

Aliases: `delete`, `rm`

| Flag | Type | Default | Description |
|---|---|---|---|
| `-y`, `--yes` | `bool` | `false` | proceed without the confirmation prompt (required in a non-interactive session) |

#### Command: olivares tokens rotate

Rotate an API token: issue a replacement with the same spec and revoke the old one

```
olivares tokens rotate <token-id>
```

Declares no flags of its own; it takes those of [`olivares tokens`](#command-olivares-tokens) and the root command.

#### Command: olivares upgrade

Upgrade this binary in place to a newer signed release (verified, atomic, reversible)

```
olivares upgrade
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--arch` | `string` | `amd64` | target architecture to download for |
| `--bundle` | `string` | — | install from a local air-gap bundle directory or .tar.gz (no network at all; installing needs a live installed license, verified offline; --check does not) |
| `--channel` | `string` | `stable` | release channel: stable \| security (lts is accepted by the validator, but no lts line is published) |
| `--check` | `bool` | `false` | show the upgrade plan (current -&gt; available, channel, CVEs) without swapping |
| `--current-version` | `string` | — | declare the version installed at --target when it cannot be probed (cross-arch staging, a noexec mount, or a build from source); keeps anti-rollback and min_version armed instead of guessing |
| `--data-dir` | `string` | — | data directory (license + install-id) (default $OLIVARES_DATA_DIR, an existing ./olivares-data, else $XDG_DATA_HOME/olivares or ~/.local/share/olivares) |
| `--endpoint` | `string` | — | update channel source: a GitHub repository (https://github.com/&lt;owner&gt;/&lt;repo&gt;), one of its releases (…/releases/tag/&lt;tag&gt;), or a static mirror base (&lt;base&gt;/&lt;channel&gt;/manifest.json). Default: the public repository's releases; the license worker with --enterprise |
| `--enterprise` | `bool` | `false` | upgrade the licensed enterprise edition (gated download; needs a live license) |
| `--force-rollback` | `bool` | `false` | allow installing an OLDER version than the running one (records an audit entry) |
| `--if-eligible` | `bool` | `false` | only proceed if this node is in the manifest's staged-rollout cohort (used by the timer) |
| `--install-timer` | `bool` | `false` | emit an opt-in systemd timer+service that runs `upgrade --if-eligible` in a maintenance window |
| `--license` | `string` | — | explicit license file path (enterprise; highest precedence) |
| `--os` | `string` | `linux` | target OS to download for |
| `--pubkey` | `string` | — | base64 or @file Ed25519 OTA key to verify against (default: the key embedded in this build) |
| `--target` | `string` | — | binary path to replace (default: the running executable) |
| `--timeout` | `duration` | `5m0s` | overall network timeout |
| `--timer-dir` | `string` | — | write the systemd units to this directory instead of printing them |
| `--timer-schedule` | `string` | `Sun *-*-* 03:00:00` | systemd OnCalendar expression for the auto-check timer |
| `--token` | `string` | — | enterprise download token from your license/fulfillment email |
| `-y`, `--yes` | `bool` | `false` | do not prompt for confirmation before swapping |

#### Command: olivares users

List, create, disable and re-enable the global user accounts (superadmin)

```
olivares users
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--allow-cleartext` | `bool` | `false` | **inherited**. allow sending the credential to a non-loopback host over plain HTTP (DANGEROUS: it travels readable) |
| `--ca-cert` | `string` | — | **inherited**. PEM file containing an additional trusted root CA (default: current context) |
| `--insecure` | `bool` | `false` | **inherited**. skip TLS certificate verification (DANGEROUS; development only) |
| `--pin-sha256` | `stringArray` | `[]` | **inherited**. trusted leaf SPKI SHA-256 pin, base64 or hex, repeatable — the engine prints it as pin_sha256 on the line reporting its certificate (default: current context) |
| `--server` | `string` | — | **inherited**. control-plane base URL (default $OLIVARES_SERVER_URL, then current context) |
| `--tenant` | `string` | — | **inherited**. tenant id (default $OLIVARES_TENANT, then current context) |
| `--timeout` | `duration` | `10s` | **inherited**. request timeout |
| `--token` | `string` | — | **inherited**. API bearer token (prefer --token-file: this form is visible in the process table and in shell history; default $OLIVARES_TOKEN, then current context) |
| `--token-file` | `string` | — | **inherited**. read the API bearer token from a file, or - for stdin |

#### Command: olivares users create

Create a global user account (superadmin)

```
olivares users create
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--display-name` | `string` | — | human name shown in the console and audit ledger |
| `--email` | `string` | — | email address that identifies the account (required) |
| `--password` | `string` | — | initial password (prefer --password-file: this form is visible in the process table) |
| `--password-file` | `string` | — | read the initial password from a file, or - for stdin |
| `--superadmin` | `bool` | `false` | create the account as a cross-tenant superadmin (the engine accepts this only from a superadmin) |

#### Command: olivares users disable

Disable a superadmin account (reversible; requires an AAL3 session)

```
olivares users disable <user-id>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `-y`, `--yes` | `bool` | `false` | proceed without the confirmation prompt (required in a non-interactive session) |

#### Command: olivares users enable

Re-enable a disabled superadmin account (requires an AAL3 session)

```
olivares users enable <user-id>
```

Declares no flags of its own; it takes those of [`olivares users`](#command-olivares-users) and the root command.

#### Command: olivares users ls

List the global user accounts

```
olivares users ls
```

Aliases: `list`

| Flag | Type | Default | Description |
|---|---|---|---|
| `--cursor` | `string` | — | continue from the cursor a previous page reported |
| `--limit` | `int` | `0` | server-side page size (0 = the engine's default) |

#### Command: olivares users superadmins

List the superadmin accounts and whether each is active

```
olivares users superadmins
```

Declares no flags of its own; it takes those of [`olivares users`](#command-olivares-users) and the root command.

#### Command: olivares version

Print the olivares version, build metadata and FIPS 140-3 mode

```
olivares version
```

Declares no flags of its own; it takes those of [`olivares`](#command-olivares) and the root command.

#### Command: olivares voice

Inspect governed voice sessions and set the per-agent voice policy

```
olivares voice
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--allow-cleartext` | `bool` | `false` | **inherited**. allow sending the credential to a non-loopback host over plain HTTP (DANGEROUS: it travels readable) |
| `--ca-cert` | `string` | — | **inherited**. PEM file containing an additional trusted root CA (default: current context) |
| `--insecure` | `bool` | `false` | **inherited**. skip TLS certificate verification (DANGEROUS; development only) |
| `--pin-sha256` | `stringArray` | `[]` | **inherited**. trusted leaf SPKI SHA-256 pin, base64 or hex, repeatable — the engine prints it as pin_sha256 on the line reporting its certificate (default: current context) |
| `--server` | `string` | — | **inherited**. control-plane base URL (default $OLIVARES_SERVER_URL, then current context) |
| `--tenant` | `string` | — | **inherited**. tenant id (default $OLIVARES_TENANT, then current context) |
| `--timeout` | `duration` | `10s` | **inherited**. request timeout |
| `--token` | `string` | — | **inherited**. API bearer token (prefer --token-file: this form is visible in the process table and in shell history; default $OLIVARES_TOKEN, then current context) |
| `--token-file` | `string` | — | **inherited**. read the API bearer token from a file, or - for stdin |

#### Command: olivares voice decisions

List the append-only voice decision ledger for the tenant

```
olivares voice decisions
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--cursor` | `string` | — | continue from the cursor a previous page reported |
| `--limit` | `int` | `0` | page size (0 uses the engine's default) |

#### Command: olivares voice policies

Read and replace the per-agent voice policy

```
olivares voice policies
```

Declares no flags of its own; it takes those of [`olivares voice`](#command-olivares-voice) and the root command.

#### Command: olivares voice policies ls

List the voice policies in force

```
olivares voice policies ls
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--cursor` | `string` | — | continue from the cursor a previous page reported |
| `--limit` | `int` | `0` | page size (0 uses the engine's default) |

#### Command: olivares voice policies set

Replace one agent's voice policy (PUT — the whole policy)

```
olivares voice policies set
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--agent-ref` | `string` | — | **required**. the agent this policy governs (required) |
| `--allowed-model-ref` | `string` | — | **required**. the only model this agent may speak through (required) |
| `--allowed-provider-ref` | `string` | — | **required**. the only provider this agent may speak through (required) |
| `--calls-file` | `string` | — | JSON call-policy object, '-' for stdin |
| `--max-latency-ms` | `int64` | `0` | tolerated latency in milliseconds (0 = no limit) |
| `--max-session-minutes` | `int64` | `0` | cap a session's length in minutes (0 = no cap) |

#### Command: olivares voice sessions

List, follow and open governed voice sessions

```
olivares voice sessions
```

Declares no flags of its own; it takes those of [`olivares voice`](#command-olivares-voice) and the root command.

#### Command: olivares voice sessions decisions

List one session's governance decisions

```
olivares voice sessions decisions <session-ref>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--cursor` | `string` | — | continue from the cursor a previous page reported |
| `--limit` | `int` | `0` | page size (0 uses the engine's default) |

#### Command: olivares voice sessions get

Show one voice session

```
olivares voice sessions get <session-ref>
```

Declares no flags of its own; it takes those of [`olivares voice sessions`](#command-olivares-voice-sessions) and the root command.

#### Command: olivares voice sessions ls

List voice sessions with their derived state

```
olivares voice sessions ls
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--cursor` | `string` | — | continue from the cursor a previous page reported |
| `--limit` | `int` | `0` | page size (0 uses the engine's default) |

#### Command: olivares voice sessions open

Open a governed voice session through the approval gate (two-phase)

```
olivares voice sessions open
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--agent-ref` | `string` | — | **required**. the agent that will speak (required) |
| `--approval-ref` | `string` | — | phase 2: the approval that authorizes this open |
| `--model-ref` | `string` | — | **required**. the model requested for this session (required) |
| `--provider-ref` | `string` | — | **required**. the provider requested for this session (required) |
| `--session-ref` | `string` | — | **required**. the session reference to open (required) |

#### Command: olivares voice sessions stream

Follow one live voice session as NDJSON (one object per event)

```
olivares voice sessions stream <session-ref>
```

Declares no flags of its own; it takes those of [`olivares voice sessions`](#command-olivares-voice-sessions) and the root command.

#### Command: olivares webui-files

Hidden diagnostic: it does not appear in `--help` output and is not part of the supported surface.

List the web UI assets embedded in this binary (diagnostic)

```
olivares webui-files
```

Declares no flags of its own; it takes those of [`olivares`](#command-olivares) and the root command.

#### Command: olivares work

Manage durable cross-session work, leases, decisions, and acceptance

```
olivares work
```

Declares no flags of its own; it takes those of [`olivares`](#command-olivares) and the root command.

#### Command: olivares work apply

Apply one validated work command idempotently

```
olivares work apply <command>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--authority-ref` | `string` | — | WorkCommand authority_ref |
| `--blocked-code` | `string` | — | WorkCommand blocked_code |
| `--blocked-reason` | `string` | — | WorkCommand blocked_reason |
| `--brief` | `string` | — | WorkCommand brief_md |
| `--ca-cert` | `string` | — | PEM CA bundle used to verify the control plane (default: the active client context) |
| `--changes-requested` | `bool` | `false` | WorkCommand changes_requested |
| `--code` | `string` | — | WorkCommand code |
| `--criterion-id` | `string` | — | WorkCommand criterion_id |
| `--criterion-key` | `string` | — | WorkCommand criterion_key |
| `--decision-id` | `string` | — | WorkCommand decision_id |
| `--decision-key` | `string` | — | WorkCommand decision_key |
| `--dependency-id` | `string` | — | WorkCommand dependency_id |
| `--depends-on-id` | `string` | — | WorkCommand depends_on_id |
| `--due-at` | `string` | — | WorkCommand due_at |
| `--evidence-hash` | `string` | — | WorkCommand evidence_hash |
| `--evidence-ref` | `string` | — | WorkCommand evidence_ref |
| `--fence` | `int64` | `0` | WorkCommand fence |
| `--field` | `stringArray` | `[]` | additional WorkCommand field as key=JSON (repeatable) |
| `-f`, `--file` | `string` | — | YAML or JSON WorkCommand file ('-' reads stdin; exactly one document) |
| `--force` | `bool` | `false` | WorkCommand force |
| `--holder-agent-ref` | `string` | — | WorkCommand holder_agent_ref |
| `--holder-run-ref` | `string` | — | WorkCommand holder_run_ref |
| `--holder-sid` | `string` | — | WorkCommand holder_sid |
| `--idempotency-key` | `string` | — | UUID reused for an unambiguous retry (generated and printed when omitted) |
| `--insecure` | `bool` | `false` | skip TLS certificate verification (self-signed dev planes only) |
| `--json` | `bool` | `false` | deprecated alias for -o json |
| `--ordinal` | `int` | `0` | acceptance criterion display order |
| `--owner-kind` | `string` | — | WorkCommand owner_kind |
| `--owner-ref` | `string` | — | WorkCommand owner_ref |
| `--parent-id` | `string` | — | WorkCommand parent_id |
| `--pin-sha256` | `stringArray` | `[]` | pinned leaf SPKI SHA-256, base64 or hex (repeatable) — the engine prints it as pin_sha256 on the line reporting its certificate; default: the active client context |
| `--plan` | `string` | — | replay a work-plan artifact instead of -f or inline fields |
| `--plan-hash` | `string` | — | bind the request to this plan hash |
| `--priority` | `string` | — | WorkCommand priority |
| `--provenance-hash` | `string` | — | WorkCommand provenance_hash |
| `--provenance-kind` | `string` | — | WorkCommand provenance_kind |
| `--provenance-ref` | `string` | — | WorkCommand provenance_ref |
| `--rationale` | `string` | — | WorkCommand rationale_md |
| `--reason` | `string` | — | WorkCommand reason |
| `--required` | `bool` | `false` | make an acceptance criterion required |
| `--server` | `string` | — | control-plane base URL (default $OLIVARES_SERVER_URL or the active client context) |
| `--state` | `string` | — | WorkCommand state |
| `--statement` | `string` | — | WorkCommand statement |
| `--statement-md` | `string` | — | WorkCommand statement_md |
| `--subject-kind` | `string` | — | WorkCommand subject_kind |
| `--subject-ref` | `string` | — | WorkCommand subject_ref |
| `--supersedes-id` | `string` | — | WorkCommand supersedes_id |
| `--target-id` | `string` | — | WorkCommand target_id |
| `--tenant` | `string` | — | tenant id (default $OLIVARES_TENANT or the active client context) |
| `--terminal-code` | `string` | — | WorkCommand terminal_code |
| `--terminal-reason` | `string` | — | WorkCommand terminal_reason |
| `--timeout` | `duration` | `30s` | request timeout |
| `--title` | `string` | — | WorkCommand title |
| `--token` | `string` | — | API bearer token (default $OLIVARES_TOKEN or the active client context) |
| `--transition` | `string` | — | WorkCommand transition |
| `--ttl-seconds` | `int64` | `0` | WorkCommand ttl_seconds |
| `--unblock` | `bool` | `false` | WorkCommand unblock |
| `--version` | `uint64` | `0` | expected resource version N (sent as strong If-Match "vN") |
| `--waiver-decision-id` | `string` | — | WorkCommand waiver_decision_id |
| `--work-item-id` | `string` | — | WorkCommand work_item_id |
| `--work-kind` | `string` | — | WorkCommand work_kind |
| `--workspace-id` | `string` | — | WorkCommand workspace_id |

#### Command: olivares work get

Get one durable work item, decision, or lease

```
olivares work get item|decision|lease <id>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--ca-cert` | `string` | — | PEM CA bundle used to verify the control plane (default: the active client context) |
| `--insecure` | `bool` | `false` | skip TLS certificate verification (self-signed dev planes only) |
| `--json` | `bool` | `false` | deprecated alias for -o json |
| `--pin-sha256` | `stringArray` | `[]` | pinned leaf SPKI SHA-256, base64 or hex (repeatable) — the engine prints it as pin_sha256 on the line reporting its certificate; default: the active client context |
| `--server` | `string` | — | control-plane base URL (default $OLIVARES_SERVER_URL or the active client context) |
| `--tenant` | `string` | — | tenant id (default $OLIVARES_TENANT or the active client context) |
| `--timeout` | `duration` | `30s` | request timeout |
| `--token` | `string` | — | API bearer token (default $OLIVARES_TOKEN or the active client context) |

#### Command: olivares work list

List durable work items, decisions, or leases with keyset pagination

```
olivares work list items|decisions|leases
```

Aliases: `ls`

| Flag | Type | Default | Description |
|---|---|---|---|
| `--actor-kind` | `string` | — | filter by actor kind |
| `--actor-ref` | `string` | — | filter by actor ref |
| `--archived` | `bool` | `false` | filter work items by archived state |
| `--ca-cert` | `string` | — | PEM CA bundle used to verify the control plane (default: the active client context) |
| `--cursor` | `string` | — | opaque UUIDv7 keyset cursor |
| `--decision-key` | `string` | — | filter by decision key |
| `--due-before` | `string` | — | filter by due before |
| `--effective` | `bool` | `false` | filter decisions by effective head state |
| `--expires-before` | `string` | — | filter by expires before |
| `--filter` | `stringArray` | `[]` | additional allowlisted filter as key=value (repeatable) |
| `--holder-sid` | `string` | — | filter by holder sid |
| `--insecure` | `bool` | `false` | skip TLS certificate verification (self-signed dev planes only) |
| `--json` | `bool` | `false` | deprecated alias for -o json |
| `--limit` | `int` | `100` | page size (1..200) |
| `--owner-kind` | `string` | — | filter by owner kind |
| `--owner-ref` | `string` | — | filter by owner ref |
| `--parent-id` | `string` | — | filter by parent id |
| `--pin-sha256` | `stringArray` | `[]` | pinned leaf SPKI SHA-256, base64 or hex (repeatable) — the engine prints it as pin_sha256 on the line reporting its certificate; default: the active client context |
| `--priority` | `string` | — | filter by priority |
| `--provenance-kind` | `string` | — | filter by provenance kind |
| `--provenance-ref` | `string` | — | filter by provenance ref |
| `--revoked` | `bool` | `false` | filter decisions by revoked head state |
| `--server` | `string` | — | control-plane base URL (default $OLIVARES_SERVER_URL or the active client context) |
| `--state` | `string` | — | filter by state |
| `--status` | `string` | — | filter by status |
| `--subject-kind` | `string` | — | filter by subject kind |
| `--subject-ref` | `string` | — | filter by subject ref |
| `--tenant` | `string` | — | tenant id (default $OLIVARES_TENANT or the active client context) |
| `--timeout` | `duration` | `30s` | request timeout |
| `--token` | `string` | — | API bearer token (default $OLIVARES_TOKEN or the active client context) |
| `--updated-after` | `string` | — | filter by updated after |
| `--work-item-id` | `string` | — | filter by work item id |
| `--work-kind` | `string` | — | filter by work kind |

#### Command: olivares work plan

Plan one work command and its expected durable effects without writing

```
olivares work plan <command>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--authority-ref` | `string` | — | WorkCommand authority_ref |
| `--blocked-code` | `string` | — | WorkCommand blocked_code |
| `--blocked-reason` | `string` | — | WorkCommand blocked_reason |
| `--brief` | `string` | — | WorkCommand brief_md |
| `--ca-cert` | `string` | — | PEM CA bundle used to verify the control plane (default: the active client context) |
| `--changes-requested` | `bool` | `false` | WorkCommand changes_requested |
| `--code` | `string` | — | WorkCommand code |
| `--criterion-id` | `string` | — | WorkCommand criterion_id |
| `--criterion-key` | `string` | — | WorkCommand criterion_key |
| `--decision-id` | `string` | — | WorkCommand decision_id |
| `--decision-key` | `string` | — | WorkCommand decision_key |
| `--dependency-id` | `string` | — | WorkCommand dependency_id |
| `--depends-on-id` | `string` | — | WorkCommand depends_on_id |
| `--due-at` | `string` | — | WorkCommand due_at |
| `--evidence-hash` | `string` | — | WorkCommand evidence_hash |
| `--evidence-ref` | `string` | — | WorkCommand evidence_ref |
| `--fence` | `int64` | `0` | WorkCommand fence |
| `--field` | `stringArray` | `[]` | additional WorkCommand field as key=JSON (repeatable) |
| `-f`, `--file` | `string` | — | YAML or JSON WorkCommand file ('-' reads stdin; exactly one document) |
| `--force` | `bool` | `false` | WorkCommand force |
| `--holder-agent-ref` | `string` | — | WorkCommand holder_agent_ref |
| `--holder-run-ref` | `string` | — | WorkCommand holder_run_ref |
| `--holder-sid` | `string` | — | WorkCommand holder_sid |
| `--insecure` | `bool` | `false` | skip TLS certificate verification (self-signed dev planes only) |
| `--json` | `bool` | `false` | deprecated alias for -o json |
| `--ordinal` | `int` | `0` | acceptance criterion display order |
| `--out` | `string` | — | atomically write a reusable 0600 work-plan artifact |
| `--owner-kind` | `string` | — | WorkCommand owner_kind |
| `--owner-ref` | `string` | — | WorkCommand owner_ref |
| `--parent-id` | `string` | — | WorkCommand parent_id |
| `--pin-sha256` | `stringArray` | `[]` | pinned leaf SPKI SHA-256, base64 or hex (repeatable) — the engine prints it as pin_sha256 on the line reporting its certificate; default: the active client context |
| `--plan-hash` | `string` | — | bind the request to this plan hash |
| `--priority` | `string` | — | WorkCommand priority |
| `--provenance-hash` | `string` | — | WorkCommand provenance_hash |
| `--provenance-kind` | `string` | — | WorkCommand provenance_kind |
| `--provenance-ref` | `string` | — | WorkCommand provenance_ref |
| `--rationale` | `string` | — | WorkCommand rationale_md |
| `--reason` | `string` | — | WorkCommand reason |
| `--required` | `bool` | `false` | make an acceptance criterion required |
| `--server` | `string` | — | control-plane base URL (default $OLIVARES_SERVER_URL or the active client context) |
| `--state` | `string` | — | WorkCommand state |
| `--statement` | `string` | — | WorkCommand statement |
| `--statement-md` | `string` | — | WorkCommand statement_md |
| `--subject-kind` | `string` | — | WorkCommand subject_kind |
| `--subject-ref` | `string` | — | WorkCommand subject_ref |
| `--supersedes-id` | `string` | — | WorkCommand supersedes_id |
| `--target-id` | `string` | — | WorkCommand target_id |
| `--tenant` | `string` | — | tenant id (default $OLIVARES_TENANT or the active client context) |
| `--terminal-code` | `string` | — | WorkCommand terminal_code |
| `--terminal-reason` | `string` | — | WorkCommand terminal_reason |
| `--timeout` | `duration` | `30s` | request timeout |
| `--title` | `string` | — | WorkCommand title |
| `--token` | `string` | — | API bearer token (default $OLIVARES_TOKEN or the active client context) |
| `--transition` | `string` | — | WorkCommand transition |
| `--ttl-seconds` | `int64` | `0` | WorkCommand ttl_seconds |
| `--unblock` | `bool` | `false` | WorkCommand unblock |
| `--version` | `uint64` | `0` | expected resource version N (sent as strong If-Match "vN") |
| `--waiver-decision-id` | `string` | — | WorkCommand waiver_decision_id |
| `--work-item-id` | `string` | — | WorkCommand work_item_id |
| `--work-kind` | `string` | — | WorkCommand work_kind |
| `--workspace-id` | `string` | — | WorkCommand workspace_id |

#### Command: olivares work protocol-binding

Compose and reconcile durable A2A and MCP protocol bindings

```
olivares work protocol-binding
```

Declares no flags of its own; it takes those of [`olivares work`](#command-olivares-work) and the root command.

#### Command: olivares work protocol-binding binding

Inspect and reconcile durable protocol bindings

```
olivares work protocol-binding binding
```

Declares no flags of its own; it takes those of [`olivares work protocol-binding`](#command-olivares-work-protocol-binding) and the root command.

#### Command: olivares work protocol-binding binding get

Get one durable protocol binding generation

```
olivares work protocol-binding binding get <binding-id>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--ca-cert` | `string` | — | PEM CA bundle used to verify the control plane (default: the active client context) |
| `--insecure` | `bool` | `false` | skip TLS certificate verification (self-signed dev planes only) |
| `--json` | `bool` | `false` | deprecated alias for -o json |
| `--pin-sha256` | `stringArray` | `[]` | pinned leaf SPKI SHA-256, base64 or hex (repeatable) — the engine prints it as pin_sha256 on the line reporting its certificate; default: the active client context |
| `--server` | `string` | — | control-plane base URL (default $OLIVARES_SERVER_URL or the active client context) |
| `--tenant` | `string` | — | tenant id (default $OLIVARES_TENANT or the active client context) |
| `--timeout` | `duration` | `30s` | request timeout |
| `--token` | `string` | — | API bearer token (default $OLIVARES_TOKEN or the active client context) |

#### Command: olivares work protocol-binding binding list

List durable protocol bindings in one workspace

```
olivares work protocol-binding binding list
```

Aliases: `ls`

| Flag | Type | Default | Description |
|---|---|---|---|
| `--binding-spec-id` | `string` | — | exact binding specification UUID |
| `--ca-cert` | `string` | — | PEM CA bundle used to verify the control plane (default: the active client context) |
| `--cursor` | `string` | — | opaque keyset cursor |
| `--external-id` | `string` | — | remote resource ID |
| `--external-kind` | `string` | — | remote resource kind |
| `--insecure` | `bool` | `false` | skip TLS certificate verification (self-signed dev planes only) |
| `--json` | `bool` | `false` | deprecated alias for -o json |
| `--limit` | `int` | `0` | page size |
| `--owner-kind` | `string` | — | binding owner kind |
| `--owner-ref` | `string` | — | binding owner reference |
| `--peer-authority` | `string` | — | canonical peer authority |
| `--pin-sha256` | `stringArray` | `[]` | pinned leaf SPKI SHA-256, base64 or hex (repeatable) — the engine prints it as pin_sha256 on the line reporting its certificate; default: the active client context |
| `--protocol` | `string` | — | protocol: a2a or mcp |
| `--server` | `string` | — | control-plane base URL (default $OLIVARES_SERVER_URL or the active client context) |
| `--tenant` | `string` | — | tenant id (default $OLIVARES_TENANT or the active client context) |
| `--terminal` | `string` | — | terminal filter: true or false |
| `--timeout` | `duration` | `30s` | request timeout |
| `--token` | `string` | — | API bearer token (default $OLIVARES_TOKEN or the active client context) |
| `--verdict` | `string` | — | observation verdict |
| `--work-item-id` | `string` | — | exact work item UUID |
| `--workspace-id` | `string` | — | workspace UUID (optional for a confined principal) |

#### Command: olivares work protocol-binding binding reconcile

Validate, plan, test, or apply one exact-generation remote observation

```
olivares work protocol-binding binding reconcile <binding-id>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--ca-cert` | `string` | — | PEM CA bundle used to verify the control plane (default: the active client context) |
| `--idempotency-key` | `string` | — | UUID reused for an exact apply retry |
| `--insecure` | `bool` | `false` | skip TLS certificate verification (self-signed dev planes only) |
| `--json` | `bool` | `false` | deprecated alias for -o json |
| `--mode` | `string` | `test` | operation phase: validate, plan, test, or apply |
| `--pin-sha256` | `stringArray` | `[]` | pinned leaf SPKI SHA-256, base64 or hex (repeatable) — the engine prints it as pin_sha256 on the line reporting its certificate; default: the active client context |
| `--plan-hash` | `string` | — | SHA-256 plan hash required by apply |
| `--server` | `string` | — | control-plane base URL (default $OLIVARES_SERVER_URL or the active client context) |
| `--tenant` | `string` | — | tenant id (default $OLIVARES_TENANT or the active client context) |
| `--timeout` | `duration` | `30s` | request timeout |
| `--token` | `string` | — | API bearer token (default $OLIVARES_TOKEN or the active client context) |
| `--version` | `uint64` | `0` | expected resource version N |

#### Command: olivares work protocol-binding spec

Manage immutable protocol binding specifications

```
olivares work protocol-binding spec
```

Declares no flags of its own; it takes those of [`olivares work protocol-binding`](#command-olivares-work-protocol-binding) and the root command.

#### Command: olivares work protocol-binding spec activate

Activate one protocol binding spec generation

```
olivares work protocol-binding spec activate <spec-id>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--ca-cert` | `string` | — | PEM CA bundle used to verify the control plane (default: the active client context) |
| `--idempotency-key` | `string` | — | UUID reused for an exact apply retry |
| `--insecure` | `bool` | `false` | skip TLS certificate verification (self-signed dev planes only) |
| `--json` | `bool` | `false` | deprecated alias for -o json |
| `--mode` | `string` | `plan` | operation phase: validate, plan, test, or apply |
| `--pin-sha256` | `stringArray` | `[]` | pinned leaf SPKI SHA-256, base64 or hex (repeatable) — the engine prints it as pin_sha256 on the line reporting its certificate; default: the active client context |
| `--plan-hash` | `string` | — | SHA-256 plan hash required by apply |
| `--server` | `string` | — | control-plane base URL (default $OLIVARES_SERVER_URL or the active client context) |
| `--tenant` | `string` | — | tenant id (default $OLIVARES_TENANT or the active client context) |
| `--timeout` | `duration` | `30s` | request timeout |
| `--token` | `string` | — | API bearer token (default $OLIVARES_TOKEN or the active client context) |
| `--version` | `uint64` | `0` | expected resource version N |

#### Command: olivares work protocol-binding spec create

Validate, plan, or create one draft protocol binding spec

```
olivares work protocol-binding spec create
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--ca-cert` | `string` | — | PEM CA bundle used to verify the control plane (default: the active client context) |
| `-f`, `--file` | `string` | — | YAML or JSON protocol binding spec ('-' reads stdin) |
| `--idempotency-key` | `string` | — | UUID reused for an exact apply retry |
| `--insecure` | `bool` | `false` | skip TLS certificate verification (self-signed dev planes only) |
| `--json` | `bool` | `false` | deprecated alias for -o json |
| `--mode` | `string` | `plan` | operation phase: validate, plan, test, or apply |
| `--pin-sha256` | `stringArray` | `[]` | pinned leaf SPKI SHA-256, base64 or hex (repeatable) — the engine prints it as pin_sha256 on the line reporting its certificate; default: the active client context |
| `--plan-hash` | `string` | — | SHA-256 plan hash required by apply |
| `--server` | `string` | — | control-plane base URL (default $OLIVARES_SERVER_URL or the active client context) |
| `--tenant` | `string` | — | tenant id (default $OLIVARES_TENANT or the active client context) |
| `--timeout` | `duration` | `30s` | request timeout |
| `--token` | `string` | — | API bearer token (default $OLIVARES_TOKEN or the active client context) |

#### Command: olivares work protocol-binding spec disable

Disable one protocol binding spec generation

```
olivares work protocol-binding spec disable <spec-id>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--ca-cert` | `string` | — | PEM CA bundle used to verify the control plane (default: the active client context) |
| `--idempotency-key` | `string` | — | UUID reused for an exact apply retry |
| `--insecure` | `bool` | `false` | skip TLS certificate verification (self-signed dev planes only) |
| `--json` | `bool` | `false` | deprecated alias for -o json |
| `--mode` | `string` | `plan` | operation phase: validate, plan, test, or apply |
| `--pin-sha256` | `stringArray` | `[]` | pinned leaf SPKI SHA-256, base64 or hex (repeatable) — the engine prints it as pin_sha256 on the line reporting its certificate; default: the active client context |
| `--plan-hash` | `string` | — | SHA-256 plan hash required by apply |
| `--server` | `string` | — | control-plane base URL (default $OLIVARES_SERVER_URL or the active client context) |
| `--tenant` | `string` | — | tenant id (default $OLIVARES_TENANT or the active client context) |
| `--timeout` | `duration` | `30s` | request timeout |
| `--token` | `string` | — | API bearer token (default $OLIVARES_TOKEN or the active client context) |
| `--version` | `uint64` | `0` | expected resource version N |

#### Command: olivares work protocol-binding spec get

Get one immutable protocol binding spec generation

```
olivares work protocol-binding spec get <spec-id>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--ca-cert` | `string` | — | PEM CA bundle used to verify the control plane (default: the active client context) |
| `--insecure` | `bool` | `false` | skip TLS certificate verification (self-signed dev planes only) |
| `--json` | `bool` | `false` | deprecated alias for -o json |
| `--pin-sha256` | `stringArray` | `[]` | pinned leaf SPKI SHA-256, base64 or hex (repeatable) — the engine prints it as pin_sha256 on the line reporting its certificate; default: the active client context |
| `--server` | `string` | — | control-plane base URL (default $OLIVARES_SERVER_URL or the active client context) |
| `--tenant` | `string` | — | tenant id (default $OLIVARES_TENANT or the active client context) |
| `--timeout` | `duration` | `30s` | request timeout |
| `--token` | `string` | — | API bearer token (default $OLIVARES_TOKEN or the active client context) |

#### Command: olivares work protocol-binding spec list

List protocol binding spec generations in one workspace

```
olivares work protocol-binding spec list
```

Aliases: `ls`

| Flag | Type | Default | Description |
|---|---|---|---|
| `--binding-key` | `string` | — | stable binding specification key |
| `--ca-cert` | `string` | — | PEM CA bundle used to verify the control plane (default: the active client context) |
| `--cursor` | `string` | — | opaque keyset cursor |
| `--direction` | `string` | — | binding direction |
| `--generation` | `int64` | `0` | exact specification generation |
| `--insecure` | `bool` | `false` | skip TLS certificate verification (self-signed dev planes only) |
| `--json` | `bool` | `false` | deprecated alias for -o json |
| `--limit` | `int` | `0` | page size |
| `--local-kind` | `string` | — | local resource kind |
| `--peer-authority` | `string` | — | canonical peer authority |
| `--pin-sha256` | `stringArray` | `[]` | pinned leaf SPKI SHA-256, base64 or hex (repeatable) — the engine prints it as pin_sha256 on the line reporting its certificate; default: the active client context |
| `--protocol` | `string` | — | protocol: a2a or mcp |
| `--server` | `string` | — | control-plane base URL (default $OLIVARES_SERVER_URL or the active client context) |
| `--state` | `string` | — | specification state |
| `--tenant` | `string` | — | tenant id (default $OLIVARES_TENANT or the active client context) |
| `--timeout` | `duration` | `30s` | request timeout |
| `--token` | `string` | — | API bearer token (default $OLIVARES_TOKEN or the active client context) |
| `--workspace-id` | `string` | — | workspace UUID (optional for a confined principal) |

#### Command: olivares work replay

Replay a dead-lettered durable work event

```
olivares work replay
```

Declares no flags of its own; it takes those of [`olivares work`](#command-olivares-work) and the root command.

#### Command: olivares work replay event

Requeue one dead-lettered WorkEvent under its stable event ID

```
olivares work replay event <event-id>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--ca-cert` | `string` | — | PEM CA bundle used to verify the control plane (default: the active client context) |
| `--idempotency-key` | `string` | — | UUID reused for an exact apply retry |
| `--insecure` | `bool` | `false` | skip TLS certificate verification (self-signed dev planes only) |
| `--json` | `bool` | `false` | deprecated alias for -o json |
| `--mode` | `string` | `apply` | command phase: validate, plan, or apply |
| `--pin-sha256` | `stringArray` | `[]` | pinned leaf SPKI SHA-256, base64 or hex (repeatable) — the engine prints it as pin_sha256 on the line reporting its certificate; default: the active client context |
| `--plan-hash` | `string` | — | required replay plan hash for apply |
| `--server` | `string` | — | control-plane base URL (default $OLIVARES_SERVER_URL or the active client context) |
| `--tenant` | `string` | — | tenant id (default $OLIVARES_TENANT or the active client context) |
| `--timeout` | `duration` | `30s` | request timeout |
| `--token` | `string` | — | API bearer token (default $OLIVARES_TOKEN or the active client context) |
| `--version` | `uint64` | `0` | outbox row version from replay plan ETag (required for apply) |

#### Command: olivares work validate

Validate one work command without writing

```
olivares work validate <command>
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--authority-ref` | `string` | — | WorkCommand authority_ref |
| `--blocked-code` | `string` | — | WorkCommand blocked_code |
| `--blocked-reason` | `string` | — | WorkCommand blocked_reason |
| `--brief` | `string` | — | WorkCommand brief_md |
| `--ca-cert` | `string` | — | PEM CA bundle used to verify the control plane (default: the active client context) |
| `--changes-requested` | `bool` | `false` | WorkCommand changes_requested |
| `--code` | `string` | — | WorkCommand code |
| `--criterion-id` | `string` | — | WorkCommand criterion_id |
| `--criterion-key` | `string` | — | WorkCommand criterion_key |
| `--decision-id` | `string` | — | WorkCommand decision_id |
| `--decision-key` | `string` | — | WorkCommand decision_key |
| `--dependency-id` | `string` | — | WorkCommand dependency_id |
| `--depends-on-id` | `string` | — | WorkCommand depends_on_id |
| `--due-at` | `string` | — | WorkCommand due_at |
| `--evidence-hash` | `string` | — | WorkCommand evidence_hash |
| `--evidence-ref` | `string` | — | WorkCommand evidence_ref |
| `--fence` | `int64` | `0` | WorkCommand fence |
| `--field` | `stringArray` | `[]` | additional WorkCommand field as key=JSON (repeatable) |
| `-f`, `--file` | `string` | — | YAML or JSON WorkCommand file ('-' reads stdin; exactly one document) |
| `--force` | `bool` | `false` | WorkCommand force |
| `--holder-agent-ref` | `string` | — | WorkCommand holder_agent_ref |
| `--holder-run-ref` | `string` | — | WorkCommand holder_run_ref |
| `--holder-sid` | `string` | — | WorkCommand holder_sid |
| `--insecure` | `bool` | `false` | skip TLS certificate verification (self-signed dev planes only) |
| `--json` | `bool` | `false` | deprecated alias for -o json |
| `--ordinal` | `int` | `0` | acceptance criterion display order |
| `--owner-kind` | `string` | — | WorkCommand owner_kind |
| `--owner-ref` | `string` | — | WorkCommand owner_ref |
| `--parent-id` | `string` | — | WorkCommand parent_id |
| `--pin-sha256` | `stringArray` | `[]` | pinned leaf SPKI SHA-256, base64 or hex (repeatable) — the engine prints it as pin_sha256 on the line reporting its certificate; default: the active client context |
| `--plan-hash` | `string` | — | bind the request to this plan hash |
| `--priority` | `string` | — | WorkCommand priority |
| `--provenance-hash` | `string` | — | WorkCommand provenance_hash |
| `--provenance-kind` | `string` | — | WorkCommand provenance_kind |
| `--provenance-ref` | `string` | — | WorkCommand provenance_ref |
| `--rationale` | `string` | — | WorkCommand rationale_md |
| `--reason` | `string` | — | WorkCommand reason |
| `--required` | `bool` | `false` | make an acceptance criterion required |
| `--server` | `string` | — | control-plane base URL (default $OLIVARES_SERVER_URL or the active client context) |
| `--state` | `string` | — | WorkCommand state |
| `--statement` | `string` | — | WorkCommand statement |
| `--statement-md` | `string` | — | WorkCommand statement_md |
| `--subject-kind` | `string` | — | WorkCommand subject_kind |
| `--subject-ref` | `string` | — | WorkCommand subject_ref |
| `--supersedes-id` | `string` | — | WorkCommand supersedes_id |
| `--target-id` | `string` | — | WorkCommand target_id |
| `--tenant` | `string` | — | tenant id (default $OLIVARES_TENANT or the active client context) |
| `--terminal-code` | `string` | — | WorkCommand terminal_code |
| `--terminal-reason` | `string` | — | WorkCommand terminal_reason |
| `--timeout` | `duration` | `30s` | request timeout |
| `--title` | `string` | — | WorkCommand title |
| `--token` | `string` | — | API bearer token (default $OLIVARES_TOKEN or the active client context) |
| `--transition` | `string` | — | WorkCommand transition |
| `--ttl-seconds` | `int64` | `0` | WorkCommand ttl_seconds |
| `--unblock` | `bool` | `false` | WorkCommand unblock |
| `--version` | `uint64` | `0` | expected resource version N (sent as strong If-Match "vN") |
| `--waiver-decision-id` | `string` | — | WorkCommand waiver_decision_id |
| `--work-item-id` | `string` | — | WorkCommand work_item_id |
| `--work-kind` | `string` | — | WorkCommand work_kind |
| `--workspace-id` | `string` | — | WorkCommand workspace_id |

#### Command: olivares work watch

Watch the durable work-event stream from a resumable cursor

```
olivares work watch
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--ca-cert` | `string` | — | PEM CA bundle used to verify the control plane (default: the active client context) |
| `--cursor` | `string` | — | resume after this persisted WorkEvent cursor |
| `--insecure` | `bool` | `false` | skip TLS certificate verification (self-signed dev planes only) |
| `--json` | `bool` | `false` | deprecated alias for -o json |
| `--pin-sha256` | `stringArray` | `[]` | pinned leaf SPKI SHA-256, base64 or hex (repeatable) — the engine prints it as pin_sha256 on the line reporting its certificate; default: the active client context |
| `--server` | `string` | — | control-plane base URL (default $OLIVARES_SERVER_URL or the active client context) |
| `--tenant` | `string` | — | tenant id (default $OLIVARES_TENANT or the active client context) |
| `--timeout` | `duration` | `30s` | request timeout |
| `--token` | `string` | — | API bearer token (default $OLIVARES_TOKEN or the active client context) |

<!-- END GENERATED olivares-cli-reference -->

## Stability

This is a pre-1.0 product in active development. The subcommands and flags above are confirmed in the current binary, but the full CLI surface is still evolving: subcommands, flags, defaults, and output formats may change before a stable release. When in doubt, run `olivares <subcommand> --help` against the exact build you deployed, and treat that as authoritative over any document. For what is implemented today versus planned, see [Honesty and limits](/start/honesty-and-limits/). The REST/gRPC API surface itself is governed by the [API stability policy](/reference/api-stability/).
