---
title: Rehearse the commercial cycle
description: >-
  Run the sandbox purchase → licence → download → renewal → cancel → refund
  journey as tests. Production purchase and refund stay a founder step.
---

A paid Business purchase is a chain: Dodo sends webhooks, the license Worker
issues a signed credential, the buyer downloads the commercial artifact, a
renewal extends the term, a cancel stops the next renewal, and a refund ends
that payment at once.

This page is the **rehearsal**. It runs against recorded Dodo fixtures and a
local Worker. It does not charge a card. It does not touch production.

:::note[Production purchase and refund stay a founder step]
Phase G of the go-live runbook is a production purchase plus a production
refund. Nothing on this page runs that. Sandbox only.
:::

## What the cycle asserts

| Step | Worker | Engine (`olivares license`) |
|---|---|---|
| Purchase (`payment.succeeded`) | Ledger row `held_waiting_cohort` | — |
| Issuance (`subscription.renewed`) | Licence row `active`, signed blob, grants | `verify` / `install` accept the blob |
| Download token | Mailed `/download?token=` | — |
| Download | 200 for manifest and artifact | — |
| Renewal | Second issuance on the same holder | — |
| Cancel (`subscription.updated` cancelled) | Projection only. Paid term stays. CRL empty | Blob still verifies |
| Refund (`refund.succeeded`) | That payment terminates. Download 403. CRL empty | Blob still verifies (see below) |

## Who runs it, and where

The licence Worker is part of the commercial distribution and is not in this
repository, so the rehearsal runs where that Worker lives: whoever holds it
executes the journey as a Node test and as a standalone script, against
recorded Dodo bodies and a local database.

The standalone form also probes the deployed sandbox with GET `/health` and
GET `/crl`. It never POSTs a signed purchase there: that ledger is
append-only and its webhook secret is not in any repository.

The engine half needs no Worker and you can run it from this repository. It is
the same pair of commands the journey issues once a binary is on disk:

```sh
olivares license verify "$BLOB" --pubkey "$LICENSE_PUBLIC_KEY"
olivares license install ./customer.license --data-dir ./licdata --pubkey "$LICENSE_PUBLIC_KEY"
```

A blob the rehearsal issued is signed with its own test key, not the engine's
embedded development key, so `--pubkey` is required for it.

## Two channels, not one

A **refund** ends the right in the Worker registry. The gated download
refuses. The signed blob on disk does not change.

A **CRL** is a security list. It carries signing-key-compromise only. The
engine reads it from an OTA-signed channel manifest (`--manifest`), not from
`GET /crl`. Dodo has no `subscription.revoked` event. A cancel keeps the paid
term.

So `olivares license verify` after a refund still reports `valid` unless an
OTA manifest names that serial. That is the commercial CRL rule, not a missed
check.

See [Install a license](/how-to/install-a-license/) for what a buyer does with
the file.
