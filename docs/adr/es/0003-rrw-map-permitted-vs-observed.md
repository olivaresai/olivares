> Traducción automática. La versión en inglés es la fuente autoritativa.

# ADR-0003: El mapa R/RW con un diff de Permitido-vs-Observado es una capacidad diferenciada clave

- **Status:** accepted
- **Date:** 2026-06-02
- **Deciders:** Fran Olivares
- **References:** product decisions register (P2); architecture (module III)

## Contexto y planteamiento del problema

Muchas herramientas pueden *observar* la actividad de los agentes, y muchas pueden
*enumerar* los permisos concedidos. Ninguna por sí sola responde a la pregunta que importa
para la gobernanza: **¿lo que un agente está *permitido* tocar es lo mismo que lo que se le
*observa* tocando?** El producto necesitaba una capacidad defendible y difícil de
convertir en commodity que la respondiera — una de varias que ofrece, no el producto
entero.

## Factores de la decisión

- Una capacidad difícil de convertir en commodity y directamente útil para
  seguridad/SOC.
- Construida a partir de señales que el producto puede obtener realmente (auditoría,
  telemetría, kernel).
- Honesta sobre la fidelidad en lugar de exagerar.

## Opciones consideradas

- **Diff de Permitido-vs-Observado** (least-privilege drift) sobre un mapa de acceso de
  lectura/escritura.
- **Solo observado** — mostrar lo que hicieron los agentes.
- **Solo permitido** — mostrar los permisos concedidos.
- **Visualización de sesiones** — mostrar las sesiones de agentes en vivo.

## Resultado de la decisión

Opción elegida: **el mapa de acceso R/RW (módulo III) con el diff de
Permitido-vs-Observado**. Para cada arista origen→recurso el producto clasifica
lectura/escritura, registra la fuente de la señal y la confianza, y compara los grants
declarados con el uso observado para sacar a la luz el **least-privilege drift**: accesos
inesperados, grants sin usar y aristas pendientes de reconciliación.

### Consecuencias

- **Bueno:** un artefacto distintivo y relevante para la seguridad sobre el que se apoya la
  gobernanza de la plataforma, junto con los demás módulos — no una funcionalidad
  aislada.
- **Malo / contrapartidas:** depende de la identidad por agente para una atribución firme
  (una cuenta de servicio compartida se colapsa a confianza *aproximada*); la cobertura
  está **escalonada** por almacén; debe ser honesta sobre `unknown` y `approximate` en
  lugar de fabricar certeza.
- **Neutral:** el mapa de acceso es una *vista* sobre el modelo de datos general
  (véase ADR-0005), no un esquema aparte.

## Por qué se rechazaron las alternativas

- **Solo observado / Solo permitido** — cada una es la mitad de la imagen; el valor está en
  el *diff*.
- **Visualización de sesiones** — convertida en commodity (los proveedores publican "vista
  de agentes"); no es un foso defensivo duradero.
