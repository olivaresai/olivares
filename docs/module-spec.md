<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->

# Built-in module specification

[`core/modulespec/modules.json`](../core/modulespec/modules.json) has one row per
API namespace, in registration order. A row keeps the existing namespace, SDK
descriptor name, implementation package, default constructor, kernel/catalog
classification, profile requirements, configuration defaults, console entries and
edition. `console_entries` names, by registry id, every console view whose page
follows the module's state: the view is hidden and its route says the module is
not enabled while the module is off. One row at most names a view. A module whose
panels sit on another module's page (identity on the governance identity page)
names none; those panels gate themselves. The sessions row names the
communication views, which the console also hides while server-info reports the
communication plane not ready.

Routes, permissions and migrations remain executable declarations in the module:
the row's constructor supplies `APIRoutes`, `Permissions` and `RegisterSchema`.
`RegisterSchema` also registers SQL migration files. Copying those declarations
into JSON would create another source of truth. Constructor options remain the
module's defaults; `config_defaults` is the SDK configuration passed to `Init`.

The module also documents its own operations. The beta OpenAPI document
(`/openapi.beta.json`) is built from the registered routes with the generic JSON
envelope; request bodies, typed responses and extra parameters come from the
module's `OperationDocumentation` (`api.ModuleOperationDocumenter`), kept in its
`openapi.go`. The core describes no module operation.

## Kernel and catalog

`kind: "kernel"` means the module stays active independently of the saved
selection. The kernel consists of `governance`, `sessions` and `session-cockpit`.
Governance and sessions remain in the selectable list for compatibility, but
clearing their selection does not stop them. Session-cockpit is an edition slot
outside that list: the profile does not disable it when the edition registers it,
and does not create it when the edition omits it.

In Community, that slot registers an availability descriptor and declares only
`session-cockpit:availability:read`. It registers no HTTP operation, including
`GET /v1/m/session-cockpit/availability`, which returns 404 by absence after setup.
It owns no stored data or console entry. It is outside the selectable catalog,
so `olivares modules on/off session-cockpit` cannot toggle it. The descriptor
does not provide the private cockpit implementation.

`kind: "catalog"` modules can be dormant. Selecting a module also activates the
transitive closure of its `requires` edges, without adding those dependencies to
the saved selection. A required module stays active until no selected module or
kernel module needs it. The console API and `olivares modules ls -o json` expose
the active consumers as `required_by`. Changes to the active set take effect after restart;
dormant modules retain their data.

`requires` contains API namespaces, not package or descriptor names. Record
runtime dependencies from constructor/boot wiring, reads that need another
module's live data, and events that need its consumer. In particular:

- `compliance` and `posture` require `accessmap`: their reconciled drift reads
  depend on the running access-map writer, not just its retained tables.
- `posture` also requires `inventory` for its active catalog projection.
- `liveingest` does not require `voice`: nothing in production publishes voice
  telemetry through it (the voice module is its own producer), so an upgrade
  that keeps liveingest on does not start voice or finops.

Optional integrations and erasure of retained data are not activation dependencies.
For example, compliance's erasure registry must work on dormant modules' data and
explicitly tolerates an absent kind; it does not require those modules to run.
Module off/on checks therefore apply to catalog modules with no active consumers,
not to the kernel or a dependency still required by the selection.
`TestEveryCatalogModuleTurnsOffAndOnAcrossARestart` (`cmd/olivares`) runs that
check for every catalog row: it deselects the module and the modules requiring
it in the console, restarts, expects a ready engine whose deselected modules
are dormant and answer `module_not_enabled`, then reselects them, restarts,
and expects the same routes back.

`TestModuleSpecRequiresDerivedDependencies` in `modules/registry` derives
dependencies from production imports of another module package and from
literals naming another module's registered kinds. It fails unless each owner is
active with its consumer through the kernel or the `requires` closure. The
optional reads above are listed in that test with their reason.

## Readers and maintenance

The existing readers now derive their lists from this table:

| Reader | Derived facts |
| --- | --- |
| `cmd/olivares/wire.go` | Default instances and registration order, via `modules/registry.Build` |
| `moduleCatalog` | Selectable namespaces, kernel, requirements and fresh-install defaults |
| `published26100ModuleSelection` | Frozen upgrade selection (`published_26100`) |
| `tools/permsdump` | Community default instances; Business native composition through `openapi --permission-inventory`. Both use the shared permission inventory producer. |
| `communityCensusModules` | Declared account-retirement participants and their order |
| `core/runtime` | Existing delivery-class keys and policy |
| `check-public-counts.sh`, `ai-state.sh` and `gen-release-diagrams.mjs` | Unique selectable implementation packages |
| `web/src/stores/modules.ts` | The module each console view follows, from the generated `web/src/features/module-spec.gen.ts` |

Boot, the schema manifest, database directory activation, OpenAPI and server-info
continue to consume the resulting module instances. Configured instances retain
their adapters. Edition instances can replace the shared placeholder or append
edition-only namespaces. Core reads metadata without importing product modules.
Rows marked `edition_slot` use placeholders only for inventory construction;
boot passes an explicit edition list, which may omit that surface entirely.

To add a module, implement its existing module interfaces, add a spec row, and run:

```sh
python3 scripts/generate-module-spec.py
```

Commit the generated constructor binding and console list with the row. Registry
tests run the generator with `--check`.

A console view is registered by its own directory: `web/src/features/<dir>/views.tsx`
exports `VIEWS`, the entries of the views whose page lives there (id, path,
navigation, icon, permission, a `lazy()` element from the same directory and an
`order`, its place in registry order). `web/src/features/registry.tsx` collects every
such file; nothing else is edited to add a route. A module's new console view is
also added to its row's `console_entries`; the console tests refuse a Community view
of a built-in module that no row names. A view no row names (an edition view) follows
its permission's first segment. A constructor requiring an existing instance declares
`constructor_inputs`; its referenced row must come first. Configured adapters
still belong in the composition root until their own wiring moves into a module.

Do not set `published_26100` for a new module: an upgrade must not enable it
silently. `selectable` preserves the existing catalog separately from kernel
classification; the session-cockpit edition surface remains outside that catalog.
`delivery_name` preserves the current runtime lookup spelling independently of
the SDK descriptor name. Changing a dependency, delivery policy or console
navigation is a behavior change, separate from consolidating these declarations.
