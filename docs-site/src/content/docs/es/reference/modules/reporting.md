---
title: "Reporting — informes profesionales HTML/PDF"
description: >-
  Genera informes HTML y PDF descargables a partir de los datos de compliance,
  auditoría y FinOps de la plataforma. Hay cinco tipos integrados bajo demanda;
  los informes programados forman parte de Business.
---

Reporting (`modules/reporting`) está **LIVE**. Convierte los datos de compliance,
auditoría y FinOps de la plataforma en un único documento profesional, para que
un auditor descargue la evidencia en lugar de copiar y pegar JSON de varias API.

**Edición:** Business Compliance Packs ofrece catálogos de marcos, evaluaciones, el calendario regulatorio, vistas DORA/HIPAA, sellado de evidencias, exportaciones OSCAL e informes HTML/PDF bajo demanda. Community devuelve `501` para esas capacidades y conserva riesgo, residencia, gestión de registros y exportaciones JSON/CSV de evidencias guardadas. Las actualizaciones conservan los registros existentes.

## Informes integrados

Business Compliance Packs ofrece cinco tipos de informe bajo demanda:

- `compliance-evidence` — postura de compliance por framework, con estado de
  controles y evidencia.
- `audit-summary` — totales de eventos de auditoría y verificación de integridad del ledger.
- `finops-report` — gasto de IA por modelo y proveedor.
- `access-review` — usuarios y datos de acceso para revisiones periódicas.
- `executive-summary` — vista compacta de gobernanza, riesgo, coste y adopción.

`GET /v1/m/reporting/reports` enumera los tipos y formatos. Genera uno con
`GET /v1/m/reporting/reports/{type}`; HTML es el formato predeterminado y
`?format=pdf` descarga un PDF. Las rutas requieren `reporting:report:read`.

## Límites, expresados con claridad

- La generación de PDF arranca Chromium en modo headless. Sin `chromium`,
  `chromium-browser` o `google-chrome`/`chrome` en `PATH`, las solicitudes PDF devuelven
  `501`; HTML sigue disponible.
- Un informe compliance-evidence necesita la fuente de datos de compliance. Si
  no está conectada, el documento incluye el aviso explícito «Data source not
  configured» en vez de inventar evidencia.
- Este módulo renderiza documentos a partir de datos que ya conserva la
  plataforma. No sustituye al audit ledger, la evaluación de compliance ni la
  fuente autoritativa de FinOps.

## Relacionado

- [Compliance y regulación](/es/reference/modules/xiii-compliance/) — fuente de
  postura y evidencia de compliance.
- [Costes y AI FinOps](/es/reference/modules/xi-finops/) — superficie
  autoritativa de gasto.
- [Catálogo de módulos](/es/reference/modules/overview/) — los 32 módulos
  conectados y su madurez honesta.
