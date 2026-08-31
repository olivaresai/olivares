---
title: "Olivares AI als Code verwalten (Terraform)"
description: >-
  Control-Plane-Objekte — Agenten, Policies, Identitäts-Bindings und
  Deployments — deklarieren und abgleichen mit dem Terraform/OpenTofu-Provider
  von Olivares AI, authentifiziert durch ein opakes API-Token gegen die REST-API
  der Engine.
---

Olivares AI stellt einen **Terraform-Provider** bereit, mit dem Sie die Control Plane *als
Code* verwalten können — Agenten, Governance-Policies, Agent↔Identitäts-Bindings und
Deployment-Definitionen, in HCL deklariert und über die REST-API gegen die laufende Engine
abgeglichen. Dies ist Modul XIX (eigene API + Manage-as-Code); der Provider ist ein dünner
Client über derselben REST-Oberfläche, die die [API-Referenz](/reference/api/) dokumentiert
— alles, was Sie in HCL tun können, können Sie auch über REST tun.

Der Provider und das CLI stehen unter Apache-2.0 und importieren niemals die Engine-Interna;
HCL ist nur ein weiteres Front-End zur governten API.

## Den Provider konfigurieren

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

| Einstellung | Erforderlich | Env-Fallback | Hinweise |
|---|---|---|---|
| `endpoint` | ja | `OLIVARES_ENDPOINT` | Basis-URL der Control-Plane-API |
| `api_token` | ja | `OLIVARES_API_TOKEN` | **Opakes Bearer-Token** (das Produkt nutzt opake, widerrufbare Tokens, keine JWTs) |
| `tenant` | nein | `OLIVARES_TENANT` | Tenant-UUID; weglassen, wenn das Token tenant-gebunden ist |
| `insecure_skip_verify` | nein | — | TLS-Verifikation für das selbstsignierte Dev-Zertifikat überspringen; niemals in Produktion |

Die Authentifizierung ist ein Bearer-Token, das bei jeder Anfrage gesendet wird, mit dem
Tenant im `X-Olivares-Tenant`-Header — dasselbe deny-by-default RBAC, dasselbe
Tenant-Scoping und dieselbe Auditierung pro Aktion wie im Rest der API. Erstellen Sie ein
Token für eine Service-Identität nach dem Least-Privilege-Prinzip und halten Sie es aus dem
State heraus (verwenden Sie eine Variable und ein Secret-Backend).

## Ressourcen

| Ressource | Verwaltet | Schlüsselattribute |
|---|---|---|
| `olivares_agent` | Eine Agenten-Entität im Inventar | `name` (erforderlich), `kind` (erforderlich), `external_id` (optional); berechnet `id`, `status`, `version` |
| `olivares_policy` | Eine Governance-Policy | `name` (erforderlich), `kind` (`abac` oder `approval`, erforderlich, unveränderlich), `enabled`, `spec` (erforderlich, JSON); berechnet `spec_canonical` |
| `olivares_agent_identity_binding` | Bindet einen Agenten an eine nichtmenschliche Identität (die Brücke, die die R/RW-Zuordnung schärft) | `agent_id`, `identity_id`/`identity_ref`, `mint`, `allow_unknown`; berechnet `minted`, `shared`, `agent_count` |
| `olivares_deployment` | Eine Deployment-**Definition** (deklarativer Soll-Zustand) | `subject_kind`, `subject_ref`, `name`, `environment`, `runtime`, `target`, `source_ref`, `spec`, `desired_status`; berechnet `current_version`, `applied_version`, `spec_hash` |

## Datenquellen

Schreibgeschützte Sichten, damit ein Modul governten State referenzieren kann, ohne
REST-Aufrufe neu zu implementieren: `olivares_policies`, `olivares_identities`,
`olivares_deployment`, `olivares_server_info` und `olivares_access_edges` — letztere legt
die R/RW-Kanten offen und, mit `include_drift = true`, den Permitted-vs-Observed-Drift
(einschließlich des ehrlichen Flags `reconciliation_pending` für einen Zugriff, der noch
nicht eindeutig zuordenbar ist).

## Ein minimales Beispiel

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

`terraform plan` gleicht Ihr HCL gegen die Engine ab; `terraform apply` erstellt oder
aktualisiert die Objekte über die governte API. Da Policies und Bindings die
Autorisierungsoberfläche verändern, behandeln Sie den Plan als prüfbare Änderung — die
Engine auditiert jede Mutation mit dem echten Akteur.

:::caution[`olivares_deployment` deklariert Soll-Zustand; das Live-Apply ist gegated]
`olivares_deployment` verwaltet eine Deployment-**Definition** — deklarativer,
versionierter Soll-Zustand. Sie wird auf Modul VII (Deploy) abgebildet, dessen
Live-Aktuierung eine **deny-closed-Naht** ist: Bis ein Executor bereitgestellt ist, *plant
und governt* die Engine ein Deployment, aber **`apply`/`retire` liefern `503`** zurück,
statt auf Infrastruktur einzuwirken. Eine `olivares_deployment`-Ressource erfasst und
governt heute also Absicht; sie gleicht für sich genommen keine reale Infrastruktur ab.
Siehe [Modul VII](/de/reference/modules/vii-deploy/) und
[Ehrlichkeit & Grenzen](/de/start/honesty-and-limits/).
:::

:::note[Der Provider ist absichtlich eine Teilmenge der API]
Der Provider deckt die obigen Manage-as-Code-Objekte ab. Die vollständige governte
Oberfläche — und das feldgenaue Schema jeder `spec` — ist die REST-API; einige
Modul-Routen sind erreichbar, aber bewusst außerhalb des ausgelieferten
OpenAPI-Dokuments. Prüfen Sie die Attribute einer Ressource gegen
`terraform providers schema -json` und die [API-Referenz](/reference/api/), bevor Sie sich
darauf verlassen; diese Seite reproduziert kein Schema, das sie nicht im Gleichschritt mit
dem Code halten kann.
:::

## Verwandt

- [API-Referenz](/reference/api/) — die REST-Oberfläche, die der Provider ansteuert.
- [API-Stabilitätsrichtlinie](/de/reference/api-stability/) — die Versionierungs-/Deprecation-Zusage, auf die sich der Provider verlässt (er warnt einmal pro Lauf, wenn eine Antwort ein Deprecation-Signal trägt).
- [Modul XIX — eigene API + Manage-as-Code](/de/reference/modules/xix-api-manage-as-code/).
- [Modul VII — Deployment & Integration](/de/reference/modules/vii-deploy/) — der 503-Naht-Vorbehalt oben.
- [Governen und genehmigen](/de/how-to/govern-and-approve/) — wie Policy und Genehmigungen das governen, was Sie deklarieren.
