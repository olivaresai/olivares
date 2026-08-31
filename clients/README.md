# Client SDKs

First-party clients for the Olivares AI control plane REST API (the published
`/v1` contract), plus the OpenAPI→SDK pipeline that generates their operation
layers. Everything under this directory is **Apache-2.0** and never links the
AGPL engine (enforced by `scripts/check-boundary.sh`).

| Directory     | Artifact                              | Language   |
|---------------|---------------------------------------|------------|
| `go/`         | `github.com/olivaresai/olivares/clients/go` (package `olivares`) | Go, stdlib-only |
| `python/`     | `olivares-client` (package `olivares_client`) | Python ≥3.10, stdlib-only |
| `typescript/` | `@olivaresai/client`                  | TypeScript (global `fetch`) |
| `java/`       | `ai.olivares:olivares-client` (package `ai.olivares.client`) | Java ≥17, JDK-stdlib-only |
| `generator/`  | the OpenAPI→SDK pipeline              | Go, stdlib-only |

## How the pipeline works

Two published OpenAPI documents are the input: `web/openapi/openapi.json` (the
**stable** core contract, from `core/api/openapi.go`) and
`web/openapi/openapi.beta.json` (the **beta** module routes `/v1/m/<ns>/…`,
reflected from the routes the modules register). The SDKs cover the
**union**: each generated method keeps its tier, and beta operations carry a
`Stability: beta` annotation, so an integrator sees the tier in their IDE — a
beta route is never folded silently into the stable surface. The generator is
deterministic and hermetic; regeneration and drift-checking are ordinary tasks:

```
task sdk:generate   # openapi:dump → regenerate the four operation layers
task sdk:check      # fail if committed generated code is stale vs the spec
task sdk:test:python
task sdk:test:ts
task sdk:test:java
```

The Go SDK and the generator are workspace modules — `task build:go` /
`task test` (the green gate) build and race-test them, and `task sdk:check`
(in the gate + CI) regenerates ALL four operation layers from the spec and
fails on drift. The Python, TypeScript and Java SDK tests are the three manual
tasks above — they need their language toolchain (Python 3, Node/pnpm, a
JDK 17+ and Maven respectively), which the Go gate does not, so they run
out-of-band. The generator emits every target as text and never invokes a
foreign toolchain, so the codegen + drift gate stay hermetic and JDK-free.

## Design

Each SDK is two layers:

- a **hand-written core** — auth (opaque `olvs_`/`olvk_` bearer tokens),
  tenancy (`X-Olivares-Tenant`), the single error envelope, cursor pagination
  (`items`/`cursor`/`has_more`), Retry-After-aware retries (429 always, 503
  for GET), and the stability policy's deprecation signal (RFC 9745
  `Deprecation` / RFC 8594 `Sunset` headers surfaced once per endpoint);
- a **generated operation layer** — one method per published operation, with
  generic JSON values even when the contract publishes a request schema, exact
  media types for raw request bytes, and language-native deprecation markers
  generated from the spec.

## Versioning

Each SDK embeds `API_VERSION` (the contract major it was generated from) and
`SPEC_HASH` (the snapshot it was generated against). SDK packages are
versioned independently (pre-1.0 today); from GA on their MAJOR tracks the API
major. The governing commitment is the public stability policy:
<https://olivares.ai/docs>.
