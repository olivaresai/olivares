<!-- SPDX-FileCopyrightText: 2026 Olivares.AI -->
<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# Configured external PDP at startup: explicit configuration is preserved or refused

The composition root reads the optional external policy decision point (PDP) from the
environment and turns it into a `governance` option. Until R38, an **explicit** engine
whose configuration was invalid — an unreadable Cedar file, a policy that does not
parse, incomplete OPA settings, an unknown selector — was logged and then converted
into *no option at all*. A missing option is indistinguishable from "the operator asked
for nothing", so construction continued and produced a native-only module set. The
native ABAC engine and RBAC still governed every request, but the deny overlay the
operator had configured was silently gone, and no caller received an error it could
propagate.

CP1 changes that: **invalid explicit PDP configuration is a construction error.** The
loader returns it, `buildModules` returns it, and every command that builds the module
set fails with a fixed, actionable message instead of running with authority the
operator did not choose. This is an intentional compatibility change.

## Configuration

| `OLIVARES_PDP_ENGINE` | Other settings | Outcome |
| --- | --- | --- |
| unset, blank, or `none` in any case | — | Native ABAC engine alone. No error. |
| `cedar` | `OLIVARES_PDP_CEDAR_FILE` unset or blank | Valid **empty overlay**. The evaluator's implicit base permit restricts nothing. |
| `cedar` | file readable and empty | Valid empty overlay, as above. |
| `cedar` | file readable, policy parses | The configured forbid rules are enforced, exactly as before. |
| `cedar` | file cannot be read | **Error** — unreadable policy source. |
| `cedar` | policy does not parse | **Error** — invalid Cedar policy. |
| `opa` | base URL and decision path both non-empty after normalization | Adapter constructed. The bearer token is optional. |
| `opa` | base URL or decision path empty after normalization | **Error** — invalid OPA settings. |
| anything else | — | **Error** — unsupported engine. |

The selector is trimmed and matched case-insensitively; `  CeDaR  ` and `cedar` select
the same engine.

### OPA normalization, exactly

The OPA constructor normalizes and requires both settings. It is unchanged by CP1; this
section only states what it already does, so an operator can predict the verdict:

- **Base URL** (`OLIVARES_PDP_OPA_URL`): surrounding whitespace and *trailing* slashes
  are trimmed. The result must be non-empty, so `""`, `"   "` and `"///"` are rejected.
- **Decision path** (`OLIVARES_PDP_OPA_PATH`): surrounding whitespace is trimmed, dots
  are replaced by slashes, and boundary slashes are trimmed. The result must be
  non-empty, so `""`, `"/"`, `"."` and `"./.."` are rejected. **There is no default
  decision path** — it is always explicit. `authz.allow` and `/authz/allow/` are both
  valid and normalize to the same request path.
- **Token** (`OLIVARES_PDP_OPA_TOKEN`): optional.

Two things construction deliberately does **not** do, and CP1 does not add:

- It does not validate URL syntax or scheme. A non-empty but malformed base URL is
  accepted at construction and fails later, on a request, under the evaluator's
  existing runtime behavior.
- It issues no HTTP request and no reachability probe. A correctly configured OPA that
  happens to be down does not block startup, and a transient request failure at runtime
  does not shut a running process down.

## Which commands this affects

There is one composition root, and no operational exemption from it. Read this section
as a rule first and a list second: the rule decides, and no command is exempt because it
is missing from a list below.

**The rule.** Four functions construct the module set: `boot`, `moduleOpenAPIDocument`,
`collectSchemaManifest` and `bootSchemaRegistrar`. Each returns the configuration error
to its caller, and no caller converts it to success. Therefore:

> **Every command that calls `boot` fails while the explicit PDP configuration is
> invalid — whether it calls `boot` directly or reaches it through a helper.** This is
> not limited to commands that serve traffic. A short-lived read-only command that opens
> the engine to answer a question builds the same module set and fails the same way.

`boot` calls `buildModules` and returns its error, so the failure is the same for every
one of its callers: the engine is never created, nothing listens, and the operation the
command was going to perform does not run. Work `boot` had already done before that
point is a separate matter — see the limits below.

**Examples, not an inventory.** The commands below are boot-backed today and are listed
to show the *breadth* of the rule. They are illustrative, and the list is deliberately
incomplete — a command's absence here means nothing, and any other boot-backed command
behaves identically:

- `serve`, `quickstart` and the governed-RAG quickstart, through the shared serve path;
- audit reads and audit key rotation, and the roster reads that share their boot helper;
- secrets and superadmin inspection commands;
- source listing and source-plan commands;
- ordinary disaster-recovery operations — restore, verify, staging and migration paths —
  **not only** `dr-drill`;
- eventing commands, including egress and fence operations;
- DDIL and support commands.

**The other three constructors** stop in their own places:

| Caller | Commands | Where it stops |
| --- | --- | --- |
| `moduleOpenAPIDocument` | `openapi --beta` | The beta module-route document is not produced. Plain `openapi` builds no modules and is unaffected. |
| `collectSchemaManifest` | `migrate manifest`, support-bundle schema collection | The manifest is not collected. |
| `bootSchemaRegistrar` | `migrate apply`, directory-writer activation | The registrar is not built, so the migration or maintenance operation does not run. |

Support is affected twice over, and on purpose: its schema section goes through
`collectSchemaManifest`, and its bundle collection also goes through a boot helper. Do
not expect a broken policy setting to be diagnosable from a support bundle collected on
the same host.

**Limits of this failure, stated plainly.** It is a constructor error, not a
transaction. Work a command performed *before* module construction — local directory
setup, key or configuration loading — is not rolled back. A process that is already
running and serving is not interrupted; the error only prevents a new construction.

## Remediation

Correct the setting, or choose the native engine explicitly:

- Unreadable file: check the path and the read permission of the user the engine runs
  as. The message does not repeat the path — it is in your configuration.
- Invalid policy: fix the Cedar source. An **empty** file is valid and means "no
  overlay"; deleting the `OLIVARES_PDP_CEDAR_FILE` setting means the same.
- Invalid OPA settings: set both the base URL and the decision path to values that
  survive the normalization above.
- Unsupported engine: use `cedar`, `opa` or `none`, or leave the variable unset.
- To run a single command without the external PDP, set `OLIVARES_PDP_ENGINE=none` for
  that command. Nothing rewrites an operator's configuration automatically.

## Diagnostics

The composition root's errors are fixed strings. Each names the environment variable to
correct and the class of the failure, and none of them carries the supplied path, the
policy source, the URL, the bearer token, the engine string, or the wrapped
constructor/OS error. The invalid-configuration log the loader used to emit is gone: the
caller reports the returned error, so there is exactly one message. Successful
construction still logs the selected engine, normalized to the supported selector.

## What this does not change

- Valid `none`, empty-Cedar, Cedar and OPA configurations construct and evaluate exactly
  as they did before.
- Runtime OPA behavior, the public `NewExternalPDP` factory, the evaluators, the scoped
  grant engine and the offline-staleness bound are untouched. When both the PDP and the
  staleness bound are misconfigured, the PDP is reported first.
- A healthy configured Cedar or OPA engine still has **no typed evidence companion**: it
  remains `UNKNOWN` in typed chains. This increment closes configuration-error
  propagation only. It enables no permit, no CLEAN witness, no policy version, no
  retained history.
