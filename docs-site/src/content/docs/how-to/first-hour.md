---
title: "Your first hour with Olivares AI (v26.9.0, as shipped)"
description: >-
  What a clean install of the public v26.9.0 binary actually lets you do in the
  first hour: the setup token, the AAL3 wall, passkey registration, provider-profile
  session launch, deploy, knowledge, and the Codex and Grok PEP hooks.
---

This page describes **v26.9.0 as shipped**. It is not a planned first-run
wizard and it is not a screenshot of a mockup. Every step below is something
the public binary does today, with the file or environment variable that
makes it true. Where the product refuses, the page says so.

The numbered facts were measured on a clean install of the public binary
on 2026-09-04. This page cites those measurements and the code they land
on.

How to get the binary onto the host is
[Self-host Olivares AI](/how-to/self-hosting/) and
[Verify a release](/how-to/verify-a-release/). This page does not tell you
to pipe `https://olivares.ai/install` into a shell. The recommended first
command, once the binary is on the host, is `olivares quickstart`.

:::note[What this is not]
`--seed-demo` is not a tour. Measured on a clean install of the public
binary on 2026-09-04: **36 of 54** console routes still empty after a
seeded boot. That count is the measurement date's census. The generated
[console reference](/reference/console/) on this tree lists **75 routes**.
This page does not re-count empty tabs after `--seed-demo` on v26.9.0.
The demo estate fills the access-graph walk in
[From zero to a read/write access graph](/tutorials/zero-to-graph/); it does
not fill the rest of the console. Do not use it to “explore the product”.
:::

## 1. Recommended boot: `olivares quickstart`

A fresh data directory has **no default credentials**. The recommended
first command is `olivares quickstart` (`cmd/olivares/cmd_quickstart.go`).
It is `serve` with secure defaults: TLS on, no default credentials, a
single-use setup token. The default listen address is `:8443` — every
interface, because this is a server (`cmd/olivares/binddefaults.go`).
Measured on a clean install of the public binary on 2026-09-04 with real
TLS (including a run on **:8460**); the panel text is the same.

The welcome panel (`announceQuickstart`, `:154-163`) is numbered. The
engine prints `https://localhost:8443` for that bind, and lists every other
address this host answers at below the token. **Do not use an IP for the passkey
ceremony.** The browser rejects an IP as WebAuthn RP ID (`SecurityError`).
The product derives the RP ID from the request hostname
(`core/api/handlers_webauthn.go:33-50`). Open the console at
`https://localhost:PORT` (or a real hostname), not `127.0.0.1`, before
registering the passkey. `PORT` is `8443` unless you passed `--listen`.

```text
=== WELCOME TO OLIVARES AI ===
  1. Open:   https://localhost:8443
     (HTTPS with a self-signed certificate on first boot — your browser will
      warn once; that is expected for a local install.)
  2. Complete setup with this one-time token (shown once, single-use):

         olst_…
```

The banner prints `localhost` for the default bind, then lists this host's
other addresses under the token — those are for reaching the console from
another machine, and a passkey ceremony still needs a name, not an address.

The token prefix is `olst_` (`cmd/olivares/e2e_binary_test.go` matches
`olst_[A-Z0-9]+`). The console page is `/setup`. The API the wizard posts
is:

```http
POST /v1/setup
Content-Type: application/json

{"token":"olst_…","email":"you@example.com","password":"…"}
```

`olivares serve` prints a similar banner (`announceSetup` in
`cmd/olivares/cmd_serve.go`). Use `quickstart` for the first hour: the
URL, the self-signed-certificate warning, and the one-time token are in
one panel. Then log in with `POST /v1/auth/login`. You now have an AAL1
password session.

`README.md` and that welcome panel name the token and the URL. They do
**not** name passkey enrollment. That comes next, from the console, after
you press a button.

## 2. The AAL3 wall — the console routes you after you press, not before

After setup, **creating sources, connectors, workspaces and secrets is
refused until the session is AAL3**. The gate is `requireAAL3` in
`core/api/middleware.go:310`. A principal below AAL3 gets `403
step_up_required`. **21 call sites** go through that gate. The write paths in this
tree include:

| Surface | Handler | File |
|---|---|---|
| Source roster put/delete/reload | `handlePutSource` / `handleDeleteSource` / `handleReloadRuntime` | `core/api/handlers_sources.go` |
| Connector writes and test | `handlers_connectors.go` | `core/api/handlers_connectors.go` |
| Secret put/delete | `handlePutSecret` / `handleDeleteSecret` | `core/api/handlers_secrets.go` |
| Workspace create / update | `handleCreateWorkspace` / `handleUpdateWorkspace` | `core/api/handlers_scoping.go:152` / `:199` |
| Member onboarding | `handleOnboardMember` | `core/api/handlers_onboarding.go:59` |

**PIV/CAC does not get you there on a stock install.** Unconfigured, the
PIV routes answer **501** `piv_not_configured` (`core/api/handlers_piv.go`,
`core/api/errors.go`).

### What the console actually does

The console **does** send you to enrollment. It does it **reactively**.

1. You hit a privileged action. The step-up panel shows
   **Authenticate with security key**
   (`web/src/features/identity/i18n/en.json` `assurance.authenticate`;
   button in `web/src/features/identity/assurance.tsx:230-239`).
2. That click calls `POST /v1/auth/webauthn/authenticate/options` (step-up,
   not register). With no passkey, the engine answers **400**
   `no_webauthn_credential` (`isNoWebAuthnCredential` in
   `web/src/features/identity/api.ts:260-265`).
3. The panel then tells you to register in the **Privileged login** tab
   first (`assurance.tsx:160-168` → `assurance.unenrolled`). That sentence
   is **not a link**.

You go by hand:

1. Open the console at **`https://localhost:PORT`**, not `127.0.0.1`
   (see §1).
2. Open `/identity` (`web/src/features/registry.tsx` — `path: '/identity'`).
3. Tab **Privileged login** (`tabs.login`).
4. **Register passkey** (`passkeys.register`). A **platform authenticator
   is enough** (the browser or OS prompt, no physical key). The server
   requires **user verification**
   (`core/auth/webauthn.go:74-85`, `UserVerification: VerificationRequired`).
   Measured on a clean install of the public binary on 2026-09-04 with a
   complete WebAuthn ceremony: register **200**, authenticate
   `{"aal":3}`, then `PUT /v1/console/connectors` **200**.

`POST /v1/auth/webauthn/register/options` is authenticated as a **session
principal** and does **not** call `requireAAL3`. On success it writes
**200** with `{publicKey: …}` (`core/api/handlers_webauthn.go:76-88`).
Complete the ceremony with `POST /v1/auth/webauthn/register`. Then retry
the step-up.

`README.md` and the `olivares quickstart` welcome panel do **not** name
the Privileged login tab. The console names it only **after** that 400.

The identity panel names AAL3 (NIST SP 800-63B-4) and PIV/CAC (FIPS 201-3)
as **target standards** and states it **claims no certification**
(`targetStandardsNote`). This page does not claim a certification either.

### Add connector: the wall, no fields

**Add connector** (`web/src/features/console/i18n/en.json`
`connectors.add`) opens a dialog wrapped in
`<RequireAssurance minAal={AAL.HARDWARE}>` around `ConnectorForm`
(`web/src/features/console/connectors-tab.tsx:348-357`). Below AAL3 the
form is not mounted. Measured on a clean install of the public binary on
2026-09-04: that dialog has **no inputs** (`inputs: []`) — only the
step-up panel. Seeing the catalog of kinds is
AAL1 (`ConnectorCatalog` sits outside the gate, same file `:341-346`);
**adding** one is not.

### `/workspace` and protocol bindings: no workspace switcher

`/workspace` (`registry.tsx` `path: '/workspace'`) and
`/communications/protocol-bindings` need a workspace. On a clean install
you have none, and you cannot create one until AAL3
(`handleCreateWorkspace`). `WorkspaceSwitcher` **does not render** when
there is at most one workspace (`web/src/components/layout/workspace-switcher.tsx:43-44`:
`if (workspaces.length <= 1) return null`). What stays in the top bar is
**Switch organization** (`web/src/lib/i18n/locales/en/auth.json`
`tenant.switch`). There is no workspace picker to click.

## 3. Launching a Claude Code session from the console

The console can spawn a `claude` process only when the **host** has an
inference credential source. Without one, stream-json launches are
deny-closed. Measured on a clean install of the public binary on
2026-09-04: **HTTP 503**.

Set **one** of:

- `OLIVARES_SESSION_RUNTIME_WIF` — in-process WIF mint (`cmd/olivares/sessionruntime.go`, `cmd/olivares/wifbroker.go`)
- `OLIVARES_SESSION_RUNTIME_TOKEN_FILE` — path to a rotated short-lived token file

The composition root logs which source is wired, or:

```text
session runtime: no inference credential source configured; stream-json launches are deny-closed
```

(`cmd/olivares/sessionruntime.go:74-76`). Optional siblings:
`OLIVARES_SESSION_RUNTIME_WIF_RULE`, `OLIVARES_SESSION_RUNTIME_TOKEN_TTL`,
`OLIVARES_SESSION_RUNTIME_BASE_URL`, `OLIVARES_SESSION_RUNTIME_CLAUDE_BIN`
(listed in `cmd/olivares/config_registry.go` and
[Configuration](/reference/configuration/)).

The **Operate** co-deployment shapes (same host as `claude`) are in
[Run Claude Code with Olivares](/how-to/run-claude-code-with-olivares/). The
OTLP observe path is [Connect Claude Code](/how-to/connect-claude-code/).

### Provider keys are not what launches a session

**Models → Provider keys** is a governance registry of **references**. The
form **never accepts a secret**:

> This form never accepts a secret. Olivares stores a reference and a masked
> hint only.

(`web/src/features/models/i18n/en.json` `keys.dialog.noSecretNote`). Filling
that tab does not satisfy `OLIVARES_SESSION_RUNTIME_WIF` or
`OLIVARES_SESSION_RUNTIME_TOKEN_FILE`. It does not enable launches.

## 4. Deploy plan is 503 until you provision an executor

`POST` plan/apply on the deploy module returns **503** until the host sets
`OLIVARES_DEPLOY_EXECUTOR_CONFIG` to a JSON file. Absent, the module keeps
the deny-closed unwired executor (`cmd/olivares/deployexec_load.go:16-20`).
An unreadable file **fails startup**.

The JSON object is `deployExecutorConfig` in
`cmd/olivares/deployexec_load.go:26-42`. Optional backend blocks:
`tofu`, `terraform`, `gitops`, `k8s`, `docker`, `nomad`, `crossplane`, plus
`credential`, `blast_radius`, `identity_binding`, `drift`.

The environment variable is documented in
[Configuration](/reference/configuration/)
(`docs-site/src/content/docs/reference/configuration.md:152`). It is not
documented as a first-hour console click. There is nothing to pick in the
UI that substitutes for that file.

The modules catalog marks Deployment actuate as **on-demand (503)**
([Modules](/reference/modules/overview/)). That row is the same fact.

## 5. Query a public knowledge base, or query with an agent identity

The default embedder is the zero-egress **LocalHashEmbedder**. Boot warns
that retrieval is **lexical, not semantic**, and `embed_model=local-hash`
(`cmd/olivares/claude_inference.go`, `cmd/olivares/knowledgestatus.go`).
Lexical retrieval still returns chunks when the guard allows them.

Without an authenticated agent identity, the retrieval guard grants only
**public, unrestricted** content (`modules/knowledge/query.go:120-127`).
A human REST `/query` on an **internal** KB is denied. Measured on a
clean install of the public binary on 2026-09-04: a **public** KB
returned **1 result** (score 0.738). Query a public knowledge base, or
query with an agent identity.

A denied query reports `excluded_chunks: 0` today even when everything
was excluded. That counter only increments for the operator-authored
`excluded_sources` floor (`query.go:256-284`); a clearance or
ACL denial never adds to it.

## 6. Codex and Grok sessions: provider profiles, then the remaining CLI hooks

v26.9.0 operates the official Codex CLI and the official Grok CLI as session
drivers, in addition to Claude Code (`CHANGELOG.md` `[26.9.0]` Added). The
console administers those launches on **Provider profiles**
(`/provider-profiles`, `sessions:profile:read`) and **Source bindings**
(`/provider-bindings`, `sessions:profile-binding:read`). Both routes are in
the generated [console reference](/reference/console/).

A profile is the durable identity of one configured provider instance on one
execution environment. It is not an authenticated provider account
(`web/src/features/agentops/types.ts`). Registering a profile validates homes
that already exist on this node. The server does not install, create, or log
in.

### Register the driver on the host before a launch

Readiness is per driver. There is no shared switch
(`cmd/olivares/sessionruntime.go`). Setting the matching environment variable
registers that driver on this node:

| Driver | Environment variable | When unset |
|---|---|---|
| Claude Code | `OLIVARES_SESSION_RUNTIME_CLAUDE_BIN` (default `claude`) | the Claude path uses the default name |
| Codex | `OLIVARES_SESSION_RUNTIME_CODEX_BIN` | Codex profiles stay observable and are not launchable |
| Grok | `OLIVARES_SESSION_RUNTIME_GROK_BIN` | Grok profiles stay observable and are not launchable |

Values are pinned official binaries. The engine does not resolve `codex` or
`grok` off `PATH`. Source: [Configuration](/reference/configuration/).

Claude launches still need one inference credential source, as in §3.
Codex and Grok authentication is the profile's AUTHORIZED `auth_source`
(`provider_account_home` or `managed_injection`, no fallback).
`CHANGELOG.md` `[26.9.0]` does **not** claim compatibility with an
authenticated official Grok account.

The launch dialog requires a provider profile. The productive create endpoint
requires `provider_profile_ref`. Omitting it keeps the older request body,
which this API refuses ([CLI](/reference/cli/)
`olivares agent session create --provider-profile`).

How to register a profile, bind a source, launch, interrupt and stop:
[Operate a provider session](/how-to/operate-provider-sessions/).

### What remains CLI-only (hooks and managed config)

These commands are not console session launch. They still exist:

| Command | What it is | Source |
|---|---|---|
| `olivares codex` | Renders Codex `requirements.toml` / `managed_config.toml` from a Policy JSON. **Writes files; it does not talk to the control plane.** | `cmd/olivares/cmd_codexmanagedconfig.go` |
| `olivares codex-hook` | Deny-closed PEP hook Codex invokes (stdin → control plane → stdout in the event’s shape). | `cmd/olivares/cmd_codexhook.go` |
| `olivares grok-hook` | Deny-closed PEP hook Grok Build invokes. A deny only **blocks** on `pre_tool_use`. | `cmd/olivares/cmd_grokhook.go` |

Codex hook install (verified against Codex’s `hooks.json` shape in that
file’s comment): `command` must be a **string**,
`olivares codex-hook`. Environment:
`OLIVARES_CODEX_HOOK_URL`, `OLIVARES_CODEX_HOOK_TOKEN`,
`OLIVARES_CODEX_HOOK_TENANT` (and optional agent/org/account).

Grok hook environment: `OLIVARES_GROK_HOOK_URL`,
`OLIVARES_GROK_HOOK_TOKEN`, `OLIVARES_GROK_HOOK_TENANT`. Grok can disable a
hook by name via `~/.grok/disabled-hooks`; that is not a console control.

Do not look for a “Connect Codex” or “Connect Grok” **button**. Connector
enrollment is **Control console → Connectors** (type `codex` or `grok`). That
is the observe/govern plane in
[Integrate Codex](/how-to/integrations/codex/) and
[Integrate Grok Build](/how-to/integrations/grok/). It does not register a
session driver.

## 7. `--seed-demo` does not fill the console

`olivares serve --seed-demo` loads a demo estate so the access-graph
tutorial can run. Measured on a clean install of the public binary on
2026-09-04: **36 of 54** console screens still empty on that estate. That
count is the measurement date's census; the generated console reference
today lists **75 routes**. Use `--seed-demo` only for the path in
[From zero to a read/write access graph](/tutorials/zero-to-graph/). Do not
treat empty tabs after `--seed-demo` as a broken install, and do not treat
the flag as a product tour.

## Related

- [Honesty & limits](/start/honesty-and-limits/) — what the docs are allowed to claim.
- [Self-host Olivares AI](/how-to/self-hosting/) — how the binary is run.
- [Operate a provider session](/how-to/operate-provider-sessions/) — profiles, driver pins, launch, interrupt.
- [Configuration](/reference/configuration/) — the environment variables named above.
- [Modules](/reference/modules/overview/) — on-demand (503) vs live actuation.
