---
title: "Módulo XII — calidad, evaluaciones y testing"
description: >-
  Medición de calidad: puntuar salidas candidatas frente a suites golden versionadas
  con scorers conectables (incluido un juez LLM fail-closed), y convertir el resultado
  en la evidencia canónica entre módulos que otros módulos consumen.
---

El módulo XII responde a una sola pregunta — *¿sigue mi agente haciendo lo correcto?* —
**puntuando** salidas candidatas frente a **suites golden versionadas** y emitiendo el
resultado como evidencia canónica entre módulos. Es un módulo de la capa de Inteligencia:
**mide**, no ejecuta el sujeto ni actúa sobre la infraestructura. Esta página es la
referencia de lo que el módulo de evaluaciones hace hoy, y sus límites honestos.

## Qué mide (y qué nunca ejecuta)

XII es una capa de medición, no una capa de ejecución. Una salida candidata le llega ya
producida — desde el sandbox de testing (módulo XVII), desde CI, en línea en la petición,
o como una señal muestreada de una sesión real — y XII la puntúa frente a los casos de una
suite. **El único modelo que XII invoca alguna vez es el juez** (para el scorer
`llm_judge`); nunca ejecuta por sí mismo el agente o modelo sujeto. Producir salidas es
tarea del sandbox, no de XII.

El conjunto de scorers es **conectable**. Built-ins deterministas y puros cubren los
contratos comunes — `exact`, `contains`, `not_contains`, `regex`, `json_valid`,
`json_equal` y `numeric_range`. Junto a ellos hay un scorer **`llm_judge`** que invoca un
modelo a través del puerto Judge para calificar frente a una rúbrica.

## Suites, ejecuciones y el artefacto canónico

Una **suite** es un dataset golden versionado: contiene sus casos, un scorer por defecto,
un umbral de aprobación y un umbral de regresión. Los casos son **append-only e inmutables
por versión** — corregir un caso acuña una nueva `suite_version`, nunca una edición in
situ, de modo que el dataset que produjo cualquier veredicto pasado siempre es
reconstruible.

Una **ejecución** puntúa cada caso de una suite, agrega un `score` y un `pass_rate`, y
persiste tres cosas: evidencia por caso append-only, un agregado de ejecución mutable, y un
único **`EvalResult`** del núcleo — el artefacto canónico (`Suite`, `SubjectKind`,
`SubjectID`, `Score`, `Passed`, `OccurredAt`, `Metrics`) que compliance (XIII) y la UI leen
**sin conocer las propias tablas de XII**. Las ejecuciones se ejecutan de forma síncrona; el
stream SSE de una ejecución *reproduce la ejecución persistida* (frames por caso, luego un
resumen), no actúa. Una regresión frente a una baseline establece `regressed` y escribe un
**`Finding`** del núcleo (`Kind = eval_regression`), emitido en best-effort en el bus como
[`finding.reported`](/es/reference/events/) para que los módulos de entrega
(salud/notificaciones) lo enruten. Del lado de lectura, los **scorecards** agregan
pass-rate, score medio y tendencia por sujeto y se exportan como CSV/JSON.

## Datos mínimos, por construcción

La salida candidata **nunca se persiste** — venga de donde venga. Un resultado por caso
almacena solo un hash unidireccional del detalle y una etiqueta recortada y depurada para
la UI; el expurgo lo hace el handler antes del almacenamiento, nunca se asume del store.
El **monitor** puntúa *señales de comportamiento* de una sesión real — su estado, recuento
de findings, severidad máxima y cifras de tokens/coste (extraídas de las señales del núcleo
`Session`, `Finding` y `CostRecord`) — y **nunca el texto de salida en bruto**, que la
plataforma no persiste en absoluto. Las fixtures golden son la única excepción acotada:
contenido autorizado por el operador, opt-in, no de producción, recortado por el handler
antes de escribir para que una suite pueda ejecutarse de verdad.

## Calibración del juez, mitigación de sesgo y la puerta de regresión en CI

Los veredictos del juez se **confían solo después de medirse**. Un conjunto de calibración
etiquetado por humanos (construido con la sesión guiada `olivares evals label`) alimenta una
**ejecución de calibración** que mide al juez frente a la referencia humana: porcentaje de
acuerdo con su intervalo de Wilson al 95 %, **kappa de Cohen** (el acuerdo por sí solo no es
defendible bajo desbalanceo de clases), sensibilidad/especificidad con sus denominadores, y
una correlación de sesgo por verbosidad. El informe es evidencia append-only; el objetivo —
acuerdo ≥ 0.85 **y** un kappa definido ≥ 0.6 — puede elevarse por ejecución pero nunca
bajarse. Un conjunto cuyas etiquetas humanas son todas de aprobado no puede medir el acuerdo
corregido por azar y no certifica nada.

La mitigación de sesgo está integrada y *medida*: el prompt del juez fuerza el razonamiento
**antes** del veredicto (el análisis se descarta sobre la marcha — datos mínimos) e instruye
contra premiar la longitud; el modo pairwise opt-in de la comparación A/B juzga cada caso
compartido dos veces con el orden de presentación invertido, declara un ganador **solo
cuando ambos órdenes coinciden**, e informa de la tasa medida de `position_consistency`.

La **puerta de regresión** (`POST /gate`, CLI `evals gate`) convierte todo esto en un
veredicto bloqueante de CI: una regresión frente a la baseline, un pass-rate por debajo del
umbral de la suite, o un **juez sin calibrar** hacen fallar la puerta (exit 1); una
credencial de juez ausente degrada a un *warn declarado*, nunca a un pase silencioso. El
coste del juez en CI se controla con una muestra determinista y sembrada de casos, una caché
de veredictos clavada en contenido + pin del modelo del juez + versión del prompt, y un
pre-flight de presupuesto FinOps que se niega a gastar más allá de un tope. La única salida
de una puerta fallida es el **override gobernado** — de nivel admin, con motivo escrito,
auditado — que cambia el veredicto *efectivo* que CI re-comprueba, nunca el registrado. Cada
tasa reportada se entrega con su denominador y su intervalo al 95 %; consulta
`docs/EVAL-METHODOLOGY.md` en el repositorio para la metodología completa y las fuentes.

:::caution[Límites honestos]
- **`llm_judge` es fail-closed, nunca un falso aprobado.** La invocación del modelo es una
  costura declarada: sin juez cableado, el scorer `llm_judge` devuelve `skipped` (excluido
  del denominador), nunca un pase silencioso. La raíz de composición inyecta el adaptador de
  juez real; hasta entonces, los casos juzgados se reportan honestamente como no evaluados.
- **La puerta bloquea merges, no infraestructura.** La puerta de regresión devuelve un
  veredicto que un pipeline de CI mapea a su código de salida; XII sigue sin desplegar nada
  ni disparar nada. Un juez sin calibrar no puede pasar su propia puerta — la calibración se
  mide frente a etiquetas humanas, nunca se asume.
- **XII no ejecuta el sujeto.** Puntúa salidas que se le entregan; nunca ejecuta el agente o
  modelo bajo prueba. La única llamada a modelo que hace es la del juez.
- **La monitorización son señales, no texto.** La monitorización de sesiones reales puntúa señales de
  resultado con datos mínimos — nunca la salida en bruto, que nunca se persiste. La ausencia
  de una señal monitorizada no es prueba de comportamiento.
- **Sin superficie de actuación.** XII gobierna y observa la calidad; no despliega nada, no
  dispara nada y no bloquea ninguna infraestructura. El *veredicto* pre/post-deploy que
  proporciona es evidencia sobre la que actúa el módulo de despliegue — consulta
  [Honestidad y límites](/es/start/honesty-and-limits/).
:::

## Relacionado

- [Catálogo de módulos](/es/reference/modules/overview/) — dónde encaja XII y la división Gobernar/Actuar.
- [Referencia del bus de eventos](/es/reference/events/) — el evento `finding.reported` que emite una regresión.
- [Visión general de la arquitectura](/es/explanation/architecture/overview/) — la capa de Inteligencia.
- [Gobernar y aprobar](/es/how-to/govern-and-approve/) — actuar sobre un finding de regresión.
- [Honestidad y límites](/es/start/honesty-and-limits/) — las costuras deny-closed a lo largo del producto.
