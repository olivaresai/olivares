---
title: "Exportación de postura a torres de control"
description: >-
  Una proyección saliente de solo lectura de la postura de ground-truth del
  motor — inventario descubierto, desviación de mínimo privilegio y findings de
  seguridad — que una torre de control extrae para enriquecer su propia vista.
  Una proyección en JSON neutral, no un push nativo verificado.
---

La exportación de postura (`modules/posture-export`) es la **superficie de postura
saliente** del motor: un único endpoint de solo lectura que una torre de control
sondea para enriquecer su propio inventario con el
[grafo de acceso](/es/reference/modules/iii-access-map/) de ground-truth del motor,
la desviación de mínimo privilegio, el inventario descubierto y la postura de
seguridad. Es la cara de "integrar, no competir" de la plataforma — nunca emite
identidad (eso es entrante, propiedad de
[gobernanza](/es/reference/modules/vi-governance/)), solo postura, y no cambia
nada.

## Qué expone

Una ruta, `GET /v1/m/posture/export`, restringida por `posture:export:read` y
fijada a un único ámbito de tenant. La respuesta es un documento JSON neutral
ensamblado dentro de **una sola transacción auditada** con tres proyecciones:

- **`inventory`** — entidades descubiertas activas (kind, ref, status, fuentes de
  señal, hosts, primera/última vista, recuento de ocurrencias), opcionalmente
  filtradas por `?kind=`.
- **`posture_drift`** — la desviación de mínimo privilegio reconciliada: accesos
  observados-pero-no-permitidos, más recuentos de grants sin usar y grants de
  inventario.
- **`findings`** — findings de seguridad proyectados solo como refs y un
  `detail_hash`, filtrables por suelo `?severity=` y `?category=`.

Cada exportación es de **datos mínimos** — solo refs, hashes y relaciones, nunca
un payload en bruto ni un secreto — y un pase de expurgo defensivo limpia cada
campo de formato libre. La exportación misma mueve datos fuera de la caja, así que
**se autoaudita** al ledger con el principal real en la misma transacción que las
lecturas.

## Madurez y contexto acotado

**PARTIAL.** La acción de exportación está live y auditada; lo que *no* está
verificado es el otro extremo. Los formatos de ingesta de las torres nombradas —
**Microsoft Agent 365** y **ServiceNow AI Control Tower** — no tienen una API de
fuente primaria contra la que el motor pudiera validar, así que esto es una
**proyección honesta en JSON neutral que una torre extrae (o que un operador
enruta a través de un sink configurado), explícitamente NO un push nativo
funcional**. Cada respuesta lleva esa nota de procedencia inline.

Topes por petición acotan el inventario, la desviación y los findings; una
exportación parcial informa de sus propios flags de truncamiento y nunca se
etiqueta como autoritativa.

## Relacionado

- [Reenviar auditoría a Splunk](/es/how-to/forward-audit-to-splunk/) — el plano
  `siemforward`, la contraparte de *push* que envía el ledger sellado y los
  findings a una torre SIEM.
- [Módulo XIII — cumplimiento y regulatorio](/es/reference/modules/xiii-compliance/) —
  la evidencia sellada con la que esta postura comparte su ground truth.
- [Módulo III — mapa de acceso y recursos](/es/reference/modules/iii-access-map/) —
  la desviación reconciliada que proyecta la exportación.
- [Honestidad y límites](/es/start/honesty-and-limits/) — por qué esto es una
  proyección, no un push verificado.
- [Catálogo de módulos](/es/reference/modules/overview/) — dónde se sitúa la
  exportación de postura entre los 30 módulos entregados.
