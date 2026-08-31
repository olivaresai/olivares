---
title: "Explicación"
description: "Visión general orientada a la comprensión de Olivares AI: cómo integra, gestiona y asegura la IA empresarial de forma un único ground truth: Claude Code al nivel más profundo, Codex y Grok Build a su lado — su arquitectura modular a lo largo de 30 módulos, el access map read-first y el modelo open-core."
---

Esta sección está orientada a la comprensión. Explica *por qué* Olivares AI tiene la forma
que tiene — los principios de diseño, la postura de seguridad y el modelo de licencia —
en lugar de guiarte a través de una tarea. Si quieres *hacer*
algo, empieza por el [tutorial](/es/tutorials/zero-to-graph/) o las
[guías how-to](/es/how-to/connect-claude-code/); si necesitas un contrato exacto, usa
la [referencia](/es/reference/). Para saber dónde vive cada tipo de página, consulta
[Cómo están organizados los docs](/es/start/how-the-docs-are-organized/).

:::note[Producto en etapa de diseño]
Buena parte de la profundidad descrita aquí es pre-1.0 y de etapa de diseño. Estas páginas son
honestas sobre qué corre hoy frente a qué está planificado o es post-v1. Cuando una capacidad
no está construida, o su cobertura es parcial, la página lo dice. Consulta
[Honestidad y límites](/es/start/honesty-and-limits/) para las divulgaciones permanentes del proyecto.
:::

## Una plataforma modular: motor + módulos + conectores

Olivares AI ayuda a las empresas a **integrar, gestionar y asegurar la IA que ya
ejecutan** — un único ground truth: Claude Code al nivel más profundo, Codex y Grok Build a su lado, complementándolos en lugar de
competir. Se entrega como un único binario estático de Go (`olivares`) con la UI web
embebida y servida desde el mismo origen que la API. La arquitectura es una
plataforma, no una sola herramienta: un **motor central** proporciona los subsistemas compartidos —
ingesta y un bus de eventos in-process, el SDK de conectores, el runtime de módulos, un
modelo de datos multitenant, la API REST/gRPC, autenticación y autorización, y
el audit ledger append-only — y cada capacidad es uno de **30 módulos** que
cuelga de esos subsistemas sin re-arquitecturar el núcleo. Los **conectores** alimentan
el motor desde fuera a través de un SDK estable; un conector nunca importa del
núcleo, lo que mantiene limpia la frontera de licencia.

El store predeterminado es SQLite (pure-Go) para uso de un solo nodo y air-gapped, pasando
a Postgres con row-level security para multitenancy y escala. El bus de eventos es
in-process por defecto; NATS es un binding distribuido opcional, no un
requisito. La plataforma entrega hoy **30 módulos**, cada uno con su propia madurez honesta
— la mayoría en vivo y cableados de extremo a extremo, algunos parciales u opt-in — a lo largo de nueve
áreas de capacidad; el registro de modelos propios y el fine-tuning es una **capacidad planificada**,
no un módulo entregado.

→ Lee [Visión general de la arquitectura](/es/explanation/architecture/overview/) para el
motor completo, el modelo de datos y las topologías de despliegue.

## El access map: read-first, minimal-data, Permitted-vs-Observed

Entre las más útiles de las 30 capacidades está el **R/RW access map**. Construye
un grafo de qué agente lee o escribe qué recurso, y lo hace con dos
restricciones deliberadas:

- **Read-first.** El map observa a través de telemetría, logs de auditoría nativa y un
  respaldo eBPF a nivel de kernel — se sitúa fuera de la ruta de datos, nunca en ella. No
  hace de proxy, ni intercepta, ni gestiona el tráfico en vivo.
- **Minimal-data.** Almacena solo la relación (agente → recurso, lectura o
  escritura) junto con la fuente de la señal y un nivel de confianza. No almacena
  payloads, secretos ni PII.

Sobre ese grafo se sitúa la vista más distintiva: el **diff de Permitted-vs-Observed**,
que aflora el least-privilege drift comparando lo que la política *permite* frente a
lo que se *observa* que hacen los agentes. La ruta cooperativa y de alta fidelidad es Claude
Code vía OpenTelemetry más introspección MCP, corroborada por la auditoría
nativa del store (por ejemplo, pgAudit clasificando lecturas y escrituras, o CloudTrail
exponiendo el acceso de solo lectura en object storage); el respaldo no cooperativo es
eBPF en el kernel. Las anotaciones MCP se tratan como no fiables según la especificación
MCP y se corroboran, nunca se confía en ellas por sí solas.

:::caution[La cobertura es por niveles]
La fidelidad depende de la fuente. Es limpia para bases de datos SQL, object stores y
warehouses; con pérdidas para sistemas como bases de datos documentales y vectoriales; y no
es alcanzable pasivamente para algunos stores (por ejemplo Redis, SQLite o D1). El map
muestra su nivel de confianza en lugar de fabricar una atribución que no tiene.
:::

→ Lee el [Modelo de seguridad](/es/explanation/security/security-model/) para la
postura y el [Modelo de amenazas](/es/explanation/security/threat-model/) para los
supuestos y los límites.

## Self-hosted y open-core

El data plane — los collectors — **siempre corre en la infraestructura del cliente**, de modo que
los datos del estate no tienen que salir de la frontera del cliente. El control plane puede
correr como un único binario self-hosted, como un despliegue distribuido (collectors
empujando a un núcleo central sobre gRPC con mTLS, respaldado por Postgres) o totalmente
air-gapped con cero egress y una licencia offline; una opción gestionada es trabajo
futuro.

La licencia es open-core. El núcleo del motor, los módulos y la UI web son
AGPL-3.0-only; el SDK y los conectores son Apache-2.0; un tier enterprise es
comercial. Esta división es lo que permite a terceros construir conectores sin que la
frontera copyleft alcance su código.

→ Lee [Open core y licencia](/es/explanation/open-core-and-licensing/) para el
mapa de licencias por directorio y lo que significa en la práctica.

## Decisiones de arquitectura

El razonamiento detrás de las decisiones de carga — tokens bearer opacos en lugar de
JWTs, el PDP de autorización enchufable detrás de una única juntura, SQLite-a-Postgres,
el audit ledger hash-chained y firmado — está registrado como Architecture Decision
Records.

## Regulación, posicionamiento y encaje

Otros dos hilos orientados a la comprensión acompañan a la arquitectura. El primero
es **regulatorio**: cómo el control plane convierte el comportamiento en vivo de tu estate
en la evidencia técnica que necesita un expediente del Reglamento de IA de la UE, generada a partir de datos de runtime
y almacenada en el control plane que operas tú mismo.

→ Lee [Evidencia del Reglamento de IA de la UE a partir de datos de runtime](/es/explanation/eu-ai-act-evidence/).

El segundo es **dónde se sitúa el producto en el mercado** — definido con honestidad, con
cada estadística trazada a una fuente primaria. Estas páginas explican el vocabulario de los analistas
(agent sprawl, guardian agents, AI TRiSM), cómo se relaciona Olivares AI con
herramientas adyacentes (gateways/observabilidad de LLM, torres de control de IA — integramos, no
competimos), la vertical de educación superior, y de dónde vienen los datos y las afirmaciones.

→ Navega [Posicionamiento y encaje](/es/explanation/positioning/market-context-and-sources/),
empezando por el verificado
[contexto de mercado y fuentes](/es/explanation/positioning/market-context-and-sources/).
