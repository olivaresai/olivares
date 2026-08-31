---
title: Co-despliegue de Claude apps gateway + Olivares AI
description: >-
  Cómo ejecutar la Claude apps gateway self-hosted de Anthropic y dejar que
  Olivares AI la gobierne como otra superficie empresarial: inventario, postura,
  ingesta de auditoría, correlación OTLP y endpoint fase 1 del protocolo de gateway.
sidebar:
  order: 9
---

## Qué es la Claude apps gateway

La
[Claude apps gateway](https://code.claude.com/docs/en/claude-apps-gateway) de
Anthropic es un servicio self-hosted incluido en el binario `claude` a partir de
v2.1.195; se ejecuta con `claude gateway --config gateway.yaml` y usa PostgreSQL.
Sitúa un inicio de sesión OIDC delante de Amazon Bedrock, Claude Platform on AWS,
Google Cloud Agent Platform, Microsoft Foundry o la API de Anthropic, de modo que
los desarrolladores usen sesiones del IdP corporativo en vez de credenciales
locales del proveedor. Su `gateway.yaml` mapea grupos del IdP a allowlists de
modelos y managed settings, y su Admin API de spend limits puede limitar el gasto
por usuario, grupo u organización. Reenvía telemetría por OTLP y emite eventos de
auditoría JSON de una línea. El
[anuncio](https://claude.com/blog/introducing-the-claude-apps-gateway) de
Anthropic del 29 de junio de 2026 la presenta como infraestructura gateway
first-party para Claude Code.

## Ejecútala. Olivares la gobierna.

Si ya ejecutas la gateway de Anthropic, o planeas hacerlo, consérvala. La doctrina
es **y, no o**: la gateway de Anthropic posee la sesión gateway de Claude Code, el
acceso a modelos y la ruta de upstreams; Olivares AI convierte ese despliegue en
una superficie gobernada dentro del control plane más amplio.

El conector `claude-apps-gateway` inventaría `gateway.yaml`: issuer,
IdP-group -> allowlists de modelos, postura del spend admin, destinos OTLP y
upstreams. Eleva findings de postura sobre los estados de configuración que
importan a un operador de gobernanza, e ingiere los eventos JSON de auditoría de
la gateway para que denegaciones, emisiones de sesión y registros de inferencia
entren en el audit ledger con alteraciones detectables. Apunta el fan-out OTLP de la gateway al
receptor OTLP de Olivares y la señal `session.id` podrá correlacionarse con los
registros de runtime de sesiones gobernadas; Olivares sigue reteniendo datos
estructurales, no payloads de prompts.

## Límites documentados

Decisiones de alcance documentadas por Anthropic, citadas de sus docs a fecha de
2026-07-03. Son declaraciones de alcance, no defectos; definen dónde debe estar
la frontera del co-despliegue. Las celdas de estado y notas se mantienen en inglés
porque son citas literales.

| Funcionalidad | Estado | Notas |
|---|---|---|
| SAML, LDAP, and other non-OIDC auth | Not supported. | OIDC only. Front with an OIDC bridge if needed |
| Multi-tenant (multiple OIDC issuers) | Not supported. | One issuer per gateway. Run separate instances |
| Admin UI | Not available. | Configuration is the YAML file; redeploy to change it |
| Helm chart | Not available. | The gateway runs as a standard stateless Deployment |
| CI pipelines | There is no service-token flow for unattended pipelines |  |
| OTLP/gRPC | Not supported. | OTLP over HTTP only |
| Windows server | Not supported. | Deploy on Linux |
| Model catalog | Claude models only | the gateway translates Claude IDs per upstream |

## Qué añade Olivares al lado

Olivares no elimina esos límites de la gateway de Anthropic. Añade el plano de
gobernanza que falta junto a ella.

| Límite de la gateway de Anthropic | Capacidad de Olivares junto a ella |
|---|---|
| SAML, LDAP, and other non-OIDC auth | Para la consola y el plano de gobernanza de Olivares, [SSO/SCIM identity](/es/how-to/connectors/sso-scim-identity/) documenta federación OIDC/SAML y [la arquitectura con tu IdP](/es/explanation/architecture/where-it-fits-with-your-idp/) mapea humanos y agentes sobre SSO/SCIM y rosters SPIFFE/WIF. Esto no incorpora SAML a la gateway de Anthropic; mantén la gateway en OIDC o ponle delante un puente OIDC. |
| Multi-tenant (multiple OIDC issuers) | El [control plane multi-tenant](/es/reference/modules/xx-multi-tenancy/) de Olivares delimita entidades, findings, sesiones y audit ledger por tenant, con RLS de PostgreSQL en despliegues multi-tenant. Ejecuta instancias separadas de la gateway por issuer y gobierna cada una como su propia superficie; no trates una gateway de Anthropic como multi-issuer. |
| Admin UI | La consola web de Olivares es una capa de presentación sobre la misma API descrita por el [módulo XIX](/es/reference/modules/xix-api-manage-as-code/), y la documentación de identidad muestra la UI viva **Identity & NHI -> SSO & SCIM**. Es una consola de administración del control plane, no un editor visual del `gateway.yaml` de Anthropic. |
| Helm chart | Olivares entrega su propio [despliegue Kubernetes con Helm](/es/tutorials/getting-started/kubernetes/) y un operador de Kubernetes separado. Esto despliega el control plane de Olivares; no afirma empaquetar la gateway de Anthropic. |
| CI pipelines | La automatización de Olivares puede usar tokens API opacos, revocables y ligados a tenant mediante [manage-as-code](/es/how-to/manage-as-code/). Para credenciales gobernadas de runtime y despliegue, el broker WIF/SPIFFE acuña credenciales de vida corta; eso está separado de la gateway de Anthropic, cuya guía para CI sigue siendo proveedor-directo salvo que uses deliberadamente el endpoint proxy de Olivares descrito abajo. |
| OTLP/gRPC | El receptor `claude` de Olivares acepta las rutas OTLP normales usadas por [OpenTelemetry GenAI](/es/how-to/connectors/otel-genai/), incluidas HTTP y gRPC. La gateway de Anthropic sigue enviando OTLP/HTTP; otros agentes gobernados pueden usar gRPC directamente, y los eventos resultantes pueden alimentar el audit ledger criptográfico y los [paquetes de evidencia de cumplimiento](/es/reference/modules/xiii-compliance/). |
| Windows server | Aquí no se reclama ninguna capacidad de servidor Windows. Ejecuta los componentes de servidor en Linux, contenedores o Kubernetes, y gobierna los endpoints de desarrollador mediante telemetría, hooks y evidencia de conectores. |
| Model catalog | [Módulo X](/es/reference/modules/x-models/) gobierna un estate de modelos/proveedores cross-vendor: Claude, OpenAI, Gemini e inferencia local; el conector Bedrock añade observabilidad de uso/coste y Guardrails de Bedrock. La gateway de Anthropic sigue siendo solo Claude mientras Olivares gobierna el estate más amplio, incluida la postura de Codex mediante [gobernanza de agentes autenticados por suscripción](/es/explanation/positioning/governing-subscription-authed-agents/). |

## Superset de protocolo, fase 1

Anthropic publica el protocolo de gateway e invita a implementaciones de terceros.
El proxy de inferencia de Olivares implementa un superset de fase 1 descrito por
el contrato de ingeniería del protocolo de apps-gateway:
descubrimiento OAuth, autorización de dispositivo RFC 8628, polling de token a
través del seam de credenciales de sesiones tras aprobación autenticada, entrega
de managed settings en modo documento único con ETag, la forma de lista read-only
de spend limits y `GET /protocol`.

El descriptor documenta las divergencias por sí mismo: los managed settings están
en modo documento único, la cabecera de versión es `x-olivares-version`, las rutas
de escritura/effective/audit de spend limits devuelven respuestas `501`
conformes, y Olivares conserva su mapeo más rico de denegaciones por presupuesto
mientras añade `x-should-retry: false`. La fase 1 no entrega el callback OIDC ni
la página de navegador `/device` de Anthropic, reglas de merge por grupo para
managed settings, rutas de escritura de spend limits, `count_tokens` ni atribución
por cabeceras `x-claude-code-session-id`.

## Elegir una topología

- **Solo la gateway.** Suficiente para una organización OIDC de un solo issuer,
  solo Claude, cómoda gestionando YAML y redespliegues, y satisfecha con los spend
  limits, el fan-out OTLP y la salida JSON de auditoría de la propia gateway.
- **Gateway + Olivares.** El co-despliegue recomendado cuando Claude Code entra en
  un estate regulado: conserva la gateway de Anthropic, añade el conector
  `claude-apps-gateway`, apunta OTLP a Olivares y conserva la imagen resultante de
  postura, runtime y evidencia en el control plane.
- **Proxy de Olivares como endpoint del protocolo de gateway.** Úsalo cuando
  quieras deliberadamente que el proxy de inferencia de Olivares sirva la
  superficie de fase 1 del protocolo de gateway. Es útil cuando el subconjunto
  entregado basta; no es un reemplazo completo del flujo OIDC de navegador ni de
  la administración de escritura de spend limits de la gateway de Anthropic.
