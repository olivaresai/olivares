---
title: Cloud customer authority
description: >-
  How Olivares AI binds a portal organisation to a commerce provider customer,
  how the Cloud page reads that binding, and what provision, suspend and delete
  mean. Cloud payment stays off until operators turn the forwarder on.
---

A Cloud deployment is not “the email that paid”. The control plane stores a
tenant under an exact commerce pair `(provider, customer id)`. The customer
portal stores people and organisations. Those two worlds only meet when the
license Worker **signs a binding** and keeps it in D1.

This page describes that model. It is an explanation, not a go-live runbook.
Cloud **payment forwarding stays off** (`CLOUD_FORWARD_ENABLED` is `"false"`).
A catalogue that sells Cloud SKUs while the forwarder is off is still named
CFG-12 and still refuses to pretend the tenant exists.

## What a binding is

A **Cloud customer binding** is a row the Worker writes after an authenticated
owner or admin of a portal organisation asks to bind a pair that organisation
already holds on its commerce account:

| Field | Meaning |
|---|---|
| Organisation id | Portal organisation selected in this session |
| Provider | `dodo` or `polar` — the closed set the control plane accepts |
| Provider customer id | Opaque merchant id, exact bytes, not normalised |
| Signature | Ed25519 over a canonical payload, Worker `LICENSE_SIGNING_KEY` |
| Revision | Integer ≥ 1. Identity columns do not move after insert |

The Worker corroborates the pair against `portal_customer_accounts` for **this**
organisation. The request body is not ownership. A pair already bound to another
organisation is refused.

Create and read:

- `POST /portal/cloud/bindings` with header `Idempotency-Key`
- `GET /portal/cloud/bindings` and `GET /portal/cloud/bindings/{id}`

The same `Idempotency-Key` with the same body replays the stored row. A
different body is `409`.

## How the Cloud page answers

The public wrapper `fetchCloudTenants` has four states. They are not the same
sentence:

| State | Means | What the page must not say |
|---|---|---|
| `ok` with tenants | The scoped control plane answered this pair | — |
| `ok` with none | We asked, and there are none | — |
| `not-configured` | No plane, or this organisation has no verified binding | “You have no deployment, buy one” |
| `unreachable` | The plane could not be asked to completion | “You have no deployment” |
| `integration-pending` | Legacy email or a raw array was passed. That is not a binding | A completed dashboard |

Only a **minted** binding set — an object the Worker module created after
verifying signatures — causes a request to `GET /portal/tenants`. The portal
read key authenticates the Worker service. It does not select the organisation.

## Provision, suspend, delete

These are explicit operations on a binding, not a payment webhook:

1. **Provision** — ask the control plane to stand up a tenant for the bound pair.
2. **Suspend** — existing admin route `POST /admin/tenants/{id}/suspend`.
3. **Delete** — existing admin route `POST /admin/tenants/{id}/delete`.

Each call carries `Idempotency-Key`. The Worker stores the 2xx result and
replays it. Mutations use `CLOUD_CP_API_KEY`, never the portal read key.

The HTTP provision contract is `POST {CLOUD_CP_BASE_URL}/admin/tenants`. The
control plane you run today may still create tenants only from a commerce
webhook. In that case the HTTP client reports an unreachable plane. Tests use a
Fake client so the cycle is proven without turning payment on.

## What this is not

- It is not FinOps admission (`Reserve` / `Commit` / `Release`). That is engine
  spend. Cloud tenant lifecycle is a different ledger.
- It is not a self-hosted Connect deployment binding (proof-of-possession on a
  customer host). That is `connect-v1`.
- It is not permission to turn Cloud payment on. CFG-12 remains the guard
  against charging for a tenant nobody provisions through the webhook.

## What is proven, and where

The binding, the lifecycle and the wrapper are **implemented** and **tested**:
the tests run the whole cycle against a Fake control-plane client, so the
idempotency and the replay answers are measured, not asserted in prose. What
they do not measure is a live control plane, and no statement on this page
should be read as one. Cloud sale stays off until an operator sets the
forwarder and the catalogue together.
