# @olivaresai/client

First-party TypeScript client for the Olivares AI control plane REST API
(`/v1`). Runtime-dependency-free (global `fetch`, Node ≥ 20 or any modern
runtime). Apache-2.0.

```ts
import { Client } from "@olivaresai/client";

const c = new Client({ endpoint: "https://olivares.example:8443", token: "olvk_..." });
const info = await c.getV1ServerInfo();
for await (const agent of c.paginate("/v1/agents")) {
  console.log(agent.id);
}
```

The transport core handles auth (opaque bearer tokens), tenancy
(`X-Olivares-Tenant`), the API's single error envelope (`APIError`), cursor
pagination, Retry-After-aware retries for rate-limited calls and the stability
policy's deprecation signal (RFC 9745 `Deprecation` / RFC 8594 `Sunset`
response headers → one `console.warn` per endpoint, or your `onDeprecation`
callback). The operation layer (`src/operations.gen.ts`) is generated from the
published OpenAPI snapshot by `task sdk:generate` — do not edit it.

Versioning: `API_VERSION` is the API contract major this client was generated
from; the package MAJOR tracks it from GA on. Governing policy:
<https://olivares.ai/docs>.

Tests: `pnpm install && pnpm test` (or `task sdk:test:ts`).
