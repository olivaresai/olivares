---
title: "Utiliser les SDK clients (Go, Java, Python, TypeScript)"
description: >-
  Appelez l'API REST du control plane avec les clients Go, Java, Python et
  TypeScript de première partie — authentification par jeton opaque, tenancy,
  pagination, comportement de retry et signalement de dépréciation gérés pour
  vous.
---

Le control plane fournit quatre **SDK clients de première partie** pour son contrat REST publié
(`/v1`), générés à partir du même document OpenAPI que le moteur sert et que la
[référence de l'API](/reference/api/) rend :

| SDK | Paquet | Besoins d'exécution |
|---|---|---|
| Go | `github.com/olivaresai/olivares/clients/go` (package `olivares`) | stdlib uniquement |
| Java | `ai.olivares:olivares-client` (package `ai.olivares.client`) | Java ≥ 17, `java.net.http` du JDK uniquement |
| Python | `olivares-client` (import `olivares_client`) | Python ≥ 3.10, stdlib uniquement |
| TypeScript | `@olivaresai/client` | `fetch` global (Node ≥ 20, Deno, navigateurs) |

:::note[Statut de distribution]
Les SDK vivent dans le dépôt du produit sous `clients/` et sont versionnés avec lui. La
publication sur les registres publics (pkg.go.dev, Maven Central, PyPI, npm) a lieu avec la
release publique — jusque-là, consommez-les depuis le dépôt (chemin de module Go ci-dessus,
`mvn -f clients/java install`, `pip install ./clients/python`, `npm install ./clients/typescript`).
:::

Les quatre partagent une seule conception. Un cœur écrit à la main implémente le comportement
contractuel — jetons bearer opaques (session `olvs_` / clé d'API `olvk_`), l'en-tête
`X-Olivares-Tenant`, l'enveloppe d'erreur unique de l'API, la pagination par curseur
(`items`/`cursor`/`has_more`), les retries qui honorent `Retry-After` pour les appels limités en
débit (429 toujours ; 503 uniquement pour les GET idempotents), et les en-têtes de dépréciation
de la [politique de stabilité](/fr/reference/api-stability/) remontés une fois par endpoint. Par-
dessus se trouve une méthode générée par opération publiée, nommée d'après la route
(`GET /v1/agents` → `GetV1Agents` / `get_v1_agents` / `getV1Agents`), avec des corps de
requête/réponse en JSON générique — le contrat publié garde délibérément les corps opaques.

## Go

```go
import olivares "github.com/olivaresai/olivares/clients/go"

c, err := olivares.New("https://olivares.example:8443", os.Getenv("OLIVARES_API_TOKEN"),
    olivares.WithTenant("9be0…"))
if err != nil { … }

info, err := c.GetV1ServerInfo(ctx)

for agent, err := range c.ListPages(ctx, "/v1/agents", olivares.Query("limit", "100")) {
    if err != nil { … }
    fmt.Println(agent["id"])
}
```

Les erreurs sont des `*olivares.APIError` (à matcher avec `errors.As`) ; `Code` porte les codes
d'erreur stables du contrat (`not_found`, `forbidden`, `rate_limited`, …). Les signaux de
dépréciation arrivent une fois par endpoint sous forme d'un avertissement `slog`, ou via votre
propre callback `WithDeprecationHandler`.

## Java

```java
import ai.olivares.client.Client;
import ai.olivares.client.ClientOptions;
import ai.olivares.client.OlivaresApiException;
import ai.olivares.client.RequestOptions;

Client c = new Client(ClientOptions.builder()
    .endpoint("https://olivares.example:8443")
    .token(System.getenv("OLIVARES_API_TOKEN"))
    .tenant("9be0…")
    .build());

var info = c.getV1ServerInfo();

for (var agent : c.paginate("/v1/agents",
        RequestOptions.builder().query("limit", "100").build())) {
    System.out.println(agent.get("id"));
}
```

Les erreurs lèvent `OlivaresApiException` avec `getStatus()`, `getCode()`,
`getApiMessage()` et `getRequestId()`. Les signaux de dépréciation arrivent une fois
par endpoint via le callback `onDeprecation`. Le cœur est sans dépendance — uniquement
le `java.net.http` du JDK et un codec JSON écrit à la main.

## Python

```python
from olivares_client import Client, APIError

c = Client("https://olivares.example:8443", token="olvk_…", tenant="9be0…")

info = c.get_v1_server_info()
for agent in c.paginate("/v1/agents", limit="100"):
    print(agent["id"])
```

Les erreurs lèvent `APIError` avec `.status`, `.code`, `.message`, `.request_id`. Les endpoints
dépréciés émettent un `DeprecationWarning` par endpoint (ou votre callback `on_deprecation=`).
Pour le TLS auto-signé du moteur prêt à l'emploi, passez `verify=False` en labo — épinglez une
vraie CA en production.

## TypeScript

```ts
import { Client, APIError } from "@olivaresai/client";

const c = new Client({ endpoint: "https://olivares.example:8443", token: "olvk_…" });

const info = await c.getV1ServerInfo();
for await (const agent of c.paginate("/v1/agents", { query: { limit: "100" } })) {
  console.log(agent.id);
}
```

Les erreurs sont des instances `APIError` ; les signaux de dépréciation arrivent une fois par
endpoint via `console.warn` ou votre callback `onDeprecation`.

## Versionnage et régénération

Chaque SDK exporte `API_VERSION` (la majeure du contrat d'API à partir de laquelle il a été
généré) et `SPEC_HASH` (le SHA-256 du snapshot OpenAPI exact) — `APIVersion` et `SpecHash` en
Go. Les couches d'opérations sont régénérées par `task sdk:generate` et leur dérive est vérifiée
par `task sdk:check`, qui s'exécute dans le gate de pre-push et en CI — un changement de contrat
ne peut pas diverger silencieusement des clients livrés. L'engagement de compatibilité pour tout
ce que les SDK touchent est la
[politique de stabilité de l'API](/fr/reference/api-stability/).

## Liens connexes

- [Stabilité, versionnage, dépréciation & retrait de l'API](/fr/reference/api-stability/)
- [Référence de l'API REST](/reference/api/)
- [Gérer le control plane en tant que code](/fr/how-to/manage-as-code/) — le provider Terraform,
  pour une gestion déclarative plutôt que des appels programmatiques.
