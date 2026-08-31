# olivares-client (Java)

First-party Java client for the Olivares AI control plane REST API (`/v1`).
JDK standard library only (`java.net.http`); Java ≥ 17. Apache-2.0.

This tree does not configure Maven Central publication, so registry availability
is not established here. Until the release notes confirm those coordinates, build
and install the client into your local Maven repository:

```sh
cd clients/java
mvn install
```

```xml
<dependency>
  <groupId>ai.olivares</groupId>
  <artifactId>olivares-client</artifactId>
  <version>0.1.0</version>
</dependency>
```

```java
import ai.olivares.client.Client;

var c = Client.of("https://olivares.example:8443", "olvk_...");
var info = c.getV1ServerInfo();
for (var agent : c.paginate("/v1/agents")) {
    System.out.println(agent.get("id"));
}
```

The transport core handles auth (opaque bearer tokens), tenancy
(`X-Olivares-Tenant`), the API's single error envelope
(`OlivaresApiException`), cursor pagination, Retry-After-aware retries for
rate-limited calls and the stability policy's deprecation signal (RFC 9745
`Deprecation` / RFC 8594 `Sunset` response headers → one WARNING log per
endpoint, or your `onDeprecation` handler). The operation layer (`Client.java`,
`ApiMetadata.java`) is generated from the published OpenAPI snapshots by
`task sdk:generate` — do not edit it.

Each operation has two overloads: a convenience form (`getV1Agents()`) and one
taking per-call `RequestOptions` (`getV1Agents(RequestOptions.builder()
.query("limit", "5").tenant("acme").build())`). Custom TLS / proxies / timeouts
go through your own `java.net.http.HttpClient` via
`ClientOptions.builder().httpClient(...)`. Published JSON schemas are represented
as `Map`/`List`/scalars by the dependency-free `Json` codec; raw request bytes keep
their declared media type.

Versioning: `ApiMetadata.API_VERSION` is the API contract major this client was
generated from; `ClientCore.VERSION` is the SDK's own semantic version, whose
MAJOR tracks the API major from GA on. Governing policy:
<https://olivares.ai/docs>.

Tests: `mvn test` (or `task sdk:test:java`). Local release-shaped artifacts
(sources + javadoc jars): `mvn -Prelease package`. Registry publication is a
separate release step and is not configured in this tree.
