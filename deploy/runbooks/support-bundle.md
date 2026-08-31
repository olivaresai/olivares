<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->

# Runbook — collect a redacted support bundle (`support bundle`)

**When this fires:** support / incident response asks you for diagnostics, or you are escalating a control-plane
issue and need to attach state to a ticket **without leaking secrets**. `olivares support bundle` assembles a
redacted, self-describing `.tar.gz` you can hand off.

## What it collects (and how each channel is protected)
| Tar entry | Source | Protection |
|---|---|---|
| `config/effective.txt` | the systemd env file (`--config`, default `defaultEnvFilePath`) | **fail-closed (deny-by-default):** a value is shown verbatim **only** if it is a canonical secret reference (`file:`/`env:`/`store:`, never resolved) or its key is on the explicit public-settings allowlist; **every other value is `[REDACTED]`** — so a novel secret-bearing key over-redacts, never leaks. Key names are always shown. |
| `status/status.json` | `GET <server>/status` (skip with `--offline`) | canonical redactor |
| `logs/engine.log` | `--logs <file>` and/or `--journal --since` | canonical redactor (whole-text, so multi-line PEM blocks are caught) |
| `manifests/schema.json`, `manifests/dr-*.json` | schema manifest; `--dr-bundle` manifest (non-secret by design) | redactor pass + the final guard |
| `verify/report-*.json` | `--verify-report` (`audit verify` / `dr.RestoreVerify` output) | canonical redactor |
| `secrets/inventory.txt` | secret store **List** (name / hint / description / updated) | redactor pass; **never** the value — the store is opened with a nil sealer, so `Resolve`/`Get` is unreachable |
| `manifest.json` | this tool | sha256 per entry + redaction counts; a `notice` recording that references were not resolved and key files excluded |

**Two structural guarantees on top of redaction:** (1) a **closed allowlist** — only the entries above can
enter the tar; `*-signing.key`, `secret-store.key`, TLS keys and data-dir blobs have **no path in**; (2) a
**fail-closed final guard** — before writing, every entry is scanned and the tool **refuses to emit** any that
still carries a catalog-shaped secret/PII (it is a true fixed-point of the redactor, so anything the redactor
would redact, the guard detects). Redaction is unconditional — there is no `--no-redact`.

## Run it
```
# Full bundle from a running node (default: all sections):
olivares support bundle --out olivares-support.tar.gz \
  --data-dir /var/lib/olivares --server https://127.0.0.1:8443 \
  --config /etc/olivares/olivares.env --logs /var/log/olivares/engine.log

# Offline (no live status), journal instead of a log file, only some sections:
olivares support bundle --offline --journal --since "6 hours ago" \
  --include config,logs,verify --out /tmp/bundle.tar.gz

# Include a DR bundle's (non-secret) manifest and a verify report:
olivares support bundle --dr-bundle /backups/latest.drbundle \
  --verify-report <(olivares audit verify --tenant t_x --pubkey "<off-box key>")
```
Flags: `--out` · `--data-dir/--engine/--dsn` · `--server` (`$OLIVARES_SERVER_URL`) `--insecure` `--timeout`
`--offline` · `--config` · `--logs` `--journal` `--since` · `--include`/`--exclude` (sections:
config,status,logs,manifests,verify,secrets) · `--dr-bundle` · `--verify-report`.

## Verify the bundle before you share it
```
tar tzf olivares-support.tar.gz                     # only expected entries; no *.key
tar xzf olivares-support.tar.gz -O manifest.json | jq '.redaction_summary, .sections[].redactions'
```
The manifest's `redaction_summary` shows redactions fired on config/logs; `notice` documents the guarantees.
If the tool **refused** with `refusing to emit <path>: unredacted secret/PII detected`, a channel carried a
secret the redactor did not rewrite — treat it as a finding (see limits) rather than forcing the bundle.

## Limits (honest)
- **`config/effective.txt` is fail-closed**, so a secret in a config *value* is redacted even if its key is
  unknown — the residual risk there is only *over*-redaction of an unlisted public setting (harmless; the key
  name is still shown).
- **The free-text channels (logs / status / verify) are shape-based.** The redactor and its fail-closed guard
  cover the **catalog of known shapes** (API keys, JWTs, bearer tokens, PEM private keys, credit cards, emails,
  IPs, and `key=value` secrets for a broad key catalog). An **opaque, high-entropy secret with no `key=value`
  structure** sitting in a free-text log line, or **PII outside the catalog** (e.g. a name typed into a
  secret's description), cannot be detected without false-positiving on legitimate hashes and IDs — so it is
  **not** guaranteed to be redacted there. Do not paste raw secrets into free-text logs or secret
  descriptions, and **eyeball a bundle before sharing it externally**.
- **References are shown, not resolved.** `store:foo` appears verbatim so support can see *which* handle is
  configured; the value behind it is never read.
- Bundle contents are a point-in-time snapshot; it is not a substitute for the live console.
