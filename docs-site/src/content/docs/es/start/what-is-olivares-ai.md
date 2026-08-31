---
title: ¿Qué es Olivares AI?
description: >-
  Integra, gestiona y asegura la IA que ejecutas, desde una sola máquina hasta toda una
  infraestructura — un único ground truth: Claude Code al nivel más profundo, Codex y Grok
  Build a su lado. Un único binario autoalojado que da a tu IA contexto, acceso a recursos y
  sesiones gestionadas, y te da a ti los permisos, las políticas, los presupuestos y la
  evidencia de auditoría para operarla en tu infraestructura — sin telemetría obligatoria ni
  egreso del plano de control de forma predeterminada. Solo cruza tu perímetro lo que tú
  configuras para que lo cruce, desde llamadas a tus API de modelos hasta las salidas
  SIEM/webhook que conectas.
---

Olivares AI **integra, gestiona y asegura la IA que ejecutas** — en una sola máquina o a
lo largo de toda una infraestructura, un único ground truth: Claude Code al nivel más
profundo, Codex y Grok Build a su lado, complementándolos en lugar de competir. A medida
que pones a trabajar más modelos, agentes, servidores MCP y herramientas sobre
infraestructura real y heterogénea, dos cosas se vuelven difíciles a la vez: hacer la IA
genuinamente útil y mantenerla bajo control. Esto es tan cierto para una sola máquina
autoalojada como para una infraestructura regulada; la diferencia es de escala, no de
naturaleza.

Olivares AI hace ambas. Por un lado da a tu IA lo que necesita para trabajar — contexto,
acceso a los recursos adecuados, sesiones gestionadas. Por otro te da a ti los **permisos
granulares, las políticas, los presupuestos y la evidencia de auditoría** para operarlo
todo: qué modelo y qué agente puede alcanzar qué, los datos que tocan, qué se les permite
ejecutar, cuánto gastan, y la prueba que puedes entregar a un regulador.

Todo corre como un **único binario autoalojado** en tus propios hosts. No hay telemetría
obligatoria ni egreso del plano de control de forma predeterminada. Solo cruza tu perímetro
lo que **tú** configuras para que lo cruce: llamadas a tus API de modelos, las salidas
SIEM/webhook que conectas y un proveedor externo de embeddings si aprovisionas uno. Es una
propiedad de la arquitectura y de tu configuración; es una descripción, **no una garantía**.

## Una capacidad: el mapa de acceso de lectura/escritura

Entre esas capacidades está el **mapa de acceso L/RW**. Para cada origen (un agente, una
identidad no humana, una sesión) construye una arista hacia
cada recurso que toca, clasificada como **lectura**, **escritura**, **lectura-escritura** o
**desconocido**, y etiquetada con:

- **de dónde vino la señal** (`SignalSource`) — OpenTelemetry desde un
  agente cooperativo, una clasificación READ/WRITE de pgAudit de Postgres, un registro de
  AWS CloudTrail, un backstop a nivel de kernel eBPF/Tetragon, una anotación MCP
  (tratada como **no confiable** y corroborada, nunca confiable por sí sola), una concesión
  de política declarada, o una señal de agente a agente (A2A); y
- **cuánto confiar en la atribución** (`Confidence`) — `attributed` cuando está
  firmemente ligada a una identidad por agente, `approximate` cuando se infiere (una cuenta
  de servicio compartida, o un almacén con pérdida).

En su centro está el diff: **Permitido frente a Observado**. Las aristas permitidas vienen
de concesiones declaradas; las aristas observadas vienen de telemetría y auditoría reales.
Compararlas saca a la superficie *accesos inesperados* (un agente leyendo una tabla que
nunca le fue concedida), *concesiones sin usar* (un permiso que ningún agente ejerció jamás)
y aristas *pendientes de reconciliación* (un acceso que el sistema aún no puede atribuir
firmemente).

El producto es **honesto sobre la fidelidad**. La cobertura es **escalonada**: limpia en
almacenes con auditoría nativa (SQL, almacenamiento de objetos, almacenes de datos), con
pérdida en algunos almacenes (documento/vector), e imposible de reconstruir pasivamente en
otros (p. ej. Redis, SQLite, D1). Cuando no se puede determinar la naturaleza de
lectura/escritura, el modo es `unknown` — el producto nunca fabrica una clasificación.

## Una plataforma, no una única funcionalidad

El mapa de acceso es una capacidad entre muchas. El producto es una **plataforma modular**
(en el espíritu de Grafana o Backstage): un motor más módulos más conectores, diseñada para
que cualquier módulo se acople sin rearquitecturar el resto. Incluye **30 módulos** —
inventario y sesiones en vivo, el mapa L/RW, orquestación de agentes (A2A, en desarrollo), gestión de MCP y
de skills, identidad e identidad no humana, despliegue, conocimiento y contexto, seguridad y
guardrails, gestión de modelos y proveedores, coste/FinOps, evals y un sandbox de pruebas,
red-teaming, cumplimiento y evidencia, un catálogo interno, integraciones de salida y push a
SIEM, voz/tiempo real, y salud/SLA — más capacidades de plataforma no contadas entre los
30 (su propia API y manage-as-code, multi-tenancy, cuadros de mando ejecutivos) — a
través de **158 integraciones** (un recuento medido
desde el código por `scripts/check-public-counts.sh`). Unas pocas capacidades son pre-v1 o seams
deny-closed hasta que se aprovisionan; los docs son explícitos sobre cuáles.

Consulta el [catálogo de módulos](/es/reference/modules/overview/) para la lista completa, y el
[resumen de arquitectura](/es/explanation/architecture/overview/) para ver cómo encajan el motor
y los módulos.

## Cómo observa: lectura primero, datos mínimos

Olivares AI es de **lectura primero**: el motor observa a través de logs, OpenTelemetry y
eBPF; **no** se sitúa en la ruta de datos del agente, así que un fallo del colector nunca
rompe tu tráfico de producción. Y es de **datos mínimos por diseño**: el grafo de
acceso almacena **relaciones** — origen → recurso, lectura/escritura, fuente, confianza,
marca de tiempo — **nunca payloads, cuerpos de SQL, secretos ni PII**. Lo que no se
almacena no puede filtrarse.

Esta es también la razón por la que es autoalojable y compatible con air-gap: no hay
telemetría obligatoria ni egreso del plano de control de forma predeterminada. Solo cruza
tu perímetro lo que **tú** configuras para que lo cruce — llamadas a tus API de modelos,
las salidas SIEM/webhook que conectas y un proveedor externo de embeddings si aprovisionas
uno. Olivares AI no está en esa lista: el proveedor nunca está en la ruta de datos. Solo se
le llega cuando le pides algo — `olivares upgrade`, o una descarga por suscripción de add-ons
comerciales y sus actualizaciones — nunca como efecto de ejecutar. Y `olivares upgrade --endpoint` apunta incluso eso a tu propio mirror. Es un argumento
sólido para la residencia de datos, el RGPD y los entornos air-gapped.

## A dónde ir después

- **Pruébalo:** el [tutorial de cero a grafo](/es/tutorials/zero-to-graph/) arranca el
  binario único y llega a un grafo Permitido-frente-a-Observado poblado.
- **Entiéndelo:** el [resumen de arquitectura](/es/explanation/architecture/overview/)
  y el [modelo de seguridad y amenazas](/es/explanation/security/threat-model/).
- **Opéralo:** [autoalojamiento](/es/how-to/self-hosting/) y
  [instalación air-gapped](/es/how-to/air-gap-install/).

:::note[Estado]
Olivares AI está **pre-1.0**. El binario único compila, arranca y llega a un grafo de
acceso poblado hoy (esto lo ejercita de extremo a extremo la suite de tests), pero varias
capacidades están en fase de diseño o son post-v1. La documentación es explícita sobre lo
que corre ahora frente a lo que está planificado — consulta
[Honestidad y límites](/es/start/honesty-and-limits/).
:::
