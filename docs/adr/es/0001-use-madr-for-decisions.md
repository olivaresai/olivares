> Traducción automática. La versión en inglés es la fuente autoritativa.

# ADR-0001: Registrar las decisiones de arquitectura usando MADR

- **Status:** accepted
- **Date:** 2026-06-07
- **Deciders:** Olivares AI
- **References:** sesión de documentación/producto que establece el registro de ADR

## Contexto y planteamiento del problema

Las decisiones de arquitectura del control plane estaban registradas en varios
documentos de planificación y de contrato (arquitectura, stack, licencias, los
contratos por sesión y las "decisiones de arranque"). Esa historia es real y está
bien segregada, pero no está en una forma que un nuevo contribuidor o evaluador
pueda leer decisión por decisión: *qué* se decidió, *por qué* y *qué se rechazó*.
El contexto se pierde entre sesiones cuando la justificación vive solo en una larga
prosa de planificación.

## Factores de decisión

- Un registro duradero, indexado por decisión, que sobreviva entre contribuidores.
- Un formato ligero que no se convierta en un proyecto de documentación por sí
  mismo.
- Publicable como parte de la documentación del producto.

## Opciones consideradas

- **MADR (Markdown Any Decision Records).** Mínimo, ampliamente adoptado, nativo de
  Markdown.
- **Un registro de decisiones a medida.** Más libertad, pero sin convención
  compartida.
- **Sin ADR formales.** Mantener la justificación solo en los documentos de
  planificación.

## Resultado de la decisión

Opción elegida: **MADR**. Cada decisión ya tomada se captura como un
`docs/adr/NNNN-*.md` numerado con contexto, la opción elegida, las consecuencias y
las alternativas rechazadas, y se publica en la sección *Explanation* del sitio de
documentación.

### Consecuencias

- **Bueno:** las decisiones son descubribles y autoexplicativas; los nuevos
  contribuidores no vuelven a litigar cuestiones ya zanjadas.
- **Malo / compromisos:** una pequeña disciplina continua de añadir un registro
  cuando se toma una decisión.
- **Neutral:** los documentos de planificación existentes siguen siendo la fuente
  que citan los ADR, no algo que los ADR reemplacen.

## Por qué se rechazaron las alternativas

- **Registro a medida** — reinventa una convención ya resuelta; más difícil para
  contribuidores externos.
- **Sin ADR** — deja la justificación enterrada en prosa, que es como se estaba
  perdiendo el contexto.
