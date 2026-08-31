---
title: "Reporting — informes profesionales HTML/PDF"
description: >-
  Genera informes HTML y PDF descargables a partir de los datos de compliance,
  auditoría y FinOps de la plataforma. Hay cinco tipos integrados bajo demanda;
  los informes programados son un add-on enterprise.
---

Reporting (`modules/reporting`) está **LIVE**. Convierte los datos de compliance,
auditoría y FinOps de la plataforma en un único documento profesional, para que
un auditor descargue la evidencia en lugar de copiar y pegar JSON de varias API.

## Informes integrados

El módulo open-core ofrece cinco tipos de informe bajo demanda:

- `compliance-evidence` — postura de compliance por framework, con estado de
  controles y evidencia.
- `audit-summary` — totales de eventos de auditoría y verificación de integridad del ledger.
- `finops-report` — gasto de IA por modelo y proveedor.
- `access-review` — usuarios y datos de acceso para revisiones periódicas.
- `executive-summary` — vista compacta de gobernanza, riesgo, coste y adopción.

`GET /v1/m/reporting/reports` enumera los tipos y formatos. Genera uno con
`GET /v1/m/reporting/reports/{type}`; HTML es el formato predeterminado y
`?format=pdf` descarga un PDF. Las rutas requieren `reporting:report:read`.

## Open core y enterprise

El HTML bajo demanda está incluido en el binario open-core. El PDF bajo demanda
está incluido cuando hay un ejecutable compatible con Chromium. **Add-on
enterprise:** la generación programada de informes está gobernada por build tag
y no forma parte del runtime community.

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
- [Catálogo de módulos](/es/reference/modules/overview/) — los 30 módulos
  conectados y su madurez honesta.
