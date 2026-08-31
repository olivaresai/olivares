---
title: El plano de trabajo
description: >-
  Cómo coordinan el trabajo los agentes y las sesiones en Olivares AI — elementos de
  trabajo, mensajes, acuses y relevos —; qué es real y duradero hoy y qué se mantiene
  deliberadamente sin cablear. La mitad del producto que no es el mapa de acceso.
---

La mayor parte de esta documentación trata sobre **a qué puede acceder un agente**: el
mapa de acceso, los permisos y la desviación entre lo *Permitido* y lo *Observado*. Esta
página trata sobre la otra mitad — **cómo coordinan los agentes y las sesiones el trabajo
en sí** —, la parte que hasta ahora el resto del sitio sólo ha descrito como una lista de
comandos y eventos.

El problema para el que existe no es hipotético: es el que este proyecto ha sufrido durante
su propio desarrollo. Sesiones que no pueden verse entre sí, estados que divergen entre
ellas, trabajo hecho dos veces y decisiones que sólo viven en el terminal de una persona y
se pierden al cerrarlo. Un plano de control que gobierna el *acceso* y no dice nada sobre el
*trabajo* deja ese hueco exactamente donde estaba.

## Qué es un elemento de trabajo

Un **elemento de trabajo** es una unidad de trabajo con un responsable, un estado y un
registro duradero. No es un mensaje de chat ni un ticket en el gestor de otra persona: vive
en el mismo almacén que el ledger de auditoría, de modo que después se puede responder qué
le ocurrió por los mismos medios que para todo lo demás que registra el plano de control.

A su alrededor hay tres primitivas:

| Primitiva | Qué hace |
|---|---|
| **Mensaje** | Un participante comunica algo a otro de forma duradera; no lo emite a un registro que nadie lee |
| **Acuse** | El receptor registra que se *hizo cargo* del mensaje. «Leído» y «respondido» dejan de significar lo mismo |
| **Relevo** | La titularidad de un elemento de trabajo cambia, con el motivo adjunto al cambio |

Merece la pena detenerse en el acuse. La coordinación se rompe con mucha más frecuencia
porque un mensaje se vio pero no se atendió que porque nunca se entregara; un sistema que no
puede distinguir ambos casos tampoco puede decirte cuál de ellos ocurrió.

## Qué es real hoy y qué no

:::caution[Lee esta sección antes de construir sobre esta base]
Las primitivas anteriores están **implementadas y son duraderas**. Su alcance es
**deliberadamente más estrecho** que la idea, y el límite se impone en el código en vez de
prometerse en prosa. Tres límites, expresados sin rodeos:
:::

**1 · La coordinación está acotada a un workflow, y el plano público de comunicación se
mantiene deliberadamente sin cablear.** Los mensajes, los acuses y los relevos son reales dentro de
la ejecución del propio workflow. El plano general de comunicación entre todo y todos *no*
está conectado; y no es un olvido pendiente de que alguien lo detecte: una prueba de
arranque comprueba qué fuentes de autoridad puede cablear `boot` y **falla si aparece
cualquier otra** (`cmd/olivares/communicationauthorityboot_test.go`,
`TestBootWiresExactCommunicationRequestAuthoritySourcesOnly`). Cablearlo por accidente
produce un test rojo, no una sorpresa en producción.

**2 · El despacho entre agentes sólo se monta con un destino autorizado.** El ejecutor de
trabajo remoto se construye con una puerta de aprobación delante
(`cmd/olivares/wire.go`); no existe ninguna ruta que despache trabajo a un par arbitrario
porque un fichero de configuración lo haya pedido amablemente.

**3 · El modo shadow y la autoridad final sobre el trabajo NO EXISTEN.** No están
«próximamente» ni «parcialmente»: están ausentes. Hoy un despliegue no puede dar al plano
de trabajo la última palabra sobre una sesión, y nada del producto debe interpretarse como
si ofreciera esa capacidad. Cualquier incorporación hipotética de estas capacidades tendría
que venir acompañada de la evidencia de que funciona: una ventana de comparación frente a
las fuentes existentes, no un incremento de versión.

## Por qué se escriben aquí los límites

Porque la alternativa es peor para ti. Una página que describiera el diseño y te dejara
descubrir el límite durante la integración te costaría la tarde; una página que llamara
«roadmap» a la mitad ausente haría justo el tipo de afirmación que este proyecto se niega a
hacer. La página de [honestidad y límites](/es/start/honesty-and-limits/) establece la regla
general; esto es aplicar esa regla a la superficie más reciente del producto.

## Dónde mirar después

- [Vista general de los módulos](/es/reference/modules/overview/) — dónde se sitúa la
  orquestación entre los demás módulos.
- [Referencia de orquestación](/es/reference/modules/iv-orchestration/) — el módulo
  responsable de ejecutar workflows.
- [Referencia del bus de eventos](/es/reference/events/) — los eventos que emite el plano
  de trabajo, como contrato AsyncAPI.
- [Construir un workflow gobernado](/es/how-to/build-a-workflow/) — el camino práctico una
  vez que sabes qué hace y qué no hace el plano.
