> Traducción automática. La versión en inglés es la fuente autoritativa.

# ADR-0012: Ingesta distribuida — los colectores hacen push al núcleo sobre gRPC + mTLS

- **Status:** accepted
- **Date:** 2026-06-04
- **Deciders:** Fran Olivares (decisión de arranque CB-1)
- **References:** decisiones de arranque del roadmap (CB-1 → opción C); contrato de runtime-ingestion

## Contexto y planteamiento del problema

El plano de ingesta necesitaba una decisión de topología. Los colectores observan en los
hosts del cliente; el núcleo agrega. Las opciones iban desde solo in-process hasta un
modelo de push totalmente distribuido, con implicaciones para el aislamiento, la frontera
de confianza de red y el empaquetado.

## Factores de decisión

- Mantener el plano de datos en la infraestructura del cliente con un cruce de red
  endurecido.
- Preservar el binario único para el caso simple.
- Aislar las dependencias del colector respecto del núcleo.

## Opciones consideradas

- **C — push remoto:** un colector ejecuta los conectores de fuente localmente y hace
  **push** de las observaciones al núcleo sobre **gRPC + mTLS**, sin **ningún listener de
  entrada** en el colector.
- **B — local fuera de proceso:** conectores como subprocesos locales (AutoMTLS), el
  sustrato de nodo único.
- **A — in-process:** fuentes enlazadas en el binario (fast-path de primera parte).

## Resultado de la decisión

Opción elegida: **C (push remoto) como objetivo distribuido**, con B como sustrato de
nodo único y A conservada como fast-path in-process para fuentes de primera parte. Todos
los transportes entran en v1; C **no** se aplaza. El mecanismo vive en el runtime; el
empaquetado distribuido (DaemonSet/Helm) se entrega con el trabajo de cadena de
suministro.

### Consecuencias

- **Bueno:** los datos cruzan la frontera de red endurecidos (mTLS + bearer + authz); el
  colector no expone ningún puerto de entrada; el binario único se preserva.
- **Malo / compromisos:** más piezas móviles para el despliegue distribuido.
- **Neutral:** el valor por defecto de binario único usa las vías in-process /
  subproceso local.

## Por qué se rechazaron las alternativas

- Ni **A** ni **B** por sí solas cubren la escala multi-host; se conservan como el
  fast-path y el sustrato de nodo único respectivamente, no como la respuesta
  distribuida.
