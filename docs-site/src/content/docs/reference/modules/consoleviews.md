---
title: "Saved console views"
description: >-
  Named, shareable snapshots of console view state — filters, ranges, scopes —
  stored server-side per tenant. Save an investigation, share it with the team.
  What the module stores, its ownership and sharing rules, and its honest limits.
---

The `consoleviews` module gives the console **saved views**: a named snapshot of
a view's state — the same filters, time ranges and scopes the console encodes in
the URL — stored **server-side per tenant**, so an investigation like *"failed
admissions, last 24 h"* survives the browser, follows the operator across
machines, and (when shared) is one click away for the whole team.

## What it stores — and what it never stores

A saved view is **parameters only**: a size-capped JSON object (max 4 KB)
holding the view's URL-state, plus a name, an optional description, the owning
principal and a `shared` flag. The module **never stores query results, ledger
rows, or any data the parameters would select** — loading a saved view re-runs
the underlying query under the caller's own permissions. The console treats
stored parameters strictly as data.

## Ownership, sharing, and who can do what

- **Create/update** — any member with `consoleviews:view:write` (editor tier).
  A view belongs to the principal who created it; only the owner can edit it.
- **Visibility** — the owner always sees their own views; a view marked
  `shared` is visible to every tenant member with `consoleviews:view:read`
  (viewer tier). A view you may not see answers `404`, never a `403` —
  visibility does not leak existence.
- **Delete** — the owner, or a tenant **admin/owner role** for any view (the
  cleanup power for views left behind by departed users).
- **Caps** — 200 views per owner, 2000 per tenant, refused with a clear message
  when reached. `(feature, owner, name)` is a natural key: saving a duplicate
  name on the same feature answers `409`.

Every create, update and delete is recorded in the tenant's audit ledger,
attributed to the real principal — the recorded metadata identifies the view
(feature, name, shared flag), never its parameters.

## Routes

| Method | Route | Permission |
|---|---|---|
| `GET` | `/v1/m/consoleviews/views?feature_id=` | `consoleviews:view:read` |
| `GET` | `/v1/m/consoleviews/views/{id}` | `consoleviews:view:read` |
| `POST` | `/v1/m/consoleviews/views` | `consoleviews:view:write` |
| `PUT` | `/v1/m/consoleviews/views/{id}` | `consoleviews:view:write` |
| `DELETE` | `/v1/m/consoleviews/views/{id}` | `consoleviews:view:write` |

Module routes are part of the **beta** surface — see the
[module-route reference](/reference/api-beta/).

## Honest limits

- The server validates a view's `feature_id` as a slug but does **not** pin the
  console's feature list — the console registry is the authority and changes
  per release; the console ignores saved views for features it no longer has.
- A shared view shares **parameters**, not results: two operators loading the
  same saved view can see different data if their permissions differ. That is
  by design — sharing never widens access.
- Saved views are console furniture, not evidence: they live outside the
  ledger's chain (only their lifecycle events are evidenced).
- A **workspace-confined** operator can read saved views but cannot create,
  edit or delete them: the scoped-grant engine forbids collection-level writes
  for confined principals (fail-closed), and the tenant-wide admin delete
  override explicitly excludes confined admins.
- The per-owner/per-tenant caps are soft under concurrent writers on Postgres
  (bounded marginal overshoot); duplicate names are always hard-refused.
