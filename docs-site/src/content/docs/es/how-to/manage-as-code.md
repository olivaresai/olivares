---
title: "Gestionar Olivares AI como código (Terraform)"
description: >-
  Declara y reconcilia objetos del control plane —agentes, políticas, vínculos
  de identidad y despliegues— con el proveedor de Terraform/OpenTofu de Olivares
  AI, autenticado mediante un token de API opaco contra la API REST del motor.
---

Olivares AI expone un **proveedor de Terraform** para que puedas gestionar el control plane *como
código* —agentes, políticas de gobierno, vínculos agente↔identidad y definiciones de despliegue
declarados en HCL y reconciliados contra el motor en ejecución a través de su API REST—. Es el
módulo XIX (API propia + gestión como código); el proveedor es un cliente ligero sobre la misma
superficie REST que documenta la [referencia de la API](/reference/api/), de modo que cualquier cosa
que puedas hacer en HCL la puedes hacer por REST.

El proveedor y la CLI son Apache-2.0 y nunca importan las interioridades del motor; HCL es
solo otro front-end de la API gobernada.

## Configurar el proveedor

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

| Ajuste | Obligatorio | Variable de entorno alternativa | Notas |
|---|---|---|---|
| `endpoint` | sí | `OLIVARES_ENDPOINT` | URL base de la API del control plane |
| `api_token` | sí | `OLIVARES_API_TOKEN` | **Token bearer opaco** (el producto usa tokens opacos y revocables, no JWT) |
| `tenant` | no | `OLIVARES_TENANT` | UUID del tenant; omítelo cuando el token esté ligado a un tenant |
| `insecure_skip_verify` | no | — | Omite la verificación TLS para el certificado autofirmado de desarrollo; nunca en producción |

La autenticación es un token bearer enviado en cada petición, con el tenant transportado en la
cabecera `X-Olivares-Tenant` —el mismo RBAC deny-by-default, ámbito por tenant y auditoría
por acción que el resto de la API—. Emite un token para una identidad de servicio con mínimo
privilegio y mantenlo fuera del estado (usa una variable y un backend de secretos).

## Recursos

| Recurso | Gestiona | Atributos clave |
|---|---|---|
| `olivares_agent` | Una entidad de agente en el inventario | `name` (obligatorio), `kind` (obligatorio), `external_id` (opcional); calculados `id`, `status`, `version` |
| `olivares_policy` | Una política de gobierno | `name` (obligatorio), `kind` (`abac` o `approval`, obligatorio, inmutable), `enabled`, `spec` (obligatorio, JSON); calculado `spec_canonical` |
| `olivares_agent_identity_binding` | Vincula un agente a una identidad no humana (el puente que afina la atribución R/RW) | `agent_id`, `identity_id`/`identity_ref`, `mint`, `allow_unknown`; calculados `minted`, `shared`, `agent_count` |
| `olivares_deployment` | Una **definición** de despliegue (estado deseado declarativo) | `subject_kind`, `subject_ref`, `name`, `environment`, `runtime`, `target`, `source_ref`, `spec`, `desired_status`; calculados `current_version`, `applied_version`, `spec_hash` |

## Fuentes de datos

Vistas de solo lectura para que un módulo pueda referenciar estado gobernado sin reimplementar
llamadas REST: `olivares_policies`, `olivares_identities`, `olivares_deployment`,
`olivares_server_info` y `olivares_access_edges` —esta última expone las aristas R/RW
y, con `include_drift = true`, el desvío permitido-vs-observado (incluido el honesto flag
`reconciliation_pending` para un acceso que aún no es atribuible con firmeza).

## Un ejemplo mínimo

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

`terraform plan` reconcilia tu HCL contra el motor; `terraform apply` crea o
actualiza los objetos a través de la API gobernada. Como las políticas y los vínculos cambian la
superficie de autorización, trata el plan como un cambio revisable —el motor audita cada
mutación con el actor real.

:::caution[`olivares_deployment` declara estado deseado; la aplicación en vivo está vetada]
`olivares_deployment` gestiona una **definición** de despliegue —estado deseado declarativo y
versionado—. Se corresponde con el módulo VII (deploy), cuya actuación en vivo es un **seam
deny-closed**: hasta que se aprovisione un ejecutor, el motor *planifica y gobierna* un despliegue
pero **`apply`/`retire` devuelven `503`** en lugar de actuar sobre la infraestructura. Así, un
recurso `olivares_deployment` registra y gobierna la intención hoy; por sí mismo no
reconcilia infraestructura real. Consulta [módulo VII](/es/reference/modules/vii-deploy/) y
[Honestidad y límites](/es/start/honesty-and-limits/).
:::

:::note[El proveedor es un subconjunto de la API, a propósito]
El proveedor cubre los objetos de gestión-como-código anteriores. La superficie gobernada completa
—y el esquema a nivel de campo de cada `spec`— es la API REST; algunas rutas de módulo son
alcanzables pero quedan deliberadamente fuera del documento OpenAPI servido. Verifica los atributos
de un recurso contra `terraform providers schema -json` y la [referencia de la API](/reference/api/)
antes de depender de ellos; esta página no reproduce esquema que no puede mantener en sincronía con el código.
:::

## Relacionado

- [Referencia de la API](/reference/api/) — la superficie REST que el proveedor maneja.
- [Política de estabilidad de la API](/es/reference/api-stability/) — el compromiso de versionado/deprecación en el que confía el proveedor (avisa una vez por ejecución cuando una respuesta lleva una señal de deprecación).
- [Módulo XIX — API propia + gestión como código](/es/reference/modules/xix-api-manage-as-code/).
- [Módulo VII — despliegue e integración](/es/reference/modules/vii-deploy/) — la salvedad del seam 503 anterior.
- [Gobernar y aprobar](/es/how-to/govern-and-approve/) — cómo la política y las aprobaciones gobiernan lo que declaras.
