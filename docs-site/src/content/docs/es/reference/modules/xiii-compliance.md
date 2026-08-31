---
title: "Módulo XIII — compliance y regulatorio"
description: >-
  Mapear lo que el control plane ya observa y audita sobre marcos regulatorios, y
  exportar evidencia consumible por auditores derivada del ledger append-only.
  Diseñado-para-auditoría, nunca certificado: estado + evidencia, nunca "compliant".
---

El módulo XIII abre puertas empresariales **mapeando** lo que el control plane ya observa y
audita sobre marcos regulatorios, y produciendo **evidencia consumible por auditores**
derivada del audit ledger append-only y hash-chained. Es un módulo de la capa de
inteligencia: no captura **nada nuevo** — agrega y transforma lo que el núcleo y los demás
módulos ya registran, y **nunca reclama certificación**.

## Qué es

El módulo XIII tiene cinco superficies, todas de lectura-y-derivación sobre datos
existentes:

- **Un catálogo de controles versionado** mantenido en el repo como la fuente de verdad
  determinista — EU AI Act, NIST AI RMF, ISO/IEC 42001, SOC 2 / ISO 27001 y GDPR (más
  cross-walks GenAI/agénticos), modelados como **controles** versionados, cada uno con su
  requisito y su criterio de satisfacción. Es un **mapeo técnico, no asesoramiento legal**, y
  un control cuya obligación la plataforma no puede evidenciar lleva una nota explícita para
  que la cobertura parcial nunca se lea como total.
- **Un mapa declarativo control → evidencia.** Cada control mapea a **capacidades** del
  control plane. Una capacidad es o bien **operacional** — presente solo cuando existen datos
  reales del tenant (un ledger que verifica, aristas de acceso observadas, security findings,
  resultados de evaluación, despliegues, una clasificación de riesgo, una atestación de
  residencia) — o bien **arquitectónica** — una garantía de diseño de la plataforma citada a
  los docs de diseño y etiquetada como tal, nunca como telemetría.
- **Evidencia de auditoría exportable** — un paquete de evidencia sellado y append-only
  derivado del ledger.
- **Clasificación de riesgo de agentes** en un tier del EU AI Act cross-mapeado a las
  funciones de NIST AI RMF, a partir de atributos observados — gobernada y auditada.
- **Residencia de datos** — una atestación por región de dónde se ejecutan realmente el
  despliegue y sus stores, más un scan que convierte señales de egress existentes en un
  finding de residencia.

## Estado de control y entidades

El estado se computa honestamente, nunca se asevera. Un control está **satisfied** solo
cuando toda capacidad mapeada está presente **y al menos una es operacional**; **by_design**
cuando todas las capacidades presentes son arquitectónicas (listo en diseño, nunca
satisfied); **partial** cuando algunas están presentes; **gap** cuando ninguna lo está;
**unmapped** cuando ninguna capacidad lo respalda en absoluto. `satisfied` nunca descansa
solo sobre evidencia de diseño.

El módulo declara cuatro entidades append-only / auditadas en el modelo de datos
compartido: un **paquete de evidencia** sellado (que registra la secuencia y el hash de la
cabeza de cadena y el resultado de la verificación viva del hash-chain), un **resultado**
por control dentro de ese paquete, una **clasificación de riesgo** por sujeto, y una
**atestación de residencia** por región. El paquete de evidencia **referencia** el ledger
por secuencia y hash y prueba que las alteraciones de su cuerpo son detectables con un hash de
manifiesto determinista — nunca copia el ledger y nunca contiene payloads ni PII.

## Qué consume y qué produce

La clasificación de riesgo lee atributos ya registrados por otros módulos — aristas de
[acceso](/es/reference/modules/iii-access-map/) read/write salientes, security findings
high/critical, y una señal opcional de autonomía — y produce un tier **sugerido** que es
gobernado: un humano debe revisarlo y aprobarlo, y el motor de sugerencia **nunca puede
asignar el tier inaceptable** (eso es una determinación legal). El scan de residencia
correlaciona el lineage de egress existente frente a atestaciones `self_hosted` y, por
violación, eleva un finding del núcleo y publica una señal de bus interna para que el módulo
de notificaciones (XV) la entregue a SIEM/Slack/PagerDuty. Leer o exportar un paquete de
evidencia, sellar uno, clasificar o revisar riesgo, y atestar residencia son acciones
privilegiadas, con alcance de tenant, que se **autoauditan en el ledger** dentro de la propia
transacción del llamante.

:::caution[Límites honestos]
- **Diseñado-para-auditoría, nunca certificado.** Cada respuesta de reporting lleva el aviso
  de que **no es una certificación ni asesoramiento legal**. La salida habla de estado de
  control y evidencia; nunca dice "compliant" ni "certified". Las garantías opt-in (como el
  cifrado en reposo) tienen por defecto **absent** hasta que se atestan.
- **Sin actuación.** Este módulo mapea controles y exporta evidencia — no remedia, no
  refuerza ni cambia nada. Su único efecto colateral es el finding de residencia y la señal
  de bus, sobre los que actúan otros módulos.
- **La evidencia vale lo que valen sus fuentes.** Un control sin datos de tenant que lo
  respalden es un **gap** honesto, no un aprobado falseado; una capacidad operacional ausente
  baja el estado de un marco en lugar de inflarlo. La evidencia de least-privilege drift
  consume el drift **reconciliado** del módulo III (no la ruta cruda del store), por lo que
  hereda los límites de cobertura por tiers del módulo III — una arista ausente no es prueba
  de que un acceso no ocurrió.
- **La evidencia arquitectónica es diseño, no prueba.** Las capacidades citadas a los docs de
  diseño atestan cómo está construida la plataforma, no que un control se ejecutara en tu
  tenant; producen `by_design`, que es deliberadamente distinto de `satisfied`.
:::

## Relacionado

- [Catálogo de módulos](/es/reference/modules/overview/) — dónde encaja el módulo XIII y la
  división honesta gobernar/observar vs actuar.
- [Módulo III — mapa de acceso y recursos](/es/reference/modules/iii-access-map/) — la señal
  de drift que consumen el clasificador de riesgo y la capacidad de drift.
- [Honestidad y límites](/es/start/honesty-and-limits/) — por qué estado, no certificación.
- [Gobernar y aprobar](/es/how-to/govern-and-approve/) — revisar un tier de riesgo sugerido.
- [Reenviar auditoría a Splunk](/es/how-to/forward-audit-to-splunk/) — el feed continuo del
  ledger que el auditor re-verifica.
- [Visión general de la arquitectura](/es/explanation/architecture/overview/) — la capa de
  inteligencia y el bus de eventos.
