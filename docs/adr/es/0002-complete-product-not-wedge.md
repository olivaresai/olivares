> Traducción automática. La versión en inglés es la fuente autoritativa.

# ADR-0002: Publicar el producto completo (28 módulos), no una cuña

- **Status:** accepted
- **Date:** 2026-06-02
- **Deciders:** Fran Olivares
- **References:** product decisions register (P1); module catalog (the 28 modules)

## Contexto y planteamiento del problema

Una estrategia habitual de salida al mercado para un producto de infraestructura es una
"cuña" estrecha: publicar una única capacidad afilada, ganar una cabeza de puente y
expandirse más tarde. Para Olivares AI la cuña candidata era el mapa de acceso de
lectura/escritura por sí solo. La cuestión era si publicar la cuña o el control plane
completo.

## Factores de la decisión

- Primera impresión: los compradores empresariales (CTO/SOC/seguridad) evalúan un control
  plane como una plataforma, no como una funcionalidad.
- El mapa R/RW es más valioso *dentro* de una plataforma completa que como herramienta
  independiente.
- Evitar la re-arquitectura: una plataforma modular admite nuevos módulos sin rehacer
  trabajo.

## Opciones consideradas

- **Producto completo** — publicar los 28 módulos como una plataforma coherente (la
  gestión de modelo propio / el fine-tuning es una capacidad planificada, no uno de los
  28).
- **Cuña estrecha** — publicar el mapa R/RW por sí solo y expandirse más tarde.

## Resultado de la decisión

Opción elegida: **producto completo**. La versión inicial es la plataforma completa,
construida en torno a Claude y Claude Code — inventario, sesiones en vivo, el mapa R/RW,
gobernanza, scoping de fuentes/credenciales, despliegue, conocimiento, seguridad,
grabación de sesiones privilegiadas, gestión de modelos/proveedores, el proxy de
inferencia en línea, FinOps, evals, cumplimiento, el reenviador a SIEM, catálogo,
integraciones de salida, eventing, voz, sandbox, red-teaming y salud — con la propia API
del motor, la multi-tenencia y los cuadros de mando como capacidades de núcleo/consola. El
mapa R/RW es **una capacidad diferenciada clave dentro de** ese producto, no el producto en
sí.

### Consecuencias

- **Bueno:** una plataforma completa y creíble desde el primer día; el mapa de acceso
  aterriza en su contexto.
- **Malo / contrapartidas:** una superficie de v1 mucho mayor que construir y mantener
  honesta; la profundidad varía por módulo y debe documentarse con honestidad (véase
  *Honestidad y límites*).
- **Neutral:** la gestión de modelo propio / el fine-tuning es una capacidad planificada,
  no uno de los 28 módulos publicados.

## Por qué se rechazaron las alternativas

- **Cuña estrecha** — rechazada: infravalora un producto de plataforma y arriesga que el
  mapa R/RW se perciba como un "visor de sesiones" de mercado en lugar del motor de
  least-privilege drift que es.
