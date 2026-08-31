---
title: "Troubleshooting (symptom → diagnosis → fix)"
description: >-
  The operator's failure-mode guide, distilled from the product's own
  runbooks: boot and first-run problems, readiness failures, ingest
  backpressure, ledger verification failures, and the warnings the engine
  prints on purpose.
---

Each entry follows the same shape: the symptom you see, how to confirm what
it is, and the fix. The log lines quoted are the engine's actual strings, so
you can grep for them. Where a deeper runbook exists, the entry links the
relevant page instead of re-deriving it.

## First boot and setup

### I missed the setup token

A restart does **not** re-print it (only the token's hash is stored, in
`setup.token` in the data dir). While no users exist yet the recovery is
safe: stop the engine, delete `setup.token`, start it — a new token is
minted and printed. This works *only* on an install with no users, so it is
not a takeover path. The token goes to **stdout only** (the journal under
systemd, the container log on Docker/Kubernetes) — never to log files.

### `=== FIRST-BOOT SETUP ===` never appeared

Users already exist in that data directory — you are not on first boot.
Either log in with the existing administrator or, for a genuinely fresh
start, use a fresh `--data-dir`.

### The engine warns about keys on first boot

```text
generated a new audit signing key; back it up path=/var/lib/olivares/audit-signing.key
generated a self-signed TLS certificate; clients must trust it, or pin it with --pin-sha256=<pin_sha256> (that value, verbatim) cert=/var/lib/olivares/tls.crt cert_fingerprint_sha256=d38567e8…378c4e7f pin_sha256=JsdrhrY77Me8miAmobJsqamE3NDWIOSBrDTwbHkyCD0
```

Both are deliberate, and the first is the one that bites later: there is
**no enforced escrow** — copy `audit-signing.key` off-box now, and pin the
public key (`GET /v1/audit/pubkey`) off-box, or a future host compromise
leaves you unable to prove your own ledger
([backup & restore](/how-to/backup-and-restore/#the-two-keys-that-decide-everything)).

The TLS line prints **two** digests, and they are not interchangeable:
`cert_fingerprint_sha256` is the certificate digest, the one a browser shows;
`pin_sha256` is the leaf SPKI digest, and it is the only one `--pin-sha256`
compares. Copy that value verbatim:

```bash
olivares status --server https://127.0.0.1:8443 \
  --pin-sha256 JsdrhrY77Me8miAmobJsqamE3NDWIOSBrDTwbHkyCD0
```

Pinning the certificate fingerprint instead does not fail as a bad flag value —
it is a well-formed 32-byte digest, so the connection is attempted and refused
with `TLS SPKI pin mismatch`, which reports the value you should have used.
With `curl --pinnedpubkey sha256//…` add the trailing `=` padding: the engine
prints unpadded base64 on purpose, so the value renders unquoted in the log and
survives a copy-paste, but curl requires the padded form.

## Sources and the access map

### The map is empty

First check whether anything is wired. The engine says so explicitly at
boot:

```text
ingest: no observation sources configured (OLIVARES_SOURCES_CONFIG.sources is empty); no connector will ingest — the estate runs on no live traffic
```

A missing, unreadable or invalid sources file **warns and continues** (boot
never crashes on it) — so a healthy-looking engine with an empty map usually
means the config never loaded. Fix the file/path and restart; success looks
like `ingest: wired source … kind=…` per source. A source that fails to
construct logs `ingest: failed to register in-process source; not wired`
with the reason — it is reported, never silently dropped.

### pgAudit is wired but no edges arrive

Three causes cover nearly every case, all by design
([the pgAudit guide](/how-to/connectors/pgaudit/)):

1. **The server does not log in UTC.** Records with a non-UTC zone
   abbreviation are **skipped** rather than mis-timestamped — set
   `log_timezone = 'UTC'`.
2. **csvlog is batch, not tail.** `follow` applies to `jsonlog` only; a
   csvlog source ingests on each pass, not continuously.
3. **The audited classes are off** — check `pgaudit.log` includes
   `read, write`.

### Everything shows as drift

Expected on a fresh install: with no grants declared, every observed access
is honestly "unexpected". That is the starting state, not a bug —
[triage it](/how-to/cookbook/drift-triage/) by declaring the grants you
intend.

## Availability

### `/readyz` returns 503

Read the body — it distinguishes the two cases:

- `{"status":"unavailable","store":"down"}` — the store is unreachable. On
  SQLite: disk full, PVC problems, file permissions. On Postgres:
  reachability and credentials. **Liveness deliberately keeps passing** (the
  process is alive), so nothing restart-loops on a store outage; restart the
  pod/service manually after fixing the store if it stays wedged.
- `{"status":"standby","leader":false,…}` — an HA standby answering
  honestly. Not an error: the Service routes to the leader; standbys drain
  by design. If **all** replicas report standby, the leader election is
  stuck — check Postgres advisory-lock connectivity.

### The pod died and nothing took over

On the **default single-replica** topology there is no automatic failover —
recovery is the StatefulSet rescheduling plus the RWO volume re-attaching
(watch for Multi-Attach errors; the volume pins recovery to its AZ).
Automatic failover is a property of the
[HA topology](/tutorials/getting-started/kubernetes/#3-active-passive-ha)
(Postgres + replicas + shared signing key). Never run production with
persistence disabled: an `emptyDir` loses the signing key on every
reschedule.

## Performance

### Ingest latency p99 is rising (backpressure)

The bus **blocks rather than drops** — rising
`olivares_ingest_duration_seconds` p99 is the designed signal that a
subscriber is saturated, not data loss. Name the culprit directly:

```promql
olivares_eventbus_queue_depth / olivares_eventbus_queue_capacity > 0.9
```

The per-subscriber labels point at the slow module;
`olivares_eventbus_publish_blocked_total` counts the backpressure events.
The usual root cause is **store write throughput** (the SQLite single-writer
ceiling) — that is a capacity fix (move to Postgres, or reduce write
amplification), not a tuning knob. Slow output connectors (a webhook, a
SIEM) must never be synchronous subscribers.

With the distributed bus enabled (`OLIVARES_BUS_CONFIG`), remember the
cross-node bridge is **at-most-once**: a saturated bridge fills
`olivares_eventbus_bridge_pending_messages` and then **drops remote events**,
counted in `olivares_eventbus_bridge_dropped_total` — alert on any increase,
and page when `olivares_eventbus_bridge_connected == 0`.

### Logins fail with "locked out"

`olivares_auth_login_attempts_total{outcome="locked_out"}` rising means the
per-account/per-IP throttle engaged after repeated failures. It clears
itself; investigate the source of the failures rather than raising limits.

## Evidence

### The ledger fails verification

First, know what you ran: the default `audit verify` **exits 0 even on a
failed chain** (the result is in the JSON report) — automation must use
`--strict` or parse the report:

```bash
olivares audit verify --tenant $TENANT --data-dir /var/lib/olivares --strict \
  --pubkey <BASE64-PINNED-OFF-BOX>
```

Pin the **off-box** public key: with no pins the verifier trusts keys read
from the (possibly compromised) host — fine as an advisory check, not as
tamper evidence. Then classify by the `reason` field:

| Reason | Class | Response |
|---|---|---|
| `hash-mismatch`, `prev-mismatch`, `head-mismatch`, `tail-truncated` | tampering or truncation | treat as a SEV1: preserve the box, reconcile against the off-box checkpoint |
| `checkpoint-sig-invalid`, `checkpoint-link-mismatch`, `event-sig-invalid` | tampering or wrong key | SEV1 unless you can prove a key-custody mix-up |
| `seq-gap` | deletion **or** a restore inconsistency | compare against the off-box checkpoint before crying tamper |
| `event-sig-missing` | possibly legacy records from before signing was enabled | bound it with `--from` at the enablement boundary; pre-boundary absence is expected |

A restored backup that passes a naive walk but disagrees with your pinned
off-box checkpoint is the restore-anomaly case — that comparison is why the
pin exists.

### `olivares_audit_checkpoint_age_seconds` keeps growing

Checkpoints have stopped being written (default cadence 1h;
`OlivaresAuditCheckpointStale` fires at 2h). Check the engine log for
checkpoint errors and the store's writability — while it grows, your
tamper-evidence anchor ages.

## Notifications and sinks

### A destination never receives anything

A destination with an unknown kind is **skipped and logged**
(`notify: destination has unknown connector kind; skipped` — check the
`kind` spelling). For eventing sinks, `POST …/subscriptions/{id}/test`
sends a delivery you can watch, and endpoints must be HTTPS
([push to SIEM](/how-to/cookbook/push-to-siem/)).

---

If a symptom is not here and the engine's own message does not explain it,
that is a documentation bug — please report it with the log line.
