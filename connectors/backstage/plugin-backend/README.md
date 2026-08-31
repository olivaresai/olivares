<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: Apache-2.0
-->

# Olivares portal proxy — Backstage backend plugin

`@olivaresai/backstage-plugin-olivares-backend` is the server half of the
Olivares-in-Backstage integration. It mounts a **read-only** proxy at
`/api/olivares` that the [frontend plugin](../plugin-frontend) calls to render the
agent **inventory**, **live sessions**, and the **R/RW access-map + drift** inside
the portal — without ever exposing a control-plane credential to the browser.

It complements, and does not replace, the [catalog entity provider](../README.md):
the entity provider publishes the Olivares estate **into** the Software Catalog;
this plugin serves the **live, governed data** the frontend renders.

> **Release status:** this is a private source scaffold, not a published or
> installable package. Its framework-free tests pass, but its Backstage package
> build is not yet green. The wiring below describes the intended host integration
> after a local packaging pass.

## Why a backend plugin (and not a direct browser call)

A Backstage frontend cannot safely hold a control-plane bearer token, and a
browser cannot reach a private control plane across origins. This plugin is the
seam that makes the integration both **real** and **safe**:

- **Portal authentication gate.** Every proxied route requires a signed-in Backstage **user**
  (`httpAuth.credentials(req, { allow: ['user'] })`). An anonymous or service
  principal is refused before any upstream call. This plugin does not make a
  Backstage permission/RBAC decision beyond requiring a user credential.
- **Shared control-plane authorization.** The read is forwarded with a configured
  service token kept on the server. The control plane enforces that token's
  permissions and audits its principal; every admitted portal user therefore shares
  the same upstream scope. The browser never sees the token.
- **Advisory caller header.** When enabled, the plugin forwards the portal user's
  entity ref as `X-Olivares-On-Behalf-Of`. The v26.8.0 control plane does not consume
  this header, so it neither changes authorization nor attributes the ledger entry to
  the portal user.
- **Read-first by construction.** Only the `GET` routes in the closed allow-list
  (`src/allowlist.ts`) are forwarded; any other path is `404` and any non-`GET`
  is `405`. Nothing here mutates Backstage or the Olivares estate.

## 1. Configure the control-plane connection (`app-config.yaml`)

The proxy reads the **same** `olivares.*` block the catalog entity provider uses,
so the connection is configured once. The token is read from an **environment
placeholder** — never inline the literal.

```yaml
# app-config.yaml
olivares:
  baseUrl: ${OLIVARES_BASE_URL}      # e.g. https://olivares.example.com
  token: ${OLIVARES_TOKEN}           # bearer; sent upstream as Authorization: Bearer
  tenant: ${OLIVARES_TENANT}         # optional; sent as X-Olivares-Tenant
  attributeOnBehalfOf: true          # optional (default true); advisory header only
  timeoutSeconds: 30                 # optional (default 30)
```

## 2. Intended backend wiring (after packaging)

```ts
// packages/backend/src/index.ts
import { createBackend } from '@backstage/backend-defaults';

const backend = createBackend();
backend.add(import('@backstage/plugin-catalog-backend'));

// The Olivares portal proxy (this package).
backend.add(import('@olivaresai/backstage-plugin-olivares-backend'));

// (Optional) the catalog entity provider that publishes the estate into the catalog.
backend.add(import('@olivaresai/backstage-plugin-catalog-backend-module-olivares'));

backend.start();
```

The plugin id is `olivares`, so the router is reachable at `/api/olivares`.

## 3. The proxied read surface

Every path maps 1:1 to a documented control-plane read endpoint:

| Backstage path                                | Control plane (`olivares.baseUrl`)        |
| --------------------------------------------- | ----------------------------------------- |
| `GET /api/olivares/health`                    | (local liveness; no upstream)             |
| `GET /api/olivares/whoami`                     | `GET /v1/auth/whoami`                      |
| `GET /api/olivares/agents`                     | `GET /v1/agents`                          |
| `GET /api/olivares/access-edges`               | `GET /v1/access-edges`                    |
| `GET /api/olivares/inventory/summary`          | `GET /v1/m/inventory/summary`             |
| `GET /api/olivares/inventory/entities`         | `GET /v1/m/inventory/entities`            |
| `GET /api/olivares/sessions/live`              | `GET /v1/m/sessions/live`                 |
| `GET /api/olivares/sessions/live/:ref`         | `GET /v1/m/sessions/live/{ref}`           |
| `GET /api/olivares/sessions/live/:ref/timeline`| `GET /v1/m/sessions/live/{ref}/timeline`  |
| `GET /api/olivares/access-map/graph`           | `GET /v1/m/accessmap/graph`               |
| `GET /api/olivares/access-map/neighbors`       | `GET /v1/m/accessmap/neighbors`           |
| `GET /api/olivares/access-map/drift`           | `GET /v1/m/accessmap/drift`               |

Only the query parameters each control-plane endpoint documents are forwarded
(`src/allowlist.ts`); anything else is dropped. Path parameters (`:ref`) are
validated and URL-encoded so a hostile reference cannot escape its segment.

## Tests

The forwarding logic — the allow-list, the upstream URL/header builders, and the
config parser — is framework-free and unit-tested with Node's built-in test
runner (no Backstage runtime needed):

```bash
pnpm test    # tsc -p tsconfig.test.json && node --test
```

## Version caveat

Like the catalog module, this package does **not** pin a Backstage version: the
`@backstage/*` dependency is a peer (`*`) so the plugin adopts the host's version.
Validate against your Backstage release before rolling out.
