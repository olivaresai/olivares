# ADR-0009: Append-only, hash-chained, signed audit ledger

- **Status:** accepted
- **Date:** 2026-06
- **Deciders:** Olivares AI
- **References:** API/authz/audit contract (§6, decision §13.4); threat model (ledger)

## Context and problem statement

The audit ledger is one of the product's most sensitive assets: if it can be silently
altered, the product lies. It must make tampering detectable and support external,
verifiable copies — while being honest about what on-host integrity can and cannot
guarantee.

## Decision drivers

- Tamper-evidence: a rewrite of history must be detectable.
- Off-box verifiability for compliance and incident response.
- No new storage subsystem for checkpoints.

## Considered options

- **Append-only + hash-chain + Ed25519-signed checkpoints**, with export to an external
  WORM/SIEM copy.
- **A plain audit table** with application-level controls.

## Decision outcome

Chosen option: an **append-only, hash-chained ledger**; a checkpoint is itself a signed
audit event (Ed25519, detached signature), so rewriting pre-checkpoint history is
cryptographically detectable. The ledger exports to external SIEM/WORM formats (CEF,
LEEF, syslog, OTLP — a complete, postable export request, with the
bare-LogRecord projection as its own `otlp_log_record` export token — OCSF), each
record carrying the chain fields so a SIEM can re-verify offline; PII is never
exported.

### Consequences

- **Good:** tamper-evidence with no separate checkpoint table; offline re-verification;
  SIEM-ready export.
- **Bad / trade-offs:** the on-disk signing key does not stop a host root / DB
  superuser — so the **external WORM/SIEM export is the real anti-tamper control**, and
  the docs say so.
- **Neutral:** the export was pull-based when this decision was taken; an automatic
  push-forwarder seam existed but was not yet implemented.

  > **Status amendment, 2026-07-25.** The push forwarder is implemented and wired:
  > `modules/siemforward` satisfies `audit.Forwarder` and `cmd/olivares/boot.go` starts a
  > per-tenant ledger pump when an `audit.recorded` eventing subscription exists,
  > delivering each sealed record at-least-once over the durable transport.
  > `NopForwarder` is what applies when forwarding is not configured. The pull export
  > remains available and unchanged. The decision itself is untouched; only this status
  > note is added, because the ADR records what was decided, not what shipped later.

  > **Status amendment, 2026-07-28.** "A SIEM can re-verify offline" above was, when this
  > decision was taken, true only of chain LINKAGE and a checkpoint signature: the
  > projections did not carry the commitment to an event's metadata, so a record's own
  > hash could not be re-derived from an exported line. It can now — every input the
  > chain hash consumes travels on every dialect, the metadata commitment included — and
  > that commitment is BLINDED per record, so completing the preimage discloses nothing
  > about the metadata behind it. Three claims stay distinct and this ADR's sentence
  > covers only the first: preimage recomputation, NOT authenticity (an externally
  > trusted key), NOT completeness (adjacent records and a checkpoint).
  >
  > Two consequences belong on the record. Both metadata hash rules are now permanently
  > live, discriminated per row by a stored blind, because an append-only ledger cannot
  > restate the hash rule of rows it has already sealed without making a legitimate
  > history indistinguishable from a forged one. And the archive format gained a version
  > to carry the blind, with the previous version accepted permanently, for the same
  > reason: an artifact written to be read years later cannot have a version retired
  > under it. The decision itself is untouched.

## Why the alternatives were rejected

- **Plain audit table** — provides no cryptographic tamper-evidence; unacceptable for a
  security product whose ledger integrity is "everything".
