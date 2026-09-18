---
title: Install the Grok CLI
description: >-
  Install or verify the official Grok Build CLI with Olivares, record the
  probe, author managed requirements, and launch or stop a governed session.
---

Olivares AI installs the **official Grok Build CLI**. It does not vendor a copy
of Grok and it does not replace `grok`.

## 1. Install or verify

```sh
olivares agent tool detect --driver grok --probe
olivares agent tool plan --driver grok --version stable
olivares agent tool install --driver grok --version stable --yes
olivares agent tool list -o json
```

**Ask for the channel the origin publishes.** The Grok origin publishes
`stable`. It answers HTTP 404 for `latest`, and Olivares then refuses with
`version_unknown` and prints the URL and the status. Channel names belong to
the vendor.

The receipt records the version, the measured digest, the `--version` probe,
and the verification class. Probe is not authentication. Presence of
`~/.grok/config.toml` is reported as file presence only.

**Verification class (honest):** Grok origin-install is `none-origin-only`.
Olivares fetches from the official HTTPS origin, places `bin/grok`, and probes
`--version`. That is **not** a publisher signature. The receipt says so.

macOS and Windows are not offered in this increment.

## 2. Managed configuration

```sh
olivares grok managed-config --policy policy.json \
  --requirements-out /etc/grok/requirements.toml
```

Grok **clamps** `/etc/grok/requirements.toml` as the highest layer (sandbox
profile and MCP allowlist). A user can still disable a hook by writing its name
in `~/.grok/disabled-hooks`; that file is **observe-only**.

See [Integrate Grok Build](/how-to/integrations/grok/) for the can-enforce
versus can-only-observe table.

## 3. Launch and stop

Pin the official binary, or use a managed install receipt:

```sh
export OLIVARES_SESSION_RUNTIME_GROK_BIN=/var/lib/olivares/tools/grok/<version>-<platform>/bin/grok
```

If that variable is unset and a **registered** managed release exists, the
session runtime uses that executable. It does not search `PATH`.

```sh
olivares agent session create --name "grok-work" --workspace ws-123 --provider-profile prof-123
olivares agent session stop run-123
```

Create and stop go through the sessions API and leave a managed run record.
Compatibility with an authenticated official Grok account is **not claimed**
here.

Further operate rules: [Operate a provider session](/how-to/operate-provider-sessions/).
