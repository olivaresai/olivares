---
title: Olivares AI frente a las gateways y la observabilidad de LLM (LiteLLM, Langfuse)
description: >-
  Una comparación honesta con el popular stack de LLM-ops autoalojado — una gateway
  (LiteLLM) más una plataforma de observabilidad (Langfuse). Qué hace bien cada uno,
  en qué se diferencia Olivares AI, y por qué es "y", no "o".
sidebar:
  order: 3
---

Un stack autoalojado habitual y sensato combina una **gateway de LLM** (por ejemplo
**LiteLLM**) con una **plataforma de observabilidad de LLM** (por ejemplo
**Langfuse**). Si tienes uno, podrías preguntarte razonablemente si necesitas un
control plane en absoluto. Esta página responde a eso con honestidad — incluidos los
casos en los que la respuesta es **no**.

:::tip[La versión breve]
LiteLLM y Langfuse tratan sobre **las llamadas al modelo que hace tu aplicación**:
encaminarlas, trazarlas, gestionar prompts, controlar el coste por llamada. Olivares
AI trata sobre **cada agente de tu estate y todo lo que lee o escribe** — bases de
datos, almacenes de objetos, servidores MCP, herramientas, ficheros — y si eso
coincide con lo que la política permite. Distinta altitud. Se componen; nosotros
**ingerimos la misma señal gen-ai de OpenTelemetry** que ellos emiten.
:::

## Lo que ese stack hace bien (úsalo para esto)

- **LiteLLM** — una gateway unificada y compatible con OpenAI por delante de muchos
  proveedores: encaminamiento, fallbacks, reintentos, claves virtuales, presupuestos
  y límites de tasa por clave, y contabilidad de costes sobre las llamadas al modelo
  que pasan por ella.
- **Langfuse** — ingeniería y observabilidad de LLM: **trazas** de petición/respuesta,
  gestión y versionado de prompts, evaluaciones, datasets y una interfaz orientada al
  desarrollador para depurar cadenas.

Si tu problema es *"instrumentar las llamadas a LLM de mi aplicación, depurar prompts
y gestionar el acceso a modelos desde un único endpoint"*, este stack es excelente y
autoalojable. No necesitas un control plane para eso, y no vamos a fingir lo
contrario.

## En qué se diferencia estructuralmente Olivares AI

| Dimensión | Gateway de LLM + observabilidad | Olivares AI |
|---|---|---|
| **Unidad de interés** | Una llamada al modelo (prompt → completion) | Un agente y cada recurso que lee/escribe — BD, almacenes de objetos, MCP, herramientas, ficheros |
| **Punto de observación** | **En la ruta de la petición** (proxy/SDK); ve lo que la aplicación envía | **Fuera de banda, read-first**; observa telemetría, auditoría nativa y un backstop de kernel — nunca en la ruta de datos |
| **Fuente de verdad** | Lo que la aplicación/proxy **reporta** | Telemetría autorreportada **corroborada contra el propio ledger del sistema** — pgAudit (lectura vs escritura), CloudTrail (acceso a objetos), backstop eBPF |
| **La pregunta clave** | "¿Qué hizo este prompt y cuánto costó?" | "¿Está este agente usando un acceso **que nadie concedió**?" — [deriva Permitido-vs-Observado](/es/explanation/#el-access-map-read-first-minimal-data-permitted-vs-observed) |
| **Enforcement** | La gateway puede gatear **llamadas al modelo** (claves, presupuestos) | Puertas deny-closed sobre **acciones y acceso a recursos**: aprobaciones, el [PEP de hooks de Claude Code](/es/how-to/connectors/claude-code-hooks-pep/), gating de herramientas MCP, kill switches |
| **Artefacto de auditoría** | Trazas / logs para depurar | Audit ledger append-only, hash-chained, **firmado con Ed25519**, **verificable fuera de la caja**, exportable como paquetes de evidencia **OSCAL** |
| **Postura de despliegue** | Autoalojable | Autoalojado **o air-gapped**; el plano de datos nunca sale de tu perímetro; **AGPL**, source-available |

La diferencia de fondo es la **verdad de base**. Una traza de observabilidad te dice
lo que la aplicación *dijo* que hizo. No puede decirte que un agente alcanzó una tabla
que la traza nunca mencionó. Olivares AI coteja la señal cooperativa contra el plano
de datos, de modo que "lo que el agente tocó" es un hecho corroborado, no un
autorreporte. Consulta [Vocabulario de analistas](/es/explanation/positioning/analyst-vocabulary/)
para entender por qué esa es la primera de nuestras tres líneas.

## Es "y", no "o" — ingerimos tu telemetría

Olivares AI **no** es un reemplazo de tu gateway ni de tu herramienta de trazado, y no
quiere estar en la ruta de la petición que ellos ocupan. **Consume la misma señal**:
el control plane ingiere spans de la convención semántica **OpenTelemetry GenAI**, la
misma telemetría gen-ai que estas herramientas emiten y consumen. Así que una
disposición sana es:

- Mantén **LiteLLM** como tu gateway de modelos y **Langfuse** para el trazado
  orientado al desarrollador y el trabajo con prompts.
- Apunta el flujo **OTel gen-ai** a Olivares AI como una fuente corroborante, y deja
  que el access map, la detección de deriva y el ledger aporten la capa de gobernanza
  a escala de estate por encima.

→ [Ingerir OpenTelemetry GenAI](/es/how-to/connectors/otel-genai/) ·
[OTel empresarial para Claude Code](/es/how-to/claude-code-enterprise-otel/)

## Cuándo *no* deberías recurrir a Olivares AI

La honestidad obliga en ambos sentidos. Probablemente **no** necesites este control
plane si:

- Tu único objetivo es **trazar y depurar llamadas a LLM** en una o dos aplicaciones,
  con un playground de prompts — Langfuse por sí solo encaja mejor.
- Solo necesitas una **gateway multiproveedor** con presupuestos y failover — ese es
  el trabajo de LiteLLM, y nos integramos con ese patrón en lugar de reimplementarlo.
- **No tienes un estate que gobernar**: un único servicio, un único modelo, sin
  agentes tocando bases de datos/almacenes de objetos/MCP, y sin obligación de
  auditoría o regulatoria.

Olivares AI se gana su sitio cuando las preguntas se vuelven *a escala de estate y
adversariales*: **qué agentes existen, qué puede alcanzar realmente cada uno, dónde
está derivando el acceso respecto de la política, puedo demostrarlo ante un auditor, y
puedo detener una acción maliciosa de forma deny-closed** — todo ello sin enviar esa
imagen a la nube de otro.
