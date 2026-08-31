---
title: Contexto de mercado y fuentes
description: >-
  Las señales de mercado tras Olivares AI — agent sprawl, pilotos que fracasan,
  controles de acceso ausentes — cada una con su fuente primaria verificada, su
  cifra exacta y una advertencia honesta. El único lugar del que cada otra página
  cita sus números.
sidebar:
  order: 1
---

Esta página es la **única fuente de verdad para cada estadística de mercado** usada en
el sitio web, el README y la documentación de Olivares AI. Existe porque el mercado de
gobernanza de IA está inundado de cifras cuya atribución se ha distorsionado al
recontarse — y el analista de un comprador lo comprobará. Preferimos perder una frase
contundente antes que citar una cifra que no podemos respaldar.

:::note[La regla de atribución]
Citamos **solo fuentes primarias**, las nombramos con exactitud y reproducimos la cifra
tal como la enuncia la fuente. **No** blanqueamos un número a través de un blog que dejó
caer la atribución, y **no** apilamos estadísticas de agregadores ("el 70% de las
Fortune 100…") que ningún comprador puede rastrear. Cuando un hallazgo es **preliminar o
no revisado por pares**, lo decimos en la misma línea. Esto refleja cómo el propio
producto trata la evidencia: la
[confianza de atribución](/es/reference/glossary/#atribución-confianza) es un campo de
primera clase, y un control con evidencia solo en fase de diseño informa `by_design`,
nunca `satisfied`.
:::

## Las cifras que usamos, y de dónde proceden

| Afirmación | Cifra (tal como la enuncia la fuente) | Fuente primaria | Advertencia / cómo la usamos |
|---|---|---|---|
| Las organizaciones con brechas de IA carecían de controles de acceso | El **97%** de las organizaciones que sufrieron un incidente de seguridad relacionado con IA carecían de controles de acceso de IA adecuados; el **13%** de las organizaciones reportó una brecha de sus modelos o aplicaciones de IA | **IBM, *Cost of a Data Breach Report 2025*** (investigación realizada por el **Ponemon Institute**), IBM Newsroom | La atribución es **IBM / Ponemon — no Forrester**, una atribución errónea que circula ampliamente. La usamos para la *brecha de control de acceso*, que es exactamente lo que abordan el [mapa de acceso R/RW](/es/explanation/#el-access-map-read-first-minimal-data-permitted-vs-observed) y el diff Permitted-vs-Observed. |
| Los proyectos agénticos se abandonarán | **Más del 40%** de los proyectos de IA agéntica serán **cancelados para finales de 2027**, debido a costes crecientes, valor de negocio poco claro o controles de riesgo inadecuados | **Gartner**, nota de prensa (2025) | La usamos para el punto de la *deuda de gobernanza* — los proyectos mueren por falta de controles y de valor demostrable, no por calidad del modelo. |
| Los guardian agents se convierten en mercado | Las tecnologías de **guardian agent** supondrán el **10–15% del mercado de IA agéntica para 2030** | **Gartner**, nota de prensa (2025) | Establece "guardian agents" como una categoría reconocida por los analistas. Somos explícitos en que *no* somos un agente de runtime que vigila a otros agentes — ver [Vocabulario de los analistas](/es/explanation/positioning/analyst-vocabulary/). |
| La mayoría de los pilotos no muestran impacto en P&L | El **~95%** de los pilotos de IA generativa no aportan **ningún impacto medible en P&L**; externamente, las herramientas **compradas/asociadas** tienen éxito aproximadamente **el doble de veces** que las construidas internamente | **MIT Media Lab, Project NANDA — *The GenAI Divide: State of AI in Business 2025*** (reportado vía *Fortune*, ago 2025) | **Preliminar, no revisado por pares.** Siempre lo etiquetamos como tal. Usamos el hallazgo de *buy-vs-build* para apoyar el argumento de "adopta un control plane mantenido en lugar de improvisar tu propia gobernanza" — nunca como una estadística asentada. |
| La educación superior usa la IA más rápido de lo que la gobierna | Una amplia mayoría (**~80%**) del personal de educación superior usa herramientas de IA, mientras que **menos de una cuarta parte (<25%)** está familiarizada con las políticas de IA de su institución | **EDUCAUSE** AI Landscape / encuestas de comunidad (2025–2026) | Estimaciones de encuesta; verifica el estudio/año exacto antes de citarlo externamente. Usamos la *brecha de conciencia de las políticas* en la [página de educación superior](/es/explanation/positioning/higher-education-and-research/). |

## Evidencia cualitativa en la que nos apoyamos

Estos no son porcentajes; son posiciones de fuentes nombradas y citables que enmarcan
*por qué existe la categoría*.

- **Bessemer Venture Partners** (*Atlas — "Securing AI Agents: the defining cybersecurity
  challenge of 2026"*): la intervención quirúrgica y en pleno vuelo sobre el comportamiento
  de los agentes es **"donde el mercado está más subdesarrollado y donde reside la
  oportunidad de infraestructura más clara,"** y **"la mayoría de las empresas no tienen un
  inventario preciso de los agentes que operan en su entorno."** Esta es la enunciación
  externa de la brecha que cierra nuestro [mapa de acceso](/es/explanation/).
- **Anthropic** (publicaciones de ingeniería sobre el sandboxing de Claude Code y los
  Managed Agents): los sandboxes self-hosted mueven la ejecución a infraestructura que el
  cliente controla, pero Anthropic **asigna al cliente el registro de auditoría, la
  política/RBAC, la orquestación multi-host y la inspección de tráfico**. Esa
  responsabilidad delegada es el hueco que Olivares AI cubre — ver
  [vs torres de control](/es/explanation/positioning/vs-control-towers/).

## Señales de encuesta (direccionales — verificar antes de citar externamente)

Las encuestas independientes y de comunidad informan de forma consistente de la misma
forma: los agentes proliferan más rápido de lo que las organizaciones pueden inventariarlos
o atribuirlos. Tratamos los porcentajes concretos de abajo como **contexto direccional**
sintetizado a partir de encuestas nombradas; **no** forman parte de nuestro conjunto
primario-verificado de arriba y deberían volver a comprobarse contra el instrumento
original antes de cualquier uso externo.

- Las encuestas de Cloud Security Alliance / Token Security (n≈418), Protiviti y Optro
  informan, de diversas formas: de que una amplia proporción de organizaciones tiene
  **agentes desconocidos/no gestionados** en su entorno, de que solo una minoría mantiene
  un **inventario en tiempo real**, de que una mayoría experimentó un **incidente
  relacionado con agentes** en el año anterior, y de que solo una minoría puede **rastrear
  la acción de un agente hasta una persona o sistema**.

El punto que esas encuestas plantean en conjunto es lo único que afirmamos públicamente:
**las organizaciones están perdiendo el control de sus agentes, y no pueden atribuir lo
que esos agentes hacen.** Esa es una afirmación que nuestro producto está construido para
volver falsa para sus usuarios — y es el núcleo honesto de cada página de posicionamiento
de aquí.

## Cosas que deliberadamente **no** afirmamos

- Sin recuentos de clientes, muros de logos ni "con la confianza de N empresas" — el
  producto es pre-1.0 y prelanzamiento (ver [Honestidad y límites](/es/start/honesty-and-limits/)).
- Sin certificación ni atestación que no poseamos (SOC 2, ISO 27001/42001 son
  **readiness**, no certificados — ver el paquete de confianza y procurement que se incluye
  con el código fuente).
- Sin benchmarks inventados, afirmaciones de throughput ni números de precisión. Las cifras
  de capacidad provienen únicamente del harness de benchmark reproducible, con procedencia
  de hardware.
