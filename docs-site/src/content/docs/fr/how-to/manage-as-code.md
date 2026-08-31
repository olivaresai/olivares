---
title: "Gérer Olivares AI comme du code (Terraform)"
description: >-
  Déclarez et réconciliez les objets du control plane — agents, politiques,
  liaisons d'identité et déploiements — avec le provider Terraform/OpenTofu
  d'Olivares AI, authentifié par un jeton d'API opaque contre l'API REST du moteur.
---

Olivares AI expose un **provider Terraform** qui vous permet de gérer le control plane *en tant que
code* — agents, politiques de gouvernance, liaisons agent↔identité et définitions de déploiement
déclarés en HCL et réconciliés contre le moteur en cours d'exécution via son API REST. Il s'agit du
module XIX (API propre + manage-as-code) ; le provider est un client léger sur la même surface REST
que documente la [référence de l'API](/reference/api/), donc tout ce que vous pouvez faire en HCL, vous
pouvez le faire via REST.

Le provider et la CLI sont sous Apache-2.0 et n'importent jamais les internes du moteur ; le HCL n'est
qu'une autre interface vers l'API gouvernée.

## Configurer le provider

```hcl
terraform {
  required_providers {
    olivares = {
      source = "olivaresai/olivares"
    }
  }
}

provider "olivares" {
  endpoint = "https://olivares.internal:8443" # or OLIVARES_ENDPOINT
  api_token = var.olivares_token                  # or OLIVARES_API_TOKEN (sensitive)
  # tenant   = "…"                                # optional; or OLIVARES_TENANT (sent as X-Olivares-Tenant)
  # insecure_skip_verify = true                   # dev self-signed cert only
}
```

| Paramètre | Requis | Variable d'env. de repli | Notes |
|---|---|---|---|
| `endpoint` | oui | `OLIVARES_ENDPOINT` | URL de base de l'API du control plane |
| `api_token` | oui | `OLIVARES_API_TOKEN` | **Jeton bearer opaque** (le produit utilise des jetons opaques et révocables, pas des JWT) |
| `tenant` | non | `OLIVARES_TENANT` | UUID du tenant ; à omettre lorsque le jeton est lié à un tenant |
| `insecure_skip_verify` | non | — | Ignore la vérification TLS pour le certificat auto-signé de développement ; jamais en production |

L'authentification est un jeton bearer envoyé à chaque requête, le tenant étant transporté dans
l'en-tête `X-Olivares-Tenant` — avec le même RBAC deny-by-default, le même cloisonnement par tenant et
le même audit par action que le reste de l'API. Émettez un jeton pour une identité de service au moindre
privilège, et gardez-le hors de l'état (utilisez une variable et un backend de secrets).

## Ressources

| Ressource | Gère | Attributs clés |
|---|---|---|
| `olivares_agent` | Une entité agent dans l'inventaire | `name` (requis), `kind` (requis), `external_id` (optionnel) ; calculés `id`, `status`, `version` |
| `olivares_policy` | Une politique de gouvernance | `name` (requis), `kind` (`abac` ou `approval`, requis, immuable), `enabled`, `spec` (requis, JSON) ; calculé `spec_canonical` |
| `olivares_agent_identity_binding` | Lier un agent à une identité non humaine (le pont qui affine l'attribution R/RW) | `agent_id`, `identity_id`/`identity_ref`, `mint`, `allow_unknown` ; calculés `minted`, `shared`, `agent_count` |
| `olivares_deployment` | Une **définition** de déploiement (état désiré déclaratif) | `subject_kind`, `subject_ref`, `name`, `environment`, `runtime`, `target`, `source_ref`, `spec`, `desired_status` ; calculés `current_version`, `applied_version`, `spec_hash` |

## Sources de données

Vues en lecture seule pour qu'un module puisse référencer l'état gouverné sans réimplémenter les appels
REST : `olivares_policies`, `olivares_identities`, `olivares_deployment`,
`olivares_server_info` et `olivares_access_edges` — cette dernière expose les arêtes R/RW et,
avec `include_drift = true`, la dérive Permitted-vs-Observed (y compris le drapeau honnête
`reconciliation_pending` pour un accès qui n'est pas encore attribuable de façon ferme).

## Un exemple minimal

```hcl
resource "olivares_agent" "billing_bot" {
  name = "billing-reconciler"
  kind = "service"
}

resource "olivares_policy" "require_approval_for_prod" {
  name    = "prod-deploys-need-approval"
  kind    = "approval"
  enabled = true
  spec    = jsonencode({
    # policy body — see the API reference for the schema of each kind
  })
}

# Read the current Permitted-vs-Observed drift as data:
data "olivares_access_edges" "estate" {
  include_drift = true
}
```

`terraform plan` réconcilie votre HCL contre le moteur ; `terraform apply` crée ou
met à jour les objets via l'API gouvernée. Parce que les politiques et les liaisons modifient la surface
d'autorisation, traitez le plan comme un changement à relire — le moteur audite chaque
mutation avec l'acteur réel.

:::caution[`olivares_deployment` déclare l'état désiré ; l'application en direct est sous contrôle]
`olivares_deployment` gère une **définition** de déploiement — un état désiré déclaratif et versionné.
Elle correspond au module VII (deploy), dont l'actionnement en direct est un **seam deny-closed** :
tant qu'un exécuteur n'est pas provisionné, le moteur *planifie et gouverne* un déploiement mais
**`apply`/`retire` renvoient `503`** au lieu d'agir sur l'infrastructure. Ainsi, une ressource
`olivares_deployment` enregistre et gouverne l'intention aujourd'hui ; elle ne réconcilie pas par
elle-même l'infrastructure réelle. Voir [module VII](/fr/reference/modules/vii-deploy/) et
[Honnêteté et limites](/fr/start/honesty-and-limits/).
:::

:::note[Le provider est un sous-ensemble de l'API, à dessein]
Le provider couvre les objets manage-as-code ci-dessus. La surface gouvernée complète — et
le schéma au niveau des champs de chaque `spec` — c'est l'API REST ; certaines routes de module sont
accessibles mais délibérément en dehors du document OpenAPI servi. Vérifiez les attributs d'une ressource
contre `terraform providers schema -json` et la [référence de l'API](/reference/api/) avant de
vous y fier ; cette page ne reproduit pas un schéma qu'elle ne peut pas maintenir en phase avec le code.
:::

## Voir aussi

- [Référence de l'API](/reference/api/) — la surface REST que pilote le provider.
- [Politique de stabilité de l'API](/fr/reference/api-stability/) — l'engagement de versionnage/dépréciation sur lequel s'appuie le provider (il avertit une fois par exécution lorsqu'une réponse porte un signal de dépréciation).
- [Module XIX — API propre + manage-as-code](/fr/reference/modules/xix-api-manage-as-code/).
- [Module VII — déploiement et intégration](/fr/reference/modules/vii-deploy/) — la mise en garde sur le seam 503 ci-dessus.
- [Gouverner et approuver](/fr/how-to/govern-and-approve/) — comment la politique et les approbations gouvernent ce que vous déclarez.
