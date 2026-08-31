---
title: SIEM & telemetry egress
description: >-
  Every wire format the control plane emits — CEF, LEEF 2.0, RFC 5424 syslog,
  OTLP logs, OCSF 1.8.0, SARIF 2.1.0 — the severity mapping a rule author keys
  on, the receiver limits that apply to each transport, and the two places where
  a projection is not a spec-complete envelope.
---

This page is the **egress contract**: what leaves the control plane, in which
dialect, over which transport, and what a receiver does with it. It is written
for the person who has to make an ArcSight rule, a QRadar DSM, a Sentinel DCR or
a code-scanning upload work on the first try.

Everything here is checked against the vendors' own specifications, with the
date of the check. Where a vendor does **not** specify something, this page says
so rather than guessing — those gaps are marked *undefined by the vendor*, and
the encoder takes the conservative side of each.

## The two feeds

There are two independent sources of records, and they share one encoder so the
dialects cannot drift apart:

| Feed | What it carries | Pull | Push |
|---|---|---|---|
| **Audit ledger** | The append-only, hash-chained ledger with its integrity fields (sequence, previous hash, hash, signature) | `GET /v1/audit/export?format=…` (NDJSON / one record per line) | The ledger forwarder, over any output connector |
| **Notifications & findings** | Governance findings, policy decisions, health and lifecycle events | — | Any output connector |

The ledger's integrity fields ride **verbatim** in every format, so a SOC can
re-verify the chain from the copy in its own SIEM, not just from the product.

## Formats

| Format | Standard | Pinned version | Where it is selectable |
|---|---|---|---|
| CEF | ArcSight Common Event Format | V27 (July 2024) | ledger export, connectors |
| LEEF | IBM QRadar Log Event Extended Format | 2.0 | ledger export, connectors |
| syslog | RFC 5424 (+ RFC 5426 UDP, RFC 6587 TCP framing, RFC 5425 TLS) | — | ledger export, connectors |
| OTLP request (`otlp`) | OTLP/HTTP JSON export request (`ExportLogsServiceRequest`) | see *Projections* below | ledger export, connectors |
| OTLP request (`otlp_envelope`) | Exact byte-for-byte alias of `otlp` | see *Projections* below | ledger export, connectors |
| OTLP LogRecord (`otlp_log_record`) | OpenTelemetry logs, one LogRecord per line | see *Projections* below | ledger export |
| OCSF | Open Cybersecurity Schema Framework, `ai_operation` profile | 1.8.0 | ledger export, connectors |
| ASIM | Microsoft Sentinel Advanced SIEM Information Model | — | connectors |
| ECS | Elastic Common Schema | 9.4.0 | Elastic connector |
| UDM | Google SecOps Unified Data Model | — | Chronicle connector |
| SARIF | OASIS Static Analysis Results Interchange Format | 2.1.0 Errata 01 | findings export |

Each selection surface accepts its own ordered subset of those tokens, derived from
one shared catalog so the lists cannot drift apart:

| Surface | Accepted tokens | Default |
|---|---|---|
| Ledger export (`GET /v1/audit/export?format=…`) | `cef\|leef\|syslog\|otlp\|otlp_envelope\|otlp_log_record\|ocsf` | `cef` |
| Eventing sink (`sink_format` on a push subscription) | `ocsf\|cef\|leef\|syslog\|otlp\|otlp_envelope\|json` | `ocsf` |
| Notification connectors (`filelog`, `splunkhec`, `s3archive`, `siem`) | `json\|cef\|leef\|syslog\|otlp\|otlp_envelope\|ocsf\|asim` | `json` |
| syslog connector | `syslog\|cef\|leef` | `syslog` |

The ledger export has no raw-JSON passthrough — its JSON shapes are the OTLP forms
above. `json` means two different deliveries: the eventing sink posts the raw
captured event envelope (the structured passthrough, no dialect transform), while
the notification connectors render a minimal notification projection — the
displayable fields, not the original payload. All four notification connectors
accept `asim`, `s3archive` included. A format outside its surface's list is
rejected: a typo at authoring or configuration time gets an error naming the
surface's accepted tokens, and a corrupted stored value is refused at encode time
(naming the corrupt spelling, not the list); nothing silently falls back to JSON.

## Severity: the single source of truth

Every rule that filters on severity keys on this table. It is one mapping in one
place, so the CEF number, the syslog priority and the OTLP severity of the same
event can never disagree.

| Product severity | CEF (0-10) | syslog (0-7) | OTLP | ECS | UDM |
|---|---|---|---|---|---|
| info | 1 | 6 (info) | INFO | 1 | INFORMATIONAL |
| low | 3 | 5 (notice) | INFO2 | 3 | LOW |
| medium | 5 | 4 (warning) | WARN | 5 | MEDIUM |
| high | 7 | 3 (error) | ERROR | 7 | HIGH |
| critical | 10 | 2 (critical) | FATAL | 10 | CRITICAL |
| undetermined | 0 (Unknown) | 6 (info) | UNSPECIFIED | 0 | *(omitted)* |

Two properties are enforced by tests, because both are easy to lose by accident:

- **The five determined severities never share a number.** A collector selector
  such as `local0.notice`, an ArcSight rule or a Sentinel DCR filters on the
  emitted number, and the RFC 5424 frame carries no other severity signal — so
  two severities sharing one priority would destroy a distinction silently and
  irreversibly.
- **An undetermined severity is not invented.** CEF V27 relabelled `0` from
  *Low* to *Unknown*, and that is what an event with no determined severity
  gets. (LEEF is the one exception: its range is 1-10 with no value for
  "unknown", so the floor applies. See below.)

:::note[Why the syslog column is what it is]
Neither CEF nor RFC 5424 defines a mapping from CEF severity to syslog priority
— checked against both specifications, 2026-07-24. The syslog column is
therefore **product policy**, chosen so that each severity stays distinguishable
and that "critical" lands on the priority RFC 5424 actually calls *critical*.
The only vendor mapping that exists (a configurable ArcSight connector setting)
also puts its highest band on `crit`. If you have standardised on a different
banding, map it at your collector — the numbers above will not move under you
without a `Changed` entry in the changelog.
:::

## CEF specifics

- **Header sizes** are bounded to the V27 maxima: device vendor 63, device
  product 63, device version 31, event class id 1023, name 512.
- The specification publishes those numbers but never says whether they count
  **characters or wire octets**, and defines no behaviour for an over-length
  field (*undefined by the vendor*, checked 2026-07-24). Both readings are
  therefore honoured: a value is bounded to the number in decoded characters
  **and** in UTF-8 octets on the wire. A non-ASCII device name or event name
  fits fewer characters than the number suggests — the conservative direction.
- Truncation applies to the **header only**. The extension, which carries the
  auditable content, is never truncated.
- Time-valued extension keys (`rt`, `start`, `end`) are decimal **epoch
  milliseconds**, as the CEF dictionary requires.

## LEEF specifics

- `sev` is an integer in LEEF 2.0's documented **1-10** range. An event whose
  severity was never determined is emitted as `sev=1`: LEEF has no "unknown"
  value, and `sev=0` is outside the range.
- `devTime` is a **13-digit epoch**, which QRadar accepts without a
  `devTimeFormat`. It is **omitted** — never fabricated — for an event with no
  recorded time, and QRadar then falls back to receipt time, as documented.
- `sev`, `devTime` and `devTimeFormat` are **owned by the encoder**. If an event
  carries a field with one of those names (in any capitalisation), it is emitted
  re-keyed as `olvSev` / `olvDevTime` / `olvDevTimeFormat`: the value still
  reaches you, but it cannot override the normalised severity or re-date the
  event. IBM documents that a recognised `devTime` outranks the syslog
  timestamp, which is why this is not left to chance.

:::caution[Undefined by IBM]
IBM does not document what QRadar does with `sev=0`, with an unparseable
`devTime`, or whether attribute keys are matched case-sensitively (checked
2026-07-24). The behaviour above is the conservative reading of each. If you
have receiver evidence to the contrary, it is worth an issue.
:::

## syslog transport and receiver limits

The syslog connector carries a native RFC 5424 record, or a CEF / LEEF record as
the MSG of a spec-correct RFC 5424 frame — which is how ArcSight and QRadar
ingest those formats over syslog.

- **TLS on 6514 (RFC 5425) is the default**, with octet-counting framing as the
  RFC requires. Cleartext TCP or UDP is an explicit operator opt-out; no code
  path downgrades a TLS destination to cleartext.
- **Receiver payload budget** (`max_payload_bytes`, default `0` = off). A
  receiver that splits an oversize record turns one auditable event into two
  unparseable halves. When you declare the budget of the destination you run, a
  record over it **fails the delivery** — retried, then dead-lettered, where you
  can see it — instead of being sent to be split. The record itself is never
  truncated.

Reference values for that setting, with what each source actually says (checked
2026-07-24):

| Receiver | Bytes | What the source says |
|---|---|---|
| Any RFC 5424 receiver | 480 | The minimum a receiver **MUST** support (§6.1) |
| Any RFC 5424 receiver | 2048 | The size implementations **SHOULD** support |
| ArcSight syslog daemon | 1024 | Its guides say a longer message **"might be split"** — a deployment caveat, not a receiver rule, and it does not apply to the file or pipe paths |
| QRadar TCP | 4096 | The **default** maximum payload; raisable (IBM documents 8192, with 32000 as the ceiling) |

None of those sources defines whether the count includes the syslog header, so
the budget is measured on the **complete record** in UTF-8 octets.

## OCSF

Events are emitted as OCSF **1.8.0** with the `ai_operation` profile, in the
three classes that register it: API Activity (6003, the default), Process
Activity (1007) and Datastore Activity (6005). Output is validated in the test
suite against the official 1.8.0 class schemas, which forbid unknown fields — so
an un-profiled attribute or an incomplete profile object fails the build rather
than reaching you.

:::caution[AWS Security Lake accepts OCSF ≤ 1.3]
A Security Lake custom source caps at **OCSF 1.3 in Parquet**, so 1.8.0
`ai_operation` events do **not** land there as-is (checked 2026-07-24). Until a
1.3-shaped downgrade emitter exists, route to Security Lake through a
transformation of your own, or use one of the other destinations. This is a
declared gap, not an oversight.
:::

## Projections that are not envelopes

Two honest limitations, both worth knowing before you point a collector at them:

- **`otlp` is the postable request on every surface; `otlp_log_record` is the bare
  projection.** Since the format-catalog remap, an `otlp` EVENT line is a complete
  OTLP/HTTP JSON export request (`ExportLogsServiceRequest`) wherever the token is
  accepted — ledger export, output connectors, eventing push — with the resource
  identity and instrumentation scope a collector needs. `otlp_envelope` is an exact
  byte-for-byte alias of `otlp` on every surface, kept because that spelling shipped
  the envelope first — the two never differ. The one-LogRecord-per-line projection —
  one JSON object per line, for file and NDJSON consumption — still exists under its
  own honest name, `otlp_log_record`, and only on the ledger pull export: a bare
  LogRecord line is not a postable `/v1/logs` body, so the push surfaces deliberately
  do not offer it. Three specifics, because they cost an afternoon otherwise: the
  pull file's LAST line is Olivares' `{"export_complete":true,…}` marker and is
  **not** a request, so a loop that posts every line must skip it — filter
  STRUCTURALLY, e.g. `jq -c 'select(has("resourceLogs"))'`, never by substring: an
  event whose actor or target happens to contain `export_complete` would be dropped
  by a `grep -v`, and that is evidence deleted, not a marker skipped; a push sink
  must point at the collector's exact `/v1/logs` URL, since the endpoint is posted
  verbatim; and the generic HTTPS sink reports any 2xx as delivered without reading
  the collector's partial-success response — the dedicated **OTLP logs connector**
  does read it. `otlp_log_record` carries the exact bytes the pre-remap `otlp` token
  produced across the normal timestamp domain — the zero time, and any instant from
  the epoch through `2262-04-11T23:47:16.854775807Z`. Outside it byte compatibility
  is NOT guaranteed, and where the bytes differ it is a correction: a pre-epoch date
  previously became a negative value in a field OTLP declares unsigned, a date
  between the signed and unsigned ceilings now carries its true unsigned value, and
  a date past `2554-07-21T23:34:33.709551615Z` now encodes as unknown (`0`) instead
  of a wrapped value — including the small positive ones that read as early 1970. At
  isolated wrap-to-zero inputs the old and new bytes happen to coincide. Two upgrade
  notes stated plainly: the pull *file* is still NDJSON (one request per line plus a
  completion marker), not one request; and a stored eventing subscription whose
  format was spelled exactly `otlp` before the remap now delivers the envelope where
  it used to deliver a bare line — the engine logs one structured warning per such
  subscription, and audit metadata recorded before the remap reads under the token's
  old meaning.
- **The OWASP Agentic AI Security trace extension** ships under OCSF's
  `unmapped` container, which is the placement its (v0.1 public preview)
  specification prescribes. It is not a first-class OCSF attribute set, and
  schema validation covers only its placement.

## Findings as SARIF

Governance findings export as **SARIF 2.1.0 Errata 01** for a code-scanning
consumer:

- `GET /v1/m/security/findings/export?format=sarif` — the same filters as the
  findings list, with a result cap and an honest truncation header when it is
  reached.
- `olivares findings export` — the same export from the CLI, written atomically
  with `0600` permissions.

The run declares the URI base its result locations resolve against, carries a
stable `partialFingerprints.primaryLocationLineHash` per finding so a consumer
de-duplicates instead of re-alerting, and refuses to emit a result with an empty
rule id or an out-of-enum level — those are the two things that make a consumer
reject the whole file, and finding out at upload time is worse than finding out
here.

Findings whose subject is not a committed file get a synthetic location URI. The
run stays valid and ingestible, but GitHub renders alerts only for URIs that
match a file in the checkout — so a detector that wants GitHub anchoring should
set the artifact URI explicitly.
