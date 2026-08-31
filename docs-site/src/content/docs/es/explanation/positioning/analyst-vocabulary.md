---
title: El vocabulario de los analistas, mapeado con honestidad
description: >-
  El vocabulario de los analistas de 2026 para la gobernanza de IA — agent sprawl,
  guardian agents, AI TRiSM, discover/observe/govern/secure — definido, atribuido
  donde tiene fuente, y mapeado a lo que Olivares AI realmente hace y no hace.
sidebar:
  order: 2
---

Si evalúas herramientas de IA, te has topado con estas palabras: **agent sprawl**
(proliferación de agentes), **guardian agents**, **AI TRiSM**, **discover / observe /
govern / secure**. Son una abreviatura útil, y un comprador de 2026 espera que un
proveedor las hable. También es fácil abusar de ellas — para insinuar que un producto *es*
una categoría cuando simplemente se sitúa cerca de una.

Esta página hace tres cosas: **define** cada término, lo **atribuye** allí donde tiene
un dueño real, y **dice con claridad** cuáles describen a Olivares AI y con cuáles
solo nos relacionamos. Para las cifras que respaldan el mercado subyacente, consulta
[Contexto de mercado y fuentes](/es/explanation/positioning/market-context-and-sources/).

## Agent sprawl

**Qué significa.** La proliferación incontrolada de agentes de IA, copilotos, servidores
MCP y automatizaciones por toda una organización — creados por distintos equipos, con
distintas credenciales, tocando distintos sistemas, más rápido de lo que nadie mantiene
un inventario. El resultado son agentes desconocidos con acceso desconocido.

**¿Nos describe?** Describe el *problema para el que existimos*. El primer trabajo de
Olivares AI es hacer visible la proliferación: **descubre** los agentes, modelos,
servidores MCP y herramientas de tu estate y construye un
[mapa de acceso de lectura/escritura](/es/explanation/#el-access-map-read-first-minimal-data-permitted-vs-observed)
de lo que cada uno puede alcanzar — read-first, de datos mínimos, sobre **tu**
infraestructura. El
[diff Permitted-vs-Observed](/es/reference/glossary/#observado--permitido) convierte entonces
"tenemos muchos agentes" en "aquí están los que usan accesos que nadie concedió". La
proliferación es la enfermedad; un inventario preciso y atribuido es el primer
tratamiento.

## Guardian agents

**Qué significa.** El término de **Gartner** para las capacidades de IA que monitorizan,
supervisan o intervienen sobre *otros* agentes de IA. Gartner prevé que las tecnologías
de guardian-agent supondrán el **10–15% del mercado de IA agéntica para 2030** (nota de
prensa de Gartner, 2025; ver [fuentes](/es/explanation/positioning/market-context-and-sources/)).

**¿Nos describe? Con cuidado.** Olivares AI ofrece el *resultado de gobernanza y
supervisión* del que trata la categoría — observar el comportamiento de los agentes,
diferenciar lo permitido frente a lo observado, restringir acciones deny-closed y
registrarlo todo en un ledger con alteraciones detectables. Pero **no** somos un agente
de runtime autónomo que razona sobre otros agentes en la ruta de la petición. Somos un
**control plane read-first** que se sitúa *fuera* de la ruta de datos: observamos a
través de telemetría, registros de auditoría nativos y un backstop de kernel eBPF, y
aplicamos en puntos bien definidos (aprobaciones, el
[PEP de hooks de Claude Code](/es/how-to/connectors/claude-code-hooks-pep/), kill switches)
— no insertando un proxy de IA en cada llamada. Si "guardian agent" significa
*gobernanza supervisora sobre tu estate de agentes*, sí. Si significa *un LLM montando
guardia inline*, esa es una arquitectura distinta, y no nos la atribuiremos.

## AI TRiSM

**Qué significa.** **AI TRiSM** — *AI Trust, Risk and Security Management* (gestión de
confianza, riesgo y seguridad de la IA) — es un **framework acuñado y propiedad de
Gartner** para gestionar la confianza, el riesgo y la seguridad de la IA a lo largo de
su ciclo de vida. Tal como se resume habitualmente, abarca la **gobernanza** y la
**inspección y aplicación en runtime** de la IA, junto con la gobernanza de la
información y la seguridad de la infraestructura.

:::caution[Nota de atribución]
El framework AI TRiSM, su taxonomía de capas y cualquier definición son **investigación
propietaria de Gartner**. Las reformulaciones públicas (incluidos nombres de capas y
diagramas) suelen originarse en **reimpresiones con licencia**. Describimos AI TRiSM a
nivel de *tema* y mapeamos nuestras capacidades a esos temas; **no** reproducimos el
modelo exacto de Gartner, ni afirmamos conformidad con él, ni insinuamos un respaldo de
Gartner.
:::

**Cómo nos mapeamos a él (nivel de tema).**

- **Gobernanza** — autoría de políticas, clasificación de riesgo (tier de la UE × función
  NIST), aprobaciones/HITL, manage-as-code, y el catálogo de frameworks del
  [módulo de cumplimiento](/es/reference/modules/xiii-compliance/).
- **Inspección en runtime** — el mapa de acceso y el drift Permitted-vs-Observed,
  hallazgos de guardrail/anomalía, líneas de tiempo de sesión — todo read-first y
  out-of-band.
- **Aplicación en runtime** — puntos deny-closed donde *sí* nos situamos en una ruta de
  decisión: aprobaciones, el PEP de hooks de Claude Code, gating de herramientas MCP,
  kill switches.
- **Gobernanza de la información** — descubrimiento de PII/sensibilidad sobre bases de
  conocimiento gobernadas, atestación de residencia de datos, retención y legal-hold.

Usamos AI TRiSM como un *mapa del espacio del problema que un comprador ya conoce*, para
mostrar cobertura — no como una insignia.

## Discover / observe / govern / secure

**Qué significa.** La secuencia de verbos que analistas y proveedores usan para describir
el ciclo de vida de la gobernanza de IA: primero **descubrir** lo que existe, luego
**observar** lo que hace, luego **gobernar** lo que se le permite hacer, y luego
**asegurar** todo el estate.

**¿Nos describe?** Sí — está cerca de nuestra propia narrativa de producto, lo cual
conviene enunciar en nuestros términos exactos para que el mapeo sea honesto:

| Verbo de analista | Qué hace realmente Olivares AI |
|---|---|
| **Discover** | Inventario de agentes, modelos, servidores MCP y herramientas a lo largo del estate. |
| **Observe** | El mapa de acceso R/RW — read-first, de datos mínimos, con confianza de atribución por arista; las rutas cooperativas (OTel, hooks) corroboradas por auditoría nativa (pgAudit, CloudTrail) y un backstop eBPF. |
| **Govern** | Drift Permitted-vs-Observed, política + aprobaciones/HITL, puntos de actuación deny-closed, manage-as-code. |
| **Secure** | Guardrails, el ledger de auditoría con alteraciones detectables, kill switches, evidencia de cumplimiento — **y** el self-hosting sin telemetría obligatoria ni egreso del plano de control de forma predeterminada. Solo cruza tu perímetro lo que tú configuras para que lo cruce: llamadas a tus API de modelos, las salidas SIEM/webhook que conectas y un proveedor externo de embeddings si aprovisionas uno. |

La advertencia honesta que recorre los cuatro: **la fidelidad es escalonada**. La
observación es limpia para bases de datos SQL, object stores y warehouses; con pérdidas
para almacenes de documentos y vectoriales; y no alcanzable de forma pasiva para algunos
sistemas. El mapa
[muestra su confianza](/es/reference/glossary/#atribución-confianza) en lugar de inventar
una atribución que no tiene.

## Los tres carriles a los que apunta este vocabulario

Quita las etiquetas y quedan los mismos tres diferenciadores — los carriles que el
mercado ha dejado abiertos y a los que el texto debería seguir regresando:

1. **Ground truth desde el plano de datos.** No nos fiamos de la palabra de un agente
   sobre lo que tocó. **Correlacionamos** la señal cooperativa (OTel, MCP, hooks) frente
   al propio ledger del sistema — pgAudit clasificando lecturas frente a escrituras,
   CloudTrail exponiendo el acceso a object stores — y un backstop de kernel eBPF para
   el caso no cooperativo. Esa correlación es lo que hace de Permitted-vs-Observed un
   *hecho*, no un autoinforme.
2. **Aplicación deny-closed sobre el agente de desarrollo local.** La mayoría de las
   herramientas solo *observan* Claude Code. Olivares AI además lo **gobierna**: el PEP de
   hooks convierte la política en una decisión deny-closed en el agente, no en una línea
   de log a posteriori.
3. **Soberanía.** Self-hosted, source-available **AGPL** — el plano de datos nunca
   abandona tu frontera y no hay un control plane SaaS en tu ruta de cumplimiento.

Cada término anterior está al servicio de esos tres. Cuando una página de aquí usa una
palabra de analista, es para encontrarse con el comprador donde está — y luego apuntar de
vuelta a una de estas tres cosas que el producto hace genuinamente.
