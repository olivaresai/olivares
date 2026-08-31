---
title: "Módulo VIII — datos, conocimiento y contexto"
description: >-
  El data plane gobernado para lo que los agentes saben y usan: bases de
  conocimiento y RAG semántico sobre un índice vectorial intercambiable,
  recuperación gobernada por identidad/clasificación/residencia, y linaje
  append-only que evidencia qué cruces configuró el operador y rechaza los demás.
---

El módulo VIII es el **data plane gobernado**: construye bases de conocimiento y
ejecuta **RAG semántico** sobre un índice vectorial intercambiable, gobierna cada
recuperación por identidad, clasificación y residencia, y registra **linaje
append-only** de lo que cruzó el perímetro y de lo que rechazó el residency gate, de
modo que la residencia se demuestra en vez de limitarse a afirmarla.
También aloja el registro de prompts versionado, la memoria de agentes gobernada y
las políticas de contexto/compactación como datos — no como promesas.

## Qué es

El módulo **orquesta** el data plane; no reimplementa a sus vecinos. Extrae
contenido de **conectores de datos de solo lectura**, pasa cada cuerpo, plantilla
de prompt y entrada de memoria por su **propio mecanismo de expurgo antes** de que nada se
trocee (chunk), embeba, hashee o almacene, y luego gobierna la recuperación contra
los grants que declara el módulo de identidad. El embedding se delega a una junta de
modelo — el módulo nunca llama directamente a un proveedor — y el ranking se delega a
una junta de índice vectorial, de modo que el contrato de gobierno es idéntico tanto
si la recuperación corre in-process como contra un backend ANN externo.

La **línea roja** es innegociable: el producto gobierna los datos del cliente y nunca
los vende ni los exfiltra. Los datos solo cruzan el perímetro por un cruce aprovisionado
por el operador — un proveedor externo de embeddings o una salida SIEM/webhook — y el
residency gate opera deny-closed para cualquier otro destino. Tres mecanismos lo dejan
registrado en el diseño — expurgo antes de indexar, el egress gate y el linaje que
evidencia qué cruces se produjeron.

## Contrato y entidades

El módulo VIII declara **ocho entidades con scope de tenant** en el modelo de datos
compartido: la base de conocimiento, el documento (metadatos y procedencia, nunca el
cuerpo), el chunk (texto expurgado más una clasificación y ACL heredadas), el prompt
y sus revisiones **append-only** inmutables, la memoria de agentes gobernada, la
política de contexto/compactación, y la fila de linaje **append-only**. Sus rutas se
montan bajo el namespace propio del módulo, envueltas con autenticación, scoping de
tenant y autorización; leer conocimiento y linaje es una acción **privilegiada y
auditada**.

La recuperación es el contrato de seguridad, y **el orden es el contrato**: resolver
los grants de la identidad (fail-closed — un error del guard deniega, nunca un allow
degradado), aplicar el residency gate, embeber la consulta, luego **filtrar los
candidatos por clasificación y ACL antes de rankear** para que un chunk que la
identidad no puede ver nunca entre en el conjunto rankeado, luego rankear, luego
añadir la fila de linaje inmutable. El **egress gate** se compone encima: una base de
conocimiento bloqueada por residencia rechaza el ingest o la recuperación con un
embedder que produciría egress, aplicado en create, update, ingest y retrieval
(defensa en profundidad). El contenido de documentos viaja por un contrato de
conector tipado por diseño, **no** por el bus de eventos — los datos de referencia
masivos no deben difundirse.

## En el bus de eventos

El módulo VIII **produce** eventos [`finding.reported`](/es/reference/events/): un
`FindingReport` hasheado por ingest cuando se expurga un secreto o PII, y un finding
cuando un residency o egress gate deniega — solo detalle hasheado, nunca el secreto
ni el cuerpo. La forensia y el compliance consumen el linaje y estos findings. **No
consume** nada del bus para contenido: por diseño el contenido viaja por un contrato
de pull tipado, así que minimal-data es una propiedad del cable, no un filtro en
runtime aplicado a posteriori.

:::caution[Límites honestos]
- **La calidad semántica depende de un embedder configurado.** El embedder por
  defecto es **local y zero-egress** pero **no semántico** (un fallback determinista
  de feature-hash). La base de conocimiento registra su modelo de embed para que el
  fallback nunca se confunda con calidad semántica, y el binario avisa una vez cuando
  corre degradado. Un embedder respaldado por modelo lo configura el operador
  (`OLIVARES_EMBEDDINGS_*`); pon `OLIVARES_EMBEDDINGS_REQUIRE=1` y el arranque **se
  niega a iniciar** en lugar de servir vectores léxicos como si fueran semánticos.
- **La residencia es un egress gate fail-closed, no un ajuste de inferencia.**
  Elegir una región de inferencia no satisface por sí mismo una base de conocimiento
  bloqueada por residencia — el embedder debe estar demostrablemente en-región, o el
  ingest y la recuperación se rechazan. Una identidad sin clearance o sin región se
  normaliza a public / no-region, nunca a un grant más amplio.
- **El ranking por defecto es exacto e in-process** (un escaneo lineal, adecuado para
  un nodo self-hosted o air-gapped hasta aproximadamente 10⁵ chunks por tenant). Un
  backend ANN externo se enchufa tras la junta de índice vectorial para escalar; un
  backend configurado-pero-caído **deniega la petición**, nunca cae en silencio a
  resultados distintos.
- **El transporte en vivo de los conectores es un follow-up documentado.** Los
  conectores hoy parsean el formato exportado nativo con fixtures tras una interfaz
  estable; sin un export configurado una fuente está simplemente vacía. El ingest es
  síncrono; el ingest asíncrono a gran escala es un follow-up.
:::

## Relacionado

- [Catálogo de módulos](/es/reference/modules/overview/) — dónde se sitúa el módulo VIII y su estado de actuación honesto.
- [Referencia del bus de eventos](/es/reference/events/) — el evento `finding.reported` y su payload.
- [Resumen de arquitectura](/es/explanation/architecture/overview/) — el motor, las juntas y las capas.
- [Conectar una fuente](/es/how-to/connect-a-source/) — registrar un conector de datos de solo lectura.
- [Instalación air-gap](/es/how-to/air-gap-install/) — ejecutar el data plane con zero egress.
- [Honestidad y límites](/es/start/honesty-and-limits/) — el contrato honesto a nivel de producto.
