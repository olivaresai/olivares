---
title: Gobernar Claude Code y Codex autenticados por suscripción
description: >-
  Cómo Olivares AI gobierna los agentes de codificación que se autentican con una
  suscripción — Claude Code en Pro/Max, Codex en ChatGPT — sin situarse nunca en
  medio de esa suscripción. Tres mecanismos (observar, managed-settings + hooks, una
  gateway por clave de API), una línea roja: nunca enrutamos tu credencial de suscripción.
sidebar:
  order: 6
---

El agente más difícil de gobernar es aquel al que un desarrollador inicia sesión con
una **suscripción** personal o de empresa: Claude Code conectado con Pro/Max, o Codex
conectado con ChatGPT. La misma forma se aplica a Grok Build y a cualquier agente CLI que
autentique a la persona en vez de a la carga de trabajo: los mecanismos que siguen tratan
la *forma* de ese inicio de sesión, no de un proveedor concreto. Se ejecuta en un portátil,
se autentica con una credencial OAuth,
y es exactamente la superficie que un guardrail de proveedor cloud en la ruta de
inferencia nunca ve (consulta
[la cuña](/es/explanation/positioning/where-olivares-fits-vs-your-gateway/)). La "solución"
tentadora — poner por delante un servicio que sostenga la suscripción y enrute su tráfico
— es una que Olivares AI **no** construirá, porque los proveedores de modelos lo prohíben
y porque convertiría nuestro control plane en un único punto de compromiso de credenciales.

Esta página es el relato honesto de cómo gobernamos estos agentes **sin actuar nunca como
intermediario de la suscripción**: qué observamos, dónde aplicamos, y la única ruta
estrecha donde una gateway resulta apropiada (y nunca es la de la suscripción).

:::danger[La línea roja: nunca enrutamos tu suscripción]
Olivares AI **nunca sostiene, hace de proxy ni enruta una credencial de suscripción de
terceros.** La propia política de Anthropic afirma: *"Anthropic does not permit
third-party developers to offer Claude.ai login or to route requests through
Free, Pro, or Max plan credentials on behalf of their users"*
([Claude Code legal & compliance](https://code.claude.com/docs/en/legal-and-compliance),
consultado el 2026-06-21 — la prohibición nombra los tres planes de consumo **Free, Pro,
Max**). Los términos de OpenAI funcionan igual para un login de consumo de ChatGPT/Codex.
Nuestra postura es más estricta que la propia línea: no enrutamos **ningún** OAuth de
suscripción, de **ningún** plan. La gobernanza ocurre *alrededor* del agente, nunca
*dentro* de su credencial.
:::

## Por qué intermediar la suscripción queda descartado

Conviene ser preciso con la regla, porque la asesoría jurídica de un comprador la
comprobará. La política de Anthropic traza dos listas que no deben confundirse:

- **Quién puede usar OAuth en absoluto** — cinco planes: *"OAuth authentication is intended
  exclusively for purchasers of Claude Free, Pro, Max, Team, and Enterprise
  subscription plans and is designed to support ordinary use of Claude Code and
  other native Anthropic applications."*
- **Qué no puede hacer un tercero** — enrutar en nombre de los usuarios: *"Anthropic does
  not permit third-party developers to offer Claude.ai login or to route requests
  through Free, Pro, or Max plan credentials on behalf of their users."*

La prohibición nombra explícitamente los planes de **consumo** (Free, Pro, Max). La
página, en cambio, no concede a nadie permiso para enrutar plazas Team o Enterprise —
guarda silencio al respecto, y no leemos el silencio como una licencia. Para los
*desarrolladores que construyen herramientas*, la propia guía de Anthropic se aleja por
completo del OAuth de suscripción: *"Developers building products or services that
interact with Claude's capabilities, including those using the Agent SDK, should
use API key authentication through Claude Console or a supported cloud provider."*
([fuente](https://code.claude.com/docs/en/legal-and-compliance); división por términos
de plan: Team/Enterprise/API bajo Commercial Terms, Free/Pro/Max bajo Consumer Terms.)

Nuestro conector de Codex codifica la misma disciplina en código, por diseño: la
credencial de automatización es una **clave de API** de OpenAI o un **token de acceso de
workspace**, nunca una suscripción personal de ChatGPT — *"proxying it for third-party/programmatic
use violates OpenAI's terms exactly as a consumer Claude subscription does for
Anthropic. There is no subscription config field by design"*
(`connectors/codex/codex.go`). Así que la línea roja no es una promesa de marketing
añadida a posteriori; es la forma del producto.

## Tres mecanismos, ninguno de ellos la suscripción

Gobernamos un agente autenticado por suscripción a través de tres canales independientes.
Los dos primeros no tocan la inferencia en absoluto; el tercero solo la toca para el
tráfico que se autentica con una **clave de API**, nunca con una suscripción.

### 1. Observar — telemetría, uso y postura

Claude Code emite OpenTelemetry, y un administrador puede activarlo para toda la flota
desde el tier gestionado: *"Administrators can configure OpenTelemetry settings
for all users through the managed settings file"*
([Claude Code monitoring](https://code.claude.com/docs/en/monitoring-usage)). Ingerimos
esa **señal gen-ai** — sesiones, tokens, coste, actividad de herramientas — y la
convertimos en el mapa de acceso y en hallazgos de postura. Es importante destacar que
esto es **de datos mínimos por construcción también del lado de Claude Code**: el
contenido del prompt está *"redacted by default"* y los detalles de herramientas, el
contenido de herramientas y los cuerpos crudos de API están cada uno *"(default:
disabled)"* (misma fuente). Consumimos uso y metadatos, no conversaciones.

Para Codex, el mismo canal de observación es la ingesta del conector de las APIs de
Analytics y de Compliance/Audit — uso, adopción y registros de auditoría inmutables
convertidos en muestras de coste y evidencia con alteraciones detectables, que llevan
*"never prompt/diff content or key values"* (`connectors/codex/codex.go`).

→ [Ingerir OpenTelemetry GenAI](/es/how-to/connectors/otel-genai/) ·
[OTel empresarial para Claude Code](/es/how-to/claude-code-enterprise-otel/)

### 2. Managed settings + hooks — el PEP in-process

La observación no es aplicación. El canal de aplicación para Claude Code es su fichero de
**managed settings** en el tier de política del SO, que lleva un hook `PreToolUse` no
sobreescribible que llama de vuelta al punto de decisión de Olivares antes de que se
ejecute cada herramienta. Anthropic documenta la propiedad en la que nos apoyamos:
*"Environment variables defined in the managed settings file have high precedence and
cannot be overridden by users"*, y los managed settings *"can be distributed via MDM"*
([monitoring](https://code.claude.com/docs/en/monitoring-usage)).

Olivares renderiza ese fichero (`olivares agent managed-settings`) con
`allowManagedHooksOnly` para que el hook propio de un desarrollador nunca pueda preceder
ni socavar al gobernado, y el endpoint por sesión y el bearer se inyectan en el lanzamiento
— no se escriben en el fichero estático. La decisión en sí es **deny-closed en cada
arista**: una llamada a herramienta solo se permite cuando se resuelve una identidad
firme, la disposición de la política no es `deny`, el motor de políticas en vivo no lo
prohíbe y — para un `ask` — una aprobación humana queda ligada al hash exacto del plan.
Una parada de emergencia ([kill switch](/es/reference/glossary/#kill-switch)) supera a todo,
incluida una concesión de break-glass activa.

Este es el mecanismo que la página del
[PEP de hooks de Claude Code](/es/how-to/connectors/claude-code-hooks-pep/) documenta
operativamente, y es lo que nos permite *gobernar* el agente de desarrollo local, no
solo observarlo — el segundo de los
[tres carriles](/es/explanation/positioning/analyst-vocabulary/#los-tres-carriles-a-los-que-apunta-este-vocabulario).

### 3. Gateway para una clave de API — nunca para OAuth

Existe exactamente una ruta donde Olivares se sitúa en la línea de petición de inferencia,
y existe solo para los llamadores que **no** usan el canal de managed-settings de Claude
Code: tráfico crudo de SDK o `curl` autenticado con una **clave de API** (o un equivalente
Bedrock/Vertex). Claude Code enruta tales peticiones con `ANTHROPIC_BASE_URL` — *"To route
requests through a custom API endpoint, set the `ANTHROPIC_BASE_URL` environment variable
instead"* — y autentica una gateway con un bearer vía `ANTHROPIC_AUTH_TOKEN`, *"when
routing through an LLM gateway or proxy that authenticates with bearer tokens rather than
Anthropic API keys"*
([Claude Code IAM](https://code.claude.com/docs/en/iam)). Apuntado al proxy de inferencia
inline de Olivares, ese tráfico obtiene un pipeline gobernado — residencia, acceso a
modelos, ventana de contexto, DLP, presupuesto, grabación — antes de ser reenviado.

La frontera es absoluta: **esta ruta transporta tráfico de clave de API / bearer, nunca
la credencial OAuth de una suscripción.** Es la costura de aplicación para los llamadores
de SDK/`curl` que los managed settings no pueden alcanzar, y nada más.

## La caja de honestidad: verified-deployed, no inevadible

:::caution[Aplicación que podemos demostrar que está *desplegada*, no aplicación que *no puede* esquivarse]
El PEP de managed-settings + hook es **deny-closed** y **no sobreescribible por el
usuario mediante settings** — pero no es magia. Un desarrollador que apunte
`ANTHROPIC_BASE_URL` a su propio endpoint envía la inferencia a otro lugar por completo;
nuestra propia nota de ingeniería lo dice con claridad: *"a custom
`ANTHROPIC_BASE_URL` bypasses server-managed-settings entirely"*
(`modules/inferenceproxy/doc.go`). Así que nunca afirmamos que el PEP sea imposible de
escapar. En su lugar afirmamos dos cosas que podemos sostener:

1. **Está verified-deployed.** Olivares atesta que los managed settings y el hook del PEP
   están realmente presentes en el host — un host no aprovisionado se ejecuta
   ungoverned-but-observed, y eso es visible, no oculto.
2. **El bypass es en sí mismo un hallazgo.** Un `ANTHROPIC_BASE_URL` no predeterminado en
   un host aflora como un hallazgo de postura, y un entorno gestionado que fija una base
   URL que diverge de la gateway de Olivares autorizada genera un hallazgo de **drift**
   (`connectors/claude-config`, `connectors/managedsettings`). La evasión no pasa en
   silencio; se enciende.

"Verified-deployed, evasión-como-hallazgo" es la historia de aplicación honesta para
cualquier agente que se ejecuta en una máquina que el desarrollador controla. No te
venderemos "inevadible".
:::

## La asimetría de Codex, contada con honestidad

Claude Code y Codex no son simétricos, y la diferencia importa. Para un Codex autenticado
por ChatGPT **no hay un equivalente documentado de `ANTHROPIC_BASE_URL`** — la
[página de configuración gestionada](https://developers.openai.com/codex/enterprise/managed-configuration)
de OpenAI no documenta ningún setting ni variable de entorno para enrutar la inferencia a
través de una base URL o gateway personalizada (verificado por fetch, 2026-06-21; una
ausencia en esa página, no una prueba de que no exista en ningún otro sitio). Así que
**no** gobernamos Codex interceptando su inferencia.

En su lugar lo gobernamos donde OpenAI *sí* da a los administradores controles aplicados.
La configuración gestionada de Codex permite a una empresa fijar *"Requirements:
admin-enforced constraints that users can't override"* que *"constrain security-sensitive
settings (approval policy, approvers reviewer, automatic review policy, sandbox
mode, permission profiles, web search mode, managed hooks, and optionally which
MCP servers users can enable)"* (misma fuente). Olivares redacta y atesta esos
requisitos (`connectors/codex-managed-config`) — política de aprobación, modo sandbox,
la allowlist de MCP, telemetría expurgada (`log_user_prompt = false`) — e ingiere la
evidencia de Analytics y Compliance de Codex. Gobernanza a través de la configuración y
la evidencia, no a través de un man-in-the-middle en la llamada al modelo.

## En una tabla

| Canal | Qué hace | ¿Toca la inferencia? | La credencial |
|---|---|---|---|
| **Observar** | Uso, coste, actividad de herramientas → mapa de acceso + postura; Analytics/Compliance de Codex → ledger | No | Ninguna — solo telemetría, contenido expurgado por defecto |
| **Managed settings + hooks** | PEP `PreToolUse` deny-closed en Claude Code, no sobreescribible vía settings | No | La del propio agente; nunca la vemos |
| **Gateway (solo clave de API)** | Pipeline gobernado para llamadores crudos de SDK/`curl` vía `ANTHROPIC_BASE_URL` | Sí | **Clave de API / bearer — nunca OAuth de suscripción** |
| **Codex managed-config** | Requisitos aplicados por admin (aprobación/sandbox/MCP) + ingesta de evidencia | No | La de la organización; configuración, no interceptación |

## Relacionado

- [Dónde encaja Olivares frente a tu gateway / Guardrails](/es/explanation/positioning/where-olivares-fits-vs-your-gateway/)
  — por qué nada de esto compite con tu gateway de IA.
- [Olivares AI frente a WitnessAI](/es/explanation/positioning/vs-witnessai/) — el
  cara a cara sobre la gobernanza de agentes en los IDE.
- [Hooks de Claude Code y el PEP](/es/how-to/connectors/claude-code-hooks-pep/) y
  [Ejecutar Claude Code con Olivares](/es/how-to/run-claude-code-with-olivares/) — el
  cómo operativo.
- [Honestidad y límites](/es/start/honesty-and-limits/) — el compromiso permanente bajo el
  que está escrita esta página.
