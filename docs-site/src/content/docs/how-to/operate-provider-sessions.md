---
title: Operate a provider session
description: >-
  Register a provider profile for an existing Claude, Codex or Grok home on this
  node, pin the official driver binary, launch a governed session from the
  console or CLI, then interrupt or stop the turn without inventing a runner.
---

This page is the **operate** path for official provider CLIs. The control plane
launches an owned child process under a **provider profile**. It does not install
the provider, create its home, or start an interactive browser sign-in.

It is not the connector/hook path. To inventory or govern Grok Build or Codex
configuration files, use [Integrate Grok Build](/how-to/integrations/grok/) or
[Integrate Codex](/how-to/integrations/codex/). To co-deploy Claude Code on the
same host, use [Run Claude Code with Olivares](/how-to/run-claude-code-with-olivares/).

Source for this behavior: `CHANGELOG.md` section `[26.9.0]` (provider
profiles, Codex driver, Grok driver), the generated
[console](/reference/console/) and [configuration](/reference/configuration/)
references, `cmd/olivares/sessionruntime.go`, and
`web/src/features/agentops/types.ts`.

## Preconditions

Complete these before a launch. A missing item is a refusal, not a fallback.

1. Olivares AI is installed and the first administrator exists.
   See [Your first hour](/how-to/first-hour/) for the setup token and the AAL3
   passkey wall. Creating sources and privileged session operations require AAL3
   (`core/api/middleware.go` `requireAAL3`).
2. The official provider CLI is already installed on **this node**. The profile
   registers homes that already exist. The server resolves the paths (absolute,
   symlinks resolved, existing directory) and does not create, install, or log
   in (`web/src/features/agentops/types.ts` `CreateProfileRequest`).
3. You hold `sessions:profile:read` to open **Provider profiles**
   (`/provider-profiles`) and `sessions:profile:write` to register.
   Binding a source needs `sessions:profile-binding:write` plus source
   administration. Launching a run needs `sessions:run:write`.
   Permissions: [Console reference](/reference/console/).
4. The matching driver is **registered on this node** by pinning its official
   binary. Readiness is per driver. There is no shared switch
   (`cmd/olivares/sessionruntime.go`).

| Driver | Pin this environment variable | When it is unset |
|---|---|---|
| Claude Code | `OLIVARES_SESSION_RUNTIME_CLAUDE_BIN` (default `claude`) | the Claude path uses the default executable name |
| Codex | `OLIVARES_SESSION_RUNTIME_CODEX_BIN` | If unset, a **registered** managed install (`olivares agent tool install --driver codex`) pins the receipt executable. Otherwise Codex profiles stay observable and are not launchable. The engine does not search `PATH`. |
| Grok | `OLIVARES_SESSION_RUNTIME_GROK_BIN` | If unset, a **registered** managed install (`olivares agent tool install --driver grok`) pins the receipt executable. Otherwise Grok profiles stay observable and are not launchable. The engine does not search `PATH`. |

The value is the official binary this node may operate. The engine does not
resolve `codex` or `grok` off `PATH`. The generated configuration table also
lists `OLIVARES_SESSION_RUNTIME_OPENCODE_BIN` with the same registration rule;
this page does not add further OpenCode claims.

Claude launches still need one inference credential source
(`OLIVARES_SESSION_RUNTIME_WIF` or `OLIVARES_SESSION_RUNTIME_TOKEN_FILE`).
See [Your first hour §3](/how-to/first-hour/#3-launching-a-claude-code-session-from-the-console).
Codex and Grok use the profile's AUTHORIZED `auth_source` only:
`provider_account_home` or `managed_injection`, with no fallback between them
and no default (`CHANGELOG.md` `[26.9.0]`; `ProviderProfileDTO.auth_source`).

:::caution[What this page does not claim]
`CHANGELOG.md` `[26.9.0]` states that Grok driver behavior is proven against an
owned fake ACP child through the real HTTP, runtime, store and process group.
**Compatibility with an authenticated official Grok account is separate work
and is not asserted here.**
:::

## 1. Register a provider profile

A provider profile is the durable identity of **one** configured provider
instance on **one** execution environment: driver, owning environment, and the
canonical `config_home` / `user_home` the child runs under. It is configuration
and storage identity, not an authenticated provider account
(`CHANGELOG.md` `[26.9.0]` B1; console copy `agentops.profiles.subtitle`).

### Console

1. Open **Provider profiles** (`/provider-profiles`).
2. Select **Register profile**.
3. Set the driver (`claude`, `codex`, or `grok`), the existing `config_home`,
   and the existing `user_home`. `environment_ref` may be omitted (this node).
4. Save. The list shows `profile_ref`, driver, state, and whether the profile
   is enabled in this environment. Paths are **not** in the ordinary list.
5. To see stored homes, use **Reveal configuration** (`sessions:profile:admin`).
   That read is on demand and is dropped when hidden. It still carries no
   credential value.

Rename, disable and enable keep the same id and homes. **Retire** is
irreversible, confirmed by typing, and frees the home for a **new** id.

### What the engine refuses

- A profile whose driver is not registered on this node stays visible and is
  not launchable (`operable` is not a launch guarantee;
  `GET …/launch-readiness` is the requirements panel).
- A profile that belongs to another execution environment is shown as foreign
  and is never launched from this node.
- A launch that forwards `HOME`, `CLAUDE_CONFIG_DIR`, `CODEX_HOME` or
  `GROK_HOME` is refused. Those names belong to the profile
  (`agentops.create.profileEnvConflict`).

There is no screenshot of this screen in the published capture set. Do not
treat a Connectors-tab image as this form.

## 2. Bind a source (optional, for observed attribution)

A source can be dedicated to a profile at the exact roster revision this node
applied. The key is the roster row's persistent id, never its editable name
(`CHANGELOG.md` `[26.9.0]` B1; **Source bindings** `/provider-bindings`).

1. Open **Source bindings** (`/provider-bindings`).
2. Bind the source's persistent `id` and the `applied_revision` this node's
   reconciler wired. `GET /v1/console/sources` reports both.
3. Revoke the binding to stop **new** profile attribution. An earlier envelope
   keeps its historical binding during replay.

Without a binding, a known registration still appears as a `source`
observation row. It is not merged into a managed run. See
[Live operation & sessions](/reference/modules/ii-sessions/).

## 3. Launch

The productive create endpoint requires `provider_profile_ref`. Omitting it
keeps the older request body, which this API refuses
(`CHANGELOG.md` `[26.9.0]` B2; CLI flag `--provider-profile`).

The launch dialog offers the **active** profiles. No profile is pre-selected.
Workspace and template selections may be cleared; the profile may not
(`CHANGELOG.md` `[26.9.0]` Fixed).

### Console

1. Open **Operate sessions** (`/agentops`) or **Observe sessions** (`/sessions`).
   They share a screen ([console reference](/reference/console/)).
2. Open the launch dialog.
3. Select **Provider profile** (`agentops.create.profile`). The hint states a
   profile is required.
4. Optionally set workspace, template, model and effort. Model and effort stay
   provider-owned open strings on the official agent flags for Grok
   (`CHANGELOG.md` `[26.9.0]`).
5. Submit **Request launch**. Only the profile **reference** is posted. The
   server resolves the homes.

### CLI

```sh
olivares agent session create --provider-profile <profile_ref>
```

Add `--server`, `--tenant` and `--token` (or the active client context) as in
the [CLI reference](/reference/cli/). Isolation is `native` this release;
`container` and `sandbox` are accepted by the API and refused by the launcher
until those runners ship (generated CLI help).

Result: a run resource. The managed live row is unique per observation scope
and external id. Reads that name a row use `live_ref`, not the bare provider
session id two homes may share.

## 4. Interrupt or stop

| Intent | Console | CLI | Result |
|---|---|---|---|
| End the active turn, keep the process and conversation | interrupt control on the live session | `olivares agent session interrupt <run-ref>` | the turn ends; the process stays usable for the next turn (`CHANGELOG.md` `[26.9.0]`) |
| End the run | stop control | `olivares agent session stop <run-ref>` | the run resource; runtime still reaps the child |

Work-bound runs send their exact lease fence. Stale or uncertain results stay
explicit. Resume continues only on the same proven home.

A Grok interrupt uses ACP `session/cancel`, which is a notification with no
acknowledgement. The interrupt resolves pending approvals, cancels, and leaves
the turn open until the prompt's own correlated result returns
(`CHANGELOG.md` `[26.9.0]`). Do not treat a silent cancel as a confirmed
provider receipt.

## Related

- [Your first hour](/how-to/first-hour/) — setup token, AAL3, Claude credential source.
- [Run Claude Code with Olivares](/how-to/run-claude-code-with-olivares/) — co-deployment topologies.
- [Integrate Codex](/how-to/integrations/codex/) / [Integrate Grok Build](/how-to/integrations/grok/) — connector and PEP hook.
- [Session runtime API](/reference/session-runtime-api/) — list, attach, input, stop; Community PTY and edition cut.
- [Live operation & sessions](/reference/modules/ii-sessions/) — `live_ref` and attribution.
- [Configuration](/reference/configuration/) — driver pin variables.
- [CLI reference](/reference/cli/) — `olivares agent session *` (generated from the binary).
