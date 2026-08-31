---
title: Olivares AI frente a las torres de control de IA
description: >-
  Cómo se relaciona Olivares AI con las torres de control de IA y los paneles de
  gobernanza de ecosistemas (ServiceNow AI Control Tower, planos de administración
  de agentes de los hyperscalers). Integramos, no competimos: somos la fuente de
  verdad por debajo de la torre.
sidebar:
  order: 4
---

Una **torre de control de IA** es la capa de panel y flujo de trabajo a escala de
organización para la gobernanza de IA: un único lugar donde ver los agentes
registrados, encaminar aprobaciones, abrir tickets e informar de la postura a la
dirección. Algunos ejemplos son **ServiceNow AI Control Tower** y los planos de
administración de agentes de los hyperscalers (las superficies Entra Agent ID /
Agent 365 de Microsoft, las funciones de gobernanza de AWS AgentCore).

Si has invertido en una, la pregunta correcta no es "¿torre u Olivares?". Es
"¿qué alimenta la torre con la verdad?". Nuestra respuesta, deliberadamente, es
**integramos; no competimos.**

:::tip[La versión breve]
Las torres de control son potentes en **flujo de trabajo, ticketing, paneles a
escala de organización y gobernanza de agentes dentro de su propio ecosistema**.
Son débiles en **estates heterogéneos, autoalojados y multinube** y en la **verdad
de base**: lo que un agente tocó realmente, corroborado contra el plano de datos.
Olivares AI es la **capa de origen por debajo de la torre**: produce el inventario
atribuido, la deriva Permitido-vs-Observado y la evidencia a prueba de
manipulaciones, y **las eleva**.
:::

## Lo que las torres de control hacen bien

- **Flujo de trabajo e ITSM**: aprobaciones, registros de cambios, tickets de
  incidencias, propiedad — el proceso existente de la organización, donde la
  gobernanza de IA debería integrarse en lugar de iniciar un silo paralelo.
- **Informes ejecutivos**: un único panel para la dirección a través de muchas
  iniciativas de IA.
- **Gobernanza nativa del ecosistema**: la torre de un hyperscaler gobierna bien los
  agentes *en la nube de ese hyperscaler* — sus identidades, sus políticas, su
  runtime.

Son fortalezas reales y no las reproducimos. Olivares AI no es un producto ITSM ni
pretende ser el panel de informes de tu CISO.

## Dónde dejan un hueco las torres

| Hueco | Por qué importa | Qué aporta Olivares AI |
|---|---|---|
| **Estate heterogéneo** | Los agentes corren en varias nubes, on-prem, portátiles y CI — no solo en el runtime de un único proveedor | Inventario y access map a escala de estate a través de almacenes SQL/objetos/warehouse, MCP, herramientas y el agente de desarrollo local |
| **Verdad de base** | Una torre muestra lo que está *registrado*; rara vez corrobora lo que los agentes *hicieron* | Telemetría autorreportada cotejada contra pgAudit / CloudTrail / eBPF — Permitido-vs-Observado como un hecho |
| **Enforcement sobre el agente de desarrollo** | Las torres observan; pocas pueden detener la acción de un agente local de forma deny-closed | El [PEP de hooks de Claude Code](/es/how-to/connectors/claude-code-hooks-pep/) y las puertas de actuación deny-closed |
| **Evidencia con alteraciones detectables** | Los paneles son mutables; los auditores quieren pruebas inmutables | Audit ledger append-only firmado con Ed25519; paquetes de evidencia OSCAL; verificación fuera de la caja |
| **Soberanía** | Las torres SaaS procesan tus datos de gobernanza en su nube | Autoalojado / air-gapped; el plano de datos nunca sale de tu perímetro |

## Cómo nos integramos (en ambas direcciones)

Olivares AI está construido para situarse **por debajo** de tu torre y alimentarla, y
para **leer de** las torres que exponen un roster.

- **Eleva la postura y la evidencia hacia arriba.** Exporta el inventario y la
  postura para que una torre de control los consuma (`GET /v1/m/posture/export`), y
  reenvía el audit ledger y los hallazgos a tu **SIEM/ITSM** para que aterricen en el
  flujo de trabajo que ya operas.
  → [Reenviar la auditoría a Splunk](/es/how-to/forward-audit-to-splunk/)
- **Lee los rosters de identidad hacia abajo, en solo lectura.** Los conectores de
  federación de identidad sincronizan los rosters de agentes desde **Microsoft Entra
  Agent ID**, **AWS AgentCore Identity**, **Google Agent Identity**, y en solo lectura
  desde **Microsoft Agent 365** y **ServiceNow AI Control Tower** — mapeándolos sobre
  el roster SPIFFE/WIF para que el access map atribuya las aristas a identidades
  reales y gobernadas. Consulta
  [Dónde encaja Olivares AI con tu IdP](/es/explanation/architecture/where-it-fits-with-your-idp/).

La relación es **complementaria por diseño**: la torre posee el flujo de trabajo y la
vista de sala de juntas; Olivares AI posee la verdad de base y la evidencia inmutable
que hacen confiables las cifras de la torre.

## Cuándo basta con la torre

Si todo tu estate de agentes vive dentro de **un** único ecosistema de hyperscaler o
SaaS, la torre nativa de ese proveedor lo gobierna, y **no tienes ningún requisito de
soberanía ni huella heterogénea/autoalojada**, puede que no necesites un control
plane aparte — la torre nativa más su exportación de auditoría pueden cubrirte.
Olivares AI se vuelve necesario cuando el estate es **mixto**, cuando necesitas
**verdad de base corroborada en lugar de un registro**, o cuando **un control plane
alojado por un proveedor no es una opción para tu evidencia de gobernanza**.
