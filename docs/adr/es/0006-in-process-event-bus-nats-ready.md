> Traducción automática. La versión en inglés es la fuente autoritativa.

# ADR-0006: Bus de eventos en proceso por defecto, agnóstico al transporte para NATS

- **Status:** accepted
- **Fecha:** 2026-06
- **Decisores:** Olivares AI
- **Referencias:** contrato SDK/runtime/bus de eventos; diseño del stack

## Contexto y planteamiento del problema

Los connectors elevan observaciones a un bus de eventos interno; los modules y los connectors de salida
se suscriben por tipo. El binario único debe funcionar sin ningún broker de mensajes y, a la vez, un
despliegue multi-host necesita un bus distribuido.

## Factores de decisión

- Sin dependencia de un broker en el modo por defecto de binario único.
- Un camino hacia un bus distribuido que no obligue a los suscriptores a cambiar.

## Opciones consideradas

- **Canales de Go en proceso como opción por defecto, tras una interfaz `Bus` agnóstica al transporte**
  que una implementación distribuida (NATS) pueda reemplazar.
- **Un broker (NATS) desde el principio.**

## Resultado de la decisión

Opción elegida: **bus de canales de Go en proceso como opción por defecto de la v1**, con la interfaz
`Bus` **sin exponer ningún canal** para que pueda encajarse una implementación de **NATS** en despliegues
multi-host **sin cambiar un solo suscriptor**. La entrega es asíncrona y de entrega al menos una vez (at-least-once);
los consumidores deduplican por el timestamp de la clave natural.

> **Enmendado por ADR-0017 (2026-06-12):** el "at-least-once" de la frase anterior
> era incorrecto como descripción de la entrega del BUS: la implementación y el contrato S02 §4
> son de entrega como mucho una vez (at-most-once) (los errores del handler se registran, los eventos en cola se descartan al cerrar);
> el at-least-once aplica a la reemisión a nivel de fuente (reejecuciones de `Gather`), que es lo que
> deduplican los consumidores. ADR-0017 registra el backend de NATS entregado: fan-out
> local en proceso sin cambios + puente NoEcho, at-most-once entre nodos, sin JetStream en la v1.

### Consecuencias

- **Bueno:** el binario único no necesita broker; el camino distribuido es un reemplazo directo (drop-in).
- **Malo / compromisos:** la semántica at-least-once empuja la deduplicación a los consumidores.
- **Neutral:** NATS es opcional y solo para la topología distribuida.

## Por qué se rechazaron las alternativas

- **Broker desde el principio** — añade una dependencia externa a cada instalación, frustrando
  el objetivo de binario único / air-gap, por un valor que solo necesita la topología distribuida.
