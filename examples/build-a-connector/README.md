# Build your first connector

Connectors are the breadth moat and the cleanest place to contribute: a connector
imports **only** from `sdk/` (Apache-2.0) and **never** from `core/` (AGPL), so it
ships without copyleft obligations. The `olivares-connector-new` scaffold gives you a
complete, compiling, boundary-clean repository to start from.

> The [`smoke.sh`](./smoke.sh) here generates a connector, builds it, runs its
> lifecycle test, and proves the license boundary — all offline. Run it yourself:
>
> ```sh
> examples/build-a-connector/smoke.sh
> ```

## 1. Scaffold the repository

```sh
go run ./sdk/scaffold/cmd/olivares-connector-new \
  -dir ./widget-audit \
  -name acme.widget-audit \
  -module example.com/acme/widget-audit \
  -kind source \
  -sdk-path ./sdk          # dev: point go.mod at this checkout's sdk/ so it builds now
```

| Flag | Meaning |
|---|---|
| `-name` | `<vendor>.<connector>`, two lowercase `[a-z0-9-]` parts |
| `-module` | the Go module path of your new repo |
| `-kind` | `source` (gathers facts → emits observations) or `output` (delivers notifications) |
| `-plugin` | also emit the `go-plugin` `main` so it ships as a separate process |
| `-sdk-path` | a local `sdk/` checkout → emits `replace` directives so it compiles immediately (no public SDK tags yet) |

It writes a ready-to-build repo:

```
widget-audit/
├── go.mod                    # standalone module; replace → your sdk/ checkout
├── widgetaudit.go            # the connector: Descriptor + Open + Gather/Close
├── widgetaudit_test.go       # a lifecycle test with an in-memory sink
├── README.md                 # build → test → sign → distribute → operate → certify
└── scripts/check-boundary.sh # the standalone /core-boundary guard (CI-ready)
```

## 2. Build, test, and stay boundary-clean

The repo is its own module — build it with `GOWORK=off` so it resolves exactly as a
third party's would (not through this workspace). A **source** connector pulls in
**zero** third-party dependencies (the SDK is stdlib-only), so it builds fully
offline:

```sh
cd widget-audit
GOWORK=off go build ./...
GOWORK=off go test ./...                 # the lifecycle test
GOWORK=off bash scripts/check-boundary.sh
# Boundary check OK: no github.com/olivaresai/olivares/core package in the build graph.
```

The boundary check runs `go list -deps` over the real build graph and fails if your
connector ever reaches `/core`. Wire it into your CI — it is the one hard rule.

## 3. Implement your connector

Open `widgetaudit.go` and fill in the lifecycle:

- **`Descriptor()`** — your connector's stable identity and config fields.
- **`Open(ctx, cfg, sink)`** — validate config, connect to your system.
- **`Gather(ctx)`** (source) — pull facts and `Emit` `model.Observation` values
  (`EdgeObservation` for R/RW access, `CostSample`, `FindingReport`); or
  **`Notify(ctx, …)`** (output) — deliver events/findings to an external system.
- **`Close()`** — release resources.

Be honest about coverage and confidence: if a signal is approximate or untrusted
(for example MCP annotations), report it as such rather than as ground truth — the
SDK's `Confidence` / attribution vocabulary exists for exactly this.

## 4. Sign, distribute, certify

The generated `README.md` covers the full lifecycle: sign your release artifact
(Sigstore bundle), distribute it (GitHub release or OCI), and get it listed in the
curated **verified connectors** index. The host only ever executes a connector whose
signature it has verified against a pinned identity — see
[`how-to/build-a-connector`](../../docs-site/src/content/docs/how-to/build-a-connector.md)
and ADR-0016.

## References

- Scaffold + generator: `sdk/scaffold/` (`go doc ./sdk/scaffold`)
- The SDK contract: `sdk/` (`go doc ./sdk`), versioning policy: `sdk/VERSIONING.md`
- The first-party reference connector: `connectors/example/`
- The connector contract: `docs/contracts/S142-external-connector-sdk.md`
- Contributing a *first-party* connector: [`CONTRIBUTING.md`](../../CONTRIBUTING.md#adding-a-connector)
