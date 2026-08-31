<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: Apache-2.0
-->

# Olivares in Backstage — frontend plugin

`@olivaresai/backstage-plugin-olivares` makes the Olivares AI estate a native
part of the developer portal. It is a **real plugin, not an iframe**: it renders
the control plane's own data with Backstage components, so operating agents and
seeing their R/RW access is part of the platform engineer's existing flow.

> **Release status:** this is a private source scaffold, not a published or
> installable package. Its framework-free tests pass, but its Backstage package
> build is not yet green. The wiring below describes the intended host integration
> after a local packaging pass.

The source implements:

- **A standalone page** (`/olivares`) with three tabs:
  - **Inventory** — the discovered agents, MCP servers, tools and sessions
    (module I), with counts by kind and a searchable catalog table.
  - **Sessions** — live operation (module II): what each agent is doing now, its
    state, tokens and cost.
  - **Access map** — the R/RW graph and the permitted-vs-observed **least-privilege
    drift** (module III), with the security headline up top.
- **Entity integration** — a tab (`EntityOlivaresContent`) and a card
  (`EntityOlivaresCard`) that surface an agent's live sessions and R/RW access
  directly on its catalog Component page.

All of it is **read-only**. It pairs with the
[`olivares-backend`](../plugin-backend) plugin, which holds the control-plane
credential and requires an authenticated Backstage user. The frontend never sees a
token, but control-plane authorization and audit attribution use the backend's shared
service-token principal, not the signed-in user's Backstage identity.

## Honest by construction

The plugin renders the engine's signals and never invents them (the same posture
as the first-party web app, see `ARCHITECTURE.md`):

- Attribution that is only `approximate`/`unknown` is visually de-emphasized and
  **never shown as firm**.
- A reconciliation-**pending** unexpected access is shown amber ("pending"),
  **never as a firm red violation**.
- A `silent_evasion` session state is surfaced (not hidden) as worth the
  operator's eye, and an absent goal/cost shows "—", never a fabricated value.

## Intended host wiring (after packaging)

### 1. Prerequisite

Install and configure the [`olivares-backend`](../plugin-backend) plugin first
(it provides `/api/olivares` and the `olivares.*` config block).

### 2. Register the API + page

```tsx
// packages/app/src/App.tsx
import { OlivaresPage } from '@olivaresai/backstage-plugin-olivares';

const routes = (
  <FlatRoutes>
    {/* … */}
    <Route path="/olivares" element={<OlivaresPage />} />
  </FlatRoutes>
);
```

Add a sidebar item (optional):

```tsx
// packages/app/src/components/Root/Root.tsx
import ExtensionIcon from '@material-ui/icons/Extension';

<SidebarItem icon={ExtensionIcon} to="olivares" text="Olivares" />;
```

### 3. Wire the entity tab + card (optional, recommended)

```tsx
// packages/app/src/components/catalog/EntityPage.tsx
import {
  EntityOlivaresContent,
  EntityOlivaresCard,
  isOlivaresEntity,
} from '@olivaresai/backstage-plugin-olivares';

// A governance tab that only appears for Olivares-managed entities:
const serviceEntityPage = (
  <EntityLayout>
    {/* … existing routes … */}
    <EntityLayout.Route
      path="/olivares"
      title="Olivares"
      if={isOlivaresEntity}
    >
      <EntityOlivaresContent />
    </EntityLayout.Route>
  </EntityLayout>
);

// Or a card on the overview tab:
<EntitySwitch>
  <EntitySwitch.Case if={isOlivaresEntity}>
    <Grid item md={6}>
      <EntityOlivaresCard />
    </Grid>
  </EntitySwitch.Case>
</EntitySwitch>;
```

`isOlivaresEntity` is true for entities the
[catalog entity provider](../README.md) published or annotated
(`olivares.ai/managed` / `olivares.ai/external-id`), so the tab and card never
clutter unrelated entities.

## Tests

The plugin's logic — the API client, the display/aggregation transforms, and the
entity↔agent matching — is framework-free and unit-tested with Node's built-in
test runner (no Backstage runtime needed):

```bash
pnpm test    # tsc -p tsconfig.test.json && node --test
```

## Version caveat

Like the rest of the Olivares Backstage integration, this package does **not**
pin a Backstage version: the `@backstage/*` dependencies are peers (`*`) so the
plugin adopts the host's version. The frontend uses the classic
`@backstage/core-plugin-api` plugin system for broad compatibility. Validate
against your Backstage release before rolling out.
