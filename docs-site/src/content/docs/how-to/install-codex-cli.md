---
title: Install the Codex CLI
description: >-
  Install or verify the official OpenAI Codex CLI with Olivares, record the
  probe, author managed config, and launch or stop a governed session.
---

Olivares AI installs the **official Codex CLI**. It does not vendor a copy of
Codex and it does not replace `codex`.

## 1. Install or verify

```sh
olivares agent tool detect --driver codex --probe-path /usr/local/bin/codex
olivares agent tool plan --driver codex --version latest
olivares agent tool install --driver codex --version latest --yes
olivares agent tool list -o json
```

**What is verified today.** `detect` and the `--version` probe of a Codex CLI
that is already on the host are verified against the official CLI. The
origin-install path is **unverified**: the declared origin answers HTTP 403 to
a plain client, so Olivares refuses with `version_unknown`, prints the URL and
the status, and records nothing. Install Codex with the publisher's own
installer, then let Olivares detect, probe and govern it.

The receipt records the version, the fetched-object digest, the `--version`
probe, and the verification class. Probe is not authentication. Presence of
`~/.codex/auth.json` is reported as file presence only.

**Verification class (honest):** Codex origin-install is designed as
mixed-assurance (`codex-mixed-v1`): exact package digest plus publisher-signed
subjects. If no subject-proof verifier is configured, install **refuses**
(`verification_unavailable`) rather than recording a publisher-signed receipt.
That refusal is reached only when the origin answers; today the origin refuses
first. Detection of a Codex binary that is already on the host still works.

macOS and Windows are not offered in this increment.

## 2. Managed configuration

```sh
olivares codex managed-config --policy policy.json \
  --requirements-out /etc/codex/requirements.toml \
  --managed-config-out /etc/codex/managed_config.toml
```

`requirements.toml` is the non-overridable layer. `managed_config.toml` holds
defaults. See [Integrate Codex](/how-to/integrations/codex/) for the
can-enforce versus can-only-observe table.

## 3. Launch and stop

Pin the official binary, or use a managed install receipt:

```sh
export OLIVARES_SESSION_RUNTIME_CODEX_BIN=/var/lib/olivares/tools/codex/<version>-<platform>/bin/codex
```

If that variable is unset and a **registered** managed release exists, the
session runtime uses that executable. It does not search `PATH`.

```sh
olivares agent session create --name "codex-work" --workspace ws-123 --provider-profile prof-123
olivares agent session stop run-123
```

Create and stop go through the sessions API and leave a managed run record.
The CLI does not spawn Codex itself.

Further operate rules: [Operate a provider session](/how-to/operate-provider-sessions/).
