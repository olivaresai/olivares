---
title: Add a provider and launch Claude Code, Codex or Grok
description: >-
  Register an API key with the control plane, test the connection without
  spending anything, bind it to a provider profile, and launch the first
  session — from the console and from the CLI.
---

This page is the first hour of the **provider** plane: where your API key goes, how
you know it works, and how a session launches with it.

In v26.9.0, the published release, a server environment variable is the only answer to the first question. In the pending v26.10 it can still be an environment variable on the
server. Those variables still work. They are no longer the only path, and they are no
longer how a new operator starts.

## What the three words mean

One sentence each, because the product used to blur them and the refusals it gives
you name them apart.

| Word | What it is |
|---|---|
| **Provider** | A credential: an API key, an optional endpoint, and the kind it belongs to (`anthropic`, `openai`, `xai`, `openai_compatible`). |
| **Provider profile** | An identity on this machine: which official CLI runs, and under which configuration home and user home. |
| **Session** | One launched child process, under one profile, using one provider's credential. |

A provider on its own launches nothing. A profile with no provider launches only if
the host's own variables happen to be set. The binding between them is what makes a
session start.

## 1. Add the provider

### From the console

1. Open **Providers** (AI → Environments → Providers).
2. Select **Add provider**.
3. Choose the provider, give it a name you will recognise in a picker, and paste the
   key. Leave the endpoint empty unless you are pointing at your own gateway; an
   `openai_compatible` provider requires one, because there is no official endpoint
   to assume.
4. Confirm. The write needs an AAL3 session, like every other credential in this
   product; the console shows the ceremony rather than a refusal.

The engine seals the key at rest and returns a four-character hint. **The key is
never returned again**, including immediately after you write it. If you lose it,
rotate it — there is no read that recovers it.

### From the CLI

```sh
# The key is read from stdin. It is never a flag value: a flag lands the credential
# in your shell history and in the process table.
olivares provider add --kind anthropic --name "Anthropic (prod)" < key.txt

# Or from a named environment variable of your own shell:
ANTHROPIC_KEY=sk-ant-... olivares provider add \
  --kind openai --name "Codex" --key-env ANTHROPIC_KEY
```

## 2. Test the connection

```sh
olivares provider test prv_01J8ABCDEF
```

The test asks the provider which models it serves. **It sends no completion and
spends nothing.**

It answers one of three things, and they are three different questions:

| Outcome | What it means | What to do |
|---|---|---|
| `ok` | The provider answered and accepted the credential. | Nothing. Bind it. |
| `refused` | The provider answered and rejected the credential. | Rotate the key. |
| `unreachable` | No answer was obtained. | Check the endpoint, the network and any proxy. **This says nothing about the key** — do not regenerate it. |

A provider you have not tested is shown as **not tested**, never as working.
Registering a credential is an intention; a test is a fact.

## 3. Register a profile and bind the provider

The profile's homes must already exist on the machine that runs the control plane.
The server validates them there and never creates a missing one: an empty fallback
home would give a session a provider identity nobody configured.

```sh
olivares agent profile create \
  --driver claude \
  --config-home /home/ops/.claude \
  --user-home /home/ops \
  --name "Claude (work)" \
  --auth-source managed_injection \
  --provider prv_01J8ABCDEF
```

`--auth-source` decides where the child's provider identity comes from, and the two
values are not a fallback chain:

- `provider_account_home` — the login already saved inside the profile's own homes.
  Olivares injects nothing and never reads that file.
- `managed_injection` — a credential the engine supplies. With a provider bound, it
  is that provider's.

There is a one-verb shortcut that does the detection, the registration and the
binding together, defaulting the homes to this driver's own under your `$HOME`:

```sh
olivares agent deploy claude --provider prv_01J8ABCDEF
```

It reports four states and they are not the same problem: **not installed** (and it
names the install command rather than running it), **installed**, **profile ready**,
**launchable**. It never runs a vendor login and never creates a missing home.

To bind (or rebind) later:

```sh
olivares provider bind prv_01J8ABCDEF --profile ppf_01J8ZZZZZZ
```

The engine refuses a credential the profile's driver cannot read. An OpenAI key on a
Claude profile is a refusal that names both, at bind time and again at launch — not a
session that fails halfway through a handshake.

| Provider kind | Drivers that read it |
|---|---|
| `anthropic` | `claude`, `opencode` |
| `openai` | `codex`, `opencode` |
| `xai` | `grok`, `opencode` |
| `openai_compatible` | all of them, with its endpoint |

## 4. Launch the first session

```sh
olivares agent workspace add /srv/projects/acme --name acme --mode ro --dlp deny
olivares agent session create \
  --name acme-1 \
  --workspace ws-123 \
  --provider-profile ppf_01J8ZZZZZZ
olivares agent session attach run-123
```

`--provider-profile` is **required**: the server does not select a profile, a home or
an environment implicitly.

From the console, the same path is **Onboarding → Agents and the first session**, or
**Sessions → New session**.

## Rotation and revocation

```sh
olivares provider rotate prv_01J8ABCDEF < new-key.txt   # reseals in place
olivares provider rm prv_01J8ABCDEF --yes               # irreversible
```

Rotation replaces the value under the same reference, so every profile bound to it
keeps working and the **next** launch uses the new key. A session already running
keeps the credential it started with. The previous connection test is cleared: a
verdict measured on a credential that no longer exists is not evidence about the one
replacing it.

Revocation destroys the sealed value and refuses every future launch **by name**. The
record and the bindings are kept on purpose — a profile that silently stopped naming
anything would read as a profile nobody configured. Revoking here does not revoke the
key at the provider; do that in their own console.

## What the engine refuses, and why

| You see | It means |
|---|---|
| `provider credentials cannot be stored on this deployment` | No sealed vault is wired. The engine refuses to store a key rather than storing one it cannot protect. |
| `the provider connection test is not available` | No probe is wired. **Launching is unaffected.** |
| `a … credential is not readable by driver …` | The kind and the driver do not match. See the table above. |
| `the provider this profile is bound to is revoked` | Bind an active provider. |
| `this launch has two endpoints` | The deployment's inference gateway and the provider's own `base_url` both apply. Clear one; the engine does not choose. |
| `the registered provider credential could not be opened` | The launch is denied. **It does not fall back to a host credential** — that would run your session on an account you did not select. |

## The environment variables, and where they still apply

`OLIVARES_SESSION_RUNTIME_WIF` and `OLIVARES_SESSION_RUNTIME_TOKEN_FILE` are
unchanged and still supported. They are the host-wide credential and they apply to
every profile that names **no** provider.

A profile that names one uses that one. The most specific selection wins, and there
is no fallback from it: a bound credential that cannot be produced denies the launch
rather than quietly using the deployment's.

## Related

- [Operate a provider session](/how-to/operate-provider-sessions/)
- [Your first hour](/how-to/first-hour/)
- [CLI reference](/reference/cli/)
