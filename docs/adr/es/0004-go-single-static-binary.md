> Traducción automática. La versión en inglés es la fuente autoritativa.

# ADR-0004: Motor en Go, un único binario estático con la web embebida

- **Status:** accepted
- **Date:** 2026-06
- **Deciders:** Fran Olivares
- **References:** stack decisions register (T1, T5); stack architecture

## Contexto y planteamiento del problema

Un control plane de seguridad self-hosted y compatible con air-gap necesita ser trivial de
desplegar, nativo del ecosistema eBPF/OpenTelemetry/cloud-native, y entregable como un
único artefacto. El lenguaje del motor y la forma de entregar la UI se derivan ambos de
ello.

## Factores de la decisión

- Un único artefacto autocontenido para self-hosting y air-gap.
- eBPF nativo y un runtime de módulos/plugins maduro.
- Concurrencia robusta para la ingesta y el bus de eventos.

## Opciones consideradas

- **Go**, único binario estático, web embebida mediante `go:embed`.
- Motor en **Rust**.
- Motor en **Node/TypeScript**.
- **SPA separada** (dos artefactos) en lugar de una UI embebida.

## Resultado de la decisión

Opción elegida: **Go**, compilado a un único binario estático, con la UI web de React
**embebida mediante `go:embed`** y servida desde el mismo origen que la API — de modo que
todo el producto es **un único fichero**.

### Consecuencias

- **Bueno:** un único artefacto que entregar, verificar y ejecutar; eBPF nativo; gran
  encaje cloud-native; concurrencia adecuada para la ingesta.
- **Malo / contrapartidas:** la UI se compila y se embebe como parte del build del binario.
- **Neutral:** Node/TypeScript se usa solo para la UI web, no para el motor.

## Por qué se rechazaron las alternativas

- **Rust** — build/iteración más lentos y excesivo para las necesidades de v1.
- **Motor en Node/TS** — mala historia de eBPF y no es un único binario estático, pese a
  ser una zona de confort.
- **SPA separada** — dos artefactos que desplegar y versionar; la UI embebida lo mantiene
  como uno solo.
