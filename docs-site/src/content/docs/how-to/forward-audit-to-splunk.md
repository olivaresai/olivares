---
title: Forward to Splunk (drop a Universal Forwarder + tail)
description: >-
  Get the control plane's governance findings and its tamper-evident audit ledger
  into Splunk by tailing a file with a Universal Forwarder — without a native
  Splunk-to-Splunk emitter. Honest about which stream is which.
---

You can get Olivares AI data into Splunk **today**, without waiting for a native
connector: write the data to a file and point a **Splunk Universal Forwarder (UF)**
at it. The UF handles the Splunk-to-Splunk (S2S) hop to your indexer.

:::caution[There is no native Splunk S2S emitter]
Olivares AI does **not** implement Splunk's proprietary S2S forwarder protocol. A
native S2S emitter is post-v1. The supported postures are **file-tail forwarding**
(a UF tails a file Olivares writes), the **pull export** (for WORM archival and
offline re-verification), and an **HTTP push over Splunk HEC** — including, since
the SIEM-interop work, a push of the **ledger itself** via an eventing sink
([push to your SIEM](/how-to/cookbook/push-to-siem/)). This page documents the
file and pull paths; the recipe covers push.
:::

There are **two different streams**, and they are not the same thing. Choose
deliberately:

| Stream | What it is | Ways to Splunk |
|---|---|---|
| **Governance / findings** | the notification stream module IX routes (health, spend, security, compliance findings) | the `filelog` output connector appends it to a file; or `splunkhec` pushes it; or an [eventing sink](/how-to/cookbook/push-to-siem/) subscribed to `finding.reported` |
| **Tamper-evident audit ledger** | the append-only, hash-chained, signed audit trail | the **pull** export `GET /v1/audit/export` (this page); or the **push** pump — an eventing sink subscribed to `audit.recorded`, delivered at-least-once. There is no native *file* sink; materialize a file with the scheduled export below |

## Stream A — findings, via the `filelog` connector

The `filelog` output connector appends the notification/findings stream **one record
per line** to a file (or `stdout`/`stderr`), which a UF can tail. Configure a
notification destination of kind `filelog` with these fields:

| Field | Meaning |
|---|---|
| `path` | append target: a file path, or `stdout`/`stderr`/`-` |
| `format` | per-line format: `json` \| `cef` \| `leef` \| `syslog` \| `otlp` \| `otlp_envelope` \| `ocsf` \| `asim` (default `json`) |
| `hostname` | syslog `HOSTNAME` field (for the `syslog` format) |
| `fsync` | flush each record to disk (durability for a WORM copy; slower) |

For Splunk, `format: json` (rich fields) or `format: cef`/`syslog` (line formats Splunk
parses natively) both work. The file is opened append-only, so the same file doubles
as an immutable external copy when placed on WORM storage.

:::note[`filelog` carries findings, not the signed ledger]
The `filelog` connector forwards the **findings** stream — it never sees the
tamper-evident audit ledger. To forward the verifiable ledger, use Stream B.
:::

### Turnkey alternative: Splunk HEC

If you would rather push over HTTP than tail a file, the `splunkhec` connector posts
the same findings stream to Splunk's HTTP Event Collector (`/services/collector`)
with an `Authorization: Splunk <token>` header — a turnkey HTTP path, still not S2S
and still the findings stream, not the ledger.

## Stream B — the tamper-evident ledger, via the pull export

The audit ledger is exposed as an **authenticated pull export**, not a file the
engine writes on its own. Each record carries the chain-integrity fields
(`seq`, `prev_hash`, `hash`, `sig`) so your SIEM can **re-verify the hash chain
offline**; PII is never exported.

```bash
# One-shot full export (CEF). Requires a token with the audit:read permission.
curl -fsS "https://localhost:8443/v1/audit/export?format=cef" \
  -H "Authorization: Bearer $OLVK_TOKEN" \
  -H "X-Olivares-Tenant: $TENANT" >> /var/log/olivares/audit.cef
```

Supported `format` values are `cef`, `leef`, `syslog`, `otlp`, `otlp_envelope`,
`otlp_log_record` and `ocsf`. `otlp` is a complete, postable OTLP/HTTP export
request per record, `otlp_envelope` is an exact alias of it, and
`otlp_log_record` is the bare one-LogRecord-per-line projection. Line formats
(`cef`/`leef`/`syslog`) stream as `text/plain`;
`otlp`/`otlp_envelope`/`otlp_log_record`/`ocsf` stream as
NDJSON (`application/x-ndjson`), one JSON object per line.

:::note[`ocsf` is OCSF v1.8.0 API Activity]
The earlier editions of this page noted that the engine's error text omitted
`ocsf` from the advertised list — that gap was fixed upstream; the summary and
the bad-request message are both built from the engine's own format registry, so they always name every accepted format.
:::

### Incremental tailing with a cursor

The export pages the gap-free chain by sequence number via `?from=`. To keep a file
continuously appended for the UF to tail, run a small scheduled job that resumes from
the last sequence it saw:

```bash
#!/bin/sh
# cron: every minute. Appends only new ledger records since last run.
STATE=/var/lib/olivares-export/last_seq
OUT=/var/log/olivares/audit.cef
FROM=$(cat "$STATE" 2>/dev/null || echo 1)

curl -fsS "https://localhost:8443/v1/audit/export?format=cef&from=$FROM" \
  -H "Authorization: Bearer $OLVK_TOKEN" -H "X-Olivares-Tenant: $TENANT" \
  | tee -a "$OUT" \
  | sed -n 's/.*olivares-audit-export-complete .*last_seq=\([0-9]*\).*/\1/p' \
  | tail -1 > "$STATE.next" && [ -s "$STATE.next" ] && mv "$STATE.next" "$STATE"
```

Each export ends with a completion terminator — a
`# olivares-audit-export-complete count=N last_seq=M` comment for the text formats,
or an `{"export_complete":true,...}` JSON line for
`otlp`/`otlp_envelope`/`otlp_log_record`/`ocsf`. **Its absence means
the stream was truncated** — do not advance the cursor if it is missing.

## Point the Universal Forwarder at the file

Whichever stream you chose, install a Splunk UF on the host and add a
`monitor://` input. No `inputs.conf` ships with Olivares AI — this is the stanza you
add:

```ini
# $SPLUNK_HOME/etc/system/local/inputs.conf
[monitor:///var/log/olivares/audit.cef]
disabled = false
sourcetype = cef
index = olivares_audit

# For the findings file written by the filelog connector:
[monitor:///var/log/olivares/findings.json]
disabled = false
sourcetype = _json
index = olivares_findings
```

The UF forwards over S2S to your indexer; Olivares AI never speaks S2S itself.

## Summary of what is and isn't supported

- **Supported:** File-tail forwarding (UF tails a file) — for both streams.
- **Supported:** Splunk HEC push — for the findings stream (`splunkhec`
  destination) **and** for the ledger and findings via an eventing **sink**
  (`sink_kind: splunk_hec`, events `audit.recorded` / `finding.reported`,
  at-least-once) — see [push to your SIEM](/how-to/cookbook/push-to-siem/).
- **Supported:** Offline ledger re-verification — both the pull export and the push pump
  carry the hash-chain fields verbatim, so a SIEM can re-verify integrity.
- **Not supported:** Native Splunk S2S emitter — not implemented (post-v1).
- **Not supported:** Automatic ledger *file* sink — to get the ledger into a local file you
  materialize it with the scheduled pull export above (the push pump targets HTTP
  sinks, not files).
