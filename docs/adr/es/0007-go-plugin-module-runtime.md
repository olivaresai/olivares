> Traducción automática. La versión en inglés es la fuente autoritativa.

# ADR-0007: Runtime de modules/connectors fuera de proceso mediante go-plugin (gRPC)

- **Status:** accepted
- **Fecha:** 2026-06
- **Decisores:** Olivares AI
- **Referencias:** diseño del stack (runtime de módulos); diseño de la frontera de licencia

## Contexto y planteamiento del problema

La plataforma debe permitir que connectors y modules propios y de terceros la extiendan
sin arrastrar sus árboles de dependencias al motor, y sin contaminar el ecosistema
permisivo de connectors con la licencia copyleft del motor.

## Factores de decisión

- Aislar las dependencias de los connectors del build/SBOM del motor.
- Un contrato estable y versionado a través de la frontera de proceso.
- Mantener limpia la frontera Apache-2.0 de los connectors (un connector nunca enlaza el motor AGPL).

## Opciones consideradas

- **`hashicorp/go-plugin` sobre gRPC** para modules/connectors fuera de proceso, más
  los modules de núcleo compilados en proceso.
- **Solo plugins en proceso** (el paquete `plugin` de Go o compilados dentro).

## Resultado de la decisión

Opción elegida: **`hashicorp/go-plugin` (gRPC)** para connectors/modules fuera de proceso,
con los connectors propios embebidos y lanzados como subprocesos aislados, y los modules
de núcleo compilados dentro. El SDK de connectors es una interfaz de Go más un contrato
gRPC/protobuf versionado.

### Consecuencias

- **Bueno:** las dependencias de un connector no entran en el binario/SBOM del motor; la
  frontera Apache/AGPL se mantiene limpia y se aplica en CI; los terceros pueden distribuir
  connectors de forma independiente.
- **Malo / compromisos:** un contrato gRPC que versionar y un salto de IPC para los
  componentes fuera de proceso.
- **Neutral:** el binario único sigue embebiendo los connectors propios (aislados como
  subprocesos), de modo que sigue siendo un solo artefacto.

## Por qué se rechazaron las alternativas

- **Solo en proceso** — arrastra las dependencias de cada connector al motor y hace
  imposible aplicar la frontera de licencia de forma mecánica.
