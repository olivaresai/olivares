<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: Apache-2.0
-->

# Backstage interop

`@olivaresai/backstage-plugin-catalog-backend-module-olivares` makes the Olivares
AI control plane a first-class citizen of a customer's Backstage developer portal.
It contains two source artifacts:

1. **An entity provider** that publishes the Olivares estate **into** the Backstage
   Software Catalog — Olivares agents become `Component`s, governance identities
   become `Resource`s, and the estate becomes a `System`.
2. **A scaffolder Template** ("Register an agent with Olivares governance") that
   scaffolds a `catalog-info.yaml` for a new agent and registers it in the catalog.

Both are **read-first / declarative** from Olivares' point of view. The provider
reads the control-plane inventory over its read-only REST surface and supplies
catalog entities; the template scaffolds a file in the developer's own repository.
Neither mutates Backstage configuration, and neither mutates the Olivares estate —
creating or acting on an agent in the control plane is a governed, human-in-the-loop
action (module VII), out of scope here.

> **Release status:** this directory is a source scaffold, not a published or
> installable package set. All three `package.json` files are `private: true`, and
> the package builds are not yet green. The package-name snippets below describe
> the intended Backstage integration after a local packaging pass.

## The Olivares × Backstage integration (three packages)

This directory contains the full integration source for the developer portal the platform
buyer already lives in. The three packages are intended to compose after packaging; choose
the ones you need. They share the one `olivares.*` config block:

| Package | Role | What it does |
| --- | --- | --- |
| `…catalog-backend-module-olivares` (this dir) | catalog backend module | Publishes the Olivares estate **into** the Software Catalog (agents → Components, identities → Resources), plus the scaffolder Template. |
| [`…olivares-backend`](./plugin-backend) (`plugin-backend/`) | backend plugin | A **read-only portal proxy** at `/api/olivares` that requires an authenticated Backstage user and forwards reads under a configured control-plane service token — the browser never sees that token. |
| [`…olivares`](./plugin-frontend) (`plugin-frontend/`) | frontend plugin | A **native in-portal UI**: inventory, live sessions, and the R/RW access-map + drift, plus an entity tab/card — rendering the control plane's data, not an iframe. |

The catalog module answers *"what Olivares agents exist?"* in the catalog; the
frontend + backend answer *"what are they doing and what can they touch?"* live
in the portal. The proxy authenticates the portal user, but the configured service
token — not the individual portal identity — determines control-plane authorization
and audit attribution. All three packages are read-first; manage-as-code is a later
milestone.

## 1. Configure the control-plane connection (`app-config.yaml`)

The provider reads its connection from the `olivares.*` config block. The token is
read from an **environment placeholder** — never inline the literal.

```yaml
# app-config.yaml
olivares:
  baseUrl: ${OLIVARES_BASE_URL}      # e.g. https://olivares.example.com
  token: ${OLIVARES_TOKEN}           # bearer token; sent as Authorization: Bearer
  tenant: ${OLIVARES_TENANT}         # optional; sent as X-Olivares-Tenant
  schedule:                          # optional; overrides the refresh cadence
    frequencyMinutes: 30
    timeoutSeconds: 30
```

The provider calls only the documented **read-only** endpoints:

- `GET /v1/agents` — the agent roster (`{ id, name, kind, status, external_id }`).
- `GET /v1/m/governance/identities` — the governance identity roster.

## 2. Wire the entity provider (new backend system)

The package's default export is a new-backend-system module. Add it next to the
catalog plugin in your backend:

```ts
// packages/backend/src/index.ts
import { createBackend } from '@backstage/backend-defaults';

const backend = createBackend();
backend.add(import('@backstage/plugin-catalog-backend'));

// Register the Olivares entity provider.
backend.add(
  import('@olivaresai/backstage-plugin-catalog-backend-module-olivares'),
);

backend.start();
```

Internally the module obtains the `catalogProcessingExtensionPoint` and calls
`addEntityProvider`, giving the provider a `SchedulerServiceTaskRunner` so the
catalog drives the refresh cadence — the provider never owns its own timer. On each
run it reads the inventory and reconciles its catalog bucket with a **full**
mutation (`connection.applyMutation({ type: 'full', … })`): a full inventory
snapshot is a complete reconciliation, so entities that disappear upstream are
removed and none go stale. (`type: 'delta'` is for event-driven upserts; this
provider is a poller, so `full` is the correct posture.)

## 3. Install the scaffolder Template

Point the scaffolder/catalog at `template/template.yaml` (which uses
`template/skeleton/catalog-info.yaml`):

```yaml
# app-config.yaml
catalog:
  locations:
    - type: url
      target: https://your-host/olivares/template/template.yaml
      rules:
        - allow: [Template]
```

The template scaffolds a `catalog-info.yaml`, publishes it to the chosen
repository, and registers it with the catalog. See `examples/catalog-info.yaml`
for what the published entities look like.

## Minimal data

The provider maps only structural identity already exposed by the control plane: an
agent's name, kind, status and optional stable `external_id`, plus an identity's name.
The bearer token is sent solely as an `Authorization` header and is never copied into
an entity; prompt/tool payloads and secrets are not emitted. Names and external IDs can
still be personal or customer-sensitive data and must be governed accordingly. An
entity with no stable name is skipped rather than given a fabricated one.

## Version caveat

Backstage's catalog entity schema (`backstage.io/v1alpha1`) is **alpha** and the
scaffolder Template schema (`scaffolder.backstage.io/v1beta3`) is **beta**. This
package intentionally does **not** pin a Backstage version: the `@backstage/*`
dependencies are declared as peer dependencies (`*`) so the plugin adopts the
host's Backstage version. Validate against your Backstage release before rolling
out, and re-check the `apiVersion`s if you upgrade across a schema change.

## Verification status

This directory is a non-Go Backstage integration scaffold, not a verified
buildable package set yet. On 2026-07-03, `pnpm install` plus explicit
`@backstage/cli@0.36.3` and `@backstage/cli-defaults@0.1.3` dev dependencies
made the backend and frontend framework-free unit tests pass:

- `plugin-backend`: `pnpm run test` passed 20 `node:test` tests.
- `plugin-frontend`: `pnpm run test` passed 26 `node:test` tests.

The package builds are still not green. `pnpm run build` in the root package,
`plugin-backend`, and `plugin-frontend` fails with `No declaration files found at
dist-types/src/index.d.ts`; `backstage-cli package prepack` also showed that the
root package lacks a `tsconfig.json`. Treat this integration as scaffold until a
Backstage packaging pass adds the missing type-generation structure and makes all
three package builds pass.
