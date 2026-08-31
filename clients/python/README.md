# olivares-client

First-party Python client for the Olivares AI control plane REST API (`/v1`).
Standard library only; Python ≥ 3.10. Apache-2.0.

```python
from olivares_client import Client

c = Client("https://olivares.example:8443", token="olvk_...")
info = c.get_v1_server_info()
for agent in c.paginate("/v1/agents"):
    print(agent["id"])
```

The transport core handles auth (opaque bearer tokens), tenancy
(`X-Olivares-Tenant`), the API's single error envelope (`APIError`), cursor
pagination, Retry-After-aware retries for rate-limited calls and the stability
policy's deprecation signal (RFC 9745 `Deprecation` / RFC 8594 `Sunset`
response headers → one `DeprecationWarning` per endpoint, or your
`on_deprecation` callback). The operation layer (`_operations.py`) is generated
from the published OpenAPI snapshot by `task sdk:generate` — do not edit it.

Versioning: `olivares_client.API_VERSION` is the API contract major this
client was generated from; the package MAJOR tracks it from GA on. Governing
policy: <https://olivares.ai/docs>.

Tests: `python3 -m unittest discover -s tests` (or `task sdk:test:python`).
