---
title: Dónde encaja Olivares frente a tu gateway de IA y Guardrails
description: >-
  Ya ejecutas una gateway de IA (LiteLLM, Portkey, Cloudflare) o Guardrails de
  hyperscaler (Bedrock, Azure). Bien — consérvalos. Olivares AI no es una gateway y
  no compite en enrutamiento ni caché. Es el plano de gobernanza y evidencia que se
  sitúa junto a ellos y cierra la brecha que dejan abierta.
sidebar:
  order: 7
---

Si ya has invertido en una **gateway de IA** o en los **Guardrails** de un hyperscaler,
lo honesto que hay que decir primero es: **consérvalos, y Olivares AI no intenta
reemplazarlos.** El trabajo de una gateway es la llamada al modelo — enrutarla,
cachearla, balancearla, presupuestarla. El trabajo de los Guardrails es la seguridad de
contenido en esa llamada. Ambos son reales, ambos son buenos en lo que hacen, y ninguno es
lo que Olivares es.

:::tip[La versión breve]
**Olivares AI no es una gateway de IA.** No enruta, cachea, balancea la carga ni se sitúa
en la ruta caliente de tu tráfico de modelos, y nunca lo hará. Se sitúa **junto y detrás**
de tu gateway como el *plano de gobernanza y evidencia*: aplicación in-process dentro del
runtime del agente, un ledger de evidencia con alteraciones detectables, ciclo de vida de
identidades no humanas, y human-in-the-loop / break-glass / kill-switch sobre **sesiones
en vivo**. Tu gateway gobierna la *petición*; Olivares gobierna el *agente y todo lo que
toca*, y se lo demuestra a un auditor.
:::

## Qué hacen bien una gateway y los Guardrails (úsalos para esto)

Son capacidades de commodity, bien entendidas, y los proveedores las describen con
claridad:

- **Las gateways de IA** son gestores de la ruta de la petición para llamadas a modelos.
  LiteLLM es un *"OpenAI Proxy Server (LLM Gateway) to call 100+ LLMs in a unified
  interface & track spend, set budgets per virtual key/user"*
  ([LiteLLM](https://docs.litellm.ai/docs/simple_proxy)); Cloudflare AI Gateway te
  permite *"Connect to any model, dynamically route requests, and manage usage,
  billing, and logs from one unified gateway"*
  ([Cloudflare](https://www.cloudflare.com/products/ai-gateway/)); Portkey
  *"records real-time API requests, including cost"*
  ([Portkey](https://portkey.ai/features/ai-gateway)). Enrutamiento, fallbacks,
  caché, claves virtuales, presupuestos por clave, logging de peticiones — ese es su
  carril.
- **Los Guardrails de hyperscaler** son filtros de seguridad de contenido. Bedrock
  Guardrails *"provides configurable safeguards to help you build safe generative AI
  applications"* que *"detect and filter undesirable content and protect
  sensitive information that might be present in user inputs or model responses"* —
  filtros de contenido, temas vetados, filtros de palabras, enmascaramiento de PII, comprobaciones
  de contextual-grounding y automated-reasoning
  ([AWS](https://docs.aws.amazon.com/bedrock/latest/userguide/guardrails.html)).

Si tu problema es *"dar a mis aplicaciones un endpoint a muchos modelos, con presupuestos,
caché y filtrado de contenido,"* ese stack lo resuelve, y no necesitas un control plane
para hacerlo. Nos integramos con ese patrón; no lo reimplementamos.

## La brecha de gobernanza que dejan abierta

Una gateway ve una **petición**. Los Guardrails ven **contenido**. Ninguno ve el
**agente** — su identidad a lo largo del tiempo, qué alcanzó a través de tu plano de datos,
quién aprobó una acción arriesgada, y si algo de ello puede demostrarse después. Esa es la
brecha que Olivares llena.

| Brecha que dejan la gateway / Guardrails | Por qué importa | Qué aporta Olivares AI |
|---|---|---|
| **Aplicación en el runtime del agente** | Una gateway aplica en la *frontera de la petición*; no puede detener una llamada a herramienta de Claude Code local que nunca la atraviesa | Un [PEP in-process](/es/how-to/connectors/claude-code-hooks-pep/) deny-closed en el agente: puerta de identidad firme, disposición de política, overlay de política en vivo, todo antes de que la herramienta se ejecute |
| **Evidencia con alteraciones detectables** | La gateway y los Guardrails emiten *logs* — registros de petición mutables; un auditor quiere prueba inmutable | Ledger append-only, hash-chained, [firmado con Ed25519](/es/reference/glossary/#audit-ledger), verificable off-box, exportable como evidencia OSCAL |
| **Ciclo de vida de identidad no humana** | La "clave virtual" de una gateway es un cubo de presupuesto, no una identidad que se aprovisiona, atribuye, rota y da de baja | [Ciclo de vida de NHI](/es/reference/glossary/#identidad--nhi): obsolescencia → bloqueo, cascada de offboarding, dual-control en la rotación, ligado al mapa de acceso |
| **Intervención en sesión en vivo** | Los logs y los presupuestos son a posteriori; ninguna de estas herramientas examinadas detiene una sesión en pleno vuelo | [Aprobaciones HITL](/es/reference/glossary/#aprobación-hitl), [break-glass](/es/reference/glossary/#break-glass) y un [kill switch](/es/reference/glossary/#kill-switch) que deniega toda actuación gobernada hasta una rehabilitación con dual-control |
| **Verdad de base a lo largo del estate** | Una gateway solo ve las llamadas que la atraviesan; los agentes también tocan BD, almacenes de objetos, MCP y ficheros directamente | El [mapa de acceso R/RW](/es/explanation/#el-access-map-read-first-minimal-data-permitted-vs-observed) read-first y el drift Permitido-vs-Observado, corroborado contra la auditoría nativa |
| **Soberanía** | Las gateways SaaS y los Guardrails cloud procesan ese tráfico en su nube | Self-hosted / air-gapped; el plano de datos nunca abandona tu frontera |

Ninguna de estas es una funcionalidad de enrutamiento. Ese es el punto: la brecha no es
*mejor enrutamiento*, es **gobernanza que la ruta de la petición nunca se diseñó para
proporcionar.**

## Sobre los Guardrails en concreto: la seguridad de contenido es un hook, no un competidor

Bedrock Guardrails puede aplicarse de dos formas — inline durante una llamada de
inferencia de Bedrock, o *"directly through the `ApplyGuardrail` API without invoking the
foundation models"*, que funciona *"with any foundation model whether hosted on
Amazon Bedrock or self-hosted models"*
([AWS](https://aws.amazon.com/bedrock/guardrails/)). Eso es genuinamente útil, y
Olivares trata la seguridad de contenido como un **detector que conectas**, nunca un muro
que te pedimos elegir *en lugar* de los Guardrails. Dos hechos honestos y distintos:

- El proxy de inferencia inline expone una **costura de inspección de contenido** — un
  punto enchufable donde un detector de contenido / DLP devuelve un veredicto sobre el que
  actúa el decisor deny-closed. La seguridad de contenido pertenece *ahí*, en el pipeline,
  en lugar de ser reimplementada como un filtro competidor.
- Olivares lee las **propias decisiones** de tus Guardrails de forma read-first. El conector
  de AWS ingiere las decisiones de guardrail de Bedrock desde sus logs de CloudWatch / S3
  como postura y evidencia; deliberadamente **no** invoca el runtime de pago
  `ApplyGuardrail`. Tus veredictos de contenido pasan a formar parte del registro a prueba
  de manipulaciones.

Así que la seguridad de contenido se compone con lo que ya ejecutas. Lo que los Guardrails
*no* documentan — y donde la brecha de gobernanza permanece abierta — es el resto de la
vida del agente: las páginas de Bedrock no documentan identidad de agente, ni gestión de
sesiones, ni aprobaciones humanas, ni gobernanza de coste (no documentado en esas páginas,
comprobado el 2026-06-21). Olivares es exactamente ese complemento: lleva la identidad, los
controles de sesión, las aprobaciones y la evidencia; el filtro de contenido se queda donde
ya vive.

## Cómo se componen

Una disposición sana mantiene cada herramienta en su carril:

- **Conserva tu gateway** (LiteLLM / Portkey / Kong / Cloudflare) como el plano de la
  llamada al modelo — enrutamiento, caché, claves virtuales, presupuestos sobre la petición.
- **Conserva tus Guardrails** (Bedrock / Azure Content Safety) como tu detector de
  seguridad de contenido — el PEP de Olivares ejecuta un detector enchufable en su costura
  de inspección de contenido y lee las propias decisiones de tus Guardrails de forma
  read-first como evidencia; no invoca `ApplyGuardrail` por sí mismo.
- **Añade Olivares junto a ellos** como el plano de gobernanza y evidencia: el PEP
  in-process sobre los agentes que nunca llegan a tu gateway, el mapa de acceso a lo largo
  de todo el estate, el ledger con alteraciones detectables, y los controles en vivo de
  HITL/break-glass/kill.

El único lugar donde Olivares sí toca la inferencia es estrecho y explícito — una ruta de
gateway **solo de clave de API** para llamadores crudos de SDK/`curl`, descrita en
[Gobernar agentes autenticados por suscripción](/es/explanation/positioning/governing-subscription-authed-agents/).
Existe para gobernar tráfico que tus otras herramientas no pueden alcanzar, nunca para
competir con ellas en enrutamiento, y **nunca** transporta una credencial de suscripción.

## Cuándo tu gateway es suficiente

La honestidad obliga en ambos sentidos. Si tus agentes solo llaman a los modelos
**a través** de tu gateway, tus necesidades de seguridad de contenido las cubren los
Guardrails, **no tienes agentes self-hosted ni residentes en portátil** alcanzando bases de
datos / almacenes de objetos / MCP directamente, y **no tienes requisito de soberanía ni de
evidencia con alteraciones detectables** — entonces tu gateway más sus logs y los Guardrails
pueden ser todo lo que necesitas, y no deberías añadir un control plane por sí mismo.

Olivares se gana su sitio cuando las preguntas se vuelven *a escala de estate y
adversariales*: qué agentes existen y qué alcanzó realmente cada uno, puedo detener una mala
acción deny-closed **en el agente**, quién aprobó la arriesgada, y puedo entregar a un
auditor **prueba inmutable** — todo ello sin enviar esa imagen a la nube de otro. Para el
tratamiento más profundo de dos comparaciones adyacentes, consulta
[frente a las control towers de IA](/es/explanation/positioning/vs-control-towers/) y
[frente a las gateways y la observabilidad de LLM](/es/explanation/positioning/vs-llm-observability/).
