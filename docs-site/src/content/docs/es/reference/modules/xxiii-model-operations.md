---
title: "Módulo XXIII — operaciones de modelos"
description: >-
  El registro gobernado de los modelos que POSEES — alojados, ajustados o importados — con
  admisión de modelos firmados y despliegues locales de inferencia. Gobierna la cadena de
  suministro de tus propios modelos; no los entrena, sirve ni somete a benchmarks.
---

El módulo XXIII es el lado de los modelos **propios** del stack de modelos. Donde el módulo X
(modelos y proveedores) gobierna el *catálogo de referencia* y el *enrutamiento* de los modelos
que consumes, este módulo gobierna los modelos que **posees y operas**: un registro versionado,
la puerta de **admisión** de modelos firmados que decide qué versiones se pueden desplegar, y los
**despliegues de inferencia** locales que los sirven. Hace seguimiento y gobierna; nunca entrena
un modelo, ejecuta un trabajo de ajuste fino ni ejecuta inferencia por sí mismo.

La superficie de consola de este módulo es **Operaciones de modelos** (grupo Inteligencia), con
pestañas para modelos propios, datasets, trabajos de ajuste fino, admisión, despliegues y el
ledger de sellos AIBOM. La postura GPAI de proveedores (por proveedor) reside en **Modelos y
proveedores**, y la cadena de suministro de agentes tiene su propia vista — ambos son asuntos por
proveedor / por estate, no por modelo propio.

## Qué es

Tres superficies cooperantes, todas deny-closed y auditadas:

- **Modelos y versiones propios.** Un registro de los modelos que posees (`hosted`, `fine_tuned`,
  `imported`), cada uno con **versiones** inmutables que nombran un artefacto. Se registra una
  versión y después se admite su artefacto firmado — la fila de versión nunca cambia.
- **Admisión.** Una **política de confianza** por tenant y el historial de **veredictos**
  registrado. La política nombra los anclajes de confianza — raíces CA y/o claves públicas, más
  identidades y emisores Sigstore opcionales — y el **método** de firma se deriva de lo que
  configuras (`sigstore-keyless`, `certificate-pki` o `bare-key`); una política vacía no admite
  nada. Admitir una versión verifica un **bundle** de firma contra la política y registra el
  veredicto. Un veredicto que no consigue verificarse se registra honestamente, no se oculta.
- **Despliegues.** Despliegues locales de inferencia (vLLM, Ollama, llama.cpp, otros). Cuando el
  tenant **exige** modelos firmados, crear o actualizar un despliegue que referencia una versión
  vuelve a comprobar la admisión: si la versión no tiene veredicto verificado, o la raíz de
  confianza que la admitió ya no está en la política, el despliegue se rechaza.

## Linaje y evidencia

- **Datasets.** Componentes de linaje de datos mínimos — un nombre, una referencia de contenido
  opcional y un hash, una clasificación y una etiqueta de gobernanza — **nunca los contenidos del
  dataset**. Un dataset abarca todo el tenant; su referencia de modelo opcional es un puntero de
  linaje, validado deny-closed. `verified` es una **afirmación del operador** sobre la
  procedencia, nunca un resultado criptográfico, y la consola lo etiqueta como tal.
- **Trabajos de ajuste fino.** Registros de trabajo de ajuste fino ejecutado externamente y de la
  **versión** de modelo que produjo cada uno. El plano nunca inicia, cancela ni ejecuta
  entrenamiento y no almacena pesos ni contenidos de datasets — son registros de inventario, no
  un lanzador de entrenamiento.
- **AIBOM y model card.** Desde un modelo propio puedes **generar** un AIBOM CycloneDX en vivo (o
  una serialización SPDX 3.0.1) y una model card (JSON o Markdown), todo de solo lectura. Un
  documento generado no es evidencia hasta que lo **sellas**: el sellado ancla un compromiso de
  hash de contenido canónico al audit ledger (siempre CycloneDX — SPDX nunca puede sellarse). El
  ledger almacena solo el hash, por lo que el recibo del sello es la única oportunidad de guardar
  el documento sellado. La pestaña transversal de **sellos AIBOM** es el ledger duradero y de
  solo anexado de esos compromisos.

## Qué aplica

Cuando `require_signed` está activado, un despliegue que referencia una versión de modelo se
admite **solo si** esa versión tiene un veredicto de admisión verificado cuya raíz de confianza de
anclaje siga configurada. Retirar una raíz de la política deniega retroactivamente futuras
creaciones/actualizaciones de despliegues de versiones que solo admitió esa raíz — primero deben
ser **readmitidas** bajo los anclajes actuales. Este es el mismo pin de anclaje que el motor
registra en cada veredicto (`signer_roots`), expuesto para que un operador pueda ver exactamente
qué raíz avaló una versión.

## Qué no es

- **No** ejecuta entrenamiento ni trabajos de ajuste fino — registra su estado para el linaje.
- **No** sirve inferencia — gobierna los registros de despliegue que lo hacen.
- **No** decide «actualmente desplegable» a partir de un veredicto almacenado — solo la
  comprobación del motor en el momento de desplegar tiene autoridad, por lo que la consola nunca
  etiqueta una versión como fiable o desplegable solo a partir del historial.

## Cadena de suministro de agentes

La vista de consola independiente **Artefactos de agente** registra cuatro clases de artefactos
del estate del tenant: skills de agente, extensiones `.mcpb`, plantillas MCP App `ui://` y archivos
de instrucciones `AGENTS.md`. El registro almacena identidad, procedencia, huellas de contenido y
metadatos de postura — nunca cuerpos de skills, manifiestos ni texto de instrucciones. Un grado de
postura es un **resultado de escaneo registrado** de un scanner de connector u operador, no un
escaneo que ejecute la consola; un grado ausente se muestra neutramente como no escaneado.

Su BOM CycloneDX 1.6 de cadena de suministro de agentes es distinto de un AIBOM de linaje por
modelo. Los sellos añaden un compromiso de hash de contenido canónico al ledger separado
`models.agent_aibom`, mientras que el recibo devuelto sigue siendo la única copia del documento
sellado. La cobertura es solo de artefactos registrados: un artefacto nunca registrado no aparece.
