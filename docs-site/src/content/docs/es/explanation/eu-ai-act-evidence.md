---
title: Evidencia del Reglamento de IA de la UE a partir de datos de runtime
description: >-
  Cómo un control plane self-hosted convierte el comportamiento en vivo de tu
  estate de IA en la evidencia técnica que necesita un expediente del Reglamento de IA de la UE —
  con forma de Anexo IV, generada a partir de datos de runtime y almacenada en el control
  plane que operas tú mismo. Para compradores europeos regulados que no pueden poner un
  control plane SaaS estadounidense en su ruta de cumplimiento.
---

La mayoría del tooling de gobernanza de IA produce evidencia del mismo modo en que una presentación produce hechos:
alguien lo escribe, y confías en que era cierto. Bajo el
**Reglamento (UE) 2024/1689 (el Reglamento de IA de la UE)** eso no basta. El proveedor de
un sistema de alto riesgo tiene que elaborar la **documentación técnica del Anexo IV** *antes* de
poner el sistema en el mercado y **mantenerla actualizada** a lo largo del ciclo de vida
(Art. 11), y el plan de vigilancia poscomercialización (Art. 72) tiene que alimentarse de lo que el
sistema realmente hace en producción.

Esta página explica cómo Olivares AI te permite **generar** esa evidencia a partir del
**comportamiento de runtime de tu estate** en lugar de curarla a mano — y por qué un
**control plane self-hosted y AGPL** es la forma que sobrevive a la revisión de un comprador europeo
regulado cuando un control plane SaaS estadounidense no lo hace.

:::note[Quién es el "proveedor" aquí]
Olivares AI es **tooling de gobernanza sobre sistemas de IA, no en sí mismo un sistema de alto riesgo
del Anexo III** en el uso típico. Si *tu* sistema de IA es de alto riesgo, y quién
es su proveedor o responsable del despliegue, es una determinación legal que tú haces — no nosotros. Lo que
hacemos es que las **obligaciones de documentación técnica y vigilancia sean baratas de
satisfacer con evidencia real**. Consulta [Honestidad y límites](/es/start/honesty-and-limits/)
para saber qué afirma y qué no afirma la plataforma.
:::

## Por qué "a partir de datos de runtime" es lo esencial

Los deberes de documentación del Reglamento de IA de la UE no son únicos. El Anexo IV pide la
arquitectura del sistema, sus **recursos computacionales**, sus características de monitorización y control,
sus métricas de rendimiento, su sistema de gestión de riesgos y un
registro de **cambios en el ciclo de vida** — y el Art. 72 exige un plan de vigilancia poscomercialización
que de verdad ejecutes. Un documento Word estático se desactualiza en el momento en que un
modelo se sustituye o un agente gana una nueva herramienta.

Olivares AI ya observa el estate para construir su
[read/write access map](/es/explanation/#el-access-map-read-first-minimal-data-permitted-vs-observed)
y su [audit ledger](/es/reference/glossary/#audit-ledger) append-only, hash-chained y firmado con Ed25519.
El módulo de cumplimiento convierte
esas mismas observaciones en **paquetes de evidencia consumibles por auditores**: sellados,
anclados al ledger, exportables como JSON, CSV u **OSCAL**, con una prueba de integridad
en vivo. El documento se *deriva de lo que pasó*, no se afirma sobre lo que se
pretendía.

Dos reglas de honestidad están cableadas en el producto y se trasladan directamente a la evidencia:

- Un control cuyo único respaldo es arquitectónico reporta **`by_design`**, nunca
  `satisfied`. "Satisfied" requiere evidencia de tenant real y enlazada.
- El catálogo de frameworks está **version-pinned a su fuente primaria** con una
  fecha `verified_on`, y cada framework lleva un descargo de "esto es un mapeo técnico,
  no una certificación".

## El crosswalk del Anexo IV, en breve

El módulo de cumplimiento mapea los artículos del Reglamento de IA de la UE que puede evidenciar —
**Art. 5, 6, 9, 10, 11, 12, 13, 14, 15, 50 y 72** — a capacidades que el control
plane ya produce. Abajo está la vista por sección del Anexo IV; la plantilla completa fila por fila
(con los endpoints exactos y los huecos explícitos) se entrega en el paquete de confianza y
adquisición como `eu-ai-act-annex-iv.md`.

| Tema del Anexo IV | Qué proporciona el control plane | Obtenido de |
|---|---|---|
| **1.** Descripción general (propósito, proveedor, versiones, entrega) | Inventario de modelos + versiones; **model card** (JSON/Markdown; los campos desconocidos son explícitamente `not_recorded`, nunca inventados) | `GET /v1/m/models/owned-models/{id}/model-card` |
| **2.** Proceso de desarrollo, arquitectura, **recursos computacionales**, procedencia de datos, supervisión, V&V | Arquitectura de referencia; **contabilidad de cómputo/coste** por inferencia (el lado *operativo* del 2(c) — las cifras de tiempo de entrenamiento **no** se evidencian, y el catálogo lo dice); registro de datasets + **AIBOM** sellado (CycloneDX 1.6) y **SPDX 3.0.1 AI Profile**; configuración de aprobaciones/HITL; resultados de eval + red-team | Muestras de coste de FinOps; `GET /v1/m/models/owned-models/{id}/aibom?format=spdx`; módulo de evals |
| **3.** Monitorización, funcionamiento y control | Evidencia de operación en vivo: findings de guardrail/anomalía, access map + **Permitted-vs-Observed drift**, líneas de tiempo de sesiones, estado del kill-switch | findings; `GET /v1/m/accessmap/drift` |
| **4.** Métricas de rendimiento | Metodología de eval + resultados (calibración de LLM-judge, gates de regresión bloqueantes) | módulo de evals |
| **5.** Sistema de gestión de riesgos (Art. 9) | Clasificación de riesgo por agente (tier de la UE × función NIST), revisión gobernada con dual-control, exportación de risk-register | `GET /v1/m/compliance/risk`; `GET /v1/m/compliance/dora` |
| **6.** Cambios en el ciclo de vida | Ledger de cambios/despliegues; historial de admisión de modelos; ciclo de vida de versiones | registros de despliegue; `GET /v1/m/models/model-admissions` |
| **7.** Estándares aplicados | El **catálogo de 26 frameworks**, version-pinned, con `verified_on` | `GET /v1/m/compliance/frameworks` |
| **8.** Declaración UE de conformidad (Art. 47) | **No generada** — un acto legal del proveedor; la plataforma solo la almacena/enlaza | suministrada por el proveedor |
| **9.** Plan de vigilancia poscomercialización (Art. 72) | Evidencia continua que el plan puede citar: findings, SLOs, comunicaciones de incidentes, exportación al ledger + SIEM | docs de production-readiness + status/incidentes |

### Los huecos honestos, declarados de entrada

Poner esto en el expediente lo *refuerza* — un evaluador confía en un documento que
nombra sus propias fronteras.

- **El cómputo de tiempo de entrenamiento, la calidad/sesgo estadístico de los datasets y la justificación
  de diseño** no son evidenciados por el control plane. Esos los redacta el proveedor.
- **Los deberes de transparencia del Art. 50** (avisos de interacción, marcado de contenido de IA) son un
  hueco honesto de la propia plataforma, registrado como tal en el catálogo.
- El control plane evidencia la **mitad operativa** del Anexo IV — lo que tu
  estate hace, atribuible y con evidencia de manipulación. **No** redacta la
  narrativa de diseño del proveedor ni firma la declaración de conformidad.

### No hardcodees las fechas — sírvelas

Los calendarios de aplicación de alto riesgo están **sujetos a cambios** (el acuerdo provisional del
Digital Omnibus de 2026-05-07 movió varios). Copiar fechas a un fichero estático es cómo los
documentos de cumplimiento se quedan obsoletos y erróneos. El control plane sirve el
calendario regulatorio **como datos** — cada entrada con su fuente y `verified_on`:

```http
GET /v1/m/compliance/calendar
```

Tu pipeline de GRC lee el calendario en vivo; tu paquete de evidencia lo referencia.
Nadie reteclea una fecha.

## Flujo de empaquetado

1. Por cada sistema de IA en alcance, obtén: model card (`?format=md`), AIBOM
   (`?format=spdx`), clasificación de riesgo, resúmenes de eval, el snapshot de drift y
   el extracto del calendario.
2. **Sella** el bundle como un paquete de evidencia de cumplimiento — append-only,
   anclado al ledger:
   `POST /v1/m/compliance/frameworks/eu_ai_act/evidence` →
   `GET /v1/m/compliance/evidence/{id}/export?format=oscal`.
3. Adjunta las secciones redactadas por el proveedor (decisiones de diseño, la narrativa del Art. 9,
   la declaración del Art. 47). La plataforma no fabrica lo que solo el
   proveedor conoce.

El resultado es un expediente del Anexo IV cuyas secciones operativas son **reproducibles a partir
del ledger** y **re-verificables fuera de la caja** — una propiedad que un documento curado a mano
no puede ofrecer.

## Por qué la soberanía es el factor decisivo para los compradores europeos regulados

Para un banco, hospital, ministerio o universidad bajo supervisión de la UE, *dónde vive la
evidencia* no es un detalle — a menudo es la puerta.

- **El data plane nunca sale de tu frontera.** Los collectors corren en **tu**
  infraestructura; el access map almacena solo la *relación* (agente → recurso,
  lectura o escritura) con una fuente y un nivel de confianza — **sin payloads, sin secretos,
  sin PII**. La evidencia de cumplimiento se construye a partir de datos que nunca tuvieron que transitar la
  nube de un proveedor.
- **El control plane puede ser totalmente self-hosted, o air-gapped** con cero egress
  y una licencia offline. No hay ningún proveedor en tu ruta de cumplimiento que añadir como
  subencargado del tratamiento, evaluar bajo un mecanismo de transferencia o del que depender para la
  retención de *tu* evidencia regulatoria.
- **AGPL-3.0, source-available.** Tu equipo de seguridad puede leer cada línea que
  produce la evidencia. La prueba de integridad es verificable **fuera de la caja** con
  `audit verify`, de modo que no estás confiando en nuestra afirmación de que el ledger está intacto —
  lo estás comprobando. La dependencia de un único proveedor se mitiga estructuralmente, no se
  promete (consulta la nota de viabilidad del proveedor del paquete de confianza).
- **La residencia se atesta, no se asume.** `GET /v1/m/compliance/residency`
  produce una atestación de residencia; los despliegues multirregión tienen alcance de región y son
  deny-closed por diseño.

Un **control plane SaaS estadounidense** invierte todo esto: la evidencia conductual de tu estate de IA
— el mismísimo registro que un regulador de la UE puede pedir — se genera, procesa
y retiene en la nube de un tercero, bajo un modelo de responsabilidad compartida que no
controlas, frecuentemente fuera de la UE. Ese es precisamente el arreglo en el que a muchos
compradores europeos regulados se les dice que no pueden entrar. **Self-hosted no es una preferencia
de despliegue aquí; es la postura de cumplimiento.**

:::caution[Diseñamos para la auditoría; no certificamos]
Nada de lo anterior te hace, ni a nosotros, "conforme con el Reglamento de IA de la UE" — el cumplimiento es una conclusión
legal sobre un sistema específico, extraída por su proveedor con asesoría. Lo que el
control plane te da es **evidencia que puedes respaldar**, generada a partir de datos reales
de runtime, mantenida donde tu regulador la espera. El
[catálogo de frameworks](/es/reference/modules/xiii-compliance/) lleva el
descargo de "no es una certificación" en cada entrada, por diseño.
:::

## Relacionado

- [Evidencia legible por máquina](/es/reference/modules/xiii-compliance/) — la superficie de la
  API de evidencia, validación continua estilo KSI.
- [Modelo de seguridad](/es/explanation/security/security-model/) — por qué el ledger tiene
  evidencia de manipulación y cómo funciona la verificación fuera de la caja.
- [Contexto de mercado y fuentes](/es/explanation/positioning/market-context-and-sources/)
  — las estadísticas verificadas detrás del argumento de la deuda de gobernanza.
